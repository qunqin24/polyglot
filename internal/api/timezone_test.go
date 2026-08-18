package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The instance timezone only affects how timestamps are displayed — everything
// is stored in UTC. The browser detects it during first-run setup so nobody has
// to pick one, and it stays editable afterwards.

func TestSetupStoresTheDetectedTimezone(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {}, "openai")

	resp := postSetup(t, h, testSetupToken,
		`{"username":"admin","password":"a-good-password","timezone":"Asia/Shanghai"}`)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup = %d: %s", resp.StatusCode, body)
	}

	// Setup signs the new administrator in, so the returned cookies can read it
	// straight back — the same trip the WebUI makes.
	c := &adminClient{base: h.server.URL, cookies: resp.Cookies()}
	for _, ck := range c.cookies {
		if ck.Name == "polyglot_csrf" {
			c.csrf = ck.Value
		}
	}
	if got := me(t, c)["timezone"]; got != "Asia/Shanghai" {
		t.Errorf("timezone after setup = %v, want Asia/Shanghai", got)
	}
}

func TestSetupWithoutTimezoneDefaultsToUTC(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {}, "openai")

	// A curl-driven install sends no timezone at all. That must still work.
	resp := postSetup(t, h, testSetupToken,
		`{"username":"admin","password":"a-good-password"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("setup = %d: %s", resp.StatusCode, b)
	}

	c := &adminClient{base: h.server.URL, cookies: resp.Cookies()}
	for _, ck := range c.cookies {
		if ck.Name == "polyglot_csrf" {
			c.csrf = ck.Value
		}
	}
	// UTC rather than an empty string, so no client has to special-case it.
	if got := me(t, c)["timezone"]; got != "UTC" {
		t.Errorf("default timezone = %v, want UTC", got)
	}
}

func TestSetupRejectsAnUnknownTimezone(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {}, "openai")

	resp := postSetup(t, h, testSetupToken,
		`{"username":"admin","password":"a-good-password","timezone":"Mars/Olympus_Mons"}`)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("setup = %d, want 400: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Mars/Olympus_Mons") {
		t.Errorf("the error does not name the offending value: %s", body)
	}

	// Validation happens before the account is created, so setup is still open
	// and the operator can simply try again.
	n, err := h.store.AdminCount(t.Context())
	if err != nil {
		t.Fatalf("admin count: %v", err)
	}
	if n != 0 {
		t.Errorf("a rejected setup created %d administrator(s)", n)
	}
}

func TestUpdateTimezoneFromSettings(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {}, "openai")
	c := h.adminSession(t)

	if got := me(t, c)["timezone"]; got != "UTC" {
		t.Fatalf("timezone before any change = %v, want UTC", got)
	}

	if code, body := put(t, c, "/api/settings", `{"timezone":"Europe/Berlin"}`); code != http.StatusOK {
		t.Fatalf("update = %d: %s", code, body)
	}
	if got := me(t, c)["timezone"]; got != "Europe/Berlin" {
		t.Errorf("timezone after update = %v", got)
	}

	// A rejected update must leave the working value alone.
	code, body := put(t, c, "/api/settings", `{"timezone":"Mars/Olympus_Mons"}`)
	if code != http.StatusBadRequest {
		t.Errorf("bad timezone = %d, want 400: %s", code, body)
	}
	if got := me(t, c)["timezone"]; got != "Europe/Berlin" {
		t.Errorf("a rejected update changed the stored timezone to %v", got)
	}

	// Clearing falls back to the default rather than storing an empty string.
	if code, body := put(t, c, "/api/settings", `{"timezone":""}`); code != http.StatusOK {
		t.Fatalf("clear = %d: %s", code, body)
	}
	if got := me(t, c)["timezone"]; got != "UTC" {
		t.Errorf("timezone after clearing = %v, want UTC", got)
	}
}

func TestNormaliseTimezone(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},    // unset is not an error
		{"   ", "", false}, // and neither is blank
		{"UTC", "UTC", false},
		{"Asia/Shanghai", "Asia/Shanghai", false},
		{"  Europe/Berlin  ", "Europe/Berlin", false},
		{"America/Argentina/Buenos_Aires", "America/Argentina/Buenos_Aires", false},
		{"Mars/Olympus_Mons", "", true},
		{"Asia/Shanghai'; DROP TABLE settings; --", "", true},
		{strings.Repeat("a/", 40), "", true}, // absurdly long
	}
	for _, c := range cases {
		got, err := normaliseTimezone(c.in)
		switch {
		case c.wantErr && err == nil:
			t.Errorf("normaliseTimezone(%q) = %q, want an error", c.in, got)
		case !c.wantErr && err != nil:
			t.Errorf("normaliseTimezone(%q): %v", c.in, err)
		case !c.wantErr && got != c.want:
			t.Errorf("normaliseTimezone(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestZoneDatabaseIsAvailable checks that real zones resolve. It cannot prove
// which database answered — Go prefers the host's zone files and falls back to
// the copy embedded by the time/tzdata import in admin.go — but it does fail
// loudly in an environment where neither is present, which is the situation
// that would otherwise reach an operator as "unknown timezone Asia/Shanghai".
func TestZoneDatabaseIsAvailable(t *testing.T) {
	for _, zone := range []string{
		"Asia/Shanghai", "Asia/Tokyo", "Asia/Kolkata", "Europe/London",
		"America/New_York", "America/Sao_Paulo", "Australia/Sydney", "Africa/Cairo",
	} {
		if _, err := normaliseTimezone(zone); err != nil {
			t.Errorf("%s did not resolve — is the zone database missing? %v", zone, err)
		}
	}
}

// --- helpers ---------------------------------------------------------------

func me(t *testing.T, c *adminClient) map[string]any {
	t.Helper()
	var out map[string]any
	c.get(t, "/api/auth/me", &out)
	return out
}

func put(t *testing.T, c *adminClient, path, body string) (int, string) {
	t.Helper()
	resp := c.do(t, http.MethodPut, path, strings.NewReader(body))
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
