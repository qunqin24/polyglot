// Package telemetry is Polyglot's observability layer: one request lifecycle
// object, a metric registry exposed in the Prometheus text format, and
// optional OpenTelemetry tracing.
//
// Three rules shape everything here.
//
// **Nothing leaves this machine unless the operator sent it somewhere.**
// Polyglot has no telemetry server, no usage reporting and no phone-home path,
// and this package deliberately contains no code that could grow one. The only
// egress is an OTLP endpoint the operator configured, to a collector the
// operator runs.
//
// **Telemetry is never more important than the request.** Every recording path
// is non-blocking and best effort. A saturated metric registry folds into an
// overflow series, a dead collector drops spans, and neither can fail, slow or
// alter a proxied request.
//
// **Privacy is structural, not a filter.** Prompts, completions, tool
// arguments, headers, credentials, URLs and error bodies are not passed into
// this package at all, so there is nothing here to leak. Metric labels come
// from a bounded set of operator-configured names; identifiers that would
// explode cardinality — request ids, trace ids, API keys — are carried on
// traces and log rows, never on a metric.
package telemetry

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Config is the process-wide telemetry configuration, built by
// internal/config from the environment.
type Config struct {
	// Enabled is the master switch. With it off, requests still carry a
	// lifecycle object — request id, retry counts and timings still reach the
	// request log — but no metric is recorded, no span is created and no
	// exporter is started.
	Enabled bool

	// Metrics turns in-process metric collection on.
	Metrics bool
	// MetricsToken guards GET /metrics. Without one that endpoint does not
	// exist, because Polyglot's own port is routinely on the public internet
	// and an unauthenticated /metrics would publish an operator's provider
	// names, model list and traffic shape to anyone who asked.
	MetricsToken string
	// MaxSeries caps distinct label combinations per metric.
	MaxSeries int

	// Tracing turns on span creation. It needs OTLPEndpoint to be useful:
	// with no exporter configured there is nowhere for a span to go, and
	// Polyglot logs that and leaves tracing off rather than pretending.
	Tracing bool
	// TraceSampleRatio is head sampling for traces Polyglot starts itself,
	// from 0 to 1. A trace continued from an inbound traceparent follows the
	// caller's decision instead.
	TraceSampleRatio float64

	// OTLPEndpoint is an OpenTelemetry collector base URL, e.g.
	// http://collector:4318. OTLP over HTTP with the JSON encoding is the
	// only transport implemented.
	OTLPEndpoint string
	// OTLPHeaders are extra headers for the collector, typically an API token
	// for a hosted backend.
	OTLPHeaders map[string]string

	ServiceName    string
	ServiceVersion string
}

// Telemetry is the only thing business code talks to. It knows about
// Prometheus and OTLP so that the gateway, the router and the codecs never
// have to.
type Telemetry struct {
	cfg Config
	log *slog.Logger

	reg    *registry
	tracer *tracer

	// instruments
	requests     *metric
	duration     *metric
	inputTok     *metric
	outputTok    *metric
	reasoningTok *metric
	ttft         *metric
	generation   *metric
	tps          *metric
	retries      *metric
	fallbacks    *metric
	errors       *metric
}

// New builds the telemetry layer. It never returns an error: a misconfigured
// exporter degrades to "not exporting" with a warning, because a typo in an
// OTLP URL must not stop a gateway from serving traffic.
func New(cfg Config, log *slog.Logger) *Telemetry {
	t := &Telemetry{cfg: cfg, log: log}
	if !cfg.Enabled {
		log.Debug("telemetry is disabled")
		return t
	}

	if cfg.Metrics {
		t.reg = newRegistry(cfg.MaxSeries)
		t.registerInstruments()
	}

	if cfg.Tracing {
		switch {
		case cfg.OTLPEndpoint == "":
			log.Warn("tracing is enabled but no OTLP endpoint is configured; " +
				"set OTLP_ENDPOINT to a collector, e.g. http://collector:4318")
		default:
			reg := t.reg
			if reg == nil {
				// Span drops are counted even when the Prometheus surface is
				// off, so Shutdown can still report them.
				reg = newRegistry(cfg.MaxSeries)
			}
			ratio := cfg.TraceSampleRatio
			if ratio <= 0 || ratio > 1 {
				ratio = 1
			}
			t.tracer = &tracer{exporter: newOTLPExporter(cfg, reg, log), ratio: ratio}
			// SafeEndpoint, not the raw value: an endpoint may carry a
			// credential in its userinfo, and that must not reach a log.
			log.Info("tracing enabled",
				"otlp_endpoint", SafeEndpoint(tracesURL(cfg.OTLPEndpoint)), "sample_ratio", ratio)
		}
	}
	return t
}

// Metric names use the application as the prefix, which is the Prometheus
// convention and keeps them unambiguous next to other exporters on the same
// scrape target.
func (t *Telemetry) registerInstruments() {
	r := t.reg
	// provider and model are safe labels: both are operator-configured names
	// from the registry. A model a client invented never reaches them — see
	// labels.go.
	route := []string{"protocol", "upstream_protocol", "provider", "model", "stream"}

	t.requests = r.counter("polyglot_requests_total",
		"Gateway requests by route and outcome.",
		append(append([]string{}, route...), "status", "code")...)
	t.duration = r.histogram("polyglot_request_duration_seconds",
		"End-to-end gateway request duration in seconds.", durationBuckets,
		route...)
	t.inputTok = r.counter("polyglot_input_tokens_total",
		"Input tokens reported by upstreams.", "provider", "model")
	t.outputTok = r.counter("polyglot_output_tokens_total",
		"Output tokens reported by upstreams.", "provider", "model")
	t.reasoningTok = r.counter("polyglot_reasoning_tokens_total",
		"Reasoning tokens reported by upstreams.", "provider", "model")
	t.ttft = r.histogram("polyglot_ttft_seconds",
		"Time from receiving a streaming request to its first content token.", ttftBuckets,
		"provider", "model")
	t.generation = r.histogram("polyglot_generation_duration_seconds",
		"Time between the first and last content token of a streamed reply.", durationBuckets,
		"provider", "model")
	t.tps = r.histogram("polyglot_output_tokens_per_second",
		"Output tokens divided by generation duration, streaming replies only.", tpsBuckets,
		"provider", "model")
	t.retries = r.counter("polyglot_retries_total",
		"Upstream attempts beyond the first, by the provider whose failure caused them.",
		"provider", "reason")
	t.fallbacks = r.counter("polyglot_fallbacks_total",
		"Retries that moved to a different provider.", "from_provider", "reason")
	t.errors = r.counter("polyglot_errors_total",
		"Failed requests by error class.", "protocol", "provider", "reason")
}

// Enabled reports whether anything is being recorded.
func (t *Telemetry) Enabled() bool { return t != nil && t.cfg.Enabled }

// MetricsEnabled reports whether the Prometheus registry is live.
func (t *Telemetry) MetricsEnabled() bool { return t != nil && t.reg != nil }

// ScrapeAuthorized reports whether a request may read /metrics. Without a
// configured token the endpoint is closed entirely — there is no "trusted
// network" fallback, because X-Forwarded-For is honoured on this server and a
// source-address rule would be spoofable.
func (t *Telemetry) ScrapeAuthorized(r *http.Request) bool {
	if !t.MetricsEnabled() || t.cfg.MetricsToken == "" {
		return false
	}
	presented := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
	return subtle.ConstantTimeCompare([]byte(presented), []byte(t.cfg.MetricsToken)) == 1
}

// WriteMetrics renders the exposition. Callers are responsible for
// authorization; both the scrape endpoint and the admin API use this.
func (t *Telemetry) WriteMetrics(w http.ResponseWriter) {
	if !t.MetricsEnabled() {
		return
	}
	// A panic while rendering metrics must not become a 500 on an endpoint an
	// operator is watching, and must never take down the process.
	defer func() {
		if rec := recover(); rec != nil {
			t.log.Error("telemetry: rendering metrics panicked", "panic", rec)
		}
	}()
	t.reg.setInternalMetrics()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	t.reg.writeExposition(w)
}

// Shutdown flushes the span exporter. Metrics need no flush: they are read on
// scrape and were never written anywhere.
func (t *Telemetry) Shutdown(timeout time.Duration) {
	if t == nil || t.tracer == nil || t.tracer.exporter == nil {
		return
	}
	t.tracer.exporter.Shutdown(timeout)
}

// setInternalMetrics publishes the registry's own health alongside everything
// else, so a saturated registry or an unreachable collector is visible in the
// same place an operator is already looking.
func (r *registry) setInternalMetrics() {
	inflight := r.register(kindGauge, "polyglot_requests_in_flight",
		"Gateway requests currently being served.", nil, nil)
	inflight.mu.Lock()
	inflight.lookup(nil).value = float64(r.inFlight.Load())
	inflight.mu.Unlock()

	dropped := r.register(kindCounter, "polyglot_telemetry_dropped_total",
		"Telemetry discarded to protect the request path.", []string{"kind"}, nil)
	dropped.mu.Lock()
	dropped.lookup([]string{"series"}).value = float64(r.droppedSeries.Load())
	dropped.lookup([]string{"span"}).value = float64(r.droppedSpans.Load())
	dropped.mu.Unlock()
}
