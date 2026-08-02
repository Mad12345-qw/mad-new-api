import json
import os
import sqlite3
import threading
from contextlib import contextmanager
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterator


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


class Database:
    def __init__(self, data_dir: str | None = None) -> None:
        root = Path(data_dir or os.environ.get("DETECTOR_DATA_DIR", "/data"))
        root.mkdir(parents=True, exist_ok=True)
        self.path = root / "model-detector.db"
        self._lock = threading.RLock()
        self.migrate()

    @contextmanager
    def connect(self) -> Iterator[sqlite3.Connection]:
        with self._lock:
            connection = sqlite3.connect(self.path, timeout=30)
            connection.row_factory = sqlite3.Row
            connection.execute("PRAGMA foreign_keys = ON")
            connection.execute("PRAGMA journal_mode = WAL")
            try:
                yield connection
                connection.commit()
            finally:
                connection.close()

    def migrate(self) -> None:
        with self.connect() as db:
            db.executescript(
                """
                CREATE TABLE IF NOT EXISTS settings (
                    key TEXT PRIMARY KEY,
                    value TEXT NOT NULL,
                    updated_at TEXT NOT NULL
                );
                CREATE TABLE IF NOT EXISTS upstreams (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    name TEXT NOT NULL,
                    base_url TEXT NOT NULL,
                    api_style TEXT NOT NULL,
                    claimed_channel TEXT NOT NULL DEFAULT 'unknown',
                    api_key_encrypted TEXT NOT NULL,
                    api_key_masked TEXT NOT NULL,
                    models_json TEXT NOT NULL DEFAULT '[]',
                    model_routes_json TEXT NOT NULL DEFAULT '[]',
                    discovery_json TEXT NOT NULL DEFAULT '{}',
                    role TEXT NOT NULL DEFAULT 'candidate',
                    reference_upstream_id INTEGER,
                    allow_paid_probes INTEGER NOT NULL DEFAULT 0,
                    enabled INTEGER NOT NULL DEFAULT 1,
                    source_type TEXT NOT NULL DEFAULT 'manual',
                    new_api_channel_id INTEGER,
                    new_api_key_index INTEGER,
                    new_api_channel_type INTEGER,
                    new_api_channel_status INTEGER,
                    expected_channel TEXT NOT NULL DEFAULT 'unknown',
                    expected_models_json TEXT NOT NULL DEFAULT '[]',
                    auto_disable_on_mismatch INTEGER NOT NULL DEFAULT 0,
                    last_synced_at TEXT,
                    created_at TEXT NOT NULL,
                    updated_at TEXT NOT NULL,
                    FOREIGN KEY(reference_upstream_id) REFERENCES upstreams(id) ON DELETE SET NULL
                );
                CREATE TABLE IF NOT EXISTS runs (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    upstream_id INTEGER NOT NULL,
                    trigger TEXT NOT NULL,
                    mode TEXT NOT NULL,
                    status TEXT NOT NULL,
                    verdict TEXT,
                    likely_channel TEXT,
                    confidence REAL,
                    summary TEXT,
                    compliance_status TEXT NOT NULL DEFAULT 'not_evaluated',
                    compliance_detail_json TEXT NOT NULL DEFAULT '{}',
                    expected_channel TEXT,
                    auto_action TEXT NOT NULL DEFAULT 'none',
                    auto_action_detail TEXT,
                    rule_version TEXT NOT NULL,
                    started_at TEXT NOT NULL,
                    finished_at TEXT,
                    progress_current INTEGER NOT NULL DEFAULT 0,
                    progress_total INTEGER NOT NULL DEFAULT 0,
                    progress_model TEXT,
                    progress_phase TEXT NOT NULL DEFAULT 'queued',
                    FOREIGN KEY(upstream_id) REFERENCES upstreams(id) ON DELETE CASCADE
                );
                CREATE TABLE IF NOT EXISTS evidence (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    run_id INTEGER NOT NULL,
                    model TEXT,
                    probe TEXT NOT NULL,
                    category TEXT NOT NULL,
                    strength TEXT NOT NULL,
                    supports TEXT,
                    title TEXT NOT NULL,
                    detail_json TEXT NOT NULL,
                    raw_sha256 TEXT,
                    created_at TEXT NOT NULL,
                    FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE
                );
                CREATE TABLE IF NOT EXISTS model_results (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    run_id INTEGER NOT NULL,
                    model TEXT NOT NULL,
                    family TEXT NOT NULL,
                    protocol TEXT NOT NULL,
                    endpoint TEXT,
                    status TEXT NOT NULL,
                    verdict TEXT NOT NULL,
                    likely_channel TEXT NOT NULL,
                    confidence REAL NOT NULL,
                    summary TEXT NOT NULL,
                    success_probes INTEGER NOT NULL DEFAULT 0,
                    planned_probes INTEGER NOT NULL DEFAULT 0,
                    chain_json TEXT NOT NULL,
                    created_at TEXT NOT NULL,
                    FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE
                );
                CREATE TABLE IF NOT EXISTS channel_actions (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    run_id INTEGER NOT NULL,
                    upstream_id INTEGER NOT NULL,
                    new_api_channel_id INTEGER NOT NULL,
                    action TEXT NOT NULL,
                    status TEXT NOT NULL,
                    reason TEXT NOT NULL,
                    detail_json TEXT NOT NULL DEFAULT '{}',
                    created_at TEXT NOT NULL,
                    FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE,
                    FOREIGN KEY(upstream_id) REFERENCES upstreams(id) ON DELETE CASCADE
                );
                CREATE TABLE IF NOT EXISTS notification_events (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    run_id INTEGER NOT NULL,
                    destination TEXT NOT NULL,
                    status TEXT NOT NULL,
                    detail TEXT NOT NULL DEFAULT '',
                    created_at TEXT NOT NULL,
                    FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE
                );
                CREATE INDEX IF NOT EXISTS idx_runs_upstream_started
                    ON runs(upstream_id, started_at DESC);
                CREATE INDEX IF NOT EXISTS idx_evidence_run
                    ON evidence(run_id, id);
                CREATE INDEX IF NOT EXISTS idx_channel_actions_run
                    ON channel_actions(run_id, id);
                CREATE INDEX IF NOT EXISTS idx_notifications_run
                    ON notification_events(run_id, id);
                """
            )
            columns = {row["name"] for row in db.execute("PRAGMA table_info(upstreams)")}
            if "claimed_channel" not in columns:
                db.execute("ALTER TABLE upstreams ADD COLUMN claimed_channel TEXT NOT NULL DEFAULT 'unknown'")
            if "model_routes_json" not in columns:
                db.execute("ALTER TABLE upstreams ADD COLUMN model_routes_json TEXT NOT NULL DEFAULT '[]'")
            if "discovery_json" not in columns:
                db.execute("ALTER TABLE upstreams ADD COLUMN discovery_json TEXT NOT NULL DEFAULT '{}'")
            upstream_migrations = {
                "source_type": "TEXT NOT NULL DEFAULT 'manual'",
                "new_api_channel_id": "INTEGER",
                "new_api_key_index": "INTEGER",
                "new_api_channel_type": "INTEGER",
                "new_api_channel_status": "INTEGER",
                "expected_channel": "TEXT NOT NULL DEFAULT 'unknown'",
                "expected_models_json": "TEXT NOT NULL DEFAULT '[]'",
                "auto_disable_on_mismatch": "INTEGER NOT NULL DEFAULT 0",
                "last_synced_at": "TEXT",
            }
            for name, definition in upstream_migrations.items():
                if name not in columns:
                    db.execute(f"ALTER TABLE upstreams ADD COLUMN {name} {definition}")
            evidence_columns = {row["name"] for row in db.execute("PRAGMA table_info(evidence)")}
            if "model" not in evidence_columns:
                db.execute("ALTER TABLE evidence ADD COLUMN model TEXT")
            run_columns = {row["name"] for row in db.execute("PRAGMA table_info(runs)")}
            run_migrations = {
                "compliance_status": "TEXT NOT NULL DEFAULT 'not_evaluated'",
                "compliance_detail_json": "TEXT NOT NULL DEFAULT '{}'",
                "expected_channel": "TEXT",
                "auto_action": "TEXT NOT NULL DEFAULT 'none'",
                "auto_action_detail": "TEXT",
                "progress_current": "INTEGER NOT NULL DEFAULT 0",
                "progress_total": "INTEGER NOT NULL DEFAULT 0",
                "progress_model": "TEXT",
                "progress_phase": "TEXT NOT NULL DEFAULT 'queued'",
            }
            for name, definition in run_migrations.items():
                if name not in run_columns:
                    db.execute(f"ALTER TABLE runs ADD COLUMN {name} {definition}")
            db.execute(
                "CREATE UNIQUE INDEX IF NOT EXISTS idx_upstreams_new_api_channel_key "
                "ON upstreams(new_api_channel_id, new_api_key_index) "
                "WHERE new_api_channel_id IS NOT NULL AND new_api_key_index IS NOT NULL"
            )
            for key, value in (
                ("scheduler_enabled", False),
                ("interval_minutes", 15),
                ("scheduled_mode", "safe"),
                ("webhook_url", ""),
                ("webhook_type", "generic"),
                ("email_enabled", False),
                ("smtp_host", ""),
                ("smtp_port", 587),
                ("smtp_username", ""),
                ("smtp_password_encrypted", ""),
                ("smtp_from", ""),
                ("smtp_starttls", True),
                ("smtp_ssl", False),
                ("alert_email_to", ""),
                ("auto_disable_enabled", False),
                ("auto_disable_min_confidence", 0.95),
                ("last_new_api_sync_at", ""),
            ):
                db.execute(
                    "INSERT INTO settings(key,value,updated_at) VALUES(?,?,?) "
                    "ON CONFLICT(key) DO NOTHING",
                    (key, json.dumps(value, ensure_ascii=False), utc_now()),
                )

    def set_setting(self, key: str, value: Any, db: sqlite3.Connection | None = None) -> None:
        own = db is None
        if own:
            context = self.connect()
            db = context.__enter__()
        assert db is not None
        try:
            db.execute(
                "INSERT INTO settings(key,value,updated_at) VALUES(?,?,?) "
                "ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at",
                (key, json.dumps(value, ensure_ascii=False), utc_now()),
            )
        finally:
            if own:
                context.__exit__(None, None, None)

    def settings(self) -> dict[str, Any]:
        with self.connect() as db:
            return {row["key"]: json.loads(row["value"]) for row in db.execute("SELECT key,value FROM settings")}

    def rows(self, sql: str, parameters: tuple[Any, ...] = ()) -> list[dict[str, Any]]:
        with self.connect() as db:
            return [dict(row) for row in db.execute(sql, parameters).fetchall()]

    def row(self, sql: str, parameters: tuple[Any, ...] = ()) -> dict[str, Any] | None:
        values = self.rows(sql, parameters)
        return values[0] if values else None

    def execute(self, sql: str, parameters: tuple[Any, ...] = ()) -> int:
        with self.connect() as db:
            cursor = db.execute(sql, parameters)
            return int(cursor.lastrowid)
