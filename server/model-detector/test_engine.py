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
    anthropic_contract_evidence,
    anthropic_contract_probe_specs,
    body_shape,
    antigravity_alias_evidence,
    antigravity_alias_matrix_evidence,
    antigravity_alias_probe,
    antigravity_alias_probe_specs,
    capability_probe,
    classify,
    claude_system_preservation_evidence,
    claude_system_preservation_probe,
    cliproxyapi_header_fingerprint,
    endpoint,
    gpt_cross_protocol_evidence,
    gpt_cross_protocol_probe_specs,
    gemini_contract_evidence,
    gemini_contract_probe_specs,
    image_validation_probes,
    implementation_evidence,
    model_alias_evidence,
    observed_chain,
    openai_contract_evidence,
    openai_contract_probe_specs,
    payload_evidence,
    provenance_evidence,
    route_probe,
    sanitize_error_message,
    sanitize_headers,
    transport_evidence,
    within_run_route_divergence_evidence,
    zero_success_summary,
)
from security import decrypt_secret, encrypt_secret, mask_secret


class EngineTests(unittest.TestCase):
    def test_endpoint_preserves_existing_v1(self) -> None:
        self.assertEqual(endpoint("https://relay.example/v1", "/v1/models"), "https://relay.example/v1/models")
        self.assertEqual(endpoint("https://relay.example", "/v1/models"), "https://relay.example/v1/models")

    def test_sensitive_headers_are_removed(self) -> None:
        headers = sanitize_headers({"Authorization": "Bearer secret", "X-Request-Id": "req_1", "Api-Key": "secret"})
        self.assertEqual(headers, {"x-request-id": "req_1"})

    def test_cliproxyapi_headers_are_an_exact_implementation_fingerprint(self) -> None:
        direct = cliproxyapi_header_fingerprint(
            {"x-cpa-trace-id": "20260729171232-eb53a98a245608bb-abd329cc"}
        )
        self.assertIsNotNone(direct)
        self.assertTrue(direct["trace_format_valid"])
        exposed = cliproxyapi_header_fingerprint(
            {"access-control-expose-headers": "X-CPA-TRACE-ID, X-CPA-VERSION, X-CPA-COMMIT"}
        )
        self.assertIn("x-cpa-version", exposed["cors_expose_markers"])

    def test_cliproxyapi_evidence_does_not_store_trace_value(self) -> None:
        route = route_for_model("gpt-5.6-sol", "openai", ["openai"])
        response = ProbeResponse(
            200,
            1,
            {"x-cpa-trace-id": "20260729171232-eb53a98a245608bb-abd329cc"},
            "{}",
            {},
            "digest",
        )
        evidence = implementation_evidence("model_sync", route, response)
        self.assertEqual(evidence[0].category, "cliproxyapi_implementation")
        self.assertNotIn("eb53a98a245608bb", json.dumps(evidence[0].detail))

    def test_gpt_cross_protocol_matrix_identifies_codex_translation(self) -> None:
        route = route_for_model("gpt-5.6-sol", "openai", ["openai"])
        chat = ProbeResponse(
            200,
            1,
            {"x-cpa-trace-id": "20260729171232-eb53a98a245608bb-abd329cc"},
            "{}",
            {"object": "chat.completion", "usage": {"prompt_tokens": 4388}},
            "chat-digest",
        )
        anthropic = ProbeResponse(
            200,
            1,
            {},
            "{}",
            {
                "type": "message",
                "usage": {
                    "input_tokens": 4388,
                    "billing_usage": {"source": "oai_chat", "semantic": "openai"},
                },
            },
            "anthropic-digest",
        )
        evidence = gpt_cross_protocol_evidence(
            route,
            {"gpt_cross_protocol_chat": chat, "gpt_cross_protocol_anthropic": anthropic},
        )
        matrix = next(item for item in evidence if item.category == "multi_protocol_codex_translation")
        self.assertEqual(matrix.strength, "strong")
        self.assertEqual(matrix.supports, "codex_subscription_relay")
        self.assertTrue(matrix.detail["token_counts_match"])

    def test_cliproxyapi_layer_increases_confirmed_chain_lower_bound(self) -> None:
        route = route_for_model("gpt-5.6-sol", "openai", ["openai"])
        evidence = [
            Evidence("model", "observation", "info", None, "headers", {"headers": {"x-oneapi-request-id": "outer"}}),
            Evidence("model", "cliproxyapi_implementation", "strong", "codex_subscription_relay", "CPA", {}),
            Evidence("model", "multi_protocol_codex_translation", "strong", "codex_subscription_relay", "translation", {}),
        ]
        chain = observed_chain(
            evidence,
            route,
            {"verdict": "probable_alternate_channel", "likely_channel": "codex_subscription_relay", "confidence": 0.99},
        )
        self.assertIn("cliproxyapi", [item["kind"] for item in chain["layers"]])
        self.assertEqual(chain["minimum_confirmed_hops"], 3)

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

    def test_error_message_is_useful_but_credentials_are_redacted(self) -> None:
        message = sanitize_error_message("model route failed; api_key=sk-secret-value-123456789012345")
        self.assertIn("model route failed", message)
        self.assertNotIn("sk-secret-value", message)

    def test_disabled_organization_error_is_localized(self) -> None:
        message = sanitize_error_message("This organization has been disabled. (request id: req_1)")
        self.assertEqual(message, "该上游组织账户已被禁用")

    def test_zero_success_summary_explains_rejected_probes(self) -> None:
        responses = [
            ProbeResponse(
                400,
                10,
                {},
                "{}",
                {"error": {"message": "model mapping not found"}},
                "digest",
            )
            for _ in range(8)
        ]
        summary = zero_success_summary(responses, 8, [])
        self.assertIn("HTTP 400×8", summary)
        self.assertIn("model mapping not found", summary)

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

    def test_claude_compatibility_layer_never_claims_to_be_final_source(self) -> None:
        route = route_for_model("claude-opus-4-8", "claude", ["anthropic"])
        evidence = [
            Evidence(
                "anthropic_contract_matrix",
                "claude_request_contract_rewrite",
                "strong",
                "claude_compatibility_relay",
                "contract rewrite",
                {"bypassed_count": 3},
            )
        ]
        chain = observed_chain(
            evidence,
            route,
            {"verdict": "suspected_substitution", "likely_channel": "claude_compatibility_relay", "confidence": 0.94},
        )
        self.assertIn("claude_compatibility_relay", [item["kind"] for item in chain["layers"]])
        terminal = next(item for item in chain["layers"] if item["position"] == "terminal")
        self.assertEqual(terminal["kind"], "unknown_terminal")
        self.assertIn("Bedrock", next(item for item in chain["layers"] if item["kind"] == "claude_compatibility_relay")["note"])

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

    def test_openai_contract_matrix_is_rule_driven(self) -> None:
        route = route_for_model("gpt-5.6-sol", "openai", ["openai"])
        probes = openai_contract_probe_specs(route)
        self.assertEqual(len(probes), 4)
        names = [name for name, _, _ in probes]
        self.assertEqual(
            names,
            [
                "openai_invalid_prompt_cache_retention",
                "openai_invalid_safety_identifier_type",
                "openai_invalid_prompt_cache_key_type",
                "openai_below_minimum_output_tokens",
            ],
        )
        payloads = {name: payload for name, payload, _ in probes}
        self.assertEqual(payloads["openai_below_minimum_output_tokens"]["max_output_tokens"], 1)
        self.assertFalse(payloads["openai_invalid_safety_identifier_type"]["store"])

    def test_openai_contract_matrix_detects_bulk_validation_bypass(self) -> None:
        route = route_for_model("gpt-5.6-sol", "openai", ["openai"])
        observations = []
        for index, (_, _, spec) in enumerate(openai_contract_probe_specs(route)):
            response = ProbeResponse(
                200,
                1,
                {},
                "{}",
                {
                    "model": route.model,
                    "prompt_cache_key": "generated",
                    "prompt_cache_retention": "24h",
                    "safety_identifier": "generated",
                    "usage": {"input_tokens": 304, "output_tokens": 5},
                },
                f"digest-{index}",
            )
            observations.append((spec, response))
        evidence = openai_contract_evidence(observations)
        signature = next(item for item in evidence if item.category == "request_contract_rewrite")
        self.assertEqual(signature.strength, "strong")
        self.assertEqual(signature.supports, "codex_subscription_relay")
        self.assertEqual(signature.detail["bypassed_count"], 4)
        sensitive_rows = [
            item
            for item in signature.detail["bypassed"]
            if item["field"] in {"prompt_cache_key", "safety_identifier"}
        ]
        self.assertTrue(all(item["returned_field_value"] is None for item in sensitive_rows))
        self.assertTrue(all(item["returned_field_sha256"] for item in sensitive_rows))

    def test_openai_contract_match_does_not_prove_direct_channel(self) -> None:
        route = route_for_model("gpt-5.6-sol", "openai", ["openai"])
        observations = []
        for index, (_, _, spec) in enumerate(openai_contract_probe_specs(route)):
            response = ProbeResponse(
                int(spec["expected_status"]),
                1,
                {},
                "{}",
                {
                    "error": {
                        "code": spec["expected_error_code"],
                        "type": "invalid_request_error",
                        "param": spec["expected_error_param"],
                    }
                },
                f"digest-{index}",
            )
            observations.append((spec, response))
        evidence = openai_contract_evidence(observations)
        self.assertEqual(len(evidence), 4)
        self.assertTrue(all(item.category == "official_contract_match" for item in evidence))
        self.assertTrue(all(item.supports is None for item in evidence))

    def test_gemini_36_flash_contract_matrix_is_scoped_and_non_generating(self) -> None:
        route = route_for_model("gemini-3.6-flash", "google", ["gemini"])
        probes = gemini_contract_probe_specs(route)
        self.assertEqual([item[0] for item in probes], [
            "gemini_count_tokens",
            "gemini_invalid_zero_output",
            "gemini_invalid_unknown_field",
            "gemini_invalid_safety_category",
            "gemini_missing_contents",
        ])
        self.assertTrue(probes[0][1].endswith(":countTokens"))
        self.assertEqual(probes[0][2]["contents"][0]["parts"][0]["text"], "X")
        self.assertNotIn("generationConfig", probes[0][2])
        self.assertEqual(probes[1][2]["generationConfig"]["maxOutputTokens"], 0)
        safety_probe = next(item for item in probes if item[0] == "gemini_invalid_safety_category")
        self.assertEqual(
            safety_probe[2]["safetySettings"][0]["category"],
            "HARM_CATEGORY_MODEL_DETECTOR_SENTINEL",
        )
        openai_only = route_for_model("gemini-3.6-flash", "google", ["openai"])
        self.assertEqual(len(gemini_contract_probe_specs(openai_only)), len(probes))
        self.assertIsNone(route_for_model("gemini-3.1-pro", "google", ["gemini"]))

    def test_gemini_official_contract_match_never_proves_direct_channel(self) -> None:
        route = route_for_model("gemini-3.6-flash", "google", ["gemini"])
        observations = []
        for index, (_, _, _, spec) in enumerate(gemini_contract_probe_specs(route)):
            if spec.get("expected_response_key"):
                body = {spec["expected_response_key"]: 1}
            else:
                body = {"error": {"code": 400, "status": spec["expected_error_status"]}}
            observations.append((spec, ProbeResponse(int(spec["expected_status"]), 1, {}, "{}", body, f"g-{index}")))
        evidence = gemini_contract_evidence(observations)
        self.assertEqual(len(evidence), 5)
        self.assertTrue(all(item.category == "official_contract_match" for item in evidence))
        self.assertTrue(all(item.supports is None for item in evidence))
        self.assertEqual(classify(evidence, "gemini_developer_api")["verdict"], "inconclusive")

    def test_gemini_bulk_validation_bypass_confirms_compatibility_relay(self) -> None:
        route = route_for_model("gemini-3.6-flash", "google", ["gemini"])
        observations = []
        for index, (_, _, _, spec) in enumerate(gemini_contract_probe_specs(route)):
            status = 200
            body = {"totalTokens": 1} if spec.get("expected_response_key") else {"candidates": [], "usageMetadata": {}}
            observations.append((spec, ProbeResponse(status, 1, {}, "{}", body, f"b-{index}")))
        evidence = gemini_contract_evidence(observations)
        signature = next(item for item in evidence if item.category == "gemini_request_contract_rewrite")
        self.assertEqual(signature.detail["bypassed_count"], 4)
        verdict = classify(evidence, "gemini_developer_api")
        self.assertEqual(verdict["verdict"], "suspected_substitution")
        self.assertEqual(verdict["likely_channel"], "gemini_compatibility_relay")
        self.assertEqual(verdict["confidence"], 0.94)

    def test_google_chat_probe_exposes_deleted_output_limit(self) -> None:
        route = route_for_model("gemini-3.6-flash", "google", ["openai"])
        path, payload = route_probe(route)
        self.assertEqual(path, "/v1/chat/completions")
        self.assertEqual(payload["max_tokens"], 1)
        self.assertIn("twelve space-separated tokens", payload["messages"][0]["content"])
        response = ProbeResponse(
            200,
            1,
            {},
            "{}",
            {"choices": [{"message": {"content": "A B C D E F G H I J K L"}}], "usage": {"completion_tokens": 12}},
            "gemini-limit-digest",
        )
        evidence = provenance_evidence("model_sync", route, payload, response)
        rewrite = next(item for item in evidence if item.category == "gemini_max_token_rewrite")
        self.assertEqual(rewrite.supports, "gemini_compatibility_relay")
        self.assertEqual(rewrite.detail["requested_output_tokens"], 1)
        self.assertEqual(rewrite.detail["reported_output_tokens"], 12)

    def test_antigravity_hidden_alias_plus_cpa_headers_is_implementation_level_proof(self) -> None:
        route = route_for_model("gemini-3.6-flash", "google", ["openai"])
        path, payload, rule = antigravity_alias_probe(route)
        self.assertEqual(path, "/v1/chat/completions")
        self.assertEqual(payload["model"], "gemini-3.6-flash-high")
        response = ProbeResponse(
            200,
            1,
            {"x-cpa-trace-id": "20260729123456-node-trace"},
            "{}",
            {"model": "gemini-3.6-flash", "choices": [{"message": {"content": "X"}}]},
            "antigravity-alias-digest",
        )
        evidence = implementation_evidence("antigravity_hidden_alias", route, response)
        evidence.extend(antigravity_alias_evidence(route, response, rule))
        verdict = classify(evidence, "gemini_developer_api")
        self.assertEqual(verdict["verdict"], "suspected_substitution")
        self.assertEqual(verdict["likely_channel"], "antigravity_subscription_relay")
        self.assertEqual(verdict["confidence"], 0.99)

    def test_omniroute_antigravity_disclosure_plus_rewrite_is_terminal_proof(self) -> None:
        route = route_for_model("gemini-3.6-flash", "google", ["openai"])
        path, payload = route_probe(route)
        self.assertEqual(path, "/v1/chat/completions")
        response = ProbeResponse(
            200,
            1,
            {
                "x-omniroute-provider": "antigravity",
                "x-omniroute-model": "gemini-3.6-flash-medium",
                "x-omniroute-version": "3.8.49",
                "x-omniroute-decision": "strategy=single; provider=antigravity; latency_ms=1034",
            },
            "{}",
            {
                "model": "antigravity/gemini-3.6-flash-medium",
                "choices": [{"message": {"content": "A B C D E F G H I J K L"}}],
                "usage": {"completion_tokens": 12},
            },
            "omniroute-antigravity-digest",
        )
        evidence = provenance_evidence("model_sync", route, payload, response)
        disclosure = next(item for item in evidence if item.category == "omniroute_provider_disclosure")
        self.assertEqual(disclosure.supports, "antigravity_subscription_relay")
        self.assertEqual(disclosure.detail["routed_model"], "gemini-3.6-flash-medium")
        self.assertEqual(disclosure.detail["response_model"], "antigravity/gemini-3.6-flash-medium")
        verdict = classify(evidence, "gemini_developer_api")
        self.assertEqual(verdict["verdict"], "suspected_substitution")
        self.assertEqual(verdict["likely_channel"], "antigravity_subscription_relay")
        self.assertEqual(verdict["confidence"], 0.99)
        chain = observed_chain(evidence, route, verdict)
        self.assertIn("omniroute", [layer["kind"] for layer in chain["layers"]])
        self.assertEqual(chain["minimum_confirmed_hops"], 3)

    def test_omniroute_antigravity_disclosure_alone_is_not_absolute_proof(self) -> None:
        route = route_for_model("gemini-3.6-flash", "google", ["openai"])
        response = ProbeResponse(
            200,
            1,
            {"x-omniroute-provider": "antigravity", "x-omniroute-model": "gemini-3.6-flash-medium"},
            "{}",
            {"model": "antigravity/gemini-3.6-flash-medium", "choices": []},
            "omniroute-disclosure-only",
        )
        evidence = implementation_evidence("model_sync", route, response)
        verdict = classify(evidence, "gemini_developer_api")
        self.assertEqual(verdict["likely_channel"], "antigravity_subscription_relay")
        self.assertEqual(verdict["confidence"], 0.97)

    def test_antigravity_tier_alias_matrix_requires_multiple_successes(self) -> None:
        route = route_for_model("gemini-3.6-flash", "google", ["openai"])
        specs = antigravity_alias_probe_specs(route)
        self.assertEqual(
            [item[2]["model"] for item in specs],
            [
                "gemini-3.6-flash-high",
                "gemini-3.6-flash-medium",
                "gemini-3.6-flash-low",
                "gemini-3.6-flash-tiered",
            ],
        )
        observations = []
        for index, (_, _, payload, rule) in enumerate(specs):
            status = 200 if index < 3 else 404
            response = ProbeResponse(
                status,
                1,
                {},
                "{}",
                {"model": "gemini-3.6-flash"} if status == 200 else {"error": {"type": "not_found"}},
                f"matrix-{index}",
            )
            observations.append((rule, response))
        evidence = antigravity_alias_matrix_evidence(observations)
        self.assertEqual(len(evidence), 1)
        self.assertEqual(evidence[0].category, "antigravity_tier_alias_matrix")
        self.assertEqual(evidence[0].detail["successful_count"], 3)
        self.assertEqual(evidence[0].detail["suffix_success_count"], 3)
        self.assertFalse(evidence[0].detail["tiered_alias_succeeded"])

        only_one = antigravity_alias_matrix_evidence(observations[:1])
        self.assertEqual(only_one, [])

    def test_claude_system_nonce_return_excludes_oauth_sanitization_for_that_request(self) -> None:
        route = route_for_model("claude-fable-5", "anthropic", ["anthropic"])
        path, payload, nonce = claude_system_preservation_probe(route)
        self.assertEqual(path, "/v1/messages")
        self.assertIn(nonce, payload["system"])
        response = ProbeResponse(
            200,
            1,
            {},
            "{}",
            {
                "content": [{"type": "text", "text": nonce}],
                "usage": {"input_tokens": 20, "cache_creation_input_tokens": 4000},
            },
            "claude-preserved",
        )
        evidence = claude_system_preservation_evidence(route, response, nonce)
        self.assertEqual(evidence[0].category, "system_preservation_observation")
        self.assertIsNone(evidence[0].supports)
        self.assertTrue(evidence[0].detail["nonce_returned"])
        self.assertNotIn(nonce, json.dumps(evidence[0].detail))

    def test_claude_missing_system_nonce_plus_hidden_cache_and_cpa_confirms_oauth_cloak(self) -> None:
        route = route_for_model("claude-opus-5", "anthropic", ["anthropic"])
        _, _, nonce = claude_system_preservation_probe(route)
        response = ProbeResponse(
            200,
            1,
            {"x-cpa-trace-id": "20260729123456-node-trace"},
            "{}",
            {
                "content": [{"type": "text", "text": "I cannot see a verification nonce."}],
                "usage": {
                    "input_tokens": 1156,
                    "cache_creation_input_tokens": 6512,
                    "cache_read_input_tokens": 0,
                },
            },
            "claude-oauth-cloak",
        )
        evidence = implementation_evidence("claude_system_preservation", route, response)
        evidence.extend(claude_system_preservation_evidence(route, response, nonce))
        signature = next(item for item in evidence if item.category == "claude_oauth_system_sanitization")
        self.assertFalse(signature.detail["nonce_returned"])
        self.assertNotIn(nonce, json.dumps(signature.detail))
        verdict = classify(evidence, "anthropic_official")
        self.assertEqual(verdict["verdict"], "suspected_substitution")
        self.assertEqual(verdict["likely_channel"], "claude_subscription_relay")
        self.assertEqual(verdict["confidence"], 0.99)

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

    def test_anthropic_contract_specs_use_non_generation_validation_edges(self) -> None:
        route = route_for_model("claude-fable-5", "claude", ["anthropic"])
        specs = anthropic_contract_probe_specs(route)
        self.assertEqual(len(specs), 4)
        by_name = {name: payload for name, payload, _ in specs}
        self.assertTrue(by_name["anthropic_unknown_field"]["model_detector_invalid"])
        self.assertIsInstance(by_name["anthropic_invalid_metadata"]["metadata"]["user_id"], dict)
        self.assertEqual(by_name["anthropic_middle_system_role"]["messages"][1]["role"], "system")
        self.assertEqual(by_name["anthropic_max_tokens_zero"]["max_tokens"], 0)

    def test_anthropic_contract_matrix_detects_batch_rewrite(self) -> None:
        route = route_for_model("claude-fable-5", "claude", ["anthropic"])
        specs = anthropic_contract_probe_specs(route)
        observations = [
            (specs[0][2], ProbeResponse(200, 1, {}, "{}", {"type": "message"}, "a")),
            (specs[1][2], ProbeResponse(200, 1, {}, "{}", {"type": "message"}, "b")),
            (
                specs[2][2],
                ProbeResponse(
                    400,
                    1,
                    {},
                    "{}",
                    {"error": {"type": "invalid_request_error", "message": "roles invalid"}},
                    "c",
                ),
            ),
            (
                specs[3][2],
                ProbeResponse(200, 1, {}, "{}", {"usage": {"output_tokens": 0}}, "d"),
            ),
        ]
        evidence = anthropic_contract_evidence(observations)
        matrix = next(item for item in evidence if item.category == "claude_request_contract_rewrite")
        self.assertEqual(matrix.strength, "strong")
        self.assertEqual(matrix.detail["bypassed_count"], 2)
        verdict = classify(evidence, "anthropic_official")
        self.assertEqual(verdict["verdict"], "suspected_substitution")
        self.assertEqual(verdict["likely_channel"], "claude_compatibility_relay")
        self.assertEqual(verdict["confidence"], 0.94)

    def test_repeated_same_category_cannot_inflate_or_override_stronger_contract(self) -> None:
        evidence = [
            Evidence(
                f"relay_headers_{index}",
                "relay_headers",
                "medium",
                "claude_subscription_relay",
                "same repeated header family",
                {},
            )
            for index in range(8)
        ]
        evidence.append(
            Evidence(
                "anthropic_contract_matrix",
                "claude_request_contract_rewrite",
                "strong",
                "claude_compatibility_relay",
                "contract rewrite",
                {"bypassed_count": 3},
            )
        )
        verdict = classify(evidence, "anthropic_official")
        self.assertEqual(verdict["verdict"], "suspected_substitution")
        self.assertEqual(verdict["likely_channel"], "claude_compatibility_relay")
        self.assertEqual(verdict["confidence"], 0.94)

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

    def test_claude_cache_tokens_and_adapter_metadata_detect_heterogeneous_pool(self) -> None:
        route = route_for_model("claude-fable-5", "claude", ["anthropic"])
        adapter_evidence = within_run_route_divergence_evidence(
            route,
            [
                {
                    "probe": "model_sync",
                    "total_input_tokens": 8,
                    "billing_source": None,
                    "billing_semantic": None,
                    "response_object": "message",
                    "hidden_instruction_chars": 0,
                },
                {
                    "probe": "claude_system_preservation",
                    "total_input_tokens": 187,
                    "billing_source": "oai_chat",
                    "billing_semantic": "openai",
                    "response_object": "message",
                    "hidden_instruction_chars": 0,
                },
            ],
        )
        self.assertIsNotNone(adapter_evidence)
        self.assertTrue(adapter_evidence.detail["adapter_path_divergence"])
        self.assertFalse(adapter_evidence.detail["token_path_divergence"])
        self.assertEqual(classify([adapter_evidence], "anthropic_official")["likely_channel"], "heterogeneous_backend_pool")

        cache_evidence = within_run_route_divergence_evidence(
            route,
            [
                {"probe": "model_sync", "total_input_tokens": 8, "response_object": "message"},
                {
                    "probe": "claude_system_preservation",
                    "total_input_tokens": 7448,
                    "cache_creation_input_tokens": 6370,
                    "response_object": "message",
                },
            ],
        )
        self.assertIsNotNone(cache_evidence)
        self.assertTrue(cache_evidence.detail["token_path_divergence"])
        self.assertEqual(cache_evidence.detail["input_token_ratio"], 931.0)

    def test_claude_implicit_cache_and_openai_translation_are_explicit_evidence(self) -> None:
        route = route_for_model("claude-opus-5", "claude", ["anthropic"])
        _, payload = capability_probe(route)
        response = ProbeResponse(
            200,
            1,
            {},
            "{}",
            {
                "type": "message",
                "usage": {
                    "input_tokens": 16,
                    "cache_creation_input_tokens": 80,
                    "cache_read_input_tokens": 0,
                    "output_tokens": 1,
                },
            },
            "cache-digest",
        )
        evidence = provenance_evidence("model_capability", route, payload, response)
        injected = next(item for item in evidence if item.category == "request_cache_control_injection")
        self.assertEqual(injected.detail["cache_creation_input_tokens"], 80)

        translated = ProbeResponse(
            200,
            1,
            {},
            "{}",
            {
                "type": "message",
                "usage": {
                    "input_tokens": 58,
                    "output_tokens": 1,
                    "billing_usage": {"source": "oai_chat", "semantic": "openai"},
                },
            },
            "translation-digest",
        )
        translated_evidence = provenance_evidence("model_sync", route, {"model": route.model}, translated)
        result = classify(translated_evidence, "anthropic_official")
        self.assertEqual(result["likely_channel"], "claude_compatibility_relay")
        self.assertEqual(result["verdict"], "suspected_substitution")
        self.assertEqual(result["confidence"], 0.96)

    def test_gpt_image_2_uses_only_non_generation_validation_payloads(self) -> None:
        route = route_for_model("gpt-image-2", "openai", ["openai"])
        probes = image_validation_probes(route)
        self.assertEqual(len(probes), 2)
        self.assertEqual(probes[0][1], "/v1/images/generations")
        self.assertEqual(probes[0][2]["size"], "1x1")
        self.assertNotIn("prompt", probes[0][2])
        self.assertNotIn("prompt", probes[1][2])
        self.assertTrue(all(item[1] == "/v1/images/generations" for item in probes))


class ActiveModelProbeTests(unittest.IsolatedAsyncioTestCase):
    @staticmethod
    def response(status: int, body: dict | None = None, headers: dict[str, str] | None = None, text: str = "") -> ProbeResponse:
        return ProbeResponse(status, 1, headers or {}, text or "{}", body or {}, f"digest-{status}")

    async def test_gpt_image_2_run_uses_two_validation_responses_without_generation(self) -> None:
        route = route_for_model("gpt-image-2", "openai", ["openai"])
        progress_events = []
        requests = AsyncMock(
            side_effect=[
                self.response(400, {"error": {"type": "invalid_request_error", "code": "invalid_size"}}),
                self.response(400, {"error": {"type": "invalid_request_error", "code": "missing_prompt"}}),
            ]
        )
        upstream = {
            "base_url": "https://relay.example/v1",
            "api_key_encrypted": "encrypted",
            "allow_paid_probes": 1,
        }
        with patch("engine.decrypt_secret", return_value="secret"), patch("engine.captured_request", requests):
            result = (
                await DetectorEngine().run_models(
                    upstream,
                    [route.to_dict()],
                    progress_callback=progress_events.append,
                )
            )[0]

        self.assertEqual(result["protocol"], "openai_images")
        self.assertEqual(result["success_probes"], 2)
        self.assertEqual(result["planned_probes"], 2)
        self.assertEqual(result["verdict"], "inconclusive")
        payloads = [call.args[4] for call in requests.await_args_list]
        self.assertTrue(all(payload.get("size") == "1x1" or "prompt" not in payload for payload in payloads))
        self.assertEqual(progress_events[0]["phase"], "model_probes")
        self.assertEqual(progress_events[-1]["phase"], "model_completed")
        self.assertEqual(progress_events[-1]["current"], 1)
        self.assertEqual(progress_events[-1]["total"], 1)

    async def test_responses_404_falls_back_once_then_uses_chat_for_remaining_probes(self) -> None:
        route = route_for_model("gpt-5.6-sol", "openai", ["openai"])
        requests = AsyncMock(
            side_effect=[
                self.response(200, {"object": "chat.completion", "usage": {"prompt_tokens": 300}}),
                self.response(200, {"type": "message", "usage": {"input_tokens": 300}}),
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
        self.assertEqual(urls[:2], ["https://relay.example/v1/chat/completions", "https://relay.example/v1/messages"])
        self.assertEqual(urls[2], "https://relay.example/v1/responses")
        self.assertTrue(all(url == "https://relay.example/v1/chat/completions" for url in urls[3:]))
        self.assertEqual(result["protocol"], "openai_chat")
        self.assertEqual(result["planned_probes"], 3)
        self.assertEqual(result["success_probes"], 3)

    async def test_no_successful_model_request_remains_inconclusive(self) -> None:
        route = route_for_model("claude-fable-5", "claude", ["openai"])
        requests = AsyncMock(
            side_effect=[
                self.response(401),
                self.response(401),
                self.response(401),
                self.response(401),
                self.response(401),
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
        self.assertEqual(result["confidence"], 0.0)
        self.assertEqual(result["success_probes"], 0)
        self.assertEqual(result["planned_probes"], 5)

    async def test_successful_protocol_translation_does_not_guess_terminal_channel(self) -> None:
        route = route_for_model("claude-fable-5", "claude", ["openai"])
        requests = AsyncMock(
            side_effect=[
                self.response(200, {"id": "chatcmpl-sync"}),
                self.response(200, text="data: {\"id\":\"chatcmpl-stream\"}\n\n", headers={"content-type": "text/event-stream"}),
                self.response(200, {"choices": [{"message": {"tool_calls": []}}]}),
                self.response(404, {"error": {"type": "not_found"}}),
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
                self.response(404, {"error": {"type": "not_found"}}),
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
        self.assertEqual(result["planned_probes"], 5)
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
        self.assertEqual(requests.await_count, 6)
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
