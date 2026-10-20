// Package consumer feeds usage events to the guard from consumer group "budget-guard".
// It is independent of the ledger's group: it can lag or crash without affecting it.
package consumer

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/aarshitaacharya/freeloader/guard/internal/event"
	"github.com/aarshitaacharya/freeloader/guard/internal/guard"
	"github.com/aarshitaacharya/freeloader/guard/internal/metrics"
	"github.com/twmb/franz-go/pkg/kgo"
)

type Config struct {
	Brokers      []string
	Topic, Group string
	Guard        *guard.Guard
	Metrics      *metrics.Metrics
	Log          *slog.Logger
	EvalInterval time.Duration // how often EvaluateAll runs; default 15 s
}

type Ready chan struct{}

// Run consumes until ctx is cancelled. At-least-once: offsets are marked only after
// every event of a batch has been handled, and Handle is idempotent (seen: markers).
func Run(ctx context.Context, c Config, ready Ready) error {
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.EvalInterval <= 0 {
		c.EvalInterval = 15 * time.Second
	}
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(c.Brokers...),
		kgo.ConsumerGroup(c.Group),
		kgo.ConsumeTopics(c.Topic),
		// After a hard kill the dead member's partitions are only reclaimed when its session expires.
		// 10 s (franz-go's default is 45 s) keeps a crash from stalling consumption for most of a minute.
		kgo.SessionTimeout(10*time.Second),
		kgo.RebalanceTimeout(30*time.Second),
		// A first start reads the whole retained log; Handle ignores events from earlier days.
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.AutoCommitMarks(),
		kgo.AutoCommitInterval(time.Second),
		kgo.BlockRebalanceOnPoll(),
	)
	if err != nil {
		return err
	}
	defer cl.Close()

	// Bring flags in line with the configured limits right away (a lowered limit takes
	// effect now, not at the next event) and then keep doing so.
	evalAll := func() {
		if err := c.Guard.EvaluateAll(ctx); err != nil && ctx.Err() == nil {
			c.Log.Warn("evaluating limits failed", "err", err)
		}
	}
	evalAll()
	go func() {
		t := time.NewTicker(c.EvalInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				evalAll()
			}
		}
	}()
	if ready != nil {
		close(ready)
	}

	for ctx.Err() == nil {
		pctx, cancel := context.WithTimeout(ctx, time.Second)
		fetches := cl.PollRecords(pctx, 500)
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
		done := true
		for _, r := range records {
			e, err := event.Parse(r.Value)
			if err != nil {
				c.Metrics.Invalid.Inc()
				continue
			}
			if err := handle(ctx, c, e); err != nil {
				done = false // ctx cancelled while retrying
				break
			}
		}
		if done && len(records) > 0 {
			cl.MarkCommitRecords(records...)
		}
		cl.AllowRebalance()
	}
	fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = cl.CommitMarkedOffsets(fctx)
	return nil
}

// handle retries cache failures with backoff: dropping an event would silently
// under-count, while waiting only delays the flag.
func handle(ctx context.Context, c Config, e event.Event) error {
	backoff := 200 * time.Millisecond
	for {
		err := c.Guard.Handle(ctx, e)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.Log.Warn("handling event failed; retrying", "err", err, "retry_in", backoff)
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
