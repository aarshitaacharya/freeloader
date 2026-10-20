import { useState } from "react";
import { compact, niceTicks } from "../lib/format";
import { OTHER, type DayBucket, type Entity } from "../lib/usage";

const W = 720;
const H = 260;
const PAD = { l: 44, r: 8, t: 8, b: 24 };
const MAX_BAR = 24;
const GAP = 2;
const RADIUS = 4;

export const slotColor = (slot: Entity["slot"]): string => (slot === OTHER ? "var(--series-other)" : `var(--series-${String(slot + 1)})`);

interface Props {
  days: readonly DayBucket[];
  entities: readonly Entity[]; // only the ones present in `days`, in legend order
  unit: string;
}

/** Path for a rect with the top two corners rounded (the data end); square at the baseline. */
function topRounded(x: number, y: number, w: number, h: number, r: number): string {
  const rr = Math.min(r, w / 2, h);
  return `M${String(x)},${String(y + h)}V${String(y + rr)}Q${String(x)},${String(y)} ${String(x + rr)},${String(y)}H${String(x + w - rr)}Q${String(x + w)},${String(y)} ${String(x + w)},${String(y + rr)}V${String(y + h)}Z`;
}

export function StackedColumns({ days, entities, unit }: Props) {
  const [hover, setHover] = useState<number>();
  const max = Math.max(0, ...days.map((d) => d.total));
  const ticks = niceTicks(max);
  const top = ticks[ticks.length - 1] ?? 1;
  const plotW = W - PAD.l - PAD.r;
  const plotH = H - PAD.t - PAD.b;
  const slot = plotW / Math.max(1, days.length);
  const barW = Math.min(MAX_BAR, slot * 0.7);
  const y = (v: number) => PAD.t + plotH - (v / top) * plotH;
  const labelEvery = Math.ceil(days.length / 8);
  const hovered = hover === undefined ? undefined : days[hover];

  return (
    <div className="chart">
      <ul className="legend" aria-label="Legend">
        {entities.map((e) => (
          <li key={e.key}>
            <span className="swatch" style={{ background: slotColor(e.slot) }} aria-hidden="true" />
            {e.label}
          </li>
        ))}
      </ul>
      <div className="chart-wrap">
        <svg viewBox={`0 0 ${String(W)} ${String(H)}`} role="img" aria-label={`Stacked daily ${unit}`}>
          {ticks.map((t) => (
            <g key={t}>
              <line x1={PAD.l} x2={W - PAD.r} y1={y(t)} y2={y(t)} className={t === 0 ? "axis" : "grid"} />
              <text x={PAD.l - 6} y={y(t) + 4} textAnchor="end" className="tick">{compact(t)}</text>
            </g>
          ))}
          {days.map((d, i) => {
            const cx = PAD.l + slot * i + slot / 2;
            const x = cx - barW / 2;
            let acc = 0;
            const segs = entities
              .map((e) => ({ e, v: d.values.get(e.key) ?? 0 }))
              .filter((s) => s.v > 0)
              .map((s) => {
                const y0 = y(acc);
                acc += s.v;
                return { ...s, y0, y1: y(acc) };
              });
            return (
              <g key={d.day} opacity={hover === undefined || hover === i ? 1 : 0.55}>
                {segs.map((s, si) => {
                  const isTop = si === segs.length - 1;
                  const gap = si === 0 ? 0 : GAP; // 2px surface gap between segments
                  const h = Math.max(1, s.y0 - s.y1 - gap);
                  const yy = s.y1;
                  return isTop ? (
                    <path key={s.e.key} d={topRounded(x, yy, barW, h, RADIUS)} fill={slotColor(s.e.slot)} />
                  ) : (
                    <rect key={s.e.key} x={x} y={yy} width={barW} height={h} fill={slotColor(s.e.slot)} />
                  );
                })}
                {i % labelEvery === 0 && (
                  <text x={cx} y={H - 6} textAnchor="middle" className="tick">{d.day.slice(5)}</text>
                )}
                <rect
                  x={PAD.l + slot * i}
                  y={PAD.t}
                  width={slot}
                  height={plotH}
                  fill="transparent"
                  data-testid={`hit-${d.day}`}
                  onMouseEnter={() => { setHover(i); }}
                  onMouseLeave={() => { setHover(undefined); }}
                />
              </g>
            );
          })}
        </svg>
        {hovered && hover !== undefined && (
          <div
            className="tooltip"
            role="status"
            style={{ left: `${String(((PAD.l + slot * hover + slot / 2) / W) * 100)}%` }}
          >
            <strong>{hovered.day}</strong>
            <span className="muted"> · {compact(hovered.total)} {unit}</span>
            <ul>
              {entities
                .filter((e) => (hovered.values.get(e.key) ?? 0) > 0)
                .map((e) => (
                  <li key={e.key}>
                    <span className="swatch" style={{ background: slotColor(e.slot) }} aria-hidden="true" />
                    {e.label}: {(hovered.values.get(e.key) ?? 0).toLocaleString()}
                  </li>
                ))}
            </ul>
          </div>
        )}
      </div>
    </div>
  );
}

export function DataTable({ days, entities, unit }: Props) {
  return (
    <div className="table-scroll">
      <table>
        <caption className="sr-only">Daily {unit} by series</caption>
        <thead>
          <tr>
            <th scope="col">Day</th>
            {entities.map((e) => (
              <th key={e.key} scope="col" className="num">{e.label}</th>
            ))}
            <th scope="col" className="num">Total</th>
          </tr>
        </thead>
        <tbody>
          {days.map((d) => (
            <tr key={d.day}>
              <th scope="row">{d.day}</th>
              {entities.map((e) => (
                <td key={e.key} className="num">{(d.values.get(e.key) ?? 0).toLocaleString()}</td>
              ))}
              <td className="num">{d.total.toLocaleString()}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
