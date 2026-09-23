package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/qunqin24/polyglot/internal/pricing"
)

// There is no API for creating a team in this phase, and that is deliberate:
// a stub endpoint would be pretending to have shipped something. So these
// tests reach past the store's own methods and write the second team with raw
// SQL, which is the honest admission that the second team has no front door
// yet. It is also the only way to prove the scope holds, because a boundary
// with nothing on the other side of it proves nothing at all.
const otherTeamID int64 = 2

// teamFixture is one team's worth of every ownable thing, so a leak in any
// direction has something to leak.
type teamFixture struct {
	id       int64
	name     string
	scope    *Scope
	provider *Provider
	model    *Model
	alias    *ModelAlias
	key      *APIKey
	keyName  string
	logKey   *LogKey
	logIDs   []int64
	logReqs  []string
}

// sharedModelID is offered by both teams. Ambiguity, routing and statistics all
// key on the upstream id, so giving the two teams the same one is what turns a
// missing predicate into a visible wrong answer rather than an empty result
// that would have been empty anyway.
const sharedModelID = "shared-model"

// sharedAlias is likewise declared by both teams, pointing at their own
// provider. model_aliases has no team_id of its own — it reaches its team
// through that provider — so this is the pair that proves the join carries the
// predicate.
const sharedAlias = "shared-alias"

// TestAQueryCannotCrossTeams drives every exported method on *Scope through
// team 1's handle, with team 2 fully populated, and asserts that not one row of
// team 2's ever comes back and that every by-id lookup of one answers
// ErrNotFound.
//
// ErrNotFound and never a permission error: a refusal confirms the row exists,
// which is the single thing a tenant boundary must not tell a caller who cannot
// see it. "No such row" is true from where that caller stands and is
// indistinguishable from a row that never existed.
//
// The reflection guard at the end is the part that keeps this test honest as
// the package grows. Roughly fifty methods sit on *Scope and between them they
// carry the ~58 SQL statements that touch ownable tables; nothing else in the
// suite reaches all of them. A method added to *Scope without a block here
// turns the build red rather than shipping an unproven statement.
func TestAQueryCannotCrossTeams(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "polyglot.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	now := time.Now()
	since := now.Add(-10 * time.Minute)

	// Team 1 is the team migration 0020 seeds. Team 2 is written by hand.
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO teams (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		otherTeamID, "Second", now.Unix(), now.Unix()); err != nil {
		t.Fatalf("create the second team: %v", err)
	}

	one := seedTeam(t, st, DefaultTeamID, "team1", "openai", "openai", 2, 1.0, now)
	two := seedTeam(t, st, otherTeamID, "team2", "gemini", "anthropic", 3, 50.0, now)

	tm := one.scope
	db := st.DB()

	covered := map[string]bool{}
	drive := func(name string, fn func(t *testing.T)) {
		covered[name] = true
		t.Run(name, fn)
	}

	// ---------------------------------------------------------------------
	// Reads. Every one of these runs with both teams fully populated and
	// nothing yet mutated, so an empty result is an answer and not an
	// accident of ordering.
	// ---------------------------------------------------------------------

	drive("ListProviders", func(t *testing.T) {
		got, err := tm.ListProviders(ctx)
		if err != nil {
			t.Fatalf("list providers: %v", err)
		}
		if len(got) != 1 || got[0].ID != one.provider.ID {
			t.Fatalf("listed %s, want only team 1's provider", providerNames(got))
		}
		if got[0].TeamID != DefaultTeamID {
			t.Errorf("team 1's provider came back as team %d", got[0].TeamID)
		}
	})

	drive("GetProvider", func(t *testing.T) {
		_, err := tm.GetProvider(ctx, two.provider.ID)
		mustBeNotFound(t, "GetProvider on the other team's provider", err)
		if _, err := tm.GetProvider(ctx, one.provider.ID); err != nil {
			t.Fatalf("own provider: %v", err)
		}
	})

	drive("ProviderByName", func(t *testing.T) {
		// providers.name is still globally unique in this phase, so without the
		// team predicate this lookup would happily hand over the other team's row.
		_, err := tm.ProviderByName(ctx, two.provider.Name)
		mustBeNotFound(t, "ProviderByName on the other team's provider", err)
		p, err := tm.ProviderByName(ctx, one.provider.Name)
		if err != nil {
			t.Fatalf("own provider by name: %v", err)
		}
		if p.ID != one.provider.ID {
			t.Errorf("ProviderByName returned provider %d, want %d", p.ID, one.provider.ID)
		}
	})

	drive("ListModels", func(t *testing.T) {
		got, err := tm.ListModels(ctx, ModelFilter{})
		if err != nil {
			t.Fatalf("list models: %v", err)
		}
		if len(got) != 1 || got[0].ID != one.model.ID {
			t.Fatalf("listed %d models, want only team 1's", len(got))
		}
		// Naming the other team's provider in the filter must narrow to
		// nothing rather than widen to it.
		got, err = tm.ListModels(ctx, ModelFilter{ProviderID: two.provider.ID})
		if err != nil {
			t.Fatalf("list models by the other team's provider: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("filtering by the other team's provider returned %d models", len(got))
		}
	})

	drive("CountModels", func(t *testing.T) {
		n, err := tm.CountModels(ctx, ModelFilter{})
		if err != nil {
			t.Fatalf("count models: %v", err)
		}
		if n != 1 {
			t.Fatalf("counted %d models, want 1 — the other team's model is being counted", n)
		}
	})

	drive("ModelsByUpstreamID", func(t *testing.T) {
		got, err := tm.ModelsByUpstreamID(ctx, sharedModelID)
		if err != nil {
			t.Fatalf("models by upstream id: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("resolved %q to %d models, want 1 — this is what the router calls, so a leak here routes a request to another team's upstream", sharedModelID, len(got))
		}
		if got[0].ProviderID != one.provider.ID {
			t.Errorf("resolved %q to provider %d, want %d", sharedModelID, got[0].ProviderID, one.provider.ID)
		}
	})

	drive("ModelForProvider", func(t *testing.T) {
		_, err := tm.ModelForProvider(ctx, two.provider.ID, sharedModelID)
		mustBeNotFound(t, "ModelForProvider naming the other team's provider", err)
		if _, err := tm.ModelForProvider(ctx, one.provider.ID, sharedModelID); err != nil {
			t.Fatalf("own model on own provider: %v", err)
		}
	})

	drive("GetModel", func(t *testing.T) {
		_, err := tm.GetModel(ctx, two.model.ID)
		mustBeNotFound(t, "GetModel on the other team's model", err)
		if _, err := tm.GetModel(ctx, one.model.ID); err != nil {
			t.Fatalf("own model: %v", err)
		}
	})

	drive("AmbiguousModelIDs", func(t *testing.T) {
		got, err := tm.AmbiguousModelIDs(ctx)
		if err != nil {
			t.Fatalf("ambiguous models: %v", err)
		}
		// Both teams offer sharedModelID, but each from one provider. Two teams
		// are not competing to resolve a name, so within team 1 it is
		// unambiguous; reporting it would be the leak showing up as a warning
		// in the operator's UI.
		if got[sharedModelID] {
			t.Fatalf("%q reported as ambiguous — the other team's provider is being counted as a competing route", sharedModelID)
		}
		if len(got) != 0 {
			t.Fatalf("ambiguous ids = %v, want none", got)
		}
	})

	drive("ModelCountsByProvider", func(t *testing.T) {
		got, err := tm.ModelCountsByProvider(ctx)
		if err != nil {
			t.Fatalf("model counts: %v", err)
		}
		if _, ok := got[two.provider.ID]; ok {
			t.Fatalf("counts include the other team's provider %d: %v", two.provider.ID, got)
		}
		if got[one.provider.ID] != 1 {
			t.Fatalf("own provider counted %d models, want 1", got[one.provider.ID])
		}
	})

	drive("ListAliases", func(t *testing.T) {
		got, err := tm.ListAliases(ctx)
		if err != nil {
			t.Fatalf("list aliases: %v", err)
		}
		if len(got) != 1 || got[0].ID != one.alias.ID {
			t.Fatalf("listed %d aliases, want only team 1's", len(got))
		}
	})

	drive("AliasesFor", func(t *testing.T) {
		got, err := tm.AliasesFor(ctx, sharedAlias)
		if err != nil {
			t.Fatalf("aliases for %q: %v", sharedAlias, err)
		}
		if len(got) != 1 {
			t.Fatalf("alias %q resolved to %d routes, want 1 — a second route here is another team's provider offered as a fallback", sharedAlias, len(got))
		}
		if got[0].ProviderID != one.provider.ID {
			t.Errorf("alias resolved to provider %d, want %d", got[0].ProviderID, one.provider.ID)
		}
	})

	drive("GetAlias", func(t *testing.T) {
		_, err := tm.GetAlias(ctx, two.alias.ID)
		mustBeNotFound(t, "GetAlias on the other team's alias", err)
		if _, err := tm.GetAlias(ctx, one.alias.ID); err != nil {
			t.Fatalf("own alias: %v", err)
		}
	})

	drive("ListAPIKeys", func(t *testing.T) {
		got, err := tm.ListAPIKeys(ctx)
		if err != nil {
			t.Fatalf("list api keys: %v", err)
		}
		if len(got) != 1 || got[0].ID != one.key.ID {
			t.Fatalf("listed %d keys, want only team 1's", len(got))
		}
		if got[0].TeamID != DefaultTeamID || got[0].TeamName != "Default" {
			t.Errorf("key carries team %d/%q, want 1/\"Default\"", got[0].TeamID, got[0].TeamName)
		}
	})

	drive("GetAPIKey", func(t *testing.T) {
		_, err := tm.GetAPIKey(ctx, two.key.ID)
		mustBeNotFound(t, "GetAPIKey on the other team's key", err)
		if _, err := tm.GetAPIKey(ctx, one.key.ID); err != nil {
			t.Fatalf("own key: %v", err)
		}
	})

	drive("APIKeySecret", func(t *testing.T) {
		// This one returns the plaintext credential, so a crossing here hands
		// one team a working key belonging to another.
		_, err := tm.APIKeySecret(ctx, two.key.ID)
		mustBeNotFound(t, "APIKeySecret on the other team's key", err)
		if _, err := tm.APIKeySecret(ctx, one.key.ID); err != nil {
			t.Fatalf("own key secret: %v", err)
		}
	})

	drive("APIKeyNameTaken", func(t *testing.T) {
		// Names are unique per team now (0020 replaced the global index), so a
		// global check here would refuse a name the index would accept.
		taken, err := tm.APIKeyNameTaken(ctx, two.keyName, 0)
		if err != nil {
			t.Fatalf("check name: %v", err)
		}
		if taken {
			t.Fatalf("%q reported as taken, but the key holding it belongs to another team", two.keyName)
		}
		taken, err = tm.APIKeyNameTaken(ctx, one.keyName, 0)
		if err != nil {
			t.Fatalf("check own name: %v", err)
		}
		if !taken {
			t.Errorf("own key name %q reported free", one.keyName)
		}
	})

	drive("APIKeySpendSince", func(t *testing.T) {
		spent, unpriced, err := tm.APIKeySpendSince(ctx, two.key.ID, since)
		if err != nil {
			t.Fatalf("spend for the other team's key: %v", err)
		}
		if spent != 0 || unpriced != 0 {
			t.Fatalf("spend for the other team's key = %v/%d unpriced, want 0/0 — this feeds the budget check, so a leak spends one team's cap on another team's traffic", spent, unpriced)
		}
		spent, unpriced, err = tm.APIKeySpendSince(ctx, one.key.ID, since)
		if err != nil {
			t.Fatalf("own spend: %v", err)
		}
		if spent != 1.0 || unpriced != 1 {
			t.Errorf("own spend = %v/%d unpriced, want 1/1", spent, unpriced)
		}
	})

	drive("APIKeyUsageSince", func(t *testing.T) {
		got, err := tm.APIKeyUsageSince(ctx, two.key.ID, since)
		if err != nil {
			t.Fatalf("usage for the other team's key: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("usage for the other team's key returned %d samples", len(got))
		}
		got, err = tm.APIKeyUsageSince(ctx, one.key.ID, since)
		if err != nil {
			t.Fatalf("own usage: %v", err)
		}
		if len(got) != len(one.logIDs) {
			t.Errorf("own usage returned %d samples, want %d", len(got), len(one.logIDs))
		}
	})

	drive("ListLogKeys", func(t *testing.T) {
		got, err := tm.ListLogKeys(ctx)
		if err != nil {
			t.Fatalf("list log keys: %v", err)
		}
		if len(got) != 1 || got[0].ID != one.logKey.ID {
			t.Fatalf("listed %d log keys, want only team 1's", len(got))
		}
		if got[0].TeamID != DefaultTeamID {
			t.Errorf("log key came back as team %d", got[0].TeamID)
		}
	})

	drive("ListRequestLogs", func(t *testing.T) {
		got, err := tm.ListRequestLogs(ctx, LogFilter{})
		if err != nil {
			t.Fatalf("list request logs: %v", err)
		}
		if len(got) != len(one.logIDs) {
			t.Fatalf("listed %d log rows, want %d", len(got), len(one.logIDs))
		}
		for _, l := range got {
			if l.TeamID != DefaultTeamID {
				t.Fatalf("log row %d belongs to team %d", l.ID, l.TeamID)
			}
		}
		// Asking for the other team's request by its own id must narrow to
		// nothing: the filter is the caller's half of the predicate, never a
		// way around it.
		got, err = tm.ListRequestLogs(ctx, LogFilter{RequestID: two.logReqs[0]})
		if err != nil {
			t.Fatalf("list by the other team's request id: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("filtering by the other team's request id returned %d rows", len(got))
		}
	})

	drive("CountRequestLogs", func(t *testing.T) {
		n, err := tm.CountRequestLogs(ctx, LogFilter{})
		if err != nil {
			t.Fatalf("count request logs: %v", err)
		}
		if n != int64(len(one.logIDs)) {
			t.Fatalf("counted %d log rows, want %d", n, len(one.logIDs))
		}
	})

	drive("GetRequestLog", func(t *testing.T) {
		// This is the lookup every read of a recorded request body goes
		// through, so it is the highest-consequence not-found in the package.
		_, err := tm.GetRequestLog(ctx, two.logIDs[0])
		mustBeNotFound(t, "GetRequestLog on the other team's log row", err)
		if _, err := tm.GetRequestLog(ctx, one.logIDs[0]); err != nil {
			t.Fatalf("own log row: %v", err)
		}
	})

	drive("APIKeyOrigins", func(t *testing.T) {
		got, err := tm.APIKeyOrigins(ctx, two.key.ID, since, 10)
		if err != nil {
			t.Fatalf("origins for the other team's key: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("origins for the other team's key returned %v", got)
		}
		got, err = tm.APIKeyOrigins(ctx, one.key.ID, since, 10)
		if err != nil {
			t.Fatalf("own origins: %v", err)
		}
		if len(got) != 1 || got[0].ClientIP != one.clientIP() {
			t.Errorf("own origins = %v, want only %s", got, one.clientIP())
		}
	})

	drive("ModelStats", func(t *testing.T) {
		got, err := tm.ModelStats(ctx, since)
		if err != nil {
			t.Fatalf("model stats: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("model stats returned %d entries, want 1: %+v", len(got), got)
		}
		if got[0].ProviderName != one.provider.Name {
			t.Errorf("model stats reported provider %q", got[0].ProviderName)
		}
		if got[0].Requests != int64(len(one.logIDs)) {
			t.Errorf("model stats counted %d requests, want %d", got[0].Requests, len(one.logIDs))
		}
	})

	drive("Stats", func(t *testing.T) {
		got, err := tm.Stats(ctx, since)
		if err != nil {
			t.Fatalf("stats: %v", err)
		}
		if got.TotalRequests != 2 || got.SuccessCount != 1 || got.ErrorCount != 1 {
			t.Fatalf("totals = %d/%d ok/%d err, want 2/1/1", got.TotalRequests, got.SuccessCount, got.ErrorCount)
		}
		if got.CostUSD != 1.0 {
			t.Errorf("cost = %v, want 1 — the other team spent 100 in the same window", got.CostUSD)
		}
		if got.UnpricedRequests != 1 {
			t.Errorf("unpriced = %d, want 1", got.UnpricedRequests)
		}
		if got.LossyRequests != 2 {
			t.Errorf("lossy = %d, want 2", got.LossyRequests)
		}
		if got.ConvertedRequests != 0 {
			t.Errorf("converted = %d, want 0 — team 1 spoke openai in and out; only the other team converted", got.ConvertedRequests)
		}
		if len(got.ByProvider) != 1 || got.ByProvider[0].ProviderName != one.provider.Name {
			t.Errorf("by provider = %+v, want only %q", got.ByProvider, one.provider.Name)
		}
		var series int64
		for _, b := range got.Series {
			series += b.Count
		}
		if series != 2 {
			t.Errorf("series totals %d requests, want 2", series)
		}
	})

	drive("ConversionStats", func(t *testing.T) {
		got, err := tm.ConversionStats(ctx, since)
		if err != nil {
			t.Fatalf("conversion stats: %v", err)
		}
		if got.TotalRequests != 2 || got.ConvertedRequests != 0 || got.LossyRequests != 2 {
			t.Fatalf("totals = %d/%d converted/%d lossy, want 2/0/2",
				got.TotalRequests, got.ConvertedRequests, got.LossyRequests)
		}
		for _, p := range got.Pairs {
			if p.ClientProtocol != "openai" || p.UpstreamProtocol != "openai" {
				t.Errorf("pair %s -> %s is the other team's traffic", p.ClientProtocol, p.UpstreamProtocol)
			}
		}
		for _, f := range got.Flows {
			if f.ProviderName != one.provider.Name {
				t.Errorf("flow names provider %q", f.ProviderName)
			}
		}
		// The fidelity-field roll-up is the one statement in stats.go whose
		// FROM the scoped constructors cannot build, so its predicate is hand
		// written and worth its own assertion.
		if len(got.Fields) != 1 || got.Fields[0].Field != one.tag()+"_field" {
			t.Errorf("fidelity fields = %+v, want only %s_field", got.Fields, one.tag())
		}
	})

	drive("LatencyStats", func(t *testing.T) {
		got, err := tm.LatencyStats(ctx, since)
		if err != nil {
			t.Fatalf("latency stats: %v", err)
		}
		var series, bars int64
		for _, p := range got.Series {
			series += p.Count
		}
		for _, b := range got.Histogram {
			bars += b.Count
		}
		// The series is a window-function CTE whose predicate has to sit inside
		// the CTE: filtering outside it would rank one team's latencies against
		// every other team's and then show the survivors.
		if series != 2 {
			t.Errorf("latency series totals %d requests, want 2", series)
		}
		if bars != 2 {
			t.Errorf("latency histogram totals %d requests, want 2", bars)
		}
		for _, e := range got.Errors {
			if e.ErrorType != one.tag()+"_error" {
				t.Errorf("errors include %q from the other team", e.ErrorType)
			}
		}
		if len(got.Errors) != 1 {
			t.Errorf("errors = %+v, want exactly team 1's one failure", got.Errors)
		}
	})

	drive("CostStats", func(t *testing.T) {
		got, err := tm.CostStats(ctx, since)
		if err != nil {
			t.Fatalf("cost stats: %v", err)
		}
		if got.CostUSD != 1.0 || got.UnpricedRequests != 1 {
			t.Fatalf("cost = %v with %d unpriced, want 1/1", got.CostUSD, got.UnpricedRequests)
		}
		if len(got.Models) != 1 || got.Models[0].ProviderName != one.provider.Name {
			t.Fatalf("cost by model = %+v, want only %q", got.Models, one.provider.Name)
		}
		for _, s := range got.Stacks {
			if s.ProviderName != "" && s.ProviderName != one.provider.Name {
				t.Errorf("cost stack names provider %q", s.ProviderName)
			}
		}
	})

	// ---------------------------------------------------------------------
	// Writes. A write that crosses is worse than a read that crosses, so each
	// of these checks the returned error *and* goes back to the raw row to
	// confirm nothing moved. Several of these methods report "nothing
	// happened" as success by design, and for those the row is the only
	// evidence there is.
	// ---------------------------------------------------------------------

	drive("CreateProvider", func(t *testing.T) {
		// An INSERT has no WHERE for a constructor to open, so what is being
		// checked here is that the row lands in this handle's team.
		p, err := tm.CreateProvider(ctx, &Provider{
			Name: "team1-second-upstream", Protocol: "openai",
			BaseURL: "https://second.example.com", Enabled: true,
		})
		if err != nil {
			t.Fatalf("create provider: %v", err)
		}
		if got := scalar[int64](t, db, `SELECT team_id FROM providers WHERE id = ?`, p.ID); got != DefaultTeamID {
			t.Fatalf("created provider landed in team %d, want %d", got, DefaultTeamID)
		}
		if err := tm.DeleteProvider(ctx, p.ID); err != nil {
			t.Fatalf("clean up: %v", err)
		}
	})

	drive("UpdateProvider", func(t *testing.T) {
		_, err := tm.UpdateProvider(ctx, two.provider.ID, &Provider{
			Name: "hijacked", Protocol: "openai", BaseURL: "https://hijack.example.com", Enabled: true,
		}, nil)
		mustBeNotFound(t, "UpdateProvider on the other team's provider", err)
		if got := scalar[string](t, db, `SELECT name FROM providers WHERE id = ?`, two.provider.ID); got != two.provider.Name {
			t.Fatalf("the other team's provider was renamed to %q", got)
		}
	})

	drive("DisableProvider", func(t *testing.T) {
		// This one reports a no-op as success on purpose — two requests can
		// fail at once — so the row is the whole of the evidence.
		if err := tm.DisableProvider(ctx, two.provider.ID, "not yours"); err != nil {
			t.Fatalf("disable: %v", err)
		}
		if got := scalar[int64](t, db, `SELECT enabled FROM providers WHERE id = ?`, two.provider.ID); got != 1 {
			t.Fatal("the other team's provider was switched off")
		}
		if got := scalar[string](t, db, `SELECT disabled_reason FROM providers WHERE id = ?`, two.provider.ID); got != "" {
			t.Fatalf("the other team's provider records reason %q", got)
		}
	})

	drive("DeleteProvider", func(t *testing.T) {
		err := tm.DeleteProvider(ctx, two.provider.ID)
		mustBeNotFound(t, "DeleteProvider on the other team's provider", err)
		if n := scalar[int64](t, db, `SELECT COUNT(*) FROM providers WHERE id = ?`, two.provider.ID); n != 1 {
			t.Fatal("the other team's provider was deleted")
		}
		// models and model_aliases cascade off providers, so a crossing delete
		// would have taken them too.
		if n := scalar[int64](t, db, `SELECT COUNT(*) FROM models WHERE provider_id = ?`, two.provider.ID); n != 1 {
			t.Fatal("the other team's models were cascaded away")
		}
	})

	drive("CreateModel", func(t *testing.T) {
		_, err := tm.CreateModel(ctx, &Model{
			ProviderID: two.provider.ID, UpstreamModelID: "smuggled", Enabled: true,
		})
		mustBeNotFound(t, "CreateModel on the other team's provider", err)
		if n := scalar[int64](t, db, `SELECT COUNT(*) FROM models WHERE provider_id = ?`, two.provider.ID); n != 1 {
			t.Fatal("a model was written onto the other team's provider")
		}
	})

	drive("UpdateModel", func(t *testing.T) {
		_, err := tm.UpdateModel(ctx, two.model.ID, "renamed", false)
		mustBeNotFound(t, "UpdateModel on the other team's model", err)
		name := scalar[string](t, db, `SELECT display_name FROM models WHERE id = ?`, two.model.ID)
		enabled := scalar[int64](t, db, `SELECT enabled FROM models WHERE id = ?`, two.model.ID)
		if name != two.model.DisplayName || enabled != 1 {
			t.Fatalf("the other team's model now reads %q/enabled=%d", name, enabled)
		}
	})

	drive("SetModelPrice", func(t *testing.T) {
		usd := 1.0
		_, err := tm.SetModelPrice(ctx, two.model.ID, pricing.Price{Input: &usd, Output: &usd})
		mustBeNotFound(t, "SetModelPrice on the other team's model", err)
		var in sql.NullFloat64
		if err := db.QueryRowContext(ctx, `SELECT price_input FROM models WHERE id = ?`, two.model.ID).Scan(&in); err != nil {
			t.Fatalf("read the other team's price: %v", err)
		}
		if in.Valid {
			t.Fatalf("the other team's model was priced at %v", in.Float64)
		}
	})

	drive("DeleteModel", func(t *testing.T) {
		err := tm.DeleteModel(ctx, two.model.ID)
		mustBeNotFound(t, "DeleteModel on the other team's model", err)
		if n := scalar[int64](t, db, `SELECT COUNT(*) FROM models WHERE id = ?`, two.model.ID); n != 1 {
			t.Fatal("the other team's model was deleted")
		}
	})

	drive("SyncModels", func(t *testing.T) {
		_, err := tm.SyncModels(ctx, two.provider.ID, []DiscoveredModel{{ID: "discovered", DisplayName: "Discovered"}})
		mustBeNotFound(t, "SyncModels on the other team's provider", err)
		if n := scalar[int64](t, db, `SELECT COUNT(*) FROM models WHERE provider_id = ?`, two.provider.ID); n != 1 {
			t.Fatal("discovery wrote models onto the other team's provider")
		}
		var synced sql.NullInt64
		if err := db.QueryRowContext(ctx,
			`SELECT models_synced_at FROM providers WHERE id = ?`, two.provider.ID).Scan(&synced); err != nil {
			t.Fatalf("read the other team's sync time: %v", err)
		}
		if synced.Valid {
			t.Fatal("the other team's provider recorded a sync that did not happen")
		}
	})

	drive("CreateAlias", func(t *testing.T) {
		_, err := tm.CreateAlias(ctx, &ModelAlias{
			Alias: "smuggled", ProviderID: two.provider.ID, UpstreamModel: sharedModelID, Enabled: true,
		})
		mustBeNotFound(t, "CreateAlias pointing at the other team's provider", err)
		if n := scalar[int64](t, db, `SELECT COUNT(*) FROM model_aliases WHERE provider_id = ?`, two.provider.ID); n != 1 {
			t.Fatal("an alias was written onto the other team's provider")
		}
	})

	drive("UpdateAlias", func(t *testing.T) {
		_, err := tm.UpdateAlias(ctx, two.alias.ID, &ModelAlias{
			Alias: "hijacked", ProviderID: one.provider.ID, UpstreamModel: sharedModelID, Enabled: true,
		})
		mustBeNotFound(t, "UpdateAlias on the other team's alias", err)
		if got := scalar[string](t, db, `SELECT alias FROM model_aliases WHERE id = ?`, two.alias.ID); got != sharedAlias {
			t.Fatalf("the other team's alias was renamed to %q", got)
		}
		// The second guard: an alias this team does own must not be repointed
		// at another team's provider. Checking only ownership of the row being
		// edited would let one team aim traffic at the other team's upstream,
		// which is the same leak as editing that provider directly.
		_, err = tm.UpdateAlias(ctx, one.alias.ID, &ModelAlias{
			Alias: sharedAlias, ProviderID: two.provider.ID, UpstreamModel: sharedModelID, Enabled: true,
		})
		mustBeNotFound(t, "UpdateAlias repointing an own alias at the other team's provider", err)
		if got := scalar[int64](t, db, `SELECT provider_id FROM model_aliases WHERE id = ?`, one.alias.ID); got != one.provider.ID {
			t.Fatalf("own alias now points at provider %d, want %d", got, one.provider.ID)
		}
	})

	drive("DeleteAlias", func(t *testing.T) {
		err := tm.DeleteAlias(ctx, two.alias.ID)
		mustBeNotFound(t, "DeleteAlias on the other team's alias", err)
		if n := scalar[int64](t, db, `SELECT COUNT(*) FROM model_aliases WHERE id = ?`, two.alias.ID); n != 1 {
			t.Fatal("the other team's alias was deleted")
		}
	})

	drive("CreateAPIKey", func(t *testing.T) {
		// Reusing the other team's key name on purpose: 0020 made the name
		// index UNIQUE(team_id, name), so this must be accepted. If it is
		// refused, the scope is reading a constraint that no longer exists.
		const secret = "pg-team1-created-key-secret-0001"
		k, err := tm.CreateAPIKey(ctx, two.keyName, secret[:11], secret)
		if err != nil {
			t.Fatalf("create key reusing another team's name: %v", err)
		}
		if got := scalar[int64](t, db, `SELECT team_id FROM api_keys WHERE id = ?`, k.ID); got != DefaultTeamID {
			t.Fatalf("created key landed in team %d, want %d", got, DefaultTeamID)
		}
		if k.TeamID != DefaultTeamID {
			t.Errorf("returned key reports team %d", k.TeamID)
		}
		if err := tm.DeleteAPIKey(ctx, k.ID); err != nil {
			t.Fatalf("clean up: %v", err)
		}
	})

	drive("CreateAPIKeyWithPolicy", func(t *testing.T) {
		const secret = "pg-team1-policy-key-secret-00002"
		budget := 5.0
		k, err := tm.CreateAPIKeyWithPolicy(ctx, "team1-policy", secret[:11], secret, APIKeyPolicy{
			BudgetUSD: &budget, BudgetPeriod: BudgetTotal, AllowedModels: []string{sharedModelID},
		})
		if err != nil {
			t.Fatalf("create key with policy: %v", err)
		}
		if got := scalar[int64](t, db, `SELECT team_id FROM api_keys WHERE id = ?`, k.ID); got != DefaultTeamID {
			t.Fatalf("created key landed in team %d, want %d", got, DefaultTeamID)
		}
		if err := tm.DeleteAPIKey(ctx, k.ID); err != nil {
			t.Fatalf("clean up: %v", err)
		}
	})

	drive("UpdateAPIKey", func(t *testing.T) {
		_, err := tm.UpdateAPIKey(ctx, two.key.ID, "hijacked", false, APIKeyPolicy{})
		mustBeNotFound(t, "UpdateAPIKey on the other team's key", err)
		name := scalar[string](t, db, `SELECT name FROM api_keys WHERE id = ?`, two.key.ID)
		enabled := scalar[int64](t, db, `SELECT enabled FROM api_keys WHERE id = ?`, two.key.ID)
		if name != two.keyName || enabled != 1 {
			t.Fatalf("the other team's key now reads %q/enabled=%d", name, enabled)
		}
		// api_key_models has no team_id of its own; the rows-affected check on
		// the parent UPDATE is what keeps it from needing one.
		if n := scalar[int64](t, db, `SELECT COUNT(*) FROM api_key_models WHERE api_key_id = ?`, two.key.ID); n != 0 {
			t.Fatalf("the other team's key gained %d model restrictions", n)
		}
	})

	drive("SetAPIKeyEnabled", func(t *testing.T) {
		err := tm.SetAPIKeyEnabled(ctx, two.key.ID, false)
		mustBeNotFound(t, "SetAPIKeyEnabled on the other team's key", err)
		if got := scalar[int64](t, db, `SELECT enabled FROM api_keys WHERE id = ?`, two.key.ID); got != 1 {
			t.Fatal("the other team's key was disabled")
		}
	})

	drive("TouchAPIKey", func(t *testing.T) {
		// TouchAPIKey returns nil whether or not it matched — it is called on
		// every authenticated request and a miss is not worth an error — so
		// the untouched column is the only thing that can be asserted.
		if err := tm.TouchAPIKey(ctx, two.key.ID); err != nil {
			t.Fatalf("touch: %v", err)
		}
		var last sql.NullInt64
		if err := db.QueryRowContext(ctx, `SELECT last_used_at FROM api_keys WHERE id = ?`, two.key.ID).Scan(&last); err != nil {
			t.Fatalf("read the other team's key: %v", err)
		}
		if last.Valid {
			t.Fatal("the other team's key was marked as used")
		}
	})

	drive("ResetAPIKeyBudget", func(t *testing.T) {
		before := scalar[int64](t, db, `SELECT budget_anchor FROM api_keys WHERE id = ?`, two.key.ID)
		err := tm.ResetAPIKeyBudget(ctx, two.key.ID, now.Add(48*time.Hour))
		mustBeNotFound(t, "ResetAPIKeyBudget on the other team's key", err)
		if after := scalar[int64](t, db, `SELECT budget_anchor FROM api_keys WHERE id = ?`, two.key.ID); after != before {
			t.Fatalf("the other team's budget window moved from %d to %d", before, after)
		}
	})

	drive("DeleteAPIKey", func(t *testing.T) {
		err := tm.DeleteAPIKey(ctx, two.key.ID)
		mustBeNotFound(t, "DeleteAPIKey on the other team's key", err)
		if n := scalar[int64](t, db, `SELECT COUNT(*) FROM api_keys WHERE id = ?`, two.key.ID); n != 1 {
			t.Fatal("the other team's key was deleted")
		}
	})

	drive("CreateLogKey", func(t *testing.T) {
		k, err := tm.CreateLogKey(ctx, "team1-extra-log", "plog_team1_extra_secret_value", nil)
		if err != nil {
			t.Fatalf("create log key: %v", err)
		}
		if got := scalar[int64](t, db, `SELECT team_id FROM log_keys WHERE id = ?`, k.ID); got != DefaultTeamID {
			t.Fatalf("created log key landed in team %d, want %d", got, DefaultTeamID)
		}
		if err := tm.DeleteLogKey(ctx, k.ID); err != nil {
			t.Fatalf("clean up: %v", err)
		}
	})

	drive("DeleteLogKey", func(t *testing.T) {
		err := tm.DeleteLogKey(ctx, two.logKey.ID)
		mustBeNotFound(t, "DeleteLogKey on the other team's log key", err)
		if n := scalar[int64](t, db, `SELECT COUNT(*) FROM log_keys WHERE id = ?`, two.logKey.ID); n != 1 {
			t.Fatal("the other team's log key was deleted")
		}
	})

	// ---------------------------------------------------------------------
	// The guard. Everything above is a list somebody maintains by hand, and a
	// hand-maintained list of fifty methods goes stale the first time one is
	// added. This is what makes going stale a build failure: it enumerates
	// what is actually on the type and insists each name was driven.
	//
	// Do not soften this into a spot check. It is the only thing in the suite
	// that reaches every scoped statement, because it reaches every method
	// that carries one.
	// ---------------------------------------------------------------------
	scopeType := reflect.TypeOf(&Scope{})
	onType := map[string]bool{}
	for i := 0; i < scopeType.NumMethod(); i++ {
		name := scopeType.Method(i).Name
		onType[name] = true
		if !covered[name] {
			t.Errorf("(*Scope).%s is never driven by TestAQueryCannotCrossTeams.\n"+
				"Every exported method on Scope reads or writes team-owned data, and nothing else in the suite "+
				"proves the predicate is there. Add a drive(%q, …) block that calls it through team 1's handle "+
				"and asserts team 2's rows are neither returned nor written — or, if it genuinely does not touch "+
				"tenant data, it does not belong on Scope.", name, name)
		}
	}
	for name := range covered {
		if !onType[name] {
			t.Errorf("this test drives %q, which is no longer an exported method on *Scope — "+
				"the coverage table has drifted from the type", name)
		}
	}
}

// TestAnInsertThatForgetsItsTeamIsRejected pins layer 0.
//
// The type system stops a *caller* reaching tenant data without a team, and the
// scoped constructors stop a *statement* being written without the predicate.
// Neither of them sees an INSERT: an insert has no WHERE clause for a
// constructor to open, so the last line of defence is the database itself.
//
// That line is invisible from Go — no test that goes through the store can tell
// whether the trigger is installed, because every one of those paths names the
// team. Hence the raw SQL here.
//
// Note what is being asserted: the write is *refused*, not quietly filed under
// the default team. team_id DEFAULT 0 exists so that a forgotten team becomes a
// findable orphan rather than an invisible one, and the trigger turns that
// sentinel into a loud failure on the three ownership roots.
func TestAnInsertThatForgetsItsTeamIsRejected(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "polyglot.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	db := st.DB()
	now := time.Now().Unix()

	roots := []struct {
		table   string
		forgot  string
		carries string
		args    []any
	}{
		{
			table:   "providers",
			forgot:  `INSERT INTO providers (name, protocol, base_url, created_at, updated_at) VALUES ('orphan', 'openai', 'https://orphan.example.com', ?, ?)`,
			carries: `INSERT INTO providers (team_id, name, protocol, base_url, created_at, updated_at) VALUES (1, 'adopted', 'openai', 'https://adopted.example.com', ?, ?)`,
			args:    []any{now, now},
		},
		{
			table:   "api_keys",
			forgot:  `INSERT INTO api_keys (name, prefix, secret_hash, created_at) VALUES ('orphan', 'pg-orphan', 'orphan-hash', ?)`,
			carries: `INSERT INTO api_keys (team_id, name, prefix, secret_hash, created_at) VALUES (1, 'adopted', 'pg-adopted', 'adopted-hash', ?)`,
			args:    []any{now},
		},
		{
			table:   "log_keys",
			forgot:  `INSERT INTO log_keys (name, token_hash, prefix, created_at) VALUES ('orphan', 'orphan-log-hash', 'plog_orphan', ?)`,
			carries: `INSERT INTO log_keys (team_id, name, token_hash, prefix, created_at) VALUES (1, 'adopted', 'adopted-log-hash', 'plog_adopted', ?)`,
			args:    []any{now},
		},
	}

	for _, r := range roots {
		t.Run(r.table, func(t *testing.T) {
			_, err := db.ExecContext(ctx, r.forgot, r.args...)
			if err == nil {
				t.Fatalf("an INSERT into %s that named no team was accepted; "+
					"migration 0020's %s_team_required trigger is not doing its job", r.table, r.table)
			}
			// RAISE(ABORT, …) carries its own text so the failure says what
			// went wrong instead of arriving as an anonymous constraint error.
			if !strings.Contains(err.Error(), "did not carry a team") {
				t.Errorf("the refusal reads %q, which does not say the team was missing", err)
			}
			if n := scalar[int64](t, db, `SELECT COUNT(*) FROM `+r.table+` WHERE team_id = 0`); n != 0 {
				t.Fatalf("%d orphan rows landed in %s despite the abort", n, r.table)
			}
			if n := scalar[int64](t, db, `SELECT COUNT(*) FROM `+r.table+` WHERE name = 'orphan'`); n != 0 {
				t.Fatalf("the orphan row is in %s under another team_id", r.table)
			}

			// The other half: a trigger that refused everything would also pass
			// the check above. An INSERT that names its team has to go through.
			if _, err := db.ExecContext(ctx, r.carries, r.args...); err != nil {
				t.Fatalf("an INSERT into %s that named team 1 was refused: %v", r.table, err)
			}
			if n := scalar[int64](t, db, `SELECT COUNT(*) FROM `+r.table+` WHERE name = 'adopted' AND team_id = 1`); n != 1 {
				t.Fatalf("the row that named team 1 is not in %s under team 1", r.table)
			}
		})
	}

	// request_logs deliberately has no trigger: it has one writer, on the
	// buffered flush path, and per-row work there is the one thing the rules
	// say not to add. A record with no team therefore lands as the sentinel —
	// findable — rather than being filed under the default team where nobody
	// would look for it again. Assert the sentinel, because "no trigger" is
	// only defensible if that is what actually happens.
	t.Run("request_logs keeps the sentinel instead", func(t *testing.T) {
		l := &RequestLog{
			RequestID: "no-team", StartedAt: time.Now(), FinishedAt: time.Now(),
			Status: "success", StatusCode: 200,
			ClientProtocol: "openai", UpstreamProtocol: "openai",
		}
		if err := st.InsertRequestLogs(ctx, []*RequestLog{l}); err != nil {
			t.Fatalf("insert: %v", err)
		}
		team := scalar[int64](t, db, `SELECT team_id FROM request_logs WHERE request_id = 'no-team'`)
		if team == DefaultTeamID {
			t.Fatal("a log row with no team was filed under the default team, where nobody will look for it")
		}
		if team != 0 {
			t.Fatalf("a log row with no team landed as team %d, want the 0 sentinel", team)
		}
		// And it is invisible through either team's handle, which is what
		// makes the sentinel safe to leave lying around.
		got, err := st.ForTeam(DefaultTeamID).ListRequestLogs(ctx, LogFilter{})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, row := range got {
			if row.RequestID == "no-team" {
				t.Fatal("the orphan log row is visible through the default team's handle")
			}
		}
	})
}

// seedTeam fills one team with a provider, a model, an alias, an API key, a log
// key and some request logs. Both teams get the same shapes so that a leak in
// either direction has something recognisable to carry.
func seedTeam(t *testing.T, st *Store, id int64, tag, clientProto, upstreamProto string,
	logRows int, costPerSuccess float64, now time.Time) *teamFixture {
	t.Helper()
	ctx := context.Background()
	f := &teamFixture{id: id, name: tag, scope: st.ForTeam(id), keyName: tag + "-key"}

	var err error
	f.provider, err = f.scope.CreateProvider(ctx, &Provider{
		Name:     tag + "-upstream",
		Protocol: upstreamProto,
		BaseURL:  "https://" + tag + ".example.com",
		APIKey:   tag + "-credential",
		Enabled:  true,
		Priority: 10,
	})
	if err != nil {
		t.Fatalf("%s: create provider: %v", tag, err)
	}
	if f.provider.TeamID != id {
		t.Fatalf("%s: provider landed in team %d", tag, f.provider.TeamID)
	}

	f.model, err = f.scope.CreateModel(ctx, &Model{
		ProviderID:      f.provider.ID,
		UpstreamModelID: sharedModelID,
		DisplayName:     tag + " model",
		Enabled:         true,
	})
	if err != nil {
		t.Fatalf("%s: create model: %v", tag, err)
	}

	f.alias, err = f.scope.CreateAlias(ctx, &ModelAlias{
		Alias:         sharedAlias,
		ProviderID:    f.provider.ID,
		UpstreamModel: sharedModelID,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("%s: create alias: %v", tag, err)
	}

	secret := "pg-" + tag + "-secret-0123456789abcdef"
	f.key, err = f.scope.CreateAPIKey(ctx, f.keyName, secret[:11], secret)
	if err != nil {
		t.Fatalf("%s: create api key: %v", tag, err)
	}

	f.logKey, err = f.scope.CreateLogKey(ctx, tag+"-log", "plog_"+tag+"_secret_value", nil)
	if err != nil {
		t.Fatalf("%s: create log key: %v", tag, err)
	}

	var logs []*RequestLog
	for i := 0; i < logRows; i++ {
		started := now.Add(-time.Duration(i+1) * time.Minute)
		l := &RequestLog{
			RequestID:        fmt.Sprintf("req-%s-%d", tag, i),
			StartedAt:        started,
			FinishedAt:       started.Add(150 * time.Millisecond),
			LatencyMS:        150 + int64(i),
			Status:           "success",
			StatusCode:       200,
			ClientProtocol:   clientProto,
			UpstreamProtocol: upstreamProto,
			ProviderID:       &f.provider.ID,
			ProviderName:     f.provider.Name,
			ModelAlias:       sharedAlias,
			UpstreamModel:    sharedModelID,
			APIKeyID:         &f.key.ID,
			APIKeyName:       f.keyName,
			ClientIP:         f.clientIP(),
			ClientApp:        tag + "-app",
			Stream:           true,
			InputTokens:      100,
			OutputTokens:     10,
			// Every row carries a lossy note, so the fidelity roll-ups have
			// something per team to get wrong.
			FidelityNotes: `[{"field":"` + tag + `_field","fidelity":"lossy","detail":""}]`,
			TeamID:        id,
			TeamName:      tag,
		}
		if i == logRows-1 {
			// The last row is the failure, and it is unpriced: a cost nobody
			// knows is null, never zero.
			l.Status = "error"
			l.StatusCode = 502
			l.ErrorType = tag + "_error"
			l.ErrorMessage = "upstream refused"
		} else {
			cost := costPerSuccess
			l.CostUSD = &cost
			l.CostSource = "catalog"
		}
		logs = append(logs, l)
		f.logReqs = append(f.logReqs, l.RequestID)
	}
	if err := st.InsertRequestLogs(ctx, logs); err != nil {
		t.Fatalf("%s: insert request logs: %v", tag, err)
	}
	for _, reqID := range f.logReqs {
		var rowID int64
		if err := st.DB().QueryRowContext(ctx,
			`SELECT id FROM request_logs WHERE request_id = ?`, reqID).Scan(&rowID); err != nil {
			t.Fatalf("%s: find log row %s: %v", tag, reqID, err)
		}
		f.logIDs = append(f.logIDs, rowID)
	}
	return f
}

func (f *teamFixture) tag() string      { return f.name }
func (f *teamFixture) clientIP() string { return fmt.Sprintf("10.%d.0.1", f.id) }

func providerNames(ps []*Provider) string {
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}

// mustBeNotFound is the shape every crossing has to take. ErrNotFound and never
// a permission error: a refusal would confirm the row is there.
func mustBeNotFound(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s succeeded", what)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("%s returned %v, want ErrNotFound — a tenant boundary must not answer with a "+
			"permission error, which tells the caller the row exists", what, err)
	}
}

func scalar[T any](t *testing.T, db *sql.DB, query string, args ...any) T {
	t.Helper()
	var v T
	if err := db.QueryRow(query, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return v
}
