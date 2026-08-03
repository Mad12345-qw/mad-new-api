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

    def test_health_reports_rule_and_scheduler_state(self) -> None:
        response = self.client.get("/healthz")
        self.assertEqual(response.status_code, 200)
        self.assertTrue(response.json()["ok"])
        self.assertIn("rule_version", response.json())
        self.assertIn("scheduler_enabled", response.json())

    def test_integrated_ui_uses_new_api_admin_session_without_token_prompt(self) -> None:
        html = self.client.get("/detector/").text
        self.assertIn("localStorage.getItem('user')", html)
        self.assertIn("currentNewApiUserId", html)
        self.assertNotIn('id="loginToken"', html)
        self.assertNotIn('id="loginForm"', html)

    def test_ui_distinguishes_all_channels_from_one_channel_and_localizes_progress(self) -> None:
        html = self.client.get("/detector/").text
        self.assertIn("检测全部已监控渠道", html)
        self.assertIn("检测本渠道（", html)
        self.assertIn("正在执行跨协议预检", html)
        self.assertIn("已有检测任务正在运行", html)
        self.assertNotIn("a detector batch is already running", html)
        self.assertIn("判定与原因", html)
        self.assertIn("为什么是 0% / 无法判断", html)

    def test_zero_confidence_run_gets_a_visible_http_failure_reason(self) -> None:
        runs = [{"id": 7001, "mode": "active", "status": "completed", "confidence": 0.0}]
        with patch.object(
            service.db,
            "rows",
            side_effect=[
                [{"run_id": 7001, "planned": 8, "succeeded": 0}],
                [
                    {"run_id": 7001, "detail_json": json.dumps({"status_code": 400})}
                    for _ in range(8)
                ],
            ],
        ):
            service.attach_run_failure_reasons(runs)
        self.assertIn("HTTP 400×8", runs[0]["failure_reason"])
        self.assertIn("模型映射", runs[0]["failure_reason"])

    def test_duplicate_batch_error_is_chinese(self) -> None:
        self.login()
        fake_lock = Mock()
        fake_lock.locked.return_value = True
        with patch.object(service, "run_lock", fake_lock):
            response = self.client.post("/detector/api/run", json={"upstream_id": None, "mode": "active"})
        self.assertEqual(response.status_code, 409)
        self.assertEqual(response.json()["detail"], "已有检测任务正在运行，请等待完成后再试")

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

    def test_scheduler_requires_explicit_enable(self) -> None:
        self.login()
        response = self.client.put(
            "/detector/api/settings",
            json={
                "scheduler_enabled": False,
                "interval_minutes": 30,
                "scheduled_mode": "active",
                "webhook_url": "",
            },
        )
        self.assertEqual(response.status_code, 200, response.text)
        self.assertFalse(response.json()["scheduler_enabled"])
        self.assertEqual(response.json()["interval_minutes"], 30)
        state = self.client.get("/detector/api/state").json()
        self.assertFalse(state["settings"]["scheduler_enabled"])

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

    def test_saved_upstream_can_rediscover_without_returning_or_retyping_key(self) -> None:
        self.login()
        with patch("app.validate_public_api_url", return_value=None):
            created = self.client.post(
                "/detector/api/upstreams",
                json={
                    "name": "Saved Relay",
                    "base_url": "https://relay.example/v1",
                    "api_style": "auto",
                    "api_key": "saved-secret-key",
                    "models": ["gpt-5.6-sol"],
                    "allow_paid_probes": True,
                },
            ).json()
        mocked = AsyncMock(return_value={"models": [], "attempts": []})
        with patch("app.discover_models", mocked):
            response = self.client.post(f"/detector/api/upstreams/{created['id']}/discover")
        self.assertEqual(response.status_code, 200, response.text)
        self.assertNotIn("api_key", response.text)
        self.assertEqual(mocked.await_args.args[1], "saved-secret-key")

    def test_new_api_channel_sync_applies_model_mapping_and_encrypts_each_key(self) -> None:
        channel_id = 91001
        channels = [
            {
                "id": channel_id,
                "name": "Synced Relay",
                "type": 1,
                "type_name": "OpenAI",
                "status": 1,
                "base_url": "https://relay.example/v1",
                "models": ["customer-gpt", "claude-fable-5"],
                "model_mapping": {"customer-gpt": "gpt-5.6-sol"},
                "keys": [
                    {"index": 0, "key": "sync-secret-key-one", "enabled": True},
                    {"index": 1, "key": "sync-secret-key-two", "enabled": True},
                ],
            }
        ]
        with patch.object(service.new_api, "channels", AsyncMock(return_value=channels)):
            result = self.client.portal.call(service.sync_new_api_channels)
        self.assertEqual(result["created"], 2)
        rows = service.db.rows(
            "SELECT * FROM upstreams WHERE new_api_channel_id=? ORDER BY new_api_key_index",
            (channel_id,),
        )
        self.assertEqual(len(rows), 2)
        self.assertEqual(json.loads(rows[0]["models_json"]), ["gpt-5.6-sol", "claude-fable-5"])
        self.assertNotIn("sync-secret-key", rows[0]["api_key_encrypted"])
        self.assertEqual(rows[0]["source_type"], "new_api")

    def test_compliance_never_disables_when_no_model_probe_succeeds(self) -> None:
        compliance = service.evaluate_compliance(
            {"expected_channel": "openai_official"},
            {
                "verdict": "suspected_substitution",
                "likely_channel": "codex_subscription_relay",
                "confidence": 0.99,
            },
            [{"model": "gpt-5.6-sol", "success_probes": 0, "evidence": []}],
        )
        self.assertEqual(compliance["status"], "inconclusive")

    def test_compliance_confirms_only_high_confidence_incompatible_source(self) -> None:
        compliance = service.evaluate_compliance(
            {"expected_channel": "anthropic_official"},
            {
                "verdict": "suspected_substitution",
                "likely_channel": "claude_subscription_relay",
                "confidence": 0.99,
            },
            [{"model": "claude-fable-5", "success_probes": 3, "evidence": []}],
        )
        self.assertEqual(compliance["status"], "mismatch_confirmed")
        uncertain = service.evaluate_compliance(
            {"expected_channel": "anthropic_official"},
            {
                "verdict": "suspected_substitution",
                "likely_channel": "claude_subscription_relay",
                "confidence": 0.8,
            },
            [{"model": "claude-fable-5", "success_probes": 3, "evidence": []}],
        )
        self.assertEqual(uncertain["status"], "inconclusive")

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

    def test_historical_cliproxyapi_header_survives_load_balanced_rounds(self) -> None:
        self.login()
        with patch("app.validate_public_api_url", return_value=None):
            upstream = self.client.post(
                "/detector/api/upstreams",
                json={
                    "name": "Historical CPA Relay",
                    "base_url": "https://cpa-history.example/v1",
                    "api_key": "unit-secret-cpa-history-9999",
                    "models": ["gpt-5.6-sol"],
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
                "probable_alternate_channel",
                "codex_subscription_relay",
                0.99,
                "historical CPA",
                service.RULE_VERSION,
                service.utc_now(),
                service.utc_now(),
            ),
        )
        service.db.execute(
            "INSERT INTO evidence(run_id,model,probe,category,strength,supports,title,detail_json,raw_sha256,created_at) "
            "VALUES(?,?,?,?,?,?,?,?,?,?)",
            (
                historical_run,
                "gpt-5.6-sol",
                "model_sync",
                "observation",
                "info",
                None,
                "headers",
                json.dumps(
                    {
                        "headers": {
                            "x-cpa-trace-id": "20260729171232-eb53a98a245608bb-abd329cc",
                            "access-control-expose-headers": "X-CPA-TRACE-ID, X-CPA-VERSION, X-CPA-COMMIT",
                        }
                    }
                ),
                None,
                service.utc_now(),
            ),
        )
        current = {
            "model": "gpt-5.6-sol",
            "family": "openai",
            "verdict": "probable_alternate_channel",
            "likely_channel": "codex_subscription_relay",
            "confidence": 0.98,
            "summary": "Codex relay",
            "chain": {
                "layers": [
                    {"position": "outer", "kind": "new_api_gateway"},
                    {"position": "terminal", "kind": "codex_subscription_relay"},
                ],
                "minimum_confirmed_hops": 2,
            },
            "evidence": [
                service.Evidence("model_sync", "codex_prompt_fingerprint", "strong", "codex_subscription_relay", "prompt", {}),
                service.Evidence("matrix", "request_rewrite", "strong", "codex_subscription_relay", "rewrite", {}),
                service.Evidence("matrix", "multi_protocol_codex_translation", "strong", "codex_subscription_relay", "protocol", {}),
            ],
        }
        adjusted = service.apply_historical_route_changes(upstream["id"], [current])[0]
        self.assertEqual(adjusted["confidence"], 0.99)
        self.assertTrue(any(item.probe == "historical_cpa_headers" for item in adjusted["evidence"]))
        self.assertTrue(any(layer["kind"] == "cliproxyapi" for layer in adjusted["chain"]["layers"]))
        self.assertEqual(adjusted["chain"]["minimum_confirmed_hops"], 3)

    def test_recent_within_run_claude_divergence_survives_a_lightweight_round(self) -> None:
        self.login()
        with patch("app.validate_public_api_url", return_value=None):
            upstream = self.client.post(
                "/detector/api/upstreams",
                json={
                    "name": "Historical Claude Pool",
                    "base_url": "https://claude-pool.example/v1",
                    "api_key": "unit-secret-claude-pool-9999",
                    "models": ["claude-fable-5"],
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
                "suspected_substitution",
                "heterogeneous_backend_pool",
                0.96,
                "historical divergence",
                service.RULE_VERSION,
                service.utc_now(),
                service.utc_now(),
            ),
        )
        service.db.execute(
            "INSERT INTO evidence(run_id,model,probe,category,strength,supports,title,detail_json,raw_sha256,created_at) "
            "VALUES(?,?,?,?,?,?,?,?,?,?)",
            (
                historical_run,
                "claude-fable-5",
                "within_run_consistency",
                "within_run_route_divergence",
                "strong",
                "heterogeneous_backend_pool",
                "divergent paths",
                json.dumps(
                    {
                        "rule_id": "within_run_route_divergence_v1",
                        "conclusion": "same model reached mutually exclusive paths",
                    }
                ),
                "historical-divergence-digest",
                service.utc_now(),
            ),
        )
        current = {
            "model": "claude-fable-5",
            "family": "anthropic",
            "verdict": "inconclusive",
            "likely_channel": "unknown",
            "confidence": 0.0,
            "summary": "lightweight current node",
            "chain": {
                "layers": [
                    {"position": "outer", "kind": "new_api_gateway"},
                    {"position": "terminal", "kind": "unknown_terminal"},
                ],
                "minimum_confirmed_hops": 1,
            },
            "evidence": [
                service.Evidence(
                    "model_capability",
                    "token_accounting",
                    "info",
                    None,
                    "usage",
                    {"usage": {"input_tokens": 17, "cache_read_input_tokens": 79}},
                )
            ],
        }
        adjusted = service.apply_historical_route_changes(upstream["id"], [current])[0]
        self.assertEqual(adjusted["verdict"], "suspected_substitution")
        self.assertEqual(adjusted["likely_channel"], "heterogeneous_backend_pool")
        self.assertEqual(adjusted["confidence"], 0.94)
        self.assertTrue(
            any(item.category == "historical_within_run_route_divergence" for item in adjusted["evidence"])
        )
        terminal = next(item for item in adjusted["chain"]["layers"] if item["position"] == "terminal")
        self.assertEqual(terminal["kind"], "heterogeneous_backend_pool")
        self.assertEqual(terminal["status"], "confirmed_recent")
        self.assertEqual(adjusted["chain"]["minimum_confirmed_hops"], 2)

        translated_current = {
            "model": "claude-fable-5",
            "family": "anthropic",
            "verdict": "suspected_substitution",
            "likely_channel": "claude_compatibility_relay",
            "confidence": 0.96,
            "summary": "current OpenAI to Claude translation",
            "chain": {
                "layers": [
                    {"position": "outer", "kind": "new_api_gateway"},
                    {"position": "terminal", "kind": "claude_compatibility_relay"},
                ],
                "minimum_confirmed_hops": 2,
            },
            "evidence": [],
        }
        translated_adjusted = service.apply_historical_route_changes(
            upstream["id"], [translated_current]
        )[0]
        self.assertEqual(translated_adjusted["verdict"], "suspected_substitution")
        self.assertEqual(translated_adjusted["likely_channel"], "heterogeneous_backend_pool")
        self.assertEqual(translated_adjusted["confidence"], 0.96)
        self.assertIn("OpenAI to Claude translation", translated_adjusted["summary"])


if __name__ == "__main__":
    unittest.main()
