package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// An operator who loses a key used to have to delete it and rebuild its
// limits, budget and model list. The secret is now kept encrypted so it can be
// read back — but only through the cipher: the plaintext must never be sitting
// in the database for anyone who copies the .db file.
func TestAnAPIKeyReadsBackButIsNotStoredInTheClear(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "polyglot.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	const secret = "pg-not-a-real-key-0123456789"
	key, err := st.CreateAPIKey(ctx, "reusable", secret[:11], secret)
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if !key.Revealable {
		t.Error("a freshly created key reports that it cannot be shown")
	}

	got, err := st.APIKeySecret(ctx, key.ID)
	if err != nil {
		t.Fatalf("read the secret back: %v", err)
	}
	if got != secret {
		t.Errorf("read back %q, want the key that was created", got)
	}

	// Authentication still goes through the hash, untouched by any of this.
	byHash, err := st.APIKeyByHash(ctx, HashToken(secret))
	if err != nil || byHash.ID != key.ID {
		t.Fatalf("the key no longer authenticates by hash: %v", err)
	}
	if !byHash.Revealable {
		t.Error("Revealable did not survive a read through the hash lookup")
	}

	// The stored blob is ciphertext. A grep for the key across the row must
	// find nothing, or the encryption is decorative.
	var enc []byte
	var hash string
	if err := st.db.QueryRowContext(ctx,
		`SELECT secret_enc, secret_hash FROM api_keys WHERE id = ?`, key.ID).Scan(&enc, &hash); err != nil {
		t.Fatalf("read the raw row: %v", err)
	}
	if len(enc) == 0 {
		t.Fatal("no ciphertext was written")
	}
	if strings.Contains(string(enc), secret) || strings.Contains(hash, secret) {
		t.Error("the plaintext key is sitting in the database")
	}
}

// Asking for a key that is not there is a not-found, not a decrypt failure.
func TestSecretOfAMissingKeyIsNotFound(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "polyglot.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	if _, err := st.APIKeySecret(ctx, 404); err != ErrNotFound {
		t.Errorf("secret of a missing key = %v, want ErrNotFound", err)
	}
}
