#!/usr/bin/env python3
"""Create local recovery archives and publish encrypted off-site snapshots."""

from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import os
import shutil
import socket
import sqlite3
import subprocess
import tarfile
import tempfile
from datetime import datetime, timezone
from pathlib import Path

try:
    import fcntl
except ImportError:  # pragma: no cover - production runs on Linux
    fcntl = None


FORMAT_VERSION = 1
APP_ROOT = Path(os.environ.get("MADAPI_APP_ROOT", "/opt/madapi-new-api"))
DEFAULT_DB = Path(os.environ.get("MADAPI_DATABASE", str(APP_ROOT / "data/one-api.db")))
DEFAULT_LOCAL_DIR = Path(
    os.environ.get("MADAPI_LOCAL_BACKUP_DIR", str(APP_ROOT / "backups/disaster-recovery/hourly"))
)
DEFAULT_REPO = Path(os.environ.get("MADAPI_RECOVERY_REPO_DIR", "/opt/madapi-ops/private-backups"))
DEFAULT_PUBLIC_KEY = Path(
    os.environ.get("MADAPI_RECOVERY_PUBLIC_KEY", "/etc/madapi-ops/recovery-public.pem")
)
DEFAULT_LOCK = Path(
    os.environ.get("MADAPI_RECOVERY_LOCK", "/run/lock/madapi-ops-disaster-recovery.lock")
)

REQUIRED_FILES = (
    APP_ROOT / ".env",
    APP_ROOT / "docker-compose.yml",
)

def configured_optional_paths() -> tuple[Path, ...]:
    raw = os.environ.get("MADAPI_RECOVERY_OPTIONAL_PATHS", "")
    return tuple(Path(item.strip()) for item in raw.split(os.pathsep) if item.strip())


OPTIONAL_PATHS = configured_optional_paths()


def run(args: list[str], *, cwd: Path | None = None, input_bytes: bytes | None = None) -> bytes:
    result = subprocess.run(
        args,
        cwd=cwd,
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


def relative_target(path: Path) -> Path:
    return Path(str(path).lstrip("/"))


def copy_path(source: Path, root: Path) -> None:
    target = root / relative_target(source)
    target.parent.mkdir(parents=True, exist_ok=True)
    if source.is_dir():
        shutil.copytree(source, target, symlinks=False, dirs_exist_ok=True)
    else:
        shutil.copy2(source, target, follow_symlinks=True)


def snapshot_database(source: Path, target: Path) -> str:
    target.parent.mkdir(parents=True, exist_ok=True)
    source_db = sqlite3.connect(f"file:{source}?mode=ro", uri=True)
    target_db = sqlite3.connect(target)
    try:
        source_db.backup(target_db)
        result = target_db.execute("PRAGMA quick_check").fetchone()
    finally:
        target_db.close()
        source_db.close()
    integrity = str(result[0] if result else "missing")
    if integrity != "ok":
        raise RuntimeError(f"database integrity check failed: {integrity}")
    return integrity


def build_plain_archive(db_path: Path, output: Path, stamp: str) -> dict[str, object]:
    with tempfile.TemporaryDirectory(prefix="madapi-backup-stage-") as temp_name:
        stage = Path(temp_name)
        for required in REQUIRED_FILES:
            if not required.exists():
                raise RuntimeError(f"required recovery path is missing: {required}")
            copy_path(required, stage)

        db_target = stage / relative_target(db_path)
        integrity = snapshot_database(db_path, db_target)
        for optional in OPTIONAL_PATHS:
            if optional.exists():
                copy_path(optional, stage)

        files: list[dict[str, object]] = []
        for path in sorted(item for item in stage.rglob("*") if item.is_file()):
            files.append(
                {
                    "path": path.relative_to(stage).as_posix(),
                    "size": path.stat().st_size,
                    "sha256": sha256_file(path),
                }
            )

        manifest: dict[str, object] = {
            "format_version": FORMAT_VERSION,
            "created_at": stamp,
            "source_host": socket.gethostname(),
            "database_path": relative_target(db_path).as_posix(),
            "database_integrity": integrity,
            "files": files,
        }
        (stage / "manifest.json").write_text(
            json.dumps(manifest, ensure_ascii=True, indent=2) + "\n",
            encoding="utf-8",
        )
        output.parent.mkdir(parents=True, exist_ok=True)
        with tarfile.open(output, "w:gz", compresslevel=9) as archive:
            for item in sorted(stage.iterdir()):
                archive.add(item, arcname=item.name, recursive=True)
    os.chmod(output, 0o600)
    return manifest


def encrypt_archive(archive: Path, public_key: Path, bundle: Path, stamp: str) -> None:
    if not public_key.is_file():
        raise RuntimeError(f"recovery public key is missing: {public_key}")
    bundle.mkdir(parents=True, exist_ok=False)

    key_material = os.urandom(80)
    data_key = key_material[:32]
    mac_key = key_material[32:64]
    iv = key_material[64:]

    wrapped = bundle / "wrapped-key.bin"
    ciphertext = bundle / "archive.enc"
    run(
        [
            "openssl",
            "pkeyutl",
            "-encrypt",
            "-pubin",
            "-inkey",
            str(public_key),
            "-pkeyopt",
            "rsa_padding_mode:oaep",
            "-pkeyopt",
            "rsa_oaep_md:sha256",
            "-out",
            str(wrapped),
        ],
        input_bytes=key_material,
    )
    run(
        [
            "openssl",
            "enc",
            "-aes-256-ctr",
            "-K",
            data_key.hex(),
            "-iv",
            iv.hex(),
            "-in",
            str(archive),
            "-out",
            str(ciphertext),
        ]
    )

    authentication = hmac.new(mac_key, digestmod=hashlib.sha256)
    authentication.update(wrapped.read_bytes())
    with ciphertext.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            authentication.update(chunk)
    (bundle / "archive.hmac").write_text(authentication.hexdigest() + "\n", encoding="ascii")

    metadata = {
        "format_version": FORMAT_VERSION,
        "created_at": stamp,
        "cipher": "AES-256-CTR",
        "authentication": "HMAC-SHA256",
        "key_wrap": "RSA-OAEP-SHA256",
        "archive_size": ciphertext.stat().st_size,
        "archive_sha256": sha256_file(ciphertext),
        "wrapped_key_sha256": sha256_file(wrapped),
    }
    (bundle / "metadata.json").write_text(
        json.dumps(metadata, ensure_ascii=True, indent=2) + "\n",
        encoding="utf-8",
    )


def keep_newest_directories(parent: Path, keep: int) -> None:
    if not parent.exists():
        return
    entries = sorted((item for item in parent.iterdir() if item.is_dir()), key=lambda item: item.name)
    for old in entries[:-keep]:
        shutil.rmtree(old)


def keep_newest_files(parent: Path, keep: int) -> None:
    if not parent.exists():
        return
    entries = sorted((item for item in parent.iterdir() if item.is_file()), key=lambda item: item.name)
    for old in entries[:-keep]:
        old.unlink()


def publish_bundle(repo: Path, bundle: Path, stamp: str) -> None:
    if not (repo / ".git").is_dir():
        raise RuntimeError(f"private backup repository is not initialized: {repo}")

    branch_exists = subprocess.run(
        ["git", "ls-remote", "--exit-code", "--heads", "origin", "production-backups"],
        cwd=repo,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        check=False,
    ).returncode == 0
    if branch_exists:
        run(["git", "fetch", "origin", "production-backups"], cwd=repo)
        run(["git", "checkout", "-B", "production-backups", "origin/production-backups"], cwd=repo)
    else:
        run(["git", "checkout", "--orphan", "production-backups"], cwd=repo)
        for item in repo.iterdir():
            if item.name == ".git":
                continue
            if item.is_dir():
                shutil.rmtree(item)
            else:
                item.unlink()

    run(["git", "config", "user.name", "MadAPI Backup"], cwd=repo)
    run(["git", "config", "user.email", "backup@madapi.invalid"], cwd=repo)

    snapshots = repo / "snapshots"
    daily = repo / "daily"
    snapshots.mkdir(exist_ok=True)
    daily.mkdir(exist_ok=True)
    destination = snapshots / stamp.replace(":", "").replace("-", "")
    shutil.copytree(bundle, destination)

    latest = {"snapshot": destination.name, "created_at": stamp}
    (repo / "latest.json").write_text(json.dumps(latest, indent=2) + "\n", encoding="ascii")

    day = stamp[:10]
    day_target = daily / day
    if not day_target.exists():
        shutil.copytree(bundle, day_target)

    keep_newest_directories(snapshots, 28)
    keep_newest_directories(daily, 30)

    run(["git", "add", "-A"], cwd=repo)
    tree = run(["git", "write-tree"], cwd=repo).decode("ascii").strip()
    commit = run(
        ["git", "commit-tree", tree, "-m", f"Encrypted production backup {stamp}"],
        cwd=repo,
    ).decode("ascii").strip()
    run(["git", "reset", "--hard", commit], cwd=repo)
    run(["git", "push", "--force", "origin", f"{commit}:refs/heads/production-backups"], cwd=repo)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--database", type=Path, default=DEFAULT_DB)
    parser.add_argument("--local-dir", type=Path, default=DEFAULT_LOCAL_DIR)
    parser.add_argument("--repo", type=Path, default=DEFAULT_REPO)
    parser.add_argument("--public-key", type=Path, default=DEFAULT_PUBLIC_KEY)
    parser.add_argument("--lock", type=Path, default=DEFAULT_LOCK)
    parser.add_argument("--publish", action="store_true")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if fcntl is None:
        raise RuntimeError("production backup requires a Linux host")
    args.lock.parent.mkdir(parents=True, exist_ok=True)
    with args.lock.open("w", encoding="ascii") as lock:
        fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        now = datetime.now(timezone.utc).replace(microsecond=0)
        stamp = now.isoformat().replace("+00:00", "Z")
        filename = now.strftime("%Y%m%dT%H%M%SZ.tar.gz")
        archive = args.local_dir / filename
        build_plain_archive(args.database, archive, stamp)
        keep_newest_files(args.local_dir, 72)

        if args.publish:
            with tempfile.TemporaryDirectory(prefix="madapi-encrypted-backup-") as temp_name:
                bundle = Path(temp_name) / "bundle"
                encrypt_archive(archive, args.public_key, bundle, stamp)
                publish_bundle(args.repo, bundle, stamp)
        print(json.dumps({"status": "ok", "created_at": stamp, "published": args.publish}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
