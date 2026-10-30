# Dashboards and query performance

## Data freshness

The Overview dashboard shows ingest volume per minute and is up to date within about 10 seconds. Analytical dashboards (daily active users, revenue, event mix) read the curated lake tables, which are rebuilt by the nightly pipeline at 02:00 UTC; today's partial data appears after the next run.

## How the lake is organised

Raw events land as JSON files partitioned by tenant and day. The nightly Spark job compacts each day into Parquet files partitioned by date and sorted by tenant and event type, removes duplicates, and refreshes the daily rollup table. Trino queries the curated tables, so a dashboard that filters on a date range and a tenant reads only the files it needs.

## Making your queries fast

Always filter on `dt` (the event date) so Trino can skip partitions, and select only the columns you need. Prefer the `event_daily_rollup` table for totals by day; it is far smaller than the event table. Avoid `SELECT *` over the raw JSON table except for debugging.

## Exports

Growth and Enterprise plans can export curated tables to their own S3 bucket as Parquet once a day.
