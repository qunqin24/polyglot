package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrAlreadySetup means the single administrator already exists. It is
// separate from a raw SQLite constraint error so the API can return 409
// without exposing database internals.
var ErrAlreadySetup = errors.New("Polyglot has already been set up")

type Admin struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

// AdminCount reports whether setup has run. Polyglot allows exactly one admin
// in this version.
func (s *Store) AdminCount(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return n, nil
}

func (s *Store) CreateAdmin(ctx context.Context, username, passwordHash string) (*Admin, error) {
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO admins (username, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		username, passwordHash, now, now)
	if err != nil {
		return nil, fmt.Errorf("create admin: %w", err)
	}
	id, _ := res.LastInsertId()
	return &Admin{ID: id, Username: username, PasswordHash: passwordHash, CreatedAt: time.Unix(now, 0)}, nil
}

// CreateInitialAdmin atomically creates the one administrator. The singleton
// index is the final backstop; INSERT OR IGNORE turns a racing loser into a
// stable domain error rather than a driver-specific constraint message.
func (s *Store) CreateInitialAdmin(ctx context.Context, username, passwordHash string) (*Admin, error) {
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO admins (username, password_hash, created_at, updated_at)
		 SELECT ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM admins)`,
		username, passwordHash, now, now)
	if err != nil {
		return nil, fmt.Errorf("create initial admin: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("read initial admin result: %w", err)
	}
	if n == 0 {
		return nil, ErrAlreadySetup
	}
	id, _ := res.LastInsertId()
	return &Admin{ID: id, Username: username, PasswordHash: passwordHash, CreatedAt: time.Unix(now, 0)}, nil
}

func (s *Store) AdminByUsername(ctx context.Context, username string) (*Admin, error) {
	var (
		a       Admin
		created int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, created_at FROM admins WHERE username = ?`, username).
		Scan(&a.ID, &a.Username, &a.PasswordHash, &created)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get admin: %w", err)
	}
	a.CreatedAt = time.Unix(created, 0)
	return &a, nil
}

func (s *Store) AdminByID(ctx context.Context, id int64) (*Admin, error) {
	var (
		a       Admin
		created int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, created_at FROM admins WHERE id = ?`, id).
		Scan(&a.ID, &a.Username, &a.PasswordHash, &created)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get admin: %w", err)
	}
	a.CreatedAt = time.Unix(created, 0)
	return &a, nil
}

// OnlyAdmin returns the single local administrator. Polyglot has exactly one
// by design, so recovery tooling can find it without being told a username —
// which matters, because a locked-out operator has often forgotten that too.
func (s *Store) OnlyAdmin(ctx context.Context) (*Admin, error) {
	var (
		a       Admin
		created int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, created_at FROM admins ORDER BY id LIMIT 1`).
		Scan(&a.ID, &a.Username, &a.PasswordHash, &created)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get admin: %w", err)
	}
	a.CreatedAt = time.Unix(created, 0)
	return &a, nil
}

func (s *Store) UpdateAdminPassword(ctx context.Context, id int64, hash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE admins SET password_hash = ?, updated_at = ? WHERE id = ?`,
		hash, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("update admin password: %w", err)
	}
	return nil
}

// --- sessions -------------------------------------------------------------

func (s *Store) CreateSession(ctx context.Context, tokenHash string, adminID int64, ttl time.Duration, userAgent string) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, admin_id, created_at, expires_at, user_agent) VALUES (?, ?, ?, ?, ?)`,
		tokenHash, adminID, now.Unix(), now.Add(ttl).Unix(), truncate(userAgent, 256))
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// SessionAdmin resolves a session token hash to its admin, rejecting expired
// rows.
func (s *Store) SessionAdmin(ctx context.Context, tokenHash string) (*Admin, error) {
	var adminID int64
	var expires int64
	err := s.db.QueryRowContext(ctx,
		`SELECT admin_id, expires_at FROM sessions WHERE token_hash = ?`, tokenHash).Scan(&adminID, &expires)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	if time.Now().Unix() > expires {
		s.DeleteSession(ctx, tokenHash)
		return nil, ErrNotFound
	}
	return s.AdminByID(ctx, adminID)
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

func (s *Store) DeleteSessionsForAdmin(ctx context.Context, adminID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE admin_id = ?`, adminID)
	return err
}

func (s *Store) PurgeExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, time.Now().Unix())
	return err
}

// --- api keys -------------------------------------------------------------

type APIKey struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	Prefix          string     `json:"prefix"`
	Enabled         bool       `json:"enabled"`
	CreatedAt       time.Time  `json:"created_at"`
	LastUsedAt      *time.Time `json:"last_used_at"`
	RPM             *int       `json:"rpm"`
	RPH             *int       `json:"rph"`
	RPD             *int       `json:"rpd"`
	TPM             *int       `json:"tpm"`
	TPD             *int       `json:"tpd"`
	MaxConcurrent   *int       `json:"max_concurrent"`
	MaxOutputTokens *int       `json:"max_output_tokens"`
	ExpiresAt       *time.Time `json:"expires_at"`
	AllowedModels   []string   `json:"allowed_models"`
	// BudgetUSD caps what this key may spend in the current window. Nil is no
	// cap, which is what every key had before budgets existed.
	BudgetUSD *float64 `json:"budget_usd"`
	// BudgetPeriod is one of the BudgetPeriod* constants: how the window
	// resets. Meaningless without a budget, and stored anyway so switching a
	// cap back on remembers what it was.
	BudgetPeriod string `json:"budget_period"`
	// BudgetAnchor is when the current PeriodTotal window began. Nil means the
	// key's creation. The rolling periods ignore it — they are computed from
	// the clock.
	BudgetAnchor *time.Time `json:"budget_anchor"`
	// SpentUSD and UnpricedRequests describe the current window. Both are
	// filled in by the admin API, never by the row itself, and are absent on a
	// key with no budget.
	SpentUSD         *float64 `json:"spent_usd,omitempty"`
	UnpricedRequests int      `json:"unpriced_requests,omitempty"`
	// BudgetResetsAt is when the current window ends. Absent on a total, which
	// ends when an operator says so. Filled in by the admin API from
	// BudgetResets, so the page shows the same instant the limiter uses
	// instead of a second implementation of "start of the month".
	BudgetResetsAt *time.Time `json:"budget_resets_at,omitempty"`
}

// How a key's budget window resets.
const (
	// BudgetTotal counts from BudgetAnchor forever: the money is gone until an
	// operator resets it.
	BudgetTotal = "total"
	// The rolling windows all start at UTC midnight, the way RPD and TPD do.
	BudgetDaily   = "daily"
	BudgetWeekly  = "weekly"
	BudgetMonthly = "monthly"
)

// ValidBudgetPeriod reports whether p names a window.
func ValidBudgetPeriod(p string) bool {
	switch p {
	case BudgetTotal, BudgetDaily, BudgetWeekly, BudgetMonthly:
		return true
	}
	return false
}

// BudgetWindowStart is when the current window began — the one definition of
// it, because enforcement and the number shown in the UI must agree or the key
// stops working at a figure nobody was shown.
func (k *APIKey) BudgetWindowStart(now time.Time) time.Time {
	now = now.UTC()
	switch k.BudgetPeriod {
	case BudgetDaily:
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	case BudgetWeekly:
		// ISO weeks start on Monday; Go counts Sunday as 0.
		offset := (int(now.Weekday()) + 6) % 7
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		return day.AddDate(0, 0, -offset)
	case BudgetMonthly:
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	default:
		if k.BudgetAnchor != nil {
			return k.BudgetAnchor.UTC()
		}
		return k.CreatedAt.UTC()
	}
}

// BudgetResets is when the current window ends, or zero for a total that only
// an operator can clear.
func (k *APIKey) BudgetResets(now time.Time) time.Time {
	start := k.BudgetWindowStart(now)
	switch k.BudgetPeriod {
	case BudgetDaily:
		return start.AddDate(0, 0, 1)
	case BudgetWeekly:
		return start.AddDate(0, 0, 7)
	case BudgetMonthly:
		return start.AddDate(0, 1, 0)
	default:
		return time.Time{}
	}
}

// APIKeyPolicy contains the optional restrictions on a client key. Nil numeric
// fields and an empty model list mean unlimited.
type APIKeyPolicy struct {
	RPM             *int
	RPH             *int
	RPD             *int
	TPM             *int
	TPD             *int
	MaxConcurrent   *int
	MaxOutputTokens *int
	ExpiresAt       *time.Time
	AllowedModels   []string
	BudgetUSD       *float64
	BudgetPeriod    string
}

const apiKeyCols = `id, name, prefix, enabled, created_at, last_used_at,
	rpm, rph, rpd, tpm, tpd, max_concurrent, max_output_tokens, expires_at,
	budget_usd, budget_period, budget_anchor`

func (s *Store) ListAPIKeys(ctx context.Context) ([]*APIKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+apiKeyCols+` FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()
	var out []*APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, fmt.Errorf("list api keys: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.loadAPIKeyModels(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func scanAPIKey(sc interface{ Scan(...any) error }) (*APIKey, error) {
	var (
		k                           APIKey
		enabled                     int
		created                     int64
		lastUsed                    sql.NullInt64
		rpm, rph, rpd, tpm, tpd     sql.NullInt64
		concurrent, output, expires sql.NullInt64
		budget                      sql.NullFloat64
		anchor                      sql.NullInt64
	)
	if err := sc.Scan(&k.ID, &k.Name, &k.Prefix, &enabled, &created, &lastUsed,
		&rpm, &rph, &rpd, &tpm, &tpd, &concurrent, &output, &expires,
		&budget, &k.BudgetPeriod, &anchor); err != nil {
		return nil, err
	}
	if budget.Valid {
		usd := budget.Float64
		k.BudgetUSD = &usd
	}
	if anchor.Valid {
		t := time.Unix(anchor.Int64, 0).UTC()
		k.BudgetAnchor = &t
	}
	k.Enabled = enabled != 0
	k.CreatedAt = time.Unix(created, 0)
	if lastUsed.Valid {
		t := time.Unix(lastUsed.Int64, 0)
		k.LastUsedAt = &t
	}
	k.RPM = nullableInt(rpm)
	k.RPH = nullableInt(rph)
	k.RPD = nullableInt(rpd)
	k.TPM = nullableInt(tpm)
	k.TPD = nullableInt(tpd)
	k.MaxConcurrent = nullableInt(concurrent)
	k.MaxOutputTokens = nullableInt(output)
	if expires.Valid {
		t := time.Unix(expires.Int64, 0).UTC()
		k.ExpiresAt = &t
	}
	k.AllowedModels = []string{}
	return &k, nil
}

func (s *Store) CreateAPIKey(ctx context.Context, name, prefix, secretHash string) (*APIKey, error) {
	return s.CreateAPIKeyWithPolicy(ctx, name, prefix, secretHash, APIKeyPolicy{})
}

func (s *Store) CreateAPIKeyWithPolicy(ctx context.Context, name, prefix, secretHash string, p APIKeyPolicy) (*APIKey, error) {
	now := time.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create api key: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO api_keys (name, prefix, secret_hash, enabled, created_at,
		 rpm, rph, rpd, tpm, tpd, max_concurrent, max_output_tokens, expires_at,
		 budget_usd, budget_period, budget_anchor)
		 VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		name, prefix, secretHash, now, nullPositive(p.RPM), nullPositive(p.RPH), nullPositive(p.RPD),
		nullPositive(p.TPM), nullPositive(p.TPD), nullPositive(p.MaxConcurrent),
		nullPositive(p.MaxOutputTokens), nullTime(p.ExpiresAt),
		nullBudget(p.BudgetUSD), budgetPeriodOr(p.BudgetPeriod), now)
	if err != nil {
		return nil, fmt.Errorf("create api key: %w", err)
	}
	id, _ := res.LastInsertId()
	models := cleanModels(p.AllowedModels)
	if err := replaceAPIKeyModels(ctx, tx, id, models); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create api key: %w", err)
	}
	anchor := time.Unix(now, 0).UTC()
	return &APIKey{ID: id, Name: name, Prefix: prefix, Enabled: true, CreatedAt: time.Unix(now, 0),
		RPM: positivePtr(p.RPM), RPH: positivePtr(p.RPH), RPD: positivePtr(p.RPD),
		TPM: positivePtr(p.TPM), TPD: positivePtr(p.TPD), MaxConcurrent: positivePtr(p.MaxConcurrent),
		MaxOutputTokens: positivePtr(p.MaxOutputTokens), ExpiresAt: p.ExpiresAt, AllowedModels: models,
		BudgetUSD: positiveMoney(p.BudgetUSD), BudgetPeriod: budgetPeriodOr(p.BudgetPeriod),
		BudgetAnchor: &anchor}, nil
}

// APIKeyByHash looks up an active key. Callers pass the SHA-256 of the
// presented token; the plaintext never reaches the database.
func (s *Store) APIKeyByHash(ctx context.Context, hash string) (*APIKey, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+apiKeyCols+` FROM api_keys WHERE secret_hash = ?`, hash)
	k, err := scanAPIKey(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lookup api key: %w", err)
	}
	if err := s.loadAPIKeyModels(ctx, []*APIKey{k}); err != nil {
		return nil, err
	}
	return k, nil
}

func (s *Store) GetAPIKey(ctx context.Context, id int64) (*APIKey, error) {
	k, err := scanAPIKey(s.db.QueryRowContext(ctx, `SELECT `+apiKeyCols+` FROM api_keys WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get api key: %w", err)
	}
	if err := s.loadAPIKeyModels(ctx, []*APIKey{k}); err != nil {
		return nil, err
	}
	return k, nil
}

func (s *Store) UpdateAPIKey(ctx context.Context, id int64, name string, enabled bool, p APIKeyPolicy) (*APIKey, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin update api key: %w", err)
	}
	defer tx.Rollback()
	// Changing the period starts the new window now. Counting a fresh monthly
	// cap against last year's spending, or a "total" against everything the key
	// ever did, would exhaust it the moment it was set.
	res, err := tx.ExecContext(ctx, `UPDATE api_keys SET name = ?, enabled = ?, rpm = ?, rph = ?, rpd = ?,
		tpm = ?, tpd = ?, max_concurrent = ?, max_output_tokens = ?, expires_at = ?,
		budget_usd = ?, budget_anchor = CASE WHEN budget_period = ? THEN budget_anchor ELSE ? END,
		budget_period = ? WHERE id = ?`,
		name, boolInt(enabled), nullPositive(p.RPM), nullPositive(p.RPH), nullPositive(p.RPD),
		nullPositive(p.TPM), nullPositive(p.TPD), nullPositive(p.MaxConcurrent),
		nullPositive(p.MaxOutputTokens), nullTime(p.ExpiresAt),
		nullBudget(p.BudgetUSD), budgetPeriodOr(p.BudgetPeriod), time.Now().Unix(),
		budgetPeriodOr(p.BudgetPeriod), id)
	if err != nil {
		return nil, fmt.Errorf("update api key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	if err := replaceAPIKeyModels(ctx, tx, id, cleanModels(p.AllowedModels)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit update api key: %w", err)
	}
	return s.GetAPIKey(ctx, id)
}

func (s *Store) SetAPIKeyEnabled(ctx context.Context, id int64, enabled bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE api_keys SET enabled = ? WHERE id = ?`, boolInt(enabled), id)
	if err != nil {
		return fmt.Errorf("set api key enabled: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) TouchAPIKey(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = ? WHERE id = ?`, time.Now().Unix(), id)
	return err
}

func (s *Store) DeleteAPIKey(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// APIKeySpendSince adds up what a key's finished requests cost since a point.
//
// The count of unpriced requests travels with the total because it is the
// difference between "spent nothing" and "spent an amount nobody can state":
// a model with no price leaves cost_usd null, and a budget that silently
// treated that as zero would be a cap on the priced half of the traffic.
func (s *Store) APIKeySpendSince(ctx context.Context, id int64, since time.Time) (float64, int, error) {
	row := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(cost_usd), 0),
		COALESCE(SUM(CASE WHEN cost_usd IS NULL THEN 1 ELSE 0 END), 0)
		FROM request_logs WHERE api_key_id = ? AND started_at >= ?`,
		id, since.UnixMilli())
	var spent float64
	var unpriced int
	if err := row.Scan(&spent, &unpriced); err != nil {
		return 0, 0, fmt.Errorf("sum api key spend: %w", err)
	}
	return spent, unpriced, nil
}

// ResetAPIKeyBudget starts a new total window. The request logs are untouched:
// what was spent still happened, this only moves the line it is counted from.
func (s *Store) ResetAPIKeyBudget(ctx context.Context, id int64, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE api_keys SET budget_anchor = ? WHERE id = ?`, at.Unix(), id)
	if err != nil {
		return fmt.Errorf("reset api key budget: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type APIKeyUsageSample struct {
	At         time.Time
	FinishedAt time.Time
	Tokens     int
}

// APIKeyUsageSince rebuilds the in-memory rolling windows after a restart.
// Request logs are the durable source of truth; reasoning tokens are not added
// because providers disagree about whether they are already in output_tokens.
func (s *Store) APIKeyUsageSince(ctx context.Context, id int64, since time.Time) ([]APIKeyUsageSample, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT started_at, finished_at, input_tokens + output_tokens
		FROM request_logs WHERE api_key_id = ? AND (started_at >= ? OR finished_at >= ?)
		AND NOT (error_type = 'rate_limit' AND provider_id IS NULL) ORDER BY started_at`,
		id, since.UnixMilli(), since.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("load api key usage: %w", err)
	}
	defer rows.Close()
	var out []APIKeyUsageSample
	for rows.Next() {
		var at int64
		var finished int64
		var tokens int
		if err := rows.Scan(&at, &finished, &tokens); err != nil {
			return nil, fmt.Errorf("scan api key usage: %w", err)
		}
		out = append(out, APIKeyUsageSample{At: time.UnixMilli(at), FinishedAt: time.UnixMilli(finished), Tokens: tokens})
	}
	return out, rows.Err()
}

func (s *Store) loadAPIKeyModels(ctx context.Context, keys []*APIKey) error {
	if len(keys) == 0 {
		return nil
	}
	byID := make(map[int64]*APIKey, len(keys))
	for _, k := range keys {
		k.AllowedModels = []string{}
		byID[k.ID] = k
	}
	rows, err := s.db.QueryContext(ctx, `SELECT api_key_id, model FROM api_key_models ORDER BY model`)
	if err != nil {
		return fmt.Errorf("list api key models: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var model string
		if err := rows.Scan(&id, &model); err != nil {
			return fmt.Errorf("scan api key model: %w", err)
		}
		if k := byID[id]; k != nil {
			k.AllowedModels = append(k.AllowedModels, model)
		}
	}
	return rows.Err()
}

func replaceAPIKeyModels(ctx context.Context, tx *sql.Tx, id int64, models []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM api_key_models WHERE api_key_id = ?`, id); err != nil {
		return fmt.Errorf("clear api key models: %w", err)
	}
	for _, model := range models {
		if _, err := tx.ExecContext(ctx, `INSERT INTO api_key_models (api_key_id, model) VALUES (?, ?)`, id, model); err != nil {
			return fmt.Errorf("add api key model: %w", err)
		}
	}
	return nil
}

func cleanModels(models []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model != "" && !seen[model] {
			seen[model] = true
			out = append(out, model)
		}
	}
	sort.Strings(out)
	return out
}

func nullableInt(v sql.NullInt64) *int {
	if !v.Valid || v.Int64 <= 0 {
		return nil
	}
	n := int(v.Int64)
	return &n
}

func positivePtr(v *int) *int {
	if v == nil || *v <= 0 {
		return nil
	}
	n := *v
	return &n
}

func nullPositive(v *int) any {
	if v == nil || *v <= 0 {
		return nil
	}
	return *v
}

// A budget of zero or less is no budget: the numeric limits already spell
// "unlimited" that way, and money should not be the one that means "free".
func positiveMoney(v *float64) *float64 {
	if v == nil || *v <= 0 {
		return nil
	}
	usd := *v
	return &usd
}

func nullBudget(v *float64) any {
	if v == nil || *v <= 0 {
		return nil
	}
	return *v
}

func budgetPeriodOr(p string) string {
	if ValidBudgetPeriod(p) {
		return p
	}
	return BudgetTotal
}

func nullTime(v *time.Time) any {
	if v == nil {
		return nil
	}
	return v.UTC().Unix()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
