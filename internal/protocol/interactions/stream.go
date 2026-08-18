package interactions

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/idgen"
	"github.com/qunqin24/polyglot/internal/protocol"
	"github.com/qunqin24/polyglot/internal/stream"
)

// Interactions streaming.
//
// Events are discriminated by the `event_type` member inside the JSON, not by
// the SSE `event:` line: recorded traffic sends bare JSON objects with no
// named line at all, so keying off the line would silently drop everything.
// The line is still written on the way out, because it is valid SSE and some
// clients read it.
//
// A step is identified by its `index`, which is what maps onto the canonical
// content block index. Function call arguments arrive as partial JSON strings
// and are passed straight through as fragments — never parsed here, because a
// fragment is not valid JSON on its own.

func (Codec) DecodeStream(ctx context.Context, r io.Reader, emit func(*canonical.Event) error) error {
	sr := stream.NewReader(r)

	// Track what each step index is, so a delta can be turned into the right
	// canonical event without re-reading the step that opened it.
	kinds := map[int]string{}
	toolIDs := map[int]string{}
	toolNames := map[int]string{}
	started := false

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		frame, err := sr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		data := strings.TrimSpace(string(frame.Data))
		if data == "" || data == "[DONE]" {
			continue
		}

		var ev streamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			// A frame we cannot parse is skipped rather than failing the
			// stream: the reply so far is still worth delivering.
			continue
		}

		switch ev.EventType {
		case evInteractionCreated:
			if started {
				continue
			}
			started = true
			out := &canonical.Event{Type: canonical.EventMessageStart}
			if ev.Interaction != nil {
				out.ID = ev.Interaction.ID
				out.Model = ev.Interaction.Model
			}
			if out.ID == "" {
				out.ID = "v1_" + idgen.New()
			}
			if err := emit(out); err != nil {
				return err
			}

		case evStepStart:
			if ev.Step == nil {
				continue
			}
			kinds[ev.Index] = ev.Step.Type
			if ev.Step.Type == stepFunctionCall {
				id := ev.Step.ID
				if id == "" {
					id = "fc_" + idgen.New()
				}
				toolIDs[ev.Index] = id
				toolNames[ev.Index] = ev.Step.Name
				if err := emit(&canonical.Event{
					Type:              canonical.EventToolCallStart,
					Index:             ev.Index,
					ToolCallID:        id,
					ToolName:          ev.Step.Name,
					ToolCallSignature: deref(ev.Step.Signature),
				}); err != nil {
					return err
				}
			}

		case evStepDelta:
			if ev.Delta == nil {
				continue
			}
			if err := emitDelta(ev, kinds, toolIDs, toolNames, emit); err != nil {
				return err
			}

		case evStepStop:
			if kinds[ev.Index] == stepFunctionCall {
				if err := emit(&canonical.Event{
					Type:       canonical.EventToolCallEnd,
					Index:      ev.Index,
					ToolCallID: toolIDs[ev.Index],
					ToolName:   toolNames[ev.Index],
				}); err != nil {
					return err
				}
			}

		case evCompleted:
			finish := canonical.FinishStop
			if ev.Interaction != nil {
				if ev.Interaction.Status == statusRequiresAction {
					finish = canonical.FinishToolCalls
				}
				if u := ev.Interaction.Usage; u != nil {
					if err := emit(&canonical.Event{
						Type: canonical.EventUsage,
						Usage: &canonical.Usage{
							InputTokens:       u.TotalInputTokens,
							OutputTokens:      u.TotalOutputTokens,
							ReasoningTokens:   u.TotalThoughtTokens,
							CachedInputTokens: u.TotalCachedTokens,
						},
					}); err != nil {
						return err
					}
				}
			}
			if err := emit(&canonical.Event{Type: canonical.EventMessageEnd, FinishReason: finish}); err != nil {
				return err
			}

		case evError:
			msg := "upstream error"
			if ev.Error != nil && ev.Error.Message != "" {
				msg = ev.Error.Message
			}
			if err := emit(&canonical.Event{
				Type:  canonical.EventError,
				Error: canonical.Errorf(canonical.ErrUpstream, "%s", msg),
			}); err != nil {
				return err
			}
		}
	}
}

// emitDelta turns one step.delta into the canonical event it means.
func emitDelta(ev streamEvent, kinds, toolIDs, toolNames map[int]string, emit func(*canonical.Event) error) error {
	d := ev.Delta
	switch d.Type {
	case deltaText:
		if d.Text == "" {
			return nil
		}
		return emit(&canonical.Event{Type: canonical.EventTextDelta, Index: ev.Index, Text: d.Text})

	case deltaThoughtSummary:
		if d.Content == nil || d.Content.Text == "" {
			return nil
		}
		return emit(&canonical.Event{
			Type: canonical.EventReasoningDelta, Index: ev.Index, Text: d.Content.Text,
		})

	case deltaThoughtSignature:
		if d.Signature == "" {
			return nil
		}
		// The replay token for a thought. It arrives as its own delta rather
		// than on the step that opened it.
		return emit(&canonical.Event{
			Type:      canonical.EventReasoningDelta,
			Index:     ev.Index,
			Reasoning: &canonical.ReasoningMeta{Signature: d.Signature},
		})

	case deltaArguments:
		if d.Arguments == "" {
			return nil
		}
		// A fragment of the argument JSON. Passed through untouched: it is
		// not valid JSON on its own and must not be parsed here.
		return emit(&canonical.Event{
			Type:           canonical.EventToolCallDelta,
			Index:          ev.Index,
			ToolCallID:     toolIDs[ev.Index],
			ToolName:       toolNames[ev.Index],
			ArgumentsDelta: d.Arguments,
		})
	}
	return nil
}

// --- encoding -------------------------------------------------------------

func (Codec) NewStreamEncoder(w io.Writer, req *canonical.Request) protocol.StreamEncoder {
	return &streamEncoder{
		w:     stream.NewWriter(w),
		model: modelOf(req),
		id:    "v1_" + idgen.New(),
	}
}

type streamEncoder struct {
	w     *stream.Writer
	model string
	id    string

	opened   map[int]bool
	started  bool
	finished bool
	usage    *canonical.Usage
	finish   canonical.FinishReason
}

func (e *streamEncoder) Write(ev *canonical.Event) error {
	if e.opened == nil {
		e.opened = map[int]bool{}
	}
	switch ev.Type {
	case canonical.EventMessageStart:
		if ev.ID != "" {
			e.id = ev.ID
		}
		if ev.Model != "" {
			e.model = ev.Model
		}
		return e.emit(evInteractionCreated, streamEvent{
			EventType: evInteractionCreated,
			Interaction: &interactionResponse{
				ID: e.id, Object: "interaction", Model: e.model, Status: "in_progress",
			},
		})

	case canonical.EventTextDelta:
		if err := e.open(ev.Index, step{Type: stepModelOutput}); err != nil {
			return err
		}
		return e.emit(evStepDelta, streamEvent{
			EventType: evStepDelta, Index: ev.Index,
			Delta: &stepDelta{Type: deltaText, Text: ev.Text},
		})

	case canonical.EventReasoningDelta:
		if err := e.open(ev.Index, step{Type: stepThought}); err != nil {
			return err
		}
		if ev.Reasoning != nil && ev.Reasoning.Signature != "" {
			return e.emit(evStepDelta, streamEvent{
				EventType: evStepDelta, Index: ev.Index,
				Delta: &stepDelta{Type: deltaThoughtSignature, Signature: ev.Reasoning.Signature},
			})
		}
		if ev.Text == "" {
			return nil
		}
		return e.emit(evStepDelta, streamEvent{
			EventType: evStepDelta, Index: ev.Index,
			Delta: &stepDelta{
				Type:    deltaThoughtSummary,
				Content: &thoughtSummaryItem{Type: "text", Text: ev.Text},
			},
		})

	case canonical.EventToolCallStart:
		s := step{Type: stepFunctionCall, ID: ev.ToolCallID, Name: ev.ToolName}
		if ev.ToolCallSignature != "" {
			s.Signature = canonical.Ptr(ev.ToolCallSignature)
		}
		if err := e.open(ev.Index, s); err != nil {
			return err
		}
		if ev.ArgumentsDelta == "" {
			return nil
		}
		return e.emit(evStepDelta, streamEvent{
			EventType: evStepDelta, Index: ev.Index,
			Delta: &stepDelta{Type: deltaArguments, Arguments: ev.ArgumentsDelta},
		})

	case canonical.EventToolCallDelta:
		if err := e.open(ev.Index, step{
			Type: stepFunctionCall, ID: ev.ToolCallID, Name: ev.ToolName,
		}); err != nil {
			return err
		}
		if ev.ArgumentsDelta == "" {
			return nil
		}
		return e.emit(evStepDelta, streamEvent{
			EventType: evStepDelta, Index: ev.Index,
			Delta: &stepDelta{Type: deltaArguments, Arguments: ev.ArgumentsDelta},
		})

	case canonical.EventToolCallEnd:
		return e.closeStep(ev.Index)

	case canonical.EventUsage:
		if ev.Usage != nil {
			u := *ev.Usage
			e.usage = &u
		}
		return nil

	case canonical.EventMessageEnd:
		if ev.Usage != nil {
			u := *ev.Usage
			e.usage = &u
		}
		e.finish = ev.FinishReason
		return nil

	case canonical.EventError:
		msg := "upstream error"
		if ev.Error != nil {
			msg = ev.Error.Message
		}
		return e.emit(evError, streamEvent{
			EventType: evError,
			Error:     &wireError{Message: msg, Code: "upstream_error"},
		})
	}
	return nil
}

// open emits step.start the first time an index is written to, and closes the
// previous step so the timeline stays well formed.
func (e *streamEncoder) open(index int, s step) error {
	if !e.started {
		e.started = true
		if err := e.emit(evInteractionCreated, streamEvent{
			EventType: evInteractionCreated,
			Interaction: &interactionResponse{
				ID: e.id, Object: "interaction", Model: e.model, Status: "in_progress",
			},
		}); err != nil {
			return err
		}
	}
	if e.opened[index] {
		return nil
	}
	e.opened[index] = true
	return e.emit(evStepStart, streamEvent{EventType: evStepStart, Index: index, Step: &s})
}

func (e *streamEncoder) closeStep(index int) error {
	if !e.opened[index] {
		return nil
	}
	delete(e.opened, index)
	return e.emit(evStepStop, streamEvent{EventType: evStepStop, Index: index})
}

func (e *streamEncoder) Close() error {
	if e.finished {
		return nil
	}
	e.finished = true

	for index := range e.opened {
		if err := e.closeStep(index); err != nil {
			return err
		}
	}

	final := &interactionResponse{
		ID: e.id, Object: "interaction", Model: e.model, Status: statusCompleted,
	}
	if e.finish == canonical.FinishToolCalls {
		final.Status = statusRequiresAction
	}
	if u := e.usage; u != nil {
		final.Usage = &usage{
			TotalInputTokens:   u.InputTokens,
			TotalOutputTokens:  u.OutputTokens,
			TotalThoughtTokens: u.ReasoningTokens,
			TotalCachedTokens:  u.CachedInputTokens,
			TotalTokens:        u.InputTokens + u.OutputTokens + u.ReasoningTokens,
		}
	}
	if err := e.emit(evCompleted, streamEvent{EventType: evCompleted, Interaction: final}); err != nil {
		return err
	}
	// The documented terminator.
	return e.w.Event("done", []byte("[DONE]"))
}

func (e *streamEncoder) emit(name string, ev streamEvent) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return e.w.Event(name, b)
}

func modelOf(req *canonical.Request) string {
	if req == nil {
		return ""
	}
	return req.Model
}
