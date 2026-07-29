import os
import tempfile
import unittest
import json

import httpx
from unittest.mock import AsyncMock, patch

from discovery import route_for_model
from engine import (
    DetectorEngine,
    Evidence,
    ProbeResponse,
    body_shape,
    capability_probe,
    classify,
    endpoint,
    image_validation_probes,
    model_alias_evidence,
    observed_chain,
    payload_evidence,
    provenance_evidence,
    route_probe,
    sanitize_headers,
    transport_evidence,
    within_run_route_divergence_evidence,
)
from security import decrypt_secret, encrypt_secret, mask_secret


class EngineTests(unittest.TestCase):
    def test_endpoint_preserves_existing_v1(self) -> None:
        self.assertEqual(endpoint("https://relay.example/v1", "/v1/models"), "https://relay.example/v1/models")
        self.assertEqual(endpoint("https://relay.example", "/v1/models"), "https://relay.example/v1/models")

    def test_sensitive_headers_are_removed(self) -> None:
        headers = sanitize_headers({"Authorization": "Bearer secret", "X-Request-Id": "req_1", "Api-Key": "secret"})
        self.assertEqual(headers, {"x-request-id": "req_1"})

    def test_one_alternate_signal_is_inconclusive(self) -> None:
        result = classify([Evidence("x", "headers", "strong", "aws_bedrock", "x", {})], "anthropic_official")
        self.assertEqual(result["verdict"], "inconclusive")

    def test_two_independent_alternate_signals_can_flag_claim_mismatch(self) -> None:
        evidence = [
            Evidence("endpoint", "endpoint", "strong", "aws_bedrock", "x", {}),
            Evidence("error", "headers", "strong", "aws_bedrock", "y", {}),
        ]
        result = classify(evidence, "anthropic_official")
        self.assertEqual(result["verdict"], "suspected_substitution")
        self.assertGreaterEqual(result["confidence"], 0.9)

    def test_two_weak_categories_still_remain_inconclusive(self) -> None:
        evidence = [
            Evidence("endpoint", "endpoint", "medium", "vertex_ai", "x", {}),
            Evidence("error", "errors", "weak", "vertex_ai", "y", {}),
        ]
        self.assertEqual(classify(evidence, "gemini_developer_api")["verdict"], "inconclusive")

    def test_direct_requires_endpoint_and_second_category(self) -> None:
        evidence = transport_evidence("https://api.openai.com/v1")
        self.assertEqual(classify(evidence, "openai_official")["verdict"], "inconclusive")
        evidence.append(Evidence("models", "headers", "strong", "openai_official", "x", {}))
        self.assertEqual(classify(evidence, "openai_official")["verdict"], "confirmed_direct")

    def test_latest_aliases_never_become_strong_evidence(self) -> None:
        items = model_alias_evidence(["claude-fable-5", "claude-opus-5", "gpt-5.6-sol"])
        self.assertEqual(len(items), 3)
        self.assertTrue(all(item.strength == "weak" and item.supports is None for item in items))

    def test_body_shape_discards_values(self) -> None:
        shaped = body_shape({"token": "secret", "nested": {"count": 2}})
        self.assertEqual(shaped["token"], "str")
        self.assertEqual(shaped["nested"]["count"], "int")

    def test_multihop_chain_separates_outer_translation_and_unknown_terminal(self) -> None:
        route = route_for_model("claude-fable-5", "claude", ["openai"])
        evidence = [
            Evidence(
                "model_sync",
                "observation",
                "info",
                None,
                "response",
                {"headers": {"x-oneapi-request-id": "req_1"}},
            )
        ]
        chain = observed_chain(
            evidence,
            route,
            {"verdict": "inconclusive", "likely_channel": "unknown", "confidence": 0.0},
        )
        self.assertEqual([layer["kind"] for layer in chain["layers"]], ["new_api_gateway", "protocol_translation", "unknown_terminal"])
        self.assertTrue(chain["unknown_intermediate_possible"])
        self.assertEqual(chain["minimum_confirmed_hops"], 1)
        self.assertEqual(chain["observed_logical_layers"], 3)

    def test_multihop_chain_keeps_preserved_proxy_markers_as_logical_layers_only(self) -> None:
        route = route_for_model("claude-fable-5", "claude", ["openai"])
        evidence = [
            Evidence(
                "model_sync",
                "observation",
                "info",
                None,
                "response",
                {
                    "headers": {
                        "x-oneapi-request-id": "req_1",
                        "x-litellm-model-id": "model_1",
                        "via": "1.1 relay",
                        "x-envoy-upstream-service-time": "12",
                    }
                },
            )
        ]
        chain = observed_chain(evidence, route, {"verdict": "inconclusive", "likely_channel": "unknown", "confidence": 0.0})
        self.assertEqual(
            [layer["kind"] for layer in chain["layers"]],
            ["new_api_gateway", "litellm_marker", "proxy_marker", "protocol_translation", "unknown_terminal"],
        )
        self.assertEqual(chain["minimum_confirmed_hops"], 1)
        self.assertEqual(chain["observed_logical_layers"], 5)

    def test_capability_payloads_match_each_protocol(self) -> None:
        anthropic = route_for_model("claude-opus-5", "claude", ["anthropic"])
        responses = route_for_model("gpt-5.6-sol", "openai", ["openai"])
        chat = route_for_model("claude-fable-5", "claude", ["openai"])

        path, payload = capability_probe(anthropic)
        self.assertEqual(path, "/v1/messages")
        self.assertEqual(payload["tool_choice"], {"type": "tool", "name": "detector_marker"})
        self.assertIn("input_schema", payload["tools"][0])

        path, payload = capability_probe(responses)
        self.assertEqual(path, "/v1/responses")
        self.assertEqual(payload["tool_choice"], {"type": "function", "name": "detector_marker"})
        self.assertEqual(payload["tools"][0]["type"], "function")

        path, payload = capability_probe(chat)
        self.assertEqual(path, "/v1/chat/completions")
        self.assertEqual(payload["tool_choice"]["function"]["name"], "detector_marker")
        self.assertIn("function", payload["tools"][0])

    def test_payload_evidence_recognizes_tool_structures_across_protocols(self) -> None:
        bodies = [
            {"choices": [{"message": {"tool_calls": [{"function": {"name": "detector_marker"}}]}}]},
            {"content": [{"type": "tool_use", "name": "detector_marker"}]},
            {"candidates": [{"content": {"parts": [{"functionCall": {"name": "detector_marker"}}]}}]},
        ]
        for body in bodies:
            response = ProbeResponse(200, 1, {}, "{}", body, "digest")
            evidence = payload_evidence("model_capability", response)
            self.assertTrue(any(item.category == "tool_structure" for item in evidence))

    def test_codex_subscription_relay_requires_exact_prompt_and_independent_behavior(self) -> None:
        route = route_for_model("gpt-5.6-sol", "openai", ["openai"])
        payload = {
            "model": route.model,
            "input": "Reply X.",
            "max_output_tokens": 16,
            "store": False,
        }
        instructions = "\n".join(
            [
                "You are Codex, a coding agent based on GPT-5. You and the user share one workspace.",
                "# Personality",
                "# Working with the user",
                "## Editing constraints",
                "## Autonomy and persistence",
                "X" * 5000,
            ]
        )
        response = ProbeResponse(
            200,
            1,
            {"via": "1.1 Caddy", "x-client-request-id": "uuid", "x-new-api-version": "v1"},
            "{}",
            {
                "instructions": instructions,
                "prompt_cache_key": "generated-cache-key",
                "safety_identifier": "generated-safety-id",
                "max_output_tokens": None,
                "usage": {"input_tokens": 4389},
            },
            "digest",
        )
        evidence = provenance_evidence("model_sync", route, payload, response)
        result = classify(evidence, "openai_official")
        self.assertEqual(result["likely_channel"], "codex_subscription_relay")
        self.assertEqual(result["verdict"], "suspected_substitution")
        self.assertEqual(result["confidence"], 0.98)
        prompt_detail = next(item.detail for item in evidence if item.category == "codex_prompt_fingerprint")
        self.assertNotIn(instructions, str(prompt_detail))
        self.assertEqual(prompt_detail["instruction_chars"], len(instructions))

    def test_generic_responses_payload_does_not_become_codex_relay(self) -> None:
        route = route_for_model("gpt-5.6-sol", "openai", ["openai"])
        payload = {"model": route.model, "input": "Reply X.", "max_output_tokens": 16}
        response = ProbeResponse(
            200,
            1,
            {"x-request-id": "req_official"},
            "{}",
            {
                "instructions": None,
                "max_output_tokens": 16,
                "usage": {"input_tokens": 8},
            },
            "digest",
        )
        evidence = provenance_evidence("model_sync", route, payload, response)
        self.assertFalse(any(item.supports == "codex_subscription_relay" for item in evidence))
        self.assertEqual(classify(evidence, "openai_official")["verdict"], "inconclusive")

    def test_codex_fingerprint_is_extracted_from_completed_sse_event(self) -> None:
        route = route_for_model("gpt-5.6-sol", "openai", ["openai"])
        payload = {
            "model": route.model,
            "input": "Reply X.",
            "max_output_tokens": 16,
            "stream": True,
            "store": False,
        }
        instructions = "\n".join(
            [
                "You are Codex, a coding agent based on GPT-5. You and the user share one workspace.",
                "# Personality",
                "# Working with the user",
                "## Editing constraints",
                "## Autonomy and persistence",
                "X" * 5000,
            ]
        )
        event = {
            "type": "response.completed",
            "response": {
                "instructions": instructions,
                "prompt_cache_key": "generated-cache-key",
                "safety_identifier": "generated-safety-id",
                "max_output_tokens": None,
                "usage": {"input_tokens": 4390},
            },
        }
        response = ProbeResponse(
            200,
            1,
            {"content-type": "text/event-stream"},
            "data: " + json.dumps(event) + "\n\n",
            None,
            "digest",
        )
        evidence = provenance_evidence("model_stream", route, payload, response)
        result = classify(evidence, "openai_official")
        self.assertEqual(result["likely_channel"], "codex_subscription_relay")
        self.assertEqual(result["confidence"], 0.98)
        detail = next(item.detail for item in evidence if item.category == "codex_prompt_fingerprint")
        self.assertTrue(detail["extracted_from_sse"])

    def test_claude_subscription_relay_requires_cache_and_proxy_headers(self) -> None:
        route = route_for_model("claude-fable-5", "claude", ["openai"])
        _, payload = capability_probe(route)
        body = {
            "usage": {
                "prompt_tokens": 6933,
                "usage_source": "anthropic",
                "billing_usage": {
                    "source": "claude_messages",
                    "claude_usage": {"input_tokens": 1248, "cache_creation_input_tokens": 5685},
                },
            }
        }
        with_headers = ProbeResponse(
            200,
            1,
            {"x-client-request-id": "uuid", "x-request-id": "req", "x-new-api-version": "v1"},
            "{}",
            body,
            "digest",
        )
        evidence = provenance_evidence("model_capability", route, payload, with_headers)
        result = classify(evidence, "anthropic_official")
        self.assertEqual(result["likely_channel"], "claude_subscription_relay")
        self.assertEqual(result["verdict"], "suspected_substitution")
        self.assertEqual(result["confidence"], 0.85)
        metadata = next(item for item in evidence if item.category == "gateway_translation_metadata")
        self.assertIsNone(metadata.supports)

        without_headers = ProbeResponse(200, 1, {}, "{}", body, "digest")
        evidence = provenance_evidence("model_capability", route, payload, without_headers)
        self.assertEqual(classify(evidence, "anthropic_official")["verdict"], "inconclusive")

    def test_native_claude_cache_creation_counts_toward_hidden_total_on_sync_probe(self) -> None:
        route = route_for_model("claude-opus-5", "claude", ["anthropic"])
        _, payload = route_probe(route, False)
        response = ProbeResponse(
            200,
            1,
            {"x-client-request-id": "uuid", "x-request-id": "req"},
            "{}",
            {
                "type": "message",
                "usage": {
                    "input_tokens": 1156,
                    "cache_creation_input_tokens": 6512,
                    "cache_read_input_tokens": 0,
                    "output_tokens": 1,
                },
            },
            "digest",
        )
        evidence = provenance_evidence("model_sync", route, payload, response)
        hidden = next(item for item in evidence if item.category == "claude_hidden_prompt_cache")
        self.assertEqual(hidden.detail["reported_total_input_tokens"], 7668)
        result = classify(evidence, "anthropic_official")
        self.assertEqual(result["likely_channel"], "claude_subscription_relay")
        self.assertEqual(result["verdict"], "suspected_substitution")

    def test_latest_native_claude_probe_uses_output_config_effort(self) -> None:
        route = route_for_model("claude-fable-5", "claude", ["anthropic"])
        _, payload = route_probe(route, False)
        self.assertEqual(payload["output_config"], {"effort": "low"})
        self.assertNotIn("effort", payload)

    def test_opus_4_8_uses_adaptive_thinking_controls(self) -> None:
        route = route_for_model("claude-opus-4-8", "claude", ["anthropic"])
        _, payload = capability_probe(route)
        self.assertEqual(payload["thinking"], {"type": "adaptive", "display": "omitted"})
        self.assertEqual(payload["output_config"], {"effort": "low"})

    def test_within_run_token_bimodality_overrides_single_route_guess(self) -> None:
        route = route_for_model("gpt-5.5", "openai", ["openai"])
        evidence = within_run_route_divergence_evidence(
            route,
            [
                {"probe": "model_sync", "input_tokens": 11, "hidden_instruction_chars": 0},
                {"probe": "model_capability", "input_tokens": 4430, "hidden_instruction_chars": 21334},
            ],
        )
        self.assertIsNotNone(evidence)
        result = classify(
            [
                evidence,
                Evidence("model_capability", "codex_prompt_fingerprint", "strong", "codex_subscription_relay", "x", {}),
            ],
            "openai_official",
        )
        self.assertEqual(result["likely_channel"], "heterogeneous_backend_pool")
        self.assertEqual(result["verdict"], "suspected_substitution")
        self.assertEqual(result["confidence"], 0.96)

    def test_gpt_image_2_uses_only_non_generation_validation_payloads(self) -> None:
        route = route_for_model("gpt-image-2", "openai", ["openai"])
        probes = image_validation_probes(route)
        self.assertEqual(len(probes), 3)
        self.assertEqual(probes[0][1], "/v1/images/generations")
        self.assertEqual(probes[0][2]["size"], "1x1")
        self.assertNotIn("prompt", probes[0][2])
        self.assertNotIn("prompt", probes[1][2])
        self.assertEqual(probes[2][1], "/v1/responses")


class ActiveModelProbeTests(unittest.IsolatedAsyncioTestCase):
    @staticmethod
    def response(status: int, body: dict | None = None, headers: dict[str, str] | None = None, text: str = "") -> ProbeResponse:
        return ProbeResponse(status, 1, headers or {}, text or "{}", body or {}, f"digest-{status}")

    async def test_gpt_image_2_run_uses_three_validation_responses_without_generation(self) -> None:
        route = route_for_model("gpt-image-2", "openai", ["openai"])
        requests = AsyncMock(
            side_effect=[
                self.response(400, {"error": {"type": "invalid_request_error", "code": "invalid_size"}}),
                self.response(400, {"error": {"type": "invalid_request_error", "code": "missing_prompt"}}),
                self.response(400, {"error": {"type": "invalid_request_error", "code": "unsupported_model"}}),
            ]
        )
        upstream = {
            "base_url": "https://relay.example/v1",
            "api_key_encrypted": "encrypted",
            "allow_paid_probes": 1,
        }
        with patch("engine.decrypt_secret", return_value="secret"), patch("engine.captured_request", requests):
            result = (await DetectorEngine().run_models(upstream, [route.to_dict()]))[0]

        self.assertEqual(result["protocol"], "openai_images")
        self.assertEqual(result["success_probes"], 3)
        self.assertEqual(result["planned_probes"], 3)
        self.assertEqual(result["verdict"], "inconclusive")
        payloads = [call.args[4] for call in requests.await_args_list]
        self.assertTrue(all(payload.get("size") == "1x1" or "prompt" not in payload for payload in payloads))

    async def test_responses_404_falls_back_once_then_uses_chat_for_remaining_probes(self) -> None:
        route = route_for_model("gpt-5.6-sol", "openai", ["openai"])
        requests = AsyncMock(
            side_effect=[
                self.response(404, {"error": {"type": "not_found"}}),
                self.response(200, {"id": "chatcmpl-sync"}),
                self.response(200, text="data: {\"id\":\"chatcmpl-stream\"}\n\n", headers={"content-type": "text/event-stream"}),
                self.response(200, {"choices": [{"message": {"tool_calls": []}}]}),
            ]
        )
        upstream = {
            "base_url": "https://relay.example/v1",
            "api_key_encrypted": "encrypted",
            "allow_paid_probes": 1,
        }
        with patch("engine.decrypt_secret", return_value="secret"), patch("engine.captured_request", requests):
            result = (await DetectorEngine().run_models(upstream, [route.to_dict()]))[0]

        urls = [call.args[2] for call in requests.await_args_list]
        self.assertEqual(urls[0], "https://relay.example/v1/responses")
        self.assertTrue(all(url == "https://relay.example/v1/chat/completions" for url in urls[1:]))
        self.assertEqual(result["protocol"], "openai_chat")
        self.assertEqual(result["planned_probes"], 3)
        self.assertEqual(result["success_probes"], 3)

    async def test_no_successful_model_request_remains_inconclusive(self) -> None:
        route = route_for_model("claude-fable-5", "claude", ["openai"])
        requests = AsyncMock(side_effect=[self.response(401), self.response(401), self.response(401), self.response(401)])
        upstream = {
            "base_url": "https://relay.example/v1",
            "api_key_encrypted": "encrypted",
            "allow_paid_probes": 1,
        }
        with patch("engine.decrypt_secret", return_value="secret"), patch("engine.captured_request", requests):
            result = (await DetectorEngine().run_models(upstream, [route.to_dict()]))[0]
        self.assertEqual(result["verdict"], "inconclusive")
        self.assertEqual(result["confidence"], 0.0)
        self.assertEqual(result["success_probes"], 0)
        self.assertEqual(result["planned_probes"], 4)

    async def test_successful_protocol_translation_does_not_guess_terminal_channel(self) -> None:
        route = route_for_model("claude-fable-5", "claude", ["openai"])
        requests = AsyncMock(
            side_effect=[
                self.response(200, {"id": "chatcmpl-sync"}),
                self.response(200, text="data: {\"id\":\"chatcmpl-stream\"}\n\n", headers={"content-type": "text/event-stream"}),
                self.response(200, {"choices": [{"message": {"tool_calls": []}}]}),
                self.response(404, {"error": {"type": "not_found"}}),
            ]
        )
        upstream = {
            "base_url": "https://relay.example/v1",
            "api_key_encrypted": "encrypted",
            "allow_paid_probes": 1,
        }
        with patch("engine.decrypt_secret", return_value="secret"), patch("engine.captured_request", requests):
            result = (await DetectorEngine().run_models(upstream, [route.to_dict()]))[0]
        self.assertEqual(result["verdict"], "inconclusive")
        self.assertEqual(result["likely_channel"], "unknown")
        self.assertIn("协议转换", result["summary"])
        self.assertEqual(result["chain"]["layers"][-1]["kind"], "unknown_terminal")
        self.assertTrue(any(layer["kind"] == "protocol_translation" for layer in result["chain"]["layers"]))

    async def test_native_claude_crosscheck_records_declared_protocol_conflict(self) -> None:
        route = route_for_model("claude-fable-5", "claude", ["openai"])
        requests = AsyncMock(
            side_effect=[
                self.response(200, {"id": "chatcmpl-sync"}),
                self.response(200, text="data: {\"id\":\"chatcmpl-stream\"}\n\n", headers={"content-type": "text/event-stream"}),
                self.response(200, {"choices": [{"message": {"tool_calls": []}}]}),
                self.response(200, {"id": "msg_native", "type": "message", "usage": {"input_tokens": 8, "output_tokens": 2}}),
            ]
        )
        upstream = {
            "base_url": "https://relay.example/v1",
            "api_key_encrypted": "encrypted",
            "allow_paid_probes": 1,
        }
        with patch("engine.decrypt_secret", return_value="secret"), patch("engine.captured_request", requests):
            result = (await DetectorEngine().run_models(upstream, [route.to_dict()]))[0]
        self.assertEqual(result["planned_probes"], 4)
        self.assertEqual(result["success_probes"], 4)
        self.assertTrue(any(item.category == "protocol_declaration_conflict" for item in result["evidence"]))
        self.assertTrue(any(layer["kind"] == "protocol_declaration_conflict" for layer in result["chain"]["layers"]))

    async def test_claude_capability_read_timeout_retries_once(self) -> None:
        route = route_for_model("claude-fable-5", "claude", ["openai"])
        requests = AsyncMock(
            side_effect=[
                self.response(200, {"id": "chatcmpl-sync"}),
                self.response(200, text="data: {\"id\":\"chatcmpl-stream\"}\n\n", headers={"content-type": "text/event-stream"}),
                httpx.ReadTimeout("slow capability"),
                self.response(200, {"choices": [{"message": {"tool_calls": []}}]}),
                self.response(404, {"error": {"type": "not_found"}}),
            ]
        )
        upstream = {
            "base_url": "https://relay.example/v1",
            "api_key_encrypted": "encrypted",
            "allow_paid_probes": 1,
        }
        with patch("engine.decrypt_secret", return_value="secret"), patch("engine.captured_request", requests):
            result = (await DetectorEngine().run_models(upstream, [route.to_dict()]))[0]
        self.assertEqual(requests.await_count, 5)
        self.assertTrue(any(item.category == "probe_retry" for item in result["evidence"]))
        self.assertEqual(result["success_probes"], 3)


class SecurityTests(unittest.TestCase):
    def setUp(self) -> None:
        self.previous = os.environ.get("DETECTOR_MASTER_KEY")
        os.environ["DETECTOR_MASTER_KEY"] = "unit-test-master-key-with-32-characters"

    def tearDown(self) -> None:
        if self.previous is None:
            os.environ.pop("DETECTOR_MASTER_KEY", None)
        else:
            os.environ["DETECTOR_MASTER_KEY"] = self.previous

    def test_secret_round_trip_and_mask(self) -> None:
        encrypted = encrypt_secret("sk-example-123456")
        self.assertNotIn("sk-example", encrypted)
        self.assertEqual(decrypt_secret(encrypted), "sk-example-123456")
        self.assertEqual(mask_secret("sk-example-123456"), "****3456")


if __name__ == "__main__":
    unittest.main()
