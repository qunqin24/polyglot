package store

import (
	"context"
	"database/sql"
	"encoding/hex"
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

// defaultRetentionDays is what a team with no row of its own gets: recording
// off, and the middle window if it is ever switched on.
const defaultRetentionDays = 7

// ContentLogging reports one team's setting.
//
// This is the switch that records prompts and completions, so whether a team's
// conversations are captured, and for how long they are kept, is that team's
// answer and not a deployment-wide one.
//
// The read is a single atomic load and has to stay one: the gateway consults it
// once per request on the streaming path. The map behind the pointer is never
// written to in place — SetContentLogging replaces it wholesale — so a reader
// sees either the old map or the new one and never takes a lock to find out
// which.
func (s *Store) ContentLogging(teamID int64) ContentLogSettings {
	if v := s.contentLogging.Load(); v != nil {
		if cfg, ok := (*v)[teamID]; ok {
			return cfg
		}
	}
	return ContentLogSettings{RetentionDays: defaultRetentionDays}
}

// contentLoggingAll hands back the whole snapshot for the pruner, which has to
// know every team's window rather than one team's. Same single atomic load; the
// map must not be written to.
func (s *Store) contentLoggingAll() map[int64]ContentLogSettings {
	if v := s.contentLogging.Load(); v != nil {
		return *v
	}
	return nil
}

func (s *Store) loadContentLogging(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT team_id, enabled, retention_days FROM team_content_logging`)
	if err != nil {
		return fmt.Errorf("read content logging settings: %w", err)
	}
	defer rows.Close()
	all := map[int64]ContentLogSettings{}
	for rows.Next() {
		var (
			teamID  int64
			enabled int
			days    int
		)
		if err := rows.Scan(&teamID, &enabled, &days); err != nil {
			return fmt.Errorf("read content logging settings: %w", err)
		}
		all[teamID] = ContentLogSettings{Enabled: enabled != 0, RetentionDays: days}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read content logging settings: %w", err)
	}
	s.contentLogging.Store(&all)
	return nil
}

// SetContentLogging writes one team's setting.
//
// It names the team in an argument rather than hanging off *Scope because the
// cache it has to replace is the whole deployment's, under the same mutex the
// loader uses; splitting the read and the write of one table across two
// receivers would cost more than an explicit id, and a required int64 still
// makes forgetting the team a compile error.
func (s *Store) SetContentLogging(ctx context.Context, teamID int64, cfg ContentLogSettings) error {
	if cfg.RetentionDays != 3 && cfg.RetentionDays != 7 && cfg.RetentionDays != 30 {
		return fmt.Errorf("retention_days must be 3, 7, or 30")
	}
	s.contentMu.Lock()
	defer s.contentMu.Unlock()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO team_content_logging (team_id, enabled, retention_days, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT (team_id) DO UPDATE SET enabled = excluded.enabled,
		     retention_days = excluded.retention_days, updated_at = excluded.updated_at`,
		teamID, boolInt(cfg.Enabled), cfg.RetentionDays, time.Now().Unix()); err != nil {
		return fmt.Errorf("save content logging settings: %w", err)
	}
	// Copy on write. The mutex serialises writers; readers never take one, and
	// see the old map until the pointer swings to the new one.
	cur := s.contentLoggingAll()
	next := make(map[int64]ContentLogSettings, len(cur)+1)
	for id, v := range cur {
		next[id] = v
	}
	next[teamID] = cfg
	s.contentLogging.Store(&next)
	return nil
}
func (s *Store) ContentDir() string { return s.contentDir }
func validContentID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 12
}

// OpenLogContent opens a recorded body, provided it is still inside the
// retention window.
//
// It takes a team because the window is per team now — not because it decides
// who may read the body. It deliberately does not: its only caller reaches it
// through the scoped request-log lookup, which is what settles whether this
// reader may see that request at all. A second check here would look like the
// boundary and let the real one rot.
func (s *Store) OpenLogContent(teamID int64, id string) (*os.File, error) {
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
	cutoff := time.Now().AddDate(0, 0, -s.ContentLogging(teamID).RetentionDays)
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

// PruneLogContents deletes recorded bodies that have outlived the retention
// window of the team that wrote them.
//
// It stays on *Store. This is the easiest mistake in the whole team change to
// make: a sweep that moved every method onto the scoped handle would take this
// one along, and a scoped pruner reclaims disk for one team while every other
// team's bodies accumulate for ever. The entry point is deployment-wide; only
// the cutoff is per team. Those two facts do not conflict.
func (s *Store) PruneLogContents(ctx context.Context) error {
	entries, err := os.ReadDir(s.contentDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	shortest, longest := retentionBounds(s.contentLoggingAll())
	now := time.Now()

	// One bounded query for the whole sweep, not one per file: every content id
	// written inside the longest window anybody configured, with the team that
	// wrote it.
	owner, err := s.contentOwners(ctx, now.AddDate(0, 0, -longest))
	if err != nil {
		return err
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		days := shortest
		if teamID, ok := owner[strings.TrimSuffix(e.Name(), ".jsonl.gz")]; ok {
			days = s.ContentLogging(teamID).RetentionDays
		}
		if info.ModTime().Before(now.AddDate(0, 0, -days)) {
			if err := os.Remove(filepath.Join(s.contentDir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

// contentOwners maps a content id to the team whose request wrote it, for every
// log row since the given time.
func (s *Store) contentOwners(ctx context.Context, since time.Time) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT content_id, team_id FROM request_logs WHERE content_id != '' AND started_at >= ?`,
		// Milliseconds: started_at is written and read that way everywhere
		// else. Seconds here would be a bound about a thousand times too low,
		// so every row ever written would match — the sweep would scan the
		// whole table instead of one window, and a file older than the longest
		// retention would keep being attributed to its team instead of falling
		// out of the map and being pruned on the shortest window.
		since.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("read content owners: %w", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var (
			id     string
			teamID int64
		)
		if err := rows.Scan(&id, &teamID); err != nil {
			return nil, fmt.Errorf("read content owners: %w", err)
		}
		out[id] = teamID
	}
	return out, rows.Err()
}

// retentionBounds reports the shortest and longest window any team configured.
//
// The longest bounds the lookup query. The shortest is what a file with no log
// row is cleaned on: a body nobody can attribute belongs on the tightest window
// anybody chose, and since the windows are measured in days, a file whose log
// row has not been flushed yet is nowhere near the boundary.
func retentionBounds(all map[int64]ContentLogSettings) (shortest, longest int) {
	shortest, longest = defaultRetentionDays, defaultRetentionDays
	first := true
	for _, cfg := range all {
		days := cfg.RetentionDays
		if days <= 0 {
			days = defaultRetentionDays
		}
		if first {
			shortest, longest, first = days, days, false
			continue
		}
		if days < shortest {
			shortest = days
		}
		if days > longest {
			longest = days
		}
	}
	return shortest, longest
}

type LogKey struct {
	ID int64 `json:"id"`
	// TeamID is whose logs this key reads. Never serialised, for the same
	// reason Provider.TeamID is not: there is one team, so the WebUI has
	// nothing to do with the concept yet.
	TeamID     int64      `json:"-"`
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
	if err := row.Scan(&k.ID, &k.TeamID, &k.Name, &k.Prefix, &created, &last, &expires); err != nil {
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

const logKeyCols = "id, team_id, name, prefix, created_at, last_used_at, expires_at"

func (t *Scope) ListLogKeys(ctx context.Context) ([]*LogKey, error) {
	rows, err := t.query(ctx, logKeyCols, ownedLogKeys, "ORDER BY id DESC")
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

// CreateLogKey mints a read-only key for this team's logs. The INSERT names
// team_id itself; there is no WHERE clause to carry it, which is what
// migration 0020's trigger is there to catch.
func (t *Scope) CreateLogKey(ctx context.Context, name, secret string, expires *time.Time) (*LogKey, error) {
	var exp any
	if expires != nil {
		exp = expires.Unix()
	}
	res, err := t.s.db.ExecContext(ctx, "INSERT INTO log_keys (team_id,name,token_hash,prefix,created_at,expires_at) VALUES (?,?,?,?,?,?)", t.teamID, name, HashToken(secret), secret[:min(12, len(secret))], time.Now().Unix(), exp)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return scanLogKey(t.queryRow(ctx, logKeyCols, ownedLogKeys, "AND id=?", id))
}
func (t *Scope) DeleteLogKey(ctx context.Context, id int64) error {
	res, err := t.exec(ctx, "DELETE", ownedLogKeys, "AND id=?", id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

// AuthorizeLogKey resolves a presented secret to the team whose logs it reads.
//
// It stays on *Store, and it is not an oversight that it carries no team
// predicate: this is the lookup that *discovers* the team, so scoping it would
// be circular — there is nothing to scope it by until it has answered. That is
// also why log_keys.token_hash stays globally unique. Everything downstream of
// this call runs on the scope built from the team it returns, and a log key
// reads prompt and completion bodies, so that is the one call chain in this
// phase where a missing predicate would be silent today and would read another
// team's conversations the moment there are two.
func (s *Store) AuthorizeLogKey(ctx context.Context, secret string) (int64, error) {
	var id, teamID int64
	err := s.db.QueryRowContext(ctx, "SELECT id, team_id FROM log_keys WHERE token_hash=? AND (expires_at IS NULL OR expires_at>?)", HashToken(secret), time.Now().Unix()).Scan(&id, &teamID)
	if err == sql.ErrNoRows {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	// By the id this lookup just returned, so it needs no predicate of its own.
	_, err = s.db.ExecContext(ctx, "UPDATE log_keys SET last_used_at=? WHERE id=? AND (last_used_at IS NULL OR last_used_at<?)", time.Now().Unix(), id, time.Now().Add(-time.Minute).Unix())
	return teamID, err
}
