# ADR 0003: fl-cache state may be lost; the gateway fails open

**Status:** accepted

## Context
fl-cache holds the response cache, rate-limit counters and provider "exhausted" flags, all in memory with
no persistence. It runs as a single replica. It can restart (deploy, crash, node drain) or be unreachable.

## Decision
- Nothing in fl-cache is a source of truth. Responses can be regenerated, counters reset harmlessly, flags are
  recomputed by fl-guard from the event log (level 6). The durable record of usage is Postgres.
- When a cache call fails or times out (default 100 ms), the gateway **serves the request anyway**: no cache
  lookup, no rate limiting, no counters. It increments `fl_gateway_cache_errors_total` and logs a warning.
- Provider routing treats a failed "exhausted" lookup as "not flagged".
- `/readyz` still fails when fl-cache is down (so Kubernetes stops sending *new* pods traffic during startup),
  but `/healthz` does not depend on it, so a cache outage never gets gateway pods killed.

## Consequences
- While fl-cache is down, per-app limits are **not enforced** and every request spends provider quota. For a
  personal tool whose job is making free quota go further, serving beats strictness; the metric makes the
  outage visible. A shared multi-tenant deployment would choose differently (fail closed, or a local fallback limiter).
- After a restart the hit rate starts at zero and recovers as the cache refills; rate-limit counters restart
  from zero, so a budget can be exceeded once across a restart.
- Verified by `internal/integration`: stop the container mid-run, requests keep succeeding; start it, the
  gateway recovers without a restart.
