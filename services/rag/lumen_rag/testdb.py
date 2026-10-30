"""Throwaway Postgres databases for tests and the eval: create a fresh database,
apply the catalog's Flyway migrations to it, drop it afterwards."""

from __future__ import annotations

import pathlib
import uuid
from contextlib import contextmanager
from typing import Iterator

import psycopg
from psycopg.conninfo import conninfo_to_dict, make_conninfo

MIGRATIONS = (pathlib.Path(__file__).resolve().parents[2]
              / "catalog" / "src" / "main" / "resources" / "db" / "migration")


def apply_migrations(dsn: str, directory: pathlib.Path = MIGRATIONS) -> None:
    files = sorted(directory.glob("V*__*.sql"), key=lambda p: int(p.name[1:].split("__")[0]))
    with psycopg.connect(dsn, autocommit=True) as conn:
        for f in files:
            conn.execute(f.read_text())


@contextmanager
def fresh_database(admin_dsn: str) -> Iterator[str]:
    name = f"lumen_test_{uuid.uuid4().hex[:10]}"
    with psycopg.connect(admin_dsn, autocommit=True) as conn:
        conn.execute(f'CREATE DATABASE "{name}"')
    params = conninfo_to_dict(admin_dsn)
    params["dbname"] = name
    dsn = make_conninfo(**params)
    try:
        apply_migrations(dsn)
        yield dsn
    finally:
        with psycopg.connect(admin_dsn, autocommit=True) as conn:
            conn.execute(f'DROP DATABASE IF EXISTS "{name}" WITH (FORCE)')
