// Package guard counts what the gateway sends to each provider and flags a
// provider (or one of its models) as exhausted before its free quota runs out.
package guard

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/aarshitaacharya/freeloader/guard/internal/cache"
	"github.com/aarshitaacharya/freeloader/guard/internal/config"
	"github.com/aarshitaacharya/freeloader/guard/internal/event"
	"github.com/aarshitaacharya/freeloader/guard/internal/metrics"
)

const (
	seenTTL = 172800 * time.Second // spec: SET seen:{request_id} 1 EX 172800 NX
	slack   = time.Hour            // counters outlive their day a little, then expire on their own
)

type Guard struct {
	Cfg     *config.Config
	KV      cache.KV
	Metrics *metrics.Metrics
	Log     *slog.Logger
	Now     func() time.Time

	mu        sync.Mutex
	exhausted map[string]bool // scope name -> currently flagged (for log transitions and clearing)
}

func New(cfg *config.Config, kv cache.KV, m *metrics.Metrics, log *slog.Logger) *Guard {
	if log == nil {
		log = slog.Default()
	}
	return &Guard{Cfg: cfg, KV: kv, Metrics: m, Log: log, Now: time.Now, exhausted: map[string]bool{}}
}

// scope is one thing with a quota: a provider as a whole ("groq") or one of its
// models ("groq/openai/gpt-oss-20b"). The scope name is also the flag name:
// the gateway skips a target when exhausted:{provider} or exhausted:{provider}/{model} exists.
type scope struct {
	name     string
	provider string
	model    string // "" for a provider-wide scope
	limits   config.Limits
	loc      *time.Location
}

// scopes lists every scope the guard tracks: each provider (its counters feed the
// provider gauges even without limits) and each model that has limits.
func (g *Guard) scopes() []scope {
	var out []scope
	names := make([]string, 0, len(g.Cfg.Providers))
	for n := range g.Cfg.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := g.Cfg.Providers[n]
		out = append(out, scope{name: n, provider: n, limits: p.Limits, loc: p.Loc})
		models := make([]string, 0, len(p.Models))
		for m := range p.Models {
			models = append(models, m)
		}
		sort.Strings(models)
		for _, m := range models {
			if lim := p.Models[m]; lim.Enforced() {
				out = append(out, scope{name: n + "/" + m, provider: n, model: m, limits: lim, loc: p.Loc})
			}
		}
	}
	return out
}

func (g *Guard) day(s scope) string { return g.Now().In(s.loc).Format("2006-01-02") }

// untilReset is how long today's counters and flags stay relevant: until the
// provider's next midnight in its own timezone.
func (g *Guard) untilReset(s scope) time.Duration {
	now := g.Now().In(s.loc)
	y, m, d := now.Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, s.loc).Sub(now)
}

func reqKey(s scope, day string) string { return fmt.Sprintf("guard:req:%s:%s", s.name, day) }
func tokKey(s scope, day string) string { return fmt.Sprintf("guard:tok:%s:%s", s.name, day) }

// Handle counts one usage event. It returns an error only for cache failures,
// which the caller retries.
//
// Delivery is at-least-once, so the first thing it does is claim
// seen:{request_id} (SET ... NX): a redelivered event finds the marker and is
// ignored. The claim comes before the counters, so a crash in between loses one
// count rather than doubling it; the guard errs on the side of under-counting.
func (g *Guard) Handle(ctx context.Context, e event.Event) error {
	// Cache hits use no provider quota; a request nobody answered used none either.
	if e.Provider == "" || e.CacheHit {
		g.Metrics.Skipped.Inc()
		return nil
	}
	p, ok := g.Cfg.Providers[e.Provider]
	if !ok {
		g.Metrics.Skipped.Inc()
		return nil
	}
	// Only today's events count toward today's quota (a replayed backlog must not).
	if e.TS.In(p.Loc).Format("2006-01-02") != g.Now().In(p.Loc).Format("2006-01-02") {
		g.Metrics.Skipped.Inc()
		return nil
	}

	first, err := g.KV.SetNX(ctx, "seen:"+e.RequestID.String(), seenTTL)
	if err != nil {
		g.Metrics.Errors.Inc()
		return err
	}
	if !first {
		g.Metrics.Duplicates.Inc()
		return nil
	}

	for _, s := range g.scopes() {
		if s.provider != e.Provider || (s.model != "" && s.model != e.Model) {
			continue
		}
		day := g.day(s)
		ttl := g.untilReset(s) + slack
		req, err := g.KV.IncrWindow(ctx, reqKey(s, day), ttl, 1)
		if err != nil {
			g.Metrics.Errors.Inc()
			return err
		}
		tok, err := g.KV.IncrWindow(ctx, tokKey(s, day), ttl, e.Tokens())
		if err != nil {
			g.Metrics.Errors.Inc()
			return err
		}
		g.publish(s, req, tok)
		if over, why := g.over(s, req, tok); over {
			if err := g.flag(ctx, s, why); err != nil {
				g.Metrics.Errors.Inc()
				return err
			}
		}
	}
	g.Metrics.Events.Inc()
	return nil
}

// over reports whether the scope has reached the threshold of any daily limit.
func (g *Guard) over(s scope, req, tok int64) (bool, string) {
	thr := g.Cfg.Guard.Threshold
	if l := s.limits.RequestsPerDay; l > 0 && float64(req) >= thr*float64(l) {
		return true, fmt.Sprintf("requests %d of %d per day", req, l)
	}
	if l := s.limits.TokensPerDay; l > 0 && float64(tok) >= thr*float64(l) {
		return true, fmt.Sprintf("tokens %d of %d per day", tok, l)
	}
	return false, ""
}

func (g *Guard) flag(ctx context.Context, s scope, why string) error {
	if err := g.KV.Set(ctx, "exhausted:"+s.name, "1", g.untilReset(s)); err != nil {
		return err
	}
	g.mu.Lock()
	was := g.exhausted[s.name]
	g.exhausted[s.name] = true
	g.mu.Unlock()
	g.setExhaustedGauge(s, 1)
	if !was {
		g.Log.Warn("provider flagged exhausted: the gateway will stop routing to it",
			"scope", s.name, "reason", why, "until", g.Now().Add(g.untilReset(s)).In(s.loc).Format(time.RFC3339))
	}
	return nil
}

func (g *Guard) unflag(ctx context.Context, s scope) error {
	if err := g.KV.Del(ctx, "exhausted:"+s.name); err != nil {
		return err
	}
	g.mu.Lock()
	was := g.exhausted[s.name]
	g.exhausted[s.name] = false
	g.mu.Unlock()
	g.setExhaustedGauge(s, 0)
	if was {
		g.Log.Info("provider no longer exhausted", "scope", s.name)
	}
	return nil
}

func (g *Guard) publish(s scope, req, tok int64) {
	if s.model == "" {
		g.Metrics.Requests.WithLabelValues(s.provider).Set(float64(req))
		g.Metrics.Tokens.WithLabelValues(s.provider).Set(float64(tok))
	} else {
		g.Metrics.ModelRequests.WithLabelValues(s.provider, s.model).Set(float64(req))
		g.Metrics.ModelTokens.WithLabelValues(s.provider, s.model).Set(float64(tok))
	}
}

func (g *Guard) setExhaustedGauge(s scope, v float64) {
	if s.model == "" {
		g.Metrics.Exhausted.WithLabelValues(s.provider).Set(v)
	} else {
		g.Metrics.ModelExhausted.WithLabelValues(s.provider, s.model).Set(v)
	}
}

// EvaluateAll re-reads every counter and brings flags and gauges in line with the
// configured limits. It runs at startup and periodically, which is what makes a
// lowered limit take effect without waiting for another event, a raised limit clear a
// flag, and the gauges drop to zero when a provider's day rolls over.
func (g *Guard) EvaluateAll(ctx context.Context) error {
	for _, s := range g.scopes() {
		day := g.day(s)
		req, err := g.KV.GetInt(ctx, reqKey(s, day))
		if err != nil {
			return err
		}
		tok, err := g.KV.GetInt(ctx, tokKey(s, day))
		if err != nil {
			return err
		}
		g.publish(s, req, tok)
		if over, why := g.over(s, req, tok); over {
			if err := g.flag(ctx, s, why); err != nil {
				return err
			}
		} else if s.limits.Enforced() {
			if err := g.unflag(ctx, s); err != nil {
				return err
			}
		} else {
			g.setExhaustedGauge(s, 0)
		}
	}
	return nil
}
