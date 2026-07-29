import importlib
import json
import os
import tempfile
import unittest
from unittest.mock import AsyncMock, Mock, patch

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
        with patch("app.validate_public_api_url", return_value=None):
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

    def test_discovery_returns_routed_models_without_storing_key(self) -> None:
        self.login()
        before = service.db.row("SELECT COUNT(*) AS count FROM upstreams")["count"]
        routed = {
            "model": "claude-fable-5",
            "family": "anthropic",
            "provider": "Anthropic",
            "protocol": "openai_chat",
            "endpoint": "/v1/chat/completions",
            "fallbacks": [],
            "supported_endpoint_types": ["openai"],
            "owned_by": "claude",
            "discovered_via": "openai_models",
            "route_reason": "translation",
        }
        mocked = AsyncMock(return_value={"models": [routed], "attempts": [{"source": "openai_models", "status_code": 200}]})
        with patch("app.validate_public_api_url", return_value=None), patch("app.discover_models", mocked):
            response = self.client.post(
                "/detector/api/discover",
                json={"base_url": "https://relay.example/v1", "api_key": "temporary-discovery-key"},
            )
        self.assertEqual(response.status_code, 200, response.text)
        self.assertEqual(response.json()["models"][0]["protocol"], "openai_chat")
        self.assertEqual(service.db.row("SELECT COUNT(*) AS count FROM upstreams")["count"], before)

    def test_existing_new_api_admin_session_is_accepted(self) -> None:
        previous = os.environ.get("DETECTOR_NEW_API_INTERNAL_URL")
        os.environ["DETECTOR_NEW_API_INTERNAL_URL"] = "http://new-api:3000"
        mocked = Mock(status_code=200)
        mocked.json.return_value = {"success": True, "data": {"id": 7, "role": 10}}
        try:
            with patch("app.httpx.get", return_value=mocked) as request:
                response = self.client.get(
                    "/detector/api/state",
                    headers={"cookie": "session=admin-session", "New-Api-User": "7"},
                )
            self.assertEqual(response.status_code, 200, response.text)
            self.assertEqual(request.call_args.kwargs["headers"]["cookie"], "session=admin-session")
            self.assertEqual(request.call_args.kwargs["headers"]["New-Api-User"], "7")
        finally:
            if previous is None:
                os.environ.pop("DETECTOR_NEW_API_INTERNAL_URL", None)
            else:
                os.environ["DETECTOR_NEW_API_INTERNAL_URL"] = previous

    def test_new_api_session_requires_matching_numeric_user_header(self) -> None:
        previous = os.environ.get("DETECTOR_NEW_API_INTERNAL_URL")
        os.environ["DETECTOR_NEW_API_INTERNAL_URL"] = "http://new-api:3000"
        mocked = Mock(status_code=200)
        mocked.json.return_value = {"success": True, "data": {"id": 8, "role": 100}}
        try:
            with patch("app.httpx.get", return_value=mocked):
                missing = self.client.get("/detector/api/state", headers={"cookie": "session=x"})
                mismatch = self.client.get(
                    "/detector/api/state",
                    headers={"cookie": "session=x", "New-Api-User": "7"},
                )
            self.assertEqual(missing.status_code, 401)
            self.assertEqual(mismatch.status_code, 401)
        finally:
            if previous is None:
                os.environ.pop("DETECTOR_NEW_API_INTERNAL_URL", None)
            else:
                os.environ["DETECTOR_NEW_API_INTERNAL_URL"] = previous

    def test_safe_probe_failure_remains_inconclusive(self) -> None:
        self.login()
        with patch("app.validate_public_api_url", return_value=None):
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

    def test_active_run_persists_per_model_chain_and_evidence(self) -> None:
        self.login()
        route = {
            "model": "claude-fable-5",
            "family": "anthropic",
            "provider": "Anthropic",
            "protocol": "openai_chat",
            "endpoint": "/v1/chat/completions",
            "fallbacks": [],
            "supported_endpoint_types": ["openai"],
            "owned_by": "claude",
            "discovered_via": "openai_models",
            "route_reason": "translation",
        }
        with patch("app.validate_public_api_url", return_value=None):
            created = self.client.post(
                "/detector/api/upstreams",
                json={
                    "name": "Routed Relay",
                    "base_url": "https://relay.example/v1",
                    "api_key": "unit-secret-routed-9999",
                    "model_routes": [route],
                    "allow_paid_probes": True,
                },
            ).json()
        model_result = {
            "model": "claude-fable-5",
            "family": "anthropic",
            "protocol": "openai_chat",
            "endpoint": "/v1/chat/completions",
            "verdict": "inconclusive",
            "likely_channel": "unknown",
            "confidence": 0.0,
            "summary": "terminal hidden",
            "success_probes": 2,
            "planned_probes": 2,
            "chain": {
                "layers": [{"kind": "new_api_gateway", "label": "outer"}],
                "minimum_confirmed_hops": 1,
                "unknown_intermediate_possible": True,
            },
            "evidence": [service.Evidence("model_sync", "payload", "info", None, "shape", {"ok": True})],
        }
        with patch.object(service.engine, "run", AsyncMock(return_value=({"verdict": "inconclusive", "likely_channel": "unknown", "confidence": 0.0, "summary": "safe"}, []))), patch.object(
            service.engine, "run_models", AsyncMock(return_value=[model_result])
        ):
            response = self.client.post("/detector/api/run", json={"upstream_id": created["id"], "mode": "active"})
        self.assertEqual(response.status_code, 200, response.text)
        detail = self.client.get(f"/detector/api/runs/{response.json()['run_ids'][0]}").json()
        self.assertEqual(detail["model_results"][0]["model"], "claude-fable-5")
        self.assertEqual(detail["model_results"][0]["chain"]["minimum_confirmed_hops"], 1)
        self.assertTrue(any(item["model"] == "claude-fable-5" for item in detail["evidence"]))

    def test_historical_claude_route_change_is_promoted_to_a_model_verdict(self) -> None:
        self.login()
        route = {
            "model": "claude-fable-5",
            "family": "anthropic",
            "provider": "Anthropic",
            "protocol": "openai_chat",
            "endpoint": "/v1/chat/completions",
            "fallbacks": [],
            "supported_endpoint_types": ["openai"],
            "owned_by": "claude",
            "discovered_via": "openai_models",
            "route_reason": "translation",
        }
        with patch("app.validate_public_api_url", return_value=None):
            upstream = self.client.post(
                "/detector/api/upstreams",
                json={
                    "name": "Historical Route Relay",
                    "base_url": "https://history.example/v1",
                    "api_key": "unit-secret-history-9999",
                    "model_routes": [route],
                    "allow_paid_probes": True,
                },
            ).json()
        historical_run = service.db.execute(
            "INSERT INTO runs(upstream_id,trigger,mode,status,verdict,likely_channel,confidence,summary,rule_version,started_at,finished_at) "
            "VALUES(?,?,?,?,?,?,?,?,?,?,?)",
            (
                upstream["id"],
                "manual",
                "active",
                "completed",
                "inconclusive",
                "unknown",
                0.0,
                "historical",
                service.RULE_VERSION,
                service.utc_now(),
                service.utc_now(),
            ),
        )
        historical_detail = {
            "usage": {
                "prompt_tokens": 6933,
                "billing_usage": {
                    "source": "claude_messages",
                    "claude_usage": {"input_tokens": 1248, "cache_creation_input_tokens": 5685},
                },
            }
        }
        service.db.execute(
            "INSERT INTO evidence(run_id,model,probe,category,strength,supports,title,detail_json,raw_sha256,created_at) "
            "VALUES(?,?,?,?,?,?,?,?,?,?)",
            (
                historical_run,
                "claude-fable-5",
                "model_capability",
                "token_accounting",
                "info",
                None,
                "usage",
                json.dumps(historical_detail),
                None,
                service.utc_now(),
            ),
        )
        current = {
            "model": "claude-fable-5",
            "family": "anthropic",
            "verdict": "inconclusive",
            "likely_channel": "unknown",
            "confidence": 0.0,
            "summary": "unknown terminal",
            "chain": {"layers": [{"position": "terminal", "kind": "unknown_terminal"}]},
            "evidence": [
                service.Evidence(
                    "model_capability",
                    "token_accounting",
                    "info",
                    None,
                    "usage",
                    {"usage": {"prompt_tokens": 110, "prompt_tokens_details": {"cache_creation_tokens": 31}}},
                )
            ],
        }
        adjusted = service.apply_historical_route_changes(upstream["id"], [current])[0]
        self.assertEqual(adjusted["likely_channel"], "heterogeneous_backend_pool")
        self.assertEqual(adjusted["verdict"], "probable_alternate_channel")
        self.assertEqual(adjusted["confidence"], 0.93)
        self.assertTrue(any(item.category == "historical_route_change" for item in adjusted["evidence"]))
        self.assertTrue(any(layer["kind"] == "heterogeneous_backend_pool" for layer in adjusted["chain"]["layers"]))


if __name__ == "__main__":
    unittest.main()
