"""Benchmark the dashboard queries on the raw zone versus the curated zone.

Both sides get the same partition filters (tenant and day), so the difference
measured is what the pipeline adds: typed columnar Parquet instead of many
small JSON files, de-duplication, sort order inside files, and pre-computed
daily rollups.

Each query runs once to warm up, then --runs times; we report the median
wall time and the bytes Trino read from storage.

    TRINO_HOST=localhost TRINO_PORT=8088 python bench/query_bench.py --runs 7
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import pathlib
import statistics
import time

import trino

TODAY = dt.date.today()


def d(days_ago: int) -> dt.date:
    return TODAY - dt.timedelta(days=days_ago)


# name -> (raw SQL, curated SQL). {start}/{end} are filled per query.
QUERIES = {
    "Daily events by type (7 days)": (
        """SELECT dt, event_type, count(*) AS events, approx_distinct(user_id) AS users
           FROM lake.raw.events_json
           WHERE tenant = 'acme' AND dt BETWEEN '{start}' AND '{end}'
           GROUP BY dt, event_type""",
        """SELECT dt, event_type, events, users
           FROM lake.curated.event_daily_rollup
           WHERE tenant_id = 'acme' AND dt BETWEEN DATE '{start}' AND DATE '{end}'""",
        7,
    ),
    "Revenue by currency (14 days)": (
        """SELECT dt, properties.currency, count(*), sum(properties.amount_cents)
           FROM lake.raw.events_json
           WHERE tenant = 'acme' AND dt BETWEEN '{start}' AND '{end}' AND event_type = 'order.placed'
           GROUP BY dt, properties.currency""",
        """SELECT dt, currency, orders, revenue_cents
           FROM lake.curated.revenue_daily
           WHERE tenant_id = 'acme' AND dt BETWEEN DATE '{start}' AND DATE '{end}'""",
        14,
    ),
    "Top pages (7 days)": (
        """SELECT properties.path, count(*) AS views, approx_distinct(user_id)
           FROM lake.raw.events_json
           WHERE tenant = 'acme' AND dt BETWEEN '{start}' AND '{end}' AND event_type = 'page.view'
           GROUP BY properties.path ORDER BY views DESC LIMIT 5""",
        """SELECT path, count(*) AS views, approx_distinct(user_id)
           FROM lake.curated.events
           WHERE tenant_id = 'acme' AND dt BETWEEN DATE '{start}' AND DATE '{end}' AND event_type = 'page.view'
           GROUP BY path ORDER BY views DESC LIMIT 5""",
        7,
    ),
    "Urgent tickets by topic (30 days)": (
        """SELECT properties.topic, count(*)
           FROM lake.raw.events_json
           WHERE tenant = 'acme' AND dt BETWEEN '{start}' AND '{end}'
             AND event_type = 'support.ticket' AND properties.priority = 'urgent'
           GROUP BY properties.topic""",
        """SELECT topic, count(*)
           FROM lake.curated.events
           WHERE tenant_id = 'acme' AND dt BETWEEN DATE '{start}' AND DATE '{end}'
             AND event_type = 'support.ticket' AND priority = 'urgent'
           GROUP BY topic""",
        30,
    ),
    "Daily active users (30 days)": (
        """SELECT dt, approx_distinct(user_id) FROM lake.raw.events_json
           WHERE tenant = 'acme' AND dt BETWEEN '{start}' AND '{end}' GROUP BY dt""",
        """SELECT dt, approx_distinct(user_id) FROM lake.curated.events
           WHERE tenant_id = 'acme' AND dt BETWEEN DATE '{start}' AND DATE '{end}' GROUP BY dt""",
        30,
    ),
}


def run_once(conn, sql: str) -> tuple[float, int, int]:
    cur = conn.cursor()
    start = time.perf_counter()
    cur.execute(sql)
    rows = cur.fetchall()
    wall = (time.perf_counter() - start) * 1000
    return wall, int(cur.stats.get("physicalInputBytes", 0)), len(rows)


def measure(conn, sql: str, runs: int) -> dict:
    run_once(conn, sql)  # warm-up: metadata and file listings get cached
    walls, bytes_read, n = [], 0, 0
    for _ in range(runs):
        w, b, n = run_once(conn, sql)
        walls.append(w)
        bytes_read = b
    return {"median_ms": round(statistics.median(walls), 1), "min_ms": round(min(walls), 1),
            "bytes": bytes_read, "rows": n}


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--runs", type=int, default=7)
    ap.add_argument("--out", default=str(pathlib.Path(__file__).parent / "results" / "query_bench.json"))
    args = ap.parse_args()

    conn = trino.dbapi.connect(host=os.environ.get("TRINO_HOST", "localhost"),
                               port=int(os.environ.get("TRINO_PORT", "8088")), user="bench")
    results = []
    for name, (raw_sql, cur_sql, days) in QUERIES.items():
        params = {"start": d(days), "end": d(1)}
        raw = measure(conn, raw_sql.format(**params), args.runs)
        cur = measure(conn, cur_sql.format(**params), args.runs)
        assert raw["rows"] == cur["rows"], f"{name}: raw returned {raw['rows']} rows, curated {cur['rows']}"
        results.append({"query": name, "raw": raw, "curated": cur,
                        "speedup": round(raw["median_ms"] / cur["median_ms"], 2),
                        "latency_reduction": round(1 - cur["median_ms"] / raw["median_ms"], 3),
                        "bytes_reduction": round(1 - cur["bytes"] / raw["bytes"], 3) if raw["bytes"] else None})

    tot_raw = sum(r["raw"]["median_ms"] for r in results)
    tot_cur = sum(r["curated"]["median_ms"] for r in results)
    summary = {"runs": args.runs, "total_raw_ms": round(tot_raw, 1), "total_curated_ms": round(tot_cur, 1),
               "total_latency_reduction": round(1 - tot_cur / tot_raw, 3), "queries": results}

    print(f"\n| Query | Raw JSON (median) | Curated (median) | Faster | Bytes read raw → curated |")
    print("|---|---:|---:|---:|---:|")
    for r in results:
        print(f"| {r['query']} | {r['raw']['median_ms']:.0f} ms | {r['curated']['median_ms']:.0f} ms | "
              f"{r['latency_reduction']:.0%} | {r['raw']['bytes'] / 1e6:.1f} MB → {r['curated']['bytes'] / 1e6:.2f} MB |")
    print(f"| **All five** | **{tot_raw:.0f} ms** | **{tot_cur:.0f} ms** | **{summary['total_latency_reduction']:.0%}** | |")
    pathlib.Path(args.out).parent.mkdir(parents=True, exist_ok=True)
    pathlib.Path(args.out).write_text(json.dumps(summary, indent=2) + "\n")


if __name__ == "__main__":
    main()
