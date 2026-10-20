# ADR 0005: Cooldowns for persistent provider failures

**Status:** accepted

## Context
The spec puts a provider in a 30-second local cooldown after a 429. Running against the real providers showed
other failures where retrying immediately is equally pointless, and costly:
- A key from a Google project with no credit answers **402** on every call.
- Two Gemini models accepted requests and then **never responded**; each request would wait the full
  30 s attempt timeout before failing over.
- A revoked key answers **401** on every call.

## Decision
- 402 joins 401/403/404/408/429/5xx as a failure that fails over to the next provider.
- A replica stops routing to a provider for the cooldown (default 30 s) after **429, 401, 402, 403 or a
  timeout**. Plain 5xx do not start a cooldown: they are often momentary and failing fast on them is cheap.
- The cooldown is per replica and in memory. The shared `exhausted:{provider}` flag in fl-cache (set by
  fl-guard, level 6) remains the fleet-wide mechanism.

## Consequences
- A dead or hung provider costs one failed attempt (or one timeout) per cooldown window per replica, not one
  per request. Verified live: with Groq's key deliberately broken, the first request went Groq 401 → Gemini 402 →
  OpenRouter (3 attempts); the next went straight to OpenRouter (1 attempt).
- A provider that recovers within the window stays unused until it ends. Acceptable for a free-tier fleet.
