package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/qunqin24/polyglot/internal/store"
)

func TestGeminiClientToOpenAIUpstream(t *testing.T) {
	var upstreamBody string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		io.WriteString(w, `{
			"id":"chatcmpl-1","object":"chat.completion","model":"upstream-model-x",
			"choices":[{"index":0,"message":{"role":"assistant","content":"42"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":5,"completion_tokens":1}
		}`)
	}, "openai")

	resp := h.post("/v1beta/models/my-model:generateContent", `{
		"contents":[{"role":"user","parts":[{"text":"What is 6x7?"}]}],
		"generationConfig":{"temperature":0.1,"maxOutputTokens":64}
	}`, nil)
	body := readAll(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(upstreamBody, `"model":"upstream-model-x"`) {
		t.Errorf("model from the URL was not routed: %s", upstreamBody)
	}
	if !strings.Contains(upstreamBody, `"max_completion_tokens":64`) {
		t.Errorf("generationConfig did not convert: %s", upstreamBody)
	}

	var out struct {
		Candidates []struct {
			Content struct {
				Role  string `json:"role"`
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("client body is not valid Gemini JSON: %v (%s)", err, body)
	}
	if len(out.Candidates) != 1 || out.Candidates[0].Content.Role != "model" {
		t.Fatalf("candidates = %+v", out.Candidates)
	}
	if out.Candidates[0].Content.Parts[0].Text != "42" {
		t.Errorf("text = %+v", out.Candidates[0].Content.Parts)
	}
	if out.Candidates[0].FinishReason != "STOP" {
		t.Errorf("finishReason = %q", out.Candidates[0].FinishReason)
	}
	if out.UsageMetadata.PromptTokenCount != 5 {
		t.Errorf("usage = %+v", out.UsageMetadata)
	}
}

func TestOpenAIClientToGeminiUpstream(t *testing.T) {
	var gotPath, gotKey, upstreamBody string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-goog-api-key")
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		io.WriteString(w, `{
			"candidates":[{"content":{"role":"model","parts":[{"text":"Bonjour"}]},"finishReason":"STOP","index":0}],
			"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"totalTokenCount":6},
			"modelVersion":"upstream-model-x"
		}`)
	}, "gemini")

	resp := h.post("/v1/chat/completions", `{
		"model":"my-model",
		"messages":[
			{"role":"system","content":"Reply in French."},
			{"role":"user","content":"Hello"}
		]
	}`, nil)
	body := readAll(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	// Gemini puts the model and the method in the path.
	if gotPath != "/v1beta/models/upstream-model-x:generateContent" {
		t.Errorf("upstream path = %q", gotPath)
	}
	if gotKey != "sk-upstream-secret-value" {
		t.Errorf("gemini auth header = %q", gotKey)
	}
	if !strings.Contains(upstreamBody, `"systemInstruction"`) {
		t.Errorf("system message did not become systemInstruction: %s", upstreamBody)
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("client body is not valid OpenAI JSON: %v (%s)", err, body)
	}
	if out.Choices[0].Message.Content != "Bonjour" || out.Choices[0].FinishReason != "stop" {
		t.Errorf("choice = %+v", out.Choices[0])
	}
	if out.Usage.PromptTokens != 4 {
		t.Errorf("usage = %+v", out.Usage)
	}
}

// TestAnthropicClientToGeminiUpstreamStreaming is the hardest conversion in the
// matrix: Gemini emits whole function calls, Anthropic needs them fragmented
// across content blocks.
func TestAnthropicClientToGeminiUpstreamStreaming(t *testing.T) {
	var gotPath string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		for _, chunk := range []string{
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"Checking"}]},"index":0}],"modelVersion":"upstream-model-x","responseId":"r1"}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Paris"}}}]},"index":0}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":12,"totalTokenCount":20}}`,
		} {
			io.WriteString(w, "data: "+chunk+"\n\n")
			f.Flush()
		}
	}, "gemini")

	resp := h.post("/v1/messages", `{
		"model":"my-model","max_tokens":100,
		"messages":[{"role":"user","content":"weather?"}],
		"tools":[{"name":"get_weather","input_schema":{"type":"object"}}],
		"stream":true
	}`, nil)
	body := readAll(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(gotPath, ":streamGenerateContent") || !strings.Contains(gotPath, "alt=sse") {
		t.Errorf("upstream stream path = %q", gotPath)
	}

	var text, args strings.Builder
	var toolName, stopReason string
	for _, line := range strings.Split(body, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type         string `json:"type"`
			ContentBlock *struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta *struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			t.Fatalf("event is not valid Anthropic JSON: %v (%s)", err, payload)
		}
		if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
			toolName = ev.ContentBlock.Name
		}
		if ev.Delta != nil {
			text.WriteString(ev.Delta.Text)
			args.WriteString(ev.Delta.PartialJSON)
			if ev.Delta.StopReason != "" {
				stopReason = ev.Delta.StopReason
			}
		}
	}

	if text.String() != "Checking" {
		t.Errorf("text = %q", text.String())
	}
	if toolName != "get_weather" {
		t.Errorf("tool_use block missing, name = %q\n%s", toolName, body)
	}
	if args.String() != `{"city":"Paris"}` {
		t.Errorf("tool arguments = %q", args.String())
	}
	// Gemini reports STOP for a tool call; Anthropic clients need tool_use.
	if stopReason != "tool_use" {
		t.Errorf("stop_reason = %q, want tool_use", stopReason)
	}
}

func TestGeminiClientStreamingFromAnthropicUpstream(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		for _, ev := range []string{
			`event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"upstream-model-x","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}`,
			`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Salut"}}`,
			`event: content_block_stop
data: {"type":"content_block_stop","index":0}`,
			`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`event: message_stop
data: {"type":"message_stop"}`,
		} {
			io.WriteString(w, ev+"\n\n")
			f.Flush()
		}
	}, "anthropic")

	resp := h.post("/v1beta/models/my-model:streamGenerateContent",
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, nil)
	body := readAll(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	var text string
	var finish string
	total := 0
	for _, line := range strings.Split(body, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var chunk struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
			UsageMetadata *struct {
				TotalTokenCount int `json:"totalTokenCount"`
			} `json:"usageMetadata"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("chunk is not valid Gemini JSON: %v (%s)", err, payload)
		}
		for _, c := range chunk.Candidates {
			for _, p := range c.Content.Parts {
				text += p.Text
			}
			if c.FinishReason != "" {
				finish = c.FinishReason
			}
		}
		if chunk.UsageMetadata != nil && chunk.UsageMetadata.TotalTokenCount > 0 {
			total = chunk.UsageMetadata.TotalTokenCount
		}
	}
	if text != "Salut" {
		t.Errorf("text = %q", text)
	}
	if finish != "STOP" {
		t.Errorf("finishReason = %q", finish)
	}
	if total != 5 {
		t.Errorf("totalTokenCount = %d, want 5", total)
	}
}

func TestGeminiErrorShape(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {}, "openai")

	resp := h.post("/v1beta/models/unmapped:generateContent",
		`{"contents":[{"role":"user","parts":[{"text":"x"}]}]}`, nil)
	body := readAll(t, resp)

	var out struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("error body is not JSON: %v (%s)", err, body)
	}
	if out.Error.Status != "NOT_FOUND" || out.Error.Code != http.StatusNotFound {
		t.Errorf("error is not in Gemini shape: %s", body)
	}
}

func TestGeminiUnsupportedMethod(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called for an unimplemented method")
	}, "gemini")

	resp := h.post("/v1beta/models/my-model:countTokens", `{"contents":[]}`, nil)
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "countTokens") {
		t.Errorf("the message should name the unsupported method: %s", body)
	}
}

// TestGeminiKeyInQueryString covers the Gemini SDK convention of passing the
// API key as ?key=.
func TestGeminiKeyInQueryString(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP","index":0}]}`)
	}, "gemini")

	req, _ := http.NewRequest(http.MethodPost,
		h.server.URL+"/v1beta/models/my-model:generateContent?key="+h.clientKey,
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
}

// TestGeminiNamespacedModelInPath guards a collision between two syntaxes:
// Gemini separates the model from the method with ":", and Polyglot's own
// namespace syntax puts "::" inside the model name. Splitting on the first
// colon turns "up-gemini::mock:generateContent" into model "up-gemini" and an
// unknown method, which is what the official SDK hit.
func TestGeminiNamespacedModelInPath(t *testing.T) {
	var upstreamPath string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},
			"finishReason":"STOP","index":0}],"modelVersion":"mock"}`)
	}, "gemini")

	// `provider::model` names a real registered model, so give it one.
	providers, err := h.store.ListProviders(context.Background())
	if err != nil || len(providers) == 0 {
		t.Fatalf("list providers: %v", err)
	}
	if _, err := h.store.CreateModel(context.Background(), &store.Model{
		ProviderID:      providers[0].ID,
		UpstreamModelID: "upstream-model-x",
		Enabled:         true,
	}); err != nil {
		t.Fatalf("create model: %v", err)
	}

	resp := h.post("/v1beta/models/fake::upstream-model-x:generateContent",
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, nil)
	body := readAll(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(upstreamPath, "upstream-model-x") {
		t.Errorf("namespaced model was mis-parsed; upstream path = %q", upstreamPath)
	}
}
