package settings

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// legacyNames are the environment variables these settings replaced. Each was
// read both bare and with a POLYGLOT_ prefix, the prefixed spelling winning.
var legacyNames = []string{
	"LOG_RETENTION_DAYS",
	"PROVIDER_COOLDOWN",
	"UPSTREAM_TIMEOUT",
	"MAX_REQUEST_MB",
	"FETCH_REMOTE_MEDIA",
	"MAX_MEDIA_MB",
	"TRUST_PROXY_HEADERS",
	"BLOCK_PRIVATE_UPSTREAM",
	"UPDATE_CHECK_ENABLED",
	"UPDATE_REPOSITORY",
	"TELEMETRY_ENABLED",
	"METRICS_ENABLED",
	"METRICS_TOKEN",
	"TRACING_ENABLED",
	"OTLP_ENDPOINT",
	"OTLP_HEADERS",
	"TRACE_SAMPLE_RATIO",
	"OTEL_SERVICE_NAME",
	// Replaced by detecting HTTPS on each request rather than by a setting.
	"PUBLIC_URL",
	"SECURE_COOKIES",
}

func lookupLegacy(env func(string) (string, bool), name string) (string, string, bool) {
	for _, key := range []string{"POLYGLOT_" + name, name} {
		if v, ok := env(key); ok && strings.TrimSpace(v) != "" {
			return key, strings.TrimSpace(v), true
		}
	}
	return "", "", false
}

func legacyStillSet(env func(string) (string, bool)) []string {
	var out []string
	for _, name := range legacyNames {
		if key, _, ok := lookupLegacy(env, name); ok {
			out = append(out, key)
		}
	}
	return out
}

// importLegacy overlays whatever old variables are set onto v. It reports the
// names it used and, separately, values it could not read — those are skipped
// rather than fatal, since the setting can now be fixed in the WebUI.
func importLegacy(v *Values, env func(string) (string, bool)) (imported, problems []string) {
	use := func(name string, apply func(string) error) {
		key, raw, ok := lookupLegacy(env, name)
		if !ok {
			return
		}
		if err := apply(raw); err != nil {
			problems = append(problems, fmt.Sprintf("%s=%q: %v", key, raw, err))
			return
		}
		imported = append(imported, key)
	}
	intVar := func(dst *int, min int) func(string) error {
		return func(s string) error {
			n, err := strconv.Atoi(s)
			if err != nil {
				return err
			}
			if n < min {
				return fmt.Errorf("below %d", min)
			}
			*dst = n
			return nil
		}
	}
	boolVar := func(dst *bool) func(string) error {
		return func(s string) error {
			b, err := strconv.ParseBool(s)
			if err != nil {
				return err
			}
			*dst = b
			return nil
		}
	}
	secsVar := func(dst *int) func(string) error {
		return func(s string) error {
			d, err := time.ParseDuration(s)
			if err != nil {
				return err
			}
			if d <= 0 {
				return fmt.Errorf("must be positive")
			}
			*dst = int(math.Ceil(d.Seconds()))
			return nil
		}
	}
	strVar := func(dst *string) func(string) error {
		return func(s string) error { *dst = s; return nil }
	}

	use("LOG_RETENTION_DAYS", intVar(&v.LogRetentionDays, 0))
	use("PROVIDER_COOLDOWN", secsVar(&v.ProviderCooldownSecs))
	use("UPSTREAM_TIMEOUT", secsVar(&v.UpstreamTimeoutSecs))
	use("MAX_REQUEST_MB", intVar(&v.MaxRequestMB, 1))
	use("FETCH_REMOTE_MEDIA", boolVar(&v.FetchRemoteMedia))
	use("MAX_MEDIA_MB", intVar(&v.MaxMediaMB, 1))
	use("TRUST_PROXY_HEADERS", boolVar(&v.TrustProxyHeaders))
	use("BLOCK_PRIVATE_UPSTREAM", boolVar(&v.BlockPrivateUpstream))
	use("UPDATE_CHECK_ENABLED", boolVar(&v.UpdateCheckEnabled))
	use("UPDATE_REPOSITORY", strVar(&v.UpdateRepository))
	use("METRICS_ENABLED", boolVar(&v.MetricsEnabled))
	use("METRICS_TOKEN", strVar(&v.MetricsToken))
	use("TRACING_ENABLED", boolVar(&v.TracingEnabled))
	use("OTLP_ENDPOINT", strVar(&v.OTLPEndpoint))
	use("OTLP_HEADERS", func(s string) error {
		v.OTLPHeaders = ParseHeaderList(s)
		return nil
	})
	use("TRACE_SAMPLE_RATIO", func(s string) error {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		if f < 0 || f > 1 {
			return fmt.Errorf("must be between 0 and 1")
		}
		v.TraceSampleRatio = f
		return nil
	})
	use("OTEL_SERVICE_NAME", strVar(&v.ServiceName))
	// The old master switch turned both halves off; there is no switch of its
	// own any more.
	use("TELEMETRY_ENABLED", func(s string) error {
		on, err := strconv.ParseBool(s)
		if err != nil {
			return err
		}
		if !on {
			v.MetricsEnabled = false
			v.TracingEnabled = false
		}
		return nil
	})
	return imported, problems
}

// ParseHeaderList reads the "k=v,k2=v2" spelling the OpenTelemetry SDKs use
// for OTEL_EXPORTER_OTLP_HEADERS, so an operator can paste the value a hosted
// backend gave them.
func ParseHeaderList(s string) map[string]string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		k, val, ok := strings.Cut(part, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			continue
		}
		out[k] = strings.TrimSpace(val)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
