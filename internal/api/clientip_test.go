package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/qunqin24/polyglot/internal/config"
	"github.com/qunqin24/polyglot/internal/store"
)

// The client address is logged so an operator can notice a key being used from
// somewhere it should not be. That is only worth anything if the address
// cannot be chosen by the caller, which is what most of these tests are about.

func TestTheClientAddressIsLogged(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okChatResponse)
	}, "openai")

	readAll(t, h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, nil))

	log := h.waitForLog(t)
	if log.ClientIP == "" {
		t.Fatal("no client address was recorded")
	}
	// httptest dials over loopback, and the port must not be part of it.
	if !strings.HasPrefix(log.ClientIP, "127.0.0.1") && !strings.HasPrefix(log.ClientIP, "::1") {
		t.Errorf("client_ip = %q, want a loopback address", log.ClientIP)
	}
	if strings.Contains(log.ClientIP, ":") && !strings.Contains(log.ClientIP, "::") {
		t.Errorf("client_ip = %q still carries a port", log.ClientIP)
	}
}

// The default. Polyglot is normally behind a reverse proxy, and the address
// that proxy forwards is the only one worth logging.
func TestAForwardedHeaderIsHonouredByDefault(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okChatResponse)
	}, "openai")

	readAll(t, h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"X-Forwarded-For": "203.0.113.7"}))

	log := h.waitForLog(t)
	if log.ClientIP != "203.0.113.7" {
		t.Errorf("client_ip = %q, want the address the proxy reported", log.ClientIP)
	}
}

// TRUST_PROXY_HEADERS=false is for a deployment whose port can be reached
// without going through the proxy. There the header is a caller talking about
// itself, and the log ignores it.
func TestAForgedForwardedHeaderIsIgnoredWhenTrustIsOff(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okChatResponse)
	}, "openai", withConfig(func(c *config.Config) { c.TrustProxyHeaders = false }))

	readAll(t, h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{
			"X-Forwarded-For": "203.0.113.7",
			"X-Real-IP":       "203.0.113.8",
		}))

	log := h.waitForLog(t)
	if strings.Contains(log.ClientIP, "203.0.113") {
		t.Fatalf("a forged header set the logged address to %q "+
			"even with TRUST_PROXY_HEADERS off", log.ClientIP)
	}
}

// Grouping by address is what makes a leak visible; a flat list of requests
// does not.
func TestKeyOriginsGroupsAddresses(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okChatResponse)
	}, "openai")

	keys, err := h.store.ListAPIKeys(context.Background())
	if err != nil || len(keys) == 0 {
		t.Fatalf("list keys: %v", err)
	}
	keyID := keys[0].ID

	// Two calls from here, plus a row planted as if from somewhere else.
	for range 2 {
		readAll(t, h.post("/v1/chat/completions",
			`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, nil))
	}
	h.waitForLog(t)

	now := time.Now()
	if err := h.store.InsertRequestLogs(context.Background(), []*store.RequestLog{{
		StartedAt: now, FinishedAt: now, Status: "success", StatusCode: 200,
		ClientProtocol: "openai", APIKeyID: &keyID, ClientIP: "203.0.113.9",
	}}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	origins, err := h.store.APIKeyOrigins(context.Background(), keyID, now.Add(-time.Hour), 20)
	if err != nil {
		t.Fatalf("origins: %v", err)
	}
	if len(origins) < 2 {
		t.Fatalf("want at least two distinct addresses, got %+v", origins)
	}
	// Busiest first, so the unfamiliar single-request address stands out.
	if origins[0].Requests < origins[len(origins)-1].Requests {
		t.Errorf("origins are not ordered by request count: %+v", origins)
	}
	var found bool
	for _, o := range origins {
		if o.ClientIP == "203.0.113.9" && o.Requests == 1 {
			found = true
			if o.FirstSeen.IsZero() || o.LastSeen.IsZero() {
				t.Errorf("origin has no timestamps: %+v", o)
			}
		}
	}
	if !found {
		t.Errorf("the second address was not reported: %+v", origins)
	}
}

// Filtering to one address answers the follow-up: what did they do?
func TestLogsCanBeFilteredByAddress(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okChatResponse)
	}, "openai")

	readAll(t, h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, nil))
	h.waitForLog(t)

	admin := h.adminSession(t)
	var out struct {
		Logs []*store.RequestLog `json:"logs"`
	}
	admin.get(t, "/api/logs?client_ip=203.0.113.99", &out)
	if len(out.Logs) != 0 {
		t.Errorf("an address with no traffic returned %d rows", len(out.Logs))
	}

	admin.get(t, "/api/logs?client_ip=127.0.0.1", &out)
	if len(out.Logs) == 0 {
		t.Error("filtering by the real address returned nothing")
	}
}
