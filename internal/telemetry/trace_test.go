package telemetry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// collector is a stand-in for an OpenTelemetry collector: it records what was
// posted to /v1/traces and can be told to misbehave.
type collector struct {
	*httptest.Server

	mu       sync.Mutex
	payloads []tracePayloadJSON
	received chan struct{}

	status int
	delay  time.Duration
}

func newCollector(t *testing.T) *collector {
	c := &collector{received: make(chan struct{}, 64), status: http.StatusOK}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		delay, status := c.delay, c.status
		c.mu.Unlock()

		if delay > 0 {
			time.Sleep(delay)
		}
		if r.URL.Path != "/v1/traces" {
			t.Errorf("spans were posted to %s, want /v1/traces", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		var p tracePayloadJSON
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Errorf("the collector could not decode the payload: %v", err)
		}
		c.mu.Lock()
		c.payloads = append(c.payloads, p)
		c.mu.Unlock()
		w.WriteHeader(status)
		select {
		case c.received <- struct{}{}:
		default:
		}
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *collector) spans() []spanJSON {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []spanJSON
	for _, p := range c.payloads {
		for _, rs := range p.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				out = append(out, ss.Spans...)
			}
		}
	}
	return out
}

func tracingTo(t *testing.T, endpoint string) *Telemetry {
	tel := New(Config{
		Enabled: true, Metrics: true, Tracing: true,
		OTLPEndpoint: endpoint, TraceSampleRatio: 1,
		ServiceName: "polyglot", ServiceVersion: "test",
	}, testLogger())
	t.Cleanup(func() { tel.Shutdown(2 * time.Second) })
	return tel
}

func TestARequestProducesATraceWithAnUpstreamChild(t *testing.T) {
	c := newCollector(t)
	tel := tracingTo(t, c.URL)

	r := tel.StartRequest(RequestInfo{
		ID: "req-trace", ClientProtocol: "anthropic", Method: "POST", Route: "/v1/messages",
	})
	r.Streaming(true)
	a := r.StartAttempt("openrouter", "openai", "gpt-5")
	a.Succeeded()
	r.Finish("success", 200, "")

	tel.Shutdown(2 * time.Second)

	spans := c.spans()
	if len(spans) < 2 {
		t.Fatalf("got %d spans, want the request and its upstream attempt", len(spans))
	}
	byName := map[string]spanJSON{}
	for _, s := range spans {
		byName[s.Name] = s
	}
	root, ok := byName["gateway.request"]
	if !ok {
		t.Fatalf("no gateway.request span; got %v", spans)
	}
	child, ok := byName["upstream.request"]
	if !ok {
		t.Fatalf("no upstream.request span; got %v", spans)
	}
	if child.ParentSpanID != root.SpanID {
		t.Errorf("the upstream span is not a child of the request span (%s vs %s)",
			child.ParentSpanID, root.SpanID)
	}
	if child.TraceID != root.TraceID {
		t.Error("the two spans are in different traces")
	}
	if len(root.TraceID) != 32 || len(root.SpanID) != 16 {
		t.Errorf("ids are not OTLP hex: trace=%q span=%q", root.TraceID, root.SpanID)
	}
	if root.EndTimeUnixNano == "" || root.EndTimeUnixNano == "0" {
		t.Error("the request span never ended")
	}
}

// A span carries provider and model, which are configuration, and the request
// id, which is an opaque token. It must never carry a prompt, a header or a
// credential — and there is no code path that could put one there, which is
// what this test pins.
func TestSpanAttributesCarryNothingSensitive(t *testing.T) {
	c := newCollector(t)
	tel := tracingTo(t, c.URL)

	r := tel.StartRequest(RequestInfo{
		ID: "req-priv", ClientProtocol: "openai", Method: "POST", Route: "/v1/chat/completions",
	})
	r.StartAttempt("openrouter", "openai", "gpt-5").Succeeded()
	r.Usage(10, 20, 0)
	r.Finish("success", 200, "")
	tel.Shutdown(2 * time.Second)

	c.mu.Lock()
	raw, err := json.Marshal(c.payloads)
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("marshal payloads: %v", err)
	}
	body := string(raw)
	for _, forbidden := range []string{
		"Authorization", "Bearer", "api_key", "apiKey",
		"messages", "prompt", "content", "Cookie",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("a span carried %q:\n%s", forbidden, body)
		}
	}
	if !strings.Contains(body, "req-priv") {
		t.Error("the request id should be on the span; it is what ties a trace to a log row")
	}
}

// Nothing a collector does may reach the request path. This drives the three
// ways a collector goes wrong — refused, erroring, hanging — and checks that
// the lifecycle completes normally every time.
func TestACollectorThatFailsNeverAffectsARequest(t *testing.T) {
	cases := []struct {
		name     string
		endpoint func(t *testing.T) string
	}{
		{"unreachable", func(t *testing.T) string {
			// A port nothing is listening on.
			s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			url := s.URL
			s.Close()
			return url
		}},
		{"rejects everything", func(t *testing.T) string {
			c := newCollector(t)
			c.mu.Lock()
			c.status = http.StatusInternalServerError
			c.mu.Unlock()
			return c.URL
		}},
		{"hangs", func(t *testing.T) string {
			c := newCollector(t)
			c.mu.Lock()
			c.delay = 1500 * time.Millisecond
			c.mu.Unlock()
			return c.URL
		}},
		{"not a URL at all", func(t *testing.T) string { return "://nonsense" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tel := tracingTo(t, tc.endpoint(t))

			done := make(chan Outcome, 1)
			go func() {
				r := tel.StartRequest(RequestInfo{ID: "req", ClientProtocol: "openai"})
				r.Streaming(true)
				a := r.StartAttempt("p", "openai", "m")
				r.ContentToken()
				r.Usage(1, 2, 0)
				a.Succeeded()
				done <- r.Finish("success", 200, "")
			}()

			select {
			case out := <-done:
				if out.RequestID != "req" {
					t.Errorf("request id = %q", out.RequestID)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the request lifecycle blocked on the exporter")
			}

			// And the metrics for that request are still there.
			if !strings.Contains(expose(t, tel), "polyglot_requests_total") {
				t.Error("metrics stopped being recorded because the trace exporter was unhappy")
			}
		})
	}
}

// A full queue must drop rather than block, because the alternative is
// applying backpressure from an observability backend onto live LLM traffic.
func TestAFullExportQueueDropsInsteadOfBlocking(t *testing.T) {
	c := newCollector(t)
	c.mu.Lock()
	c.delay = time.Second // wedge the exporter
	c.mu.Unlock()

	tel := tracingTo(t, c.URL)

	start := time.Now()
	for range otlpQueueSize * 2 {
		r := tel.StartRequest(RequestInfo{ID: "req", ClientProtocol: "openai"})
		r.StartAttempt("p", "openai", "m").Succeeded()
		r.Finish("success", 200, "")
	}
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("producing %d requests took %s; the exporter is applying backpressure",
			otlpQueueSize*2, elapsed)
	}
	if tel.reg.droppedSpans.Load() == 0 {
		t.Error("spans were dropped but nothing counted them")
	}
	if !strings.Contains(expose(t, tel), `polyglot_telemetry_dropped_total{kind="span"}`) {
		t.Error("dropped spans must be visible in the exposition")
	}
}

func TestTracingWithoutACollectorStaysOff(t *testing.T) {
	tel := New(Config{Enabled: true, Metrics: true, Tracing: true}, testLogger())
	if tel.tracer != nil {
		t.Error("tracing was switched on with nowhere to send a span")
	}
	// And a request still works.
	r := tel.StartRequest(RequestInfo{ID: "req", ClientProtocol: "openai"})
	r.StartAttempt("p", "openai", "m").Succeeded()
	if out := r.Finish("success", 200, ""); out.RequestID != "req" {
		t.Error("the request lifecycle broke without a tracer")
	}
}

func TestInboundTraceparentContinuesTheCallersTrace(t *testing.T) {
	c := newCollector(t)
	tel := tracingTo(t, c.URL)

	const parent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	sc := ParseTraceparent(parent)
	if sc.TraceID.String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("parsed trace id = %s", sc.TraceID)
	}
	if !sc.Sampled {
		t.Fatal("the sampled flag was not read")
	}

	r := tel.StartRequest(RequestInfo{ID: "req", ClientProtocol: "openai", Parent: sc})
	r.StartAttempt("p", "openai", "m").Succeeded()
	r.Finish("success", 200, "")
	tel.Shutdown(2 * time.Second)

	spans := c.spans()
	if len(spans) == 0 {
		t.Fatal("no spans were exported")
	}
	for _, s := range spans {
		if s.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
			t.Errorf("span %q is in trace %s, want the caller's", s.Name, s.TraceID)
		}
	}
	for _, s := range spans {
		if s.Name == "gateway.request" && s.ParentSpanID != "00f067aa0ba902b7" {
			t.Errorf("the request span's parent = %q, want the caller's span", s.ParentSpanID)
		}
	}
}

func TestAMalformedTraceparentIsIgnored(t *testing.T) {
	for _, bad := range []string{
		"", "garbage", "00-tooshort-00f067aa0ba902b7-01",
		"ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", // reserved version
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01", // all-zero trace id
		"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01", // all-zero span id
	} {
		if sc := ParseTraceparent(bad); sc.TraceID != (TraceID{}) {
			t.Errorf("ParseTraceparent(%q) accepted a malformed header", bad)
		}
	}
}

// A caller that decided not to sample has decided for us too; a trace that is
// half exported is worse than one that is not exported at all.
func TestAnUnsampledParentSuppressesTheTrace(t *testing.T) {
	c := newCollector(t)
	tel := tracingTo(t, c.URL)

	sc := ParseTraceparent("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00")
	if sc.Sampled {
		t.Fatal("flags 00 means not sampled")
	}
	r := tel.StartRequest(RequestInfo{ID: "req", ClientProtocol: "openai", Parent: sc})
	r.StartAttempt("p", "openai", "m").Succeeded()
	r.Finish("success", 200, "")
	tel.Shutdown(time.Second)

	if n := len(c.spans()); n != 0 {
		t.Errorf("%d spans were exported for a trace the caller did not sample", n)
	}
}

func TestOTLPEndpointAcceptsBothSpellings(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"http://collector:4318", "http://collector:4318/v1/traces"},
		{"http://collector:4318/", "http://collector:4318/v1/traces"},
		{"http://collector:4318/v1/traces", "http://collector:4318/v1/traces"},
	} {
		if got := tracesURL(tc.in); got != tc.want {
			t.Errorf("tracesURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
