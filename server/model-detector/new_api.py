import hmac
import os
from typing import Any

import httpx


class NewAPIIntegrationError(RuntimeError):
    pass


class NewAPIClient:
    def __init__(self, timeout_seconds: float = 15.0) -> None:
        self.base_url = os.environ.get("DETECTOR_NEW_API_INTERNAL_URL", "").strip().rstrip("/")
        self.service_token = os.environ.get("DETECTOR_NEW_API_SERVICE_TOKEN", "").strip()
        self.timeout_seconds = timeout_seconds

    @property
    def configured(self) -> bool:
        return bool(self.base_url and len(self.service_token) >= 32)

    def _headers(self) -> dict[str, str]:
        if not self.configured:
            raise NewAPIIntegrationError("New API integration is not configured")
        return {
            "X-Model-Detector-Token": self.service_token,
            "Accept": "application/json",
        }

    async def _request(self, method: str, path: str, payload: dict[str, Any] | None = None) -> dict[str, Any]:
        try:
            async with httpx.AsyncClient(
                timeout=self.timeout_seconds,
                follow_redirects=False,
                verify=True,
            ) as client:
                response = await client.request(
                    method,
                    self.base_url + path,
                    headers=self._headers(),
                    json=payload,
                )
        except httpx.HTTPError as exc:
            raise NewAPIIntegrationError(f"New API internal request failed: {type(exc).__name__}") from exc
        try:
            body = response.json()
        except ValueError as exc:
            raise NewAPIIntegrationError(f"New API internal request returned HTTP {response.status_code} without JSON") from exc
        if response.status_code != 200 or not isinstance(body, dict) or body.get("success") is not True:
            message = body.get("message") if isinstance(body, dict) else ""
            raise NewAPIIntegrationError(
                f"New API internal request rejected: HTTP {response.status_code} {str(message)[:200]}".strip()
            )
        data = body.get("data")
        return data if isinstance(data, dict) else {"items": data}

    async def channels(self) -> list[dict[str, Any]]:
        data = await self._request("GET", "/api/model-detector/channels")
        items = data.get("channels", data.get("items", []))
        return [item for item in items if isinstance(item, dict)] if isinstance(items, list) else []

    async def disable_channel(self, channel_id: int, reason: str, run_id: int) -> dict[str, Any]:
        return await self._request(
            "POST",
            f"/api/model-detector/channels/{channel_id}/disable",
            {"reason": reason[:500], "run_id": run_id},
        )

    async def health(self) -> dict[str, Any]:
        return await self._request("GET", "/api/model-detector/health")


def constant_time_token_matches(left: str, right: str) -> bool:
    """Small shared test helper for contract fixtures; secrets are never returned or logged."""
    return bool(left and right) and hmac.compare_digest(left, right)
