// Package capture records actual gateway payloads as compressed, append-only
// events. It writes incrementally so long streams never accumulate in RAM.
package capture

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qunqin24/polyglot/internal/idgen"
)

type Stage struct {
	ID       string      `json:"id"`
	Kind     string      `json:"kind"`
	Attempt  int         `json:"attempt,omitempty"`
	Protocol string      `json:"protocol,omitempty"`
	Provider string      `json:"provider,omitempty"`
	Method   string      `json:"method,omitempty"`
	URL      string      `json:"url,omitempty"`
	Headers  http.Header `json:"headers,omitempty"`
	Status   int         `json:"status,omitempty"`
	Bytes    int64       `json:"bytes"`
	Complete bool        `json:"complete"`
}

type Event struct {
	Type  string    `json:"type"`
	At    time.Time `json:"at"`
	Stage *Stage    `json:"stage,omitempty"`
	ID    string    `json:"id,omitempty"`
	Data  []byte    `json:"data,omitempty"`
}

type Manifest struct {
	Version  int      `json:"version"`
	Complete bool     `json:"complete"`
	Stages   []*Stage `json:"stages"`
}

type Recorder struct {
	ID   string
	file *os.File
	zip  *gzip.Writer
	enc  *json.Encoder
	err  error
}

func New(dir string) (*Recorder, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	id := idgen.New()
	f, err := os.OpenFile(filepath.Join(dir, id+".jsonl.gz"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	z := gzip.NewWriter(f)
	return &Recorder{ID: id, file: f, zip: z, enc: json.NewEncoder(z)}, nil
}
func (r *Recorder) event(e Event) {
	if r == nil || r.err != nil {
		return
	}
	e.At = time.Now().UTC()
	r.err = r.enc.Encode(e)
}
func (r *Recorder) Start(s Stage) {
	s.Headers = Headers(s.Headers)
	s.URL = URL(s.URL)
	r.event(Event{Type: "start", Stage: &s})
}
func (r *Recorder) Data(id string, b []byte) {
	if r == nil {
		return
	}
	for len(b) > 0 {
		n := min(len(b), 32<<10)
		r.event(Event{Type: "data", ID: id, Data: b[:n]})
		b = b[n:]
	}
}
func (r *Recorder) End(id string) { r.event(Event{Type: "end", ID: id}) }
func (r *Recorder) Body(s Stage, b []byte) {
	if r == nil {
		return
	}
	r.Start(s)
	r.Data(s.ID, b)
	r.End(s.ID)
}
func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.event(Event{Type: "complete"})
	return errors.Join(r.err, r.zip.Close(), r.file.Close())
}

// Credentials in transport metadata are never part of the recorded payload.
// An allowlist also excludes custom provider headers carrying secrets.
func Headers(h http.Header) http.Header {
	out := http.Header{}
	for _, k := range []string{"Content-Type", "Content-Encoding", "Content-Length", "Accept", "User-Agent", "Anthropic-Version", "Anthropic-Beta", "Openai-Version", "X-Request-Id", "Request-Id", "X-Polyglot-Request-Id", "Retry-After"} {
		if v := h.Values(k); len(v) > 0 {
			out[k] = append([]string(nil), v...)
		}
	}
	return out
}
func URL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	q := u.Query()
	for k := range q {
		switch strings.ToLower(k) {
		case "key", "api_key", "apikey", "token", "access_token", "signature":
			q.Set(k, "[REDACTED]")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

type reader struct {
	io.ReadCloser
	r  *Recorder
	id string
}

func (r *Recorder) Reader(id string, src io.ReadCloser) io.ReadCloser {
	if r == nil {
		return src
	}
	return &reader{ReadCloser: src, r: r, id: id}
}
func (r *reader) Read(b []byte) (int, error) {
	n, e := r.ReadCloser.Read(b)
	r.r.Data(r.id, b[:n])
	if e == io.EOF {
		r.r.End(r.id)
	}
	return n, e
}

type responseWriter struct {
	http.ResponseWriter
	r       *Recorder
	started bool
	err     error
}

func (r *Recorder) Writer(w http.ResponseWriter) http.ResponseWriter {
	if r == nil {
		return w
	}
	cw := &responseWriter{ResponseWriter: w, r: r}
	// Preserve Flusher only if the original supports it.
	if f, ok := w.(http.Flusher); ok {
		return &flushingWriter{responseWriter: cw, flusher: f}
	}
	return cw
}
func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseWriter) WriteHeader(status int) {
	if !w.started {
		w.started = true
		w.r.Start(Stage{ID: "client.response", Kind: "client_response", Status: status, Headers: w.Header()})
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *responseWriter) Write(b []byte) (int, error) {
	if !w.started {
		w.WriteHeader(200)
	}
	n, e := w.ResponseWriter.Write(b)
	w.r.Data("client.response", b[:n])
	w.err = e
	return n, e
}

type flushingWriter struct {
	*responseWriter
	flusher http.Flusher
}

func (w *flushingWriter) Flush() {
	if !w.started {
		w.WriteHeader(200)
	}
	w.flusher.Flush()
}
func FinishResponse(w http.ResponseWriter) {
	var cw *responseWriter
	switch v := w.(type) {
	case *responseWriter:
		cw = v
	case *flushingWriter:
		cw = v.responseWriter
	}
	if cw != nil && cw.started && cw.err == nil {
		cw.r.End("client.response")
	}
}

// Walk never loads the full capture. Data chunks are bounded by the writer.
func Walk(src io.Reader, visit func(Event) error) error {
	z, err := gzip.NewReader(src)
	if err != nil {
		return err
	}
	defer z.Close()
	d := json.NewDecoder(z)
	for {
		var e Event
		if err := d.Decode(&e); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if err := visit(e); err != nil {
			return err
		}
	}
}
func ReadManifest(src io.Reader) (*Manifest, error) {
	m := &Manifest{Version: 1, Stages: []*Stage{}}
	byID := map[string]*Stage{}
	err := Walk(src, func(e Event) error {
		switch e.Type {
		case "start":
			if e.Stage != nil {
				s := *e.Stage
				byID[s.ID] = &s
				m.Stages = append(m.Stages, &s)
			}
		case "data":
			if s := byID[e.ID]; s != nil {
				s.Bytes += int64(len(e.Data))
			}
		case "end":
			if s := byID[e.ID]; s != nil {
				s.Complete = true
			}
		case "complete":
			m.Complete = true
		}
		return nil
	})
	return m, err
}
