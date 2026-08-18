package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/store"
)

// logView is the wire shape of a request log. Polyglot stores no prompt or
// completion text, so there is nothing here to redact.
//
// Fidelity is always an array, never null: most requests convert cleanly and
// have no notes at all.
type logView struct {
	*store.RequestLog
	Fidelity []canonical.Note `json:"fidelity"`
}

func (s *Server) handleListLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.LogFilter{
		Status:    q.Get("status"),
		Model:     q.Get("model"),
		Protocol:  q.Get("protocol"),
		ClientIP:  q.Get("client_ip"),
		ClientApp: q.Get("client_app"),
	}
	if v, err := strconv.Atoi(q.Get("limit")); err == nil {
		f.Limit = v
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v > 0 {
		f.Offset = v
	}
	if v, err := strconv.ParseInt(q.Get("before"), 10, 64); err == nil {
		f.Before = v
	}
	if v, err := strconv.ParseInt(q.Get("provider_id"), 10, 64); err == nil {
		f.ProviderID = v
	}

	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}

	logs, err := s.store.ListRequestLogs(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	total, err := s.store.CountRequestLogs(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	hasMore := int64(f.Offset+len(logs)) < total
	writeJSON(w, http.StatusOK, map[string]any{
		"logs": orEmpty(logs), "has_more": hasMore, "total": total,
	})
}

func (s *Server) handleGetLog(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid log id")
		return
	}
	l, err := s.store.GetRequestLog(r.Context(), id)
	if err != nil {
		writeErr(w, storeErrStatus(err), "%v", err)
		return
	}
	view := logView{RequestLog: l, Fidelity: []canonical.Note{}}
	if l.FidelityNotes != "" {
		if err := json.Unmarshal([]byte(l.FidelityNotes), &view.Fidelity); err != nil {
			s.log.Warn("decode stored fidelity notes", "log_id", id, "error", err)
		}
		if view.Fidelity == nil {
			view.Fidelity = []canonical.Note{}
		}
	}
	writeJSON(w, http.StatusOK, view)
}

// handleKeyOrigins lists the addresses one API key has been used from.
//
// This is the shape the "has my key leaked" question actually takes. Scrolling
// request logs will not answer it: one unfamiliar address among thousands of
// rows is invisible, while the same address grouped and counted is obvious.
func (s *Server) handleKeyOrigins(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid key id")
		return
	}
	days := 30
	if v, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && v > 0 && v <= 365 {
		days = v
	}
	origins, err := s.store.APIKeyOrigins(r.Context(), id, time.Now().AddDate(0, 0, -days), 20)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"origins": orEmpty(origins), "days": days})
}

// handleModelStats reports how each model has actually behaved.
//
// It is a separate call from the model list on purpose: the list is what the
// page needs to render, and an aggregate over the log should never be what
// decides whether the page appears at all.
func (s *Server) handleModelStats(w http.ResponseWriter, r *http.Request) {
	hours := 24
	if v, err := strconv.Atoi(r.URL.Query().Get("hours")); err == nil && v > 0 && v <= 24*30 {
		hours = v
	}
	stats, err := s.store.ModelStats(r.Context(), time.Now().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stats": orEmpty(stats), "hours": hours})
}
