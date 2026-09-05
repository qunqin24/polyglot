package protocol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/protocol"
	"testing"
)

// Independent validation: every upstream wire format -> every client format,
// with interleaved parallel tool calls and nonzero cache/thinking usage.
func TestRegressionParentCrossProtocolParallelUsage(t *testing.T) {
	usage := &canonical.Usage{InputTokens: 100, OutputTokens: 42, ReasoningTokens: 22, CachedInputTokens: 80}
	events := []*canonical.Event{
		{Type: canonical.EventMessageStart, ID: "test", Model: "test-model"},
		{Type: canonical.EventTextDelta, Index: 0, Text: "checking"},
		{Type: canonical.EventToolCallStart, Index: 1, ToolCallID: "call_a", ToolName: "alpha"},
		{Type: canonical.EventToolCallDelta, Index: 1, ArgumentsDelta: `{"a":`},
		{Type: canonical.EventToolCallStart, Index: 2, ToolCallID: "call_b", ToolName: "beta"},
		{Type: canonical.EventToolCallDelta, Index: 2, ArgumentsDelta: `{"b":`},
		{Type: canonical.EventToolCallDelta, Index: 1, ArgumentsDelta: `1}`},
		{Type: canonical.EventToolCallEnd, Index: 1},
		{Type: canonical.EventToolCallDelta, Index: 2, ArgumentsDelta: `2}`},
		{Type: canonical.EventToolCallEnd, Index: 2},
		{Type: canonical.EventUsage, Usage: usage},
		{Type: canonical.EventMessageEnd, FinishReason: canonical.FinishToolCalls, Usage: usage},
	}
	for _, source := range allProtocols() {
		for _, target := range allProtocols() {
			t.Run(string(source)+"_to_"+string(target), func(t *testing.T) {
				req := &canonical.Request{Model: "test-model", Stream: true, IncludeUsage: true}
				var upstream, downstream bytes.Buffer
				src := protocol.MustGet(source)
				dst := protocol.MustGet(target)
				enc := src.NewStreamEncoder(&upstream, req)
				for _, ev := range events {
					if err := enc.Write(ev); err != nil {
						t.Fatal(err)
					}
				}
				if err := enc.Close(); err != nil {
					t.Fatal(err)
				}
				out := dst.NewStreamEncoder(&downstream, req)
				if err := src.DecodeStream(context.Background(), &upstream, out.Write); err != nil {
					t.Fatal(err)
				}
				if err := out.Close(); err != nil {
					t.Fatal(err)
				}
				acc := canonical.NewAccumulator()
				if err := dst.DecodeStream(context.Background(), &downstream, func(ev *canonical.Event) error { acc.Add(ev); return nil }); err != nil {
					t.Fatal(err)
				}
				r := acc.Response()
				if r.Message.TextContent() != "checking" {
					t.Errorf("text=%q", r.Message.TextContent())
				}
				calls := r.ToolCalls()
				if len(calls) != 2 {
					t.Fatalf("calls=%+v", calls)
				}
				got := map[string]string{}
				for _, c := range calls {
					got[c.Name] = string(c.Arguments)
				}
				if got["alpha"] != `{"a":1}` || got["beta"] != `{"b":2}` {
					t.Errorf("arguments=%v", got)
				}
				if r.Usage.InputTokens != 100 || r.Usage.OutputTokens != 42 || r.Usage.CachedInputTokens != 80 {
					t.Errorf("usage=%+v", r.Usage)
				}
				if r.FinishReason != canonical.FinishToolCalls {
					t.Errorf("finish=%v", r.FinishReason)
				}
			})
		}
	}
}

func TestRegressionParentErrorHasNoSuccessfulEnd(t *testing.T) {
	for _, p := range allProtocols() {
		t.Run(string(p), func(t *testing.T) {
			var wire bytes.Buffer
			codec := protocol.MustGet(p)
			enc := codec.NewStreamEncoder(&wire, &canonical.Request{Model: "test", Stream: true})
			if err := enc.Write(&canonical.Event{Type: canonical.EventMessageStart, ID: "test", Model: "test"}); err != nil {
				t.Fatal(err)
			}
			if err := enc.Write(&canonical.Event{Type: canonical.EventError, Error: canonical.Errorf(canonical.ErrUpstream, "test failure")}); err != nil {
				t.Fatal(err)
			}
			if err := enc.Close(); err != nil {
				t.Fatal(err)
			}
			body := wire.String()
			for _, marker := range []string{`"finish_reason":"stop"`, `"type":"message_stop"`, `"finishReason":"STOP"`, `response.completed`, `interaction.completed`} {
				if bytes.Contains([]byte(body), []byte(marker)) {
					t.Errorf("success marker %s after error: %s", marker, body)
				}
			}
		})
	}
}

func TestRegressionParentRequestSemantics(t *testing.T) {
	cases := []struct {
		name     string
		src, dst protocol.Name
		body     string
		check    func(*testing.T, map[string]json.RawMessage)
	}{
		{"chat-default-strict", protocol.OpenAI, protocol.OpenAIResponses, `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`, func(t *testing.T, w map[string]json.RawMessage) {
			if !bytes.Contains(w["tools"], []byte(`"strict":false`)) {
				t.Fatalf("strict default changed: %s", w["tools"])
			}
		}},
		{"adaptive-budget", protocol.Anthropic, protocol.Anthropic, `{"model":"m","max_tokens":4096,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive"}}`, func(t *testing.T, w map[string]json.RawMessage) {
			if string(w["max_tokens"]) != "4096" {
				t.Fatalf("adaptive changed max_tokens: %s", w["max_tokens"])
			}
		}},
		{"native-structured-output", protocol.Anthropic, protocol.Anthropic, `{"model":"m","max_tokens":4096,"messages":[{"role":"user","content":"hi"}],"output_config":{"format":{"type":"json_schema","schema":{"type":"object","properties":{"x":{"type":"string"}}}}}}`, func(t *testing.T, w map[string]json.RawMessage) {
			if !bytes.Contains(w["output_config"], []byte(`"x"`)) {
				t.Fatalf("schema lost: %s", w["output_config"])
			}
		}},
		{"image-tool-result", protocol.OpenAIResponses, protocol.OpenAIResponses, `{"model":"m","input":[{"type":"function_call_output","call_id":"c","output":[{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]}]}`, func(t *testing.T, w map[string]json.RawMessage) {
			if !bytes.Contains(w["input"], []byte(`data:image/png;base64,aGVsbG8=`)) {
				t.Fatalf("tool image lost: %s", w["input"])
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := canonical.NewDiagnostics()
			r, e := protocol.MustGet(c.src).DecodeRequest([]byte(c.body), d)
			if e != nil {
				t.Fatal(e)
			}
			out, e := protocol.MustGet(c.dst).EncodeRequest(r, d)
			if e != nil {
				t.Fatal(e)
			}
			var w map[string]json.RawMessage
			if e = json.Unmarshal(out, &w); e != nil {
				t.Fatal(e)
			}
			c.check(t, w)
		})
	}
}

func TestRegressionParentToolResultContent(t *testing.T) {
	c := protocol.MustGet(protocol.OpenAIResponses)
	for _, value := range []string{`""`, `[{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]`} {
		t.Run(value, func(t *testing.T) {
			r, err := c.DecodeRequest([]byte(`{"model":"m","input":[{"type":"function_call_output","call_id":"c","output":`+value+`}]}`), nil)
			if err != nil {
				t.Fatal(err)
			}
			result := r.Messages[0].Content[0].ToolResult
			if value == `""` {
				if len(result.Content) > 0 && result.Content[0].Text != "" {
					t.Errorf("empty output became %q", result.Content[0].Text)
				}
				return
			}
			if len(result.Content) != 1 || result.Content[0].Type != canonical.PartImage {
				t.Errorf("image decoded as %+v", result.Content)
			}
			out, err := protocol.MustGet(protocol.Anthropic).EncodeRequest(r, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(out, []byte(`"type":"image"`)) {
				t.Errorf("cross protocol image lost: %s", out)
			}
		})
	}
}
