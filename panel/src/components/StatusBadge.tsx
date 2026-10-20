import type { ProviderState } from "../api/schemas";

const LOOK: Record<ProviderState, { icon: string; label: string; cls: string }> = {
  healthy: { icon: "✓", label: "Healthy", cls: "good" },
  cooling_down: { icon: "⏸", label: "Cooling down", cls: "warning" },
  exhausted: { icon: "⛔", label: "Exhausted", cls: "critical" },
  no_api_key: { icon: "⚠", label: "No API key", cls: "serious" },
};

/** Status is never colour alone: always an icon and a label. */
export function StatusBadge({ state }: { state: ProviderState }) {
  const l = LOOK[state];
  return (
    <span className={`badge ${l.cls}`}>
      <span aria-hidden="true" className="badge-icon">{l.icon}</span>
      {l.label}
    </span>
  );
}
