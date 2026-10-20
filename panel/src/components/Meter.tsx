import { compact, percent } from "../lib/format";

/** Fill carries severity: accent, then warning from 70%, critical from 90% (the guard's default threshold). */
export function severity(pct: number): "ok" | "warning" | "critical" {
  if (pct >= 90) return "critical";
  if (pct >= 70) return "warning";
  return "ok";
}

export function Meter({ label, used, limit, unit }: { label: string; used: number; limit: number; unit: string }) {
  if (limit <= 0) {
    return (
      <div className="meter-row">
        <span className="meter-label">{label}</span>
        <span className="muted">{compact(used)} {unit} today · no limit configured</span>
      </div>
    );
  }
  const pct = Math.min(100, (used / limit) * 100);
  return (
    <div className="meter-row">
      <span className="meter-label">{label}</span>
      <div
        className={`meter ${severity(pct)}`}
        role="meter"
        aria-label={`${label}: ${String(used)} of ${String(limit)} ${unit}`}
        aria-valuemin={0}
        aria-valuemax={limit}
        aria-valuenow={Math.min(used, limit)}
      >
        <div className="meter-fill" style={{ width: `${String(pct)}%` }} />
      </div>
      <span className="meter-text">
        {compact(used)} / {compact(limit)} {unit} ({percent(used, limit)})
      </span>
    </div>
  );
}
