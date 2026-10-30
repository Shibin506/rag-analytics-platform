"""Trino steps of the nightly pipeline. Plain functions, so the Airflow DAG,
the CLI below and the tests all run the same code.

    python -m lumen_pipeline.trino_tasks setup
    python -m lumen_pipeline.trino_tasks refresh --start 2026-10-01 --end 2026-10-30
"""

from __future__ import annotations

import argparse
import datetime as dt
import os
import pathlib
import re
import sys
from dataclasses import dataclass

import trino

TABLES_SQL = pathlib.Path(__file__).resolve().parents[1] / "trino" / "tables.sql"


def connect(session: dict[str, str] | None = None) -> trino.dbapi.Connection:
    return trino.dbapi.connect(
        host=os.environ.get("TRINO_HOST", "localhost"),
        port=int(os.environ.get("TRINO_PORT", "8088")),
        user="lumen-pipeline",
        catalog="lake",
        schema="curated",
        session_properties=session,
    )


def run(conn, sql: str, params: tuple | None = None) -> list[tuple]:
    cur = conn.cursor()
    cur.execute(sql, params) if params else cur.execute(sql)
    return cur.fetchall()


def statements(sql: str) -> list[str]:
    no_comments = re.sub(r"--[^\n]*", "", sql)
    return [s.strip() for s in no_comments.split(";") if s.strip()]


def setup(conn) -> None:
    """Create schemas and tables (idempotent)."""
    for stmt in statements(TABLES_SQL.read_text()):
        run(conn, stmt)


def sync_partitions(conn) -> None:
    """Register partitions that the gateway and Spark added as plain files."""
    run(conn, "CALL lake.system.sync_partition_metadata('raw', 'events_json', 'FULL')")
    run(conn, "CALL lake.system.sync_partition_metadata('curated', 'events', 'FULL')")


def refresh_rollups(start: dt.date, end: dt.date) -> dict[str, int]:
    """Rebuild the daily rollups for [start, end]. Overwrites only those days'
    partitions, so a re-run (or a backfill) never double counts."""
    conn = connect({"lake.insert_existing_partitions_behavior": "OVERWRITE"})
    params = (start, end)
    events = run(conn, """
        INSERT INTO lake.curated.event_daily_rollup
        SELECT tenant_id, event_type, count(*), approx_distinct(user_id), dt
        FROM lake.curated.events
        WHERE dt BETWEEN ? AND ?
        GROUP BY tenant_id, event_type, dt""", params)
    revenue = run(conn, """
        INSERT INTO lake.curated.revenue_daily
        SELECT tenant_id, currency, count(*), sum(amount_cents), dt
        FROM lake.curated.events
        WHERE dt BETWEEN ? AND ? AND event_type = 'order.placed'
        GROUP BY tenant_id, currency, dt""", params)
    return {"event_daily_rollup": events[0][0], "revenue_daily": revenue[0][0]}


@dataclass
class QualityReport:
    day: dt.date
    raw_distinct: int
    curated: int
    curated_duplicates: int
    null_keys: int
    rollup_total: int

    @property
    def problems(self) -> list[str]:
        out = []
        if self.curated != self.raw_distinct:
            out.append(f"curated has {self.curated} rows but raw has {self.raw_distinct} distinct events")
        if self.curated_duplicates:
            out.append(f"{self.curated_duplicates} duplicate event ids in curated")
        if self.null_keys:
            out.append(f"{self.null_keys} curated rows with null keys")
        if self.rollup_total != self.curated:
            out.append(f"rollup counts {self.rollup_total} events, curated has {self.curated}")
        return out


def quality_check(day: dt.date) -> QualityReport:
    """Reconcile raw, curated and rollup counts for one day."""
    conn = connect()
    d = day.isoformat()
    raw = run(conn, """
        SELECT count(DISTINCT (tenant_id, event_id)) FROM lake.raw.events_json
        WHERE dt = ? AND date(from_iso8601_timestamp(occurred_at)) = ?""", (d, day))[0][0]
    curated, dupes, nulls = run(conn, """
        SELECT count(*), count(*) - count(DISTINCT (tenant_id, event_id)),
               count_if(event_id IS NULL OR tenant_id IS NULL OR occurred_at IS NULL)
        FROM lake.curated.events WHERE dt = ?""", (day,))[0]
    rollup = run(conn, "SELECT coalesce(sum(events), 0) FROM lake.curated.event_daily_rollup WHERE dt = ?",
                 (day,))[0][0]
    return QualityReport(day, raw, curated, dupes, nulls, rollup)


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("command", choices=["setup", "sync", "refresh", "check"])
    yesterday = dt.date.today() - dt.timedelta(days=1)
    ap.add_argument("--start", type=dt.date.fromisoformat, default=yesterday)
    ap.add_argument("--end", type=dt.date.fromisoformat, default=None)
    a = ap.parse_args(argv)
    end = a.end or a.start
    if a.command == "setup":
        setup(connect())
    elif a.command == "sync":
        sync_partitions(connect())
    elif a.command == "refresh":
        print(refresh_rollups(a.start, end))
    else:
        failed = False
        day = a.start
        while day <= end:
            r = quality_check(day)
            print(day, "OK" if not r.problems else r.problems)
            failed |= bool(r.problems)
            day += dt.timedelta(days=1)
        return 1 if failed else 0
    return 0


if __name__ == "__main__":
    sys.exit(main())
