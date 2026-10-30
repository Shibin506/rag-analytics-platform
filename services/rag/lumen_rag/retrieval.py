"""Hybrid retrieval: dense vectors (pgvector, HNSW) plus Postgres full-text
search, fused with reciprocal rank fusion and nudged by user feedback."""

from __future__ import annotations

import math
import time
from dataclasses import dataclass, field

import numpy as np
from psycopg import Connection

from .embeddings import Embedder

RRF_K = 60
CANDIDATES = 30
# How far a chunk's net feedback can move its fused score (±25% at most).
FEEDBACK_WEIGHT = 0.25


@dataclass
class Hit:
    chunk_id: int
    document_id: str
    title: str
    content: str
    score: float
    vector_rank: int | None = None
    keyword_rank: int | None = None
    feedback: int = 0


@dataclass
class Retrieval:
    hits: list[Hit]
    ms: int
    debug: dict = field(default_factory=dict)


def feedback_multiplier(up: int, down: int) -> float:
    """Smoothed and bounded: one vote moves a chunk a little, many votes move it
    up to FEEDBACK_WEIGHT, and it never flips relevance on its own."""
    return 1.0 + FEEDBACK_WEIGHT * math.tanh((up - down) / 3.0)


def fuse(
    vector_ids: list[int],
    keyword_ids: list[int],
    feedback: dict[int, tuple[int, int]] | None = None,
    mode: str = "hybrid",
) -> list[tuple[int, float]]:
    """Reciprocal rank fusion of two ranked id lists.

    mode="vector" or "keyword" uses one list only (for the eval's baselines).
    """
    feedback = feedback or {}
    scores: dict[int, float] = {}
    lists = {"hybrid": (vector_ids, keyword_ids), "vector": (vector_ids,), "keyword": (keyword_ids,)}[mode]
    for ids in lists:
        for rank, cid in enumerate(ids, start=1):
            scores[cid] = scores.get(cid, 0.0) + 1.0 / (RRF_K + rank)
    for cid in scores:
        up, down = feedback.get(cid, (0, 0))
        scores[cid] *= feedback_multiplier(up, down)
    return sorted(scores.items(), key=lambda kv: (-kv[1], kv[0]))


class Retriever:
    def __init__(self, embedder: Embedder) -> None:
        self.embedder = embedder

    def search(self, conn: Connection, tenant: str, question: str, k: int = 6, mode: str = "hybrid") -> Retrieval:
        start = time.perf_counter()
        qvec = self.embedder.embed([question])[0]
        vector_ids = self._vector(conn, tenant, qvec) if mode != "keyword" else []
        keyword_ids = self._keyword(conn, tenant, question) if mode != "vector" else []
        candidates = list(dict.fromkeys(vector_ids + keyword_ids))
        feedback = self._feedback(conn, candidates)
        ranked = fuse(vector_ids, keyword_ids, feedback, mode)[:k]

        rows = {}
        if ranked:
            cur = conn.execute(
                """SELECT c.id, c.document_id::text, d.title, c.content
                   FROM chunks c JOIN documents d ON d.id = c.document_id
                   WHERE c.id = ANY(%s)""",
                ([cid for cid, _ in ranked],),
            )
            rows = {r[0]: r for r in cur.fetchall()}
        vpos = {cid: i + 1 for i, cid in enumerate(vector_ids)}
        kpos = {cid: i + 1 for i, cid in enumerate(keyword_ids)}
        hits = []
        for cid, score in ranked:
            r = rows[cid]
            up, down = feedback.get(cid, (0, 0))
            hits.append(Hit(cid, r[1], r[2], r[3], score, vpos.get(cid), kpos.get(cid), up - down))
        ms = int((time.perf_counter() - start) * 1000)
        return Retrieval(hits, ms, {"vector": len(vector_ids), "keyword": len(keyword_ids)})

    @staticmethod
    def _vector(conn: Connection, tenant: str, qvec: np.ndarray) -> list[int]:
        cur = conn.execute(
            """SELECT id FROM chunks WHERE tenant_id = %s
               ORDER BY embedding <=> %s LIMIT %s""",
            (tenant, qvec, CANDIDATES),
        )
        return [r[0] for r in cur.fetchall()]

    @staticmethod
    def _keyword(conn: Connection, tenant: str, question: str) -> list[int]:
        # OR the question's terms: websearch_to_tsquery would AND them and
        # miss most chunks for natural-language questions.
        cur = conn.execute(
            """WITH q AS (
                   SELECT to_tsquery('english', string_agg(quote_literal(lexeme), ' | ')) AS query
                   FROM unnest(to_tsvector('english', %s))
               )
               SELECT c.id FROM chunks c, q
               WHERE c.tenant_id = %s AND q.query IS NOT NULL AND c.tsv @@ q.query
               ORDER BY ts_rank_cd(c.tsv, q.query, 32) DESC, c.id
               LIMIT %s""",
            (question, tenant, CANDIDATES),
        )
        return [r[0] for r in cur.fetchall()]

    @staticmethod
    def _feedback(conn: Connection, ids: list[int]) -> dict[int, tuple[int, int]]:
        if not ids:
            return {}
        cur = conn.execute("SELECT chunk_id, up, down FROM chunk_feedback WHERE chunk_id = ANY(%s)", (ids,))
        return {r[0]: (int(r[1]), int(r[2])) for r in cur.fetchall()}
