package store

import (
	"context"
	"path/filepath"
	"testing"
)

// A model's own protocol wins over its provider's, an alias follows the model
// it points at, and a sync that names no protocol never erases one.
func TestModelProtocolOverride(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "polyglot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tm := st.ForTeam(DefaultTeamID)

	vendor, err := tm.CreateProvider(ctx, &Provider{Name: "multi", Protocol: "openai", BaseURL: "https://x.example", Vendor: "commandcode", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := tm.CreateProvider(ctx, &Provider{Name: "plain", Protocol: "openai", BaseURL: "https://y.example", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if vendor.Vendor != "commandcode" || plain.Vendor != "" {
		t.Fatalf("vendor not stored: %q / %q", vendor.Vendor, plain.Vendor)
	}

	if _, err := tm.SyncModels(ctx, vendor.ID, []DiscoveredModel{
		{ID: "claude-x", Protocol: "anthropic"}, {ID: "glm"},
	}); err != nil {
		t.Fatal(err)
	}
	m, err := tm.ModelForProvider(ctx, vendor.ID, "claude-x")
	if err != nil {
		t.Fatal(err)
	}
	if m.Protocol != "anthropic" || m.ProtocolOverride != "anthropic" || m.ProviderVendor != "commandcode" {
		t.Errorf("claude-x = protocol %q override %q vendor %q", m.Protocol, m.ProtocolOverride, m.ProviderVendor)
	}
	g, _ := tm.ModelForProvider(ctx, vendor.ID, "glm")
	if g.Protocol != "openai" || g.ProtocolOverride != "" {
		t.Errorf("glm = protocol %q override %q, want the provider's", g.Protocol, g.ProtocolOverride)
	}

	// A later sync with no protocol keeps it; one with a protocol replaces it.
	tm.SyncModels(ctx, vendor.ID, []DiscoveredModel{{ID: "claude-x"}})
	if m, _ = tm.ModelForProvider(ctx, vendor.ID, "claude-x"); m.ProtocolOverride != "anthropic" {
		t.Errorf("a sync without a protocol erased it: %q", m.ProtocolOverride)
	}
	tm.SyncModels(ctx, vendor.ID, []DiscoveredModel{{ID: "claude-x", Protocol: "openai-responses"}})
	if m, _ = tm.ModelForProvider(ctx, vendor.ID, "claude-x"); m.ProtocolOverride != "openai-responses" {
		t.Errorf("a sync with a protocol did not update it: %q", m.ProtocolOverride)
	}

	// Aliases: one pointing at the model reports its protocol; one pointing at
	// an unregistered name falls back to the provider's.
	for _, a := range []ModelAlias{
		{Alias: "fast", ProviderID: vendor.ID, UpstreamModel: "claude-x", Enabled: true},
		{Alias: "glm", ProviderID: vendor.ID, Enabled: true}, // no upstream name: forwards "glm"
		{Alias: "ghost", ProviderID: vendor.ID, UpstreamModel: "nope", Enabled: true},
	} {
		if _, err := tm.CreateAlias(ctx, &a); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string][2]string{"fast": {"openai-responses", "openai-responses"}, "glm": {"openai", ""}, "ghost": {"openai", ""}}
	for alias, w := range want {
		rows, err := tm.AliasesFor(ctx, alias)
		if err != nil || len(rows) != 1 {
			t.Fatalf("alias %s: %v %d", alias, err, len(rows))
		}
		if rows[0].Protocol != w[0] || rows[0].ProtocolOverride != w[1] {
			t.Errorf("alias %s = protocol %q override %q, want %v", alias, rows[0].Protocol, rows[0].ProtocolOverride, w)
		}
	}
}
