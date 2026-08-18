package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/protocol"
	"github.com/qunqin24/polyglot/internal/stream"
)

// --- upstream Gemini stream -> canonical events ---------------------------

// DecodeStream reads Gemini's streamGenerateContent output. Polyglot always
// requests alt=sse, so each frame is one complete GenerateContentResponse
// carrying the newest parts.
//
// Unlike the other two protocols, Gemini emits a function call whole rather
// than in fragments, so a tool call becomes start + one delta + end.
func (Codec) DecodeStream(ctx context.Context, r io.Reader, emit func(*canonical.Event) error) error {
	sr := stream.NewReader(r)

	var (
		started   bool
		nextIndex int
		textIndex = -1
		thoughtIx = -1
		usage     canonical.Usage
		finish    canonical.FinishReason
		sawFinish bool
		hasTool   bool
		callSeq   = map[string]int{}
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
		if len(frame.Data) == 0 {
			continue
		}

		// A stream can carry an error object instead of a chunk.
		if we := decodeWireError(frame.Data); we != nil {
			return emit(&canonical.Event{Type: canonical.EventError, Error: we})
		}

		var chunk generateResponse
		if err := json.Unmarshal(frame.Data, &chunk); err != nil {
			continue
		}
		if !started {
			started = true
			if err := emit(&canonical.Event{
				Type:  canonical.EventMessageStart,
				ID:    chunk.ResponseID,
				Model: chunk.ModelVersion,
			}); err != nil {
				return err
			}
		}
		if chunk.UsageMetadata != nil {
			usage = usageToCanonical(chunk.UsageMetadata)
		}

		for _, cand := range chunk.Candidates {
			if cand.Index != 0 {
				continue
			}
			if cand.Content != nil {
				for _, p := range cand.Content.Parts {
					switch {
					case p.FunctionCall != nil:
						hasTool = true
						idx := alloc()
						id := p.FunctionCall.ID
						if id == "" {
							id = synthID(p.FunctionCall.Name, callSeq)
						}
						if err := emit(&canonical.Event{
							Type:              canonical.EventToolCallStart,
							Index:             idx,
							ToolCallID:        id,
							ToolName:          p.FunctionCall.Name,
							ToolCallSignature: p.ThoughtSignature,
						}); err != nil {
							return err
						}
						args := p.FunctionCall.Args
						if len(args) == 0 {
							args = json.RawMessage("{}")
						}
						if err := emit(&canonical.Event{
							Type:           canonical.EventToolCallDelta,
							Index:          idx,
							ArgumentsDelta: string(args),
						}); err != nil {
							return err
						}
						if err := emit(&canonical.Event{Type: canonical.EventToolCallEnd, Index: idx}); err != nil {
							return err
						}

					case p.Thought:
						if thoughtIx < 0 {
							thoughtIx = alloc()
						}
						ev := &canonical.Event{Type: canonical.EventReasoningDelta, Index: thoughtIx, Text: p.Text}
						if p.ThoughtSignature != "" {
							ev.Reasoning = &canonical.ReasoningMeta{Signature: p.ThoughtSignature}
						}
						if err := emit(ev); err != nil {
							return err
						}

					case p.Text != "" || p.ThoughtSignature != "":
						if textIndex < 0 {
							textIndex = alloc()
						}
						if err := emit(&canonical.Event{
							Type: canonical.EventTextDelta, Index: textIndex, Text: p.Text,
							// Gemini closes a thinking block on the text part
							// that follows it; the token has to reach the
							// client or its history cannot be replayed.
							Signature: p.ThoughtSignature,
						}); err != nil {
							return err
						}
					}
				}
			}
			if cand.FinishReason != "" {
				finish = finishToCanonical(cand.FinishReason, hasTool)
				sawFinish = true
			}
		}
	}

	if !started {
		return canonical.Errorf(canonical.ErrUpstream, "upstream closed the stream without sending any data")
	}
	if err := emit(&canonical.Event{Type: canonical.EventUsage, Usage: &usage}); err != nil {
		return err
	}
	if !sawFinish {
		finish = canonical.FinishStop
		if hasTool {
			finish = canonical.FinishToolCalls
		}
	}
	return emit(&canonical.Event{Type: canonical.EventMessageEnd, FinishReason: finish, Usage: &usage})
}

func decodeWireError(b []byte) *canonical.Error {
	var we wireError
	if err := json.Unmarshal(b, &we); err != nil || we.Error.Message == "" {
		return nil
	}
	return &canonical.Error{
		Type:       canonical.TypeForStatus(we.Error.Code),
		Message:    we.Error.Message,
		Code:       we.Error.Status,
		StatusCode: we.Error.Code,
	}
}

// --- canonical events -> Gemini stream ------------------------------------

// streamEncoder writes GenerateContentResponse chunks as SSE.
//
// Gemini expects a function call to arrive as one complete object, so tool
// arguments are buffered until the call ends and only then emitted. This is
// the one place Polyglot cannot forward a tool call incrementally, and it is
// a property of the target protocol rather than a shortcut.
type streamEncoder struct {
	w     *stream.Writer
	req   *canonical.Request
	id    string
	model string

	// Keep one content delta behind the live stream. Gemini can deliver the
	// replay signature in a following part whose text is empty, while AI SDK
	// 6 filters empty deltas before their provider metadata reaches clients.
	// Holding only the latest delta lets us put that trailing token on the
	// immediately preceding non-empty part without buffering the response.
	pendingText      *part
	pendingReasoning *part

	pendingTools map[int]*pendingTool
	toolOrder    []int
	usage        canonical.Usage
	finish       canonical.FinishReason
	finished     bool
	closed       bool
}

type pendingTool struct {
	name      string
	args      []byte
	signature string
	done      bool
}

func (Codec) NewStreamEncoder(w io.Writer, req *canonical.Request) protocol.StreamEncoder {
	model := ""
	if req != nil {
		model = req.Model
	}
	return &streamEncoder{
		w:            stream.NewWriter(w),
		req:          req,
		model:        model,
		pendingTools: map[int]*pendingTool{},
		finish:       canonical.FinishStop,
	}
}

func (e *streamEncoder) send(chunk *generateResponse) error {
	b, err := json.Marshal(chunk)
	if err != nil {
		return fmt.Errorf("encode gemini chunk: %w", err)
	}
	return e.w.Event("", b)
}

func (e *streamEncoder) partChunk(p part) error {
	return e.send(&generateResponse{
		ResponseID:   e.id,
		ModelVersion: e.model,
		Candidates: []candidate{{
			Content: &content{Role: "model", Parts: []part{p}},
			Index:   0,
		}},
	})
}

func (e *streamEncoder) Write(ev *canonical.Event) error {
	switch ev.Type {
	case canonical.EventMessageStart:
		if ev.Model != "" {
			e.model = ev.Model
		}
		if ev.ID != "" {
			e.id = ev.ID
		}
		return nil

	case canonical.EventTextDelta:
		if err := e.flushPendingReasoning(); err != nil {
			return err
		}
		if ev.Text == "" && ev.Signature == "" {
			return nil
		}
		if ev.Text == "" {
			if e.pendingText != nil {
				e.pendingText.ThoughtSignature = ev.Signature
				return e.flushPendingText()
			}
			// There is no preceding text to carry this token. Preserve the
			// provider's original shape as the only lossless fallback.
			return e.partChunk(part{Text: ev.Text, ThoughtSignature: ev.Signature})
		}
		if err := e.flushPendingText(); err != nil {
			return err
		}
		p := &part{Text: ev.Text, ThoughtSignature: ev.Signature}
		if ev.Signature != "" {
			return e.partChunk(*p)
		}
		e.pendingText = p
		return nil

	case canonical.EventReasoningDelta:
		if err := e.flushPendingText(); err != nil {
			return err
		}
		p := part{Text: ev.Text, Thought: true}
		if ev.Reasoning != nil {
			p.ThoughtSignature = ev.Reasoning.Signature
		}
		if p.Text == "" && p.ThoughtSignature == "" {
			return nil
		}
		if p.Text == "" {
			if e.pendingReasoning != nil {
				e.pendingReasoning.ThoughtSignature = p.ThoughtSignature
				return e.flushPendingReasoning()
			}
			return e.partChunk(p)
		}
		if err := e.flushPendingReasoning(); err != nil {
			return err
		}
		if p.ThoughtSignature != "" {
			return e.partChunk(p)
		}
		e.pendingReasoning = &p
		return nil

	case canonical.EventToolCallStart:
		if err := e.flushPendingContent(); err != nil {
			return err
		}
		t, ok := e.pendingTools[ev.Index]
		if !ok {
			t = &pendingTool{name: ev.ToolName}
			e.pendingTools[ev.Index] = t
			e.toolOrder = append(e.toolOrder, ev.Index)
		}
		if ev.ToolCallSignature != "" {
			t.signature = ev.ToolCallSignature
		}
		return nil

	case canonical.EventToolCallDelta:
		t, ok := e.pendingTools[ev.Index]
		if !ok {
			t = &pendingTool{name: ev.ToolName}
			e.pendingTools[ev.Index] = t
			e.toolOrder = append(e.toolOrder, ev.Index)
		}
		t.args = append(t.args, ev.ArgumentsDelta...)
		return nil

	case canonical.EventToolCallEnd:
		if t, ok := e.pendingTools[ev.Index]; ok && ev.ToolCallSignature != "" {
			t.signature = ev.ToolCallSignature
		}
		return e.flushTool(ev.Index)

	case canonical.EventUsage:
		if ev.Usage != nil {
			e.usage = *ev.Usage
		}
		return nil

	case canonical.EventMessageEnd:
		if ev.Usage != nil {
			e.usage = *ev.Usage
		}
		if ev.FinishReason != "" {
			e.finish = ev.FinishReason
		}
		if err := e.flushPendingContent(); err != nil {
			return err
		}
		if err := e.flushAllTools(); err != nil {
			return err
		}
		e.finished = true
		return e.sendFinal()

	case canonical.EventError:
		if ev.Error == nil {
			return nil
		}
		if err := e.flushPendingContent(); err != nil {
			return err
		}
		return e.w.Event("", Codec{}.EncodeError(ev.Error))
	}
	return nil
}

func (e *streamEncoder) flushPendingText() error {
	if e.pendingText == nil {
		return nil
	}
	p := *e.pendingText
	e.pendingText = nil
	return e.partChunk(p)
}

func (e *streamEncoder) flushPendingReasoning() error {
	if e.pendingReasoning == nil {
		return nil
	}
	p := *e.pendingReasoning
	e.pendingReasoning = nil
	return e.partChunk(p)
}

func (e *streamEncoder) flushPendingContent() error {
	if err := e.flushPendingReasoning(); err != nil {
		return err
	}
	return e.flushPendingText()
}

// flushTool emits a buffered call once its arguments are complete.
func (e *streamEncoder) flushTool(index int) error {
	t, ok := e.pendingTools[index]
	if !ok || t.done {
		return nil
	}
	t.done = true

	args := json.RawMessage(t.args)
	if len(args) == 0 || !json.Valid(args) {
		// A truncated stream can leave the arguments incomplete; Gemini
		// requires a well-formed object, so send an empty one rather than
		// invalid JSON.
		args = json.RawMessage("{}")
	}
	return e.partChunk(part{
		FunctionCall: &functionCall{Name: t.name, Args: args},
		// A Gemini client replays this with the call on its next turn.
		ThoughtSignature: t.signature,
	})
}

func (e *streamEncoder) flushAllTools() error {
	for _, idx := range e.toolOrder {
		if err := e.flushTool(idx); err != nil {
			return err
		}
	}
	return nil
}

func (e *streamEncoder) sendFinal() error {
	return e.send(&generateResponse{
		ResponseID:   e.id,
		ModelVersion: e.model,
		Candidates: []candidate{{
			Content:      &content{Role: "model", Parts: []part{}},
			FinishReason: finishFromCanonical(e.finish),
			Index:        0,
		}},
		UsageMetadata: &usageMetadata{
			PromptTokenCount:     e.usage.InputTokens,
			CandidatesTokenCount: e.usage.OutputTokens,
			TotalTokenCount:      e.usage.InputTokens + e.usage.OutputTokens,
			ThoughtsTokenCount:   e.usage.ReasoningTokens,
		},
	})
}

func (e *streamEncoder) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	if e.finished {
		return nil
	}
	if err := e.flushPendingContent(); err != nil {
		return err
	}
	if err := e.flushAllTools(); err != nil {
		return err
	}
	return e.sendFinal()
}
