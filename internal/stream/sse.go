// Package stream holds the transport-level plumbing shared by every protocol:
// SSE framing in both directions. Protocol semantics live in the codecs.
package stream

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
)

// maxFieldSize caps a single SSE field so a hostile or broken upstream cannot
// make us buffer without bound.
const maxFieldSize = 8 << 20 // 8 MiB

// Frame is one dispatched SSE event.
type Frame struct {
	Event string
	Data  []byte
}

// Reader parses an SSE byte stream into frames. It tolerates both "\n" and
// "\r\n" line endings and comment lines.
type Reader struct {
	br    *bufio.Reader
	event string
	data  bytes.Buffer
}

func NewReader(r io.Reader) *Reader {
	return &Reader{br: bufio.NewReaderSize(r, 32<<10)}
}

// Next returns the next frame, or io.EOF when the stream ends. A frame with
// no data lines is skipped, per the SSE spec.
func (s *Reader) Next() (*Frame, error) {
	for {
		line, err := s.readLine()
		if err != nil {
			if err == io.EOF {
				// A stream that ends without a blank line still has a
				// pending frame worth delivering.
				if s.data.Len() > 0 || s.event != "" {
					f := s.frame()
					return f, nil
				}
			}
			return nil, err
		}

		if len(line) == 0 { // dispatch
			if s.data.Len() == 0 && s.event == "" {
				continue
			}
			return s.frame(), nil
		}
		if line[0] == ':' { // comment / keep-alive
			continue
		}

		field, value := splitField(line)
		switch field {
		case "event":
			s.event = string(value)
		case "data":
			if s.data.Len() > 0 {
				s.data.WriteByte('\n')
			}
			s.data.Write(value)
		case "id", "retry":
			// Not meaningful for LLM streams.
		}
	}
}

func (s *Reader) frame() *Frame {
	f := &Frame{Event: s.event, Data: append([]byte(nil), s.data.Bytes()...)}
	s.event = ""
	s.data.Reset()
	return f
}

func (s *Reader) readLine() ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := s.br.ReadLine()
		if err != nil {
			if len(buf) > 0 && err == io.EOF {
				return buf, nil
			}
			return nil, err
		}
		if buf == nil && !isPrefix {
			return chunk, nil
		}
		if len(buf)+len(chunk) > maxFieldSize {
			return nil, fmt.Errorf("sse: line exceeds %d bytes", maxFieldSize)
		}
		buf = append(buf, chunk...)
		if !isPrefix {
			return buf, nil
		}
	}
}

func splitField(line []byte) (field string, value []byte) {
	i := bytes.IndexByte(line, ':')
	if i < 0 {
		return string(line), nil
	}
	value = line[i+1:]
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	return string(line[:i]), value
}

// Writer emits SSE frames to a client, flushing after each one so deltas are
// not held in a buffer.
type Writer struct {
	w   io.Writer
	f   http.Flusher
	err error
}

func NewWriter(w io.Writer) *Writer {
	sw := &Writer{w: w}
	if f, ok := w.(http.Flusher); ok {
		sw.f = f
	}
	return sw
}

// Event writes a named event. An empty name emits a data-only frame, which is
// what OpenAI-style streams use.
func (w *Writer) Event(name string, data []byte) error {
	if w.err != nil {
		return w.err
	}
	var buf bytes.Buffer
	if name != "" {
		buf.WriteString("event: ")
		buf.WriteString(name)
		buf.WriteByte('\n')
	}
	// Data may not contain raw newlines in a single field; split it.
	for _, part := range bytes.Split(data, []byte("\n")) {
		buf.WriteString("data: ")
		buf.Write(part)
		buf.WriteByte('\n')
	}
	buf.WriteByte('\n')
	if _, err := w.w.Write(buf.Bytes()); err != nil {
		w.err = err
		return err
	}
	w.Flush()
	return nil
}

func (w *Writer) Flush() {
	if w.f != nil {
		w.f.Flush()
	}
}

func (w *Writer) Err() error { return w.err }

// SetSSEHeaders applies the headers required for a streaming response. It must
// be called before the first write.
func SetSSEHeaders(h http.Header) {
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	// Defeat buffering in an intermediate nginx, a very common VPS setup.
	h.Set("X-Accel-Buffering", "no")
}
