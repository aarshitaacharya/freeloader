package route

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aarshitaacharya/freeloader/gateway/internal/auth"
	"github.com/aarshitaacharya/freeloader/gateway/internal/cache"
	"github.com/aarshitaacharya/freeloader/gateway/internal/config"
	"github.com/aarshitaacharya/freeloader/gateway/internal/provider"
)

func newRouter(kv cache.KV) (*Router, *time.Time) {
	provs := map[string]*provider.Provider{
		"a": provider.New("a", "http://a", "k", time.Second),
		"b": provider.New("b", "http://b", "k", time.Second),
		"c": provider.New("c", "http://c", "", time.Second), // no key
	}
	aliases := map[string][]config.Target{
		"fast": {{Provider: "a", Model: "ma"}, {Provider: "b", Model: "mb"}, {Provider: "c", Model: "mc"}},
	}
	r := New(aliases, provs, kv, 30*time.Second)
	now := time.Unix(1_000_000, 0)
	r.Now = func() time.Time { return now }
	return r, &now
}

func providersOf(ts []config.Target) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Provider)
	}
	return out
}

func TestResolve(t *testing.T) {
	r, _ := newRouter(cache.NewMem())
	app := &auth.App{}

	alias, ts, err := r.Resolve(app, "fast")
	if err != nil || alias != "fast" || len(ts) != 3 {
		t.Fatalf("alias: %q %v %v", alias, ts, err)
	}

	alias, ts, err = r.Resolve(app, "b/some/model:free")
	if err != nil || alias != "direct" || len(ts) != 1 || ts[0] != (config.Target{Provider: "b", Model: "some/model:free"}) {
		t.Fatalf("direct: %q %v %v", alias, ts, err)
	}

	for _, bad := range []string{"nope", "zzz/model", "a/", ""} {
		if _, _, err := r.Resolve(app, bad); !errors.Is(err, ErrUnknownModel) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestAppProviderPriority(t *testing.T) {
	r, _ := newRouter(cache.NewMem())
	cases := []struct {
		prio []string
		want []string
	}{
		{nil, []string{"a", "b", "c"}},
		{[]string{"b", "a"}, []string{"b", "a"}}, // reorders and drops c
		{[]string{"c"}, []string{"c"}},
		{[]string{"zzz"}, nil}, // lists nothing available
	}
	for _, tc := range cases {
		_, ts, _ := r.Resolve(&auth.App{ProviderPriority: tc.prio}, "fast")
		got := providersOf(ts)
		if len(got) != len(tc.want) {
			t.Errorf("prio %v: got %v, want %v", tc.prio, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("prio %v: got %v, want %v", tc.prio, got, tc.want)
			}
		}
	}
}

func TestSkipReasons(t *testing.T) {
	kv := cache.NewMem()
	r, now := newRouter(kv)
	ctx := context.Background()

	if reason, skip := r.Skip(ctx, "a"); skip {
		t.Fatalf("healthy provider skipped: %s", reason)
	}
	if reason, skip := r.Skip(ctx, "c"); !skip || reason != "no_api_key" {
		t.Errorf("keyless: %q %v", reason, skip)
	}
	if reason, skip := r.Skip(ctx, "ghost"); !skip || reason != "no_api_key" {
		t.Errorf("unknown: %q %v", reason, skip)
	}

	r.StartCooldown("a")
	if reason, skip := r.Skip(ctx, "a"); !skip || reason != "cooldown" {
		t.Errorf("cooldown: %q %v", reason, skip)
	}
	*now = now.Add(29 * time.Second)
	if !r.CoolingDown("a") {
		t.Error("still cooling at 29s")
	}
	*now = now.Add(2 * time.Second)
	if r.CoolingDown("a") {
		t.Error("cooldown should end after 30s")
	}

	_ = kv.Set(ctx, "exhausted:b", []byte("1"), time.Hour)
	if reason, skip := r.Skip(ctx, "b"); !skip || reason != "exhausted" {
		t.Errorf("exhausted: %q %v", reason, skip)
	}
}

func TestCacheOutageDoesNotBlockRouting(t *testing.T) {
	kv := cache.NewMem()
	kv.Err = errors.New("down")
	r, _ := newRouter(kv)
	if reason, skip := r.Skip(context.Background(), "a"); skip {
		t.Errorf("a cache outage must not make providers unroutable (%s)", reason)
	}
}

func TestCooldownRemaining(t *testing.T) {
	r, now := newRouter(cache.NewMem())
	r.StartCooldown("a")
	*now = now.Add(10 * time.Second)
	r.StartCooldown("b")
	got := r.CooldownRemaining([]config.Target{{Provider: "a"}, {Provider: "b"}})
	if got != 30*time.Second {
		t.Errorf("got %v, want 30s (b's)", got)
	}
}

func TestAliasesSorted(t *testing.T) {
	r, _ := newRouter(cache.NewMem())
	if got := r.Aliases(); len(got) != 1 || got[0] != "fast" {
		t.Errorf("%v", got)
	}
}
