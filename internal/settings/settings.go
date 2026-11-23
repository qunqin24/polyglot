// Package settings holds the instance settings an operator edits in the
// WebUI. They used to be environment variables; now they live in the
// database, and everything that reads them asks the Manager at the moment it
// needs the value, so a change applies without a restart — except where a
// field says otherwise.
//
// The few things that must be known before the database is open (where to
// listen, where the data directory is, the log level, the dev proxy) are
// still environment variables; see internal/config.
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qunqin24/polyglot/internal/idgen"
)

// Values is every editable setting. The JSON names are the admin API's.
type Values struct {
	// LogRetentionDays prunes request logs older than this. 0 keeps them.
	LogRetentionDays int `json:"log_retention_days"`
	// ProviderCooldownSecs is how long a provider that just failed is skipped
	// when another offers the same model.
	ProviderCooldownSecs int `json:"provider_cooldown_secs"`
	// UpstreamTimeoutSecs caps one upstream request when the provider sets no
	// timeout of its own.
	UpstreamTimeoutSecs int `json:"upstream_timeout_secs"`
	// MaxRequestMB caps an inbound client request body.
	MaxRequestMB int `json:"max_request_mb"`

	// FetchRemoteMedia lets Polyglot download an attachment a client linked,
	// for the upstreams that will not fetch one themselves. It is the only
	// outbound request whose destination a client chooses, so it is off until
	// the operator turns it on.
	FetchRemoteMedia bool `json:"fetch_remote_media"`
	// MaxMediaMB caps one downloaded attachment.
	MaxMediaMB int `json:"max_media_mb"`

	// TrustProxyHeaders makes the client address come from X-Forwarded-For.
	// On by default: Polyglot normally runs behind a reverse proxy, and
	// without it every address in the request log is the proxy's. Turn it off
	// when the port is reachable without that proxy.
	TrustProxyHeaders bool `json:"trust_proxy_headers"`
	// BlockPrivateUpstream refuses providers that resolve to private
	// addresses. Off by default because a local Ollama is a normal upstream.
	BlockPrivateUpstream bool `json:"block_private_upstream"`

	// UpdateCheckEnabled lets the WebUI compare this build with the public
	// tags of UpdateRepository.
	UpdateCheckEnabled bool   `json:"update_check_enabled"`
	UpdateRepository   string `json:"update_repository"`

	// MetricsEnabled collects Prometheus metrics in process. GET /metrics
	// answers only to MetricsToken, which is generated on first start.
	// Takes effect on restart.
	MetricsEnabled bool   `json:"metrics_enabled"`
	MetricsToken   string `json:"metrics_token"`

	// Tracing sends OpenTelemetry spans to the operator's own collector.
	// Takes effect on restart.
	TracingEnabled   bool              `json:"tracing_enabled"`
	OTLPEndpoint     string            `json:"otlp_endpoint"`
	OTLPHeaders      map[string]string `json:"otlp_headers"`
	TraceSampleRatio float64           `json:"trace_sample_ratio"`
	ServiceName      string            `json:"service_name"`
}

// Defaults are what a fresh install starts with.
func Defaults() Values {
	return Values{
		LogRetentionDays:     30,
		ProviderCooldownSecs: 30,
		UpstreamTimeoutSecs:  600,
		MaxRequestMB:         32,
		MaxMediaMB:           20,
		TrustProxyHeaders:    true,
		UpdateCheckEnabled:   true,
		UpdateRepository:     "qunqin24/polyglot",
		MetricsEnabled:       true,
		TraceSampleRatio:     1,
		ServiceName:          "polyglot",
	}
}

func (v Values) ProviderCooldown() time.Duration {
	return time.Duration(v.ProviderCooldownSecs) * time.Second
}

func (v Values) UpstreamTimeout() time.Duration {
	return time.Duration(v.UpstreamTimeoutSecs) * time.Second
}

func (v Values) MaxRequestBytes() int64 { return int64(v.MaxRequestMB) << 20 }
func (v Values) MaxMediaBytes() int64   { return int64(v.MaxMediaMB) << 20 }

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Validate rejects values that would break the gateway. The messages are
// shown to the operator as they are.
func (v Values) Validate() error {
	switch {
	case v.LogRetentionDays < 0 || v.LogRetentionDays > 3650:
		return fmt.Errorf("log_retention_days must be between 0 and 3650")
	case v.ProviderCooldownSecs < 1 || v.ProviderCooldownSecs > 3600:
		return fmt.Errorf("provider_cooldown_secs must be between 1 and 3600")
	case v.UpstreamTimeoutSecs < 1 || v.UpstreamTimeoutSecs > 24*3600:
		return fmt.Errorf("upstream_timeout_secs must be between 1 and 86400")
	case v.MaxRequestMB < 1 || v.MaxRequestMB > 1024:
		return fmt.Errorf("max_request_mb must be between 1 and 1024")
	case v.MaxMediaMB < 1 || v.MaxMediaMB > 1024:
		return fmt.Errorf("max_media_mb must be between 1 and 1024")
	case !repositoryPattern.MatchString(v.UpdateRepository):
		return fmt.Errorf("update_repository must look like owner/repository")
	case v.TraceSampleRatio < 0 || v.TraceSampleRatio > 1:
		return fmt.Errorf("trace_sample_ratio must be between 0 and 1")
	case strings.TrimSpace(v.ServiceName) == "":
		return fmt.Errorf("service_name must not be empty")
	case strings.TrimSpace(v.MetricsToken) == "":
		return fmt.Errorf("metrics_token must not be empty")
	}
	if v.OTLPEndpoint != "" {
		u, err := url.Parse(v.OTLPEndpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("otlp_endpoint must be an http:// or https:// URL")
		}
	}
	return nil
}

// NewMetricsToken is a fresh credential for GET /metrics.
func NewMetricsToken() string { return "pmet_" + idgen.Secret() }

// Store is the persistence the Manager needs. internal/store implements it;
// the blob is encrypted there because it holds the metrics token and the
// collector headers.
type Store interface {
	GetSecretSetting(ctx context.Context, key string) (string, error)
	SetSecretSetting(ctx context.Context, key, value string) error
}

// Key is the settings row the values are kept under.
const Key = "runtime"

// Manager hands out the current values and persists changes.
type Manager struct {
	st  Store
	cur atomic.Pointer[Values]

	mu   sync.Mutex
	subs []func(Values)
}

// Load reads the stored values. On the first start after upgrading from a
// version configured by environment, the old variables are imported once and
// the operator is told they can be removed; after that they are ignored, and
// a warning names any still set.
func Load(ctx context.Context, st Store, log *slog.Logger, env func(string) (string, bool)) (*Manager, error) {
	raw, err := st.GetSecretSetting(ctx, Key)
	if err != nil {
		return nil, err
	}
	v := Defaults()
	stored := raw != ""
	if stored {
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, fmt.Errorf("read settings: %w", err)
		}
	} else {
		imported, problems := importLegacy(&v, env)
		for _, p := range problems {
			log.Warn("ignored an invalid legacy environment variable", "detail", p)
		}
		if len(imported) > 0 {
			log.Warn("imported settings from environment variables into the database; "+
				"they are no longer read and can be removed — edit these in the WebUI from now on",
				"variables", strings.Join(imported, ","))
		}
	}
	if v.MetricsToken == "" {
		v.MetricsToken = NewMetricsToken()
		stored = false
	}
	if err := v.Validate(); err != nil {
		// A stored value can only be invalid if a newer rule tightened an
		// older one, or an imported variable was out of range. Start on the
		// defaults rather than refuse to start; the WebUI can fix it.
		log.Warn("stored settings failed validation; using defaults for this start", "error", err)
		token := v.MetricsToken
		v = Defaults()
		v.MetricsToken = token
	}

	m := &Manager{st: st}
	m.cur.Store(&v)
	if !stored {
		if err := m.persist(ctx, v); err != nil {
			return nil, err
		}
	} else if left := legacyStillSet(env); len(left) > 0 {
		log.Warn("these environment variables are no longer read; the WebUI's settings page holds them now",
			"variables", strings.Join(left, ","))
	}
	return m, nil
}

// Get returns the current values. The result is a copy; changing it changes
// nothing.
func (m *Manager) Get() Values {
	v := *m.cur.Load()
	if v.OTLPHeaders != nil {
		h := make(map[string]string, len(v.OTLPHeaders))
		for k, val := range v.OTLPHeaders {
			h[k] = val
		}
		v.OTLPHeaders = h
	}
	return v
}

// Update validates, stores and applies new values, then tells every
// subscriber.
func (m *Manager) Update(ctx context.Context, v Values) error {
	v.UpdateRepository = strings.TrimSpace(v.UpdateRepository)
	v.OTLPEndpoint = strings.TrimSpace(v.OTLPEndpoint)
	v.ServiceName = strings.TrimSpace(v.ServiceName)
	if err := v.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.persist(ctx, v); err != nil {
		return err
	}
	m.cur.Store(&v)
	for _, f := range m.subs {
		f(v)
	}
	return nil
}

// Subscribe registers f to run after every change, and runs it once now so a
// component never has to read the initial value separately.
func (m *Manager) Subscribe(f func(Values)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subs = append(m.subs, f)
	f(*m.cur.Load())
}

func (m *Manager) persist(ctx context.Context, v Values) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return m.st.SetSecretSetting(ctx, Key, string(body))
}

// Static returns a Manager over fixed values with no store behind it, for
// tests and tools that never save.
func Static(v Values) *Manager {
	if v.MetricsToken == "" {
		v.MetricsToken = NewMetricsToken()
	}
	m := &Manager{st: discard{}}
	m.cur.Store(&v)
	return m
}

type discard struct{}

func (discard) GetSecretSetting(context.Context, string) (string, error) { return "", nil }
func (discard) SetSecretSetting(context.Context, string, string) error   { return nil }
