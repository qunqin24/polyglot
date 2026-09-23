package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// DefaultTeamID is the team every deployment has. There is always exactly one
// team; a single-operator gateway simply never sees it, because there is
// nothing to tell it apart from. Migration 0020 seeds it and folds every row
// that existed before into it.
const DefaultTeamID = 1

// Team owns providers, credentials, models, keys, spend and logs. It is not an
// organisation, a user, or a role: the only question it answers is which of
// those resources belong together.
type Team struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Scope binds one team. Every method that reads or writes team-owned data hangs
// off this handle rather than off *Store, so reaching that data without naming
// a team is a compile error instead of a review comment.
//
// A row belonging to another team is not visible through this handle, and
// asking for one by id returns ErrNotFound — never a permission error. A 403
// confirms that the resource exists, which is the one thing a tenant boundary
// must not do; "no such row" is both true from where the caller stands and
// indistinguishable from a row that was never there.
type Scope struct {
	s      *Store
	teamID int64
}

// ForTeam returns the handle through which this team's data is reached. The
// team id is not validated here: a handle for a team that does not exist simply
// finds nothing, which is the same answer as a team that owns nothing.
func (s *Store) ForTeam(id int64) *Scope { return &Scope{s: s, teamID: id} }

// owned names a table that carries a team_id column of its own. alias is the
// name this package's existing column lists already use for it — "p" for
// providers, empty where the statements are unqualified — so a constructor's
// FROM clause and a caller's fragment agree without either restating the other.
//
// Only ownership roots appear here. models, model_aliases and api_key_models
// have no team_id: they reach their team through their parent, which is what
// queryViaProvider is for.
type owned struct {
	table string
	alias string
}

var (
	ownedProviders   = owned{table: "providers", alias: "p"}
	ownedAPIKeys     = owned{table: "api_keys"}
	ownedRequestLogs = owned{table: "request_logs"}
	ownedLogKeys     = owned{table: "log_keys"}
)

// from renders the table for a SELECT's FROM clause.
func (o owned) from() string {
	if o.alias == "" {
		return o.table
	}
	return o.table + " " + o.alias
}

// ref is how a fragment refers to this table's columns: the alias where there
// is one, the table name otherwise.
func (o owned) ref() string {
	if o.alias == "" {
		return o.table
	}
	return o.alias
}

// The constructors below all emit the WHERE clause themselves, with the team
// predicate as its first term, and then append the caller's fragment. So rest
// is everything that follows that predicate: it opens with a dangling AND when
// it narrows the result, or goes straight to ORDER BY / GROUP BY / LIMIT when
// it does not.
//
// The dangling AND is deliberate and it is the point of the whole arrangement.
// It says on sight that the clause is already open, and it means a statement
// written the ordinary way — with its own WHERE — comes out as
// "WHERE team_id = ? WHERE …", which SQLite rejects. A forgotten predicate is a
// syntax error, not a leak. No constructor here can be handed a whole statement,
// so there is nowhere for one to be written without the predicate.
//
// The team id binds at the position the predicate occupies in the statement,
// which is after any placeholder appearing before the WHERE: a SELECT's column
// list may carry its own (stats.go builds its latency histogram buckets that
// way) and an UPDATE's assignments always do. Counting them is what lets a
// caller pass its arguments in plain statement order and never mention the team
// at all.

// query runs a SELECT over an owned table.
func (t *Scope) query(ctx context.Context, cols string, o owned, rest string, args ...any) (*sql.Rows, error) {
	return t.s.db.QueryContext(ctx, selectFrom(cols, o, rest), t.bind(placeholders(cols), args)...)
}

// queryRow is query for a statement that returns at most one row.
func (t *Scope) queryRow(ctx context.Context, cols string, o owned, rest string, args ...any) *sql.Row {
	return t.s.db.QueryRowContext(ctx, selectFrom(cols, o, rest), t.bind(placeholders(cols), args)...)
}

// exec writes to an owned table. verb is the statement head with the table left
// out, because the registry supplies it: either "DELETE", or "UPDATE SET col =
// ?, …" — an UPDATE's assignments have to sit between the table and the WHERE,
// so they travel with the verb rather than in rest.
//
// An unrecognised verb is an error rather than a fallback. There is no shape
// this can take that emits a statement without the predicate.
func (t *Scope) exec(ctx context.Context, verb string, o owned, rest string, args ...any) (sql.Result, error) {
	head, err := writeHead(verb, o)
	if err != nil {
		return nil, err
	}
	// A single-table write carries no alias, so the predicate qualifies the
	// column with the table itself rather than with o.ref().
	q := head + " WHERE " + o.table + ".team_id = ? " + rest
	return t.s.db.ExecContext(ctx, q, t.bind(placeholders(verb), args)...)
}

// queryViaProvider runs a SELECT over a child table that has no team_id of its
// own — models, model_aliases — and finds the team through the provider that
// owns the row. table and alias are the child's; the provider is always joined
// as "p", which is the alias those column lists already use.
//
// The join lives in the constructor so that it cannot be written as a LEFT
// JOIN, which would let a row with no surviving provider through the predicate
// and into another team's result.
func (t *Scope) queryViaProvider(ctx context.Context, cols, table, alias, rest string, args ...any) (*sql.Rows, error) {
	return t.s.db.QueryContext(ctx, selectVia(cols, table, alias, rest), t.bind(placeholders(cols), args)...)
}

// queryRowViaProvider is queryViaProvider for a statement that returns at most
// one row.
func (t *Scope) queryRowViaProvider(ctx context.Context, cols, table, alias, rest string, args ...any) *sql.Row {
	return t.s.db.QueryRowContext(ctx, selectVia(cols, table, alias, rest), t.bind(placeholders(cols), args)...)
}

func selectFrom(cols string, o owned, rest string) string {
	return "SELECT " + cols + " FROM " + o.from() + " WHERE " + o.ref() + ".team_id = ? " + rest
}

func selectVia(cols, table, alias, rest string) string {
	return "SELECT " + cols + " FROM " + table + " " + alias +
		" JOIN " + ownedProviders.table + " " + ownedProviders.alias +
		" ON " + ownedProviders.alias + ".id = " + alias + ".provider_id" +
		" WHERE " + ownedProviders.alias + ".team_id = ? " + rest
}

func writeHead(verb string, o owned) (string, error) {
	switch {
	case verb == "DELETE":
		return "DELETE FROM " + o.table, nil
	case strings.HasPrefix(verb, "UPDATE SET "):
		return "UPDATE " + o.table + " " + strings.TrimPrefix(verb, "UPDATE "), nil
	}
	return "", fmt.Errorf("store: %s: unsupported write verb %q", o.table, verb)
}

// placeholders counts the parameters a fragment binds. Assignments and computed
// column lists are the only fragments that sit ahead of the WHERE clause.
func placeholders(fragment string) int { return strings.Count(fragment, "?") }

// bind inserts the team id at the position the predicate occupies, after the
// before placeholders that precede it in the statement.
func (t *Scope) bind(before int, args []any) []any {
	if before > len(args) {
		// The caller supplied fewer arguments than its own fragment binds, so
		// the statement is short one parameter whatever this does. Put the team
		// id last and let the driver report the count mismatch.
		before = len(args)
	}
	out := make([]any, 0, len(args)+1)
	out = append(out, args[:before]...)
	out = append(out, t.teamID)
	return append(out, args[before:]...)
}
