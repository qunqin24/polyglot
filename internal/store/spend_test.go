package store

import (
	"context"
	"math"
	"testing"
	"time"
)

func spendPeriodFor(now time.Time, loc *time.Location) SpendPeriod {
	local := now.In(loc)
	from := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -6)
	_, off := local.Zone()
	return SpendPeriod{From: from, To: now, PrevFrom: from.AddDate(0, 0, -7), Bucket: 24 * time.Hour, Offset: off}
}

// The period and the one before it are separate sums, an unpriced request is
// counted as unpriced rather than as free, and the bands still add up to the
// total when the dimension changes.
func TestSpendSplitsByDimensionAndComparesWithThePreviousPeriod(t *testing.T) {
	st := statsStore(t)
	tm := st.ForTeam(DefaultTeamID)
	usd := func(v float64) *float64 { return &v }
	write(t, st,
		logAt(time.Hour, func(l *RequestLog) { l.CostUSD = usd(1.5); l.APIKeyName = "生产接入"; l.CachedInputTokens = 40 }),
		logAt(2*time.Hour, func(l *RequestLog) {
			l.CostUSD = usd(0.5)
			l.APIKeyName = "本地开发"
			l.ProviderName = "DeepSeek"
			l.UpstreamModel = "deepseek-v4-flash"
		}),
		logAt(3*time.Hour, func(l *RequestLog) { l.APIKeyName = "本地开发" }), // unpriced
		logAt(8*24*time.Hour, func(l *RequestLog) { l.CostUSD = usd(4) }), // previous period
	)
	period := spendPeriodFor(time.Now(), time.FixedZone("CST", 8*3600))

	byModel, err := tm.SpendStats(context.Background(), period, SpendByModel)
	if err != nil {
		t.Fatalf("spend by model: %v", err)
	}
	if byModel.CostUSD != 2 || byModel.UnpricedRequests != 1 || byModel.Requests != 3 {
		t.Errorf("totals = %v / %d unpriced / %d requests, want 2 / 1 / 3", byModel.CostUSD, byModel.UnpricedRequests, byModel.Requests)
	}
	if byModel.PrevCostUSD != 4 {
		t.Errorf("previous period = %v, want 4", byModel.PrevCostUSD)
	}
	if len(byModel.Rows) == 0 || byModel.Rows[0].Key != "gpt-5.2" || byModel.Rows[0].ProviderName != "OpenRouter" {
		t.Errorf("first model row = %+v, want gpt-5.2 on OpenRouter", byModel.Rows)
	}
	if len(byModel.Starts) != 7 {
		t.Errorf("%d day buckets, want 7", len(byModel.Starts))
	}
	if len(byModel.Cached) != 1 || byModel.Cached[0].CachedTokens != 40 {
		t.Errorf("cached usage = %+v", byModel.Cached)
	}

	byKey, err := tm.SpendStats(context.Background(), period, SpendByKey)
	if err != nil {
		t.Fatalf("spend by key: %v", err)
	}
	keys := map[string]SpendRow{}
	for _, r := range byKey.Rows {
		keys[r.Key] = r
	}
	if keys["生产接入"].CostUSD != 1.5 || keys["本地开发"].Requests != 2 || keys["本地开发"].Unpriced != 1 {
		t.Errorf("key rows = %+v", byKey.Rows)
	}
	var stacked float64
	for _, s := range byKey.Stacks {
		for _, p := range s.Points {
			stacked += p
		}
	}
	if math.Abs(stacked-2) > 1e-9 {
		t.Errorf("bands sum to %v, want 2", stacked)
	}

	if _, err := tm.SpendStats(context.Background(), period, "nonsense"); err == nil {
		t.Error("an unknown dimension was accepted")
	}
}

// A day bucket starts at local midnight. A request at 23:30 local belongs to
// that day even though in UTC it is already the next one.
func TestSpendDaysStartAtLocalMidnight(t *testing.T) {
	st := statsStore(t)
	tm := st.ForTeam(DefaultTeamID)
	loc := time.FixedZone("UTC-5", -5*3600)
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	late := today.Add(-30 * time.Minute) // 23:30 yesterday, local
	cost := 1.0
	write(t, st, logAt(time.Since(late), func(l *RequestLog) { l.CostUSD = &cost }))

	ss, err := tm.SpendStats(context.Background(), spendPeriodFor(time.Now(), loc), SpendByModel)
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	yesterday := today.AddDate(0, 0, -1).Unix()
	for i, s := range ss.Starts {
		var sum float64
		for _, b := range ss.Stacks {
			sum += b.Points[i]
		}
		if s == yesterday && sum != 1 {
			t.Errorf("yesterday's bucket = %v, want 1", sum)
		}
		if s != yesterday && sum != 0 {
			t.Errorf("bucket %v got %v, want 0", time.Unix(s, 0).In(loc), sum)
		}
	}
}
