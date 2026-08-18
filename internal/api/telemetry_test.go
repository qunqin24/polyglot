package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qunqin24/polyglot/internal/store"
	"github.com/qunqin24/polyglot/internal/telemetry"
)

// These tests drive telemetry the way it is actually reached: over HTTP,
// through the real gateway, against a real upstream. A metric that is only
// exercised by calling the recorder directly proves nothing about whether the
// pipeline ever calls it.

const scrapeToken = "scrape-me"

func telemetryOn() telemetry.Config {
	return telemetry.Config{Enabled: true, Metrics: true, MetricsToken: scrapeToken}
}

// scrape reads /metrics the way Prometheus would.
func (h *harness) scrape(t *testing.T) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.server.URL+"/metrics", nil)
	if err != nil {
		t.Fatalf("build scrape request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+scrapeToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("scrape status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

// waitForLog returns the most recent request log row once the buffered logger
// has flushed it.
func (h *harness) waitForLog(t *testing.T) *store.RequestLog {
	t.Helper()
	for range 60 {
		time.Sleep(100 * time.Millisecond)
		logs, err := h.store.ListRequestLogs(context.Background(), store.LogFilter{Limit: 1})
		if err != nil {
			t.Fatalf("list logs: %v", err)
		}
		if len(logs) > 0 {
			return logs[0]
		}
	}
	t.Fatal("no request log was written")
	return nil
}

const okChatResponse = `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},` +
	`"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`

func TestMetricsRecordARealRequest(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okChatResponse)
	}, "openai", withTelemetry(telemetryOn()))

	readAll(t, h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, nil))

	out := h.scrape(t)
	for _, want := range []string{
		`polyglot_requests_total{protocol="openai",upstream_protocol="openai",` +
			`provider="fake",model="upstream-model-x",stream="false",status="success",code="200"} 1`,
		`polyglot_input_tokens_total{provider="fake",model="upstream-model-x"} 11`,
		`polyglot_output_tokens_total{provider="fake",model="upstream-model-x"} 7`,
		`polyglot_requests_in_flight 0`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing from /metrics:\n  %s\ngot:\n%s", want, out)
		}
	}
}

func TestStreamingIsUnchangedAndStillMeasured(t *testing.T) {
	// The upstream pauses before the first chunk, then produces three, so TTFT
	// and the generation window are distinguishable.
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		f.Flush()
		time.Sleep(120 * time.Millisecond)
		for _, chunk := range []string{"Hel", "lo ", "there"} {
			fmt.Fprintf(w, "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", chunk)
			f.Flush()
			time.Sleep(40 * time.Millisecond)
		}
		io.WriteString(w, `data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":3,"completion_tokens":9}}`+"\n\n")
		f.Flush()
		io.WriteString(w, "data: [DONE]\n\n")
		f.Flush()
	}, "openai", withTelemetry(telemetryOn()))

	body := readAll(t, h.post("/v1/chat/completions",
		`{"model":"my-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil))

	// The stream itself must be untouched: same frames, same terminator.
	if !strings.Contains(body, "Hel") || !strings.Contains(body, "there") {
		t.Errorf("stream content was altered:\n%s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("the stream terminator is missing:\n%s", body)
	}
	if strings.Count(body, "data: ") < 4 {
		t.Errorf("chunks were coalesced; telemetry must not buffer the stream:\n%s", body)
	}

	log := h.waitForLog(t)
	if log.TTFTMS == nil {
		t.Fatal("a streamed reply produced no TTFT")
	}
	// The upstream waited 120ms before its first chunk, so a TTFT measured
	// from the start of the request cannot be near zero. This is the check
	// that would fail if TTFT were measured from the upstream's headers.
	if *log.TTFTMS < 100 {
		t.Errorf("ttft = %dms, want at least the upstream's 120ms first-chunk delay", *log.TTFTMS)
	}
	if log.GenerationMS == nil {
		t.Fatal("no generation duration for a multi-chunk stream")
	}
	if *log.GenerationMS >= *log.TTFTMS+120 {
		t.Errorf("generation = %dms looks like it includes the wait before the first token",
			*log.GenerationMS)
	}
	if log.OutputTPS == nil {
		t.Fatal("no TPS despite an upstream token count and a measurable window")
	}
	if log.OutputTokens != 9 {
		t.Errorf("output tokens = %d, want the upstream's 9", log.OutputTokens)
	}

	out := h.scrape(t)
	for _, want := range []string{
		`polyglot_ttft_seconds_count{provider="fake",model="upstream-model-x"} 1`,
		`polyglot_output_tokens_per_second_count{provider="fake",model="upstream-model-x"} 1`,
		`stream="true"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in:\n%s", want, out)
		}
	}
}

func TestUpstreamFailuresAreClassified(t *testing.T) {
	cases := []struct {
		name   string
		status int
		reason string
	}{
		{"rate limited", http.StatusTooManyRequests, "rate_limit"},
		{"server error", http.StatusInternalServerError, "upstream_5xx"},
		{"bad request", http.StatusBadRequest, "invalid_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, `{"error":{"message":"upstream said no"}}`)
			}, "openai", withTelemetry(telemetryOn()))

			readAll(t, h.post("/v1/chat/completions",
				`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, nil))

			out := h.scrape(t)
			want := fmt.Sprintf(`polyglot_errors_total{protocol="openai",provider="fake",reason=%q} 1`, tc.reason)
			if !strings.Contains(out, want) {
				t.Errorf("missing %s in:\n%s", want, out)
			}
			if !strings.Contains(out, `status="error"`) {
				t.Errorf("the request was not counted as an error:\n%s", out)
			}
		})
	}
}

// A model offered by two providers gives the gateway a candidate list, so the
// first provider failing is a retry that is also a fallback.
func TestRetryToASecondProviderIsCounted(t *testing.T) {
	// The backup upstream works; the harness's own upstream does not.
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okChatResponse)
	}))
	t.Cleanup(backup.Close)

	broken := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"message":"down"}}`)
	}
	h := newHarness(t, broken, "openai",
		withTelemetry(telemetryOn()),
		withSetup(func(t *testing.T, st *store.Store, firstProvider int64) {
			// A second provider offering the same model id, at a lower
			// priority number so the broken one is tried first and the order
			// is deterministic.
			p2, err := st.CreateProvider(context.Background(), &store.Provider{
				Name: "backup", Protocol: "openai", BaseURL: backup.URL,
				APIKey: "sk-backup-secret", Enabled: true, Priority: -10,
			})
			if err != nil {
				t.Fatalf("create backup provider: %v", err)
			}
			for _, providerID := range []int64{firstProvider, p2.ID} {
				if _, err := st.CreateModel(context.Background(), &store.Model{
					ProviderID: providerID, UpstreamModelID: "shared-model", Enabled: true,
				}); err != nil {
					t.Fatalf("register model: %v", err)
				}
			}
		}))

	body := readAll(t, h.post("/v1/chat/completions",
		`{"model":"shared-model","messages":[{"role":"user","content":"hi"}]}`, nil))
	if !strings.Contains(body, "hello") {
		t.Fatalf("the fallback provider did not serve the request:\n%s", body)
	}

	log := h.waitForLog(t)
	if log.RetryCount != 1 {
		t.Errorf("retry_count = %d, want 1", log.RetryCount)
	}
	if log.FallbackCount != 1 {
		t.Errorf("fallback_count = %d, want 1", log.FallbackCount)
	}
	if log.ProviderName != "backup" {
		t.Errorf("the log names %q; the request was served by the backup", log.ProviderName)
	}

	out := h.scrape(t)
	for _, want := range []string{
		`polyglot_retries_total{provider="fake",reason="upstream_5xx"} 1`,
		`polyglot_fallbacks_total{from_provider="fake",reason="upstream_5xx"} 1`,
		`provider="backup"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in:\n%s", want, out)
		}
	}
}

// A client that hangs up mid-stream is not an upstream failure. Counting it as
// one would make every cancelled agent run look like an outage, and leaving
// the in-flight gauge raised would make the gateway look permanently busy.
func TestAClientDisconnectIsCancelledAndReleasesTheGauge(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		io.WriteString(w, `data: {"id":"x","choices":[{"index":0,"delta":{"content":"one"}}]}`+"\n\n")
		f.Flush()
		<-release // hold the stream open until the client is gone
	}, "openai", withTelemetry(telemetryOn()))
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		h.server.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"my-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+h.clientKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	buf := make([]byte, 32)
	resp.Body.Read(buf) // wait until the first chunk has really arrived
	cancel()
	resp.Body.Close()

	log := h.waitForLog(t)
	if log.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", log.Status)
	}

	out := h.scrape(t)
	if !strings.Contains(out, "polyglot_requests_in_flight 0") {
		t.Errorf("the in-flight gauge did not return to zero after a disconnect:\n%s", out)
	}
	if strings.Contains(out, `reason="upstream_5xx"`) {
		t.Errorf("a client disconnect was counted as an upstream failure:\n%s", out)
	}
}

func TestTelemetryDisabledLeavesRequestsUntouched(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okChatResponse)
	}, "openai", withTelemetry(telemetry.Config{Enabled: false, Metrics: true}))

	body := readAll(t, h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, nil))
	if !strings.Contains(body, "hello") {
		t.Errorf("the request did not work with telemetry off:\n%s", body)
	}

	// /metrics does not exist, rather than existing and being empty.
	req, _ := http.NewRequest(http.MethodGet, h.server.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+scrapeToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("/metrics = %d with telemetry off, want 404", resp.StatusCode)
	}

	// The request log keeps its own fields: it is a different system.
	log := h.waitForLog(t)
	if log.RequestID == "" {
		t.Error("the request log lost its request id when telemetry was switched off")
	}
	if log.Status != "success" {
		t.Errorf("status = %q", log.Status)
	}
}

// /metrics without a token must not exist at all, and must not fall through to
// the SPA handler and answer with an HTML page.
func TestScrapeEndpointIsAbsentWithoutAToken(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okChatResponse)
	}, "openai", withTelemetry(telemetry.Config{Enabled: true, Metrics: true}))

	for _, header := range []map[string]string{nil, {"Authorization": "Bearer guess"}} {
		req, _ := http.NewRequest(http.MethodGet, h.server.URL+"/metrics", nil)
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("get /metrics: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("/metrics = %d without METRICS_TOKEN, want 404", resp.StatusCode)
		}
		if strings.Contains(string(body), "polyglot_requests_total") {
			t.Error("/metrics served the exposition without authorisation")
		}
	}

	// An administrator can still read them through the authenticated API.
	admin := h.adminSession(t)
	resp := admin.do(t, http.MethodGet, "/api/metrics", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/metrics = %d for a signed-in admin", resp.StatusCode)
	}
}

// The exposition and the traces are read by whoever runs the monitoring stack,
// which is not always whoever holds the API keys. Nothing that identifies a
// caller or a payload may appear in either.
func TestNothingSensitiveReachesTheExposition(t *testing.T) {
	const prompt = "my-very-secret-prompt-text"
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		// An upstream that echoes the prompt back inside its error body, which
		// is exactly how prompt text leaks into telemetry if error text is
		// used as a label.
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":{"message":"bad prompt: %s (key sk-upstream-secret-value)"}}`, prompt)
	}, "openai", withTelemetry(telemetryOn()))

	readAll(t, h.post("/v1/chat/completions",
		fmt.Sprintf(`{"model":"my-model","messages":[{"role":"user","content":%q}]}`, prompt),
		map[string]string{"X-Custom-Header": "should-not-appear"}))

	out := h.scrape(t)
	for _, forbidden := range []string{
		prompt,                     // the prompt itself
		h.clientKey,                // Polyglot's own API key
		"sk-upstream-secret-value", // the provider credential
		"Bearer",
		"Authorization",
		"should-not-appear", // a caller-supplied header
		"bad prompt",        // the upstream's error text
	} {
		if strings.Contains(out, forbidden) {
			t.Errorf("/metrics leaked %q:\n%s", forbidden, out)
		}
	}

	// The request id is deliberately absent too: it is unbounded, and one
	// series per request would take the registry down.
	log := h.waitForLog(t)
	if log.RequestID != "" && strings.Contains(out, log.RequestID) {
		t.Errorf("the request id became a metric label:\n%s", out)
	}
}

func TestEveryRequestGetsAnIDThatIsEchoedAndLogged(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okChatResponse)
	}, "openai", withTelemetry(telemetryOn()))

	resp := h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`, nil)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	echoed := resp.Header.Get(RequestIDHeader)
	if echoed == "" {
		t.Fatal("no X-Request-Id was returned")
	}
	log := h.waitForLog(t)
	if log.RequestID != echoed {
		t.Errorf("log request_id = %q, response header = %q; they must match", log.RequestID, echoed)
	}
}

func TestACallersRequestIDIsReusedOnlyWhenItIsPlausible(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okChatResponse)
	}, "openai", withTelemetry(telemetryOn()))

	// A well-formed caller id is kept, so a caller that already correlates its
	// own traffic keeps its identifier through the gateway.
	resp := h.post("/v1/chat/completions",
		`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{RequestIDHeader: "caller-abc.123"})
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if got := resp.Header.Get(RequestIDHeader); got != "caller-abc.123" {
		t.Errorf("a valid caller id was replaced: %q", got)
	}

	// Anything that could forge a field in a log line is replaced silently.
	for _, bad := range []string{
		"has spaces", "new\nline", strings.Repeat("x", 200), `quote"inject`,
	} {
		resp := h.post("/v1/chat/completions",
			`{"model":"my-model","messages":[{"role":"user","content":"hi"}]}`,
			map[string]string{RequestIDHeader: strings.ReplaceAll(bad, "\n", "")})
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if got := resp.Header.Get(RequestIDHeader); got == bad {
			t.Errorf("a malformed caller id %q was accepted", bad)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("a malformed request id failed the request with %d", resp.StatusCode)
		}
	}
}
