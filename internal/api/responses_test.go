package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The Responses API is a fourth protocol, so it must work in both directions
// against the three that already exist.

func TestResponsesClientToOpenAIUpstream(t *testing.T) {
	var upstreamBody string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("upstream path = %q", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		io.WriteString(w, `{
			"id":"chatcmpl-1","object":"chat.completion","model":"upstream-model-x",
			"choices":[{"index":0,"message":{"role":"assistant","content":"Bonjour"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":11,"completion_tokens":3}
		}`)
	}, "openai")

	resp := h.post("/v1/responses", `{
		"model":"my-model",
		"instructions":"Reply in French.",
		"input":"Hello",
		"max_output_tokens":128
	}`, nil)
	body := readAll(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	// instructions must become a system message on the Chat Completions side.
	var up struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
		MaxCompletionTokens int `json:"max_completion_tokens"`
	}
	if err := json.Unmarshal([]byte(upstreamBody), &up); err != nil {
		t.Fatalf("upstream body is not valid OpenAI JSON: %v (%s)", err, upstreamBody)
	}
	if len(up.Messages) != 2 || up.Messages[0].Role != "system" || up.Messages[1].Role != "user" {
		t.Fatalf("instructions did not convert to a system message: %s", upstreamBody)
	}
	if up.MaxCompletionTokens != 128 {
		t.Errorf("max_output_tokens did not carry over: %d", up.MaxCompletionTokens)
	}

	// The client must get a Responses-shaped reply.
	var out struct {
		Object string `json:"object"`
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("client body is not valid Responses JSON: %v (%s)", err, body)
	}
	if out.Object != "response" || out.Status != "completed" {
		t.Errorf("envelope = %+v", out)
	}
	if len(out.Output) != 1 || out.Output[0].Type != "message" {
		t.Fatalf("output = %+v", out.Output)
	}
	if out.Output[0].Content[0].Type != "output_text" || out.Output[0].Content[0].Text != "Bonjour" {
		t.Errorf("content = %+v", out.Output[0].Content)
	}
	if out.Usage.InputTokens != 11 || out.Usage.OutputTokens != 3 {
		t.Errorf("usage = %+v", out.Usage)
	}
}

func TestOpenAIClientToResponsesUpstream(t *testing.T) {
	var gotPath, upstreamBody string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		io.WriteString(w, `{
			"id":"resp_1","object":"response","model":"upstream-model-x","status":"completed",
			"output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed",
			           "content":[{"type":"output_text","text":"Hi there"}]}],
			"usage":{"input_tokens":8,"output_tokens":4,"total_tokens":12}
		}`)
	}, "openai-responses")

	resp := h.post("/v1/chat/completions", `{
		"model":"my-model",
		"messages":[
			{"role":"system","content":"Be terse."},
			{"role":"user","content":"Hello"}
		]
	}`, nil)
	body := readAll(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if gotPath != "/responses" {
		t.Errorf("upstream path = %q, want /responses", gotPath)
	}

	var up struct {
		Instructions string          `json:"instructions"`
		Input        json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal([]byte(upstreamBody), &up); err != nil {
		t.Fatalf("upstream body is not valid Responses JSON: %v (%s)", err, upstreamBody)
	}
	if up.Instructions != "Be terse." {
		t.Errorf("system message did not become instructions: %q", up.Instructions)
	}
	if !strings.Contains(string(up.Input), "input_text") {
		t.Errorf("input items look wrong: %s", up.Input)
	}

	var out struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("client body is not valid OpenAI JSON: %v (%s)", err, body)
	}
	if out.Object != "chat.completion" || out.Choices[0].Message.Content != "Hi there" {
		t.Errorf("choice = %+v", out.Choices)
	}
}

// TestResponsesClientToAnthropicUpstreamStreaming is the hardest pairing: a
// typed Responses event stream produced from Anthropic's block events.
func TestResponsesClientToAnthropicUpstreamStreaming(t *testing.T) {
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

	resp := h.post("/v1/responses", `{
		"model":"my-model",
		"input":"weather?",
		"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}],
		"stream":true
	}`, nil)
	body := readAll(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	var text, args strings.Builder
	var toolName, status string
	outputTokens := 0
	for _, line := range strings.Split(body, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
			Item  *struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"item"`
			Response *struct {
				Status string `json:"status"`
				Usage  *struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			t.Fatalf("event is not valid Responses JSON: %v (%s)", err, payload)
		}
		switch ev.Type {
		case "response.output_text.delta":
			text.WriteString(ev.Delta)
		case "response.function_call_arguments.delta":
			args.WriteString(ev.Delta)
		case "response.output_item.added":
			if ev.Item != nil && ev.Item.Type == "function_call" {
				toolName = ev.Item.Name
			}
		case "response.completed":
			if ev.Response != nil {
				status = ev.Response.Status
				if ev.Response.Usage != nil {
					outputTokens = ev.Response.Usage.OutputTokens
				}
			}
		}
	}

	if text.String() != "Let me check" {
		t.Errorf("text = %q", text.String())
	}
	if toolName != "get_weather" {
		t.Errorf("function_call item missing, name = %q\n%s", toolName, body)
	}
	if args.String() != `{"city":"Paris"}` {
		t.Errorf("reassembled arguments = %q", args.String())
	}
	if status != "completed" {
		t.Errorf("final status = %q", status)
	}
	if outputTokens != 21 {
		t.Errorf("output_tokens = %d", outputTokens)
	}
}

func TestResponsesErrorShape(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {}, "openai")

	resp := h.post("/v1/responses", `{"model":"unmapped","input":"x"}`, nil)
	body := readAll(t, resp)

	var out struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("error body is not JSON: %v (%s)", err, body)
	}
	if resp.StatusCode != http.StatusNotFound || out.Error.Type != "not_found_error" {
		t.Errorf("error is not in Responses shape: %d %s", resp.StatusCode, body)
	}
}
