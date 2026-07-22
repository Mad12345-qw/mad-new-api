import importlib.util
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("patch_nginx.py")
SPEC = importlib.util.spec_from_file_location("patch_nginx", MODULE_PATH)
patch_nginx = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(patch_nginx)


class PatchNginxTest(unittest.TestCase):
    def test_inserts_playground_route_before_existing_managed_block(self):
        original = "server {\n" + patch_nginx.INSERT_BEFORE + "}\n"
        updated, changed = patch_nginx.patched_config(original)
        self.assertTrue(changed)
        self.assertIn("location = /pg/images/generations", updated)
        self.assertLess(
            updated.index(patch_nginx.BLOCK_MARKER),
            updated.index(patch_nginx.INSERT_BEFORE),
        )

    def test_is_idempotent(self):
        original = (
            "server {\n"
            + patch_nginx.PLAYGROUND_BLOCK
            + patch_nginx.INSERT_BEFORE
            + "}\n"
        )
        updated, changed = patch_nginx.patched_config(original)
        self.assertFalse(changed)
        self.assertEqual(updated, original)


if __name__ == "__main__":
    unittest.main()
