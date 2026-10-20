// Package events publishes one usage event per request to the llm.usage topic.
package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Event is the JSON value of a llm.usage record; the record key is app_id so one
// app's events stay in order within a partition.
type Event struct {
	RequestID        string    `json:"request_id"` // UUIDv7
	TS               time.Time `json:"ts"`
	AppID            int64     `json:"app_id"`
	Provider         string    `json:"provider"` // who answered; "" if nobody did
	Model            string    `json:"model"`    // the provider-side model id
	Alias            string    `json:"alias"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	TokensEstimated  bool      `json:"tokens_estimated"`
	LatencyMS        int64     `json:"latency_ms"`
	Status           int       `json:"status"`
	CacheHit         bool      `json:"cache_hit"`
	Attempts         int       `json:"attempts"`
}

// Publisher must never block or fail the request path.
type Publisher interface {
	Publish(Event)
}

// Noop discards events (event streaming not configured).
type Noop struct{}

func (Noop) Publish(Event) {}

// Memory collects events in memory, for tests.
type Memory struct {
	mu     sync.Mutex
	events []Event
}

func (m *Memory) Publish(e Event) {
	m.mu.Lock()
	m.events = append(m.events, e)
	m.mu.Unlock()
}

func (m *Memory) Events() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Event(nil), m.events...)
}

// Kafka publishes to a Kafka-API broker (Redpanda) with franz-go.
//
// Serving requests matters more than perfect accounting: Publish never blocks.
// If the broker is down the producer buffers up to maxBuffered events; beyond
// that, or when delivery fails after the timeout, events are dropped and counted
// in fl_gateway_events_dropped_total.
type Kafka struct {
	cl      *kgo.Client
	dropped prometheus.Counter
	log     *slog.Logger
	lastLog atomic.Int64 // unix nanos of the last drop warning, to avoid log floods
}

const (
	maxBuffered     = 10000
	deliveryTimeout = 10 * time.Second
)

// NewKafka creates the producer. It does not contact the broker, so the gateway
// starts fine while Redpanda is still coming up or is down. extra options are
// applied last and override the defaults (used by tests to shorten timeouts).
func NewKafka(brokers []string, topic string, dropped prometheus.Counter, log *slog.Logger, extra ...kgo.Opt) (*Kafka, error) {
	opts := []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.DefaultProduceTopic(topic),
		kgo.RequiredAcks(kgo.AllISRAcks()), // acks=all
		// Not idempotent, deliberately: an idempotent producer cannot fail a batch it has already
		// sequenced, so during a broker outage buffered events could never time out and newer
		// events would queue behind them forever. Duplicates after a retry are harmless: the ledger
		// dedupes on request_id. One in-flight request per broker keeps one app's events in order
		// across retries.
		kgo.DisableIdempotentWrite(),
		kgo.MaxProduceRequestsInflightPerBroker(1),
		kgo.MaxBufferedRecords(maxBuffered),
		kgo.ProducerLinger(10 * time.Millisecond),
		kgo.RecordDeliveryTimeout(deliveryTimeout),
		// A stopped broker often leaves a half-open TCP connection that never answers (no
		// refusal). Short timeouts make the producer notice in seconds, not minutes.
		kgo.DialTimeout(3 * time.Second),
		kgo.ProduceRequestTimeout(3 * time.Second),
		kgo.RequestTimeoutOverhead(3 * time.Second),
	}
	cl, err := kgo.NewClient(append(opts, extra...)...) // extra options (tests) override the defaults
	if err != nil {
		return nil, err
	}
	return &Kafka{cl: cl, dropped: dropped, log: log}, nil
}

func (k *Kafka) Publish(e Event) {
	value, err := json.Marshal(e)
	if err != nil {
		k.drop(err)
		return
	}
	rec := &kgo.Record{Key: []byte(strconv.FormatInt(e.AppID, 10)), Value: value}
	// TryProduce fails immediately (instead of blocking) when the buffer is full.
	k.cl.TryProduce(context.Background(), rec, func(_ *kgo.Record, err error) {
		if err != nil {
			k.drop(err)
		}
	})
}

func (k *Kafka) drop(err error) {
	k.dropped.Inc()
	now := time.Now().UnixNano()
	if last := k.lastLog.Load(); now-last > int64(10*time.Second) && k.lastLog.CompareAndSwap(last, now) {
		k.log.Warn("usage event dropped (is the event stream down?)", "err", err)
	}
}

// Buffered is the number of events accepted but not yet delivered. It rises as soon
// as the broker stops acknowledging, so an outage shows up before any event is
// dropped.
func (k *Kafka) Buffered() int { return int(k.cl.BufferedProduceRecords()) }

// Close flushes buffered events (up to ctx's deadline) and disconnects.
func (k *Kafka) Close(ctx context.Context) {
	_ = k.cl.Flush(ctx)
	k.cl.Close()
}
