import { useRef, useState } from "react";
import { askStream, gql, sendFeedback, type AskDone, type Source } from "../api";
import { splitCitations } from "../lib/citations";
import { usePoll } from "../lib/usePoll";

interface History {
  viewer: {
    answers: { id: string; question: string; mode: string; totalMs: number; createdAt: string; upVotes: number; downVotes: number }[];
  };
}

const SUGGESTIONS = [
  "Do you refund monthly plans?",
  "What is the maximum number of events in one request?",
  "Why don't today's events show on the revenue dashboard?",
  "Which identity providers work with SAML SSO?",
];

export function Assistant({ tenant }: { tenant: string }) {
  const [question, setQuestion] = useState("");
  const [answer, setAnswer] = useState("");
  const [sources, setSources] = useState<Source[]>([]);
  const [done, setDone] = useState<AskDone | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [active, setActive] = useState<number | null>(null);
  const [voted, setVoted] = useState<1 | -1 | null>(null);
  const abort = useRef<AbortController | null>(null);
  const history = usePoll(() => gql<History>(
    "{ viewer { answers(limit: 8) { id question mode totalMs createdAt upVotes downVotes } } }"), 0, [tenant]);

  async function ask(q: string) {
    if (q.trim().length < 3) return;
    abort.current?.abort();
    abort.current = new AbortController();
    setQuestion(q);
    setAnswer("");
    setSources([]);
    setDone(null);
    setError(null);
    setVoted(null);
    setActive(null);
    setBusy(true);
    try {
      await askStream(q, {
        onSources: setSources,
        onDelta: (t) => setAnswer((a) => a + t),
        onDone: (d) => {
          setDone(d);
          void history.refresh();
        },
        onError: setError,
      }, abort.current.signal);
    } catch (e) {
      if (!(e instanceof DOMException && e.name === "AbortError")) setError(String(e));
    } finally {
      setBusy(false);
    }
  }

  async function vote(v: 1 | -1) {
    if (!done) return;
    try {
      await sendFeedback(done.answer_id, v);
      setVoted(v);
      void history.refresh();
    } catch (e) {
      setError(String(e));
    }
  }

  return (
    <div className="grid two" style={{ alignItems: "start" }}>
      <section className="panel" style={{ gridColumn: "1 / -1" }}>
        <h2>Ask your documents</h2>
        <p className="sub">Hybrid retrieval (pgvector + full-text) over your uploaded documents; every claim cites its source.</p>
        <form className="ask" onSubmit={(e) => { e.preventDefault(); void ask(question); }}>
          <input type="text" value={question} onChange={(e) => setQuestion(e.target.value)}
                 placeholder="e.g. How long are raw events retained?" aria-label="Question" />
          <button className="btn primary" disabled={busy || question.trim().length < 3}>{busy ? "Answering…" : "Ask"}</button>
        </form>
        <div className="meta" style={{ marginTop: 8 }}>
          {SUGGESTIONS.map((s) => <button key={s} className="linkish" onClick={() => void ask(s)}>{s}</button>)}
        </div>
      </section>

      {(answer || error || busy) && (
        <section className="panel">
          <h2>Answer</h2>
          {error && <p className="error">{error}</p>}
          <div className="answer">
            {splitCitations(answer).map((p, i) => p.kind === "text"
              ? <span key={i}>{p.text}</span>
              : <button key={i} className={`cite ${active === p.index ? "active" : ""}`}
                        onClick={() => setActive(p.index)} aria-label={`Source ${p.index}`}>S{p.index}</button>)}
            {busy && <span className="meta"> ▍</span>}
          </div>
          {done && (
            <div className="meta" style={{ marginTop: 12 }}>
              <span className="badge">{done.mode === "claude" ? "Claude" : "Extractive (no API key)"}</span>
              <span>retrieval {done.retrieval_ms} ms · total {done.total_ms} ms</span>
              <span className="spacer" />
              <span>Helpful?</span>
              <button className="btn" disabled={voted !== null} onClick={() => void vote(1)} aria-label="Helpful">👍</button>
              <button className="btn" disabled={voted !== null} onClick={() => void vote(-1)} aria-label="Not helpful">👎</button>
              {voted !== null && <span>Thanks — feedback now nudges ranking for these sources.</span>}
            </div>
          )}
        </section>
      )}

      {sources.length > 0 && (
        <section className="panel">
          <h2>Sources</h2>
          <p className="sub">Retrieved excerpts, best first</p>
          <div className="sources">
            {sources.map((s, i) => (
              <div key={s.chunk_id} className={`source ${active === i + 1 ? "active" : ""}`}>
                <b>S{i + 1} · {s.title}</b>
                <pre>{s.content.split("\n\n").slice(1).join("\n\n") || s.content}</pre>
              </div>
            ))}
          </div>
        </section>
      )}

      <section className="panel" style={{ gridColumn: "1 / -1" }}>
        <h2>Recent questions</h2>
        {history.error && <p className="error">{history.error}</p>}
        <ul className="plain">
          {history.data?.viewer.answers.map((a) => (
            <li key={a.id} className="meta" style={{ justifyContent: "space-between" }}>
              <button className="linkish" onClick={() => void ask(a.question)}>{a.question}</button>
              <span>{a.mode} · {a.totalMs} ms · 👍 {a.upVotes} 👎 {a.downVotes}</span>
            </li>
          ))}
          {history.data?.viewer.answers.length === 0 && <li className="meta">No questions yet.</li>}
        </ul>
      </section>
    </div>
  );
}
