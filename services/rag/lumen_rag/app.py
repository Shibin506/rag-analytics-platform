"""FastAPI app: ask questions (JSON or server-sent events), record feedback,
list documents. The indexing worker runs in a background thread."""

from __future__ import annotations

import hashlib
import json
import logging
import threading
import time
import uuid
from contextlib import asynccontextmanager
from dataclasses import dataclass
from typing import AsyncIterator, Literal

import boto3
from botocore.config import Config as BotoConfig
from fastapi import Depends, FastAPI, Header, HTTPException, Request
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import PlainTextResponse, StreamingResponse
from pgvector.psycopg import register_vector
from prometheus_client import CONTENT_TYPE_LATEST, Counter, Histogram, generate_latest
from psycopg_pool import ConnectionPool
from pydantic import BaseModel, Field
from starlette.concurrency import run_in_threadpool

from .answer import Answerer, ClaudeAnswerer, ExtractiveAnswerer, cited_chunks
from .config import Settings
from .embeddings import make_embedder
from .indexer import run_worker
from .retrieval import Retriever

log = logging.getLogger("lumen_rag")

ASKS = Counter("lumen_rag_asks_total", "Questions answered", ["tenant", "mode"])
ASK_SECONDS = Histogram("lumen_rag_ask_seconds", "End-to-end answer latency", ["mode"],
                        buckets=(0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 4, 8, 16))
RETRIEVAL_SECONDS = Histogram("lumen_rag_retrieval_seconds", "Retrieval latency",
                              buckets=(0.002, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5))
FEEDBACK = Counter("lumen_rag_feedback_total", "Feedback votes", ["vote"])


@dataclass
class Deps:
    settings: Settings
    pool: ConnectionPool
    retriever: Retriever
    answerer: Answerer


class AskIn(BaseModel):
    question: str = Field(min_length=3, max_length=1000)
    top_k: int | None = Field(default=None, ge=1, le=20)


class SourceOut(BaseModel):
    id: str
    chunk_id: int
    document_id: str
    title: str
    content: str
    score: float


class AskOut(BaseModel):
    answer_id: str
    answer: str
    mode: str
    sources: list[SourceOut]
    cited_chunk_ids: list[int]
    retrieval_ms: int
    total_ms: int


class FeedbackIn(BaseModel):
    answer_id: uuid.UUID
    vote: Literal[1, -1]
    comment: str | None = Field(default=None, max_length=2000)


def make_s3_fetcher(s: Settings):
    client = boto3.client(
        "s3", endpoint_url=s.s3_endpoint, aws_access_key_id=s.s3_access_key,
        aws_secret_access_key=s.s3_secret_key, region_name=s.s3_region,
        config=BotoConfig(s3={"addressing_style": "path"}, retries={"max_attempts": 5}),
    )

    def fetch(key: str) -> str:
        return client.get_object(Bucket=s.s3_bucket, Key=key)["Body"].read().decode("utf-8")

    return fetch


def create_app(settings: Settings | None = None, answerer: Answerer | None = None,
               fetch_text=None, start_worker: bool = True) -> FastAPI:
    settings = settings or Settings()

    @asynccontextmanager
    async def lifespan(app: FastAPI):
        pool = ConnectionPool(settings.database_url, min_size=1, max_size=10,
                              configure=register_vector, open=True, timeout=30)
        embedder = make_embedder(settings.embedder)
        chosen = answerer or (ClaudeAnswerer(settings.claude_model, settings.claude_effort)
                              if settings.use_claude else ExtractiveAnswerer())
        app.state.deps = Deps(settings, pool, Retriever(embedder), chosen)
        log.info("rag up: embedder=%s answer_mode=%s", embedder.name, chosen.mode)

        stop = threading.Event()
        worker = None
        if start_worker:
            worker = threading.Thread(
                target=run_worker, daemon=True, name="indexer",
                args=(pool, settings.database_url, fetch_text or make_s3_fetcher(settings), embedder,
                      settings.chunk_words, settings.poll_seconds, stop.is_set),
            )
            worker.start()
        yield
        stop.set()
        if worker:
            worker.join(timeout=settings.poll_seconds + 2)
        pool.close()

    app = FastAPI(title="Lumen RAG", version="0.1.0", lifespan=lifespan)
    app.add_middleware(CORSMiddleware, allow_origins=["*"], allow_methods=["*"], allow_headers=["*"])

    def deps(request: Request) -> Deps:
        return request.app.state.deps

    key_cache: dict[str, tuple[str, float]] = {}

    def tenant(x_api_key: str = Header(default=""), d: Deps = Depends(deps)) -> str:
        if not x_api_key:
            raise HTTPException(401, "missing X-API-Key")
        digest = hashlib.sha256(x_api_key.encode()).hexdigest()
        hit = key_cache.get(digest)
        if hit and hit[1] > time.monotonic():
            return hit[0]
        with d.pool.connection() as conn:
            row = conn.execute("SELECT id FROM tenants WHERE api_key_sha256 = %s", (digest,)).fetchone()
        if not row:
            raise HTTPException(401, "invalid API key")
        key_cache[digest] = (row[0], time.monotonic() + 60)
        return row[0]

    def retrieve(d: Deps, t: str, body: AskIn):
        with d.pool.connection() as conn:
            r = d.retriever.search(conn, t, body.question, body.top_k or d.settings.top_k)
        RETRIEVAL_SECONDS.observe(r.ms / 1000)
        return r

    def save_answer(d: Deps, t: str, question: str, text: str, mode: str, cited: list[int],
                    retrieval_ms: int, total_ms: int) -> str:
        answer_id = str(uuid.uuid4())
        with d.pool.connection() as conn:
            conn.execute(
                """INSERT INTO answers (id, tenant_id, question, answer, mode, chunk_ids, retrieval_ms, total_ms)
                   VALUES (%s, %s, %s, %s, %s, %s, %s, %s)""",
                (answer_id, t, question, text, mode, cited, retrieval_ms, total_ms),
            )
        ASKS.labels(t, mode).inc()
        ASK_SECONDS.labels(mode).observe(total_ms / 1000)
        return answer_id

    def sources(hits) -> list[SourceOut]:
        return [SourceOut(id=f"S{i}", chunk_id=h.chunk_id, document_id=h.document_id, title=h.title,
                          content=h.content, score=round(h.score, 6)) for i, h in enumerate(hits, start=1)]

    @app.get("/healthz")
    def healthz(d: Deps = Depends(deps)):
        with d.pool.connection() as conn:
            conn.execute("SELECT 1")
        return {"status": "ok", "answer_mode": d.answerer.mode}

    @app.get("/metrics")
    def metrics():
        return PlainTextResponse(generate_latest(), media_type=CONTENT_TYPE_LATEST)

    @app.post("/v1/ask", response_model=AskOut)
    async def ask(body: AskIn, t: str = Depends(tenant), d: Deps = Depends(deps)):
        start = time.perf_counter()
        r = await run_in_threadpool(retrieve, d, t, body)
        text = "".join([part async for part in d.answerer.stream(body.question, r.hits)])
        cited = cited_chunks(text, r.hits)
        total = int((time.perf_counter() - start) * 1000)
        answer_id = await run_in_threadpool(save_answer, d, t, body.question, text, d.answerer.mode, cited, r.ms, total)
        return AskOut(answer_id=answer_id, answer=text, mode=d.answerer.mode, sources=sources(r.hits),
                      cited_chunk_ids=cited, retrieval_ms=r.ms, total_ms=total)

    @app.post("/v1/ask/stream")
    async def ask_stream(body: AskIn, t: str = Depends(tenant), d: Deps = Depends(deps)):
        start = time.perf_counter()
        r = await run_in_threadpool(retrieve, d, t, body)

        async def events() -> AsyncIterator[str]:
            yield sse("sources", [s.model_dump() for s in sources(r.hits)])
            parts: list[str] = []
            try:
                async for part in d.answerer.stream(body.question, r.hits):
                    parts.append(part)
                    yield sse("delta", {"text": part})
            except Exception as exc:  # surface model errors to the client
                log.exception("answer failed")
                yield sse("error", {"message": str(exc)})
                return
            text = "".join(parts)
            cited = cited_chunks(text, r.hits)
            total = int((time.perf_counter() - start) * 1000)
            answer_id = await run_in_threadpool(save_answer, d, t, body.question, text, d.answerer.mode,
                                                cited, r.ms, total)
            yield sse("done", {"answer_id": answer_id, "mode": d.answerer.mode, "cited_chunk_ids": cited,
                               "retrieval_ms": r.ms, "total_ms": total})

        return StreamingResponse(events(), media_type="text/event-stream",
                                 headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"})

    @app.post("/v1/feedback", status_code=201)
    def feedback(body: FeedbackIn, t: str = Depends(tenant), d: Deps = Depends(deps)):
        with d.pool.connection() as conn:
            owner = conn.execute("SELECT tenant_id FROM answers WHERE id = %s", (body.answer_id,)).fetchone()
            if not owner or owner[0] != t:
                raise HTTPException(404, "answer not found")
            conn.execute("INSERT INTO feedback (answer_id, vote, comment) VALUES (%s, %s, %s)",
                         (body.answer_id, body.vote, body.comment))
        FEEDBACK.labels("up" if body.vote == 1 else "down").inc()
        return {"status": "recorded"}

    @app.get("/v1/documents")
    def documents(t: str = Depends(tenant), d: Deps = Depends(deps)):
        with d.pool.connection() as conn:
            rows = conn.execute(
                """SELECT id::text, title, status, chunk_count, error, created_at, indexed_at
                   FROM documents WHERE tenant_id = %s ORDER BY created_at DESC LIMIT 200""",
                (t,),
            ).fetchall()
        cols = ["id", "title", "status", "chunk_count", "error", "created_at", "indexed_at"]
        return [dict(zip(cols, r)) for r in rows]

    return app


def sse(event: str, data) -> str:
    return f"event: {event}\ndata: {json.dumps(data, default=str)}\n\n"


def main() -> None:
    import uvicorn

    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
    uvicorn.run(create_app(), host="0.0.0.0", port=8000, log_level="info")


if __name__ == "__main__":
    main()
