# ADR 0007: fl-ledger is at-least-once delivery with an idempotent write

**Status:** accepted

## Context
Usage must land in Postgres durably and never be double counted, but Kafka consumers get at-least-once
delivery: after a crash, events that were already written are delivered again.

## Decision
- **One transaction per batch.** Each event is inserted with `ON CONFLICT (request_id) DO NOTHING`; only events
  that were actually inserted are added to the `usage_daily` rollup, in the same transaction. A redelivered event
  changes nothing. Events and rollup commit or roll back together.
- **Offsets are committed only after the database commit**, explicitly by the ledger (franz-go's auto-commit is
  off): on a timer (default 1 s), when partitions are handed over, and on graceful shutdown. A crash between the
  database commit and the offset commit causes redelivery, which the insert ignores and counts in
  `fl_ledger_duplicates_total` (evidence that the dedupe fired).
- **A failed write never advances.** The batch is retried with backoff; offsets are not committed past it.
- **Records that can never parse are skipped and counted** (`fl_ledger_invalid_total`) so one bad record cannot
  wedge a partition. There is deliberately no foreign key from `usage_events` to `apps`, for the same reason.
- **Replay** (`ledger replay --confirm`) truncates `usage_events` *and* `usage_daily` (both are derived) and
  rebuilds them from offset 0 under a fresh consumer group, stopping at the end offsets it captured at the
  start. It refuses to run while any ledger replica is up (Postgres advisory lock: replicas hold it shared,
  replay needs it exclusive) and refuses when retention has already removed the start of the log unless
  `--force`: a replay can only rebuild what the log still holds (7 days).
- Rollup rows are upserted in sorted key order so two replicas can never deadlock on each other.

## Consequences
- After a hard kill, partitions are unavailable until the dead member's session expires (10 s here, franz-go's
  default is 45 s). Measured: events sent during the outage were recorded 14 s after killing every replica.
- `usage_daily.requests` counts every event (cache hits and unanswered requests included, the latter under
  provider `''`); quota actually spent is `requests - cache_hits` for answered requests.
- Verified live: 100 requests → 100 `usage_events` rows and a rollup of exactly 100 requests / 1500 tokens
  after SIGKILLing both replicas before they committed (35 redeliveries ignored); replay rebuilt both tables
  with identical fingerprints.
