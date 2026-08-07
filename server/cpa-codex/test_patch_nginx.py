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
        self.assertIn("location = /codex/v1/responses", result)
        self.assertIn("location = /codex/cockpit/v1/responses", result)
        self.assertIn("limit_req zone=madapi_codex_responses_per_ip burst=1 nodelay", result)
        self.assertIn("add_header Retry-After 3 always", result)
        self.assertIn("proxy_pass http://127.0.0.1:8318/v1/;", result)
        self.assertIn("proxy_pass http://127.0.0.1:8319/v1/;", result)
        self.assertIn("X-MadAPI-Authorization $http_authorization", result)
        self.assertIn("location ^~ /codex-canary/v1/", result)
        self.assertIn("location ^~ /codex-canary/cockpit/v1/", result)
        self.assertIn("X-MadAPI-Codex-Canary 1", result)
        self.assertLess(result.index("location = /codex/v1/models"), result.index("location / {"))

    def test_retry_guard_is_scoped_to_codex_responses(self):
        self.assertIn("limit_req_zone $binary_remote_addr", patch_nginx.RETRY_GUARD_CONFIG)
        self.assertNotIn("location", patch_nginx.RETRY_GUARD_CONFIG)
        self.assertNotIn("/v1/", patch_nginx.RETRY_GUARD_CONFIG)

    def test_direct_mode_keeps_canary_header_only_on_newapi_routes(self):
        result = patch_nginx.reconcile_routes(BASE, mode="direct")
        primary = result[result.index("location ^~ /codex/v1/"):result.index("location ^~ /codex/cockpit/v1/")]
        self.assertIn("proxy_pass http://127.0.0.1:3001;", primary)
        self.assertIn("X-MadAPI-Codex-Canary 1", primary)
        self.assertNotIn("127.0.0.1:8318", primary)
        cockpit = result[result.index("location ^~ /codex/cockpit/v1/"):result.index("location = /codex-canary/v1/models")]
        self.assertIn("X-MadAPI-Codex-Cockpit 1", cockpit)

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
