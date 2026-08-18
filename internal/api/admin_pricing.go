package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/qunqin24/polyglot/internal/pricing"
	"github.com/qunqin24/polyglot/internal/store"
)

// The pricing surface: what every registered model costs, where that number
// came from, and which models have no price at all.
//
// It is cost visibility and nothing more. Nothing here deducts a balance or
// refuses a request — Polyglot converts protocols, it does not sell tokens.

// PricedModel is one registry row with the price in force on it. The embedded
// model carries the operator's override; Effective is that override laid over
// the catalog, which is what a request is actually charged at.
type PricedModel struct {
	*store.Model
	Effective pricing.Rates  `json:"effective"`
	Source    pricing.Source `json:"source"`
}

func (s *Server) handleListPricing(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ModelFilter{Search: strings.TrimSpace(q.Get("search"))}
	if v, err := strconv.ParseInt(q.Get("provider_id"), 10, 64); err == nil {
		f.ProviderID = v
	}
	f.Limit = 2000

	models, err := s.store.ListModels(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}

	// Never a nil slice: it marshals to null and every typed client would have
	// to guard against it.
	out := make([]PricedModel, 0, len(models))
	unpriced := 0
	filter := q.Get("filter")
	for _, m := range models {
		rates, src := s.prices.Resolve(m.ProviderID, m.UpstreamModelID)
		if src == pricing.SourceUnknown {
			unpriced++
		}
		switch filter {
		case "unpriced":
			if src != pricing.SourceUnknown {
				continue
			}
		case "custom":
			if src != pricing.SourceOverride {
				continue
			}
		}
		out = append(out, PricedModel{Model: m, Effective: rates, Source: src})
	}

	status, err := s.store.CatalogStatus(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"models": out,
		// Counted over every model, not over the filtered page, so the number
		// does not change when the operator narrows the view.
		"unpriced": unpriced,
		"total":    len(models),
		"catalog":  status,
	})
}

// handleSetModelPrice stores what the operator typed.
//
// Every field is optional and a null clears it, putting that number back on
// the catalog. That is the whole reason an override is four nullable numbers
// rather than a copy of the row the form displayed: an operator correcting one
// price still tracks an official cut in the others.
func (s *Server) handleSetModelPrice(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid model id")
		return
	}
	var in pricing.Price
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	for _, v := range []*float64{in.Input, in.Output, in.CacheRead, in.CacheWrite} {
		if v != nil && (*v < 0 || *v > 1e6) {
			writeErr(w, http.StatusBadRequest, "price must be between 0 and 1000000 per million tokens")
			return
		}
	}

	m, err := s.store.SetModelPrice(r.Context(), id, in)
	if err != nil {
		writeErr(w, storeErrStatus(err), "%v", err)
		return
	}
	s.reloadPrices(r)

	rates, src := s.prices.Resolve(m.ProviderID, m.UpstreamModelID)
	writeJSON(w, http.StatusOK, PricedModel{Model: m, Effective: rates, Source: src})
}

func (s *Server) handleCatalogStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.store.CatalogStatus(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"catalog": status})
}

// handleRefreshCatalog pulls the current prices from models.dev.
//
// A failure is information, not something the operator has to fix: the catalog
// they already have stays loaded, prices keep resolving, and the message says
// what went wrong. The same rule a failed model listing follows.
func (s *Server) handleRefreshCatalog(w http.ResponseWriter, r *http.Request) {
	snap, err := pricing.Fetch(r.Context())
	if err != nil {
		s.log.Warn("refresh price catalog", "error", err)
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := s.store.ReplaceCatalog(r.Context(), snap, "models.dev"); err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	s.reloadPrices(r)

	status, err := s.store.CatalogStatus(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "catalog": status})
}

// reloadPrices refreshes the in-memory snapshot the request logger prices from.
// A failure leaves the previous snapshot in place, which is stale rather than
// wrong, so it is logged and never returned: the edit itself succeeded.
func (s *Server) reloadPrices(r *http.Request) {
	if err := s.prices.Reload(r.Context()); err != nil {
		s.log.Warn("reload model prices", "error", err)
	}
}
