import importlib.util
import json
import sqlite3
import tempfile
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("sync-cpa-channel-bridge.py")
SPEC = importlib.util.spec_from_file_location("sync_cpa_channel_bridge", MODULE_PATH)
BRIDGE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BRIDGE)


class BridgeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.database = self.root / "one-api.db"
        self.connection = sqlite3.connect(self.database)
        self.connection.executescript(
            """
            CREATE TABLE channels (
              id INTEGER PRIMARY KEY AUTOINCREMENT, type INTEGER, key TEXT,
              open_ai_organization TEXT, test_model TEXT, status INTEGER,
              name TEXT, weight INTEGER, created_time INTEGER, test_time INTEGER,
              response_time INTEGER, base_url TEXT, other TEXT, balance REAL,
              balance_updated_time INTEGER, models TEXT, "group" TEXT,
              used_quota INTEGER, model_mapping TEXT, status_code_mapping TEXT,
              priority INTEGER, auto_ban INTEGER, other_info TEXT, tag TEXT,
              setting TEXT, param_override TEXT, header_override TEXT,
              remark TEXT, channel_info TEXT, settings TEXT
            );
            CREATE TABLE abilities (
              "group" TEXT, model TEXT, channel_id INTEGER, enabled INTEGER,
              priority INTEGER, weight INTEGER, tag TEXT,
              PRIMARY KEY ("group", model, channel_id)
            );
            """
        )
        rows = [
            (1, "openai-secret", "OpenAI", "https://openai.test", "gpt-5.6-terra,gpt-image-2", "", 100, 50),
            (14, "claude-secret", "Claude", "https://claude.test", "claude-opus-5", "", 100, 80),
            (24, "gemini-secret", "Gemini", "https://gemini.test", "gemini-3.6-flash", "", 90, 60),
            (1, "pro-secret", "Pro", "https://pro.test", "gpt-5.6-sol-pro", '{"gpt-5.6-sol-pro":"gpt-5.6-sol"}', 110, 100),
        ]
        self.connection.executemany(
            'INSERT INTO channels(type,key,name,base_url,models,model_mapping,priority,weight,status,"group") VALUES(?,?,?,?,?,?,?,?,1,"default")',
            rows,
        )
        self.connection.commit()

    def tearDown(self):
        self.connection.close()
        self.temp.cleanup()

    def test_builds_prefixed_providers_and_idempotent_channel_clones(self):
        targets = {"gpt-5.6-terra", "gpt-image-2", "claude-opus-5", "gemini-3.6-flash", "gpt-5.6-sol-pro"}
        sources = BRIDGE.source_channels(self.connection, targets)
        config = BRIDGE.build_config(sources, "gateway-secret-value-that-is-long-enough")
        clone_ids = BRIDGE.upsert_clones(self.connection, sources, "http://cpa:8317", "gateway-secret-value-that-is-long-enough")
        self.connection.commit()
        second = BRIDGE.upsert_clones(self.connection, sources, "http://cpa:8317", "gateway-secret-value-that-is-long-enough")
        self.connection.commit()

        self.assertEqual(clone_ids, second)
        self.assertEqual(4, self.connection.execute("SELECT COUNT(*) FROM channels WHERE \"group\"='codex'").fetchone()[0])
        self.assertEqual(5, self.connection.execute("SELECT COUNT(*) FROM abilities WHERE \"group\"='codex' AND enabled=1").fetchone()[0])
        mapping = json.loads(self.connection.execute("SELECT model_mapping FROM channels WHERE tag='madapi-official-cpa-source:4'").fetchone()[0])
        self.assertEqual("madch4/gpt-5.6-sol", mapping["gpt-5.6-sol-pro"])
        self.assertIn('disable-image-generation: "passthrough"', config)
        self.assertIn('prefix: "madch1"', config)
        self.assertIn('image: true', config)
        self.assertIn('claude-api-key:', config)
        self.assertIn('gemini-api-key:', config)
        self.assertNotIn("gateway-secret-value-that-is-long-enough\n", json.dumps({"ids": clone_ids}))


if __name__ == "__main__":
    unittest.main()
