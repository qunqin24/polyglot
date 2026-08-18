package telemetry

import (
	"crypto/rand"
	"encoding/hex"
	"math/big"
	"strings"
	"sync"
	"time"
)

// Tracing follows the OpenTelemetry data model — trace and span ids, a parent
// link, timestamps, typed attributes and a status — because that is what every
// backend on the other end expects. The span type here is the OTLP span with
// the fields Polyglot actually sets; the exporter serialises it as OTLP.
//
// Spans are deliberately coarse. A gateway request produces three or four,
// never one per token: a span per delta would cost more than the proxying.

// SpanKind mirrors the OTLP enumeration.
type SpanKind int

const (
	SpanInternal SpanKind = 1
	SpanServer   SpanKind = 2
	SpanClient   SpanKind = 3
)

// SpanStatus mirrors the OTLP status codes.
const (
	statusUnset = 0
	statusOK    = 1
	statusError = 2
)

// TraceID and SpanID are raw ids; they are rendered as hex on the wire.
type (
	TraceID [16]byte
	SpanID  [8]byte
)

func (t TraceID) String() string { return hex.EncodeToString(t[:]) }
func (s SpanID) String() string  { return hex.EncodeToString(s[:]) }

func (t TraceID) isZero() bool { return t == TraceID{} }
func (s SpanID) isZero() bool  { return s == SpanID{} }

// Attr is one span attribute. Only strings, ints and bools are needed, so the
// value is kept as a small tagged union rather than an `any`.
type Attr struct {
	Key  string
	Str  string
	Int  int64
	Bool bool
	Kind attrKind
}

type attrKind uint8

const (
	attrString attrKind = iota
	attrInt
	attrBool
)

func StringAttr(k, v string) Attr { return Attr{Key: k, Str: v, Kind: attrString} }
func IntAttr(k string, v int64) Attr {
	return Attr{Key: k, Int: v, Kind: attrInt}
}
func BoolAttr(k string, v bool) Attr { return Attr{Key: k, Bool: v, Kind: attrBool} }

// Span is one unit of work. A nil *Span is valid and does nothing, which is
// what callers get when tracing is off — so the request path needs no branches
// of its own.
type Span struct {
	tracer *tracer

	TraceID  TraceID
	SpanID   SpanID
	ParentID SpanID
	Name     string
	Kind     SpanKind

	Start time.Time
	Stop  time.Time

	mu         sync.Mutex
	attrs      []Attr
	statusCode int
	statusMsg  string
	ended      bool
}

// SetAttributes attaches attributes. Callers must pass only bounded, non-
// sensitive values: a span is exported to whatever collector the operator
// configured, so prompts and credentials must never reach it.
func (s *Span) SetAttributes(attrs ...Attr) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.attrs = append(s.attrs, attrs...)
	s.mu.Unlock()
}

// SetError marks the span failed. The message must already be a bounded error
// class, not an upstream body.
func (s *Span) SetError(class string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.statusCode = statusError
	s.statusMsg = class
	s.mu.Unlock()
}

// End finishes the span and hands it to the exporter. Calling End twice is
// harmless: the second call does nothing, which is what keeps a deferred End
// safe next to an explicit one on the error path.
func (s *Span) End() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	s.Stop = time.Now()
	s.mu.Unlock()
	s.tracer.emit(s)
}

// SpanContext is what travels from a parent span to a child.
type SpanContext struct {
	TraceID TraceID
	SpanID  SpanID
	Sampled bool
}

func (s *Span) Context() SpanContext {
	if s == nil {
		return SpanContext{}
	}
	return SpanContext{TraceID: s.TraceID, SpanID: s.SpanID, Sampled: true}
}

type tracer struct {
	exporter spanExporter
	ratio    float64
}

// spanExporter is the seam every backend plugs into. OTLP over HTTP is the one
// implementation today; a second one is a new type here and nothing else.
type spanExporter interface {
	Export(*Span)
	Shutdown(timeout time.Duration)
}

func (t *tracer) start(parent SpanContext, name string, kind SpanKind, attrs ...Attr) *Span {
	if t == nil {
		return nil
	}
	s := &Span{tracer: t, Name: name, Kind: kind, Start: time.Now(), attrs: attrs}
	if !parent.TraceID.isZero() {
		// Parent-based sampling: a caller that decided not to sample this
		// trace has decided for us too, and a half-exported trace is worse
		// than none.
		if !parent.Sampled {
			return nil
		}
		s.TraceID = parent.TraceID
		s.ParentID = parent.SpanID
	} else {
		if !t.sample() {
			return nil
		}
		if _, err := rand.Read(s.TraceID[:]); err != nil {
			return nil
		}
	}
	if _, err := rand.Read(s.SpanID[:]); err != nil {
		return nil
	}
	return s
}

// sample is head sampling on the root span only: once a trace is kept, every
// span in it is kept, so a trace is never half missing.
func (t *tracer) sample() bool {
	if t.ratio >= 1 {
		return true
	}
	if t.ratio <= 0 {
		return false
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1<<32))
	if err != nil {
		return true
	}
	return float64(n.Int64())/float64(1<<32) < t.ratio
}

func (t *tracer) emit(s *Span) {
	if t == nil || t.exporter == nil {
		return
	}
	t.exporter.Export(s)
}

// --- W3C trace context ----------------------------------------------------

// ParseTraceparent reads the W3C traceparent header so a trace that started in
// the caller's system continues through Polyglot instead of restarting here.
//
//	version-traceid-spanid-flags   e.g. 00-<32 hex>-<16 hex>-01
//
// Anything malformed returns the zero context and a new trace begins, which is
// the specified behaviour and also stops a bad header from being interesting
// to an attacker.
func ParseTraceparent(h string) SpanContext {
	parts := strings.Split(strings.TrimSpace(h), "-")
	if len(parts) != 4 || len(parts[0]) != 2 || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return SpanContext{}
	}
	if parts[0] == "ff" { // reserved: never a valid version
		return SpanContext{}
	}
	var sc SpanContext
	if _, err := hex.Decode(sc.TraceID[:], []byte(parts[1])); err != nil {
		return SpanContext{}
	}
	if _, err := hex.Decode(sc.SpanID[:], []byte(parts[2])); err != nil {
		return SpanContext{}
	}
	flags, err := hex.DecodeString(parts[3])
	if err != nil {
		return SpanContext{}
	}
	if sc.TraceID.isZero() || sc.SpanID.isZero() {
		return SpanContext{}
	}
	sc.Sampled = flags[0]&0x01 == 1
	return sc
}
