import importlib.util
import json
import tempfile
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("catalog_sync.py")
SPEC = importlib.util.spec_from_file_location("catalog_sync", MODULE_PATH)
catalog_sync = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(catalog_sync)


class CatalogSyncTests(unittest.TestCase):
    def test_allowed_model_ids_keeps_text_endpoints_and_removes_duplicates(self):
        payload = {
            "data": [
                {"id": "grok-4", "supported_endpoint_types": ["openai-response"]},
                {"id": "deepseek-v4", "supported_endpoint_types": ["openai"]},
                {"id": "grok-4", "supported_endpoint_types": ["openai"]},
                {"id": "image-only", "supported_endpoint_types": ["images"]},
            ]
        }
        self.assertEqual(catalog_sync.allowed_model_ids(payload), ["grok-4", "deepseek-v4"])

    def test_render_config_contains_only_a_selector_not_the_catalog_key(self):
        rendered = catalog_sync.render_config(["grok-4", "deepseek-v4"])
        self.assertIn("madapi-passthrough: true", rendered)
        self.assertIn('alias: "grok-4"', rendered)
        self.assertNotIn("MADAPI_CATALOG_KEY", rendered)
        self.assertNotIn("sk-", rendered)

    def test_allowed_model_ids_rejects_an_empty_text_catalog(self):
        with self.assertRaises(ValueError):
            catalog_sync.allowed_model_ids({"data": [{"id": "image-only", "supported_endpoint_types": ["images"]}]})

    def test_write_config_preserves_the_existing_file_for_cpa_watcher(self):
        original_path = catalog_sync.CONFIG_PATH
        try:
            with tempfile.TemporaryDirectory() as directory:
                catalog_sync.CONFIG_PATH = Path(directory) / "config.yaml"
                catalog_sync.write_config("first\n")
                original_inode = catalog_sync.CONFIG_PATH.stat().st_ino
                catalog_sync.write_config("second\n")
                self.assertEqual(catalog_sync.CONFIG_PATH.stat().st_ino, original_inode)
                self.assertEqual(catalog_sync.CONFIG_PATH.read_text(encoding="utf-8"), "second\n")
        finally:
            catalog_sync.CONFIG_PATH = original_path


if __name__ == "__main__":
    unittest.main()
