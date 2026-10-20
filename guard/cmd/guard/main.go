// Command guard is fl-guard: it watches the llm.usage topic, counts what the
// gateway sends to each provider per day, and flags a provider (or model) as
// exhausted in fl-cache at 90% of its configured daily limit, so every gateway
// replica stops routing to it.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // reset times need timezone data even in a minimal container

	"github.com/aarshitaacharya/freeloader/guard/internal/cache"
	"github.com/aarshitaacharya/freeloader/guard/internal/config"
	"github.com/aarshitaacharya/freeloader/guard/internal/consumer"
	"github.com/aarshitaacharya/freeloader/guard/internal/guard"
	"github.com/aarshitaacharya/freeloader/guard/internal/metrics"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	brokersEnv := os.Getenv("FL_KAFKA_BROKERS")
	if brokersEnv == "" {
		return errors.New("FL_KAFKA_BROKERS is required")
	}
	cfg, err := config.Load(env("FL_CONFIG", "config/providers.yaml"))
	if err != nil {
		return err
	}
	kv := cache.NewRedis(env("FL_CACHE_ADDR", "localhost:6400"), 500*time.Millisecond)
	defer func() { _ = kv.Close() }()

	m := metrics.New()
	g := guard.New(cfg, kv, m, log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ready := make(consumer.Ready)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(m.Reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-ready:
		default:
			http.Error(w, "consumer not started", http.StatusServiceUnavailable)
			return
		}
		if err := kv.Ping(r.Context()); err != nil {
			http.Error(w, "fl-cache unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	srv := &http.Server{Addr: env("FL_GUARD_METRICS_ADDR", ":9102"), Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server", "err", err)
		}
	}()
	defer func() { _ = srv.Close() }()

	log.Info("guard starting", "threshold", cfg.Guard.Threshold, "providers", len(cfg.Providers))
	err = consumer.Run(ctx, consumer.Config{
		Brokers: strings.Split(brokersEnv, ","), Topic: env("FL_EVENTS_TOPIC", "llm.usage"),
		Group: env("FL_GUARD_GROUP", "budget-guard"), Guard: g, Metrics: m, Log: log,
	}, ready)
	log.Info("guard stopped")
	return err
}
