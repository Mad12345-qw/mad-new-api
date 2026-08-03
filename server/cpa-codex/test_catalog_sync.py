import importlib.util
import tempfile
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("catalog_sync.py")
SPEC = importlib.util.spec_from_file_location("catalog_sync", MODULE_PATH)
catalog_sync = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(catalog_sync)


class CatalogSyncTests(unittest.TestCase):
    def test_allowed_model_ids_keeps_text_models(self):
        payload = {
            "data": [
                {"id": "gpt-5.6-sol", "supported_endpoint_types": ["openai-response"]},
                {"id": "grok-4.5", "supported_endpoint_types": ["openai"]},
                {"id": "image-only", "supported_endpoint_types": ["images"]},
            ]
        }
        self.assertEqual(catalog_sync.allowed_model_ids(payload), ["gpt-5.6-sol", "grok-4.5"])

    def test_native_config_keeps_real_model_names_and_retries(self):
        rendered = catalog_sync.render_config(["gpt-5.6-sol", "grok-4.5"], "native")
        self.assertIn('alias: "gpt-5.6-sol"', rendered)
        self.assertIn("request-retry: 2", rendered)
        self.assertIn("max-retry-credentials: 3", rendered)
        self.assertEqual(rendered.count("      - api-key:"), 3)
        self.assertIn("madapi-passthrough: true", rendered)
        self.assertNotIn("force-mapping: true", rendered)

    def test_cockpit_config_maps_all_eight_shells(self):
        model_ids = [upstream for upstream, _ in catalog_sync.COCKPIT_TARGETS]
        rendered = catalog_sync.render_config(model_ids, "cockpit")
        self.assertEqual(rendered.count("      - name:"), 8)
        self.assertIn('name: "deepseek-v4-flash"', rendered)
        self.assertIn('alias: "gpt-5.2"', rendered)
        self.assertEqual(rendered.count("force-mapping: true"), 8)

    def test_cockpit_config_rejects_missing_target(self):
        with self.assertRaisesRegex(ValueError, "deepseek-v4-flash"):
            catalog_sync.render_config(
                [upstream for upstream, _ in catalog_sync.COCKPIT_TARGETS if upstream != "deepseek-v4-flash"],
                "cockpit",
            )

    def test_write_config_preserves_inode(self):
        original_path = catalog_sync.CONFIG_PATH
        try:
            with tempfile.TemporaryDirectory() as directory:
                catalog_sync.CONFIG_PATH = Path(directory) / "config.yaml"
                catalog_sync.write_config("first\n")
                original_inode = catalog_sync.CONFIG_PATH.stat().st_ino
                catalog_sync.write_config("second\n")
                self.assertEqual(catalog_sync.CONFIG_PATH.stat().st_ino, original_inode)
        finally:
            catalog_sync.CONFIG_PATH = original_path


if __name__ == "__main__":
    unittest.main()
