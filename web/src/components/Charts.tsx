import { useEffect, useMemo, useRef, useState } from "react";
import { niceMax } from "../lib/format";

export interface Series {
  key: string;
  label: string;
  color: string; // a CSS custom property, e.g. "var(--series-1)"
}

export interface Datum {
  key: string;
  label: string;
  values: Record<string, number>;
}

interface StackedBarsProps {
  data: Datum[];
  series: Series[];
  height?: number;
  format: (n: number) => string;
  /** Show every Nth x label (bars stay readable on narrow screens). */
  labelEvery?: number;
  title: string;
}

const PAD = { top: 8, right: 8, bottom: 22, left: 44 };
const GAP = 2; // surface gap between stacked segments
const R = 4; // rounded data-end

/** Top-rounded bar path: rounded corners at the data end, square at the base. */
function topRounded(x: number, y: number, w: number, h: number) {
  const r = Math.min(R, w / 2, h);
  return `M${x},${y + h} V${y + r} Q${x},${y} ${x + r},${y} H${x + w - r} Q${x + w},${y} ${x + w},${y + r} V${y + h} Z`;
}

export function Legend({ series }: { series: Series[] }) {
  return (
    <div className="legend" role="list">
      {series.map((s) => (
        <span key={s.key} role="listitem">
          <i style={{ background: s.color }} />
          {s.label}
        </span>
      ))}
    </div>
  );
}

/**
 * Stacked bar chart with a per-column hover tooltip, a legend for two or more
 * series, and a table view for screen readers and exact values.
 */
/** Width of an element in CSS pixels, kept up to date as it resizes. */
function useWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  const [width, setWidth] = useState(0);
  useEffect(() => {
    if (!ref.current) return;
    const ro = new ResizeObserver(([e]) => setWidth(Math.round(e.contentRect.width)));
    ro.observe(ref.current);
    return () => ro.disconnect();
  }, []);
  return [ref, width] as const;
}

export function StackedBars({ data, series, height = 220, format, labelEvery = 1, title }: StackedBarsProps) {
  const [hover, setHover] = useState<number | null>(null);
  const [asTable, setAsTable] = useState(false);
  const [ref, measured] = useWidth<HTMLDivElement>();
  // Draw at the real pixel width so text stays 11px at any size.
  const W = Math.max(240, measured || 640);

  const { max, ticks } = useMemo(() => {
    const totals = data.map((d) => series.reduce((s, x) => s + (d.values[x.key] ?? 0), 0));
    const max = niceMax(Math.max(0, ...totals));
    return { max, ticks: [0, 0.25, 0.5, 0.75, 1].map((f) => f * max) };
  }, [data, series]);

  const innerW = W - PAD.left - PAD.right;
  const innerH = height - PAD.top - PAD.bottom;
  const band = data.length ? innerW / data.length : innerW;
  const barW = Math.max(2, Math.min(28, band * 0.7));
  const y = (v: number) => PAD.top + innerH - (v / max) * innerH;
  // Thin the x labels until each has ~56px.
  const every = Math.max(labelEvery, Math.ceil(56 / Math.max(band, 1)));

  if (asTable) {
    return (
      <div>
        <button className="linkish" onClick={() => setAsTable(false)}>Show chart</button>
        <table className="data" aria-label={title}>
          <thead>
            <tr>
              <th />
              {series.map((s) => <th key={s.key} className="num">{s.label}</th>)}
            </tr>
          </thead>
          <tbody>
            {data.map((d) => (
              <tr key={d.key}>
                <td>{d.label}</td>
                {series.map((s) => <td key={s.key} className="num">{format(d.values[s.key] ?? 0)}</td>)}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    );
  }

  const h = hover !== null ? data[hover] : null;
  return (
    <div className="chart" ref={ref}>
      <div style={{ display: "flex", alignItems: "baseline" }}>
        {series.length > 1 ? <Legend series={series} /> : <span />}
        <span className="spacer" />
        <button className="linkish" onClick={() => setAsTable(true)}>View as table</button>
      </div>
      <svg viewBox={`0 0 ${W} ${height}`} width={W} height={height} role="img" aria-label={title} onMouseLeave={() => setHover(null)}>
        {ticks.map((t) => (
          <g key={t}>
            <line x1={PAD.left} x2={W - PAD.right} y1={y(t)} y2={y(t)} stroke="var(--grid)" strokeWidth={1} />
            <text x={PAD.left - 6} y={y(t) + 4} textAnchor="end">{format(t)}</text>
          </g>
        ))}
        {data.map((d, i) => {
          const cx = PAD.left + band * i + band / 2;
          let base = 0;
          const visible = series.filter((s) => (d.values[s.key] ?? 0) > 0);
          return (
            <g key={d.key}>
              {visible.map((s, j) => {
                const v = d.values[s.key] ?? 0;
                const top = y(base + v);
                const bottom = y(base);
                base += v;
                const isTop = j === visible.length - 1;
                const segH = Math.max(0, bottom - top - (j > 0 ? GAP : 0));
                const x = cx - barW / 2;
                return isTop ? (
                  <path key={s.key} d={topRounded(x, top, barW, segH)} fill={s.color}
                        opacity={hover === null || hover === i ? 1 : 0.45} />
                ) : (
                  <rect key={s.key} x={x} y={top} width={barW} height={segH} fill={s.color}
                        opacity={hover === null || hover === i ? 1 : 0.45} />
                );
              })}
              {i % every === 0 && (
                <text x={cx} y={height - 6} textAnchor="middle">{d.label}</text>
              )}
              {/* hit target: the whole column, wider than the bar */}
              <rect x={PAD.left + band * i} y={PAD.top} width={band} height={innerH} fill="transparent"
                    onMouseEnter={() => setHover(i)} />
            </g>
          );
        })}
        <line x1={PAD.left} x2={W - PAD.right} y1={y(0)} y2={y(0)} stroke="var(--border)" />
      </svg>
      {h && hover !== null && (
        <div className="tooltip" style={{
          left: `${((PAD.left + band * hover + band / 2) / W) * 100}%`,
          top: `${(y(series.reduce((s, x) => s + (h.values[x.key] ?? 0), 0)) / height) * 100}%`,
        }}>
          <div className="t">{h.label}</div>
          {series.map((s) => (
            <div className="row" key={s.key}>
              <span><i style={{ display: "inline-block", width: 8, height: 8, borderRadius: 2, background: s.color, marginRight: 6 }} />{s.label}</span>
              <b>{format(h.values[s.key] ?? 0)}</b>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

export function StatTile({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <div className="panel tile">
      <div className="label">{label}</div>
      <div className="value">{value}</div>
      {note && <div className="note">{note}</div>}
    </div>
  );
}
