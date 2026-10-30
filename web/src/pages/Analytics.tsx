import { gql } from "../api";
import { StackedBars } from "../components/Charts";
import { fmtCompact, fmtDay, fmtMoney, fmtNumber } from "../lib/format";
import { usePoll } from "../lib/usePoll";

interface Data {
  viewer: {
    analytics: {
      available: boolean;
      latestDay: string | null;
      queryMs: number;
      daily: { day: string; eventType: string; events: number; users: number }[];
      revenue: { day: string; currency: string; orders: number; revenueCents: number }[];
      topPages: { path: string; views: number; users: number }[];
    };
  };
}

const QUERY = `{ viewer { analytics {
  available latestDay queryMs
  daily(days: 14) { day eventType events users }
  revenue(days: 14) { day currency orders revenueCents }
  topPages(days: 7, limit: 8) { path views users }
} } }`;

// Fixed slot per entity, so a filtered view never repaints the survivors.
const TYPES = [
  { key: "page.view", label: "page.view", color: "var(--series-1)" },
  { key: "order.placed", label: "order.placed", color: "var(--series-2)" },
  { key: "support.ticket", label: "support.ticket", color: "var(--series-3)" },
];

function byDay<T extends { day: string }>(rows: T[], key: (r: T) => string, value: (r: T) => number) {
  const days = new Map<string, Record<string, number>>();
  for (const r of rows) {
    const d = days.get(r.day) ?? {};
    d[key(r)] = (d[key(r)] ?? 0) + value(r);
    days.set(r.day, d);
  }
  return [...days.entries()].sort(([a], [b]) => a.localeCompare(b))
    .map(([day, values]) => ({ key: day, label: fmtDay(day), values }));
}

export function Analytics({ tenant }: { tenant: string }) {
  const { data, error } = usePoll(() => gql<Data>(QUERY), 0, [tenant]);
  if (error) return <p className="error">{error}</p>;
  if (!data) return <p className="notice">Querying the lake through Trino…</p>;
  const a = data.viewer.analytics;
  if (!a.available) {
    return <p className="notice">Trino is not reachable right now, so lake analytics are unavailable. Live ingest metrics are still on the Overview page.</p>;
  }
  if (!a.latestDay) {
    return <p className="notice">No curated data yet. The nightly pipeline (Airflow DAG <span className="mono">lumen_nightly</span>) publishes it at 02:00 UTC.</p>;
  }

  const currencies = [...new Set(a.revenue.map((r) => r.currency))].sort();
  const maxViews = Math.max(1, ...a.topPages.map((p) => p.views));

  return (
    <div className="grid">
      <p className="meta">
        <span className="badge">Curated through {fmtDay(a.latestDay)}</span>
        <span>Served by Trino from Parquet rollups in {a.queryMs} ms</span>
      </p>

      <section className="panel">
        <h2>Daily events by type</h2>
        <p className="sub">Last 14 days · from <span className="mono">lake.curated.event_daily_rollup</span></p>
        <StackedBars title="Daily events by event type" series={TYPES} format={fmtCompact} labelEvery={2}
                     data={byDay(a.daily, (r) => r.eventType, (r) => r.events)} />
      </section>

      <section className="panel">
        <h2>Revenue by currency</h2>
        <p className="sub">Last 14 days · one chart per currency (amounts in different currencies are never added together)</p>
        <div className="grid three">
          {currencies.map((c) => (
            <div key={c}>
              <div className="meta" style={{ marginBottom: 4 }}>
                <b style={{ color: "var(--text-primary)" }}>{c}</b>
                <span>{fmtMoney(a.revenue.filter((r) => r.currency === c).reduce((s, r) => s + r.revenueCents, 0), c)} total</span>
              </div>
              <StackedBars title={`Daily revenue in ${c}`} height={150} labelEvery={3}
                           series={[{ key: "revenue", label: `Revenue (${c})`, color: "var(--series-1)" }]}
                           format={(n) => fmtMoney(n, c)}
                           data={byDay(a.revenue.filter((r) => r.currency === c), () => "revenue", (r) => r.revenueCents)} />
            </div>
          ))}
        </div>
      </section>

      <section className="panel">
        <h2>Top pages</h2>
        <p className="sub">Last 7 days · scanned from <span className="mono">lake.curated.events</span> with partition pruning</p>
        <div className="table-wrap"><table className="data">
          <thead>
            <tr><th>Path</th><th className="num">Views</th><th className="num">Unique users</th><th className="bar-col" style={{ width: "35%" }} /></tr>
          </thead>
          <tbody>
            {a.topPages.map((p) => (
              <tr key={p.path}>
                <td className="mono">{p.path}</td>
                <td className="num">{fmtNumber(p.views)}</td>
                <td className="num">{fmtNumber(p.users)}</td>
                <td className="bar-col"><div className="inline-bar" style={{ width: `${(p.views / maxViews) * 100}%` }} /></td>
              </tr>
            ))}
          </tbody>
        </table></div>
      </section>
    </div>
  );
}
