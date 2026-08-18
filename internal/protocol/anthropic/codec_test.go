package anthropic

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
		"model": "claude-sonnet-4",
		"max_tokens": 1024,
		"system": "Be terse.",
		"messages": [
			{"role": "user", "content": "Hi"},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "hmm", "signature": "sig123"},
				{"type": "text", "text": "Hello"}
			]}
		],
		"temperature": 0.3,
		"top_k": 20,
		"stop_sequences": ["END"],
		"thinking": {"type": "enabled", "budget_tokens": 2048}
	}`)

	if req.Model != "claude-sonnet-4" || *req.MaxTokens != 1024 {
		t.Errorf("req = %+v", req)
	}
	if joinText(req.System) != "Be terse." {
		t.Errorf("system = %q", joinText(req.System))
	}
	if req.TopK == nil || *req.TopK != 20 {
		t.Errorf("top_k lost")
	}
	if req.Reasoning == nil || req.Reasoning.BudgetTokens == nil || *req.Reasoning.BudgetTokens != 2048 {
		t.Errorf("thinking = %+v", req.Reasoning)
	}
	parts := req.Messages[1].Content
	if len(parts) != 2 || parts[0].Type != canonical.PartReasoning {
		t.Fatalf("assistant parts = %+v", parts)
	}
	if parts[0].Reasoning == nil || parts[0].Reasoning.Signature != "sig123" {
		t.Errorf("thinking signature lost: %+v", parts[0])
	}
}

func TestDecodeToolResultBecomesToolTurn(t *testing.T) {
	req, _ := decode(t, `{
		"model": "m", "max_tokens": 100,
		"messages": [
			{"role": "user", "content": "weather?"},
			{"role": "assistant", "content": [{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Paris"}}]},
			{"role": "user", "content": [{"type":"tool_result","tool_use_id":"toolu_1","content":"18C"}]}
		]
	}`)

	if req.Messages[2].Role != canonical.RoleTool {
		t.Errorf("a user turn of pure tool results must become a tool turn, got %q", req.Messages[2].Role)
	}
	tr := req.Messages[2].Content[0].ToolResult
	if tr == nil || tr.ToolCallID != "toolu_1" {
		t.Fatalf("tool result = %+v", req.Messages[2].Content[0])
	}
	tc := req.Messages[1].Content[0].ToolCall
	if tc == nil || tc.Name != "get_weather" || string(tc.Arguments) != `{"city":"Paris"}` {
		t.Errorf("tool use = %+v", req.Messages[1].Content[0])
	}
}

func TestRequestRoundTrip(t *testing.T) {
	original := `{
		"model": "claude-sonnet-4",
		"max_tokens": 512,
		"system": "Be terse.",
		"messages": [
			{"role": "user", "content": "weather?"},
			{"role": "assistant", "content": [{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Paris"}}]},
			{"role": "user", "content": [{"type":"tool_result","tool_use_id":"toolu_1","content":"18C"}]},
			{"role": "assistant", "content": "It is 18C."}
		],
		"tools": [{"name":"get_weather","description":"w","input_schema":{"type":"object"}}],
		"temperature": 0.2
	}`

	req, d := decode(t, original)
	if d.Lossy() {
		t.Errorf("unexpected lossy notes: %+v", d.All())
	}
	out, err := Codec{}.EncodeRequest(req, canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}

	var got messagesRequest
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("re-encode invalid: %v", err)
	}
	if got.MaxTokens != 512 {
		t.Errorf("max_tokens = %d", got.MaxTokens)
	}
	if len(got.Messages) != 4 {
		t.Fatalf("want 4 messages, got %d: %s", len(got.Messages), out)
	}
	for i, want := range []string{"user", "assistant", "user", "assistant"} {
		if got.Messages[i].Role != want {
			t.Errorf("messages[%d].role = %q, want %q", i, got.Messages[i].Role, want)
		}
	}
	var blocks []block
	json.Unmarshal(got.Messages[1].Content, &blocks)
	if len(blocks) != 1 || blocks[0].Type != "tool_use" || blocks[0].ID != "toolu_1" {
		t.Errorf("tool_use lost: %+v", blocks)
	}
	json.Unmarshal(got.Messages[2].Content, &blocks)
	if len(blocks) != 1 || blocks[0].Type != "tool_result" || blocks[0].ToolUseID != "toolu_1" {
		t.Errorf("tool_result lost: %+v", blocks)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "get_weather" {
		t.Errorf("tools lost: %+v", got.Tools)
	}
}

func TestCompactionBlockRoundTrips(t *testing.T) {
	req, d := decode(t, `{
		"model":"claude-sonnet-4","max_tokens":128,
		"messages":[
			{"role":"assistant","content":[{"type":"compaction","content":"opaque compacted context"}]},
			{"role":"user","content":"continue"}
		]
	}`)
	out, err := Codec{}.EncodeRequest(req, d)
	if err != nil {
		t.Fatal(err)
	}
	if d.Lossy() {
		t.Fatalf("same-protocol compaction was reported as lost: %+v", d.All())
	}
	if !strings.Contains(string(out), `"type":"compaction"`) ||
		!strings.Contains(string(out), `"content":"opaque compacted context"`) {
		t.Fatalf("compaction block did not survive: %s", out)
	}
}

// TestEncodeMergesConsecutiveSameRole covers the constraint that makes
// Anthropic different: no two consecutive turns may share a role.
func TestEncodeMergesConsecutiveSameRole(t *testing.T) {
	req := &canonical.Request{
		Model: "m",
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{canonical.Text("one")}},
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{canonical.Text("two")}},
			{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{canonical.Text("ok")}},
			{Role: canonical.RoleTool, Content: []canonical.ContentPart{{
				Type:       canonical.PartToolResult,
				ToolResult: &canonical.ToolResult{ToolCallID: "t1", Content: []canonical.ContentPart{canonical.Text("res")}},
			}}},
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{canonical.Text("and now")}},
		},
	}
	out, err := Codec{}.EncodeRequest(req, canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	var got messagesRequest
	json.Unmarshal(out, &got)

	wantRoles := []string{"user", "assistant", "user"}
	if len(got.Messages) != len(wantRoles) {
		t.Fatalf("want %d merged messages, got %d: %s", len(wantRoles), len(got.Messages), out)
	}
	for i, want := range wantRoles {
		if got.Messages[i].Role != want {
			t.Errorf("messages[%d].role = %q, want %q", i, got.Messages[i].Role, want)
		}
	}
	var blocks []block
	json.Unmarshal(got.Messages[0].Content, &blocks)
	if len(blocks) != 2 {
		t.Errorf("consecutive user turns should merge into 2 blocks, got %+v", blocks)
	}
	// The tool result must sit in the same user turn as the following text.
	json.Unmarshal(got.Messages[2].Content, &blocks)
	if len(blocks) != 2 || blocks[0].Type != "tool_result" {
		t.Errorf("tool result did not merge into the user turn: %+v", blocks)
	}
}

func TestEncodeSuppliesMaxTokens(t *testing.T) {
	req := &canonical.Request{
		Model:    "m",
		Messages: []canonical.Message{{Role: canonical.RoleUser, Content: []canonical.ContentPart{canonical.Text("hi")}}},
	}
	d := canonical.NewDiagnostics()
	out, err := Codec{}.EncodeRequest(req, d)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	var got messagesRequest
	json.Unmarshal(out, &got)
	if got.MaxTokens != defaultMaxTokens {
		t.Errorf("max_tokens = %d, want the default %d", got.MaxTokens, defaultMaxTokens)
	}
	// Inventing a required value must be recorded, not silent.
	found := false
	for _, n := range d.All() {
		if n.Field == "max_tokens" {
			found = true
		}
	}
	if !found {
		t.Errorf("supplying max_tokens must produce a conversion note, notes = %+v", d.All())
	}
}

func TestEncodeCannotRaiseMaxTokensPastAnAPIKeyPolicy(t *testing.T) {
	req := &canonical.Request{
		Model: "m", Messages: []canonical.Message{{Role: canonical.RoleUser, Content: []canonical.ContentPart{canonical.Text("hi")}}},
		MaxTokens: canonical.Ptr(64), PolicyMaxTokens: canonical.Ptr(128),
		Reasoning: &canonical.ReasoningConfig{Enabled: true, BudgetTokens: canonical.Ptr(1024)},
	}
	if _, err := (Codec{}).EncodeRequest(req, canonical.NewDiagnostics()); err == nil {
		t.Fatal("thinking budget larger than the API key output policy was accepted")
	} else if cerr, ok := err.(*canonical.Error); !ok || cerr.Code != "max_output_tokens_exceeded" {
		t.Fatalf("error = %#v, want max_output_tokens_exceeded", err)
	}
}

func TestEncodeResponseFormatIsRecordedAsLossy(t *testing.T) {
	req := &canonical.Request{
		Model:          "m",
		Messages:       []canonical.Message{{Role: canonical.RoleUser, Content: []canonical.ContentPart{canonical.Text("hi")}}},
		ResponseFormat: &canonical.ResponseFormat{Type: canonical.FormatJSONObject},
	}
	d := canonical.NewDiagnostics()
	out, err := Codec{}.EncodeRequest(req, d)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	if !d.Lossy() {
		t.Errorf("response_format has no Anthropic equivalent and must be flagged lossy")
	}
	if !strings.Contains(string(out), "JSON") {
		t.Errorf("the JSON requirement was neither honoured nor mentioned: %s", out)
	}
}

func TestDecodeResponse(t *testing.T) {
	body := `{
		"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4",
		"content":[
			{"type":"thinking","thinking":"let me see","signature":"sig"},
			{"type":"text","text":"18C"},
			{"type":"tool_use","id":"toolu_9","name":"f","input":{"a":1}}
		],
		"stop_reason":"tool_use",
		"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":2}
	}`
	resp, err := Codec{}.DecodeResponse([]byte(body), canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if resp.FinishReason != canonical.FinishToolCalls {
		t.Errorf("finish = %q", resp.FinishReason)
	}
	if len(resp.Message.Content) != 3 {
		t.Fatalf("content = %+v", resp.Message.Content)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 || string(calls[0].Arguments) != `{"a":1}` {
		t.Errorf("tool calls = %+v", calls)
	}
	// Anthropic's input_tokens counts only what the cache did not serve, so
	// the prompt the model actually read is 10 + 2. Canonical holds that
	// total, with the cached part called out inside it — the shape every other
	// protocol already uses, and the only one that converts without lying.
	if resp.Usage.InputTokens != 12 || resp.Usage.CachedInputTokens != 2 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestNativeResponseBlockRoundTrips(t *testing.T) {
	body := `{
		"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4",
		"content":[{"type":"compaction","content":"opaque compacted context"}],
		"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}
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
		t.Fatalf("native response block was reported as lost: %+v", d.All())
	}
	if !strings.Contains(string(out), `"type":"compaction"`) ||
		!strings.Contains(string(out), `"content":"opaque compacted context"`) {
		t.Fatalf("native response block did not survive: %s", out)
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

const sampleStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4","content":[],"usage":{"input_tokens":12,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"pondering"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sigABC"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"It is "}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"18C"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"ci"}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"ty\":\"Paris\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":30}}

event: message_stop
data: {"type":"message_stop"}

`

func TestDecodeStream(t *testing.T) {
	acc := canonical.NewAccumulator()
	for _, ev := range collect(t, sampleStream) {
		acc.Add(ev)
	}
	resp := acc.Response()

	if resp.ID != "msg_1" {
		t.Errorf("id = %q", resp.ID)
	}
	if resp.Message.TextContent() != "It is 18C" {
		t.Errorf("text = %q", resp.Message.TextContent())
	}
	parts := resp.Message.Content
	if parts[0].Type != canonical.PartReasoning || parts[0].Text != "pondering" {
		t.Errorf("thinking block = %+v", parts[0])
	}
	if parts[0].Reasoning == nil || parts[0].Reasoning.Signature != "sigABC" {
		t.Errorf("signature lost: %+v", parts[0].Reasoning)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 || string(calls[0].Arguments) != `{"city":"Paris"}` {
		t.Fatalf("tool call = %+v", calls)
	}
	if resp.FinishReason != canonical.FinishToolCalls {
		t.Errorf("finish = %q", resp.FinishReason)
	}
	if resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 30 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestStreamRoundTrip(t *testing.T) {
	var buf strings.Builder
	enc := Codec{}.NewStreamEncoder(&buf, &canonical.Request{Model: "claude-sonnet-4"})
	for _, ev := range collect(t, sampleStream) {
		if err := enc.Write(ev); err != nil {
			t.Fatalf("write event: %v", err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"event: message_start", "event: content_block_start",
		"event: content_block_delta", "event: content_block_stop", "event: message_delta", "event: message_stop"} {
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

func TestNativeCompactionStreamRoundTrips(t *testing.T) {
	in := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"compaction","content":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"compaction_delta","content":"summary"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`
	var out strings.Builder
	enc := Codec{}.NewStreamEncoder(&out, &canonical.Request{Model: "claude-sonnet-4"})
	for _, ev := range collect(t, in) {
		if err := enc.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"type":"compaction"`, `"type":"compaction_delta"`, `"content":"summary"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("native stream lost %q:\n%s", want, out.String())
		}
	}
}

// TestStreamEncoderClosesBlocks guards Anthropic's rule that exactly one
// content block is open at a time and every one is closed.
func TestStreamEncoderClosesBlocks(t *testing.T) {
	var buf strings.Builder
	enc := Codec{}.NewStreamEncoder(&buf, &canonical.Request{Model: "m"})
	events := []*canonical.Event{
		{Type: canonical.EventMessageStart, Model: "m"},
		{Type: canonical.EventTextDelta, Index: 0, Text: "a"},
		{Type: canonical.EventToolCallStart, Index: 1, ToolCallID: "t1", ToolName: "f"},
		{Type: canonical.EventToolCallDelta, Index: 1, ArgumentsDelta: `{"x":1}`},
		{Type: canonical.EventMessageEnd, FinishReason: canonical.FinishToolCalls},
	}
	for _, ev := range events {
		if err := enc.Write(ev); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	enc.Close()

	out := buf.String()
	starts := strings.Count(out, `"type":"content_block_start"`)
	stops := strings.Count(out, `"type":"content_block_stop"`)
	if starts != 2 || stops != 2 {
		t.Errorf("blocks: %d starts, %d stops (want 2/2)\n%s", starts, stops, out)
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

func TestDecodeStreamUpstreamError(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"m","model":"x","content":[]}}

event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}

`
	events := collect(t, sse)
	last := events[len(events)-1]
	if last.Type != canonical.EventError || last.Error.Type != canonical.ErrOverloaded {
		t.Fatalf("want an overloaded error event, got %+v", last)
	}
}
