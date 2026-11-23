package live

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestHub() (*Hub, *time.Time) {
	now := time.Unix(1_700_000_000, 0)
	h := New()
	h.now = func() time.Time { return now }
	return h, &now
}

func req(id string, started time.Time) Request {
	return Request{ID: id, StartedAt: started.UnixMilli(), Model: "m", Provider: "p", Status: StatusRunning}
}

func finished(r Request, at time.Time, cost *float64) Request {
	e := at.UnixMilli()
	r.EndedAt, r.Status, r.CostUSD = &e, StatusOK, cost
	return r
}

func next(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(time.Second):
		t.Fatal("no event")
		return Event{}
	}
}

func none(t *testing.T, ch <-chan Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event %+v", ev)
	default:
	}
}

func TestEverySubscriberGetsEveryEvent(t *testing.T) {
	h, now := newTestHub()
	a, cancelA := h.Subscribe(1)
	b, cancelB := h.Subscribe(1)
	defer cancelA()
	defer cancelB()
	if next(t, a).Type != TypeSnapshot || next(t, b).Type != TypeSnapshot {
		t.Fatal("first event must be a snapshot")
	}

	h.Publish(Start(1, req("r1", *now)))
	h.Publish(FirstToken(1, "r1", now.Add(time.Second)))
	for _, ch := range []<-chan Event{a, b} {
		if ev := next(t, ch); ev.Type != TypeStart || ev.Request.ID != "r1" {
			t.Errorf("got %+v", ev)
		}
		if ev := next(t, ch); ev.Type != TypeFirstToken || ev.ID != "r1" {
			t.Errorf("got %+v", ev)
		}
	}

	cancelA()
	cancelA() // twice is harmless
	h.Publish(Progress(1, "r1", 7))
	if ev := next(t, b); ev.Type != TypeProgress || *ev.OutputTokens != 7 {
		t.Errorf("got %+v", ev)
	}
	none(t, a)
}

func TestSlowSubscriberNeverBlocksPublishAndRecoversWithASnapshot(t *testing.T) {
	h, now := newTestHub()
	slow, cancel := h.Subscribe(1)
	defer cancel()
	fast, cancelFast := h.Subscribe(1)
	defer cancelFast()
	next(t, fast)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < SubscriberBuffer*3; i++ {
			h.Publish(Start(1, req("r"+string(rune('a'+i%26))+strings.Repeat("x", i/26), *now)))
			for len(fast) > 0 {
				<-fast
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a subscriber that does not read")
	}
	if len(slow) != SubscriberBuffer {
		t.Fatalf("buffer holds %d, want it full at %d", len(slow), SubscriberBuffer)
	}

	// Once it drains, the next event is replaced by a snapshot that includes
	// everything it missed.
	for len(slow) > 0 {
		<-slow
	}
	h.Publish(End(1, finished(req("late", *now), *now, nil)))
	ev := next(t, slow)
	if ev.Type != TypeSnapshot {
		t.Fatalf("got %s, want a catch-up snapshot", ev.Type)
	}
	found := false
	for _, r := range ev.Requests {
		found = found || r.ID == "late"
	}
	if !found || len(ev.Requests) < SubscriberBuffer*3 {
		t.Errorf("snapshot has %d requests, found late=%v", len(ev.Requests), found)
	}
	// The synthesised start was replaced by the snapshot; the end that follows
	// it is an ordinary event, and from there it is back to normal.
	if ev := next(t, slow); ev.Type != TypeEnd {
		t.Fatalf("got %s, want end", ev.Type)
	}
	h.Publish(FirstToken(1, "ra", now.Add(time.Second)))
	if ev := next(t, slow); ev.Type != TypeFirstToken {
		t.Errorf("got %s after catching up", ev.Type)
	}
}

func TestSnapshotHoldsRunningAndRecentlyEnded(t *testing.T) {
	h, now := newTestHub()
	h.Publish(Start(1, req("running", *now)))
	h.Publish(FirstToken(1, "running", now.Add(time.Second)))
	h.Publish(Progress(1, "running", 42))

	h.Publish(Start(1, req("done", now.Add(-10*time.Second))))
	cost := 0.0123
	h.Publish(End(1, finished(req("done", now.Add(-10*time.Second)), now.Add(-5*time.Second), &cost)))

	ch, cancel := h.Subscribe(1)
	defer cancel()
	snap := next(t, ch)
	if snap.Type != TypeSnapshot || len(snap.Requests) != 2 {
		t.Fatalf("snapshot = %+v", snap)
	}
	byID := map[string]Request{}
	for _, r := range snap.Requests {
		byID[r.ID] = r
	}
	run, done := byID["running"], byID["done"]
	if run.Status != StatusRunning || run.EndedAt != nil || run.OutputTokens != 42 || run.FirstTokenAt == nil {
		t.Errorf("running = %+v", run)
	}
	if done.Status != StatusOK || done.EndedAt == nil || done.CostUSD == nil || *done.CostUSD != cost {
		t.Errorf("done = %+v", done)
	}
	// Oldest start first.
	if snap.Requests[0].ID != "done" {
		t.Errorf("order = %s first", snap.Requests[0].ID)
	}
}

func TestFinishedRequestsFallOutAfterSixtySeconds(t *testing.T) {
	h, now := newTestHub()
	h.Publish(Start(1, req("old", *now)))
	h.Publish(End(1, finished(req("old", *now), *now, nil)))

	*now = now.Add(Retention - time.Second)
	ch, cancel := h.Subscribe(1)
	if n := len(next(t, ch).Requests); n != 1 {
		t.Fatalf("at 59s the snapshot has %d requests, want 1", n)
	}
	cancel()

	*now = now.Add(2 * time.Second)
	ch, cancel = h.Subscribe(1)
	defer cancel()
	if snap := next(t, ch); len(snap.Requests) != 0 || snap.Requests == nil {
		t.Fatalf("at 61s the snapshot has %v, want an empty list", snap.Requests)
	}
}

func TestRunningRequestsAreKeptHoweverOld(t *testing.T) {
	h, now := newTestHub()
	h.Publish(Start(1, req("slow", *now)))
	*now = now.Add(10 * time.Minute)
	ch, cancel := h.Subscribe(1)
	defer cancel()
	if n := len(next(t, ch).Requests); n != 1 {
		t.Fatalf("a request still running after ten minutes was dropped (%d)", n)
	}
}

func TestEndKeepsTheFirstTokenTimeAndIsPrecededByAStartIfNeeded(t *testing.T) {
	h, now := newTestHub()
	ch, cancel := h.Subscribe(1)
	defer cancel()
	next(t, ch)

	h.Publish(Start(1, req("a", *now)))
	h.Publish(FirstToken(1, "a", now.Add(300*time.Millisecond)))
	h.Publish(End(1, finished(req("a", *now), now.Add(time.Second), nil)))
	next(t, ch)
	next(t, ch)
	end := next(t, ch)
	if end.Type != TypeEnd || end.Request.FirstTokenAt == nil || *end.Request.FirstTokenAt != now.Add(300*time.Millisecond).UnixMilli() {
		t.Errorf("end = %+v", end.Request)
	}

	h.Publish(End(1, finished(req("never-started", *now), *now, nil)))
	if ev := next(t, ch); ev.Type != TypeStart || ev.Request.ID != "never-started" || ev.Request.Status != StatusRunning {
		t.Errorf("want a synthesised start, got %+v", ev)
	}
	if ev := next(t, ch); ev.Type != TypeEnd {
		t.Errorf("got %s", ev.Type)
	}
}

func TestTeamsAreIsolated(t *testing.T) {
	h, now := newTestHub()
	one, cancel1 := h.Subscribe(1)
	two, cancel2 := h.Subscribe(2)
	defer cancel1()
	defer cancel2()
	next(t, one)
	next(t, two)

	h.Publish(Start(1, req("team-one", *now)))
	if ev := next(t, one); ev.Request.ID != "team-one" {
		t.Errorf("got %+v", ev)
	}
	none(t, two)

	late, cancel3 := h.Subscribe(2)
	defer cancel3()
	if n := len(next(t, late).Requests); n != 0 {
		t.Errorf("team 2's snapshot leaked %d requests of team 1", n)
	}
}

func TestEventJSONMatchesTheContract(t *testing.T) {
	cost := 0.5
	r := Request{ID: "x", Status: StatusOK, CostUSD: &cost}
	b, _ := json.Marshal(End(9, r))
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, leaked := m["Team"]; leaked || len(m) != 2 {
		t.Errorf("end event has keys %v", keys(m))
	}
	var rm map[string]json.RawMessage
	json.Unmarshal(m["request"], &rm)
	for _, k := range []string{"id", "started_at", "first_token_at", "ended_at", "model", "provider", "provider_id",
		"api_key", "client_protocol", "upstream_protocol", "stream", "status", "status_code", "error_type",
		"input_tokens", "output_tokens", "cost_usd"} {
		if _, ok := rm[k]; !ok {
			t.Errorf("request lacks %q", k)
		}
	}
	if string(rm["first_token_at"]) != "null" {
		t.Errorf("first_token_at = %s, want null", rm["first_token_at"])
	}
	b, _ = json.Marshal(Progress(1, "x", 0))
	if !strings.Contains(string(b), `"output_tokens":0`) {
		t.Errorf("progress = %s", b)
	}
	var unpriced Request
	b, _ = json.Marshal(unpriced)
	if !strings.Contains(string(b), `"cost_usd":null`) {
		t.Errorf("unknown cost must be null, got %s", b)
	}
}

func keys(m map[string]json.RawMessage) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestNilHubIsInert(t *testing.T) {
	var h *Hub
	h.Publish(Start(1, Request{ID: "x"}))
	_, cancel := h.Subscribe(1)
	cancel()
}

func TestConcurrentUse(t *testing.T) {
	h := New()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			ch, cancel := h.Subscribe(1)
			defer cancel()
			for j := 0; j < 200; j++ {
				select {
				case <-ch:
				default:
				}
			}
		}()
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				id := string(rune('a'+i)) + strings.Repeat("-", j%5)
				h.Publish(Start(1, Request{ID: id, StartedAt: time.Now().UnixMilli()}))
				h.Publish(FirstToken(1, id, time.Now()))
				h.Publish(End(1, Request{ID: id, Status: StatusOK}))
			}
		}(i)
	}
	wg.Wait()
}
