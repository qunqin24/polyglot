package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/qunqin24/polyglot/internal/pricing"
	"github.com/qunqin24/polyglot/internal/store"
)

// A reply with a prompt cache in it, so the cost is the cache-aware sum rather
// than a multiplication.
const cachedChatResponse = `{"id":"x","choices":[{"index":0,"message":{"role":"assistant",` +
	`"content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,` +
	`"completion_tokens":500,"prompt_tokens_details":{"cached_tokens":800}}}`

// registerPricedModel puts the model the default alias points at into the
// registry, which is where an override lives.
func registerPricedModel(t *testing.T, st *store.Store, providerID int64) {
	t.Helper()
	if _, err := st.CreateModel(context.Background(), &store.Model{
		ProviderID: providerID, UpstreamModelID: "upstream-model-x", Enabled: true,
	}); err != nil {
		t.Fatalf("register model: %v", err)
	}
}

// The whole path: an operator sets a price, a request runs, and the log row
// carries what it cost. It also proves the resolver the usage logger prices
// from is reloaded when the price changes — a cached snapshot that never
// refreshed would keep pricing every request at the old number.
func TestSettingAPriceMakesTheNextRequestCostSomething(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, cachedChatResponse)
	}, "openai", withSetup(func(t *testing.T, st *store.Store, providerID int64) {
		registerPricedModel(t, st, providerID)
	}))

	admin := h.adminSession(t)

	var listed struct {
		Models []struct {
			ID              int64  `json:"id"`
			UpstreamModelID string `json:"upstream_model_id"`
			Source          string `json:"source"`
		} `json:"models"`
		Unpriced int `json:"unpriced"`
	}
	admin.get(t, "/api/pricing", &listed)
	if len(listed.Models) != 1 {
		t.Fatalf("pricing listed %d models, want the one in the registry", len(listed.Models))
	}
	// Nothing in a vendor catalog is called "upstream-model-x", so it starts
	// with no price — the normal state for anything bought through a reseller.
	if listed.Models[0].Source != "" || listed.Unpriced != 1 {
		t.Errorf("source = %q, unpriced = %d; want an unpriced model",
			listed.Models[0].Source, listed.Unpriced)
	}

	resp := admin.do(t, http.MethodPut, "/api/models/"+itoa(listed.Models[0].ID)+"/pricing",
		strings.NewReader(`{"input":3,"output":15,"cache_read":0.3,"cache_write":null}`))
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("set price: status %d, body %s", resp.StatusCode, body)
	}
	resp.Body.Close()

	readAll(t, h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, nil))

	log := h.waitForLog(t)
	if log.CostUSD == nil {
		t.Fatal("the request has no cost, but its model was priced before it ran")
	}
	// 200 fresh prompt tokens at $3, 800 cached at $0.30, 500 output at $15.
	want := 200*3/1e6 + 800*0.3/1e6 + 500*15/1e6
	if diff := *log.CostUSD - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("cost = %v, want %v — the cached prompt must be charged at the cache price",
			*log.CostUSD, want)
	}
	if log.CostSource != "custom" {
		t.Errorf("cost source = %q, want the operator's own price", log.CostSource)
	}
	if log.CostNote != "" {
		t.Errorf("cost note = %q, want none: every price this request needed was set", log.CostNote)
	}
}

// A long prompt is charged at the vendor's long-context rate, over the real
// path: catalog to resolver to request log. gpt-5.5 doubles above 272k tokens
// and gemini-2.5-pro rises above 200k, and those are the requests where the
// money is, so pricing them at the base rate got the largest amounts wrong.
func TestALongPromptIsChargedAtTheLongContextRate(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant",`+
			`"content":"hello"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":300000,"completion_tokens":1000}}`)
	}, "openai")

	ctx := context.Background()
	if err := h.store.ReplaceCatalog(ctx, &pricing.Snapshot{
		Version: "2099-01-01",
		Entries: []pricing.Entry{{
			ID: "upstream-model-x", Vendor: "openai", Rates: pricing.Rates{
				Price: pricing.Price{Input: usdPtr(5), Output: usdPtr(30)},
				Tier: &pricing.Tier{AboveTokens: 272_000, Price: pricing.Price{
					Input: usdPtr(10), Output: usdPtr(45),
				}},
			},
		}},
	}, "models.dev"); err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if err := h.prices.Reload(ctx); err != nil {
		t.Fatalf("reload prices: %v", err)
	}

	readAll(t, h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, nil))

	log := h.waitForLog(t)
	if log.CostUSD == nil {
		t.Fatal("no cost for a model the catalog prices")
	}
	want := 300_000*10/1e6 + 1000*45/1e6
	if diff := *log.CostUSD - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("cost = %v, want %v — a 300k prompt is past the 272k threshold", *log.CostUSD, want)
	}
	// The row has to say which rung it used, or the number looks like the base
	// rate applied to a suspiciously large prompt.
	if log.CostNote != pricing.NoteLongContext {
		t.Errorf("cost note = %q, want the long-context rate recorded", log.CostNote)
	}
}

func usdPtr(v float64) *float64 { return &v }

// A model nobody has a price for produces no cost at all. Zero would be a
// claim that the request was free, which is a different and unfounded answer.
func TestAnUnpricedModelLogsNoCostRatherThanZero(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, cachedChatResponse)
	}, "openai")

	readAll(t, h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, nil))

	log := h.waitForLog(t)
	if log.CostUSD != nil {
		t.Errorf("cost = %v for a model with no price anywhere, want none", *log.CostUSD)
	}
	if log.CostSource != "" {
		t.Errorf("cost source = %q on an unpriced request", log.CostSource)
	}

	// And the Overview total says so rather than quietly reporting $0.00 as if
	// the traffic were free.
	admin := h.adminSession(t)
	var stats struct {
		CostUSD          float64 `json:"cost_usd"`
		UnpricedRequests int64   `json:"unpriced_requests"`
	}
	admin.get(t, "/api/stats?hours=24", &stats)
	if stats.UnpricedRequests != 1 {
		t.Errorf("unpriced requests = %d, want the gap in the total reported", stats.UnpricedRequests)
	}
}

// Clearing a price puts the model back on the catalog. The endpoint takes four
// nullable numbers precisely so this is possible; a form that could only write
// values would strand every model on whatever was typed once.
func TestClearingAPriceIsPossibleThroughTheAPI(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, cachedChatResponse)
	}, "openai", withSetup(func(t *testing.T, st *store.Store, providerID int64) {
		registerPricedModel(t, st, providerID)
	}))

	admin := h.adminSession(t)
	var listed struct {
		Models []struct {
			ID int64 `json:"id"`
		} `json:"models"`
	}
	admin.get(t, "/api/pricing", &listed)
	id := itoa(listed.Models[0].ID)

	set := admin.do(t, http.MethodPut, "/api/models/"+id+"/pricing",
		strings.NewReader(`{"input":9,"output":9,"cache_read":null,"cache_write":null}`))
	set.Body.Close()

	cleared := admin.do(t, http.MethodPut, "/api/models/"+id+"/pricing",
		strings.NewReader(`{"input":null,"output":null,"cache_read":null,"cache_write":null}`))
	defer cleared.Body.Close()
	var out struct {
		Source string `json:"source"`
		Price  struct {
			Input *float64 `json:"input"`
		} `json:"price"`
	}
	if err := json.NewDecoder(cleared.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Price.Input != nil {
		t.Errorf("input override = %v after clearing it", *out.Price.Input)
	}
	// Nothing in the catalog answers to this id, so it is unpriced again
	// rather than stuck at the number that was typed.
	if out.Source != "" {
		t.Errorf("source = %q after clearing, want no price", out.Source)
	}
}

// A price that is not a number must not reach the database.
func TestANonsensePriceIsRejected(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, cachedChatResponse)
	}, "openai", withSetup(func(t *testing.T, st *store.Store, providerID int64) {
		registerPricedModel(t, st, providerID)
	}))

	admin := h.adminSession(t)
	var listed struct {
		Models []struct {
			ID int64 `json:"id"`
		} `json:"models"`
	}
	admin.get(t, "/api/pricing", &listed)

	resp := admin.do(t, http.MethodPut, "/api/models/"+itoa(listed.Models[0].ID)+"/pricing",
		strings.NewReader(`{"input":-5,"output":1}`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d for a negative price, want 400", resp.StatusCode)
	}
}
