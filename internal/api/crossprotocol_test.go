package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// These tests are the point of the whole project: a client speaking one
// protocol reaching an upstream that speaks another, through the canonical hub.

func TestAnthropicClientToOpenAIUpstream(t *testing.T) {
	var upstreamBody string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		io.WriteString(w, `{
			"id":"chatcmpl-1","object":"chat.completion","model":"upstream-model-x",
			"choices":[{"index":0,"message":{"role":"assistant","content":"Bonjour"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":11,"completion_tokens":3}
		}`)
	}, "openai")

	resp := h.post("/v1/messages", `{
		"model":"my-model",
		"max_tokens":256,
		"system":"Reply in French.",
		"messages":[{"role":"user","content":"Hello"}]
	}`, nil)
	body := readAll(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	// The upstream must have received a well-formed OpenAI request.
	var up struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		MaxCompletionTokens int `json:"max_completion_tokens"`
	}
	if err := json.Unmarshal([]byte(upstreamBody), &up); err != nil {
		t.Fatalf("upstream body is not valid OpenAI JSON: %v (%s)", err, upstreamBody)
	}
	if up.Model != "upstream-model-x" {
		t.Errorf("upstream model = %q", up.Model)
	}
	if len(up.Messages) != 2 || up.Messages[0].Role != "system" || up.Messages[1].Role != "user" {
		t.Fatalf("system prompt was not converted to a system message: %s", upstreamBody)
	}
	if up.MaxCompletionTokens != 256 {
		t.Errorf("max_tokens did not carry over: %d", up.MaxCompletionTokens)
	}

	// The client must have received a well-formed Anthropic response.
	var out struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("client body is not valid Anthropic JSON: %v (%s)", err, body)
	}
	if out.Type != "message" || out.Role != "assistant" {
		t.Errorf("response envelope = %+v", out)
	}
	if len(out.Content) != 1 || out.Content[0].Type != "text" || out.Content[0].Text != "Bonjour" {
		t.Errorf("content = %+v", out.Content)
	}
	if out.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q", out.StopReason)
	}
	if out.Usage.InputTokens != 11 || out.Usage.OutputTokens != 3 {
		t.Errorf("usage = %+v", out.Usage)
	}
}

func TestOpenAIClientToAnthropicUpstream(t *testing.T) {
	var upstreamBody string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("upstream path = %q, want /v1/messages", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "sk-upstream-secret-value" {
			t.Errorf("anthropic auth header = %q", got)
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Error("anthropic-version header is missing")
		}
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		io.WriteString(w, `{
			"id":"msg_1","type":"message","role":"assistant","model":"upstream-model-x",
			"content":[{"type":"text","text":"Hi there"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":8,"output_tokens":4}
		}`)
	}, "anthropic")

	resp := h.post("/v1/chat/completions", `{
		"model":"my-model",
		"messages":[
			{"role":"system","content":"Be terse."},
			{"role":"user","content":"Hello"}
		],
		"max_tokens":128
	}`, nil)
	body := readAll(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	var up struct {
		Model     string          `json:"model"`
		MaxTokens int             `json:"max_tokens"`
		System    json.RawMessage `json:"system"`
		Messages  []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(upstreamBody), &up); err != nil {
		t.Fatalf("upstream body is not valid Anthropic JSON: %v (%s)", err, upstreamBody)
	}
	if up.MaxTokens != 128 {
		t.Errorf("max_tokens = %d", up.MaxTokens)
	}
	// The OpenAI system message must become Anthropic's top-level system field.
	if !strings.Contains(string(up.System), "Be terse") {
		t.Errorf("system field = %s", up.System)
	}
	if len(up.Messages) != 1 || up.Messages[0].Role != "user" {
		t.Errorf("messages = %+v", up.Messages)
	}

	var out struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("client body is not valid OpenAI JSON: %v (%s)", err, body)
	}
	if out.Object != "chat.completion" {
		t.Errorf("object = %q", out.Object)
	}
	if out.Choices[0].Message.Content != "Hi there" || out.Choices[0].FinishReason != "stop" {
		t.Errorf("choice = %+v", out.Choices[0])
	}
	if out.Usage.PromptTokens != 8 || out.Usage.CompletionTokens != 4 {
		t.Errorf("usage = %+v", out.Usage)
	}
}

// TestOpenAIClientToAnthropicUpstreamStreaming converts an Anthropic event
// stream into OpenAI chunks in flight, including a tool call whose arguments
// arrive in fragments.
func TestOpenAIClientToAnthropicUpstreamStreaming(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		for _, ev := range []string{
			`event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"upstream-model-x","content":[],"usage":{"input_tokens":9,"output_tokens":0}}}`,
			`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me check"}}`,
			`event: content_block_stop
data: {"type":"content_block_stop","index":0}`,
			`event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`,
			`event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"ci"}}`,
			`event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"ty\":\"Paris\"}"}}`,
			`event: content_block_stop
data: {"type":"content_block_stop","index":1}`,
			`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":21}}`,
			`event: message_stop
data: {"type":"message_stop"}`,
		} {
			io.WriteString(w, ev+"\n\n")
			f.Flush()
		}
	}, "anthropic")

	resp := h.post("/v1/chat/completions", `{
		"model":"my-model",
		"messages":[{"role":"user","content":"weather?"}],
		"stream":true,
		"stream_options":{"include_usage":true}
	}`, nil)
	body := readAll(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	var text, args strings.Builder
	var toolName, toolID, finish string
	sawUsage := false
	for _, line := range strings.Split(body, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Object  string `json:"object"`
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("chunk is not valid OpenAI JSON: %v (%s)", err, payload)
		}
		if chunk.Usage != nil {
			if chunk.Usage.PromptTokens != 9 || chunk.Usage.CompletionTokens != 21 {
				t.Errorf("usage chunk = %+v", chunk.Usage)
			}
			sawUsage = true
		}
		for _, c := range chunk.Choices {
			text.WriteString(c.Delta.Content)
			for _, tc := range c.Delta.ToolCalls {
				if tc.ID != "" {
					toolID = tc.ID
				}
				if tc.Function.Name != "" {
					toolName = tc.Function.Name
				}
				args.WriteString(tc.Function.Arguments)
			}
			if c.FinishReason != nil && *c.FinishReason != "" {
				finish = *c.FinishReason
			}
		}
	}

	if text.String() != "Let me check" {
		t.Errorf("text = %q", text.String())
	}
	if toolName != "get_weather" || toolID != "toolu_1" {
		t.Errorf("tool call identity lost: name=%q id=%q", toolName, toolID)
	}
	if args.String() != `{"city":"Paris"}` {
		t.Errorf("reassembled arguments = %q", args.String())
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(args.String()), &parsed); err != nil {
		t.Errorf("reassembled arguments are not valid JSON: %v", err)
	}
	if finish != "tool_calls" {
		t.Errorf("finish_reason = %q", finish)
	}
	if !sawUsage {
		t.Errorf("no usage chunk in:\n%s", body)
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Errorf("stream must end with [DONE]")
	}
}

// TestAnthropicClientToOpenAIUpstreamStreaming is the mirror image: OpenAI
// chunks in, Anthropic typed events out.
func TestAnthropicClientToOpenAIUpstreamStreaming(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		for _, chunk := range []string{
			`{"id":"c1","object":"chat.completion.chunk","model":"upstream-model-x","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{"reasoning_content":"thinking"}}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{"content":" world"}}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`{"id":"c1","choices":[],"usage":{"prompt_tokens":6,"completion_tokens":7}}`,
		} {
			io.WriteString(w, "data: "+chunk+"\n\n")
			f.Flush()
		}
		io.WriteString(w, "data: [DONE]\n\n")
		f.Flush()
	}, "openai")

	resp := h.post("/v1/messages",
		`{"model":"my-model","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`, nil)
	body := readAll(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	for _, want := range []string{
		"event: message_start", "event: content_block_start", "event: content_block_delta",
		"event: content_block_stop", "event: message_delta", "event: message_stop",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Anthropic stream is missing %q:\n%s", want, body)
		}
	}

	var text, thinking strings.Builder
	var stopReason string
	outputTokens := 0
	for _, line := range strings.Split(body, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type  string `json:"type"`
			Delta *struct {
				Type       string `json:"type"`
				Text       string `json:"text"`
				Thinking   string `json:"thinking"`
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Usage *struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			t.Fatalf("event is not valid Anthropic JSON: %v (%s)", err, payload)
		}
		if ev.Delta != nil {
			text.WriteString(ev.Delta.Text)
			thinking.WriteString(ev.Delta.Thinking)
			if ev.Delta.StopReason != "" {
				stopReason = ev.Delta.StopReason
			}
		}
		if ev.Type == "message_delta" && ev.Usage != nil {
			outputTokens = ev.Usage.OutputTokens
		}
	}
	if text.String() != "Hello world" {
		t.Errorf("text = %q", text.String())
	}
	if thinking.String() != "thinking" {
		t.Errorf("reasoning did not become a thinking block: %q", thinking.String())
	}
	if stopReason != "end_turn" {
		t.Errorf("stop_reason = %q", stopReason)
	}
	if outputTokens != 7 {
		t.Errorf("output_tokens = %d", outputTokens)
	}
}

// TestAnthropicErrorShape checks that a client speaking Anthropic gets an
// Anthropic-shaped error, not an OpenAI one.
func TestAnthropicErrorShape(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {}, "openai")

	resp := h.post("/v1/messages", `{"model":"unmapped","max_tokens":10,"messages":[{"role":"user","content":"x"}]}`, nil)
	body := readAll(t, resp)

	var out struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("error body is not JSON: %v (%s)", err, body)
	}
	if out.Type != "error" || out.Error.Type != "not_found_error" {
		t.Errorf("error is not in Anthropic shape: %s", body)
	}
}
