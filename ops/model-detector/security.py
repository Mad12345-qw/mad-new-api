import base64
import hashlib
import hmac
import os
import time

from cryptography.fernet import Fernet, InvalidToken


class SecurityError(RuntimeError):
    pass


def _required_env(name: str, min_length: int = 1) -> str:
    value = os.environ.get(name, "").strip()
    if not value:
        raise SecurityError(f"required environment variable {name} is missing")
    if len(value) < min_length:
        raise SecurityError(f"required environment variable {name} must contain at least {min_length} characters")
    return value


def build_fernet() -> Fernet:
    raw = _required_env("DETECTOR_MASTER_KEY", 32).encode("utf-8")
    key = base64.urlsafe_b64encode(hashlib.sha256(raw).digest())
    return Fernet(key)


def encrypt_secret(value: str) -> str:
    return build_fernet().encrypt(value.encode("utf-8")).decode("ascii")


def decrypt_secret(value: str) -> str:
    try:
        return build_fernet().decrypt(value.encode("ascii")).decode("utf-8")
    except InvalidToken as exc:
        raise SecurityError("stored secret cannot be decrypted with the configured master key") from exc


def mask_secret(value: str) -> str:
    if not value:
        return ""
    suffix = value[-4:] if len(value) >= 4 else value[-1:]
    return f"****{suffix}"


def verify_admin_token(value: str) -> bool:
    expected = os.environ.get("DETECTOR_ADMIN_TOKEN", "")
    return bool(expected) and hmac.compare_digest(value, expected)


def validate_runtime_secrets() -> None:
    _required_env("DETECTOR_MASTER_KEY", 32)
    _required_env("DETECTOR_ADMIN_TOKEN", 32)


def make_session(now: int | None = None) -> str:
    timestamp = int(now or time.time())
    payload = str(timestamp).encode("ascii")
    key = _required_env("DETECTOR_ADMIN_TOKEN", 32).encode("utf-8")
    signature = hmac.new(key, payload, hashlib.sha256).hexdigest()
    return f"{timestamp}.{signature}"


def verify_session(value: str, max_age_seconds: int = 43200) -> bool:
    try:
        timestamp_text, signature = value.split(".", 1)
        timestamp = int(timestamp_text)
    except (AttributeError, ValueError):
        return False
    now = int(time.time())
    if timestamp > now + 60 or now - timestamp > max_age_seconds:
        return False
    key = os.environ.get("DETECTOR_ADMIN_TOKEN", "").encode("utf-8")
    if not key:
        return False
    expected = hmac.new(key, timestamp_text.encode("ascii"), hashlib.sha256).hexdigest()
    return hmac.compare_digest(signature, expected)
