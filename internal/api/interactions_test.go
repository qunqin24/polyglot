package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Google's Interactions API, driven over real HTTP through the real gateway.
//
// The mock upstream replays recorded response shapes rather than invented
// ones. There is no Go SDK for this API, so these tests and the codec tests
// are the two layers available; the third — an official SDK driving the real
// binary — cannot exist yet and has deliberately not been faked.

const interactionsReply = `{
	"id": "v1_abc",
	"object": "interaction",
	"model": "gemini-3.6-flash",
	"status": "completed",
	"usage": {"total_tokens": 20, "total_input_tokens": 8, "total_output_tokens": 12},
	"steps": [
		{"type": "thought", "signature": "sig-think"},
		{"type": "model_output", "content": [{"type": "text", "text": "hello from interactions"}]}
	]
}`

// An Interactions client reaching an Interactions upstream.
func TestInteractionsEndToEnd(t *testing.T) {
	var sent string
	h := newHarness(t, captureUpstream(&sent, interactionsReply), "gemini-interactions")

	body := readAll(t, h.post("/v1beta/interactions",
		`{"model":"my-model","input":"hi"}`, nil))

	// The gateway must never let the provider keep the conversation.
	var upstream map[string]json.RawMessage
	if err := json.Unmarshal([]byte(sent), &upstream); err != nil {
		t.Fatalf("upstream body is not JSON: %v\n%s", err, sent)
	}
	if string(upstream["store"]) != "false" {
		t.Errorf("store = %s, want an explicit false:\n%s", upstream["store"], sent)
	}
	if string(upstream["model"]) != `"upstream-model-x"` {
		t.Errorf("model = %s, want the routed upstream name", upstream["model"])
	}

	if !strings.Contains(body, "hello from interactions") {
		t.Errorf("the reply did not reach the client:\n%s", body)
	}
	if !strings.Contains(body, `"steps"`) {
		t.Errorf("the client should get a steps timeline:\n%s", body)
	}

	log := h.waitForLog(t)
	if log.InputTokens != 8 || log.OutputTokens != 12 {
		t.Errorf("usage did not reach the log: in=%d out=%d", log.InputTokens, log.OutputTokens)
	}
}

// An OpenAI client reaching an Interactions upstream — the conversion this
// gateway exists to do.
func TestOpenAIClientReachesAnInteractionsUpstream(t *testing.T) {
	var sent string
	h := newHarness(t, captureUpstream(&sent, interactionsReply), "gemini-interactions")

	body := readAll(t, h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, nil))

	if !strings.Contains(sent, `"input"`) {
		t.Errorf("the request was not converted to the input form:\n%s", sent)
	}
	if strings.Contains(sent, "messages") {
		t.Errorf("OpenAI's message array leaked to an Interactions upstream:\n%s", sent)
	}
	if !strings.Contains(body, "hello from interactions") {
		t.Errorf("the reply did not convert back to OpenAI shape:\n%s", body)
	}
	if !strings.Contains(body, "chat.completion") {
		t.Errorf("the client should get an OpenAI reply:\n%s", body)
	}
}

// Streaming, using the recorded event shapes.
func TestInteractionsStreamingEndToEnd(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for _, chunk := range []string{
			`{"interaction":{"id":"v1_x","status":"in_progress","object":"interaction","model":"m"},"event_type":"interaction.created"}`,
			`{"index":0,"step":{"type":"model_output"},"event_type":"step.start"}`,
			`{"index":0,"delta":{"type":"text","text":"Hel"},"event_type":"step.delta"}`,
			`{"index":0,"delta":{"type":"text","text":"lo"},"event_type":"step.delta"}`,
			`{"index":0,"event_type":"step.stop"}`,
			`{"interaction":{"id":"v1_x","status":"completed","usage":{"total_tokens":9,"total_input_tokens":4,"total_output_tokens":5}},"event_type":"interaction.completed"}`,
		} {
			io.WriteString(w, "data: "+chunk+"\n\n")
			f.Flush()
		}
		io.WriteString(w, "event: done\ndata: [DONE]\n\n")
		f.Flush()
	}, "gemini-interactions")

	body := readAll(t, h.post("/v1beta/interactions",
		`{"model":"my-model","input":"hi","stream":true}`, nil))

	if !strings.Contains(body, "Hel") || !strings.Contains(body, "lo") {
		t.Errorf("streamed text was lost:\n%s", body)
	}
	if !strings.Contains(body, `"event_type":"interaction.completed"`) {
		t.Errorf("the stream did not complete:\n%s", body)
	}

	log := h.waitForLog(t)
	if log.OutputTokens != 5 {
		t.Errorf("streamed usage did not reach the log: %d", log.OutputTokens)
	}
	if log.TTFTMS == nil {
		t.Error("a streamed interaction produced no TTFT")
	}
}

// A Gemini client on the legacy protocol reaching an Interactions upstream:
// the two Google formats are separate protocols and must convert between
// themselves like any other pair.
func TestLegacyGeminiClientReachesInteractions(t *testing.T) {
	var sent string
	h := newHarness(t, captureUpstream(&sent, interactionsReply), "gemini-interactions")

	body := readAll(t, h.post("/v1beta/models/my-model:generateContent",
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, nil))

	if !strings.Contains(sent, `"input"`) || strings.Contains(sent, "contents") {
		t.Errorf("generateContent was not converted to Interactions:\n%s", sent)
	}
	if !strings.Contains(body, "candidates") {
		t.Errorf("the client should get a generateContent reply:\n%s", body)
	}
	if !strings.Contains(body, "hello from interactions") {
		t.Errorf("the reply text was lost:\n%s", body)
	}
}
