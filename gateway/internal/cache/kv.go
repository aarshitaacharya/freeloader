// Package cache is the gateway's view of fl-cache: a tiny key-value interface
// plus a Redis-protocol implementation and an in-memory fake for tests.
package cache

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// KV is the subset of fl-cache the gateway uses. Every method may fail when the
// cache is down or slow; callers decide whether to fail open (they do).
type KV interface {
	// Get returns (value, true, nil) on a hit and (nil, false, nil) on a miss.
	Get(ctx context.Context, key string) ([]byte, bool, error)
	// Set stores val for ttl (rounded up to whole seconds, minimum 1s).
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	// IncrWindow runs `SET key 0 EX ttl NX` then `INCRBY key n`, pipelined, and
	// returns the new value. The key expires ttl after it was first created.
	IncrWindow(ctx context.Context, key string, ttl time.Duration, n int64) (int64, error)
	Ping(ctx context.Context) error
}

// GetInt reads an integer counter; a missing key is 0.
func GetInt(ctx context.Context, kv KV, key string) (int64, error) {
	b, ok, err := kv.Get(ctx, key)
	if err != nil || !ok {
		return 0, err
	}
	return strconv.ParseInt(string(b), 10, 64)
}

func wholeSeconds(d time.Duration) time.Duration {
	s := (d + time.Second - 1) / time.Second
	if s < 1 {
		s = 1
	}
	return s * time.Second
}

// Redis talks to fl-cache (or real Redis) using the stock go-redis client.
type Redis struct {
	c       *redis.Client
	timeout time.Duration
}

func NewRedis(addr string, timeout time.Duration) *Redis {
	return &Redis{
		c: redis.NewClient(&redis.Options{
			Addr:         addr,
			DialTimeout:  timeout,
			ReadTimeout:  timeout,
			WriteTimeout: timeout,
			PoolSize:     64,
			Protocol:     2, // RESP2: fl-cache does not implement HELLO/RESP3
		}),
		timeout: timeout,
	}
}

func (r *Redis) Close() error { return r.c.Close() }

func (r *Redis) op(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, r.timeout)
}

func (r *Redis) Get(ctx context.Context, key string) ([]byte, bool, error) {
	ctx, cancel := r.op(ctx)
	defer cancel()
	b, err := r.c.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

func (r *Redis) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	ctx, cancel := r.op(ctx)
	defer cancel()
	// Whole seconds so go-redis emits `EX n` (fl-cache has no PX).
	return r.c.Set(ctx, key, val, wholeSeconds(ttl)).Err()
}

func (r *Redis) IncrWindow(ctx context.Context, key string, ttl time.Duration, n int64) (int64, error) {
	ctx, cancel := r.op(ctx)
	defer cancel()
	pipe := r.c.Pipeline()
	pipe.SetNX(ctx, key, 0, wholeSeconds(ttl))
	incr := pipe.IncrBy(ctx, key, n)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return incr.Val(), nil
}

func (r *Redis) Ping(ctx context.Context) error {
	ctx, cancel := r.op(ctx)
	defer cancel()
	return r.c.Ping(ctx).Err()
}

// Mem is an in-memory KV with TTLs, for tests. Set Err to simulate an outage.
type Mem struct {
	mu   sync.Mutex
	data map[string]memEntry
	Now  func() time.Time
	Err  error // if non-nil, every call returns it

	Gets, Sets int // call counters (guarded by mu)
}

type memEntry struct {
	val     []byte
	expires time.Time
}

func NewMem() *Mem { return &Mem{data: map[string]memEntry{}, Now: time.Now} }

func (m *Mem) live(key string) (memEntry, bool) {
	e, ok := m.data[key]
	if ok && !e.expires.IsZero() && !m.Now().Before(e.expires) {
		delete(m.data, key)
		return memEntry{}, false
	}
	return e, ok
}

func (m *Mem) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Gets++
	if m.Err != nil {
		return nil, false, m.Err
	}
	e, ok := m.live(key)
	return e.val, ok, nil
}

func (m *Mem) Set(_ context.Context, key string, val []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Sets++
	if m.Err != nil {
		return m.Err
	}
	m.data[key] = memEntry{val: append([]byte(nil), val...), expires: m.Now().Add(wholeSeconds(ttl))}
	return nil
}

func (m *Mem) IncrWindow(_ context.Context, key string, ttl time.Duration, n int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return 0, m.Err
	}
	e, ok := m.live(key)
	if !ok {
		e = memEntry{val: []byte("0"), expires: m.Now().Add(wholeSeconds(ttl))}
	}
	cur, err := strconv.ParseInt(string(e.val), 10, 64)
	if err != nil {
		return 0, err
	}
	cur += n
	e.val = []byte(strconv.FormatInt(cur, 10))
	m.data[key] = e
	return cur, nil
}

func (m *Mem) Ping(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Err
}
