package api

import (
	"net/http"
	"time"

	"github.com/qunqin24/polyglot/internal/pricing"
	"github.com/qunqin24/polyglot/internal/store"
)

// spendResponse is the Overview's spend report as the WebUI draws it.
type spendResponse struct {
	*store.SpendStats
	Range string `json:"range"`
	// PeriodEnd is where the period would end if it ran its course — the end
	// of the month for "month" — so the page can project the spend to it.
	PeriodEnd int64 `json:"period_end"`
	// CacheSavingsUSD is what the cached input would have cost at the full
	// input price, less what it cost at the cache-read price. Only models with
	// both prices count; CacheSavingsPartial says some cached input was left
	// out because one of the two was unknown.
	CacheSavingsUSD     float64 `json:"cache_savings_usd"`
	CacheSavingsPartial bool    `json:"cache_savings_partial"`
	// Names are display names for the model ids in Rows and Stacks, from the
	// models.dev catalog. An id it does not know is absent.
	Names map[string]string `json:"names"`
	// RetainedSince is set when request logs older than the retention setting
	// have been pruned and the previous period reaches back before that: its
	// total is then only part of the story, and no comparison should be drawn
	// from it. Unix seconds.
	RetainedSince *int64 `json:"retained_since,omitempty"`
}

// spendPeriod turns ?range= into a window aligned to the instance's timezone:
// a day starts at local midnight and "this month" at the local first.
func spendPeriod(rng string, now time.Time, loc *time.Location) (store.SpendPeriod, time.Time, string) {
	local := now.In(loc)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	_, offset := local.Zone()
	day := 24 * time.Hour
	switch rng {
	case "24h":
		from := now.Add(-24 * time.Hour).Truncate(time.Hour)
		return store.SpendPeriod{From: from, To: now, PrevFrom: from.Add(-24 * time.Hour), Bucket: time.Hour, Offset: offset},
			from.Add(24 * time.Hour), rng
	case "7d", "30d":
		days := 7
		if rng == "30d" {
			days = 30
		}
		from := midnight.AddDate(0, 0, -(days - 1))
		return store.SpendPeriod{From: from, To: now, PrevFrom: from.AddDate(0, 0, -days), Bucket: day, Offset: offset},
			from.AddDate(0, 0, days), rng
	default:
		from := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
		return store.SpendPeriod{From: from, To: now, PrevFrom: from.AddDate(0, -1, 0), Bucket: day, Offset: offset},
			from.AddDate(0, 1, 0), "month"
	}
}

func (s *Server) handleSpendStats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	loc, err := time.LoadLocation(s.timezone(r.Context()))
	if err != nil {
		loc = time.UTC
	}
	period, end, rng := spendPeriod(q.Get("range"), time.Now(), loc)

	dim := store.SpendDimension(q.Get("dim"))
	if dim == "" {
		dim = store.SpendByModel
	}
	stats, err := s.team().SpendStats(r.Context(), period, dim)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "%v", err)
		return
	}

	resp := spendResponse{SpendStats: stats, Range: rng, PeriodEnd: end.Unix(), Names: map[string]string{}}
	if days := s.settings.Get().LogRetentionDays; days > 0 {
		cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
		if period.PrevFrom.Before(cutoff) {
			at := cutoff.Unix()
			resp.RetainedSince = &at
		}
	}
	for _, c := range stats.Cached {
		rates, src := s.prices.Resolve(c.ProviderID, c.Model)
		in, read := rates.Price.Input, rates.Price.CacheRead
		if src == pricing.SourceUnknown || in == nil || read == nil {
			resp.CacheSavingsPartial = true
			continue
		}
		if *in > *read {
			resp.CacheSavingsUSD += float64(c.CachedTokens) * (*in - *read) / 1e6
		}
	}

	if dim == store.SpendByModel {
		catalog, err := s.store.CatalogModels(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "%v", err)
			return
		}
		for _, row := range stats.Rows {
			if c, ok := catalog[pricing.Normalize(row.Key)]; ok && c.Name != "" {
				resp.Names[row.Key] = c.Name
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
