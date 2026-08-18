package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/qunqin24/polyglot/internal/store"
)

// A nil Go slice marshals to JSON null. Every list-shaped field in an API
// response must be an array instead, because clients type them as arrays and
// crash on null. These tests pin that contract.

func TestStatsArraysAreNeverNull(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {}, "openai")
	admin := h.adminSession(t)

	// The interesting case is a fresh install with no traffic at all.
	for _, tc := range []struct {
		path   string
		fields []string
	}{
		{"/api/stats?hours=24", []string{"by_provider", "series"}},
		{"/api/stats/conversions?hours=24", []string{"pairs", "flows", "fields"}},
		{"/api/stats/latency?hours=24", []string{"series", "histogram", "errors"}},
		{"/api/stats/cost?hours=24", []string{"starts", "stacks", "models"}},
	} {
		var raw map[string]json.RawMessage
		admin.get(t, tc.path, &raw)
		for _, field := range tc.fields {
			v, ok := raw[field]
			if !ok {
				t.Errorf("%s response has no %q field", tc.path, field)
				continue
			}
			if string(v) == "null" {
				t.Errorf("%s .%s is null; it must be [] so the UI can iterate it", tc.path, field)
			}
		}
	}
}

// A gateway with no models yet is the case that would serialise as null.
func TestPricingModelsAreNeverNull(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {}, "openai")
	admin := h.adminSession(t)

	var raw map[string]json.RawMessage
	admin.get(t, "/api/pricing", &raw)
	if string(raw["models"]) == "null" {
		t.Error("pricing.models is null; it must be [] so the UI can iterate it")
	}
}

func TestLogFidelityIsNeverNull(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}, "openai")

	// A clean OpenAI -> OpenAI request produces no conversion notes, which is
	// exactly the case that used to serialise as null.
	readAll(t, h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"x"}]}`, nil))

	var logs []*store.RequestLog
	for range 40 {
		time.Sleep(100 * time.Millisecond)
		var err error
		logs, err = h.store.ListRequestLogs(t.Context(), store.LogFilter{Limit: 1})
		if err != nil {
			t.Fatalf("list logs: %v", err)
		}
		if len(logs) > 0 {
			break
		}
	}
	if len(logs) == 0 {
		t.Fatal("no request log was written")
	}

	admin := h.adminSession(t)
	var raw map[string]json.RawMessage
	admin.get(t, "/api/logs/"+itoa(logs[0].ID), &raw)

	v, ok := raw["fidelity"]
	if !ok {
		t.Fatal("log detail has no fidelity field")
	}
	if string(v) == "null" {
		t.Error("log.fidelity is null; it must be [] so the UI can read .length")
	}
}

// TestListEndpointsReturnArrays covers the collection endpoints on a fresh
// install, where every table is empty.
func TestListEndpointsReturnArrays(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {}, "openai")
	admin := h.adminSession(t)

	for _, path := range []string{"/api/providers", "/api/aliases", "/api/keys", "/api/protocols"} {
		resp := admin.do(t, http.MethodGet, path, nil)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.TrimSpace(string(body)) == "null" {
			t.Errorf("GET %s returned null, want []", path)
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(body, &arr); err != nil {
			t.Errorf("GET %s is not a JSON array: %v (%s)", path, err, body)
		}
	}

	// These two nest their array under a key.
	for _, tc := range []struct{ path, field string }{
		{"/api/logs", "logs"},
		{"/api/models", "models"},
	} {
		var resp map[string]json.RawMessage
		admin.get(t, tc.path, &resp)
		v, ok := resp[tc.field]
		if !ok {
			t.Errorf("GET %s has no %q field", tc.path, tc.field)
			continue
		}
		if string(v) == "null" {
			t.Errorf("%s.%s is null, want []", tc.path, tc.field)
		}
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
