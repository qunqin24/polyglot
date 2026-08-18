package telemetry

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func metricsOn() *Telemetry {
	return New(Config{Enabled: true, Metrics: true, ServiceName: "polyglot"}, testLogger())
}

// expose renders the exposition the way an operator would read it.
func expose(t *testing.T, tel *Telemetry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	tel.WriteMetrics(rec)
	return rec.Body.String()
}

func TestARequestIsCountedWithItsRouteAndOutcome(t *testing.T) {
	tel := metricsOn()

	r := tel.StartRequest(RequestInfo{ID: "req-1", ClientProtocol: "anthropic"})
	r.Streaming(false)
	a := r.StartAttempt("openrouter", "openai", "gpt-5")
	r.Usage(120, 40, 0)
	a.Succeeded()
	r.Finish("success", 200, "")

	out := expose(t, tel)
	want := `polyglot_requests_total{protocol="anthropic",upstream_protocol="openai",` +
		`provider="openrouter",model="gpt-5",stream="false",status="success",code="200"} 1`
	if !strings.Contains(out, want) {
		t.Errorf("missing request counter\nwant a line: %s\ngot:\n%s", want, out)
	}
	for _, want := range []string{
		`polyglot_input_tokens_total{provider="openrouter",model="gpt-5"} 120`,
		`polyglot_output_tokens_total{provider="openrouter",model="gpt-5"} 40`,
		`polyglot_request_duration_seconds_count{protocol="anthropic",upstream_protocol="openai",` +
			`provider="openrouter",model="gpt-5",stream="false"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in:\n%s", want, out)
		}
	}
}

// A model name that resolved to nothing is whatever a client typed. Letting it
// become a label would let anyone mint unlimited time series with a loop of
// typos, so it must be replaced before it reaches the registry.
func TestAnUnroutedModelNameNeverBecomesALabel(t *testing.T) {
	tel := metricsOn()

	r := tel.StartRequest(RequestInfo{ID: "req-2", ClientProtocol: "openai"})
	r.Streaming(false)
	// No StartAttempt: routing failed.
	r.Finish("error", 404, classNotFound)

	out := expose(t, tel)
	if !strings.Contains(out, `model="unrouted"`) {
		t.Errorf("an unrouted request should be labelled model=%q:\n%s", labelUnrouted, out)
	}
	if !strings.Contains(out, `polyglot_errors_total{protocol="openai",provider="none",reason="not_found"} 1`) {
		t.Errorf("the error class was not counted:\n%s", out)
	}
}

func TestTTFTAndTPSComeFromTokenTimestamps(t *testing.T) {
	tel := metricsOn()

	r := tel.StartRequest(RequestInfo{ID: "req-3", ClientProtocol: "openai"})
	r.Streaming(true)
	a := r.StartAttempt("groq", "openai", "llama-3.3")

	time.Sleep(20 * time.Millisecond) // stands in for the wait before the first token
	r.ContentToken()
	time.Sleep(40 * time.Millisecond) // ... and for the generation itself
	r.ContentToken()

	r.Usage(10, 80, 0)
	a.Succeeded()
	out := r.Finish("success", 200, "")

	if out.TTFTMS == nil {
		t.Fatal("a stream with content tokens must produce a TTFT")
	}
	if *out.TTFTMS < 15 {
		t.Errorf("TTFT = %dms, want at least the 20ms wait before the first token", *out.TTFTMS)
	}
	if out.GenerationMS == nil {
		t.Fatal("two content tokens apart in time must produce a generation duration")
	}
	if *out.GenerationMS < 30 {
		t.Errorf("generation = %dms, want roughly the 40ms between tokens", *out.GenerationMS)
	}
	if out.TPS == nil {
		t.Fatal("output tokens over a measurable generation window must produce a TPS")
	}
	// 80 tokens over ~40ms is a large number; the point is that it divides by
	// the generation window and not by the whole request.
	if *out.TPS < 100 {
		t.Errorf("TPS = %.1f; it looks like it was divided by the whole request duration", *out.TPS)
	}
}

// The alternative definition — output tokens over the whole HTTP request — is
// wrong, because the request also contains DNS, TLS, provider queueing and the
// entire wait for the first token. This pins the difference.
func TestTPSExcludesTheWaitBeforeTheFirstToken(t *testing.T) {
	tel := metricsOn()

	r := tel.StartRequest(RequestInfo{ID: "req-4", ClientProtocol: "openai"})
	r.Streaming(true)
	a := r.StartAttempt("p", "openai", "m")

	time.Sleep(80 * time.Millisecond) // a long, slow start
	r.ContentToken()
	time.Sleep(20 * time.Millisecond) // then a fast generation
	r.ContentToken()

	r.Usage(0, 100, 0)
	a.Succeeded()
	out := r.Finish("success", 200, "")

	if out.TPS == nil {
		t.Fatal("expected a TPS")
	}
	naive := 100 / time.Since(r.start).Seconds()
	if *out.TPS <= naive*1.5 {
		t.Errorf("TPS = %.1f is close to the whole-request figure %.1f; "+
			"it must use the generation window only", *out.TPS, naive)
	}
}

func TestSubMillisecondGenerationIsUnmeasurable(t *testing.T) {
	tel := metricsOn()
	r := tel.StartRequest(RequestInfo{ID: "req-atomic-tool", ClientProtocol: "gemini"})
	r.Streaming(true)
	a := r.StartAttempt("p", "gemini", "m")

	// A Gemini function call arrives as one upstream part and is expanded into
	// start/delta events synchronously. The two clock reads are distinct at
	// nanosecond precision but do not describe token generation over time.
	first := r.start.Add(4 * time.Second)
	r.contentAt(first)
	r.contentAt(first.Add(500 * time.Microsecond))
	r.Usage(14979, 190, 0)
	a.Succeeded()
	out := r.Finish("success", 200, "")

	if out.TTFTMS == nil || *out.TTFTMS != 4000 {
		t.Errorf("TTFT = %v, want the measurable four-second wait", out.TTFTMS)
	}
	if out.GenerationMS != nil || out.TPS != nil {
		t.Errorf("a 500us synthetic event window produced generation=%v tps=%v; both must be absent",
			out.GenerationMS, out.TPS)
	}
}

func TestUnmeasurableRatesAreAbsentRatherThanZero(t *testing.T) {
	tel := metricsOn()

	// A buffered reply: no token timestamps exist at all.
	r := tel.StartRequest(RequestInfo{ID: "req-5", ClientProtocol: "openai"})
	r.Streaming(false)
	a := r.StartAttempt("p", "openai", "m")
	r.Usage(5, 5, 0)
	a.Succeeded()
	out := r.Finish("success", 200, "")

	if out.TTFTMS != nil || out.GenerationMS != nil || out.TPS != nil {
		t.Errorf("a non-streaming reply reported ttft=%v generation=%v tps=%v; "+
			"all three are unmeasurable and must stay nil", out.TTFTMS, out.GenerationMS, out.TPS)
	}

	// A stream whose upstream reported no usage: the timing is known, the
	// token count is not, so there is no honest rate to publish.
	r2 := tel.StartRequest(RequestInfo{ID: "req-6", ClientProtocol: "openai"})
	r2.Streaming(true)
	a2 := r2.StartAttempt("p", "openai", "m")
	r2.ContentToken()
	time.Sleep(5 * time.Millisecond)
	r2.ContentToken()
	a2.Succeeded()
	out2 := r2.Finish("success", 200, "")

	if out2.TTFTMS == nil {
		t.Error("TTFT is measurable without token counts and should be reported")
	}
	if out2.TPS != nil {
		t.Errorf("TPS = %v without an upstream token count; it must be nil", *out2.TPS)
	}
}

func TestRetriesAndFallbacksAreCounted(t *testing.T) {
	tel := metricsOn()

	r := tel.StartRequest(RequestInfo{ID: "req-7", ClientProtocol: "openai"})
	r.Streaming(false)

	first := r.StartAttempt("provider-a", "openai", "m")
	first.Failed(classUpstream5xx)
	r.Retrying(first, classUpstream5xx, true)

	second := r.StartAttempt("provider-b", "openai", "m")
	second.Succeeded()
	out := r.Finish("success", 200, "")

	if out.RetryCount != 1 {
		t.Errorf("retry count = %d, want 1", out.RetryCount)
	}
	if out.FallbackCount != 1 {
		t.Errorf("fallback count = %d, want 1 (the second attempt used another provider)", out.FallbackCount)
	}

	exposition := expose(t, tel)
	for _, want := range []string{
		`polyglot_retries_total{provider="provider-a",reason="upstream_5xx"} 1`,
		`polyglot_fallbacks_total{from_provider="provider-a",reason="upstream_5xx"} 1`,
		// The request is attributed to the provider that actually served it.
		`provider="provider-b"`,
	} {
		if !strings.Contains(exposition, want) {
			t.Errorf("missing %s in:\n%s", want, exposition)
		}
	}
}

func TestInFlightReturnsToZero(t *testing.T) {
	tel := metricsOn()

	var reqs []*Request
	for range 5 {
		r := tel.StartRequest(RequestInfo{ID: "req", ClientProtocol: "openai"})
		r.StartAttempt("p", "openai", "m")
		reqs = append(reqs, r)
	}
	if got := tel.reg.inFlight.Load(); got != 5 {
		t.Fatalf("in flight = %d, want 5", got)
	}

	for _, r := range reqs {
		r.Finish("success", 200, "")
	}
	if got := tel.reg.inFlight.Load(); got != 0 {
		t.Errorf("in flight = %d after every request finished, want 0", got)
	}
	if !strings.Contains(expose(t, tel), "polyglot_requests_in_flight 0") {
		t.Error("the in-flight gauge is missing from the exposition")
	}
}

// Finish is deferred next to explicit error paths, so it can be reached twice.
// A second call must not double-count or drive the gauge negative.
func TestFinishIsIdempotent(t *testing.T) {
	tel := metricsOn()

	r := tel.StartRequest(RequestInfo{ID: "req-8", ClientProtocol: "openai"})
	r.StartAttempt("p", "openai", "m")
	r.Finish("success", 200, "")
	r.Finish("success", 200, "")

	if got := tel.reg.inFlight.Load(); got != 0 {
		t.Errorf("in flight = %d after a double Finish, want 0", got)
	}
	out := expose(t, tel)
	if !strings.Contains(out, `stream="false",status="success",code="200"} 1`) {
		t.Errorf("the request was counted more than once:\n%s", out)
	}
}

func TestCardinalityIsCapped(t *testing.T) {
	tel := New(Config{Enabled: true, Metrics: true, MaxSeries: 8}, testLogger())

	// Far more distinct models than the cap allows.
	for i := range 50 {
		r := tel.StartRequest(RequestInfo{ID: "req", ClientProtocol: "openai"})
		r.StartAttempt("p", "openai", "model-"+strings.Repeat("x", i))
		r.Finish("success", 200, "")
	}

	tel.reg.mu.RLock()
	m := tel.reg.byName["polyglot_requests_total"]
	tel.reg.mu.RUnlock()
	m.mu.Lock()
	n := len(m.series)
	m.mu.Unlock()

	if n > 9 { // the cap, plus the single overflow series
		t.Errorf("the registry grew to %d series with a cap of 8", n)
	}
	out := expose(t, tel)
	if !strings.Contains(out, overflowLabel) {
		t.Errorf("samples past the cap must fold into an overflow series:\n%s", out)
	}
	if !strings.Contains(out, `polyglot_telemetry_dropped_total{kind="series"}`) {
		t.Error("a saturated registry must say so in its own metric")
	}
}

func TestDisabledTelemetryRecordsNothingButStillTracksTheRequest(t *testing.T) {
	tel := New(Config{Enabled: false, Metrics: true}, testLogger())

	if tel.Enabled() || tel.MetricsEnabled() {
		t.Fatal("the master switch did not turn metrics off")
	}

	r := tel.StartRequest(RequestInfo{ID: "req-9", ClientProtocol: "openai"})
	r.Streaming(true)
	a := r.StartAttempt("p", "openai", "m")
	r.ContentToken()
	time.Sleep(2 * time.Millisecond)
	r.ContentToken()
	r.Usage(1, 2, 0)
	a.Succeeded()
	out := r.Finish("success", 200, "")

	// The request log is a different system with its own switch: turning
	// telemetry off must not blank out the log's own fields.
	if out.RequestID != "req-9" {
		t.Errorf("request id = %q, want it to survive with telemetry off", out.RequestID)
	}
	if out.TTFTMS == nil {
		t.Error("TTFT belongs to the request log and must still be measured")
	}
	if body := expose(t, tel); body != "" {
		t.Errorf("disabled telemetry produced an exposition:\n%s", body)
	}
}

func TestScrapeEndpointIsClosedWithoutAToken(t *testing.T) {
	tel := metricsOn()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if tel.ScrapeAuthorized(req) {
		t.Error("/metrics is readable with no METRICS_TOKEN configured")
	}
	req.Header.Set("Authorization", "Bearer anything")
	if tel.ScrapeAuthorized(req) {
		t.Error("any bearer token was accepted when none is configured")
	}

	guarded := New(Config{Enabled: true, Metrics: true, MetricsToken: "s3cret"}, testLogger())
	req2 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if guarded.ScrapeAuthorized(req2) {
		t.Error("an unauthenticated scrape was allowed")
	}
	req2.Header.Set("Authorization", "Bearer wrong")
	if guarded.ScrapeAuthorized(req2) {
		t.Error("a wrong token was accepted")
	}
	req2.Header.Set("Authorization", "Bearer s3cret")
	if !guarded.ScrapeAuthorized(req2) {
		t.Error("the configured token was rejected")
	}
}

// An operator may legitimately put a credential in a collector URL. It must not
// then appear in a log line or in `polyglot config` output.
func TestAnEndpointCredentialIsNeverEchoed(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"http://user:t0ken@collector:4318", "http://redacted@collector:4318"},
		{"https://otlp.example.com", "https://otlp.example.com"},
		{"", ""},
	} {
		got := SafeEndpoint(tc.in)
		if got != tc.want {
			t.Errorf("SafeEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if strings.Contains(got, "t0ken") {
			t.Errorf("SafeEndpoint(%q) leaked the credential: %q", tc.in, got)
		}
	}
}

// A label value is operator-configured, but it still reaches a text format
// where a quote or a newline would forge a second sample.
func TestLabelValuesCannotBreakTheExposition(t *testing.T) {
	tel := metricsOn()

	r := tel.StartRequest(RequestInfo{ID: "req", ClientProtocol: "openai"})
	r.StartAttempt(`ev"il`, "openai", "m\nodel polyglot_fake_metric 99")
	r.Finish("success", 200, "")

	out := expose(t, tel)

	// The property that matters is that the injected text stays inside a
	// quoted label value: it must never become a sample line of its own.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "polyglot_fake_metric") || strings.HasPrefix(line, "odel") {
			t.Errorf("a label value forged a line of exposition:\n%s", line)
		}
	}
	if !strings.Contains(out, `provider="ev\"il"`) {
		t.Errorf("a quote in a label value was not escaped:\n%s", out)
	}
	// Control characters are removed rather than escaped, so a value can never
	// span two lines however the writer changes later.
	if strings.Contains(out, "m\nodel") {
		t.Errorf("a newline survived into a label value:\n%s", out)
	}
}
