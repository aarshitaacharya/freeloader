//go:build integration

// Real Redpanda and the real fl-cache (C++) in containers: proves the guard works
// end to end, including its commands against our own cache server.
package consumer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/aarshitaacharya/freeloader/guard/internal/cache"
	"github.com/aarshitaacharya/freeloader/guard/internal/config"
	"github.com/aarshitaacharya/freeloader/guard/internal/consumer"
	"github.com/aarshitaacharya/freeloader/guard/internal/event"
	"github.com/aarshitaacharya/freeloader/guard/internal/guard"
	"github.com/aarshitaacharya/freeloader/guard/internal/metrics"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/redpanda"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

var (
	broker string
	kv     *cache.Redis
	raw    *redis.Client
	quiet  = slog.New(slog.NewTextHandler(io.Discard, nil))
	n      int
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	rp, err := redpanda.Run(ctx, "docker.redpanda.com/redpandadata/redpanda:v26.2.5")
	if err != nil {
		fmt.Fprintln(os.Stderr, "redpanda:", err)
		os.Exit(1)
	}
	broker, _ = rp.KafkaSeedBroker(ctx)
	fc, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "fl-cache:dev", ExposedPorts: []string{"6400/tcp"},
			WaitingFor: wait.ForListeningPort("6400/tcp").WithStartupTimeout(30 * time.Second),
		}, Started: true,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "fl-cache:", err, "(build it: docker build -t fl-cache:dev -f cache/Dockerfile .)")
		os.Exit(1)
	}
	host, _ := fc.Host(ctx)
	port, _ := fc.MappedPort(ctx, "6400/tcp")
	addr := fmt.Sprintf("%s:%s", host, port.Port())
	kv = cache.NewRedis(addr, 2*time.Second)
	raw = redis.NewClient(&redis.Options{Addr: addr, Protocol: 2})

	code := m.Run()
	_ = kv.Close()
	_ = raw.Close()
	_ = testcontainers.TerminateContainer(fc)
	_ = testcontainers.TerminateContainer(rp)
	os.Exit(code)
}

func setup(t *testing.T, limits config.Limits) (topic string, g *guard.Guard, m *metrics.Metrics, produce func(...event.Event)) {
	t.Helper()
	n++
	topic = fmt.Sprintf("llm.usage.g%d", n)
	admin, _ := kgo.NewClient(kgo.SeedBrokers(broker))
	defer admin.Close()
	if _, err := kadm.NewClient(admin).CreateTopic(context.Background(), 3, 1, nil, topic); err != nil {
		t.Fatal(err)
	}
	prov := fmt.Sprintf("prov%d", n) // unique per test: fl-cache state is shared across tests
	cfg := &config.Config{Guard: config.Guard{Threshold: 0.9}, Providers: map[string]config.Provider{
		prov: {Loc: time.UTC, Models: map[string]config.Limits{"small": limits}},
	}}
	m = metrics.New()
	g = guard.New(cfg, kv, m, quiet)

	prod, _ := kgo.NewClient(kgo.SeedBrokers(broker), kgo.DefaultProduceTopic(topic))
	t.Cleanup(prod.Close)
	produce = func(evs ...event.Event) {
		var recs []*kgo.Record
		for _, e := range evs {
			b, _ := json.Marshal(e)
			recs = append(recs, &kgo.Record{Key: []byte(strconv.FormatInt(e.AppID, 10)), Value: b})
		}
		if err := prod.ProduceSync(context.Background(), recs...).FirstErr(); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PROVIDER", prov)
	return
}

// has reports whether a key exists (fl-cache implements GET but not EXISTS).
func has(ctx context.Context, key string) bool {
	_, err := raw.Get(ctx, key).Result()
	return err == nil
}

func newEvent(provider, model string) event.Event {
	id, _ := uuid.NewV7()
	return event.Event{RequestID: id, TS: time.Now().UTC(), AppID: 1, Provider: provider, Model: model,
		PromptTokens: 5, CompletionTokens: 5, Status: 200, Attempts: 1}
}

func runGuard(t *testing.T, topic, group string, g *guard.Guard, m *metrics.Metrics) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(consumer.Ready)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = consumer.Run(ctx, consumer.Config{Brokers: []string{broker}, Topic: topic, Group: group,
			Guard: g, Metrics: m, Log: quiet, EvalInterval: time.Hour}, ready)
	}()
	select {
	case <-ready:
	case <-time.After(20 * time.Second):
		t.Fatal("guard did not start")
	}
	t.Cleanup(func() { cancel(); <-done })
	return cancel
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func TestGuardFlagsInTheRealCacheAndRedeliveryDoesNotDoubleCount(t *testing.T) {
	topic, g, m, produce := setup(t, config.Limits{RequestsPerDay: 10})
	prov := os.Getenv("PROVIDER")
	ctx := context.Background()

	var evs []event.Event
	for i := 0; i < 8; i++ {
		evs = append(evs, newEvent(prov, "small"))
	}
	produce(evs...)
	cancelA := runGuard(t, topic, "budget-guard-a", g, m)
	eventually(t, "8 events counted", func() bool { return testutil.ToFloat64(m.ModelRequests.WithLabelValues(prov, "small")) == 8 })
	if has(ctx, "exhausted:"+prov+"/small") {
		t.Fatal("flagged at 80%")
	}

	// A second guard group re-reads the entire log (what a redelivery after a crash looks like).
	// Nothing may be counted twice.
	m2 := metrics.New()
	g2 := guard.New(g.Cfg, kv, m2, quiet)
	runGuard(t, topic, "budget-guard-b", g2, m2)
	eventually(t, "second group saw the 8 events as duplicates", func() bool { return testutil.ToFloat64(m2.Duplicates) == 8 })
	if got, _ := cache.GetIntFromRaw(ctx, raw, fmt.Sprintf("guard:req:%s/small:%s", prov, time.Now().UTC().Format("2006-01-02"))); got != 8 {
		t.Errorf("request counter in fl-cache = %d after redelivery, want 8", got)
	}

	// Two more events cross 90% (9 of 10).
	produce(newEvent(prov, "small"))
	eventually(t, "flag appears in fl-cache", func() bool { return has(ctx, "exhausted:"+prov+"/small") })
	ttl := raw.TTL(ctx, "exhausted:"+prov+"/small").Val()
	untilMidnight := time.Until(time.Now().UTC().Truncate(24 * time.Hour).Add(24 * time.Hour))
	if ttl <= 0 || ttl > untilMidnight+2*time.Second {
		t.Errorf("flag TTL = %v, want within (0, %v] (until UTC midnight)", ttl, untilMidnight)
	}
	if ttl := raw.TTL(ctx, "seen:"+evs[0].RequestID.String()).Val(); ttl < 47*time.Hour {
		t.Errorf("seen: marker TTL = %v, want ~48h", ttl)
	}
	cancelA()
}
