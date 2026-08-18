package pricing

import (
	"context"
	"strings"
	"sync"
)

// ModelKey identifies one model on one provider — the same pair the registry
// is keyed by. Two providers offering the same model id are two different
// things to buy, so an override set on one must never leak to the other.
type ModelKey struct {
	ProviderID int64
	Model      string
}

// Store is the pricing data a Resolver reads. internal/store implements it.
type Store interface {
	CatalogPrices(ctx context.Context) (map[string]Rates, error)
	ModelPriceOverrides(ctx context.Context) (map[ModelKey]Price, error)
}

// Resolver answers what a model costs, from a snapshot it holds in memory.
//
// It is read on the path that writes request logs, which runs behind the usage
// buffer rather than inside the request, and the whole data set is a few
// hundred catalog rows plus one row per registered model. Reading SQLite for
// every logged request to look up two numbers would be the wrong trade, so the
// tables are loaded once and reloaded when something changes them.
type Resolver struct {
	st Store

	mu       sync.RWMutex
	catalog  map[string]Rates
	override map[ModelKey]Price
}

func NewResolver(st Store) *Resolver {
	return &Resolver{
		st:       st,
		catalog:  map[string]Rates{},
		override: map[ModelKey]Price{},
	}
}

// Reload refreshes the snapshot. Callers run it at startup and after anything
// that changes a price — an edit, or a catalog refresh.
func (r *Resolver) Reload(ctx context.Context) error {
	catalog, err := r.st.CatalogPrices(ctx)
	if err != nil {
		return err
	}
	override, err := r.st.ModelPriceOverrides(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.catalog, r.override = catalog, override
	r.mu.Unlock()
	return nil
}

// Resolve returns the price schedule in force for a model and where it came
// from.
//
// The catalog is consulted by normalised model id alone: models.dev is asked
// what the vendor charges, not what this particular provider charges, because
// a reseller's own margin is not something any catalog can tell us. An
// operator who pays less says so with an override.
//
// An override drops the long-context tier along with the rest of the vendor's
// schedule. The operator stated one set of numbers; carrying a tier they never
// mentioned would charge them a multiple they did not agree to, and inventing
// one from their base price would be worse. An overridden model is flat.
func (r *Resolver) Resolve(providerID int64, model string) (Rates, Source) {
	key := ModelKey{ProviderID: providerID, Model: strings.TrimSpace(model)}

	r.mu.RLock()
	base, inCatalog := r.catalog[Normalize(model)]
	over, hasOverride := r.override[key]
	r.mu.RUnlock()

	if hasOverride && !over.Empty() {
		return Rates{Price: base.Price.Overlay(over)}, SourceOverride
	}
	if inCatalog {
		return base, SourceCatalog
	}
	return Rates{}, SourceUnknown
}

// CostOf prices one finished request. A nil cost is an unknown cost: no price
// was in force, or the one that was could not produce a number. It is never
// reported as zero, which would say the request was free.
func (r *Resolver) CostOf(providerID int64, model string, tk Tokens) (usd *float64, src Source, note string) {
	rates, source := r.Resolve(providerID, model)
	if source == SourceUnknown {
		return nil, SourceUnknown, ""
	}
	cost, ok := Compute(rates, tk)
	if !ok {
		return nil, SourceUnknown, ""
	}
	v := Round(cost.USD)
	return &v, source, strings.Join(cost.Notes, ",")
}
