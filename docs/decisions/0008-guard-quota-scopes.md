# ADR 0008: fl-guard flags exhaustion per provider *and* per model

**Status:** accepted

## Context
The spec has the guard set one `exhausted:{provider}` flag at 90% of the provider's daily limit. The real
free-tier limits (read from each provider's own pages) do not fit one number per provider:
- **Groq** limits are per model (e.g. `gpt-oss-20b`: 1,000 requests and 200,000 tokens per day).
- **Gemini** limits are per model and differ wildly (`3.1 Flash Lite` 500 requests/day; `3.8 Flash` only 20).
- **OpenRouter**'s free quota (50 requests/day) is shared by *all* `:free` models.
A single flag per provider would let Gemini's 20-per-day model block its 500-per-day one.

## Decision
- Limits are configured at two scopes in `providers.yaml`: `limits` (provider-wide) and `models.<id>` (per model).
- The guard keeps counters for each scope and sets `exhausted:{provider}` or `exhausted:{provider}/{model}`
  at the configured threshold (default 90%) of either the daily request or the daily token limit.
  The gateway skips a target when either flag exists.
- Each flag expires at the provider's own next midnight (`reset.timezone`), so counters and flags reset on the
  provider's day, not ours. Gemini: America/Los_Angeles (documented). Groq and OpenRouter: UTC, **unverified**
  (the config says so); a wrong guess only shifts when the flag lifts, and the gateway's cooldown on real 429s is
  the backstop.
- Counting: one `seen:{request_id}` marker (48 h) makes redelivery harmless. The marker is claimed *before* the
  counters, so a crash between the two loses one count rather than doubling it: the guard errs toward
  under-counting. Cache hits, unanswered requests, unknown providers and events from an earlier day are ignored.
- At startup and every 15 s the guard re-evaluates every scope from the counters in fl-cache. That is what makes a
  **lowered limit take effect immediately** (not at the next event), a raised one clear the flag, and gauges drop
  to zero on a new day.

## Consequences
- The guard only sees what the gateway sends. Limits are per project/organization, so traffic from elsewhere
  (the Gemini page showed 20 requests/minute against a 15 limit from this project) spends quota it cannot see.
- It only sees *answered* requests, because events record the provider that answered. Failed attempts that burned
  quota (OpenRouter counts them) are invisible to it. Per-attempt events would be needed to close that gap.
- If fl-cache restarts, the flags *and* the counters are lost (ADR 0003), so the guard starts counting from zero
  and a provider that was nearly exhausted can be tried again until the counters catch up; the gateway's cooldown
  on real 429s is the backstop.
- Verified live: with the limit lowered to 100/day and 101 requests counted, the guard flagged the provider on
  startup and the next request got an immediate 503 without touching the provider; raising it cleared the flag.

## Addendum (2026-10-20): reset times still unverified

Searching Groq's and OpenRouter's documentation did not turn up a stated reset time for their daily request
limits. Gemini's (midnight Pacific) is documented. Groq's rate-limit headers hint at a rolling window rather
than a fixed midnight, which would make the guard's "flag until the provider's next midnight" an approximation:
it could clear a flag later or earlier than the provider does. The guard's threshold (90%) leaves headroom, and
a provider that is still over its limit simply returns 429, which the gateway handles with a cooldown and
failover. Both entries stay marked UNVERIFIED in `config/providers.example.yaml`.
