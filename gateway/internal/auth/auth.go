// Package auth maps "Authorization: Bearer fl_..." to an app.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// App is one client application with its own key and budget.
type App struct {
	ID               int64
	Name             string
	RPMLimit         int   // requests per minute; <= 0 means unlimited
	TokensPerDay     int64 // <= 0 means unlimited
	CacheTTL         time.Duration
	ProviderPriority []string
	CacheAll         bool // opt in to caching requests whose temperature is not 0
	Disabled         bool
}

var (
	ErrMissingKey = errors.New("missing API key")
	ErrInvalidKey = errors.New("invalid API key")
	ErrDisabled   = errors.New("app is disabled")
	ErrNotFound   = errors.New("app not found")
)

const KeyPrefix = "fl_"

// Store looks apps up by the SHA-256 of their key.
type Store interface {
	LookupByKeyHash(ctx context.Context, hash []byte) (*App, error) // ErrNotFound if none
	Ping(ctx context.Context) error
}

// HashKey is how keys are stored: SHA-256, never the key itself.
func HashKey(key string) []byte {
	h := sha256.Sum256([]byte(key))
	return h[:]
}

type entry struct {
	app     *App // nil = known-bad key (negative cache)
	expires time.Time
}

// Authenticator caches lookups in memory (default 30 s), so a changed or
// disabled app takes up to one TTL to take effect on a given replica.
type Authenticator struct {
	store Store
	ttl   time.Duration
	now   func() time.Time

	mu    sync.Mutex
	cache map[string]entry
}

const maxCacheEntries = 10000

func New(store Store, ttl time.Duration) *Authenticator {
	return &Authenticator{store: store, ttl: ttl, now: time.Now, cache: map[string]entry{}}
}

// Authenticate validates an Authorization header value.
func (a *Authenticator) Authenticate(ctx context.Context, header string) (*App, error) {
	key, ok := strings.CutPrefix(header, "Bearer ")
	key = strings.TrimSpace(key)
	if !ok || key == "" {
		return nil, ErrMissingKey
	}
	if !strings.HasPrefix(key, KeyPrefix) {
		return nil, ErrInvalidKey // can't be ours: don't spend a DB lookup
	}
	id := hex.EncodeToString(HashKey(key))

	a.mu.Lock()
	e, hit := a.cache[id]
	a.mu.Unlock()
	if !hit || !a.now().Before(e.expires) {
		app, err := a.store.LookupByKeyHash(ctx, HashKey(key))
		switch {
		case errors.Is(err, ErrNotFound):
			app = nil // cache the negative result too, so bad keys can't hammer Postgres
		case err != nil:
			return nil, err // database trouble: don't cache, surface it
		}
		e = entry{app: app, expires: a.now().Add(a.ttl)}
		a.mu.Lock()
		if len(a.cache) >= maxCacheEntries {
			clear(a.cache)
		}
		a.cache[id] = e
		a.mu.Unlock()
	}
	if e.app == nil {
		return nil, ErrInvalidKey
	}
	if e.app.Disabled {
		return nil, ErrDisabled
	}
	return e.app, nil
}

func (a *Authenticator) Ping(ctx context.Context) error { return a.store.Ping(ctx) }

// PGStore is the Postgres-backed Store.
type PGStore struct{ Pool *pgxpool.Pool }

func (s PGStore) LookupByKeyHash(ctx context.Context, hash []byte) (*App, error) {
	var (
		app    App
		ttlSec int
		prio   []string
	)
	err := s.Pool.QueryRow(ctx, `
		SELECT id, name, rpm_limit, tokens_per_day, cache_ttl_s, provider_priority, cache_all, disabled
		FROM apps WHERE key_hash = $1`, hash).
		Scan(&app.ID, &app.Name, &app.RPMLimit, &app.TokensPerDay, &ttlSec, &prio, &app.CacheAll, &app.Disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	app.CacheTTL = time.Duration(ttlSec) * time.Second
	app.ProviderPriority = prio
	return &app, nil
}

func (s PGStore) Ping(ctx context.Context) error { return s.Pool.Ping(ctx) }
