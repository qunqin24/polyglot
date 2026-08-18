package responses

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/qunqin24/polyglot/internal/canonical"
)

func decode(t *testing.T, body string) (*canonical.Request, *canonical.Diagnostics) {
	t.Helper()
	d := canonical.NewDiagnostics()
	req, err := Codec{}.DecodeRequest([]byte(body), d)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	return req, d
}

// TestDecodeStringInput covers the simplest form the API accepts: input as a
// bare string rather than a list of items.
func TestDecodeStringInput(t *testing.T) {
	req, _ := decode(t, `{
		"model": "gpt-5",
		"input": "Hello",
		"instructions": "Be terse.",
		"max_output_tokens": 256
	}`)

	if joinText(req.System) != "Be terse." {
		t.Errorf("instructions did not become the system prompt: %q", joinText(req.System))
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != canonical.RoleUser {
		t.Fatalf("messages = %+v", req.Messages)
	}
	if req.Messages[0].TextContent() != "Hello" {
		t.Errorf("text = %q", req.Messages[0].TextContent())
	}
	if req.MaxTokens == nil || *req.MaxTokens != 256 {
		t.Errorf("max_output_tokens = %v", req.MaxTokens)
	}
}

func TestDecodeItemInput(t *testing.T) {
	req, _ := decode(t, `{
		"model": "gpt-5",
		"input": [
			{"role": "user", "content": [{"type": "input_text", "text": "weather?"}]},
			{"type": "function_call", "call_id": "call_1", "name": "get_weather", "arguments": "{\"city\":\"Paris\"}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "18C"},
			{"role": "assistant", "content": [{"type": "output_text", "text": "It is 18C."}]}
		],
		"tools": [{"type": "function", "name": "get_weather", "description": "w", "parameters": {"type": "object"}}],
		"tool_choice": {"type": "function", "name": "get_weather"},
		"reasoning": {"effort": "high", "summary": "auto"}
	}`)

	if len(req.Messages) != 4 {
		t.Fatalf("want 4 messages, got %d: %+v", len(req.Messages), req.Messages)
	}
	call := req.Messages[1].Content[0].ToolCall
	if call == nil || call.Name != "get_weather" || call.ID != "call_1" {
		t.Fatalf("function_call did not convert: %+v", req.Messages[1].Content[0])
	}
	if string(call.Arguments) != `{"city":"Paris"}` {
		t.Errorf("arguments = %s", call.Arguments)
	}
	if req.Messages[2].Role != canonical.RoleTool {
		t.Errorf("function_call_output should become a tool turn, got %q", req.Messages[2].Role)
	}
	res := req.Messages[2].Content[0].ToolResult
	if res == nil || res.ToolCallID != "call_1" || res.Content[0].Text != "18C" {
		t.Errorf("function_call_output = %+v", req.Messages[2].Content[0])
	}
	// Flat tool definitions, not nested under "function".
	if len(req.Tools) != 1 || req.Tools[0].Name != "get_weather" {
		t.Errorf("tools = %+v", req.Tools)
	}
	if req.ToolChoice == nil || req.ToolChoice.Mode != canonical.ToolChoiceSpecific ||
		req.ToolChoice.Name != "get_weather" {
		t.Errorf("tool_choice = %+v", req.ToolChoice)
	}
	if req.Reasoning == nil || req.Reasoning.Effort != canonical.EffortHigh || !req.Reasoning.Visible {
		t.Errorf("reasoning = %+v", req.Reasoning)
	}
}

func TestStatefulFieldsRoundTripOnTheResponsesRoute(t *testing.T) {
	req, d := decode(t, `{
		"model": "gpt-5",
		"input": "hi",
		"previous_response_id": "resp_abc",
		"store": true
	}`)
	if d.Lossy() {
		t.Fatalf("stateful fields were reported as lost before routing: %+v", d.All())
	}
	out, err := Codec{}.EncodeRequest(req, canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	var got responsesRequest
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("encoded request: %v", err)
	}
	if got.PreviousResponseID != "resp_abc" || got.Store == nil || !*got.Store {
		t.Errorf("stateful fields changed: previous=%q store=%v", got.PreviousResponseID, got.Store)
	}
}

func TestEncryptedReasoningHistoryRoundTrips(t *testing.T) {
	req, d := decode(t, `{
		"model":"gpt-5","input":[
			{"type":"reasoning","id":"rs_A","encrypted_content":"opaque","summary":[{"type":"summary_text","text":"R"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"A"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}
		]}`)
	out, err := Codec{}.EncodeRequest(req, d)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	if d.Lossy() {
		t.Fatalf("issued reasoning was reported as lost: %+v", d.All())
	}
	var got responsesRequest
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	var items []item
	if err := json.Unmarshal(got.Input, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) < 1 || items[0].Type != "reasoning" || items[0].ID != "rs_A" ||
		items[0].EncryptedContent != "opaque" || len(items[0].Summary) != 1 || items[0].Summary[0].Text != "R" {
		t.Fatalf("reasoning replay changed: %+v", items)
	}
}

func TestForeignEncryptedReasoningIsNotSentToResponses(t *testing.T) {
	req := &canonical.Request{Model: "gpt-5", Messages: []canonical.Message{{
		Role: canonical.RoleAssistant,
		Content: []canonical.ContentPart{{Type: canonical.PartReasoning, Text: "R",
			Reasoning: &canonical.ReasoningMeta{Provider: "anthropic", Redacted: "opaque"}}, canonical.Text("answer")},
	}}}
	d := canonical.NewDiagnostics()
	out, err := Codec{}.EncodeRequest(req, d)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Lossy() || strings.Contains(string(out), "opaque") {
		t.Fatalf("foreign encrypted reasoning must be omitted with a note: output=%s notes=%+v", out, d.All())
	}
}

func TestNativeInputItemRoundTrips(t *testing.T) {
	req, d := decode(t, `{"model":"gpt-5","input":[
		{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"q"}},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}
	]}`)
	out, err := Codec{}.EncodeRequest(req, d)
	if err != nil {
		t.Fatal(err)
	}
	if d.Lossy() {
		t.Fatalf("native item was reported as lost: %+v", d.All())
	}
	if !strings.Contains(string(out), `"type":"web_search_call"`) ||
		!strings.Contains(string(out), `"query":"q"`) {
		t.Fatalf("native item did not survive: %s", out)
	}
}

func TestRequestRoundTrip(t *testing.T) {
	original := `{
		"model": "gpt-5",
		"instructions": "Be terse.",
		"input": [
			{"role": "user", "content": [{"type": "input_text", "text": "weather?"}]},
			{"type": "function_call", "call_id": "call_1", "name": "get_weather", "arguments": "{\"city\":\"Paris\"}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "18C"}
		],
		"tools": [{"type": "function", "name": "get_weather", "parameters": {"type": "object"}}],
		"temperature": 0.2,
		"max_output_tokens": 512,
		"text": {"format": {"type": "json_object"}}
	}`
	req, d := decode(t, original)
	if d.Lossy() {
		t.Errorf("unexpected lossy notes: %+v", d.All())
	}

	out, err := Codec{}.EncodeRequest(req, canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	var got responsesRequest
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("re-encode invalid: %v", err)
	}
	if got.Instructions != "Be terse." {
		t.Errorf("instructions = %q", got.Instructions)
	}
	if got.MaxOutputTokens == nil || *got.MaxOutputTokens != 512 {
		t.Errorf("max_output_tokens lost")
	}
	if got.Text == nil || got.Text.Format == nil || got.Text.Format.Type != "json_object" {
		t.Errorf("text.format lost: %+v", got.Text)
	}
	if len(got.Tools) != 1 || got.Tools[0].Type != "function" || got.Tools[0].Name != "get_weather" {
		t.Errorf("tools must stay flat: %+v", got.Tools)
	}

	var items []item
	if err := json.Unmarshal(got.Input, &items); err != nil {
		t.Fatalf("input is not an item array: %v", err)
	}
	var kinds []string
	for _, it := range items {
		kinds = append(kinds, it.Type)
	}
	want := []string{"message", "function_call", "function_call_output"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Errorf("input items = %v, want %v", kinds, want)
	}
	if items[1].CallID != "call_1" || items[2].CallID != "call_1" {
		t.Errorf("call_id pairing broken: %+v", items)
	}
}

// TestEncodeDropsUnsupportedSamplingParams pins the honesty rule: parameters
// the Responses API has no field for must be recorded, and must not be sent
// (an unknown field would be rejected outright).
func TestEncodeDropsUnsupportedSamplingParams(t *testing.T) {
	req := &canonical.Request{
		Model:    "gpt-5",
		Messages: []canonical.Message{{Role: canonical.RoleUser, Content: []canonical.ContentPart{canonical.Text("hi")}}},
		Stop:     []string{"END"},
		Seed:     canonical.Ptr(int64(42)),
		TopK:     canonical.Ptr(40),
	}
	d := canonical.NewDiagnostics()
	out, err := Codec{}.EncodeRequest(req, d)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	for _, field := range []string{"stop", "seed", "top_k"} {
		if !strings.Contains(string(out), `"`+field+`"`) {
			continue
		}
		t.Errorf("%q must not be sent to the Responses API: %s", field, out)
	}
	fields := map[string]bool{}
	for _, n := range d.All() {
		fields[n.Field] = true
	}
	for _, field := range []string{"stop", "seed", "top_k"} {
		if !fields[field] {
			t.Errorf("dropping %q was not recorded; notes = %+v", field, d.All())
		}
	}
}

func TestDecodeResponse(t *testing.T) {
	body := `{
		"id": "resp_1", "object": "response", "created_at": 1700000000, "model": "gpt-5",
		"status": "completed",
		"output": [
			{"type": "reasoning", "id": "rs_1", "summary": [{"type": "summary_text", "text": "thinking"}]},
			{"type": "message", "id": "msg_1", "role": "assistant", "status": "completed",
			 "content": [{"type": "output_text", "text": "It is 18C."}]},
			{"type": "function_call", "id": "fc_1", "call_id": "call_9", "name": "log", "arguments": "{\"a\":1}"}
		],
		"usage": {"input_tokens": 10, "output_tokens": 5, "total_tokens": 15,
		          "output_tokens_details": {"reasoning_tokens": 3},
		          "input_tokens_details": {"cached_tokens": 2}}
	}`
	resp, err := Codec{}.DecodeResponse([]byte(body), canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if resp.ID != "resp_1" {
		t.Errorf("id = %q", resp.ID)
	}
	parts := resp.Message.Content
	if len(parts) != 3 || parts[0].Type != canonical.PartReasoning {
		t.Fatalf("content = %+v", parts)
	}
	if parts[0].Text != "thinking" || parts[0].Reasoning == nil || parts[0].Reasoning.ID != "rs_1" {
		t.Errorf("reasoning item = %+v", parts[0])
	}
	// A function_call in the output is the only signal of a tool call.
	if resp.FinishReason != canonical.FinishToolCalls {
		t.Errorf("finish = %q, want tool_calls", resp.FinishReason)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.ReasoningTokens != 3 || resp.Usage.CachedInputTokens != 2 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestNativeResponseItemRoundTrips(t *testing.T) {
	body := `{
		"id":"resp_1","object":"response","created_at":1700000000,"model":"gpt-5","status":"completed",
		"output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"q"}}]
	}`
	d := canonical.NewDiagnostics()
	resp, err := Codec{}.DecodeResponse([]byte(body), d)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Codec{}.EncodeResponse(resp, nil, d)
	if err != nil {
		t.Fatal(err)
	}
	if d.Lossy() {
		t.Fatalf("native response item was reported as lost: %+v", d.All())
	}
	for _, want := range []string{`"type":"web_search_call"`, `"query":"q"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("native response lost %q: %s", want, out)
		}
	}
}

// TestDecodeIncompleteMeansLength covers the fact that the Responses API
// reports truncation via status plus incomplete_details, not a finish_reason.
func TestDecodeIncompleteMeansLength(t *testing.T) {
	body := `{
		"id": "resp_1", "model": "gpt-5", "status": "incomplete",
		"incomplete_details": {"reason": "max_output_tokens"},
		"output": [{"type":"message","role":"assistant","content":[{"type":"output_text","text":"cut"}]}]
	}`
	resp, err := Codec{}.DecodeResponse([]byte(body), canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if resp.FinishReason != canonical.FinishLength {
		t.Errorf("finish = %q, want length", resp.FinishReason)
	}
}

// --- streaming ------------------------------------------------------------

func collect(t *testing.T, sse string) []*canonical.Event {
	t.Helper()
	var events []*canonical.Event
	err := Codec{}.DecodeStream(context.Background(), strings.NewReader(sse), func(ev *canonical.Event) error {
		cp := *ev
		events = append(events, &cp)
		return nil
	})
	if err != nil {
		t.Fatalf("DecodeStream: %v", err)
	}
	return events
}

const sampleStream = `event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","model":"gpt-5","status":"in_progress","output":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}

event: response.reasoning_summary_text.delta
data: {"type":"response.reasoning_summary_text.delta","sequence_number":2,"item_id":"rs_1","output_index":0,"summary_index":0,"delta":"pondering"}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":{"type":"reasoning","id":"rs_1"}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":4,"output_index":1,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}

event: response.content_part.added
data: {"type":"response.content_part.added","sequence_number":5,"item_id":"msg_1","output_index":1,"content_index":0,"part":{"type":"output_text","text":""}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":6,"item_id":"msg_1","output_index":1,"content_index":0,"delta":"It is "}

event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":7,"item_id":"msg_1","output_index":1,"content_index":0,"delta":"18C"}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":8,"output_index":1,"item":{"type":"message","id":"msg_1"}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":9,"output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","sequence_number":10,"item_id":"fc_1","output_index":2,"delta":"{\"ci"}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","sequence_number":11,"item_id":"fc_1","output_index":2,"delta":"ty\":\"Paris\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":12,"output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}

event: response.completed
data: {"type":"response.completed","sequence_number":13,"response":{"id":"resp_1","object":"response","model":"gpt-5","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}],"usage":{"input_tokens":12,"output_tokens":30,"total_tokens":42,"output_tokens_details":{"reasoning_tokens":8}}}}

`

func TestDecodeStream(t *testing.T) {
	acc := canonical.NewAccumulator()
	for _, ev := range collect(t, sampleStream) {
		acc.Add(ev)
	}
	resp := acc.Response()

	if resp.ID != "resp_1" {
		t.Errorf("id = %q", resp.ID)
	}
	if resp.Message.TextContent() != "It is 18C" {
		t.Errorf("text = %q", resp.Message.TextContent())
	}
	parts := resp.Message.Content
	if parts[0].Type != canonical.PartReasoning || parts[0].Text != "pondering" {
		t.Errorf("reasoning block = %+v", parts[0])
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "get_weather" {
		t.Fatalf("tool call = %+v", calls)
	}
	// The fragments must only be valid once reassembled.
	if string(calls[0].Arguments) != `{"city":"Paris"}` {
		t.Errorf("arguments = %s", calls[0].Arguments)
	}
	if resp.FinishReason != canonical.FinishToolCalls {
		t.Errorf("finish = %q", resp.FinishReason)
	}
	if resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 30 || resp.Usage.ReasoningTokens != 8 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestStreamRoundTrip(t *testing.T) {
	var buf strings.Builder
	enc := Codec{}.NewStreamEncoder(&buf, &canonical.Request{Model: "gpt-5"})
	for _, ev := range collect(t, sampleStream) {
		if err := enc.Write(ev); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"event: response.created",
		"event: response.output_item.added",
		"event: response.output_text.delta",
		"event: response.function_call_arguments.delta",
		"event: response.output_item.done",
		"event: response.completed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("re-encoded stream is missing %q:\n%s", want, out)
		}
	}

	acc := canonical.NewAccumulator()
	for _, ev := range collect(t, out) {
		acc.Add(ev)
	}
	resp := acc.Response()
	if resp.Message.TextContent() != "It is 18C" {
		t.Errorf("text = %q", resp.Message.TextContent())
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 || string(calls[0].Arguments) != `{"city":"Paris"}` {
		t.Errorf("tool call round trip failed: %+v", calls)
	}
	if resp.FinishReason != canonical.FinishToolCalls {
		t.Errorf("finish = %q", resp.FinishReason)
	}
	if resp.Usage.OutputTokens != 30 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestNativeStreamEventsRoundTrip(t *testing.T) {
	in := `event: response.created
data: {"type":"response.created","response":{"id":"r","model":"gpt-5","status":"in_progress","output":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"type":"web_search_call","id":"ws_1","status":"in_progress"}}

event: response.web_search_call.completed
data: {"type":"response.web_search_call.completed","output_index":0,"item_id":"ws_1"}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":0,"item":{"type":"web_search_call","id":"ws_1","status":"completed"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"r","model":"gpt-5","status":"completed","output":[{"type":"web_search_call","id":"ws_1","status":"completed"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}

`
	var out strings.Builder
	enc := Codec{}.NewStreamEncoder(&out, &canonical.Request{Model: "gpt-5"})
	for _, ev := range collect(t, in) {
		if err := enc.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"response.web_search_call.completed", `"type":"web_search_call"`, `"id":"ws_1"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("native stream lost %q:\n%s", want, out.String())
		}
	}
}

// TestStreamEncoderBalancesItems pins the protocol rule that every output item
// is announced and closed exactly once.
func TestStreamEncoderBalancesItems(t *testing.T) {
	var buf strings.Builder
	enc := Codec{}.NewStreamEncoder(&buf, &canonical.Request{Model: "gpt-5"})
	for _, ev := range []*canonical.Event{
		{Type: canonical.EventMessageStart, Model: "gpt-5"},
		{Type: canonical.EventTextDelta, Index: 0, Text: "hi"},
		{Type: canonical.EventToolCallStart, Index: 1, ToolCallID: "call_1", ToolName: "f"},
		{Type: canonical.EventToolCallDelta, Index: 1, ArgumentsDelta: `{"x":1}`},
		{Type: canonical.EventMessageEnd, FinishReason: canonical.FinishToolCalls},
	} {
		if err := enc.Write(ev); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	enc.Close()

	out := buf.String()
	added := strings.Count(out, `"type":"response.output_item.added"`)
	done := strings.Count(out, `"type":"response.output_item.done"`)
	if added != 2 || done != 2 {
		t.Errorf("items: %d added, %d done (want 2/2)\n%s", added, done, out)
	}
	// Sequence numbers must be strictly increasing so a client can spot a gap.
	var seqs []int
	for _, line := range strings.Split(out, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Seq int `json:"sequence_number"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			t.Fatalf("event is not valid JSON: %v (%s)", err, payload)
		}
		seqs = append(seqs, ev.Seq)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Errorf("sequence_number is not increasing: %v", seqs)
			break
		}
	}
}

func TestDecodeStreamContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Codec{}.DecodeStream(ctx, strings.NewReader(sampleStream), func(*canonical.Event) error { return nil })
	if err != context.Canceled {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestDecodeStreamError(t *testing.T) {
	sse := `event: response.created
data: {"type":"response.created","response":{"id":"r","model":"m","output":[]}}

event: error
data: {"type":"error","code":"rate_limit_exceeded","message":"slow down"}

`
	events := collect(t, sse)
	last := events[len(events)-1]
	if last.Type != canonical.EventError || last.Error.Message != "slow down" {
		t.Fatalf("want an error event, got %+v", last)
	}
}

// TestStreamEncoderClosesItemsInOrder pins a rule the protocol relies on: a
// client reads output items as a sequence, so each item's whole lifecycle
// (added → deltas → done) must finish before the next item is announced.
func TestStreamEncoderClosesItemsInOrder(t *testing.T) {
	var buf strings.Builder
	enc := Codec{}.NewStreamEncoder(&buf, &canonical.Request{Model: "gpt-5"})
	for _, ev := range []*canonical.Event{
		{Type: canonical.EventMessageStart, Model: "gpt-5"},
		{Type: canonical.EventReasoningDelta, Index: 0, Text: "thinking"},
		{Type: canonical.EventTextDelta, Index: 1, Text: "answer"},
		{Type: canonical.EventToolCallStart, Index: 2, ToolCallID: "call_1", ToolName: "f"},
		{Type: canonical.EventToolCallDelta, Index: 2, ArgumentsDelta: `{"x":1}`},
		{Type: canonical.EventToolCallEnd, Index: 2},
		{Type: canonical.EventMessageEnd, FinishReason: canonical.FinishToolCalls},
	} {
		if err := enc.Write(ev); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	enc.Close()

	// Walk the added/done pairs and require them to nest as 0,0,1,1,2,2.
	var lifecycle []string
	for _, line := range strings.Split(buf.String(), "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type        string `json:"type"`
			OutputIndex int    `json:"output_index"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			t.Fatalf("event is not valid JSON: %v (%s)", err, payload)
		}
		switch ev.Type {
		case "response.output_item.added":
			lifecycle = append(lifecycle, fmt.Sprintf("add%d", ev.OutputIndex))
		case "response.output_item.done":
			lifecycle = append(lifecycle, fmt.Sprintf("done%d", ev.OutputIndex))
		}
	}

	want := "add0,done0,add1,done1,add2,done2"
	if got := strings.Join(lifecycle, ","); got != want {
		t.Errorf("item lifecycles interleave.\ngot:  %s\nwant: %s\n\n%s", got, want, buf.String())
	}
}
