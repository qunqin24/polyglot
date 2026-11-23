package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func clearListen(t *testing.T) {
	for _, k := range []string{"PORT", "POLYGLOT_PORT", "LISTEN", "POLYGLOT_LISTEN", "DB_PATH", "POLYGLOT_DB_PATH"} {
		t.Setenv(k, "")
	}
}

func TestDefaultListenIsLoopback(t *testing.T) {
	clearListen(t)
	t.Setenv("DATA_DIR", t.TempDir())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Listen != "127.0.0.1:3000" {
		t.Errorf("listen = %q, want loopback default", cfg.Listen)
	}
}

func TestListenIsTheOnlyAddressSetting(t *testing.T) {
	clearListen(t)
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("LISTEN", "192.0.2.10:9876")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Listen != "192.0.2.10:9876" {
		t.Errorf("listen = %q", cfg.Listen)
	}
}

// Quietly ignoring PORT would move the server; the start has to stop and say
// what to set instead.
func TestARemovedPortStopsTheStart(t *testing.T) {
	clearListen(t)
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PORT", "4321")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "LISTEN=:4321") {
		t.Fatalf("err = %v, want a pointer to LISTEN=:4321", err)
	}
}

// Ignoring DB_PATH would open an empty database next to the real one.
func TestARemovedDBPathStopsTheStartUnlessItIsTheDefault(t *testing.T) {
	clearListen(t)
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)

	t.Setenv("DB_PATH", filepath.Join(dir, "polyglot.db"))
	if _, err := Load(); err != nil {
		t.Fatalf("DB_PATH equal to the default should load: %v", err)
	}

	t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "elsewhere.db"))
	if _, err := Load(); err == nil {
		t.Fatal("DB_PATH pointing elsewhere was ignored")
	}
}

func TestTheDatabaseLivesInTheDataDir(t *testing.T) {
	clearListen(t)
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DBPath != filepath.Join(dir, "polyglot.db") {
		t.Errorf("db path = %q", cfg.DBPath)
	}
}
