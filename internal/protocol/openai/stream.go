package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/idgen"
	"github.com/qunqin24/polyglot/internal/protocol"
	"github.com/qunqin24/polyglot/internal/stream"
)

func randomID() string { return idgen.New() }

// --- upstream OpenAI SSE -> canonical events ------------------------------

// DecodeStream converts an OpenAI chunk stream into canonical events. Content
// blocks are numbered in the order they first appear, so a reasoning block
// that precedes the answer keeps its position when re-encoded elsewhere.
func (Codec) DecodeStream(ctx context.Context, r io.Reader, emit func(*canonical.Event) error) error {
	sr := stream.NewReader(r)

	var (
		started    bool
		nextIndex  int
		textIndex  = -1
		reasonIdx  = -1
		toolIndex  = map[int]int{} // upstream tool_calls index -> canonical block
		toolOpen   []int           // canonical indices of open tool blocks, in order
		finish     canonical.FinishReason
		sawFinish  bool
		lastUsage  *canonical.Usage
		responseID string
		model      string
	)

	alloc := func() int {
		i := nextIndex
		nextIndex++
		return i
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		frame, err := sr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read upstream stream: %w", err)
		}
		data := frame.Data
		if len(data) == 0 {
			continue
		}
		if string(data) == "[DONE]" {
			break
		}

		// Some providers send an error object in the middle of a stream.
		if isErrorPayload(data) {
			var we wireError
			if json.Unmarshal(data, &we) == nil && we.Error.Message != "" {
				return emit(&canonical.Event{Type: canonical.EventError, Error: &canonical.Error{
					Type:    canonical.ErrUpstream,
					Message: we.Error.Message,
					Code:    fmt.Sprint(we.Error.Code),
				}})
			}
		}

		var chunk chatChunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			// A malformed chunk is not worth killing the stream over.
			continue
		}
		if chunk.ID != "" {
			responseID = chunk.ID
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if !started {
			started = true
			if err := emit(&canonical.Event{Type: canonical.EventMessageStart, ID: responseID, Model: model}); err != nil {
				return err
			}
		}

		if chunk.Usage != nil {
			u := usageToCanonical(chunk.Usage)
			lastUsage = &u
		}

		for _, ch := range chunk.Choices {
			if ch.Index != 0 {
				continue // Polyglot serves a single choice.
			}
			d := ch.Delta

			if rc := deltaReasoning(d); rc != "" {
				if reasonIdx < 0 {
					reasonIdx = alloc()
				}
				if err := emit(&canonical.Event{Type: canonical.EventReasoningDelta, Index: reasonIdx, Text: rc}); err != nil {
					return err
				}
			}
			if d.Content != nil && *d.Content != "" {
				if textIndex < 0 {
					textIndex = alloc()
				}
				if err := emit(&canonical.Event{Type: canonical.EventTextDelta, Index: textIndex, Text: *d.Content}); err != nil {
					return err
				}
			}
			if d.Refusal != nil && *d.Refusal != "" {
				if textIndex < 0 {
					textIndex = alloc()
				}
				if err := emit(&canonical.Event{Type: canonical.EventTextDelta, Index: textIndex, Text: *d.Refusal}); err != nil {
					return err
				}
			}

			for i, tc := range d.ToolCalls {
				upIdx := i
				if tc.Index != nil {
					upIdx = *tc.Index
				}
				blockIdx, known := toolIndex[upIdx]
				if !known {
					blockIdx = alloc()
					toolIndex[upIdx] = blockIdx
					toolOpen = append(toolOpen, blockIdx)
					ev := &canonical.Event{
						Type:              canonical.EventToolCallStart,
						Index:             blockIdx,
						ToolCallID:        orDefault(tc.ID, "call_"+randomID()),
						ToolName:          tc.Function.Name,
						ToolCallSignature: tc.ExtraContent.Signature(),
					}
					if err := emit(ev); err != nil {
						return err
					}
				}
				// Arguments arrive as raw fragments that are only valid JSON
				// once concatenated; never parse a fragment here.
				if tc.Function.Arguments != "" {
					if err := emit(&canonical.Event{
						Type:           canonical.EventToolCallDelta,
						Index:          blockIdx,
						ArgumentsDelta: tc.Function.Arguments,
					}); err != nil {
						return err
					}
				}
			}

			if ch.FinishReason != nil && *ch.FinishReason != "" {
				finish = finishToCanonical(*ch.FinishReason)
				sawFinish = true
			}
		}
	}

	if !started {
		return canonical.Errorf(canonical.ErrUpstream, "upstream closed the stream without sending any data")
	}
	for _, idx := range toolOpen {
		if err := emit(&canonical.Event{Type: canonical.EventToolCallEnd, Index: idx}); err != nil {
			return err
		}
	}
	if lastUsage != nil {
		if err := emit(&canonical.Event{Type: canonical.EventUsage, Usage: lastUsage}); err != nil {
			return err
		}
	}
	if !sawFinish {
		finish = canonical.FinishStop
	}
	end := &canonical.Event{Type: canonical.EventMessageEnd, FinishReason: finish, Usage: lastUsage}
	return emit(end)
}

func deltaReasoning(d wireDelta) string {
	if d.ReasoningContent != nil {
		return *d.ReasoningContent
	}
	if d.Reasoning != nil {
		return *d.Reasoning
	}
	return ""
}

func isErrorPayload(b []byte) bool {
	var probe struct {
		Error json.RawMessage `json:"error"`
	}
	return json.Unmarshal(b, &probe) == nil && len(probe.Error) > 0
}

// --- canonical events -> OpenAI SSE ---------------------------------------

type streamEncoder struct {
	w            *stream.Writer
	req          *canonical.Request
	id           string
	model        string
	created      int64
	roleSent     bool
	toolOrdinals map[int]int // canonical block index -> OpenAI tool_calls index
	nextOrdinal  int
	usage        *canonical.Usage
	finished     bool
	closed       bool
}

func (Codec) NewStreamEncoder(w io.Writer, req *canonical.Request) protocol.StreamEncoder {
	model := ""
	if req != nil {
		model = req.Model
	}
	return &streamEncoder{
		w:            stream.NewWriter(w),
		req:          req,
		id:           "chatcmpl-" + randomID(),
		model:        model,
		created:      time.Now().Unix(),
		toolOrdinals: map[int]int{},
	}
}

func (e *streamEncoder) chunk(delta wireDelta, finish *string) *chatChunk {
	return &chatChunk{
		ID:      e.id,
		Object:  "chat.completion.chunk",
		Created: e.created,
		Model:   e.model,
		Choices: []wireChunkChoice{{Index: 0, Delta: delta, FinishReason: finish}},
	}
}

func (e *streamEncoder) send(c *chatChunk) error {
	b, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode chunk: %w", err)
	}
	return e.w.Event("", b)
}

func (e *streamEncoder) ensureRole() error {
	if e.roleSent {
		return nil
	}
	e.roleSent = true
	empty := ""
	return e.send(e.chunk(wireDelta{Role: "assistant", Content: &empty}, nil))
}

func (e *streamEncoder) Write(ev *canonical.Event) error {
	switch ev.Type {
	case canonical.EventMessageStart:
		if ev.Model != "" {
			e.model = ev.Model
		}
		return e.ensureRole()

	case canonical.EventTextDelta:
		if err := e.ensureRole(); err != nil {
			return err
		}
		text := ev.Text
		return e.send(e.chunk(wireDelta{Content: &text}, nil))

	case canonical.EventReasoningDelta:
		if err := e.ensureRole(); err != nil {
			return err
		}
		text := ev.Text
		return e.send(e.chunk(wireDelta{ReasoningContent: &text}, nil))

	case canonical.EventToolCallStart:
		if err := e.ensureRole(); err != nil {
			return err
		}
		ord := e.nextOrdinal
		e.nextOrdinal++
		e.toolOrdinals[ev.Index] = ord
		tc := wireToolCall{ID: orDefault(ev.ToolCallID, "call_"+randomID()), Type: "function", Index: &ord}
		tc.Function.Name = ev.ToolName
		tc.ExtraContent = protocol.SignatureExtra(ev.ToolCallSignature)
		tc.Function.Arguments = ""
		return e.send(e.chunk(wireDelta{ToolCalls: []wireToolCall{tc}}, nil))

	case canonical.EventToolCallDelta:
		if ev.ArgumentsDelta == "" {
			return nil
		}
		ord, ok := e.toolOrdinals[ev.Index]
		if !ok {
			ord = e.nextOrdinal
			e.nextOrdinal++
			e.toolOrdinals[ev.Index] = ord
		}
		tc := wireToolCall{Index: &ord}
		tc.Function.Arguments = ev.ArgumentsDelta
		return e.send(e.chunk(wireDelta{ToolCalls: []wireToolCall{tc}}, nil))

	case canonical.EventToolCallEnd:
		return nil

	case canonical.EventUsage:
		e.usage = ev.Usage
		return nil

	case canonical.EventMessageEnd:
		if err := e.ensureRole(); err != nil {
			return err
		}
		if ev.Usage != nil {
			e.usage = ev.Usage
		}
		reason := finishFromCanonical(ev.FinishReason)
		e.finished = true
		return e.send(e.chunk(wireDelta{}, &reason))

	case canonical.EventError:
		if ev.Error == nil {
			return nil
		}
		return e.w.Event("", Codec{}.EncodeError(ev.Error))
	}
	return nil
}

func (e *streamEncoder) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	if !e.finished {
		reason := "stop"
		if err := e.send(e.chunk(wireDelta{}, &reason)); err != nil {
			return err
		}
	}
	// OpenAI reports usage in a final chunk with an empty choices array, and
	// only when the client opted in.
	if e.usage != nil && e.req != nil && e.req.IncludeUsage {
		final := &chatChunk{
			ID:      e.id,
			Object:  "chat.completion.chunk",
			Created: e.created,
			Model:   e.model,
			Choices: []wireChunkChoice{},
			Usage:   usageFromCanonical(*e.usage),
		}
		if err := e.send(final); err != nil {
			return err
		}
	}
	return e.w.Event("", []byte("[DONE]"))
}
