import os
import tempfile
import unittest

from engine import Evidence, body_shape, classify, endpoint, model_alias_evidence, sanitize_headers, transport_evidence
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
