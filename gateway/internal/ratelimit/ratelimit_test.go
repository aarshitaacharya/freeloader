package ratelimit

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/aarshitaacharya/freeloader/gateway/internal/auth"
	"github.com/aarshitaacharya/freeloader/gateway/internal/cache"
)

func setup(now time.Time) (*Limiter, *cache.Mem, *time.Time) {
	kv := cache.NewMem()
	l := New(kv)
	cur := now
	l.Now = func() time.Time { return cur }
	kv.Now = func() time.Time { return cur }
	return l, kv, &cur
}

func TestRPMFixedWindow(t *testing.T) {
	// 12:00:10 UTC: 50 s left in this minute.
	l, _, now := setup(time.Date(2026, 10, 19, 12, 0, 10, 0, time.UTC))
	app := &auth.App{ID: 7, RPMLimit: 3}
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		d, err := l.Check(ctx, app)
		if err != nil || !d.Allowed {
			t.Fatalf("request %d: %+v %v", i, d, err)
		}
	}
	d, _ := l.Check(ctx, app)
	if d.Allowed || d.Reason != ReasonRPM {
		t.Fatalf("4th request should be rejected for rpm: %+v", d)
	}
	if d.RetryAfter != 50*time.Second {
		t.Errorf("RetryAfter = %v, want 50s", d.RetryAfter)
	}

	// Next minute: a fresh window.
	*now = now.Add(50 * time.Second)
	if d, _ := l.Check(ctx, app); !d.Allowed {
		t.Fatalf("new minute should be allowed: %+v", d)
	}
}

func TestRPMIsPerApp(t *testing.T) {
	l, _, _ := setup(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC))
	ctx := context.Background()
	a, b := &auth.App{ID: 1, RPMLimit: 1}, &auth.App{ID: 2, RPMLimit: 1}
	if d, _ := l.Check(ctx, a); !d.Allowed {
		t.Fatal("a first")
	}
	if d, _ := l.Check(ctx, a); d.Allowed {
		t.Fatal("a second should be limited")
	}
	if d, _ := l.Check(ctx, b); !d.Allowed {
		t.Fatal("b must not be affected by a")
	}
}

func TestUnlimitedWhenZero(t *testing.T) {
	l, kv, _ := setup(time.Now())
	app := &auth.App{ID: 1}
	for i := 0; i < 100; i++ {
		if d, _ := l.Check(context.Background(), app); !d.Allowed {
			t.Fatal("limit 0 means unlimited")
		}
	}
	if kv.Gets != 0 {
		t.Error("an unlimited app should not touch the cache")
	}
}

func TestTokensPerDay(t *testing.T) {
	l, _, now := setup(time.Date(2026, 10, 19, 22, 0, 0, 0, time.UTC)) // 2h to UTC midnight
	app := &auth.App{ID: 1, TokensPerDay: 1000}
	ctx := context.Background()

	if d, _ := l.Check(ctx, app); !d.Allowed {
		t.Fatal("fresh budget")
	}
	_ = l.AddTokens(ctx, app, 600)
	if d, _ := l.Check(ctx, app); !d.Allowed {
		t.Fatal("600/1000 used")
	}
	_ = l.AddTokens(ctx, app, 500) // 1100 >= 1000
	d, _ := l.Check(ctx, app)
	if d.Allowed || d.Reason != ReasonTokens {
		t.Fatalf("over budget should be rejected: %+v", d)
	}
	if d.RetryAfter != 2*time.Hour {
		t.Errorf("RetryAfter = %v, want 2h", d.RetryAfter)
	}

	// New UTC day: counter key changes, budget resets.
	*now = time.Date(2026, 10, 20, 0, 0, 1, 0, time.UTC)
	if d, _ := l.Check(ctx, app); !d.Allowed {
		t.Fatal("budget should reset at UTC midnight")
	}
}

func TestTokenRejectionDoesNotConsumeRPM(t *testing.T) {
	l, kv, _ := setup(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC))
	app := &auth.App{ID: 1, TokensPerDay: 10, RPMLimit: 5}
	ctx := context.Background()
	_ = l.AddTokens(ctx, app, 10)
	for i := 0; i < 20; i++ {
		l.Check(ctx, app)
	}
	n, _ := cache.GetInt(ctx, kv, "rl:1:"+strconv.FormatInt(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC).Unix()/60, 10))
	if n != 0 {
		t.Errorf("rpm counter = %d, want 0", n)
	}
}

func TestCacheErrorsAreReportedAndFailOpen(t *testing.T) {
	l, kv, _ := setup(time.Now())
	kv.Err = errors.New("cache down")
	for _, app := range []*auth.App{{ID: 1, RPMLimit: 1}, {ID: 2, TokensPerDay: 5}} {
		d, err := l.Check(context.Background(), app)
		if err == nil {
			t.Error("expected the cache error to be returned")
		}
		if !d.Allowed {
			t.Error("decision must be Allowed (fail open) alongside the error")
		}
	}
}
