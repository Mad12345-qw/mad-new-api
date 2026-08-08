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


class PatchNginxTests(unittest.TestCase):
    def test_installs_newapi_first_routes_with_websocket_support(self):
        result = patch_nginx.reconcile_routes(BASE)
        self.assertEqual(result.count("proxy_pass http://127.0.0.1:3001;"), 5)
        self.assertNotIn("auth_request", result)
        self.assertNotIn("proxy_pass http://127.0.0.1:8320", result)
        self.assertIn('proxy_set_header Authorization $http_authorization;', result)
        self.assertIn('proxy_set_header X-MadAPI-Codex-Cockpit "1";', result)
        self.assertEqual(result.count("proxy_set_header Upgrade $http_upgrade;"), 2)
        self.assertEqual(result.count('proxy_set_header Connection "upgrade";'), 2)

    def test_is_idempotent(self):
        first = patch_nginx.reconcile_routes(BASE)
        self.assertEqual(first, patch_nginx.reconcile_routes(first))

    def test_replaces_previous_managed_routes(self):
        old = BASE.replace("    location / {", "    # MadAPI CPA Codex front routes begin\n    # old\n    # MadAPI CPA Codex front routes end\n\n    location / {")
        result = patch_nginx.reconcile_routes(old)
        self.assertEqual(result.count(patch_nginx.BEGIN_MARKER), 1)


if __name__ == "__main__":
    unittest.main()
