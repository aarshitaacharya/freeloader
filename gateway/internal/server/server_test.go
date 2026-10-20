package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type fakeStore struct {
	mu   sync.Mutex
	apps map[string]*auth.App
}

func (f *fakeStore) LookupByKeyHash(_ context.Context, hash []byte) (*auth.App, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, a := range f.apps {
		if string(auth.HashKey(k)) == string(hash) {
			cp := *a
			return &cp, nil
		}
	}
	return nil, auth.ErrNotFound
}
func (f *fakeStore) Ping(context.Context) error { return nil }

// upstream is a fake provider. Behaviour can be swapped per test.
type upstream struct {
	srv     *httptest.Server
	calls   atomic.Int32
	mu      sync.Mutex
	handler func(w http.ResponseWriter, r *http.Request)
}

func okBody(tokens int) string {
	return `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":` + itoa(tokens) + `}}`
}
func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func replyOK(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, okBody(15)) }
func replyStatus(code int) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = io.WriteString(w, `{"error":{"message":"upstream says no"}}`)
	}
}

func newUpstream(t *testing.T, h func(http.ResponseWriter, *http.Request)) *upstream {
	u := &upstream{handler: h}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.calls.Add(1)
		u.mu.Lock()
		h := u.handler
		u.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) set(h func(http.ResponseWriter, *http.Request)) {
	u.mu.Lock()
	u.handler = h
	u.mu.Unlock()
}

type harness struct {
	t      *testing.T
	srv    *httptest.Server
	kv     *cache.Mem
	m      *metrics.Metrics
	router *route.Router
	ups    map[string]*upstream
	store  *fakeStore
}

const testKey = "fl_testkey"

func newHarness(t *testing.T, app auth.App, maxAttempts int) *harness {
	t.Helper()
	h := &harness{t: t, kv: cache.NewMem(), m: metrics.New(), ups: map[string]*upstream{}}
	provs := map[string]*provider.Provider{}
	for _, name := range []string{"a", "b", "c"} {
		h.ups[name] = newUpstream(t, replyOK)
		provs[name] = provider.New(name, h.ups[name].srv.URL, "key-"+name, 2*time.Second)
	}
	aliases := map[string][]config.Target{
		"fast": {{Provider: "a", Model: "model-a"}, {Provider: "b", Model: "model-b"}, {Provider: "c", Model: "model-c"}},
	}
	if app.ID == 0 {
		app.ID = 1
	}
	if app.Name == "" {
		app.Name = "demo"
	}
	h.store = &fakeStore{apps: map[string]*auth.App{testKey: &app}}
	h.router = route.New(aliases, provs, h.kv, 30*time.Second)
	s := New(Deps{
		Auth: auth.New(h.store, 30*time.Second), Limiter: ratelimit.New(h.kv), KV: h.kv,
		Router: h.router, Metrics: h.m, MaxAttempts: maxAttempts,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	h.srv = httptest.NewServer(s.Handler())
	t.Cleanup(h.srv.Close)
	return h
}

func defaultApp() auth.App {
	return auth.App{RPMLimit: 1000, TokensPerDay: 1_000_000, CacheTTL: time.Hour}
}

type result struct {
	status int
	header http.Header
	body   string
}

func (h *harness) do(method, path, body string, hdr map[string]string) result {
	h.t.Helper()
	req, _ := http.NewRequest(method, h.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return result{resp.StatusCode, resp.Header, string(b)}
}

func (h *harness) chat(body string) result {
	return h.do("POST", "/v1/chat/completions", body, map[string]string{"Authorization": "Bearer " + testKey})
}

const det = `{"model":"fast","temperature":0,"messages":[{"role":"user","content":"hi"}]}`
const nondet = `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`

func (h *harness) calls() (a, b, c int) {
	return int(h.ups["a"].calls.Load()), int(h.ups["b"].calls.Load()), int(h.ups["c"].calls.Load())
}

func wantStatus(t *testing.T, r result, want int) {
	t.Helper()
	if r.status != want {
		t.Fatalf("status = %d, want %d; body: %s", r.status, want, r.body)
	}
}

func errCode(t *testing.T, r result) string {
	t.Helper()
	var e struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.body), &e); err != nil {
		t.Fatalf("not an OpenAI-style error body: %q", r.body)
	}
	return e.Error.Code
}

// ---- Level 4 checks -------------------------------------------------------

func TestSecondIdenticalRequestIsServedFromCache(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)

	first := h.chat(det)
	wantStatus(t, first, 200)
	if first.header.Get("X-Freeloader-Cache") != "MISS" || first.header.Get("X-Freeloader-Provider") != "a" {
		t.Fatalf("first: cache=%q provider=%q", first.header.Get("X-Freeloader-Cache"), first.header.Get("X-Freeloader-Provider"))
	}

	start := time.Now()
	second := h.chat(det)
	elapsed := time.Since(start)
	wantStatus(t, second, 200)
	if second.header.Get("X-Freeloader-Cache") != "HIT" {
		t.Fatalf("second: X-Freeloader-Cache = %q, want HIT", second.header.Get("X-Freeloader-Cache"))
	}
	if second.header.Get("X-Freeloader-Provider") != "a" {
		t.Error("a cache hit should still report the provider that produced the answer")
	}
	if second.body != first.body {
		t.Error("cached body differs from the original")
	}
	if a, _, _ := h.calls(); a != 1 {
		t.Errorf("upstream calls = %d, want 1 (second request must not touch a provider)", a)
	}
	if elapsed > 10*time.Millisecond {
		t.Errorf("cache hit took %v, want < 10ms", elapsed)
	}
	if got := testutil.ToFloat64(h.m.CacheHits); got != 1 {
		t.Errorf("cache hit metric = %v", got)
	}
}

func TestFailoverWhenProviderKeyIsBroken(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	h.ups["a"].set(replyStatus(401)) // revoked / wrong key

	r := h.chat(nondet)
	wantStatus(t, r, 200)
	if r.header.Get("X-Freeloader-Provider") != "b" || r.header.Get("X-Freeloader-Attempts") != "2" {
		t.Fatalf("provider=%q attempts=%q", r.header.Get("X-Freeloader-Provider"), r.header.Get("X-Freeloader-Attempts"))
	}
	if got := testutil.ToFloat64(h.m.Failovers.WithLabelValues("a", "b")); got != 1 {
		t.Errorf("failover metric a->b = %v", got)
	}
	if got := testutil.ToFloat64(h.m.UpstreamErrors.WithLabelValues("a", "401")); got != 1 {
		t.Errorf("upstream error metric = %v", got)
	}
}

func TestRPMLimitReturns429WithRetryAfter(t *testing.T) {
	app := defaultApp()
	app.RPMLimit = 2
	h := newHarness(t, app, 3)

	wantStatus(t, h.chat(nondet), 200)
	wantStatus(t, h.chat(nondet), 200)
	r := h.chat(nondet)
	wantStatus(t, r, 429)
	if code := errCode(t, r); code != "rate_limit_exceeded" {
		t.Errorf("code = %q", code)
	}
	ra := r.header.Get("Retry-After")
	if ra == "" {
		t.Fatal("missing Retry-After")
	}
	if n := atoiOrFail(t, ra); n < 1 || n > 60 {
		t.Errorf("Retry-After = %d, want 1..60", n)
	}
	if got := testutil.ToFloat64(h.m.RateLimited.WithLabelValues("demo")); got != 1 {
		t.Errorf("ratelimited metric = %v", got)
	}
	if a, _, _ := h.calls(); a != 2 {
		t.Errorf("a rejected request must not reach a provider; calls = %d", a)
	}
}

func atoiOrFail(t *testing.T, s string) int {
	t.Helper()
	var n int
	if err := json.Unmarshal([]byte(s), &n); err != nil {
		t.Fatalf("Retry-After %q is not an integer", s)
	}
	return n
}

// ---- Caching rules ---------------------------------------------------------

func TestCachingRules(t *testing.T) {
	t.Run("absent temperature is not cached", func(t *testing.T) {
		h := newHarness(t, defaultApp(), 3)
		r1, r2 := h.chat(nondet), h.chat(nondet)
		if r1.header.Get("X-Freeloader-Cache") != "BYPASS" || r2.header.Get("X-Freeloader-Cache") != "BYPASS" {
			t.Error("expected BYPASS")
		}
		if a, _, _ := h.calls(); a != 2 {
			t.Errorf("calls = %d, want 2", a)
		}
	})
	t.Run("nonzero temperature is not cached", func(t *testing.T) {
		h := newHarness(t, defaultApp(), 3)
		body := `{"model":"fast","temperature":0.7,"messages":[{"role":"user","content":"hi"}]}`
		h.chat(body)
		h.chat(body)
		if a, _, _ := h.calls(); a != 2 {
			t.Errorf("calls = %d, want 2", a)
		}
	})
	t.Run("cache_all opts in", func(t *testing.T) {
		app := defaultApp()
		app.CacheAll = true
		h := newHarness(t, app, 3)
		h.chat(nondet)
		r := h.chat(nondet)
		if r.header.Get("X-Freeloader-Cache") != "HIT" {
			t.Errorf("cache = %q, want HIT", r.header.Get("X-Freeloader-Cache"))
		}
	})
	t.Run("ttl 0 disables caching", func(t *testing.T) {
		app := defaultApp()
		app.CacheTTL = 0
		h := newHarness(t, app, 3)
		h.chat(det)
		h.chat(det)
		if a, _, _ := h.calls(); a != 2 {
			t.Errorf("calls = %d, want 2", a)
		}
	})
	t.Run("different request misses", func(t *testing.T) {
		h := newHarness(t, defaultApp(), 3)
		h.chat(det)
		r := h.chat(strings.Replace(det, "hi", "bye", 1))
		if r.header.Get("X-Freeloader-Cache") != "MISS" {
			t.Errorf("cache = %q", r.header.Get("X-Freeloader-Cache"))
		}
	})
	t.Run("failed upstream results are never cached", func(t *testing.T) {
		h := newHarness(t, defaultApp(), 1)
		h.ups["a"].set(replyStatus(500))
		wantStatus(t, h.chat(det), 502)
		h.ups["a"].set(replyOK)
		r := h.chat(det)
		wantStatus(t, r, 200)
		if r.header.Get("X-Freeloader-Cache") != "MISS" {
			t.Error("the earlier failure must not have been cached")
		}
	})
}

// ---- Failover behaviour ----------------------------------------------------

func TestFailoverOn429StartsCooldown(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	h.ups["a"].set(replyStatus(429))

	r := h.chat(nondet)
	wantStatus(t, r, 200)
	if r.header.Get("X-Freeloader-Provider") != "b" {
		t.Fatalf("provider = %q", r.header.Get("X-Freeloader-Provider"))
	}
	// a is now cooling down: the next request must not even try it.
	h.chat(nondet)
	if a, b, _ := h.calls(); a != 1 || b != 2 {
		t.Errorf("calls a=%d b=%d, want a=1 (cooldown) b=2", a, b)
	}
}

func TestFailoverOn5xxAndTimeout(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	h.ups["a"].set(replyStatus(503))
	h.ups["b"].set(func(w http.ResponseWriter, r *http.Request) { // hangs past the attempt timeout
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	})
	// shorten b's attempt timeout
	h.router.Provider("b").Timeout = 100 * time.Millisecond

	r := h.chat(nondet)
	wantStatus(t, r, 200)
	if r.header.Get("X-Freeloader-Provider") != "c" || r.header.Get("X-Freeloader-Attempts") != "3" {
		t.Errorf("provider=%q attempts=%q", r.header.Get("X-Freeloader-Provider"), r.header.Get("X-Freeloader-Attempts"))
	}
}

func TestClientErrorsPassThroughWithoutFailover(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	h.ups["a"].set(replyStatus(400))
	r := h.chat(nondet)
	wantStatus(t, r, 400)
	if !strings.Contains(r.body, "upstream says no") {
		t.Errorf("upstream error body not relayed: %s", r.body)
	}
	if _, b, _ := h.calls(); b != 0 {
		t.Error("a 400 is the client's fault; no other provider should be tried")
	}
}

func TestAllProvidersFail(t *testing.T) {
	t.Run("all 5xx gives 502", func(t *testing.T) {
		h := newHarness(t, defaultApp(), 3)
		for _, u := range h.ups {
			u.set(replyStatus(500))
		}
		r := h.chat(nondet)
		wantStatus(t, r, 502)
		if errCode(t, r) != "upstream_error" {
			t.Errorf("code = %s", errCode(t, r))
		}
	})
	t.Run("all 429 gives 429 with Retry-After", func(t *testing.T) {
		h := newHarness(t, defaultApp(), 3)
		for _, u := range h.ups {
			u.set(replyStatus(429))
		}
		r := h.chat(nondet)
		wantStatus(t, r, 429)
		if errCode(t, r) != "providers_rate_limited" || r.header.Get("Retry-After") == "" {
			t.Errorf("code=%s retry-after=%q", errCode(t, r), r.header.Get("Retry-After"))
		}
		// Everything is cooling down now: refuse quickly without calling anyone.
		before, _, _ := h.calls()
		r2 := h.chat(nondet)
		wantStatus(t, r2, 503)
		if after, _, _ := h.calls(); after != before {
			t.Error("cooling providers were called")
		}
	})
}

func TestMaxAttemptsIsRespected(t *testing.T) {
	h := newHarness(t, defaultApp(), 2)
	h.ups["a"].set(replyStatus(500))
	h.ups["b"].set(replyStatus(500))
	wantStatus(t, h.chat(nondet), 502)
	if _, _, c := h.calls(); c != 0 {
		t.Errorf("c was called %d times; max_attempts=2 should have stopped before it", c)
	}
}

func TestExhaustedFlagSkipsProvider(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	_ = h.kv.Set(context.Background(), "exhausted:a", []byte("1"), time.Hour)
	r := h.chat(nondet)
	wantStatus(t, r, 200)
	if r.header.Get("X-Freeloader-Provider") != "b" {
		t.Errorf("provider = %q", r.header.Get("X-Freeloader-Provider"))
	}
	if a, _, _ := h.calls(); a != 0 {
		t.Error("an exhausted provider must not be called (and a skip is not an attempt)")
	}
	if r.header.Get("X-Freeloader-Attempts") != "1" {
		t.Errorf("attempts = %q, want 1", r.header.Get("X-Freeloader-Attempts"))
	}
}

func TestAppProviderPriority(t *testing.T) {
	app := defaultApp()
	app.ProviderPriority = []string{"c", "b"}
	h := newHarness(t, app, 3)
	r := h.chat(nondet)
	if r.header.Get("X-Freeloader-Provider") != "c" {
		t.Errorf("provider = %q, want c", r.header.Get("X-Freeloader-Provider"))
	}
}

func TestDirectProviderModel(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	var gotModel string
	h.ups["b"].set(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		gotModel, _ = m["model"].(string)
		replyOK(w, r)
	})
	r := h.chat(`{"model":"b/vendor/some-model:free","messages":[{"role":"user","content":"x"}]}`)
	wantStatus(t, r, 200)
	if gotModel != "vendor/some-model:free" || r.header.Get("X-Freeloader-Provider") != "b" {
		t.Errorf("model sent upstream = %q, provider = %q", gotModel, r.header.Get("X-Freeloader-Provider"))
	}
	if a, _, _ := h.calls(); a != 0 {
		t.Error("explicit provider requests must not touch other providers")
	}
}

func TestAliasModelIsRewrittenPerProvider(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	var got string
	h.ups["a"].set(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		got, _ = m["model"].(string)
		replyOK(w, r)
	})
	h.chat(nondet)
	if got != "model-a" {
		t.Errorf("upstream model = %q, want model-a", got)
	}
}

// ---- Token budget -----------------------------------------------------------

func TestTokenBudget(t *testing.T) {
	app := defaultApp()
	app.TokensPerDay = 20 // each fake response uses 15 tokens
	h := newHarness(t, app, 3)
	wantStatus(t, h.chat(nondet), 200) // 15 used
	wantStatus(t, h.chat(nondet), 200) // 30 used: the request that crosses the line is served
	r := h.chat(nondet)
	wantStatus(t, r, 429)
	if !strings.Contains(r.body, "tokens_per_day") || r.header.Get("Retry-After") == "" {
		t.Errorf("body=%s retry-after=%q", r.body, r.header.Get("Retry-After"))
	}
}

func TestCachedResponsesDoNotConsumeTokens(t *testing.T) {
	app := defaultApp()
	app.TokensPerDay = 20
	h := newHarness(t, app, 3)
	for i := 0; i < 10; i++ {
		wantStatus(t, h.chat(det), 200) // one real call (15 tokens), nine cache hits
	}
}

// ---- Validation & auth ------------------------------------------------------

func TestRequestValidation(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	tests := []struct {
		name, body string
		status     int
		code       string
	}{
		{"invalid json", `{nope`, 400, "invalid_json"},
		{"not an object", `[1]`, 400, "invalid_json"},
		{"missing model", `{"messages":[{"role":"user","content":"x"}]}`, 400, "missing_model"},
		{"empty model", `{"model":"","messages":[{"role":"user","content":"x"}]}`, 400, "missing_model"},
		{"missing messages", `{"model":"fast"}`, 400, "missing_messages"},
		{"unknown model", `{"model":"nope","messages":[{"role":"user","content":"x"}]}`, 404, "model_not_found"},
		{"streaming not yet supported", `{"model":"fast","stream":true,"messages":[{"role":"user","content":"x"}]}`, 501, "streaming_unsupported"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := h.chat(tc.body)
			wantStatus(t, r, tc.status)
			if errCode(t, r) != tc.code {
				t.Errorf("code = %q, want %q", errCode(t, r), tc.code)
			}
		})
	}
}

func TestAuthErrors(t *testing.T) {
	app := defaultApp()
	h := newHarness(t, app, 3)
	h.store.apps["fl_off"] = &auth.App{ID: 2, Name: "off", Disabled: true}
	tests := []struct {
		name, header string
		status       int
		code         string
	}{
		{"no header", "", 401, "invalid_api_key"},
		{"wrong key", "Bearer fl_wrong", 401, "invalid_api_key"},
		{"foreign key", "Bearer sk-123", 401, "invalid_api_key"},
		{"disabled app", "Bearer fl_off", 403, "app_disabled"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hdr := map[string]string{}
			if tc.header != "" {
				hdr["Authorization"] = tc.header
			}
			r := h.do("POST", "/v1/chat/completions", nondet, hdr)
			wantStatus(t, r, tc.status)
			if errCode(t, r) != tc.code {
				t.Errorf("code = %q", errCode(t, r))
			}
		})
	}
	if a, b, c := h.calls(); a+b+c != 0 {
		t.Error("unauthenticated requests must never reach a provider")
	}
}

// ---- Resilience ------------------------------------------------------------

func TestServesNormallyWhenCacheIsDown(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	h.kv.Err = errors.New("fl-cache unreachable")
	for i := 0; i < 3; i++ {
		r := h.chat(det)
		wantStatus(t, r, 200)
		if r.header.Get("X-Freeloader-Cache") == "HIT" {
			t.Fatal("cannot hit a cache that is down")
		}
	}
	if a, _, _ := h.calls(); a != 3 {
		t.Errorf("calls = %d, want 3 (no cache, no limits, still serving)", a)
	}
	if testutil.ToFloat64(h.m.CacheErrors) == 0 {
		t.Error("cache errors should be counted so the outage is visible")
	}
}

func TestRecoversWhenCacheComesBack(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	h.kv.Err = errors.New("down")
	h.chat(det)
	h.kv.Err = nil
	h.chat(det) // miss, populates
	r := h.chat(det)
	if r.header.Get("X-Freeloader-Cache") != "HIT" {
		t.Error("cache should work again after recovery")
	}
}

func TestConcurrentRequests(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	h.ups["a"].set(replyStatus(429))
	var wg sync.WaitGroup
	var failures atomic.Int32
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if h.chat(nondet).status != 200 {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()
	if failures.Load() != 0 {
		t.Errorf("%d concurrent requests failed", failures.Load())
	}
}

// ---- Other endpoints --------------------------------------------------------

func TestModelsEndpoint(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	r := h.do("GET", "/v1/models", "", map[string]string{"Authorization": "Bearer " + testKey})
	wantStatus(t, r, 200)
	var out struct {
		Object string
		Data   []struct{ ID string }
	}
	if err := json.Unmarshal([]byte(r.body), &out); err != nil || out.Object != "list" || len(out.Data) != 1 || out.Data[0].ID != "fast" {
		t.Errorf("unexpected body: %s", r.body)
	}
	wantStatus(t, h.do("GET", "/v1/models", "", nil), 401)
}

func TestProbesAndMetrics(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	wantStatus(t, h.do("GET", "/healthz", "", nil), 200)
	wantStatus(t, h.do("GET", "/readyz", "", nil), 200)
	h.kv.Err = errors.New("down")
	r := h.do("GET", "/readyz", "", nil)
	wantStatus(t, r, 503)
	if !strings.Contains(r.body, "fl-cache") {
		t.Errorf("readyz should name the failing dependency: %q", r.body)
	}
	wantStatus(t, h.do("GET", "/healthz", "", nil), 200) // liveness must not depend on the cache

	h.kv.Err = nil
	h.chat(nondet)
	m := h.do("GET", "/metrics", "", nil)
	wantStatus(t, m, 200)
	for _, name := range []string{
		`fl_gateway_requests_total{alias="fast",app="demo",status="200"} 1`,
		"fl_gateway_request_seconds_bucket", "fl_gateway_cache_hits_total", "fl_gateway_failovers_total",
		"fl_gateway_ratelimited_total", "fl_gateway_upstream_errors_total", "fl_gateway_events_dropped_total",
	} {
		if !strings.Contains(m.body, name) {
			// vec metrics with no children yet don't print samples, only HELP/TYPE
			if strings.Contains(name, "{") || strings.Contains(name, "_bucket") {
				t.Errorf("metrics output missing %q", name)
			}
		}
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	wantStatus(t, h.do("GET", "/v1/chat/completions", "", nil), 405)
}

func TestCooldownAfterPersistentFailures(t *testing.T) {
	tests := []struct {
		status       int
		wantCooldown bool
	}{
		{429, true}, {401, true}, {402, true}, {403, true},
		{500, false}, {503, false}, {404, false},
	}
	for _, tc := range tests {
		t.Run(itoa(tc.status), func(t *testing.T) {
			h := newHarness(t, defaultApp(), 3)
			h.ups["a"].set(replyStatus(tc.status))
			wantStatus(t, h.chat(nondet), 200) // fails over to b
			wantStatus(t, h.chat(nondet), 200)
			a, _, _ := h.calls()
			if tc.wantCooldown && a != 1 {
				t.Errorf("a was called %d times; after a %d it should be skipped for the cooldown", a, tc.status)
			}
			if !tc.wantCooldown && a != 2 {
				t.Errorf("a was called %d times; a %d should not start a cooldown", a, tc.status)
			}
		})
	}
}

func TestHungProviderCostsOneTimeoutNotOnePerRequest(t *testing.T) {
	h := newHarness(t, defaultApp(), 3)
	h.ups["a"].set(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	})
	h.router.Provider("a").Timeout = 150 * time.Millisecond

	start := time.Now()
	wantStatus(t, h.chat(nondet), 200) // pays the timeout once
	first := time.Since(start)
	start = time.Now()
	wantStatus(t, h.chat(nondet), 200)
	second := time.Since(start)
	if first < 150*time.Millisecond {
		t.Errorf("first request took %v; expected it to wait out a's timeout", first)
	}
	if second > 100*time.Millisecond {
		t.Errorf("second request took %v; a is cooling down and should be skipped", second)
	}
	if a, _, _ := h.calls(); a != 1 {
		t.Errorf("a was called %d times, want 1", a)
	}
}
