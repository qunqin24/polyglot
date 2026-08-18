package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/store"
)

func limitStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "polyglot.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func restrictedKey(t *testing.T, st *store.Store, p store.APIKeyPolicy) *store.APIKey {
	t.Helper()
	_, prefix, hash := NewAPIKey()
	k, err := st.CreateAPIKeyWithPolicy(context.Background(), "limited", prefix, hash, p)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	return k
}

func pint(v int) *int { return &v }

func TestRequestLimitsUseRollingWindows(t *testing.T) {
	st := limitStore(t)
	key := restrictedKey(t, st, store.APIKeyPolicy{RPM: pint(1)})
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	limiter := NewKeyLimiter(st)
	limiter.now = func() time.Time { return now }

	lease, denied, err := limiter.Acquire(context.Background(), key)
	if err != nil || denied != nil || lease == nil {
		t.Fatalf("first request was not admitted: lease=%v denied=%v err=%v", lease, denied, err)
	}
	lease.Complete(4)
	if _, denied, err := limiter.Acquire(context.Background(), key); err != nil || denied == nil || denied.Dimension != "RPM" {
		t.Fatalf("second request = denied %v, err %v; want RPM denial", denied, err)
	}

	now = now.Add(time.Minute + time.Second)
	lease, denied, err = limiter.Acquire(context.Background(), key)
	if err != nil || denied != nil || lease == nil {
		t.Fatalf("request after rolling window was not admitted: denied=%v err=%v", denied, err)
	}
	lease.Complete(0)
}

func TestConcurrencyIsReleasedOnlyWhenTheLeaseCompletes(t *testing.T) {
	st := limitStore(t)
	key := restrictedKey(t, st, store.APIKeyPolicy{MaxConcurrent: pint(1)})
	limiter := NewKeyLimiter(st)

	lease, denied, err := limiter.Acquire(context.Background(), key)
	if err != nil || denied != nil {
		t.Fatalf("first acquire: %v, %v", denied, err)
	}
	if _, denied, _ := limiter.Acquire(context.Background(), key); denied == nil || denied.Dimension != "concurrent requests" {
		t.Fatalf("second acquire = %v, want concurrency denial", denied)
	}
	lease.Complete(0)
	lease2, denied, err := limiter.Acquire(context.Background(), key)
	if err != nil || denied != nil || lease2 == nil {
		t.Fatalf("slot was not released: denied=%v err=%v", denied, err)
	}
	lease2.Complete(0)
}

func TestTokenLimitsUseCompletedCanonicalUsage(t *testing.T) {
	st := limitStore(t)
	key := restrictedKey(t, st, store.APIKeyPolicy{TPM: pint(10)})
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	limiter := NewKeyLimiter(st)
	limiter.now = func() time.Time { return now }

	lease, _, _ := limiter.Acquire(context.Background(), key)
	// An in-flight request has not consumed its reported usage yet.
	second, denied, err := limiter.Acquire(context.Background(), key)
	if err != nil || denied != nil {
		t.Fatalf("in-flight request incorrectly exhausted TPM: denied=%v err=%v", denied, err)
	}
	second.Complete(0)
	lease.Complete(10)
	if _, denied, _ := limiter.Acquire(context.Background(), key); denied == nil || denied.Dimension != "TPM" {
		t.Fatalf("completed usage did not exhaust TPM: %v", denied)
	}
}

func TestRequestPolicyRestrictsModelsAndOutput(t *testing.T) {
	key := &store.APIKey{AllowedModels: []string{"coding"}, MaxOutputTokens: pint(128)}
	req := &canonical.Request{Model: "coding"}
	if err := ApplyRequestPolicy(key, req); err != nil {
		t.Fatalf("allowed request: %v", err)
	}
	if req.MaxTokens == nil || *req.MaxTokens != 128 {
		t.Fatalf("missing output limit was not injected: %v", req.MaxTokens)
	}

	tooLarge := &canonical.Request{Model: "coding", MaxTokens: pint(129)}
	if err := ApplyRequestPolicy(key, tooLarge); err == nil || err.Code != "max_output_tokens_exceeded" {
		t.Fatalf("too-large output = %v", err)
	}
	wrongModel := &canonical.Request{Model: "other"}
	if err := ApplyRequestPolicy(key, wrongModel); err == nil || err.Code != "model_not_allowed" {
		t.Fatalf("wrong model = %v", err)
	}
}

func TestAPIKeyPolicyRoundTripsThroughSQLite(t *testing.T) {
	st := limitStore(t)
	expires := time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC)
	created := restrictedKey(t, st, store.APIKeyPolicy{
		RPM: pint(12), TPD: pint(5000), MaxConcurrent: pint(3), MaxOutputTokens: pint(256),
		ExpiresAt: &expires, AllowedModels: []string{"z-model", "coding", "coding"},
	})
	got, err := st.GetAPIKey(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if got.RPM == nil || *got.RPM != 12 || got.TPD == nil || *got.TPD != 5000 || got.MaxConcurrent == nil || *got.MaxConcurrent != 3 {
		t.Fatalf("numeric limits did not round-trip: %+v", got)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) {
		t.Fatalf("expiration = %v, want %v", got.ExpiresAt, expires)
	}
	if len(got.AllowedModels) != 2 || got.AllowedModels[0] != "coding" || got.AllowedModels[1] != "z-model" {
		t.Fatalf("models were not cleaned and sorted: %v", got.AllowedModels)
	}
}
