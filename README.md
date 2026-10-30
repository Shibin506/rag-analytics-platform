<h1 align="center">Lumen</h1>

<p align="center"><b>A RAG analytics and client-integration platform: clients stream events and documents in over gRPC or REST, a Spark + Airflow pipeline turns the raw lake into fast Parquet tables for Trino, and a retrieval-augmented assistant answers questions from the documents with citations.</b></p>

<p align="center">
Go · Java (Spring Boot, GraphQL) · Python (FastAPI, PySpark, Airflow) · TypeScript + React · gRPC · PostgreSQL + pgvector · S3 · Apache Spark · Trino · Apache Airflow · Docker
</p>

![Analytics dashboard](docs/images/analytics-light.png)

## What it does

| | |
|---|---|
| **Ingest** | A Go gateway accepts events over **gRPC** (unary and client-streaming) and **REST**, validates each one against its tenant's **JSON Schema**, and writes valid events to the S3 raw zone as newline-delimited JSON, partitioned by tenant and day. One bad event never blocks the rest of a batch: each rejection comes back with its index and the exact fields that failed. |
| **Schema registry** | A Java **GraphQL** API (Spring for GraphQL) versions every event schema and **refuses breaking changes** (removed fields, type changes, new required fields, narrowed enums, tightened limits). Gateways pick up a new version within 15 seconds, with no deploy. |
| **Lake pipeline** | A nightly **Airflow** DAG runs a **Spark** job that compacts the small raw JSON files into typed, de-duplicated, ZSTD **Parquet** partitioned by day and sorted by tenant / event type / time; then **Trino** registers partitions, rebuilds daily rollups and reconciles row counts between raw, curated and rollup tables. Every step overwrites whole day partitions, so retries and backfills are safe. |
| **Analytics** | The GraphQL API serves dashboards from Trino (partition-pruned queries over the rollups and the curated events table). |
| **RAG assistant** | A Python **FastAPI** service chunks uploaded documents by section, embeds them into **pgvector** (HNSW index) next to a Postgres full-text index, and answers with **hybrid retrieval** (vector + keyword, reciprocal-rank fusion). Answers stream over server-sent events and cite every claim as `[S1]`. Thumbs up/down are stored and **nudge future ranking**. Answers come from **Claude** when `ANTHROPIC_API_KEY` is set, or from a built-in extractive answerer otherwise, so the stack runs with no key at all. |
| **Dashboard** | A **React + TypeScript** app: live ingest telemetry, lake analytics, the assistant, schema editing with compatibility checks, and document upload. Light and dark themes, works on phones. |

## Measured results

Everything below was measured on a 4-vCPU cloud VM with the stack in this repo. Reproduce the numbers with the commands shown.

**Lake queries: raw zone vs. curated zone.** Five dashboard queries over 30 days of data (2.35 M events, two tenants), same partition filters on both sides, median of 7 runs after a warm-up ([`bench/query_bench.py`](bench/query_bench.py), results in [`bench/results/query_bench.json`](bench/results/query_bench.json)):

| Query | Raw JSON | Curated (Parquet + rollups) | Faster | Bytes read |
|---|---:|---:|---:|---:|
| Daily events by type (7 days) | 450 ms | 141 ms | 69% | 78.6 MB → 0.01 MB |
| Revenue by currency (14 days) | 695 ms | 160 ms | 77% | 157.3 MB → 0.01 MB |
| Top pages (7 days) | 476 ms | 194 ms | 59% | 78.6 MB → 18.0 MB |
| Urgent tickets by topic (30 days) | 1,172 ms | 337 ms | 71% | 325.8 MB → 74.7 MB |
| Daily active users (30 days) | 1,216 ms | 360 ms | 70% | 325.8 MB → 74.7 MB |
| **All five** | **4,008 ms** | **1,193 ms** | **70%** | |

Both sides return identical rows (the benchmark asserts it).

**Pipeline.** Compacting those 30 days (677 MB of raw NDJSON in 180 objects) took 73 s with Spark in `local[*]` mode; the quality check reconciled every day exactly (raw distinct events = curated rows = rollup totals).

**Ingest.** One load-generator client streaming over gRPC pushed 2.4 M events through the gateway in 30 s (≈78 k events/s), including JSON Schema validation and S3 writes.

**Retrieval** ([`services/rag/evals`](services/rag/evals)). 26 questions over the 8 sample help-center documents, each with the document and exact phrase that answers it. With the default dependency-free embedder:

| Retrieval | hit@1 | hit@3 | hit@5 | MRR |
|---|---:|---:|---:|---:|
| Vector only | 0.73 | 0.89 | 0.92 | 0.81 |
| Keyword only | 0.73 | 0.92 | 0.92 | 0.81 |
| **Hybrid** | **0.77** | 0.89 | 0.89 | **0.84** |

Retrieval latency p50 1 ms, p95 2 ms. The extractive answer contains the expected phrase for 85% of questions. CI fails if hybrid hit@3 drops below 0.85. A neural embedder (`EMBEDDER=fastembed`, BAAI/bge-small-en-v1.5) is supported but was not measured here.

**Tests.** 59 tests: Go 13 (race detector on), Java 26 (including GraphQL end to end against Postgres), Python 15 (API end to end against pgvector), Spark 2, web 3.

## Architecture

```mermaid
flowchart LR
    C((Client apps)) -->|gRPC / REST<br/>events + documents| G["Ingest gateway<br/>Go"]
    G -->|NDJSON, tenant/day partitions| RAW[("S3 raw zone")]
    G -->|telemetry, document rows| PG[("PostgreSQL<br/>+ pgvector")]
    PG -. schemas every 15 s .-> G

    subgraph Nightly["Airflow DAG · 02:00 UTC"]
        SP["Spark compaction"] --> TR1["Trino: partitions,<br/>rollups, quality checks"]
    end
    RAW --> SP --> CUR[("S3 curated zone<br/>Parquet")]
    TR1 --> CUR

    PG -->|NOTIFY| R["RAG service<br/>Python / FastAPI"]
    RAW -. document text .-> R
    R -->|chunks + embeddings| PG
    R <-->|optional| CL["Claude API"]

    CAT["Catalog API<br/>Java / GraphQL"] --> PG
    CAT -->|SQL| T["Trino"] --> CUR
    W["Web dashboard<br/>React + TypeScript"] --> CAT & R & G
```

| Service | Language | Port | What it owns |
|---|---|---|---|
| [`services/ingest`](services/ingest) | Go | 9090 gRPC, 8080 REST | Auth, validation, S3 raw zone, ingest telemetry, load generator |
| [`services/catalog`](services/catalog) | Java 21, Spring Boot | 8081 (`/graphql`, `/graphiql`) | Database migrations (Flyway), schema registry, telemetry and analytics queries |
| [`services/rag`](services/rag) | Python 3.11, FastAPI | 8000 | Indexing worker, hybrid retrieval, answers, feedback, eval |
| [`pipelines`](pipelines) | PySpark, Airflow, Trino SQL | 8089 (Airflow UI) | Nightly compaction, rollups, quality checks, backfills |
| [`web`](web) | TypeScript, React | 3000 | Dashboard |
| Trino · SeaweedFS (S3) · Postgres | | 8088 · 8333 · 5432 | Infrastructure |

The gRPC contract is in [`proto/`](proto/lumen/ingest/v1/ingest.proto); the database schema in [`services/catalog/src/main/resources/db/migration`](services/catalog/src/main/resources/db/migration); the lake tables in [`pipelines/trino/tables.sql`](pipelines/trino/tables.sql).

## Run it

Requirements: Docker with Compose, about 8 GB of free disk.

```bash
git clone https://github.com/Shibin506/rag-analytics-platform.git
cd rag-analytics-platform
docker compose up -d --build
```

Send 14 days of synthetic events (two tenants, ~2% deliberately invalid) and upload the sample documents:

```bash
docker compose --profile demo run --rm loadgen
```

Build the lake for those 14 days (the nightly DAG does the same for one day at a time):

```bash
docker compose exec airflow /opt/lumen/pipelines/backfill.sh
```

Then open:

- **Dashboard** — http://localhost:3000
- **GraphiQL** — http://localhost:8081/graphiql (send header `X-API-Key: lumen_acme_demo_key`)
- **Airflow** — http://localhost:8089 (admin / admin); trigger `lumen_nightly`
- **Trino UI** — http://localhost:8088

To answer with Claude instead of the extractive answerer, put `ANTHROPIC_API_KEY=...` in a `.env` file next to `docker-compose.yml` and run `docker compose up -d rag`.

Demo API keys: `lumen_acme_demo_key` (tenant `acme`) and `lumen_globex_demo_key` (tenant `globex`).

### Try the APIs

```bash
# Send an event over REST
curl -s localhost:8080/v1/events -H 'X-API-Key: lumen_acme_demo_key' -d '{"events":[{
  "event_id":"8d3f7c62-5b1e-4f7e-9a51-1f0c2b3d4e5f","event_type":"order.placed",
  "occurred_at":"'"$(date -u +%FT%TZ)"'","properties":{"order_id":"o1","amount_cents":"12.50","currency":"usd"}}]}'
# -> {"accepted":0,"rejected":[{"index":0,"event_id":"8d3f…","reason":"schema v1: /amount_cents: got string,
#     want integer; /currency: 'usd' does not match pattern '^[A-Z]{3}$'"}]}

# Ask the documents
curl -s localhost:8000/v1/ask -H 'X-API-Key: lumen_acme_demo_key' -H 'Content-Type: application/json' \
  -d '{"question":"Do you refund monthly plans?"}'
```

```graphql
# Register a new schema version (rejected if it breaks compatibility)
mutation {
  registerSchema(eventType: "support.ticket", schema: "{...}") { ok version breakingChanges }
}
```

## Development

| | Command |
|---|---|
| Go | `cd services/ingest && go test -race ./...` |
| Java | `cd services/catalog && TEST_JDBC_URL=jdbc:postgresql://localhost:5432/<empty db> mvn test` |
| Python | `cd services/rag && pip install -e ".[dev]" && TEST_DATABASE_URL=postgresql://lumen:lumen@localhost:5432/lumen pytest` |
| Retrieval eval | `cd services/rag && TEST_DATABASE_URL=... python -m evals.run_eval` |
| Spark | `cd pipelines && pytest tests` |
| Web | `cd web && npm ci && npm test && npm run dev` |
| Query benchmark | `TRINO_PORT=8088 python bench/query_bench.py` |
| Regenerate gRPC code | `python -m grpc_tools.protoc -Iproto --go_out=services/ingest/gen --go_opt=module=github.com/Shibin506/rag-analytics-platform/services/ingest/gen --go-grpc_out=services/ingest/gen --go-grpc_opt=module=github.com/Shibin506/rag-analytics-platform/services/ingest/gen proto/lumen/ingest/v1/ingest.proto` |

CI ([`.github/workflows/ci.yml`](.github/workflows/ci.yml)) runs every suite, the retrieval eval gate, builds every image and checks that the Airflow DAG imports.

## Design notes

- **The partition is the event's day, not the arrival day.** Late events land where they belong, and the DAG reprocesses the previous day too, so they are picked up.
- **Idempotent everywhere.** Spark overwrites one day's partition; Trino rollups use `OVERWRITE` for the days refreshed; duplicates from client retries are removed by `(tenant_id, event_id)`, keeping the first ingested copy. Re-running any day changes nothing.
- **Quality checks fail loudly.** The last DAG task reconciles raw distinct events, curated rows, null keys, duplicate IDs and rollup totals per day, and fails the run (without retrying) on any mismatch.
- **No rename-based commits on S3.** Spark writes each day to local disk and publishes the finished files with plain uploads. On a cluster against AWS S3 use `--commit direct` with the S3A magic committer instead.
- **Schema compatibility is checked on the server.** Concurrent registrations are serialised with a Postgres advisory lock, so two writers never get the same version.
- **Feedback is bounded.** Votes can move a chunk's fused score by at most ±25% (smoothed with `tanh`), so a few votes help but cannot override relevance.
- **Model answers are grounded and checkable.** The model sees only the retrieved excerpts, must cite `[S#]` for every fact, and uses Anthropic's server-side fallback if a request is declined. The extractive mode keeps the stack usable, and testable in CI, without a key.

## Author

Built by **Shibin Biji Thomas** ([GitHub](https://github.com/Shibin506) · [LinkedIn](https://www.linkedin.com/in/shibin-biji-593228183/)).

The help-center documents in [`samples/docs`](samples/docs) describe a fictional company and exist only to exercise the assistant.

MIT License.
