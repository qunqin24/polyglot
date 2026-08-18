// Package usage buffers request logs in memory and flushes them to SQLite in
// batches. One row per completed request — never one per streamed chunk.
package usage

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/qunqin24/polyglot/internal/pricing"
	"github.com/qunqin24/polyglot/internal/store"
)

// Pricer turns a finished request into money. internal/pricing implements it;
// a nil one simply leaves every cost unknown.
type Pricer interface {
	CostOf(providerID int64, model string, tk pricing.Tokens) (*float64, pricing.Source, string)
}

// SpendRecorder is told what a request cost as soon as its price is known, so
// a key with a budget does not have to re-read the log to know where it
// stands. internal/auth implements it; a nil one means no key has a budget to
// keep track of.
type SpendRecorder interface {
	RecordSpend(keyID int64, usd float64)
}

const (
	bufferSize    = 1024
	flushInterval = 2 * time.Second
	maxBatch      = 128
)

// Logger owns the buffer. Log never blocks the request path: if the buffer is
// full the record is dropped and counted, because slowing down proxying to
// write analytics would be the wrong trade.
type Logger struct {
	st     *store.Store
	ch     chan *store.RequestLog
	log    *slog.Logger
	prices Pricer
	spend  SpendRecorder

	retention time.Duration

	mu      sync.Mutex
	dropped int64

	done chan struct{}
	once sync.Once
}

// OnSpend routes every price this logger computes to a recorder as well as to
// the log row. Optional: without it costs are still logged, they just are not
// counted against any budget.
func (l *Logger) OnSpend(r SpendRecorder) { l.spend = r }

func New(st *store.Store, log *slog.Logger, retentionDays int, prices Pricer) *Logger {
	return &Logger{
		st:        st,
		ch:        make(chan *store.RequestLog, bufferSize),
		log:       log,
		prices:    prices,
		retention: time.Duration(retentionDays) * 24 * time.Hour,
		done:      make(chan struct{}),
	}
}

func (l *Logger) Log(rec *store.RequestLog) {
	select {
	case l.ch <- rec:
	default:
		l.mu.Lock()
		l.dropped++
		n := l.dropped
		l.mu.Unlock()
		if n%100 == 1 {
			l.log.Warn("request log buffer full, dropping records", "dropped_total", n)
		}
	}
}

// Dropped reports how many records the buffer discarded, surfaced in Settings
// so a saturated logger is visible rather than silent.
func (l *Logger) Dropped() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dropped
}

// Run flushes until ctx is cancelled, then drains what is left.
func (l *Logger) Run(ctx context.Context) {
	defer close(l.done)

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	var prune <-chan time.Time
	if l.retention > 0 {
		pt := time.NewTicker(6 * time.Hour)
		defer pt.Stop()
		prune = pt.C
		l.prune(ctx)
	}

	batch := make([]*store.RequestLog, 0, maxBatch)
	flush := func(c context.Context) {
		if len(batch) == 0 {
			return
		}
		if err := l.st.InsertRequestLogs(c, batch); err != nil {
			l.log.Error("write request logs", "error", err, "count", len(batch))
		}
		batch = batch[:0]
	}

	for {
		select {
		case rec := <-l.ch:
			l.price(rec)
			batch = append(batch, rec)
			if len(batch) >= maxBatch {
				flush(ctx)
			}
		case <-ticker.C:
			flush(ctx)
		case <-prune:
			l.prune(ctx)
		case <-ctx.Done():
			// Drain whatever is buffered with a fresh context: the request
			// context is already cancelled during shutdown.
			drain, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			for {
				select {
				case rec := <-l.ch:
					l.price(rec)
					batch = append(batch, rec)
					if len(batch) >= maxBatch {
						flush(drain)
					}
					continue
				default:
				}
				break
			}
			flush(drain)
			cancel()
			return
		}
	}
}

// Wait blocks until Run has drained, so shutdown does not lose the last logs.
func (l *Logger) Wait(timeout time.Duration) {
	l.once.Do(func() {
		select {
		case <-l.done:
		case <-time.After(timeout):
		}
	})
}

// price works out what a record cost, on the flush goroutine rather than in
// the request. The lookup is two map reads and would be harmless inline, but
// "logging never costs the request anything" is worth keeping structural
// rather than true by measurement.
//
// A request that reported no tokens is left unpriced. Its cost is unknown, not
// zero: an upstream that failed mid-stream, or answered without a usage block,
// may well have charged for what it did produce, and writing 0 would state
// that it did not.
func (l *Logger) price(rec *store.RequestLog) {
	if l.prices == nil || rec.ProviderID == nil || rec.UpstreamModel == "" {
		return
	}
	if rec.InputTokens == 0 && rec.OutputTokens == 0 {
		return
	}
	usd, src, note := l.prices.CostOf(*rec.ProviderID, rec.UpstreamModel, pricing.Tokens{
		Input:       rec.InputTokens,
		CachedInput: rec.CachedInputTokens,
		CacheWrite:  rec.CacheWriteTokens,
		Output:      rec.OutputTokens,
	})
	if usd == nil {
		return
	}
	rec.CostUSD = usd
	rec.CostSource = string(src)
	rec.CostNote = note
	if l.spend != nil && rec.APIKeyID != nil {
		l.spend.RecordSpend(*rec.APIKeyID, *usd)
	}
}

func (l *Logger) prune(ctx context.Context) {
	if l.retention <= 0 {
		return
	}
	n, err := l.st.PruneRequestLogs(ctx, time.Now().Add(-l.retention))
	if err != nil {
		l.log.Error("prune request logs", "error", err)
		return
	}
	if n > 0 {
		l.log.Info("pruned old request logs", "rows", n)
	}
}
