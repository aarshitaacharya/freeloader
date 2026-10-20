// Command ledger is fl-ledger: it consumes the llm.usage topic and records usage
// durably in Postgres, exactly once in effect.
//
//	ledger                          run the consumer (join consumer group "ledger")
//	ledger replay --confirm         truncate usage tables and rebuild them from the log
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aarshitaacharya/freeloader/ledger/internal/consumer"
	"github.com/aarshitaacharya/freeloader/ledger/internal/metrics"
	"github.com/aarshitaacharya/freeloader/ledger/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
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
	replay := len(os.Args) > 1 && os.Args[1] == "replay"
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	confirm := fs.Bool("confirm", false, "required: replay TRUNCATES usage_events and usage_daily")
	force := fs.Bool("force", false, "replay even though retention has removed the start of the log")
	if replay {
		_ = fs.Parse(os.Args[2:])
		if !*confirm {
			return errors.New("replay truncates usage_events and usage_daily and rebuilds them from the log; pass --confirm")
		}
	}

	dbURL := os.Getenv("DATABASE_URL")
	brokersEnv := os.Getenv("FL_KAFKA_BROKERS")
	if dbURL == "" || brokersEnv == "" {
		return errors.New("DATABASE_URL and FL_KAFKA_BROKERS are required")
	}
	brokers := strings.Split(brokersEnv, ",")
	topic := env("FL_EVENTS_TOPIC", "llm.usage")
	group := env("FL_LEDGER_GROUP", "ledger")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	st := &store.Store{Pool: pool}

	if replay {
		res, err := consumer.Replay(ctx, consumer.ReplayConfig{Brokers: brokers, Topic: topic, Group: group, Store: st, Log: log, Force: *force})
		if err != nil {
			return err
		}
		fmt.Printf("replay complete: read %d events, wrote %d (duplicates %d, invalid %d)\n", res.Events, res.Inserted, res.Duplic, res.Invalid)
		return nil
	}

	commitEvery, err := time.ParseDuration(env("FL_LEDGER_COMMIT_INTERVAL", "1s"))
	if err != nil {
		return fmt.Errorf("FL_LEDGER_COMMIT_INTERVAL: %w", err)
	}
	m := metrics.New()
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
		if err := st.Ping(r.Context()); err != nil {
			http.Error(w, "postgres unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	srv := &http.Server{Addr: env("FL_LEDGER_METRICS_ADDR", ":9101"), Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server", "err", err)
		}
	}()
	defer func() { _ = srv.Close() }()

	log.Info("ledger starting", "group", group, "topic", topic, "commit_interval", commitEvery.String())
	err = consumer.Run(ctx, consumer.Config{Brokers: brokers, Topic: topic, Group: group,
		CommitInterval: commitEvery, Store: st, Metrics: m, Log: log}, ready)
	log.Info("ledger stopped")
	return err
}
