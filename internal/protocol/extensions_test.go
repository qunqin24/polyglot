package protocol_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/protocol"
)

// An OpenAI-compatible upstream is a family, not one API. These are real
// parameters real providers take and Polyglot has no canonical field for.
const vendorRequest = `{
	"model": "m",
	"messages": [{"role": "user", "content": "hi"}],
	"temperature": 0.5,
	"provider": {"order": ["Anthropic"], "allow_fallbacks": false},
	"transforms": ["middle-out"],
	"guided_json": {"type": "object"},
	"repetition_penalty": 1.1,
	"service_tier": "flex"
}`

var vendorFields = []string{"provider", "transforms", "guided_json", "repetition_penalty", "service_tier"}

func decodeInto(t *testing.T, proto protocol.Name, body string) (*canonical.Request, *canonical.Diagnostics) {
	t.Helper()
	d := canonical.NewDiagnostics()
	req, err := protocol.MustGet(proto).DecodeRequest([]byte(body), d)
	if err != nil {
		t.Fatalf("DecodeRequest(%s): %v", proto, err)
	}
	return req, d
}

func objectOf(t *testing.T, wire []byte) map[string]json.RawMessage {
	t.Helper()
	var out map[string]json.RawMessage
	if err := json.Unmarshal(wire, &out); err != nil {
		t.Fatalf("encoded body is not a JSON object: %v\n%s", err, wire)
	}
	return out
}

// The whole point: a provider's own parameters reach the provider.
func TestVendorFieldsSurviveASameProtocolRoute(t *testing.T) {
	req, _ := decodeInto(t, protocol.OpenAI, vendorRequest)

	if req.Extensions.Len() != len(vendorFields) {
		t.Fatalf("captured %d unknown fields, want %d: %v",
			req.Extensions.Len(), len(vendorFields), req.Extensions.Names())
	}

	d := canonical.NewDiagnostics()
	wire, err := protocol.MustGet(protocol.OpenAI).EncodeRequest(req, d)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}

	got := objectOf(t, wire)
	for _, name := range vendorFields {
		if _, ok := got[name]; !ok {
			t.Errorf("%q did not reach the upstream body:\n%s", name, wire)
		}
	}
	// Values are replayed byte for byte, not re-interpreted.
	if string(got["transforms"]) != `["middle-out"]` {
		t.Errorf("transforms = %s, want the original value", got["transforms"])
	}
	if !strings.Contains(string(got["provider"]), `"allow_fallbacks":false`) {
		t.Errorf("a nested vendor object lost structure: %s", got["provider"])
	}
	// And the fields Polyglot does understand are still encoded normally.
	if _, ok := got["temperature"]; !ok {
		t.Errorf("a recognised field went missing:\n%s", wire)
	}
}

// Sending guided_json to Gemini would at best be ignored and at worst be a
// 400. Cross-protocol they are reported, not forwarded — which is exactly the
// fidelity rule, and is what used to not happen at all.
func TestVendorFieldsAreReportedNotForwardedAcrossProtocols(t *testing.T) {
	req, _ := decodeInto(t, protocol.OpenAI, vendorRequest)

	for _, target := range []protocol.Name{protocol.Anthropic, protocol.Gemini, protocol.OpenAIResponses} {
		t.Run(string(target), func(t *testing.T) {
			d := canonical.NewDiagnostics()
			wire, err := protocol.MustGet(target).EncodeRequest(req, d)
			if err != nil {
				t.Fatalf("EncodeRequest: %v", err)
			}
			for _, name := range vendorFields {
				if strings.Contains(string(wire), `"`+name+`"`) {
					t.Errorf("%q was forwarded to %s, which cannot use it:\n%s", name, target, wire)
				}
			}

			var note *canonical.Note
			for i, n := range d.All() {
				if n.Field == "extensions" {
					note = &d.All()[i]
				}
			}
			if note == nil {
				t.Fatalf("dropping %d fields produced no note; a silent drop is a bug", len(vendorFields))
			}
			if note.Fidelity != canonical.FidelityUnsupported {
				t.Errorf("note fidelity = %q, want unsupported", note.Fidelity)
			}
			for _, name := range vendorFields {
				if !strings.Contains(note.Detail, name) {
					t.Errorf("the note does not name %q: %s", name, note.Detail)
				}
			}
		})
	}
}

// Gemini keeps its parameters inside generationConfig, so a capture that only
// looked at the top level would miss almost all of them.
func TestGeminiCapturesNestedGenerationConfigFields(t *testing.T) {
	body := `{
		"contents": [{"role": "user", "parts": [{"text": "hi"}]}],
		"generationConfig": {"temperature": 0.4, "responseLogprobs": true, "mediaResolution": "MEDIA_RESOLUTION_HIGH"},
		"cachedContent": "projects/p/locations/l/cachedContents/1"
	}`
	req, _ := decodeInto(t, protocol.Gemini, body)

	names := req.Extensions.Names()
	want := []string{"cachedContent", "generationConfig.mediaResolution", "generationConfig.responseLogprobs"}
	if len(names) != len(want) {
		t.Fatalf("captured %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("captured %v, want %v", names, want)
		}
	}

	req.Model = "gemini-3-pro"
	wire, err := protocol.MustGet(protocol.Gemini).EncodeRequest(req, canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	got := objectOf(t, wire)
	if _, ok := got["cachedContent"]; !ok {
		t.Errorf("a top-level Gemini field was lost:\n%s", wire)
	}
	gc := objectOf(t, got["generationConfig"])
	for _, name := range []string{"responseLogprobs", "mediaResolution"} {
		if _, ok := gc[name]; !ok {
			t.Errorf("generationConfig.%s was lost:\n%s", name, wire)
		}
	}
	// The nested object still carries what the codec itself produced.
	if _, ok := gc["temperature"]; !ok {
		t.Errorf("merging extensions dropped a recognised nested field:\n%s", wire)
	}
}

// An extension must never overwrite a value the conversion decided. The model
// name is the case that matters: the router rewrites it, and a stale one from
// the client body would send the request to the wrong upstream model.
func TestAnExtensionNeverOverwritesAConvertedField(t *testing.T) {
	req, _ := decodeInto(t, protocol.OpenAI, vendorRequest)
	// Forge an extension that collides with a field the encoder produces.
	req.Extensions.Items = append(req.Extensions.Items, canonical.Extension{
		Name: "model", Value: json.RawMessage(`"attacker-chosen-model"`),
	})
	req.Model = "router-chosen-model"

	d := canonical.NewDiagnostics()
	wire, err := protocol.MustGet(protocol.OpenAI).EncodeRequest(req, d)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	got := objectOf(t, wire)
	if string(got["model"]) != `"router-chosen-model"` {
		t.Fatalf("an extension overwrote the routed model: %s", got["model"])
	}

	var noted bool
	for _, n := range d.All() {
		if n.Field == "extensions" && n.Fidelity == canonical.FidelityLossy &&
			strings.Contains(n.Detail, "model") {
			noted = true
		}
	}
	if !noted {
		t.Error("a skipped extension was not reported")
	}
}

// A body built to allocate is answered with a note, not with memory.
func TestExtensionCaptureIsBounded(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"model":"m","messages":[{"role":"user","content":"hi"}]`)
	for i := range canonical.MaxExtensions * 4 {
		sb.WriteString(`,"junk_`)
		sb.WriteString(strings.Repeat("x", 3))
		sb.WriteString(string(rune('a' + i%26)))
		sb.WriteString(string(rune('a' + (i/26)%26)))
		sb.WriteString(`":1`)
	}
	sb.WriteString(`}`)

	req, _ := decodeInto(t, protocol.OpenAI, sb.String())
	if req.Extensions.Len() > canonical.MaxExtensions {
		t.Errorf("captured %d extensions, cap is %d", req.Extensions.Len(), canonical.MaxExtensions)
	}
	if !req.Extensions.Truncated {
		t.Error("hitting the cap must be recorded, not silent")
	}

	d := canonical.NewDiagnostics()
	if _, err := protocol.MustGet(protocol.OpenAI).EncodeRequest(req, d); err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	var noted bool
	for _, n := range d.All() {
		if n.Field == "extensions" && strings.Contains(n.Detail, "more than") {
			noted = true
		}
	}
	if !noted {
		t.Error("truncation produced no note")
	}
}

// A clean request must not gain a note, an allocation or a reordered body just
// because the mechanism exists.
func TestAPlainRequestIsUnaffected(t *testing.T) {
	plain := `{"model":"m","messages":[{"role":"user","content":"hi"}],"temperature":0.5}`
	req, d := decodeInto(t, protocol.OpenAI, plain)

	if req.Extensions != nil {
		t.Errorf("a fully understood request captured extensions: %v", req.Extensions.Names())
	}
	for _, n := range d.All() {
		if n.Field == "extensions" {
			t.Errorf("a clean request produced an extensions note: %+v", n)
		}
	}

	d2 := canonical.NewDiagnostics()
	if _, err := protocol.MustGet(protocol.OpenAI).EncodeRequest(req, d2); err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	for _, n := range d2.All() {
		if n.Field == "extensions" {
			t.Errorf("a clean request produced an extensions note on encode: %+v", n)
		}
	}
}

// Every protocol must capture and replay, not just the OpenAI one.
func TestEveryProtocolRoundTripsItsOwnExtensions(t *testing.T) {
	bodies := map[protocol.Name]string{
		protocol.OpenAI: `{"model":"m","messages":[{"role":"user","content":"hi"}],"x_vendor_flag":true}`,
		protocol.Anthropic: `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}],` +
			`"x_vendor_flag":true}`,
		protocol.Gemini:          `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"x_vendor_flag":true}`,
		protocol.OpenAIResponses: `{"model":"m","input":"hi","x_vendor_flag":true}`,
	}

	for proto, body := range bodies {
		t.Run(string(proto), func(t *testing.T) {
			req, _ := decodeInto(t, proto, body)
			if req.Extensions.Len() != 1 {
				t.Fatalf("captured %v, want just x_vendor_flag", req.Extensions.Names())
			}
			req.Model = "upstream-model"

			wire, err := protocol.MustGet(proto).EncodeRequest(req, canonical.NewDiagnostics())
			if err != nil {
				t.Fatalf("EncodeRequest: %v", err)
			}
			if got := objectOf(t, wire)["x_vendor_flag"]; string(got) != "true" {
				t.Errorf("x_vendor_flag did not survive %s -> %s: %s", proto, proto, wire)
			}
		})
	}
}

// Reply fields a dialect adds reach a client speaking that dialect.
func TestResponseExtensionsReturnToASameProtocolClient(t *testing.T) {
	upstream := `{"id":"x","object":"chat.completion","created":1,"model":"m",
		"system_fingerprint":"fp_abc","provider":"Anthropic",
		"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":1,"completion_tokens":2}}`

	codec := protocol.MustGet(protocol.OpenAI)
	resp, err := codec.DecodeResponse([]byte(upstream), canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if resp.Extensions.Len() != 2 {
		t.Fatalf("captured %v, want system_fingerprint and provider", resp.Extensions.Names())
	}

	wire, err := codec.EncodeResponse(resp, &canonical.Request{Model: "m"}, canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	got := objectOf(t, wire)
	if string(got["system_fingerprint"]) != `"fp_abc"` {
		t.Errorf("system_fingerprint did not reach the client:\n%s", wire)
	}
	if string(got["provider"]) != `"Anthropic"` {
		t.Errorf("the upstream's provider field did not reach the client:\n%s", wire)
	}

	// A client on another protocol gets a note instead.
	d := canonical.NewDiagnostics()
	if _, err := protocol.MustGet(protocol.Anthropic).EncodeResponse(
		resp, &canonical.Request{Model: "m"}, d); err != nil {
		t.Fatalf("EncodeResponse(anthropic): %v", err)
	}
	var noted bool
	for _, n := range d.All() {
		if n.Field == "extensions" && n.Fidelity == canonical.FidelityUnsupported {
			noted = true
		}
	}
	if !noted {
		t.Error("reply extensions were dropped for another protocol with no note")
	}
}

// Provider-executed tools. These run inside the provider — Polyglot never sees
// a round trip — so it cannot translate them, but a same-protocol route has no
// reason to lose them. Before this they were dropped everywhere, which meant
// asking Gemini for a web search through Polyglot quietly returned an answer
// with no web search in it.

const geminiSearchRequest = `{
	"contents": [{"role": "user", "parts": [{"text": "what happened today"}]}],
	"tools": [
		{"googleSearch": {}},
		{"functionDeclarations": [{"name": "get_weather", "description": "w"}]}
	],
	"safetySettings": [{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_NONE"}]
}`

func TestGeminiWebSearchReachesGemini(t *testing.T) {
	req, _ := decodeInto(t, protocol.Gemini, geminiSearchRequest)
	req.Model = "gemini-3-flash"

	if req.NativeTools.Len() != 1 {
		t.Fatalf("captured %v native tools, want googleSearch", req.NativeTools.Names())
	}
	// The function declaration in the same array is still converted normally.
	if len(req.Tools) != 1 || req.Tools[0].Name != "get_weather" {
		t.Fatalf("the function tool was lost: %+v", req.Tools)
	}

	d := canonical.NewDiagnostics()
	wire, err := protocol.MustGet(protocol.Gemini).EncodeRequest(req, d)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	if !strings.Contains(string(wire), "googleSearch") {
		t.Fatalf("the built-in web search tool never reached Gemini:\n%s", wire)
	}
	if !strings.Contains(string(wire), "get_weather") {
		t.Errorf("the function tool was lost on encode:\n%s", wire)
	}
	// safetySettings has no canonical form either, and must come back too.
	if !strings.Contains(string(wire), "safetySettings") {
		t.Errorf("safetySettings did not reach Gemini:\n%s", wire)
	}

	// And it is reported as carried, not as dropped.
	for _, n := range d.All() {
		if n.Field == "tools" && n.Fidelity == canonical.FidelityUnsupported {
			t.Errorf("a forwarded tool is still reported as unsupported: %s", n.Detail)
		}
	}
}

// A server tool Google has not shipped yet must survive too, or this breaks
// again on their next release.
func TestAnUnknownGeminiServerToolIsStillCarried(t *testing.T) {
	body := `{
		"contents": [{"role": "user", "parts": [{"text": "hi"}]}],
		"tools": [{"someToolInventedNextYear": {"mode": "AUTO"}}]
	}`
	req, _ := decodeInto(t, protocol.Gemini, body)
	req.Model = "gemini-9"

	if req.NativeTools.Len() != 1 {
		t.Fatalf("an unrecognised server tool was dropped: %v", req.NativeTools.Names())
	}
	wire, err := protocol.MustGet(protocol.Gemini).EncodeRequest(req, canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	if !strings.Contains(string(wire), "someToolInventedNextYear") {
		t.Errorf("a server tool this codec does not know was lost:\n%s", wire)
	}
	if !strings.Contains(string(wire), `"mode":"AUTO"`) {
		t.Errorf("its configuration was not replayed verbatim:\n%s", wire)
	}
}

// Cross-protocol they still cannot be honoured, and that must be said rather
// than forwarded as something the target will reject.
func TestNativeToolsAreReportedAcrossProtocols(t *testing.T) {
	req, _ := decodeInto(t, protocol.Gemini, geminiSearchRequest)
	req.Model = "m"

	for _, target := range []protocol.Name{protocol.OpenAI, protocol.Anthropic, protocol.OpenAIResponses} {
		t.Run(string(target), func(t *testing.T) {
			d := canonical.NewDiagnostics()
			wire, err := protocol.MustGet(target).EncodeRequest(req, d)
			if err != nil {
				t.Fatalf("EncodeRequest: %v", err)
			}
			if strings.Contains(string(wire), "googleSearch") {
				t.Errorf("a Gemini server tool was sent to %s:\n%s", target, wire)
			}
			var noted bool
			for _, n := range d.All() {
				if n.Field == "tools" && n.Fidelity == canonical.FidelityUnsupported &&
					strings.Contains(n.Detail, "googleSearch") {
					noted = true
				}
			}
			if !noted {
				t.Error("a dropped server tool produced no note naming it")
			}
		})
	}
}

func TestResponsesStateAndNativeContentAreReportedAcrossProtocols(t *testing.T) {
	req, _ := decodeInto(t, protocol.OpenAIResponses, `{
		"model":"gpt-5","previous_response_id":"resp_1","store":true,
		"input":[
			{"type":"web_search_call","id":"ws_1","status":"completed"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}
		]
	}`)
	d := canonical.NewDiagnostics()
	wire, err := protocol.MustGet(protocol.OpenAI).EncodeRequest(req, d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "web_search_call") || strings.Contains(string(wire), "resp_1") {
		t.Fatalf("Responses-only fields leaked into Chat Completions: %s", wire)
	}
	fields := map[string]bool{}
	for _, n := range d.All() {
		if n.Fidelity == canonical.FidelityUnsupported {
			fields[n.Field] = true
		}
	}
	for _, field := range []string{"previous_response_id", "store", "messages[0].content"} {
		if !fields[field] {
			t.Errorf("missing unsupported note for %s: %+v", field, d.All())
		}
	}
}

// Every protocol's own server tools survive its own route.
func TestNativeToolsRoundTripForEveryProtocol(t *testing.T) {
	cases := []struct {
		proto  protocol.Name
		body   string
		marker string
	}{
		{protocol.OpenAI,
			`{"model":"m","messages":[{"role":"user","content":"hi"}],` +
				`"tools":[{"type":"web_search_preview"}]}`, "web_search_preview"},
		{protocol.Anthropic,
			`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}],` +
				`"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":3}]}`, "web_search_20250305"},
		{protocol.OpenAIResponses,
			`{"model":"m","input":"hi","tools":[{"type":"file_search","vector_store_ids":["vs_1"]}]}`, "vector_store_ids"},
	}
	for _, tc := range cases {
		t.Run(string(tc.proto), func(t *testing.T) {
			req, _ := decodeInto(t, tc.proto, tc.body)
			req.Model = "upstream-model"
			if req.NativeTools.Len() != 1 {
				t.Fatalf("captured %v, want one server tool", req.NativeTools.Names())
			}
			wire, err := protocol.MustGet(tc.proto).EncodeRequest(req, canonical.NewDiagnostics())
			if err != nil {
				t.Fatalf("EncodeRequest: %v", err)
			}
			if !strings.Contains(string(wire), tc.marker) {
				t.Errorf("the server tool did not survive %s -> %s:\n%s", tc.proto, tc.proto, wire)
			}
		})
	}
}

// A search answer without its citations is only half the feature. Gemini puts
// them on the candidate, not at the top level.
func TestGeminiGroundingMetadataReachesTheClient(t *testing.T) {
	upstream := `{
		"candidates": [{
			"content": {"role": "model", "parts": [{"text": "It rained."}]},
			"finishReason": "STOP",
			"groundingMetadata": {
				"webSearchQueries": ["weather today"],
				"groundingChunks": [{"web": {"uri": "https://example.com", "title": "Example"}}]
			}
		}],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 3, "totalTokenCount": 8},
		"modelVersion": "gemini-3-flash"
	}`

	codec := protocol.MustGet(protocol.Gemini)
	resp, err := codec.DecodeResponse([]byte(upstream), canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if resp.Extensions.Len() == 0 {
		t.Fatal("groundingMetadata was not captured from the candidate")
	}

	wire, err := codec.EncodeResponse(resp, &canonical.Request{Model: "m"}, canonical.NewDiagnostics())
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	for _, want := range []string{"groundingMetadata", "weather today", "https://example.com"} {
		if !strings.Contains(string(wire), want) {
			t.Errorf("%q did not reach the client; the search citations are lost:\n%s", want, wire)
		}
	}
}

// Audio is not converted yet. Without an explicit check it falls through to
// "file" and gets sent as a document, which the target rejects — turning "not
// implemented" into "silently sends a malformed request". Reporting it is the
// honest behaviour, and this pins it until audio is actually implemented.
func TestAudioIsReportedNotSmuggledThroughAsADocument(t *testing.T) {
	cases := []struct {
		proto protocol.Name
		body  string
	}{
		{protocol.Gemini, `{"contents":[{"role":"user","parts":[
			{"text":"transcribe this"},
			{"inlineData":{"mimeType":"audio/mpeg","data":"SUQzBA=="}}]}]}`},
		{protocol.OpenAI, `{"model":"m","messages":[{"role":"user","content":[
			{"type":"text","text":"transcribe this"},
			{"type":"file","file":{"filename":"a.mp3","file_data":"data:audio/mpeg;base64,SUQzBA=="}}]}]}`},
		{protocol.Anthropic, `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":[
			{"type":"text","text":"transcribe this"},
			{"type":"document","source":{"type":"base64","media_type":"audio/mpeg","data":"SUQzBA=="}}]}]}`},
		{protocol.OpenAIResponses, `{"model":"m","input":[{"type":"message","role":"user","content":[
			{"type":"input_text","text":"transcribe this"},
			{"type":"input_file","filename":"a.mp3","file_data":"data:audio/mpeg;base64,SUQzBA=="}]}]}`},
	}

	for _, tc := range cases {
		t.Run(string(tc.proto), func(t *testing.T) {
			req, d := decodeInto(t, tc.proto, tc.body)

			for _, m := range req.Messages {
				for _, p := range m.Content {
					if p.Type == canonical.PartFile || p.Type == canonical.PartImage {
						t.Errorf("audio was decoded as %q; it is not converted yet", p.Type)
					}
				}
			}
			var noted bool
			for _, n := range d.All() {
				if n.Fidelity == canonical.FidelityUnsupported && strings.Contains(n.Detail, "audio/mpeg") {
					noted = true
				}
			}
			if !noted {
				t.Errorf("audio was dropped without saying so: %+v", d.All())
			}

			// The rest of the turn is untouched: one unconvertible attachment
			// costs the attachment, not the conversation.
			if req.Messages[0].TextContent() != "transcribe this" {
				t.Errorf("the text was lost along with the audio: %+v", req.Messages[0])
			}

			// And the audio must not reach any upstream in a document's clothing.
			req.Model = "m"
			for _, target := range []protocol.Name{protocol.OpenAI, protocol.Gemini, protocol.Anthropic} {
				wire, err := protocol.MustGet(target).EncodeRequest(req, canonical.NewDiagnostics())
				if err != nil {
					t.Fatalf("EncodeRequest(%s): %v", target, err)
				}
				if strings.Contains(string(wire), "audio/mpeg") {
					t.Errorf("audio reached %s as an attachment:\n%s", target, wire)
				}
			}
		})
	}
}
