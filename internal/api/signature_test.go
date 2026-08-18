package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Gemini 3 binds an opaque thoughtSignature to every function call it makes and
// rejects a later request whose first functionCall part has lost it:
//
//	Function call ... in the 2. content block is missing a thought_signature.
//
// Only Gemini's wire format has a field for it, so on a cross-protocol path the
// signature has to reach the client and come back. These tests walk both legs.

const geminiToolCallStream = `data: {"candidates":[{"content":{"role":"model","parts":[` +
	`{"functionCall":{"name":"list_items","args":{"scope":"all"}},"thoughtSignature":"EroDCsYDAdHtim-REAL-SIG"}` +
	`]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":31,"candidatesTokenCount":9,"totalTokenCount":40},"modelVersion":"gemini-3.1-flash-lite"}

`

// TestSignatureReachesResponsesClient is the outbound leg: a Gemini signature
// must survive being re-encoded as a Responses function_call item.
func TestSignatureReachesResponsesClient(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, geminiToolCallStream)
		w.(http.Flusher).Flush()
	}, "gemini")

	resp := h.post("/v1/responses", `{
		"model":"my-model",
		"input":"list everything",
		"tools":[{"type":"function","name":"list_items","parameters":{"type":"object"}}],
		"stream":true
	}`, nil)
	body := readAll(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "EroDCsYDAdHtim-REAL-SIG") {
		t.Fatalf("the thought signature never reached the client:\n%s", body)
	}

	// It must ride in the envelope Google defined, so clients that already
	// preserve it keep working.
	var found bool
	for _, line := range strings.Split(body, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Item *struct {
				Type         string `json:"type"`
				ExtraContent *struct {
					Google *struct {
						ThoughtSignature string `json:"thought_signature"`
					} `json:"google"`
				} `json:"extra_content"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(payload), &ev) != nil || ev.Item == nil {
			continue
		}
		if ev.Item.Type == "function_call" && ev.Item.ExtraContent != nil &&
			ev.Item.ExtraContent.Google != nil &&
			ev.Item.ExtraContent.Google.ThoughtSignature == "EroDCsYDAdHtim-REAL-SIG" {
			found = true
		}
	}
	if !found {
		t.Errorf("signature was not carried as extra_content.google.thought_signature:\n%s", body)
	}
}

// TestSignatureReturnsToGeminiUpstream is the inbound leg, and the request that
// was failing with 400: the client replays the function_call it was given, and
// the signature must be back on the functionCall part upstream.
func TestSignatureReturnsToGeminiUpstream(t *testing.T) {
	var upstreamBody string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},
			"finishReason":"STOP","index":0}],"modelVersion":"gemini-3.1-flash-lite"}`)
	}, "gemini")

	resp := h.post("/v1/responses", `{
		"model":"my-model",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"list everything"}]},
			{"type":"function_call","call_id":"call_1","name":"list_items","arguments":"{\"scope\":\"all\"}",
			 "extra_content":{"google":{"thought_signature":"EroDCsYDAdHtim-REAL-SIG"}}},
			{"type":"function_call_output","call_id":"call_1","output":"[]"}
		],
		"tools":[{"type":"function","name":"list_items","parameters":{"type":"object"}}]
	}`, nil)
	body := readAll(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	var up struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				FunctionCall *struct {
					Name string `json:"name"`
				} `json:"functionCall"`
				ThoughtSignature string `json:"thoughtSignature"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal([]byte(upstreamBody), &up); err != nil {
		t.Fatalf("upstream body is not valid Gemini JSON: %v (%s)", err, upstreamBody)
	}

	for _, c := range up.Contents {
		for _, p := range c.Parts {
			if p.FunctionCall == nil {
				continue
			}
			if p.ThoughtSignature != "EroDCsYDAdHtim-REAL-SIG" {
				t.Fatalf("functionCall %q went upstream with signature %q, want the one the client replayed:\n%s",
					p.FunctionCall.Name, p.ThoughtSignature, upstreamBody)
			}
			return
		}
	}
	t.Fatalf("no functionCall part reached the upstream:\n%s", upstreamBody)
}

// TestUnsignedHistoryStillReachesGemini covers history Gemini never produced —
// an Anthropic or OpenAI conversation being converted into Gemini's protocol.
// There is no signature to restore, and Gemini rejects unsigned calls, so the
// documented placeholder goes out and the loss is recorded.
func TestUnsignedHistoryStillReachesGemini(t *testing.T) {
	var upstreamBody string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},
			"finishReason":"STOP","index":0}],"modelVersion":"gemini-3.1-flash-lite"}`)
	}, "gemini")

	resp := h.post("/v1/chat/completions", `{
		"model":"my-model",
		"messages":[
			{"role":"user","content":"list everything"},
			{"role":"assistant","tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"list_items","arguments":"{}"}}
			]},
			{"role":"tool","tool_call_id":"call_1","content":"[]"}
		]
	}`, nil)
	body := readAll(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(upstreamBody, "skip_thought_signature_validator") {
		t.Fatalf("unsigned tool history went to Gemini without the placeholder, which it rejects:\n%s", upstreamBody)
	}
}

// TestGeminiStreamedTextSignatureSurvivesTheNextTurn covers the native
// Gemini-to-Gemini path used by OpenCode. For a text response Gemini can close
// the model turn with a signature-only empty text part. The client stores the
// streamed parts and sends them back on its next request; both the thought and
// the final signature must reach the upstream again without a lossy note.
func TestGeminiStreamedTextSignatureSurvivesTheNextTurn(t *testing.T) {
	upstreamBodies := make(chan string, 2)
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamBodies <- string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		for _, chunk := range []string{
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"Let me think.","thought":true}]},"index":0}],"modelVersion":"gemini-3.1-pro-preview"}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"It is 18C."}]},"index":0}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"","thoughtSignature":"SIG-EMPTY-TEXT"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":3}}`,
		} {
			io.WriteString(w, "data: "+chunk+"\n\n")
			f.Flush()
		}
	}, "gemini")

	first := h.post("/v1beta/models/my-model:streamGenerateContent",
		`{"contents":[{"role":"user","parts":[{"text":"weather?"}]}]}`, nil)
	firstBody := readAll(t, first)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", first.StatusCode, firstBody)
	}
	<-upstreamBodies
	if !strings.Contains(firstBody, `"text":"It is 18C.","thoughtSignature":"SIG-EMPTY-TEXT"`) {
		t.Fatalf("the first streamed reply did not put its signature on replayable text:\n%s", firstBody)
	}
	if strings.Contains(firstBody, `"text":"","thoughtSignature":"SIG-EMPTY-TEXT"`) {
		t.Fatalf("the first streamed reply left its signature on an empty delta that AI SDK 6 discards:\n%s", firstBody)
	}

	var replayParts []json.RawMessage
	for _, line := range strings.Split(firstBody, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var chunk struct {
			Candidates []struct {
				Content *struct {
					Parts []json.RawMessage `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("client could not parse Gemini stream chunk: %v (%s)", err, payload)
		}
		for _, cand := range chunk.Candidates {
			if cand.Content != nil {
				replayParts = append(replayParts, cand.Content.Parts...)
			}
		}
	}
	nextBody, err := json.Marshal(map[string]any{"contents": []any{
		map[string]any{"role": "user", "parts": []any{map[string]any{"text": "weather?"}}},
		map[string]any{"role": "model", "parts": replayParts},
		map[string]any{"role": "user", "parts": []any{map[string]any{"text": "and tomorrow?"}}},
	}})
	if err != nil {
		t.Fatalf("marshal replay request: %v", err)
	}

	second := h.post("/v1beta/models/my-model:streamGenerateContent", string(nextBody), nil)
	secondBody := readAll(t, second)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d, body = %s", second.StatusCode, secondBody)
	}
	replayedUpstream := <-upstreamBodies
	for _, want := range []string{
		`"text":"Let me think.","thought":true`,
		`"thoughtSignature":"SIG-EMPTY-TEXT"`,
	} {
		if !strings.Contains(replayedUpstream, want) {
			t.Errorf("next upstream request is missing %s:\n%s", want, replayedUpstream)
		}
	}

	log := h.waitForLog(t)
	if strings.Contains(log.FidelityNotes, "assistant reasoning was omitted") {
		t.Errorf("a native Gemini replay was still reported as lossy: %s", log.FidelityNotes)
	}
}
