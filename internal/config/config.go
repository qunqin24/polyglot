// Package config loads the little configuration Polyglot needs before its
// database is open: where to listen and where the data directory is. Every
// other setting is edited in the WebUI and lives in the database; see
// internal/settings.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

type Config struct {
	// Listen is the address the single HTTP server binds to.
	Listen string
	// DataDir holds every runtime artifact; mount one volume and you have
	// migrated the whole install.
	DataDir string
	// DBPath is always DataDir/polyglot.db.
	DBPath string

	// MaxUpstreamBytes caps a non-streaming upstream response body.
	MaxUpstreamBytes int64

	// Dev serves the WebUI from a Vite dev server instead of the embedded
	// bundle.
	Dev bool
	// DevProxy is the Vite origin used when Dev is on.
	DevProxy string
}

// Variables is every environment variable Polyglot still reads, besides
// LOG_LEVEL which the logger reads before anything else. Everything an
// operator tunes at runtime is on the WebUI's settings page; these are only
// the things needed before the database is open.
var Variables = []string{"LISTEN", "DATA_DIR", "LOG_LEVEL", "POLYGLOT_DEV", "POLYGLOT_DEV_PROXY"}

func Load() (*Config, error) {
	c := &Config{
		Listen:           "127.0.0.1:3000",
		DataDir:          defaultDataDir(),
		MaxUpstreamBytes: 64 << 20,
		Dev:              envBool("POLYGLOT_DEV", false),
		DevProxy:         envStr("POLYGLOT_DEV_PROXY", "http://127.0.0.1:5173"),
	}
	if v := envStr("DATA_DIR", ""); v != "" {
		c.DataDir = v
	}
	if v := envStr("LISTEN", ""); v != "" {
		c.Listen = v
	}
	c.DBPath = filepath.Join(c.DataDir, "polyglot.db")

	// Two removed variables cannot be ignored quietly. Ignoring PORT would
	// move the server to another port, and ignoring DB_PATH would open an
	// empty database next to the real one — both look like an outage or lost
	// data rather than a renamed setting, so they stop the start with the fix.
	if v := envStr("PORT", ""); v != "" && envStr("LISTEN", "") == "" {
		return nil, fmt.Errorf("PORT is no longer read; set LISTEN=:%s instead", v)
	}
	if v := envStr("DB_PATH", ""); v != "" && filepath.Clean(v) != filepath.Clean(c.DBPath) {
		return nil, fmt.Errorf("DB_PATH is no longer read and the database is always $DATA_DIR/polyglot.db; "+
			"move %s to %s (or point DATA_DIR at its directory and name it polyglot.db), then unset DB_PATH",
			v, c.DBPath)
	}

	if err := os.MkdirAll(c.DataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create data dir %s: %w", c.DataDir, err)
	}
	return c, nil
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
