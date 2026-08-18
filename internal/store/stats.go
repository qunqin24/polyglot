package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Stats is the Overview spine: the numbers the page shows before anyone asks
// for anything more. The three panel payloads further down are separate calls
// on purpose — the Overview refreshes itself every few seconds, and nobody
// should pay for a chart they are not looking at.
type Stats struct {
	TotalRequests int64 `json:"total_requests"`
	SuccessCount  int64 `json:"success_count"`
	ErrorCount    int64 `json:"error_count"`
	InputTokens   int64 `json:"input_tokens"`
	OutputTokens  int64 `json:"output_tokens"`
	// Parts of InputTokens and OutputTokens, never additions to them. The
	// canonical model defines the prompt as the whole thing, cached portion
	// included, so a share is one over the other.
	CachedInputTokens int64 `json:"cached_input_tokens"`
	CacheWriteTokens  int64 `json:"cache_write_tokens"`
	ReasoningTokens   int64 `json:"reasoning_tokens"`
	AvgLatencyMS      int64 `json:"avg_latency_ms"`
	P95LatencyMS      int64 `json:"p95_latency_ms"`
	// CostUSD is what the priced requests in this window came to, and
	// UnpricedRequests is how many are missing from that sum because nobody
	// knew what they cost. The second number is what keeps the first honest:
	// a total with silent gaps reads as a complete one.
	CostUSD          float64 `json:"cost_usd"`
	UnpricedRequests int64   `json:"unpriced_requests"`
	// ConvertedRequests went out in a protocol other than the one they arrived
	// in, and LossyRequests carry at least one note saying something did not
	// survive that. Both are headline numbers for the conversion panel.
	ConvertedRequests int64          `json:"converted_requests"`
	LossyRequests     int64          `json:"lossy_requests"`
	ByProvider        []ProviderStat `json:"by_provider"`
	// BucketSeconds is the width of one point in Series, chosen from the
	// window so every chart lands between roughly 12 and 56 points whatever
	// range the operator picked.
	BucketSeconds int64    `json:"bucket_seconds"`
	Series        []Bucket `json:"series"`
}

type ProviderStat struct {
	ProviderName string `json:"provider_name"`
	Count        int64  `json:"count"`
	ErrorCount   int64  `json:"error_count"`
}

// Bucket is one point on the Overview timeline. Empty buckets are present with
// zero counts rather than omitted: a gap drawn as a straight line between the
// points either side of it claims traffic that never happened.
type Bucket struct {
	Start        int64   `json:"start"` // unix seconds, truncated to the bucket
	Count        int64   `json:"count"`
	Errors       int64   `json:"errors"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	AvgLatencyMS int64   `json:"avg_latency_ms"`
}

// bucketSeconds picks a point width from the window. The Overview is read at a
// glance, so the count of points matters more than their resolution.
func bucketSeconds(window time.Duration) int64 {
	switch {
	case window <= 2*time.Hour:
		return 120
	case window <= 12*time.Hour:
		return 900
	case window <= 48*time.Hour:
		return 3600
	case window <= 14*24*time.Hour:
		return 6 * 3600
	default:
		return 24 * 3600
	}
}

func (s *Store) Stats(ctx context.Context, since time.Time) (*Stats, error) {
	from := since.UnixMilli()
	bucket := bucketSeconds(time.Since(since))
	// Start the slices empty rather than nil: a nil slice marshals to JSON
	// null, and every client would have to guard against it.
	st := &Stats{
		ByProvider:    []ProviderStat{},
		Series:        []Bucket{},
		BucketSeconds: bucket,
	}

	err := s.db.QueryRowContext(ctx, `SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status != 'success' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cached_input_tokens), 0),
			COALESCE(SUM(cache_write_tokens), 0),
			COALESCE(SUM(reasoning_tokens), 0),
			COALESCE(CAST(AVG(latency_ms) AS INTEGER), 0),
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(CASE WHEN cost_usd IS NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN upstream_protocol != '' AND upstream_protocol != client_protocol
				THEN 1 ELSE 0 END), 0)
		FROM request_logs WHERE started_at >= ?`, from).
		Scan(&st.TotalRequests, &st.SuccessCount, &st.ErrorCount, &st.InputTokens, &st.OutputTokens,
			&st.CachedInputTokens, &st.CacheWriteTokens, &st.ReasoningTokens,
			&st.AvgLatencyMS, &st.CostUSD, &st.UnpricedRequests, &st.ConvertedRequests)
	if err != nil {
		return nil, fmt.Errorf("stats totals: %w", err)
	}

	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_logs
		WHERE started_at >= ? AND fidelity_notes != ''
		  AND EXISTS (SELECT 1 FROM json_each(`+validNotes+`) n
		              WHERE n.value ->> 'fidelity' IN ('lossy', 'unsupported'))`, from).
		Scan(&st.LossyRequests); err != nil {
		return nil, fmt.Errorf("stats lossy: %w", err)
	}

	if st.TotalRequests > 0 {
		offset := st.TotalRequests * 95 / 100
		if offset >= st.TotalRequests {
			offset = st.TotalRequests - 1
		}
		if err := s.db.QueryRowContext(ctx,
			`SELECT latency_ms FROM request_logs WHERE started_at >= ? ORDER BY latency_ms LIMIT 1 OFFSET ?`,
			from, offset).Scan(&st.P95LatencyMS); err != nil && err != sql.ErrNoRows {
			return nil, fmt.Errorf("stats p95: %w", err)
		}
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT provider_name, COUNT(*), SUM(CASE WHEN status != 'success' THEN 1 ELSE 0 END)
		 FROM request_logs WHERE started_at >= ? AND provider_name != ''
		 GROUP BY provider_name ORDER BY COUNT(*) DESC LIMIT 10`, from)
	if err != nil {
		return nil, fmt.Errorf("stats by provider: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p ProviderStat
		if err := rows.Scan(&p.ProviderName, &p.Count, &p.ErrorCount); err != nil {
			return nil, fmt.Errorf("stats by provider: %w", err)
		}
		st.ByProvider = append(st.ByProvider, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	brows, err := s.db.QueryContext(ctx,
		`SELECT (started_at / ?) * ? AS b, COUNT(*),
		        SUM(CASE WHEN status != 'success' THEN 1 ELSE 0 END),
		        COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cost_usd), 0), COALESCE(CAST(AVG(latency_ms) AS INTEGER), 0)
		 FROM request_logs WHERE started_at >= ? GROUP BY b ORDER BY b`,
		bucket*1000, bucket, from)
	if err != nil {
		return nil, fmt.Errorf("stats series: %w", err)
	}
	defer brows.Close()
	found := map[int64]Bucket{}
	for brows.Next() {
		var b Bucket
		if err := brows.Scan(&b.Start, &b.Count, &b.Errors, &b.InputTokens, &b.OutputTokens,
			&b.CostUSD, &b.AvgLatencyMS); err != nil {
			return nil, fmt.Errorf("stats series: %w", err)
		}
		found[b.Start] = b
	}
	if err := brows.Err(); err != nil {
		return nil, err
	}
	st.Series = denseBuckets(since, bucket, found)
	return st, nil
}

// denseBuckets walks every bucket in the window and fills the ones no request
// landed in, so a quiet hour reads as a quiet hour instead of disappearing.
func denseBuckets(since time.Time, bucket int64, found map[int64]Bucket) []Bucket {
	start := (since.Unix() / bucket) * bucket
	end := (time.Now().Unix() / bucket) * bucket
	out := make([]Bucket, 0, (end-start)/bucket+1)
	for t := start; t <= end; t += bucket {
		if b, ok := found[t]; ok {
			out = append(out, b)
			continue
		}
		out = append(out, Bucket{Start: t})
	}
	return out
}

// validNotes guards json_each against a row whose fidelity_notes is empty or,
// for a database old enough, something json_each would refuse to walk.
const validNotes = `CASE WHEN json_valid(request_logs.fidelity_notes)
	THEN request_logs.fidelity_notes ELSE '[]' END`

// ConversionStats answers the question only this gateway can be asked: which
// protocol did the caller speak, which one did the upstream speak, and what
// did not survive the trip between them.
type ConversionStats struct {
	TotalRequests     int64                `json:"total_requests"`
	ConvertedRequests int64                `json:"converted_requests"`
	LossyRequests     int64                `json:"lossy_requests"`
	Pairs             []ConversionPair     `json:"pairs"`
	Flows             []ProtocolFlow       `json:"flows"`
	Fields            []FidelityFieldCount `json:"fields"`
}

// ConversionPair is one cell of the client-protocol by upstream-protocol
// matrix. The diagonal is the same protocol in and out, which still goes
// through canonical and is still worth counting.
type ConversionPair struct {
	ClientProtocol   string `json:"client_protocol"`
	UpstreamProtocol string `json:"upstream_protocol"`
	Count            int64  `json:"count"`
	Errors           int64  `json:"errors"`
}

type ProtocolFlow struct {
	ClientProtocol string `json:"client_protocol"`
	ProviderName   string `json:"provider_name"`
	Count          int64  `json:"count"`
}

// FidelityFieldCount is how often one field was reported as not making it
// across intact. Only the field name and the grade travel — the note's detail
// text stays in the log row, where the operator asked to see it.
type FidelityFieldCount struct {
	Field    string `json:"field"`
	Fidelity string `json:"fidelity"`
	Count    int64  `json:"count"`
}

func (s *Store) ConversionStats(ctx context.Context, since time.Time) (*ConversionStats, error) {
	from := since.UnixMilli()
	cs := &ConversionStats{
		Pairs:  []ConversionPair{},
		Flows:  []ProtocolFlow{},
		Fields: []FidelityFieldCount{},
	}

	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN upstream_protocol != '' AND upstream_protocol != client_protocol
				THEN 1 ELSE 0 END), 0)
		FROM request_logs WHERE started_at >= ?`, from).
		Scan(&cs.TotalRequests, &cs.ConvertedRequests); err != nil {
		return nil, fmt.Errorf("conversion totals: %w", err)
	}

	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_logs
		WHERE started_at >= ? AND fidelity_notes != ''
		  AND EXISTS (SELECT 1 FROM json_each(`+validNotes+`) n
		              WHERE n.value ->> 'fidelity' IN ('lossy', 'unsupported'))`, from).
		Scan(&cs.LossyRequests); err != nil {
		return nil, fmt.Errorf("conversion lossy: %w", err)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT client_protocol, upstream_protocol, COUNT(*),
		        SUM(CASE WHEN status != 'success' THEN 1 ELSE 0 END)
		 FROM request_logs WHERE started_at >= ? AND client_protocol != '' AND upstream_protocol != ''
		 GROUP BY client_protocol, upstream_protocol ORDER BY COUNT(*) DESC LIMIT 40`, from)
	if err != nil {
		return nil, fmt.Errorf("conversion pairs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p ConversionPair
		if err := rows.Scan(&p.ClientProtocol, &p.UpstreamProtocol, &p.Count, &p.Errors); err != nil {
			return nil, fmt.Errorf("conversion pairs: %w", err)
		}
		cs.Pairs = append(cs.Pairs, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	frows, err := s.db.QueryContext(ctx,
		`SELECT client_protocol, provider_name, COUNT(*)
		 FROM request_logs WHERE started_at >= ? AND client_protocol != '' AND provider_name != ''
		 GROUP BY client_protocol, provider_name ORDER BY COUNT(*) DESC LIMIT 24`, from)
	if err != nil {
		return nil, fmt.Errorf("conversion flows: %w", err)
	}
	defer frows.Close()
	for frows.Next() {
		var f ProtocolFlow
		if err := frows.Scan(&f.ClientProtocol, &f.ProviderName, &f.Count); err != nil {
			return nil, fmt.Errorf("conversion flows: %w", err)
		}
		cs.Flows = append(cs.Flows, f)
	}
	if err := frows.Err(); err != nil {
		return nil, err
	}

	// One row per note, not per request: a request that lost two fields is two
	// rows here, which is what "how often was this field a problem" means.
	nrows, err := s.db.QueryContext(ctx,
		`SELECT n.value ->> 'field', n.value ->> 'fidelity', COUNT(*)
		 FROM request_logs, json_each(`+validNotes+`) n
		 WHERE started_at >= ? AND fidelity_notes != ''
		   AND n.value ->> 'field' != ''
		 GROUP BY 1, 2 ORDER BY 3 DESC LIMIT 24`, from)
	if err != nil {
		return nil, fmt.Errorf("conversion fields: %w", err)
	}
	defer nrows.Close()
	for nrows.Next() {
		var f FidelityFieldCount
		if err := nrows.Scan(&f.Field, &f.Fidelity, &f.Count); err != nil {
			return nil, fmt.Errorf("conversion fields: %w", err)
		}
		cs.Fields = append(cs.Fields, f)
	}
	return cs, nrows.Err()
}

// latencyEdges are the upper bounds of the histogram, in milliseconds. They
// double, because latency spreads over orders of magnitude and equal-width
// bars would put every request in the first one.
var latencyEdges = []int64{100, 200, 400, 800, 1600, 3200, 6400, 12800, 25600, 51200}

// LatencyStats is the performance panel: how the spread moved over the window,
// what the whole distribution looks like, and what the failures were.
type LatencyStats struct {
	BucketSeconds int64          `json:"bucket_seconds"`
	Series        []LatencyPoint `json:"series"`
	Histogram     []LatencyBar   `json:"histogram"`
	Errors        []ErrorCount   `json:"errors"`
}

// LatencyPoint carries three percentiles rather than an average. An average
// hides the tail, and the tail is the part anybody is looking for here.
type LatencyPoint struct {
	Start int64 `json:"start"`
	Count int64 `json:"count"`
	P50   int64 `json:"p50"`
	P95   int64 `json:"p95"`
	P99   int64 `json:"p99"`
}

// LatencyBar counts the requests at or under UpperMS and over the bar before
// it. UpperMS is 0 on the last bar, which has no upper bound.
type LatencyBar struct {
	UpperMS int64 `json:"upper_ms"`
	Count   int64 `json:"count"`
}

type ErrorCount struct {
	StatusCode int    `json:"status_code"`
	ErrorType  string `json:"error_type"`
	Count      int64  `json:"count"`
}

func (s *Store) LatencyStats(ctx context.Context, since time.Time) (*LatencyStats, error) {
	from := since.UnixMilli()
	bucket := bucketSeconds(time.Since(since))
	ls := &LatencyStats{
		BucketSeconds: bucket,
		Series:        []LatencyPoint{},
		Histogram:     []LatencyBar{},
		Errors:        []ErrorCount{},
	}

	// PERCENT_RANK gives the first row of each bucket a rank of 0, so the
	// MAX(CASE ...) below always has at least one row to pick from and a
	// percentile is never null where a request exists.
	prows, err := s.db.QueryContext(ctx, `WITH b AS (
			SELECT (started_at / ?) * ? AS start, latency_ms,
			       PERCENT_RANK() OVER (PARTITION BY (started_at / ?) * ? ORDER BY latency_ms) AS pr
			FROM request_logs WHERE started_at >= ?
		)
		SELECT start, COUNT(*),
		       MAX(CASE WHEN pr <= 0.50 THEN latency_ms END),
		       MAX(CASE WHEN pr <= 0.95 THEN latency_ms END),
		       MAX(CASE WHEN pr <= 0.99 THEN latency_ms END)
		FROM b GROUP BY start ORDER BY start`,
		bucket*1000, bucket, bucket*1000, bucket, from)
	if err != nil {
		return nil, fmt.Errorf("latency series: %w", err)
	}
	defer prows.Close()
	found := map[int64]LatencyPoint{}
	for prows.Next() {
		var p LatencyPoint
		if err := prows.Scan(&p.Start, &p.Count, &p.P50, &p.P95, &p.P99); err != nil {
			return nil, fmt.Errorf("latency series: %w", err)
		}
		found[p.Start] = p
	}
	if err := prows.Err(); err != nil {
		return nil, err
	}
	start := (since.Unix() / bucket) * bucket
	end := (time.Now().Unix() / bucket) * bucket
	for t := start; t <= end; t += bucket {
		if p, ok := found[t]; ok {
			ls.Series = append(ls.Series, p)
			continue
		}
		ls.Series = append(ls.Series, LatencyPoint{Start: t})
	}

	// The bucket index is built here rather than in SQL so the edges above
	// stay the single place they are written down.
	expr := "CASE"
	args := []any{}
	for i, e := range latencyEdges {
		expr += fmt.Sprintf(" WHEN latency_ms < ? THEN %d", i)
		args = append(args, e)
	}
	expr += fmt.Sprintf(" ELSE %d END", len(latencyEdges))
	args = append(args, from)
	hrows, err := s.db.QueryContext(ctx,
		`SELECT `+expr+` AS b, COUNT(*) FROM request_logs WHERE started_at >= ? GROUP BY b ORDER BY b`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("latency histogram: %w", err)
	}
	defer hrows.Close()
	counts := make([]int64, len(latencyEdges)+1)
	for hrows.Next() {
		var idx int
		var n int64
		if err := hrows.Scan(&idx, &n); err != nil {
			return nil, fmt.Errorf("latency histogram: %w", err)
		}
		if idx >= 0 && idx < len(counts) {
			counts[idx] = n
		}
	}
	if err := hrows.Err(); err != nil {
		return nil, err
	}
	for i, n := range counts {
		bar := LatencyBar{Count: n}
		if i < len(latencyEdges) {
			bar.UpperMS = latencyEdges[i]
		}
		ls.Histogram = append(ls.Histogram, bar)
	}

	erows, err := s.db.QueryContext(ctx,
		`SELECT status_code, error_type, COUNT(*) FROM request_logs
		 WHERE started_at >= ? AND status != 'success'
		 GROUP BY status_code, error_type ORDER BY COUNT(*) DESC LIMIT 8`, from)
	if err != nil {
		return nil, fmt.Errorf("latency errors: %w", err)
	}
	defer erows.Close()
	for erows.Next() {
		var e ErrorCount
		if err := erows.Scan(&e.StatusCode, &e.ErrorType, &e.Count); err != nil {
			return nil, fmt.Errorf("latency errors: %w", err)
		}
		ls.Errors = append(ls.Errors, e)
	}
	return ls, erows.Err()
}

// costStackLimit is how many models get their own band in the stacked cost
// chart. Everything past it is folded into one "other" band rather than
// dropped, so the bands still add up to the total.
const costStackLimit = 5

// CostStats is the spend panel. Every number here is an estimate from a
// published price list — cost visibility, not a bill — and UnpricedRequests is
// what keeps it honest: a request nobody could price is missing from the
// total, and is never counted as free.
type CostStats struct {
	CostUSD          float64 `json:"cost_usd"`
	UnpricedRequests int64   `json:"unpriced_requests"`

	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	CacheWriteTokens  int64 `json:"cache_write_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	ReasoningTokens   int64 `json:"reasoning_tokens"`

	BucketSeconds int64       `json:"bucket_seconds"`
	Starts        []int64     `json:"starts"`
	Stacks        []CostStack `json:"stacks"`
	Models        []ModelCost `json:"models"`
}

// CostStack is one band of the stacked chart. Points lines up with Starts, one
// value per bucket. An empty Model is the folded remainder.
type CostStack struct {
	ProviderName string    `json:"provider_name"`
	Model        string    `json:"model"`
	Points       []float64 `json:"points"`
}

// ModelCost is one rectangle of the spend map. Keyed by provider and model
// both, because the same model served by two providers costs two different
// amounts.
type ModelCost struct {
	ProviderName string  `json:"provider_name"`
	Model        string  `json:"model"`
	CostUSD      float64 `json:"cost_usd"`
	Requests     int64   `json:"requests"`
	Unpriced     int64   `json:"unpriced"`
}

func (s *Store) CostStats(ctx context.Context, since time.Time) (*CostStats, error) {
	from := since.UnixMilli()
	bucket := bucketSeconds(time.Since(since))
	cs := &CostStats{
		BucketSeconds: bucket,
		Starts:        []int64{},
		Stacks:        []CostStack{},
		Models:        []ModelCost{},
	}

	if err := s.db.QueryRowContext(ctx, `SELECT
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(CASE WHEN cost_usd IS NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(cached_input_tokens), 0),
			COALESCE(SUM(cache_write_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(reasoning_tokens), 0)
		FROM request_logs WHERE started_at >= ?`, from).
		Scan(&cs.CostUSD, &cs.UnpricedRequests, &cs.InputTokens, &cs.CachedInputTokens,
			&cs.CacheWriteTokens, &cs.OutputTokens, &cs.ReasoningTokens); err != nil {
		return nil, fmt.Errorf("cost totals: %w", err)
	}

	mrows, err := s.db.QueryContext(ctx,
		`SELECT provider_name, upstream_model, COALESCE(SUM(cost_usd), 0), COUNT(*),
		        SUM(CASE WHEN cost_usd IS NULL THEN 1 ELSE 0 END)
		 FROM request_logs WHERE started_at >= ? AND upstream_model != ''
		 GROUP BY provider_name, upstream_model
		 ORDER BY COALESCE(SUM(cost_usd), 0) DESC, COUNT(*) DESC LIMIT 16`, from)
	if err != nil {
		return nil, fmt.Errorf("cost by model: %w", err)
	}
	defer mrows.Close()
	for mrows.Next() {
		var m ModelCost
		if err := mrows.Scan(&m.ProviderName, &m.Model, &m.CostUSD, &m.Requests, &m.Unpriced); err != nil {
			return nil, fmt.Errorf("cost by model: %w", err)
		}
		cs.Models = append(cs.Models, m)
	}
	if err := mrows.Err(); err != nil {
		return nil, err
	}

	start := (since.Unix() / bucket) * bucket
	end := (time.Now().Unix() / bucket) * bucket
	index := map[int64]int{}
	for t := start; t <= end; t += bucket {
		index[t] = len(cs.Starts)
		cs.Starts = append(cs.Starts, t)
	}

	// The bands are the models that cost the most over the whole window, so a
	// band keeps its colour across every bucket instead of changing meaning
	// from one point to the next.
	type key struct{ provider, model string }
	band := map[key]int{}
	for i, m := range cs.Models {
		if i >= costStackLimit || m.CostUSD <= 0 {
			break
		}
		band[key{m.ProviderName, m.Model}] = i
		cs.Stacks = append(cs.Stacks, CostStack{
			ProviderName: m.ProviderName,
			Model:        m.Model,
			Points:       make([]float64, len(cs.Starts)),
		})
	}
	other := CostStack{Points: make([]float64, len(cs.Starts))}
	otherUsed := false

	srows, err := s.db.QueryContext(ctx,
		`SELECT (started_at / ?) * ?, provider_name, upstream_model, SUM(cost_usd)
		 FROM request_logs WHERE started_at >= ? AND cost_usd IS NOT NULL
		 GROUP BY 1, 2, 3`, bucket*1000, bucket, from)
	if err != nil {
		return nil, fmt.Errorf("cost series: %w", err)
	}
	defer srows.Close()
	for srows.Next() {
		var t int64
		var provider, model string
		var cost float64
		if err := srows.Scan(&t, &provider, &model, &cost); err != nil {
			return nil, fmt.Errorf("cost series: %w", err)
		}
		i, ok := index[t]
		if !ok {
			continue
		}
		if b, ok := band[key{provider, model}]; ok {
			cs.Stacks[b].Points[i] += cost
			continue
		}
		other.Points[i] += cost
		otherUsed = true
	}
	if err := srows.Err(); err != nil {
		return nil, err
	}
	if otherUsed {
		cs.Stacks = append(cs.Stacks, other)
	}
	return cs, nil
}
