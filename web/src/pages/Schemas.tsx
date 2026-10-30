import { useEffect, useState } from "react";
import { gql } from "../api";
import { usePoll } from "../lib/usePoll";

interface Data {
  viewer: {
    eventTypes: { name: string; latestVersion: number; versions: { version: number; schema: string; createdAt: string }[] }[];
  };
}

interface Check {
  ok: boolean;
  version: number | null;
  errors: string[];
  breakingChanges: string[];
}

const QUERY = "{ viewer { eventTypes { name latestVersion versions { version schema createdAt } } } }";

export function Schemas({ tenant }: { tenant: string }) {
  const { data, error, refresh } = usePoll(() => gql<Data>(QUERY), 0, [tenant]);
  const [selected, setSelected] = useState<string>("");
  const [draft, setDraft] = useState("");
  const [result, setResult] = useState<{ check: Check; registered: boolean } | null>(null);
  const [busy, setBusy] = useState(false);

  const types = data?.viewer.eventTypes ?? [];
  const current = types.find((t) => t.name === selected) ?? types[0];

  useEffect(() => {
    if (current) {
      setSelected(current.name);
      setDraft(JSON.stringify(JSON.parse(current.versions[0].schema), null, 2));
      setResult(null);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [current?.name, current?.latestVersion]);

  async function run(register: boolean) {
    if (!current) return;
    setBusy(true);
    try {
      const op = register
        ? "mutation($t: String!, $s: String!) { r: registerSchema(eventType: $t, schema: $s) { ok version errors breakingChanges } }"
        : "query($t: String!, $s: String!) { r: checkSchema(eventType: $t, schema: $s) { ok version errors breakingChanges } }";
      const res = await gql<{ r: Check }>(op, { t: current.name, s: draft });
      setResult({ check: res.r, registered: register && res.r.ok });
      if (register && res.r.ok) await refresh();
    } catch (e) {
      setResult({ check: { ok: false, version: null, errors: [String(e)], breakingChanges: [] }, registered: false });
    } finally {
      setBusy(false);
    }
  }

  if (error) return <p className="error">{error}</p>;
  if (!data) return <p className="notice">Loading…</p>;

  return (
    <div className="grid two" style={{ alignItems: "start" }}>
      <section className="panel">
        <h2>Event types</h2>
        <p className="sub">Every event is validated against the latest version at the gateway.</p>
        <div className="table-wrap"><table className="data">
          <thead><tr><th>Event type</th><th className="num">Latest</th><th className="num">Versions</th></tr></thead>
          <tbody>
            {types.map((t) => (
              <tr key={t.name} style={{ cursor: "pointer", fontWeight: t.name === current?.name ? 600 : 400 }}
                  onClick={() => setSelected(t.name)}>
                <td className="mono">{t.name}</td>
                <td className="num">v{t.latestVersion}</td>
                <td className="num">{t.versions.length}</td>
              </tr>
            ))}
          </tbody>
        </table></div>
      </section>

      {current && (
        <section className="panel">
          <h2>Edit <span className="mono">{current.name}</span></h2>
          <p className="sub">New versions must be backward compatible: add optional fields, relax limits, add enum values.</p>
          <textarea rows={16} value={draft} onChange={(e) => { setDraft(e.target.value); setResult(null); }}
                    aria-label="JSON Schema" spellCheck={false} />
          <div className="meta" style={{ marginTop: 8 }}>
            <button className="btn" disabled={busy} onClick={() => void run(false)}>Check compatibility</button>
            <button className="btn primary" disabled={busy} onClick={() => void run(true)}>Register new version</button>
          </div>
          {result && (
            <div style={{ marginTop: 12 }}>
              {result.check.ok ? (
                <span className="badge good">✓ {result.registered ? `Registered v${result.check.version} — gateways enforce it within 15 s` : `Compatible — would become v${result.check.version}`}</span>
              ) : (
                <>
                  <span className="badge bad">✕ Rejected</span>
                  <ul>
                    {[...result.check.errors, ...result.check.breakingChanges].map((m) => <li key={m} className="mono">{m}</li>)}
                  </ul>
                </>
              )}
            </div>
          )}
        </section>
      )}
    </div>
  );
}
