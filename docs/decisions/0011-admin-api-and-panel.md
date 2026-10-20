# 0011: Admin API in the gateway, OpenAPI as the contract, a static panel

**Status:** accepted (level 9)

## Decision

- `/admin/*` lives in the gateway (`gateway/internal/admin`), not in a seventh service. It is mounted only when `ADMIN_TOKEN` is set; otherwise `/admin` is a 404. Auth is one bearer token, compared in constant time.
- `api/openapi.yaml` is the source of truth. The Go handlers are written by hand, and a test validates every real response (success and error) against the spec, so spec and server cannot drift. The panel generates its types from the same file with `openapi-typescript`, validates every response at runtime with zod, and `tsc` fails if a zod schema stops matching the generated type.
- The panel is a static Vite + React bundle served by nginx. nginx also proxies `/admin` to the gateway, so the panel and the API share an origin (no CORS). In Kubernetes the ingress routes `/v1` and `/admin` straight to the gateway and `/` to the panel.
- The admin token is typed into the panel and kept in `sessionStorage` (gone when the tab closes), never in the bundle.

## Why

- The gateway already owns the `apps` table and the auth cache; a separate admin service would need its own Postgres access and deploy path for four endpoints.
- Hand-written handlers plus a contract test were chosen over code generation: the Go side stays plain `net/http`, and the test is what makes the spec trustworthy.
- Runtime validation matters because types alone only describe what the server is supposed to send; a 2xx with a wrong shape becomes a clear `ContractError` instead of a blank chart.

## Behaviour worth knowing

- **Changes are not instant.** Gateway replicas cache app lookups for 30 s, so disabling an app or lowering a limit takes up to 30 s to apply everywhere. The panel says so.
- **Provider "cooling down" is one replica's view.** Cooldowns are per replica (ADR 0005); the admin call is answered by whichever replica the ingress picks. "Exhausted" and the counters come from fl-cache and are the same everywhere.
- **The usage page trails live traffic** by the ledger's commit interval, because it reads the `usage_daily` rollup.
- **Charts show at most 8 series**; anything smaller is folded into "Other" (the palette has 8 validated hues). The table view lists every series.
- **No login throttling.** A leaked or guessable `ADMIN_TOKEN` is the whole defence; `make init` and `up.sh` generate a random one, and the ingress is the place to add rate limiting if this is ever exposed beyond a laptop.
