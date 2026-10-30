"""Turns pending documents into searchable chunks.

Workers claim documents with FOR UPDATE SKIP LOCKED, so any number of RAG
replicas can index in parallel without double work. A Postgres NOTIFY from the
ingest gateway wakes the worker immediately; a poll catches anything missed
(for example documents uploaded while no worker was running).
"""

from __future__ import annotations

import logging
import select
import time
from typing import Callable

from psycopg import Connection
from psycopg_pool import ConnectionPool

from .chunking import chunk_text
from .embeddings import Embedder

log = logging.getLogger(__name__)

FetchText = Callable[[str], str]


def claim_next(conn: Connection) -> tuple[str, str, str] | None:
    row = conn.execute(
        """UPDATE documents SET status = 'indexing', error = NULL
           WHERE id = (SELECT id FROM documents WHERE status = 'pending'
                       ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1)
           RETURNING id::text, tenant_id, s3_key"""
    ).fetchone()
    return row  # type: ignore[return-value]


def index_document(conn: Connection, doc_id: str, tenant: str, text: str, embedder: Embedder, chunk_words: int) -> int:
    chunks = chunk_text(text, chunk_words)
    vectors = embedder.embed([c.content for c in chunks]) if chunks else []
    with conn.transaction():
        conn.execute("DELETE FROM chunks WHERE document_id = %s", (doc_id,))
        with conn.cursor() as cur:
            cur.executemany(
                """INSERT INTO chunks (document_id, tenant_id, ordinal, content, embedding)
                   VALUES (%s, %s, %s, %s, %s)""",
                [(doc_id, tenant, c.ordinal, c.content, v) for c, v in zip(chunks, vectors)],
            )
        conn.execute(
            """UPDATE documents SET status = 'indexed', chunk_count = %s, indexed_at = now()
               WHERE id = %s""",
            (len(chunks), doc_id),
        )
    return len(chunks)


def drain(pool: ConnectionPool, fetch_text: FetchText, embedder: Embedder, chunk_words: int) -> int:
    """Index every pending document; returns how many were processed."""
    done = 0
    while True:
        with pool.connection() as conn:
            claimed = claim_next(conn)
            conn.commit()
            if claimed is None:
                return done
            doc_id, tenant, key = claimed
            start = time.perf_counter()
            try:
                n = index_document(conn, doc_id, tenant, fetch_text(key), embedder, chunk_words)
                log.info("indexed document %s: %d chunks in %.0f ms", doc_id, n, (time.perf_counter() - start) * 1000)
            except Exception as exc:  # one bad document must not stop the worker
                conn.rollback()
                conn.execute("UPDATE documents SET status = 'failed', error = %s WHERE id = %s", (str(exc)[:500], doc_id))
                conn.commit()
                log.exception("indexing %s failed", doc_id)
            done += 1


def run_worker(pool: ConnectionPool, dsn: str, fetch_text: FetchText, embedder: Embedder, chunk_words: int,
               poll_seconds: float, stop: Callable[[], bool]) -> None:
    import psycopg

    listener = psycopg.connect(dsn, autocommit=True)
    listener.execute("LISTEN documents_pending")
    try:
        while not stop():
            drain(pool, fetch_text, embedder, chunk_words)
            # Sleep until a NOTIFY arrives or the poll interval passes.
            ready, _, _ = select.select([listener.fileno()], [], [], poll_seconds)
            if ready:
                for _ in listener.notifies(timeout=0):
                    pass
    finally:
        listener.close()
