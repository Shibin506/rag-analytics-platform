-- Core schema shared by every Lumen service. The catalog service applies it with Flyway;
-- the Go ingest gateway and the Python RAG service only read and write rows.

CREATE EXTENSION IF NOT EXISTS vector;

-- A tenant is one client integration. Clients authenticate with an API key; only its
-- SHA-256 is stored.
CREATE TABLE tenants (
    id              text PRIMARY KEY CHECK (id ~ '^[a-z][a-z0-9_]{1,31}$'),
    name            text        NOT NULL,
    api_key_sha256  text        NOT NULL UNIQUE,
    created_at      timestamptz NOT NULL DEFAULT now()
);

-- Schema registry: a JSON Schema for the `properties` of each event type, versioned.
-- The ingest gateway validates every event against the latest version.
CREATE TABLE event_schemas (
    id          bigserial PRIMARY KEY,
    tenant_id   text        NOT NULL REFERENCES tenants (id),
    event_type  text        NOT NULL CHECK (event_type ~ '^[a-z][a-z0-9_.]{1,63}$'),
    version     integer     NOT NULL CHECK (version > 0),
    schema      jsonb       NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, event_type, version)
);

-- Per-minute ingest telemetry written by the gateway (upserted, so many gateway
-- replicas can add to the same row).
CREATE TABLE ingest_stats (
    tenant_id   text        NOT NULL REFERENCES tenants (id),
    minute      timestamptz NOT NULL,
    event_type  text        NOT NULL,
    accepted    bigint      NOT NULL DEFAULT 0,
    rejected    bigint      NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, minute, event_type)
);

-- Documents uploaded for retrieval. The raw text lives in S3; the RAG worker moves a
-- document from pending to indexed.
CREATE TABLE documents (
    id           uuid        PRIMARY KEY,
    tenant_id    text        NOT NULL REFERENCES tenants (id),
    title        text        NOT NULL,
    s3_key       text        NOT NULL,
    status       text        NOT NULL DEFAULT 'pending'
                             CHECK (status IN ('pending', 'indexing', 'indexed', 'failed')),
    error        text,
    chunk_count  integer     NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    indexed_at   timestamptz
);
CREATE INDEX documents_pending ON documents (created_at) WHERE status = 'pending';

-- Chunks with a dense embedding (pgvector) and a full-text vector, for hybrid retrieval.
CREATE TABLE chunks (
    id           bigserial PRIMARY KEY,
    document_id  uuid    NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    tenant_id    text    NOT NULL REFERENCES tenants (id),
    ordinal      integer NOT NULL,
    content      text    NOT NULL,
    embedding    vector(384) NOT NULL,
    tsv          tsvector GENERATED ALWAYS AS (to_tsvector('english', content)) STORED,
    UNIQUE (document_id, ordinal)
);
CREATE INDEX chunks_embedding_hnsw ON chunks USING hnsw (embedding vector_cosine_ops);
CREATE INDEX chunks_tsv ON chunks USING gin (tsv);
CREATE INDEX chunks_tenant ON chunks (tenant_id);

-- Every answer the RAG service gives, with its latency and the chunks it cited.
CREATE TABLE answers (
    id            uuid        PRIMARY KEY,
    tenant_id     text        NOT NULL REFERENCES tenants (id),
    question      text        NOT NULL,
    answer        text        NOT NULL,
    mode          text        NOT NULL,
    chunk_ids     bigint[]    NOT NULL,
    retrieval_ms  integer     NOT NULL,
    total_ms      integer     NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX answers_tenant_time ON answers (tenant_id, created_at DESC);

-- Thumbs up / down from users. Feeds back into retrieval ranking (see chunk_feedback).
CREATE TABLE feedback (
    id          bigserial PRIMARY KEY,
    answer_id   uuid        NOT NULL REFERENCES answers (id) ON DELETE CASCADE,
    vote        smallint    NOT NULL CHECK (vote IN (-1, 1)),
    comment     text,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- Net votes per chunk across every answer that cited it.
CREATE VIEW chunk_feedback AS
SELECT c.chunk_id,
       count(*) FILTER (WHERE f.vote = 1)  AS up,
       count(*) FILTER (WHERE f.vote = -1) AS down
FROM feedback f
JOIN answers a ON a.id = f.answer_id
CROSS JOIN LATERAL unnest(a.chunk_ids) AS c(chunk_id)
GROUP BY c.chunk_id;
