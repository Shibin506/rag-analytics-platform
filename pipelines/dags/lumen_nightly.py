"""Nightly lake pipeline.

    setup_tables -> compact_events (Spark) -> sync_partitions (Trino)
                 -> refresh_rollups (Trino) -> quality_check (Trino)

Runs at 02:00 UTC for the previous day. It also reprocesses the day before
(`lookback_days`), so late events that arrived after the last run are picked
up. Every step overwrites whole day partitions, so retries and backfills
(`airflow dags backfill -s 2026-10-01 -e 2026-10-30 lumen_nightly`) are safe.
"""

from __future__ import annotations

import datetime as dt

import pendulum
from airflow import DAG
from airflow.exceptions import AirflowFailException
from airflow.operators.bash import BashOperator
from airflow.operators.python import PythonOperator

from lumen_pipeline import trino_tasks

LOOKBACK_DAYS = 1


def _window(ds: str) -> tuple[dt.date, dt.date]:
    end = dt.date.fromisoformat(ds)
    return end - dt.timedelta(days=LOOKBACK_DAYS), end


def _setup(**_) -> None:
    trino_tasks.setup(trino_tasks.connect())


def _sync(**_) -> None:
    trino_tasks.sync_partitions(trino_tasks.connect())


def _refresh(ds: str, **_) -> dict:
    return trino_tasks.refresh_rollups(*_window(ds))


def _check(ds: str, **_) -> None:
    start, end = _window(ds)
    problems = []
    day = start
    while day <= end:
        report = trino_tasks.quality_check(day)
        problems += [f"{day}: {p}" for p in report.problems]
        day += dt.timedelta(days=1)
    if problems:
        # Fail without retrying: re-running will not fix a data mismatch.
        raise AirflowFailException("data quality check failed:\n" + "\n".join(problems))


with DAG(
    dag_id="lumen_nightly",
    description="Compact raw events into Parquet, refresh rollups, verify counts",
    schedule="0 2 * * *",
    start_date=pendulum.datetime(2026, 10, 1, tz="UTC"),
    catchup=False,
    max_active_runs=1,
    default_args={"retries": 2, "retry_delay": dt.timedelta(minutes=2)},
    tags=["lumen", "lake"],
) as dag:
    setup_tables = PythonOperator(task_id="setup_tables", python_callable=_setup)

    compact_events = BashOperator(
        task_id="compact_events",
        bash_command=(
            "spark-submit --master 'local[*]' --driver-memory 2g "
            "--jars \"$SPARK_EXTRA_JARS\" /opt/lumen/pipelines/spark/compact_events.py "
            f"--start {{{{ macros.ds_add(ds, -{LOOKBACK_DAYS}) }}}} --end {{{{ ds }}}}"
        ),
        execution_timeout=dt.timedelta(hours=2),
    )

    sync_partitions = PythonOperator(task_id="sync_partitions", python_callable=_sync)
    refresh_rollups = PythonOperator(task_id="refresh_rollups", python_callable=_refresh)
    quality_check = PythonOperator(task_id="quality_check", python_callable=_check, retries=0)

    setup_tables >> compact_events >> sync_partitions >> refresh_rollups >> quality_check
