import type { UsageRow } from "../api/schemas";
import { assignEntities, dailyBuckets, legendFor, listDays, MAX_SERIES, OTHER, totals } from "./usage";

const row = (day: string, provider: string, requests: number, extra: Partial<UsageRow> = {}): UsageRow => ({
  day, app_id: 1, app_name: "web", provider, requests, tokens: requests * 10, cache_hits: 0, ...extra,
});

describe("listDays", () => {
  it("is inclusive and crosses month boundaries", () => {
    expect(listDays("2026-10-30", "2026-11-02")).toEqual(["2026-10-30", "2026-10-31", "2026-11-01", "2026-11-02"]);
  });
  it("is a single day when from == to", () => { expect(listDays("2026-10-20", "2026-10-20")).toEqual(["2026-10-20"]); });
});

describe("assignEntities", () => {
  const rows = [row("2026-10-20", "groq", 5), row("2026-10-20", "gemini", 50), row("2026-10-20", "", 9)];

  it("gives the biggest entity slot 0 and labels the empty provider", () => {
    const e = assignEntities(rows, "provider");
    expect(e.map((x) => [x.key, x.slot])).toEqual([["gemini", 0], ["", 1], ["groq", 2]]);
    expect(e[1]?.label).toBe("cache / no provider");
  });

  it("keeps colours stable when the view is filtered (they come from the full range)", () => {
    const all = assignEntities(rows, "provider");
    const filtered = rows.filter((r) => r.provider !== "gemini");
    // Callers pass the full rows to assignEntities; the filter only affects what is drawn.
    const buckets = dailyBuckets(filtered, all, "provider", "requests", ["2026-10-20"]);
    expect([...buckets[0]?.values.keys() ?? []].sort()).toEqual(["", "groq"]);
    expect(all.find((e) => e.key === "groq")?.slot).toBe(2);
  });

  it("folds everything past the eighth entity into Other instead of inventing a hue", () => {
    const many = Array.from({ length: 11 }, (_, i) => row("2026-10-20", `p${String(i).padStart(2, "0")}`, 100 - i));
    const e = assignEntities(many, "provider");
    expect(e.filter((x) => x.slot === OTHER)).toHaveLength(11 - MAX_SERIES);
    expect(e.filter((x) => x.slot !== OTHER).map((x) => x.slot)).toEqual([0, 1, 2, 3, 4, 5, 6, 7]);

    const [b] = dailyBuckets(many, e, "provider", "requests", ["2026-10-20"]);
    expect(b?.values.get(OTHER)).toBe(92 + 91 + 90); // p08..p10
    expect(b?.total).toBe(many.reduce((s, r) => s + r.requests, 0));
    expect(legendFor(e, b ? [b] : []).at(-1)).toMatchObject({ key: OTHER, label: "Other" });
  });

  it("a table lists every entity even past the eighth (no colour limit there)", () => {
    const many = Array.from({ length: 11 }, (_, i) => row("2026-10-20", `p${String(i).padStart(2, "0")}`, 100 - i));
    const e = assignEntities(many, "provider");
    const [b] = dailyBuckets(many, e, "provider", "requests", ["2026-10-20"], false);
    expect(b?.values.has(OTHER)).toBe(false);
    expect(b?.values.size).toBe(11);
    expect(legendFor(e, b ? [b] : [], false)).toHaveLength(11);
  });

  it("groups by app and labels deleted apps", () => {
    const e = assignEntities([row("2026-10-20", "groq", 3, { app_id: 7, app_name: "" })], "app");
    expect(e[0]).toMatchObject({ key: "7", label: "app 7 (deleted)" });
  });
});

describe("dailyBuckets", () => {
  it("zero-fills days without traffic and picks the metric", () => {
    const e = assignEntities([row("2026-10-19", "groq", 2)], "provider");
    const days = ["2026-10-18", "2026-10-19", "2026-10-20"];
    const req = dailyBuckets([row("2026-10-19", "groq", 2)], e, "provider", "requests", days);
    expect(req.map((b) => b.total)).toEqual([0, 2, 0]);
    const tok = dailyBuckets([row("2026-10-19", "groq", 2)], e, "provider", "tokens", days);
    expect(tok.map((b) => b.total)).toEqual([0, 20, 0]);
  });
  it("ignores rows outside the requested days", () => {
    const e = assignEntities([row("2026-01-01", "groq", 2)], "provider");
    expect(dailyBuckets([row("2026-01-01", "groq", 2)], e, "provider", "requests", ["2026-10-20"])[0]?.total).toBe(0);
  });
});

describe("totals", () => {
  it("sums requests, tokens and cache hits", () => {
    expect(totals([row("d", "a", 4, { cache_hits: 1 }), row("d", "b", 6, { cache_hits: 2 })])).toEqual({ requests: 10, tokens: 100, cacheHits: 3 });
  });
  it("is zero for no rows", () => { expect(totals([])).toEqual({ requests: 0, tokens: 0, cacheHits: 0 }); });
});
