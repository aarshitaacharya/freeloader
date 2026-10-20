# ADR 0002: Fixed-window rate limits

**Status:** accepted

## Context
Each app has a requests-per-minute and a tokens-per-day budget. The counters must be shared by
every gateway replica (the gateway runs as 3 pods), so they live in fl-cache.

## Decision
- **Requests per minute:** a fixed-window counter `rl:{app}:{unix_minute}`, created with
  `SET key 0 EX 60 NX` then `INCRBY key 1`, pipelined. Over the limit returns HTTP 429 with
  `Retry-After` set to the seconds left in the current minute.
- **Tokens per day:** `tok:{app}:{yyyy-mm-dd}` (UTC) accumulates the tokens each completed upstream call
  reports. A request is rejected when the counter has already reached the budget.

## Consequences
- Two commands and one round trip per request; trivially correct across replicas because `INCRBY` is
  atomic in fl-cache.
- **Window-edge bursts:** an app can send up to 2× its limit across a minute boundary (the last second of
  one window plus the first of the next). Accepted: the goal is to stop a runaway script draining a shared
  daily quota, not to shape traffic precisely. A sliding window or token bucket would fix it at the cost of
  more commands per request.
- **Token budget can overshoot by one request.** Tokens are only known after the call, so the check
  uses what has been recorded so far. A request that starts under budget may finish over it.
- Cache hits count against requests per minute but not tokens (they use no quota).
- Counters expire on their own (60 s, 48 h), so there is nothing to clean up.
