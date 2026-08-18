package openai

import (
	"context"
	"encoding/json"
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

func TestDecodeRequestBasics(t *testing.T) {
	req, _ := decode(t, `{
		"model": "gpt-4o",
		"messages": [
			{"role": "system", "content": "Be terse."},
			{"role": "user", "content": "Hi"}
		],
		"temperature": 0.5,
		"max_tokens": 100,
		"stop": "END",
		"stream": true,
		"stream_options": {"include_usage": true}
	}`)

	if req.Model != "gpt-4o" {
		t.Errorf("model = %q", req.Model)
	}
	if got := joinText(req.System); got != "Be terse." {
		t.Errorf("system = %q", got)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != canonical.RoleUser {
		t.Fatalf("messages = %+v", req.Messages)
	}
	if req.Messages[0].TextContent() != "Hi" {
		t.Errorf("user text = %q", req.Messages[0].TextContent())
	}
	if req.Temperature == nil || *req.Temperature != 0.5 {
		t.Errorf("temperature = %v", req.Temperature)
	}
	if req.MaxTokens == nil || *req.MaxTokens != 100 {
		t.Errorf("max_tokens = %v", req.MaxTokens)
	}
	if len(req.Stop) != 1 || req.Stop[0] != "END" {
		t.Errorf("stop = %v", req.Stop)
	}
	if !req.Stream || !req.IncludeUsage {
		t.Errorf("stream=%v includeUsage=%v", req.Stream, req.IncludeUsage)
	}
}

func TestDecodeRequestToolsAndResults(t *testing.T) {
	req, _ := decode(t, `{
		"model": "m",
		"messages": [
			{"role": "user", "content": "weather?"},
			{"role": "assistant", "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"Paris\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "18C"}
		],
		"tools": [{"type": "function", "function": {"name": "get_weather", "description": "w", "parameters": {"type": "object"}}}],
		"tool_choice": {"type": "function", "function": {"name": "get_weather"}}
	}`)

	if len(req.Tools) != 1 || req.Tools[0].Name != "get_weather" {
		t.Fatalf("tools = %+v", req.Tools)
	}
	if req.ToolChoice == nil || req.ToolChoice.Mode != canonical.ToolChoiceSpecific || req.ToolChoice.Name != "get_weather" {
		t.Fatalf("tool_choice = %+v", req.ToolChoice)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("want 3 messages, got %d", len(req.Messages))
	}
	call := req.Messages[1].Content[0]
	if call.Type != canonical.PartToolCall || call.ToolCall.Name != "get_weather" {
		t.Fatalf("tool call part = %+v", call)
	}
	if string(call.ToolCall.Arguments) != `{"city":"Paris"}` {
		t.Errorf("arguments = %s", call.ToolCall.Arguments)
	}
	res := req.Messages[2].Content[0]
	if res.Type != canonical.PartToolResult || res.ToolResult.ToolCallID != "call_1" {
		t.Fatalf("tool result part = %+v", res)
	}
}

// TestRequestRoundTrip is the property that matters most: a request that goes
// OpenAI -> Canonical -> OpenAI must keep its meaning.
func TestRequestRoundTrip(t *testing.T) {
	original := `{
		"model": "gpt-4o",
		"messages": [
			{"role": "system", "content": "Be terse."},
			{"role": "user", "content": "weather?"},
			{"role": "assistant", "content": "", "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"Paris\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "18C"},
			{"role": "assistant", "content": "It is 18C."}
		],
		"tools": [{"type": "function", "function": {"name": "get_weather", "parameters": {"type": "object"}}}],
		"temperature": 0.2,
		"top_p": 0.9,
		"stop": ["END"]
	}`

	req, d := decode(t, original)
	if d.Lossy() {
		t.Errorf("unexpected lossy notes: %+v", d.All())
	}
	out, err := Codec{}.EncodeRequest(req, canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}

	var got chatRequest
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("re-encode invalid: %v", err)
	}
	if got.Model != "gpt-4o" {
		t.Errorf("model = %q", got.Model)
	}
	if len(got.Messages) != 5 {
		t.Fatalf("want 5 messages, got %d: %s", len(got.Messages), out)
	}
	wantRoles := []string{"system", "user", "assistant", "tool", "assistant"}
	for i, want := range wantRoles {
		if got.Messages[i].Role != want {
			t.Errorf("messages[%d].role = %q, want %q", i, got.Messages[i].Role, want)
		}
	}
	tc := got.Messages[2].ToolCalls
	if len(tc) != 1 || tc[0].ID != "call_1" || tc[0].Function.Arguments != `{"city":"Paris"}` {
		t.Errorf("tool calls did not survive: %+v", tc)
	}
	if got.Messages[3].ToolCallID != "call_1" {
		t.Errorf("tool_call_id lost: %+v", got.Messages[3])
	}
	if len(got.Tools) != 1 || got.Tools[0].Function.Name != "get_weather" {
		t.Errorf("tools lost: %+v", got.Tools)
	}
	if got.Temperature == nil || *got.Temperature != 0.2 {
		t.Errorf("temperature lost")
	}
}

func TestCompatibleReasoningContentRoundTripsInAssistantHistory(t *testing.T) {
	req, d := decode(t, `{
		"model":"deepseek-reasoner",
		"messages":[
			{"role":"user","content":"question"},
			{"role":"assistant","reasoning_content":"private chain","content":"answer"},
			{"role":"user","content":"follow up"}
		]
	}`)
	out, err := Codec{}.EncodeRequest(req, d)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	if d.Lossy() {
		t.Fatalf("same-protocol reasoning was reported as lost: %+v", d.All())
	}
	var got chatRequest
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 3 || got.Messages[1].ReasoningContent != "private chain" {
		t.Fatalf("reasoning_content was not replayed: %+v", got.Messages)
	}
}

func TestForeignReasoningContentIsNotSentAsCompatibleReasoning(t *testing.T) {
	req := &canonical.Request{Model: "m", Messages: []canonical.Message{{
		Role: canonical.RoleAssistant,
		Content: []canonical.ContentPart{{Type: canonical.PartReasoning, Text: "secret",
			Reasoning: &canonical.ReasoningMeta{Provider: "anthropic"}}, canonical.Text("answer")},
	}}}
	d := canonical.NewDiagnostics()
	out, err := Codec{}.EncodeRequest(req, d)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Lossy() || strings.Contains(string(out), `"reasoning_content":"secret"`) {
		t.Fatalf("foreign reasoning must be omitted with a note: output=%s notes=%+v", out, d.All())
	}
}

func TestDecodeResponse(t *testing.T) {
	body := `{
		"id": "chatcmpl-1", "object": "chat.completion", "created": 1700000000, "model": "gpt-4o",
		"choices": [{"index": 0, "message": {"role": "assistant", "content": "hello", "reasoning_content": "thinking"}, "finish_reason": "stop"}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15,
		          "completion_tokens_details": {"reasoning_tokens": 3}}
	}`
	resp, err := Codec{}.DecodeResponse([]byte(body), canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if resp.ID != "chatcmpl-1" || resp.FinishReason != canonical.FinishStop {
		t.Errorf("resp = %+v", resp)
	}
	if len(resp.Message.Content) != 2 {
		t.Fatalf("want reasoning + text parts, got %+v", resp.Message.Content)
	}
	if resp.Message.Content[0].Type != canonical.PartReasoning || resp.Message.Content[0].Text != "thinking" {
		t.Errorf("reasoning part = %+v", resp.Message.Content[0])
	}
	if resp.Message.Content[0].Reasoning == nil || resp.Message.Content[0].Reasoning.Provider != "openai" {
		t.Errorf("reasoning provider metadata = %+v", resp.Message.Content[0].Reasoning)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 || resp.Usage.ReasoningTokens != 3 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestEncodeResponse(t *testing.T) {
	resp := &canonical.Response{
		ID:    "id-1",
		Model: "m",
		Message: canonical.Message{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{
			{Type: canonical.PartReasoning, Text: "hmm"},
			canonical.Text("answer"),
			{Type: canonical.PartToolCall, ToolCall: &canonical.ToolCall{
				ID: "call_9", Name: "f", Arguments: json.RawMessage(`{"a":1}`)}},
		}},
		FinishReason: canonical.FinishToolCalls,
		Usage:        canonical.Usage{InputTokens: 3, OutputTokens: 4},
	}
	b, err := Codec{}.EncodeResponse(resp, nil, canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	var got chatResponse
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got.Object != "chat.completion" || got.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("got = %+v", got)
	}
	if got.Choices[0].Message.ReasoningContent != "hmm" {
		t.Errorf("reasoning lost")
	}
	var content string
	json.Unmarshal(got.Choices[0].Message.Content, &content)
	if content != "answer" {
		t.Errorf("content = %q", content)
	}
	if got.Usage.TotalTokens != 7 {
		t.Errorf("usage total = %d", got.Usage.TotalTokens)
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

func TestDecodeStreamText(t *testing.T) {
	sse := `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}

data: {"id":"c1","choices":[{"index":0,"delta":{"content":"Hel"}}]}

data: {"id":"c1","choices":[{"index":0,"delta":{"content":"lo"}}]}

data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"c1","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}

data: [DONE]

`
	events := collect(t, sse)

	acc := canonical.NewAccumulator()
	for _, ev := range events {
		acc.Add(ev)
	}
	resp := acc.Response()
	if resp.Message.TextContent() != "Hello" {
		t.Errorf("text = %q", resp.Message.TextContent())
	}
	if resp.FinishReason != canonical.FinishStop {
		t.Errorf("finish = %q", resp.FinishReason)
	}
	if resp.Usage.InputTokens != 2 || resp.Usage.OutputTokens != 3 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if events[0].Type != canonical.EventMessageStart {
		t.Errorf("first event = %s", events[0].Type)
	}
	if events[len(events)-1].Type != canonical.EventMessageEnd {
		t.Errorf("last event = %s", events[len(events)-1].Type)
	}
}

// TestDecodeStreamToolCallFragments checks the rule that matters for tool
// calling: argument deltas are raw fragments and must never be parsed early.
func TestDecodeStreamToolCallFragments(t *testing.T) {
	sse := `data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant"}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"ci"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ty\":\"Par"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"is\"}"}}]}}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`
	acc := canonical.NewAccumulator()
	for _, ev := range collect(t, sse) {
		acc.Add(ev)
	}
	resp := acc.Response()
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("want 1 tool call, got %d", len(calls))
	}
	if calls[0].Name != "get_weather" || calls[0].ID != "call_1" {
		t.Errorf("call = %+v", calls[0])
	}
	if string(calls[0].Arguments) != `{"city":"Paris"}` {
		t.Errorf("arguments = %s", calls[0].Arguments)
	}
	var parsed map[string]string
	if err := json.Unmarshal(calls[0].Arguments, &parsed); err != nil {
		t.Fatalf("accumulated arguments are not valid JSON: %v", err)
	}
	if resp.FinishReason != canonical.FinishToolCalls {
		t.Errorf("finish = %q", resp.FinishReason)
	}
}

func TestDecodeStreamReasoningOrdering(t *testing.T) {
	sse := `data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"reasoning_content":"think"}}]}

data: {"choices":[{"index":0,"delta":{"content":"answer"}}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`
	acc := canonical.NewAccumulator()
	for _, ev := range collect(t, sse) {
		acc.Add(ev)
	}
	parts := acc.Response().Message.Content
	if len(parts) != 2 {
		t.Fatalf("want 2 parts, got %+v", parts)
	}
	if parts[0].Type != canonical.PartReasoning || parts[1].Type != canonical.PartText {
		t.Errorf("reasoning must stay before text, got %+v", parts)
	}
}

// TestStreamRoundTrip runs canonical events back out as OpenAI SSE and re-reads
// them, which is exactly what a cross-protocol stream conversion does.
func TestStreamRoundTrip(t *testing.T) {
	in := `data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"reasoning_content":"why"}}]}

data: {"choices":[{"index":0,"delta":{"content":"hi"}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":"{\"x\":"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2}}

data: [DONE]

`
	var buf strings.Builder
	enc := Codec{}.NewStreamEncoder(&buf, &canonical.Request{Model: "m", IncludeUsage: true})
	for _, ev := range collect(t, in) {
		if err := enc.Write(ev); err != nil {
			t.Fatalf("encode event: %v", err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !strings.Contains(buf.String(), "data: [DONE]") {
		t.Errorf("stream not terminated:\n%s", buf.String())
	}

	acc := canonical.NewAccumulator()
	for _, ev := range collect(t, buf.String()) {
		acc.Add(ev)
	}
	resp := acc.Response()
	if resp.Message.TextContent() != "hi" {
		t.Errorf("text = %q", resp.Message.TextContent())
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 || string(calls[0].Arguments) != `{"x":1}` {
		t.Errorf("tool call round trip failed: %+v", calls)
	}
	if resp.FinishReason != canonical.FinishToolCalls {
		t.Errorf("finish = %q", resp.FinishReason)
	}
	if resp.Usage.InputTokens != 1 || resp.Usage.OutputTokens != 2 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	parts := resp.Message.Content
	if parts[0].Type != canonical.PartReasoning || parts[0].Text != "why" {
		t.Errorf("reasoning lost in round trip: %+v", parts)
	}
}

func TestDecodeStreamClientCancel(t *testing.T) {
	sse := `data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"a"}}]}

data: {"choices":[{"index":0,"delta":{"content":"b"}}]}

data: [DONE]

`
	stop := canonical.Errorf(canonical.ErrInternal, "client gone")
	n := 0
	err := Codec{}.DecodeStream(context.Background(), strings.NewReader(sse), func(ev *canonical.Event) error {
		n++
		if n == 2 {
			return stop
		}
		return nil
	})
	if err != stop {
		t.Fatalf("emit error must abort the read, got %v", err)
	}
}

func TestDecodeStreamContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Codec{}.DecodeStream(ctx, strings.NewReader("data: {}\n\n"), func(*canonical.Event) error { return nil })
	if err != context.Canceled {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

// Images used to be dropped here with a note. They are converted now, so the
// note must be gone — a "we lost your image" warning on a request that carried
// the image through is worse than no note at all.
func TestImagesAreConvertedNotReported(t *testing.T) {
	d := canonical.NewDiagnostics()
	req, err := Codec{}.DecodeRequest([]byte(`{
		"model": "m",
		"messages": [{"role": "user", "content": [
			{"type": "text", "text": "look"},
			{"type": "image_url", "image_url": {"url": "http://x/y.png"}}
		]}]
	}`), d)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	for _, n := range d.All() {
		if n.Fidelity == canonical.FidelityUnsupported {
			t.Errorf("an image that converts cleanly was reported as unsupported: %+v", n)
		}
	}
	var img *canonical.Media
	for _, p := range req.Messages[0].Content {
		if p.Type == canonical.PartImage {
			img = p.Media
		}
	}
	if img == nil {
		t.Fatal("the image part was not decoded")
	}
	if img.URL != "http://x/y.png" {
		t.Errorf("image url = %q", img.URL)
	}
}

// Audio is still not converted, and that must still be said out loud.
func TestUnsupportedContentIsRecorded(t *testing.T) {
	d := canonical.NewDiagnostics()
	_, err := Codec{}.DecodeRequest([]byte(`{
		"model": "m",
		"messages": [{"role": "user", "content": [
			{"type": "text", "text": "listen"},
			{"type": "input_audio", "input_audio": {"data": "AAA", "format": "wav"}}
		]}]
	}`), d)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if !d.Lossy() {
		t.Fatal("dropping audio must be recorded, not silent")
	}
	found := false
	for _, n := range d.All() {
		if n.Fidelity == canonical.FidelityUnsupported && strings.Contains(n.Detail, "udio") {
			found = true
		}
	}
	if !found {
		t.Errorf("notes = %+v", d.All())
	}
}
