import { gql } from "../api";
import { StackedBars, StatTile } from "../components/Charts";
import { fmtCompact, fmtMinute, fmtNumber, fmtPercent } from "../lib/format";
import { usePoll } from "../lib/usePoll";

interface Data {
  viewer: {
    ingestSeries: { minute: string; accepted: number; rejected: number }[];
    ingestTotals: {
      accepted: number;
      rejected: number;
      rejectionRate: number;
      byEventType: { eventType: string; accepted: number; rejected: number }[];
    };
    documents: { status: string }[];
    feedback: { answers: number; up: number; down: number; satisfaction: number | null };
  };
}

const QUERY = `{ viewer {
  ingestSeries(minutes: 60) { minute accepted rejected }
  ingestTotals(hours: 24) { accepted rejected rejectionRate byEventType { eventType accepted rejected } }
  documents { status }
  feedback { answers up down satisfaction }
} }`;

const SERIES = [
  { key: "accepted", label: "Accepted", color: "var(--series-1)" },
  { key: "rejected", label: "Rejected", color: "var(--series-2)" },
];

export function Overview({ tenant }: { tenant: string }) {
  const { data, error } = usePoll(() => gql<Data>(QUERY), 5000, [tenant]);
  if (error) return <p className="error">{error}</p>;
  if (!data) return <p className="notice">Loading…</p>;
  const v = data.viewer;
  const indexed = v.documents.filter((d) => d.status === "indexed").length;
  const maxType = Math.max(1, ...v.ingestTotals.byEventType.map((t) => t.accepted + t.rejected));

  return (
    <div className="grid">
      <div className="grid tiles">
        <StatTile label="Events accepted · 24 h" value={fmtCompact(v.ingestTotals.accepted)}
                  note={`${fmtNumber(v.ingestTotals.rejected)} rejected`} />
        <StatTile label="Rejection rate · 24 h" value={fmtPercent(v.ingestTotals.rejectionRate)}
                  note="failed schema or envelope checks" />
        <StatTile label="Documents indexed" value={`${indexed}`} note={`${v.documents.length} uploaded`} />
        <StatTile label="Answer satisfaction"
                  value={v.feedback.satisfaction === null ? "—" : fmtPercent(v.feedback.satisfaction, 0)}
                  note={`${v.feedback.up} 👍 · ${v.feedback.down} 👎 on ${v.feedback.answers} answers`} />
      </div>

      <section className="panel">
        <h2>Ingest per minute</h2>
        <p className="sub">Last 60 minutes, live from the gateways (refreshes every 5 s)</p>
        <StackedBars
          title="Events ingested per minute"
          series={SERIES}
          labelEvery={10}
          format={fmtCompact}
          data={v.ingestSeries.map((p) => ({
            key: p.minute, label: fmtMinute(p.minute), values: { accepted: p.accepted, rejected: p.rejected },
          }))}
        />
      </section>

      <section className="panel">
        <h2>By event type</h2>
        <p className="sub">Last 24 hours</p>
        <div className="table-wrap"><table className="data">
          <thead>
            <tr><th>Event type</th><th className="num">Accepted</th><th className="num">Rejected</th><th className="num">Rejected %</th><th className="bar-col" style={{ width: "30%" }} /></tr>
          </thead>
          <tbody>
            {v.ingestTotals.byEventType.map((t) => {
              const total = t.accepted + t.rejected;
              return (
                <tr key={t.eventType}>
                  <td className="mono">{t.eventType}</td>
                  <td className="num">{fmtNumber(t.accepted)}</td>
                  <td className="num">{fmtNumber(t.rejected)}</td>
                  <td className="num">{fmtPercent(total ? t.rejected / total : 0)}</td>
                  <td className="bar-col"><div className="inline-bar" style={{ width: `${(total / maxType) * 100}%` }} /></td>
                </tr>
              );
            })}
          </tbody>
        </table></div>
      </section>
    </div>
  );
}
