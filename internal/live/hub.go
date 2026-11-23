// Package live is the in-process broadcast hub behind the WebUI's "happening
// now" panel. The gateway publishes what a request is doing as it does it; the
// admin API fans that out to every open dashboard.
//
// Nothing here is persistent and nothing here may slow a request down: Publish
// never blocks, and a subscriber that cannot keep up loses events rather than
// holding anyone up. The request log stays the record of what happened; this
// is only a view of what is happening.
package live

import (
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// Event types. The JSON shape is a contract with the WebUI.
const (
	TypeSnapshot   = "snapshot"
	TypeStart      = "start"
	TypeFirstToken = "first_token"
	TypeProgress   = "progress"
	TypeEnd        = "end"
)

// Request statuses.
const (
	StatusRunning = "running"
	StatusOK      = "ok"
	StatusError   = "error"
)

const (
	// Retention is how long a finished request stays in the snapshot.
	Retention = 60 * time.Second
	// SubscriberBuffer is how many events one subscriber may fall behind by
	// before events for it are dropped.
	SubscriberBuffer = 256
	// maxRecent caps finished requests kept per team, whatever the rate, so a
	// burst cannot grow the snapshot without bound.
	maxRecent = 5000
	// staleRunning is when a request that never reported its end is forgotten.
	// Every request ends through the usage logger, so this is a backstop for a
	// bug, not a normal path.
	staleRunning = 2 * time.Hour
)

// Request is one request as the dashboard sees it. Times are Unix
// milliseconds.
type Request struct {
	ID               string   `json:"id"`
	StartedAt        int64    `json:"started_at"`
	FirstTokenAt     *int64   `json:"first_token_at"`
	EndedAt          *int64   `json:"ended_at"`
	Model            string   `json:"model"`
	Provider         string   `json:"provider"`
	ProviderID       int64    `json:"provider_id"`
	APIKey           string   `json:"api_key"`
	ClientProtocol   string   `json:"client_protocol"`
	UpstreamProtocol string   `json:"upstream_protocol"`
	Stream           bool     `json:"stream"`
	Status           string   `json:"status"`
	StatusCode       int      `json:"status_code"`
	ErrorType        string   `json:"error_type"`
	InputTokens      int      `json:"input_tokens"`
	OutputTokens     int      `json:"output_tokens"`
	CostUSD          *float64 `json:"cost_usd"`
}

// Event is what is sent to a subscriber. Which fields are set depends on Type;
// the rest are omitted from the JSON.
type Event struct {
	Type string `json:"type"`
	// Team says whose request this is. It routes the event and is never sent.
	Team int64 `json:"-"`

	Requests []Request `json:"requests,omitempty"` // snapshot
	Request  *Request  `json:"request,omitempty"`  // start, end

	ID           string `json:"id,omitempty"`            // first_token, progress
	At           int64  `json:"at,omitempty"`            // first_token
	OutputTokens *int   `json:"output_tokens,omitempty"` // progress
}

// MarshalJSON writes only the fields the event's type has. A snapshot always
// carries its list, even an empty one, which omitempty would drop.
func (e Event) MarshalJSON() ([]byte, error) {
	switch e.Type {
	case TypeSnapshot:
		reqs := e.Requests
		if reqs == nil {
			reqs = []Request{}
		}
		return json.Marshal(struct {
			Type     string    `json:"type"`
			Requests []Request `json:"requests"`
		}{e.Type, reqs})
	case TypeStart, TypeEnd:
		return json.Marshal(struct {
			Type    string   `json:"type"`
			Request *Request `json:"request"`
		}{e.Type, e.Request})
	case TypeFirstToken:
		return json.Marshal(struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			At   int64  `json:"at"`
		}{e.Type, e.ID, e.At})
	case TypeProgress:
		n := 0
		if e.OutputTokens != nil {
			n = *e.OutputTokens
		}
		return json.Marshal(struct {
			Type         string `json:"type"`
			ID           string `json:"id"`
			OutputTokens int    `json:"output_tokens"`
		}{e.Type, e.ID, n})
	}
	return json.Marshal(struct {
		Type string `json:"type"`
	}{e.Type})
}

// Snapshot builds the event a new subscriber starts with. A snapshot with no
// requests still carries an empty list, not null.
func snapshotEvent(team int64, reqs []Request) Event {
	if reqs == nil {
		reqs = []Request{}
	}
	return Event{Type: TypeSnapshot, Team: team, Requests: reqs}
}

// Progress is a convenience for building a progress event.
func Progress(team int64, id string, outputTokens int) Event {
	return Event{Type: TypeProgress, Team: team, ID: id, OutputTokens: &outputTokens}
}

// FirstToken is a convenience for building a first_token event.
func FirstToken(team int64, id string, at time.Time) Event {
	return Event{Type: TypeFirstToken, Team: team, ID: id, At: at.UnixMilli()}
}

// Start is a convenience for building a start event.
func Start(team int64, r Request) Event {
	return Event{Type: TypeStart, Team: team, Request: &r}
}

// End is a convenience for building an end event.
func End(team int64, r Request) Event {
	return Event{Type: TypeEnd, Team: team, Request: &r}
}

type subscriber struct {
	ch chan Event
	// lagged is set when an event was dropped for this subscriber. The next
	// event that fits is replaced by a fresh snapshot, so a dropped "end" cannot
	// leave a request running forever on someone's screen.
	lagged bool
}

type teamState struct {
	running map[string]*Request
	recent  []Request // finished, oldest first
	subs    map[*subscriber]struct{}
}

// Hub holds the live state per team and fans events out. The zero value is not
// usable; use New. A nil *Hub is valid and does nothing, so callers need no
// guards.
type Hub struct {
	mu    sync.Mutex
	teams map[int64]*teamState
	now   func() time.Time
}

// New returns an empty hub.
func New() *Hub {
	return &Hub{teams: make(map[int64]*teamState), now: time.Now}
}

func (h *Hub) team(id int64) *teamState {
	ts := h.teams[id]
	if ts == nil {
		ts = &teamState{running: make(map[string]*Request), subs: make(map[*subscriber]struct{})}
		h.teams[id] = ts
	}
	return ts
}

// Publish records the event in the hub's state and offers it to every
// subscriber of the event's team. It never blocks: a subscriber whose buffer
// is full misses this event.
//
// An end for a request the hub never saw start is preceded by a synthesised
// start, so a subscriber always sees the lifecycle in order.
func (h *Hub) Publish(ev Event) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	ts := h.team(ev.Team)
	now := h.now()
	h.prune(ts, now)

	var out []Event
	switch ev.Type {
	case TypeStart:
		if ev.Request == nil {
			return
		}
		r := *ev.Request
		r.Status = StatusRunning
		r.EndedAt = nil
		ts.running[r.ID] = &r
		cp := r
		ev.Request = &cp
		out = append(out, ev)
	case TypeFirstToken:
		r := ts.running[ev.ID]
		if r == nil || r.FirstTokenAt != nil {
			return
		}
		at := ev.At
		r.FirstTokenAt = &at
		out = append(out, ev)
	case TypeProgress:
		r := ts.running[ev.ID]
		if r == nil || ev.OutputTokens == nil {
			return
		}
		r.OutputTokens = *ev.OutputTokens
		out = append(out, ev)
	case TypeEnd:
		if ev.Request == nil {
			return
		}
		r := *ev.Request
		prev := ts.running[r.ID]
		if prev == nil {
			begin := r
			begin.Status, begin.EndedAt, begin.FirstTokenAt = StatusRunning, nil, nil
			begin.InputTokens, begin.OutputTokens, begin.CostUSD = 0, 0, nil
			begin.StatusCode, begin.ErrorType = 0, ""
			out = append(out, Event{Type: TypeStart, Team: ev.Team, Request: &begin})
		} else {
			delete(ts.running, r.ID)
			// A buffered reply has no first-token time of its own; keep the
			// one the gateway announced so end agrees with what was shown.
			if r.FirstTokenAt == nil {
				r.FirstTokenAt = prev.FirstTokenAt
			}
		}
		if r.Status == StatusRunning {
			r.Status = StatusError
		}
		if r.EndedAt == nil {
			ended := now.UnixMilli()
			r.EndedAt = &ended
		}
		ts.recent = append(ts.recent, r)
		if n := len(ts.recent); n > maxRecent {
			ts.recent = append([]Request(nil), ts.recent[n-maxRecent:]...)
		}
		cp := r
		out = append(out, Event{Type: TypeEnd, Team: ev.Team, Request: &cp})
	default:
		return
	}

	for sub := range ts.subs {
		for _, e := range out {
			h.deliver(ts, sub, e)
		}
	}
}

// deliver offers one event to one subscriber without blocking. Caller holds
// h.mu.
func (h *Hub) deliver(ts *teamState, sub *subscriber, e Event) {
	if sub.lagged {
		// The state already includes e, so a snapshot supersedes it.
		select {
		case sub.ch <- snapshotEvent(0, h.snapshot(ts, h.now())):
			sub.lagged = false
		default:
		}
		return
	}
	select {
	case sub.ch <- e:
	default:
		sub.lagged = true
	}
}

// prune forgets finished requests older than Retention and running ones that
// were never ended. Caller holds h.mu.
func (h *Hub) prune(ts *teamState, now time.Time) {
	cutoff := now.Add(-Retention).UnixMilli()
	i := 0
	for i < len(ts.recent) && ts.recent[i].EndedAt != nil && *ts.recent[i].EndedAt < cutoff {
		i++
	}
	if i > 0 {
		ts.recent = append([]Request(nil), ts.recent[i:]...)
	}
	stale := now.Add(-staleRunning).UnixMilli()
	for id, r := range ts.running {
		if r.StartedAt < stale {
			delete(ts.running, id)
		}
	}
}

// snapshot lists the running requests and the recently finished ones, oldest
// start first. Caller holds h.mu.
func (h *Hub) snapshot(ts *teamState, now time.Time) []Request {
	h.prune(ts, now)
	out := make([]Request, 0, len(ts.running)+len(ts.recent))
	for _, r := range ts.running {
		out = append(out, *r)
	}
	out = append(out, ts.recent...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt < out[j].StartedAt })
	return out
}

// Subscribe registers a listener for one team. The returned channel already
// holds a snapshot, so a page has content the moment it connects. cancel
// unsubscribes and is safe to call more than once; the channel is never closed,
// so a reader must stop on its own context.
func (h *Hub) Subscribe(teamID int64) (<-chan Event, func()) {
	if h == nil {
		return make(chan Event), func() {}
	}
	sub := &subscriber{ch: make(chan Event, SubscriberBuffer)}

	h.mu.Lock()
	ts := h.team(teamID)
	sub.ch <- snapshotEvent(teamID, h.snapshot(ts, h.now()))
	ts.subs[sub] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(ts.subs, sub)
			h.mu.Unlock()
		})
	}
	return sub.ch, cancel
}
