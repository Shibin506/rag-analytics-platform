"""Retrieval and answer eval over the sample help-center documents.

For each question we know the document and the exact phrase that answers it.
A retrieval hit means a returned chunk comes from that document AND contains
that phrase. We report hit@1, hit@3, hit@5 and MRR for vector-only,
keyword-only and hybrid retrieval, and how often the extractive answer
contains the phrase.

    TEST_DATABASE_URL=postgresql://lumen:lumen@localhost:5432/lumen \
        python -m evals.run_eval [--embedder hashing|fastembed] [--out evals/results.json]
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import sys
import uuid

import psycopg
from pgvector.psycopg import register_vector

from lumen_rag.answer import ExtractiveAnswerer
from lumen_rag.embeddings import make_embedder
from lumen_rag.indexer import index_document
from lumen_rag.retrieval import Retriever
from lumen_rag.testdb import fresh_database

HERE = pathlib.Path(__file__).resolve().parent
DOCS = HERE.parents[2] / "samples" / "docs"


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--embedder", default="hashing")
    ap.add_argument("--chunk-words", type=int, default=120)
    ap.add_argument("--out", default=None)
    ap.add_argument("--min-hybrid-hit3", type=float, default=0.0, help="fail below this hit@3 (CI gate)")
    args = ap.parse_args()

    admin = os.environ.get("TEST_DATABASE_URL")
    if not admin:
        print("TEST_DATABASE_URL is required", file=sys.stderr)
        return 2
    questions = [json.loads(line) for line in (HERE / "questions.jsonl").read_text().splitlines() if line.strip()]
    embedder = make_embedder(args.embedder)

    with fresh_database(admin) as dsn, psycopg.connect(dsn) as conn:
        register_vector(conn)
        doc_of: dict[str, str] = {}
        for f in sorted(DOCS.glob("*.md")):
            doc_id = str(uuid.uuid4())
            text = f.read_text()
            conn.execute("INSERT INTO documents (id, tenant_id, title, s3_key) VALUES (%s, 'acme', %s, %s)",
                         (doc_id, text.splitlines()[0].removeprefix("# "), f.name))
            index_document(conn, doc_id, "acme", text, embedder, args.chunk_words)
            doc_of[doc_id] = f.stem
        conn.commit()

        retriever = Retriever(embedder)
        answerer = ExtractiveAnswerer()
        results: dict[str, dict] = {}
        misses: dict[str, list[str]] = {}
        latencies: list[int] = []
        answer_ok = 0
        for mode in ("vector", "keyword", "hybrid"):
            hits_at = {1: 0, 3: 0, 5: 0}
            rr = 0.0
            for q in questions:
                r = retriever.search(conn, "acme", q["q"], k=8, mode=mode)
                latencies.append(r.ms)
                rank = next((i for i, h in enumerate(r.hits, start=1)
                             if doc_of[h.document_id] == q["doc"] and q["phrase"] in h.content), None)
                for k in hits_at:
                    hits_at[k] += rank is not None and rank <= k
                rr += 1.0 / rank if rank else 0.0
                if rank is None or rank > 3:
                    misses.setdefault(mode, []).append(q["q"])
                if mode == "hybrid" and q["phrase"] in answerer.compose(q["q"], r.hits[:6]):
                    answer_ok += 1
            n = len(questions)
            results[mode] = {f"hit@{k}": round(v / n, 3) for k, v in hits_at.items()} | {"mrr": round(rr / n, 3)}

    latencies.sort()
    report = {
        "embedder": embedder.name,
        "chunk_words": args.chunk_words,
        "questions": len(questions),
        "retrieval": results,
        "extractive_answer_contains_phrase": round(answer_ok / len(questions), 3),
        "retrieval_ms_p50": latencies[len(latencies) // 2],
        "retrieval_ms_p95": latencies[int(len(latencies) * 0.95) - 1],
        "misses_at_3": misses,
    }

    print(f"\nembedder={embedder.name} chunk_words={args.chunk_words} questions={len(questions)}\n")
    print("| retrieval | hit@1 | hit@3 | hit@5 | MRR |")
    print("|---|---|---|---|---|")
    for mode, m in results.items():
        print(f"| {mode} | {m['hit@1']:.2f} | {m['hit@3']:.2f} | {m['hit@5']:.2f} | {m['mrr']:.2f} |")
    print(f"\nextractive answer contains the expected phrase: {report['extractive_answer_contains_phrase']:.0%}")
    print(f"retrieval latency p50 {report['retrieval_ms_p50']} ms, p95 {report['retrieval_ms_p95']} ms")
    for mode, qs in misses.items():
        print(f"\n{mode} misses at 3: " + "; ".join(qs))
    if args.out:
        pathlib.Path(args.out).write_text(json.dumps(report, indent=2) + "\n")

    return 0 if results["hybrid"]["hit@3"] >= args.min_hybrid_hit3 else 1


if __name__ == "__main__":
    sys.exit(main())
