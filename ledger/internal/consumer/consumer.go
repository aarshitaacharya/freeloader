// Package consumer reads the llm.usage topic and feeds the store.
package consumer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/aarshitaacharya/freeloader/ledger/internal/event"
	"github.com/aarshitaacharya/freeloader/ledger/internal/metrics"
	"github.com/aarshitaacharya/freeloader/ledger/internal/store"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

type Config struct {
	Brokers        []string
	Topic          string
	Group          string
	CommitInterval time.Duration // how often processed offsets are committed to Kafka
	MaxBatch       int
	Store          *store.Store
	Metrics        *metrics.Metrics
	Log            *slog.Logger

	// SkipFinalCommit simulates a crash on shutdown (tests): processed events are
	// not committed, so they are redelivered on the next start.
	SkipFinalCommit bool
}

func (c *Config) defaults() {
	if c.CommitInterval <= 0 {
		c.CommitInterval = time.Second
	}
	if c.MaxBatch <= 0 {
		c.MaxBatch = 500
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
}

// Ready is closed once the consumer has joined the group and is polling.
type Ready chan struct{}

// Run consumes until ctx is cancelled.
//
// At-least-once, exactly-once in effect:
//  1. poll a batch;  2. write it to Postgres in one transaction (Store.Apply);
//  3. only then remember the records as done; 4. commit those offsets to Kafka on a
//     timer, on rebalance and on graceful shutdown.
//
// A crash between steps 2 and 4 means the batch is redelivered and Store.Apply
// ignores the events it has already recorded (counted in duplicates_total).
func Run(ctx context.Context, c Config, ready Ready) error {
	c.defaults()
	// Every running ledger holds the shared lock; replay needs the exclusive one, so a
	// replay can never truncate the tables underneath a live consumer.
	lock, ok, err := c.Store.LockShared(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("could not take the ledger lock: a replay is running")
	}
	defer lock.Release()
	pending := &committer{last: map[int32]*kgo.Record{}}
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(c.Brokers...),
		kgo.ConsumerGroup(c.Group),
		kgo.ConsumeTopics(c.Topic),
		// After a hard kill the dead member's partitions are only reclaimed when its session expires.
		// 10 s (franz-go's default is 45 s) keeps a crash from stalling consumption for most of a minute.
		kgo.SessionTimeout(10*time.Second),
		kgo.RebalanceTimeout(30*time.Second),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		// We decide when offsets are committed: franz-go never commits on its own.
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
		kgo.OnPartitionsRevoked(func(ctx context.Context, cl *kgo.Client, _ map[string][]int32) {
			if !c.SkipFinalCommit {
				pending.flush(ctx, cl, c.Log) // hand partitions over with as little redelivery as possible
			}
		}),
		kgo.OnPartitionsAssigned(func(_ context.Context, _ *kgo.Client, assigned map[string][]int32) {
			c.Log.Info("partitions assigned", "partitions", assigned[c.Topic])
		}),
	)
	if err != nil {
		return err
	}
	defer cl.Close()
	if ready != nil {
		close(ready)
	}

	lastCommit := time.Now()
	lagSeen := map[int32]bool{}
	for ctx.Err() == nil {
		pctx, cancel := context.WithTimeout(ctx, time.Second)
		fetches := cl.PollRecords(pctx, c.MaxBatch)
		cancel()
		if fetches.IsClientClosed() {
			break
		}
		fetches.EachError(func(topic string, p int32, err error) {
			if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
				c.Log.Warn("fetch error", "topic", topic, "partition", p, "err", err)
			}
		})

		records := fetches.Records()
		if len(records) == 0 {
			// A poll that comes back empty means nothing was waiting: we are caught up.
			for p := range lagSeen {
				c.Metrics.Lag.WithLabelValues(strconv.Itoa(int(p))).Set(0)
			}
		} else {
			if err := process(ctx, c, records); err != nil {
				cl.AllowRebalance()
				break // ctx cancelled while retrying; nothing was marked
			}
			pending.note(records) // only now, after the database commit, may these offsets be committed
			fetches.EachPartition(func(p kgo.FetchTopicPartition) {
				if len(p.Records) == 0 {
					return
				}
				last := p.Records[len(p.Records)-1].Offset
				c.Metrics.Lag.WithLabelValues(strconv.Itoa(int(p.Partition))).Set(float64(p.HighWatermark - (last + 1)))
				lagSeen[p.Partition] = true
			})
		}
		cl.AllowRebalance()

		if time.Since(lastCommit) >= c.CommitInterval {
			pending.flush(ctx, cl, c.Log)
			lastCommit = time.Now()
		}
	}

	if !c.SkipFinalCommit {
		fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pending.flush(fctx, cl, c.Log)
	}
	return nil
}

// committer remembers the last record that has been durably written to Postgres on
// each partition, and commits those offsets to Kafka when asked.
type committer struct {
	mu   sync.Mutex
	last map[int32]*kgo.Record
}

func (c *committer) note(records []*kgo.Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range records {
		if cur := c.last[r.Partition]; cur == nil || r.Offset > cur.Offset {
			c.last[r.Partition] = r
		}
	}
}

func (c *committer) flush(ctx context.Context, cl *kgo.Client, log *slog.Logger) {
	c.mu.Lock()
	recs := make([]*kgo.Record, 0, len(c.last))
	for _, r := range c.last {
		recs = append(recs, r)
	}
	c.mu.Unlock()
	if len(recs) == 0 {
		return
	}
	if err := cl.CommitRecords(ctx, recs...); err != nil {
		if ctx.Err() == nil {
			log.Warn("offset commit failed (events will be redelivered and deduped)", "err", err)
		}
		return
	}
	c.mu.Lock()
	for _, r := range recs {
		if c.last[r.Partition] == r { // nothing newer arrived while we were committing
			delete(c.last, r.Partition)
		}
	}
	c.mu.Unlock()
}

// process parses a batch and writes it, retrying with backoff until it succeeds.
// It returns an error only when ctx is cancelled first.
func process(ctx context.Context, c Config, records []*kgo.Record) error {
	evs := make([]event.Event, 0, len(records))
	for _, r := range records {
		e, err := event.Parse(r.Value)
		if err != nil {
			c.Metrics.Invalid.Inc()
			c.Log.Warn("skipping invalid record", "partition", r.Partition, "offset", r.Offset, "err", err)
			continue
		}
		evs = append(evs, e)
	}
	backoff := 250 * time.Millisecond
	for {
		start := time.Now()
		res, err := c.Store.Apply(ctx, evs)
		if err == nil {
			c.Metrics.Batch.Observe(time.Since(start).Seconds())
			c.Metrics.Events.Add(float64(res.Inserted))
			c.Metrics.Duplicates.Add(float64(res.Duplicates))
			if res.Duplicates > 0 {
				c.Log.Info("ignored redelivered events", "duplicates", res.Duplicates, "inserted", res.Inserted)
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.Metrics.DBErrors.Inc()
		c.Log.Error("writing batch failed; will retry (offsets not advanced)", "err", err, "retry_in", backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

// ReplayConfig configures a rebuild from the log.
type ReplayConfig struct {
	Brokers []string
	Topic   string
	Group   string // prefix; a fresh suffix is appended
	Store   *store.Store
	Log     *slog.Logger
	// Force replays even if the log no longer starts at offset 0 (retention has
	// removed old events, so the rebuilt totals will be smaller than before).
	Force bool
}

type ReplayResult struct {
	Events   int // records read
	Inserted int
	Duplic   int
	Invalid  int
}

// Replay truncates usage_events and usage_daily and rebuilds both from the
// earliest offset of the topic under a fresh consumer group, proving the log is
// the source of truth. It refuses to run while any ledger is consuming.
func Replay(ctx context.Context, c ReplayConfig) (ReplayResult, error) {
	var res ReplayResult
	if c.Log == nil {
		c.Log = slog.Default()
	}
	lock, ok, err := c.Store.LockExclusive(ctx)
	if err != nil {
		return res, err
	}
	if !ok {
		return res, errors.New("a ledger is running (it holds the shared lock): stop every ledger before replaying")
	}
	defer lock.Release()

	admin, err := kgo.NewClient(kgo.SeedBrokers(c.Brokers...))
	if err != nil {
		return res, err
	}
	defer admin.Close()
	adm := kadm.NewClient(admin)
	starts, err := adm.ListStartOffsets(ctx, c.Topic)
	if err != nil {
		return res, fmt.Errorf("list start offsets: %w", err)
	}
	ends, err := adm.ListEndOffsets(ctx, c.Topic)
	if err != nil {
		return res, fmt.Errorf("list end offsets: %w", err)
	}
	target := map[int32]int64{}
	var aged []int32
	ends.Each(func(o kadm.ListedOffset) { target[o.Partition] = o.Offset })
	starts.Each(func(o kadm.ListedOffset) {
		if o.Offset > 0 {
			aged = append(aged, o.Partition)
		}
	})
	if len(aged) > 0 && !c.Force {
		return res, fmt.Errorf("partitions %v no longer start at offset 0 (retention removed old events): replay would lose history; rerun with --force to accept that", aged)
	}

	c.Log.Warn("replay: truncating usage_events and usage_daily")
	if err := c.Store.Truncate(ctx); err != nil {
		return res, err
	}

	group := fmt.Sprintf("%s-replay-%d", c.Group, time.Now().Unix())
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(c.Brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(c.Topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(), // a replay group never needs to remember where it was
	)
	if err != nil {
		return res, err
	}
	defer cl.Close()

	next := map[int32]int64{}
	remaining := 0
	for p, end := range target {
		next[p] = 0
		if end > 0 {
			remaining++
		}
	}
	c.Log.Info("replay: consuming", "group", group, "end_offsets", target)
	for remaining > 0 {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		fetches := cl.PollRecords(pctx, 1000)
		cancel()
		var evs []event.Event
		for _, r := range fetches.Records() {
			if r.Offset >= target[r.Partition] {
				continue // written after replay started: the live ledger will handle it
			}
			res.Events++
			if e, err := event.Parse(r.Value); err != nil {
				res.Invalid++
			} else {
				evs = append(evs, e)
			}
			if r.Offset+1 > next[r.Partition] {
				next[r.Partition] = r.Offset + 1
			}
		}
		if len(evs) > 0 {
			r, err := c.Store.Apply(ctx, evs)
			if err != nil {
				return res, err
			}
			res.Inserted += r.Inserted
			res.Duplic += r.Duplicates
		}
		remaining = 0
		for p, end := range target {
			if next[p] < end {
				remaining++
			}
		}
	}
	return res, nil
}
