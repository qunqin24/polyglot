package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/qunqin24/polyglot/internal/pricing"
)

// alone, so a hand-added model is never clobbered by a sync.
// Model is a real upstream model a provider offers. Clients may call its
// UpstreamModelID directly — no alias required.
type Model struct {
	ID              int64      `json:"id"`
	ProviderID      int64      `json:"provider_id"`
	ProviderName    string     `json:"provider_name,omitempty"`
	Protocol        string     `json:"protocol,omitempty"`
	UpstreamModelID string     `json:"upstream_model_id"`
	DisplayName     string     `json:"display_name"`
	Enabled         bool       `json:"enabled"`
	LastSeenAt      *time.Time `json:"last_seen_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`

	// Price is what the operator typed for this model. Every field is null
	// unless they set one, and null means "follow the catalog" rather than
	// "free" — so correcting one number still tracks an official cut in the
	// others.
	Price pricing.Price `json:"price"`

	// Ambiguous reports that another enabled provider offers the same
	// upstream id, so calling it unqualified resolves by provider priority.
	Ambiguous bool `json:"ambiguous,omitempty"`
}

const modelCols = `m.id, m.provider_id, p.name, p.protocol, m.upstream_model_id, m.display_name,
	m.enabled, m.last_seen_at, m.created_at, m.updated_at,
	m.price_input, m.price_output, m.price_cache_read, m.price_cache_write`

func scanModel(sc interface{ Scan(...any) error }) (*Model, error) {
	var (
		m             Model
		enabled       int
		lastSeen      sql.NullInt64
		created       int64
		updated       int64
		in, o, cr, cw sql.NullFloat64
	)
	if err := sc.Scan(&m.ID, &m.ProviderID, &m.ProviderName, &m.Protocol, &m.UpstreamModelID,
		&m.DisplayName, &enabled, &lastSeen, &created, &updated,
		&in, &o, &cr, &cw); err != nil {
		return nil, err
	}
	m.Price = price(in, o, cr, cw)
	m.Enabled = enabled != 0
	m.CreatedAt = time.Unix(created, 0)
	m.UpdatedAt = time.Unix(updated, 0)
	if lastSeen.Valid {
		t := time.Unix(lastSeen.Int64, 0)
		m.LastSeenAt = &t
	}
	return &m, nil
}

type ModelFilter struct {
	ProviderID int64
	Search     string
	// EnabledOnly restricts to models a client could actually call.
	EnabledOnly bool
	Limit       int
	Offset      int
}

// models carries no team_id of its own: it reaches its team through the
// provider that offers it. Every read below goes through queryViaProvider,
// whose join cannot be written as a LEFT JOIN, and every write carries its own
// guard on providers.

// ListModels returns registry entries joined with their provider.
func (t *Scope) ListModels(ctx context.Context, f ModelFilter) ([]*Model, error) {
	// The filter terms are conditional; the team predicate is not, which is
	// why it comes from the constructor rather than from this list.
	var rest strings.Builder
	var args []any
	if f.ProviderID > 0 {
		rest.WriteString("AND m.provider_id = ? ")
		args = append(args, f.ProviderID)
	}
	if f.Search != "" {
		rest.WriteString("AND (m.upstream_model_id LIKE ? OR m.display_name LIKE ?) ")
		args = append(args, "%"+f.Search+"%", "%"+f.Search+"%")
	}
	if f.EnabledOnly {
		rest.WriteString("AND m.enabled = 1 AND p.enabled = 1 ")
	}

	rest.WriteString("ORDER BY p.priority DESC, p.name, m.upstream_model_id")

	limit := f.Limit
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	rest.WriteString(" LIMIT ? OFFSET ?")
	args = append(args, limit, f.Offset)

	rows, err := t.queryViaProvider(ctx, modelCols, "models", "m", rest.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer rows.Close()
	var out []*Model
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, fmt.Errorf("list models: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CountModels reports how many rows match, ignoring paging.
func (t *Scope) CountModels(ctx context.Context, f ModelFilter) (int, error) {
	var rest strings.Builder
	var args []any
	if f.ProviderID > 0 {
		rest.WriteString("AND m.provider_id = ? ")
		args = append(args, f.ProviderID)
	}
	if f.Search != "" {
		rest.WriteString("AND (m.upstream_model_id LIKE ? OR m.display_name LIKE ?) ")
		args = append(args, "%"+f.Search+"%", "%"+f.Search+"%")
	}
	if f.EnabledOnly {
		rest.WriteString("AND m.enabled = 1 AND p.enabled = 1 ")
	}
	var n int
	if err := t.queryRowViaProvider(ctx, "COUNT(*)", "models", "m", rest.String(), args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count models: %w", err)
	}
	return n, nil
}

// ModelsByUpstreamID returns every enabled model with this exact upstream id,
// ordered by provider priority then provider id. The order is total and
// stable, so an ambiguous id always resolves to the same provider.
func (t *Scope) ModelsByUpstreamID(ctx context.Context, upstreamID string) ([]*Model, error) {
	rows, err := t.queryViaProvider(ctx, modelCols, "models", "m",
		`AND m.upstream_model_id = ? AND m.enabled = 1 AND p.enabled = 1
		 ORDER BY p.priority DESC, p.id`, upstreamID)
	if err != nil {
		return nil, fmt.Errorf("models by upstream id %q: %w", upstreamID, err)
	}
	defer rows.Close()
	var out []*Model
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, fmt.Errorf("models by upstream id: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ModelForProvider looks up one model on one provider, used by the explicit
// "provider::model" form.
func (t *Scope) ModelForProvider(ctx context.Context, providerID int64, upstreamID string) (*Model, error) {
	row := t.queryRowViaProvider(ctx, modelCols, "models", "m",
		`AND m.provider_id = ? AND m.upstream_model_id = ? AND m.enabled = 1`,
		providerID, upstreamID)
	m, err := scanModel(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("model %q on provider %d: %w", upstreamID, providerID, err)
	}
	return m, nil
}

func (t *Scope) GetModel(ctx context.Context, id int64) (*Model, error) {
	row := t.queryRowViaProvider(ctx, modelCols, "models", "m", `AND m.id = ?`, id)
	m, err := scanModel(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get model %d: %w", id, err)
	}
	return m, nil
}

// CreateModel adds a manual entry.
//
// INSERT … SELECT … WHERE EXISTS proves the provider belongs to this team in
// the same statement that writes the row; nothing is inserted otherwise, and
// no rows affected reads as ErrNotFound rather than as a permission error.
func (t *Scope) CreateModel(ctx context.Context, m *Model) (*Model, error) {
	now := time.Now().Unix()
	res, err := t.s.db.ExecContext(ctx,
		`INSERT INTO models (provider_id, upstream_model_id, display_name, enabled, created_at, updated_at)
		 SELECT ?, ?, ?, ?, ?, ?
		 WHERE EXISTS (SELECT 1 FROM providers WHERE id = ? AND team_id = ?)`,
		m.ProviderID, m.UpstreamModelID, m.DisplayName, boolInt(m.Enabled), now, now,
		m.ProviderID, t.teamID)
	if err != nil {
		return nil, fmt.Errorf("create model %q: %w", m.UpstreamModelID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	id, _ := res.LastInsertId()
	return t.GetModel(ctx, id)
}

// UpdateModel changes the operator-editable fields. The upstream id is not
// editable: that would silently repoint an entry at a different model.
func (t *Scope) UpdateModel(ctx context.Context, id int64, displayName string, enabled bool) (*Model, error) {
	res, err := t.s.db.ExecContext(ctx,
		`UPDATE models SET display_name = ?, enabled = ?, updated_at = ?
		 WHERE id = ? AND provider_id IN (SELECT id FROM providers WHERE team_id = ?)`,
		displayName, boolInt(enabled), time.Now().Unix(), id, t.teamID)
	if err != nil {
		return nil, fmt.Errorf("update model %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return t.GetModel(ctx, id)
}

func (t *Scope) DeleteModel(ctx context.Context, id int64) error {
	res, err := t.s.db.ExecContext(ctx,
		`DELETE FROM models
		 WHERE id = ? AND provider_id IN (SELECT id FROM providers WHERE team_id = ?)`, id, t.teamID)
	if err != nil {
		return fmt.Errorf("delete model %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SyncResult summarises one discovery run.
type SyncResult struct {
	Added   int `json:"added"`
	Updated int `json:"updated"`
	Total   int `json:"total"`
}

// DiscoveredModel is one entry returned by a provider's model listing.
type DiscoveredModel struct {
	ID          string
	DisplayName string
}

// SyncModels records the models a discovery run found.
//
// It never deletes. An upstream can omit a model because of a partial
// response, a transient failure, or a temporary withdrawal, and losing an
// operator's configuration over that would be a bad trade. Rows that were not
// seen simply keep their older last_seen_at, which the UI surfaces.
func (t *Scope) SyncModels(ctx context.Context, providerID int64, found []DiscoveredModel) (*SyncResult, error) {
	now := time.Now().Unix()

	tx, err := t.s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin model sync: %w", err)
	}
	defer tx.Rollback()

	// The provider is proved to belong to this team once, as the transaction's
	// first statement; everything after it is keyed on that same provider id.
	// Repeating the check inside each statement would mean the per-model upsert
	// stops being a single prepared statement, and this is the one statement
	// here that runs once per discovered model.
	var ok int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM providers WHERE id = ? AND team_id = ?`, providerID, t.teamID).Scan(&ok)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("check provider %d: %w", providerID, err)
	}

	existing := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT upstream_model_id FROM models WHERE provider_id = ?`, providerID)
	if err != nil {
		return nil, fmt.Errorf("read existing models: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read existing models: %w", err)
		}
		existing[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO models
		(provider_id, upstream_model_id, display_name, enabled, last_seen_at, created_at, updated_at)
		VALUES (?, ?, ?, 1, ?, ?, ?)
		ON CONFLICT (provider_id, upstream_model_id) DO UPDATE SET
			-- Keep a name the operator set; only fill in a blank one.
			display_name = CASE WHEN models.display_name = '' THEN excluded.display_name ELSE models.display_name END,
			-- 'enabled' is deliberately untouched: a model the operator disabled
			-- stays exactly as they left it.
			last_seen_at = excluded.last_seen_at,
			updated_at   = excluded.updated_at`)
	if err != nil {
		return nil, fmt.Errorf("prepare model upsert: %w", err)
	}
	defer stmt.Close()

	result := &SyncResult{}
	for _, m := range found {
		if strings.TrimSpace(m.ID) == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, providerID, m.ID, m.DisplayName, now, now, now); err != nil {
			return nil, fmt.Errorf("upsert model %q: %w", m.ID, err)
		}
		if existing[m.ID] {
			result.Updated++
		} else {
			result.Added++
		}
	}

	// Redundant after the guard above, but providers is an ownership root and
	// every write to one carries the predicate. An invariant with one
	// exception is an invariant somebody has to remember.
	if _, err := tx.ExecContext(ctx,
		`UPDATE providers SET models_synced_at = ? WHERE id = ? AND team_id = ?`,
		now, providerID, t.teamID); err != nil {
		return nil, fmt.Errorf("record sync time: %w", err)
	}

	var total int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM models WHERE provider_id = ?`, providerID).Scan(&total); err != nil {
		return nil, fmt.Errorf("count models: %w", err)
	}
	result.Total = total

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit model sync: %w", err)
	}
	return result, nil
}

// AmbiguousModelIDs returns upstream ids offered by more than one enabled
// provider. The Models page flags these, because calling them unqualified
// resolves by provider priority rather than by the operator's intent.
//
// Ambiguity is a fact about one team: two teams offering the same upstream id
// are not competing to resolve it.
func (t *Scope) AmbiguousModelIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := t.queryViaProvider(ctx, "m.upstream_model_id", "models", "m",
		`AND m.enabled = 1 AND p.enabled = 1
		 GROUP BY m.upstream_model_id HAVING COUNT(DISTINCT m.provider_id) > 1`)
	if err != nil {
		return nil, fmt.Errorf("find ambiguous models: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("find ambiguous models: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ModelCountsByProvider powers the per-provider "327 models" summary.
//
// This is the one read over models that had no join to hang the predicate on.
// It gets the same join every other read here uses rather than a subquery of
// its own: models.provider_id is NOT NULL and references providers, so the
// inner join matches each row exactly once and the counts are the counts this
// query always returned.
func (t *Scope) ModelCountsByProvider(ctx context.Context) (map[int64]int, error) {
	rows, err := t.queryViaProvider(ctx, "m.provider_id, COUNT(*)", "models", "m",
		`GROUP BY m.provider_id`)
	if err != nil {
		return nil, fmt.Errorf("count models by provider: %w", err)
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("count models by provider: %w", err)
		}
		out[id] = n
	}
	return out, rows.Err()
}
