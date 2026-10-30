import { describe, expect, it } from "vitest";
import { splitCitations } from "./citations";
import { niceMax } from "./format";
import { SseParser } from "./sse";

describe("SseParser", () => {
  it("handles events split across chunks", () => {
    const p = new SseParser();
    expect(p.push('event: sources\ndata: [1,')).toEqual([]);
    expect(p.push('2]\n\nevent: delta\ndata: {"text":"hi"}\n\nevent: do')).toEqual([
      { event: "sources", data: "[1,2]" },
      { event: "delta", data: '{"text":"hi"}' },
    ]);
    expect(p.push('ne\r\ndata: {}\r\n\r\n')).toEqual([{ event: "done", data: "{}" }]);
  });
});

describe("splitCitations", () => {
  it("separates text from source markers", () => {
    expect(splitCitations("Refunds take 5 days [S1][S3]. Done")).toEqual([
      { kind: "text", text: "Refunds take 5 days " },
      { kind: "cite", index: 1 },
      { kind: "cite", index: 3 },
      { kind: "text", text: ". Done" },
    ]);
    expect(splitCitations("no citations")).toEqual([{ kind: "text", text: "no citations" }]);
  });
});

describe("niceMax", () => {
  it("rounds up to readable axis tops", () => {
    expect([0, 7, 12, 230, 4100, 25000].map(niceMax)).toEqual([1, 10, 20, 250, 5000, 25000]);
  });
});
