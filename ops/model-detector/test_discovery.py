import asyncio
import unittest
from unittest.mock import AsyncMock, Mock, patch

from discovery import api_endpoint, discover_models, parse_model_inventory, route_for_model, validate_public_api_url


class DiscoveryTests(unittest.TestCase):
    def test_versioned_base_preserves_images_route(self) -> None:
        self.assertEqual(
            api_endpoint("https://relay.example/api/v1", "/v1/images/generations"),
            "https://relay.example/api/v1/images/generations",
        )
        self.assertEqual(
            api_endpoint("https://relay.example/api/v1", "/api/pricing"),
            "https://relay.example/api/pricing",
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

    def test_only_requested_gemini_and_gpt_image_2_are_kept(self) -> None:
        self.assertIsNone(route_for_model("gemini-3.1-pro", "google gemini", ["gemini", "openai"]))
        gemini = route_for_model("gemini-3.6-flash", "google gemini", ["gemini", "openai"])
        self.assertEqual(gemini.family, "google")
        self.assertEqual(gemini.protocol, "gemini_generate")
        self.assertEqual(gemini.endpoint, "/v1beta/models/gemini-3.6-flash:generateContent")
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
                {"id": "gemini-3.6-flash", "owned_by": "google", "supported_endpoint_types": ["gemini"]},
                {"id": "unrelated-model", "owned_by": "openai", "supported_endpoint_types": ["openai"]},
            ]
        }
        routes = parse_model_inventory(payload, "openai_models")
        self.assertEqual([item.model for item in routes], ["claude-fable-5", "gpt-5.6-sol", "gpt-image-2", "gemini-3.6-flash"])

    def test_openai_inventory_source_is_used_when_gateway_omits_endpoint_hints(self) -> None:
        routes = parse_model_inventory({"data": [{"id": "claude-fable-5", "owned_by": "claude"}]}, "openai_models")
        self.assertEqual(routes[0].protocol, "openai_chat")
        self.assertEqual(routes[0].supported_endpoint_types, ["openai"])

    def test_public_pricing_inventory_discovers_only_scoped_models(self) -> None:
        routes = parse_model_inventory(
            {
                "data": [
                    {"model_name": "gemini-3.6-flash", "supported_endpoint_types": ["openai"]},
                    {"model_name": "gemini-3.5-flash", "supported_endpoint_types": ["openai"]},
                ]
            },
            "new_api_pricing",
        )
        self.assertEqual([item.model for item in routes], ["gemini-3.6-flash"])
        self.assertEqual(routes[0].protocol, "openai_chat")

    def test_anthropic_inventory_source_selects_native_messages_without_endpoint_hints(self) -> None:
        routes = parse_model_inventory({"data": [{"id": "claude-opus-5", "type": "model"}]}, "anthropic_models")
        self.assertEqual(routes[0].protocol, "anthropic_messages")
        self.assertEqual(routes[0].supported_endpoint_types, ["anthropic"])

    def test_discovery_merges_openai_and_gemini_capabilities_before_routing(self) -> None:
        responses = {
            "/v1/models": {
                "data": [{"id": "gemini-3.6-flash", "owned_by": "google"}],
            },
            "/v1beta/models": {
                "models": [{"name": "models/gemini-3.6-flash"}],
            },
        }

        async def fake_get(url, headers):
            path = "/" + url.split("/", 3)[-1]
            response = Mock()
            response.status_code = 200
            response.headers = {"content-type": "application/json"}
            response.json.return_value = (
                {"data": []}
                if "anthropic-version" in headers
                else responses.get(path, {"data": []})
            )
            return response

        client = AsyncMock()
        client.get.side_effect = fake_get
        client.__aenter__.return_value = client
        client.__aexit__.return_value = None
        with patch("discovery.httpx.AsyncClient", return_value=client):
            result = asyncio.run(discover_models("https://relay.example", "temporary"))
        self.assertEqual(len(result["models"]), 1)
        route = result["models"][0]
        self.assertEqual(route["protocol"], "gemini_generate")
        self.assertEqual(route["supported_endpoint_types"], ["gemini", "openai"])

    def test_private_resolution_is_rejected(self) -> None:
        with patch("discovery.socket.getaddrinfo", return_value=[(2, 1, 6, "", ("127.0.0.1", 443))]):
            with self.assertRaises(ValueError):
                validate_public_api_url("https://relay.example/v1")


if __name__ == "__main__":
    unittest.main()
