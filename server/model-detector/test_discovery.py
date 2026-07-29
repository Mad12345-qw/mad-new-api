import unittest
from unittest.mock import patch

from discovery import api_endpoint, parse_model_inventory, route_for_model, validate_public_api_url


class DiscoveryTests(unittest.TestCase):
    def test_versioned_base_preserves_images_route(self) -> None:
        self.assertEqual(
            api_endpoint("https://relay.example/api/v1", "/v1/images/generations"),
            "https://relay.example/api/v1/images/generations",
        )

    def test_claude_uses_declared_openai_translation_automatically(self) -> None:
        route = route_for_model("claude-fable-5", "claude", ["openai"])
        self.assertIsNotNone(route)
        self.assertEqual(route.family, "anthropic")
        self.assertEqual(route.protocol, "openai_chat")
        self.assertIn("协议转换层", route.route_reason)

    def test_claude_prefers_native_messages_when_advertised(self) -> None:
        route = route_for_model("claude-opus-5", "claude", ["openai", "anthropic"])
        self.assertEqual(route.protocol, "anthropic_messages")
        self.assertEqual(route.endpoint, "/v1/messages")

    def test_latest_openai_model_prefers_responses(self) -> None:
        route = route_for_model("gpt-5.6-sol", "openai", ["openai"])
        self.assertEqual(route.protocol, "openai_responses")
        self.assertIn("openai_chat", route.fallbacks)

    def test_gemini_is_out_of_scope_and_only_gpt_image_2_is_kept(self) -> None:
        self.assertIsNone(route_for_model("gemini-3.1-pro", "google gemini", ["gemini", "openai"]))
        image = route_for_model("gpt-image-2", "openai", ["openai"])
        self.assertEqual(image.protocol, "openai_images")
        self.assertEqual(image.endpoint, "/v1/images/generations")
        self.assertIsNone(route_for_model("gpt-image-2-4k", "openai", ["openai"]))

    def test_openai_inventory_is_filtered_and_keeps_endpoint_hints(self) -> None:
        payload = {
            "data": [
                {"id": "claude-fable-5", "owned_by": "claude", "supported_endpoint_types": ["openai"]},
                {"id": "gpt-5.6-sol", "owned_by": "openai", "supported_endpoint_types": ["openai"]},
                {"id": "gpt-image-2", "owned_by": "openai", "supported_endpoint_types": ["openai"]},
                {"id": "gpt-image-2-4k", "owned_by": "openai", "supported_endpoint_types": ["openai"]},
                {"id": "gemini-3.1-pro", "owned_by": "google", "supported_endpoint_types": ["gemini"]},
                {"id": "unrelated-model", "owned_by": "custom", "supported_endpoint_types": ["openai"]},
            ]
        }
        routes = parse_model_inventory(payload, "openai_models")
        self.assertEqual([item.model for item in routes], ["claude-fable-5", "gpt-5.6-sol", "gpt-image-2"])

    def test_openai_inventory_source_is_used_when_gateway_omits_endpoint_hints(self) -> None:
        routes = parse_model_inventory({"data": [{"id": "claude-fable-5", "owned_by": "claude"}]}, "openai_models")
        self.assertEqual(routes[0].protocol, "openai_chat")
        self.assertEqual(routes[0].supported_endpoint_types, ["openai"])

    def test_anthropic_inventory_source_selects_native_messages_without_endpoint_hints(self) -> None:
        routes = parse_model_inventory({"data": [{"id": "claude-opus-5", "type": "model"}]}, "anthropic_models")
        self.assertEqual(routes[0].protocol, "anthropic_messages")
        self.assertEqual(routes[0].supported_endpoint_types, ["anthropic"])

    def test_private_resolution_is_rejected(self) -> None:
        with patch("discovery.socket.getaddrinfo", return_value=[(2, 1, 6, "", ("127.0.0.1", 443))]):
            with self.assertRaises(ValueError):
                validate_public_api_url("https://relay.example/v1")


if __name__ == "__main__":
    unittest.main()
