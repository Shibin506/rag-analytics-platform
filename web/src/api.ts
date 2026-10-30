import { SseParser } from "./lib/sse";

export const DEMO_KEYS = {
  acme: "lumen_acme_demo_key",
  globex: "lumen_globex_demo_key",
} as const;

let apiKey: string = DEMO_KEYS.acme;
export const setApiKey = (k: string) => {
  apiKey = k;
};

export class ApiError extends Error {}

export async function gql<T>(query: string, variables?: Record<string, unknown>): Promise<T> {
  const res = await fetch("/graphql", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-API-Key": apiKey },
    body: JSON.stringify({ query, variables }),
  });
  if (!res.ok) throw new ApiError(`GraphQL HTTP ${res.status}`);
  const body = (await res.json()) as { data?: T; errors?: { message: string }[] };
  if (body.errors?.length) throw new ApiError(body.errors.map((e) => e.message).join("; "));
  return body.data as T;
}

export async function uploadDocument(title: string, text: string): Promise<string> {
  const res = await fetch("/api/ingest/v1/documents", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-API-Key": apiKey },
    body: JSON.stringify({ title, text }),
  });
  const body = await res.json();
  if (!res.ok) throw new ApiError(body.error ?? `HTTP ${res.status}`);
  return body.document_id as string;
}

export interface Source {
  id: string;
  chunk_id: number;
  document_id: string;
  title: string;
  content: string;
  score: number;
}

export interface AskDone {
  answer_id: string;
  mode: string;
  cited_chunk_ids: number[];
  retrieval_ms: number;
  total_ms: number;
}

export interface AskHandlers {
  onSources: (s: Source[]) => void;
  onDelta: (text: string) => void;
  onDone: (d: AskDone) => void;
  onError: (message: string) => void;
}

/** Ask a question and stream the answer (server-sent events over fetch). */
export async function askStream(question: string, h: AskHandlers, signal?: AbortSignal) {
  const res = await fetch("/api/rag/v1/ask/stream", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-API-Key": apiKey },
    body: JSON.stringify({ question }),
    signal,
  });
  if (!res.ok || !res.body) {
    const text = await res.text();
    h.onError(text || `HTTP ${res.status}`);
    return;
  }
  const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
  const parser = new SseParser();
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    for (const e of parser.push(value)) {
      const data = JSON.parse(e.data);
      if (e.event === "sources") h.onSources(data);
      else if (e.event === "delta") h.onDelta(data.text);
      else if (e.event === "done") h.onDone(data);
      else if (e.event === "error") h.onError(data.message);
    }
  }
}

export async function sendFeedback(answerId: string, vote: 1 | -1) {
  const res = await fetch("/api/rag/v1/feedback", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-API-Key": apiKey },
    body: JSON.stringify({ answer_id: answerId, vote }),
  });
  if (!res.ok) throw new ApiError(`feedback failed: HTTP ${res.status}`);
}
