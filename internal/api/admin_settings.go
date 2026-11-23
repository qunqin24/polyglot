package api

import (
	"net/http"
	"reflect"
	"strings"

	"github.com/qunqin24/polyglot/internal/settings"
)

// The settings that used to be environment variables. Everything here applies
// to the next request except metrics and tracing, which are built when the
// process starts; the response says when a saved change is still waiting for
// that restart.

type runtimeSettingsResponse struct {
	Settings settings.Values `json:"settings"`
	// RestartPending is true when a saved metrics or tracing change has not
	// taken effect yet because the process has not restarted since.
	RestartPending bool `json:"restart_pending"`
}

func (s *Server) runtimeSettingsBody() runtimeSettingsResponse {
	cur := s.settings.Get()
	return runtimeSettingsResponse{Settings: cur, RestartPending: telemetryDiffers(s.startSettings, cur)}
}

func telemetryDiffers(a, b settings.Values) bool {
	return a.MetricsEnabled != b.MetricsEnabled ||
		a.TracingEnabled != b.TracingEnabled ||
		a.OTLPEndpoint != b.OTLPEndpoint ||
		!reflect.DeepEqual(nonNil(a.OTLPHeaders), nonNil(b.OTLPHeaders)) ||
		a.TraceSampleRatio != b.TraceSampleRatio ||
		a.ServiceName != b.ServiceName
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func (s *Server) handleRuntimeSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.runtimeSettingsBody())
}

// handleUpdateRuntimeSettings replaces every editable value. The metrics token
// is not one of them: it changes only through handleRegenerateMetricsToken, so
// a form that never showed it cannot blank it by accident.
func (s *Server) handleUpdateRuntimeSettings(w http.ResponseWriter, r *http.Request) {
	var in settings.Values
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	in.MetricsToken = s.settings.Get().MetricsToken
	if err := s.settings.Update(r.Context(), in); err != nil {
		writeErr(w, http.StatusBadRequest, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, s.runtimeSettingsBody())
}

// handleRegenerateMetricsToken issues a new scrape credential. The old one
// stops working immediately, so whatever scrapes /metrics has to be updated.
func (s *Server) handleRegenerateMetricsToken(w http.ResponseWriter, r *http.Request) {
	v := s.settings.Get()
	v.MetricsToken = settings.NewMetricsToken()
	if err := s.settings.Update(r.Context(), v); err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, s.runtimeSettingsBody())
}

// secureCookies decides the Secure flag for the admin session cookie from the
// request itself, so no setting is needed: a request that arrived over TLS,
// or through a trusted proxy that says it did, gets a cookie the browser will
// only send back over HTTPS.
func (s *Server) secureCookies(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return s.settings.Get().TrustProxyHeaders &&
		strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}
