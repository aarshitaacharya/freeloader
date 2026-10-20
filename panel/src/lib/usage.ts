import type { UsageRow } from "../api/schemas";

export type Metric = "requests" | "tokens";
export type GroupBy = "provider" | "app";

/** Categorical slots in the palette; a ninth entity is folded into "Other", never a new hue. */
export const MAX_SERIES = 8;
export const OTHER = "other";

export interface Entity {
  key: string;
  label: string;
  /** 0..7 = categorical slot, OTHER = folded. */
  slot: number | typeof OTHER;
}

export function entityKey(row: UsageRow, by: GroupBy): string {
  return by === "provider" ? row.provider : String(row.app_id);
}

export function entityLabel(row: UsageRow, by: GroupBy): string {
  if (by === "provider") return row.provider === "" ? "cache / no provider" : row.provider;
  return row.app_name === "" ? `app ${String(row.app_id)} (deleted)` : row.app_name;
}

/**
 * Colour follows the entity, not its rank in the current view: slots are assigned from the
 * whole fetched range (biggest first, ties by key) and never change when a filter is applied.
 */
export function assignEntities(rows: readonly UsageRow[], by: GroupBy): Entity[] {
  const totals = new Map<string, { label: string; total: number }>();
  for (const r of rows) {
    const k = entityKey(r, by);
    const cur = totals.get(k) ?? { label: entityLabel(r, by), total: 0 };
    cur.total += r.requests;
    totals.set(k, cur);
  }
  const ordered = [...totals.entries()].sort((a, b) => b[1].total - a[1].total || a[0].localeCompare(b[0]));
  return ordered.map(([key, v], i) => ({ key, label: v.label, slot: i < MAX_SERIES ? i : OTHER }));
}

export interface DayBucket {
  day: string;
  /** value per entity key (folded entities are summed under OTHER) */
  values: Map<string, number>;
  total: number;
}

/** Every day from..to inclusive (UTC), zero-filled, so gaps in traffic show as gaps. */
export function listDays(from: string, to: string): string[] {
  const out: string[] = [];
  const end = Date.parse(`${to}T00:00:00Z`);
  for (let t = Date.parse(`${from}T00:00:00Z`); t <= end; t += 86_400_000) {
    out.push(new Date(t).toISOString().slice(0, 10));
  }
  return out;
}

export function dailyBuckets(
  rows: readonly UsageRow[],
  entities: readonly Entity[],
  by: GroupBy,
  metric: Metric,
  days: readonly string[],
  /** Charts fold past the eighth series into "Other"; a table has no colour limit and passes false. */
  fold = true,
): DayBucket[] {
  const slotOf = new Map(entities.map((e) => [e.key, e.slot]));
  const buckets = new Map<string, DayBucket>(days.map((d) => [d, { day: d, values: new Map(), total: 0 }]));
  for (const r of rows) {
    const b = buckets.get(r.day);
    if (!b) continue;
    const key = fold && slotOf.get(entityKey(r, by)) === OTHER ? OTHER : entityKey(r, by);
    const v = metric === "requests" ? r.requests : r.tokens;
    b.values.set(key, (b.values.get(key) ?? 0) + v);
    b.total += v;
  }
  return days.map((d) => buckets.get(d) ?? { day: d, values: new Map(), total: 0 });
}

export interface Totals {
  requests: number;
  tokens: number;
  cacheHits: number;
}

export function totals(rows: readonly UsageRow[]): Totals {
  return rows.reduce<Totals>(
    (t, r) => ({ requests: t.requests + r.requests, tokens: t.tokens + r.tokens, cacheHits: t.cacheHits + r.cache_hits }),
    { requests: 0, tokens: 0, cacheHits: 0 },
  );
}

export const todayUTC = (now: Date = new Date()): string => now.toISOString().slice(0, 10);

export function daysAgo(n: number, now: Date = new Date()): string {
  return new Date(now.getTime() - n * 86_400_000).toISOString().slice(0, 10);
}

/**
 * Legend/stack order: slots in order, then a single "Other" if anything was folded and has data.
 * With `fold` false (tables) every entity that has data is listed, folded or not.
 */
export function legendFor(entities: readonly Entity[], days: readonly DayBucket[], fold = true): Entity[] {
  const present = new Set(days.flatMap((d) => [...d.values.keys()]));
  if (!fold) return entities.filter((e) => present.has(e.key));
  const named = entities.filter((e) => e.slot !== OTHER && present.has(e.key));
  return present.has(OTHER) ? [...named, { key: OTHER, label: "Other", slot: OTHER }] : named;
}
