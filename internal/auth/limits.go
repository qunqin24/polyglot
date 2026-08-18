package auth

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/store"
)

// KeyLimiter enforces rolling usage windows for this Polyglot process. The
// completed request log rebuilds the windows after a restart; concurrency is
// deliberately process-local because an in-flight request cannot survive one.
type KeyLimiter struct {
	store *store.Store
	now   func() time.Time

	mu     sync.Mutex
	nextID uint64
	keys   map[int64]*keyUsage
}

type keyUsage struct {
	loaded     bool
	concurrent int
	events     []usageEvent
	// What this key has spent in the window that spendWindow names. Loaded
	// from the request log once, then kept current by RecordSpend — the flush
	// goroutine knows every price the moment it computes one.
	spend       float64
	spendWindow time.Time
	spendLoaded bool
}

type usageEvent struct {
	id      uint64
	at      time.Time
	tokenAt time.Time
	tokens  int
}

type LimitError struct {
	Dimension  string
	Limit      int
	RetryAfter time.Duration
	// A budget is money rather than a count, so it carries its own two
	// figures and Limit says nothing. Budget is zero for every other limit.
	Spent  float64
	Budget float64
}

func (e *LimitError) Error() string {
	if e.Budget > 0 {
		return fmt.Sprintf("API key budget of $%.2f is spent ($%.2f used)", e.Budget, e.Spent)
	}
	return fmt.Sprintf("API key %s limit of %d exceeded", e.Dimension, e.Limit)
}

func NewKeyLimiter(st *store.Store) *KeyLimiter {
	return &KeyLimiter{store: st, now: time.Now, keys: map[int64]*keyUsage{}}
}

// QuotaLease represents one admitted client request. Complete releases its
// concurrency slot and records the actual canonical input+output usage once.
type QuotaLease struct {
	limiter *KeyLimiter
	keyID   int64
	eventID uint64
	once    sync.Once
}

func (l *QuotaLease) Complete(tokens int) {
	if l == nil {
		return
	}
	l.once.Do(func() { l.limiter.complete(l.keyID, l.eventID, tokens) })
}

func (l *KeyLimiter) Acquire(ctx context.Context, key *store.APIKey) (*QuotaLease, *LimitError, error) {
	if l == nil || key == nil {
		return nil, nil, nil
	}
	now := l.now().UTC()
	oldest := oldestRelevant(now)

	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.keys[key.ID]
	if state == nil {
		state = &keyUsage{}
		l.keys[key.ID] = state
	}
	if !state.loaded {
		samples, err := l.store.APIKeyUsageSince(ctx, key.ID, oldest)
		if err != nil {
			return nil, nil, err
		}
		for _, sample := range samples {
			state.events = append(state.events, usageEvent{at: sample.At, tokenAt: sample.FinishedAt, tokens: sample.Tokens})
		}
		state.loaded = true
	}
	state.events = pruneEvents(state.events, oldest)

	if limit := value(key.MaxConcurrent); limit > 0 && state.concurrent >= limit {
		return nil, &LimitError{Dimension: "concurrent requests", Limit: limit, RetryAfter: time.Second}, nil
	}
	if err := checkRequestLimits(state.events, key, now); err != nil {
		return nil, err, nil
	}
	if err := checkTokenLimits(state.events, key, now); err != nil {
		return nil, err, nil
	}
	if limitErr, err := l.checkBudget(ctx, key, state, now); err != nil || limitErr != nil {
		return nil, limitErr, err
	}

	l.nextID++
	state.events = append(state.events, usageEvent{id: l.nextID, at: now})
	state.concurrent++
	return &QuotaLease{limiter: l, keyID: key.ID, eventID: l.nextID}, nil, nil
}

// checkBudget stops a key that has spent its cap.
//
// The cap is approximate and cannot be otherwise: a request is priced after it
// finishes, so the one that crosses the line has already been paid for, and a
// model with no price adds nothing at all because an unknown cost is not zero.
// Both facts are stated in the UI rather than papered over here.
func (l *KeyLimiter) checkBudget(ctx context.Context, key *store.APIKey, state *keyUsage, now time.Time) (*LimitError, error) {
	if key.BudgetUSD == nil || *key.BudgetUSD <= 0 {
		return nil, nil
	}
	// A rolling period moves the start on its own, and resetting a total moves
	// the anchor; either way a different window means the figure is stale.
	start := key.BudgetWindowStart(now)
	if !state.spendLoaded || !state.spendWindow.Equal(start) {
		spent, _, err := l.store.APIKeySpendSince(ctx, key.ID, start)
		if err != nil {
			return nil, err
		}
		state.spend = spent
		state.spendWindow = start
		state.spendLoaded = true
	}
	if state.spend < *key.BudgetUSD {
		return nil, nil
	}
	var retry time.Duration
	if resets := key.BudgetResets(now); !resets.IsZero() {
		retry = resets.Sub(now)
	}
	return &LimitError{Dimension: "budget", RetryAfter: retry, Spent: state.spend, Budget: *key.BudgetUSD}, nil
}

// RecordSpend adds a finished request's cost to its key's window. Called from
// the usage logger's flush goroutine, which is where a price is worked out.
//
// A key whose window has not been read from the log yet is skipped rather than
// started at this one cost: the read that comes with the next request would
// then count the same row twice once it reaches SQLite. The cost of that
// choice is bounded — one request, only in the gap between a process start (or
// a window rollover) and the first budget check for that key.
func (l *KeyLimiter) RecordSpend(keyID int64, usd float64) {
	if l == nil || usd <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if state := l.keys[keyID]; state != nil && state.spendLoaded {
		state.spend += usd
	}
}

func (l *KeyLimiter) complete(keyID int64, eventID uint64, tokens int) {
	if tokens < 0 {
		tokens = 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.keys[keyID]
	if state == nil {
		return
	}
	if state.concurrent > 0 {
		state.concurrent--
	}
	for i := range state.events {
		if state.events[i].id == eventID {
			state.events[i].tokens = tokens
			state.events[i].tokenAt = l.now().UTC()
			break
		}
	}
}

func checkRequestLimits(events []usageEvent, key *store.APIKey, now time.Time) *LimitError {
	checks := []struct {
		name  string
		limit int
		since time.Time
	}{
		{"RPM", value(key.RPM), now.Add(-time.Minute)},
		{"RPH", value(key.RPH), now.Add(-time.Hour)},
		{"RPD", value(key.RPD), utcDayStart(now)},
	}
	for _, check := range checks {
		if check.limit <= 0 {
			continue
		}
		count := 0
		var first time.Time
		for _, event := range events {
			if !event.at.Before(check.since) {
				if count == 0 {
					first = event.at
				}
				count++
			}
		}
		if count >= check.limit {
			retry := first.Add(windowFor(check.name)).Sub(now)
			if check.name == "RPD" {
				retry = utcDayStart(now).Add(24 * time.Hour).Sub(now)
			}
			return &LimitError{Dimension: check.name, Limit: check.limit, RetryAfter: atLeastSecond(retry)}
		}
	}
	return nil
}

func checkTokenLimits(events []usageEvent, key *store.APIKey, now time.Time) *LimitError {
	checks := []struct {
		name  string
		limit int
		since time.Time
	}{
		{"TPM", value(key.TPM), now.Add(-time.Minute)},
		{"TPD", value(key.TPD), utcDayStart(now)},
	}
	for _, check := range checks {
		if check.limit <= 0 {
			continue
		}
		tokens := 0
		var first time.Time
		for _, event := range events {
			if !event.tokenAt.Before(check.since) && event.tokens > 0 {
				if first.IsZero() {
					first = event.tokenAt
				}
				tokens += event.tokens
			}
		}
		if tokens >= check.limit {
			retry := first.Add(time.Minute).Sub(now)
			if check.name == "TPD" {
				retry = utcDayStart(now).Add(24 * time.Hour).Sub(now)
			}
			return &LimitError{Dimension: check.name, Limit: check.limit, RetryAfter: atLeastSecond(retry)}
		}
	}
	return nil
}

func ApplyRequestPolicy(key *store.APIKey, req *canonical.Request) *canonical.Error {
	if key == nil || req == nil {
		return nil
	}
	if !ModelAllowed(key, req.Model) {
		return &canonical.Error{Type: canonical.ErrPermission, Code: "model_not_allowed", Param: "model",
			Message: fmt.Sprintf("model %q is not allowed for this API key", req.Model)}
	}
	if limit := value(key.MaxOutputTokens); limit > 0 {
		req.PolicyMaxTokens = canonical.Ptr(limit)
		if req.MaxTokens == nil {
			req.MaxTokens = canonical.Ptr(limit)
		} else if *req.MaxTokens > limit {
			return &canonical.Error{Type: canonical.ErrInvalidRequest, Code: "max_output_tokens_exceeded",
				Param: "max_tokens", Message: fmt.Sprintf("requested maximum output of %d tokens exceeds this API key's limit of %d", *req.MaxTokens, limit)}
		}
	}
	return nil
}

// ModelAllowed is also used by model-discovery endpoints, so a restricted key
// does not advertise models it cannot call.
func ModelAllowed(key *store.APIKey, model string) bool {
	return key == nil || len(key.AllowedModels) == 0 || slices.Contains(key.AllowedModels, model)
}

func value(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func pruneEvents(events []usageEvent, oldest time.Time) []usageEvent {
	out := events[:0]
	for _, event := range events {
		if (event.id != 0 && event.tokenAt.IsZero()) || !event.at.Before(oldest) || !event.tokenAt.Before(oldest) {
			out = append(out, event)
		}
	}
	return out
}

func oldestRelevant(now time.Time) time.Time {
	hour := now.Add(-time.Hour)
	day := utcDayStart(now)
	if day.Before(hour) {
		return day
	}
	return hour
}

func utcDayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func windowFor(name string) time.Duration {
	if name == "RPM" {
		return time.Minute
	}
	return time.Hour
}

func atLeastSecond(d time.Duration) time.Duration {
	if d < time.Second {
		return time.Second
	}
	return d
}
