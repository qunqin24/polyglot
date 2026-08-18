// Package api wires every HTTP surface Polyglot exposes: the client-facing
// protocol endpoints, the admin API for the WebUI, and the embedded WebUI
// itself — all on one port, from one process.
package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/qunqin24/polyglot/internal/auth"
	"github.com/qunqin24/polyglot/internal/config"
	"github.com/qunqin24/polyglot/internal/gateway"
	"github.com/qunqin24/polyglot/internal/media"
	"github.com/qunqin24/polyglot/internal/pricing"
	"github.com/qunqin24/polyglot/internal/protocol"
	"github.com/qunqin24/polyglot/internal/provider"
	"github.com/qunqin24/polyglot/internal/router"
	"github.com/qunqin24/polyglot/internal/setup"
	"github.com/qunqin24/polyglot/internal/store"
	"github.com/qunqin24/polyglot/internal/telemetry"
	"github.com/qunqin24/polyglot/internal/update"
	"github.com/qunqin24/polyglot/internal/usage"
	"github.com/qunqin24/polyglot/internal/version"

	// Registering the codecs is the only reason these are imported here.
	_ "github.com/qunqin24/polyglot/internal/protocol/anthropic"
	_ "github.com/qunqin24/polyglot/internal/protocol/gemini"
	_ "github.com/qunqin24/polyglot/internal/protocol/interactions"
	_ "github.com/qunqin24/polyglot/internal/protocol/openai"
	_ "github.com/qunqin24/polyglot/internal/protocol/responses"
)

type Server struct {
	store      *store.Store
	health     *provider.Health
	router     *router.Router
	gw         *gateway.Gateway
	client     *provider.Client
	cfg        *config.Config
	log        *slog.Logger
	usage      *usage.Logger
	tel        *telemetry.Telemetry
	prices     *pricing.Resolver
	keyLimiter *auth.KeyLimiter
	setupGuard *setup.Guard
	updates    *update.Checker
}

func NewServer(st *store.Store, cfg *config.Config, log *slog.Logger, ul *usage.Logger,
	tel *telemetry.Telemetry, prices *pricing.Resolver, setupGuard *setup.Guard) *Server {
	rt := router.New(st, cfg.UpstreamTimeout)
	client := provider.NewClient()
	health := provider.NewHealth(cfg.ProviderCooldown)
	var fetcher *media.Fetcher
	if cfg.FetchRemoteMedia {
		fetcher = media.NewFetcher(cfg.MaxMediaBytes, log)
	}
	keyLimiter := auth.NewKeyLimiter(st)
	// Prices are worked out on the usage logger's flush goroutine, and a key
	// with a budget needs to hear about them. This is the only wire between
	// the two; without it costs are still logged, they just count against
	// nothing.
	ul.OnSpend(keyLimiter)
	return &Server{
		store:      st,
		health:     health,
		router:     rt,
		client:     client,
		cfg:        cfg,
		log:        log,
		usage:      ul,
		tel:        tel,
		prices:     prices,
		keyLimiter: keyLimiter,
		setupGuard: setupGuard,
		updates:    update.New(cfg.UpdateRepository, version.Version, cfg.UpdateCheckEnabled),
		gw: &gateway.Gateway{
			Store:      st,
			Router:     rt,
			Client:     client,
			Usage:      ul,
			Config:     cfg,
			Log:        log,
			Telemetry:  tel,
			Health:     health,
			Media:      fetcher,
			KeyLimiter: keyLimiter,
		},
	}
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	// X-Forwarded-For is honoured only when the operator has said there is a
	// proxy in front. Trusting it unconditionally would let any caller choose
	// the address that reaches the request log, which would defeat the one
	// thing that log is kept for: noticing a key being used from somewhere it
	// should not be.
	if s.cfg.TrustProxyHeaders {
		r.Use(middleware.RealIP)
	}
	r.Use(requestID)
	r.Use(requestLogger(s.log))
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)

	r.Get("/health", s.handleHealth)

	// Prometheus scrape endpoint. It is registered unconditionally and answers
	// 404 unless METRICS_TOKEN authorises the caller, so a disabled scrape
	// surface looks exactly like a path that was never implemented — and, more
	// importantly, never falls through to the SPA handler.
	r.Get("/metrics", s.handleMetrics)

	// --- client-facing protocol endpoints ---------------------------------

	// OpenAI. Also the surface most third-party tools speak.
	r.Group(func(r chi.Router) {
		r.Use(auth.Gateway(s.store, protocol.OpenAI))
		r.Post("/v1/chat/completions", s.handleOpenAIChat)
		r.Get("/v1/models", s.handleClientModelList)
		r.Get("/v1/models/{model}", s.handleGetModel)
	})

	// OpenAI Responses. A distinct wire format, so a distinct endpoint.
	r.Group(func(r chi.Router) {
		r.Use(auth.Gateway(s.store, protocol.OpenAIResponses))
		r.Post("/v1/responses", s.handleOpenAIResponses)
	})

	// Anthropic.
	r.Group(func(r chi.Router) {
		r.Use(auth.Gateway(s.store, protocol.Anthropic))
		r.Post("/v1/messages", s.handleAnthropicMessages)
	})

	// Gemini Interactions. Unlike generateContent, the model and the streaming
	// choice are in the body, so one path serves both.
	r.Group(func(r chi.Router) {
		r.Use(auth.Gateway(s.store, protocol.GeminiInteractions))
		r.Post("/v1beta/interactions", s.handleGeminiInteractions)
		r.Post("/v1/interactions", s.handleGeminiInteractions)
	})

	// Gemini. The model and the streaming choice live in the path.
	r.Group(func(r chi.Router) {
		r.Use(auth.Gateway(s.store, protocol.Gemini))
		r.Post("/v1beta/models/{action}", s.handleGeminiGenerate)
		r.Post("/v1/models/{action}", s.handleGeminiGenerate)
		r.Get("/v1beta/models", s.handleGeminiListModels)
	})

	// --- admin API --------------------------------------------------------

	r.Route("/api", func(r chi.Router) {
		r.Get("/setup", s.handleSetupStatus)
		r.Post("/setup", s.handleSetup)
		r.Post("/auth/login", s.handleLogin)
		r.Post("/auth/logout", s.handleLogout)

		r.Group(func(r chi.Router) {
			r.Use(auth.Admin(s.store))

			r.Get("/auth/me", s.handleMe)
			r.Post("/auth/password", s.handleChangePassword)
			r.Get("/update", s.handleUpdateStatus)

			r.Put("/settings", s.handleUpdateSettings)

			r.Get("/protocols", s.handleProtocols)
			r.Get("/stats", s.handleStats)
			// One endpoint per Overview panel, because the page refreshes
			// itself and a panel nobody opened should cost nothing.
			r.Get("/stats/conversions", s.handleConversionStats)
			r.Get("/stats/latency", s.handleLatencyStats)
			r.Get("/stats/cost", s.handleCostStats)
			// The same exposition an operator would scrape, behind the admin
			// session — so the numbers are readable without standing up
			// Prometheus first.
			r.Get("/metrics", s.handleAdminMetrics)

			r.Get("/providers", s.handleListProviders)
			r.Post("/providers", s.handleCreateProvider)
			r.Put("/providers/{id}", s.handleUpdateProvider)
			r.Delete("/providers/{id}", s.handleDeleteProvider)
			r.Post("/providers/test", s.handleTestProvider)
			r.Post("/providers/discover", s.handleDiscoverProviderModels)
			r.Get("/providers/{id}/models", s.handleProviderModels)
			r.Post("/providers/{id}/models", s.handleAddProviderModels)

			// The registry of real upstream models.
			r.Get("/models", s.handleListModels)
			r.Get("/models/stats", s.handleModelStats)
			r.Post("/models", s.handleCreateModel)
			r.Put("/models/{id}", s.handleUpdateModel)
			r.Delete("/models/{id}", s.handleDeleteModel)

			// What every model costs. Prices come from models.dev unless the
			// operator typed their own; a model with neither has no price and
			// says so.
			r.Get("/pricing", s.handleListPricing)
			r.Put("/models/{id}/pricing", s.handleSetModelPrice)
			r.Get("/pricing/catalog", s.handleCatalogStatus)
			r.Post("/pricing/catalog/refresh", s.handleRefreshCatalog)

			// Aliases: an optional logical naming layer on top.
			r.Get("/aliases", s.handleListAliases)
			r.Post("/aliases", s.handleCreateAlias)
			r.Put("/aliases/{id}", s.handleUpdateAlias)
			r.Delete("/aliases/{id}", s.handleDeleteAlias)

			r.Get("/keys", s.handleListKeys)
			r.Post("/keys", s.handleCreateKey)
			r.Put("/keys/{id}", s.handleUpdateKey)
			r.Post("/keys/{id}/budget/reset", s.handleResetKeyBudget)
			r.Delete("/keys/{id}", s.handleDeleteKey)
			r.Get("/keys/{id}/origins", s.handleKeyOrigins)

			r.Get("/logs", s.handleListLogs)
			r.Get("/logs/{id}", s.handleGetLog)

			r.Post("/inspect", s.handleInspect)
		})
	})

	// --- WebUI ------------------------------------------------------------
	r.NotFound(s.webUIHandler())

	return r
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DB().PingContext(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"protocols": protocol.Registered(),
	})
}

// securityHeaders applies the small set that actually matters for a
// self-hosted admin UI.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// requestLogger logs one line per request. It never logs headers, because
// that is where the credentials are, and never the query string, because that
// is the other place a key turns up.
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			if r.URL.Path == "/health" {
				return
			}
			log.Debug("http",
				"request_id", telemetry.RequestIDFrom(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds())
		})
	}
}

// handleMetrics serves the Prometheus exposition to a scraper.
//
// Without METRICS_TOKEN there is no endpoint at all. Polyglot's port is
// routinely reachable from the internet, and an open /metrics would publish an
// operator's provider names, model list and traffic volumes to anyone who
// asked. A source-address rule would not do instead: this server honours
// X-Forwarded-For, so "only from localhost" is a header away from being false.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.tel.ScrapeAuthorized(r) {
		http.NotFound(w, r)
		return
	}
	s.tel.WriteMetrics(w)
}

// handleAdminMetrics serves the same body to a signed-in administrator.
func (s *Server) handleAdminMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.tel.MetricsEnabled() {
		writeErr(w, http.StatusNotFound, "metrics collection is disabled")
		return
	}
	s.tel.WriteMetrics(w)
}
