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
        for path, _label in patch_nginx.ROUTES:
            self.assertIn(f"location = {path}", updated)
        self.assertIn(patch_nginx.VIDEO_STATUS_MARKER.strip(), updated)
        self.assertIn(patch_nginx.DUPLICATE_PREFIX_MARKER.strip(), updated)
        self.assertIn("rewrite ^/v1/v1beta/", updated)
        self.assertIn("client_max_body_size 64m", updated)

    def test_is_idempotent(self):
        original = (
            "server {\n"
            + "".join(
                patch_nginx.route_block(path, label)
                for path, label in patch_nginx.ROUTES
            )
            + patch_nginx.VIDEO_STATUS_BLOCK
            + patch_nginx.DUPLICATE_PREFIX_BLOCK
            + patch_nginx.INSERT_BEFORE
            + "}\n"
        )
        updated, changed = patch_nginx.patched_config(original)
        self.assertFalse(changed)
        self.assertEqual(updated, original)

    def test_adds_edit_routes_to_existing_playground_config(self):
        original = (
            "server {\n"
            + patch_nginx.PLAYGROUND_BLOCK
            + patch_nginx.INSERT_BEFORE
            + "}\n"
        )
        updated, changed = patch_nginx.patched_config(original)
        self.assertTrue(changed)
        self.assertEqual(updated.count("location = /pg/images/generations"), 1)
        self.assertIn("location = /v1/images/edits", updated)
        self.assertIn("location = /pg/images/edits", updated)
        self.assertIn("location = /v1/contents/generations/tasks", updated)
        self.assertIn(patch_nginx.VIDEO_STATUS_MARKER.strip(), updated)


if __name__ == "__main__":
    unittest.main()
