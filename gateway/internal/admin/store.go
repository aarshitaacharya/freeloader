package admin

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore is the Postgres-backed Store.
type PGStore struct{ Pool *pgxpool.Pool }

const appColumns = `id, name, rpm_limit, tokens_per_day, cache_ttl_s, provider_priority, cache_all, disabled, created_at`

func scanApp(row pgx.Row) (App, error) {
	var a App
	err := row.Scan(&a.ID, &a.Name, &a.RPMLimit, &a.TokensPerDay, &a.CacheTTLSeconds,
		&a.ProviderPriority, &a.CacheAll, &a.Disabled, &a.CreatedAt)
	if a.ProviderPriority == nil {
		a.ProviderPriority = []string{}
	}
	return a, err
}

func (s PGStore) ListApps(ctx context.Context) ([]App, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+appColumns+` FROM apps ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []App{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s PGStore) CreateApp(ctx context.Context, in NewApp, keyHash []byte) (App, error) {
	prio, _ := json.Marshal(in.ProviderPriority)
	a, err := scanApp(s.Pool.QueryRow(ctx, `
		INSERT INTO apps (name, key_hash, rpm_limit, tokens_per_day, cache_ttl_s, provider_priority, cache_all)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+appColumns,
		in.Name, keyHash, *in.RPMLimit, *in.TokensPerDay, *in.CacheTTLSeconds, prio, *in.CacheAll))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation (apps.name)
		return App{}, ErrNameTaken
	}
	return a, err
}

func (s PGStore) UpdateApp(ctx context.Context, id int64, p AppPatch) (App, error) {
	var prio []byte // nil -> NULL -> COALESCE keeps the current value
	if p.ProviderPriority != nil {
		prio, _ = json.Marshal(*p.ProviderPriority)
	}
	a, err := scanApp(s.Pool.QueryRow(ctx, `
		UPDATE apps SET
			rpm_limit         = COALESCE($2, rpm_limit),
			tokens_per_day    = COALESCE($3, tokens_per_day),
			cache_ttl_s       = COALESCE($4, cache_ttl_s),
			provider_priority = COALESCE($5::jsonb, provider_priority),
			cache_all         = COALESCE($6, cache_all),
			disabled          = COALESCE($7, disabled)
		WHERE id = $1 RETURNING `+appColumns,
		id, p.RPMLimit, p.TokensPerDay, p.CacheTTLSeconds, prio, p.CacheAll, p.Disabled))
	if errors.Is(err, pgx.ErrNoRows) {
		return App{}, ErrNotFound
	}
	return a, err
}

func (s PGStore) Usage(ctx context.Context, from, to time.Time, appID *int64) ([]UsageRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT to_char(u.day, 'YYYY-MM-DD'), u.app_id, COALESCE(a.name, ''), u.provider, u.requests, u.tokens, u.cache_hits
		FROM usage_daily u LEFT JOIN apps a ON a.id = u.app_id
		WHERE u.day BETWEEN $1 AND $2 AND ($3::bigint IS NULL OR u.app_id = $3)
		ORDER BY u.day, u.app_id, u.provider`, from, to, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageRow
	for rows.Next() {
		var r UsageRow
		if err := rows.Scan(&r.Day, &r.AppID, &r.AppName, &r.Provider, &r.Requests, &r.Tokens, &r.CacheHits); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
