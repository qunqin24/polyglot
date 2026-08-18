package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/qunqin24/polyglot/internal/router"
	"github.com/qunqin24/polyglot/internal/store"
)

// The model registry: the real models each provider offers, discovered
// automatically or added by hand. Clients call these ids directly; aliases are
// a separate, optional layer handled further down this file.

// handleAddProviderModels registers the models an operator picked for a
// provider that already exists. It is the same act as choosing models in the
// add dialog, so it goes through the same path — discovery never decides on its
// own what belongs in the registry.
func (s *Server) handleAddProviderModels(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid provider id")
		return
	}
	if _, err := s.store.GetProvider(r.Context(), id); err != nil {
		writeErr(w, storeErrStatus(err), "%v", err)
		return
	}
	var in struct {
		Models []DiscoveredChoice `json:"models"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	added, err := s.registerChosenModels(r.Context(), id, in.Models)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added})
}

// --- model registry -------------------------------------------------------

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ModelFilter{
		Search:      strings.TrimSpace(q.Get("search")),
		EnabledOnly: q.Get("enabled_only") == "1" || q.Get("enabled_only") == "true",
	}
	if v, err := strconv.ParseInt(q.Get("provider_id"), 10, 64); err == nil {
		f.ProviderID = v
	}
	if v, err := strconv.Atoi(q.Get("limit")); err == nil {
		f.Limit = v
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil {
		f.Offset = v
	}

	models, err := s.store.ListModels(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	total, err := s.store.CountModels(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	ambiguous, err := s.store.AmbiguousModelIDs(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	for _, m := range models {
		m.Ambiguous = ambiguous[m.UpstreamModelID]
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"models": orEmpty(models),
		"total":  total,
	})
}

type modelInput struct {
	ProviderID      int64  `json:"provider_id"`
	UpstreamModelID string `json:"upstream_model_id"`
	DisplayName     string `json:"display_name"`
	Enabled         *bool  `json:"enabled"`
}

// handleCreateModel adds a model by hand, for providers that cannot be
// discovered. A manual model is callable exactly like a discovered one.
func (s *Server) handleCreateModel(w http.ResponseWriter, r *http.Request) {
	var in modelInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	in.UpstreamModelID = strings.TrimSpace(in.UpstreamModelID)
	if in.ProviderID <= 0 {
		writeErr(w, http.StatusBadRequest, "provider_id is required")
		return
	}
	if in.UpstreamModelID == "" {
		writeErr(w, http.StatusBadRequest, "upstream_model_id is required")
		return
	}
	// "::" is how a client names a provider explicitly, so it cannot appear
	// inside a model id without making that syntax ambiguous.
	if strings.Contains(in.UpstreamModelID, router.NamespaceSeparator) {
		writeErr(w, http.StatusBadRequest,
			"a model id must not contain %q; that sequence selects a provider",
			router.NamespaceSeparator)
		return
	}
	if _, err := s.store.GetProvider(r.Context(), in.ProviderID); err != nil {
		writeErr(w, storeErrStatus(err), "%v", err)
		return
	}

	m := &store.Model{
		ProviderID:      in.ProviderID,
		UpstreamModelID: in.UpstreamModelID,
		DisplayName:     strings.TrimSpace(in.DisplayName),
		Enabled:         true,
	}
	if in.Enabled != nil {
		m.Enabled = *in.Enabled
	}

	created, err := s.store.CreateModel(r.Context(), m)
	if err != nil {
		if isUniqueViolation(err) {
			writeErr(w, http.StatusConflict, "this provider already has a model %q", in.UpstreamModelID)
			return
		}
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateModel(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid model id")
		return
	}
	var in struct {
		DisplayName string `json:"display_name"`
		Enabled     bool   `json:"enabled"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	m, err := s.store.UpdateModel(r.Context(), id, strings.TrimSpace(in.DisplayName), in.Enabled)
	if err != nil {
		writeErr(w, storeErrStatus(err), "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid model id")
		return
	}
	if err := s.store.DeleteModel(r.Context(), id); err != nil {
		writeErr(w, storeErrStatus(err), "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- aliases --------------------------------------------------------------
//
// An alias is a logical model name. It exists so a client can keep asking for
// "coding" while an operator repoints it at another provider or model. It is
// entirely optional: calling a real upstream model id needs no alias.

type aliasInput struct {
	Alias         string `json:"alias"`
	ProviderID    int64  `json:"provider_id"`
	UpstreamModel string `json:"upstream_model"`
	Priority      int    `json:"priority"`
	Enabled       *bool  `json:"enabled"`
}

func (in *aliasInput) validate() string {
	alias := strings.TrimSpace(in.Alias)
	if alias == "" {
		return "alias is required"
	}
	if strings.Contains(alias, router.NamespaceSeparator) {
		return "an alias must not contain \"" + router.NamespaceSeparator + "\""
	}
	if in.ProviderID <= 0 {
		return "provider_id is required"
	}
	if strings.TrimSpace(in.UpstreamModel) == "" {
		return "upstream_model is required"
	}
	return ""
}

func (in *aliasInput) toStore() *store.ModelAlias {
	a := &store.ModelAlias{
		Alias:         strings.TrimSpace(in.Alias),
		ProviderID:    in.ProviderID,
		UpstreamModel: strings.TrimSpace(in.UpstreamModel),
		Priority:      in.Priority,
		Enabled:       true,
	}
	if in.Enabled != nil {
		a.Enabled = *in.Enabled
	}
	return a
}

func (s *Server) handleListAliases(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListAliases(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, orEmpty(list))
}

func (s *Server) handleCreateAlias(w http.ResponseWriter, r *http.Request) {
	var in aliasInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if msg := in.validate(); msg != "" {
		writeErr(w, http.StatusBadRequest, "%s", msg)
		return
	}
	a, err := s.store.CreateAlias(r.Context(), in.toStore())
	if err != nil {
		if isUniqueViolation(err) {
			writeErr(w, http.StatusConflict, "this alias already points at that provider and model")
			return
		}
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) handleUpdateAlias(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid alias id")
		return
	}
	var in aliasInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if msg := in.validate(); msg != "" {
		writeErr(w, http.StatusBadRequest, "%s", msg)
		return
	}
	a, err := s.store.UpdateAlias(r.Context(), id, in.toStore())
	if err != nil {
		writeErr(w, storeErrStatus(err), "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleDeleteAlias(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid alias id")
		return
	}
	if err := s.store.DeleteAlias(r.Context(), id); err != nil {
		writeErr(w, storeErrStatus(err), "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
