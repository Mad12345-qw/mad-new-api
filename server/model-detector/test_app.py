import importlib
import os
import tempfile
import unittest

from fastapi.testclient import TestClient


_temp_dir = tempfile.TemporaryDirectory()
os.environ["DETECTOR_DATA_DIR"] = _temp_dir.name
os.environ["DETECTOR_MASTER_KEY"] = "test-master-key-which-is-not-production"
os.environ["DETECTOR_ADMIN_TOKEN"] = "test-admin-token-which-is-not-production"
os.environ["DETECTOR_TIMEOUT_SECONDS"] = "1"
service = importlib.import_module("app")


class AppTests(unittest.TestCase):
    def setUp(self) -> None:
        self.client_context = TestClient(service.app)
        self.client = self.client_context.__enter__()

    def tearDown(self) -> None:
        self.client_context.__exit__(None, None, None)

    def login(self) -> None:
        response = self.client.post("/detector/api/login", json={"token": os.environ["DETECTOR_ADMIN_TOKEN"]})
        self.assertEqual(response.status_code, 200)

    def test_auth_and_secret_redaction(self) -> None:
        self.assertEqual(self.client.get("/detector/api/state").status_code, 401)
        self.assertEqual(self.client.post("/detector/api/login", json={"token": "wrong-token-value"}).status_code, 401)
        self.login()
        response = self.client.post(
            "/detector/api/upstreams",
            json={
                "name": "Unit Relay",
                "base_url": "http://127.0.0.1:9/v1",
                "api_style": "openai",
                "api_key": "sk-unit-test-secret-1234",
                "models": ["gpt-5.6-sol"],
                "claimed_channel": "openai_official",
            },
        )
        self.assertEqual(response.status_code, 200, response.text)
        payload = response.json()
        self.assertEqual(payload["api_key_masked"], "****1234")
        self.assertNotIn("api_key_encrypted", payload)
        stored = service.db.row("SELECT api_key_encrypted FROM upstreams WHERE id=?", (payload["id"],))
        self.assertNotIn("sk-unit-test-secret", stored["api_key_encrypted"])

    def test_safe_probe_failure_remains_inconclusive(self) -> None:
        self.login()
        created = self.client.post(
            "/detector/api/upstreams",
            json={
                "name": "Offline Relay",
                "base_url": "http://127.0.0.1:9/v1",
                "api_style": "anthropic",
                "api_key": "unit-secret-5678",
                "models": ["claude-fable-5", "claude-opus-5"],
                "claimed_channel": "anthropic_official",
            },
        ).json()
        result = self.client.post("/detector/api/run", json={"upstream_id": created["id"], "mode": "safe"})
        self.assertEqual(result.status_code, 200, result.text)
        detail = self.client.get(f"/detector/api/runs/{result.json()['run_ids'][0]}").json()
        self.assertEqual(detail["run"]["verdict"], "inconclusive")
        self.assertTrue(any(item["category"] == "model_alias" for item in detail["evidence"]))


if __name__ == "__main__":
    unittest.main()
