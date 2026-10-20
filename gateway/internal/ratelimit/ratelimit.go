// Package ratelimit enforces per-app budgets using counters in fl-cache, so
// all gateway replicas share one count.
package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/aarshitaacharya/freeloader/gateway/internal/auth"
	"github.com/aarshitaacharya/freeloader/gateway/internal/cache"
)

type Limiter struct {
	KV  cache.KV
	Now func() time.Time
}

func New(kv cache.KV) *Limiter { return &Limiter{KV: kv, Now: time.Now} }

type Reason string

const (
	ReasonRPM    Reason = "requests_per_minute"
	ReasonTokens Reason = "tokens_per_day"
)

type Decision struct {
	Allowed    bool
	Reason     Reason
	RetryAfter time.Duration
}

func rpmKey(appID int64, minute int64) string { return fmt.Sprintf("rl:%d:%d", appID, minute) }
func tokKey(appID int64, day string) string   { return fmt.Sprintf("tok:%d:%s", appID, day) }

// Check applies the per-app budgets. It returns an error only if the cache is
// unreachable; the caller then fails open (serving matters more than limiting).
//
// Requests per minute: fixed-window counter rl:{app}:{unix_minute}.
// Tokens per day: rejects once tok:{app}:{yyyy-mm-dd} (UTC) has reached the
// budget, so one request may overshoot it by its own size.
func (l *Limiter) Check(ctx context.Context, app *auth.App) (Decision, error) {
	now := l.Now().UTC()

	// Tokens first: a request rejected for tokens must not consume an RPM slot.
	if app.TokensPerDay > 0 {
		used, err := cache.GetInt(ctx, l.KV, tokKey(app.ID, now.Format("2006-01-02")))
		if err != nil {
			return Decision{Allowed: true}, err
		}
		if used >= app.TokensPerDay {
			midnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
			return Decision{Reason: ReasonTokens, RetryAfter: midnight.Sub(now)}, nil
		}
	}

	if app.RPMLimit > 0 {
		minute := now.Unix() / 60
		n, err := l.KV.IncrWindow(ctx, rpmKey(app.ID, minute), 60*time.Second, 1)
		if err != nil {
			return Decision{Allowed: true}, err
		}
		if n > int64(app.RPMLimit) {
			next := time.Unix((minute+1)*60, 0)
			return Decision{Reason: ReasonRPM, RetryAfter: next.Sub(now)}, nil
		}
	}
	return Decision{Allowed: true}, nil
}

// AddTokens records tokens actually consumed by a completed upstream call.
func (l *Limiter) AddTokens(ctx context.Context, app *auth.App, n int64) error {
	if n <= 0 || app.TokensPerDay <= 0 {
		return nil
	}
	day := l.Now().UTC().Format("2006-01-02")
	// 48 h: outlives the UTC day it counts, then disappears on its own.
	_, err := l.KV.IncrWindow(ctx, tokKey(app.ID, day), 48*time.Hour, n)
	return err
}
