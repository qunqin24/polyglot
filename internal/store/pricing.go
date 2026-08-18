package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/qunqin24/polyglot/internal/pricing"
)

// The price catalog and the operator's overrides. Together they answer what a
// model costs; internal/pricing decides how to combine them.

// CatalogStatus describes the snapshot currently loaded.
type CatalogStatus struct {
	Version   string    `json:"version"`
	Source    string    `json:"source"` // embedded | models.dev
	FetchedAt time.Time `json:"fetched_at"`
	Models    int       `json:"models"`
}

// CatalogStatus reports which snapshot is loaded, or nil when none is.
func (s *Store) CatalogStatus(ctx context.Context) (*CatalogStatus, error) {
	var (
		st      CatalogStatus
		fetched int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT version, source, fetched_at FROM price_catalog_meta WHERE id = 1`).
		Scan(&st.Version, &st.Source, &fetched)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read price catalog status: %w", err)
	}
	st.FetchedAt = time.Unix(fetched, 0)
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM price_catalog`).Scan(&st.Models); err != nil {
		return nil, fmt.Errorf("count price catalog: %w", err)
	}
	return &st, nil
}

// ReplaceCatalog swaps in a new snapshot wholesale.
//
// It touches only the catalog: an operator's overrides live on the model rows
// and are not part of this table, so refreshing prices can never overwrite a
// price somebody typed — the same rule discovery follows for the registry.
func (s *Store) ReplaceCatalog(ctx context.Context, snap *pricing.Snapshot, source string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin catalog replace: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM price_catalog`); err != nil {
		return fmt.Errorf("clear price catalog: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO price_catalog (model_id, vendor, input, output, cache_read, cache_write,
			tier_above_tokens, tier_input, tier_output, tier_cache_read, tier_cache_write)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare catalog insert: %w", err)
	}
	defer stmt.Close()
	for _, e := range snap.Entries {
		var (
			above                    any
			tIn, tOut, tRead, tWrite any
		)
		if e.Tier != nil {
			above = e.Tier.AboveTokens
			tIn, tOut = nullFloat64(e.Tier.Input), nullFloat64(e.Tier.Output)
			tRead, tWrite = nullFloat64(e.Tier.CacheRead), nullFloat64(e.Tier.CacheWrite)
		}
		_, err := stmt.ExecContext(ctx, e.ID, e.Vendor,
			nullFloat64(e.Input), nullFloat64(e.Output),
			nullFloat64(e.CacheRead), nullFloat64(e.CacheWrite),
			above, tIn, tOut, tRead, tWrite)
		if err != nil {
			return fmt.Errorf("insert catalog price %q: %w", e.ID, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO price_catalog_meta (id, version, source, fetched_at) VALUES (1, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET version = excluded.version,
		     source = excluded.source, fetched_at = excluded.fetched_at`,
		snap.Version, source, time.Now().Unix()); err != nil {
		return fmt.Errorf("record catalog version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit catalog replace: %w", err)
	}
	return nil
}

// LoadEmbeddedCatalog installs the snapshot compiled into the binary, unless
// the database already holds one at least as new.
//
// Upgrading the binary should bring newer official prices with it, but must
// never roll back a refresh the operator ran more recently — the versions are
// dates, so the comparison is the obvious one.
func (s *Store) LoadEmbeddedCatalog(ctx context.Context, snap *pricing.Snapshot) error {
	cur, err := s.CatalogStatus(ctx)
	if err != nil {
		return err
	}
	if cur != nil && cur.Version >= snap.Version {
		return nil
	}
	return s.ReplaceCatalog(ctx, snap, "embedded")
}

// CatalogPrices reads the whole catalog, keyed by normalised model id.
func (s *Store) CatalogPrices(ctx context.Context) (map[string]pricing.Rates, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT model_id, input, output, cache_read, cache_write,
		        tier_above_tokens, tier_input, tier_output, tier_cache_read, tier_cache_write
		 FROM price_catalog`)
	if err != nil {
		return nil, fmt.Errorf("read price catalog: %w", err)
	}
	defer rows.Close()

	out := map[string]pricing.Rates{}
	for rows.Next() {
		var (
			id                       string
			in, outp, cr, cw         sql.NullFloat64
			above                    sql.NullInt64
			tIn, tOut, tRead, tWrite sql.NullFloat64
		)
		if err := rows.Scan(&id, &in, &outp, &cr, &cw,
			&above, &tIn, &tOut, &tRead, &tWrite); err != nil {
			return nil, fmt.Errorf("read price catalog: %w", err)
		}
		r := pricing.Rates{Price: price(in, outp, cr, cw)}
		if above.Valid && above.Int64 > 0 {
			r.Tier = &pricing.Tier{
				AboveTokens: int(above.Int64),
				Price:       price(tIn, tOut, tRead, tWrite),
			}
		}
		out[id] = r
	}
	return out, rows.Err()
}

// ModelPriceOverrides reads every price an operator typed, keyed by provider
// and model. A model with nothing set is absent rather than present and blank.
func (s *Store) ModelPriceOverrides(ctx context.Context) (map[pricing.ModelKey]pricing.Price, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT provider_id, upstream_model_id, price_input, price_output,
		        price_cache_read, price_cache_write
		 FROM models
		 WHERE price_input IS NOT NULL OR price_output IS NOT NULL
		    OR price_cache_read IS NOT NULL OR price_cache_write IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("read model prices: %w", err)
	}
	defer rows.Close()

	out := map[pricing.ModelKey]pricing.Price{}
	for rows.Next() {
		var (
			key           pricing.ModelKey
			in, o, cr, cw sql.NullFloat64
		)
		if err := rows.Scan(&key.ProviderID, &key.Model, &in, &o, &cr, &cw); err != nil {
			return nil, fmt.Errorf("read model prices: %w", err)
		}
		out[key] = price(in, o, cr, cw)
	}
	return out, rows.Err()
}

// SetModelPrice stores an operator's override. A nil field clears that price
// and puts the model back on the catalog, which is why this takes four
// nullable numbers rather than a copy of whatever the form displayed.
func (s *Store) SetModelPrice(ctx context.Context, id int64, p pricing.Price) (*Model, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE models SET price_input = ?, price_output = ?, price_cache_read = ?,
		     price_cache_write = ?, updated_at = ? WHERE id = ?`,
		nullFloat64(p.Input), nullFloat64(p.Output), nullFloat64(p.CacheRead),
		nullFloat64(p.CacheWrite), time.Now().Unix(), id)
	if err != nil {
		return nil, fmt.Errorf("set price for model %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.GetModel(ctx, id)
}

func price(in, out, cr, cw sql.NullFloat64) pricing.Price {
	return pricing.Price{
		Input:      floatPtr(in),
		Output:     floatPtr(out),
		CacheRead:  floatPtr(cr),
		CacheWrite: floatPtr(cw),
	}
}

func floatPtr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}
