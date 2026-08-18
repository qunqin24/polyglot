package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	// The IANA zone database, embedded so normaliseTimezone works on a host
	// that ships no zone files at all. Polyglot's promise is one self-contained
	// static binary; validation that succeeds on one machine and fails on
	// another would break it. A system database still takes precedence, and
	// this lives here rather than in main so it cannot be dropped from the
	// binary while the code that depends on it stays behind.
	_ "time/tzdata"

	"github.com/qunqin24/polyglot/internal/auth"
	"github.com/qunqin24/polyglot/internal/protocol"
	"github.com/qunqin24/polyglot/internal/setup"
	"github.com/qunqin24/polyglot/internal/store"
	"github.com/qunqin24/polyglot/internal/version"
)

// minPasswordLen is the one password rule Polyglot enforces. Complexity rules
// push people towards worse passwords, so length is all we check.
const minPasswordLen = 8

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.AdminCount(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"needs_setup": n == 0,
		"version":     version.Version,
	})
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
	// Timezone is detected by the browser during first-run setup so the
	// operator does not have to pick one. It is optional: an install driven by
	// curl simply keeps the UTC default.
	Timezone string `json:"timezone"`
}

// defaultTimezone is what an instance uses until someone says otherwise. UTC
// is the honest choice: it is what every timestamp is actually stored in.
const defaultTimezone = "UTC"

// normaliseTimezone validates an IANA name and returns it, or an error naming
// the problem. An empty value means "unset", which is not an error.
//
// time.LoadLocation is the only real validator — it consults the same database
// the browser does. cmd/polyglot embeds time/tzdata so this works even on a
// host with no zone files installed.
func normaliseTimezone(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if len(v) > 64 {
		return "", fmt.Errorf("timezone name is too long")
	}
	if _, err := time.LoadLocation(v); err != nil {
		return "", fmt.Errorf("unknown timezone %q; use an IANA name such as Asia/Shanghai", v)
	}
	return v, nil
}

// timezone reads the configured zone, falling back to UTC. A read failure is
// not worth failing a request over: the caller only needs something to format
// timestamps with.
func (s *Server) timezone(ctx context.Context) string {
	tz, err := s.store.GetSetting(ctx, store.SettingTimezone)
	if err != nil || tz == "" {
		return defaultTimezone
	}
	return tz
}

// handleSetup creates the single local administrator. It is only reachable
// while no admin exists, so it cannot be used to add a second one.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.AdminCount(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	if n > 0 {
		writeErr(w, http.StatusConflict, "Polyglot has already been set up")
		return
	}
	if s.setupGuard == nil || !s.setupGuard.Valid(r.Header.Get(setup.HeaderName)) {
		writeErr(w, http.StatusForbidden, "invalid setup token")
		return
	}

	var in credentials
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if in.Username == "" {
		writeErr(w, http.StatusBadRequest, "username is required")
		return
	}
	if len(in.Password) < minPasswordLen {
		writeErr(w, http.StatusBadRequest, "password must be at least %d characters", minPasswordLen)
		return
	}

	tz, err := normaliseTimezone(in.Timezone)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "%v", err)
		return
	}

	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "hash password: %v", err)
		return
	}
	admin, err := s.store.CreateInitialAdmin(r.Context(), in.Username, hash)
	if err != nil {
		if errors.Is(err, store.ErrAlreadySetup) {
			writeErr(w, http.StatusConflict, "Polyglot has already been set up")
			return
		}
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	s.log.Info("administrator created", "username", admin.Username)
	if err := s.setupGuard.Consume(); err != nil {
		s.log.Warn("could not remove consumed setup token", "error", err)
	}

	// A bad zone must not cost the operator the account they just created, so
	// this is deliberately after CreateInitialAdmin and only logged if it fails.
	if tz != "" {
		if err := s.store.SetSetting(r.Context(), store.SettingTimezone, tz); err != nil {
			s.log.Warn("could not store the detected timezone", "timezone", tz, "error", err)
		}
	}

	cookies, err := auth.NewSession(r.Context(), s.store, admin.ID, r.UserAgent(), s.cfg.SecureCookies)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	for _, c := range cookies {
		http.SetCookie(w, c)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"username": admin.Username})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}

	admin, err := s.store.AdminByUsername(r.Context(), strings.TrimSpace(in.Username))
	if err != nil || !auth.CheckPassword(admin.PasswordHash, in.Password) {
		// One message for both cases: no username enumeration.
		writeErr(w, http.StatusUnauthorized, "incorrect username or password")
		return
	}

	cookies, err := auth.NewSession(r.Context(), s.store, admin.ID, r.UserAgent(), s.cfg.SecureCookies)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	for _, c := range cookies {
		http.SetCookie(w, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"username": admin.Username})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	for _, c := range auth.ClearSession(r.Context(), s.store, r, s.cfg.SecureCookies) {
		http.SetCookie(w, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	admin := auth.AdminFromContext(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"username":         admin.Username,
		"created_at":       admin.CreatedAt,
		"version":          version.Version,
		"data_dir":         s.cfg.DataDir,
		"log_retention":    s.cfg.LogRetentionDays,
		"dropped_logs":     s.usage.Dropped(),
		"upstream_timeout": s.cfg.UpstreamTimeout.String(),
		"timezone":         s.timezone(r.Context()),
	})
}

// handleUpdateSettings changes the instance settings an operator can edit.
// Today that is the display timezone.
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Timezone string `json:"timezone"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	tz, err := normaliseTimezone(in.Timezone)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "%v", err)
		return
	}
	if tz == "" {
		tz = defaultTimezone
	}
	if err := s.store.SetSetting(r.Context(), store.SettingTimezone, tz); err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"timezone": tz})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	admin := auth.AdminFromContext(r.Context())
	if !auth.CheckPassword(admin.PasswordHash, in.CurrentPassword) {
		writeErr(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	if len(in.NewPassword) < minPasswordLen {
		writeErr(w, http.StatusBadRequest, "password must be at least %d characters", minPasswordLen)
		return
	}
	hash, err := auth.HashPassword(in.NewPassword)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "hash password: %v", err)
		return
	}
	if err := s.store.UpdateAdminPassword(r.Context(), admin.ID, hash); err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	// Every existing session is invalidated, including this one.
	if err := s.store.DeleteSessionsForAdmin(r.Context(), admin.ID); err != nil {
		s.log.Error("clear sessions after password change", "error", err)
	}
	for _, c := range auth.ClearSession(r.Context(), s.store, r, s.cfg.SecureCookies) {
		http.SetCookie(w, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleProtocols tells the WebUI which protocols this build supports, so the
// dropdowns are never out of sync with the binary.
func (s *Server) handleProtocols(w http.ResponseWriter, r *http.Request) {
	names := protocol.Registered()
	out := make([]map[string]string, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]string{"name": string(n), "label": n.Display()})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.Stats(r.Context(), statsWindow(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// statsWindow reads the ?hours= the Overview picker sends. The ceiling is 90
// days, the same for every panel, so no query can be asked to walk more of the
// log table than the page offers.
func statsWindow(r *http.Request) time.Time {
	hours := 24
	if v := r.URL.Query().Get("hours"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 24*90 {
			hours = n
		}
	}
	return time.Now().Add(-time.Duration(hours) * time.Hour)
}

// The three handlers below back the Overview panels. Each is loaded when its
// panel is opened rather than folded into handleStats, so the numbers on the
// page everyone leaves open stay cheap to refresh.

func (s *Server) handleConversionStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.ConversionStats(r.Context(), statsWindow(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleLatencyStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.LatencyStats(r.Context(), statsWindow(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleCostStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.CostStats(r.Context(), statsWindow(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}
