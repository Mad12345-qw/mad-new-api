#!/usr/bin/env python3
from __future__ import annotations

import argparse
import fcntl
import html
import json
import os
import smtplib
import sqlite3
import sys
import tempfile
from datetime import datetime, timedelta, timezone
from email.message import EmailMessage
from pathlib import Path


DB_PATH = os.environ.get("MAD_API_DB", "/opt/madapi-new-api/data/one-api.db")
STATE_DIR = Path(os.environ.get("MAD_API_ALERT_STATE_DIR", "/var/lib/madapi-ops/error-alert"))
RECIPIENT = os.environ.get("MAD_API_ALERT_RECIPIENT", "").strip()
ERROR_TYPE = 5
SUCCESS_TYPE = 2
TOPUP_TYPE = 1
TRIGGER_COUNT = 5


def as_bool(value: str | None) -> bool:
    return str(value or "").strip().lower() in {"1", "true", "yes", "on"}


def open_database() -> sqlite3.Connection:
    uri = f"file:{DB_PATH}?mode=ro"
    connection = sqlite3.connect(uri, uri=True, timeout=10)
    connection.row_factory = sqlite3.Row
    return connection


def load_state(path: Path) -> dict:
    if not path.exists():
        return {"initialized": False, "last_seen_id": 0, "streak": 0, "alerted": False, "errors": []}
    try:
        state = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return {"initialized": False, "last_seen_id": 0, "streak": 0, "alerted": False, "errors": []}
    return {
        "initialized": bool(state.get("initialized")),
        "last_seen_id": int(state.get("last_seen_id", 0)),
        "streak": int(state.get("streak", 0)),
        "alerted": bool(state.get("alerted")),
        "errors": list(state.get("errors", []))[-TRIGGER_COUNT:],
    }


def save_state(path: Path, state: dict) -> None:
    state_dir = path.parent
    state_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    fd, temporary_name = tempfile.mkstemp(prefix="state-", dir=state_dir)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(state, handle, ensure_ascii=False, separators=(",", ":"))
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary_name, 0o600)
        os.replace(temporary_name, path)
    finally:
        if os.path.exists(temporary_name):
            os.unlink(temporary_name)


def smtp_options(connection: sqlite3.Connection) -> dict[str, str]:
    keys = {
        "SMTPServer",
        "SMTPPort",
        "SMTPAccount",
        "SMTPFrom",
        "SMTPToken",
        "SMTPSSLEnabled",
        "SMTPStartTLSEnabled",
    }
    placeholders = ",".join("?" for _ in keys)
    rows = connection.execute(
        f"SELECT key, value FROM options WHERE key IN ({placeholders})", tuple(keys)
    ).fetchall()
    options = {row["key"]: row["value"] for row in rows}
    required = ("SMTPServer", "SMTPPort", "SMTPAccount", "SMTPToken")
    missing = [key for key in required if not options.get(key)]
    if missing:
        raise RuntimeError("SMTP configuration is incomplete: " + ", ".join(missing))
    return options


def format_time(timestamp: int) -> str:
    china_time = timezone(timedelta(hours=8))
    return datetime.fromtimestamp(timestamp, tz=china_time).strftime("%Y-%m-%d %H:%M:%S Beijing")


def compact_error(row: sqlite3.Row) -> dict:
    content = (row["content"] or "").strip()
    if len(content) > 600:
        content = content[:600] + "..."
    return {
        "id": row["id"],
        "created_at": row["created_at"],
        "channel_id": row["channel_id"],
        "channel_name": row["channel_name"] or "",
        "model_name": row["model_name"] or "",
        "request_id": row["request_id"] or "",
        "content": content,
    }


def build_error_message(errors: list[dict], test: bool = False) -> EmailMessage:
    message = EmailMessage()
    message["To"] = RECIPIENT
    message["Subject"] = "MadAPI 邮件提醒测试" if test else "？？？MadAPI 报错？？？？？？？？？"

    if test:
        plain = (
            "Mad API error alert email is configured successfully.\n\n"
            "The monitor checks request-result logs every minute. It sends one alert when "
            "five request errors occur consecutively, and re-arms after a successful request.\n"
        )
        body = "<h2>Mad API alert test succeeded</h2><p>The server-side email alert path is working.</p>"
    else:
        lines = [
            "Mad API has recorded five consecutive request errors.",
            "No further alert will be sent until a successful request resets the error streak.",
            "",
        ]
        rows = []
        for item in errors:
            lines.extend(
                [
                    f"Log ID: {item['id']}",
                    f"Time: {format_time(item['created_at'])}",
                    f"Channel ID: {item['channel_id']}",
                    f"Channel Name: {item['channel_name']}",
                    f"Model: {item['model_name']}",
                    f"Request ID: {item['request_id']}",
                    f"Error: {item['content']}",
                    "",
                ]
            )
            rows.append(
                "<tr>"
                f"<td>{item['id']}</td>"
                f"<td>{html.escape(format_time(item['created_at']))}</td>"
                f"<td>{html.escape(str(item['channel_id']))}</td>"
                f"<td>{html.escape(item['channel_name'])}</td>"
                f"<td>{html.escape(item['model_name'])}</td>"
                f"<td>{html.escape(item['request_id'])}</td>"
                f"<td><pre>{html.escape(item['content'])}</pre></td>"
                "</tr>"
            )
        plain = "\n".join(lines)
        body = (
            "<h2>Mad API: five consecutive request errors</h2>"
            "<p>No repeated alert will be sent until a successful request resets the streak.</p>"
            "<table border='1' cellpadding='6' cellspacing='0'>"
            "<tr><th>Log</th><th>Time</th><th>Channel ID</th><th>Channel Name</th><th>Model</th><th>Request ID</th><th>Error</th></tr>"
            + "".join(rows)
            + "</table>"
        )

    message.set_content(plain)
    message.add_alternative(body, subtype="html")
    return message


def build_topup_message(row: sqlite3.Row) -> EmailMessage:
    message = EmailMessage()
    message["To"] = RECIPIENT
    message["Subject"] = "！！！MadAPI 充值！！！ ！！！ ！！！"
    content = (row["content"] or "").strip()
    plain = "\n".join(
        [
            "MadAPI 出现新的充值成功日志。",
            "",
            f"日志 ID：{row['id']}",
            f"时间：{format_time(row['created_at'])}",
            f"用户 ID：{row['user_id']}",
            f"用户名：{row['username'] or ''}",
            f"充值内容：{content}",
        ]
    )
    body = (
        "<h2>MadAPI 新充值提醒</h2>"
        "<table border='1' cellpadding='6' cellspacing='0'>"
        f"<tr><th>日志 ID</th><td>{row['id']}</td></tr>"
        f"<tr><th>时间</th><td>{html.escape(format_time(row['created_at']))}</td></tr>"
        f"<tr><th>用户 ID</th><td>{row['user_id']}</td></tr>"
        f"<tr><th>用户名</th><td>{html.escape(row['username'] or '')}</td></tr>"
        f"<tr><th>充值内容</th><td>{html.escape(content)}</td></tr>"
        "</table>"
    )
    message.set_content(plain)
    message.add_alternative(body, subtype="html")
    return message


def send_email(options: dict[str, str], message: EmailMessage) -> None:
    if not RECIPIENT:
        raise RuntimeError("MAD_API_ALERT_RECIPIENT is required")
    sender = options.get("SMTPFrom") or options["SMTPAccount"]
    message["From"] = sender
    host = options["SMTPServer"]
    port = int(options["SMTPPort"])
    use_ssl = as_bool(options.get("SMTPSSLEnabled"))
    use_starttls = as_bool(options.get("SMTPStartTLSEnabled"))

    smtp_class = smtplib.SMTP_SSL if use_ssl else smtplib.SMTP
    with smtp_class(host, port, timeout=30) as client:
        if not use_ssl:
            client.ehlo()
            if use_starttls:
                client.starttls()
                client.ehlo()
        client.login(options["SMTPAccount"], options["SMTPToken"])
        client.send_message(message)


def initialize_state(connection: sqlite3.Connection, state_path: Path) -> None:
    row = connection.execute("SELECT COALESCE(MAX(id), 0) FROM logs").fetchone()
    state = {"initialized": True, "last_seen_id": int(row[0]), "streak": 0, "alerted": False, "errors": []}
    save_state(state_path, state)
    print(f"initialized at log id {state['last_seen_id']}")


def monitor(connection: sqlite3.Connection, state_path: Path) -> None:
    state = load_state(state_path)
    if not state["initialized"]:
        initialize_state(connection, state_path)
        return

    rows = connection.execute(
        """
        SELECT l.id, l.type, l.user_id, l.username, l.created_at, l.channel_id,
               COALESCE(NULLIF(l.channel_name, ''), c.name, '') AS channel_name,
               l.model_name, l.request_id, l.content
        FROM logs AS l
        LEFT JOIN channels AS c ON c.id = l.channel_id
        WHERE l.id > ? AND l.type IN (?, ?, ?)
        ORDER BY l.id ASC
        """,
        (state["last_seen_id"], TOPUP_TYPE, SUCCESS_TYPE, ERROR_TYPE),
    ).fetchall()

    for row in rows:
        state["last_seen_id"] = int(row["id"])
        if row["type"] == TOPUP_TYPE:
            send_email(smtp_options(connection), build_topup_message(row))
            save_state(state_path, state)
            print(f"topup alert sent at log id {row['id']}")
            continue

        if row["type"] == SUCCESS_TYPE:
            state["streak"] = 0
            state["alerted"] = False
            state["errors"] = []
            continue

        state["streak"] += 1
        state["errors"] = (state["errors"] + [compact_error(row)])[-TRIGGER_COUNT:]
        if state["streak"] >= TRIGGER_COUNT and not state["alerted"]:
            send_email(smtp_options(connection), build_error_message(state["errors"]))
            state["alerted"] = True
            save_state(state_path, state)
            print(f"alert sent at log id {row['id']}")

    save_state(state_path, state)


def main() -> int:
    parser = argparse.ArgumentParser(description="Mad API consecutive error email monitor")
    parser.add_argument("--test-email", action="store_true", help="send a labeled SMTP test email")
    parser.add_argument("--status", action="store_true", help="show non-sensitive monitor state")
    args = parser.parse_args()

    STATE_DIR.mkdir(mode=0o700, parents=True, exist_ok=True)
    state_path = STATE_DIR / "state.json"
    lock_path = STATE_DIR / "monitor.lock"

    with lock_path.open("a+", encoding="ascii") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            print("another monitor run is active")
            return 0

        with open_database() as connection:
            if args.test_email:
                send_email(smtp_options(connection), build_error_message([], test=True))
                print("test email sent")
            elif args.status:
                state = load_state(state_path)
                safe_state = {key: state[key] for key in ("initialized", "last_seen_id", "streak", "alerted")}
                print(json.dumps(safe_state, ensure_ascii=True))
            else:
                monitor(connection, state_path)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(f"mad-api-error-alert failed: {exc}", file=sys.stderr)
        raise SystemExit(1)
