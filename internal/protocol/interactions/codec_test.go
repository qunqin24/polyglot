package interactions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/qunqin24/polyglot/internal/canonical"
)

// The payloads below are taken from recorded Interactions traffic rather than
// invented, because there is no Go SDK to check this codec against. Testing
// against shapes I made up would only prove the codec agrees with itself.

func decode(t *testing.T, body string) (*canonical.Request, *canonical.Diagnostics) {
	t.Helper()
	d := canonical.NewDiagnostics()
	req, err := Codec{}.DecodeRequest([]byte(body), d)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	return req, d
}

func TestDecodeTheOneShotStringForm(t *testing.T) {
	// The quickstart's exact body.
	req, d := decode(t, `{
		"model": "gemini-3.6-flash",
		"input": "Explain how AI works in a few words"
	}`)

	if req.Model != "gemini-3.6-flash" {
		t.Errorf("model = %q", req.Model)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != canonical.RoleUser {
		t.Fatalf("messages = %+v", req.Messages)
	}
	if got := req.Messages[0].TextContent(); got != "Explain how AI works in a few words" {
		t.Errorf("text = %q", got)
	}
	if d.Lossy() {
		t.Errorf("a plain request should convert cleanly: %+v", d.All())
	}
}

func TestDecodeAStepTimeline(t *testing.T) {
	req, _ := decode(t, `{
		"model": "m",
		"system_instruction": "Be terse.",
		"input": [
			{"type": "user_input", "content": [{"type": "text", "text": "weather in Boston?"}]},
			{"type": "thought", "signature": "sig-think"},
			{"type": "function_call", "id": "fc_1", "name": "get_weather",
			 "arguments": {"location": "Boston"}, "signature": "sig-call"},
			{"type": "function_result", "call_id": "fc_1", "name": "get_weather",
			 "result": [{"type": "text", "text": "52F and rain"}]}
		],
		"tools": [{"type": "function", "name": "get_weather", "parameters": {"type": "object"}}]
	}`)

	if got := joinText(req.System); got != "Be terse." {
		t.Errorf("system = %q", got)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "get_weather" {
		t.Fatalf("tools = %+v", req.Tools)
	}
	// user, assistant (thought + call), tool
	if len(req.Messages) != 3 {
		t.Fatalf("want 3 messages, got %d: %+v", len(req.Messages), req.Messages)
	}
	if req.Messages[1].Role != canonical.RoleAssistant {
		t.Errorf("the thought and call should be one assistant turn: %+v", req.Messages[1])
	}

	var call *canonical.ToolCall
	var reasoning *canonical.ReasoningMeta
	for _, p := range req.Messages[1].Content {
		if p.Type == canonical.PartToolCall {
			call = p.ToolCall
		}
		if p.Type == canonical.PartReasoning {
			reasoning = p.Reasoning
		}
	}
	if call == nil || call.Name != "get_weather" {
		t.Fatalf("tool call lost: %+v", req.Messages[1])
	}
	// Compared by value, not by bytes: arguments are carried through verbatim,
	// so the client's own spacing survives — which is the point of never
	// re-encoding them.
	var args map[string]string
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		t.Fatalf("arguments are not valid JSON: %v (%s)", err, call.Arguments)
	}
	if args["location"] != "Boston" {
		t.Errorf("arguments = %s", call.Arguments)
	}
	// Both replay tokens have to survive or the next turn is rejected.
	if call.Signature != "sig-call" {
		t.Errorf("function call signature = %q", call.Signature)
	}
	if reasoning == nil || reasoning.Signature != "sig-think" {
		t.Errorf("thought signature lost: %+v", reasoning)
	}

	res := req.Messages[2].Content[0].ToolResult
	if res == nil || res.ToolCallID != "fc_1" {
		t.Fatalf("tool result = %+v", req.Messages[2])
	}
	if got := (canonical.Message{Content: res.Content}).TextContent(); got != "52F and rain" {
		t.Errorf("result text = %q", got)
	}
}

// Polyglot keeps no history, so it must never let Google keep one either.
func TestStoreIsAlwaysFalseOnTheWire(t *testing.T) {
	req, _ := decode(t, `{"model":"m","input":"hi"}`)
	req.Model = "upstream-model"

	wire, err := Codec{}.EncodeRequest(req, canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(wire, &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// Absent means true on this API, so the field has to be present and false.
	if string(out["store"]) != "false" {
		t.Errorf("store = %s, want an explicit false:\n%s", out["store"], wire)
	}
}

func TestStatefulFieldsAreReported(t *testing.T) {
	_, d := decode(t, `{
		"model": "m",
		"input": "hi",
		"previous_interaction_id": "int_001",
		"store": true,
		"background": true
	}`)

	for _, field := range []string{"previous_interaction_id", "store", "background"} {
		var found bool
		for _, n := range d.All() {
			if n.Field == field && n.Fidelity == canonical.FidelityUnsupported {
				found = true
			}
		}
		if !found {
			t.Errorf("%s was accepted silently; a stateless gateway cannot honour it: %+v", field, d.All())
		}
	}
}

// A conversation replayed to Interactions must carry the signatures back, or
// Google rejects the turn. This is the same rule the gemini codec lives by.
func TestSignaturesAreReplayedOnEncode(t *testing.T) {
	req, _ := decode(t, `{
		"model": "m",
		"input": [
			{"type": "user_input", "content": [{"type": "text", "text": "go"}]},
			{"type": "thought", "signature": "sig-think"},
			{"type": "function_call", "id": "fc_1", "name": "f", "arguments": {}, "signature": "sig-call"},
			{"type": "function_result", "call_id": "fc_1", "name": "f",
			 "result": [{"type": "text", "text": "done"}]}
		]
	}`)
	req.Model = "upstream-model"

	wire, err := Codec{}.EncodeRequest(req, canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	for _, want := range []string{"sig-think", "sig-call"} {
		if !strings.Contains(string(wire), want) {
			t.Errorf("%s was not replayed; the next turn would be rejected:\n%s", want, wire)
		}
	}

	// And the timeline is rebuilt in the order the model produced it.
	var out interactionRequest
	if err := json.Unmarshal(wire, &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	var steps []step
	if err := json.Unmarshal(out.Input, &steps); err != nil {
		t.Fatalf("input is not a step array: %v", err)
	}
	wantOrder := []string{stepUserInput, stepThought, stepFunctionCall, stepFunctionResult}
	if len(steps) != len(wantOrder) {
		t.Fatalf("got %d steps, want %d: %s", len(steps), len(wantOrder), out.Input)
	}
	for i, want := range wantOrder {
		if steps[i].Type != want {
			t.Errorf("steps[%d] = %q, want %q", i, steps[i].Type, want)
		}
	}
}

// Reasoning that arrived from another protocol has no Interactions signature.
// Sending it is still better than dropping the turn, but the loss is recorded.
func TestReplayWithoutASignatureIsReported(t *testing.T) {
	req := &canonical.Request{
		Model: "m",
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{canonical.Text("hi")}},
			{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{
				{Type: canonical.PartToolCall, ToolCall: &canonical.ToolCall{
					ID: "c1", Name: "f", Arguments: json.RawMessage(`{}`)}},
			}},
		},
	}
	d := canonical.NewDiagnostics()
	if _, err := (Codec{}).EncodeRequest(req, d); err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	var noted bool
	for _, n := range d.All() {
		if strings.Contains(n.Detail, "without a signature") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("an unsigned replay was not reported: %+v", d.All())
	}
}

// The exact non-streaming reply from Google's quickstart.
func TestDecodeResponseFromTheQuickstart(t *testing.T) {
	body := `{
		"id": "v1_ChdpQUFvYXI",
		"status": "completed",
		"usage": {"total_tokens": 197, "total_input_tokens": 8, "total_output_tokens": 12},
		"steps": [
			{"type": "thought", "signature": "EvEFCu4FAQw"},
			{"type": "model_output", "content": [{"type": "text",
			 "text": "AI learns patterns from data."}]}
		]
	}`
	resp, err := Codec{}.DecodeResponse([]byte(body), canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if resp.ID != "v1_ChdpQUFvYXI" || resp.FinishReason != canonical.FinishStop {
		t.Errorf("resp = %+v", resp)
	}
	// Recorded traffic uses total_* naming; the migration guide's
	// prompt_tokens/completion_tokens would decode to zero here.
	if resp.Usage.InputTokens != 8 || resp.Usage.OutputTokens != 12 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	var sawReasoning, sawText bool
	for _, p := range resp.Message.Content {
		if p.Type == canonical.PartReasoning && p.Reasoning != nil && p.Reasoning.Signature == "EvEFCu4FAQw" {
			sawReasoning = true
		}
		if p.Type == canonical.PartText && strings.Contains(p.Text, "AI learns") {
			sawText = true
		}
	}
	if !sawReasoning {
		t.Errorf("the thought signature was lost: %+v", resp.Message.Content)
	}
	if !sawText {
		t.Errorf("the answer text was lost: %+v", resp.Message.Content)
	}
}

func TestRequiresActionMeansToolCalls(t *testing.T) {
	body := `{
		"id": "int_001", "status": "requires_action",
		"steps": [{"type": "function_call", "id": "fc_1", "name": "get_weather",
		           "arguments": {"location": "Boston"}, "signature": "s"}]
	}`
	resp, err := Codec{}.DecodeResponse([]byte(body), canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if resp.FinishReason != canonical.FinishToolCalls {
		t.Errorf("finish = %q, want tool_calls", resp.FinishReason)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "get_weather" || calls[0].Signature != "s" {
		t.Fatalf("tool calls = %+v", calls)
	}
}

// --- streaming ------------------------------------------------------------

// Recorded chunks from a real tool-calling turn. Note they are bare JSON
// objects with no SSE `event:` line — the discriminator is `event_type`
// inside the payload, which is why this codec keys off that.
const recordedToolCallStream = `data: {"interaction":{"id":"v1_Umsx","status":"in_progress","object":"interaction","model":"gemini-2.5-flash"},"event_type":"interaction.created"}

data: {"interaction_id":"v1_Umsx","status":"in_progress","event_type":"interaction.status_update"}

data: {"index":0,"step":{"type":"thought"},"event_type":"step.start"}

data: {"index":0,"delta":{"signature":"CiQBDDnWx","type":"thought_signature"},"event_type":"step.delta"}

data: {"index":0,"event_type":"step.stop"}

data: {"index":1,"step":{"id":"61nzpsv4","signature":"","type":"function_call","name":"getWeather","arguments":{}},"event_type":"step.start"}

data: {"index":1,"delta":{"arguments":"{\"location\":","type":"arguments_delta"},"event_type":"step.delta"}

data: {"index":1,"delta":{"arguments":"\"San Francisco\"}","type":"arguments_delta"},"event_type":"step.delta"}

data: {"index":1,"event_type":"step.stop"}

data: {"interaction":{"id":"v1_Umsx","status":"requires_action","usage":{"total_tokens":133,"total_input_tokens":53,"total_output_tokens":15,"total_thought_tokens":65},"object":"interaction","model":"gemini-2.5-flash"},"event_type":"interaction.completed"}

`

func collect(t *testing.T, sse string) []*canonical.Event {
	t.Helper()
	var out []*canonical.Event
	err := Codec{}.DecodeStream(context.Background(), strings.NewReader(sse), func(ev *canonical.Event) error {
		out = append(out, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("DecodeStream: %v", err)
	}
	return out
}

func TestDecodeRecordedToolCallStream(t *testing.T) {
	events := collect(t, recordedToolCallStream)

	if len(events) == 0 || events[0].Type != canonical.EventMessageStart {
		t.Fatalf("first event = %+v", events)
	}

	var sig string
	var args strings.Builder
	var sawStart, sawEnd bool
	var usage *canonical.Usage
	var finish canonical.FinishReason
	for _, ev := range events {
		switch ev.Type {
		case canonical.EventReasoningDelta:
			if ev.Reasoning != nil {
				sig = ev.Reasoning.Signature
			}
		case canonical.EventToolCallStart:
			sawStart = true
			if ev.ToolName != "getWeather" || ev.ToolCallID != "61nzpsv4" {
				t.Errorf("tool call start = %+v", ev)
			}
		case canonical.EventToolCallDelta:
			args.WriteString(ev.ArgumentsDelta)
		case canonical.EventToolCallEnd:
			sawEnd = true
		case canonical.EventUsage:
			usage = ev.Usage
		case canonical.EventMessageEnd:
			finish = ev.FinishReason
		}
	}

	if sig != "CiQBDDnWx" {
		t.Errorf("thought signature = %q; it arrives as its own delta type", sig)
	}
	if !sawStart || !sawEnd {
		t.Errorf("tool call was not opened and closed: start=%v end=%v", sawStart, sawEnd)
	}
	// The fragments only become valid JSON once joined — which is the point.
	if got := args.String(); got != `{"location":"San Francisco"}` {
		t.Errorf("accumulated arguments = %q", got)
	}
	if usage == nil || usage.InputTokens != 53 || usage.OutputTokens != 15 || usage.ReasoningTokens != 65 {
		t.Errorf("usage = %+v", usage)
	}
	if finish != canonical.FinishToolCalls {
		t.Errorf("finish = %q, want tool_calls for requires_action", finish)
	}
}

// A fragment of argument JSON must never be parsed on its own.
func TestArgumentFragmentsArePassedThroughUnparsed(t *testing.T) {
	events := collect(t, recordedToolCallStream)
	for _, ev := range events {
		if ev.Type != canonical.EventToolCallDelta {
			continue
		}
		if json.Valid([]byte(ev.ArgumentsDelta)) && strings.Contains(ev.ArgumentsDelta, "location") {
			continue // a whole-object delta is legal too
		}
		if ev.ArgumentsDelta == "" {
			t.Error("an empty fragment was emitted")
		}
	}
	// The first fragment is deliberately invalid JSON on its own.
	var first string
	for _, ev := range events {
		if ev.Type == canonical.EventToolCallDelta {
			first = ev.ArgumentsDelta
			break
		}
	}
	if json.Valid([]byte(first)) {
		t.Errorf("this recording's first fragment %q should be partial JSON; "+
			"the test is no longer exercising fragment handling", first)
	}
}

func TestStreamEncoderProducesTheDocumentedShape(t *testing.T) {
	var sb strings.Builder
	enc := Codec{}.NewStreamEncoder(&sb, &canonical.Request{Model: "m"})

	for _, ev := range []*canonical.Event{
		{Type: canonical.EventMessageStart, ID: "v1_x", Model: "m"},
		{Type: canonical.EventTextDelta, Index: 0, Text: "Hello"},
		{Type: canonical.EventToolCallStart, Index: 1, ToolCallID: "fc_1", ToolName: "f"},
		{Type: canonical.EventToolCallDelta, Index: 1, ToolCallID: "fc_1", ArgumentsDelta: `{"a":1}`},
		{Type: canonical.EventToolCallEnd, Index: 1, ToolCallID: "fc_1"},
		{Type: canonical.EventMessageEnd, FinishReason: canonical.FinishToolCalls,
			Usage: &canonical.Usage{InputTokens: 3, OutputTokens: 4}},
	} {
		if err := enc.Write(ev); err != nil {
			t.Fatalf("Write(%s): %v", ev.Type, err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	out := sb.String()
	for _, want := range []string{
		`"event_type":"interaction.created"`,
		`"event_type":"step.start"`,
		`"event_type":"step.delta"`,
		`"type":"text"`,
		`"type":"arguments_delta"`,
		`"event_type":"step.stop"`,
		`"event_type":"interaction.completed"`,
		`"status":"requires_action"`,
		`"total_input_tokens":3`,
		"data: [DONE]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in:\n%s", want, out)
		}
	}
}

// What this codec writes, it must be able to read back.
func TestStreamRoundTrip(t *testing.T) {
	var sb strings.Builder
	enc := Codec{}.NewStreamEncoder(&sb, &canonical.Request{Model: "m"})
	in := []*canonical.Event{
		{Type: canonical.EventMessageStart, ID: "v1_x", Model: "m"},
		{Type: canonical.EventReasoningDelta, Index: 0, Text: "thinking"},
		{Type: canonical.EventTextDelta, Index: 1, Text: "answer"},
		{Type: canonical.EventMessageEnd, FinishReason: canonical.FinishStop,
			Usage: &canonical.Usage{InputTokens: 1, OutputTokens: 2}},
	}
	for _, ev := range in {
		if err := enc.Write(ev); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	acc := canonical.NewAccumulator()
	for _, ev := range collect(t, sb.String()) {
		acc.Add(ev)
	}
	got := acc.Response()
	if !strings.Contains(got.Message.TextContent(), "answer") {
		t.Errorf("text did not survive the round trip: %+v", got.Message.Content)
	}
	if got.Usage.InputTokens != 1 || got.Usage.OutputTokens != 2 {
		t.Errorf("usage = %+v", got.Usage)
	}
}
