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
	ID       int64  `json:"id"`
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

const providerCols = `p.id, p.name, p.protocol, p.base_url, p.note, p.api_key_enc, p.headers,
	p.timeout_secs, p.enabled, p.priority, p.strict_fields, p.auto_disable_on_auth_error,
	p.disabled_reason, p.disabled_at, p.models_synced_at,
	p.created_at, p.updated_at`

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
	if err := sc.Scan(&p.ID, &p.Name, &p.Protocol, &p.BaseURL, &p.Note, &keyEnc, &headers,
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

func (s *Store) ListProviders(ctx context.Context) ([]*Provider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+providerCols+` FROM providers p ORDER BY p.priority DESC, p.name`)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	defer rows.Close()
	var out []*Provider
	for rows.Next() {
		p, err := s.scanProvider(rows)
		if err != nil {
			return nil, fmt.Errorf("list providers: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProvider(ctx context.Context, id int64) (*Provider, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+providerCols+` FROM providers p WHERE p.id = ?`, id)
	p, err := s.scanProvider(row)
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
func (s *Store) ProviderByName(ctx context.Context, name string) (*Provider, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+providerCols+` FROM providers p WHERE lower(p.name) = lower(?)`, name)
	p, err := s.scanProvider(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get provider %q: %w", name, err)
	}
	return p, nil
}

func (s *Store) CreateProvider(ctx context.Context, p *Provider) (*Provider, error) {
	enc, err := s.cipher.Encrypt(p.APIKey)
	if err != nil {
		return nil, err
	}
	headers, err := json.Marshal(orEmptyMap(p.Headers))
	if err != nil {
		return nil, fmt.Errorf("encode headers: %w", err)
	}
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO providers (name, protocol, base_url, note, api_key_enc, headers, timeout_secs, enabled, priority,
			strict_fields, auto_disable_on_auth_error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.Protocol, p.BaseURL, p.Note, enc, string(headers), p.TimeoutSecs, boolInt(p.Enabled), p.Priority,
		boolInt(p.StrictFields), boolInt(p.AutoDisableOnAuthError), now, now)
	if err != nil {
		return nil, fmt.Errorf("create provider %q: %w", p.Name, err)
	}
	id, _ := res.LastInsertId()
	return s.GetProvider(ctx, id)
}

// UpdateProvider writes every field. A nil apiKey leaves the stored credential
// untouched, which is how the WebUI edits a provider without ever receiving
// the existing secret.
func (s *Store) UpdateProvider(ctx context.Context, id int64, p *Provider, apiKey *string) (*Provider, error) {
	headers, err := json.Marshal(orEmptyMap(p.Headers))
	if err != nil {
		return nil, fmt.Errorf("encode headers: %w", err)
	}
	now := time.Now().Unix()
	if apiKey != nil {
		enc, err := s.cipher.Encrypt(*apiKey)
		if err != nil {
			return nil, err
		}
		_, err = s.db.ExecContext(ctx,
			`UPDATE providers SET name=?, protocol=?, base_url=?, note=?, api_key_enc=?, headers=?, timeout_secs=?,
				enabled=?, priority=?, strict_fields=?, auto_disable_on_auth_error=?,
				disabled_reason=CASE WHEN ?=1 THEN '' ELSE disabled_reason END,
				disabled_at=CASE WHEN ?=1 THEN NULL ELSE disabled_at END,
				updated_at=? WHERE id=?`,
			p.Name, p.Protocol, p.BaseURL, p.Note, enc, string(headers), p.TimeoutSecs, boolInt(p.Enabled), p.Priority,
			boolInt(p.StrictFields), boolInt(p.AutoDisableOnAuthError),
			boolInt(p.Enabled), boolInt(p.Enabled), now, id)
		if err != nil {
			return nil, fmt.Errorf("update provider %d: %w", id, err)
		}
	} else {
		_, err = s.db.ExecContext(ctx,
			`UPDATE providers SET name=?, protocol=?, base_url=?, note=?, headers=?, timeout_secs=?,
				enabled=?, priority=?, strict_fields=?, auto_disable_on_auth_error=?,
				disabled_reason=CASE WHEN ?=1 THEN '' ELSE disabled_reason END,
				disabled_at=CASE WHEN ?=1 THEN NULL ELSE disabled_at END,
				updated_at=? WHERE id=?`,
			p.Name, p.Protocol, p.BaseURL, p.Note, string(headers), p.TimeoutSecs, boolInt(p.Enabled), p.Priority,
			boolInt(p.StrictFields), boolInt(p.AutoDisableOnAuthError),
			boolInt(p.Enabled), boolInt(p.Enabled), now, id)
		if err != nil {
			return nil, fmt.Errorf("update provider %d: %w", id, err)
		}
	}
	return s.GetProvider(ctx, id)
}

func (s *Store) DeleteProvider(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM providers WHERE id = ?`, id)
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

func (s *Store) ListAliases(ctx context.Context) ([]*ModelAlias, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+aliasCols+` FROM model_aliases a JOIN providers p ON p.id = a.provider_id
		 ORDER BY a.alias, a.priority DESC, a.id`)
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
func (s *Store) AliasesFor(ctx context.Context, alias string) ([]*ModelAlias, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+aliasCols+` FROM model_aliases a JOIN providers p ON p.id = a.provider_id
		 WHERE a.alias = ? AND a.enabled = 1 AND p.enabled = 1
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

func (s *Store) GetAlias(ctx context.Context, id int64) (*ModelAlias, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+aliasCols+` FROM model_aliases a JOIN providers p ON p.id = a.provider_id WHERE a.id = ?`, id)
	m, err := scanAlias(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get alias %d: %w", id, err)
	}
	return m, nil
}

func (s *Store) CreateAlias(ctx context.Context, m *ModelAlias) (*ModelAlias, error) {
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO model_aliases (alias, provider_id, upstream_model, priority, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		m.Alias, m.ProviderID, m.UpstreamModel, m.Priority, boolInt(m.Enabled), now, now)
	if err != nil {
		return nil, fmt.Errorf("create alias %q: %w", m.Alias, err)
	}
	id, _ := res.LastInsertId()
	return s.GetAlias(ctx, id)
}

func (s *Store) UpdateAlias(ctx context.Context, id int64, m *ModelAlias) (*ModelAlias, error) {
	_, err := s.db.ExecContext(ctx,
		`UPDATE model_aliases SET alias=?, provider_id=?, upstream_model=?, priority=?, enabled=?, updated_at=? WHERE id=?`,
		m.Alias, m.ProviderID, m.UpstreamModel, m.Priority, boolInt(m.Enabled), time.Now().Unix(), id)
	if err != nil {
		return nil, fmt.Errorf("update alias %d: %w", id, err)
	}
	return s.GetAlias(ctx, id)
}

func (s *Store) DeleteAlias(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM model_aliases WHERE id = ?`, id)
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
func (s *Store) DisableProvider(ctx context.Context, id int64, reason string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE providers SET enabled = 0, disabled_reason = ?, disabled_at = ?, updated_at = ?
		 WHERE id = ? AND enabled = 1`,
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
