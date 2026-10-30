#!/usr/bin/env bash
# Single-container Airflow: migrate the metadata DB, make an admin user, then
# run the scheduler and webserver together.
set -euo pipefail
airflow db migrate
airflow users create --role Admin --username "${AIRFLOW_ADMIN_USER:-admin}" \
  --password "${AIRFLOW_ADMIN_PASSWORD:-admin}" --firstname Lumen --lastname Admin \
  --email admin@example.com >/dev/null 2>&1 || true
airflow scheduler &
exec airflow webserver --port 8080
