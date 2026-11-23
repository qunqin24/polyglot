package router_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/qunqin24/polyglot/internal/protocol"
	"github.com/qunqin24/polyglot/internal/router"
	"github.com/qunqin24/polyglot/internal/store"
)

// Whichever way a model is reached — bare id, provider::model, or an alias —
// it runs on its own protocol at that protocol's URL; an ordinary provider is
// untouched.
func TestResolveUsesTheModelsProtocolOnAVendorProvider(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tm := st.ForTeam(store.DefaultTeamID)

	cc, err := tm.CreateProvider(ctx, &store.Provider{Name: "Command Code", Protocol: "openai",
		BaseURL: "https://api.commandcode.ai/provider", Vendor: "commandcode", APIKey: "k", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tm.SyncModels(ctx, cc.ID, []store.DiscoveredModel{
		{ID: "claude-sonnet-5", Protocol: "anthropic"},
		{ID: "gpt-5.5", Protocol: "openai-responses"},
		{ID: "glm-5", Protocol: "openai"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tm.CreateAlias(ctx, &store.ModelAlias{Alias: "smart", ProviderID: cc.ID, UpstreamModel: "claude-sonnet-5", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// An alias at a model nobody registered still gets the vendor's rule.
	if _, err := tm.CreateAlias(ctx, &store.ModelAlias{Alias: "unlisted", ProviderID: cc.ID, UpstreamModel: "claude-brand-new", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	plain, err := tm.CreateProvider(ctx, &store.Provider{Name: "plain", Protocol: "anthropic", BaseURL: "https://api.anthropic.com", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tm.SyncModels(ctx, plain.ID, []store.DiscoveredModel{{ID: "claude-plain"}}); err != nil {
		t.Fatal(err)
	}

	r := router.New(time.Minute)
	cases := []struct {
		ask   string
		proto protocol.Name
		base  string
	}{
		{"claude-sonnet-5", protocol.Anthropic, "https://api.commandcode.ai/provider"},
		{"gpt-5.5", protocol.OpenAIResponses, "https://api.commandcode.ai/provider/v1"},
		{"glm-5", protocol.OpenAI, "https://api.commandcode.ai/provider/v1"},
		{"Command Code::gpt-5.5", protocol.OpenAIResponses, "https://api.commandcode.ai/provider/v1"},
		{"smart", protocol.Anthropic, "https://api.commandcode.ai/provider"},
		{"unlisted", protocol.Anthropic, "https://api.commandcode.ai/provider"},
		{"claude-plain", protocol.Anthropic, "https://api.anthropic.com"},
	}
	for _, tc := range cases {
		res, err := r.Resolve(ctx, tm, tc.ask)
		if err != nil || len(res) != 1 {
			t.Fatalf("%s: %v (%d candidates)", tc.ask, err, len(res))
		}
		got := res[0].Target
		if got.Protocol != tc.proto || got.BaseURL != tc.base {
			t.Errorf("%s -> %s %s, want %s %s", tc.ask, got.Protocol, got.BaseURL, tc.proto, tc.base)
		}
	}
	res, _ := r.Resolve(ctx, tm, "claude-plain")
	if res[0].Target.BearerAuth || res[0].Target.Vendor != nil {
		t.Error("an ordinary provider picked up vendor behaviour")
	}
}
