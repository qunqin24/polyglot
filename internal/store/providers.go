package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrNotFound = errors.New("not found")

// Provider is one upstream service. Protocol says how to talk to it; Name is
// only a label.
type Provider struct {
	ID int64 `json:"id"`
	// TeamID is the team this provider belongs to. It is never serialised:
	// there is always exactly one team, so putting a tenant field in the admin
	// API's JSON would introduce the concept to the WebUI on day one while
	// there is still nothing for it to distinguish. Same rule as APIKey below.
	TeamID   int64  `json:"-"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url"`
	// Note is the operator's own description of this provider. Metadata only:
	// nothing in the request path, the router or pricing reads it.
	Note        string            `json:"note"`
	APIKey      string            `json:"-"` // never serialised to the WebUI
	HasAPIKey   bool              `json:"has_api_key"`
	Headers     map[string]string `json:"headers"`
	TimeoutSecs int               `json:"timeout_secs"`
	Enabled     bool              `json:"enabled"`
	// Priority breaks ties when the same upstream model id exists on several
	// providers. Lower wins; equal priorities fall back to id order.
	Priority int `json:"priority"`
	// AutoDisableOnAuthError takes this provider out of rotation when its
	// credential stops working. Off by default: an upstream may answer 401 or
	// 403 for a region restriction or an exhausted quota, not only for a bad
	// key, so switching a provider off is the operator's call to opt into.
	AutoDisableOnAuthError bool `json:"auto_disable_on_auth_error"`
	// DisabledReason explains a provider that switched itself off. Cleared
	// whenever the operator enables it again.
	DisabledReason string     `json:"disabled_reason"`
	DisabledAt     *time.Time `json:"disabled_at"`

	// StrictFields stops Polyglot replaying request fields it does not
	// recognise to this upstream. It names the exception rather than the rule
	// so the zero value is the behaviour almost every provider wants: an
	// upstream that accepts its own extra parameters gets them.
	StrictFields bool `json:"strict_fields"`
	// ModelsSyncedAt is when discovery last ran; nil means never.
	ModelsSyncedAt *time.Time `json:"models_synced_at"`
	// ModelCount is filled in by the admin API, not by the row itself.
	ModelCount int `json:"model_count"`
	// CoolingUntil is filled in by the admin API when this provider is being
	// skipped after a recent failure. It is in-process state, never a stored
	// column, so it is absent from every query in this file.
	CoolingUntil *time.Time `json:"cooling_until,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// ModelAlias is an optional logical name for a model. It exists so clients can
// use a stable name like "coding" while an operator repoints it at a different
// provider or model. Calling a real upstream model id never needs one.
type ModelAlias struct {
	ID            int64     `json:"id"`
	Alias         string    `json:"alias"`
	ProviderID    int64     `json:"provider_id"`
	ProviderName  string    `json:"provider_name,omitempty"`
	Protocol      string    `json:"protocol,omitempty"`
	UpstreamModel string    `json:"upstream_model"`
	Priority      int       `json:"priority"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

const providerCols = `p.id, p.team_id, p.name, p.protocol, p.base_url, p.note, p.api_key_enc, p.headers,
	p.timeout_secs, p.enabled, p.priority, p.strict_fields, p.auto_disable_on_auth_error,
	p.disabled_reason, p.disabled_at, p.models_synced_at,
	p.created_at, p.updated_at`

// scanProvider stays on *Store because it needs the cipher, not the team: the
// row it is handed has already been through a scoped query. Scoped callers
// reach it as t.s.scanProvider.
func (s *Store) scanProvider(sc interface{ Scan(...any) error }) (*Provider, error) {
	var (
		p          Provider
		keyEnc     []byte
		headers    string
		created    int64
		updated    int64
		enabled    int
		strict     int
		autoOff    int
		disabledAt sql.NullInt64
		syncedAt   sql.NullInt64
	)
	if err := sc.Scan(&p.ID, &p.TeamID, &p.Name, &p.Protocol, &p.BaseURL, &p.Note, &keyEnc, &headers,
		&p.TimeoutSecs, &enabled, &p.Priority, &strict, &autoOff, &p.DisabledReason, &disabledAt, &syncedAt,
		&created, &updated); err != nil {
		return nil, err
	}
	p.StrictFields = strict != 0
	p.AutoDisableOnAuthError = autoOff != 0
	if disabledAt.Valid {
		t := time.Unix(disabledAt.Int64, 0)
		p.DisabledAt = &t
	}
	if syncedAt.Valid {
		t := time.Unix(syncedAt.Int64, 0)
		p.ModelsSyncedAt = &t
	}
	key, err := s.cipher.Decrypt(keyEnc)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", p.Name, err)
	}
	p.APIKey = key
	p.HasAPIKey = key != ""
	p.Enabled = enabled != 0
	p.CreatedAt = time.Unix(created, 0)
	p.UpdatedAt = time.Unix(updated, 0)
	p.Headers = map[string]string{}
	if headers != "" {
		if err := json.Unmarshal([]byte(headers), &p.Headers); err != nil {
			return nil, fmt.Errorf("provider %q headers: %w", p.Name, err)
		}
	}
	return &p, nil
}

func (t *Scope) ListProviders(ctx context.Context) ([]*Provider, error) {
	rows, err := t.query(ctx, providerCols, ownedProviders, `ORDER BY p.priority DESC, p.name`)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	defer rows.Close()
	var out []*Provider
	for rows.Next() {
		p, err := t.s.scanProvider(rows)
		if err != nil {
			return nil, fmt.Errorf("list providers: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (t *Scope) GetProvider(ctx context.Context, id int64) (*Provider, error) {
	row := t.queryRow(ctx, providerCols, ownedProviders, `AND p.id = ?`, id)
	p, err := t.s.scanProvider(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get provider %d: %w", id, err)
	}
	return p, nil
}

// ProviderByName resolves the explicit "provider::model" form. The comparison
// is case-insensitive because operators type these by hand.
//
// providers.name is still globally unique in this phase (0020 defers the
// rebuild), so the team predicate is what makes this lookup answer for the
// caller's team rather than for whoever registered the name first.
func (t *Scope) ProviderByName(ctx context.Context, name string) (*Provider, error) {
	row := t.queryRow(ctx, providerCols, ownedProviders, `AND lower(p.name) = lower(?)`, name)
	p, err := t.s.scanProvider(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get provider %q: %w", name, err)
	}
	return p, nil
}

// CreateProvider registers an upstream for this team.
//
// The INSERT names team_id itself. An insert has no WHERE clause for a
// constructor to open, so this is the one shape where forgetting the team is
// caught by migration 0020's trigger rather than by SQL that refuses to
// compose — the team on the row is this handle's, never anything the caller
// passed in.
func (t *Scope) CreateProvider(ctx context.Context, p *Provider) (*Provider, error) {
	enc, err := t.s.cipher.Encrypt(p.APIKey)
	if err != nil {
		return nil, err
	}
	headers, err := json.Marshal(orEmptyMap(p.Headers))
	if err != nil {
		return nil, fmt.Errorf("encode headers: %w", err)
	}
	now := time.Now().Unix()
	res, err := t.s.db.ExecContext(ctx,
		`INSERT INTO providers (team_id, name, protocol, base_url, note, api_key_enc, headers, timeout_secs, enabled, priority,
			strict_fields, auto_disable_on_auth_error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.teamID, p.Name, p.Protocol, p.BaseURL, p.Note, enc, string(headers), p.TimeoutSecs, boolInt(p.Enabled), p.Priority,
		boolInt(p.StrictFields), boolInt(p.AutoDisableOnAuthError), now, now)
	if err != nil {
		return nil, fmt.Errorf("create provider %q: %w", p.Name, err)
	}
	id, _ := res.LastInsertId()
	return t.GetProvider(ctx, id)
}

// UpdateProvider writes every field. A nil apiKey leaves the stored credential
// untouched, which is how the WebUI edits a provider without ever receiving
// the existing secret.
func (t *Scope) UpdateProvider(ctx context.Context, id int64, p *Provider, apiKey *string) (*Provider, error) {
	headers, err := json.Marshal(orEmptyMap(p.Headers))
	if err != nil {
		return nil, fmt.Errorf("encode headers: %w", err)
	}
	now := time.Now().Unix()
	if apiKey != nil {
		enc, err := t.s.cipher.Encrypt(*apiKey)
		if err != nil {
			return nil, err
		}
		_, err = t.exec(ctx,
			`UPDATE SET name=?, protocol=?, base_url=?, note=?, api_key_enc=?, headers=?, timeout_secs=?,
				enabled=?, priority=?, strict_fields=?, auto_disable_on_auth_error=?,
				disabled_reason=CASE WHEN ?=1 THEN '' ELSE disabled_reason END,
				disabled_at=CASE WHEN ?=1 THEN NULL ELSE disabled_at END,
				updated_at=?`, ownedProviders, `AND id=?`,
			p.Name, p.Protocol, p.BaseURL, p.Note, enc, string(headers), p.TimeoutSecs, boolInt(p.Enabled), p.Priority,
			boolInt(p.StrictFields), boolInt(p.AutoDisableOnAuthError),
			boolInt(p.Enabled), boolInt(p.Enabled), now, id)
		if err != nil {
			return nil, fmt.Errorf("update provider %d: %w", id, err)
		}
	} else {
		_, err = t.exec(ctx,
			`UPDATE SET name=?, protocol=?, base_url=?, note=?, headers=?, timeout_secs=?,
				enabled=?, priority=?, strict_fields=?, auto_disable_on_auth_error=?,
				disabled_reason=CASE WHEN ?=1 THEN '' ELSE disabled_reason END,
				disabled_at=CASE WHEN ?=1 THEN NULL ELSE disabled_at END,
				updated_at=?`, ownedProviders, `AND id=?`,
			p.Name, p.Protocol, p.BaseURL, p.Note, string(headers), p.TimeoutSecs, boolInt(p.Enabled), p.Priority,
			boolInt(p.StrictFields), boolInt(p.AutoDisableOnAuthError),
			boolInt(p.Enabled), boolInt(p.Enabled), now, id)
		if err != nil {
			return nil, fmt.Errorf("update provider %d: %w", id, err)
		}
	}
	return t.GetProvider(ctx, id)
}

func (t *Scope) DeleteProvider(ctx context.Context, id int64) error {
	res, err := t.exec(ctx, "DELETE", ownedProviders, `AND id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete provider %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

const aliasCols = `a.id, a.alias, a.provider_id, p.name, p.protocol, a.upstream_model, a.priority, a.enabled, a.created_at, a.updated_at`

func scanAlias(sc interface{ Scan(...any) error }) (*ModelAlias, error) {
	var (
		m       ModelAlias
		enabled int
		created int64
		updated int64
	)
	if err := sc.Scan(&m.ID, &m.Alias, &m.ProviderID, &m.ProviderName, &m.Protocol,
		&m.UpstreamModel, &m.Priority, &enabled, &created, &updated); err != nil {
		return nil, err
	}
	m.Enabled = enabled != 0
	m.CreatedAt = time.Unix(created, 0)
	m.UpdatedAt = time.Unix(updated, 0)
	return &m, nil
}

// model_aliases carries no team_id of its own: it reaches its team through the
// provider it points at, which is why every read below goes through
// queryViaProvider and every write carries its own guard on providers.

func (t *Scope) ListAliases(ctx context.Context) ([]*ModelAlias, error) {
	rows, err := t.queryViaProvider(ctx, aliasCols, "model_aliases", "a",
		`ORDER BY a.alias, a.priority DESC, a.id`)
	if err != nil {
		return nil, fmt.Errorf("list aliases: %w", err)
	}
	defer rows.Close()
	var out []*ModelAlias
	for rows.Next() {
		m, err := scanAlias(rows)
		if err != nil {
			return nil, fmt.Errorf("list aliases: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AliasesFor returns the enabled routes for an alias, best first. Only
// routes whose provider is also enabled are returned.
func (t *Scope) AliasesFor(ctx context.Context, alias string) ([]*ModelAlias, error) {
	rows, err := t.queryViaProvider(ctx, aliasCols, "model_aliases", "a",
		`AND a.alias = ? AND a.enabled = 1 AND p.enabled = 1
		 ORDER BY a.priority DESC, a.id`, alias)
	if err != nil {
		return nil, fmt.Errorf("aliases for %q: %w", alias, err)
	}
	defer rows.Close()
	var out []*ModelAlias
	for rows.Next() {
		m, err := scanAlias(rows)
		if err != nil {
			return nil, fmt.Errorf("aliases for %q: %w", alias, err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (t *Scope) GetAlias(ctx context.Context, id int64) (*ModelAlias, error) {
	row := t.queryRowViaProvider(ctx, aliasCols, "model_aliases", "a", `AND a.id = ?`, id)
	m, err := scanAlias(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get alias %d: %w", id, err)
	}
	return m, nil
}

// CreateAlias points a logical name at one provider's model.
//
// The INSERT is written as INSERT … SELECT … WHERE EXISTS so the target
// provider has to be proved to belong to this team in the same statement that
// writes the row. Nothing is inserted otherwise, and no rows affected is
// reported as ErrNotFound: from where the caller stands, a provider in another
// team is a provider that does not exist.
func (t *Scope) CreateAlias(ctx context.Context, m *ModelAlias) (*ModelAlias, error) {
	now := time.Now().Unix()
	res, err := t.s.db.ExecContext(ctx,
		`INSERT INTO model_aliases (alias, provider_id, upstream_model, priority, enabled, created_at, updated_at)
		 SELECT ?, ?, ?, ?, ?, ?, ?
		 WHERE EXISTS (SELECT 1 FROM providers WHERE id = ? AND team_id = ?)`,
		m.Alias, m.ProviderID, m.UpstreamModel, m.Priority, boolInt(m.Enabled), now, now,
		m.ProviderID, t.teamID)
	if err != nil {
		return nil, fmt.Errorf("create alias %q: %w", m.Alias, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	id, _ := res.LastInsertId()
	return t.GetAlias(ctx, id)
}

// UpdateAlias needs two guards, not one: the row being edited has to be in
// this team, and so does the provider it is being repointed at. Checking only
// the first would let an alias be aimed at another team's provider, which is
// the same leak as editing that provider directly.
func (t *Scope) UpdateAlias(ctx context.Context, id int64, m *ModelAlias) (*ModelAlias, error) {
	res, err := t.s.db.ExecContext(ctx,
		`UPDATE model_aliases SET alias=?, provider_id=?, upstream_model=?, priority=?, enabled=?, updated_at=?
		 WHERE id=?
		   AND model_aliases.provider_id IN (SELECT id FROM providers WHERE team_id = ?)
		   AND EXISTS (SELECT 1 FROM providers WHERE id = ? AND team_id = ?)`,
		m.Alias, m.ProviderID, m.UpstreamModel, m.Priority, boolInt(m.Enabled), time.Now().Unix(),
		id, t.teamID, m.ProviderID, t.teamID)
	if err != nil {
		return nil, fmt.Errorf("update alias %d: %w", id, err)
	}
	// Without this the update could match nothing and GetAlias would still
	// return the untouched row, reporting a change that never happened.
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return t.GetAlias(ctx, id)
}

func (t *Scope) DeleteAlias(ctx context.Context, id int64) error {
	res, err := t.s.db.ExecContext(ctx,
		`DELETE FROM model_aliases
		 WHERE id = ? AND provider_id IN (SELECT id FROM providers WHERE team_id = ?)`, id, t.teamID)
	if err != nil {
		return fmt.Errorf("delete alias %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func orEmptyMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// DisableProvider switches a provider off and records why.
//
// It is used when a credential has stopped working: unlike a timeout or a
// rate limit, that does not heal on its own, so leaving the provider in
// rotation only produces a steady trickle of failures. The reason is stored
// because a provider that disabled itself silently would be a mystery.
func (t *Scope) DisableProvider(ctx context.Context, id int64, reason string) error {
	res, err := t.exec(ctx,
		`UPDATE SET enabled = 0, disabled_reason = ?, disabled_at = ?, updated_at = ?`,
		ownedProviders, `AND id = ? AND enabled = 1`,
		truncate(reason, 500), time.Now().Unix(), time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("disable provider %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Already off, or gone. Not an error: two requests can fail at once.
		return nil
	}
	return nil
}
