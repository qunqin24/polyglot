package provider

import (
	"context"
	"testing"

	"github.com/qunqin24/polyglot/internal/protocol"
)

func TestOpenCodeListingNamesEachModelsProtocol(t *testing.T) {
	v, _ := VendorFor(VendorOpenCodeGo)
	got, err := v.ParseModels([]byte(`{"data":[
	 {"id":"kimi-k3","name":"Kimi K3","type":"chat","recommended_api":"chat-completions","api":{"chat-completions":{}}},
	 {"id":"gpt-6-luna","type":"chat","recommended_api":"responses","api":{"responses":{}}},
	 {"id":"qwen4-max","type":"chat","recommended_api":"messages","api":{"messages":{}}},
	 {"id":"gemini-4-argon","type":"chat","recommended_api":"interactions","api":{"generate-content":{},"interactions":{}}},
	 {"id":"odd","type":"chat","recommended_api":"","api":{"messages":{}}},
	 {"id":"claude-new","type":"chat"},
	 {"id":"draw","type":"image","recommended_api":"images"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]protocol.Name{
		"kimi-k3":        protocol.OpenAI,
		"gpt-6-luna":     protocol.OpenAIResponses,
		"qwen4-max":      protocol.Anthropic,
		"gemini-4-argon": protocol.Gemini, // interactions is not served on Go; generateContent is
		"odd":            protocol.Anthropic,
		"claude-new":     protocol.Anthropic, // listing silent: the id rule decides
	}
	if len(got) != len(want) {
		t.Fatalf("got %d models, want %d (non-chat models are skipped): %+v", len(got), len(want), got)
	}
	for _, m := range got {
		if m.Protocol != want[m.ID] {
			t.Errorf("%s: protocol %q, want %q", m.ID, m.Protocol, want[m.ID])
		}
	}
}

func TestCommandCodeListingPicksProtocolFromSupportedEndpoints(t *testing.T) {
	v, _ := VendorFor(VendorCommandCode)
	got, err := v.ParseModels([]byte(`{"data":[
	 {"id":"claude-opus-5","supported_endpoints":["/messages"]},
	 {"id":"gpt-5.5","supported_endpoints":["/chat/completions","/responses"]},
	 {"id":"zai-org/GLM-5","supported_endpoints":["/chat/completions","/responses"]},
	 {"id":"tencent/hy4","supported_endpoints":["/chat/completions"]},
	 {"id":"resp-only","supported_endpoints":["/responses"]},
	 {"id":"unlisted"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]protocol.Name{
		"claude-opus-5": protocol.Anthropic, "gpt-5.5": protocol.OpenAIResponses,
		"zai-org/GLM-5": protocol.OpenAI, "tencent/hy4": protocol.OpenAI,
		"resp-only": protocol.OpenAIResponses, "unlisted": protocol.OpenAI,
	}
	for _, m := range got {
		if m.Protocol != want[m.ID] {
			t.Errorf("%s: protocol %q, want %q", m.ID, m.Protocol, want[m.ID])
		}
	}
}

func TestVendorProtocolForNeverReturnsAProtocolItDoesNotServe(t *testing.T) {
	v, _ := VendorFor(VendorCommandCode)
	if got := v.ProtocolFor(protocol.Gemini, "x"); got != protocol.OpenAI {
		t.Errorf("unserved override gave %q", got)
	}
	if got := v.ProtocolFor(protocol.Anthropic, "gpt-5"); got != protocol.Anthropic {
		t.Errorf("explicit override ignored: %q", got)
	}
	if got := v.ProtocolFor("", "claude-x"); got != protocol.Anthropic {
		t.Errorf("rule for claude gave %q", got)
	}
}

func TestVendorApplySetsURLAuthAndKeepsOperatorHeadersIntact(t *testing.T) {
	v, _ := VendorFor(VendorOpenCodeGo)
	stored := map[string]string{"X-Team": "a"}
	tg := &Target{APIKey: "oc_sk_1", Headers: stored}
	v.Apply(tg, protocol.Anthropic, 7)
	if tg.Protocol != protocol.Anthropic || tg.BaseURL != "https://opencode.ai/inference/go/anthropic" || !tg.BearerAuth {
		t.Fatalf("target = %+v", tg)
	}
	if len(stored) != 1 {
		t.Error("Apply wrote into the stored provider's header map")
	}
	if tg.Headers["X-Team"] != "a" || tg.Headers["x-opencode-session"] == "" {
		t.Errorf("headers = %v", tg.Headers)
	}
	other := &Target{APIKey: "oc_sk_1"}
	v.Apply(other, protocol.OpenAI, 7)
	if other.Headers["x-opencode-session"] != tg.Headers["x-opencode-session"] {
		t.Error("session id differs between protocols of one provider")
	}
	third := &Target{APIKey: "oc_sk_1"}
	v.Apply(third, protocol.OpenAI, 8)
	if third.Headers["x-opencode-session"] == tg.Headers["x-opencode-session"] {
		t.Error("two providers share a session id")
	}
}

func TestAnthropicBearerOnlyWhenAsked(t *testing.T) {
	ctx := context.Background()
	plain, _ := (anthropicDriver{}).ChatRequest(ctx, &Target{BaseURL: "https://api.anthropic.com", APIKey: "k"}, "m", []byte(`{}`), false)
	if plain.Header.Get("x-api-key") != "k" || plain.Header.Get("Authorization") != "" {
		t.Errorf("plain: x-api-key=%q Authorization=%q", plain.Header.Get("x-api-key"), plain.Header.Get("Authorization"))
	}
	bearer, _ := (anthropicDriver{}).ChatRequest(ctx, &Target{BaseURL: "https://x.example", APIKey: "k", BearerAuth: true}, "m", []byte(`{}`), false)
	if bearer.Header.Get("Authorization") != "Bearer k" || bearer.Header.Get("x-api-key") != "k" {
		t.Errorf("bearer: x-api-key=%q Authorization=%q", bearer.Header.Get("x-api-key"), bearer.Header.Get("Authorization"))
	}
	if bearer.URL.String() != "https://x.example/v1/messages" {
		t.Errorf("URL = %s", bearer.URL)
	}
}
