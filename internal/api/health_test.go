package api

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/qunqin24/polyglot/internal/provider"
	"github.com/qunqin24/polyglot/internal/store"
)

// Retrying is the client's job; not sending it back into the same wall is the
// gateway's. Routing is deterministic, so without a cooldown a client's own
// retries would land on the same broken provider every time.

func TestAFailedProviderIsSkippedByTheNextRequest(t *testing.T) {
	h := provider.NewHealth(time.Minute)

	if !h.Available(1) {
		t.Fatal("a provider with no history should be available")
	}
	h.Failed(1, false)
	if h.Available(1) {
		t.Error("a provider that just failed is still being offered")
	}
	if h.CoolingUntil(1).IsZero() {
		t.Error("the cooldown is not visible, so the UI cannot explain the detour")
	}

	// Recovery clears it, so one bad minute is not held against a provider.
	h.Succeeded(1)
	if !h.Available(1) {
		t.Error("a provider that succeeded is still in cooldown")
	}
}

func TestTheCooldownExpires(t *testing.T) {
	h := provider.NewHealth(30 * time.Millisecond)
	h.Failed(1, false)
	if h.Available(1) {
		t.Fatal("not cooling immediately after a failure")
	}
	time.Sleep(50 * time.Millisecond)
	if !h.Available(1) {
		t.Error("the cooldown never expired")
	}
}

// Consecutive credential rejections are what identify a broken key. A
// different kind of failure in between means the last 401 was not part of a
// run, so the count starts over.
func TestOnlyConsecutiveAuthFailuresCount(t *testing.T) {
	h := provider.NewHealth(time.Minute)

	if got := h.Failed(1, true); got != 1 {
		t.Errorf("first auth failure = %d strikes, want 1", got)
	}
	if got := h.Failed(1, false); got != 0 {
		t.Errorf("a non-auth failure should reset the run, got %d", got)
	}
	if got := h.Failed(1, true); got != 1 {
		t.Errorf("the run restarted at %d, want 1", got)
	}
	if got := h.Failed(1, true); got != provider.AuthStrikesBeforeDisable {
		t.Errorf("two in a row = %d strikes, want %d", got, provider.AuthStrikesBeforeDisable)
	}
}

// The safety rule. A blip that touches every provider must not lock the
// gateway out of all of its own upstreams.
func TestEveryProviderCoolingFallsBackToTryingAnyway(t *testing.T) {
	var hitA, hitB int
	upA := newCountingUpstream(t, &hitA)
	upB := newCountingUpstream(t, &hitB)

	h := newHarness(t, nil, "openai", withSetup(func(t *testing.T, st *store.Store, first int64) {
		ctx := context.Background()
		for _, spec := range []struct {
			name, url string
			priority  int
		}{{"alpha", upA, 10}, {"beta", upB, 5}} {
			p, err := st.CreateProvider(ctx, &store.Provider{
				Name: spec.name, Protocol: "openai", BaseURL: spec.url,
				Enabled: true, Priority: spec.priority,
			})
			if err != nil {
				t.Fatalf("create provider: %v", err)
			}
			if _, err := st.CreateModel(ctx, &store.Model{
				ProviderID: p.ID, UpstreamModelID: "shared", Enabled: true,
			}); err != nil {
				t.Fatalf("create model: %v", err)
			}
		}
	}))

	// Put every provider into cooldown, including the default harness one.
	for _, p := range mustListProviders(t, h.store) {
		h.serverHealth().Failed(p.ID, false)
	}

	resp := h.post("/v1/chat/completions", chat("shared"), nil)
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("everything in cooldown refused the request: %d %s", resp.StatusCode, body)
	}
	if hitA+hitB == 0 {
		t.Error("no upstream was tried at all; the gateway locked itself out")
	}
}

// A model served by one provider has nothing to fall back to, so a cooldown
// there would only mean refusing to try the one upstream that exists.
func TestASoleProviderIsNeverSkipped(t *testing.T) {
	var hits int
	h := newHarness(t, newCountingHandler(&hits), "openai")

	p, err := h.store.ProviderByName(context.Background(), "fake")
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	h.serverHealth().Failed(p.ID, false)

	resp := h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a cooling sole provider was skipped: %d %s", resp.StatusCode, body)
	}
	if hits != 1 {
		t.Errorf("upstream hits = %d, want 1", hits)
	}
}

func newCountingHandler(counter *int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*counter++
		w.Write([]byte(`{"id":"x","choices":[{"index":0,` +
			`"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}
}

func mustListProviders(t *testing.T, st *store.Store) []*store.Provider {
	t.Helper()
	list, err := st.ListProviders(context.Background())
	if err != nil {
		t.Fatalf("list providers: %v", err)
	}
	return list
}

// Auto-disable, end to end. It is opt-in because 401 and 403 are not always
// about the key — a region restriction or an exhausted quota reads the same —
// so switching a provider off is the operator's decision.

func authRejectingUpstream(t *testing.T, hits *int) string {
	t.Helper()
	return httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"data":[{"id":"shared-model"}]}`)
			return
		}
		*hits++
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
	})
}

func TestARejectedCredentialDisablesTheProviderWhenAskedTo(t *testing.T) {
	var rejected int
	h := newHarness(t, nil, "openai", withSetup(func(t *testing.T, st *store.Store, first int64) {
		ctx := context.Background()
		p, err := st.CreateProvider(ctx, &store.Provider{
			Name: "expired", Protocol: "openai", BaseURL: authRejectingUpstream(t, &rejected),
			Enabled: true, Priority: 100, AutoDisableOnAuthError: true,
		})
		if err != nil {
			t.Fatalf("create provider: %v", err)
		}
		if _, err := st.CreateModel(ctx, &store.Model{
			ProviderID: p.ID, UpstreamModelID: "shared", Enabled: true,
		}); err != nil {
			t.Fatalf("create model: %v", err)
		}
	}))

	// Two consecutive rejections: one could be a middlebox, two is the key.
	for range 2 {
		readAll(t, h.post("/v1/chat/completions", chat("shared"), nil))
	}

	p, err := h.store.ProviderByName(context.Background(), "expired")
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	if p.Enabled {
		t.Fatal("the provider is still enabled after its credential was rejected twice")
	}
	if p.DisabledReason == "" {
		t.Error("a provider that switched itself off must say why")
	}
	if p.DisabledAt == nil {
		t.Error("no disabled_at was recorded")
	}

	// And it stays off: further requests do not reach it at all.
	before := rejected
	readAll(t, h.post("/v1/chat/completions", chat("shared"), nil))
	if rejected != before {
		t.Errorf("a disabled provider was called again: %d -> %d", before, rejected)
	}
}

// Without the opt-in, the same failures must leave the provider alone.
func TestARejectedCredentialDoesNotDisableByDefault(t *testing.T) {
	var rejected int
	h := newHarness(t, nil, "openai", withSetup(func(t *testing.T, st *store.Store, first int64) {
		ctx := context.Background()
		p, err := st.CreateProvider(ctx, &store.Provider{
			Name: "expired", Protocol: "openai", BaseURL: authRejectingUpstream(t, &rejected),
			Enabled: true, Priority: 100,
		})
		if err != nil {
			t.Fatalf("create provider: %v", err)
		}
		if _, err := st.CreateModel(ctx, &store.Model{
			ProviderID: p.ID, UpstreamModelID: "shared", Enabled: true,
		}); err != nil {
			t.Fatalf("create model: %v", err)
		}
	}))

	for range 3 {
		readAll(t, h.post("/v1/chat/completions", chat("shared"), nil))
	}

	p, err := h.store.ProviderByName(context.Background(), "expired")
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	if !p.Enabled {
		t.Fatal("a provider was disabled without the operator opting in")
	}
}

// Enabling a provider by hand clears the explanation, so a stale reason can
// never outlive the condition that caused it.
func TestEnablingAProviderClearsTheReason(t *testing.T) {
	h := newHarness(t, nil, "openai")
	ctx := context.Background()

	p, err := h.store.ProviderByName(ctx, "fake")
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	if err := h.store.DisableProvider(ctx, p.ID, "credential rejected"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	off, _ := h.store.GetProvider(ctx, p.ID)
	if off.Enabled || off.DisabledReason == "" {
		t.Fatalf("provider was not disabled with a reason: %+v", off)
	}

	off.Enabled = true
	if _, err := h.store.UpdateProvider(ctx, p.ID, off, nil); err != nil {
		t.Fatalf("update: %v", err)
	}
	back, _ := h.store.GetProvider(ctx, p.ID)
	if back.DisabledReason != "" || back.DisabledAt != nil {
		t.Errorf("a stale reason survived re-enabling: %q / %v", back.DisabledReason, back.DisabledAt)
	}
}

// Protocol preference, over real HTTP. The router unit tests prove the
// ordering rule; this proves the gateway actually applies it.
func TestAnAnthropicClientPrefersTheAnthropicUpstream(t *testing.T) {
	var openaiHits, anthropicHits int

	h := newHarness(t, nil, "openai", withSetup(func(t *testing.T, st *store.Store, first int64) {
		ctx := context.Background()
		specs := []struct {
			name, proto, url string
		}{
			// Created first, so without the preference this one would win the
			// tie on provider id alone.
			{"openai-proxy", "openai", httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
				openaiHits++
				io.WriteString(w, `{"id":"x","choices":[{"index":0,`+
					`"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			})},
			{"claude-direct", "anthropic", httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
				anthropicHits++
				io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m",`+
					`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",`+
					`"usage":{"input_tokens":1,"output_tokens":2}}`)
			})},
		}
		for _, spec := range specs {
			// Equal priority: the operator ranked them the same, which is the
			// only situation the preference is allowed to act in.
			p, err := st.CreateProvider(ctx, &store.Provider{
				Name: spec.name, Protocol: spec.proto, BaseURL: spec.url,
				Enabled: true, Priority: 5,
			})
			if err != nil {
				t.Fatalf("create provider: %v", err)
			}
			if _, err := st.CreateModel(ctx, &store.Model{
				ProviderID: p.ID, UpstreamModelID: "shared", Enabled: true,
			}); err != nil {
				t.Fatalf("create model: %v", err)
			}
		}
	}))

	// An Anthropic client asking for a model both providers offer.
	resp := h.post("/v1/messages",
		`{"model":"shared","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if body := readAll(t, resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if anthropicHits != 1 || openaiHits != 0 {
		t.Errorf("an Anthropic client was routed through a conversion: anthropic=%d openai=%d",
			anthropicHits, openaiHits)
	}

	// The same model from an OpenAI client goes the other way, for the same
	// reason — the preference is about the caller, not a fixed favourite.
	resp = h.post("/v1/chat/completions", chat("shared"), nil)
	if body := readAll(t, resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if openaiHits != 1 {
		t.Errorf("an OpenAI client was not routed to the OpenAI upstream: openai=%d", openaiHits)
	}
}
