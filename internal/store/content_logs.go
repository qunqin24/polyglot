package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ContentLogSettings struct {
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retention_days"`
}

const contentLogSetting = "content_logging"

func (s *Store) ContentLogging() ContentLogSettings {
	if v := s.contentLogging.Load(); v != nil {
		return *v
	}
	return ContentLogSettings{RetentionDays: 7}
}
func (s *Store) loadContentLogging(ctx context.Context) error {
	raw, err := s.GetSetting(ctx, contentLogSetting)
	if err != nil {
		return err
	}
	cfg := ContentLogSettings{RetentionDays: 7}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return fmt.Errorf("decode content logging settings: %w", err)
		}
	}
	s.contentLogging.Store(&cfg)
	return nil
}
func (s *Store) SetContentLogging(ctx context.Context, cfg ContentLogSettings) error {
	if cfg.RetentionDays != 3 && cfg.RetentionDays != 7 && cfg.RetentionDays != 30 {
		return fmt.Errorf("retention_days must be 3, 7, or 30")
	}
	s.contentMu.Lock()
	defer s.contentMu.Unlock()
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := s.SetSetting(ctx, contentLogSetting, string(raw)); err != nil {
		return err
	}
	s.contentLogging.Store(&cfg)
	return nil
}
func (s *Store) ContentDir() string { return s.contentDir }
func validContentID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 12
}
func (s *Store) OpenLogContent(id string) (*os.File, error) {
	if !validContentID(id) {
		return nil, ErrNotFound
	}
	f, err := os.Open(filepath.Join(s.contentDir, id+".jsonl.gz"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	cutoff := time.Now().AddDate(0, 0, -s.ContentLogging().RetentionDays)
	if !info.ModTime().After(cutoff) {
		f.Close()
		return nil, ErrNotFound
	}
	return f, nil
}
func (s *Store) RemoveLogContent(id string) {
	if validContentID(id) {
		_ = os.Remove(filepath.Join(s.contentDir, id+".jsonl.gz"))
	}
}
func (s *Store) PruneLogContents() error {
	entries, err := os.ReadDir(s.contentDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := time.Now().AddDate(0, 0, -s.ContentLogging().RetentionDays)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(s.contentDir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

type LogKey struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

func scanLogKey(row interface{ Scan(...any) error }) (*LogKey, error) {
	var k LogKey
	var created int64
	var last, expires sql.NullInt64
	if err := row.Scan(&k.ID, &k.Name, &k.Prefix, &created, &last, &expires); err != nil {
		return nil, err
	}
	k.CreatedAt = time.Unix(created, 0)
	if last.Valid {
		t := time.Unix(last.Int64, 0)
		k.LastUsedAt = &t
	}
	if expires.Valid {
		t := time.Unix(expires.Int64, 0)
		k.ExpiresAt = &t
	}
	return &k, nil
}

const logKeyCols = "id, name, prefix, created_at, last_used_at, expires_at"

func (s *Store) ListLogKeys(ctx context.Context) ([]*LogKey, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+logKeyCols+" FROM log_keys ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*LogKey{}
	for rows.Next() {
		k, err := scanLogKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
func (s *Store) CreateLogKey(ctx context.Context, name, secret string, expires *time.Time) (*LogKey, error) {
	var exp any
	if expires != nil {
		exp = expires.Unix()
	}
	res, err := s.db.ExecContext(ctx, "INSERT INTO log_keys (name,token_hash,prefix,created_at,expires_at) VALUES (?,?,?,?,?)", name, HashToken(secret), secret[:min(12, len(secret))], time.Now().Unix(), exp)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return scanLogKey(s.db.QueryRowContext(ctx, "SELECT "+logKeyCols+" FROM log_keys WHERE id=?", id))
}
func (s *Store) DeleteLogKey(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM log_keys WHERE id=?", id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}
func (s *Store) AuthorizeLogKey(ctx context.Context, secret string) error {
	var id int64
	err := s.db.QueryRowContext(ctx, "SELECT id FROM log_keys WHERE token_hash=? AND (expires_at IS NULL OR expires_at>?)", HashToken(secret), time.Now().Unix()).Scan(&id)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "UPDATE log_keys SET last_used_at=? WHERE id=? AND (last_used_at IS NULL OR last_used_at<?)", time.Now().Unix(), id, time.Now().Add(-time.Minute).Unix())
	return err
}
