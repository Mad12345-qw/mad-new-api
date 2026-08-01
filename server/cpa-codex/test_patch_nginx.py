import importlib.util
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("patch_nginx.py")
SPEC = importlib.util.spec_from_file_location("patch_nginx", MODULE_PATH)
patch_nginx = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(patch_nginx)


LEGACY_BLOCK = """server {
    listen 443 ssl;

    # MadAPI CPA Codex sidecar
    location ^~ /codex/v1/ {
        proxy_pass http://127.0.0.1:8318/v1/;
        proxy_http_version 1.1;
        proxy_set_header Authorization \"Bearer madapi-codex-gateway\";
    }

    location / {
        proxy_pass http://127.0.0.1:3001;
    }
}
"""


class PatchNginxTests(unittest.TestCase):
    def test_removes_only_the_obsolete_front_sidecar_location(self):
        restored, removed = patch_nginx.remove_legacy_sidecar_routes(LEGACY_BLOCK)
        self.assertEqual(removed, 1)
        self.assertNotIn("127.0.0.1:8318", restored)
        self.assertIn("proxy_pass http://127.0.0.1:3001;", restored)
        self.assertIn("listen 443 ssl;", restored)

    def test_is_idempotent(self):
        restored, _ = patch_nginx.remove_legacy_sidecar_routes(LEGACY_BLOCK)
        second, removed = patch_nginx.remove_legacy_sidecar_routes(restored)
        self.assertEqual(removed, 0)
        self.assertEqual(second, restored)

    def test_preserves_an_unmanaged_codex_location(self):
        source = LEGACY_BLOCK.replace(
            "# MadAPI CPA Codex sidecar\n    location ^~ /codex/v1/ {\n        proxy_pass http://127.0.0.1:8318/v1/;",
            "# Operator-managed Codex route\n    location ^~ /codex/v1/ {\n        proxy_pass http://127.0.0.1:3001/codex/v1/;",
        )
        restored, removed = patch_nginx.remove_legacy_sidecar_routes(source)
        self.assertEqual(removed, 0)
        self.assertEqual(restored, source)


if __name__ == "__main__":
    unittest.main()
