package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/qunqin24/polyglot/internal/store"
)

// The acceptance scenarios for model discovery: a provider must be usable
// straight after it is added, without anyone creating a mapping.

// modelListHandler serves an OpenAI-style model list and echoes chat requests
// back so a test can see which model actually went upstream.
func modelListHandler(t *testing.T, ids []string, capture *string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			data := make([]map[string]string, 0, len(ids))
			for _, id := range ids {
				data = append(data, map[string]string{"id": id})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data})
			return
		}
		b, _ := io.ReadAll(r.Body)
		if capture != nil {
			*capture = string(b)
		}
		io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}
}

func chat(model string) string {
	return `{"model":"` + model + `","messages":[{"role":"user","content":"x"}]}`
}

// Discovery proposes; the operator disposes. Listing an upstream shows what is
// on offer and writes nothing; only the models ticked in the picker are
// registered, and those are callable straight away with no alias.
func TestPickedModelIsImmediatelyCallable(t *testing.T) {
	var upstreamBody string
	h := newHarness(t, modelListHandler(t, []string{"anthropic/claude-sonnet-4", "openai/gpt-4o"}, &upstreamBody), "openai")
	admin := h.adminSession(t)

	// Step one: look at what the upstream offers, before anything is saved.
	var offer struct {
		OK     bool `json:"ok"`
		Models []struct {
			ID         string `json:"id"`
			Registered bool   `json:"registered"`
		} `json:"models"`
	}
	admin.send(t, http.MethodPost, "/api/providers/discover", map[string]any{
		"protocol": "openai", "base_url": h.upstream.URL, "api_key": "sk-test",
	}, &offer)
	if !offer.OK || len(offer.Models) != 2 {
		t.Fatalf("discovery offered %+v", offer)
	}

	// Step two: save the provider, picking one of the two.
	var created struct {
		Provider    store.Provider `json:"provider"`
		ModelsAdded int            `json:"models_added"`
	}
	admin.send(t, http.MethodPost, "/api/providers", map[string]any{
		"name": "OpenRouter", "protocol": "openai", "base_url": h.upstream.URL,
		"api_key": "sk-test", "headers": map[string]string{}, "timeout_secs": 0, "enabled": true,
		"models": []map[string]string{{"id": "anthropic/claude-sonnet-4"}},
	}, &created)

	if created.ModelsAdded != 1 {
		t.Fatalf("registered %d models, want exactly the one that was picked", created.ModelsAdded)
	}

	// No alias exists. The discovered id must work as-is.
	resp := h.post("/v1/chat/completions", chat("anthropic/claude-sonnet-4"), nil)
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(upstreamBody, `"model":"anthropic/claude-sonnet-4"`) {
		t.Errorf("the discovered id was not forwarded unchanged: %s", upstreamBody)
	}

	// The model that was NOT picked must not exist anywhere: not in the
	// registry, and not on the client-facing listing. This is the whole point
	// of the change — nothing arrives that nobody chose.
	var registry struct {
		Models []store.Model `json:"models"`
	}
	admin.get(t, "/api/models", &registry)
	if len(registry.Models) != 1 || registry.Models[0].UpstreamModelID != "anthropic/claude-sonnet-4" {
		t.Fatalf("the registry holds models nobody picked: %+v", registry.Models)
	}

	// And the picked one must be advertised to clients.
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	req, _ := http.NewRequest(http.MethodGet, h.server.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+h.clientKey)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	json.Unmarshal([]byte(readAll(t, r)), &list)
	// The harness also seeds an alias, so check membership rather than the
	// whole list: the picked model is advertised, the unpicked one is not.
	var advertised []string
	for _, m := range list.Data {
		advertised = append(advertised, m.ID)
	}
	if !slices.Contains(advertised, "anthropic/claude-sonnet-4") {
		t.Errorf("the picked model is not advertised on /v1/models: %v", advertised)
	}
	if slices.Contains(advertised, "openai/gpt-4o") {
		t.Errorf("a model nobody picked is advertised on /v1/models: %v", advertised)
	}
}

// A provider whose listing fails must still be usable: discovery is a
// convenience, not a gate. Models get typed in instead.
func TestListingFailureStillLeavesAWorkingProvider(t *testing.T) {
	var upstreamBody string
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// No model listing on this upstream.
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":{"message":"not found"}}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}, "openai")
	admin := h.adminSession(t)

	// Listing fails, and says so, without being an error the operator must fix.
	var offer struct {
		OK        bool   `json:"ok"`
		Supported bool   `json:"supported"`
		Error     string `json:"error"`
	}
	admin.send(t, http.MethodPost, "/api/providers/discover", map[string]any{
		"protocol": "openai", "base_url": h.upstream.URL, "api_key": "sk-test",
	}, &offer)
	if offer.OK {
		t.Errorf("listing reported success against a 404: %+v", offer)
	}
	if offer.Error == "" {
		t.Error("a failed listing must explain itself")
	}

	var created struct {
		Provider store.Provider `json:"provider"`
	}
	admin.send(t, http.MethodPost, "/api/providers", map[string]any{
		"name": "Private", "protocol": "openai", "base_url": h.upstream.URL,
		"api_key": "sk-test", "headers": map[string]string{}, "timeout_secs": 0, "enabled": true,
	}, &created)

	// The provider exists even though nothing could be listed and nothing was
	// picked. A provider with no models is a valid configuration.
	if created.Provider.ID == 0 {
		t.Fatal("provider was not created when listing failed")
	}

	// A model typed in by hand is callable exactly like one picked from a list.
	var m store.Model
	admin.send(t, http.MethodPost, "/api/models", map[string]any{
		"provider_id": created.Provider.ID, "upstream_model_id": "custom-model",
		"display_name": "Custom", "enabled": true,
	}, &m)
	if m.UpstreamModelID != "custom-model" {
		t.Errorf("model = %+v", m)
	}

	resp := h.post("/v1/chat/completions", chat("custom-model"), nil)
	if body := readAll(t, resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("manual model not callable: status %d, body %s", resp.StatusCode, body)
	}
	if !strings.Contains(upstreamBody, `"model":"custom-model"`) {
		t.Errorf("manual model was not forwarded: %s", upstreamBody)
	}
}

// Scenario 5: the same model id on two providers must resolve deterministically
// and stay addressable per-provider.
func TestScenarioAmbiguousModelID(t *testing.T) {
	var hitA, hitB int
	h := newHarness(t, modelListHandler(t, []string{"shared-model"}, nil), "openai")

	upstreamA := newCountingUpstream(t, &hitA)
	upstreamB := newCountingUpstream(t, &hitB)

	ctx := context.Background()
	// B is created first but has the lower priority number, so A must win.
	pb, err := h.store.CreateProvider(ctx, &store.Provider{
		Name: "Beta", Protocol: "openai", BaseURL: upstreamB, Enabled: true, Priority: 1,
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	pa, err := h.store.CreateProvider(ctx, &store.Provider{
		Name: "Alpha", Protocol: "openai", BaseURL: upstreamA, Enabled: true, Priority: 10,
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	for _, id := range []int64{pa.ID, pb.ID} {
		if _, err := h.store.SyncModels(ctx, id, []store.DiscoveredModel{{ID: "shared-model"}}); err != nil {
			t.Fatalf("sync: %v", err)
		}
	}

	// Unqualified: the highest provider priority wins, every time.
	for range 3 {
		resp := h.post("/v1/chat/completions", chat("shared-model"), nil)
		if body := readAll(t, resp); resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, body)
		}
	}
	if hitA != 3 || hitB != 0 {
		t.Errorf("ambiguous resolution is not deterministic: Alpha=%d Beta=%d", hitA, hitB)
	}

	// Qualified: the operator names the provider outright.
	resp := h.post("/v1/chat/completions", chat("Beta::shared-model"), nil)
	if body := readAll(t, resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("qualified call failed: %d %s", resp.StatusCode, body)
	}
	if hitB != 1 {
		t.Errorf("provider::model did not reach Beta (hits=%d)", hitB)
	}

	// Provider names are typed by hand, so matching ignores case.
	resp = h.post("/v1/chat/completions", chat("beta::shared-model"), nil)
	if body := readAll(t, resp); resp.StatusCode != http.StatusOK {
		t.Errorf("provider name matching should be case-insensitive: %d %s", resp.StatusCode, body)
	}

	// The ambiguity must be visible to an operator.
	ambiguous, err := h.store.AmbiguousModelIDs(ctx)
	if err != nil {
		t.Fatalf("AmbiguousModelIDs: %v", err)
	}
	if !ambiguous["shared-model"] {
		t.Error("the duplicated id was not flagged as ambiguous")
	}
}

func newCountingUpstream(t *testing.T, counter *int) string {
	t.Helper()
	srv := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"data":[{"id":"shared-model"}]}`)
			return
		}
		*counter++
		io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	})
	return srv
}

// Scenario 3 and 4: an alias resolves to its target, and it takes precedence
// over a real model id of the same name.
func TestScenarioAliasTakesPrecedenceOverRealModel(t *testing.T) {
	var upstreamBody string
	h := newHarness(t, modelListHandler(t, []string{"coding"}, &upstreamBody), "openai")
	ctx := context.Background()

	// The upstream genuinely offers a model called "coding"...
	if _, err := h.store.SyncModels(ctx, 1, []store.DiscoveredModel{{ID: "coding"}}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	// ...and the operator also defines an alias with that name pointing
	// somewhere else. The alias is the deliberate instruction, so it wins.
	if _, err := h.store.CreateAlias(ctx, &store.ModelAlias{
		Alias: "coding", ProviderID: 1, UpstreamModel: "upstream-model-x", Enabled: true,
	}); err != nil {
		t.Fatalf("create alias: %v", err)
	}

	readAll(t, h.post("/v1/chat/completions", chat("coding"), nil))
	if !strings.Contains(upstreamBody, `"model":"upstream-model-x"`) {
		t.Errorf("the alias did not take precedence: %s", upstreamBody)
	}
}

// TestSyncDoesNotDeleteOrOverrideOperatorChoices pins the safe-sync rule: a
// model missing from one listing must not vanish, and a disabled model must
// not silently come back.
func TestSyncDoesNotDeleteOrOverrideOperatorChoices(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {}, "openai")
	ctx := context.Background()

	if _, err := h.store.SyncModels(ctx, 1, []store.DiscoveredModel{
		{ID: "model-a"}, {ID: "model-b"},
	}); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	// The operator adds one by hand and turns another off.
	if _, err := h.store.CreateModel(ctx, &store.Model{
		ProviderID: 1, UpstreamModelID: "hand-added", Enabled: true,
	}); err != nil {
		t.Fatalf("create model: %v", err)
	}
	models, _ := h.store.ListModels(ctx, store.ModelFilter{ProviderID: 1})
	var bID int64
	for _, m := range models {
		if m.UpstreamModelID == "model-b" {
			bID = m.ID
		}
	}
	if _, err := h.store.UpdateModel(ctx, bID, "", false); err != nil {
		t.Fatalf("disable model: %v", err)
	}

	// A later sync returns only model-a: a partial or changed listing.
	res, err := h.store.SyncModels(ctx, 1, []store.DiscoveredModel{{ID: "model-a"}})
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if res.Total != 3 {
		t.Errorf("sync deleted rows: total = %d, want 3", res.Total)
	}

	after, _ := h.store.ListModels(ctx, store.ModelFilter{ProviderID: 1})
	byID := map[string]*store.Model{}
	for _, m := range after {
		byID[m.UpstreamModelID] = m
	}
	if byID["model-b"] == nil {
		t.Error("a model missing from one listing was deleted")
	} else if byID["model-b"].Enabled {
		t.Error("sync re-enabled a model the operator had disabled")
	}
	if byID["hand-added"] == nil {
		t.Error("sync deleted a hand-typed model")
	}
	if byID["model-a"].LastSeenAt == nil {
		t.Error("last_seen_at was not recorded for a model that was seen")
	}
}
