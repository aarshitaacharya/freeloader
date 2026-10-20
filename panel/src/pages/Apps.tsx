import { useState } from "react";
import { ApiError } from "../api/client";
import type { App } from "../api/schemas";
import { useAuth, useClient } from "../auth";
import { AppDialog, KeyDialog, type SaveRequest } from "../components/AppDialogs";
import { compact } from "../lib/format";
import { useAsync } from "../lib/useAsync";

const limit = (n: number, fmt: (n: number) => string = String): string => (n === 0 ? "unlimited" : fmt(n));

export function Apps() {
  const client = useClient();
  const { logout } = useAuth();
  const apps = useAsync(() => client.listApps(), [client]);
  const providers = useAsync(() => client.listProviders(), [client]);
  const [editing, setEditing] = useState<App | "new">();
  const [created, setCreated] = useState<{ name: string; key: string }>();
  const [actionError, setActionError] = useState<string>();

  const providerNames = providers.data?.map((p) => p.name) ?? [];

  function fail(e: unknown) {
    if (e instanceof ApiError && e.unauthorized) logout();
    setActionError(e instanceof Error ? e.message : String(e));
  }

  async function save(req: SaveRequest) {
    setActionError(undefined);
    if (req.kind === "update") {
      await client.updateApp(req.id, req.values);
    } else {
      const res = await client.createApp(req.values);
      setCreated({ name: res.app.name, key: res.key });
    }
    setEditing(undefined);
    apps.reload();
  }

  async function toggle(app: App) {
    setActionError(undefined);
    try {
      await client.updateApp(app.id, { disabled: !app.disabled });
      apps.reload();
    } catch (e) {
      fail(e);
    }
  }

  return (
    <section>
      <header className="page-head">
        <h2>Apps</h2>
        <button type="button" className="primary" onClick={() => { setEditing("new"); }}>New app</button>
      </header>
      <p className="muted">
        Each app has its own key and budget. A change reaches every gateway replica within 30 seconds.
      </p>
      {apps.error !== undefined && <p role="alert" className="error">{apps.error.message}</p>}
      {actionError !== undefined && <p role="alert" className="error">{actionError}</p>}
      {apps.loading && apps.data === undefined && <p className="muted">Loading…</p>}
      {apps.data?.length === 0 && <p className="card muted">No apps yet. Create one to get an API key.</p>}
      {apps.data !== undefined && apps.data.length > 0 && (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th scope="col">Name</th>
                <th scope="col" className="num">Requests/min</th>
                <th scope="col" className="num">Tokens/day</th>
                <th scope="col" className="num">Cache TTL</th>
                <th scope="col">Provider order</th>
                <th scope="col">Status</th>
                <th scope="col"><span className="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody>
              {apps.data.map((a) => (
                <tr key={a.id} className={a.disabled ? "dim" : undefined}>
                  <th scope="row">{a.name}{a.cache_all && <span className="tag" title="Also caches requests whose temperature is not 0">cache all</span>}</th>
                  <td className="num">{limit(a.rpm_limit)}</td>
                  <td className="num">{limit(a.tokens_per_day, compact)}</td>
                  <td className="num">{a.cache_ttl_s === 0 ? "off" : `${String(a.cache_ttl_s)} s`}</td>
                  <td>{a.provider_priority.length === 0 ? <span className="muted">alias default</span> : a.provider_priority.join(" → ")}</td>
                  <td>{a.disabled ? <span className="badge critical"><span aria-hidden="true" className="badge-icon">⛔</span>Disabled</span> : <span className="badge good"><span aria-hidden="true" className="badge-icon">✓</span>Active</span>}</td>
                  <td className="actions">
                    <button type="button" onClick={() => { setEditing(a); }} aria-label={`Edit ${a.name}`}>Edit</button>
                    <button type="button" onClick={() => void toggle(a)} aria-label={`${a.disabled ? "Enable" : "Disable"} ${a.name}`}>
                      {a.disabled ? "Enable" : "Disable"}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {editing !== undefined && (
        <AppDialog
          existing={editing === "new" ? undefined : editing}
          providers={providerNames}
          onSave={save}
          onClose={() => { setEditing(undefined); }}
        />
      )}
      {created && <KeyDialog name={created.name} apiKey={created.key} onClose={() => { setCreated(undefined); }} />}
    </section>
  );
}
