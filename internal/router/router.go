// Package router turns the model name a client asked for into a concrete
// upstream: which provider, which upstream model name.
package router

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/qunqin24/polyglot/internal/canonical"
	"github.com/qunqin24/polyglot/internal/protocol"
	"github.com/qunqin24/polyglot/internal/provider"
	"github.com/qunqin24/polyglot/internal/store"
)

// NamespaceSeparator qualifies a model with its provider, e.g.
//
//	openrouter::anthropic/claude-sonnet-4
//
// It is "::" rather than "/" because upstream model ids routinely contain
// slashes, so splitting on the first slash would be unreliable.
const NamespaceSeparator = "::"

type Router struct {
	st             *store.Store
	defaultTimeout time.Duration
}

func New(st *store.Store, defaultTimeout time.Duration) *Router {
	return &Router{st: st, defaultTimeout: defaultTimeout}
}

// Resolution is one candidate upstream for a request.
type Resolution struct {
	Alias         string
	UpstreamModel string
	Target        *provider.Target
	// Via records which rule matched, for logs and for the Inspector.
	Via string
	// Priority is the key the store ordered these candidates by — the alias
	// row's for an alias, the provider's for a registry model. Candidates
	// sharing one were ranked equally by the operator, which is the only place
	// anything else is allowed to reorder them. See prefer.go.
	Priority int
}

// Resolution sources, in the order Resolve tries them.
const (
	ViaNamespace = "namespace"
	ViaAlias     = "alias"
	ViaModel     = "model"
)

// Resolve returns the candidates for a model name, best first. Extra entries
// are fallbacks the gateway may retry when an upstream is unreachable.
//
// The order is deliberate, from most specific to least:
//
//  1. provider::model — the operator named the provider outright
//  2. an alias — a logical name the operator defined
//  3. a real upstream model id from the registry
//
// A name that matches none of them is an error. Nothing is forwarded on the
// chance that an upstream might recognise it: a typo should come back as
// Polyglot saying it does not know the model, not as a vendor's 404.
//
// Aliases are checked before real ids so an operator can shadow a confusing
// upstream name, which is the whole point of having them.
func (r *Router) Resolve(ctx context.Context, model string) ([]Resolution, error) {
	if strings.TrimSpace(model) == "" {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest, "field 'model' is required")
	}

	if providerName, modelID, ok := strings.Cut(model, NamespaceSeparator); ok {
		return r.resolveNamespaced(ctx, strings.TrimSpace(providerName), strings.TrimSpace(modelID))
	}

	if res, err := r.resolveAlias(ctx, model); err != nil {
		return nil, err
	} else if len(res) > 0 {
		return res, nil
	}

	if res, err := r.resolveModel(ctx, model); err != nil {
		return nil, err
	} else if len(res) > 0 {
		return res, nil
	}

	return nil, canonical.Errorf(canonical.ErrNotFound,
		"model %q not found: no alias and no registered model matches it. "+
			"Add the model on its provider, or create an alias.", model)
}

// resolveNamespaced handles the explicit provider::model form, which is how an
// operator picks a specific provider when several offer the same model id.
func (r *Router) resolveNamespaced(ctx context.Context, providerName, modelID string) ([]Resolution, error) {
	if providerName == "" || modelID == "" {
		return nil, canonical.Errorf(canonical.ErrInvalidRequest,
			"expected a model of the form provider%smodel", NamespaceSeparator)
	}

	p, err := r.st.ProviderByName(ctx, providerName)
	if errors.Is(err, store.ErrNotFound) {
		return nil, canonical.Errorf(canonical.ErrNotFound, "no provider named %q", providerName)
	}
	if err != nil {
		return nil, err
	}
	if !p.Enabled {
		return nil, canonical.Errorf(canonical.ErrNotFound, "provider %q is disabled", p.Name)
	}

	target, err := r.targetFor(p)
	if err != nil {
		return nil, err
	}

	m, err := r.st.ModelForProvider(ctx, p.ID, modelID)
	switch {
	case err == nil:
		return []Resolution{{
			Alias: modelID, UpstreamModel: m.UpstreamModelID, Target: target, Via: ViaNamespace,
		}}, nil
	case errors.Is(err, store.ErrNotFound):
		return nil, canonical.Errorf(canonical.ErrNotFound,
			"provider %q has no enabled model %q; add it on the provider", p.Name, modelID)
	default:
		return nil, err
	}
}

func (r *Router) resolveAlias(ctx context.Context, alias string) ([]Resolution, error) {
	rows, err := r.st.AliasesFor(ctx, alias)
	if err != nil {
		return nil, err
	}
	out := make([]Resolution, 0, len(rows))
	for _, a := range rows {
		target, _, err := r.targetForID(ctx, a.ProviderID)
		if err != nil {
			return nil, err
		}
		upstream := a.UpstreamModel
		if upstream == "" {
			upstream = alias
		}
		out = append(out, Resolution{
			Alias: alias, UpstreamModel: upstream, Target: target, Via: ViaAlias,
			// An alias orders its own rows, so its priority is the key here,
			// not the provider's.
			Priority: a.Priority,
		})
	}
	return out, nil
}

// resolveModel matches a real upstream model id from the registry.
//
// When several providers offer the same id the store returns them ordered by
// provider priority (highest first) then provider id — a total, stable order —
// so the same request always lands on the same provider. The others become
// fallbacks. An operator who wants a different one uses provider::model or an
// alias.
//
// Within one priority level the caller may reorder by protocol; see
// PreferProtocol. That replaces a tiebreak which was otherwise just creation
// order, and never crosses a priority boundary.
func (r *Router) resolveModel(ctx context.Context, modelID string) ([]Resolution, error) {
	models, err := r.st.ModelsByUpstreamID(ctx, modelID)
	if err != nil {
		return nil, err
	}
	out := make([]Resolution, 0, len(models))
	for _, m := range models {
		target, p, err := r.targetForID(ctx, m.ProviderID)
		if err != nil {
			return nil, err
		}
		out = append(out, Resolution{
			Alias: modelID, UpstreamModel: m.UpstreamModelID, Target: target, Via: ViaModel,
			Priority: p.Priority,
		})
	}
	return out, nil
}

func (r *Router) targetForID(ctx context.Context, providerID int64) (*provider.Target, *store.Provider, error) {
	p, err := r.st.GetProvider(ctx, providerID)
	if err != nil {
		return nil, nil, err
	}
	t, err := r.targetFor(p)
	if err != nil {
		return nil, nil, err
	}
	return t, p, nil
}

func (r *Router) targetFor(p *store.Provider) (*provider.Target, error) {
	proto := protocol.Name(p.Protocol)
	if !proto.Valid() {
		return nil, canonical.Errorf(canonical.ErrInternal,
			"provider %q is configured with unknown protocol %q", p.Name, p.Protocol)
	}
	if err := provider.ValidateBaseURL(p.BaseURL); err != nil {
		return nil, canonical.Errorf(canonical.ErrInternal, "provider %q: %v", p.Name, err)
	}
	timeout := r.defaultTimeout
	if p.TimeoutSecs > 0 {
		timeout = time.Duration(p.TimeoutSecs) * time.Second
	}
	return &provider.Target{
		ID:           p.ID,
		Name:         p.Name,
		Protocol:     proto,
		BaseURL:      p.BaseURL,
		APIKey:       p.APIKey,
		Headers:      p.Headers,
		Timeout:      timeout,
		StrictFields: p.StrictFields,

		AutoDisableOnAuthError: p.AutoDisableOnAuthError,
	}, nil
}

// ModelEntry is one row of the client-facing model list.
type ModelEntry struct {
	ID       string
	Provider string
	Protocol string
	Created  time.Time
	// Alias reports whether this entry is a logical name rather than a real
	// upstream model.
	Alias bool
}

// ListModels enumerates everything a client may ask for: every alias, plus
// every enabled model in the registry. An ambiguous id also gets its qualified
// provider::model form, so a client can always address one specific provider.
func (r *Router) ListModels(ctx context.Context) ([]ModelEntry, error) {
	seen := map[string]bool{}
	var out []ModelEntry

	aliases, err := r.st.ListAliases(ctx)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	for _, a := range aliases {
		if !a.Enabled || seen[a.Alias] {
			continue
		}
		seen[a.Alias] = true
		out = append(out, ModelEntry{
			ID: a.Alias, Provider: a.ProviderName, Protocol: a.Protocol,
			Created: a.CreatedAt, Alias: true,
		})
	}

	models, err := r.st.ListModels(ctx, store.ModelFilter{EnabledOnly: true, Limit: 2000})
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	ambiguous, err := r.st.AmbiguousModelIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	for _, m := range models {
		if !seen[m.UpstreamModelID] {
			seen[m.UpstreamModelID] = true
			out = append(out, ModelEntry{
				ID: m.UpstreamModelID, Provider: m.ProviderName,
				Protocol: m.Protocol, Created: m.CreatedAt,
			})
		}
		// An ambiguous id is also listed qualified, so a client has a name
		// that unambiguously reaches this specific provider.
		if ambiguous[m.UpstreamModelID] {
			qualified := m.ProviderName + NamespaceSeparator + m.UpstreamModelID
			if !seen[qualified] {
				seen[qualified] = true
				out = append(out, ModelEntry{
					ID: qualified, Provider: m.ProviderName,
					Protocol: m.Protocol, Created: m.CreatedAt,
				})
			}
		}
	}
	return out, nil
}
