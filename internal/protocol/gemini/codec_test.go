package gemini

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

func TestDecodeRequest(t *testing.T) {
	req, _ := decode(t, `{
		"systemInstruction": {"parts": [{"text": "Be terse."}]},
		"contents": [
			{"role": "user", "parts": [{"text": "Hi"}]},
			{"role": "model", "parts": [{"text": "thinking", "thought": true}, {"text": "Hello"}]}
		],
		"generationConfig": {
			"temperature": 0.4, "topK": 40, "maxOutputTokens": 512,
			"stopSequences": ["END"],
			"responseMimeType": "application/json",
			"thinkingConfig": {"thinkingBudget": 4096, "includeThoughts": true}
		}
	}`)

	if joinText(req.System) != "Be terse." {
		t.Errorf("system = %q", joinText(req.System))
	}
	if req.Temperature == nil || *req.Temperature != 0.4 {
		t.Errorf("temperature lost")
	}
	if req.MaxTokens == nil || *req.MaxTokens != 512 {
		t.Errorf("maxOutputTokens lost")
	}
	if req.ResponseFormat == nil || req.ResponseFormat.Type != canonical.FormatJSONObject {
		t.Errorf("response format = %+v", req.ResponseFormat)
	}
	if req.Reasoning == nil || *req.Reasoning.BudgetTokens != 4096 || !req.Reasoning.Visible {
		t.Errorf("thinking config = %+v", req.Reasoning)
	}
	parts := req.Messages[1].Content
	if len(parts) != 2 || parts[0].Type != canonical.PartReasoning || parts[1].Type != canonical.PartText {
		t.Errorf("model turn parts = %+v", parts)
	}
}

// TestFunctionCallResponsePairing covers Gemini's biggest structural
// difference: results are matched by function name, not by call id.
func TestFunctionCallResponsePairing(t *testing.T) {
	req, _ := decode(t, `{
		"contents": [
			{"role": "user", "parts": [{"text": "weather?"}]},
			{"role": "model", "parts": [{"functionCall": {"name": "get_weather", "args": {"city": "Paris"}}}]},
			{"role": "user", "parts": [{"functionResponse": {"name": "get_weather", "response": {"result": "18C"}}}]}
		]
	}`)

	call := req.Messages[1].Content[0].ToolCall
	if call == nil {
		t.Fatalf("no tool call: %+v", req.Messages[1])
	}
	if req.Messages[2].Role != canonical.RoleTool {
		t.Errorf("function response turn role = %q", req.Messages[2].Role)
	}
	result := req.Messages[2].Content[0].ToolResult
	if result == nil {
		t.Fatalf("no tool result")
	}
	// Without matching ids, converting to OpenAI would produce an orphaned
	// tool message.
	if call.ID != result.ToolCallID {
		t.Errorf("synthesised ids do not pair: call %q vs result %q", call.ID, result.ToolCallID)
	}
	if result.Content[0].Text != "18C" {
		t.Errorf("unwrapped result = %q", result.Content[0].Text)
	}
}

func TestEncodeResolvesFunctionNameFromCallID(t *testing.T) {
	// This is what an OpenAI-shaped conversation looks like once decoded:
	// the tool result knows only the call id.
	req := &canonical.Request{
		Model: "gemini-2.0-flash",
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{canonical.Text("weather?")}},
			{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{{
				Type:     canonical.PartToolCall,
				ToolCall: &canonical.ToolCall{ID: "call_abc", Name: "get_weather", Arguments: json.RawMessage(`{"city":"Paris"}`)},
			}}},
			{Role: canonical.RoleTool, Content: []canonical.ContentPart{{
				Type: canonical.PartToolResult,
				ToolResult: &canonical.ToolResult{
					ToolCallID: "call_abc",
					Content:    []canonical.ContentPart{canonical.Text("18C")},
				},
			}}},
		},
	}
	out, err := Codec{}.EncodeRequest(req, canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	var got generateRequest
	json.Unmarshal(out, &got)

	if len(got.Contents) != 3 {
		t.Fatalf("contents = %d: %s", len(got.Contents), out)
	}
	fr := got.Contents[2].Parts[0].FunctionResponse
	if fr == nil {
		t.Fatalf("no functionResponse: %s", out)
	}
	if fr.Name != "get_weather" {
		t.Errorf("function name was not recovered from the call id: %q", fr.Name)
	}
}

func TestEncodeMergesConsecutiveRoles(t *testing.T) {
	req := &canonical.Request{
		Model: "m",
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{canonical.Text("a")}},
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{canonical.Text("b")}},
			{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{canonical.Text("c")}},
		},
	}
	out, _ := Codec{}.EncodeRequest(req, canonical.NewDiagnostics())
	var got generateRequest
	json.Unmarshal(out, &got)
	if len(got.Contents) != 2 {
		t.Fatalf("want 2 merged contents, got %d: %s", len(got.Contents), out)
	}
	if len(got.Contents[0].Parts) != 2 || got.Contents[0].Role != "user" {
		t.Errorf("first content = %+v", got.Contents[0])
	}
	if got.Contents[1].Role != "model" {
		t.Errorf("assistant must map to role 'model', got %q", got.Contents[1].Role)
	}
}

// TestDecodeResponseInfersToolFinish covers Gemini reporting STOP even when
// the model actually called a tool.
func TestDecodeResponseInfersToolFinish(t *testing.T) {
	body := `{
		"candidates":[{"content":{"role":"model","parts":[
			{"functionCall":{"name":"get_weather","args":{"city":"Paris"}}}
		]},"finishReason":"STOP","index":0}],
		"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":7},
		"modelVersion":"gemini-2.0-flash"
	}`
	resp, err := Codec{}.DecodeResponse([]byte(body), canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if resp.FinishReason != canonical.FinishToolCalls {
		t.Errorf("finish = %q; a STOP with a functionCall means tool_calls", resp.FinishReason)
	}
	// Gemini reports thought tokens separately; the canonical output count
	// includes them, as the other protocols do.
	if resp.Usage.OutputTokens != 12 || resp.Usage.ReasoningTokens != 7 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestRequestRoundTrip(t *testing.T) {
	original := `{
		"systemInstruction":{"parts":[{"text":"Be terse."}]},
		"contents":[
			{"role":"user","parts":[{"text":"weather?"}]},
			{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Paris"}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"get_weather","response":{"result":"18C"}}}]}
		],
		"tools":[{"functionDeclarations":[{"name":"get_weather","description":"w","parameters":{"type":"object"}}]}],
		"toolConfig":{"functionCallingConfig":{"mode":"AUTO"}},
		"generationConfig":{"temperature":0.2,"maxOutputTokens":100}
	}`
	req, _ := decode(t, original)
	req.Model = "gemini-2.0-flash"

	out, err := Codec{}.EncodeRequest(req, canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	var got generateRequest
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("re-encode invalid: %v", err)
	}
	if got.SystemInstruction == nil || got.SystemInstruction.Parts[0].Text != "Be terse." {
		t.Errorf("system instruction lost: %s", out)
	}
	if len(got.Contents) != 3 {
		t.Fatalf("contents = %d", len(got.Contents))
	}
	if got.Contents[1].Parts[0].FunctionCall.Name != "get_weather" {
		t.Errorf("function call lost")
	}
	if got.Contents[2].Parts[0].FunctionResponse.Name != "get_weather" {
		t.Errorf("function response lost")
	}
	if len(got.Tools) != 1 || got.Tools[0].FunctionDeclarations[0].Name != "get_weather" {
		t.Errorf("tools lost: %+v", got.Tools)
	}
	if got.GenerationConfig == nil || *got.GenerationConfig.MaxOutputTokens != 100 {
		t.Errorf("generation config lost")
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

const sampleStream = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"reasoning","thought":true}]},"index":0}],"modelVersion":"gemini-2.0-flash","responseId":"r1"}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"It is "}]},"index":0}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"18C"}]},"index":0}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Paris"}}}]},"index":0}]}

data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":14,"totalTokenCount":23}}

`

func TestDecodeStream(t *testing.T) {
	acc := canonical.NewAccumulator()
	for _, ev := range collect(t, sampleStream) {
		acc.Add(ev)
	}
	resp := acc.Response()

	if resp.Message.TextContent() != "It is 18C" {
		t.Errorf("text = %q", resp.Message.TextContent())
	}
	if resp.Message.Content[0].Type != canonical.PartReasoning {
		t.Errorf("thought must come first: %+v", resp.Message.Content)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "get_weather" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if string(calls[0].Arguments) != `{"city":"Paris"}` {
		t.Errorf("arguments = %s", calls[0].Arguments)
	}
	if resp.FinishReason != canonical.FinishToolCalls {
		t.Errorf("finish = %q", resp.FinishReason)
	}
	if resp.Usage.InputTokens != 9 || resp.Usage.OutputTokens != 14 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

// TestStreamEncoderBuffersToolArguments checks that fragmented arguments are
// only emitted once complete: Gemini cannot accept a partial functionCall.
func TestStreamEncoderBuffersToolArguments(t *testing.T) {
	var buf strings.Builder
	enc := Codec{}.NewStreamEncoder(&buf, &canonical.Request{Model: "gemini-2.0-flash"})

	events := []*canonical.Event{
		{Type: canonical.EventMessageStart, Model: "gemini-2.0-flash"},
		{Type: canonical.EventToolCallStart, Index: 0, ToolCallID: "call_1", ToolName: "get_weather"},
		{Type: canonical.EventToolCallDelta, Index: 0, ArgumentsDelta: `{"ci`},
		{Type: canonical.EventToolCallDelta, Index: 0, ArgumentsDelta: `ty":"Paris"}`},
		{Type: canonical.EventToolCallEnd, Index: 0},
		{Type: canonical.EventMessageEnd, FinishReason: canonical.FinishToolCalls,
			Usage: &canonical.Usage{InputTokens: 1, OutputTokens: 2}},
	}
	for _, ev := range events {
		if err := enc.Write(ev); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	enc.Close()

	out := buf.String()
	if strings.Contains(out, `{"ci`) && !strings.Contains(out, `"city":"Paris"`) {
		t.Fatalf("a partial argument fragment was emitted:\n%s", out)
	}

	// Every emitted chunk must be valid JSON on its own.
	found := false
	for _, line := range strings.Split(out, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var chunk generateResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("chunk is not valid JSON: %v (%s)", err, payload)
		}
		for _, c := range chunk.Candidates {
			if c.Content == nil {
				continue
			}
			for _, p := range c.Content.Parts {
				if p.FunctionCall != nil {
					found = true
					if string(p.FunctionCall.Args) != `{"city":"Paris"}` {
						t.Errorf("buffered args = %s", p.FunctionCall.Args)
					}
				}
			}
		}
	}
	if !found {
		t.Errorf("no functionCall was emitted:\n%s", out)
	}
}

func TestStreamRoundTrip(t *testing.T) {
	var buf strings.Builder
	enc := Codec{}.NewStreamEncoder(&buf, &canonical.Request{Model: "gemini-2.0-flash"})
	for _, ev := range collect(t, sampleStream) {
		if err := enc.Write(ev); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	enc.Close()

	acc := canonical.NewAccumulator()
	for _, ev := range collect(t, buf.String()) {
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
	if resp.Usage.InputTokens != 9 {
		t.Errorf("usage = %+v", resp.Usage)
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
	sse := `data: {"error":{"code":429,"message":"Quota exceeded","status":"RESOURCE_EXHAUSTED"}}

`
	events := collect(t, sse)
	if len(events) == 0 {
		t.Fatal("no events")
	}
	last := events[len(events)-1]
	if last.Type != canonical.EventError || last.Error.Type != canonical.ErrRateLimit {
		t.Fatalf("want a rate limit error event, got %+v", last)
	}
}

// TestFunctionCallSignatureSurvivesRoundTrip pins Gemini 3's hard requirement:
// a functionCall part carries a thoughtSignature, and that signature must be
// replayed verbatim when the conversation is sent back. Dropping it makes the
// upstream reject the whole request with 400.
func TestFunctionCallSignatureSurvivesRoundTrip(t *testing.T) {
	in := []byte(`{
		"contents":[
			{"role":"user","parts":[{"text":"weather?"}]},
			{"role":"model","parts":[
				{"functionCall":{"name":"get_weather","args":{"city":"Paris"}},
				 "thoughtSignature":"EroDCsYDAdHtim-SIG"}
			]},
			{"role":"user","parts":[
				{"functionResponse":{"name":"get_weather","response":{"result":"sunny"}}}
			]}
		]
	}`)

	var d canonical.Diagnostics
	req, err := Codec{}.DecodeRequest(in, &d)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	out, err := Codec{}.EncodeRequest(req, &d)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(out), "EroDCsYDAdHtim-SIG") {
		t.Fatalf("thoughtSignature was dropped from the functionCall part:\n%s", out)
	}
}
