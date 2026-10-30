-- Lake tables, created idempotently by the pipeline's first task.
-- Catalog `lake` is the Hive connector over S3 (deploy/trino/catalog/lake.properties).

CREATE SCHEMA IF NOT EXISTS lake.raw WITH (location = 's3://lumen-lake/raw/');
CREATE SCHEMA IF NOT EXISTS lake.curated WITH (location = 's3://lumen-lake/curated/');

-- Raw zone exactly as the gateway writes it: many small NDJSON objects,
-- partitioned by tenant and day.
CREATE TABLE IF NOT EXISTS lake.raw.events_json (
    event_id       varchar,
    tenant_id      varchar,
    event_type     varchar,
    schema_version integer,
    occurred_at    varchar,
    ingested_at    varchar,
    user_id        varchar,
    properties     ROW(
        path varchar, referrer varchar, duration_ms bigint,
        order_id varchar, amount_cents bigint, currency varchar, items integer,
        ticket_id varchar, priority varchar, topic varchar),
    tenant         varchar,
    dt             varchar
) WITH (
    format = 'JSON',
    external_location = 's3://lumen-lake/raw/events',
    partitioned_by = ARRAY['tenant', 'dt']
);

-- Curated zone written by the Spark job: typed, de-duplicated, ZSTD Parquet,
-- one partition per day, sorted by tenant / event type / time inside files.
CREATE TABLE IF NOT EXISTS lake.curated.events (
    event_id       varchar,
    tenant_id      varchar,
    event_type     varchar,
    schema_version integer,
    occurred_at    timestamp(3),
    ingested_at    timestamp(3),
    user_id        varchar,
    path           varchar,
    referrer       varchar,
    duration_ms    bigint,
    order_id       varchar,
    amount_cents   bigint,
    currency       varchar,
    items          integer,
    ticket_id      varchar,
    priority       varchar,
    topic          varchar,
    dt             date
) WITH (
    format = 'PARQUET',
    external_location = 's3://lumen-lake/curated/events',
    partitioned_by = ARRAY['dt']
);

-- Small daily aggregates that dashboards read instead of the event table.
-- Explicit locations keep data files out of the metastore directory.
CREATE TABLE IF NOT EXISTS lake.curated.event_daily_rollup (
    tenant_id  varchar,
    event_type varchar,
    events     bigint,
    users      bigint,
    dt         date
) WITH (
    format = 'PARQUET',
    external_location = 's3://lumen-lake/curated/event_daily_rollup',
    partitioned_by = ARRAY['dt']
);

CREATE TABLE IF NOT EXISTS lake.curated.revenue_daily (
    tenant_id     varchar,
    currency      varchar,
    orders        bigint,
    revenue_cents bigint,
    dt            date
) WITH (
    format = 'PARQUET',
    external_location = 's3://lumen-lake/curated/revenue_daily',
    partitioned_by = ARRAY['dt']
);
