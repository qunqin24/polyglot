package protocol_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/protocol"

	_ "github.com/qunqin24/polyglot/internal/protocol/anthropic"
	_ "github.com/qunqin24/polyglot/internal/protocol/gemini"
	_ "github.com/qunqin24/polyglot/internal/protocol/interactions"
	_ "github.com/qunqin24/polyglot/internal/protocol/openai"
	_ "github.com/qunqin24/polyglot/internal/protocol/responses"
)

// These tests walk every ordered pair of protocols through the canonical hub.
// Because each codec only implements Protocol <-> Canonical, covering the full
// 5x5 matrix costs nothing extra — which is the whole argument for the design.

func allProtocols() []protocol.Name {
	return []protocol.Name{
		protocol.OpenAI, protocol.Anthropic, protocol.Gemini, protocol.OpenAIResponses,
		protocol.GeminiInteractions,
	}
}

// sampleRequest exercises the features that must survive any conversion.
func sampleRequest() *canonical.Request {
	return &canonical.Request{
		Model:  "target-model",
		System: []canonical.ContentPart{canonical.Text("You are terse.")},
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				canonical.Text("What is the weather in Paris?"),
				// Inline base64 is the shape every protocol can express, so it
				// must survive all sixteen pairings rather than merely be noted.
				canonical.ImagePart("image/png", "iVBORw0KGgo="),
				canonical.FilePart("application/pdf", "report.pdf", "JVBERi0xLjQK"),
			}},
			{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{{
				Type: canonical.PartToolCall,
				ToolCall: &canonical.ToolCall{
					ID: "call_1", Name: "get_weather", Arguments: json.RawMessage(`{"city":"Paris"}`),
					Signature: "sig-abc",
				},
			}}},
			{Role: canonical.RoleTool, Content: []canonical.ContentPart{{
				Type: canonical.PartToolResult,
				ToolResult: &canonical.ToolResult{
					ToolCallID: "call_1",
					Name:       "get_weather",
					Content:    []canonical.ContentPart{canonical.Text("18C and sunny")},
				},
			}}},
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{canonical.Text("Thanks!")}},
		},
		Tools: []canonical.Tool{{
			Name:        "get_weather",
			Description: "Look up the weather",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
		}},
		ToolChoice:  &canonical.ToolChoice{Mode: canonical.ToolChoiceAuto},
		Temperature: canonical.Ptr(0.3),
		TopP:        canonical.Ptr(0.9),
		MaxTokens:   canonical.Ptr(512),
		Stop:        []string{"END"},

		// Dialect-specific baggage. Neither can cross a protocol boundary, so
		// the matrix holds them to the other half of the rule: they must be
		// reported, every single time.
		Extensions: &canonical.Extensions{
			Protocol: "openai",
			Items: []canonical.Extension{
				{Name: "guided_json", Value: json.RawMessage(`{"type":"object"}`)},
			},
		},
		NativeTools: &canonical.NativeTools{
			Protocol: "gemini",
			Items: []canonical.NativeTool{
				{Name: "googleSearch", Raw: json.RawMessage(`{"googleSearch":{}}`)},
			},
		},
	}
}

// TestRequestMatrix encodes one canonical request into every protocol, decodes
// it back, and checks the meaning survived.
func TestRequestMatrix(t *testing.T) {
	for _, target := range allProtocols() {
		t.Run(string(target), func(t *testing.T) {
			codec := protocol.MustGet(target)

			d := canonical.NewDiagnostics()
			wire, err := codec.EncodeRequest(sampleRequest(), d)
			if err != nil {
				t.Fatalf("EncodeRequest: %v", err)
			}
			if !json.Valid(wire) {
				t.Fatalf("encoded request is not valid JSON: %s", wire)
			}

			back, err := codec.DecodeRequest(wire, canonical.NewDiagnostics())
			if err != nil {
				t.Fatalf("DecodeRequest after encode: %v\nwire: %s", err, wire)
			}

			// System prompt.
			if got := textOf(back.System); !strings.Contains(got, "You are terse.") {
				t.Errorf("system prompt lost: %q", got)
			}
			// Tool definition.
			if len(back.Tools) != 1 || back.Tools[0].Name != "get_weather" {
				t.Errorf("tool definition lost: %+v", back.Tools)
			}
			// Sampling parameters.
			if back.Temperature == nil || *back.Temperature != 0.3 {
				t.Errorf("temperature lost")
			}
			if back.MaxTokens == nil || *back.MaxTokens != 512 {
				t.Errorf("max tokens lost: %v", back.MaxTokens)
			}
			// Not every protocol can express stop sequences. The rule is not
			// "it must survive" but "it must survive or be recorded" — a
			// field may never disappear in silence.
			if len(back.Stop) != 1 || back.Stop[0] != "END" {
				if !hasNoteFor(d, "stop") {
					t.Errorf("stop sequences vanished with no fidelity note: %v\nnotes: %+v",
						back.Stop, d.All())
				}
			}

			// The tool call and its result must both survive, and must still
			// refer to each other.
			call := findToolCall(back)
			result := findToolResult(back)
			if call == nil {
				t.Fatalf("tool call lost through %s: %s", target, wire)
			}
			if result == nil {
				t.Fatalf("tool result lost through %s: %s", target, wire)
			}
			if call.Name != "get_weather" {
				t.Errorf("tool call name = %q", call.Name)
			}
			if string(call.Arguments) != `{"city":"Paris"}` {
				t.Errorf("tool call arguments = %s", call.Arguments)
			}
			if call.ID != result.ToolCallID {
				t.Errorf("tool call/result pairing broken through %s: %q vs %q",
					target, call.ID, result.ToolCallID)
			}
			if got := textOf(result.Content); !strings.Contains(got, "18C and sunny") {
				t.Errorf("tool result content = %q", got)
			}
			// Only Gemini's format has a field for a thought signature, so on
			// the way to an upstream it is dropped — but never in silence.
			if call.Signature != "sig-abc" && !hasNoteFor(d, "signature") {
				t.Errorf("tool call signature vanished with no fidelity note through %s\nnotes: %+v",
					target, d.All())
			}

			// Inline attachments convert in every direction; there is no
			// protocol here that cannot express base64 bytes with a type.
			img := findMedia(back, canonical.PartImage)
			if img == nil {
				t.Fatalf("the image was lost through %s: %s", target, wire)
			}
			if img.Data != "iVBORw0KGgo=" || img.MIMEType != "image/png" {
				t.Errorf("image did not round-trip through %s: %+v", target, img)
			}
			doc := findMedia(back, canonical.PartFile)
			if doc == nil {
				t.Fatalf("the document was lost through %s: %s", target, wire)
			}
			if doc.Data != "JVBERi0xLjQK" {
				t.Errorf("document payload did not round-trip through %s: %+v", target, doc)
			}

			// The dialect-bound pieces may not cross, and may not vanish
			// quietly either. Gemini is the source protocol of the native tool
			// and openai of the extension, so exactly one of each is carried.
			if target != protocol.OpenAI && !hasNoteFor(d, "extensions") {
				t.Errorf("an openai-only field crossed to %s with no note\nnotes: %+v", target, d.All())
			}
			if target != protocol.Gemini && !hasNoteFor(d, "tools") {
				t.Errorf("a Gemini server tool crossed to %s with no note\nnotes: %+v", target, d.All())
			}
		})
	}
}

// TestResponseMatrix does the same for a model reply.
func TestResponseMatrix(t *testing.T) {
	created := time.Date(2026, 8, 16, 10, 20, 47, 0, time.UTC)
	resp := &canonical.Response{
		ID:      "resp_1",
		Model:   "target-model",
		Created: created,
		Message: canonical.Message{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{
			canonical.Text("It is 18C."),
			{Type: canonical.PartToolCall, ToolCall: &canonical.ToolCall{
				ID: "call_9", Name: "log_result", Arguments: json.RawMessage(`{"ok":true}`),
			}},
		}},
		FinishReason: canonical.FinishToolCalls,
		// A prompt of 42 tokens of which 30 came from cache. The cached part is
		// inside the total, never added to it — see canonical.Usage.
		Usage: canonical.Usage{InputTokens: 42, OutputTokens: 7, CachedInputTokens: 30},
	}

	for _, target := range allProtocols() {
		t.Run(string(target), func(t *testing.T) {
			codec := protocol.MustGet(target)
			wire, err := codec.EncodeResponse(resp, &canonical.Request{Model: "target-model"}, canonical.NewDiagnostics())
			if err != nil {
				t.Fatalf("EncodeResponse: %v", err)
			}
			back, err := codec.DecodeResponse(wire, canonical.NewDiagnostics())
			if err != nil {
				t.Fatalf("DecodeResponse: %v\nwire: %s", err, wire)
			}
			if back.Message.TextContent() != "It is 18C." {
				t.Errorf("text = %q", back.Message.TextContent())
			}
			calls := back.ToolCalls()
			if len(calls) != 1 || calls[0].Name != "log_result" {
				t.Fatalf("tool calls = %+v", calls)
			}
			if string(calls[0].Arguments) != `{"ok":true}` {
				t.Errorf("arguments = %s", calls[0].Arguments)
			}
			if back.FinishReason != canonical.FinishToolCalls {
				t.Errorf("finish reason = %q", back.FinishReason)
			}
			if back.Usage.InputTokens != 42 || back.Usage.OutputTokens != 7 {
				t.Errorf("usage = %+v", back.Usage)
			}
			// The cached portion, and the invariant that makes it comparable
			// across protocols: it is part of the prompt total, not an extra on
			// top of it. Anthropic reports its three input counters separately
			// and its codec converts both ways; if that conversion is dropped
			// the totals here stop agreeing and a client is handed a cached
			// count larger than the prompt it supposedly came from.
			if back.Usage.CachedInputTokens != 30 {
				t.Errorf("cached input tokens = %d, want 30\nwire: %s",
					back.Usage.CachedInputTokens, wire)
			}
			if back.Usage.CachedInputTokens > back.Usage.InputTokens {
				t.Errorf("cached (%d) exceeds the prompt it is part of (%d)",
					back.Usage.CachedInputTokens, back.Usage.InputTokens)
			}
			// The response timestamp. Every wire format that has one must carry
			// the upstream's value rather than stamping the local clock: a
			// protocol that decodes it into Created gets it converted to the
			// client's spelling, while one that leaves it to Capture would
			// replay it on a same-protocol route only and report it as
			// unsupported on the other four — a loss note for a field that has
			// an exact counterpart.
			if noResponseTimestamp[target] {
				return
			}
			if !back.Created.Equal(created) {
				t.Errorf("created = %s, want %s\nwire: %s", back.Created, created, wire)
			}
		})
	}
}

// noResponseTimestamp lists the protocols whose response really has no
// timestamp field, so there is nothing for the check above to compare. Adding
// an entry here is a claim about a vendor's wire format, not a way to excuse a
// codec that forgot to read one.
var noResponseTimestamp = map[protocol.Name]bool{
	// Anthropic's Messages response carries no timestamp at all; a client
	// reads the HTTP Date header instead.
	protocol.Anthropic: true,
}

// TestAResponseTimestampIsConvertedRatherThanReportedAsALoss starts from a real
// Gemini reply rather than a canonical value, because that is where the bug
// this pins came from: createTime was on the wire, no struct member named it,
// so Capture filed it as a Gemini-specific extension and every OpenAI client
// got told a field had been dropped — for a value OpenAI spells "created" and
// has always been able to carry.
//
// The general rule, and the reason this is worth a test of its own: a field
// with an exact counterpart in another protocol belongs in canonical.
// Passthrough is for what canonical cannot express, and using it for something
// convertible turns a clean conversion into a false loss report.
func TestAResponseTimestampIsConvertedRatherThanReportedAsALoss(t *testing.T) {
	const wire = `{
		"candidates": [{
			"content": {"role": "model", "parts": [{"text": "It is 18C."}]},
			"finishReason": "STOP", "index": 0
		}],
		"usageMetadata": {"promptTokenCount": 47, "candidatesTokenCount": 341},
		"modelVersion": "gemini-3.5-flash",
		"responseId": "resp-abc",
		"createTime": "2026-08-16T10:20:47.123456Z"
	}`
	want := time.Date(2026, 8, 16, 10, 20, 47, 123456000, time.UTC)

	d := canonical.NewDiagnostics()
	resp, err := protocol.MustGet(protocol.Gemini).DecodeResponse([]byte(wire), d.WithStage("decode:gemini"))
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if !resp.Created.Equal(want) {
		t.Errorf("canonical Created = %s, want %s", resp.Created.UTC(), want)
	}

	out, err := protocol.MustGet(protocol.OpenAI).EncodeResponse(resp,
		&canonical.Request{Model: "gemini-3.5-flash"}, d)
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}

	var got struct {
		Created int64 `json:"created"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Created != want.Unix() {
		t.Errorf("openai created = %d, want %d", got.Created, want.Unix())
	}

	for _, n := range d.All() {
		if strings.Contains(n.Detail, "createTime") {
			t.Errorf("createTime reported as a loss, but it converted exactly: %+v", n)
		}
	}
}

// TestAnthropicCacheCountsAreConvertedNotCopied starts from a real Anthropic
// reply, because that is the one protocol of the five that counts a cached
// prompt differently: its input_tokens is what the cache did NOT serve, while
// OpenAI, Responses and Gemini all report a prompt total with the cached part
// named inside it.
//
// Copying the field across unchanged produced a reply that cannot exist in the
// target's own accounting — 10 prompt tokens of which 5000 were cached — and
// under-counted every cached Anthropic request in the usage totals by exactly
// the part the cache served, which is usually most of it.
func TestAnthropicCacheCountsAreConvertedNotCopied(t *testing.T) {
	const wire = `{
		"id":"msg_1","type":"message","role":"assistant","model":"claude",
		"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn",
		"usage":{"input_tokens":10,"output_tokens":5,
		         "cache_read_input_tokens":5000,"cache_creation_input_tokens":200}
	}`
	// 10 fresh + 5000 read back + 200 written: the model read 5210 tokens.
	const wantInput = 5210

	d := canonical.NewDiagnostics()
	resp, err := protocol.MustGet(protocol.Anthropic).DecodeResponse([]byte(wire), d)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if resp.Usage.InputTokens != wantInput {
		t.Errorf("input tokens = %d, want %d", resp.Usage.InputTokens, wantInput)
	}
	if resp.Usage.CachedInputTokens != 5000 || resp.Usage.CacheWriteTokens != 200 {
		t.Errorf("cache split = %+v", resp.Usage)
	}

	// Back to Anthropic: the three counters must come apart exactly as they
	// went in, or a round trip through Polyglot would change what the caller
	// is billed for.
	back, err := protocol.MustGet(protocol.Anthropic).EncodeResponse(resp,
		&canonical.Request{Model: "claude"}, d)
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	var anth struct {
		Usage struct {
			InputTokens   int `json:"input_tokens"`
			CacheRead     int `json:"cache_read_input_tokens"`
			CacheCreation int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(back, &anth); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if anth.Usage.InputTokens != 10 || anth.Usage.CacheRead != 5000 || anth.Usage.CacheCreation != 200 {
		t.Errorf("anthropic usage = %+v, want the original 10/5000/200", anth.Usage)
	}

	// To OpenAI: cached_tokens is a subset of prompt_tokens there, so the
	// prompt has to be the full 5210 for the pair to mean anything.
	od := canonical.NewDiagnostics()
	out, err := protocol.MustGet(protocol.OpenAI).EncodeResponse(resp,
		&canonical.Request{Model: "claude"}, od)
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	var oai struct {
		Usage struct {
			PromptTokens  int `json:"prompt_tokens"`
			PromptDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &oai); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if oai.Usage.PromptTokens != wantInput {
		t.Errorf("prompt_tokens = %d, want %d", oai.Usage.PromptTokens, wantInput)
	}
	if oai.Usage.PromptDetails.CachedTokens > oai.Usage.PromptTokens {
		t.Errorf("cached_tokens (%d) exceeds prompt_tokens (%d), which OpenAI's accounting cannot express",
			oai.Usage.PromptDetails.CachedTokens, oai.Usage.PromptTokens)
	}

	// OpenAI has no cache-write field. The 200 tokens survive inside the
	// prompt total, but the breakdown does not, and that has to be said.
	var noted bool
	for _, n := range od.All() {
		if strings.Contains(n.Field, "cache_write") {
			noted = true
		}
	}
	if !noted {
		t.Error("cache-write tokens were folded into the input total with no fidelity note")
	}
}

// sampleEvents is a canonical stream with reasoning, text and a fragmented
// tool call — the three things that break naive stream conversion.
func sampleEvents() []*canonical.Event {
	return []*canonical.Event{
		{Type: canonical.EventMessageStart, ID: "s1", Model: "target-model"},
		{Type: canonical.EventReasoningDelta, Index: 0, Text: "Let me "},
		{Type: canonical.EventReasoningDelta, Index: 0, Text: "check."},
		{Type: canonical.EventTextDelta, Index: 1, Text: "It is "},
		{Type: canonical.EventTextDelta, Index: 1, Text: "18C."},
		{Type: canonical.EventToolCallStart, Index: 2, ToolCallID: "call_7", ToolName: "get_weather"},
		{Type: canonical.EventToolCallDelta, Index: 2, ArgumentsDelta: `{"ci`},
		{Type: canonical.EventToolCallDelta, Index: 2, ArgumentsDelta: `ty":"Paris"}`},
		{Type: canonical.EventToolCallEnd, Index: 2},
		{Type: canonical.EventUsage, Usage: &canonical.Usage{InputTokens: 11, OutputTokens: 22}},
		{Type: canonical.EventMessageEnd, FinishReason: canonical.FinishToolCalls,
			Usage: &canonical.Usage{InputTokens: 11, OutputTokens: 22}},
	}
}

// TestStreamMatrix is the cross-protocol streaming guarantee: every protocol
// can carry the same canonical stream and hand it back unchanged.
func TestStreamMatrix(t *testing.T) {
	for _, target := range allProtocols() {
		t.Run(string(target), func(t *testing.T) {
			codec := protocol.MustGet(target)

			var buf strings.Builder
			enc := codec.NewStreamEncoder(&buf, &canonical.Request{Model: "target-model", IncludeUsage: true})
			for _, ev := range sampleEvents() {
				if err := enc.Write(ev); err != nil {
					t.Fatalf("write %s: %v", ev.Type, err)
				}
			}
			if err := enc.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}

			acc := canonical.NewAccumulator()
			err := codec.DecodeStream(t.Context(), strings.NewReader(buf.String()), func(ev *canonical.Event) error {
				acc.Add(ev)
				return nil
			})
			if err != nil {
				t.Fatalf("DecodeStream: %v\nstream:\n%s", err, buf.String())
			}

			got := acc.Response()
			if got.Message.TextContent() != "It is 18C." {
				t.Errorf("text = %q\nstream:\n%s", got.Message.TextContent(), buf.String())
			}
			calls := got.ToolCalls()
			if len(calls) != 1 {
				t.Fatalf("want 1 tool call, got %d\nstream:\n%s", len(calls), buf.String())
			}
			if calls[0].Name != "get_weather" {
				t.Errorf("tool name = %q", calls[0].Name)
			}
			// The fragments must have been reassembled into valid JSON.
			if string(calls[0].Arguments) != `{"city":"Paris"}` {
				t.Errorf("arguments = %s", calls[0].Arguments)
			}
			var parsed map[string]string
			if err := json.Unmarshal(calls[0].Arguments, &parsed); err != nil {
				t.Errorf("arguments are not valid JSON: %v", err)
			}
			if got.FinishReason != canonical.FinishToolCalls {
				t.Errorf("finish reason = %q", got.FinishReason)
			}
			if got.Usage.InputTokens != 11 || got.Usage.OutputTokens != 22 {
				t.Errorf("usage = %+v", got.Usage)
			}

			// All three protocols can carry streamed reasoning, each in its
			// own shape: reasoning_content, thinking blocks, thought parts.
			if reasoningText(got) != "Let me check." {
				t.Errorf("reasoning lost on %s: %q\n%s", target, reasoningText(got), buf.String())
			}
		})
	}
}

// TestEveryCodecEncodesErrors makes sure a client always gets an error in the
// protocol it was speaking.
func TestEveryCodecEncodesErrors(t *testing.T) {
	e := canonical.Errorf(canonical.ErrRateLimit, "slow down")
	for _, name := range allProtocols() {
		body := protocol.MustGet(name).EncodeError(e)
		if !json.Valid(body) {
			t.Errorf("%s produced invalid error JSON: %s", name, body)
		}
		if !strings.Contains(string(body), "slow down") {
			t.Errorf("%s dropped the error message: %s", name, body)
		}
	}
}

// hasNoteFor reports whether the conversion recorded something about a field.
func hasNoteFor(d *canonical.Diagnostics, field string) bool {
	for _, n := range d.All() {
		if strings.Contains(n.Field, field) {
			return true
		}
	}
	return false
}

func textOf(parts []canonical.ContentPart) string {
	var sb strings.Builder
	for _, p := range parts {
		if p.Type == canonical.PartText {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

func reasoningText(r *canonical.Response) string {
	var sb strings.Builder
	for _, p := range r.Message.Content {
		if p.Type == canonical.PartReasoning {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

func findToolCall(r *canonical.Request) *canonical.ToolCall {
	for _, m := range r.Messages {
		for _, p := range m.Content {
			if p.Type == canonical.PartToolCall && p.ToolCall != nil {
				return p.ToolCall
			}
		}
	}
	return nil
}

func findToolResult(r *canonical.Request) *canonical.ToolResult {
	for _, m := range r.Messages {
		for _, p := range m.Content {
			if p.Type == canonical.PartToolResult && p.ToolResult != nil {
				return p.ToolResult
			}
		}
	}
	return nil
}

// findMedia returns the first attachment of a kind anywhere in the request.
func findMedia(r *canonical.Request, kind canonical.PartType) *canonical.Media {
	for _, m := range r.Messages {
		for _, p := range m.Content {
			if p.Type == kind && p.Media != nil {
				return p.Media
			}
		}
	}
	return nil
}
