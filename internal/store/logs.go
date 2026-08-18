package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// RequestLog is one completed gateway request. Polyglot writes exactly one of
// these per request, never one per streamed chunk.
type RequestLog struct {
	ID         int64     `json:"id"`
	RequestID  string    `json:"request_id"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	LatencyMS  int64     `json:"latency_ms"`
	// TTFTMS is the time to the first content-bearing chunk of a streamed
	// reply, measured from when Polyglot received the request. It is null for
	// a buffered reply and for a stream that produced no content.
	TTFTMS *int64 `json:"ttft_ms"`
	// GenerationMS spans the first content chunk to the last; OutputTPS is the
	// upstream's output token count over that span. Both are null when they
	// could not be measured rather than guessed at.
	GenerationMS     *int64   `json:"generation_ms"`
	OutputTPS        *float64 `json:"output_tps"`
	Status           string   `json:"status"` // success | error | cancelled
	StatusCode       int      `json:"status_code"`
	ClientProtocol   string   `json:"client_protocol"`
	UpstreamProtocol string   `json:"upstream_protocol"`
	ProviderID       *int64   `json:"provider_id"`
	ProviderName     string   `json:"provider_name"`
	ModelAlias       string   `json:"model_alias"`
	UpstreamModel    string   `json:"upstream_model"`
	APIKeyID         *int64   `json:"api_key_id"`
	APIKeyName       string   `json:"api_key_name"`
	// ClientIP is the address the request came from. It is the peer address
	// unless TRUST_PROXY_HEADERS says a proxy in front may set it.
	ClientIP string `json:"client_ip"`
	// ClientApp names what made the call — an app title, a referring site, or
	// the client software. It answers "what was this for", which no other
	// column can.
	ClientApp string `json:"client_app"`
	// RequestUser is the end-user identifier the client sent, if any.
	RequestUser string `json:"request_user"`
	// RequestMetadata is the client's own JSON-encoded labels, verbatim.
	RequestMetadata string `json:"request_metadata"`
	Stream          bool   `json:"stream"`
	InputTokens     int    `json:"input_tokens"`
	OutputTokens    int    `json:"output_tokens"`
	// CachedInputTokens and CacheWriteTokens are parts of InputTokens, not
	// additions to it, so a hit rate is one over the other. Rows written
	// before these columns existed hold 0.
	CachedInputTokens int `json:"cached_input_tokens"`
	CacheWriteTokens  int `json:"cache_write_tokens"`
	ReasoningTokens   int `json:"reasoning_tokens"`
	// RetryCount is how many upstream attempts followed the first;
	// FallbackCount is how many of those moved to a different provider.
	RetryCount    int    `json:"retry_count"`
	FallbackCount int    `json:"fallback_count"`
	ErrorType     string `json:"error_type"`
	ErrorMessage  string `json:"error_message"`
	// FidelityNotes is the JSON-encoded list of conversion notes. It records
	// what the protocol translation could not represent exactly.
	FidelityNotes string `json:"fidelity_notes"`
	// CostUSD is what this request was worth at the price in force when it
	// finished, snapshotted so changing a price tomorrow does not rewrite what
	// yesterday reportedly cost. Null is an unknown cost — no price was known
	// — and must never be rendered as zero, which would claim it was free.
	CostUSD *float64 `json:"cost_usd"`
	// CostSource says who set that price: models.dev or the operator.
	CostSource string `json:"cost_source"`
	// CostNote records what the number rests on when it is not exact, e.g. a
	// missing cache price that the plain input price stood in for.
	CostNote string `json:"cost_note"`
}

const logCols = `id, request_id, started_at, finished_at, latency_ms, ttft_ms, generation_ms, output_tps,
	status, status_code,
	client_protocol, upstream_protocol, provider_id, provider_name, model_alias, upstream_model,
	api_key_id, api_key_name, client_ip, client_app, request_user, request_metadata,
	stream, input_tokens, output_tokens, cached_input_tokens, cache_write_tokens, reasoning_tokens,
	retry_count, fallback_count, error_type, error_message, fidelity_notes,
	cost_usd, cost_source, cost_note`

func scanLog(sc interface{ Scan(...any) error }) (*RequestLog, error) {
	var (
		l          RequestLog
		started    int64
		finished   int64
		ttft       sql.NullInt64
		generation sql.NullInt64
		tps        sql.NullFloat64
		providerID sql.NullInt64
		apiKeyID   sql.NullInt64
		stream     int
		cost       sql.NullFloat64
	)
	err := sc.Scan(&l.ID, &l.RequestID, &started, &finished, &l.LatencyMS, &ttft, &generation, &tps,
		&l.Status, &l.StatusCode,
		&l.ClientProtocol, &l.UpstreamProtocol, &providerID, &l.ProviderName, &l.ModelAlias, &l.UpstreamModel,
		&apiKeyID, &l.APIKeyName, &l.ClientIP, &l.ClientApp, &l.RequestUser, &l.RequestMetadata,
		&stream, &l.InputTokens, &l.OutputTokens, &l.CachedInputTokens, &l.CacheWriteTokens, &l.ReasoningTokens,
		&l.RetryCount, &l.FallbackCount, &l.ErrorType, &l.ErrorMessage, &l.FidelityNotes,
		&cost, &l.CostSource, &l.CostNote)
	if err != nil {
		return nil, err
	}
	l.CostUSD = floatPtr(cost)
	l.StartedAt = time.UnixMilli(started)
	l.FinishedAt = time.UnixMilli(finished)
	l.Stream = stream != 0
	if ttft.Valid {
		v := ttft.Int64
		l.TTFTMS = &v
	}
	// Older versions could persist generation_ms=0 with a huge TPS when an
	// atomic tool call was expanded into two events a few microseconds apart.
	// That interval is below the precision of the stored duration and is not a
	// measurable token rate, so expose both fields as absent for old rows too.
	if generation.Valid && generation.Int64 > 0 {
		v := generation.Int64
		l.GenerationMS = &v
	}
	if tps.Valid && (!generation.Valid || generation.Int64 > 0) {
		v := tps.Float64
		l.OutputTPS = &v
	}
	if providerID.Valid {
		v := providerID.Int64
		l.ProviderID = &v
	}
	if apiKeyID.Valid {
		v := apiKeyID.Int64
		l.APIKeyID = &v
	}
	return &l, nil
}

// InsertRequestLogs writes a batch in one transaction. The gateway buffers
// records in memory and flushes them here.
func (s *Store) InsertRequestLogs(ctx context.Context, logs []*RequestLog) error {
	if len(logs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin log batch: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO request_logs (
		request_id, started_at, finished_at, latency_ms, ttft_ms, generation_ms, output_tps,
		status, status_code,
		client_protocol, upstream_protocol, provider_id, provider_name, model_alias, upstream_model,
		api_key_id, api_key_name, client_ip, client_app, request_user, request_metadata,
		stream, input_tokens, output_tokens, cached_input_tokens, cache_write_tokens, reasoning_tokens,
		retry_count, fallback_count, error_type, error_message, fidelity_notes,
		cost_usd, cost_source, cost_note
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare log insert: %w", err)
	}
	defer stmt.Close()

	for _, l := range logs {
		_, err := stmt.ExecContext(ctx,
			l.RequestID, l.StartedAt.UnixMilli(), l.FinishedAt.UnixMilli(), l.LatencyMS,
			nullInt64(l.TTFTMS), nullInt64(l.GenerationMS), nullFloat64(l.OutputTPS),
			l.Status, l.StatusCode, l.ClientProtocol, l.UpstreamProtocol,
			nullInt64(l.ProviderID), l.ProviderName, l.ModelAlias, l.UpstreamModel,
			nullInt64(l.APIKeyID), l.APIKeyName, l.ClientIP,
			truncate(l.ClientApp, 200), truncate(l.RequestUser, 200), truncate(l.RequestMetadata, 1000),
			boolInt(l.Stream),
			l.InputTokens, l.OutputTokens, l.CachedInputTokens, l.CacheWriteTokens, l.ReasoningTokens,
			l.RetryCount, l.FallbackCount,
			l.ErrorType, truncate(l.ErrorMessage, 2000), l.FidelityNotes,
			nullFloat64(l.CostUSD), l.CostSource, l.CostNote)
		if err != nil {
			return fmt.Errorf("insert request log: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit log batch: %w", err)
	}
	return nil
}

type LogFilter struct {
	Limit      int
	Offset     int
	Before     int64 // id cursor; 0 means newest
	Status     string
	ProviderID int64
	Model      string
	Protocol   string
	ClientIP   string
	ClientApp  string
}

func logFilterWhere(f LogFilter) (string, []any) {
	var where []string
	var args []any
	if f.Before > 0 {
		where = append(where, "id < ?")
		args = append(args, f.Before)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.ProviderID > 0 {
		where = append(where, "provider_id = ?")
		args = append(args, f.ProviderID)
	}
	if f.Model != "" {
		where = append(where, "(model_alias LIKE ? OR upstream_model LIKE ?)")
		args = append(args, "%"+f.Model+"%", "%"+f.Model+"%")
	}
	if f.Protocol != "" {
		where = append(where, "(client_protocol = ? OR upstream_protocol = ?)")
		args = append(args, f.Protocol, f.Protocol)
	}
	if f.ClientIP != "" {
		where = append(where, "client_ip = ?")
		args = append(args, f.ClientIP)
	}
	if f.ClientApp != "" {
		where = append(where, "client_app LIKE ?")
		args = append(args, "%"+f.ClientApp+"%")
	}
	if len(where) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

func (s *Store) ListRequestLogs(ctx context.Context, f LogFilter) ([]*RequestLog, error) {
	where, args := logFilterWhere(f)
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	q := `SELECT ` + logCols + ` FROM request_logs` + where + ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list request logs: %w", err)
	}
	defer rows.Close()
	var out []*RequestLog
	for rows.Next() {
		l, err := scanLog(rows)
		if err != nil {
			return nil, fmt.Errorf("list request logs: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// CountRequestLogs returns the number of rows matching the same filters used
// by ListRequestLogs. The admin API uses it to offer direct page navigation.
func (s *Store) CountRequestLogs(ctx context.Context, f LogFilter) (int64, error) {
	where, args := logFilterWhere(f)
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_logs`+where, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count request logs: %w", err)
	}
	return total, nil
}

func (s *Store) GetRequestLog(ctx context.Context, id int64) (*RequestLog, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+logCols+` FROM request_logs WHERE id = ?`, id)
	l, err := scanLog(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get request log %d: %w", id, err)
	}
	return l, nil
}

// PruneRequestLogs deletes rows older than the cutoff and returns the count.
func (s *Store) PruneRequestLogs(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM request_logs WHERE started_at < ?`, before.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("prune request logs: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func nullInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullFloat64(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// KeyOrigin is one address an API key has been used from.
type KeyOrigin struct {
	ClientIP  string    `json:"client_ip"`
	Requests  int64     `json:"requests"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// APIKeyOrigins lists the addresses one key has been called from, busiest
// first. This is the shape the "has this key leaked" question actually takes:
// a key used from one place for months and then from somewhere else is
// obvious here and invisible in a list of individual requests.
func (s *Store) APIKeyOrigins(ctx context.Context, keyID int64, since time.Time, limit int) ([]KeyOrigin, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT client_ip, COUNT(*), MIN(started_at), MAX(started_at)
		 FROM request_logs
		 WHERE api_key_id = ? AND started_at >= ? AND client_ip != ''
		 GROUP BY client_ip
		 ORDER BY COUNT(*) DESC, MAX(started_at) DESC
		 LIMIT ?`, keyID, since.UnixMilli(), limit)
	if err != nil {
		return nil, fmt.Errorf("api key origins: %w", err)
	}
	defer rows.Close()

	out := []KeyOrigin{}
	for rows.Next() {
		var o KeyOrigin
		var first, last int64
		if err := rows.Scan(&o.ClientIP, &o.Requests, &first, &last); err != nil {
			return nil, fmt.Errorf("api key origins: %w", err)
		}
		o.FirstSeen = time.UnixMilli(first)
		o.LastSeen = time.UnixMilli(last)
		out = append(out, o)
	}
	return out, rows.Err()
}

// ModelStat summarises how one model on one provider has behaved.
//
// It is keyed by provider *and* model on purpose: the same upstream id served
// by two providers is two different things to call, and comparing them is the
// question a multi-provider gateway exists to answer.
type ModelStat struct {
	ProviderName  string     `json:"provider_name"`
	UpstreamModel string     `json:"upstream_model"`
	Requests      int64      `json:"requests"`
	Errors        int64      `json:"errors"`
	SuccessRate   float64    `json:"success_rate"`
	LastUsedAt    *time.Time `json:"last_used_at"`

	// TTFTP95MS is the 95th percentile, not the mean: an average hides the
	// tail, and the tail is what a slow model actually feels like.
	TTFTP95MS *int64 `json:"ttft_p95_ms"`
	// TPSMedian is the median rather than the mean, because a very short
	// reply produces a wild rate that would drag an average around.
	TPSMedian *float64 `json:"tps_median"`
	// Streamed counts the requests these two could be measured from at all.
	// Without it a percentile over three samples looks like a fact.
	Streamed int64 `json:"streamed"`

	// TopError is the most common error class, answering the question that
	// immediately follows a success rate below 100%.
	TopError string `json:"top_error,omitempty"`

	// InputTokens and OutputTokens are the totals upstreams reported over the
	// window. ReasoningTokens is kept beside them rather than folded in:
	// providers disagree about whether reasoning is part of the output count
	// or separate from it, so adding them up would double count for some and
	// under-report for others. Reporting the three as they arrived is the only
	// honest option without a per-provider rule.
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningTokens int64 `json:"reasoning_tokens"`

	// CachedInputTokens is the part of InputTokens an upstream served from its
	// prompt cache, so CacheHitRate is one over the other. The rate is null
	// when no request in the window reported any input at all — different from
	// a real zero, which says the cache was available and missed every time.
	CachedInputTokens int64    `json:"cached_input_tokens"`
	CacheWriteTokens  int64    `json:"cache_write_tokens"`
	CacheHitRate      *float64 `json:"cache_hit_rate"`
}

// maxStatRows bounds the scan behind the model statistics. A personal gateway
// will not reach it in a day; a busy one degrades to "the most recent N",
// which is still a fair sample, rather than reading an unbounded table.
const maxStatRows = 50000

// ModelStats aggregates request logs per provider and model.
//
// Percentiles are computed here rather than in SQL: SQLite has no percentile
// function, and the alternatives (window functions per metric) are harder to
// read than one pass over two columns.
func (s *Store) ModelStats(ctx context.Context, since time.Time) ([]ModelStat, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT provider_name, upstream_model, status, error_type, started_at, ttft_ms, generation_ms, output_tps,
		        input_tokens, output_tokens, reasoning_tokens,
		        cached_input_tokens, cache_write_tokens
		 FROM request_logs
		 WHERE started_at >= ? AND provider_name != '' AND upstream_model != ''
		 ORDER BY id DESC LIMIT ?`, since.UnixMilli(), maxStatRows)
	if err != nil {
		return nil, fmt.Errorf("model stats: %w", err)
	}
	defer rows.Close()

	type bucket struct {
		stat    ModelStat
		ttfts   []int64
		tpses   []float64
		errKind map[string]int64
	}
	buckets := map[string]*bucket{}
	var order []string

	for rows.Next() {
		var (
			provider, model, status, errType string
			started                          int64
			ttft, generation                 sql.NullInt64
			tps                              sql.NullFloat64
			inTok, outTok, reasonTok         int64
			cachedTok, cacheWriteTok         int64
		)
		if err := rows.Scan(&provider, &model, &status, &errType, &started, &ttft, &generation, &tps,
			&inTok, &outTok, &reasonTok, &cachedTok, &cacheWriteTok); err != nil {
			return nil, fmt.Errorf("model stats: %w", err)
		}
		key := provider + "\x00" + model
		b, ok := buckets[key]
		if !ok {
			b = &bucket{
				stat:    ModelStat{ProviderName: provider, UpstreamModel: model},
				errKind: map[string]int64{},
			}
			buckets[key] = b
			order = append(order, key)
		}

		b.stat.Requests++
		b.stat.InputTokens += inTok
		b.stat.OutputTokens += outTok
		b.stat.ReasoningTokens += reasonTok
		b.stat.CachedInputTokens += cachedTok
		b.stat.CacheWriteTokens += cacheWriteTok
		// A client that hung up is not the model failing, so it counts as
		// neither a success nor an error.
		if status == "error" {
			b.stat.Errors++
			if errType != "" {
				b.errKind[errType]++
			}
		}
		at := time.UnixMilli(started)
		if b.stat.LastUsedAt == nil || at.After(*b.stat.LastUsedAt) {
			b.stat.LastUsedAt = &at
		}
		if ttft.Valid {
			b.ttfts = append(b.ttfts, ttft.Int64)
		}
		if tps.Valid && tps.Float64 > 0 && (!generation.Valid || generation.Int64 > 0) {
			b.tpses = append(b.tpses, tps.Float64)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]ModelStat, 0, len(order))
	for _, key := range order {
		b := buckets[key]
		st := b.stat
		if st.Requests > 0 {
			st.SuccessRate = float64(st.Requests-st.Errors) / float64(st.Requests)
		}
		st.Streamed = int64(len(b.tpses))
		// The hit rate is over tokens, not over requests: one long cached
		// prompt and one short uncached one is not a 50% hit, and tokens are
		// what the bill is denominated in. A window with no reported input at
		// all leaves it null — the cache cannot be said to have missed when
		// nothing was measured.
		if st.InputTokens > 0 {
			rate := float64(st.CachedInputTokens) / float64(st.InputTokens)
			st.CacheHitRate = &rate
		}
		if len(b.ttfts) > 0 {
			slices.Sort(b.ttfts)
			st.TTFTP95MS = &b.ttfts[percentileIndex(len(b.ttfts), 0.95)]
		}
		if len(b.tpses) > 0 {
			slices.Sort(b.tpses)
			st.TPSMedian = &b.tpses[percentileIndex(len(b.tpses), 0.50)]
		}
		var topCount int64
		for kind, n := range b.errKind {
			if n > topCount || (n == topCount && kind < st.TopError) {
				st.TopError, topCount = kind, n
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// percentileIndex is the nearest-rank index into a sorted slice.
func percentileIndex(n int, p float64) int {
	if n <= 1 {
		return 0
	}
	i := int(math.Ceil(p*float64(n))) - 1
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}
