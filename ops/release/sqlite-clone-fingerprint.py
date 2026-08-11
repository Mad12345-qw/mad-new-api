#!/usr/bin/env python3
"""Create a value-safe fingerprint for a NewAPI SQLite clone."""

from __future__ import annotations

import hashlib
import json
import sqlite3
import sys
from pathlib import Path


CRITICAL_TABLES = ("users", "channels", "tokens", "options")


def update_value(digest: hashlib._Hash, value: object) -> None:
    if value is None:
        payload = b"null"
    elif isinstance(value, bytes):
        payload = b"bytes:" + value
    else:
        payload = (type(value).__name__ + ":" + str(value)).encode("utf-8")
    digest.update(len(payload).to_bytes(8, "big"))
    digest.update(payload)


def fingerprint_table(database: sqlite3.Connection, table: str) -> dict[str, object]:
    quoted = '"' + table.replace('"', '""') + '"'
    columns = [row[1] for row in database.execute(f"PRAGMA table_info({quoted})")]
    digest = hashlib.sha256()
    count = 0
    try:
        cursor = database.execute(f"SELECT * FROM {quoted} ORDER BY rowid")
    except sqlite3.OperationalError:
        cursor = database.execute(f"SELECT * FROM {quoted}")
    for row in cursor:
        count += 1
        for value in row:
            update_value(digest, value)
    return {"columns": columns, "rows": count, "sha256": digest.hexdigest()}


def main() -> int:
    if len(sys.argv) != 2:
        raise SystemExit("usage: sqlite-clone-fingerprint.py <database>")
    path = Path(sys.argv[1]).resolve()
    database = sqlite3.connect(f"file:{path.as_posix()}?mode=ro", uri=True, timeout=30)
    try:
        check = database.execute("PRAGMA quick_check").fetchone()[0]
        available = {
            row[0]
            for row in database.execute(
                "SELECT name FROM sqlite_master "
                "WHERE type='table' AND name NOT LIKE 'sqlite_%'"
            )
        }
        result = {
            "database": str(path),
            "quick_check": check,
            "table_count": len(available),
            "critical": {
                table: fingerprint_table(database, table)
                for table in CRITICAL_TABLES
                if table in available
            },
        }
    finally:
        database.close()
    if result["quick_check"] != "ok":
        raise SystemExit(f"SQLite quick_check failed: {result['quick_check']}")
    print(json.dumps(result, ensure_ascii=True, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
