package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/qunqin24/polyglot/internal/config"
	"github.com/qunqin24/polyglot/internal/provider"
	"github.com/qunqin24/polyglot/internal/telemetry"
)

// setting describes one environment variable.
//
// `polyglot help` and `polyglot config` both read this list, so the two can
// never disagree about which variables exist — and a variable added to the code
// without being added here fails TestEverySettingIsDocumented.
type setting struct {
	key  string
	def  string
	desc string
	// effective returns the value actually in force. It is nil for a key that
	// is folded into another one (PORT becomes part of LISTEN), which has a
	// default and a description worth printing but no separate value to show.
	effective func(*config.Config) string
	// source names what decided the value, when the answer is neither "env"
	// nor "default". A setting another variable can switch on must say so —
	// reporting "default" next to a non-default value is the one mistake this
	// command must never make.
	source func(*config.Config) string
}

var settings = []setting{
	{"PORT", "3000", "Listen port", nil, nil},
	{"LISTEN", "127.0.0.1:3000", "Full listen address; overrides PORT",
		func(c *config.Config) string { return c.Listen }, nil},
	{"DATA_DIR", "/data", "SQLite file, encryption key, everything written at runtime",
		func(c *config.Config) string { return c.DataDir }, nil},
	{"DB_PATH", "$DATA_DIR/polyglot.db", "Moves only the database",
		func(c *config.Config) string { return c.DBPath }, nil},
	{"LOG_LEVEL", "info", "debug, info, warn or error",
		func(*config.Config) string { return strings.ToLower(orDefault(os.Getenv("LOG_LEVEL"), "info")) }, nil},
	{"LOG_FORMAT", "text", "json for structured logs a collector can parse",
		func(*config.Config) string { return strings.ToLower(orDefault(os.Getenv("LOG_FORMAT"), "text")) }, nil},
	{"LOG_RETENTION_DAYS", "30", "0 keeps request logs forever",
		func(c *config.Config) string { return fmt.Sprint(c.LogRetentionDays) }, nil},
	{"PROVIDER_COOLDOWN", "30s", "How long a failed provider is skipped when another offers the same model",
		func(c *config.Config) string {
			if c.ProviderCooldown <= 0 {
				return provider.DefaultCooldown.String()
			}
			return c.ProviderCooldown.String()
		}, nil},
	{"UPSTREAM_TIMEOUT", "10m", "Ceiling on one upstream request",
		func(c *config.Config) string { return c.UpstreamTimeout.String() }, nil},
	{"MAX_REQUEST_MB", "32", "Client request body limit",
		func(c *config.Config) string { return fmt.Sprint(c.MaxRequestBytes >> 20) }, nil},
	{"PUBLIC_URL", "", "An https:// value turns on Secure admin cookies",
		func(*config.Config) string { return orDefault(os.Getenv("PUBLIC_URL"), "(unset)") }, nil},
	{"SECURE_COOKIES", "false", "Forces Secure cookies without setting PUBLIC_URL",
		func(c *config.Config) string { return fmt.Sprint(c.SecureCookies) },
		func(c *config.Config) string {
			// An https PUBLIC_URL turns this on by itself.
			if _, set := os.LookupEnv("SECURE_COOKIES"); !set && c.SecureCookies {
				return "derived from PUBLIC_URL"
			}
			return ""
		}},
	{"TRUST_PROXY_HEADERS", "false", "Honour X-Forwarded-For — only behind a proxy you control",
		func(c *config.Config) string { return fmt.Sprint(c.TrustProxyHeaders) }, nil},
	{"BLOCK_PRIVATE_UPSTREAM", "false", "Refuse providers on private IPs; leave off for a local Ollama",
		func(*config.Config) string { return fmt.Sprint(os.Getenv("BLOCK_PRIVATE_UPSTREAM") == "true") }, nil},
	{"POLYGLOT_SECRET_KEY", "$DATA_DIR/secret.key", "Credential encryption key; never printed back",
		func(*config.Config) string { return secretState() }, nil},
	{"POLYGLOT_SETUP_TOKEN", "$DATA_DIR/setup.token", "One-time first-run credential; never printed back",
		func(c *config.Config) string {
			if c.SetupToken != "" {
				return "(set)"
			}
			return "(generated on first run)"
		}, nil},
	{"FETCH_REMOTE_MEDIA", "false", "Download an image a client linked, for upstreams that will not fetch one",
		func(c *config.Config) string { return fmt.Sprint(c.FetchRemoteMedia) }, nil},
	{"MAX_MEDIA_MB", "20", "Cap on one downloaded attachment",
		func(c *config.Config) string { return fmt.Sprint(c.MaxMediaBytes >> 20) }, nil},
	{"UPDATE_CHECK_ENABLED", "true", "Check public GitHub tags for a newer Polyglot release",
		func(c *config.Config) string { return fmt.Sprint(c.UpdateCheckEnabled) }, nil},
	{"UPDATE_REPOSITORY", "qunqin24/polyglot", "GitHub owner/repository used for update checks",
		func(c *config.Config) string { return c.UpdateRepository }, nil},

	// Observability. Every one of these points at something the operator runs;
	// none of them can send anything to this project.
	{"TELEMETRY_ENABLED", "true", "Master switch for metrics and tracing",
		func(c *config.Config) string { return fmt.Sprint(c.Telemetry.Enabled) }, nil},
	{"METRICS_ENABLED", "true", "Collect Prometheus metrics in process",
		func(c *config.Config) string { return fmt.Sprint(c.Telemetry.Metrics) },
		func(c *config.Config) string {
			if _, set := os.LookupEnv("METRICS_ENABLED"); !set && !c.Telemetry.Metrics {
				return "off with TELEMETRY_ENABLED"
			}
			return ""
		}},
	{"METRICS_TOKEN", "", "Bearer token for GET /metrics; without it there is no scrape endpoint",
		func(c *config.Config) string { return setState(c.Telemetry.MetricsToken) }, nil},
	{"TRACING_ENABLED", "false", "Emit OpenTelemetry spans; needs OTLP_ENDPOINT",
		func(c *config.Config) string { return fmt.Sprint(c.Telemetry.Tracing) },
		func(c *config.Config) string {
			if _, set := os.LookupEnv("TRACING_ENABLED"); !set && !c.Telemetry.Tracing {
				return ""
			}
			if c.Telemetry.Tracing && c.Telemetry.OTLPEndpoint == "" {
				return "set, but inactive without OTLP_ENDPOINT"
			}
			return ""
		}},
	{"OTLP_ENDPOINT", "", "Your OpenTelemetry collector, e.g. http://collector:4318 (OTLP/HTTP)",
		func(c *config.Config) string {
			return orDefault(telemetry.SafeEndpoint(c.Telemetry.OTLPEndpoint), "(unset)")
		}, nil},
	{"OTLP_HEADERS", "", "Extra collector headers as k=v,k2=v2; values are never printed back",
		func(c *config.Config) string { return headerState(c.Telemetry.OTLPHeaders) }, nil},
	{"TRACE_SAMPLE_RATIO", "1", "Fraction of traces Polyglot starts itself, 0 to 1",
		func(c *config.Config) string { return fmt.Sprint(c.Telemetry.TraceSampleRatio) }, nil},
	{"OTEL_SERVICE_NAME", "polyglot", "service.name on exported spans",
		func(c *config.Config) string { return c.Telemetry.ServiceName }, nil},
}

// printHelp lists what can be configured, with defaults and one line each. It
// deliberately touches no database, so it answers "what are my options" on a
// machine that has no install yet.
func printHelp() {
	fmt.Print(usageText)
	fmt.Printf("\nEnvironment variables:\n\n  %-24s %-24s %s\n", "VARIABLE", "DEFAULT", "WHAT IT DOES")
	for _, s := range settings {
		fmt.Printf("  %-24s %-24s %s\n", s.key, orDefault(s.def, "—"), s.desc)
	}
	fmt.Print(`
`)
}

// printConfig answers the other question — "what is this instance actually
// using, and why" — by showing the value in force and where it came from. That
// is the part a configuration file could never tell you on its own.
func printConfig() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	fmt.Printf("polyglot %s\n\n  %-24s %-44s %s\n", versionString(), "SETTING", "VALUE", "SOURCE")
	for _, s := range settings {
		if s.effective == nil {
			continue // folded into another setting; nothing separate to report
		}
		source := "default"
		if _, ok := os.LookupEnv(s.key); ok {
			source = "env"
		}
		if s.source != nil {
			if why := s.source(cfg); why != "" {
				source = why
			}
		}
		fmt.Printf("  %-24s %-44s %s\n", s.key, s.effective(cfg), source)
	}

	fmt.Printf("\nProviders, models, aliases and API keys are not shown: they live in\n%s and are managed from the WebUI.\n", cfg.DBPath)
	return nil
}

// secretState never prints the key itself, only where it comes from.
func secretState() string {
	if os.Getenv("POLYGLOT_SECRET_KEY") != "" {
		return "(set)"
	}
	return "(from $DATA_DIR/secret.key)"
}

// setState reports whether a credential is configured without echoing it.
// A collector token is as much a secret as a provider key.
func setState(v string) string {
	if v == "" {
		return "(unset)"
	}
	return "(set)"
}

// headerState reports which collector headers exist, never their values: they
// are usually the API token of a hosted observability backend.
func headerState(h map[string]string) string {
	if len(h) == 0 {
		return "(unset)"
	}
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	sort.Strings(names)
	return fmt.Sprintf("%d set: %s", len(names), strings.Join(names, ", "))
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
