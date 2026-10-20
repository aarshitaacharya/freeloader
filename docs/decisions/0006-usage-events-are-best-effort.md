# ADR 0006: Usage events are best-effort; the broker may be down

**Status:** accepted

## Context
Every request emits one event to the `llm.usage` topic (Redpanda), consumed by the ledger (Postgres totals) and
the budget guard (level 6). The gateway's first job is to keep serving.

## Decision
- **Publishing never blocks or fails a request.** The producer (franz-go, `acks=all`) buffers up to 10,000
  events and `TryProduce`s: a full buffer drops the event immediately instead of waiting.
- **Every drop is counted** in `fl_gateway_events_dropped_total`; `fl_gateway_events_buffered` shows events
  waiting for the broker, so an outage is visible *before* anything is dropped.
- **Delivery has a deadline.** An event that cannot be delivered within 10 s is dropped and counted. Dial and
  request timeouts are 3 s so a dead broker connection is noticed quickly.
- **The producer is not idempotent, on purpose.** An idempotent producer cannot fail a batch it has already
  sequenced, so during an outage buffered events could never expire and newer events queued behind them
  forever (observed: after the broker came back, 14 of 15 events were lost, including ones sent after recovery).
  Without idempotence they expire at the deadline and recovery is clean. Retries may duplicate an event; the
  ledger dedupes on `request_id` (`ON CONFLICT DO NOTHING`), so duplicates are harmless.
  One in-flight produce request per broker keeps one app's events in order across retries.
- **One event per request that reached routing**, whatever the outcome (success, cache hit, provider failure,
  rate limit). Requests rejected before that (bad key, malformed body, unknown model) emit none.
- Streams: tokens come from the provider's final usage chunk (the gateway always asks for it). If a provider
  sends none, tokens are estimated at ~4 bytes per token and the event carries `tokens_estimated: true`.

## Consequences
- During a broker outage the ledger misses events: totals are a lower bound for that window. That is the
  accepted trade-off ("serving requests matters more than perfect accounting"). The source of truth for *quota
  used* remains the per-app counters in fl-cache and the providers' own accounting.
- Verified live: with Redpanda stopped, 10 requests all succeeded, `buffered` rose to 10 at once, all 10 became
  `dropped` after the delivery timeout, and 5 requests sent after recovery were all delivered.
