package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// OTLP over HTTP with the JSON encoding, which the OpenTelemetry Collector,
// Jaeger, Tempo, Grafana Alloy, Datadog's OTLP intake and New Relic all accept
// on /v1/traces. The protobuf-over-gRPC transport is not implemented; see the
// README, which says so rather than implying otherwise.
//
// Export is asynchronous and best effort, in that order. Nothing on the
// request path ever waits for a collector: spans go onto a bounded queue, and
// a full queue drops rather than blocks. A collector that is down, slow or
// returning 500 costs Polyglot a counter, never a request.

const (
	otlpQueueSize    = 2048
	otlpBatchSize    = 256
	otlpFlushEvery   = 2 * time.Second
	otlpExportTimout = 10 * time.Second
)

type otlpExporter struct {
	endpoint string
	headers  map[string]string
	client   *http.Client
	log      *slog.Logger
	reg      *registry

	queue  chan *Span
	done   chan struct{}
	once   sync.Once
	closed atomic.Bool

	resource []attrJSON
}

func newOTLPExporter(cfg Config, reg *registry, log *slog.Logger) *otlpExporter {
	e := &otlpExporter{
		endpoint: tracesURL(cfg.OTLPEndpoint),
		headers:  cfg.OTLPHeaders,
		log:      log,
		reg:      reg,
		queue:    make(chan *Span, otlpQueueSize),
		done:     make(chan struct{}),
		client: &http.Client{
			Timeout: otlpExportTimout,
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				MaxIdleConnsPerHost: 2,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		resource: []attrJSON{
			stringAttrJSON("service.name", cfg.ServiceName),
			stringAttrJSON("service.version", cfg.ServiceVersion),
		},
	}
	go e.run()
	return e
}

// tracesURL accepts either a collector base URL or a full signal URL, because
// both spellings are in every collector's documentation.
func tracesURL(endpoint string) string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if strings.HasSuffix(endpoint, "/v1/traces") {
		return endpoint
	}
	return endpoint + "/v1/traces"
}

// SafeEndpoint strips any embedded credential before an endpoint is written to
// a log or printed by `polyglot config`. http://user:token@collector:4318 is a
// legal thing for an operator to configure, and echoing it verbatim would put
// that token in the log of a gateway that is careful about every other
// credential it holds.
func SafeEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "(unparseable endpoint)"
	}
	if u.User != nil {
		u.User = url.User("redacted")
	}
	return u.String()
}

func (e *otlpExporter) Export(s *Span) {
	// A span finishing during shutdown must not send on a closed channel. The
	// flag catches almost every case and the recover catches the narrow race
	// between the flag and the close — a dropped span either way, never a
	// panic that could reach the request that produced it.
	if e.closed.Load() {
		e.reg.droppedSpans.Add(1)
		return
	}
	defer func() {
		if recover() != nil {
			e.reg.droppedSpans.Add(1)
		}
	}()
	select {
	case e.queue <- s:
	default:
		// The collector cannot keep up. Dropping is the correct trade: the
		// alternative is applying backpressure to live LLM traffic.
		e.reg.droppedSpans.Add(1)
	}
}

func (e *otlpExporter) run() {
	defer close(e.done)
	// A panic in export must not take the process with it.
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("telemetry span exporter stopped after a panic", "panic", r)
		}
	}()

	ticker := time.NewTicker(otlpFlushEvery)
	defer ticker.Stop()

	batch := make([]*Span, 0, otlpBatchSize)
	for {
		select {
		case s, ok := <-e.queue:
			if !ok {
				e.send(batch)
				return
			}
			batch = append(batch, s)
			if len(batch) >= otlpBatchSize {
				e.send(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			e.send(batch)
			batch = batch[:0]
		}
	}
}

func (e *otlpExporter) send(batch []*Span) {
	if len(batch) == 0 {
		return
	}
	body, err := json.Marshal(tracePayload(e.resource, batch))
	if err != nil {
		e.log.Debug("telemetry: encode spans", "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), otlpExportTimout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		e.log.Debug("telemetry: build export request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range e.headers {
		req.Header.Set(k, v)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		// Debug, not Error: a collector that is briefly unavailable is an
		// operational fact about the collector, not about the gateway, and it
		// must not fill the log of a working proxy.
		e.log.Debug("telemetry: export spans", "error", err, "spans", len(batch))
		e.reg.droppedSpans.Add(int64(len(batch)))
		return
	}
	defer resp.Body.Close()
	// The body is drained and discarded: it can only report how many spans a
	// collector rejected, and draining it keeps the connection reusable.
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode >= 300 {
		e.log.Debug("telemetry: collector rejected spans", "status", resp.StatusCode, "spans", len(batch))
		e.reg.droppedSpans.Add(int64(len(batch)))
	}
}

func (e *otlpExporter) Shutdown(timeout time.Duration) {
	e.once.Do(func() {
		e.closed.Store(true)
		close(e.queue)
	})
	select {
	case <-e.done:
	case <-time.After(timeout):
	}
}

// --- OTLP JSON encoding ---------------------------------------------------
//
// The shapes below are the protobuf-to-JSON mapping OTLP specifies: ids as
// lowercase hex strings, timestamps as stringified nanoseconds since the
// epoch, and attributes as key/typed-value pairs.

type tracePayloadJSON struct {
	ResourceSpans []resourceSpansJSON `json:"resourceSpans"`
}

type resourceSpansJSON struct {
	Resource   resourceJSON     `json:"resource"`
	ScopeSpans []scopeSpansJSON `json:"scopeSpans"`
}

type resourceJSON struct {
	Attributes []attrJSON `json:"attributes"`
}

type scopeSpansJSON struct {
	Scope scopeJSON  `json:"scope"`
	Spans []spanJSON `json:"spans"`
}

type scopeJSON struct {
	Name string `json:"name"`
}

type spanJSON struct {
	TraceID           string     `json:"traceId"`
	SpanID            string     `json:"spanId"`
	ParentSpanID      string     `json:"parentSpanId,omitempty"`
	Name              string     `json:"name"`
	Kind              int        `json:"kind"`
	StartTimeUnixNano string     `json:"startTimeUnixNano"`
	EndTimeUnixNano   string     `json:"endTimeUnixNano"`
	Attributes        []attrJSON `json:"attributes,omitempty"`
	Status            statusJSON `json:"status"`
}

type statusJSON struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

type attrJSON struct {
	Key   string    `json:"key"`
	Value valueJSON `json:"value"`
}

type valueJSON struct {
	String *string `json:"stringValue,omitempty"`
	Int    *string `json:"intValue,omitempty"`
	Bool   *bool   `json:"boolValue,omitempty"`
}

func stringAttrJSON(k, v string) attrJSON {
	return attrJSON{Key: k, Value: valueJSON{String: &v}}
}

func tracePayload(resource []attrJSON, batch []*Span) tracePayloadJSON {
	spans := make([]spanJSON, 0, len(batch))
	for _, s := range batch {
		spans = append(spans, encodeSpan(s))
	}
	return tracePayloadJSON{ResourceSpans: []resourceSpansJSON{{
		Resource: resourceJSON{Attributes: resource},
		ScopeSpans: []scopeSpansJSON{{
			Scope: scopeJSON{Name: "polyglot"},
			Spans: spans,
		}},
	}}}
}

func encodeSpan(s *Span) spanJSON {
	s.mu.Lock()
	attrs := make([]attrJSON, 0, len(s.attrs))
	for _, a := range s.attrs {
		attrs = append(attrs, encodeAttr(a))
	}
	out := spanJSON{
		TraceID:           s.TraceID.String(),
		SpanID:            s.SpanID.String(),
		Name:              s.Name,
		Kind:              int(s.Kind),
		StartTimeUnixNano: strconv.FormatInt(s.Start.UnixNano(), 10),
		EndTimeUnixNano:   strconv.FormatInt(s.Stop.UnixNano(), 10),
		Attributes:        attrs,
		Status:            statusJSON{Code: s.statusCode, Message: s.statusMsg},
	}
	s.mu.Unlock()
	if !s.ParentID.isZero() {
		out.ParentSpanID = s.ParentID.String()
	}
	return out
}

func encodeAttr(a Attr) attrJSON {
	switch a.Kind {
	case attrInt:
		v := strconv.FormatInt(a.Int, 10)
		return attrJSON{Key: a.Key, Value: valueJSON{Int: &v}}
	case attrBool:
		v := a.Bool
		return attrJSON{Key: a.Key, Value: valueJSON{Bool: &v}}
	default:
		v := a.Str
		return attrJSON{Key: a.Key, Value: valueJSON{String: &v}}
	}
}
