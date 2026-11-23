package settings

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

type memStore struct{ v map[string]string }

func (m *memStore) GetSecretSetting(_ context.Context, k string) (string, error) { return m.v[k], nil }
func (m *memStore) SetSecretSetting(_ context.Context, k, v string) error {
	m.v[k] = v
	return nil
}

func env(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := vars[k]
		return v, ok
	}
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// A fresh install starts on the defaults with a scrape token already made.
func TestAFreshInstallGetsDefaultsAndAToken(t *testing.T) {
	st := &memStore{v: map[string]string{}}
	m, err := Load(context.Background(), st, quiet, env(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	v := m.Get()
	if v.LogRetentionDays != 30 || v.UpstreamTimeoutSecs != 600 || !v.TrustProxyHeaders {
		t.Errorf("defaults = %+v", v)
	}
	if v.MetricsToken == "" {
		t.Error("no metrics token was generated")
	}
	if st.v[Key] == "" {
		t.Error("the generated values were not stored")
	}
}

// The first start after an upgrade carries the old environment over; every
// start after that ignores it, so the WebUI is the one place to change it.
func TestLegacyVariablesAreImportedOnceThenIgnored(t *testing.T) {
	st := &memStore{v: map[string]string{}}
	old := env(map[string]string{
		"POLYGLOT_UPSTREAM_TIMEOUT": "90s",
		"LOG_RETENTION_DAYS":        "7",
		"TRUST_PROXY_HEADERS":       "false",
		"METRICS_TOKEN":             "scrape-me",
		"OTLP_HEADERS":              "x-api-key=abc, x-team = ops",
		"TELEMETRY_ENABLED":         "false",
		"MAX_REQUEST_MB":            "not-a-number",
	})
	m, err := Load(context.Background(), st, quiet, old)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	v := m.Get()
	if v.UpstreamTimeoutSecs != 90 || v.LogRetentionDays != 7 || v.TrustProxyHeaders {
		t.Errorf("imported = %+v", v)
	}
	if v.MetricsToken != "scrape-me" || v.OTLPHeaders["x-team"] != "ops" {
		t.Errorf("credentials not imported: token %q headers %v", v.MetricsToken, v.OTLPHeaders)
	}
	if v.MetricsEnabled || v.TracingEnabled {
		t.Error("TELEMETRY_ENABLED=false should turn metrics and tracing off")
	}
	if v.MaxRequestMB != 32 {
		t.Errorf("an unreadable value replaced the default: %d", v.MaxRequestMB)
	}

	again, err := Load(context.Background(), st, quiet, env(map[string]string{"UPSTREAM_TIMEOUT": "5s"}))
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if again.Get().UpstreamTimeoutSecs != 90 {
		t.Error("an environment variable was read after the settings were stored")
	}
}

func TestUpdateValidatesStoresAndNotifies(t *testing.T) {
	st := &memStore{v: map[string]string{}}
	m, err := Load(context.Background(), st, quiet, env(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var seen []int
	m.Subscribe(func(v Values) { seen = append(seen, v.ProviderCooldownSecs) })

	bad := m.Get()
	bad.UpdateRepository = "not a repo"
	if err := m.Update(context.Background(), bad); err == nil {
		t.Error("an invalid repository was accepted")
	}

	good := m.Get()
	good.ProviderCooldownSecs = 5
	if err := m.Update(context.Background(), good); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(seen) != 2 || seen[0] != 30 || seen[1] != 5 {
		t.Errorf("subscriber saw %v, want the initial 30 then 5", seen)
	}

	reloaded, err := Load(context.Background(), st, quiet, env(nil))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Get().ProviderCooldownSecs != 5 {
		t.Error("the change did not survive a restart")
	}
}
