import importlib.util
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("patch_nginx.py")
SPEC = importlib.util.spec_from_file_location("patch_nginx", MODULE_PATH)
patch_nginx = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(patch_nginx)


BASE = """server {
    listen 443 ssl;

    location / {
        proxy_pass http://127.0.0.1:3001;
    }
}
"""

LEGACY = """server {
    listen 443 ssl;

    # MadAPI CPA Codex sidecar
    location ^~ /codex/v1/ {
        proxy_pass http://127.0.0.1:8318/v1/;
        proxy_set_header Authorization "Bearer madapi-codex-gateway";
    }

    location / {
        proxy_pass http://127.0.0.1:3001;
    }
}
"""


class PatchNginxTests(unittest.TestCase):
    def test_installs_native_and_cockpit_front_routes(self):
        result = patch_nginx.reconcile_routes(BASE)
        self.assertIn("location = /codex/v1/models", result)
        self.assertIn("location = /codex/cockpit/v1/models", result)
        self.assertIn("proxy_pass http://127.0.0.1:8318/v1/;", result)
        self.assertIn("proxy_pass http://127.0.0.1:8319/v1/;", result)
        self.assertIn("X-MadAPI-Authorization $http_authorization", result)
        self.assertLess(result.index("location = /codex/v1/models"), result.index("location / {"))

    def test_is_idempotent(self):
        first = patch_nginx.reconcile_routes(BASE)
        second = patch_nginx.reconcile_routes(first)
        self.assertEqual(first, second)
        self.assertEqual(first.count(patch_nginx.BEGIN_MARKER), 1)

    def test_replaces_legacy_front_route(self):
        result = patch_nginx.reconcile_routes(LEGACY)
        self.assertEqual(result.count("proxy_pass http://127.0.0.1:8318/v1/;"), 1)
        self.assertIn("proxy_pass http://127.0.0.1:8319/v1/;", result)

    def test_rejects_unmanaged_codex_route(self):
        unmanaged = BASE.replace(
            "    location / {",
            "    location ^~ /codex/v1/ { proxy_pass http://127.0.0.1:9999; }\n\n    location / {",
        )
        with self.assertRaisesRegex(RuntimeError, "unmanaged Codex"):
            patch_nginx.reconcile_routes(unmanaged)


if __name__ == "__main__":
    unittest.main()
