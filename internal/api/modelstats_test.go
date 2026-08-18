package api

import (
	"context"
	"testing"
	"time"

	"github.com/qunqin24/polyglot/internal/store"
)

// plant writes a log row directly, which is how these tests get a controlled
// distribution without making hundreds of real requests.
func plant(t *testing.T, st *store.Store, l *store.RequestLog) {
	t.Helper()
	now := time.Now()
	if l.StartedAt.IsZero() {
		l.StartedAt, l.FinishedAt = now, now
	}
	if l.Status == "" {
		l.Status = "success"
	}
	if err := st.InsertRequestLogs(context.Background(), []*store.RequestLog{l}); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func statFor(t *testing.T, stats []store.ModelStat, provider, model string) store.ModelStat {
	t.Helper()
	for _, s := range stats {
		if s.ProviderName == provider && s.UpstreamModel == model {
			return s
		}
	}
	t.Fatalf("no stats for %s/%s in %+v", provider, model, stats)
	return store.ModelStat{}
}

// The same model on two providers must stay two rows: comparing them is the
// question a multi-provider gateway exists to answer.
func TestModelStatsSeparateProviders(t *testing.T) {
	h := newHarness(t, nil, "openai")

	for range 3 {
		plant(t, h.store, &store.RequestLog{ProviderName: "fast", UpstreamModel: "gpt-x",
			TTFTMS: ptr(int64(100)), OutputTPS: ptrF(90)})
	}
	plant(t, h.store, &store.RequestLog{ProviderName: "slow", UpstreamModel: "gpt-x",
		TTFTMS: ptr(int64(900)), OutputTPS: ptrF(10)})

	stats, err := h.store.ModelStats(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	fast := statFor(t, stats, "fast", "gpt-x")
	slow := statFor(t, stats, "slow", "gpt-x")

	if fast.Requests != 3 || slow.Requests != 1 {
		t.Errorf("requests: fast=%d slow=%d", fast.Requests, slow.Requests)
	}
	if *fast.TPSMedian <= *slow.TPSMedian {
		t.Errorf("the faster provider did not come out faster: %v vs %v",
			*fast.TPSMedian, *slow.TPSMedian)
	}
}

// A percentile, not an average: one very slow request must show up.
func TestTTFTUsesTheTailNotTheMean(t *testing.T) {
	h := newHarness(t, nil, "openai")

	// Eighteen fast and two very slow. The mean is 390ms, which reads as a
	// perfectly good model; the p95 is 3000ms, which is what the unlucky one
	// request in ten actually waits.
	for range 18 {
		plant(t, h.store, &store.RequestLog{ProviderName: "p", UpstreamModel: "m",
			TTFTMS: ptr(int64(100))})
	}
	for range 2 {
		plant(t, h.store, &store.RequestLog{ProviderName: "p", UpstreamModel: "m",
			TTFTMS: ptr(int64(3000))})
	}

	stats, err := h.store.ModelStats(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	st := statFor(t, stats, "p", "m")
	if st.TTFTP95MS == nil {
		t.Fatal("no p95 was computed")
	}
	if *st.TTFTP95MS != 3000 {
		t.Errorf("p95 = %dms, want 3000 — a mean would have read 390 and hidden the tail",
			*st.TTFTP95MS)
	}
}

// A median, not a mean: one absurd rate from a one-token reply must not move
// the number.
func TestTPSMedianResistsAnOutlier(t *testing.T) {
	h := newHarness(t, nil, "openai")

	for range 9 {
		plant(t, h.store, &store.RequestLog{ProviderName: "p", UpstreamModel: "m",
			OutputTPS: ptrF(50)})
	}
	plant(t, h.store, &store.RequestLog{ProviderName: "p", UpstreamModel: "m",
		OutputTPS: ptrF(9000)})

	stats, err := h.store.ModelStats(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	st := statFor(t, stats, "p", "m")
	if st.TPSMedian == nil || *st.TPSMedian != 50 {
		t.Errorf("median = %v, want 50 — a mean would read 945", st.TPSMedian)
	}
}

func TestZeroMillisecondTPSIsTreatedAsUnmeasurable(t *testing.T) {
	h := newHarness(t, nil, "openai")
	zero := int64(0)
	valid := int64(100)

	plant(t, h.store, &store.RequestLog{RequestID: "atomic-tool", ProviderName: "p", UpstreamModel: "m",
		GenerationMS: &zero, OutputTPS: ptrF(76000000)})
	plant(t, h.store, &store.RequestLog{RequestID: "real-stream", ProviderName: "p", UpstreamModel: "m",
		GenerationMS: &valid, OutputTPS: ptrF(50)})

	logs, err := h.store.ListRequestLogs(context.Background(), store.LogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	for _, log := range logs {
		if log.RequestID == "atomic-tool" && (log.GenerationMS != nil || log.OutputTPS != nil) {
			t.Errorf("legacy zero-window rate was exposed: generation=%v tps=%v",
				log.GenerationMS, log.OutputTPS)
		}
	}

	stats, err := h.store.ModelStats(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	st := statFor(t, stats, "p", "m")
	if st.Streamed != 1 || st.TPSMedian == nil || *st.TPSMedian != 50 {
		t.Errorf("stats included the zero-window outlier: streamed=%d median=%v", st.Streamed, st.TPSMedian)
	}
}

// A client hanging up is not the model failing.
func TestCancelledRequestsAreNotCountedAsFailures(t *testing.T) {
	h := newHarness(t, nil, "openai")

	plant(t, h.store, &store.RequestLog{ProviderName: "p", UpstreamModel: "m"})
	plant(t, h.store, &store.RequestLog{ProviderName: "p", UpstreamModel: "m",
		Status: "cancelled", ErrorType: "client_disconnect"})
	plant(t, h.store, &store.RequestLog{ProviderName: "p", UpstreamModel: "m",
		Status: "error", ErrorType: "rate_limit"})

	stats, err := h.store.ModelStats(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	st := statFor(t, stats, "p", "m")
	if st.Errors != 1 {
		t.Errorf("errors = %d, want only the rate limit", st.Errors)
	}
	if st.TopError != "rate_limit" {
		t.Errorf("top_error = %q, want the class that actually failed", st.TopError)
	}
}

// Speed is measurable only for streamed replies that reported usage. The
// count has to be reported or a percentile over two samples reads as a fact.
func TestStreamedCountQualifiesTheSpeedNumbers(t *testing.T) {
	h := newHarness(t, nil, "openai")

	for range 10 {
		plant(t, h.store, &store.RequestLog{ProviderName: "p", UpstreamModel: "m"})
	}
	plant(t, h.store, &store.RequestLog{ProviderName: "p", UpstreamModel: "m",
		OutputTPS: ptrF(42), TTFTMS: ptr(int64(120))})

	stats, err := h.store.ModelStats(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	st := statFor(t, stats, "p", "m")
	if st.Requests != 11 {
		t.Errorf("requests = %d", st.Requests)
	}
	if st.Streamed != 1 {
		t.Errorf("streamed = %d, want 1 — the number the median rests on", st.Streamed)
	}
}

// A model nobody has called has no numbers, rather than zeroes that look like
// measurements.
func TestAnUnusedModelHasNoSpeedNumbers(t *testing.T) {
	h := newHarness(t, nil, "openai")

	plant(t, h.store, &store.RequestLog{ProviderName: "p", UpstreamModel: "never-streamed"})

	stats, err := h.store.ModelStats(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	st := statFor(t, stats, "p", "never-streamed")
	if st.TPSMedian != nil || st.TTFTP95MS != nil {
		t.Errorf("unmeasurable speed was reported as a number: tps=%v ttft=%v",
			st.TPSMedian, st.TTFTP95MS)
	}
	if st.SuccessRate != 1 {
		t.Errorf("success rate = %v", st.SuccessRate)
	}
}

func ptr(v int64) *int64      { return &v }
func ptrF(v float64) *float64 { return &v }

// Token totals over the window, and the reason reasoning is reported beside
// them rather than folded in.
func TestTokenTotalsAreSummedPerModel(t *testing.T) {
	h := newHarness(t, nil, "openai")

	plant(t, h.store, &store.RequestLog{ProviderName: "p", UpstreamModel: "m",
		InputTokens: 100, OutputTokens: 30, ReasoningTokens: 20})
	plant(t, h.store, &store.RequestLog{ProviderName: "p", UpstreamModel: "m",
		InputTokens: 250, OutputTokens: 70, ReasoningTokens: 5})
	// A different model on the same provider must not be folded in.
	plant(t, h.store, &store.RequestLog{ProviderName: "p", UpstreamModel: "other",
		InputTokens: 9999, OutputTokens: 9999})

	stats, err := h.store.ModelStats(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	st := statFor(t, stats, "p", "m")

	if st.InputTokens != 350 {
		t.Errorf("input = %d, want 350", st.InputTokens)
	}
	if st.OutputTokens != 100 {
		t.Errorf("output = %d, want 100", st.OutputTokens)
	}
	// Reported as its own number. Providers disagree about whether reasoning
	// is part of the output count, so anything that added the two together
	// would double count for some of them.
	if st.ReasoningTokens != 25 {
		t.Errorf("reasoning = %d, want 25 reported separately", st.ReasoningTokens)
	}
	if st.OutputTokens != 100 {
		t.Errorf("reasoning was folded into output: %d", st.OutputTokens)
	}
}

// A model called only with upstreams that reported no usage shows zero rather
// than a wrong guess.
func TestTokenTotalsAreZeroWhenNoUsageWasReported(t *testing.T) {
	h := newHarness(t, nil, "openai")

	plant(t, h.store, &store.RequestLog{ProviderName: "p", UpstreamModel: "quiet"})

	stats, err := h.store.ModelStats(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	st := statFor(t, stats, "p", "quiet")
	if st.InputTokens != 0 || st.OutputTokens != 0 {
		t.Errorf("tokens = %d/%d, want zero", st.InputTokens, st.OutputTokens)
	}
	if st.Requests != 1 {
		t.Errorf("the request itself was still counted: %d", st.Requests)
	}
}
