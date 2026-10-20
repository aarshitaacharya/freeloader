package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aarshitaacharya/freeloader/gateway/internal/auth"
	"github.com/aarshitaacharya/freeloader/gateway/internal/cache"
	"github.com/aarshitaacharya/freeloader/gateway/internal/config"
	"github.com/aarshitaacharya/freeloader/gateway/internal/provider"
	"github.com/aarshitaacharya/freeloader/gateway/internal/route"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

const token = "s3cret-admin-token"

// fakeStore is an in-memory Store.
type fakeStore struct {
	apps   []App
	hashes map[string][]byte
	usage  []UsageRow
}

func (f *fakeStore) ListApps(context.Context) ([]App, error) { return append([]App{}, f.apps...), nil }
func (f *fakeStore) CreateApp(_ context.Context, in NewApp, hash []byte) (App, error) {
	for _, a := range f.apps {
		if a.Name == in.Name {
			return App{}, ErrNameTaken
		}
	}
	a := App{ID: int64(len(f.apps) + 1), Name: in.Name, RPMLimit: *in.RPMLimit, TokensPerDay: *in.TokensPerDay,
		CacheTTLSeconds: *in.CacheTTLSeconds, ProviderPriority: in.ProviderPriority, CacheAll: *in.CacheAll,
		CreatedAt: time.Date(2026, 10, 20, 1, 2, 3, 0, time.UTC)}
	f.apps = append(f.apps, a)
	if f.hashes == nil {
		f.hashes = map[string][]byte{}
	}
	f.hashes[a.Name] = hash
	return a, nil
}
func (f *fakeStore) UpdateApp(_ context.Context, id int64, p AppPatch) (App, error) {
	for i := range f.apps {
		if f.apps[i].ID != id {
			continue
		}
		a := &f.apps[i]
		if p.RPMLimit != nil {
			a.RPMLimit = *p.RPMLimit
		}
		if p.TokensPerDay != nil {
			a.TokensPerDay = *p.TokensPerDay
		}
		if p.CacheTTLSeconds != nil {
			a.CacheTTLSeconds = *p.CacheTTLSeconds
		}
		if p.ProviderPriority != nil {
			a.ProviderPriority = *p.ProviderPriority
		}
		if p.CacheAll != nil {
			a.CacheAll = *p.CacheAll
		}
		if p.Disabled != nil {
			a.Disabled = *p.Disabled
		}
		return *a, nil
	}
	return App{}, ErrNotFound
}
func (f *fakeStore) Usage(_ context.Context, from, to time.Time, appID *int64) ([]UsageRow, error) {
	var out []UsageRow
	for _, r := range f.usage {
		d, _ := time.Parse(time.DateOnly, r.Day)
		if d.Before(from) || d.After(to) || (appID != nil && r.AppID != *appID) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

type env struct {
	h     *Handler
	store *fakeStore
	kv    *cache.Mem
	rt    *route.Router
	spec  *openapi3filter.Options
	t     *testing.T
	oa    func(*http.Request) (*openapi3filter.RequestValidationInput, error)
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ny := time.FixedZone("test", -8*3600)
	cfg := map[string]config.Provider{
		"groq": {APIKey: "k", Loc: time.UTC, Models: map[string]config.Limits{
			"m1": {RequestsPerDay: 100, TokensPerDay: 5000}, "unguarded": {RequestsPerMinute: 5}}},
		"gemini":     {APIKey: "k", Loc: ny, Limits: config.Limits{RequestsPerDay: 50}},
		"openrouter": {APIKey: ""},
	}
	provs := map[string]*provider.Provider{}
	for n, c := range cfg {
		provs[n] = provider.New(n, "http://127.0.0.1:1", c.APIKey, time.Second)
	}
	kv := cache.NewMem()
	rt := route.New(map[string][]config.Target{"fast": {{Provider: "groq", Model: "m1"}}}, provs, kv, 30*time.Second)
	store := &fakeStore{}
	h, err := New(token, store, &Providers{Cfg: cfg, Router: rt, KV: kv}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.now = func() time.Time { return time.Date(2026, 10, 20, 5, 0, 0, 0, time.UTC) }

	doc, err := openapi3.NewLoader().LoadFromFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("openapi.yaml is not valid: %v", err)
	}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	return &env{h: h, store: store, kv: kv, rt: rt, t: t, spec: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
		oa: func(r *http.Request) (*openapi3filter.RequestValidationInput, error) {
			route, params, err := router.FindRoute(r)
			if err != nil {
				return nil, err
			}
			return &openapi3filter.RequestValidationInput{Request: r, PathParams: params, Route: route, Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}}, nil
		}}
}

// do sends a request with the admin token and checks the response against api/openapi.yaml.
func (e *env) do(method, path, body string) (int, []byte) {
	e.t.Helper()
	return e.doAuth(method, path, body, "Bearer "+token)
}

func (e *env) doAuth(method, path, body, authz string) (int, []byte) {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)

	in, err := e.oa(req)
	if err != nil {
		e.t.Fatalf("%s %s is not in the OpenAPI spec: %v", method, path, err)
	}
	out := &openapi3filter.ResponseValidationInput{RequestValidationInput: in, Status: rec.Code,
		Header: rec.Header(), Options: &openapi3filter.Options{IncludeResponseStatus: true}}
	out.SetBodyBytes(rec.Body.Bytes())
	if err := openapi3filter.ValidateResponse(context.Background(), out); err != nil {
		e.t.Errorf("%s %s -> %d violates the OpenAPI spec: %v\nbody: %s", method, path, rec.Code, err, rec.Body.String())
	}
	return rec.Code, rec.Body.Bytes()
}

func TestAuth(t *testing.T) {
	e := newEnv(t)
	for name, authz := range map[string]string{"none": "", "wrong": "Bearer nope", "no scheme": token, "basic": "Basic " + token} {
		if code, _ := e.doAuth("GET", "/admin/apps", "", authz); code != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", name, code)
		}
	}
	if code, _ := e.do("GET", "/admin/apps", ""); code != http.StatusOK {
		t.Errorf("right token: got %d", code)
	}
}

func TestNewRejectsEmptyToken(t *testing.T) {
	if _, err := New("", &fakeStore{}, &Providers{}, nil); err == nil {
		t.Fatal("empty token must be refused")
	}
}

func TestCreateApp(t *testing.T) {
	e := newEnv(t)
	code, body := e.do("POST", "/admin/apps", `{"name":"  demo  "}`)
	if code != http.StatusCreated {
		t.Fatalf("got %d: %s", code, body)
	}
	var got struct {
		App App    `json:"app"`
		Key string `json:"key"`
	}
	_ = json.Unmarshal(body, &got)
	if got.App.Name != "demo" || got.App.RPMLimit != 60 || got.App.TokensPerDay != 100000 || got.App.CacheTTLSeconds != 86400 {
		t.Errorf("defaults not applied: %+v", got.App)
	}
	if !strings.HasPrefix(got.Key, auth.KeyPrefix) {
		t.Errorf("key %q lacks prefix", got.Key)
	}
	// The stored hash must be the one the gateway's auth looks up.
	if !bytes.Equal(e.store.hashes["demo"], auth.HashKey(got.Key)) {
		t.Error("stored hash does not match the returned key")
	}
	// ... and the key itself is never listed again.
	_, list := e.do("GET", "/admin/apps", "")
	if strings.Contains(string(list), got.Key) {
		t.Error("key leaked into the app list")
	}
}

func TestCreateAppExplicitZeroMeansUnlimited(t *testing.T) {
	e := newEnv(t)
	code, body := e.do("POST", "/admin/apps", `{"name":"u","rpm_limit":0,"tokens_per_day":0,"cache_ttl_s":0}`)
	if code != http.StatusCreated {
		t.Fatalf("%d %s", code, body)
	}
	a := e.store.apps[0]
	if a.RPMLimit != 0 || a.TokensPerDay != 0 || a.CacheTTLSeconds != 0 {
		t.Errorf("explicit zeros were replaced by defaults: %+v", a)
	}
}

func TestCreateAppValidation(t *testing.T) {
	cases := map[string]string{
		"empty name":       `{"name":""}`,
		"blank name":       `{"name":"   "}`,
		"long name":        `{"name":"` + strings.Repeat("x", 65) + `"}`,
		"negative rpm":     `{"name":"a","rpm_limit":-1}`,
		"negative tokens":  `{"name":"a","tokens_per_day":-5}`,
		"negative ttl":     `{"name":"a","cache_ttl_s":-1}`,
		"unknown provider": `{"name":"a","provider_priority":["nope"]}`,
		"duplicate prio":   `{"name":"a","provider_priority":["groq","groq"]}`,
		"unknown field":    `{"name":"a","key_hash":"x"}`,
		"not json":         `{`,
		"wrong type":       `{"name":"a","rpm_limit":"many"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			if code, resp := e.do("POST", "/admin/apps", body); code != http.StatusBadRequest {
				t.Errorf("got %d, want 400: %s", code, resp)
			}
			if len(e.store.apps) != 0 {
				t.Error("an app was created")
			}
		})
	}
}

func TestCreateAppNameTaken(t *testing.T) {
	e := newEnv(t)
	e.do("POST", "/admin/apps", `{"name":"a"}`)
	if code, _ := e.do("POST", "/admin/apps", `{"name":"a"}`); code != http.StatusConflict {
		t.Errorf("got %d, want 409", code)
	}
}

func TestUpdateApp(t *testing.T) {
	e := newEnv(t)
	e.do("POST", "/admin/apps", `{"name":"a","rpm_limit":10,"tokens_per_day":500,"provider_priority":["groq"]}`)

	code, body := e.do("PATCH", "/admin/apps/1", `{"disabled":true,"rpm_limit":0}`)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	a := e.store.apps[0]
	if !a.Disabled || a.RPMLimit != 0 {
		t.Errorf("patch not applied: %+v", a)
	}
	if a.TokensPerDay != 500 || len(a.ProviderPriority) != 1 {
		t.Errorf("fields not in the patch changed: %+v", a)
	}
	// An empty list is a real value (reset to alias default), distinct from "absent".
	e.do("PATCH", "/admin/apps/1", `{"provider_priority":[]}`)
	if len(e.store.apps[0].ProviderPriority) != 0 {
		t.Error("empty provider_priority was ignored")
	}
}

func TestUpdateAppErrors(t *testing.T) {
	e := newEnv(t)
	e.do("POST", "/admin/apps", `{"name":"a"}`)
	cases := []struct {
		path, body string
		want       int
	}{
		{"/admin/apps/99", `{"disabled":true}`, 404},
		{"/admin/apps/abc", `{}`, 400},
		{"/admin/apps/0", `{}`, 400},
		{"/admin/apps/1", `{"rpm_limit":-3}`, 400},
		{"/admin/apps/1", `{"provider_priority":["zzz"]}`, 400},
		{"/admin/apps/1", `{"name":"renamed"}`, 400}, // not patchable
	}
	for _, c := range cases {
		if code, body := e.do("PATCH", c.path, c.body); code != c.want {
			t.Errorf("PATCH %s %s: got %d want %d: %s", c.path, c.body, code, c.want, body)
		}
	}
}

func TestUsage(t *testing.T) {
	e := newEnv(t)
	e.store.usage = []UsageRow{
		{Day: "2026-10-12", AppID: 1, AppName: "a", Provider: "groq", Requests: 1, Tokens: 1},   // before the default window
		{Day: "2026-10-14", AppID: 1, AppName: "a", Provider: "groq", Requests: 10, Tokens: 100, CacheHits: 2},
		{Day: "2026-10-20", AppID: 2, AppName: "", Provider: "", Requests: 3, Tokens: 0, CacheHits: 3},
	}
	var got struct {
		From, To string
		Rows     []UsageRow
	}
	_, body := e.do("GET", "/admin/usage", "")
	_ = json.Unmarshal(body, &got)
	if got.From != "2026-10-14" || got.To != "2026-10-20" || len(got.Rows) != 2 {
		t.Errorf("default window should be the 7 days ending today (UTC): %+v", got)
	}
	_, body = e.do("GET", "/admin/usage?app_id=2&from=2026-10-01&to=2026-10-20", "")
	got.Rows = nil
	_ = json.Unmarshal(body, &got)
	if len(got.Rows) != 1 || got.Rows[0].AppID != 2 {
		t.Errorf("app filter: %+v", got.Rows)
	}
	_, body = e.do("GET", "/admin/usage?from=2030-01-01&to=2030-01-02", "")
	if !strings.Contains(string(body), `"rows":[]`) {
		t.Errorf("an empty result must be [] not null: %s", body)
	}
	for _, q := range []string{"from=yesterday", "to=2026-13-40", "from=2026-10-20&to=2026-10-01", "from=2020-01-01&to=2026-10-20", "app_id=x"} {
		if code, _ := e.do("GET", "/admin/usage?"+q, ""); code != http.StatusBadRequest {
			t.Errorf("?%s: got %d, want 400", q, code)
		}
	}
}

func providersOf(t *testing.T, e *env) map[string]ProviderStatus {
	t.Helper()
	code, body := e.do("GET", "/admin/providers", "")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var resp struct{ Providers []ProviderStatus }
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	out := map[string]ProviderStatus{}
	for _, p := range resp.Providers {
		out[p.Name] = p
	}
	return out
}

func TestProviderStates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := providersOf(t, e)
	if p["groq"].State != "healthy" || p["gemini"].State != "healthy" || p["openrouter"].State != "no_api_key" {
		t.Errorf("initial states: groq=%s gemini=%s openrouter=%s", p["groq"].State, p["gemini"].State, p["openrouter"].State)
	}

	e.rt.StartCooldown("groq")
	p = providersOf(t, e)
	if p["groq"].State != "cooling_down" || p["groq"].CooldownRemainingS < 1 || p["groq"].CooldownRemainingS > 30 {
		t.Errorf("cooldown: %+v", p["groq"])
	}

	// exhausted outranks cooling_down
	_ = e.kv.Set(ctx, "exhausted:groq", []byte("1"), time.Hour)
	if got := providersOf(t, e)["groq"].State; got != "exhausted" {
		t.Errorf("state = %s, want exhausted", got)
	}
}

func TestProviderCountersUseTheProvidersOwnDay(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// "now" is 2026-10-20 05:00 UTC = 2026-10-19 21:00 at UTC-8, so gemini's day is the 19th.
	_, _ = e.kv.IncrWindow(ctx, "guard:req:gemini:2026-10-19", time.Hour, 7)
	_, _ = e.kv.IncrWindow(ctx, "guard:req:gemini:2026-10-20", time.Hour, 99) // wrong day for gemini
	_, _ = e.kv.IncrWindow(ctx, "guard:req:groq/m1:2026-10-20", time.Hour, 40)
	_, _ = e.kv.IncrWindow(ctx, "guard:tok:groq/m1:2026-10-20", time.Hour, 1234)
	_ = e.kv.Set(ctx, "exhausted:groq/m1", []byte("1"), time.Hour)

	p := providersOf(t, e)
	if p["gemini"].RequestsToday != 7 || p["gemini"].Limits.RequestsPerDay != 50 {
		t.Errorf("gemini: %+v", p["gemini"])
	}
	if want := time.Date(2026, 10, 20, 0, 0, 0, 0, time.FixedZone("x", -8*3600)); !p["gemini"].ResetsAt.Equal(want) {
		t.Errorf("gemini resets_at = %s, want %s", p["gemini"].ResetsAt, want)
	}
	g := p["groq"]
	if len(g.Models) != 1 || g.Models[0].Model != "m1" {
		t.Fatalf("only models with a daily limit are listed: %+v", g.Models)
	}
	if m := g.Models[0]; m.RequestsToday != 40 || m.TokensToday != 1234 || !m.Exhausted || m.Limits.RequestsPerDay != 100 {
		t.Errorf("model status: %+v", m)
	}
	if g.State != "healthy" {
		t.Errorf("a model-level flag must not mark the whole provider exhausted, got %s", g.State)
	}
}

func TestProvidersCacheDown(t *testing.T) {
	e := newEnv(t)
	e.kv.Err = context.DeadlineExceeded
	if code, _ := e.do("GET", "/admin/providers", ""); code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503 rather than made-up zeros", code)
	}
}
