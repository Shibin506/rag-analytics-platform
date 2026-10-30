"""Compact the raw zone into the curated events table.

Raw zone (written by the ingest gateway, many small NDJSON objects):
    s3a://lumen-lake/raw/events/tenant=<t>/dt=<day>/<uuid>.json
Curated zone (this job, a few large Parquet files per day):
    s3a://lumen-lake/curated/events/dt=<day>/part-*.zstd.parquet

For each day the job:
  1. reads every tenant's raw files with an explicit schema (no inference pass),
  2. flattens the event properties into typed columns,
  3. removes duplicate deliveries of the same event (client retries), keeping
     the first ingested copy,
  4. sorts by tenant, event type and time, so Parquet min/max statistics let
     Trino skip row groups for tenant- and type-filtered queries,
  5. overwrites only that day's partition, so re-running a day is idempotent.

Committing: Hadoop's default committer "renames" task output into place, and
on S3 a rename is a copy plus a delete (slow, and not supported by every
S3-compatible store). By default the job writes each day to local disk and
then publishes the finished files with plain uploads, deleting the old
partition first. That suits single-node (local[*]) runs like this one; on a
cluster pointed at AWS S3, pass --commit direct and configure the S3A magic
committer instead.

    spark-submit compact_events.py --start 2026-10-01 --end 2026-10-30
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import sys
import tempfile

from pyspark.sql import DataFrame, SparkSession, Window
from pyspark.sql import functions as F
from pyspark.sql import types as T

PROPERTIES = T.StructType([
    T.StructField("path", T.StringType()),
    T.StructField("referrer", T.StringType()),
    T.StructField("duration_ms", T.LongType()),
    T.StructField("order_id", T.StringType()),
    T.StructField("amount_cents", T.LongType()),
    T.StructField("currency", T.StringType()),
    T.StructField("items", T.IntegerType()),
    T.StructField("ticket_id", T.StringType()),
    T.StructField("priority", T.StringType()),
    T.StructField("topic", T.StringType()),
])

RAW_SCHEMA = T.StructType([
    T.StructField("event_id", T.StringType(), False),
    T.StructField("tenant_id", T.StringType(), False),
    T.StructField("event_type", T.StringType(), False),
    T.StructField("schema_version", T.IntegerType()),
    T.StructField("occurred_at", T.StringType(), False),
    T.StructField("ingested_at", T.StringType(), False),
    T.StructField("user_id", T.StringType()),
    T.StructField("properties", PROPERTIES),
])

# Column order of lake.curated.events (pipelines/trino/tables.sql).
CURATED_COLUMNS = [
    "event_id", "tenant_id", "event_type", "schema_version", "occurred_at", "ingested_at", "user_id",
    "path", "referrer", "duration_ms", "order_id", "amount_cents", "currency", "items",
    "ticket_id", "priority", "topic",
]


def spark_session(app: str = "lumen-compact-events") -> SparkSession:
    endpoint = os.environ.get("S3_ENDPOINT_URL", "http://localhost:8333")
    b = (
        SparkSession.builder.appName(app)
        .config("spark.sql.session.timeZone", "UTC")
        .config("spark.sql.sources.partitionOverwriteMode", "dynamic")
        .config("spark.sql.parquet.compression.codec", "zstd")
        .config("spark.sql.parquet.outputTimestampType", "TIMESTAMP_MICROS")
        .config("spark.sql.shuffle.partitions", os.environ.get("SPARK_SHUFFLE_PARTITIONS", "8"))
        .config("spark.hadoop.fs.s3a.endpoint", endpoint)
        .config("spark.hadoop.fs.s3a.path.style.access", "true")
        .config("spark.hadoop.fs.s3a.connection.ssl.enabled", str(endpoint.startswith("https")).lower())
        .config("spark.hadoop.fs.s3a.access.key", os.environ.get("S3_ACCESS_KEY", "lumen"))
        .config("spark.hadoop.fs.s3a.secret.key", os.environ.get("S3_SECRET_KEY", "lumen-secret"))
        .config("spark.hadoop.fs.s3a.impl", "org.apache.hadoop.fs.s3a.S3AFileSystem")
        .config("spark.hadoop.fs.s3a.aws.credentials.provider",
                "org.apache.hadoop.fs.s3a.SimpleAWSCredentialsProvider")
        # Write straight to the destination: S3-compatible stores make the
        # default rename-based commit slow.
        .config("spark.hadoop.mapreduce.fileoutputcommitter.algorithm.version", "2")
    )
    if jars := os.environ.get("SPARK_EXTRA_JARS"):
        b = b.config("spark.jars", jars)
    return b.getOrCreate()


def transform(raw: DataFrame) -> DataFrame:
    """Pure transformation (unit-tested without S3)."""
    flat = raw.select(
        "event_id", "tenant_id", "event_type", "schema_version",
        F.to_timestamp("occurred_at").alias("occurred_at"),
        F.to_timestamp("ingested_at").alias("ingested_at"),
        "user_id",
        *[F.col(f"properties.{f.name}").alias(f.name) for f in PROPERTIES.fields],
    )
    first = Window.partitionBy("tenant_id", "event_id").orderBy(F.col("ingested_at").asc(), F.col("occurred_at").asc())
    deduped = flat.withColumn("_n", F.row_number().over(first)).where("_n = 1").drop("_n")
    return (
        deduped.withColumn("dt", F.to_date("occurred_at"))
        .select(*CURATED_COLUMNS, "dt")
    )


def publish(spark: SparkSession, local_dir: str, dest_uri: str) -> int:
    """Replace dest_uri's contents with the Parquet files in local_dir."""
    jvm = spark.sparkContext._jvm
    conf = spark.sparkContext._jsc.hadoopConfiguration()
    Path = jvm.org.apache.hadoop.fs.Path
    dest = Path(dest_uri)
    fs = dest.getFileSystem(conf)
    if fs.exists(dest):
        fs.delete(dest, True)
    files = sorted(f for f in os.listdir(local_dir) if f.endswith(".parquet"))
    for name in files:
        fs.copyFromLocalFile(False, True, Path("file://" + os.path.join(local_dir, name)), Path(dest, name))
    return len(files)


def compact_day(spark: SparkSession, raw_root: str, out_root: str, day: dt.date, rows_per_file: int,
                commit: str = "stage") -> dict:
    pattern = f"{raw_root}/tenant=*/dt={day.isoformat()}/*.json"
    try:
        raw = spark.read.schema(RAW_SCHEMA).option("mode", "DROPMALFORMED").json(pattern)
    except Exception as exc:  # AnalysisException when the day has no files yet
        if "Path does not exist" in str(exc):
            return {"dt": day.isoformat(), "raw_rows": 0, "curated_rows": 0, "skipped": True}
        raise
    raw = raw.cache()
    raw_rows = raw.count()
    curated = transform(raw).where(F.col("dt") == F.lit(day))
    curated = curated.cache()
    out_rows = curated.count()
    files = max(1, -(-out_rows // rows_per_file))
    ordered = (
        curated.repartition(files, "tenant_id")
        .sortWithinPartitions("tenant_id", "event_type", "occurred_at")
        .drop("dt")  # the partition value lives in the path
    )
    dest = f"{out_root}/dt={day.isoformat()}"
    if commit == "direct":
        ordered.write.mode("overwrite").parquet(dest)
    else:
        with tempfile.TemporaryDirectory(prefix="lumen-compact-") as tmp:
            ordered.write.mode("overwrite").parquet("file://" + tmp + "/out")
            files = publish(spark, tmp + "/out", dest)
    per_tenant = {r["tenant_id"]: r["count"] for r in curated.groupBy("tenant_id").count().collect()}
    raw.unpersist()
    curated.unpersist()
    return {"dt": day.isoformat(), "raw_rows": raw_rows, "curated_rows": out_rows,
            "duplicates_removed": raw_rows - out_rows, "files": files, "per_tenant": per_tenant}


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser()
    yesterday = dt.date.today() - dt.timedelta(days=1)
    ap.add_argument("--start", type=dt.date.fromisoformat, default=yesterday)
    ap.add_argument("--end", type=dt.date.fromisoformat, default=None)
    ap.add_argument("--raw", default="s3a://lumen-lake/raw/events")
    ap.add_argument("--out", default="s3a://lumen-lake/curated/events")
    ap.add_argument("--rows-per-file", type=int, default=2_000_000)
    ap.add_argument("--commit", choices=["stage", "direct"], default="stage")
    args = ap.parse_args(argv)
    end = args.end or args.start

    spark = spark_session()
    day = args.start
    total = 0
    while day <= end:
        report = compact_day(spark, args.raw, args.out, day, args.rows_per_file, args.commit)
        total += report["curated_rows"]
        print("QUALITY " + json.dumps(report), flush=True)
        day += dt.timedelta(days=1)
    spark.stop()
    print(f"compacted {total} events", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
