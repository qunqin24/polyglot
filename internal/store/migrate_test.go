package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/qunqin24/polyglot/migrations"
)

// An operator upgrades by replacing the binary; the database is whatever the
// previous version left behind. A migration that only works on a fresh file is
// not a migration, so this builds a database at the previous schema, puts real
// rows in it, and then opens it the way the new binary would.

// openAtMigration applies migrations up to and including `upTo`, leaving the
// database exactly as the version that shipped it would have.
func openAtMigration(t *testing.T, path, upTo string) *sql.DB {
	t.Helper()

	dsn := "file:" + path + "?" + url.Values{
		"_pragma": []string{"journal_mode(WAL)", "busy_timeout(5000)", "foreign_keys(ON)"},
	}.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}

	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".sql" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
			name, time.Now().Unix()); err != nil {
			t.Fatalf("record %s: %v", name, err)
		}
		if name == upTo {
			return db
		}
	}
	t.Fatalf("migration %s does not exist; this test is stale", upTo)
	return nil
}

func TestADatabaseFromThePreviousVersionUpgrades(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "polyglot.db")

	// The schema as of the release before telemetry landed.
	old := openAtMigration(t, path, "0004_drop_passthrough_alias.sql")

	// A provider, a model and a request log written by that version. The log
	// row uses the old column set, ttfb_ms included.
	if _, err := old.Exec(
		`INSERT INTO providers (name, protocol, base_url, enabled, created_at, updated_at)
		 VALUES ('legacy', 'openai', 'https://api.example.com', 1, ?, ?)`,
		time.Now().Unix(), time.Now().Unix()); err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	var providerID int64
	if err := old.QueryRow(`SELECT id FROM providers WHERE name = 'legacy'`).Scan(&providerID); err != nil {
		t.Fatalf("read provider id: %v", err)
	}
	if _, err := old.Exec(
		`INSERT INTO models (provider_id, upstream_model_id, display_name, enabled, created_at, updated_at)
		 VALUES (?, 'gpt-4o-mini', '', 1, ?, ?)`,
		providerID, time.Now().Unix(), time.Now().Unix()); err != nil {
		t.Fatalf("insert model: %v", err)
	}
	if _, err := old.Exec(
		`INSERT INTO request_logs (started_at, finished_at, latency_ms, ttfb_ms, status, status_code,
			client_protocol, upstream_protocol, provider_id, provider_name, model_alias, upstream_model,
			api_key_name, stream, input_tokens, output_tokens, reasoning_tokens,
			error_type, error_message, fidelity_notes)
		 VALUES (?, ?, 1234, 250, 'success', 200, 'openai', 'openai', ?, 'legacy',
			'gpt-4o-mini', 'gpt-4o-mini', 'old key', 1, 100, 50, 0, '', '', '')`,
		time.Now().UnixMilli(), time.Now().UnixMilli(), providerID); err != nil {
		t.Fatalf("insert legacy request log: %v", err)
	}
	if _, err := old.Exec(`INSERT INTO api_keys (name, prefix, secret_hash, enabled, created_at)
		VALUES ('legacy key', 'pg_legacy', 'legacy-hash', 1, ?)`, time.Now().Unix()); err != nil {
		t.Fatalf("insert legacy api key: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Now the new binary starts against it.
	st, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("the new version cannot open a database from the previous one: %v", err)
	}
	defer st.Close()

	// The old row is still there and still readable through the new columns.
	logs, err := st.ListRequestLogs(context.Background(), LogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list logs after upgrade: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("got %d log rows after the upgrade, want the one written before it", len(logs))
	}
	oldRow := logs[0]
	if oldRow.LatencyMS != 1234 || oldRow.InputTokens != 100 {
		t.Errorf("the pre-upgrade row lost data: %+v", oldRow)
	}
	// The fields that did not exist yet read as their empty values, not as an
	// error and not as a fabricated number.
	if oldRow.RequestID != "" {
		t.Errorf("request_id = %q on a row written before request ids existed", oldRow.RequestID)
	}
	if oldRow.RetryCount != 0 || oldRow.FallbackCount != 0 {
		t.Errorf("retry/fallback = %d/%d on a pre-upgrade row", oldRow.RetryCount, oldRow.FallbackCount)
	}
	if oldRow.TTFTMS != nil || oldRow.GenerationMS != nil || oldRow.OutputTPS != nil {
		t.Error("a pre-upgrade row reports timings that were never measured; they must be null")
	}
	keys, err := st.ListAPIKeys(context.Background())
	if err != nil || len(keys) != 1 {
		t.Fatalf("read pre-upgrade API key: %v (%d rows)", err, len(keys))
	}
	legacyKey := keys[0]
	if legacyKey.RPM != nil || legacyKey.TPD != nil || legacyKey.MaxConcurrent != nil ||
		legacyKey.MaxOutputTokens != nil || legacyKey.ExpiresAt != nil || len(legacyKey.AllowedModels) != 0 {
		t.Errorf("an old unrestricted key gained restrictions during migration: %+v", legacyKey)
	}

	// And the old configuration still routes: the model registered by the
	// previous version is still resolvable.
	models, err := st.ModelsByUpstreamID(context.Background(), "gpt-4o-mini")
	if err != nil {
		t.Fatalf("look up the pre-upgrade model: %v", err)
	}
	if len(models) != 1 || models[0].ProviderName != "legacy" {
		t.Errorf("the model registered before the upgrade no longer resolves: %+v", models)
	}

	// A new row with the new fields writes and reads back.
	ttft := int64(88)
	gen := int64(400)
	tps := 22.5
	if err := st.InsertRequestLogs(context.Background(), []*RequestLog{{
		RequestID: "after-upgrade", StartedAt: time.Now(), FinishedAt: time.Now(),
		LatencyMS: 500, TTFTMS: &ttft, GenerationMS: &gen, OutputTPS: &tps,
		Status: "success", StatusCode: 200, ClientProtocol: "anthropic",
		UpstreamProtocol: "openai", ProviderName: "legacy", Stream: true,
		RetryCount: 2, FallbackCount: 1,
	}}); err != nil {
		t.Fatalf("insert after upgrade: %v", err)
	}
	fresh, err := st.ListRequestLogs(context.Background(), LogFilter{Limit: 1})
	if err != nil || len(fresh) == 0 {
		t.Fatalf("read back the new row: %v", err)
	}
	got := fresh[0]
	if got.RequestID != "after-upgrade" || got.RetryCount != 2 || got.FallbackCount != 1 {
		t.Errorf("new fields did not round-trip: %+v", got)
	}
	if got.TTFTMS == nil || *got.TTFTMS != 88 || got.OutputTPS == nil || *got.OutputTPS != 22.5 {
		t.Errorf("timings did not round-trip: %+v", got)
	}
}

// Running the migrations twice must be a no-op, which is what happens on every
// restart.
func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyglot.db")
	for i := range 3 {
		st, err := Open(context.Background(), path)
		if err != nil {
			t.Fatalf("open %d: %v", i+1, err)
		}
		st.Close()
	}
}

func TestSingleAdminMigrationPreservesTheExistingAdministrator(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "polyglot.db")
	old := openAtMigration(t, path, "0015_api_key_budget.sql")
	if _, err := old.Exec(`INSERT INTO admins (username, password_hash, created_at, updated_at)
		VALUES ('existing', 'hash', 1, 1)`); err != nil {
		t.Fatalf("insert old administrator: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close old database: %v", err)
	}

	st, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer st.Close()
	admin, err := st.OnlyAdmin(t.Context())
	if err != nil {
		t.Fatalf("read preserved administrator: %v", err)
	}
	if admin.Username != "existing" {
		t.Errorf("administrator = %q, want existing", admin.Username)
	}
	if _, err := st.CreateAdmin(t.Context(), "second", "hash"); err == nil {
		t.Fatal("database accepted a second administrator")
	}
	if _, err := st.CreateInitialAdmin(t.Context(), "third", "hash"); !errors.Is(err, ErrAlreadySetup) {
		t.Fatalf("initial-admin error = %v, want ErrAlreadySetup", err)
	}
}

// A column dropped by a migration must be gone, not merely unused: leaving it
// behind would let old code keep writing a value nothing reads.
func TestTheSupersededTTFBColumnIsGone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyglot.db")
	st, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	rows, err := st.DB().Query(`SELECT name FROM pragma_table_info('request_logs')`)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		found[name] = true
	}
	if found["ttfb_ms"] {
		t.Error("ttfb_ms still exists after the migration that replaced it with ttft_ms")
	}
	for _, col := range []string{"request_id", "retry_count", "fallback_count",
		"generation_ms", "output_tps", "ttft_ms"} {
		if !found[col] {
			t.Errorf("column %s is missing: %v", col, fmt.Sprint(found))
		}
	}
}
