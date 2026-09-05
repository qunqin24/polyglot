package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qunqin24/polyglot/internal/capture"
)

func TestContentSettingsUpgradeRetentionAndHashedKeys(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	old := openAtMigration(t, path, "0018_api_key_unique_name.sql")
	old.Close()
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := ContentLogSettings{Enabled: true, RetentionDays: 3}
	if err := st.SetContentLogging(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := st.SetContentLogging(ctx, ContentLogSettings{RetentionDays: 4}); err == nil {
		t.Fatal("accepted unsupported retention")
	}
	secret := "plog_long-random-secret"
	key, err := st.CreateLogKey(ctx, "agent", secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := st.db.QueryRow("SELECT token_hash FROM log_keys WHERE id=?", key.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == secret || stored != HashToken(secret) {
		t.Fatal("log secret not hashed")
	}
	if err := st.AuthorizeLogKey(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if err := st.AuthorizeLogKey(ctx, "invalid"); !errors.Is(err, ErrNotFound) {
		t.Fatal("bad key accepted")
	}
	yesterday := time.Now().Add(-24 * time.Hour)
	if _, err := st.CreateLogKey(ctx, "expired", "plog_expired-secret-value", &yesterday); err != nil {
		t.Fatal(err)
	}
	if err := st.AuthorizeLogKey(ctx, "plog_expired-secret-value"); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired key accepted")
	}
	r, err := capture.New(st.ContentDir())
	if err != nil {
		t.Fatal(err)
	}
	r.Body(capture.Stage{ID: "client.request"}, []byte("prompt"))
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(st.ContentDir(), r.ID+".jsonl.gz")
	at := time.Now().AddDate(0, 0, -4)
	if err := os.Chtimes(filename, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := st.OpenLogContent(r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired content accessible")
	}
	if _, err := st.OpenLogContent("../../secret"); !errors.Is(err, ErrNotFound) {
		t.Fatal("invalid content id accepted")
	}
	if err := st.PruneLogContents(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filename); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired content not removed")
	}
	st.Close()
	st, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if got := st.ContentLogging(); got != cfg {
		t.Fatalf("settings not persisted: %+v", got)
	}
	if err := st.DeleteLogKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.AuthorizeLogKey(ctx, secret); !errors.Is(err, ErrNotFound) {
		t.Fatal("revoked key accepted")
	}
}
