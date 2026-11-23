package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/qunqin24/polyglot/internal/live"
	"github.com/qunqin24/polyglot/internal/store"
)

// liveStream is an open /api/live connection, read on its own goroutine so a
// test can wait for frames with a deadline.
type liveStream struct {
	resp   *http.Response
	lines  chan string
	cancel context.CancelFunc
}

func (c *adminClient) openLive(t *testing.T) *liveStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/live", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("open /api/live: %v", err)
	}
	s := &liveStream{resp: resp, lines: make(chan string, 256), cancel: cancel}
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			s.lines <- sc.Text()
		}
		close(s.lines)
	}()
	t.Cleanup(func() { cancel(); resp.Body.Close() })
	return s
}

// nextEvent returns the next data frame, skipping comments.
func (s *liveStream) nextEvent(t *testing.T) live.Event {
	t.Helper()
	for {
		select {
		case line, ok := <-s.lines:
			if !ok {
				t.Fatal("stream closed")
			}
			payload, isData := strings.CutPrefix(line, "data: ")
			if !isData {
				continue
			}
			var ev live.Event
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				t.Fatalf("frame is not JSON: %v: %s", err, payload)
			}
			return ev
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for a live event")
		}
	}
}

func (s *liveStream) expect(t *testing.T, typ string) live.Event {
	t.Helper()
	ev := s.nextEvent(t)
	if ev.Type != typ {
		t.Fatalf("got %q event, want %q: %+v", ev.Type, typ, ev)
	}
	return ev
}

func TestLiveRequiresAnAdminSession(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {}, "openai")
	resp, err := http.Get(h.server.URL + "/api/live")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	// An API key is not an admin session.
	req, _ := http.NewRequest(http.MethodGet, h.server.URL+"/api/live", nil)
	req.Header.Set("Authorization", "Bearer "+h.clientKey)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status with an API key = %d, want 401", resp2.StatusCode)
	}
}

// priceTheModel gives upstream-model-x a price through the admin API, the way
// an operator would, so the cost under test is a real one.
func priceTheModel(t *testing.T, admin *adminClient) {
	t.Helper()
	var listed struct {
		Models []struct {
			ID int64 `json:"id"`
		} `json:"models"`
	}
	admin.get(t, "/api/pricing", &listed)
	if len(listed.Models) != 1 {
		t.Fatalf("pricing lists %d models", len(listed.Models))
	}
	resp := admin.do(t, http.MethodPut, "/api/models/"+itoa(listed.Models[0].ID)+"/pricing",
		strings.NewReader(`{"input":3,"output":15,"cache_read":null,"cache_write":null}`))
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set price: %d", resp.StatusCode)
	}
}

func checkEndMatchesLog(t *testing.T, end live.Event, log *store.RequestLog, wantCost bool) {
	t.Helper()
	r := end.Request
	if r == nil {
		t.Fatal("end carries no request")
	}
	if r.ID != log.RequestID || r.Status != live.StatusOK || r.StatusCode != 200 {
		t.Errorf("end = %+v, log = %s %s", r, log.RequestID, log.Status)
	}
	if r.InputTokens != log.InputTokens || r.OutputTokens != log.OutputTokens || r.InputTokens == 0 {
		t.Errorf("end tokens %d/%d, log %d/%d", r.InputTokens, r.OutputTokens, log.InputTokens, log.OutputTokens)
	}
	switch {
	case log.CostUSD == nil && r.CostUSD != nil:
		t.Errorf("log has no cost but end says %v", *r.CostUSD)
	case log.CostUSD != nil && (r.CostUSD == nil || *r.CostUSD != *log.CostUSD):
		t.Errorf("end cost %v, log cost %v", r.CostUSD, *log.CostUSD)
	}
	if wantCost != (r.CostUSD != nil) {
		t.Errorf("cost present = %v, want %v", r.CostUSD != nil, wantCost)
	}
	if r.Model != "upstream-model-x" || r.Provider != "fake" || r.APIKey != "test" ||
		r.ClientProtocol != "openai" || r.UpstreamProtocol != "openai" || r.ProviderID == 0 {
		t.Errorf("end = %+v", r)
	}
	if r.EndedAt == nil || *r.EndedAt < r.StartedAt || r.FirstTokenAt == nil {
		t.Errorf("times: %+v", r)
	}
}

func TestLiveSeesANonStreamingRequest(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okChatResponse)
	}, "openai", withSetup(func(t *testing.T, st *store.Store, providerID int64) {
		registerPricedModel(t, st, providerID)
	}))
	admin := h.adminSession(t)
	priceTheModel(t, admin)

	stream := admin.openLive(t)
	if ct := stream.resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type = %q", ct)
	}
	if snap := stream.expect(t, live.TypeSnapshot); snap.Requests == nil || len(snap.Requests) != 0 {
		t.Errorf("snapshot = %+v", snap)
	}

	readAll(t, h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, nil))

	start := stream.expect(t, live.TypeStart)
	if start.Request.Status != live.StatusRunning || start.Request.Stream || start.Request.EndedAt != nil ||
		start.Request.Model != "upstream-model-x" || start.Request.Provider != "fake" {
		t.Errorf("start = %+v", start.Request)
	}
	ft := stream.expect(t, live.TypeFirstToken)
	if ft.ID != start.Request.ID || ft.At < start.Request.StartedAt {
		t.Errorf("first_token = %+v", ft)
	}
	end := stream.expect(t, live.TypeEnd)
	checkEndMatchesLog(t, end, h.waitForLog(t), true)
	if end.Request.Stream {
		t.Error("a non-streaming request is reported as streaming")
	}
}

func TestLiveSeesAStreamingRequest(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for _, chunk := range []string{
			`{"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{"content":"lo"}}]}`,
			`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`{"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		} {
			io.WriteString(w, "data: "+chunk+"\n\n")
			f.Flush()
		}
		io.WriteString(w, "data: [DONE]\n\n")
		f.Flush()
	}, "openai", withSetup(func(t *testing.T, st *store.Store, providerID int64) {
		registerPricedModel(t, st, providerID)
	}))
	admin := h.adminSession(t)
	priceTheModel(t, admin)

	stream := admin.openLive(t)
	stream.expect(t, live.TypeSnapshot)

	readAll(t, h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true}}`, nil))

	start := stream.expect(t, live.TypeStart)
	if !start.Request.Stream {
		t.Error("start does not say the request streams")
	}
	stream.expect(t, live.TypeFirstToken)
	// Progress, if the upstream reported any mid-stream, is allowed here; this
	// upstream reports usage once, at the end.
	ev := stream.nextEvent(t)
	for ev.Type == live.TypeProgress {
		ev = stream.nextEvent(t)
	}
	if ev.Type != live.TypeEnd {
		t.Fatalf("got %q, want end", ev.Type)
	}
	log := h.waitForLog(t)
	checkEndMatchesLog(t, ev, log, true)
	if ev.Request.InputTokens != 3 || ev.Request.OutputTokens != 2 || !ev.Request.Stream {
		t.Errorf("end = %+v", ev.Request)
	}
	if want := log.StartedAt.UnixMilli() + *log.TTFTMS; *ev.Request.FirstTokenAt != want {
		t.Errorf("first_token_at = %d, want %d", *ev.Request.FirstTokenAt, want)
	}
}

func TestLiveCostIsNullNotZeroWhenThereIsNoPrice(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okChatResponse)
	}, "openai")
	admin := h.adminSession(t)
	stream := admin.openLive(t)
	stream.expect(t, live.TypeSnapshot)

	readAll(t, h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, nil))
	stream.expect(t, live.TypeStart)
	stream.expect(t, live.TypeFirstToken)
	end := stream.expect(t, live.TypeEnd)
	if end.Request.CostUSD != nil {
		t.Errorf("an unpriced request reports cost %v", *end.Request.CostUSD)
	}
	checkEndMatchesLog(t, end, h.waitForLog(t), false)
}

func TestLiveReportsAFailedRequestAndGivesNewcomersASnapshot(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"nope"}}`)
	}, "openai")
	admin := h.adminSession(t)

	readAll(t, h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, nil))
	h.waitForLog(t)

	// Connected after the fact: the finished request is in the snapshot.
	stream := admin.openLive(t)
	snap := stream.expect(t, live.TypeSnapshot)
	if len(snap.Requests) != 1 {
		t.Fatalf("snapshot has %d requests, want the one that just failed", len(snap.Requests))
	}
	r := snap.Requests[0]
	if r.Status != live.StatusError || r.StatusCode != http.StatusBadRequest || r.ErrorType == "" ||
		r.EndedAt == nil || r.FirstTokenAt != nil {
		t.Errorf("request = %+v", r)
	}
}

func TestLivePingsAnIdleConnection(t *testing.T) {
	old := livePing
	livePing = 50 * time.Millisecond
	t.Cleanup(func() { livePing = old })

	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {}, "openai")
	stream := h.adminSession(t).openLive(t)
	stream.expect(t, live.TypeSnapshot)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case line := <-stream.lines:
			if line == ": ping" {
				return
			}
		case <-deadline:
			t.Fatal("no ': ping' comment arrived on an idle stream")
		}
	}
}
