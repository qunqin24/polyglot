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

// Provider extension fields, driven over real HTTP through the real pipeline.
// The codec tests prove the conversion; these prove the gateway actually lets
// it happen, and that the per-provider switch reaches it.

// captureUpstream records the body the upstream was sent.
func captureUpstream(got *string, reply string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*got = string(b)
		io.WriteString(w, reply)
	}
}

func TestProviderFieldsReachTheUpstream(t *testing.T) {
	var sent string
	h := newHarness(t, captureUpstream(&sent, okChatResponse), "openai")

	readAll(t, h.post("/v1/chat/completions", `{
		"model": "my-model",
		"messages": [{"role": "user", "content": "hi"}],
		"provider": {"order": ["Anthropic"]},
		"guided_json": {"type": "object"}
	}`, nil))

	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(sent), &body); err != nil {
		t.Fatalf("upstream body is not JSON: %v\n%s", err, sent)
	}
	for _, name := range []string{"provider", "guided_json"} {
		if _, ok := body[name]; !ok {
			t.Errorf("%q never reached the upstream:\n%s", name, sent)
		}
	}
	// The router's model name still wins over anything in the body.
	if string(body["model"]) != `"upstream-model-x"` {
		t.Errorf("model = %s, want the upstream name the router chose", body["model"])
	}
}

// A provider that rejects unknown members can be told not to receive them.
func TestStrictFieldsStopsForwardingPerProvider(t *testing.T) {
	var sent string
	h := newHarness(t, captureUpstream(&sent, okChatResponse), "openai",
		withSetup(func(t *testing.T, st *store.Store, providerID int64) {
			p, err := st.GetProvider(context.Background(), providerID)
			if err != nil {
				t.Fatalf("get provider: %v", err)
			}
			p.StrictFields = true
			if _, err := st.UpdateProvider(context.Background(), providerID, p, nil); err != nil {
				t.Fatalf("update provider: %v", err)
			}
		}))

	readAll(t, h.post("/v1/chat/completions", `{
		"model": "my-model",
		"messages": [{"role": "user", "content": "hi"}],
		"guided_json": {"type": "object"}
	}`, nil))

	if strings.Contains(sent, "guided_json") {
		t.Errorf("a strict-fields provider still received an unknown field:\n%s", sent)
	}
	// And the drop is on the record rather than silent.
	log := h.waitForLog(t)
	if !strings.Contains(log.FidelityNotes, "guided_json") {
		t.Errorf("the dropped field produced no fidelity note: %s", log.FidelityNotes)
	}
}

// Forwarding defaults to on, so an operator who never touches the setting gets
// the behaviour that keeps their provider's parameters working.
func TestForwardingIsTheDefaultForANewProvider(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okChatResponse)
	}, "openai")

	p, err := h.store.ProviderByName(context.Background(), "fake")
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	// The flag names the exception, so a provider created without mentioning
	// it — by the WebUI, by a test, by anything — forwards. A zero value that
	// meant "strict" would be the wrong default by accident.
	if p.StrictFields {
		t.Error("a newly created provider defaults to strict-fields mode")
	}
}

// Crossing protocols, the field is reported rather than handed to an upstream
// that would reject it.
func TestProviderFieldsAreNotSentAcrossProtocols(t *testing.T) {
	var sent string
	h := newHarness(t, captureUpstream(&sent,
		`{"id":"msg_1","type":"message","role":"assistant","model":"m",`+
			`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",`+
			`"usage":{"input_tokens":1,"output_tokens":2}}`), "anthropic")

	// An OpenAI client, an Anthropic upstream.
	readAll(t, h.post("/v1/chat/completions", `{
		"model": "my-model",
		"messages": [{"role": "user", "content": "hi"}],
		"guided_json": {"type": "object"}
	}`, nil))

	if strings.Contains(sent, "guided_json") {
		t.Errorf("an OpenAI-only field was sent to an Anthropic upstream:\n%s", sent)
	}
	log := h.waitForLog(t)
	if !strings.Contains(log.FidelityNotes, "guided_json") {
		t.Errorf("a cross-protocol drop produced no note: %s", log.FidelityNotes)
	}
}

// The reply's own extra fields come back to a client on the same protocol.
func TestUpstreamReplyFieldsReachTheClient(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"x","object":"chat.completion","created":1,"model":"m",
			"system_fingerprint":"fp_abc",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":2}}`)
	}, "openai")

	body := readAll(t, h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, nil))

	if !strings.Contains(body, "fp_abc") {
		t.Errorf("system_fingerprint did not reach the client:\n%s", body)
	}
}

// Streaming replies are converted event by event and have no captured
// extensions. The request side still carries them, which is the half that
// matters for parameters like guided_json.
func TestAStreamingRequestStillCarriesProviderFields(t *testing.T) {
	var sent string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		io.WriteString(w, `data: {"id":"x","choices":[{"index":0,"delta":{"content":"ok"}}]}`+"\n\n")
		f.Flush()
		io.WriteString(w, "data: [DONE]\n\n")
		f.Flush()
	}, "openai")

	readAll(t, h.post("/v1/chat/completions", `{
		"model": "my-model",
		"stream": true,
		"messages": [{"role": "user", "content": "hi"}],
		"guided_json": {"type": "object"}
	}`, nil))

	if !strings.Contains(sent, "guided_json") {
		t.Errorf("a streaming request lost its provider field:\n%s", sent)
	}
}

// The reported case, end to end: a Gemini client asking a Gemini upstream for
// the model's built-in web search. Before this, the tool was dropped in the hub
// and the caller got an answer with no search behind it — and no error to
// suggest anything had gone wrong.
func TestGeminiBuiltInSearchSurvivesTheGateway(t *testing.T) {
	var sent string
	h := newHarness(t, captureUpstream(&sent, `{
		"candidates": [{
			"content": {"role": "model", "parts": [{"text": "It rained."}]},
			"finishReason": "STOP",
			"groundingMetadata": {"webSearchQueries": ["weather today"]}
		}],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 3, "totalTokenCount": 8}
	}`), "gemini")

	body := readAll(t, h.post("/v1beta/models/my-model:generateContent", `{
		"contents": [{"role": "user", "parts": [{"text": "what happened today"}]}],
		"tools": [{"googleSearch": {}}],
		"safetySettings": [{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_NONE"}]
	}`, nil))

	if !strings.Contains(sent, "googleSearch") {
		t.Errorf("the built-in search tool never reached Gemini:\n%s", sent)
	}
	if !strings.Contains(sent, "safetySettings") {
		t.Errorf("safetySettings never reached Gemini:\n%s", sent)
	}
	// The citations come back, or the answer cannot be checked.
	if !strings.Contains(body, "groundingMetadata") {
		t.Errorf("grounding metadata did not reach the client:\n%s", body)
	}

	// And the log says it was carried, not that it was dropped.
	log := h.waitForLog(t)
	if strings.Contains(log.FidelityNotes, "no cross-protocol equivalent") {
		t.Errorf("a forwarded tool is still logged as dropped: %s", log.FidelityNotes)
	}
}

// --- multimodal ------------------------------------------------------------

const pngPixel = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// An image sent to an OpenAI client and routed to a Gemini upstream has to
// change shape entirely: a data: URI becomes inlineData with a separate mime
// type. This is the conversion the gateway exists to do.
func TestAnImageCrossesFromOpenAIToGemini(t *testing.T) {
	var sent string
	h := newHarness(t, captureUpstream(&sent, `{
		"candidates": [{"content": {"role": "model", "parts": [{"text": "a pixel"}]}, "finishReason": "STOP"}],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 2, "totalTokenCount": 7}
	}`), "gemini")

	readAll(t, h.post("/v1/chat/completions", `{
		"model": "my-model",
		"messages": [{"role": "user", "content": [
			{"type": "text", "text": "what is this"},
			{"type": "image_url", "image_url": {"url": "data:image/png;base64,`+pngPixel+`"}}
		]}]
	}`, nil))

	if !strings.Contains(sent, "inlineData") {
		t.Fatalf("the image did not reach Gemini in its own shape:\n%s", sent)
	}
	if !strings.Contains(sent, `"mimeType":"image/png"`) {
		t.Errorf("the mime type was lost:\n%s", sent)
	}
	if !strings.Contains(sent, pngPixel) {
		t.Errorf("the image bytes were altered:\n%s", sent)
	}

	// It converted cleanly, so nothing should claim it was dropped.
	log := h.waitForLog(t)
	if strings.Contains(log.FidelityNotes, "multimodal") {
		t.Errorf("a converted image is still logged as unsupported: %s", log.FidelityNotes)
	}
}

// A PDF makes the same trip, into Anthropic's document block.
func TestADocumentCrossesFromOpenAIToAnthropic(t *testing.T) {
	var sent string
	h := newHarness(t, captureUpstream(&sent,
		`{"id":"msg_1","type":"message","role":"assistant","model":"m",`+
			`"content":[{"type":"text","text":"read"}],"stop_reason":"end_turn",`+
			`"usage":{"input_tokens":1,"output_tokens":2}}`), "anthropic")

	readAll(t, h.post("/v1/chat/completions", `{
		"model": "my-model",
		"messages": [{"role": "user", "content": [
			{"type": "text", "text": "summarise"},
			{"type": "file", "file": {"filename": "report.pdf", "file_data": "data:application/pdf;base64,JVBERi0xLjQK"}}
		]}]
	}`, nil))

	if !strings.Contains(sent, `"type":"document"`) {
		t.Fatalf("the document did not reach Anthropic:\n%s", sent)
	}
	if !strings.Contains(sent, `"media_type":"application/pdf"`) {
		t.Errorf("the document type was lost:\n%s", sent)
	}
	if !strings.Contains(sent, "JVBERi0xLjQK") {
		t.Errorf("the document bytes were altered:\n%s", sent)
	}
}

// Gemini cannot fetch a URL, and Polyglot does not download one unless told
// to. The image is reported, and the rest of the request still goes.
func TestARemoteImageToGeminiIsReportedByDefault(t *testing.T) {
	var sent string
	h := newHarness(t, captureUpstream(&sent, `{
		"candidates": [{"content": {"role": "model", "parts": [{"text": "ok"}]}, "finishReason": "STOP"}],
		"usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}
	}`), "gemini")

	body := readAll(t, h.post("/v1/chat/completions", `{
		"model": "my-model",
		"messages": [{"role": "user", "content": [
			{"type": "text", "text": "what is this"},
			{"type": "image_url", "image_url": {"url": "https://example.com/cat.png"}}
		]}]
	}`, nil))

	if strings.Contains(sent, "example.com") {
		t.Errorf("a url Gemini cannot fetch was forwarded anyway:\n%s", sent)
	}
	if !strings.Contains(sent, "what is this") {
		t.Errorf("the text was lost along with the image:\n%s", sent)
	}
	if !strings.Contains(body, "ok") {
		t.Errorf("the request should still succeed:\n%s", body)
	}
	log := h.waitForLog(t)
	if !strings.Contains(log.FidelityNotes, "Gemini does not fetch remote URLs") {
		t.Errorf("the dropped image was not explained: %s", log.FidelityNotes)
	}
}
