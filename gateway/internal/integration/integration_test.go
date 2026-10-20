//go:build integration

// Integration tests: real Postgres and real fl-cache (the C++ server) in
// containers, the gateway in-process, fake upstream providers.
//
//	make gateway-integration
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aarshitaacharya/freeloader/gateway/internal/auth"
	"github.com/aarshitaacharya/freeloader/gateway/internal/cache"
	"github.com/aarshitaacharya/freeloader/gateway/internal/config"
	"github.com/aarshitaacharya/freeloader/gateway/internal/metrics"
	"github.com/aarshitaacharya/freeloader/gateway/internal/provider"
	"github.com/aarshitaacharya/freeloader/gateway/internal/ratelimit"
	"github.com/aarshitaacharya/freeloader/gateway/internal/route"
	"github.com/aarshitaacharya/freeloader/gateway/internal/server"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

type stack struct {
	cacheC   testcontainers.Container
	cacheURL string
	pool     *pgxpool.Pool
	kv       *cache.Redis
	raw      *redis.Client // direct client, for inspecting what the gateway wrote
	upA      *upstream
	upB      *upstream
}

type upstream struct {
	srv   *httptest.Server
	calls atomic.Int32
	mode  atomic.Value // func(http.ResponseWriter)
}

func newUpstream(t *testing.T) *upstream {
	u := &upstream{}
	u.mode.Store(func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, `{"id":"c1","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	})
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		u.calls.Add(1)
		u.mode.Load().(func(http.ResponseWriter))(w)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func setup(t *testing.T) *stack {
	t.Helper()
	ctx := context.Background()
	s := &stack{}

	migration, _ := filepath.Abs("../../../migrations/000001_apps.up.sql")
	pg, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("freeloader"), postgres.WithUsername("freeloader"), postgres.WithPassword("pw"),
		postgres.WithInitScripts(migration), postgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(pg) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if s.pool, err = pgxpool.New(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.pool.Close)

	// A fixed host port, so the address survives stopping and starting the container.
	hostPort := freePort(t)
	s.cacheC, err = testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "fl-cache:dev", // built by `make gateway-integration`
			ExposedPorts: []string{"6400/tcp"},
			HostConfigModifier: func(hc *container.HostConfig) {
				hc.PortBindings = network.PortMap{network.MustParsePort("6400/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: strconv.Itoa(hostPort)}}}
			},
			WaitingFor: wait.ForListeningPort("6400/tcp").WithStartupTimeout(30 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("fl-cache: %v (did you build the image? make gateway-integration does)", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(s.cacheC) })
	s.cacheURL = fmt.Sprintf("127.0.0.1:%d", hostPort)

	s.kv = cache.NewRedis(s.cacheURL, 300*time.Millisecond)
	t.Cleanup(func() { _ = s.kv.Close() })
	s.raw = redis.NewClient(&redis.Options{Addr: s.cacheURL, Protocol: 2, DialTimeout: time.Second})
	t.Cleanup(func() { _ = s.raw.Close() })

	s.upA, s.upB = newUpstream(t), newUpstream(t)
	return s
}

func (s *stack) addApp(t *testing.T, key, name string, rpm int, tokens int64, disabled bool) int64 {
	t.Helper()
	var id int64
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO apps (name, key_hash, rpm_limit, tokens_per_day, cache_ttl_s, disabled)
		VALUES ($1, $2, $3, $4, 3600, $5) RETURNING id`,
		name, auth.HashKey(key), rpm, tokens, disabled).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// replica builds one gateway instance. All replicas share the same Postgres and fl-cache.
func (s *stack) replica(t *testing.T) (*httptest.Server, *metrics.Metrics) {
	provs := map[string]*provider.Provider{
		"a": provider.New("a", s.upA.srv.URL, "ka", 2*time.Second),
		"b": provider.New("b", s.upB.srv.URL, "kb", 2*time.Second),
	}
	aliases := map[string][]config.Target{"fast": {{Provider: "a", Model: "ma"}, {Provider: "b", Model: "mb"}}}
	m := metrics.New()
	srv := server.New(server.Deps{
		Auth: auth.New(auth.PGStore{Pool: s.pool}, 30*time.Second), Limiter: ratelimit.New(s.kv), KV: s.kv,
		Router: route.New(aliases, provs, s.kv, 30*time.Second), Metrics: m, MaxAttempts: 3,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return hs, m
}

type resp struct {
	status int
	h      http.Header
	body   string
}

func post(t *testing.T, base, key, body string) resp {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Body.Close() }()
	b, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, r.Header, string(b)}
}

const det = `{"model":"fast","temperature":0,"messages":[{"role":"user","content":"integration"}]}`
const nondet = `{"model":"fast","messages":[{"role":"user","content":"integration"}]}`

func TestGatewayAgainstRealPostgresAndFlCache(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	gw, m := s.replica(t)

	t.Run("auth against real Postgres", func(t *testing.T) {
		s.addApp(t, "fl_authdemo", "authdemo", 1000, 1_000_000, false)
		s.addApp(t, "fl_authoff", "authoff", 1000, 1_000_000, true)
		if r := post(t, gw.URL, "fl_authdemo", nondet); r.status != 200 {
			t.Fatalf("valid key: %d %s", r.status, r.body)
		}
		if r := post(t, gw.URL, "fl_wrong", nondet); r.status != 401 {
			t.Errorf("unknown key: %d", r.status)
		}
		if r := post(t, gw.URL, "fl_authoff", nondet); r.status != 403 {
			t.Errorf("disabled app: %d", r.status)
		}
	})

	t.Run("response cache lives in fl-cache", func(t *testing.T) {
		s.addApp(t, "fl_cachedemo", "cachedemo", 1000, 1_000_000, false)
		before := s.upA.calls.Load()
		first := post(t, gw.URL, "fl_cachedemo", det)
		second := post(t, gw.URL, "fl_cachedemo", det)
		if first.h.Get("X-Freeloader-Cache") != "MISS" || second.h.Get("X-Freeloader-Cache") != "HIT" {
			t.Fatalf("cache headers: %q then %q", first.h.Get("X-Freeloader-Cache"), second.h.Get("X-Freeloader-Cache"))
		}
		if s.upA.calls.Load()-before != 1 {
			t.Error("the second request reached a provider")
		}
		// Latency is deliberately not asserted here: from the host, fl-cache sits behind Docker
		// Desktop's port forward (~2 ms per round trip). The < 10 ms check runs inside the
		// compose network instead: `make check-hit-latency`.
		var req map[string]json.RawMessage
		_ = json.Unmarshal([]byte(det), &req)
		key, _ := server.CacheKey("fast", req)
		if _, ok, err := s.kv.Get(ctx, key); err != nil || !ok {
			t.Errorf("entry not found in fl-cache under %s (err=%v)", key, err)
		}
		if ttl := s.raw.TTL(ctx, key).Val(); ttl <= 0 || ttl > time.Hour {
			t.Errorf("cache entry TTL = %v, want within (0, 1h]", ttl)
		}
	})

	t.Run("token and rpm counters have the right keys and expiry", func(t *testing.T) {
		id := s.addApp(t, "fl_counters", "counters", 1000, 1_000_000, false)
		post(t, gw.URL, "fl_counters", nondet)
		day := time.Now().UTC().Format("2006-01-02")
		if n, err := cache.GetInt(ctx, s.kv, fmt.Sprintf("tok:%d:%s", id, day)); err != nil || n != 15 {
			t.Errorf("token counter = %d (err %v), want 15", n, err)
		}
		if ttl := s.raw.TTL(ctx, fmt.Sprintf("tok:%d:%s", id, day)).Val(); ttl < 47*time.Hour || ttl > 48*time.Hour {
			t.Errorf("token counter TTL = %v, want ~48h", ttl)
		}
		rl := fmt.Sprintf("rl:%d:%d", id, time.Now().UTC().Unix()/60)
		if n, _ := cache.GetInt(ctx, s.kv, rl); n != 1 {
			t.Errorf("rpm counter = %d, want 1", n)
		}
		if ttl := s.raw.TTL(ctx, rl).Val(); ttl <= 0 || ttl > 60*time.Second {
			t.Errorf("rpm counter TTL = %v, want within (0, 60s]", ttl)
		}
	})

	t.Run("failover when a provider key is broken", func(t *testing.T) {
		s.addApp(t, "fl_failover", "failover", 1000, 1_000_000, false)
		s.upA.mode.Store(func(w http.ResponseWriter) { w.WriteHeader(401); _, _ = io.WriteString(w, `{"error":"bad key"}`) })
		defer s.upA.mode.Store(func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, `{"usage":{"total_tokens":15}}`)
		})
		r := post(t, gw.URL, "fl_failover", nondet)
		if r.status != 200 || r.h.Get("X-Freeloader-Provider") != "b" {
			t.Fatalf("status=%d provider=%q body=%s", r.status, r.h.Get("X-Freeloader-Provider"), r.body)
		}
		if testutil.ToFloat64(m.Failovers.WithLabelValues("a", "b")) < 1 {
			t.Error("failover not counted")
		}
	})

	t.Run("an exhausted flag in fl-cache routes every replica around a provider", func(t *testing.T) {
		s.addApp(t, "fl_flag", "flagapp", 1000, 1_000_000, false)
		gw2, _ := s.replica(t)
		if err := s.kv.Set(ctx, "exhausted:b", []byte("1"), time.Minute); err != nil {
			t.Fatal(err)
		}
		defer s.raw.Del(ctx, "exhausted:b")
		s.upA.mode.Store(func(w http.ResponseWriter) { w.WriteHeader(500) })
		defer s.upA.mode.Store(func(w http.ResponseWriter) { _, _ = io.WriteString(w, `{"usage":{"total_tokens":15}}`) })
		bBefore := s.upB.calls.Load()
		for _, base := range []string{gw.URL, gw2.URL} {
			if r := post(t, base, "fl_flag", nondet); r.status != 502 {
				t.Errorf("a is down and b is flagged exhausted: want 502, got %d", r.status)
			}
		}
		if s.upB.calls.Load() != bBefore {
			t.Error("a flagged provider was called")
		}
	})

	t.Run("three replicas share one rate-limit counter", func(t *testing.T) {
		s.addApp(t, "fl_shared", "shared", 6, 1_000_000, false)
		var urls []string
		for i := 0; i < 3; i++ {
			g, _ := s.replica(t)
			urls = append(urls, g.URL)
		}
		var ok, limited int
		for i := 0; i < 9; i++ { // round-robin over replicas, as a Kubernetes Service would
			switch post(t, urls[i%3], "fl_shared", nondet).status {
			case 200:
				ok++
			case 429:
				limited++
			}
		}
		if ok != 6 || limited != 3 {
			t.Errorf("ok=%d limited=%d, want 6 and 3: the limit must hold across replicas", ok, limited)
		}
	})

	t.Run("rate limit response shape", func(t *testing.T) {
		s.addApp(t, "fl_tiny", "tiny", 1, 1_000_000, false)
		post(t, gw.URL, "fl_tiny", nondet)
		r := post(t, gw.URL, "fl_tiny", nondet)
		if r.status != 429 {
			t.Fatalf("status %d", r.status)
		}
		if n, err := strconv.Atoi(r.h.Get("Retry-After")); err != nil || n < 1 || n > 60 {
			t.Errorf("Retry-After = %q", r.h.Get("Retry-After"))
		}
	})

	// Runs last: it stops and restarts fl-cache.
	t.Run("fl-cache outage and restart", func(t *testing.T) {
		id := s.addApp(t, "fl_outage", "outage", 1000, 1_000_000, false)
		post(t, gw.URL, "fl_outage", det) // populate cache
		if r := post(t, gw.URL, "fl_outage", det); r.h.Get("X-Freeloader-Cache") != "HIT" {
			t.Fatal("precondition: expected a HIT before the outage")
		}

		timeout := 10 * time.Second
		if err := s.cacheC.Stop(ctx, &timeout); err != nil {
			t.Fatal(err)
		}
		callsBefore := s.upA.calls.Load()
		for i := 0; i < 3; i++ {
			r := post(t, gw.URL, "fl_outage", det)
			if r.status != 200 {
				t.Fatalf("while fl-cache is down the gateway must keep serving; got %d %s", r.status, r.body)
			}
		}
		if got := s.upA.calls.Load() - callsBefore; got != 3 {
			t.Errorf("provider calls during outage = %d, want 3 (no cache)", got)
		}
		if testutil.ToFloat64(m.CacheErrors) == 0 {
			t.Error("outage not visible in fl_gateway_cache_errors_total")
		}

		if err := s.cacheC.Start(ctx); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(15 * time.Second)
		for s.kv.Ping(ctx) != nil {
			if time.Now().After(deadline) {
				t.Fatal("fl-cache did not come back")
			}
			time.Sleep(100 * time.Millisecond)
		}

		// State is gone (cache entries and counters); the gateway recovers on its own.
		first := post(t, gw.URL, "fl_outage", det)
		second := post(t, gw.URL, "fl_outage", det)
		if first.h.Get("X-Freeloader-Cache") != "MISS" || second.h.Get("X-Freeloader-Cache") != "HIT" {
			t.Errorf("after restart want MISS then HIT, got %q then %q",
				first.h.Get("X-Freeloader-Cache"), second.h.Get("X-Freeloader-Cache"))
		}
		day := time.Now().UTC().Format("2006-01-02")
		if n, _ := cache.GetInt(ctx, s.kv, fmt.Sprintf("tok:%d:%s", id, day)); n > 15 {
			t.Errorf("counters should have reset with the cache; token counter = %d", n)
		}
	})
}
