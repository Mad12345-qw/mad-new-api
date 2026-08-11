import importlib.util
import sqlite3
import sys
import tempfile
import types
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("mad-api-error-alert.py")
SPEC = importlib.util.spec_from_file_location("mad_api_error_alert", MODULE_PATH)
ALERT = importlib.util.module_from_spec(SPEC)
if "fcntl" not in sys.modules:
    sys.modules["fcntl"] = types.SimpleNamespace(LOCK_EX=0, LOCK_NB=0, flock=lambda *_args: None)
SPEC.loader.exec_module(ALERT)


class ErrorAlertMonitorTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.state_path = Path(self.temporary.name) / "state.json"
        self.connection = sqlite3.connect(":memory:")
        self.connection.row_factory = sqlite3.Row
        self.connection.executescript(
            """
            CREATE TABLE logs (
                id INTEGER PRIMARY KEY,
                type INTEGER NOT NULL,
                user_id INTEGER,
                username TEXT,
                created_at INTEGER,
                channel_id INTEGER,
                channel_name TEXT,
                model_name TEXT,
                request_id TEXT,
                content TEXT
            );
            CREATE TABLE channels (id INTEGER PRIMARY KEY, name TEXT);
            CREATE TABLE options (key TEXT PRIMARY KEY, value TEXT);
            """
        )
        self.connection.executemany(
            "INSERT INTO options (key, value) VALUES (?, ?)",
            [
                ("SMTPServer", "smtp.example.test"),
                ("SMTPPort", "465"),
                ("SMTPAccount", "alert@example.test"),
                ("SMTPToken", "test-token"),
            ],
        )
        self.connection.commit()
        ALERT.save_state(
            self.state_path,
            {"initialized": True, "last_seen_id": 0, "streak": 0, "alerted": False, "errors": []},
        )
        self.sent = []
        self.original_send = ALERT.send_email
        ALERT.send_email = lambda _options, message: self.sent.append(message)

    def tearDown(self):
        ALERT.send_email = self.original_send
        self.connection.close()
        self.temporary.cleanup()

    def add_log(self, log_type: int):
        next_id = self.connection.execute("SELECT COALESCE(MAX(id), 0) + 1 FROM logs").fetchone()[0]
        self.connection.execute(
            """
            INSERT INTO logs (id, type, created_at, channel_id, channel_name, model_name, request_id, content)
            VALUES (?, ?, 1, 1, 'test', 'gpt-test', ?, 'failure')
            """,
            (next_id, log_type, f"request-{next_id}"),
        )
        self.connection.commit()

    def test_alert_remains_locked_until_success(self):
        for _ in range(6):
            self.add_log(ALERT.ERROR_TYPE)
        ALERT.monitor(self.connection, self.state_path)

        self.assertEqual(1, len(self.sent))
        state = ALERT.load_state(self.state_path)
        self.assertTrue(state["alerted"])
        self.assertEqual(6, state["streak"])

        self.add_log(ALERT.ERROR_TYPE)
        ALERT.monitor(self.connection, self.state_path)
        self.assertEqual(1, len(self.sent))

        self.add_log(ALERT.SUCCESS_TYPE)
        for _ in range(5):
            self.add_log(ALERT.ERROR_TYPE)
        ALERT.monitor(self.connection, self.state_path)

        self.assertEqual(2, len(self.sent))
        state = ALERT.load_state(self.state_path)
        self.assertTrue(state["alerted"])
        self.assertEqual(5, state["streak"])


if __name__ == "__main__":
    unittest.main()
