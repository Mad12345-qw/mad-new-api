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
        for path in patch_nginx.IMAGE_ROUTES:
            block_start = updated.index(f"location = {path}")
            block_end = updated.index("\n    }", block_start)
            self.assertIn("proxy_pass http://127.0.0.1:3012;", updated[block_start:block_end])

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

    def test_migrates_official_video_create_route_to_new_api(self):
        path = "/v1/videos/generations"
        label = "video-create-v1-plural"
        original = (
            "server {\n"
            + patch_nginx.route_block(path, label, upstream_port=3010)
            + patch_nginx.INSERT_BEFORE
            + "}\n"
        )

        updated, changed = patch_nginx.patched_config(original)

        self.assertTrue(changed)
        self.assertEqual(updated.count(f"location = {path}"), 1)
        self.assertIn("proxy_pass http://127.0.0.1:3001;", updated)
        self.assertNotIn(
            patch_nginx.route_block(path, label, upstream_port=3010), updated
        )

    def test_migrates_existing_image_blocks_without_matching_old_comment(self):
        original = """server {
    # image-url-compat managed block
    location = /v1/images/generations {
        proxy_pass http://127.0.0.1:3010;
        proxy_set_header X-Legacy yes;
    }
}
"""
        updated, changed = patch_nginx.patched_config(original)
        self.assertTrue(changed)
        self.assertEqual(updated.count("location = /v1/images/generations"), 1)
        self.assertIn("proxy_pass http://127.0.0.1:3012;", updated)
        self.assertNotIn("X-Legacy", updated)

    def test_keeps_video_routes_on_python_service(self):
        original = "server {\n" + patch_nginx.INSERT_BEFORE + "}\n"
        updated, _ = patch_nginx.patched_config(original)
        for path, _label in patch_nginx.ROUTES:
            if path in patch_nginx.IMAGE_ROUTES or path in patch_nginx.DIRECT_NEW_API_ROUTES:
                continue
            block_start = updated.index(f"location = {path}")
            block_end = updated.index("\n    }", block_start)
            self.assertIn("proxy_pass http://127.0.0.1:3010;", updated[block_start:block_end])

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
