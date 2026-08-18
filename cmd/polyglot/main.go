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

	cfg, err := config.Load()
	if err != nil {
		return err
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
		setupGuard, err = setup.LoadOrCreate(cfg.DataDir, cfg.SetupToken)
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

	usageLogger := usage.New(st, log, cfg.LogRetentionDays, prices)
	usageCtx, stopUsage := context.WithCancel(context.Background())
	go usageLogger.Run(usageCtx)

	// Observability is local only: metrics are held in this process until
	// something the operator runs scrapes them, and spans go to the operator's
	// own collector or nowhere. Polyglot has no telemetry endpoint of its own.
	tel := telemetry.New(cfg.Telemetry, log)
	defer tel.Shutdown(5 * time.Second)

	srv := &http.Server{
		Addr:    cfg.Listen,
		Handler: api.NewServer(st, cfg, log, usageLogger, tel, prices, setupGuard).Handler(),
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
		if path := setupGuard.Path(); path != "" {
			log.Info("first run: open the WebUI and enter the setup token",
				"url", publicURL(cfg.Listen), "setup_token_file", path)
		} else {
			log.Info("first run: open the WebUI and enter POLYGLOT_SETUP_TOKEN",
				"url", publicURL(cfg.Listen))
		}
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
	var handler slog.Handler
	if strings.ToLower(os.Getenv("LOG_FORMAT")) == "json" {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	} else {
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	}
	return slog.New(handler)
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
