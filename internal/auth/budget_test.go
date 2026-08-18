package auth

import (
	"context"
	"testing"
	"time"

	"github.com/qunqin24/polyglot/internal/store"
)

func money(v float64) *float64 { return &v }

// spend writes a finished request that cost something, the way the usage
// logger eventually does. A nil cost is a request nobody could price.
func spend(t *testing.T, st *store.Store, keyID int64, at time.Time, usd *float64) {
	t.Helper()
	rec := &store.RequestLog{
		RequestID:        "test",
		StartedAt:        at,
		FinishedAt:       at,
		APIKeyID:         &keyID,
		InputTokens:      10,
		OutputTokens:     10,
		CostUSD:          usd,
		ClientProtocol:   "openai",
		UpstreamProtocol: "openai",
		Status:           "success",
	}
	if err := st.InsertRequestLogs(context.Background(), []*store.RequestLog{rec}); err != nil {
		t.Fatalf("insert request log: %v", err)
	}
}

func TestBudgetRefusesAKeyThatHasSpentIt(t *testing.T) {
	st := limitStore(t)
	key := restrictedKey(t, st, store.APIKeyPolicy{BudgetUSD: money(1), BudgetPeriod: store.BudgetTotal})
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	limiter := NewKeyLimiter(st)
	limiter.now = func() time.Time { return now }

	lease, denied, err := limiter.Acquire(context.Background(), key)
	if err != nil || denied != nil {
		t.Fatalf("a key with an unspent budget was refused: denied=%v err=%v", denied, err)
	}
	lease.Complete(20)

	// The flush goroutine prices the request and says so.
	limiter.RecordSpend(key.ID, 1.25)

	_, denied, err = limiter.Acquire(context.Background(), key)
	if err != nil || denied == nil {
		t.Fatalf("a spent budget did not refuse the next request: denied=%v err=%v", denied, err)
	}
	if denied.Budget != 1 || denied.Spent != 1.25 {
		t.Fatalf("denial = %+v; want budget 1 spent 1.25", denied)
	}
	// A total has no window to wait for, so there is no retry to promise.
	if denied.RetryAfter != 0 {
		t.Fatalf("total budget promised a retry after %v", denied.RetryAfter)
	}
}

func TestBudgetCountsOnlyItsOwnWindow(t *testing.T) {
	st := limitStore(t)
	key := restrictedKey(t, st, store.APIKeyPolicy{BudgetUSD: money(1), BudgetPeriod: store.BudgetDaily})
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	limiter := NewKeyLimiter(st)
	limiter.now = func() time.Time { return now }

	// Yesterday's spending belongs to yesterday's budget.
	spend(t, st, key.ID, now.AddDate(0, 0, -1), money(5))
	lease, denied, err := limiter.Acquire(context.Background(), key)
	if err != nil || denied != nil {
		t.Fatalf("yesterday's spend exhausted today's budget: denied=%v err=%v", denied, err)
	}
	lease.Complete(0)

	spend(t, st, key.ID, now, money(2))
	// A new window forces the figure to be read again; the same window does
	// not, so this test starts a fresh limiter to model a later request.
	limiter = NewKeyLimiter(st)
	limiter.now = func() time.Time { return now }
	_, denied, err = limiter.Acquire(context.Background(), key)
	if err != nil || denied == nil {
		t.Fatalf("today's spend did not exhaust today's budget: denied=%v err=%v", denied, err)
	}
	// A daily budget does roll over, so this one can say when to come back.
	if denied.RetryAfter <= 0 {
		t.Fatalf("daily budget gave no retry time")
	}

	// Tomorrow the same key is admitted again, with no operator involved.
	now = now.AddDate(0, 0, 1)
	lease, denied, err = limiter.Acquire(context.Background(), key)
	if err != nil || denied != nil {
		t.Fatalf("the window did not roll over: denied=%v err=%v", denied, err)
	}
	lease.Complete(0)
}

// A model with no price leaves cost_usd null. Treating that as zero would turn
// a budget into a cap on the priced half of the traffic while claiming to cap
// all of it, so the total says nothing about those requests and the UI counts
// them separately.
func TestUnpricedRequestsAreNotFree(t *testing.T) {
	st := limitStore(t)
	key := restrictedKey(t, st, store.APIKeyPolicy{BudgetUSD: money(1), BudgetPeriod: store.BudgetTotal})
	// A total window starts when the key was created, so the spending has to
	// come after it — which is also the rule that keeps a key from inheriting
	// costs incurred before it existed.
	now := time.Now().UTC().Add(time.Minute)

	spend(t, st, key.ID, now, nil)
	spend(t, st, key.ID, now, money(0.5))

	spent, unpriced, err := st.APIKeySpendSince(context.Background(), key.ID, key.BudgetWindowStart(now))
	if err != nil {
		t.Fatalf("sum spend: %v", err)
	}
	if spent != 0.5 {
		t.Fatalf("spent = %v; want 0.5", spent)
	}
	if unpriced != 1 {
		t.Fatalf("unpriced = %d; want 1 — an unknown cost must be reported, not folded into the total", unpriced)
	}
}

func TestNoBudgetNeverRefuses(t *testing.T) {
	st := limitStore(t)
	key := restrictedKey(t, st, store.APIKeyPolicy{})
	limiter := NewKeyLimiter(st)

	spend(t, st, key.ID, time.Now(), money(9999))
	lease, denied, err := limiter.Acquire(context.Background(), key)
	if err != nil || denied != nil {
		t.Fatalf("a key with no budget was refused over money: denied=%v err=%v", denied, err)
	}
	lease.Complete(0)
}
