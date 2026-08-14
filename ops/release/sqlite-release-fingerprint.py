#!/usr/bin/env python3
from __future__ import annotations

import hashlib
import json
import sqlite3
import sys
from pathlib import Path


PROTECTED_TABLES = ("channels", "abilities", "users", "tokens", "logs", "options")


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: sqlite-release-fingerprint.py <database> <output.json>", file=sys.stderr)
        return 64
    database, output = map(Path, sys.argv[1:])
    connection = sqlite3.connect(f"file:{database.as_posix()}?mode=ro", uri=True)
    connection.row_factory = sqlite3.Row
    try:
        quick_check = connection.execute("PRAGMA quick_check").fetchone()[0]
        if quick_check != "ok":
            raise SystemExit(f"SQLite quick_check failed: {quick_check}")
        tables = {
            row[0]
            for row in connection.execute(
                "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
            )
        }
        counts = {}
        schemas = {}
        for table in PROTECTED_TABLES:
            if table not in tables:
                raise SystemExit(f"required table is missing: {table}")
            quoted = '"' + table.replace('"', '""') + '"'
            counts[table] = connection.execute(f"SELECT COUNT(*) FROM {quoted}").fetchone()[0]
            schema = connection.execute(
                "SELECT sql FROM sqlite_master WHERE type='table' AND name=?", (table,)
            ).fetchone()[0]
            schemas[table] = hashlib.sha256(schema.encode("utf-8")).hexdigest()
        payload = {
            "quick_check": quick_check,
            "table_count": len(tables),
            "protected_counts": counts,
            "protected_schema_hashes": schemas,
        }
        output.write_text(json.dumps(payload, sort_keys=True, indent=2), encoding="utf-8")
    finally:
        connection.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
