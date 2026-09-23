package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/qunqin24/polyglot/internal/capture"
	"github.com/qunqin24/polyglot/internal/store"
)

func TestFullContentLoggingAndDedicatedReadOnlyKey(t *testing.T) {
	var sent []byte
	upstream := `{"id":"chatcmpl-test","object":"chat.completion","model":"upstream-model-x","choices":[{"index":0,"message":{"role":"assistant","content":"真实回复"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "secret-cookie")
		io.WriteString(w, upstream)
	}, "openai")
	admin := h.adminSession(t)
	var cfg store.ContentLogSettings
	admin.get(t, "/api/content-logging", &cfg)
	if cfg.Enabled || cfg.RetentionDays != 7 {
		t.Fatalf("defaults=%+v", cfg)
	}
	admin.send(t, "PUT", "/api/content-logging", store.ContentLogSettings{Enabled: true, RetentionDays: 7}, &cfg)
	var created struct {
		Key    store.LogKey `json:"key"`
		Secret string       `json:"secret"`
	}
	admin.send(t, "POST", "/api/log-keys", map[string]string{"name": "agent-debug"}, &created)
	if !strings.HasPrefix(created.Secret, "plog_") {
		t.Fatal("missing dedicated secret")
	}
	prompt := `{"model":"my-model","messages":[{"role":"system","content":"真实系统提示词"},{"role":"user","content":"真实提问"}]}`
	readAll(t, h.post("/v1/chat/completions", prompt, map[string]string{"X-Custom-Secret": "client-secret"}))
	row := h.waitForLog(t)
	if row.ContentID == "" || row.ContentError != "" {
		t.Fatalf("content missing: %+v", row)
	}
	call := func(method, path, key string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, h.server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	root := "/api/logs/v1/requests"
	for _, key := range []string{"", h.clientKey} {
		resp := call("GET", root, key)
		if resp.StatusCode != 401 {
			t.Errorf("wrong key status %d", resp.StatusCode)
		}
		resp.Body.Close()
	}
	resp := call("GET", root+"?request_id="+row.RequestID, created.Secret)
	var page struct {
		Logs []*store.RequestLog `json:"logs"`
	}
	json.NewDecoder(resp.Body).Decode(&page)
	resp.Body.Close()
	if len(page.Logs) != 1 {
		t.Fatal("request-id lookup failed")
	}
	path := fmt.Sprintf("%s/%d/content", root, row.ID)
	resp = call("GET", path, created.Secret)
	var manifest capture.Manifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !manifest.Complete {
		t.Fatal("capture not complete")
	}
	payloads := map[string][]byte{}
	for _, stage := range manifest.Stages {
		if stage.Headers.Get("Authorization") != "" || stage.Headers.Get("Set-Cookie") != "" || stage.Headers.Get("X-Custom-Secret") != "" {
			t.Fatal("credentials retained")
		}
		var offset int64
		for {
			resp = call("GET", fmt.Sprintf("%s/%s?offset=%d&limit=7", path, stage.ID, offset), created.Secret)
			var chunk struct {
				Data []byte `json:"data"`
				Next int64  `json:"next_offset"`
				More bool   `json:"has_more"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&chunk); err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			payloads[stage.ID] = append(payloads[stage.ID], chunk.Data...)
			if !chunk.More {
				break
			}
			if chunk.Next <= offset {
				t.Fatal("pagination did not advance")
			}
			offset = chunk.Next
		}
	}
	if string(payloads["client.request"]) != prompt || string(payloads["upstream.1.response"]) != upstream || !bytes.Equal(payloads["upstream.1.request"], sent) || !bytes.Contains(payloads["client.response"], []byte("真实回复")) {
		t.Fatalf("payload mismatch: %q", payloads)
	}
	for _, path := range []string{"/api/providers", "/api/log-keys", "/v1/models"} {
		resp = call("GET", path, created.Secret)
		if resp.StatusCode != 401 {
			t.Errorf("log key escaped scope: %s status=%d", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
	resp = call("POST", root, created.Secret)
	if resp.StatusCode < 400 {
		t.Fatal("log API permits mutation")
	}
	resp.Body.Close()
	admin.send(t, "DELETE", fmt.Sprintf("/api/log-keys/%d", created.Key.ID), nil, &map[string]any{})
	resp = call("GET", root, created.Secret)
	if resp.StatusCode != 401 {
		t.Fatal("revoked key still accepted")
	}
	resp.Body.Close()
	admin.send(t, "PUT", "/api/content-logging", store.ContentLogSettings{Enabled: false, RetentionDays: 3}, &cfg)
	if h.store.ContentLogging(store.DefaultTeamID).Enabled {
		t.Fatal("disable did not take effect")
	}
}

func TestFullContentStreamPreservesWireBytes(t *testing.T) {
	wire := "data: {\"id\":\"c\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"你好\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"c\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, b := range []byte(wire) {
			w.Write([]byte{b})
			w.(http.Flusher).Flush()
		}
	}, "openai", withSetup(func(t *testing.T, st *store.Store, _ int64) {
		if err := st.SetContentLogging(context.Background(), store.DefaultTeamID, store.ContentLogSettings{Enabled: true, RetentionDays: 7}); err != nil {
			t.Fatal(err)
		}
	}))
	client := readAll(t, h.post("/v1/chat/completions", `{"model":"my-model","messages":[{"role":"user","content":"hi"}],"stream":true}`, nil))
	row := h.waitForLog(t)
	f, err := h.store.OpenLogContent(store.DefaultTeamID, row.ContentID)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	bodies := map[string][]byte{}
	if err := capture.Walk(f, func(e capture.Event) error { bodies[e.ID] = append(bodies[e.ID], e.Data...); return nil }); err != nil {
		t.Fatal(err)
	}
	if string(bodies["upstream.1.response"]) != wire || string(bodies["client.response"]) != client {
		t.Fatal("stream wire bytes changed")
	}
}

func TestFullContentKeepsFailedAttemptBeforeFallback(t *testing.T) {
	backup := httptestServer(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, okChatResponse) })
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, `{"error":{"message":"temporary outage"}}`)
	}, "openai", withSetup(func(t *testing.T, st *store.Store, first int64) {
		ctx := context.Background()
		tm := st.ForTeam(store.DefaultTeamID)
		if err := st.SetContentLogging(ctx, store.DefaultTeamID, store.ContentLogSettings{Enabled: true, RetentionDays: 7}); err != nil {
			t.Fatal(err)
		}
		p, err := tm.CreateProvider(ctx, &store.Provider{Name: "backup", Protocol: "openai", BaseURL: backup, Enabled: true, Priority: -10})
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []int64{first, p.ID} {
			if _, err := tm.CreateModel(ctx, &store.Model{ProviderID: id, UpstreamModelID: "shared-model", Enabled: true}); err != nil {
				t.Fatal(err)
			}
		}
	}))
	readAll(t, h.post("/v1/chat/completions", `{"model":"shared-model","messages":[{"role":"user","content":"debug fallback"}]}`, nil))
	row := h.waitForLog(t)
	f, err := h.store.OpenLogContent(store.DefaultTeamID, row.ContentID)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := capture.ReadManifest(f)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]int{}
	for _, stage := range m.Stages {
		statuses[stage.ID] = stage.Status
	}
	if statuses["upstream.1.response"] != 500 || statuses["upstream.2.response"] != 200 || row.Status != "success" {
		t.Fatalf("attempts missing: %v status=%s", statuses, row.Status)
	}
}

func TestContentRecordingOffWritesNoPayload(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, okChatResponse) }, "openai")
	readAll(t, h.post("/v1/chat/completions", chat("my-model"), nil))
	row := h.waitForLog(t)
	if row.ContentID != "" {
		t.Fatal("content recorded while disabled")
	}
	if _, err := os.Stat(h.store.ContentDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("content directory created while disabled")
	}
}

// TestALogKeyOnlyReadsItsOwnTeamsContent is the one test this phase cannot do
// without.
//
// A log key reads prompts and completions verbatim, and it is the only
// credential whose team is discovered rather than assumed: the lookup that
// resolves the secret is also what says whose logs it opens. Drop the team on
// the way from that lookup to the queries and nothing else in the suite goes
// red, because there is one team and one team's rows are every row. The day
// there are two, that omission is a key reading another team's conversations.
//
// So it is driven over real HTTP against the real router, not through the
// store: the boundary being tested is the one a holder of that key actually
// crosses.
func TestALogKeyOnlyReadsItsOwnTeamsContent(t *testing.T) {
	const theirPrompt = `{"model":"their-model","messages":[{"role":"user","content":"团队二的真实提问"}]}`
	var (
		ourKey, theirKey string
		theirLogID       int64
	)
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, okChatResponse)
	}, "openai", withSetup(func(t *testing.T, st *store.Store, _ int64) {
		ctx := context.Background()
		now := time.Now()
		// Nothing creates a second team in this phase. Raw SQL is the honest
		// admission of that; inventing an endpoint to make this line read
		// better would be inventing the endpoint.
		if _, err := st.DB().ExecContext(ctx,
			`INSERT INTO teams (id, name, created_at, updated_at) VALUES (2, 'Other', ?, ?)`,
			now.Unix(), now.Unix()); err != nil {
			t.Fatal(err)
		}
		// Different windows, so the info endpoint has a wrong answer available
		// to give.
		if err := st.SetContentLogging(ctx, store.DefaultTeamID, store.ContentLogSettings{Enabled: true, RetentionDays: 7}); err != nil {
			t.Fatal(err)
		}
		if err := st.SetContentLogging(ctx, 2, store.ContentLogSettings{Enabled: true, RetentionDays: 30}); err != nil {
			t.Fatal(err)
		}
		ourKey, theirKey = "plog_first-team-log-key-secret", "plog_second-team-log-key-secret"
		if _, err := st.ForTeam(store.DefaultTeamID).CreateLogKey(ctx, "ours", ourKey, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ForTeam(2).CreateLogKey(ctx, "theirs", theirKey, nil); err != nil {
			t.Fatal(err)
		}
		// The other team's request: a recorded body and the log row that owns
		// it, written the way the gateway writes them.
		rec, err := capture.New(st.ContentDir())
		if err != nil {
			t.Fatal(err)
		}
		rec.Body(capture.Stage{ID: "client.request", Kind: "client", Complete: true}, []byte(theirPrompt))
		if err := rec.Close(); err != nil {
			t.Fatal(err)
		}
		if err := st.InsertRequestLogs(ctx, []*store.RequestLog{{
			RequestID: "req-other-team", StartedAt: now, FinishedAt: now,
			Status: "success", StatusCode: 200,
			ClientProtocol: "openai", UpstreamProtocol: "openai",
			ModelAlias: "their-model", UpstreamModel: "their-model",
			ContentID: rec.ID, TeamID: 2, TeamName: "Other",
		}}); err != nil {
			t.Fatal(err)
		}
		rows, err := st.ForTeam(2).ListRequestLogs(ctx, store.LogFilter{Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("the other team's log row was not written: %d rows", len(rows))
		}
		theirLogID = rows[0].ID
	}))

	readAll(t, h.post("/v1/chat/completions", `{"model":"my-model","messages":[{"role":"user","content":"团队一的真实提问"}]}`, nil))
	ours := h.waitForLog(t)
	if ours.ContentID == "" || ours.ID == theirLogID {
		t.Fatalf("our own request was not recorded: %+v", ours)
	}

	call := func(path, key string) *http.Response {
		t.Helper()
		req, err := http.NewRequest("GET", h.server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	listedBy := func(key string) []*store.RequestLog {
		t.Helper()
		resp := call("/api/logs/v1/requests", key)
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("list = %d", resp.StatusCode)
		}
		var page struct {
			Logs  []*store.RequestLog `json:"logs"`
			Total int64               `json:"total"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			t.Fatal(err)
		}
		if page.Total != int64(len(page.Logs)) {
			t.Errorf("total %d counts rows this key was not given: %d", page.Total, len(page.Logs))
		}
		return page.Logs
	}

	// The listing. Their row is one id away from ours in the same table, so
	// its absence here is the predicate doing its job.
	mine := listedBy(ourKey)
	if len(mine) != 1 || mine[0].ID != ours.ID {
		t.Fatalf("a key listed %v instead of its own team's row %d", logIDs(mine), ours.ID)
	}
	theirs := listedBy(theirKey)
	if len(theirs) != 1 || theirs[0].ID != theirLogID {
		t.Fatalf("the other key listed %v instead of its own team's row %d", logIDs(theirs), theirLogID)
	}

	// And by id. Every content route reaches the body through the same scoped
	// lookup, so all four have to answer the same way — and it has to be 404
	// and not 403: a refusal would confirm the row exists, which is the one
	// thing a caller on the wrong side of a tenant boundary must not learn.
	for _, path := range []string{
		fmt.Sprintf("/api/logs/v1/requests/%d", theirLogID),
		fmt.Sprintf("/api/logs/v1/requests/%d/content", theirLogID),
		fmt.Sprintf("/api/logs/v1/requests/%d/content/client.request", theirLogID),
		fmt.Sprintf("/api/logs/v1/requests/%d/export", theirLogID),
	} {
		resp := call(path, ourKey)
		body := readAll(t, resp)
		if resp.StatusCode != 404 {
			t.Errorf("GET %s with another team's key = %d: %s", path, resp.StatusCode, body)
		}
	}
	resp := call(fmt.Sprintf("/api/logs/v1/requests/%d/content", ours.ID), theirKey)
	if body := readAll(t, resp); resp.StatusCode != 404 {
		t.Errorf("our content was readable by the other team's key = %d: %s", resp.StatusCode, body)
	}

	// The control, without which every assertion above would also pass against
	// a fixture that was simply unreadable: the owning key reads that body in
	// full.
	resp = call(fmt.Sprintf("/api/logs/v1/requests/%d/content", theirLogID), theirKey)
	var manifest capture.Manifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(manifest.Stages) != 1 || manifest.Stages[0].ID != "client.request" {
		t.Fatalf("the other team cannot read its own manifest: %+v", manifest)
	}
	resp = call(fmt.Sprintf("/api/logs/v1/requests/%d/content/client.request", theirLogID), theirKey)
	var chunk struct {
		Data []byte `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&chunk); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if string(chunk.Data) != theirPrompt {
		t.Fatalf("the other team cannot read its own body: %q", chunk.Data)
	}

	// The window a key is told about is its own team's too.
	for key, want := range map[string]float64{ourKey: 7, theirKey: 30} {
		resp := call("/api/logs/v1/", key)
		var info struct {
			RetentionDays float64 `json:"retention_days"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if info.RetentionDays != want {
			t.Errorf("retention_days = %v, want %v for this key's team", info.RetentionDays, want)
		}
	}
}

// logIDs names the rows a listing returned, because the rows themselves are
// pointers and a failure has to say which request leaked.
func logIDs(logs []*store.RequestLog) []int64 {
	out := []int64{}
	for _, l := range logs {
		out = append(out, l.ID)
	}
	return out
}
