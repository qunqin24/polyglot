package compatibility

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockUpstream stands in for OpenAI, Anthropic and Google. It recognises which
// protocol is being spoken from the request path, and renders one protocol-
// neutral scenario into that protocol's wire format. Four renderers beat four
// piles of hand-written fixtures, and it keeps every test honest about the fact
// that only the wire format differs.
type mockUpstream struct {
	mu       sync.Mutex
	scn      scenario
	lastPath string
	lastBody []byte
	lastAuth http.Header
}

type scenario struct {
	text      string
	reasoning string
	toolName  string
	toolArgs  string // a complete JSON object; streamed in fragments
	// status, when non-zero, makes the upstream fail with this HTTP code.
	status int
	errMsg string
	// delay is inserted before the reply, so a client can cancel mid-flight.
	delay time.Duration
	// inputTokens/outputTokens are reported as usage.
	inputTokens, outputTokens int
}

// geminiSignature is what a real thought signature looks like on the wire: the
// Gemini SDK types the field as bytes, so it is base64 in JSON. A plain string
// makes the official SDK fail to unmarshal the whole response.
const geminiSignature = "EroDCsYDAdHtim9zc2lnbmF0dXJlLXBheWxvYWQ="

func newMockUpstream() *mockUpstream {
	return &mockUpstream{scn: scenario{text: "Hello from the upstream.", inputTokens: 11, outputTokens: 5}}
}

// use installs a scenario for one test and restores the previous one after.
func (m *mockUpstream) use(t *testing.T, s scenario) {
	t.Helper()
	if s.inputTokens == 0 {
		s.inputTokens = 11
	}
	if s.outputTokens == 0 {
		s.outputTokens = 5
	}
	m.mu.Lock()
	prev := m.scn
	m.scn = s
	m.mu.Unlock()
	t.Cleanup(func() {
		m.mu.Lock()
		m.scn = prev
		m.mu.Unlock()
	})
}

// lastRequest returns the path and body of the most recent upstream call, so a
// test can assert on what Polyglot actually sent.
func (m *mockUpstream) lastRequest() (string, []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastPath, append([]byte(nil), m.lastBody...)
}

func (m *mockUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	m.mu.Lock()
	m.lastPath, m.lastBody, m.lastAuth = r.URL.Path, body, r.Header.Clone()
	s := m.scn
	m.mu.Unlock()

	// Model discovery, which Polyglot runs when a provider is saved.
	if r.Method == http.MethodGet {
		m.serveModels(w, r)
		return
	}

	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-r.Context().Done():
			return // the client hung up; do not answer
		}
	}

	proto := protocolOf(r.URL.Path)
	if s.status != 0 {
		writeUpstreamError(w, proto, s.status, s.errMsg)
		return
	}

	if isStreaming(proto, r, body) {
		m.stream(w, proto, s)
		return
	}
	m.complete(w, proto, s)
}

func (m *mockUpstream) serveModels(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.Contains(r.URL.Path, "v1beta/models"):
		writeJSON(w, map[string]any{"models": []any{
			map[string]any{"name": "models/mock-model", "displayName": "Mock"},
		}})
	default:
		writeJSON(w, map[string]any{"object": "list", "data": []any{
			map[string]any{"id": "mock-model", "object": "model"},
		}})
	}
}

func protocolOf(path string) string {
	switch {
	case strings.HasSuffix(path, "/responses"):
		return "openai-responses"
	case strings.HasSuffix(path, "/messages"):
		return "anthropic"
	case strings.Contains(path, ":generateContent"), strings.Contains(path, ":streamGenerateContent"):
		return "gemini"
	default:
		return "openai"
	}
}

func isStreaming(proto string, r *http.Request, body []byte) bool {
	if proto == "gemini" {
		return strings.Contains(r.URL.Path, ":streamGenerateContent")
	}
	var probe struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &probe)
	return probe.Stream
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(v)
	_, _ = w.Write(b)
}

func writeUpstreamError(w http.ResponseWriter, proto string, status int, msg string) {
	if msg == "" {
		msg = "the upstream rejected this request"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var payload any
	switch proto {
	case "anthropic":
		payload = map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": msg}}
	case "gemini":
		payload = map[string]any{"error": map[string]any{"code": status, "message": msg, "status": "INVALID_ARGUMENT"}}
	default:
		payload = map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request_error"}}
	}
	b, _ := json.Marshal(payload)
	_, _ = w.Write(b)
}

// --- complete replies ------------------------------------------------------

func (m *mockUpstream) complete(w http.ResponseWriter, proto string, s scenario) {
	switch proto {
	case "openai":
		msg := map[string]any{"role": "assistant", "content": s.text}
		if s.reasoning != "" {
			msg["reasoning_content"] = s.reasoning
		}
		finish := "stop"
		if s.toolName != "" {
			finish = "tool_calls"
			msg["tool_calls"] = []any{map[string]any{
				"id": "call_1", "type": "function",
				"function": map[string]any{"name": s.toolName, "arguments": s.toolArgs},
			}}
		}
		writeJSON(w, map[string]any{
			"id": "chatcmpl-1", "object": "chat.completion", "created": 1, "model": "mock-model",
			"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
			"usage": map[string]any{
				"prompt_tokens": s.inputTokens, "completion_tokens": s.outputTokens,
				"total_tokens": s.inputTokens + s.outputTokens,
			},
		})

	case "openai-responses":
		var output []any
		if s.reasoning != "" {
			output = append(output, map[string]any{"type": "reasoning", "id": "rs_1",
				"summary": []any{map[string]any{"type": "summary_text", "text": s.reasoning}}})
		}
		if s.text != "" {
			output = append(output, map[string]any{"type": "message", "id": "msg_1",
				"role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": s.text}}})
		}
		if s.toolName != "" {
			output = append(output, map[string]any{"type": "function_call", "id": "fc_1",
				"call_id": "call_1", "name": s.toolName, "arguments": s.toolArgs, "status": "completed"})
		}
		writeJSON(w, map[string]any{
			"id": "resp_1", "object": "response", "created_at": 1, "model": "mock-model",
			"status": "completed", "output": output,
			"usage": map[string]any{
				"input_tokens": s.inputTokens, "output_tokens": s.outputTokens,
				"total_tokens": s.inputTokens + s.outputTokens,
			},
		})

	case "anthropic":
		var content []any
		if s.reasoning != "" {
			content = append(content, map[string]any{"type": "thinking",
				"thinking": s.reasoning, "signature": "sig-mock"})
		}
		if s.text != "" {
			content = append(content, map[string]any{"type": "text", "text": s.text})
		}
		stop := "end_turn"
		if s.toolName != "" {
			stop = "tool_use"
			content = append(content, map[string]any{"type": "tool_use", "id": "toolu_1",
				"name": s.toolName, "input": json.RawMessage(s.toolArgs)})
		}
		writeJSON(w, map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "mock-model",
			"content": content, "stop_reason": stop, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": s.inputTokens, "output_tokens": s.outputTokens},
		})

	case "gemini":
		var parts []any
		if s.reasoning != "" {
			parts = append(parts, map[string]any{"text": s.reasoning, "thought": true,
				"thoughtSignature": geminiSignature})
		}
		if s.text != "" {
			parts = append(parts, map[string]any{"text": s.text})
		}
		if s.toolName != "" {
			parts = append(parts, map[string]any{
				"functionCall":     map[string]any{"name": s.toolName, "args": json.RawMessage(s.toolArgs)},
				"thoughtSignature": geminiSignature,
			})
		}
		writeJSON(w, map[string]any{
			"candidates": []any{map[string]any{
				"content":      map[string]any{"role": "model", "parts": parts},
				"finishReason": "STOP", "index": 0,
			}},
			"modelVersion": "mock-model",
			"usageMetadata": map[string]any{
				"promptTokenCount": s.inputTokens, "candidatesTokenCount": s.outputTokens,
				"totalTokenCount": s.inputTokens + s.outputTokens,
			},
		})
	}
}

// --- streamed replies ------------------------------------------------------

// sseWriter frames and flushes one event at a time, so the client sees a real
// incremental stream rather than one buffered blob.
type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSE(w http.ResponseWriter) *sseWriter {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	if f != nil {
		f.Flush()
	}
	return &sseWriter{w: w, f: f}
}

func (s *sseWriter) send(event string, data any) {
	var payload string
	switch v := data.(type) {
	case string:
		payload = v
	default:
		b, _ := json.Marshal(v)
		payload = string(b)
	}
	if event != "" {
		fmt.Fprintf(s.w, "event: %s\n", event)
	}
	fmt.Fprintf(s.w, "data: %s\n\n", payload)
	if s.f != nil {
		s.f.Flush()
	}
}

// chunks splits text so the stream really arrives in pieces.
func chunks(s string, n int) []string {
	if s == "" {
		return nil
	}
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}

func (m *mockUpstream) stream(w http.ResponseWriter, proto string, s scenario) {
	sse := newSSE(w)

	switch proto {
	case "openai":
		base := func(delta map[string]any, finish any) map[string]any {
			return map[string]any{
				"id": "chatcmpl-1", "object": "chat.completion.chunk", "created": 1, "model": "mock-model",
				"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
			}
		}
		sse.send("", base(map[string]any{"role": "assistant", "content": ""}, nil))
		for _, c := range chunks(s.reasoning, 6) {
			sse.send("", base(map[string]any{"reasoning_content": c}, nil))
		}
		for _, c := range chunks(s.text, 6) {
			sse.send("", base(map[string]any{"content": c}, nil))
		}
		finish := "stop"
		if s.toolName != "" {
			finish = "tool_calls"
			idx := 0
			sse.send("", base(map[string]any{"tool_calls": []any{map[string]any{
				"index": idx, "id": "call_1", "type": "function",
				"function": map[string]any{"name": s.toolName, "arguments": ""},
			}}}, nil))
			for _, c := range chunks(s.toolArgs, 5) {
				sse.send("", base(map[string]any{"tool_calls": []any{map[string]any{
					"index": idx, "function": map[string]any{"arguments": c},
				}}}, nil))
			}
		}
		sse.send("", base(map[string]any{}, finish))
		usage := base(map[string]any{}, nil)
		usage["choices"] = []any{}
		usage["usage"] = map[string]any{
			"prompt_tokens": s.inputTokens, "completion_tokens": s.outputTokens,
			"total_tokens": s.inputTokens + s.outputTokens,
		}
		sse.send("", usage)
		sse.send("", "[DONE]")

	case "openai-responses":
		seq := 0
		next := func() int { seq++; return seq }
		sse.send("response.created", map[string]any{"type": "response.created", "sequence_number": next(),
			"response": map[string]any{"id": "resp_1", "object": "response", "status": "in_progress", "model": "mock-model"}})
		idx := 0
		if s.text != "" {
			sse.send("response.output_item.added", map[string]any{"type": "response.output_item.added",
				"sequence_number": next(), "output_index": idx,
				"item": map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "content": []any{}}})
			for _, c := range chunks(s.text, 6) {
				sse.send("response.output_text.delta", map[string]any{"type": "response.output_text.delta",
					"sequence_number": next(), "output_index": idx, "content_index": 0, "item_id": "msg_1", "delta": c})
			}
			sse.send("response.output_item.done", map[string]any{"type": "response.output_item.done",
				"sequence_number": next(), "output_index": idx,
				"item": map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "completed",
					"content": []any{map[string]any{"type": "output_text", "text": s.text}}}})
			idx++
		}
		if s.toolName != "" {
			sse.send("response.output_item.added", map[string]any{"type": "response.output_item.added",
				"sequence_number": next(), "output_index": idx,
				"item": map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1",
					"name": s.toolName, "arguments": "", "status": "in_progress"}})
			for _, c := range chunks(s.toolArgs, 5) {
				sse.send("response.function_call_arguments.delta", map[string]any{
					"type": "response.function_call_arguments.delta", "sequence_number": next(),
					"output_index": idx, "item_id": "fc_1", "delta": c})
			}
			sse.send("response.output_item.done", map[string]any{"type": "response.output_item.done",
				"sequence_number": next(), "output_index": idx,
				"item": map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1",
					"name": s.toolName, "arguments": s.toolArgs, "status": "completed"}})
			idx++
		}
		sse.send("response.completed", map[string]any{"type": "response.completed", "sequence_number": next(),
			"response": map[string]any{"id": "resp_1", "object": "response", "status": "completed", "model": "mock-model",
				"usage": map[string]any{"input_tokens": s.inputTokens, "output_tokens": s.outputTokens,
					"total_tokens": s.inputTokens + s.outputTokens}}})

	case "anthropic":
		sse.send("message_start", map[string]any{"type": "message_start",
			"message": map[string]any{"id": "msg_1", "type": "message", "role": "assistant",
				"model": "mock-model", "content": []any{},
				"usage": map[string]any{"input_tokens": s.inputTokens, "output_tokens": 0}}})
		idx := 0
		if s.reasoning != "" {
			sse.send("content_block_start", map[string]any{"type": "content_block_start", "index": idx,
				"content_block": map[string]any{"type": "thinking", "thinking": ""}})
			for _, c := range chunks(s.reasoning, 6) {
				sse.send("content_block_delta", map[string]any{"type": "content_block_delta", "index": idx,
					"delta": map[string]any{"type": "thinking_delta", "thinking": c}})
			}
			sse.send("content_block_delta", map[string]any{"type": "content_block_delta", "index": idx,
				"delta": map[string]any{"type": "signature_delta", "signature": "sig-mock"}})
			sse.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
			idx++
		}
		if s.text != "" {
			sse.send("content_block_start", map[string]any{"type": "content_block_start", "index": idx,
				"content_block": map[string]any{"type": "text", "text": ""}})
			for _, c := range chunks(s.text, 6) {
				sse.send("content_block_delta", map[string]any{"type": "content_block_delta", "index": idx,
					"delta": map[string]any{"type": "text_delta", "text": c}})
			}
			sse.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
			idx++
		}
		stop := "end_turn"
		if s.toolName != "" {
			stop = "tool_use"
			sse.send("content_block_start", map[string]any{"type": "content_block_start", "index": idx,
				"content_block": map[string]any{"type": "tool_use", "id": "toolu_1", "name": s.toolName, "input": map[string]any{}}})
			for _, c := range chunks(s.toolArgs, 5) {
				sse.send("content_block_delta", map[string]any{"type": "content_block_delta", "index": idx,
					"delta": map[string]any{"type": "input_json_delta", "partial_json": c}})
			}
			sse.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
		}
		sse.send("message_delta", map[string]any{"type": "message_delta",
			"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": s.outputTokens}})
		sse.send("message_stop", map[string]any{"type": "message_stop"})

	case "gemini":
		emit := func(parts []any, finish string) {
			cand := map[string]any{
				"content": map[string]any{"role": "model", "parts": parts},
				"index":   0,
			}
			if finish != "" {
				cand["finishReason"] = finish
			}
			payload := map[string]any{"candidates": []any{cand}, "modelVersion": "mock-model"}
			if finish != "" {
				payload["usageMetadata"] = map[string]any{
					"promptTokenCount": s.inputTokens, "candidatesTokenCount": s.outputTokens,
					"totalTokenCount": s.inputTokens + s.outputTokens,
				}
			}
			sse.send("", payload)
		}
		for _, c := range chunks(s.reasoning, 6) {
			emit([]any{map[string]any{"text": c, "thought": true}}, "")
		}
		for _, c := range chunks(s.text, 6) {
			emit([]any{map[string]any{"text": c}}, "")
		}
		if s.toolName != "" {
			emit([]any{map[string]any{
				"functionCall":     map[string]any{"name": s.toolName, "args": json.RawMessage(s.toolArgs)},
				"thoughtSignature": geminiSignature,
			}}, "")
		}
		emit([]any{map[string]any{"text": ""}}, "STOP")
	}
}
