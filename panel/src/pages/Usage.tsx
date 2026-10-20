import { useMemo, useState } from "react";
import { useClient } from "../auth";
import { DataTable, StackedColumns } from "../components/StackedColumns";
import { compact, percent } from "../lib/format";
import { useAsync } from "../lib/useAsync";
import {
  assignEntities,
  dailyBuckets,
  daysAgo,
  legendFor,
  listDays,
  todayUTC,
  totals,
  type GroupBy,
  type Metric,
} from "../lib/usage";

const RANGES = [
  { label: "Last 7 days", days: 6 },
  { label: "Last 14 days", days: 13 },
  { label: "Last 30 days", days: 29 },
] as const;

export function Usage() {
  const client = useClient();
  const [range, setRange] = useState<number>(6);
  const [by, setBy] = useState<GroupBy>("provider");
  const [metric, setMetric] = useState<Metric>("requests");
  const [appId, setAppId] = useState<number>();
  const [table, setTable] = useState(false);

  const from = daysAgo(range);
  const to = todayUTC();
  const { data, error, loading } = useAsync(() => client.getUsage(from, to), [client, from, to], 15_000);

  const view = useMemo(() => {
    const rows = data?.rows ?? [];
    // Colours come from the whole range, so filtering never repaints the survivors.
    const entities = assignEntities(rows, by);
    const shown = appId === undefined ? rows : rows.filter((r) => r.app_id === appId);
    const days = listDays(from, to);
    const buckets = dailyBuckets(shown, entities, by, metric, days);
    const tableBuckets = dailyBuckets(shown, entities, by, metric, days, false); // the table never folds
    const apps = new Map(rows.map((r) => [r.app_id, r.app_name === "" ? `app ${String(r.app_id)} (deleted)` : r.app_name]));
    return {
      buckets, legend: legendFor(entities, buckets),
      tableBuckets, tableLegend: legendFor(entities, tableBuckets, false),
      totals: totals(shown), apps: [...apps.entries()].sort((a, b) => a[1].localeCompare(b[1])),
    };
  }, [data, by, metric, appId, from, to]);

  const t = view.totals;
  return (
    <section>
      <header className="page-head">
        <h2>Usage</h2>
        <p className="muted">
          From the ledger&apos;s daily rollup (UTC days). It trails live traffic by the ledger&apos;s commit interval.
        </p>
      </header>

      <div className="filters" role="group" aria-label="Filters">
        <label>
          Range
          <select value={range} onChange={(e) => { setRange(Number(e.target.value)); }}>
            {RANGES.map((r) => <option key={r.days} value={r.days}>{r.label}</option>)}
          </select>
        </label>
        <label>
          App
          <select value={appId ?? ""} onChange={(e) => { setAppId(e.target.value === "" ? undefined : Number(e.target.value)); }}>
            <option value="">All apps</option>
            {view.apps.map(([id, name]) => <option key={id} value={id}>{name}</option>)}
          </select>
        </label>
        <label>
          Group by
          <select value={by} onChange={(e) => { setBy(e.target.value as GroupBy); }}>
            <option value="provider">Provider</option>
            <option value="app">App</option>
          </select>
        </label>
        <label>
          Measure
          <select value={metric} onChange={(e) => { setMetric(e.target.value as Metric); }}>
            <option value="requests">Requests</option>
            <option value="tokens">Tokens</option>
          </select>
        </label>
      </div>

      {error !== undefined && <p role="alert" className="error">{error.message}</p>}
      {loading && data === undefined && <p className="muted">Loading…</p>}

      <div className="tiles">
        <div className="tile"><span className="muted">Requests</span><strong>{compact(t.requests)}</strong></div>
        <div className="tile"><span className="muted">Tokens</span><strong>{compact(t.tokens)}</strong></div>
        <div className="tile"><span className="muted">Cache hit rate</span><strong>{percent(t.cacheHits, t.requests)}</strong></div>
      </div>

      <div className="card">
        <header className="card-head">
          <h3>Daily {metric} by {by}</h3>
          <button type="button" onClick={() => { setTable((v) => !v); }} aria-pressed={table}>
            {table ? "Show chart" : "Show table"}
          </button>
        </header>
        {data !== undefined && t.requests === 0 && <p className="muted">No traffic in this range.</p>}
        {table
          ? <DataTable days={view.tableBuckets} entities={view.tableLegend} unit={metric} />
          : <StackedColumns days={view.buckets} entities={view.legend} unit={metric} />}
      </div>
    </section>
  );
}
