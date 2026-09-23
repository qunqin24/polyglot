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
	tm := st.ForTeam(DefaultTeamID)
	cfg := ContentLogSettings{Enabled: true, RetentionDays: 3}
	if err := st.SetContentLogging(ctx, DefaultTeamID, cfg); err != nil {
		t.Fatal(err)
	}
	if err := st.SetContentLogging(ctx, DefaultTeamID, ContentLogSettings{RetentionDays: 4}); err == nil {
		t.Fatal("accepted unsupported retention")
	}
	secret := "plog_long-random-secret"
	key, err := tm.CreateLogKey(ctx, "agent", secret, nil)
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
	if _, err := st.AuthorizeLogKey(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthorizeLogKey(ctx, "invalid"); !errors.Is(err, ErrNotFound) {
		t.Fatal("bad key accepted")
	}
	yesterday := time.Now().Add(-24 * time.Hour)
	if _, err := tm.CreateLogKey(ctx, "expired", "plog_expired-secret-value", &yesterday); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthorizeLogKey(ctx, "plog_expired-secret-value"); !errors.Is(err, ErrNotFound) {
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
	if _, err := st.OpenLogContent(DefaultTeamID, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired content accessible")
	}
	if _, err := st.OpenLogContent(DefaultTeamID, "../../secret"); !errors.Is(err, ErrNotFound) {
		t.Fatal("invalid content id accepted")
	}
	if err := st.PruneLogContents(ctx); err != nil {
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
	tm = st.ForTeam(DefaultTeamID)
	if got := st.ContentLogging(DefaultTeamID); got != cfg {
		t.Fatalf("settings not persisted: %+v", got)
	}
	if err := tm.DeleteLogKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthorizeLogKey(ctx, secret); !errors.Is(err, ErrNotFound) {
		t.Fatal("revoked key accepted")
	}
}

// TestRetentionIsPerTeam pins the half of the content-log change that has no
// user-visible surface at all: how long a recorded prompt stays on disk.
//
// The window is a team's own answer now, so one sweep has to apply several of
// them at once. Two mistakes are available here and neither would be caught by
// anything else: taking one team's window for the whole deployment deletes
// another team's bodies early or keeps them long past what that team chose, and
// giving an unattributable file the *longest* window keeps content nobody can
// account for around for as long as the most generous team asked for.
func TestRetentionIsPerTeam(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// There is no API that creates a second team in this phase, so this test
	// says so in raw SQL rather than pretending otherwise.
	now := time.Now()
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO teams (id, name, created_at, updated_at) VALUES (2, 'Other', ?, ?)`,
		now.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := st.SetContentLogging(ctx, DefaultTeamID, ContentLogSettings{Enabled: true, RetentionDays: 3}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetContentLogging(ctx, 2, ContentLogSettings{Enabled: true, RetentionDays: 30}); err != nil {
		t.Fatal(err)
	}

	// record writes a body and back-dates it, because a retention window is
	// only ever read off the file's modification time.
	record := func(days int) string {
		t.Helper()
		r, err := capture.New(st.ContentDir())
		if err != nil {
			t.Fatal(err)
		}
		r.Body(capture.Stage{ID: "client.request"}, []byte("真实提问"))
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		at := now.AddDate(0, 0, -days)
		if err := os.Chtimes(filepath.Join(st.ContentDir(), r.ID+".jsonl.gz"), at, at); err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	logRow := func(teamID int64, teamName, requestID, contentID string, days int) {
		t.Helper()
		at := now.AddDate(0, 0, -days)
		if err := st.InsertRequestLogs(ctx, []*RequestLog{{
			RequestID: requestID, StartedAt: at, FinishedAt: at,
			Status: "success", StatusCode: 200,
			ClientProtocol: "openai", UpstreamProtocol: "openai",
			ContentID: contentID, TeamID: teamID, TeamName: teamName,
		}}); err != nil {
			t.Fatal(err)
		}
	}

	// Ten days sits between the two configured windows, which is what makes
	// every assertion below discriminating: the same age is expired for one
	// team and current for the other.
	const age = 10
	ours := record(age)
	theirs := record(age)
	orphan := record(age)
	// Younger than the shortest window. A body whose log row has not been
	// flushed yet looks exactly like this, and deleting it would throw away
	// content that was recorded seconds ago.
	fresh := record(1)

	logRow(DefaultTeamID, "Default", "req-ours", ours, age)
	logRow(2, "Other", "req-theirs", theirs, age)

	if err := st.PruneLogContents(ctx); err != nil {
		t.Fatal(err)
	}

	exists := func(id string) bool {
		_, err := os.Stat(filepath.Join(st.ContentDir(), id+".jsonl.gz"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		return err == nil
	}
	if exists(ours) {
		t.Error("a body outlived its own team's 3-day window")
	}
	if !exists(theirs) {
		t.Error("a body was deleted 20 days inside its own team's 30-day window")
	}
	if exists(orphan) {
		t.Error("a body with no log row survived the shortest configured window")
	}
	if !exists(fresh) {
		t.Error("a body younger than every configured window was deleted")
	}

	// Surviving on disk and being readable are two different answers, and the
	// read path takes the window from a team too.
	f, err := st.OpenLogContent(2, theirs)
	if err != nil {
		t.Fatalf("the owning team cannot read a body inside its own window: %v", err)
	}
	f.Close()
	if _, err := st.OpenLogContent(DefaultTeamID, theirs); !errors.Is(err, ErrNotFound) {
		t.Errorf("a 3-day window admitted a 10-day-old body: %v", err)
	}
}
