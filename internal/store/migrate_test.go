package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"reflect"
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

	tm := st.ForTeam(DefaultTeamID)

	// The old row is still there and still readable through the new columns.
	logs, err := tm.ListRequestLogs(context.Background(), LogFilter{Limit: 10})
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
	keys, err := tm.ListAPIKeys(context.Background())
	if err != nil || len(keys) != 1 {
		t.Fatalf("read pre-upgrade API key: %v (%d rows)", err, len(keys))
	}
	legacyKey := keys[0]
	if legacyKey.RPM != nil || legacyKey.TPD != nil || legacyKey.MaxConcurrent != nil ||
		legacyKey.MaxOutputTokens != nil || legacyKey.ExpiresAt != nil || len(legacyKey.AllowedModels) != 0 {
		t.Errorf("an old unrestricted key gained restrictions during migration: %+v", legacyKey)
	}
	// The secret of a key written before there was a ciphertext column is gone,
	// and the row must say so rather than fail obscurely when someone asks.
	if legacyKey.Revealable {
		t.Error("a key that predates the ciphertext claims it can still be shown")
	}
	if _, err := tm.APIKeySecret(context.Background(), legacyKey.ID); !errors.Is(err, ErrSecretUnavailable) {
		t.Errorf("secret of a pre-upgrade key = %v, want ErrSecretUnavailable", err)
	}

	// And the old configuration still routes: the model registered by the
	// previous version is still resolvable.
	models, err := tm.ModelsByUpstreamID(context.Background(), "gpt-4o-mini")
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
		RetryCount: 2, FallbackCount: 1, TeamID: DefaultTeamID,
	}}); err != nil {
		t.Fatalf("insert after upgrade: %v", err)
	}
	fresh, err := tm.ListRequestLogs(context.Background(), LogFilter{Limit: 1})
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

// Key names became unique in 0018. A database written before that may hold
// duplicates, and the operator must not lose a key over it: the later ones are
// renamed, never deleted, because a renamed key keeps working and a deleted
// one starts returning 401s to whoever still holds it.
func TestDuplicateKeyNamesAreRenamedNotDeleted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyglot.db")
	old := openAtMigration(t, path, "0017_api_key_secret.sql")

	now := time.Now().Unix()
	for i, hash := range []string{"hash-a", "hash-b", "hash-c"} {
		if _, err := old.Exec(
			`INSERT INTO api_keys (name, prefix, secret_hash, enabled, created_at)
			 VALUES ('laptop', ?, ?, 1, ?)`,
			fmt.Sprintf("pg_dup%d", i), hash, now); err != nil {
			t.Fatalf("insert duplicate key %d: %v", i, err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("upgrading a database with duplicate key names failed: %v", err)
	}
	defer st.Close()

	keys, err := st.ForTeam(DefaultTeamID).ListAPIKeys(context.Background())
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("got %d keys after the upgrade, want all 3 kept", len(keys))
	}
	names := map[string]bool{}
	for _, k := range keys {
		if names[k.Name] {
			t.Errorf("two keys are still called %q", k.Name)
		}
		names[k.Name] = true
	}
	if !names["laptop"] {
		t.Error("the first key was renamed; only the later duplicates should be")
	}

	// And every one of them still authenticates, which is the point of keeping
	// them: the rename touched the name and nothing else.
	for _, hash := range []string{"hash-a", "hash-b", "hash-c"} {
		if _, err := st.APIKeyByHash(context.Background(), hash); err != nil {
			t.Errorf("key %s no longer authenticates after the rename: %v", hash, err)
		}
	}
}

// v0.2.0 is the release an operator upgrades from, and it shipped through
// 0019_full_content_logs.sql. 0020 is the migration that gives every ownable
// row a team, and the invariant it exists to establish is that there is always
// exactly one team with everything that came before inside it.
//
// The failure this pins down is not a crash. team_id defaults to 0 so that a
// write which forgot its team lands somewhere findable, which means a backfill
// that missed a table leaves its rows reachable only through a handle for a
// team that does not exist. Nothing errors: the operator's providers, keys and
// models simply stop being there. That is why the assertions below check for
// the sentinel by name rather than only checking that team 1 can see something.
func TestADatabaseFromV020FoldsIntoTheDefaultTeam(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "polyglot.db")
	old := openAtMigration(t, path, "0019_full_content_logs.sql")

	now := time.Now().Unix()
	startedAt := time.Now().Add(-time.Minute).UnixMilli()
	finishedAt := time.Now().UnixMilli()
	const logKeySecret = "pglog_v020_secret_value"

	// Everything a v0.2.0 deployment could own, written with v0.2.0's column
	// set: no team_id anywhere, because the column did not exist yet.
	if _, err := old.Exec(`INSERT INTO providers
		(name, protocol, base_url, headers, timeout_secs, enabled, priority, note, created_at, updated_at)
		VALUES ('v020-upstream', 'openai', 'https://api.example.com', '{"X-Title":"polyglot"}', 30, 1, 7, 'kept', ?, ?)`,
		now, now); err != nil {
		t.Fatalf("insert v0.2.0 provider: %v", err)
	}
	var providerID int64
	if err := old.QueryRow(`SELECT id FROM providers WHERE name = 'v020-upstream'`).Scan(&providerID); err != nil {
		t.Fatalf("read provider id: %v", err)
	}
	if _, err := old.Exec(`INSERT INTO models
		(provider_id, upstream_model_id, display_name, enabled, last_seen_at, created_at, updated_at, price_input)
		VALUES (?, 'deepseek-chat', 'DeepSeek Chat', 1, ?, ?, ?, 0.14)`,
		providerID, now, now, now); err != nil {
		t.Fatalf("insert v0.2.0 model: %v", err)
	}
	if _, err := old.Exec(`INSERT INTO model_aliases
		(alias, provider_id, upstream_model, priority, enabled, created_at, updated_at)
		VALUES ('fast', ?, 'deepseek-chat', 3, 1, ?, ?)`,
		providerID, now, now); err != nil {
		t.Fatalf("insert v0.2.0 alias: %v", err)
	}
	if _, err := old.Exec(`INSERT INTO api_keys
		(name, prefix, secret_hash, enabled, created_at, rpm, budget_usd, budget_period)
		VALUES ('laptop', 'pg_v020', 'v020-secret-hash', 1, ?, 60, 5.0, 'monthly')`,
		now); err != nil {
		t.Fatalf("insert v0.2.0 api key: %v", err)
	}
	var apiKeyID int64
	if err := old.QueryRow(`SELECT id FROM api_keys WHERE secret_hash = 'v020-secret-hash'`).Scan(&apiKeyID); err != nil {
		t.Fatalf("read api key id: %v", err)
	}
	if _, err := old.Exec(`INSERT INTO log_keys (name, token_hash, prefix, created_at)
		VALUES ('log reader', ?, 'pglog_v020_s', ?)`, HashToken(logKeySecret), now); err != nil {
		t.Fatalf("insert v0.2.0 log key: %v", err)
	}
	// One request log carrying a value in every shape the table holds: the
	// nullables filled rather than left null, so a migration that rewrote one
	// of them is visible in the comparison below.
	if _, err := old.Exec(`INSERT INTO request_logs (
		request_id, started_at, finished_at, latency_ms, ttft_ms, generation_ms, output_tps,
		status, status_code, client_protocol, upstream_protocol,
		provider_id, provider_name, model_alias, upstream_model,
		api_key_id, api_key_name, client_ip, client_app, request_user, request_metadata,
		stream, input_tokens, output_tokens, cached_input_tokens, cache_write_tokens, reasoning_tokens,
		retry_count, fallback_count, error_type, error_message, fidelity_notes,
		cost_usd, cost_source, cost_note, content_id, content_error
	) VALUES ('req-v020', ?, ?, 1234, 88, 900, 21.5,
		'success', 200, 'anthropic', 'openai',
		?, 'v020-upstream', 'fast', 'deepseek-chat',
		?, 'laptop', '203.0.113.9', 'Zed', 'user-7', '{"tier":"free"}',
		1, 100, 50, 40, 10, 12,
		2, 1, '', '', '[{"field":"top_k"}]',
		0.00123, 'catalog', 'cache_price_assumed', 'a1b2c3d4e5f60718293a4b5c', '')`,
		startedAt, finishedAt, providerID, apiKeyID); err != nil {
		t.Fatalf("insert v0.2.0 request log: %v", err)
	}
	// This deployment had content logging on, with the longest window.
	if _, err := old.Exec(`INSERT INTO settings (key, value, updated_at)
		VALUES ('content_logging', '{"enabled":true,"retention_days":30}', ?)`, now); err != nil {
		t.Fatalf("insert v0.2.0 content logging setting: %v", err)
	}

	// What that row looked like before the upgrade, column by column.
	before := columnValues(t, old, `SELECT * FROM request_logs WHERE request_id = 'req-v020'`)
	if err := old.Close(); err != nil {
		t.Fatalf("close the v0.2.0 database: %v", err)
	}

	// The v0.3 binary starts against it.
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("the new version cannot open a v0.2.0 database: %v", err)
	}
	defer st.Close()
	tm := st.ForTeam(DefaultTeamID)

	// (i) Exactly one team, and it is team 1. "Always exactly one team" is the
	// whole invariant: a second one here would mean the migration invented a
	// tenant nobody asked for, and none would mean every scoped read is empty.
	var teamCount int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM teams`).Scan(&teamCount); err != nil {
		t.Fatalf("count teams: %v", err)
	}
	if teamCount != 1 {
		t.Fatalf("teams holds %d rows after the upgrade, want exactly 1", teamCount)
	}
	var teamID int64
	var teamName string
	if err := st.DB().QueryRowContext(ctx, `SELECT id, name FROM teams`).Scan(&teamID, &teamName); err != nil {
		t.Fatalf("read the default team: %v", err)
	}
	if teamID != DefaultTeamID {
		t.Errorf("the only team has id %d, want %d — every scoped handle in the process is built from that constant",
			teamID, DefaultTeamID)
	}
	if teamName != "Default" {
		t.Errorf("the only team is called %q, want Default", teamName)
	}

	// (ii) Every pre-existing row across the four ownership-carrying tables
	// belongs to team 1, and none kept the sentinel. Counting 0 separately from
	// "not 1" is deliberate: 0 is the findable-orphan value the column defaults
	// to, so it is the shape a missed backfill actually takes.
	for _, table := range []string{"providers", "api_keys", "log_keys", "request_logs"} {
		var total, folded, orphaned int
		// The table name comes from this literal list, never from data.
		if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*),
			COALESCE(SUM(team_id = ?), 0), COALESCE(SUM(team_id = 0), 0) FROM `+table,
			DefaultTeamID).Scan(&total, &folded, &orphaned); err != nil {
			t.Fatalf("count %s rows by team: %v", table, err)
		}
		if total == 0 {
			t.Fatalf("%s holds no rows, so this test proves nothing about it; the fixture is stale", table)
		}
		if orphaned != 0 {
			t.Errorf("%d of %d %s rows kept team_id = 0: the backfill missed them and nothing scoped can reach them again",
				orphaned, total, table)
		}
		if folded != total {
			t.Errorf("%d of %d %s rows are outside team %d after the upgrade",
				total-folded, total, table, DefaultTeamID)
		}
	}

	// (iii) The old configuration still routes. models and model_aliases carry
	// no team_id of their own, so this is also the check that reaching a team
	// through the provider works on rows written before either existed.
	models, err := tm.ModelsByUpstreamID(ctx, "deepseek-chat")
	if err != nil {
		t.Fatalf("look up the pre-upgrade model: %v", err)
	}
	if len(models) != 1 || models[0].ProviderName != "v020-upstream" {
		t.Fatalf("the model registered before the upgrade no longer resolves: %+v", models)
	}
	aliases, err := tm.AliasesFor(ctx, "fast")
	if err != nil {
		t.Fatalf("look up the pre-upgrade alias: %v", err)
	}
	if len(aliases) != 1 || aliases[0].UpstreamModel != "deepseek-chat" {
		t.Errorf("the alias registered before the upgrade no longer resolves: %+v", aliases)
	}
	providers, err := tm.ListProviders(ctx)
	if err != nil || len(providers) != 1 {
		t.Fatalf("list providers after the upgrade: %v (%d rows)", err, len(providers))
	}

	// (iv) The key still authenticates — the lookup is by hash and unscoped, so
	// it must be untouched — and it now reports the team it belongs to, which is
	// how every request downstream of it discovers its tenant.
	key, err := st.APIKeyByHash(ctx, "v020-secret-hash")
	if err != nil {
		t.Fatalf("a key written by v0.2.0 no longer authenticates: %v", err)
	}
	if key.ID != apiKeyID || key.Name != "laptop" {
		t.Errorf("APIKeyByHash returned %+v, want the pre-upgrade key", key)
	}
	if key.TeamID != DefaultTeamID {
		t.Errorf("the pre-upgrade key reports team %d, want %d; a request carrying it would write logs no scope can read",
			key.TeamID, DefaultTeamID)
	}
	if key.TeamName != "Default" {
		t.Errorf("the pre-upgrade key reports team name %q, want Default", key.TeamName)
	}
	// The log key resolves the same way, and it is the one that reads prompt
	// bodies, so its team has to be right for the same reason and more.
	logKeyTeam, err := st.AuthorizeLogKey(ctx, logKeySecret)
	if err != nil {
		t.Fatalf("a log key written by v0.2.0 no longer authorizes: %v", err)
	}
	if logKeyTeam != DefaultTeamID {
		t.Errorf("the pre-upgrade log key resolves to team %d, want %d", logKeyTeam, DefaultTeamID)
	}

	// (v) The log row is unchanged column for column, and the two new columns
	// say what they should. Comparing the driver's own values rather than a
	// decoded struct is the point: a migration that rewrote a column would
	// survive a struct comparison against the same code that wrote it.
	after := columnValues(t, st.DB(), `SELECT * FROM request_logs WHERE request_id = 'req-v020'`)
	for col, want := range before {
		got, ok := after[col]
		if !ok {
			t.Errorf("column %s disappeared from request_logs during the upgrade", col)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("request_logs.%s = %#v after the upgrade, was %#v before it", col, got, want)
		}
	}
	if got := after["team_id"]; !reflect.DeepEqual(got, int64(DefaultTeamID)) {
		t.Errorf("request_logs.team_id = %#v on a pre-upgrade row, want %d", got, DefaultTeamID)
	}
	if got := after["team_name"]; !reflect.DeepEqual(got, "Default") {
		t.Errorf("request_logs.team_name = %#v on a pre-upgrade row, want Default; without the snapshot a "+
			"log row outlives its team as an integer pointing at nothing", got)
	}
	// And the scoped reader finds it, which is the same fact from the side the
	// logs page sees.
	scoped, err := tm.ListRequestLogs(ctx, LogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list logs after the upgrade: %v", err)
	}
	if len(scoped) != 1 || scoped[0].RequestID != "req-v020" {
		t.Fatalf("team %d sees %d of the 1 pre-upgrade log rows", DefaultTeamID, len(scoped))
	}
	if scoped[0].TeamName != "Default" {
		t.Errorf("the scoped read reports team name %q, want Default", scoped[0].TeamName)
	}

	// (vi), first direction: this deployment had content logging on at 30 days,
	// and it has to come up that way. Silently dropping to the default window
	// would throw away bodies an operator is relying on.
	if got := st.ContentLogging(DefaultTeamID); got != (ContentLogSettings{Enabled: true, RetentionDays: 30}) {
		t.Errorf("content logging after the upgrade = %+v, want it still on at 30 days", got)
	}

	// (vi), the other direction, which needs databases of its own: an upgrade
	// that silently switched recording on would start capturing prompts and
	// completions nobody asked it to capture. Both directions are failures of
	// the same seed, so both are asserted.
	for _, tc := range []struct {
		name    string
		setting string
		want    ContentLogSettings
	}{
		// A deployment that never touched the switch has no settings row at all.
		{"never enabled", "", ContentLogSettings{Enabled: false, RetentionDays: defaultRetentionDays}},
		// One that turned it on and then off again does, and its window is the
		// one the operator last chose.
		{"switched on and off again", `{"enabled":false,"retention_days":3}`,
			ContentLogSettings{Enabled: false, RetentionDays: 3}},
	} {
		t.Run("content logging "+tc.name+" stays off", func(t *testing.T) {
			live, stored, rows := contentLoggingAfterUpgrade(t, tc.setting)
			if rows != 1 {
				t.Fatalf("team_content_logging holds %d rows after the upgrade, want exactly the default team's", rows)
			}
			if stored != tc.want {
				t.Errorf("the seeded row says %+v, want %+v", stored, tc.want)
			}
			if live != tc.want {
				t.Errorf("the running store says %+v, want %+v", live, tc.want)
			}
		})
	}
}

// contentLoggingAfterUpgrade builds a v0.2.0 database holding the given
// settings['content_logging'] JSON — or no such row at all, when raw is empty —
// upgrades it, and reports what the running store believes, what the new
// per-team table actually holds, and how many rows that table has.
//
// The first two are collected separately on purpose. ContentLogging falls back
// to "off, at the default window" for a team it has no row for, so reading it
// alone cannot tell a row that was seeded correctly from one that was never
// written at all.
func contentLoggingAfterUpgrade(t *testing.T, raw string) (live, stored ContentLogSettings, rows int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "polyglot.db")
	old := openAtMigration(t, path, "0019_full_content_logs.sql")
	if raw != "" {
		if _, err := old.Exec(`INSERT INTO settings (key, value, updated_at)
			VALUES ('content_logging', ?, ?)`, raw, time.Now().Unix()); err != nil {
			t.Fatalf("write the v0.2.0 content logging setting: %v", err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close the v0.2.0 database: %v", err)
	}

	st, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer st.Close()

	if err := st.DB().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM team_content_logging`).Scan(&rows); err != nil {
		t.Fatalf("count seeded content logging rows: %v", err)
	}
	var enabled, days int
	err = st.DB().QueryRowContext(t.Context(),
		`SELECT enabled, retention_days FROM team_content_logging WHERE team_id = ?`,
		DefaultTeamID).Scan(&enabled, &days)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Left as the zero value, which no expectation matches: a missing row
		// is itself the failure.
	case err != nil:
		t.Fatalf("read the seeded content logging row: %v", err)
	default:
		stored = ContentLogSettings{Enabled: enabled != 0, RetentionDays: days}
	}
	return st.ContentLogging(DefaultTeamID), stored, rows
}

// columnValues reads one row as the values the driver hands back, keyed by
// column name. Comparing those across an upgrade catches a migration that
// rewrote a column, not only one that dropped it.
func columnValues(t *testing.T, db *sql.DB, query string, args ...any) map[string]any {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatalf("read row: %v", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("read columns: %v", err)
	}
	if !rows.Next() {
		t.Fatalf("no row matched %s: %v", query, rows.Err())
	}
	vals := make([]any, len(cols))
	dest := make([]any, len(cols))
	for i := range vals {
		dest[i] = &vals[i]
	}
	if err := rows.Scan(dest...); err != nil {
		t.Fatalf("scan row: %v", err)
	}
	out := make(map[string]any, len(cols))
	for i, c := range cols {
		out[c] = vals[i]
	}
	if rows.Next() {
		t.Fatalf("more than one row matched %s", query)
	}
	return out
}
