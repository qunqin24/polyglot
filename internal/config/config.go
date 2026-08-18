// Package config loads Polyglot's process configuration from the environment.
// There is intentionally no config file: everything that varies per
// deployment is an env var, and everything else lives in the database.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/qunqin24/polyglot/internal/media"
	"github.com/qunqin24/polyglot/internal/telemetry"
	"github.com/qunqin24/polyglot/internal/version"
)

type Config struct {
	// Listen is the address the single HTTP server binds to.
	Listen string
	// DataDir holds every runtime artifact; mount one volume and you have
	// migrated the whole install.
	DataDir string
	// DBPath is DataDir/polyglot.db unless overridden.
	DBPath string
	// SetupToken optionally supplies the one-time first-run credential. When
	// empty, the process creates DATA_DIR/setup.token instead.
	SetupToken string
	// UpdateCheckEnabled allows the admin UI to compare this build with public
	// Git tags. No request is made until an administrator opens the WebUI.
	UpdateCheckEnabled bool
	// UpdateRepository is the GitHub owner/name whose public tags are checked.
	UpdateRepository string

	// MaxRequestBytes caps an inbound client request body.
	MaxRequestBytes int64
	// MaxUpstreamBytes caps a non-streaming upstream response body.
	MaxUpstreamBytes int64

	// UpstreamTimeout is the default per-request ceiling when a provider
	// does not set its own.
	UpstreamTimeout time.Duration

	// ProviderCooldown is how long a provider that just failed is skipped for
	// when another provider offers the same model. 0 uses the default.
	ProviderCooldown time.Duration

	// LogRetentionDays prunes request logs; 0 disables pruning.
	LogRetentionDays int

	// FetchRemoteMedia lets Polyglot download an attachment a client
	// referenced by URL, for the one target that will not fetch one itself
	// (Gemini). Off by default: it is the only outbound request whose
	// destination a client chooses, so turning it on is a deliberate act.
	FetchRemoteMedia bool
	// MaxMediaBytes caps one downloaded attachment.
	MaxMediaBytes int64

	// TrustProxyHeaders makes client IP resolution honour X-Forwarded-For.
	// Only enable behind a reverse proxy you control.
	TrustProxyHeaders bool

	// SecureCookies forces the Secure flag on the admin session cookie.
	// Auto-enabled when PUBLIC_URL is https.
	SecureCookies bool

	// Dev serves the WebUI from a Vite dev server instead of the embedded
	// bundle.
	Dev bool
	// DevProxy is the Vite origin used when Dev is on.
	DevProxy string

	// Telemetry configures local observability. Polyglot has no telemetry
	// server of its own: every field here points at something the operator
	// runs, and with all of them at their defaults nothing leaves the process.
	Telemetry telemetry.Config
}

func Load() (*Config, error) {
	c := &Config{
		Listen:             "127.0.0.1:3000",
		DataDir:            defaultDataDir(),
		SetupToken:         strings.TrimSpace(os.Getenv("POLYGLOT_SETUP_TOKEN")),
		UpdateCheckEnabled: envBool("UPDATE_CHECK_ENABLED", true),
		UpdateRepository:   envStr("UPDATE_REPOSITORY", "qunqin24/polyglot"),
		MaxRequestBytes:    32 << 20, // 32 MiB
		MaxUpstreamBytes:   64 << 20,
		UpstreamTimeout:    10 * time.Minute,
		LogRetentionDays:   30,
		TrustProxyHeaders:  envBool("TRUST_PROXY_HEADERS", false),
		FetchRemoteMedia:   envBool("FETCH_REMOTE_MEDIA", false),
		MaxMediaBytes:      media.DefaultMaxBytes,
		Dev:                envBool("POLYGLOT_DEV", false),
		DevProxy:           envStr("POLYGLOT_DEV_PROXY", "http://127.0.0.1:5173"),
	}

	if v := envStr("DATA_DIR", ""); v != "" {
		c.DataDir = v
	}
	if v := envStr("PORT", ""); v != "" {
		if _, err := strconv.Atoi(v); err != nil {
			return nil, fmt.Errorf("invalid PORT %q", v)
		}
		c.Listen = ":" + v
	}
	if v := envStr("LISTEN", ""); v != "" {
		c.Listen = v
	}
	if v := envStr("DB_PATH", ""); v != "" {
		c.DBPath = v
	} else {
		c.DBPath = filepath.Join(c.DataDir, "polyglot.db")
	}
	if v := envInt("LOG_RETENTION_DAYS", -1); v >= 0 {
		c.LogRetentionDays = v
	}
	if v := envStr("PROVIDER_COOLDOWN", ""); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid PROVIDER_COOLDOWN %q: %w", v, err)
		}
		c.ProviderCooldown = d
	}
	if v := envStr("UPSTREAM_TIMEOUT", ""); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid UPSTREAM_TIMEOUT %q: %w", v, err)
		}
		c.UpstreamTimeout = d
	}
	if v := envInt("MAX_REQUEST_MB", 0); v > 0 {
		c.MaxRequestBytes = int64(v) << 20
	}
	if v := envInt("MAX_MEDIA_MB", 0); v > 0 {
		c.MaxMediaBytes = int64(v) << 20
	}
	if strings.HasPrefix(strings.ToLower(envStr("PUBLIC_URL", "")), "https://") {
		c.SecureCookies = true
	}
	if envBool("SECURE_COOKIES", false) {
		c.SecureCookies = true
	}
	if !repositoryPattern.MatchString(c.UpdateRepository) {
		return nil, fmt.Errorf("invalid UPDATE_REPOSITORY %q; want owner/repository", c.UpdateRepository)
	}

	c.Telemetry = loadTelemetry()

	if err := os.MkdirAll(c.DataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create data dir %s: %w", c.DataDir, err)
	}
	return c, nil
}

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// loadTelemetry reads the observability settings.
//
// The defaults are the conservative ones: metrics are collected in process
// because that costs a few atomic adds, but nothing is exposed and nothing is
// sent anywhere. /metrics appears only once METRICS_TOKEN gives it a
// credential, and tracing only once OTLP_ENDPOINT names a collector the
// operator runs. There is no endpoint belonging to this project for any of it
// to point at.
func loadTelemetry() telemetry.Config {
	t := telemetry.Config{
		Enabled:          envBool("TELEMETRY_ENABLED", true),
		Metrics:          envBool("METRICS_ENABLED", true),
		MetricsToken:     envStr("METRICS_TOKEN", ""),
		Tracing:          envBool("TRACING_ENABLED", false),
		TraceSampleRatio: 1,
		OTLPEndpoint:     envStr("OTLP_ENDPOINT", ""),
		OTLPHeaders:      parseHeaderList(envStr("OTLP_HEADERS", "")),
		ServiceName:      envStr("OTEL_SERVICE_NAME", "polyglot"),
		ServiceVersion:   version.Version,
	}
	if v := envStr("TRACE_SAMPLE_RATIO", ""); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			t.TraceSampleRatio = f
		}
	}
	if !t.Enabled {
		t.Metrics = false
		t.Tracing = false
	}
	return t
}

// parseHeaderList reads the W3C-ish "k=v,k2=v2" spelling the OpenTelemetry SDKs
// use for OTEL_EXPORTER_OTLP_HEADERS, so an operator can paste the value a
// hosted backend gave them.
func parseHeaderList(s string) map[string]string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(pair, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// defaultDataDir prefers /data (the Docker convention) and falls back to
// ./data so a bare binary works without root.
func defaultDataDir() string {
	if st, err := os.Stat("/data"); err == nil && st.IsDir() {
		return "/data"
	}
	if err := os.MkdirAll("/data", 0o750); err == nil {
		return "/data"
	}
	return "./data"
}

func envStr(k, def string) string {
	for _, key := range []string{"POLYGLOT_" + k, k} {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			return v
		}
	}
	return def
}

func envBool(k string, def bool) bool {
	v := envStr(k, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(k string, def int) int {
	v := envStr(k, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
