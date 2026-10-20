// Command gateway is the Freeloader front door: one OpenAI-compatible endpoint
// in front of several free LLM providers.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aarshitaacharya/freeloader/gateway/internal/auth"
	"github.com/aarshitaacharya/freeloader/gateway/internal/cache"
	"github.com/aarshitaacharya/freeloader/gateway/internal/config"
	"github.com/aarshitaacharya/freeloader/gateway/internal/metrics"
	"github.com/aarshitaacharya/freeloader/gateway/internal/provider"
	"github.com/aarshitaacharya/freeloader/gateway/internal/ratelimit"
	"github.com/aarshitaacharya/freeloader/gateway/internal/route"
	"github.com/aarshitaacharya/freeloader/gateway/internal/server"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfgPath := os.Getenv("FL_CONFIG")
	if cfgPath == "" {
		cfgPath = "config/providers.yaml"
	}
	cfg, err := config.Load(cfgPath, os.Getenv)
	if err != nil {
		return err
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	kv := cache.NewRedis(cfg.Cache.Addr, cfg.Cache.Timeout)
	defer func() { _ = kv.Close() }()

	providers := map[string]*provider.Provider{}
	for name, p := range cfg.Providers {
		providers[name] = provider.New(name, p.BaseURL, p.APIKey, cfg.Server.AttemptTimeout)
		if !providers[name].Enabled() {
			log.Warn("provider has no API key and will not be routed to", "provider", name, "env", p.APIKeyEnv)
		}
	}

	srv := server.New(server.Deps{
		Auth:        auth.New(auth.PGStore{Pool: pool}, 30*time.Second),
		Limiter:     ratelimit.New(kv),
		KV:          kv,
		Router:      route.New(cfg.Aliases, providers, kv, cfg.Server.Cooldown),
		Metrics:     metrics.New(),
		Log:         log,
		MaxAttempts: cfg.Server.MaxAttempts,
	})
	httpSrv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	errc := make(chan error, 1)
	go func() { errc <- httpSrv.ListenAndServe() }()
	log.Info("gateway listening", "addr", cfg.Server.Listen, "cache", cfg.Cache.Addr)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	// Kubernetes sends SIGTERM before killing the pod: stop accepting, let in-flight finish.
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}
