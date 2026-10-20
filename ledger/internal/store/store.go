// Package store writes usage events and their daily rollup to Postgres.
package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/aarshitaacharya/freeloader/ledger/internal/event"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ Pool *pgxpool.Pool }

type Result struct {
	Inserted   int // new events, now counted in usage_daily
	Duplicates int // already present: redelivered, so ignored
}

type rollupKey struct {
	day      time.Time
	appID    int64
	provider string
}

type rollup struct{ requests, tokens, cacheHits int64 }

const insertEvent = `
INSERT INTO usage_events
  (request_id, ts, app_id, provider, model, prompt_tokens, completion_tokens, tokens_estimated, latency_ms, status, cache_hit)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (request_id) DO NOTHING`

const upsertDaily = `
INSERT INTO usage_daily (day, app_id, provider, requests, tokens, cache_hits)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (day, app_id, provider) DO UPDATE SET
  requests   = usage_daily.requests   + EXCLUDED.requests,
  tokens     = usage_daily.tokens     + EXCLUDED.tokens,
  cache_hits = usage_daily.cache_hits + EXCLUDED.cache_hits`

// Apply writes a batch in ONE transaction: each event is inserted with
// ON CONFLICT (request_id) DO NOTHING, and only events that were actually inserted
// are added to the rollup. A redelivered event therefore changes nothing, which is
// what makes at-least-once delivery safe ("exactly once in effect"). The caller
// commits its Kafka offsets only after Apply returns nil.
func (s *Store) Apply(ctx context.Context, evs []event.Event) (Result, error) {
	var res Result
	if len(evs) == 0 {
		return res, nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit

	b := &pgx.Batch{}
	for _, e := range evs {
		b.Queue(insertEvent, e.RequestID, e.TS, e.AppID, e.Provider, e.Model,
			e.PromptTokens, e.CompletionTokens, e.TokensEstimated, e.LatencyMS, e.Status, e.CacheHit)
	}
	br := tx.SendBatch(ctx, b)
	deltas := map[rollupKey]*rollup{}
	for _, e := range evs {
		tag, err := br.Exec()
		if err != nil {
			_ = br.Close()
			return res, fmt.Errorf("insert event %s: %w", e.RequestID, err)
		}
		if tag.RowsAffected() == 0 {
			res.Duplicates++
			continue
		}
		res.Inserted++
		k := rollupKey{e.Day(), e.AppID, e.Provider}
		d := deltas[k]
		if d == nil {
			d = &rollup{}
			deltas[k] = d
		}
		d.requests++
		d.tokens += e.Tokens()
		if e.CacheHit {
			d.cacheHits++
		}
	}
	if err := br.Close(); err != nil {
		return res, err
	}

	// Upsert rollups in a fixed key order so two ledger replicas can never deadlock on each other.
	keys := make([]rollupKey, 0, len(deltas))
	for k := range deltas {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if !a.day.Equal(b.day) {
			return a.day.Before(b.day)
		}
		if a.appID != b.appID {
			return a.appID < b.appID
		}
		return a.provider < b.provider
	})
	ub := &pgx.Batch{}
	for _, k := range keys {
		d := deltas[k]
		ub.Queue(upsertDaily, pgtype.Date{Time: k.day, Valid: true}, k.appID, k.provider, d.requests, d.tokens, d.cacheHits)
	}
	ubr := tx.SendBatch(ctx, ub)
	for range keys {
		if _, err := ubr.Exec(); err != nil {
			_ = ubr.Close()
			return res, fmt.Errorf("upsert usage_daily: %w", err)
		}
	}
	if err := ubr.Close(); err != nil {
		return res, err
	}
	return res, tx.Commit(ctx)
}

// Truncate empties both derived tables (replay rebuilds them from the log).
func (s *Store) Truncate(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `TRUNCATE usage_events, usage_daily`)
	return err
}

// Totals is a comparable snapshot of the rollup, for verifying a replay.
func (s *Store) Totals(ctx context.Context) (map[string][3]int64, error) {
	rows, err := s.Pool.Query(ctx, `SELECT day::text, app_id, provider, requests, tokens, cache_hits FROM usage_daily`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][3]int64{}
	for rows.Next() {
		var day, provider string
		var app, req, tok, hits int64
		if err := rows.Scan(&day, &app, &provider, &req, &tok, &hits); err != nil {
			return nil, err
		}
		out[fmt.Sprintf("%s/%d/%s", day, app, provider)] = [3]int64{req, tok, hits}
	}
	return out, rows.Err()
}

func (s *Store) CountEvents(ctx context.Context) (n int64, err error) {
	err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM usage_events`).Scan(&n)
	return
}

func (s *Store) Ping(ctx context.Context) error { return s.Pool.Ping(ctx) }

// A ledger replica holds the shared lock for as long as it runs; replay needs the
// exclusive one, so it refuses to start while any ledger is consuming.
const advisoryKey int64 = 0x464C4C45 // "FLLE"

type Lock struct{ conn *pgxpool.Conn }

func (s *Store) lock(ctx context.Context, sql string) (*Lock, bool, error) {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var ok bool
	if err := conn.QueryRow(ctx, sql, advisoryKey).Scan(&ok); err != nil || !ok {
		conn.Release()
		return nil, false, err
	}
	return &Lock{conn: conn}, true, nil
}

// LockShared is taken by every running ledger replica.
func (s *Store) LockShared(ctx context.Context) (*Lock, bool, error) {
	return s.lock(ctx, `SELECT pg_try_advisory_lock_shared($1)`)
}

// LockExclusive is taken by replay; it fails while any shared holder exists.
func (s *Store) LockExclusive(ctx context.Context) (*Lock, bool, error) {
	return s.lock(ctx, `SELECT pg_try_advisory_lock($1)`)
}

func (l *Lock) Release() {
	if l != nil && l.conn != nil {
		// Closing the session drops its advisory locks; releasing to the pool would keep it.
		_ = l.conn.Conn().Close(context.Background())
		l.conn.Release()
	}
}
