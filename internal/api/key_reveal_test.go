package api

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// Losing a key used to mean deleting it and rebuilding its limits and budget
// from scratch. The operator can now read back a key they already own — over
// the admin session, and only there.
func TestAnOperatorCanReadBackTheirOwnKey(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request) {}, "openai")
	c := h.adminSession(t)

	var created struct {
		Key struct {
			ID         int64  `json:"id"`
			Prefix     string `json:"prefix"`
			Revealable bool   `json:"revealable"`
		} `json:"key"`
		Secret string `json:"secret"`
	}
	c.send(t, http.MethodPost, "/api/keys", map[string]any{"name": "reusable"}, &created)
	if created.Secret == "" {
		t.Fatal("creating a key returned no secret")
	}
	if !created.Key.Revealable {
		t.Error("a key created now says it cannot be shown again")
	}

	var revealed struct {
		Secret string `json:"secret"`
	}
	c.send(t, http.MethodPost, "/api/keys/"+itoa(created.Key.ID)+"/secret", nil, &revealed)
	if revealed.Secret != created.Secret {
		t.Errorf("revealed %q, want the key that was issued", revealed.Secret)
	}

	// And the listing still says so, so the button has something to key off.
	var keys []struct {
		ID         int64 `json:"id"`
		Revealable bool  `json:"revealable"`
	}
	c.get(t, "/api/keys", &keys)
	found := false
	for _, k := range keys {
		if k.ID == created.Key.ID {
			found = true
			if !k.Revealable {
				t.Error("the listing says a key just created cannot be shown")
			}
		}
	}
	if !found {
		t.Errorf("the new key is missing from the listing: %+v", keys)
	}
}

// The secret is behind the admin session like every other admin route. A
// caller with no session gets nothing, whatever the id.
func TestRevealingAKeyNeedsTheAdminSession(t *testing.T) {
	h := newHarness(t, func(http.ResponseWriter, *http.Request) {}, "openai")
	c := h.adminSession(t)

	var created struct {
		Key struct {
			ID int64 `json:"id"`
		} `json:"key"`
		Secret string `json:"secret"`
	}
	c.send(t, http.MethodPost, "/api/keys", map[string]any{"name": "private"}, &created)

	resp, err := http.Post(h.server.URL+"/api/keys/"+itoa(created.Key.ID)+"/secret",
		"application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unauthenticated reveal = %d, want 401 or 403: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) > 0 && json.Valid(body) {
		var out map[string]any
		_ = json.Unmarshal(body, &out)
		if _, ok := out["secret"]; ok {
			t.Fatal("an unauthenticated response carried the secret")
		}
	}
}
