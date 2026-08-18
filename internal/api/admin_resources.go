package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/qunqin24/polyglot/internal/auth"
	"github.com/qunqin24/polyglot/internal/protocol"
	"github.com/qunqin24/polyglot/internal/provider"
	"github.com/qunqin24/polyglot/internal/store"
)

// --- providers ------------------------------------------------------------

// maxProviderNote bounds the operator's own description of a provider.
const maxProviderNote = 500

type providerInput struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url"`
	// Note is the operator's own description. Free text, stored and shown
	// back, read by nothing else.
	Note        string            `json:"note"`
	APIKey      *string           `json:"api_key"` // nil on update = keep existing
	Headers     map[string]string `json:"headers"`
	TimeoutSecs int               `json:"timeout_secs"`
	Enabled     *bool             `json:"enabled"`
	Priority    int               `json:"priority"`
	// StrictFields stops unrecognised request fields being replayed to this
	// upstream. Absent means false, which is the behaviour almost every
	// provider wants.
	StrictFields bool `json:"strict_fields"`
	// AutoDisableOnAuthError lets a rejected credential switch this provider
	// off. Absent means false: taking a provider out of rotation is the
	// operator's decision to opt into.
	AutoDisableOnAuthError bool `json:"auto_disable_on_auth_error"`
	// Models are the upstream model ids the operator picked from the discovery
	// list. Only these are registered. Nothing arrives in the registry that
	// nobody chose — an empty list is a valid provider, with models added later.
	Models []DiscoveredChoice `json:"models"`
}

// DiscoveredChoice is one model the operator ticked in the picker.
type DiscoveredChoice struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}

func (in *providerInput) validate() string {
	if strings.TrimSpace(in.Name) == "" {
		return "name is required"
	}
	if !protocol.Name(in.Protocol).Valid() {
		return "protocol must be one of: " + protocolList()
	}
	if err := provider.ValidateBaseURL(in.BaseURL); err != nil {
		return err.Error()
	}
	if in.TimeoutSecs < 0 || in.TimeoutSecs > 3600 {
		return "timeout_secs must be between 0 and 3600"
	}
	// A note is a line about this provider, not a document. The cap is in
	// runes so it counts the same in every language.
	if utf8.RuneCountInString(in.Note) > maxProviderNote {
		return fmt.Sprintf("note must be %d characters or fewer", maxProviderNote)
	}
	for k := range in.Headers {
		// Letting an operator override the auth header here would leak the
		// provider credential into a place the UI displays.
		switch strings.ToLower(k) {
		case "authorization", "x-api-key", "x-goog-api-key":
			return "set the credential in the API Key field, not in a custom header"
		}
	}
	return ""
}

func (in *providerInput) toStore() *store.Provider {
	p := &store.Provider{
		Name:         strings.TrimSpace(in.Name),
		Protocol:     in.Protocol,
		BaseURL:      strings.TrimRight(strings.TrimSpace(in.BaseURL), "/"),
		Note:         strings.TrimSpace(in.Note),
		Headers:      in.Headers,
		TimeoutSecs:  in.TimeoutSecs,
		Priority:     in.Priority,
		Enabled:      true,
		StrictFields: in.StrictFields,

		AutoDisableOnAuthError: in.AutoDisableOnAuthError,
	}
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}
	if in.APIKey != nil {
		p.APIKey = *in.APIKey
	}
	return p
}

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListProviders(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	counts, err := s.store.ModelCountsByProvider(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	for _, p := range list {
		p.ModelCount = counts[p.ID]
		// A provider being skipped has to be visible, or "why is all my
		// traffic on the backup" has no answer anywhere in the UI.
		if until := s.health.CoolingUntil(p.ID); !until.IsZero() {
			t := until
			p.CoolingUntil = &t
		}
	}
	// store.Provider marks APIKey as json:"-", so the secret cannot reach the
	// browser even by accident.
	writeJSON(w, http.StatusOK, orEmpty(list))
}

func (s *Server) handleCreateProvider(w http.ResponseWriter, r *http.Request) {
	var in providerInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if msg := in.validate(); msg != "" {
		writeErr(w, http.StatusBadRequest, "%s", msg)
		return
	}
	p, err := s.store.CreateProvider(r.Context(), in.toStore())
	if err != nil {
		if isUniqueViolation(err) {
			writeErr(w, http.StatusConflict, "a provider named %q already exists", in.Name)
			return
		}
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	// Register exactly what the operator picked in the dialog — nothing else.
	// Discovery proposes; it never decides. A provider with no models is a
	// valid configuration, not a half-finished one: models can be added later.
	added, err := s.registerChosenModels(r.Context(), p.ID, in.Models)
	if err != nil {
		// The provider itself saved. Report what happened to the models rather
		// than failing a creation that already succeeded.
		s.log.Error("register chosen models", "provider", p.Name, "error", err)
		writeJSON(w, http.StatusCreated, map[string]any{
			"provider": p, "models_added": 0, "error": err.Error(),
		})
		return
	}
	if fresh, err := s.store.GetProvider(r.Context(), p.ID); err == nil {
		p = fresh
	}
	p.ModelCount = added

	writeJSON(w, http.StatusCreated, map[string]any{"provider": p, "models_added": added})
}

func (s *Server) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid provider id")
		return
	}
	var in providerInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if msg := in.validate(); msg != "" {
		writeErr(w, http.StatusBadRequest, "%s", msg)
		return
	}
	p, err := s.store.UpdateProvider(r.Context(), id, in.toStore(), in.APIKey)
	// A fixed credential must take effect at once, not after the cooldown the
	// broken one earned.
	s.health.Forget(id)
	if err != nil {
		writeErr(w, storeErrStatus(err), "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid provider id")
		return
	}
	s.health.Forget(id)
	if err := s.store.DeleteProvider(r.Context(), id); err != nil {
		writeErr(w, storeErrStatus(err), "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleTestProvider performs a real call to the upstream so the operator gets
// a verdict before saving. When editing an existing provider the browser has
// no copy of the stored key, so id is used to load it server-side.
func (s *Server) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID          int64             `json:"id"`
		Protocol    string            `json:"protocol"`
		BaseURL     string            `json:"base_url"`
		APIKey      *string           `json:"api_key"`
		Headers     map[string]string `json:"headers"`
		TimeoutSecs int               `json:"timeout_secs"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if !protocol.Name(in.Protocol).Valid() {
		writeErr(w, http.StatusBadRequest, "protocol must be one of: %s", protocolList())
		return
	}
	if err := provider.ValidateBaseURL(in.BaseURL); err != nil {
		writeErr(w, http.StatusBadRequest, "%v", err)
		return
	}

	key := ""
	if in.APIKey != nil {
		key = *in.APIKey
	} else if in.ID > 0 {
		existing, err := s.store.GetProvider(r.Context(), in.ID)
		if err != nil {
			writeErr(w, storeErrStatus(err), "%v", err)
			return
		}
		key = existing.APIKey
	}

	timeout := 20 * time.Second
	if in.TimeoutSecs > 0 && time.Duration(in.TimeoutSecs)*time.Second < timeout {
		timeout = time.Duration(in.TimeoutSecs) * time.Second
	}
	target := &provider.Target{
		Name:     "test",
		Protocol: protocol.Name(in.Protocol),
		BaseURL:  strings.TrimRight(in.BaseURL, "/"),
		APIKey:   key,
		Headers:  in.Headers,
		Timeout:  timeout,
	}
	models, latency, err := s.probe(r.Context(), target, timeout)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         false,
			"error":      err.Error(),
			"latency_ms": latency.Milliseconds(),
		})
		return
	}
	sample := make([]string, 0, 20)
	for _, m := range firstN(models, 20) {
		sample = append(sample, m.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"latency_ms":  latency.Milliseconds(),
		"model_count": len(models),
		"models":      sample,
	})
}

// probeInput is what the browser can tell us about a provider that may not
// exist yet. When editing, the browser has no copy of the stored key, so ID is
// used to load it server-side.
type probeInput struct {
	ID          int64             `json:"id"`
	Protocol    string            `json:"protocol"`
	BaseURL     string            `json:"base_url"`
	APIKey      *string           `json:"api_key"`
	Headers     map[string]string `json:"headers"`
	TimeoutSecs int               `json:"timeout_secs"`
}

func (s *Server) targetFromInput(r *http.Request, in *probeInput) (*provider.Target, time.Duration, string) {
	if !protocol.Name(in.Protocol).Valid() {
		return nil, 0, "protocol must be one of: " + protocolList()
	}
	if err := provider.ValidateBaseURL(in.BaseURL); err != nil {
		return nil, 0, err.Error()
	}
	key := ""
	if in.APIKey != nil {
		key = *in.APIKey
	} else if in.ID > 0 {
		existing, err := s.store.GetProvider(r.Context(), in.ID)
		if err != nil {
			return nil, 0, err.Error()
		}
		key = existing.APIKey
	}
	timeout := 20 * time.Second
	if in.TimeoutSecs > 0 && time.Duration(in.TimeoutSecs)*time.Second < timeout {
		timeout = time.Duration(in.TimeoutSecs) * time.Second
	}
	return &provider.Target{
		Name:     "discover",
		Protocol: protocol.Name(in.Protocol),
		BaseURL:  strings.TrimRight(in.BaseURL, "/"),
		APIKey:   key,
		Headers:  in.Headers,
		Timeout:  timeout,
	}, timeout, ""
}

// handleDiscoverProviderModels lists what an upstream offers so the operator can
// choose. It writes nothing: discovery proposes, the operator disposes. The
// provider need not exist yet, which is the point — the picker runs inside the
// "add provider" dialog, before anything is saved.
func (s *Server) handleDiscoverProviderModels(w http.ResponseWriter, r *http.Request) {
	var in probeInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	target, timeout, msg := s.targetFromInput(r, &in)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, "%s", msg)
		return
	}

	found, latency, err := s.probe(r.Context(), target, timeout)
	if err != nil {
		// Not being able to list is not an error the operator must fix: plenty
		// of providers have no listing endpoint, and models can be typed in.
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         false,
			"supported":  !errors.Is(err, ErrDiscoveryUnsupported),
			"error":      redactSecret(err.Error(), target.APIKey),
			"latency_ms": latency.Milliseconds(),
			"models":     []any{},
		})
		return
	}

	// Mark what is already registered so the picker can show it as such rather
	// than inviting the operator to add it twice.
	registered := map[string]bool{}
	if in.ID > 0 {
		existing, err := s.store.ListModels(r.Context(), store.ModelFilter{ProviderID: in.ID, Limit: 5000})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "%v", err)
			return
		}
		for _, m := range existing {
			registered[m.UpstreamModelID] = true
		}
	}

	models := make([]map[string]any, 0, len(found))
	for _, m := range found {
		models = append(models, map[string]any{
			"id":           m.ID,
			"display_name": m.DisplayName,
			"registered":   registered[m.ID],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"supported":  true,
		"latency_ms": latency.Milliseconds(),
		"models":     models,
	})
}

func (s *Server) handleProviderModels(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid provider id")
		return
	}
	p, err := s.store.GetProvider(r.Context(), id)
	if err != nil {
		writeErr(w, storeErrStatus(err), "%v", err)
		return
	}
	target := &provider.Target{
		Name:     p.Name,
		Protocol: protocol.Name(p.Protocol),
		BaseURL:  p.BaseURL,
		APIKey:   p.APIKey,
		Headers:  p.Headers,
		Timeout:  20 * time.Second,
	}
	found, _, err := s.probe(r.Context(), target, 20*time.Second)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "%v", err)
		return
	}
	ids := make([]string, 0, len(found))
	for _, m := range found {
		ids = append(ids, m.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": ids})
}

// ErrDiscoveryUnsupported marks a protocol with no model listing endpoint. It
// is not a failure: the operator adds models by hand instead.
var ErrDiscoveryUnsupported = errString("this protocol cannot list models")

// probe lists models upstream. It doubles as a credential check, because every
// supported protocol requires auth on that endpoint.
func (s *Server) probe(ctx context.Context, t *provider.Target, timeout time.Duration) ([]provider.DiscoveredModel, time.Duration, error) {
	start := time.Now()

	discoverer, ok := provider.DiscovererFor(t.Protocol)
	if !ok {
		return nil, 0, ErrDiscoveryUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, ok := discoverer.ModelsRequest(ctx, t)
	if !ok {
		return nil, 0, ErrDiscoveryUnsupported
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, time.Since(start), errString(redactSecret(err.Error(), t.APIKey))
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 400 {
			msg = msg[:400] + "…"
		}
		return nil, time.Since(start), errString("upstream returned " + resp.Status + ": " + redactSecret(msg, t.APIKey))
	}
	models, err := discoverer.ParseModels(body)
	if err != nil {
		return nil, time.Since(start), err
	}
	return models, time.Since(start), nil
}

// --- API keys -------------------------------------------------------------

// maxKeyBudgetUSD is a slipped-decimal-point guard, not a policy.
const maxKeyBudgetUSD float64 = 1_000_000

type apiKeyPolicyInput struct {
	RPM             int      `json:"rpm"`
	RPH             int      `json:"rph"`
	RPD             int      `json:"rpd"`
	TPM             int      `json:"tpm"`
	TPD             int      `json:"tpd"`
	MaxConcurrent   int      `json:"max_concurrent"`
	MaxOutputTokens int      `json:"max_output_tokens"`
	ExpiresAt       string   `json:"expires_at"`
	AllowedModels   []string `json:"allowed_models"`
	// BudgetUSD is a spending cap in dollars. Zero is no cap, the same way
	// zero means unlimited for every count above it.
	BudgetUSD float64 `json:"budget_usd"`
	// BudgetPeriod is how that cap resets: total, daily, weekly or monthly.
	// Empty means total.
	BudgetPeriod string `json:"budget_period"`
}

func (in apiKeyPolicyInput) policy() (store.APIKeyPolicy, string) {
	values := []struct {
		name  string
		value int
	}{
		{"rpm", in.RPM}, {"rph", in.RPH}, {"rpd", in.RPD}, {"tpm", in.TPM},
		{"tpd", in.TPD}, {"max_concurrent", in.MaxConcurrent}, {"max_output_tokens", in.MaxOutputTokens},
	}
	for _, v := range values {
		if v.value < 0 {
			return store.APIKeyPolicy{}, v.name + " must be zero (unlimited) or a positive integer"
		}
	}
	if in.BudgetUSD < 0 {
		return store.APIKeyPolicy{}, "budget_usd must be zero (no budget) or a positive amount"
	}
	// Not a policy question so much as a typo guard: a cap of ten million
	// dollars is a slipped decimal point, not an intention.
	if in.BudgetUSD > maxKeyBudgetUSD {
		return store.APIKeyPolicy{}, fmt.Sprintf("budget_usd cannot exceed %.0f", maxKeyBudgetUSD)
	}
	if in.BudgetPeriod != "" && !store.ValidBudgetPeriod(in.BudgetPeriod) {
		return store.APIKeyPolicy{}, "budget_period must be total, daily, weekly or monthly"
	}
	if len(in.AllowedModels) > 500 {
		return store.APIKeyPolicy{}, "allowed_models cannot contain more than 500 entries"
	}
	for _, model := range in.AllowedModels {
		if len(strings.TrimSpace(model)) > 200 {
			return store.APIKeyPolicy{}, "an allowed model cannot be longer than 200 characters"
		}
	}
	p := store.APIKeyPolicy{
		RPM: optionalPositive(in.RPM), RPH: optionalPositive(in.RPH), RPD: optionalPositive(in.RPD),
		TPM: optionalPositive(in.TPM), TPD: optionalPositive(in.TPD),
		MaxConcurrent: optionalPositive(in.MaxConcurrent), MaxOutputTokens: optionalPositive(in.MaxOutputTokens),
		AllowedModels: in.AllowedModels,
		BudgetPeriod:  in.BudgetPeriod,
	}
	if in.BudgetUSD > 0 {
		usd := in.BudgetUSD
		p.BudgetUSD = &usd
	}
	if strings.TrimSpace(in.ExpiresAt) != "" {
		expires, err := time.Parse(time.RFC3339, in.ExpiresAt)
		if err != nil {
			return store.APIKeyPolicy{}, "expires_at must be an RFC 3339 timestamp"
		}
		expires = expires.UTC()
		p.ExpiresAt = &expires
	}
	return p, ""
}

func optionalPositive(v int) *int {
	if v <= 0 {
		return nil
	}
	return &v
}

func policyInputFromKey(k *store.APIKey) apiKeyPolicyInput {
	in := apiKeyPolicyInput{
		RPM: intValue(k.RPM), RPH: intValue(k.RPH), RPD: intValue(k.RPD),
		TPM: intValue(k.TPM), TPD: intValue(k.TPD), MaxConcurrent: intValue(k.MaxConcurrent),
		MaxOutputTokens: intValue(k.MaxOutputTokens), AllowedModels: k.AllowedModels,
		BudgetPeriod: k.BudgetPeriod,
	}
	if k.BudgetUSD != nil {
		in.BudgetUSD = *k.BudgetUSD
	}
	if k.ExpiresAt != nil {
		in.ExpiresAt = k.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return in
}

func intValue(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListAPIKeys(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	s.fillSpend(r.Context(), list...)
	writeJSON(w, http.StatusOK, orEmpty(list))
}

// fillSpend answers "how much of this budget is gone" for the keys that have
// one. A key without a budget is left alone: nothing would refuse its requests,
// so a running total next to it would only look like one that did.
//
// One aggregate per budgeted key, on an admin page listing a handful of them.
// Not the request path — that reads the same figure from memory.
func (s *Server) fillSpend(ctx context.Context, keys ...*store.APIKey) {
	now := time.Now()
	for _, k := range keys {
		if k == nil || k.BudgetUSD == nil {
			continue
		}
		spent, unpriced, err := s.store.APIKeySpendSince(ctx, k.ID, k.BudgetWindowStart(now))
		if err != nil {
			// A missing figure is not worth failing the page over; the UI
			// shows a dash for it.
			s.log.Error("sum api key spend", "key", k.Name, "error", err)
			continue
		}
		k.SpentUSD = &spent
		k.UnpricedRequests = unpriced
		if resets := k.BudgetResets(now); !resets.IsZero() {
			k.BudgetResetsAt = &resets
		}
	}
}

// handleResetKeyBudget starts a fresh total window. Only 'total' has one to
// start: the others roll over on the clock, and pretending to reset one would
// be a button that does nothing.
func (s *Server) handleResetKeyBudget(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid key id")
		return
	}
	key, err := s.store.GetAPIKey(r.Context(), id)
	if err != nil {
		writeErr(w, storeErrStatus(err), "%v", err)
		return
	}
	if key.BudgetPeriod != store.BudgetTotal {
		writeErr(w, http.StatusBadRequest, "only a total budget is reset by hand; this one resets on its own")
		return
	}
	if err := s.store.ResetAPIKeyBudget(r.Context(), id, time.Now()); err != nil {
		writeErr(w, storeErrStatus(err), "%v", err)
		return
	}
	fresh, err := s.store.GetAPIKey(r.Context(), id)
	if err != nil {
		writeErr(w, storeErrStatus(err), "%v", err)
		return
	}
	s.fillSpend(r.Context(), fresh)
	writeJSON(w, http.StatusOK, fresh)
}

// handleCreateKey returns the plaintext key exactly once. Only its hash is
// persisted, so it can never be shown again.
func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   string            `json:"name"`
		Policy apiKeyPolicyInput `json:"policy"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "API key"
	}
	policy, msg := in.Policy.policy()
	if msg != "" {
		writeErr(w, http.StatusBadRequest, "%s", msg)
		return
	}
	plaintext, prefix, hash := auth.NewAPIKey()
	key, err := s.store.CreateAPIKeyWithPolicy(r.Context(), name, prefix, hash, policy)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "%v", err)
		return
	}
	s.fillSpend(r.Context(), key)
	writeJSON(w, http.StatusCreated, map[string]any{
		"key":    key,
		"secret": plaintext,
	})
}

func (s *Server) handleUpdateKey(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid key id")
		return
	}
	var in struct {
		Name    *string            `json:"name"`
		Enabled *bool              `json:"enabled"`
		Policy  *apiKeyPolicyInput `json:"policy"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	// Preserve the compact enabled-only update used by the table switch.
	if in.Name == nil && in.Policy == nil && in.Enabled != nil {
		if err := s.store.SetAPIKeyEnabled(r.Context(), id, *in.Enabled); err != nil {
			writeErr(w, storeErrStatus(err), "%v", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	existing, err := s.store.GetAPIKey(r.Context(), id)
	if err != nil {
		writeErr(w, storeErrStatus(err), "%v", err)
		return
	}
	name := existing.Name
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
		if name == "" {
			writeErr(w, http.StatusBadRequest, "name is required")
			return
		}
	}
	enabled := existing.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	policyIn := policyInputFromKey(existing)
	if in.Policy != nil {
		policyIn = *in.Policy
	}
	policy, msg := policyIn.policy()
	if msg != "" {
		writeErr(w, http.StatusBadRequest, "%s", msg)
		return
	}
	key, err := s.store.UpdateAPIKey(r.Context(), id, name, enabled, policy)
	if err != nil {
		writeErr(w, storeErrStatus(err), "%v", err)
		return
	}
	s.fillSpend(r.Context(), key)
	writeJSON(w, http.StatusOK, key)
}

func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid key id")
		return
	}
	if err := s.store.DeleteAPIKey(r.Context(), id); err != nil {
		writeErr(w, storeErrStatus(err), "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- helpers --------------------------------------------------------------

type errString string

func (e errString) Error() string { return string(e) }

func redactSecret(s, secret string) string {
	if len(secret) < 8 {
		return s
	}
	return strings.ReplaceAll(s, secret, "***")
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}

// orEmpty makes a nil slice serialise as [] rather than null, which the WebUI
// would otherwise have to special-case everywhere.
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func firstN[T any](s []T, n int) []T {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// protocolList names the protocols actually registered, so adding a codec
// cannot leave a stale list in an error message.
func protocolList() string {
	names := protocol.Registered()
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = string(n)
	}
	return strings.Join(out, ", ")
}

// registerChosenModels records the models an operator ticked. It reuses the
// sync path, so the guarantees there still hold: nothing is deleted, and an
// operator's enabled flag or display name is never overwritten.
func (s *Server) registerChosenModels(ctx context.Context, providerID int64, chosen []DiscoveredChoice) (int, error) {
	if len(chosen) == 0 {
		return 0, nil
	}
	models := make([]store.DiscoveredModel, 0, len(chosen))
	for _, c := range chosen {
		id := strings.TrimSpace(c.ID)
		if id == "" {
			continue
		}
		models = append(models, store.DiscoveredModel{ID: id, DisplayName: c.DisplayName})
	}
	if len(models) == 0 {
		return 0, nil
	}
	if _, err := s.store.SyncModels(ctx, providerID, models); err != nil {
		return 0, err
	}
	return len(models), nil
}
