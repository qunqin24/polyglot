package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/qunqin24/polyglot/internal/config"
	"github.com/qunqin24/polyglot/internal/settings"
	"github.com/qunqin24/polyglot/internal/store"
)

// envSetting describes one environment variable.
//
// Only what is needed before the database opens is an environment variable;
// everything else is on the WebUI's settings page. `polyglot help` and
// `polyglot config` both read this list, and TestEverySettingIsDocumented
// fails if the code reads a variable that is missing from it.
type envSetting struct {
	key       string
	def       string
	desc      string
	effective func(*config.Config) string
}

var envSettings = []envSetting{
	{"LISTEN", "127.0.0.1:3000", "Listen address, e.g. :3000 for every interface",
		func(c *config.Config) string { return c.Listen }},
	{"DATA_DIR", "/data", "Database, encryption key, everything written at runtime",
		func(c *config.Config) string { return c.DataDir }},
	{"LOG_LEVEL", "info", "debug, info, warn or error",
		func(*config.Config) string { return strings.ToLower(orDefault(os.Getenv("LOG_LEVEL"), "info")) }},
}

// printHelp lists what can be configured, with defaults and one line each. It
// deliberately touches no database, so it answers "what are my options" on a
// machine that has no install yet.
func printHelp() {
	fmt.Print(usageText)
	fmt.Printf("\nEnvironment variables:\n\n  %-12s %-18s %s\n", "VARIABLE", "DEFAULT", "WHAT IT DOES")
	for _, s := range envSettings {
		fmt.Printf("  %-12s %-18s %s\n", s.key, orDefault(s.def, "—"), s.desc)
	}
	fmt.Print(`
Timeouts, limits, log retention, proxy and network rules, update checks,
metrics and tracing are set on the WebUI's settings page.
`)
}

// printConfig answers "what is this instance actually using": the environment
// variables in force, then the settings stored in the database.
func printConfig() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	fmt.Printf("polyglot %s\n\n  %-24s %-44s %s\n", versionString(), "SETTING", "VALUE", "SOURCE")
	for _, s := range envSettings {
		source := "default"
		if _, ok := os.LookupEnv(s.key); ok {
			source = "env"
		}
		fmt.Printf("  %-24s %-44s %s\n", s.key, s.effective(cfg), source)
	}

	v, stored, err := storedSettings(cfg)
	if err != nil {
		return err
	}
	source := "WebUI"
	if !stored {
		source = "default (not started yet)"
	}
	fmt.Println()
	for _, row := range settingRows(v) {
		fmt.Printf("  %-24s %-44s %s\n", row[0], row[1], source)
	}
	fmt.Printf("\nProviders, models, aliases and API keys live in\n%s and are managed from the WebUI.\n", cfg.DBPath)
	return nil
}

// storedSettings reads the WebUI settings without creating anything: on a
// machine where the gateway never ran there is no database to open.
func storedSettings(cfg *config.Config) (settings.Values, bool, error) {
	if _, err := os.Stat(cfg.DBPath); err != nil {
		return settings.Defaults(), false, nil
	}
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return settings.Values{}, false, fmt.Errorf("open %s: %w", cfg.DBPath, err)
	}
	defer st.Close()
	raw, err := st.GetSecretSetting(ctx, settings.Key)
	if err != nil {
		return settings.Values{}, false, err
	}
	v := settings.Defaults()
	if raw == "" {
		return v, false, nil
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return settings.Values{}, false, fmt.Errorf("read settings: %w", err)
	}
	return v, true, nil
}

// settingRows formats the stored values. Credentials are reported as set or
// unset, never printed.
func settingRows(v settings.Values) [][2]string {
	return [][2]string{
		{"log retention (days)", fmt.Sprint(v.LogRetentionDays)},
		{"provider cooldown", v.ProviderCooldown().String()},
		{"upstream timeout", v.UpstreamTimeout().String()},
		{"max request (MiB)", fmt.Sprint(v.MaxRequestMB)},
		{"fetch remote media", fmt.Sprint(v.FetchRemoteMedia)},
		{"max media (MiB)", fmt.Sprint(v.MaxMediaMB)},
		{"trust proxy headers", fmt.Sprint(v.TrustProxyHeaders)},
		{"block private upstream", fmt.Sprint(v.BlockPrivateUpstream)},
		{"update check", fmt.Sprint(v.UpdateCheckEnabled)},
		{"update repository", v.UpdateRepository},
		{"metrics", fmt.Sprint(v.MetricsEnabled)},
		{"metrics token", setState(v.MetricsToken)},
		{"tracing", fmt.Sprint(v.TracingEnabled)},
		{"otlp endpoint", orDefault(v.OTLPEndpoint, "(unset)")},
		{"otlp headers", headerState(v.OTLPHeaders)},
		{"trace sample ratio", fmt.Sprint(v.TraceSampleRatio)},
		{"service name", v.ServiceName},
	}
}

// setState reports whether a credential is configured without echoing it.
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
