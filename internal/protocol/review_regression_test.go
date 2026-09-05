package protocol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/protocol"
	"github.com/qunqin24/polyglot/internal/stream"
)

func reviewRoundtrip(t *testing.T, p protocol.Name, body string) string {
	t.Helper()
	d := canonical.NewDiagnostics()
	c := protocol.MustGet(p)
	req, err := c.DecodeRequest([]byte(body), d)
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.EncodeRequest(req, d)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("output=%s diagnostics=%+v", out, d.All())
	return string(out)
}
func TestRegressionReviewAdaptiveThinking(t *testing.T) {
	out := reviewRoundtrip(t, protocol.Anthropic, `{"model":"claude-opus-4-6","max_tokens":4096,"thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(out, `"adaptive"`) {
		t.Fatal("adaptive thinking silently lost")
	}
}
func TestRegressionReviewGeminiThinkingLevel(t *testing.T) {
	out := reviewRoundtrip(t, protocol.Gemini, `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"LOW"}}}`)
	if !strings.Contains(out, `"thinkingLevel":"LOW"`) {
		t.Fatal("thinkingLevel silently lost")
	}
}
func TestRegressionReviewResponsesStrictFalse(t *testing.T) {
	out := reviewRoundtrip(t, protocol.OpenAIResponses, `{"model":"gpt-4.1","input":"hi","tools":[{"type":"function","name":"f","strict":false,"parameters":{"type":"object","properties":{"x":{"type":"string"}}}}]}`)
	if !strings.Contains(out, `"strict":false`) {
		t.Fatal("explicit strict=false lost")
	}
}
func TestRegressionReviewResponsesImageToolResult(t *testing.T) {
	c := protocol.MustGet(protocol.OpenAIResponses)
	_, err := c.DecodeRequest([]byte(`{"model":"gpt-4.1","input":[{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]}]}`), nil)
	if err != nil {
		t.Fatalf("valid array tool output rejected: %v", err)
	}
}
func TestRegressionReviewInteractionsControls(t *testing.T) {
	out := reviewRoundtrip(t, protocol.GeminiInteractions, `{"model":"gemini-2.5-flash","input":"hi","generation_config":{"tool_choice":"none","thinking_summaries":"auto"}}`)
	if !strings.Contains(out, `"tool_choice":"none"`) || !strings.Contains(out, `"thinking_summaries":"auto"`) {
		t.Fatal("generation controls silently lost")
	}
}
func TestRegressionReviewInteractionsInputShapes(t *testing.T) {
	for _, body := range []string{
		`{"model":"gemini-2.5-flash","input":[{"type":"text","text":"hello"}]}`,
		`{"model":"gemini-2.5-flash","input":"hi","response_format":{"type":"text","mime_type":"application/json","schema":{"type":"object"}}}`,
	} {
		t.Run(body, func(t *testing.T) {
			_, err := protocol.MustGet(protocol.GeminiInteractions).DecodeRequest([]byte(body), nil)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestRegressionReviewInteractionsUsage(t *testing.T) {
	c := protocol.MustGet(protocol.GeminiInteractions)
	r, err := c.DecodeResponse([]byte(`{"id":"test","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"hi"}]}],"usage":{"total_input_tokens":7,"total_output_tokens":20,"total_thought_tokens":22,"total_tokens":49}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Usage.Total() != 49 {
		t.Errorf("canonical total=%d output=%d, want total49 output42", r.Usage.Total(), r.Usage.OutputTokens)
	}
	r.Usage = canonical.Usage{InputTokens: 7, OutputTokens: 42, ReasoningTokens: 22}
	b, err := c.EncodeResponse(r, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		Usage struct {
			Total  int `json:"total_tokens"`
			Output int `json:"total_output_tokens"`
		} `json:"usage"`
	}
	json.Unmarshal(b, &w)
	if w.Usage.Total != 49 || w.Usage.Output != 20 {
		t.Errorf("encoded total=%d output=%d, want49/20", w.Usage.Total, w.Usage.Output)
	}
}
func TestRegressionReviewGeminiFunctionSchema(t *testing.T) {
	out := reviewRoundtrip(t, protocol.Gemini, `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"f","parametersJsonSchema":{"type":"object","properties":{"city":{"type":"string"}}}}]}]}`)
	if !strings.Contains(out, `"city"`) {
		t.Fatal("function parametersJsonSchema lost")
	}
}
func TestRegressionReviewParallelStream(t *testing.T) {
	upstream := `data: {"id":"c","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"f","arguments":"{\"x\":"}},{"index":1,"id":"b","type":"function","function":{"name":"g","arguments":"{\"y\":"}}]}}]}

data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}},{"index":1,"function":{"arguments":"2}"}}]},"finish_reason":"tool_calls"}]}

data: [DONE]

`
	for _, target := range []protocol.Name{protocol.Anthropic, protocol.OpenAIResponses} {
		t.Run(string(target), func(t *testing.T) {
			var b bytes.Buffer
			enc := protocol.MustGet(target).NewStreamEncoder(&b, &canonical.Request{Model: "m"})
			if err := protocol.MustGet(protocol.OpenAI).DecodeStream(context.Background(), strings.NewReader(upstream), enc.Write); err != nil {
				t.Fatal(err)
			}
			if err := enc.Close(); err != nil {
				t.Fatal(err)
			}
			closed := map[int]bool{}
			sr := stream.NewReader(&b)
			for {
				f, err := sr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				var e struct {
					Type        string `json:"type"`
					Index       int    `json:"index"`
					OutputIndex int    `json:"output_index"`
					Arguments   string `json:"arguments"`
				}
				if err := json.Unmarshal(f.Data, &e); err != nil {
					t.Fatal(err)
				}
				idx := e.Index
				if target == protocol.OpenAIResponses {
					idx = e.OutputIndex
				}
				switch e.Type {
				case "content_block_stop", "response.function_call_arguments.done":
					closed[idx] = true
					t.Logf("closed %d arguments=%s", idx, e.Arguments)
				case "content_block_delta", "response.function_call_arguments.delta":
					if closed[idx] {
						t.Errorf("delta after close for block %d: %s", idx, f.Data)
					}
				}
			}
		})
	}
}
func TestRegressionReviewTruncatedStreams(t *testing.T) {
	frames := map[protocol.Name]string{
		protocol.OpenAI:             `{"id":"c","choices":[{"index":0,"delta":{"content":"partial"}}]}`,
		protocol.Anthropic:          `{"type":"message_start","message":{"id":"m","model":"m","usage":{"input_tokens":1,"output_tokens":0}}}`,
		protocol.OpenAIResponses:    `{"type":"response.created","response":{"id":"r","model":"m"}}`,
		protocol.Gemini:             `{"candidates":[{"index":0,"content":{"parts":[{"text":"partial"}]}}]}`,
		protocol.GeminiInteractions: `{"event_type":"interaction.created","interaction":{"id":"i","model":"m","status":"in_progress"}}`,
	}
	for p, f := range frames {
		t.Run(string(p), func(t *testing.T) {
			var end bool
			err := protocol.MustGet(p).DecodeStream(context.Background(), strings.NewReader("data: "+f+"\n\n"), func(e *canonical.Event) error {
				if e.Type == canonical.EventMessageEnd {
					end = true
				}
				return nil
			})
			if err == nil {
				t.Errorf("truncated stream accepted, success end emitted=%v", end)
			}
		})
	}
}
func TestRegressionReviewRedactedThinkingStream(t *testing.T) {
	src := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"m\"}}\n\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"redacted_thinking\",\"data\":\"opaque-replay-token\"}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
	var b bytes.Buffer
	c := protocol.MustGet(protocol.Anthropic)
	enc := c.NewStreamEncoder(&b, &canonical.Request{Model: "m"})
	if err := c.DecodeStream(context.Background(), strings.NewReader(src), enc.Write); err != nil {
		t.Fatal(err)
	}
	enc.Close()
	if !strings.Contains(b.String(), "opaque-replay-token") {
		t.Fatalf("redacted replay data lost: %s", b.String())
	}
}
func TestRegressionReviewAnthropicStrictTool(t *testing.T) {
	out := reviewRoundtrip(t, protocol.Anthropic, `{"model":"claude-opus-4-6","max_tokens":4096,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"f","description":"Test function","strict":true,"input_schema":{"type":"object","properties":{},"additionalProperties":false}}]}`)
	if !strings.Contains(out, `"strict":true`) {
		t.Fatal("strict tool enforcement silently lost")
	}
}
func TestRegressionReviewAnthropicStructuredOutput(t *testing.T) {
	r := &canonical.Request{Model: "claude-opus-4-6", MaxTokens: canonical.Ptr(4096), Messages: []canonical.Message{{Role: canonical.RoleUser, Content: []canonical.ContentPart{canonical.Text("hi")}}}, ResponseFormat: &canonical.ResponseFormat{Type: canonical.FormatJSONSchema, Schema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}}
	b, err := protocol.MustGet(protocol.Anthropic).EncodeRequest(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"output_config"`) {
		t.Fatalf("schema reduced to prompt: %s", b)
	}
}
func TestRegressionReviewResponsesCachedStreamingUsage(t *testing.T) {
	var b bytes.Buffer
	c := protocol.MustGet(protocol.OpenAIResponses)
	e := c.NewStreamEncoder(&b, &canonical.Request{Model: "m"})
	if err := e.Write(&canonical.Event{Type: canonical.EventMessageStart}); err != nil {
		t.Fatal(err)
	}
	if err := e.Write(&canonical.Event{Type: canonical.EventTextDelta, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Write(&canonical.Event{Type: canonical.EventMessageEnd, FinishReason: canonical.FinishStop, Usage: &canonical.Usage{InputTokens: 100, OutputTokens: 10, CachedInputTokens: 80}}); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	a := canonical.NewAccumulator()
	if err := c.DecodeStream(context.Background(), &b, func(ev *canonical.Event) error { a.Add(ev); return nil }); err != nil {
		t.Fatal(err)
	}
	if a.Response().Usage.CachedInputTokens != 80 {
		t.Errorf("cached input became %d, want80", a.Response().Usage.CachedInputTokens)
	}
}

func TestInteractionsControlVariants(t *testing.T) {
	c := protocol.MustGet(protocol.GeminiInteractions)
	for _, choice := range []string{`"auto"`, `"none"`, `"any"`, `"validated"`, `{"allowed_tools":{"mode":"any","tools":["f"]}}`, `{"allowed_tools":{"mode":"auto","tools":["f","g"]}}`} {
		t.Run(choice, func(t *testing.T) {
			d := canonical.NewDiagnostics()
			req, err := c.DecodeRequest([]byte(`{"model":"m","input":{"type":"text","text":"hi"},"generation_config":{"tool_choice":`+choice+`,"thinking_level":"low","thinking_summaries":"none"}}`), d)
			if err != nil {
				t.Fatal(err)
			}
			body, err := c.EncodeRequest(req, d)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				GenerationConfig struct {
					ToolChoice json.RawMessage `json:"tool_choice"`
					Summaries  string          `json:"thinking_summaries"`
					Level      string          `json:"thinking_level"`
				} `json:"generation_config"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			var expected, actual any
			if err := json.Unmarshal([]byte(choice), &expected); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(got.GenerationConfig.ToolChoice, &actual); err != nil {
				t.Fatal(err)
			}
			a, _ := json.Marshal(expected)
			b, _ := json.Marshal(actual)
			if !bytes.Equal(a, b) || got.GenerationConfig.Summaries != "none" || got.GenerationConfig.Level != "low" {
				t.Fatalf("controls changed: %s", body)
			}
			if req.Messages[0].TextContent() != "hi" {
				t.Fatal("single content input lost")
			}
		})
	}
	for _, bad := range []string{`{"type":12}`, `[12]`, `12`} {
		if _, err := c.DecodeRequest([]byte(`{"model":"m","input":"hi","response_format":`+bad+`}`), nil); err == nil {
			t.Errorf("invalid response_format accepted: %s", bad)
		}
	}
}

func TestResponsesFailurePreservesUsage(t *testing.T) {
	sse := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"model\":\"m\"}}\n\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"failed\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":4}}}\n\n"
	var usage canonical.Usage
	errors, ends := 0, 0
	err := protocol.MustGet(protocol.OpenAIResponses).DecodeStream(context.Background(), strings.NewReader(sse), func(ev *canonical.Event) error {
		if ev.Type == canonical.EventError {
			errors++
		}
		if ev.Type == canonical.EventMessageEnd {
			ends++
		}
		if ev.Usage != nil {
			usage = *ev.Usage
		}
		return nil
	})
	if err != nil || errors != 1 || ends != 0 || usage.InputTokens != 10 || usage.OutputTokens != 4 {
		t.Fatalf("err=%v errors=%d ends=%d usage=%+v", err, errors, ends, usage)
	}
}
