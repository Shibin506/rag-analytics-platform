"""Spark transformation tests (local Spark, no S3)."""

import datetime as dt
import json

import pytest

pyspark = pytest.importorskip("pyspark")

from pyspark.sql import SparkSession  # noqa: E402

from spark.compact_events import CURATED_COLUMNS, RAW_SCHEMA, transform  # noqa: E402


@pytest.fixture(scope="module")
def spark():
    s = (SparkSession.builder.master("local[2]").appName("test")
         .config("spark.sql.session.timeZone", "UTC").config("spark.ui.enabled", "false").getOrCreate())
    yield s
    s.stop()


def rows(spark, records):
    lines = [json.dumps(r) for r in records]
    return spark.read.schema(RAW_SCHEMA).json(spark.sparkContext.parallelize(lines))


def ev(eid, tenant="acme", typ="order.placed", at="2026-10-29T23:59:00Z", ingested="2026-10-30T00:00:01Z", **props):
    return {"event_id": eid, "tenant_id": tenant, "event_type": typ, "schema_version": 1,
            "occurred_at": at, "ingested_at": ingested, "user_id": "u1", "properties": props}


def test_flattens_types_and_partitions_by_event_day(spark):
    out = transform(rows(spark, [
        ev("e1", amount_cents=1250, currency="USD", items=2, order_id="o1"),
        ev("e2", typ="page.view", at="2026-10-30T00:00:05+00:00", path="/pricing", duration_ms=900),
    ])).orderBy("event_id").collect()
    assert [c for c in out[0].asDict()] == CURATED_COLUMNS + ["dt"]
    assert out[0]["amount_cents"] == 1250 and out[0]["currency"] == "USD" and out[0]["path"] is None
    assert out[0]["dt"] == dt.date(2026, 10, 29)
    assert out[1]["dt"] == dt.date(2026, 10, 30) and out[1]["path"] == "/pricing"
    assert out[1]["occurred_at"] == dt.datetime(2026, 10, 30, 0, 0, 5)


def test_removes_retried_duplicates_keeping_first_ingested(spark):
    out = transform(rows(spark, [
        ev("dup", ingested="2026-10-30T00:00:09Z", order_id="second"),
        ev("dup", ingested="2026-10-30T00:00:01Z", order_id="first"),
        ev("dup", tenant="globex", order_id="other-tenant"),  # same id, different tenant: kept
        ev("solo", order_id="solo"),
    ])).collect()
    by = {(r["tenant_id"], r["event_id"]): r["order_id"] for r in out}
    assert len(out) == 3
    assert by[("acme", "dup")] == "first"
    assert by[("globex", "dup")] == "other-tenant"
