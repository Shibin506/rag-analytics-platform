export type Part = { kind: "text"; text: string } | { kind: "cite"; index: number };

/** Split an answer into text and [S1]-style citation markers. */
export function splitCitations(answer: string): Part[] {
  const parts: Part[] = [];
  const re = /\[S(\d+)\]/g;
  let last = 0;
  for (let m = re.exec(answer); m; m = re.exec(answer)) {
    if (m.index > last) parts.push({ kind: "text", text: answer.slice(last, m.index) });
    parts.push({ kind: "cite", index: Number(m[1]) });
    last = m.index + m[0].length;
  }
  if (last < answer.length) parts.push({ kind: "text", text: answer.slice(last) });
  return parts;
}
