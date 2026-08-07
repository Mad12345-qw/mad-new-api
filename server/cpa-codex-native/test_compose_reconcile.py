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
  cpa-codex:
    image: old-image
  cpa-codex-cockpit:
    image: old-image
  redis:
    image: redis:7
"""


class ComposeReconcileTests(unittest.TestCase):
    def test_replaces_two_legacy_cpa_services_with_one_native_gateway(self):
        result = compose_reconcile.reconcile_compose(BASE)
        self.assertIn("  cpa-codex-native:\n", result)
        self.assertIn("127.0.0.1:8320:8317", result)
        self.assertIn("MADAPI_INTERNAL_URL: http://new-api:3000", result)
        self.assertNotIn("  cpa-codex:\n", result)
        self.assertNotIn("  cpa-codex-cockpit:\n", result)
        self.assertIn("  redis:\n", result)

    def test_is_idempotent(self):
        first = compose_reconcile.reconcile_compose(BASE)
        self.assertEqual(first, compose_reconcile.reconcile_compose(first))


if __name__ == "__main__":
    unittest.main()
