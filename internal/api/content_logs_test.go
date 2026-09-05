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
	if h.store.ContentLogging().Enabled {
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
		if err := st.SetContentLogging(context.Background(), store.ContentLogSettings{Enabled: true, RetentionDays: 7}); err != nil {
			t.Fatal(err)
		}
	}))
	client := readAll(t, h.post("/v1/chat/completions", `{"model":"my-model","messages":[{"role":"user","content":"hi"}],"stream":true}`, nil))
	row := h.waitForLog(t)
	f, err := h.store.OpenLogContent(row.ContentID)
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
		if err := st.SetContentLogging(ctx, store.ContentLogSettings{Enabled: true, RetentionDays: 7}); err != nil {
			t.Fatal(err)
		}
		p, err := st.CreateProvider(ctx, &store.Provider{Name: "backup", Protocol: "openai", BaseURL: backup, Enabled: true, Priority: -10})
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []int64{first, p.ID} {
			if _, err := st.CreateModel(ctx, &store.Model{ProviderID: id, UpstreamModelID: "shared-model", Enabled: true}); err != nil {
				t.Fatal(err)
			}
		}
	}))
	readAll(t, h.post("/v1/chat/completions", `{"model":"shared-model","messages":[{"role":"user","content":"debug fallback"}]}`, nil))
	row := h.waitForLog(t)
	f, err := h.store.OpenLogContent(row.ContentID)
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
