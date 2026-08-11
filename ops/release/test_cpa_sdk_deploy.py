from __future__ import annotations

import importlib.util
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parent


def load(name: str, filename: str):
    spec = importlib.util.spec_from_file_location(name, ROOT / filename)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


compose = load("compose_cpa_sdk", "compose-cpa-sdk.py")
nginx = load("patch_nginx_codex", "patch-nginx-codex-route.py")


class ComposeRenderTest(unittest.TestCase):
    def test_render_is_idempotent_and_preserves_existing_services(self):
        source = """services:
  new-api:
    image: mad-new-api:old
    environment:
      TZ: Asia/Shanghai
      TRUSTED_ROUTE_GROUP: old
  cpa-codex-native:
    image: mad-cpa-codex:old
"""
        first = compose.render(source)
        second = compose.render(first)
        self.assertEqual(first, second)
        self.assertIn("  cpa-codex-native:", first)
        self.assertEqual(first.count("  cpa-sdk-host:"), 1)
        self.assertEqual(first.count("      TRUSTED_ROUTE_GROUP: codex"), 1)
        self.assertIn("      TRUSTED_ROUTE_PRESERVE_USER_GROUP: \"true\"", first)

    def test_missing_new_api_fails_closed(self):
        with self.assertRaises(ValueError):
            compose.render("services:\n  other:\n    image: example\n")


class NginxRenderTest(unittest.TestCase):
    def test_only_managed_block_changes(self):
        site = """server {
    location /before { return 200; }
    # MadAPI native CPA Codex routes begin
    old
    # MadAPI native CPA Codex routes end
    location /after { return 200; }
}
"""
        result = nginx.render(site, "location ^~ /codex/v1/ { return 204; }")
        self.assertIn("location /before", result)
        self.assertIn("location /after", result)
        self.assertNotIn("\n    old\n", result)
        self.assertEqual(result.count(nginx.BEGIN), 1)

    def test_missing_markers_fail_closed(self):
        with self.assertRaises(ValueError):
            nginx.render("server {}\n", "location / {}\n")


if __name__ == "__main__":
    unittest.main()
