import { useState } from "react";
import { gql, uploadDocument } from "../api";
import { usePoll } from "../lib/usePoll";

interface Data {
  viewer: { documents: { id: string; title: string; status: string; chunkCount: number; error: string | null; createdAt: string }[] };
}

const STATUS: Record<string, string> = { indexed: "good", failed: "bad", pending: "warn", indexing: "warn" };
const ICON: Record<string, string> = { indexed: "✓", failed: "✕", pending: "…", indexing: "…" };

export function Documents({ tenant }: { tenant: string }) {
  const { data, error, refresh } = usePoll(
    () => gql<Data>("{ viewer { documents { id title status chunkCount error createdAt } } }"), 3000, [tenant]);
  const [title, setTitle] = useState("");
  const [text, setText] = useState("");
  const [msg, setMsg] = useState<string | null>(null);

  async function onFile(f: File) {
    const body = await f.text();
    setText(body);
    const heading = body.split("\n")[0];
    setTitle(heading.startsWith("# ") ? heading.slice(2) : f.name.replace(/\.(md|txt)$/, ""));
  }

  async function submit() {
    try {
      const id = await uploadDocument(title, text);
      setMsg(`Uploaded — indexing ${id.slice(0, 8)}…`);
      setTitle("");
      setText("");
      void refresh();
    } catch (e) {
      setMsg(e instanceof Error ? e.message : String(e));
    }
  }

  return (
    <div className="grid two" style={{ alignItems: "start" }}>
      <section className="panel">
        <h2>Upload a document</h2>
        <p className="sub">Markdown or plain text. Sent to the ingest gateway, stored in S3, then chunked, embedded and indexed in pgvector.</p>
        <div className="grid">
          <input type="file" accept=".md,.txt,text/plain,text/markdown" onChange={(e) => e.target.files?.[0] && void onFile(e.target.files[0])} />
          <input type="text" placeholder="Title" value={title} onChange={(e) => setTitle(e.target.value)} aria-label="Title" />
          <textarea rows={10} placeholder="# Heading\n\nText…" value={text} onChange={(e) => setText(e.target.value)} aria-label="Text" />
          <div className="meta">
            <button className="btn primary" disabled={!title.trim() || !text.trim()} onClick={() => void submit()}>Upload</button>
            {msg && <span>{msg}</span>}
          </div>
        </div>
      </section>

      <section className="panel">
        <h2>Documents</h2>
        <p className="sub">Status refreshes every 3 s</p>
        {error && <p className="error">{error}</p>}
        <div className="table-wrap"><table className="data">
          <thead><tr><th>Title</th><th>Status</th><th className="num">Chunks</th></tr></thead>
          <tbody>
            {data?.viewer.documents.map((d) => (
              <tr key={d.id} title={d.error ?? undefined}>
                <td>{d.title}</td>
                <td><span className={`badge ${STATUS[d.status] ?? ""}`}>{ICON[d.status]} {d.status}</span></td>
                <td className="num">{d.chunkCount}</td>
              </tr>
            ))}
          </tbody>
        </table></div>
        {data?.viewer.documents.length === 0 && <p className="notice">No documents yet.</p>}
      </section>
    </div>
  );
}
