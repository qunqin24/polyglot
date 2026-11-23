package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/qunqin24/polyglot/internal/store"
)

func TestAnOperatorCanReadBackAProviderKey(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request) {}, "openai")
	c := h.adminSession(t)

	p, err := h.team().CreateProvider(context.Background(), &store.Provider{
		Name: "with-key", Protocol: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "sk-readback-1234", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	var out struct {
		Secret string `json:"secret"`
	}
	c.send(t, http.MethodPost, "/api/providers/"+itoa(p.ID)+"/secret", nil, &out)
	if out.Secret != "sk-readback-1234" {
		t.Errorf("revealed %q, want the stored key", out.Secret)
	}
}

func TestRevealingAProviderKeyFailsCleanly(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request) {}, "openai")
	c := h.adminSession(t)

	nokey, err := h.team().CreateProvider(context.Background(), &store.Provider{
		Name: "no-key", Protocol: "openai", BaseURL: "http://127.0.0.1:1", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	for name, id := range map[string]int64{"unknown id": 999999, "no key": nokey.ID} {
		resp := c.do(t, http.MethodPost, "/api/providers/"+itoa(id)+"/secret", nil)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404: %s", name, resp.StatusCode, body)
		}
	}
}

func TestRevealingAProviderKeyNeedsTheAdminSession(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request) {}, "openai")
	p, err := h.team().CreateProvider(context.Background(), &store.Provider{
		Name: "private", Protocol: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "sk-private-9999", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	resp, err := http.Post(h.server.URL+"/api/providers/"+itoa(p.ID)+"/secret", "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthenticated reveal = %d, want 401 or 403: %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "sk-private-9999") {
		t.Fatal("an unauthenticated response carried the secret")
	}
}
