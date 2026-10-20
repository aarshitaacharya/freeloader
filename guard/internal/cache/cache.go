// Package cache is the guard's view of fl-cache (counters, dedupe markers, flags).
package cache

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type KV interface {
	// SetNX sets key only if absent (SET key 1 EX ttl NX) and reports whether it did.
	SetNX(ctx context.Context, key string, ttl time.Duration) (bool, error)
	Set(ctx context.Context, key, val string, ttl time.Duration) error
	Del(ctx context.Context, key string) error
	GetInt(ctx context.Context, key string) (int64, error) // a missing key is 0
	Exists(ctx context.Context, key string) (bool, error)
	// IncrWindow runs `SET key 0 EX ttl NX` then `INCRBY key n`, pipelined; returns the new value.
	IncrWindow(ctx context.Context, key string, ttl time.Duration, n int64) (int64, error)
	Ping(ctx context.Context) error
}

func whole(d time.Duration) time.Duration {
	s := (d + time.Second - 1) / time.Second
	if s < 1 {
		s = 1
	}
	return s * time.Second // fl-cache only understands EX, so whole seconds
}

type Redis struct {
	c       *redis.Client
	timeout time.Duration
}

func NewRedis(addr string, timeout time.Duration) *Redis {
	return &Redis{timeout: timeout, c: redis.NewClient(&redis.Options{
		Addr: addr, DialTimeout: timeout, ReadTimeout: timeout, WriteTimeout: timeout, Protocol: 2,
	})}
}

func (r *Redis) Close() error { return r.c.Close() }
func (r *Redis) op(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, r.timeout)
}

func (r *Redis) SetNX(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	ctx, cancel := r.op(ctx)
	defer cancel()
	return r.c.SetNX(ctx, key, "1", whole(ttl)).Result()
}

func (r *Redis) Set(ctx context.Context, key, val string, ttl time.Duration) error {
	ctx, cancel := r.op(ctx)
	defer cancel()
	return r.c.Set(ctx, key, val, whole(ttl)).Err()
}

func (r *Redis) Del(ctx context.Context, key string) error {
	ctx, cancel := r.op(ctx)
	defer cancel()
	return r.c.Del(ctx, key).Err()
}

func (r *Redis) GetInt(ctx context.Context, key string) (int64, error) {
	ctx, cancel := r.op(ctx)
	defer cancel()
	n, err := r.c.Get(ctx, key).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return n, err
}

func (r *Redis) Exists(ctx context.Context, key string) (bool, error) {
	ctx, cancel := r.op(ctx)
	defer cancel()
	_, err := r.c.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	return err == nil, err
}

func (r *Redis) IncrWindow(ctx context.Context, key string, ttl time.Duration, n int64) (int64, error) {
	ctx, cancel := r.op(ctx)
	defer cancel()
	pipe := r.c.Pipeline()
	pipe.SetNX(ctx, key, 0, whole(ttl))
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

// Mem is an in-memory KV with TTLs for tests. Set Err to simulate an outage.
type Mem struct {
	mu   sync.Mutex
	data map[string]memEntry
	Now  func() time.Time
	Err  error
}

type memEntry struct {
	val     string
	expires time.Time
}

func NewMem() *Mem { return &Mem{data: map[string]memEntry{}, Now: time.Now} }

func (m *Mem) live(key string) (memEntry, bool) {
	e, ok := m.data[key]
	if ok && !m.Now().Before(e.expires) {
		delete(m.data, key)
		return memEntry{}, false
	}
	return e, ok
}

func (m *Mem) SetNX(_ context.Context, key string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return false, m.Err
	}
	if _, ok := m.live(key); ok {
		return false, nil
	}
	m.data[key] = memEntry{"1", m.Now().Add(whole(ttl))}
	return true, nil
}

func (m *Mem) Set(_ context.Context, key, val string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	m.data[key] = memEntry{val, m.Now().Add(whole(ttl))}
	return nil
}

func (m *Mem) Del(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	delete(m.data, key)
	return nil
}

func (m *Mem) GetInt(_ context.Context, key string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return 0, m.Err
	}
	e, ok := m.live(key)
	if !ok {
		return 0, nil
	}
	return strconv.ParseInt(e.val, 10, 64)
}

func (m *Mem) Exists(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return false, m.Err
	}
	_, ok := m.live(key)
	return ok, nil
}

func (m *Mem) TTL(key string) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.live(key)
	if !ok {
		return -1
	}
	return e.expires.Sub(m.Now())
}

func (m *Mem) IncrWindow(_ context.Context, key string, ttl time.Duration, n int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return 0, m.Err
	}
	e, ok := m.live(key)
	if !ok {
		e = memEntry{"0", m.Now().Add(whole(ttl))}
	}
	cur, err := strconv.ParseInt(e.val, 10, 64)
	if err != nil {
		return 0, err
	}
	cur += n
	e.val = strconv.FormatInt(cur, 10)
	m.data[key] = e
	return cur, nil
}

func (m *Mem) Ping(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Err
}

// GetIntFromRaw reads an integer through a plain go-redis client (tests use it to
// inspect what the guard wrote without going through the guard's own wrapper).
func GetIntFromRaw(ctx context.Context, c *redis.Client, key string) (int64, error) {
	n, err := c.Get(ctx, key).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return n, err
}
