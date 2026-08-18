package responses

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

// --- upstream Responses SSE -> canonical events ---------------------------

// DecodeStream reads the Responses API's typed event stream.
//
// Every event carries an output_index identifying which output item it belongs
// to, which maps directly onto the canonical block index. Events belonging to
// provider-native output items are carried opaquely for a same-protocol route.
func (Codec) DecodeStream(ctx context.Context, r io.Reader, emit func(*canonical.Event) error) error {
	sr := stream.NewReader(r)

	var (
		started    bool
		usage      canonical.Usage
		finish     canonical.FinishReason
		sawFinish  bool
		toolItem   = map[int]bool{} // output_index -> is a function_call
		nativeItem = map[int]bool{}
	)
	emitNative := func(name string, index int, raw []byte) error {
		return emit(&canonical.Event{Type: canonical.EventNative, Index: index,
			Native: &canonical.NativeEvent{Protocol: string(protocol.OpenAIResponses), Name: name,
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
		if len(frame.Data) == 0 || string(frame.Data) == "[DONE]" {
			continue
		}

		var ev streamEvent
		if err := json.Unmarshal(frame.Data, &ev); err != nil {
			continue
		}
		if ev.Type == "" {
			ev.Type = frame.Event
		}

		switch ev.Type {
		case "response.created":
			started = true
			out := &canonical.Event{Type: canonical.EventMessageStart}
			if ev.Response != nil {
				out.ID = ev.Response.ID
				out.Model = ev.Response.Model
			}
			if err := emit(out); err != nil {
				return err
			}

		case "response.output_item.added":
			if ev.Item == nil {
				continue
			}
			if ev.Item.Type == "function_call" {
				toolItem[ev.OutputIndex] = true
				if err := emit(&canonical.Event{
					Type:              canonical.EventToolCallStart,
					Index:             ev.OutputIndex,
					ToolCallID:        orDefault(ev.Item.CallID, "call_"+idgen.New()),
					ToolName:          ev.Item.Name,
					ToolCallSignature: ev.Item.ExtraContent.Signature(),
				}); err != nil {
					return err
				}
			} else if len(ev.Item.Raw) > 0 {
				nativeItem[ev.OutputIndex] = true
				if err := emitNative(ev.Type, ev.OutputIndex, frame.Data); err != nil {
					return err
				}
			}

		case "response.output_text.delta":
			if ev.Delta == "" {
				continue
			}
			if err := emit(&canonical.Event{
				Type: canonical.EventTextDelta, Index: ev.OutputIndex, Text: ev.Delta,
			}); err != nil {
				return err
			}

		case "response.refusal.delta":
			if ev.Delta == "" {
				continue
			}
			if err := emit(&canonical.Event{
				Type: canonical.EventTextDelta, Index: ev.OutputIndex, Text: ev.Delta,
			}); err != nil {
				return err
			}

		case "response.reasoning_summary_text.delta":
			if ev.Delta == "" {
				continue
			}
			if err := emit(&canonical.Event{
				Type: canonical.EventReasoningDelta, Index: ev.OutputIndex, Text: ev.Delta,
			}); err != nil {
				return err
			}

		case "response.function_call_arguments.delta":
			// A fragment of the argument JSON; never valid on its own.
			if ev.Delta == "" {
				continue
			}
			if err := emit(&canonical.Event{
				Type:           canonical.EventToolCallDelta,
				Index:          ev.OutputIndex,
				ArgumentsDelta: ev.Delta,
			}); err != nil {
				return err
			}

		case "response.output_item.done":
			if toolItem[ev.OutputIndex] {
				if err := emit(&canonical.Event{
					Type: canonical.EventToolCallEnd, Index: ev.OutputIndex,
				}); err != nil {
					return err
				}
			} else if nativeItem[ev.OutputIndex] {
				if err := emitNative(ev.Type, ev.OutputIndex, frame.Data); err != nil {
					return err
				}
			}

		case "response.completed", "response.incomplete", "response.failed":
			if ev.Response == nil {
				continue
			}
			if ev.Response.Usage != nil {
				usage = usageToCanonical(ev.Response.Usage)
			}
			hasTool := false
			for _, it := range ev.Response.Output {
				if it.Type == "function_call" {
					hasTool = true
				}
			}
			finish = finishFor(*ev.Response, hasTool)
			sawFinish = true
			if ev.Response.Error != nil && ev.Response.Error.Message != "" {
				return emit(&canonical.Event{Type: canonical.EventError, Error: &canonical.Error{
					Type:    canonical.ErrUpstream,
					Message: ev.Response.Error.Message,
					Code:    ev.Response.Error.Code,
				}})
			}

		case "error":
			msg := ev.Message
			if msg == "" && ev.Response != nil && ev.Response.Error != nil {
				msg = ev.Response.Error.Message
			}
			if msg != "" {
				return emit(&canonical.Event{Type: canonical.EventError, Error: &canonical.Error{
					Type: canonical.ErrUpstream, Message: msg, Code: ev.Code,
				}})
			}

		default:
			if nativeItem[ev.OutputIndex] {
				if err := emitNative(ev.Type, ev.OutputIndex, frame.Data); err != nil {
					return err
				}
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
	}
	return emit(&canonical.Event{Type: canonical.EventMessageEnd, FinishReason: finish, Usage: &usage})
}

// --- canonical events -> Responses SSE ------------------------------------

// streamEncoder writes the Responses API's typed event sequence.
//
// The protocol requires each output item to be announced before its deltas and
// closed afterwards, so the encoder tracks which canonical block index maps to
// which output_index and what kind of item it opened.
type streamEncoder struct {
	w     *stream.Writer
	req   *canonical.Request
	id    string
	model string
	seq   int

	items     map[int]*outputItem // canonical index -> open item
	order     []int
	nextIndex int
	openIdx   int // canonical index of the item currently open, -1 if none

	usage        canonical.Usage
	finish       canonical.FinishReason
	started      bool
	finished     bool
	closed       bool
	nativeOutput []item
}

type outputItem struct {
	outputIndex int
	kind        string // message | reasoning | function_call
	closed      bool
	itemID      string
	callID      string
	toolName    string
	signature   string
	args        []byte
	text        []byte
}

func (Codec) NewStreamEncoder(w io.Writer, req *canonical.Request) protocol.StreamEncoder {
	model := ""
	if req != nil {
		model = req.Model
	}
	return &streamEncoder{
		w:       stream.NewWriter(w),
		req:     req,
		id:      "resp_" + idgen.New(),
		model:   model,
		items:   map[int]*outputItem{},
		openIdx: -1,
		finish:  canonical.FinishStop,
	}
}

// send emits one typed event. The Responses API names the SSE event after the
// payload's type and numbers every event, so clients can detect a gap.
func (e *streamEncoder) send(name string, payload map[string]any) error {
	payload["type"] = name
	payload["sequence_number"] = e.seq
	e.seq++
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	return e.w.Event(name, b)
}

func (e *streamEncoder) sendNative(ev *canonical.NativeEvent) error {
	var payload map[string]any
	if err := json.Unmarshal(ev.Raw, &payload); err != nil {
		return nil
	}
	delete(payload, "type")
	delete(payload, "sequence_number")
	if ev.Name == "response.output_item.added" {
		if e.openIdx >= 0 {
			if err := e.closeItem(e.items[e.openIdx]); err != nil {
				return err
			}
		}
		if raw, ok := payload["item"]; ok {
			b, _ := json.Marshal(raw)
			var it item
			if json.Unmarshal(b, &it) == nil {
				e.nativeOutput = append(e.nativeOutput, it)
			}
		}
		if idx, ok := payload["output_index"].(float64); ok && int(idx) >= e.nextIndex {
			e.nextIndex = int(idx) + 1
		}
	}
	return e.send(ev.Name, payload)
}

// envelope is the response object echoed in lifecycle events.
func (e *streamEncoder) envelope(status string, output []item, usage *wireUsage) map[string]any {
	env := map[string]any{
		"id":         e.id,
		"object":     "response",
		"created_at": time.Now().Unix(),
		"model":      e.model,
		"status":     status,
		"output":     output,
	}
	if usage != nil {
		env["usage"] = usage
	}
	if status == "incomplete" {
		env["incomplete_details"] = map[string]string{"reason": "max_output_tokens"}
	}
	return env
}

func (e *streamEncoder) ensureStart() error {
	if e.started {
		return nil
	}
	e.started = true
	if err := e.send("response.created", map[string]any{
		"response": e.envelope("in_progress", []item{}, nil),
	}); err != nil {
		return err
	}
	return e.send("response.in_progress", map[string]any{
		"response": e.envelope("in_progress", []item{}, nil),
	})
}

// open announces an output item the first time its canonical index appears.
func (e *streamEncoder) open(canonicalIdx int, kind string, ev *canonical.Event) (*outputItem, error) {
	if it, ok := e.items[canonicalIdx]; ok {
		return it, nil
	}
	// A client reads output items as a sequence: each one is announced, filled
	// and finished before the next appears. Close whatever is still open.
	if e.openIdx >= 0 {
		if err := e.closeItem(e.items[e.openIdx]); err != nil {
			return nil, err
		}
	}
	it := &outputItem{outputIndex: e.nextIndex, kind: kind}
	e.nextIndex++
	e.items[canonicalIdx] = it
	e.order = append(e.order, canonicalIdx)
	e.openIdx = canonicalIdx

	var payload map[string]any
	switch kind {
	case "message":
		it.itemID = "msg_" + idgen.New()
		payload = map[string]any{"type": "message", "id": it.itemID, "status": "in_progress",
			"role": "assistant", "content": []contentPart{}}
	case "reasoning":
		it.itemID = "rs_" + idgen.New()
		payload = map[string]any{"type": "reasoning", "id": it.itemID, "summary": []summaryPart{}}
	case "function_call":
		it.itemID = "fc_" + idgen.New()
		it.callID = orDefault(ev.ToolCallID, "call_"+idgen.New())
		it.toolName = ev.ToolName
		it.signature = ev.ToolCallSignature
		payload = map[string]any{"type": "function_call", "id": it.itemID, "call_id": it.callID,
			"name": it.toolName, "arguments": "", "status": "in_progress"}
		// The client has to hand this back next turn for a Gemini upstream to
		// accept the call again.
		if x := protocol.SignatureExtra(it.signature); x != nil {
			payload["extra_content"] = x
		}
	}

	if err := e.send("response.output_item.added", map[string]any{
		"output_index": it.outputIndex, "item": payload,
	}); err != nil {
		return nil, err
	}
	if kind == "message" {
		if err := e.send("response.content_part.added", map[string]any{
			"item_id": it.itemID, "output_index": it.outputIndex, "content_index": 0,
			"part": contentPart{Type: "output_text", Text: ""},
		}); err != nil {
			return nil, err
		}
	}
	return it, nil
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
		it, err := e.open(ev.Index, "message", ev)
		if err != nil {
			return err
		}
		it.text = append(it.text, ev.Text...)
		return e.send("response.output_text.delta", map[string]any{
			"item_id": it.itemID, "output_index": it.outputIndex,
			"content_index": 0, "delta": ev.Text,
		})

	case canonical.EventReasoningDelta:
		if ev.Text == "" {
			return nil
		}
		if err := e.ensureStart(); err != nil {
			return err
		}
		it, err := e.open(ev.Index, "reasoning", ev)
		if err != nil {
			return err
		}
		it.text = append(it.text, ev.Text...)
		return e.send("response.reasoning_summary_text.delta", map[string]any{
			"item_id": it.itemID, "output_index": it.outputIndex,
			"summary_index": 0, "delta": ev.Text,
		})

	case canonical.EventToolCallStart:
		if err := e.ensureStart(); err != nil {
			return err
		}
		_, err := e.open(ev.Index, "function_call", ev)
		return err

	case canonical.EventToolCallDelta:
		if ev.ArgumentsDelta == "" {
			return nil
		}
		if err := e.ensureStart(); err != nil {
			return err
		}
		it, err := e.open(ev.Index, "function_call", ev)
		if err != nil {
			return err
		}
		it.args = append(it.args, ev.ArgumentsDelta...)
		return e.send("response.function_call_arguments.delta", map[string]any{
			"item_id": it.itemID, "output_index": it.outputIndex, "delta": ev.ArgumentsDelta,
		})

	case canonical.EventToolCallEnd:
		it, ok := e.items[ev.Index]
		if !ok {
			return nil
		}
		return e.closeItem(it)

	case canonical.EventNative:
		if ev.Native == nil || ev.Native.Protocol != string(protocol.OpenAIResponses) {
			return nil
		}
		if err := e.ensureStart(); err != nil {
			return err
		}
		return e.sendNative(ev.Native)

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
		return e.send("error", map[string]any{
			"message": ev.Error.Message,
			"code":    ev.Error.Code,
			"param":   ev.Error.Param,
		})
	}
	return nil
}

// closeItem emits the done events for one output item, in the order the
// protocol expects.
func (e *streamEncoder) closeItem(it *outputItem) error {
	if it.closed {
		return nil
	}
	it.closed = true
	if e.openIdx >= 0 && e.items[e.openIdx] == it {
		e.openIdx = -1
	}

	switch it.kind {
	case "message":
		if err := e.send("response.output_text.done", map[string]any{
			"item_id": it.itemID, "output_index": it.outputIndex,
			"content_index": 0, "text": string(it.text),
		}); err != nil {
			return err
		}
		if err := e.send("response.content_part.done", map[string]any{
			"item_id": it.itemID, "output_index": it.outputIndex, "content_index": 0,
			"part": contentPart{Type: "output_text", Text: string(it.text)},
		}); err != nil {
			return err
		}
	case "reasoning":
		if err := e.send("response.reasoning_summary_text.done", map[string]any{
			"item_id": it.itemID, "output_index": it.outputIndex,
			"summary_index": 0, "text": string(it.text),
		}); err != nil {
			return err
		}
	case "function_call":
		if err := e.send("response.function_call_arguments.done", map[string]any{
			"item_id": it.itemID, "output_index": it.outputIndex,
			"arguments": e.argsOf(it),
		}); err != nil {
			return err
		}
	}

	return e.send("response.output_item.done", map[string]any{
		"output_index": it.outputIndex, "item": e.finalItem(it),
	})
}

// argsOf returns complete arguments. A truncated stream can leave them
// unfinished, and the Responses API requires a well-formed object.
func (e *streamEncoder) argsOf(it *outputItem) string {
	if len(it.args) > 0 && json.Valid(it.args) {
		return string(it.args)
	}
	return "{}"
}

func (e *streamEncoder) finalItem(it *outputItem) item {
	switch it.kind {
	case "message":
		content, _ := json.Marshal([]contentPart{{Type: "output_text", Text: string(it.text)}})
		return item{Type: "message", ID: it.itemID, Role: "assistant",
			Status: "completed", Content: content}
	case "reasoning":
		out := item{Type: "reasoning", ID: it.itemID}
		if len(it.text) > 0 {
			out.Summary = []summaryPart{{Type: "summary_text", Text: string(it.text)}}
		}
		return out
	default:
		return item{Type: "function_call", ID: it.itemID, CallID: it.callID,
			Name: it.toolName, Arguments: e.argsOf(it), Status: "completed",
			ExtraContent: protocol.SignatureExtra(it.signature)}
	}
}

func (e *streamEncoder) emitEnd() error {
	// Close whatever is still open, in the order the items were created.
	var output []item
	for _, idx := range e.order {
		it := e.items[idx]
		if err := e.closeItem(it); err != nil {
			return err
		}
		output = append(output, e.finalItem(it))
	}
	output = append(output, e.nativeOutput...)
	if output == nil {
		output = []item{}
	}

	usage := &wireUsage{
		InputTokens:  e.usage.InputTokens,
		OutputTokens: e.usage.OutputTokens,
		TotalTokens:  e.usage.InputTokens + e.usage.OutputTokens,
	}
	if e.usage.ReasoningTokens > 0 {
		usage.OutputTokensDetails = &struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		}{ReasoningTokens: e.usage.ReasoningTokens}
	}

	status, event := "completed", "response.completed"
	if e.finish == canonical.FinishLength {
		status, event = "incomplete", "response.incomplete"
	}
	return e.send(event, map[string]any{"response": e.envelope(status, output, usage)})
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
