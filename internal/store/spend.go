package store

import (
	"context"
	"fmt"
	"time"
)

// The Overview's spend report: one period, the period before it, and the
// spend split by model, provider or API key. Like CostStats, every number is
// an estimate from a published price list, and a request nobody could price
// is counted in UnpricedRequests rather than as free.

// SpendDimension is what the report splits spend by.
type SpendDimension string

const (
	SpendByModel    SpendDimension = "model"
	SpendByProvider SpendDimension = "provider"
	SpendByKey      SpendDimension = "key"
)

// SpendPeriod is the window a report covers, already aligned by the caller to
// the instance's timezone.
type SpendPeriod struct {
	From time.Time
	To   time.Time
	// PrevFrom starts the period compared against, which ends at From.
	PrevFrom time.Time
	// Bucket is the width of one chart point: an hour or a day.
	Bucket time.Duration
	// Offset is the timezone's offset from UTC, in seconds, so a day bucket
	// starts at local midnight rather than at UTC midnight.
	Offset int
}

type SpendStats struct {
	From          int64 `json:"from"`
	To            int64 `json:"to"`
	PrevFrom      int64 `json:"prev_from"`
	BucketSeconds int64 `json:"bucket_seconds"`

	CostUSD          float64 `json:"cost_usd"`
	Requests         int64   `json:"requests"`
	UnpricedRequests int64   `json:"unpriced_requests"`
	PrevCostUSD      float64 `json:"prev_cost_usd"`
	PrevUnpriced     int64   `json:"prev_unpriced_requests"`

	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	CacheWriteTokens  int64 `json:"cache_write_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	ReasoningTokens   int64 `json:"reasoning_tokens"`

	Dimension SpendDimension `json:"dimension"`
	Starts    []int64        `json:"starts"`
	// Stacks are the five biggest rows over the whole period, in Rows order,
	// then one band for everything else (Key ""), so the bands add up.
	Stacks []SpendStack `json:"stacks"`
	Rows   []SpendRow   `json:"rows"`

	// Cached is the cached input per provider and model, for the caller to
	// price what the cache saved. It is not part of the response.
	Cached []CachedUsage `json:"-"`
}

type SpendRow struct {
	// Key is the model id, provider name or API key name.
	Key string `json:"key"`
	// ProviderName is set for the model dimension, where the same model on
	// two providers is two rows.
	ProviderName string  `json:"provider_name,omitempty"`
	CostUSD      float64 `json:"cost_usd"`
	Requests     int64   `json:"requests"`
	Unpriced     int64   `json:"unpriced"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
}

type SpendStack struct {
	Key          string    `json:"key"`
	ProviderName string    `json:"provider_name,omitempty"`
	Points       []float64 `json:"points"`
}

type CachedUsage struct {
	ProviderID   int64
	Model        string
	CachedTokens int64
}

const spendRowLimit = 20

func (d SpendDimension) columns() (string, error) {
	switch d {
	case SpendByModel:
		return "provider_name, upstream_model", nil
	case SpendByProvider:
		return "'', provider_name", nil
	case SpendByKey:
		return "'', api_key_name", nil
	}
	return "", fmt.Errorf("unknown spend dimension %q", d)
}

// SpendStats builds the report for one period.
func (t *Scope) SpendStats(ctx context.Context, p SpendPeriod, dim SpendDimension) (*SpendStats, error) {
	cols, err := dim.columns()
	if err != nil {
		return nil, err
	}
	from, to, prev := p.From.UnixMilli(), p.To.UnixMilli(), p.PrevFrom.UnixMilli()
	bucket := int64(p.Bucket / time.Second)
	if bucket <= 0 {
		bucket = 86400
	}
	off := int64(p.Offset)
	ss := &SpendStats{
		From: p.From.Unix(), To: p.To.Unix(), PrevFrom: p.PrevFrom.Unix(), BucketSeconds: bucket,
		Dimension: dim, Starts: []int64{}, Stacks: []SpendStack{}, Rows: []SpendRow{}, Cached: []CachedUsage{},
	}

	if err := t.queryRow(ctx, `
			COALESCE(SUM(cost_usd), 0), COUNT(*),
			COALESCE(SUM(CASE WHEN cost_usd IS NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(cached_input_tokens), 0),
			COALESCE(SUM(cache_write_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(reasoning_tokens), 0)`,
		ownedRequestLogs, `AND started_at >= ? AND started_at < ?`, from, to).
		Scan(&ss.CostUSD, &ss.Requests, &ss.UnpricedRequests, &ss.InputTokens, &ss.CachedInputTokens,
			&ss.CacheWriteTokens, &ss.OutputTokens, &ss.ReasoningTokens); err != nil {
		return nil, fmt.Errorf("spend totals: %w", err)
	}
	if err := t.queryRow(ctx, `COALESCE(SUM(cost_usd), 0), COALESCE(SUM(CASE WHEN cost_usd IS NULL THEN 1 ELSE 0 END), 0)`,
		ownedRequestLogs, `AND started_at >= ? AND started_at < ?`, prev, from).
		Scan(&ss.PrevCostUSD, &ss.PrevUnpriced); err != nil {
		return nil, fmt.Errorf("previous spend: %w", err)
	}

	rows, err := t.query(ctx, cols+`, COALESCE(SUM(cost_usd), 0), COUNT(*),
			SUM(CASE WHEN cost_usd IS NULL THEN 1 ELSE 0 END),
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0)`,
		ownedRequestLogs, `AND started_at >= ? AND started_at < ?
		 GROUP BY 1, 2
		 ORDER BY COALESCE(SUM(cost_usd), 0) DESC, COUNT(*) DESC LIMIT ?`, from, to, spendRowLimit)
	if err != nil {
		return nil, fmt.Errorf("spend rows: %w", err)
	}
	for rows.Next() {
		var r SpendRow
		if err := rows.Scan(&r.ProviderName, &r.Key, &r.CostUSD, &r.Requests, &r.Unpriced,
			&r.InputTokens, &r.OutputTokens); err != nil {
			rows.Close()
			return nil, fmt.Errorf("spend rows: %w", err)
		}
		ss.Rows = append(ss.Rows, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Bucket starts at local boundaries: shift into local time, floor, shift
	// back. The same shift is applied in SQL below so the two agree.
	first := ((p.From.Unix()+off)/bucket)*bucket - off
	index := map[int64]int{}
	for s := first; s < p.To.Unix(); s += bucket {
		index[s] = len(ss.Starts)
		ss.Starts = append(ss.Starts, s)
	}

	type key struct{ provider, key string }
	band := map[key]int{}
	for i, r := range ss.Rows {
		if i >= costStackLimit || r.CostUSD <= 0 {
			break
		}
		band[key{r.ProviderName, r.Key}] = i
		ss.Stacks = append(ss.Stacks, SpendStack{Key: r.Key, ProviderName: r.ProviderName, Points: make([]float64, len(ss.Starts))})
	}
	other := SpendStack{Points: make([]float64, len(ss.Starts))}
	otherUsed := false

	srows, err := t.query(ctx, `((started_at / 1000 + ?) / ?) * ? - ?, `+cols+`, SUM(cost_usd)`,
		ownedRequestLogs, `AND started_at >= ? AND started_at < ? AND cost_usd IS NOT NULL
		 GROUP BY 1, 2, 3`, off, bucket, bucket, off, from, to)
	if err != nil {
		return nil, fmt.Errorf("spend series: %w", err)
	}
	for srows.Next() {
		var at int64
		var provider, k string
		var cost float64
		if err := srows.Scan(&at, &provider, &k, &cost); err != nil {
			srows.Close()
			return nil, fmt.Errorf("spend series: %w", err)
		}
		i, ok := index[at]
		if !ok {
			continue
		}
		if b, ok := band[key{provider, k}]; ok {
			ss.Stacks[b].Points[i] += cost
			continue
		}
		other.Points[i] += cost
		otherUsed = true
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return nil, err
	}
	if otherUsed {
		ss.Stacks = append(ss.Stacks, other)
	}

	crows, err := t.query(ctx, `COALESCE(provider_id, 0), upstream_model, SUM(cached_input_tokens)`,
		ownedRequestLogs, `AND started_at >= ? AND started_at < ? AND cached_input_tokens > 0
		 GROUP BY 1, 2`, from, to)
	if err != nil {
		return nil, fmt.Errorf("cached usage: %w", err)
	}
	defer crows.Close()
	for crows.Next() {
		var c CachedUsage
		if err := crows.Scan(&c.ProviderID, &c.Model, &c.CachedTokens); err != nil {
			return nil, fmt.Errorf("cached usage: %w", err)
		}
		ss.Cached = append(ss.Cached, c)
	}
	return ss, crows.Err()
}
