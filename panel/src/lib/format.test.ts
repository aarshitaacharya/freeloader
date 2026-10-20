import { compact, duration, niceTicks, percent } from "./format";

describe("compact", () => {
  it.each([
    [0, "0"], [999, "999"], [1000, "1K"], [1234, "1.2K"], [15_000, "15K"], [123_456, "123K"],
    [1_000_000, "1M"], [2_500_000, "2.5M"], [3_000_000_000, "3B"],
  ])("%d -> %s", (n, want) => { expect(compact(n)).toBe(want); });
});

describe("percent", () => {
  it("handles a zero denominator without NaN", () => { expect(percent(0, 0)).toBe("–"); });
  it.each([[1, 2, "50%"], [1, 3, "33%"], [1, 100, "1.0%"], [0, 5, "0%"], [5, 5, "100%"]])("%d/%d", (a, b, want) => {
    expect(percent(a, b)).toBe(want);
  });
});

describe("duration", () => {
  it.each([[0, "0s"], [-5, "0s"], [45, "45s"], [60, "1m 0s"], [125, "2m 5s"], [3600, "1h 0m"], [11_520, "3h 12m"]])("%d -> %s", (s, want) => {
    expect(duration(s)).toBe(want);
  });
});

describe("niceTicks", () => {
  it("starts at 0 and covers the max with clean steps", () => {
    for (const max of [1, 7, 13, 99, 480, 1234, 98_765]) {
      const t = niceTicks(max);
      expect(t[0]).toBe(0);
      expect(t[t.length - 1]).toBeGreaterThanOrEqual(max);
      const step = (t[1] ?? 0) - (t[0] ?? 0);
      expect(t.every((v, i) => v === i * step)).toBe(true);
      expect(t.length).toBeLessThanOrEqual(7);
    }
  });
  it("copes with an empty chart", () => { expect(niceTicks(0)).toEqual([0, 1]); });
});
