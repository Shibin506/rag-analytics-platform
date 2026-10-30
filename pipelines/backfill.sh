#!/usr/bin/env bash
# Rebuild the lake for a date range in one go (default: the last 14 days),
# running the same steps as the nightly DAG without one DAG run per day.
#   docker compose exec airflow /opt/lumen/pipelines/backfill.sh [START] [END]
set -euo pipefail
START=${1:-$(date -u -d '14 days ago' +%F)}
END=${2:-$(date -u +%F)}
cd /opt/lumen/pipelines
python -m lumen_pipeline.trino_tasks setup
spark-submit --master 'local[*]' --driver-memory 2g --jars "$SPARK_EXTRA_JARS" \
  spark/compact_events.py --start "$START" --end "$END"
python -m lumen_pipeline.trino_tasks sync
python -m lumen_pipeline.trino_tasks refresh --start "$START" --end "$END"
python -m lumen_pipeline.trino_tasks check --start "$START" --end "$END"
