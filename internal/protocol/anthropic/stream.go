package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/idgen"
	"github.com/qunqin24/polyglot/internal/protocol"
	"github.com/qunqin24/polyglot/internal/stream"
)

// --- upstream Anthropic SSE -> canonical events ---------------------------

// DecodeStream reads Anthropic's typed event stream. Block indices are already
// explicit in the protocol, so they map straight onto canonical indices.
func (Codec) DecodeStream(ctx context.Context, r io.Reader, emit func(*canonical.Event) error) error {
	sr := stream.NewReader(r)

	var (
		started     bool
		usage       canonical.Usage
		finish      canonical.FinishReason
		toolBlock   = map[int]bool{}
		nativeBlock = map[int]bool{}
	)
	emitNative := func(name string, index int, raw []byte) error {
		return emit(&canonical.Event{Type: canonical.EventNative, Index: index,
			Native: &canonical.NativeEvent{Protocol: string(protocol.Anthropic), Name: name,
				Raw: append(json.RawMessage(nil), raw...)}})
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
		if len(frame.Data) == 0 {
			continue
		}

		var ev streamEvent
		if err := json.Unmarshal(frame.Data, &ev); err != nil {
			continue
		}
		// The event name and the payload's type agree; trust the payload.
		if ev.Type == "" {
			ev.Type = frame.Event
		}

		switch ev.Type {
		case "message_start":
			started = true
			out := &canonical.Event{Type: canonical.EventMessageStart}
			if ev.Message != nil {
				out.ID = ev.Message.ID
				out.Model = ev.Message.Model
				if ev.Message.Usage != nil {
					usage = usageToCanonical(ev.Message.Usage)
				}
			}
			if err := emit(out); err != nil {
				return err
			}

		case "content_block_start":
			if ev.ContentBlock == nil {
				continue
			}
			if ev.ContentBlock.Type == "tool_use" {
				toolBlock[ev.Index] = true
				if err := emit(&canonical.Event{
					Type:              canonical.EventToolCallStart,
					Index:             ev.Index,
					ToolCallID:        orDefault(ev.ContentBlock.ID, "toolu_"+idgen.New()),
					ToolName:          ev.ContentBlock.Name,
					ToolCallSignature: ev.ContentBlock.ExtraContent.Signature(),
				}); err != nil {
					return err
				}
			}
			if ev.ContentBlock.Type == "redacted_thinking" && ev.ContentBlock.Data != "" {
				if err := emit(&canonical.Event{
					Type:      canonical.EventReasoningDelta,
					Index:     ev.Index,
					Reasoning: &canonical.ReasoningMeta{Redacted: ev.ContentBlock.Data},
				}); err != nil {
					return err
				}
			} else if len(ev.ContentBlock.Raw) > 0 {
				nativeBlock[ev.Index] = true
				if err := emitNative(ev.Type, ev.Index, frame.Data); err != nil {
					return err
				}
			}

		case "content_block_delta":
			if nativeBlock[ev.Index] {
				if err := emitNative(ev.Type, ev.Index, frame.Data); err != nil {
					return err
				}
				continue
			}
			if ev.Delta == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				if err := emit(&canonical.Event{
					Type: canonical.EventTextDelta, Index: ev.Index, Text: ev.Delta.Text,
				}); err != nil {
					return err
				}
			case "thinking_delta":
				if err := emit(&canonical.Event{
					Type: canonical.EventReasoningDelta, Index: ev.Index, Text: ev.Delta.Thinking,
				}); err != nil {
					return err
				}
			case "signature_delta":
				if err := emit(&canonical.Event{
					Type:      canonical.EventReasoningDelta,
					Index:     ev.Index,
					Reasoning: &canonical.ReasoningMeta{Signature: ev.Delta.Signature},
				}); err != nil {
					return err
				}
			case "input_json_delta":
				// A partial_json fragment is not valid JSON on its own.
				if err := emit(&canonical.Event{
					Type:           canonical.EventToolCallDelta,
					Index:          ev.Index,
					ArgumentsDelta: ev.Delta.PartialJSON,
				}); err != nil {
					return err
				}
			}

		case "content_block_stop":
			if toolBlock[ev.Index] {
				if err := emit(&canonical.Event{Type: canonical.EventToolCallEnd, Index: ev.Index}); err != nil {
					return err
				}
			} else if nativeBlock[ev.Index] {
				if err := emitNative(ev.Type, ev.Index, frame.Data); err != nil {
					return err
				}
			}

		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				finish = stopToCanonical(ev.Delta.StopReason)
			}
			if ev.Usage != nil {
				// message_delta reports cumulative output tokens; input
				// tokens only arrive in message_start.
				u := usageToCanonical(ev.Usage)
				if u.OutputTokens > 0 {
					usage.OutputTokens = u.OutputTokens
				}
				// The prompt total and its cache breakdown move together:
				// taking the total from this frame and the split from
				// message_start would describe a prompt that never existed.
				if u.InputTokens > 0 {
					usage.InputTokens = u.InputTokens
					usage.CachedInputTokens = u.CachedInputTokens
					usage.CacheWriteTokens = u.CacheWriteTokens
				}
			}

		case "message_stop":
			// Handled below, after the loop, so a stream that ends without it
			// still produces a terminal event.

		case "error":
			if ev.Error != nil {
				return emit(&canonical.Event{Type: canonical.EventError, Error: &canonical.Error{
					Type:    upstreamErrorType(ev.Error.Type),
					Message: ev.Error.Message,
					Code:    ev.Error.Type,
				}})
			}

		case "ping":
			// Keep-alive.
		}
	}

	if !started {
		return canonical.Errorf(canonical.ErrUpstream, "upstream closed the stream without sending any data")
	}
	if err := emit(&canonical.Event{Type: canonical.EventUsage, Usage: &usage}); err != nil {
		return err
	}
	if finish == canonical.FinishUnknown {
		finish = canonical.FinishStop
	}
	return emit(&canonical.Event{Type: canonical.EventMessageEnd, FinishReason: finish, Usage: &usage})
}

func upstreamErrorType(s string) canonical.ErrorType {
	switch s {
	case "authentication_error":
		return canonical.ErrAuthentication
	case "permission_error":
		return canonical.ErrPermission
	case "not_found_error":
		return canonical.ErrNotFound
	case "rate_limit_error":
		return canonical.ErrRateLimit
	case "overloaded_error":
		return canonical.ErrOverloaded
	case "invalid_request_error":
		return canonical.ErrInvalidRequest
	default:
		return canonical.ErrUpstream
	}
}

// --- canonical events -> Anthropic SSE ------------------------------------

type streamEncoder struct {
	w     *stream.Writer
	req   *canonical.Request
	id    string
	model string

	blocks       map[int]int // canonical index -> anthropic index
	blockKind    map[int]string
	nextBlock    int
	openBlock    int // anthropic index of the currently open block, -1 if none
	started      bool
	finished     bool
	closed       bool
	usage        canonical.Usage
	finish       canonical.FinishReason
	nativeBlocks map[int]int
}

func (Codec) NewStreamEncoder(w io.Writer, req *canonical.Request) protocol.StreamEncoder {
	model := ""
	if req != nil {
		model = req.Model
	}
	return &streamEncoder{
		w:            stream.NewWriter(w),
		req:          req,
		id:           "msg_" + idgen.New(),
		model:        model,
		blocks:       map[int]int{},
		blockKind:    map[int]string{},
		openBlock:    -1,
		finish:       canonical.FinishStop,
		nativeBlocks: map[int]int{},
	}
}

func (e *streamEncoder) send(name string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	return e.w.Event(name, b)
}

func (e *streamEncoder) sendNative(ev *canonical.Event) error {
	if ev.Native == nil {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal(ev.Native.Raw, &payload); err != nil {
		return nil
	}
	if ev.Native.Name == "content_block_start" {
		if err := e.closeOpenBlock(); err != nil {
			return err
		}
		idx := e.nextBlock
		e.nextBlock++
		e.nativeBlocks[ev.Index] = idx
		payload["index"] = idx
	} else if idx, ok := e.nativeBlocks[ev.Index]; ok {
		payload["index"] = idx
	}
	return e.send(ev.Native.Name, payload)
}

func (e *streamEncoder) ensureStart() error {
	if e.started {
		return nil
	}
	e.started = true
	// Anthropic reports input tokens up front. Polyglot rarely knows them
	// before the upstream finishes, so this starts at zero and the real
	// figure arrives in message_delta.
	return e.send("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            e.id,
			"type":          "message",
			"role":          "assistant",
			"model":         e.model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         wireStreamUsage(e.usage, 0),
		},
	})
}

// openFor returns the Anthropic block index for a canonical index, opening a
// content block of the right kind the first time it is seen and closing the
// previous one. Anthropic allows only one block open at a time.
func (e *streamEncoder) openFor(canonicalIdx int, kind string, tc *canonical.Event) (int, error) {
	if idx, ok := e.blocks[canonicalIdx]; ok {
		return idx, nil
	}
	if err := e.closeOpenBlock(); err != nil {
		return 0, err
	}
	idx := e.nextBlock
	e.nextBlock++
	e.blocks[canonicalIdx] = idx
	e.blockKind[canonicalIdx] = kind
	e.openBlock = idx

	var cb map[string]any
	switch kind {
	case "text":
		cb = map[string]any{"type": "text", "text": ""}
	case "thinking":
		cb = map[string]any{"type": "thinking", "thinking": "", "signature": ""}
	case "tool_use":
		cb = map[string]any{
			"type":  "tool_use",
			"id":    orDefault(tc.ToolCallID, "toolu_"+idgen.New()),
			"name":  tc.ToolName,
			"input": map[string]any{},
		}
		// The client has to hand this back next turn for a Gemini upstream to
		// accept the call again.
		if x := protocol.SignatureExtra(tc.ToolCallSignature); x != nil {
			cb["extra_content"] = x
		}
	}
	return idx, e.send("content_block_start", map[string]any{
		"type": "content_block_start", "index": idx, "content_block": cb,
	})
}

func (e *streamEncoder) closeOpenBlock() error {
	if e.openBlock < 0 {
		return nil
	}
	idx := e.openBlock
	e.openBlock = -1
	return e.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
}

func (e *streamEncoder) Write(ev *canonical.Event) error {
	switch ev.Type {
	case canonical.EventMessageStart:
		if ev.Model != "" {
			e.model = ev.Model
		}
		return e.ensureStart()

	case canonical.EventTextDelta:
		if err := e.ensureStart(); err != nil {
			return err
		}
		idx, err := e.openFor(ev.Index, "text", ev)
		if err != nil {
			return err
		}
		return e.send("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": idx,
			"delta": map[string]any{"type": "text_delta", "text": ev.Text},
		})

	case canonical.EventReasoningDelta:
		if err := e.ensureStart(); err != nil {
			return err
		}
		idx, err := e.openFor(ev.Index, "thinking", ev)
		if err != nil {
			return err
		}
		if ev.Reasoning != nil && ev.Reasoning.Signature != "" {
			return e.send("content_block_delta", map[string]any{
				"type": "content_block_delta", "index": idx,
				"delta": map[string]any{"type": "signature_delta", "signature": ev.Reasoning.Signature},
			})
		}
		if ev.Text == "" {
			return nil
		}
		return e.send("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": idx,
			"delta": map[string]any{"type": "thinking_delta", "thinking": ev.Text},
		})

	case canonical.EventToolCallStart:
		if err := e.ensureStart(); err != nil {
			return err
		}
		_, err := e.openFor(ev.Index, "tool_use", ev)
		return err

	case canonical.EventToolCallDelta:
		if ev.ArgumentsDelta == "" {
			return nil
		}
		if err := e.ensureStart(); err != nil {
			return err
		}
		idx, err := e.openFor(ev.Index, "tool_use", ev)
		if err != nil {
			return err
		}
		return e.send("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": idx,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": ev.ArgumentsDelta},
		})

	case canonical.EventToolCallEnd:
		if idx, ok := e.blocks[ev.Index]; ok && idx == e.openBlock {
			return e.closeOpenBlock()
		}
		return nil

	case canonical.EventNative:
		if ev.Native == nil || ev.Native.Protocol != string(protocol.Anthropic) {
			return nil
		}
		if err := e.ensureStart(); err != nil {
			return err
		}
		return e.sendNative(ev)

	case canonical.EventUsage:
		if ev.Usage != nil {
			e.usage = *ev.Usage
		}
		return nil

	case canonical.EventMessageEnd:
		if err := e.ensureStart(); err != nil {
			return err
		}
		if ev.Usage != nil {
			e.usage = *ev.Usage
		}
		if ev.FinishReason != "" {
			e.finish = ev.FinishReason
		}
		e.finished = true
		return e.emitEnd()

	case canonical.EventError:
		if ev.Error == nil {
			return nil
		}
		return e.w.Event("error", Codec{}.EncodeError(ev.Error))
	}
	return nil
}

func (e *streamEncoder) emitEnd() error {
	if err := e.closeOpenBlock(); err != nil {
		return err
	}
	if err := e.send("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopFromCanonical(e.finish),
			"stop_sequence": nil,
		},
		"usage": wireStreamUsage(e.usage, e.usage.OutputTokens),
	}); err != nil {
		return err
	}
	return e.send("message_stop", map[string]any{"type": "message_stop"})
}

func (e *streamEncoder) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	if e.finished {
		return nil
	}
	if err := e.ensureStart(); err != nil {
		return err
	}
	return e.emitEnd()
}

// wireStreamUsage writes the usage object of a streamed frame, splitting the
// canonical prompt total back into Anthropic's three counters the same way
// usageFromCanonical does for a buffered reply. The cache keys are omitted
// when zero, because Anthropic itself omits them and a client that sums the
// fields present must not be handed a cache read that never happened.
//
// outputTokens is passed separately: message_start reports zero regardless of
// what the canonical usage already knows.
func wireStreamUsage(u canonical.Usage, outputTokens int) map[string]int {
	out := map[string]int{
		"input_tokens":  u.UncachedInputTokens(),
		"output_tokens": outputTokens,
	}
	if u.CachedInputTokens > 0 {
		out["cache_read_input_tokens"] = u.CachedInputTokens
	}
	if u.CacheWriteTokens > 0 {
		out["cache_creation_input_tokens"] = u.CacheWriteTokens
	}
	return out
}
