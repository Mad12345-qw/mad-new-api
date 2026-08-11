#!/usr/bin/env python3
"""Report changed SQLite columns without exposing row values."""

from __future__ import annotations

import json
import sqlite3
import sys
from pathlib import Path


def table_columns(database: sqlite3.Connection, table: str) -> tuple[list[str], list[str]]:
    quoted = '"' + table.replace('"', '""') + '"'
    rows = list(database.execute(f"PRAGMA table_info({quoted})"))
    columns = [row[1] for row in rows]
    primary = [row[1] for row in sorted(rows, key=lambda row: row[5]) if row[5]]
    if not primary and "id" in columns:
        primary = ["id"]
    return columns, primary


def table_rows(
    database: sqlite3.Connection, table: str, columns: list[str], primary: list[str]
) -> dict[tuple[object, ...], tuple[object, ...]]:
    quoted_table = '"' + table.replace('"', '""') + '"'
    quoted_columns = ", ".join('"' + column.replace('"', '""') + '"' for column in columns)
    rows: dict[tuple[object, ...], tuple[object, ...]] = {}
    for index, row in enumerate(database.execute(f"SELECT {quoted_columns} FROM {quoted_table}")):
        if primary:
            key = tuple(row[columns.index(column)] for column in primary)
        else:
            key = (index,)
        rows[key] = tuple(row)
    return rows


def compare_table(
    left: sqlite3.Connection, right: sqlite3.Connection, table: str
) -> dict[str, object]:
    left_columns, left_primary = table_columns(left, table)
    right_columns, right_primary = table_columns(right, table)
    common_columns = [column for column in left_columns if column in right_columns]
    primary = left_primary if left_primary == right_primary else []
    left_rows = table_rows(left, table, common_columns, primary)
    right_rows = table_rows(right, table, common_columns, primary)
    common_keys = left_rows.keys() & right_rows.keys()
    changed_by_column: dict[str, int] = {}
    changed_rows = 0
    for key in common_keys:
        left_row = left_rows[key]
        right_row = right_rows[key]
        row_changed = False
        for index, column in enumerate(common_columns):
            if left_row[index] != right_row[index]:
                changed_by_column[column] = changed_by_column.get(column, 0) + 1
                row_changed = True
        if row_changed:
            changed_rows += 1
    return {
        "left_rows": len(left_rows),
        "right_rows": len(right_rows),
        "added_rows": len(right_rows.keys() - left_rows.keys()),
        "removed_rows": len(left_rows.keys() - right_rows.keys()),
        "changed_rows": changed_rows,
        "changed_by_column": changed_by_column,
        "added_columns": sorted(set(right_columns) - set(left_columns)),
        "removed_columns": sorted(set(left_columns) - set(right_columns)),
        "primary_key_columns": primary,
    }


def main() -> int:
    if len(sys.argv) < 4:
        raise SystemExit("usage: sqlite-clone-diff.py <left-db> <right-db> <table ...>")
    left_path = Path(sys.argv[1]).resolve()
    right_path = Path(sys.argv[2]).resolve()
    left = sqlite3.connect(f"file:{left_path.as_posix()}?mode=ro", uri=True)
    right = sqlite3.connect(f"file:{right_path.as_posix()}?mode=ro", uri=True)
    try:
        result = {
            table: compare_table(left, right, table)
            for table in sys.argv[3:]
        }
    finally:
        left.close()
        right.close()
    print(json.dumps(result, ensure_ascii=True, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
