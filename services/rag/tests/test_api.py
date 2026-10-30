"""End-to-end tests of the API against a real Postgres with pgvector.

Set TEST_DATABASE_URL to an admin connection string (the test creates and
drops its own database); skipped otherwise.
"""

import os
import pathlib
import time
import uuid

import psycopg
import pytest
from fastapi.testclient import TestClient

from lumen_rag.app import create_app
from lumen_rag.config import Settings
from lumen_rag.retrieval import feedback_multiplier
from lumen_rag.testdb import fresh_database

ADMIN_DSN = os.environ.get("TEST_DATABASE_URL")
pytestmark = pytest.mark.skipif(not ADMIN_DSN, reason="TEST_DATABASE_URL not set")

DOCS = pathlib.Path(__file__).resolve().parents[3] / "samples" / "docs"
ACME = {"X-API-Key": "lumen_acme_demo_key"}
GLOBEX = {"X-API-Key": "lumen_globex_demo_key"}


@pytest.fixture(scope="module")
def client():
    with fresh_database(ADMIN_DSN) as dsn:
        texts = {}
        with psycopg.connect(dsn, autocommit=True) as conn:
            for f in sorted(DOCS.glob("*.md")):
                doc_id, key = str(uuid.uuid4()), f"docs/acme/{f.stem}.txt"
                texts[key] = f.read_text()
                title = texts[key].splitlines()[0].removeprefix("# ")
                conn.execute("INSERT INTO documents (id, tenant_id, title, s3_key) VALUES (%s, 'acme', %s, %s)",
                             (doc_id, title, key))
        os.environ["ANSWER_MODE"] = "extractive"
        settings = Settings(database_url=dsn, poll_seconds=0.2)
        with TestClient(create_app(settings, fetch_text=texts.__getitem__)) as c:
            deadline = time.time() + 30
            while time.time() < deadline:
                docs = c.get("/v1/documents", headers=ACME).json()
                if docs and all(d["status"] == "indexed" for d in docs):
                    break
                time.sleep(0.2)
            yield c


def test_documents_are_indexed(client):
    docs = client.get("/v1/documents", headers=ACME).json()
    assert len(docs) == 8
    assert all(d["status"] == "indexed" and d["chunk_count"] > 0 for d in docs)


def test_auth_is_required_and_tenants_are_isolated(client):
    assert client.post("/v1/ask", json={"question": "refunds?"}).status_code == 401
    assert client.post("/v1/ask", json={"question": "refunds?"}, headers={"X-API-Key": "x"}).status_code == 401
    other = client.post("/v1/ask", json={"question": "Do you refund monthly plans?"}, headers=GLOBEX).json()
    assert other["sources"] == []  # globex has no documents


def test_ask_answers_with_citations(client):
    r = client.post("/v1/ask", json={"question": "Do you refund monthly plans when I cancel?"}, headers=ACME)
    assert r.status_code == 200
    body = r.json()
    assert body["mode"] == "extractive"
    assert "Monthly plans are not refunded" in body["answer"]
    assert body["cited_chunk_ids"], body
    assert body["sources"][0]["title"] == "Billing and plans"


def test_streaming_emits_sources_deltas_and_done(client):
    with client.stream("POST", "/v1/ask/stream", json={"question": "What is the batch limit?"}, headers=ACME) as r:
        text = "".join(r.iter_text())
    events = [line.split(": ", 1)[1] for line in text.splitlines() if line.startswith("event: ")]
    assert events[0] == "sources" and events[-1] == "done" and "delta" in events


def test_feedback_is_scoped_to_the_answering_tenant(client):
    answer = client.post("/v1/ask", json={"question": "How long are raw events retained?"}, headers=ACME).json()
    assert client.post("/v1/feedback", json={"answer_id": answer["answer_id"], "vote": 1}, headers=GLOBEX).status_code == 404
    assert client.post("/v1/feedback", json={"answer_id": answer["answer_id"], "vote": 2}, headers=ACME).status_code == 422
    r = client.post("/v1/feedback", json={"answer_id": answer["answer_id"], "vote": 1, "comment": "spot on"}, headers=ACME)
    assert r.status_code == 201


def test_feedback_changes_ranking(client):
    q = {"question": "What happens after a cancellation?"}
    before = client.post("/v1/ask", json=q, headers=ACME).json()["sources"]
    boost = feedback_multiplier(6, 0)
    # Pick a source close enough to the one above it that a bounded boost can
    # overtake it.
    i = next(i for i in range(1, len(before)) if before[i]["score"] * boost > before[i - 1]["score"])
    target = before[i]["chunk_id"]
    # Users keep up-voting answers that cited it.
    with psycopg.connect(client.app.state.deps.settings.database_url, autocommit=True) as conn:
        for _ in range(6):
            aid = str(uuid.uuid4())
            conn.execute("""INSERT INTO answers (id, tenant_id, question, answer, mode, chunk_ids, retrieval_ms, total_ms)
                            VALUES (%s, 'acme', 'q', 'a', 'test', %s, 1, 1)""", (aid, [target]))
            conn.execute("INSERT INTO feedback (answer_id, vote) VALUES (%s, 1)", (aid,))
    after = client.post("/v1/ask", json=q, headers=ACME).json()["sources"]
    new_rank = [s["chunk_id"] for s in after].index(target)
    assert new_rank < i
    assert after[new_rank]["score"] == pytest.approx(before[i]["score"] * boost, rel=1e-4)


def test_metrics_and_health(client):
    assert client.get("/healthz").json()["status"] == "ok"
    m = client.get("/metrics").text
    assert "lumen_rag_asks_total" in m and "lumen_rag_retrieval_seconds_bucket" in m
