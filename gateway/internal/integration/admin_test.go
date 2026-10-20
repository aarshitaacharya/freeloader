//go:build integration

package integration

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/aarshitaacharya/freeloader/gateway/internal/admin"
	"github.com/aarshitaacharya/freeloader/gateway/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// The admin Store against a real Postgres with the real migrations.
func TestAdminStoreAgainstRealPostgres(t *testing.T) {
	ctx := context.Background()
	m1, _ := filepath.Abs("../../../migrations/000001_apps.up.sql")
	m2, _ := filepath.Abs("../../../migrations/000002_usage.up.sql")
	pg, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("freeloader"), postgres.WithUsername("freeloader"), postgres.WithPassword("pw"),
		postgres.WithInitScripts(m1, m2), postgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(pg) })
	dsn, _ := pg.ConnectionString(ctx, "sslmode=disable")
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := admin.PGStore{Pool: pool}

	rpm, tok, ttl, all := 5, int64(1000), 60, true
	key, hash, _ := auth.GenerateKey()
	created, err := store.CreateApp(ctx, admin.NewApp{Name: "alpha", RPMLimit: &rpm, TokensPerDay: &tok,
		CacheTTLSeconds: &ttl, ProviderPriority: []string{"groq", "gemini"}, CacheAll: &all}, hash)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || created.CreatedAt.IsZero() || created.Name != "alpha" || created.RPMLimit != 5 || !created.CacheAll {
		t.Errorf("created: %+v", created)
	}

	// The key made by the admin API is the one the gateway's auth finds.
	found, err := auth.PGStore{Pool: pool}.LookupByKeyHash(ctx, auth.HashKey(key))
	if err != nil || found.ID != created.ID || len(found.ProviderPriority) != 2 {
		t.Fatalf("gateway auth lookup: %+v %v", found, err)
	}

	if _, err := store.CreateApp(ctx, admin.NewApp{Name: "alpha", RPMLimit: &rpm, TokensPerDay: &tok, CacheTTLSeconds: &ttl,
		ProviderPriority: []string{}, CacheAll: &all}, []byte("other-hash")); !errors.Is(err, admin.ErrNameTaken) {
		t.Errorf("duplicate name: got %v, want ErrNameTaken", err)
	}

	// Partial update: untouched columns survive; an empty list is a real value.
	disabled := true
	empty := []string{}
	up, err := store.UpdateApp(ctx, created.ID, admin.AppPatch{Disabled: &disabled, ProviderPriority: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if !up.Disabled || len(up.ProviderPriority) != 0 || up.RPMLimit != 5 || up.TokensPerDay != 1000 || !up.CacheAll {
		t.Errorf("after update: %+v", up)
	}
	zero := 0
	up, _ = store.UpdateApp(ctx, created.ID, admin.AppPatch{RPMLimit: &zero})
	if up.RPMLimit != 0 || !up.Disabled {
		t.Errorf("setting a limit to 0 must stick: %+v", up)
	}
	if _, err := store.UpdateApp(ctx, 9999, admin.AppPatch{Disabled: &disabled}); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("missing app: got %v", err)
	}

	apps, err := store.ListApps(ctx)
	if err != nil || len(apps) != 1 || apps[0].ProviderPriority == nil {
		t.Errorf("list: %+v %v", apps, err)
	}

	// Usage: joined to app names; rows of a deleted/unknown app keep their numbers.
	for _, q := range []string{
		`INSERT INTO usage_daily VALUES ('2026-10-19', 1, 'groq', 10, 100, 2)`,
		`INSERT INTO usage_daily VALUES ('2026-10-20', 1, 'gemini', 5, 50, 0)`,
		`INSERT INTO usage_daily VALUES ('2026-10-20', 1, '', 4, 0, 4)`,
		`INSERT INTO usage_daily VALUES ('2026-10-20', 77, 'groq', 1, 1, 0)`,
		`INSERT INTO usage_daily VALUES ('2026-10-01', 1, 'groq', 9, 9, 0)`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	d := func(s string) time.Time { v, _ := time.Parse(time.DateOnly, s); return v }
	rows, err := store.Usage(ctx, d("2026-10-19"), d("2026-10-20"), nil)
	if err != nil || len(rows) != 4 {
		t.Fatalf("usage: %d rows, %v", len(rows), err)
	}
	if rows[0].Day != "2026-10-19" || rows[0].AppName != "alpha" || rows[0].Requests != 10 || rows[0].CacheHits != 2 {
		t.Errorf("row 0: %+v", rows[0])
	}
	if last := rows[3]; last.AppID != 77 || last.AppName != "" {
		t.Errorf("unknown app row: %+v", last)
	}
	id := int64(1)
	if rows, _ = store.Usage(ctx, d("2026-10-01"), d("2026-10-20"), &id); len(rows) != 4 {
		t.Errorf("app filter + wide range: %d rows", len(rows))
	}
}
