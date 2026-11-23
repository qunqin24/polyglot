package store

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// A deployment that set POLYGLOT_SECRET_KEY has every credential encrypted
// under it. The first start after the variable stopped being a setting must
// keep that key, by writing it to the file the next start reads.
func TestTheOldEnvKeyIsAdoptedIntoTheKeyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")
	t.Setenv("POLYGLOT_SECRET_KEY", "operator passphrase")

	before, err := newCipher(deriveKey("operator passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := before.Encrypt("sk-upstream")
	if err != nil {
		t.Fatal(err)
	}

	// An unrelated file was lying there while the variable was in charge.
	if err := os.WriteFile(path, bytes.Repeat([]byte{7}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCipher(path); err != nil {
		t.Fatalf("load with env: %v", err)
	}

	t.Setenv("POLYGLOT_SECRET_KEY", "")
	after, err := LoadCipher(path)
	if err != nil {
		t.Fatalf("load without env: %v", err)
	}
	if got, err := after.Decrypt(blob); err != nil || got != "sk-upstream" {
		t.Fatalf("credential after the variable was removed = %q, %v", got, err)
	}
	backups, _ := filepath.Glob(path + ".replaced-*")
	if len(backups) != 1 {
		t.Errorf("the replaced file was not kept aside: %v", backups)
	}
}
