package guard

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/aarshitaacharya/freeloader/guard/internal/cache"
	"github.com/aarshitaacharya/freeloader/guard/internal/config"
	"github.com/aarshitaacharya/freeloader/guard/internal/event"
	"github.com/aarshitaacharya/freeloader/guard/internal/metrics"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

var (
	pacific = mustLoc("America/Los_Angeles")
	ctx     = context.Background()
)

func mustLoc(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

type rig struct {
	g   *Guard
	kv  *cache.Mem
	m   *metrics.Metrics
	now *time.Time
}

// newRig: provider "p" resets in UTC, "gem" resets at midnight Pacific.
func newRig(t *testing.T, mutate func(*config.Config)) *rig {
	t.Helper()
	cfg := &config.Config{
		Guard: config.Guard{Threshold: 0.9},
		Providers: map[string]config.Provider{
			"p": {
				Loc:    time.UTC,
				Limits: config.Limits{RequestsPerDay: 100},
				Models: map[string]config.Limits{"small": {RequestsPerDay: 10}, "big": {RequestsPerDay: 1000, TokensPerDay: 5000}},
			},
			"gem":  {Loc: pacific, Models: map[string]config.Limits{"m": {RequestsPerDay: 20}}},
			"free": {Loc: time.UTC}, // no limits at all
		},
	}
	if mutate != nil {
		mutate(cfg)
	}
	r := &rig{kv: cache.NewMem(), m: metrics.New()}
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	r.now = &now
	r.kv.Now = func() time.Time { return *r.now }
	r.g = New(cfg, r.kv, r.m, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.g.Now = func() time.Time { return *r.now }
	return r
}

func (r *rig) ev(provider, model string, tokens int64) event.Event {
	id, _ := uuid.NewV7()
	return event.Event{RequestID: id, TS: *r.now, AppID: 1, Provider: provider, Model: model,
		PromptTokens: tokens / 2, CompletionTokens: tokens - tokens/2, Status: 200, Attempts: 1}
}

func (r *rig) send(t *testing.T, n int, provider, model string, tokens int64) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := r.g.Handle(ctx, r.ev(provider, model, tokens)); err != nil {
			t.Fatal(err)
		}
	}
}

func (r *rig) flagged(name string) bool {
	ok, _ := r.kv.Exists(ctx, "exhausted:"+name)
	return ok
}

func TestFlagsAtTheThresholdNotBefore(t *testing.T) {
	r := newRig(t, nil)
	r.send(t, 8, "p", "small", 10) // small: 8 of 10 per day = 80%
	if r.flagged("p/small") {
		t.Fatal("flagged at 80%")
	}
	r.send(t, 1, "p", "small", 10) // 9 of 10 = 90% exactly
	if !r.flagged("p/small") {
		t.Fatal("must flag at 90% of the daily request limit")
	}
	if testutil.ToFloat64(r.m.ModelExhausted.WithLabelValues("p", "small")) != 1 {
		t.Error("fl_guard_model_exhausted should be 1")
	}
}

func TestModelScopeDoesNotBlockOtherModels(t *testing.T) {
	r := newRig(t, nil)
	r.send(t, 9, "p", "small", 10)
	if !r.flagged("p/small") {
		t.Fatal("p/small should be flagged")
	}
	if r.flagged("p/big") || r.flagged("p") {
		t.Error("one model's small quota must not flag its siblings or the provider")
	}
	// ...yet the traffic still counts in the provider-wide counters.
	if got := testutil.ToFloat64(r.m.Requests.WithLabelValues("p")); got != 9 {
		t.Errorf("provider-wide requests = %v, want 9", got)
	}
}

func TestProviderWideScopeFlagsTheWholeProvider(t *testing.T) {
	r := newRig(t, nil)
	// 90 requests spread over models with large limits: the provider-wide 100/day trips.
	r.send(t, 45, "p", "big", 1)
	r.send(t, 45, "p", "anything-else", 1)
	if !r.flagged("p") {
		t.Fatal("provider-wide limit of 100/day should flag at 90")
	}
	if r.flagged("p/big") {
		t.Error("big (1000/day) is nowhere near its own limit")
	}
}

func TestTokenLimitAlsoTriggers(t *testing.T) {
	r := newRig(t, nil)
	r.send(t, 4, "p", "big", 1000) // 4000 of 5000 tokens
	if r.flagged("p/big") {
		t.Fatal("80% of tokens")
	}
	r.send(t, 1, "p", "big", 500) // 4500 = 90%
	if !r.flagged("p/big") {
		t.Fatal("must flag at 90% of the daily token limit")
	}
	if got := testutil.ToFloat64(r.m.Tokens.WithLabelValues("p")); got != 4500 {
		t.Errorf("fl_guard_provider_tokens_today = %v, want 4500", got)
	}
}

func TestFlagExpiresAtTheProvidersReset(t *testing.T) {
	r := newRig(t, nil) // 2026-10-20 12:00 UTC; provider "p" resets at 00:00 UTC
	r.send(t, 9, "p", "small", 1)
	if ttl := r.kv.TTL("exhausted:p/small"); ttl < 11*time.Hour+59*time.Minute || ttl > 12*time.Hour {
		t.Errorf("flag TTL = %v, want ~12h (until UTC midnight)", ttl)
	}

	// Gemini's day is Pacific: at 12:00 UTC it is 05:00 PDT, so 19 h remain.
	r.send(t, 18, "gem", "m", 1)
	if !r.flagged("gem/m") {
		t.Fatal("gem/m should be flagged at 18 of 20")
	}
	if ttl := r.kv.TTL("exhausted:gem/m"); ttl < 18*time.Hour+59*time.Minute || ttl > 19*time.Hour {
		t.Errorf("gem flag TTL = %v, want ~19h (until midnight Pacific)", ttl)
	}

	// After the reset the flag has expired by itself and the counters start from zero.
	*r.now = r.now.Add(13 * time.Hour) // 01:00 UTC next day: p has reset; gem has not (06:00 PDT)
	if r.flagged("p/small") {
		t.Error("p/small should be free again after UTC midnight")
	}
	if !r.flagged("gem/m") {
		t.Error("gem/m must stay flagged until midnight Pacific")
	}
	r.send(t, 1, "p", "small", 1)
	if r.flagged("p/small") {
		t.Error("a single request on the new day must not re-flag")
	}
	if got := testutil.ToFloat64(r.m.ModelRequests.WithLabelValues("p", "small")); got != 1 {
		t.Errorf("model requests today = %v, want 1 (new day's counter)", got)
	}
}

func TestDayBoundaryFollowsTheProvidersTimezone(t *testing.T) {
	r := newRig(t, nil)
	// 06:30 UTC on the 20th is 23:30 PDT on the 19th: still "yesterday" for Gemini.
	*r.now = time.Date(2026, 10, 20, 6, 30, 0, 0, time.UTC)
	r.send(t, 5, "gem", "m", 1)
	*r.now = time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC) // 01:00 PDT on the 20th: a new Gemini day
	r.send(t, 3, "gem", "m", 1)
	if got := testutil.ToFloat64(r.m.ModelRequests.WithLabelValues("gem", "m")); got != 3 {
		t.Errorf("after Pacific midnight the counter must restart: got %v, want 3", got)
	}
}

func TestRedeliveredEventsCountOnce(t *testing.T) {
	r := newRig(t, nil)
	e := r.ev("p", "small", 10)
	for i := 0; i < 5; i++ {
		if err := r.g.Handle(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if got := testutil.ToFloat64(r.m.ModelRequests.WithLabelValues("p", "small")); got != 1 {
		t.Errorf("requests = %v, want 1: redelivery must not double count", got)
	}
	if testutil.ToFloat64(r.m.Duplicates) != 4 || testutil.ToFloat64(r.m.Events) != 1 {
		t.Errorf("duplicates=%v events=%v, want 4 and 1", testutil.ToFloat64(r.m.Duplicates), testutil.ToFloat64(r.m.Events))
	}
	if ttl := r.kv.TTL("seen:" + e.RequestID.String()); ttl < 47*time.Hour || ttl > 48*time.Hour {
		t.Errorf("seen: marker TTL = %v, want 172800 s (48 h)", ttl)
	}
}

func TestEventsThatUseNoQuotaAreIgnored(t *testing.T) {
	r := newRig(t, nil)
	hit := r.ev("p", "small", 10)
	hit.CacheHit = true
	nobody := r.ev("", "", 0)
	unknown := r.ev("mystery", "x", 5)
	yesterday := r.ev("p", "small", 10)
	yesterday.TS = r.now.Add(-24 * time.Hour)
	for _, e := range []event.Event{hit, nobody, unknown, yesterday} {
		if err := r.g.Handle(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if got := testutil.ToFloat64(r.m.Skipped); got != 4 {
		t.Errorf("skipped = %v, want 4 (cache hit, no provider, unknown provider, stale day)", got)
	}
	if testutil.ToFloat64(r.m.Events) != 0 {
		t.Error("none of these may count toward a quota")
	}
	if ok, _ := r.kv.Exists(ctx, "seen:"+hit.RequestID.String()); ok {
		t.Error("ignored events must not claim a seen: marker")
	}
}

func TestCacheFailureIsReportedSoTheConsumerRetries(t *testing.T) {
	r := newRig(t, nil)
	r.kv.Err = errors.New("fl-cache down")
	if err := r.g.Handle(ctx, r.ev("p", "small", 10)); err == nil {
		t.Fatal("a cache failure must surface as an error (the event is retried, not dropped)")
	}
	r.kv.Err = nil
	e := r.ev("p", "small", 10)
	if err := r.g.Handle(ctx, e); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(r.m.ModelRequests.WithLabelValues("p", "small")); got != 1 {
		t.Errorf("requests = %v", got)
	}
}

// The Level 6 check: lowering a limit makes the gateway stop routing right away.
func TestLoweringALimitTakesEffectOnTheNextEvaluation(t *testing.T) {
	r := newRig(t, nil)
	r.send(t, 5, "p", "big", 10) // big allows 1000/day: nowhere near
	if r.flagged("p/big") {
		t.Fatal("not flagged yet")
	}

	// Operator lowers the limit to 5/day and restarts the guard (new Guard, same counters in fl-cache).
	lowered := newRig(t, func(c *config.Config) {
		c.Providers["p"].Models["big"] = config.Limits{RequestsPerDay: 5}
	})
	lowered.kv = r.kv
	lowered.g.KV = r.kv
	lowered.g.Now = func() time.Time { return *r.now }
	if err := lowered.g.EvaluateAll(ctx); err != nil {
		t.Fatal(err)
	}
	if !r.flagged("p/big") {
		t.Fatal("startup evaluation must flag a model whose counters already exceed the lowered limit")
	}
}

func TestRaisingALimitClearsTheFlag(t *testing.T) {
	r := newRig(t, nil)
	r.send(t, 9, "p", "small", 1)
	if !r.flagged("p/small") {
		t.Fatal("precondition")
	}
	r.g.Cfg.Providers["p"].Models["small"] = config.Limits{RequestsPerDay: 1000} // limit raised, guard restarted
	if err := r.g.EvaluateAll(ctx); err != nil {
		t.Fatal(err)
	}
	if r.flagged("p/small") {
		t.Error("the flag must be cleared once the counters are back under the threshold")
	}
	if testutil.ToFloat64(r.m.ModelExhausted.WithLabelValues("p", "small")) != 0 {
		t.Error("gauge should drop to 0")
	}
}

func TestEvaluateAllResetsGaugesOnANewDay(t *testing.T) {
	r := newRig(t, nil)
	r.send(t, 9, "p", "small", 1)
	*r.now = r.now.Add(13 * time.Hour) // next UTC day
	if err := r.g.EvaluateAll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(r.m.ModelRequests.WithLabelValues("p", "small")); got != 0 {
		t.Errorf("requests today = %v, want 0 on a new day", got)
	}
	if testutil.ToFloat64(r.m.ModelExhausted.WithLabelValues("p", "small")) != 0 {
		t.Error("exhausted gauge should be 0 on a new day")
	}
}

func TestProvidersWithoutLimitsAreCountedButNeverFlagged(t *testing.T) {
	r := newRig(t, nil)
	r.send(t, 500, "free", "anything", 1000)
	if r.flagged("free") {
		t.Error("a provider with no configured limits must never be flagged")
	}
	if got := testutil.ToFloat64(r.m.Requests.WithLabelValues("free")); got != 500 {
		t.Errorf("requests = %v, want 500 (still counted for the gauges)", got)
	}
}

func TestThresholdIsConfigurable(t *testing.T) {
	r := newRig(t, func(c *config.Config) { c.Guard.Threshold = 0.5 })
	r.send(t, 5, "p", "small", 1) // 5 of 10 = 50%
	if !r.flagged("p/small") {
		t.Error("with threshold 0.5 the flag must appear at 50%")
	}
}
