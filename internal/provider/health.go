package provider

import (
	"sync"
	"time"
)

// Health remembers which upstreams just failed, so a request is not sent into
// a wall that the previous request already found.
//
// Retrying is the client's job. The gateway's job is to not keep offering the
// client the same broken door: routing is deterministic, so without this a
// client's own retries would land on the same failing provider every time and
// never discover the working one beside it.
//
// The state is per provider rather than per provider-and-model, because the
// failures worth reacting to — unreachable, credential rejected, rate limited
// — are properties of the upstream, not of one model on it.
//
// It is in-process and deliberately not persisted. A cooldown is a statement
// about the last thirty seconds; carrying it across a restart would mean
// starting up with opinions about a world that has moved on.
type Health struct {
	cooldown time.Duration

	mu    sync.Mutex
	state map[int64]*providerHealth
}

type providerHealth struct {
	// coolingUntil is when this provider may be tried again.
	coolingUntil time.Time
	// authStrikes counts consecutive credential rejections. One can be a
	// middlebox or a blip; two in a row from the same provider is the key.
	authStrikes int
}

// DefaultCooldown is how long a failed provider is skipped for. Short enough
// that a brief wobble costs almost nothing, long enough that a provider having
// a bad minute is not asked again on every request.
const DefaultCooldown = 30 * time.Second

// AuthStrikesBeforeDisable is how many consecutive credential rejections it
// takes to conclude the credential is genuinely broken rather than unlucky.
const AuthStrikesBeforeDisable = 2

func NewHealth(cooldown time.Duration) *Health {
	if cooldown <= 0 {
		cooldown = DefaultCooldown
	}
	return &Health{cooldown: cooldown, state: map[int64]*providerHealth{}}
}

// Available reports whether a provider may be tried now.
func (h *Health) Available(id int64) bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st, ok := h.state[id]
	return !ok || !time.Now().Before(st.coolingUntil)
}

// CoolingUntil reports when a provider becomes available again, or the zero
// time when it is available now. The WebUI reads this: a provider being
// skipped needs to be visible, or "why is all my traffic on the backup" has no
// answer.
func (h *Health) CoolingUntil(id int64) time.Time {
	if h == nil {
		return time.Time{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st, ok := h.state[id]
	if !ok || !time.Now().Before(st.coolingUntil) {
		return time.Time{}
	}
	return st.coolingUntil
}

// Failed records an upstream failure and starts the cooldown.
//
// authFailure marks a rejected credential, which is different in kind: a
// timeout or a 500 heals on its own, an expired key does not. It returns the
// number of consecutive credential rejections so the caller can decide whether
// the provider should be taken out of rotation entirely.
func (h *Health) Failed(id int64, authFailure bool) int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st, ok := h.state[id]
	if !ok {
		st = &providerHealth{}
		h.state[id] = st
	}
	st.coolingUntil = time.Now().Add(h.cooldown)
	if authFailure {
		st.authStrikes++
	} else {
		// A different kind of failure says nothing about the credential.
		st.authStrikes = 0
	}
	return st.authStrikes
}

// Succeeded clears everything remembered about a provider.
func (h *Health) Succeeded(id int64) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.state, id)
}

// Forget drops state for a provider that has been edited or removed, so a
// cooldown earned by an old configuration does not outlive it.
func (h *Health) Forget(id int64) { h.Succeeded(id) }
