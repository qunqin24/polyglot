package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedTokenSurvivesRestartAndIsConsumed(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreate(dir, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := filepath.Join(dir, filename)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 600", got)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	token := strings.TrimSpace(string(body))
	if !first.Valid(token) {
		t.Fatal("generated token was not accepted")
	}

	second, err := LoadOrCreate(dir, "")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !second.Valid(token) {
		t.Fatal("restart replaced the live setup token")
	}
	if err := second.Consume(); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if second.Valid(token) {
		t.Fatal("consumed token is still valid")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("token file still exists: %v", err)
	}
}

func TestConfiguredTokenNeverTouchesDisk(t *testing.T) {
	dir := t.TempDir()
	g, err := LoadOrCreate(dir, "operator-provided-token")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !g.Valid("operator-provided-token") {
		t.Fatal("configured token was not accepted")
	}
	if g.Path() != "" {
		t.Errorf("configured token acquired a file path: %q", g.Path())
	}
	if _, err := os.Stat(filepath.Join(dir, filename)); !os.IsNotExist(err) {
		t.Errorf("configured token was written to disk: %v", err)
	}
}
