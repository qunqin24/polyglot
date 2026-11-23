// Command polyglot runs the whole gateway: HTTP API, WebUI and SQLite, in one
// process. There is nothing else to deploy.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/qunqin24/polyglot/internal/api"
	"github.com/qunqin24/polyglot/internal/config"
	"github.com/qunqin24/polyglot/internal/pricing"
	"github.com/qunqin24/polyglot/internal/settings"
	"github.com/qunqin24/polyglot/internal/setup"
	"github.com/qunqin24/polyglot/internal/store"
	"github.com/qunqin24/polyglot/internal/telemetry"
	"github.com/qunqin24/polyglot/internal/usage"
	"github.com/qunqin24/polyglot/internal/version"
	"github.com/qunqin24/polyglot/web"
)

func main() {
	if handled, err := runCLI(os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintf(os.Stderr, "polyglot: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "polyglot: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	log := newLogger()
	// The store reports a one-time key migration through the default logger.
	slog.SetDefault(log)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// Removed with nothing to import: the log format is always text, and the
	// first-run token is always generated into the data directory.
	for _, k := range []string{"LOG_FORMAT", "POLYGLOT_LOG_FORMAT", "POLYGLOT_SETUP_TOKEN"} {
		if os.Getenv(k) != "" {
			log.Warn("environment variable is no longer read", "variable", k)
		}
	}

	log.Info("starting Polyglot",
		"version", version.Version,
		"listen", cfg.Listen,
		"data_dir", cfg.DataDir,
		"webui", webUIState(cfg))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := st.PurgeExpiredSessions(ctx); err != nil {
		log.Warn("purge expired sessions", "error", err)
	}

	adminCount, err := st.AdminCount(ctx)
	if err != nil {
		return err
	}
	var setupGuard *setup.Guard
	if adminCount == 0 {
		setupGuard, err = setup.LoadOrCreate(cfg.DataDir, "")
		if err != nil {
			return err
		}
	} else if err := setup.RemoveStaleFile(cfg.DataDir); err != nil {
		log.Warn("could not remove stale setup token", "error", err)
	}

	// Prices are cost visibility, never billing: nothing below can refuse a
	// request, so every failure here is a warning and the gateway carries on
	// with costs it does not know.
	prices := pricing.NewResolver(st)
	if snap, err := pricing.Embedded(); err != nil {
		log.Warn("read embedded price catalog", "error", err)
	} else if err := st.LoadEmbeddedCatalog(ctx, snap); err != nil {
		log.Warn("load embedded price catalog", "error", err)
	}
	if err := prices.Reload(ctx); err != nil {
		log.Warn("load model prices", "error", err)
	}

	// Everything an operator tunes lives in the database now. The first start
	// after an upgrade imports whatever the old environment variables said.
	runtime, err := settings.Load(ctx, st, log, os.LookupEnv)
	if err != nil {
		return err
	}
	initial := runtime.Get()

	usageLogger := usage.New(st, log, initial.LogRetentionDays, prices)
	usageCtx, stopUsage := context.WithCancel(context.Background())
	go usageLogger.Run(usageCtx)

	// Observability is local only: metrics are held in this process until
	// something the operator runs scrapes them, and spans go to the operator's
	// own collector or nowhere. Polyglot has no telemetry endpoint of its own.
	tel := telemetry.New(telemetryConfig(initial), log)
	defer tel.Shutdown(5 * time.Second)

	srv := &http.Server{
		Addr:    cfg.Listen,
		Handler: api.NewServer(st, cfg, runtime, log, usageLogger, tel, prices, setupGuard).Handler(),
		// No WriteTimeout: streaming responses legitimately outlive any fixed
		// deadline. Per-request limits come from the request context instead.
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelDebug),
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http server: %w", err)
			return
		}
		errCh <- nil
	}()

	if adminCount == 0 {
		log.Info("first run: open the WebUI and enter the setup token",
			"url", publicURL(cfg.Listen), "setup_token_file", setupGuard.Path())
	} else {
		log.Info("Polyglot is ready", "url", publicURL(cfg.Listen))
	}

	select {
	case err := <-errCh:
		stopUsage()
		usageLogger.Wait(5 * time.Second)
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("graceful shutdown failed", "error", err)
	}
	// Flush buffered request logs only after in-flight requests finish.
	stopUsage()
	usageLogger.Wait(5 * time.Second)
	return nil
}

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

// telemetryConfig builds the observability layer's configuration from the
// stored settings. Polyglot has no telemetry endpoint of its own; with tracing
// off nothing leaves the process.
func telemetryConfig(v settings.Values) telemetry.Config {
	return telemetry.Config{
		Enabled:          v.MetricsEnabled || v.TracingEnabled,
		Metrics:          v.MetricsEnabled,
		MetricsToken:     v.MetricsToken,
		Tracing:          v.TracingEnabled,
		TraceSampleRatio: v.TraceSampleRatio,
		OTLPEndpoint:     v.OTLPEndpoint,
		OTLPHeaders:      v.OTLPHeaders,
		ServiceName:      v.ServiceName,
		ServiceVersion:   version.Version,
	}
}

func webUIState(cfg *config.Config) string {
	if cfg.Dev {
		return "dev proxy " + cfg.DevProxy
	}
	if web.Built() {
		return "embedded"
	}
	return "placeholder (run: npm --prefix web run build)"
}

func publicURL(listen string) string {
	if strings.HasPrefix(listen, ":") {
		return "http://localhost" + listen
	}
	return "http://" + listen
}
