package telemetry

import (
	"time"
)

// Request is the single place a request's telemetry lives, from the first byte
// to the last. Metrics logic stays here instead of being scattered through the
// gateway, and the request log is filled from the same numbers the metrics
// are, so the two can never disagree.
//
// A Request exists even when telemetry is switched off. Timings, the request
// id and the retry counts belong to the request log, which is a different
// system with its own switch; what TELEMETRY_ENABLED=false turns off is
// metrics, spans and exporters. The cost when off is one allocation and two
// clock reads per request.
//
// One request is handled by one goroutine, so the fields below need no lock.
// The pieces that outlive a single goroutine — the metric registry, the span
// exporter — do their own synchronisation.
type Request struct {
	tel *Telemetry
	id  string

	clientProtocol   string
	upstreamProtocol string
	provider         string
	model            string
	stream           bool
	// routed records that provider and model came from the registry. Until it
	// is true the model name is whatever a client typed, and it must not
	// become a metric label.
	routed bool

	start      time.Time
	firstToken time.Time
	lastToken  time.Time

	attempts      int
	fallbacks     int
	firstProvider string

	inputTokens     int
	outputTokens    int
	reasoningTokens int

	span     *Span
	finished bool
}

// RequestInfo is what is known about a request before anything has been
// parsed: who it came from and what protocol it speaks.
type RequestInfo struct {
	// ID is the request id assigned by the middleware. It reaches logs and
	// traces, and never a metric label.
	ID string
	// ClientProtocol is the inbound wire format.
	ClientProtocol string
	// Parent is a trace context from an inbound traceparent header, if the
	// caller sent a valid one.
	Parent SpanContext
	// Method and Path describe the endpoint. Only the routing pattern is used,
	// never a full URL with its query string, which is where an api_key=
	// parameter would be.
	Method string
	Route  string
}

// StartRequest opens the lifecycle. The returned Request is never nil, so the
// call sites need no guards.
func (t *Telemetry) StartRequest(info RequestInfo) *Request {
	r := &Request{
		tel:            t,
		id:             info.ID,
		clientProtocol: info.ClientProtocol,
		provider:       labelNone,
		model:          labelUnrouted,
		start:          time.Now(),
	}
	if !t.Enabled() {
		return r
	}
	if t.reg != nil {
		t.reg.inFlight.Add(1)
	}
	r.span = t.tracer.start(info.Parent, "gateway.request", SpanServer,
		StringAttr("polyglot.request_id", info.ID),
		StringAttr("polyglot.client_protocol", info.ClientProtocol),
		StringAttr("http.request.method", info.Method),
		StringAttr("http.route", info.Route),
	)
	return r
}

// ID returns the request id.
func (r *Request) ID() string {
	if r == nil {
		return ""
	}
	return r.id
}

// TraceParent renders the W3C traceparent for this request's span, for logs
// that need to point at a trace. It is empty when tracing is off.
func (r *Request) TraceParent() string {
	if r == nil || r.span == nil {
		return ""
	}
	return "00-" + r.span.TraceID.String() + "-" + r.span.SpanID.String() + "-01"
}

// Span returns the request span so callers can hang children off it.
func (r *Request) Span() *Span {
	if r == nil {
		return nil
	}
	return r.span
}

// StartSpan opens a child of the request span.
func (r *Request) StartSpan(name string, attrs ...Attr) *Span {
	if r == nil || r.span == nil {
		return nil
	}
	return r.tel.tracer.start(r.span.Context(), name, SpanInternal, attrs...)
}

// Streaming records whether the client asked for a stream. It is known only
// after the request body has been decoded.
func (r *Request) Streaming(stream bool) {
	if r == nil {
		return
	}
	r.stream = stream
	r.span.SetAttributes(BoolAttr("polyglot.stream", stream))
}

// Attempt is one try against one upstream. The second and later attempts are
// retries; those that land on a different provider are also fallbacks.
type Attempt struct {
	req      *Request
	span     *Span
	provider string
	isRetry  bool
}

// StartAttempt opens an upstream attempt. provider and model come from the
// router, so from here on they are safe as metric labels.
func (r *Request) StartAttempt(provider, upstreamProtocol, model string) *Attempt {
	if r == nil {
		return &Attempt{}
	}
	r.attempts++
	r.provider = sanitizeLabel(provider)
	r.upstreamProtocol = sanitizeLabel(upstreamProtocol)
	r.model = sanitizeLabel(model)
	r.routed = true

	a := &Attempt{req: r, provider: r.provider, isRetry: r.attempts > 1}
	if r.firstProvider == "" {
		r.firstProvider = r.provider
	} else if r.provider != r.firstProvider {
		r.fallbacks++
	}

	a.span = r.StartSpan("upstream.request",
		StringAttr("polyglot.provider", r.provider),
		StringAttr("polyglot.upstream_protocol", r.upstreamProtocol),
		StringAttr("polyglot.model", r.model),
		IntAttr("polyglot.attempt", int64(r.attempts)),
	)
	if a.span != nil {
		a.span.Kind = SpanClient
	}
	return a
}

// Failed closes an attempt that did not produce a usable reply. class must be
// an ErrorClass, never an upstream message.
func (a *Attempt) Failed(class string) {
	if a == nil || a.req == nil {
		return
	}
	a.span.SetError(class)
	a.span.End()
}

// Succeeded closes an attempt that worked.
func (a *Attempt) Succeeded() {
	if a == nil || a.req == nil {
		return
	}
	a.span.End()
}

// Retrying records that a failed attempt is about to be followed by another
// one. It is called only when the gateway actually retries, so the counter
// counts retries rather than failures.
func (r *Request) Retrying(from *Attempt, class string, differentProvider bool) {
	if r == nil || !r.tel.Enabled() {
		return
	}
	provider := labelNone
	if from != nil && from.provider != "" {
		provider = from.provider
	}
	r.tel.retries.inc(provider, class)
	if differentProvider {
		r.tel.fallbacks.inc(provider, class)
	}
}

// ContentToken marks the arrival of a content-bearing chunk. It is the only
// telemetry call on the streaming hot path, and it does one comparison and at
// most one clock read — no allocation, no lock, no I/O, and nothing buffered.
func (r *Request) ContentToken() {
	r.contentAt(time.Now())
}

// contentAt is the clock-injectable half of ContentToken. Production always
// calls it with time.Now; tests use fixed timestamps so the sub-millisecond
// boundary cannot become scheduler-dependent.
func (r *Request) contentAt(now time.Time) {
	if r == nil {
		return
	}
	if r.firstToken.IsZero() {
		r.firstToken = now
	}
	r.lastToken = now
}

// Usage records token counts an upstream reported. Absent usage stays absent:
// nothing here estimates a token count, because a made-up number in a metric
// is worse than a gap.
func (r *Request) Usage(input, output, reasoning int) {
	if r == nil {
		return
	}
	r.inputTokens = input
	r.outputTokens = output
	r.reasoningTokens = reasoning
}

// Outcome is what the request log takes from the lifecycle. Every optional
// field is a pointer, because "not measurable for this request" and "zero" are
// different answers and the difference matters.
type Outcome struct {
	RequestID     string
	RetryCount    int
	FallbackCount int
	// TTFTMS is set for streaming replies that produced at least one content
	// token: the time from receiving the request to that token.
	TTFTMS *int64
	// GenerationMS is the span from the first content token to the last.
	GenerationMS *int64
	// TPS is output tokens over GenerationMS. It needs an upstream token count
	// and at least two distinguishable token timestamps.
	TPS *float64
}

// Finish closes the lifecycle, records every metric for the request and
// returns what the request log should store. Calling it twice is harmless, so
// it is safe to defer next to explicit error paths.
func (r *Request) Finish(status string, code int, class string) Outcome {
	if r == nil {
		return Outcome{}
	}
	out := Outcome{
		RequestID:     r.id,
		RetryCount:    max(r.attempts-1, 0),
		FallbackCount: r.fallbacks,
	}
	if r.finished {
		return out
	}
	r.finished = true

	// TTFT and TPS are streaming-only by construction: a buffered reply has no
	// token timestamps to measure between.
	if !r.firstToken.IsZero() {
		ttft := r.firstToken.Sub(r.start).Milliseconds()
		out.TTFTMS = &ttft

		if gen := r.lastToken.Sub(r.firstToken); gen >= time.Millisecond {
			ms := gen.Milliseconds()
			out.GenerationMS = &ms
			if r.outputTokens > 0 {
				tps := float64(r.outputTokens) / gen.Seconds()
				out.TPS = &tps
			}
		}
	}

	r.record(status, code, class, out)
	return out
}

// record is the only place metrics are written. It runs after the response has
// been delivered, and a panic in it is contained rather than allowed to reach
// the request.
func (r *Request) record(status string, code int, class string, out Outcome) {
	t := r.tel
	if !t.Enabled() {
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			t.log.Error("telemetry: recording a request panicked", "panic", rec)
		}
	}()

	if r.span != nil {
		r.span.SetAttributes(
			StringAttr("polyglot.provider", r.provider),
			StringAttr("polyglot.model", r.model),
			StringAttr("polyglot.upstream_protocol", r.upstreamProtocol),
			StringAttr("polyglot.status", status),
			IntAttr("http.response.status_code", int64(code)),
			IntAttr("polyglot.retry_count", int64(out.RetryCount)),
			IntAttr("polyglot.fallback_count", int64(out.FallbackCount)),
			IntAttr("polyglot.input_tokens", int64(r.inputTokens)),
			IntAttr("polyglot.output_tokens", int64(r.outputTokens)),
		)
		if out.TTFTMS != nil {
			r.span.SetAttributes(IntAttr("polyglot.ttft_ms", *out.TTFTMS))
		}
		if class != classNone && class != "" {
			r.span.SetError(class)
		}
		r.span.End()
	}

	if t.reg == nil {
		return
	}
	t.reg.inFlight.Add(-1)

	protocol := sanitizeLabel(r.clientProtocol)
	upstream := r.upstreamProtocol
	if upstream == "" {
		upstream = labelNone
	}
	provider, model := r.provider, r.model
	if !r.routed {
		// Nothing resolved, so there is no bounded name to attribute this to.
		provider, model = labelNone, labelUnrouted
	}
	stream := boolLabel(r.stream)

	t.requests.inc(protocol, upstream, provider, model, stream, status, statusCodeLabel(code))
	t.duration.observe(time.Since(r.start).Seconds(), protocol, upstream, provider, model, stream)

	if class != "" && class != classNone {
		t.errors.inc(protocol, provider, class)
	}

	if r.inputTokens > 0 {
		t.inputTok.add(float64(r.inputTokens), provider, model)
	}
	if r.outputTokens > 0 {
		t.outputTok.add(float64(r.outputTokens), provider, model)
	}
	if r.reasoningTokens > 0 {
		t.reasoningTok.add(float64(r.reasoningTokens), provider, model)
	}
	if out.TTFTMS != nil {
		t.ttft.observe(float64(*out.TTFTMS)/1000, provider, model)
	}
	if out.GenerationMS != nil {
		t.generation.observe(float64(*out.GenerationMS)/1000, provider, model)
	}
	if out.TPS != nil {
		t.tps.observe(*out.TPS, provider, model)
	}
}
