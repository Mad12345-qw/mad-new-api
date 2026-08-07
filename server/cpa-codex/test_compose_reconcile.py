import importlib.util
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("compose_reconcile.py")
SPEC = importlib.util.spec_from_file_location("compose_reconcile", MODULE_PATH)
compose_reconcile = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(compose_reconcile)


BASE = """services:
  new-api:
    image: mad-new-api:latest
    environment:
      SESSION_SECRET: ${SESSION_SECRET}
    ports:
      - "127.0.0.1:3001:3000"

  redis:
    image: redis:7
"""


class ComposeReconcileTests(unittest.TestCase):
    def test_adds_two_front_proxies_and_required_environment(self):
        result = compose_reconcile.reconcile_compose(BASE)
        self.assertIn("MADAPI_CODEX_DISPATCH_TOKEN: ${MADAPI_CODEX_DISPATCH_TOKEN}", result)
        self.assertIn("MADAPI_CPA_DISPATCH_URL: http://cpa-codex:8317/internal/madapi/codex/execute", result)
        self.assertIn("MADAPI_CPA_IMAGE_DISPATCH_URL: http://cpa-codex:8317/internal/madapi/codex/image", result)
        self.assertIn("MADAPI_CPA_CANARY_DISPATCH_URL: http://cpa-codex-canary:8317/internal/madapi/codex/execute", result)
        self.assertIn("MADAPI_CPA_CANARY_COCKPIT_DISPATCH_URL: http://cpa-codex-cockpit-canary:8317/internal/madapi/codex/execute", result)
        self.assertIn("MADAPI_INTERNAL_CATALOG_TOKEN: ${MADAPI_INTERNAL_CATALOG_TOKEN}", result)
        self.assertIn("127.0.0.1:8318:8317", result)
        self.assertIn("127.0.0.1:8319:8317", result)
        self.assertEqual(result.count('host.docker.internal:host-gateway'), 2)
        self.assertEqual(result.count('MADAPI_IMAGE_COMPAT_URL: http://host.docker.internal:3010'), 2)
        self.assertIn("    mem_limit: 512m\n", result)
        self.assertIn("    mem_limit: 384m\n", result)
        self.assertIn("CPA_CATALOG_MODE: native", result)
        self.assertIn("CPA_CATALOG_MODE: cockpit", result)

    def test_replaces_legacy_sidecar_service_and_is_idempotent(self):
        legacy = BASE + """
  cpa-codex:
    image: old-image
    environment:
      MADAPI_INTERNAL_URL: http://new-api:3000
      MADAPI_INTERNAL_CATALOG_TOKEN: old
"""
        first = compose_reconcile.reconcile_compose(legacy)
        second = compose_reconcile.reconcile_compose(first)
        self.assertEqual(first, second)
        self.assertEqual(first.count("  cpa-codex:\n"), 1)
        self.assertEqual(first.count("  cpa-codex-cockpit:\n"), 1)
        self.assertIn("MADAPI_INTERNAL_CATALOG_TOKEN", first)


if __name__ == "__main__":
    unittest.main()
