package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qunqin24/polyglot/internal/store"
)

func limitKey(t *testing.T, st *store.Store, policy store.APIKeyPolicy) {
	t.Helper()
	keys, err := st.ListAPIKeys(context.Background())
	if err != nil || len(keys) != 1 {
		t.Fatalf("list key: %v (%d keys)", err, len(keys))
	}
	if _, err := st.UpdateAPIKey(context.Background(), keys[0].ID, keys[0].Name, true, policy); err != nil {
		t.Fatalf("apply key policy: %v", err)
	}
}

func keyInt(v int) *int { return &v }

func TestAPIKeyOutputAndModelPoliciesApplyBeforeRouting(t *testing.T) {
	var sent string
	h := newHarness(t, captureUpstream(&sent, okChatResponse), "openai",
		withSetup(func(t *testing.T, st *store.Store, _ int64) {
			limitKey(t, st, store.APIKeyPolicy{
				MaxOutputTokens: keyInt(64), AllowedModels: []string{"my-model"},
			})
		}))

	resp := h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var upstream struct {
		MaxTokens *int `json:"max_completion_tokens"`
	}
	if err := json.Unmarshal([]byte(sent), &upstream); err != nil {
		t.Fatalf("decode upstream request: %v", err)
	}
	if upstream.MaxTokens == nil || *upstream.MaxTokens != 64 {
		t.Fatalf("upstream max_completion_tokens = %v, want injected 64", upstream.MaxTokens)
	}

	resp = h.post("/v1/chat/completions",
		`{"model":"my-model","max_tokens":65,"messages":[{"role":"user","content":"hi"}]}`, nil)
	readAll(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("over-limit output status = %d, want 400", resp.StatusCode)
	}

	resp = h.post("/v1/chat/completions",
		`{"model":"another-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	readAll(t, resp)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("disallowed model status = %d, want 403", resp.StatusCode)
	}
}

func TestAPIKeyRPMReturns429WithoutCallingUpstreamAgain(t *testing.T) {
	var calls atomic.Int32
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, okChatResponse)
	}, "openai", withSetup(func(t *testing.T, st *store.Store, _ int64) {
		limitKey(t, st, store.APIKeyPolicy{RPM: keyInt(1)})
	}))

	request := `{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`
	first := h.post("/v1/chat/completions", request, nil)
	readAll(t, first)
	second := h.post("/v1/chat/completions", request, nil)
	readAll(t, second)
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429", second.StatusCode)
	}
	if second.Header.Get("Retry-After") == "" {
		t.Error("rate-limit response has no Retry-After header")
	}
	if calls.Load() != 1 {
		t.Errorf("upstream calls = %d, want 1", calls.Load())
	}
}

func TestExpiredAPIKeyIsRejectedByAuthentication(t *testing.T) {
	var calls atomic.Int32
	expired := time.Now().Add(-time.Minute)
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, okChatResponse)
	}, "openai", withSetup(func(t *testing.T, st *store.Store, _ int64) {
		limitKey(t, st, store.APIKeyPolicy{ExpiresAt: &expired})
	}))

	resp := h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	readAll(t, resp)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if calls.Load() != 0 {
		t.Errorf("expired key reached upstream %d times", calls.Load())
	}
}
