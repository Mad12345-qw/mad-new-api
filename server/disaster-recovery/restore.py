#!/usr/bin/env python3
"""Decrypt and verify a MadAPI production recovery bundle."""

from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import os
import sqlite3
import subprocess
import tarfile
import tempfile
from pathlib import Path


def run(args: list[str], *, input_bytes: bytes | None = None) -> bytes:
    result = subprocess.run(
        args,
        input=input_bytes,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if result.returncode != 0:
        message = result.stderr.decode("utf-8", errors="replace").strip()
        raise RuntimeError(f"command failed ({args[0]}): {message}")
    return result.stdout


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def safe_extract(archive: tarfile.TarFile, destination: Path) -> None:
    root = destination.resolve()
    for member in archive.getmembers():
        target = (destination / member.name).resolve()
        if target != root and root not in target.parents:
            raise RuntimeError(f"unsafe archive member: {member.name}")
        if member.issym() or member.islnk():
            raise RuntimeError(f"links are not allowed in recovery archives: {member.name}")
    archive.extractall(destination)


def decrypt_bundle(bundle: Path, private_key: Path, archive: Path) -> None:
    metadata = json.loads((bundle / "metadata.json").read_text(encoding="utf-8"))
    if metadata.get("format_version") != 1:
        raise RuntimeError("unsupported recovery bundle format")
    wrapped = bundle / "wrapped-key.bin"
    ciphertext = bundle / "archive.enc"
    if sha256_file(ciphertext) != metadata.get("archive_sha256"):
        raise RuntimeError("encrypted archive checksum mismatch")
    if sha256_file(wrapped) != metadata.get("wrapped_key_sha256"):
        raise RuntimeError("wrapped key checksum mismatch")

    key_material = run(
        [
            "openssl",
            "pkeyutl",
            "-decrypt",
            "-inkey",
            str(private_key),
            "-pkeyopt",
            "rsa_padding_mode:oaep",
            "-pkeyopt",
            "rsa_oaep_md:sha256",
            "-in",
            str(wrapped),
        ]
    )
    if len(key_material) != 80:
        raise RuntimeError("invalid decrypted key material")
    data_key = key_material[:32]
    mac_key = key_material[32:64]
    iv = key_material[64:]

    authentication = hmac.new(mac_key, digestmod=hashlib.sha256)
    authentication.update(wrapped.read_bytes())
    with ciphertext.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            authentication.update(chunk)
    expected = (bundle / "archive.hmac").read_text(encoding="ascii").strip()
    if not hmac.compare_digest(authentication.hexdigest(), expected):
        raise RuntimeError("recovery bundle authentication failed")

    run(
        [
            "openssl",
            "enc",
            "-d",
            "-aes-256-ctr",
            "-K",
            data_key.hex(),
            "-iv",
            iv.hex(),
            "-in",
            str(ciphertext),
            "-out",
            str(archive),
        ]
    )


def verify_extracted(root: Path) -> dict[str, object]:
    manifest_path = root / "manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    if manifest.get("format_version") != 1:
        raise RuntimeError("unsupported archive manifest format")
    for record in manifest.get("files", []):
        path = root / str(record["path"])
        if not path.is_file():
            raise RuntimeError(f"recovery file is missing: {record['path']}")
        if path.stat().st_size != int(record["size"]):
            raise RuntimeError(f"recovery file size mismatch: {record['path']}")
        if sha256_file(path) != record["sha256"]:
            raise RuntimeError(f"recovery file checksum mismatch: {record['path']}")

    database = root / "opt/new-api/data/one-api.db"
    connection = sqlite3.connect(f"file:{database}?mode=ro", uri=True)
    try:
        result = connection.execute("PRAGMA quick_check").fetchone()
    finally:
        connection.close()
    if not result or result[0] != "ok":
        raise RuntimeError("recovered database integrity check failed")
    return manifest


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("bundle", type=Path)
    parser.add_argument("--private-key", type=Path, required=True)
    parser.add_argument("--output", type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if os.geteuid() == 0:
        os.umask(0o077)
    with tempfile.TemporaryDirectory(prefix="madapi-restore-") as temp_name:
        archive = Path(temp_name) / "recovery.tar.gz"
        decrypt_bundle(args.bundle, args.private_key, archive)
        if args.output:
            args.output.mkdir(parents=True, exist_ok=True)
            destination = args.output
            with tarfile.open(archive, "r:gz") as handle:
                safe_extract(handle, destination)
            manifest = verify_extracted(destination)
        else:
            destination = Path(temp_name) / "extracted"
            destination.mkdir()
            with tarfile.open(archive, "r:gz") as handle:
                safe_extract(handle, destination)
            manifest = verify_extracted(destination)
        print(
            json.dumps(
                {
                    "status": "ok",
                    "created_at": manifest.get("created_at"),
                    "files": len(manifest.get("files", [])),
                    "output": str(args.output) if args.output else None,
                }
            )
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
