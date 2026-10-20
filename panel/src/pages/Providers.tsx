import { useClient } from "../auth";
import { Meter } from "../components/Meter";
import { StatusBadge } from "../components/StatusBadge";
import type { ProviderStatus } from "../api/schemas";
import { duration } from "../lib/format";
import { useAsync } from "../lib/useAsync";

function resetsIn(p: ProviderStatus, now: number): string {
  return duration((Date.parse(p.resets_at) - now) / 1000);
}

export function Providers() {
  const client = useClient();
  const { data, error, loading } = useAsync(() => client.listProviders(), [client], 10_000);
  const now = Date.now();

  return (
    <section>
      <header className="page-head">
        <h2>Providers</h2>
        <p className="muted">
          Today&apos;s requests against each provider&apos;s daily limits, as counted by fl-guard from usage events.
          Refreshes every 10 seconds.
        </p>
      </header>
      {error !== undefined && <p role="alert" className="error">{error.message}</p>}
      {loading && data === undefined && <p className="muted">Loading…</p>}
      <div className="grid">
        {data?.map((p) => (
          <article key={p.name} className="card" aria-label={p.name}>
            <header className="card-head">
              <h3>{p.name}</h3>
              <StatusBadge state={p.state} />
            </header>
            <p className="muted small">
              Daily quota resets in {resetsIn(p, now)} (midnight {p.reset_timezone})
              {p.state === "cooling_down" && <> · this replica is skipping it for another {duration(p.cooldown_remaining_s)}</>}
            </p>
            {(p.limits.requests_per_day > 0 || p.limits.tokens_per_day > 0) && (
              <>
                {p.limits.requests_per_day > 0 && <Meter label="Requests" used={p.requests_today} limit={p.limits.requests_per_day} unit="requests" />}
                {p.limits.tokens_per_day > 0 && <Meter label="Tokens" used={p.tokens_today} limit={p.limits.tokens_per_day} unit="tokens" />}
              </>
            )}
            {p.models.map((m) => (
              <div key={m.model} className="model">
                <h4>
                  {m.model}
                  {m.exhausted && <span className="badge critical"><span aria-hidden="true" className="badge-icon">⛔</span>Exhausted</span>}
                </h4>
                {m.limits.requests_per_day > 0 && <Meter label="Requests" used={m.requests_today} limit={m.limits.requests_per_day} unit="requests" />}
                {m.limits.tokens_per_day > 0 && <Meter label="Tokens" used={m.tokens_today} limit={m.limits.tokens_per_day} unit="tokens" />}
              </div>
            ))}
            {p.limits.requests_per_day === 0 && p.limits.tokens_per_day === 0 && p.models.length === 0 && (
              <p className="muted small">No daily limits configured for this provider.</p>
            )}
          </article>
        ))}
      </div>
    </section>
  );
}
