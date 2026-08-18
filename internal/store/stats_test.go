package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func statsStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "polyglot.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func logAt(ago time.Duration, mutate func(*RequestLog)) *RequestLog {
	now := time.Now()
	l := &RequestLog{
		RequestID:        "req",
		StartedAt:        now.Add(-ago),
		FinishedAt:       now.Add(-ago).Add(time.Second),
		LatencyMS:        500,
		Status:           "success",
		StatusCode:       200,
		ClientProtocol:   "openai",
		UpstreamProtocol: "openai",
		ProviderName:     "OpenRouter",
		UpstreamModel:    "gpt-5.2",
		InputTokens:      100,
		OutputTokens:     20,
	}
	if mutate != nil {
		mutate(l)
	}
	return l
}

func write(t *testing.T, st *Store, logs ...*RequestLog) {
	t.Helper()
	if err := st.InsertRequestLogs(context.Background(), logs); err != nil {
		t.Fatalf("insert logs: %v", err)
	}
}

// The timeline is drawn as a line, so a bucket nobody made a request in has to
// arrive as a zero. Omitting it would draw a straight line across the gap and
// claim traffic that never happened.
func TestSeriesFillsQuietBuckets(t *testing.T) {
	st := statsStore(t)
	write(t, st,
		logAt(30*time.Minute, nil),
		logAt(90*time.Minute, nil),
	)

	s, err := st.Stats(context.Background(), time.Now().Add(-6*time.Hour))
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if s.BucketSeconds != 900 {
		t.Fatalf("bucket seconds = %d, want 900 for a 6 hour window", s.BucketSeconds)
	}
	if len(s.Series) < 20 {
		t.Fatalf("series has %d points, want the whole window filled", len(s.Series))
	}
	var withTraffic, empty int
	for _, b := range s.Series {
		if b.Count > 0 {
			withTraffic++
		} else {
			empty++
		}
	}
	if withTraffic != 2 || empty == 0 {
		t.Fatalf("series has %d busy and %d quiet buckets, want 2 busy and the rest present as zeroes",
			withTraffic, empty)
	}
	// The buckets have to be in order and evenly spaced, or the x axis lies.
	for i := 1; i < len(s.Series); i++ {
		if gap := s.Series[i].Start - s.Series[i-1].Start; gap != s.BucketSeconds {
			t.Fatalf("gap between point %d and %d is %ds, want %ds", i-1, i, gap, s.BucketSeconds)
		}
	}
}

// A request nobody could price is missing from the total on purpose. The count
// of those is what stops the total reading as a complete bill.
func TestCostStatsKeepUnpricedOutOfTheTotal(t *testing.T) {
	st := statsStore(t)
	priced := 0.25
	write(t, st,
		logAt(time.Minute, func(l *RequestLog) { l.CostUSD = &priced }),
		logAt(2*time.Minute, nil), // no price at all
	)

	cs, err := st.CostStats(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("cost stats: %v", err)
	}
	if cs.CostUSD != priced {
		t.Errorf("cost = %v, want %v — an unpriced request must add nothing", cs.CostUSD, priced)
	}
	if cs.UnpricedRequests != 1 {
		t.Errorf("unpriced = %d, want 1", cs.UnpricedRequests)
	}
	var stacked float64
	for _, s := range cs.Stacks {
		for _, p := range s.Points {
			stacked += p
		}
	}
	if stacked != priced {
		t.Errorf("stacked bands sum to %v, want %v — the bands must add up to the total", stacked, priced)
	}
}

// Every band keeps its model for the whole window, and whatever did not earn a
// band is folded into one remainder rather than dropped, so the stack still
// adds up to what was actually spent.
func TestCostStacksFoldTheRemainderInsteadOfDroppingIt(t *testing.T) {
	st := statsStore(t)
	var logs []*RequestLog
	var total float64
	for i := range 9 {
		cost := float64(10-i) / 100
		total += cost
		c := cost
		logs = append(logs, logAt(time.Duration(i+1)*time.Minute, func(l *RequestLog) {
			l.UpstreamModel = "model-" + string(rune('a'+i))
			l.CostUSD = &c
		}))
	}
	write(t, st, logs...)

	cs, err := st.CostStats(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("cost stats: %v", err)
	}
	if len(cs.Stacks) != costStackLimit+1 {
		t.Fatalf("got %d bands, want %d models plus one remainder", len(cs.Stacks), costStackLimit)
	}
	if cs.Stacks[len(cs.Stacks)-1].Model != "" {
		t.Error("the last band must be the unnamed remainder")
	}
	var stacked float64
	for _, s := range cs.Stacks {
		for _, p := range s.Points {
			stacked += p
		}
	}
	if diff := stacked - total; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("bands sum to %v, want %v", stacked, total)
	}
}

// The conversion matrix is the one question only this gateway can be asked.
// The fidelity counts beside it are read out of the notes column, which is
// JSON, and which is empty on most rows.
func TestConversionStatsCountPairsAndFidelityNotes(t *testing.T) {
	st := statsStore(t)
	write(t, st,
		logAt(time.Minute, nil), // openai -> openai, no notes
		logAt(2*time.Minute, func(l *RequestLog) {
			l.ClientProtocol = "anthropic"
			l.UpstreamProtocol = "openai"
			l.FidelityNotes = `[{"stage":"request","field":"cache_control","fidelity":"unsupported","detail":"x"}]`
		}),
		logAt(3*time.Minute, func(l *RequestLog) {
			l.ClientProtocol = "anthropic"
			l.UpstreamProtocol = "openai"
			l.FidelityNotes = `[{"stage":"request","field":"cache_control","fidelity":"unsupported","detail":"x"},` +
				`{"stage":"request","field":"top_k","fidelity":"lossy","detail":"y"}]`
		}),
	)

	cs, err := st.ConversionStats(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("conversion stats: %v", err)
	}
	if cs.TotalRequests != 3 {
		t.Fatalf("total = %d, want 3", cs.TotalRequests)
	}
	if cs.ConvertedRequests != 2 {
		t.Errorf("converted = %d, want 2 — same protocol in and out is not a conversion", cs.ConvertedRequests)
	}
	if cs.LossyRequests != 2 {
		t.Errorf("lossy = %d, want 2 requests, not 3 notes", cs.LossyRequests)
	}
	var same, crossed int64
	for _, p := range cs.Pairs {
		if p.ClientProtocol == p.UpstreamProtocol {
			same += p.Count
		} else {
			crossed += p.Count
		}
	}
	if same != 1 || crossed != 2 {
		t.Errorf("matrix has %d same-protocol and %d converted, want 1 and 2", same, crossed)
	}
	byField := map[string]int64{}
	for _, f := range cs.Fields {
		byField[f.Field] = f.Count
	}
	if byField["cache_control"] != 2 {
		t.Errorf("cache_control counted %d times, want 2", byField["cache_control"])
	}
	if byField["top_k"] != 1 {
		t.Errorf("top_k counted %d times, want 1", byField["top_k"])
	}
}

// An average hides the tail, which is the part anyone opening this panel is
// looking for.
func TestLatencyStatsReportPercentilesAndTheWholeSpread(t *testing.T) {
	st := statsStore(t)
	var logs []*RequestLog
	for i := range 100 {
		ms := int64(50 + i*10) // 50ms .. 1040ms
		if i == 99 {
			ms = 60000 // one very slow request, which must land in the last bar
		}
		// Keep the distribution in one timeline bucket. Spreading these samples
		// over 100 seconds made the test depend on how close wall time was to the
		// next five-minute boundary.
		logs = append(logs, logAt(time.Minute, func(l *RequestLog) {
			l.LatencyMS = ms
		}))
	}
	write(t, st, logs...)

	ls, err := st.LatencyStats(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("latency stats: %v", err)
	}
	var busy *LatencyPoint
	for i := range ls.Series {
		if ls.Series[i].Count > 0 {
			busy = &ls.Series[i]
		}
	}
	if busy == nil {
		t.Fatal("no bucket carries the requests")
	}
	if !(busy.P50 < busy.P95 && busy.P95 < busy.P99) {
		t.Errorf("percentiles are %d/%d/%d, want p50 < p95 < p99", busy.P50, busy.P95, busy.P99)
	}
	if len(ls.Histogram) != len(latencyEdges)+1 {
		t.Fatalf("histogram has %d bars, want %d edges plus an overflow bar",
			len(ls.Histogram), len(latencyEdges))
	}
	last := ls.Histogram[len(ls.Histogram)-1]
	if last.UpperMS != 0 || last.Count != 1 {
		t.Errorf("overflow bar is %+v, want the one 60s request and no upper bound", last)
	}
	var total int64
	for _, b := range ls.Histogram {
		total += b.Count
	}
	if total != 100 {
		t.Errorf("histogram covers %d requests, want all 100", total)
	}
}
