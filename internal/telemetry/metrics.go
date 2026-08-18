package telemetry

import (
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// The metric registry. It is deliberately small: counters, one gauge and
// histograms, kept in memory and rendered on demand in the Prometheus text
// exposition format. Polyglot owns the wire format here for the same reason it
// owns the LLM wire formats — the format is stable and documented, while a
// client library would drag a dependency tree into a binary whose whole point
// is being one small static file.
//
// Everything in here must be safe to call from many request goroutines at once
// and must never block on I/O.

type metricKind int

const (
	kindCounter metricKind = iota
	kindGauge
	kindHistogram
)

func (k metricKind) String() string {
	switch k {
	case kindCounter:
		return "counter"
	case kindGauge:
		return "gauge"
	default:
		return "histogram"
	}
}

// defaultMaxSeries caps how many distinct label combinations one metric may
// hold. Past the cap, samples are folded into a single overflow series rather
// than growing without bound: an unbounded label is the one failure mode that
// turns observability into an outage.
const defaultMaxSeries = 2000

// overflowLabel replaces every label value once a metric hits its series cap.
const overflowLabel = "__overflow__"

// labelSep joins label values into a map key. It is a byte that cannot appear
// in a sanitised label value, so two different label sets can never collide.
const labelSep = "\xff"

type registry struct {
	mu        sync.RWMutex
	order     []*metric
	byName    map[string]*metric
	maxSeries int

	inFlight atomic.Int64
	// droppedSeries counts samples folded into the overflow series, exported
	// as a metric of its own so a saturated registry is visible.
	droppedSeries atomic.Int64
	droppedSpans  atomic.Int64
}

func newRegistry(maxSeries int) *registry {
	if maxSeries <= 0 {
		maxSeries = defaultMaxSeries
	}
	return &registry{byName: map[string]*metric{}, maxSeries: maxSeries}
}

type metric struct {
	name    string
	help    string
	kind    metricKind
	labels  []string
	buckets []float64 // histogram only, ascending, without +Inf

	reg *registry

	mu     sync.Mutex
	series map[string]*sample
}

type sample struct {
	labels []string
	value  float64
	// histogram state
	counts []uint64
	sum    float64
	count  uint64
}

func (r *registry) register(kind metricKind, name, help string, labels []string, buckets []float64) *metric {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.byName[name]; ok {
		return m
	}
	m := &metric{
		name:    name,
		help:    help,
		kind:    kind,
		labels:  labels,
		buckets: buckets,
		reg:     r,
		series:  map[string]*sample{},
	}
	r.byName[name] = m
	r.order = append(r.order, m)
	return m
}

func (r *registry) counter(name, help string, labels ...string) *metric {
	return r.register(kindCounter, name, help, labels, nil)
}

func (r *registry) histogram(name, help string, buckets []float64, labels ...string) *metric {
	return r.register(kindHistogram, name, help, labels, buckets)
}

// lookup finds or creates the series for a label set, enforcing the cap.
// It must be called with m.mu held.
func (m *metric) lookup(values []string) *sample {
	key := strings.Join(values, labelSep)
	if s, ok := m.series[key]; ok {
		return s
	}
	if len(m.series) >= m.reg.maxSeries {
		m.reg.droppedSeries.Add(1)
		over := make([]string, len(m.labels))
		for i := range over {
			over[i] = overflowLabel
		}
		key = strings.Join(over, labelSep)
		if s, ok := m.series[key]; ok {
			return s
		}
		values = over
	}
	s := &sample{labels: append([]string(nil), values...)}
	if m.kind == kindHistogram {
		s.counts = make([]uint64, len(m.buckets)+1) // +1 for +Inf
	}
	m.series[key] = s
	return s
}

// add increments a counter. Label values must be given in the order declared
// at registration; a mismatch is silently ignored rather than panicking,
// because telemetry must never take a request down with it.
func (m *metric) add(v float64, values ...string) {
	if m == nil || len(values) != len(m.labels) {
		return
	}
	m.mu.Lock()
	m.lookup(values).value += v
	m.mu.Unlock()
}

func (m *metric) inc(values ...string) { m.add(1, values...) }

// observe records one value in a histogram.
func (m *metric) observe(v float64, values ...string) {
	if m == nil || len(values) != len(m.labels) || math.IsNaN(v) || math.IsInf(v, 0) {
		return
	}
	m.mu.Lock()
	s := m.lookup(values)
	s.sum += v
	s.count++
	// Cumulative buckets: Prometheus histograms are "less than or equal", and
	// SearchFloat64s returns the first bucket whose bound is >= v.
	for i := sort.SearchFloat64s(m.buckets, v); i < len(s.counts); i++ {
		s.counts[i]++
	}
	m.mu.Unlock()
}

// snapshot copies a metric's series for rendering, so the exposition never
// holds a lock while writing to a network connection.
func (m *metric) snapshot() []sample {
	m.mu.Lock()
	out := make([]sample, 0, len(m.series))
	for _, s := range m.series {
		c := sample{labels: s.labels, value: s.value, sum: s.sum, count: s.count}
		if s.counts != nil {
			c.counts = append([]uint64(nil), s.counts...)
		}
		out = append(out, c)
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		return strings.Join(out[i].labels, labelSep) < strings.Join(out[j].labels, labelSep)
	})
	return out
}

// Histogram bucket sets. They are chosen for what an LLM gateway actually
// sees: requests measured in seconds to minutes, not milliseconds.
var (
	durationBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300}
	ttftBuckets     = []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 4, 8, 15, 30, 60}
	tpsBuckets      = []float64{1, 5, 10, 20, 40, 60, 80, 120, 200, 400, 800}
)
