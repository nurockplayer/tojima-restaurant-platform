#!/usr/bin/env python3
"""Exercise paired SQL rollback/reapply using an isolated, test-only SQLite DB."""
from pathlib import Path
import sqlite3
import tempfile

ROOT = Path(__file__).resolve().parents[1]
MIGRATIONS = ROOT / "migrations" / "test_only"
VERSION = "001_smoke"


def table_exists(connection: sqlite3.Connection, name: str) -> bool:
    return connection.execute(
        "SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", (name,)
    ).fetchone() is not None


def apply(connection: sqlite3.Connection) -> None:
    sql = (MIGRATIONS / f"{VERSION}.up.sql").read_text(encoding="utf-8")
    connection.executescript(
        f"BEGIN IMMEDIATE;\n{sql}\n"
        f"INSERT INTO schema_migrations(version) VALUES ('{VERSION}');\nCOMMIT;"
    )


def rollback(connection: sqlite3.Connection) -> None:
    sql = (MIGRATIONS / f"{VERSION}.down.sql").read_text(encoding="utf-8")
    connection.executescript(
        f"BEGIN IMMEDIATE;\n{sql}\n"
        f"DELETE FROM schema_migrations WHERE version='{VERSION}';\nCOMMIT;"
    )


def main() -> None:
    with tempfile.TemporaryDirectory(prefix="tojima-migration-smoke-") as directory:
        database = Path(directory) / "smoke.sqlite3"
        with sqlite3.connect(database) as connection:
            connection.execute(
                "CREATE TABLE schema_migrations (version TEXT PRIMARY KEY NOT NULL)"
            )
            apply(connection)
            if not table_exists(connection, "migration_smoke_probe"):
                raise SystemExit("up migration did not create the probe table")
            if connection.execute("SELECT version FROM schema_migrations").fetchall() != [(VERSION,)]:
                raise SystemExit("up migration did not record its version")
            rollback(connection)
            if table_exists(connection, "migration_smoke_probe"):
                raise SystemExit("down migration left the probe table behind")
            if connection.execute("SELECT version FROM schema_migrations").fetchall():
                raise SystemExit("rollback left the migration version behind")
            apply(connection)
            if not table_exists(connection, "migration_smoke_probe"):
                raise SystemExit("reapply did not recreate the probe table")
    print("migration smoke passed (isolated test-only SQLite database)")


if __name__ == "__main__":
    main()
