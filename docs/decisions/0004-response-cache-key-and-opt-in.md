# ADR 0004: Response-cache key and opt-in

**Status:** accepted

## Decision
- A request is cacheable if it is non-streaming and either `temperature` is explicitly `0`, or the app has
  `cache_all` set. An absent `temperature` is *not* treated as 0: providers default to a non-zero value.
- The key is `resp:` + SHA-256 of a canonical JSON object (sorted keys, numbers normalised so `0` and `0.0`
  match) containing the **model alias as requested** and the fields that can change an answer.
- The spec lists model alias, messages, temperature, max_tokens and tools. I also include `tool_choice`,
  `response_format`, `top_p`, `stop`, `seed`, `n`, `max_completion_tokens`, the penalties and
  `reasoning_effort`: ignoring them would serve a wrong cached answer for a request that differs only there.
  Fields that cannot change the answer (`user`, `metadata`, `stream_options`) are excluded.
- The key does **not** include the app, so identical requests from different apps share an entry. Every app
  belongs to the same person; a multi-tenant deployment would need the app id in the key.
- `cache_all` is a column added to the spec's `apps` table, since "apps that opt in" needs somewhere to live.
  `cache_ttl_s = 0` disables caching for an app.

## Consequences
- A hit reports the provider that originally answered (`X-Freeloader-Provider`) with `X-Freeloader-Cache: HIT`.
- Streaming responses are never cached.
