package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/qunqin24/polyglot/internal/auth"
	"github.com/qunqin24/polyglot/internal/config"
	"github.com/qunqin24/polyglot/internal/store"
)

// A forgotten administrator password is unrecoverable from inside the product:
// there is no email, no second account and no security question. The way back
// in is filesystem access to DATA_DIR, which is the same thing that would let
// someone read the database anyway.

func newAdminDir(t *testing.T, password string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)

	st, err := store.Open(t.Context(), filepath.Join(dir, "polyglot.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := st.CreateAdmin(t.Context(), "qunqin", hash); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	return dir
}

func TestResetPasswordLetsALockedOutAdminBackIn(t *testing.T) {
	dir := newAdminDir(t, "the-one-i-forgot")

	username, password, err := resetAdminPassword()
	if err != nil {
		t.Fatalf("reset: %v", err)
	}

	// The username is reported, because someone who forgot the password has
	// often forgotten that too.
	if username != "qunqin" {
		t.Errorf("username = %q", username)
	}
	if len(password) < 16 {
		t.Errorf("generated password is only %d characters", len(password))
	}

	st, err := store.Open(t.Context(), filepath.Join(dir, "polyglot.db"))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st.Close()

	admin, err := st.AdminByUsername(t.Context(), "qunqin")
	if err != nil {
		t.Fatalf("get admin: %v", err)
	}
	if !auth.CheckPassword(admin.PasswordHash, password) {
		t.Error("the new password does not sign in")
	}
	if auth.CheckPassword(admin.PasswordHash, "the-one-i-forgot") {
		t.Error("the old password still works after a reset")
	}
}

// TestResetPasswordSignsOutExistingSessions matters because a lockout is
// sometimes not forgetfulness. A reset that left live sessions alone would
// change the password without changing who is inside.
func TestResetPasswordSignsOutExistingSessions(t *testing.T) {
	dir := newAdminDir(t, "old-password")
	dbPath := filepath.Join(dir, "polyglot.db")

	st, err := store.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	admin, err := st.OnlyAdmin(t.Context())
	if err != nil {
		t.Fatalf("only admin: %v", err)
	}
	const token = "a-live-session-token"
	if err := st.CreateSession(t.Context(), store.HashToken(token), admin.ID, time.Hour, "test"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := st.SessionAdmin(t.Context(), store.HashToken(token)); err != nil {
		t.Fatalf("the session should be valid before the reset: %v", err)
	}
	st.Close()

	if _, _, err := resetAdminPassword(); err != nil {
		t.Fatalf("reset: %v", err)
	}

	st2, err := store.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	if _, err := st2.SessionAdmin(t.Context(), store.HashToken(token)); err == nil {
		t.Error("a session from before the reset is still valid")
	}
}

func TestResetPasswordOnAFreshInstall(t *testing.T) {
	// No administrator yet: say so plainly instead of failing on a nil row.
	t.Setenv("DATA_DIR", t.TempDir())

	_, _, err := resetAdminPassword()
	if err == nil {
		t.Fatal("expected an error when there is no administrator")
	}
	if !strings.Contains(err.Error(), "no administrator") {
		t.Errorf("unhelpful error for a fresh install: %v", err)
	}
}

func TestResetPasswordIsNeverTheSameTwice(t *testing.T) {
	newAdminDir(t, "start")

	_, first, err := resetAdminPassword()
	if err != nil {
		t.Fatalf("first reset: %v", err)
	}
	_, second, err := resetAdminPassword()
	if err != nil {
		t.Fatalf("second reset: %v", err)
	}
	if first == second {
		t.Error("two resets produced the same password; it is not random")
	}
}

func TestCLIRejectsAnUnknownCommand(t *testing.T) {
	// A typo must not fall through and quietly start the gateway instead.
	handled, err := runCLI([]string{"rest-password"})
	if !handled {
		t.Fatal("an unknown command was passed through to the server")
	}
	if err == nil {
		t.Fatal("an unknown command exited successfully")
	}
	if !strings.Contains(err.Error(), "rest-password") {
		t.Errorf("the error does not name the typo: %v", err)
	}
}

func TestCLIWithNoArgumentsStartsTheServer(t *testing.T) {
	handled, err := runCLI(nil)
	if handled || err != nil {
		t.Fatalf("bare `polyglot` should start the gateway, got handled=%v err=%v", handled, err)
	}
}

func TestConfigCommandNeverPrintsTheSecretKey(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("POLYGLOT_SECRET_KEY", "a-real-encryption-key-value")

	if got := secretState(); strings.Contains(got, "a-real-encryption-key-value") {
		t.Errorf("`polyglot config` would print the encryption key: %q", got)
	}
}

func TestConfigCommandNeverPrintsTheSetupToken(t *testing.T) {
	const token = "operator-setup-token-value"
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("POLYGLOT_SETUP_TOKEN", token)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	for _, s := range settings {
		if s.key != "POLYGLOT_SETUP_TOKEN" {
			continue
		}
		if got := s.effective(cfg); strings.Contains(got, token) {
			t.Fatalf("setup token would be printed: %q", got)
		}
		return
	}
	t.Fatal("POLYGLOT_SETUP_TOKEN is missing from the settings table")
}

// TestEverySettingIsDocumented scans the source for environment variables the
// code actually reads and fails if one is missing from the settings table.
// LOG_FORMAT shipped undocumented for exactly this reason: nothing connected
// "the code reads it" to "an operator can find out about it".
func TestEverySettingIsDocumented(t *testing.T) {
	// Where configuration is read from the environment.
	sources := []string{
		"../../internal/config/config.go",
		"../../internal/provider/client.go",
		"../../internal/store/cipher.go",
		"main.go",
	}
	// Development-only knobs, set by `make dev`, not aimed at operators.
	ignored := map[string]bool{
		"POLYGLOT_DEV":                    true,
		"POLYGLOT_DEV_PROXY":              true,
		"POLYGLOT_BLOCK_PRIVATE_UPSTREAM": true, // prefixed alias of a listed key
	}

	documented := map[string]bool{}
	for _, s := range settings {
		documented[s.key] = true
	}

	// envStr("X", ...), envInt("X", ...), envBool("X", ...), os.Getenv("X")
	pattern := regexp.MustCompile(`(?:envStr|envInt|envBool|Getenv)\("([A-Z][A-Z0-9_]{2,})"`)
	for _, src := range sources {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s: %v", src, err)
		}
		for _, m := range pattern.FindAllStringSubmatch(string(b), -1) {
			key := m[1]
			if ignored[key] || documented[key] {
				continue
			}
			t.Errorf("%s reads %s, but it is missing from the settings table in settings.go, "+
				"so `polyglot help` will not mention it", src, key)
		}
	}
}

// TestSettingsTableHasNoInvention is the other direction: a variable listed in
// help that nothing reads would send an operator chasing a knob that does not
// exist.
func TestSettingsTableHasNoInvention(t *testing.T) {
	var all strings.Builder
	for _, src := range []string{
		"../../internal/config/config.go",
		"../../internal/provider/client.go",
		"../../internal/store/cipher.go",
		"main.go",
	} {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s: %v", src, err)
		}
		all.Write(b)
	}
	for _, s := range settings {
		if !strings.Contains(all.String(), `"`+s.key+`"`) {
			t.Errorf("`polyglot help` advertises %s, but no code reads it", s.key)
		}
	}
}

// TestConfigNeverReportsDefaultForANonDefaultValue is the one mistake
// `polyglot config` must not make. SECURE_COOKIES is false by default but an
// https PUBLIC_URL switches it on, and reporting "default" beside "true" would
// send an operator looking in the wrong place.
func TestConfigNeverReportsDefaultForANonDefaultValue(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PUBLIC_URL", "https://gw.example.com")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.SecureCookies {
		t.Fatal("an https PUBLIC_URL no longer turns on Secure cookies; this test is stale")
	}

	for _, s := range settings {
		if s.key != "SECURE_COOKIES" {
			continue
		}
		if s.source == nil {
			t.Fatal("SECURE_COOKIES can be switched on by PUBLIC_URL but declares no source")
		}
		if why := s.source(cfg); why == "" {
			t.Error("SECURE_COOKIES is true but reports the plain default source")
		} else if !strings.Contains(why, "PUBLIC_URL") {
			t.Errorf("source = %q, want it to name PUBLIC_URL", why)
		}
		return
	}
	t.Fatal("SECURE_COOKIES is missing from the settings table")
}
