package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/qunqin24/polyglot/internal/pricing"
)

func priceStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "polyglot.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func usd(v float64) *float64 { return &v }

func snapshot(version string, entries ...pricing.Entry) *pricing.Snapshot {
	return &pricing.Snapshot{Version: version, Entries: entries}
}

func registerModel(t *testing.T, st *Store, providerName, modelID string) *Model {
	t.Helper()
	ctx := context.Background()
	p, err := st.CreateProvider(ctx, &Provider{
		Name: providerName, Protocol: "openai", BaseURL: "https://api.example.com", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	m, err := st.CreateModel(ctx, &Model{ProviderID: p.ID, UpstreamModelID: modelID, Enabled: true})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	return m
}

// The rule the model registry already follows, applied to prices: re-reading an
// upstream list never overwrites what the operator decided. A catalog refresh
// is the same act — it may not silently undo a price somebody typed because
// they are paying less than list.
func TestRefreshingTheCatalogDoesNotOverwriteAnOperatorsPrice(t *testing.T) {
	st := priceStore(t)
	ctx := context.Background()
	m := registerModel(t, st, "reseller", "claude-sonnet-4-5")

	if err := st.ReplaceCatalog(ctx, snapshot("2026-01-01", pricing.Entry{
		ID: "claude-sonnet-4-5", Vendor: "anthropic",
		Rates: pricing.Rates{Price: pricing.Price{Input: usd(3), Output: usd(15)}},
	}), "embedded"); err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if _, err := st.SetModelPrice(ctx, m.ID, pricing.Price{Input: usd(1), Output: usd(5)}); err != nil {
		t.Fatalf("set price: %v", err)
	}

	// A later catalog, with the official price changed.
	if err := st.ReplaceCatalog(ctx, snapshot("2026-06-01", pricing.Entry{
		ID: "claude-sonnet-4-5", Vendor: "anthropic",
		Rates: pricing.Rates{Price: pricing.Price{Input: usd(4), Output: usd(20)}},
	}), "models.dev"); err != nil {
		t.Fatalf("refresh catalog: %v", err)
	}

	overrides, err := st.ModelPriceOverrides(ctx)
	if err != nil {
		t.Fatalf("read overrides: %v", err)
	}
	got, ok := overrides[pricing.ModelKey{ProviderID: m.ProviderID, Model: "claude-sonnet-4-5"}]
	if !ok {
		t.Fatal("the operator's price is gone after a catalog refresh")
	}
	if got.Input == nil || *got.Input != 1 {
		t.Errorf("input price = %v, want the operator's 1", got.Input)
	}
}

// Clearing an override puts the model back on the catalog, and back on the
// catalog as it is now — not as it was when the override was written. That is
// the whole reason a blank field is stored as null rather than as a copy.
func TestClearingAnOverrideFollowsTheCatalogAgain(t *testing.T) {
	st := priceStore(t)
	ctx := context.Background()
	m := registerModel(t, st, "official", "gpt-5.5")

	if err := st.ReplaceCatalog(ctx, snapshot("2026-01-01", pricing.Entry{
		ID: "gpt-5.5", Vendor: "openai", Rates: pricing.Rates{Price: pricing.Price{Input: usd(5), Output: usd(30)}},
	}), "embedded"); err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if _, err := st.SetModelPrice(ctx, m.ID, pricing.Price{Input: usd(99)}); err != nil {
		t.Fatalf("set price: %v", err)
	}
	if _, err := st.SetModelPrice(ctx, m.ID, pricing.Price{}); err != nil {
		t.Fatalf("clear price: %v", err)
	}

	overrides, err := st.ModelPriceOverrides(ctx)
	if err != nil {
		t.Fatalf("read overrides: %v", err)
	}
	if _, ok := overrides[pricing.ModelKey{ProviderID: m.ProviderID, Model: "gpt-5.5"}]; ok {
		t.Error("a cleared price is still recorded as an override")
	}

	r := pricing.NewResolver(st)
	if err := r.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, src := r.Resolve(m.ProviderID, "gpt-5.5")
	if src != pricing.SourceCatalog || got.Input == nil || *got.Input != 5 {
		t.Errorf("resolved %v from %q, want the catalog's 5", got.Input, src)
	}
}

// A deliberate zero is a statement — this model is free — and must survive as
// one. Storing it as "nothing set" would put the model back on the catalog and
// start charging for something the operator said was free.
func TestAPriceOfZeroIsKeptAsAPriceNotAsBlank(t *testing.T) {
	st := priceStore(t)
	ctx := context.Background()
	m := registerModel(t, st, "local", "qwen3-32b")

	if err := st.ReplaceCatalog(ctx, snapshot("2026-01-01", pricing.Entry{
		ID: "qwen3-32b", Vendor: "alibaba", Rates: pricing.Rates{Price: pricing.Price{Input: usd(0.7), Output: usd(2.8)}},
	}), "embedded"); err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if _, err := st.SetModelPrice(ctx, m.ID, pricing.Price{Input: usd(0), Output: usd(0)}); err != nil {
		t.Fatalf("set price: %v", err)
	}

	r := pricing.NewResolver(st)
	if err := r.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, src := r.Resolve(m.ProviderID, "qwen3-32b")
	if src != pricing.SourceOverride || got.Input == nil || *got.Input != 0 {
		t.Errorf("resolved %v from %q, want the operator's stated zero", got.Input, src)
	}
}

// Upgrading the binary should bring newer official prices with it, but must
// never roll back a refresh the operator ran more recently.
func TestTheEmbeddedCatalogNeverReplacesANewerOne(t *testing.T) {
	st := priceStore(t)
	ctx := context.Background()

	if err := st.ReplaceCatalog(ctx, snapshot("2026-06-01", pricing.Entry{
		ID: "gpt-5.5", Vendor: "openai", Rates: pricing.Rates{Price: pricing.Price{Input: usd(4), Output: usd(24)}},
	}), "models.dev"); err != nil {
		t.Fatalf("operator refresh: %v", err)
	}

	// A binary built before that refresh.
	if err := st.LoadEmbeddedCatalog(ctx, snapshot("2026-01-01", pricing.Entry{
		ID: "gpt-5.5", Vendor: "openai", Rates: pricing.Rates{Price: pricing.Price{Input: usd(5), Output: usd(30)}},
	})); err != nil {
		t.Fatalf("load embedded: %v", err)
	}
	prices, err := st.CatalogPrices(ctx)
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	if p := prices["gpt-5.5"]; p.Input == nil || *p.Input != 4 {
		t.Errorf("input price = %v, want the operator's newer 4", p.Input)
	}

	// A newer binary does bring its prices.
	if err := st.LoadEmbeddedCatalog(ctx, snapshot("2026-09-01", pricing.Entry{
		ID: "gpt-5.5", Vendor: "openai", Rates: pricing.Rates{Price: pricing.Price{Input: usd(2), Output: usd(12)}},
	})); err != nil {
		t.Fatalf("load newer embedded: %v", err)
	}
	prices, err = st.CatalogPrices(ctx)
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	if p := prices["gpt-5.5"]; p.Input == nil || *p.Input != 2 {
		t.Errorf("input price = %v, want the newer build's 2", p.Input)
	}
}

// A tier has to survive the trip through SQLite, or long-context pricing works
// everywhere except where it runs.
func TestALongContextTierRoundTripsThroughTheDatabase(t *testing.T) {
	st := priceStore(t)
	ctx := context.Background()

	if err := st.ReplaceCatalog(ctx, snapshot("2026-01-01",
		pricing.Entry{ID: "gpt-5.5", Vendor: "openai", Rates: pricing.Rates{
			Price: pricing.Price{Input: usd(5), Output: usd(30), CacheRead: usd(0.5)},
			Tier: &pricing.Tier{AboveTokens: 272_000, Price: pricing.Price{
				Input: usd(10), Output: usd(45), CacheRead: usd(1),
			}},
		}},
		// A model with no tier must come back with none rather than an empty one.
		pricing.Entry{ID: "gemini-3.5-flash", Vendor: "google", Rates: pricing.Rates{
			Price: pricing.Price{Input: usd(1.5), Output: usd(9)},
		}},
	), "embedded"); err != nil {
		t.Fatalf("load catalog: %v", err)
	}

	prices, err := st.CatalogPrices(ctx)
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	tiered := prices["gpt-5.5"]
	if tiered.Tier == nil {
		t.Fatal("the tier did not survive the round trip")
	}
	if tiered.Tier.AboveTokens != 272_000 || tiered.Tier.Output == nil || *tiered.Tier.Output != 45 {
		t.Errorf("tier = %+v, want 272k / output 45", tiered.Tier)
	}
	if flat := prices["gemini-3.5-flash"]; flat.Tier != nil {
		t.Errorf("a model with no long-context rate came back with one: %+v", flat.Tier)
	}
}

// Rows written before prices existed keep an unknown cost by default. A later
// price must not silently rewrite history, and a zero would say they were free.
func TestOldRequestLogsKeepAnUnknownCost(t *testing.T) {
	st := priceStore(t)
	ctx := context.Background()

	if err := st.InsertRequestLogs(ctx, []*RequestLog{{
		RequestID: "before-pricing", Status: "success", StatusCode: 200,
		InputTokens: 100, OutputTokens: 50,
	}}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	logs, err := st.ListRequestLogs(ctx, LogFilter{Limit: 1})
	if err != nil || len(logs) == 0 {
		t.Fatalf("read back: %v", err)
	}
	if logs[0].CostUSD != nil {
		t.Errorf("cost = %v on a row nobody priced, want null", *logs[0].CostUSD)
	}

	stats, err := st.Stats(ctx, logs[0].StartedAt.Add(-time.Hour))
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.UnpricedRequests != 1 {
		t.Errorf("unpriced = %d, want the unpriced row counted so the total is not read as complete",
			stats.UnpricedRequests)
	}
	if stats.CostUSD != 0 {
		t.Errorf("cost total = %v, want 0 with the gap reported separately", stats.CostUSD)
	}
}
