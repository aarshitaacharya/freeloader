//go:build integration

package consumer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aarshitaacharya/freeloader/ledger/internal/consumer"
	"github.com/aarshitaacharya/freeloader/ledger/internal/event"
	"github.com/aarshitaacharya/freeloader/ledger/internal/metrics"
	"github.com/aarshitaacharya/freeloader/ledger/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/modules/redpanda"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

var (
	pool    *pgxpool.Pool
	st      *store.Store
	broker  string
	quiet   = slog.New(slog.NewTextHandler(io.Discard, nil))
	topicNo int
	topicMu sync.Mutex
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	pg, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("freeloader"), postgres.WithUsername("freeloader"), postgres.WithPassword("pw"),
		postgres.WithInitScripts("../../../migrations/000002_usage.up.sql"), postgres.BasicWaitStrategies())
	if err != nil {
		fmt.Fprintln(os.Stderr, "postgres:", err)
		os.Exit(1)
	}
	dsn, _ := pg.ConnectionString(ctx, "sslmode=disable")
	pool, _ = pgxpool.New(ctx, dsn)
	st = &store.Store{Pool: pool}

	rp, err := redpanda.Run(ctx, "docker.redpanda.com/redpandadata/redpanda:v26.2.5")
	if err != nil {
		fmt.Fprintln(os.Stderr, "redpanda:", err)
		os.Exit(1)
	}
	broker, _ = rp.KafkaSeedBroker(ctx)

	code := m.Run()
	pool.Close()
	_ = testcontainers.TerminateContainer(pg)
	_ = testcontainers.TerminateContainer(rp)
	os.Exit(code)
}

// ---- helpers ----------------------------------------------------------------

type env struct {
	t     *testing.T
	topic string
	group string
	prod  *kgo.Client
}

// newEnv gives each test its own topic (6 partitions) and consumer group, and empty tables.
func newEnv(t *testing.T) *env {
	t.Helper()
	topicMu.Lock()
	topicNo++
	n := topicNo
	topicMu.Unlock()
	e := &env{t: t, topic: fmt.Sprintf("llm.usage.t%d", n), group: fmt.Sprintf("ledger-t%d", n)}

	admin, err := kgo.NewClient(kgo.SeedBrokers(broker))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := kadm.NewClient(admin).CreateTopic(context.Background(), 6, 1, nil, e.topic); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `ALTER TABLE usage_events DROP CONSTRAINT IF EXISTS no_negative_tokens`); err != nil {
		t.Fatal(err)
	}
	if err := st.Truncate(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.prod, err = kgo.NewClient(kgo.SeedBrokers(broker), kgo.DefaultProduceTopic(e.topic), kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.prod.Close)
	return e
}

func ev(app int64, provider string, tokens int64, cacheHit bool, day time.Time) event.Event {
	id, _ := uuid.NewV7()
	return event.Event{RequestID: id, TS: day, AppID: app, Provider: provider, Model: "m", Alias: "fast",
		PromptTokens: tokens / 2, CompletionTokens: tokens - tokens/2, Status: 200, CacheHit: cacheHit, Attempts: 1, LatencyMS: 5}
}

func (e *env) produce(evs ...event.Event) {
	e.t.Helper()
	var recs []*kgo.Record
	for _, x := range evs {
		b, _ := json.Marshal(x)
		recs = append(recs, &kgo.Record{Key: []byte(strconv.FormatInt(x.AppID, 10)), Value: b})
	}
	if err := e.prod.ProduceSync(context.Background(), recs...).FirstErr(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) produceRaw(v string) {
	e.t.Helper()
	if err := e.prod.ProduceSync(context.Background(), &kgo.Record{Key: []byte("0"), Value: []byte(v)}).FirstErr(); err != nil {
		e.t.Fatal(err)
	}
}

type running struct {
	m      *metrics.Metrics
	cancel context.CancelFunc
	done   chan struct{}
}

// start runs a ledger replica until stop() is called.
func (e *env) start(commit time.Duration, crash bool) *running {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{m: metrics.New(), cancel: cancel, done: make(chan struct{})}
	ready := make(consumer.Ready)
	go func() {
		defer close(r.done)
		err := consumer.Run(ctx, consumer.Config{Brokers: []string{broker}, Topic: e.topic, Group: e.group,
			CommitInterval: commit, Store: st, Metrics: r.m, Log: quiet, SkipFinalCommit: crash}, ready)
		if err != nil {
			e.t.Errorf("consumer.Run: %v", err)
		}
	}()
	select {
	case <-ready:
	case <-time.After(20 * time.Second):
		e.t.Fatal("consumer did not start")
	}
	e.t.Cleanup(r.stop)
	return r
}

func (r *running) stop() {
	r.cancel()
	<-r.done
}

func (r *running) inserted() float64   { return testutil.ToFloat64(r.m.Events) }
func (r *running) duplicates() float64 { return testutil.ToFloat64(r.m.Duplicates) }

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

func countEvents(t *testing.T) int64 {
	t.Helper()
	n, err := st.CountEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

var today = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

// ---- tests ------------------------------------------------------------------

func TestApplyIsIdempotentAndCountsOnlyNewEvents(t *testing.T) {
	newEnv(t)
	ctx := context.Background()
	a := ev(1, "groq", 100, false, today)
	b := ev(1, "groq", 40, true, today) // cache hit: counts as a request and a cache hit
	c := ev(1, "", 0, false, today)     // nobody answered: its own provider bucket
	d := ev(2, "gemini", 10, false, today.AddDate(0, 0, 1))
	batch := []event.Event{a, b, c, d, a} // `a` twice in the same batch

	res, err := st.Apply(ctx, batch)
	if err != nil || res.Inserted != 4 || res.Duplicates != 1 {
		t.Fatalf("first apply: %+v %v", res, err)
	}
	want := map[string][3]int64{
		"2026-10-20/1/groq":   {2, 140, 1},
		"2026-10-20/1/":       {1, 0, 0},
		"2026-10-21/2/gemini": {1, 10, 0},
	}
	assertTotals(t, want)

	res, err = st.Apply(ctx, batch) // the whole batch is redelivered
	if err != nil || res.Inserted != 0 || res.Duplicates != 5 {
		t.Fatalf("redelivery: %+v %v", res, err)
	}
	assertTotals(t, want) // unchanged: exactly once in effect
	if n := countEvents(t); n != 4 {
		t.Errorf("usage_events = %d, want 4", n)
	}
}

func assertTotals(t *testing.T, want map[string][3]int64) {
	t.Helper()
	got, err := st.Totals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("rollup rows = %v, want %v", got, want)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("rollup %s = %v, want %v", k, got[k], w)
		}
	}
}

func TestEventAndRollupAreWrittenAtomically(t *testing.T) {
	newEnv(t)
	ctx := context.Background()
	// A rollup row that violates a constraint must also undo the event inserts.
	if _, err := pool.Exec(ctx, `ALTER TABLE usage_daily ADD CONSTRAINT no_negative_tokens CHECK (tokens >= 0)`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `ALTER TABLE usage_daily DROP CONSTRAINT no_negative_tokens`) }()

	good, bad := ev(1, "groq", 10, false, today), ev(2, "groq", -500, false, today)
	if _, err := st.Apply(ctx, []event.Event{good, bad}); err == nil {
		t.Fatal("expected the constraint violation to fail the batch")
	}
	if n := countEvents(t); n != 0 {
		t.Errorf("usage_events has %d rows after a failed batch; the transaction must roll back as a whole", n)
	}
	assertTotals(t, map[string][3]int64{})
}

func TestEveryEventIsRecordedAndDuplicatesAreIgnored(t *testing.T) {
	e := newEnv(t)
	var evs []event.Event
	want := map[string][3]int64{}
	for i := 0; i < 180; i++ {
		x := ev(int64(i%5+1), []string{"groq", "gemini"}[i%2], int64(10+i), i%7 == 0, today)
		evs = append(evs, x)
		k := fmt.Sprintf("2026-10-20/%d/%s", x.AppID, x.Provider)
		w := want[k]
		w[0]++
		w[1] += x.Tokens()
		if x.CacheHit {
			w[2]++
		}
		want[k] = w
	}
	e.produce(evs...)
	e.produce(evs[:20]...) // 20 events delivered twice (e.g. a producer retry)

	r := e.start(time.Second, false)
	eventually(t, "all 180 unique events", func() bool { return countEvents(t) == 180 })
	eventually(t, "20 duplicates seen", func() bool { return r.duplicates() == 20 })
	if r.inserted() != 180 {
		t.Errorf("fl_ledger_events_total = %v, want 180", r.inserted())
	}
	assertTotals(t, want)
}

func TestHardKillRedeliversAndDedupesWithoutDoubleCounting(t *testing.T) {
	e := newEnv(t)
	var evs []event.Event
	for i := 0; i < 100; i++ {
		evs = append(evs, ev(int64(i%4+1), "groq", 20, false, today))
	}
	e.produce(evs...)

	// Commit interval of an hour + a simulated crash: the events are processed and in
	// Postgres, but Kafka never hears about it.
	a := e.start(time.Hour, true)
	eventually(t, "replica A processed everything", func() bool { return countEvents(t) == 100 })
	totalsBefore, _ := st.Totals(context.Background())
	a.stop()

	b := e.start(time.Second, false) // restart: Kafka redelivers all 100
	eventually(t, "replica B saw the redelivery", func() bool { return b.duplicates() == 100 })
	if b.inserted() != 0 {
		t.Errorf("replica B inserted %v events; every redelivered event must be a duplicate", b.inserted())
	}
	if n := countEvents(t); n != 100 {
		t.Errorf("usage_events = %d, want exactly 100 (the number of requests)", n)
	}
	totalsAfter, _ := st.Totals(context.Background())
	for k, v := range totalsBefore {
		if totalsAfter[k] != v {
			t.Errorf("rollup %s changed from %v to %v: redelivery was double counted", k, v, totalsAfter[k])
		}
	}
}

func TestGracefulShutdownCommitsSoNothingIsRedelivered(t *testing.T) {
	e := newEnv(t)
	var evs []event.Event
	for i := 0; i < 50; i++ {
		evs = append(evs, ev(int64(i%3+1), "groq", 20, false, today))
	}
	e.produce(evs...)
	a := e.start(time.Hour, false) // never commits on a timer, only on shutdown
	eventually(t, "A processed everything", func() bool { return countEvents(t) == 50 })
	a.stop() // graceful: commits marked offsets

	b := e.start(time.Second, false)
	more := []event.Event{ev(1, "groq", 20, false, today), ev(2, "groq", 20, false, today)}
	e.produce(more...)
	eventually(t, "B processed the two new events", func() bool { return countEvents(t) == 52 })
	if b.duplicates() != 0 || b.inserted() != 2 {
		t.Errorf("after a graceful stop B should see only new events: inserted=%v duplicates=%v", b.inserted(), b.duplicates())
	}
}

func TestTwoReplicasSplitThePartitions(t *testing.T) {
	e := newEnv(t)
	a, b := e.start(time.Second, false), e.start(time.Second, false)
	var evs []event.Event
	for i := 0; i < 300; i++ {
		evs = append(evs, ev(int64(i%12+1), "groq", 10, false, today))
	}
	// Wait until the group has rebalanced across both replicas, then produce.
	time.Sleep(3 * time.Second)
	e.produce(evs...)
	eventually(t, "all 300 recorded", func() bool { return countEvents(t) == 300 })
	if a.inserted() == 0 || b.inserted() == 0 {
		t.Errorf("work was not shared: A=%v B=%v", a.inserted(), b.inserted())
	}
	if a.inserted()+b.inserted() != 300 || a.duplicates()+b.duplicates() != 0 {
		t.Errorf("A+B inserted %v, duplicates %v; want 300 and 0", a.inserted()+b.inserted(), a.duplicates()+b.duplicates())
	}
}

func TestInvalidRecordsAreSkippedNotBlocking(t *testing.T) {
	e := newEnv(t)
	e.produceRaw(`{not json`)
	e.produceRaw(`{"request_id":"nope","ts":"2026-10-20T00:00:00Z"}`)
	e.produce(ev(1, "groq", 10, false, today))
	r := e.start(time.Second, false)
	eventually(t, "the valid event recorded", func() bool { return countEvents(t) == 1 })
	eventually(t, "two invalid records counted", func() bool { return testutil.ToFloat64(r.m.Invalid) == 2 })
}

func TestDatabaseOutageIsRetriedWithoutLoss(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `ALTER TABLE usage_events RENAME TO usage_events_away`); err != nil {
		t.Fatal(err)
	}
	restored := false
	restore := func() {
		if !restored {
			restored = true
			_, _ = pool.Exec(ctx, `ALTER TABLE usage_events_away RENAME TO usage_events`)
		}
	}
	defer restore()

	var evs []event.Event
	for i := 0; i < 40; i++ {
		evs = append(evs, ev(1, "groq", 10, false, today))
	}
	e.produce(evs...)
	r := e.start(time.Second, false)
	eventually(t, "writes failing and being retried", func() bool { return testutil.ToFloat64(r.m.DBErrors) >= 2 })
	restore()
	eventually(t, "every event recorded once the database is back", func() bool { return countEvents(t) == 40 })
	if r.duplicates() != 0 {
		t.Errorf("duplicates = %v after a retried batch; a failed transaction must leave nothing behind", r.duplicates())
	}
}

func TestReplayRebuildsIdenticalTotals(t *testing.T) {
	e := newEnv(t)
	var evs []event.Event
	for i := 0; i < 150; i++ {
		evs = append(evs, ev(int64(i%6+1), []string{"groq", "gemini", ""}[i%3], int64(i), i%5 == 0,
			today.AddDate(0, 0, i%3))) // three different days
	}
	e.produce(evs...)
	r := e.start(time.Second, false)
	eventually(t, "ledger caught up", func() bool { return countEvents(t) == 150 })
	totals, _ := st.Totals(context.Background())

	// Replay refuses while a ledger is running.
	rc := consumer.ReplayConfig{Brokers: []string{broker}, Topic: e.topic, Group: e.group, Store: st, Log: quiet}
	if _, err := consumer.Replay(context.Background(), rc); err == nil || !strings.Contains(err.Error(), "ledger is running") {
		t.Fatalf("replay while a ledger runs: err = %v, want a refusal", err)
	}
	r.stop()

	// Damage the derived data so we can tell the rebuild really happened.
	if _, err := pool.Exec(context.Background(), `UPDATE usage_daily SET tokens = 0, requests = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `DELETE FROM usage_events WHERE app_id = 1`); err != nil {
		t.Fatal(err)
	}

	res, err := consumer.Replay(context.Background(), rc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Events != 150 || res.Inserted != 150 {
		t.Errorf("replay read %d and wrote %d, want 150/150", res.Events, res.Inserted)
	}
	if n := countEvents(t); n != 150 {
		t.Errorf("usage_events = %d after replay", n)
	}
	assertTotals(t, totals) // identical to before the damage
}

func TestReplayRefusesWhenRetentionHasRemovedHistory(t *testing.T) {
	e := newEnv(t)
	var evs []event.Event
	for i := 0; i < 60; i++ {
		evs = append(evs, ev(int64(i%3+1), "groq", 10, false, today))
	}
	e.produce(evs...)
	r := e.start(time.Second, false)
	eventually(t, "ledger caught up", func() bool { return countEvents(t) == 60 })
	r.stop()

	// Simulate retention deleting the oldest records of every partition.
	admin, _ := kgo.NewClient(kgo.SeedBrokers(broker))
	defer admin.Close()
	adm := kadm.NewClient(admin)
	ends, _ := adm.ListEndOffsets(context.Background(), e.topic)
	offs := kadm.Offsets{}
	ends.Each(func(o kadm.ListedOffset) {
		if o.Offset > 1 {
			offs.Add(kadm.Offset{Topic: e.topic, Partition: o.Partition, At: 1})
		}
	})
	if _, err := adm.DeleteRecords(context.Background(), offs); err != nil {
		t.Fatal(err)
	}

	rc := consumer.ReplayConfig{Brokers: []string{broker}, Topic: e.topic, Group: e.group, Store: st, Log: quiet}
	if _, err := consumer.Replay(context.Background(), rc); err == nil || !strings.Contains(err.Error(), "retention") {
		t.Fatalf("err = %v, want a refusal mentioning retention", err)
	}
	if n := countEvents(t); n != 60 {
		t.Errorf("a refused replay must not touch the tables; usage_events = %d", n)
	}
	rc.Force = true
	res, err := consumer.Replay(context.Background(), rc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Inserted >= 60 || res.Inserted == 0 {
		t.Errorf("forced replay rebuilt %d events; expected fewer than 60 (history was deleted)", res.Inserted)
	}
}
