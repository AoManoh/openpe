from __future__ import annotations

import argparse
import hashlib
import json
import os
import platform
import re
import secrets
import shutil
import struct
import tempfile
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, List, Optional, Sequence
from urllib.parse import urlparse

from . import __version__
from .backup_transaction import _path_key
from .bundle import _atomic_write, _durable_mkdir, checksum, has_marker, inject
from .checksum import (
    ChecksumError,
    patch_product_json,
    verify_bundle_checksum,
    verify_trusted_baseline,
    vscode_checksum,
)
from .handshake import read_descriptor, validate_profile_cors, verify_server
from .locking import mutation_lock
from .paths import IDEPaths, resolve_paths
from .processes import ProcessError, require_host_stopped
from .profiles import DEVIN_PROFILE, require_profile


SESSIONS_RELPATH = "vs/sessions/sessions.desktop.main.js"
SESSIONS_HTML_RELPATH = "vs/sessions/electron-browser/sessions.html"
WORKBENCH_RELPATH = "vs/workbench/workbench.desktop.main.js"
TRUSTED_SESSIONS_SHA256 = "594ddc67fd1d19962a8b5e47851f0a076f67c38e1312a7c526c88683c672eef5"
TRUSTED_SESSIONS_HTML_SHA256 = "13b1b5583229d7ca1797bf917d60f6e42a26776b0b2331c7fded71ea1ee06523"
TRUSTED_DEVIN_EXE_SHA256 = "b98b7638153362ca1a57541c74427fa8b3ec522cf656247bd151acc324f1911a"


class MultiPatchError(Exception):
    pass


@dataclass(frozen=True)
class Artifact:
    name: str
    path: Path
    relpath: str
    backup_name: str
    before_sha256: str
    target_sha256: str
    target: bytes


def _timestamp() -> str:
    return datetime.now(tz=timezone.utc).isoformat().replace("+00:00", "Z")


def _transaction_root(paths: IDEPaths, transaction_id: str) -> Path:
    base = paths.backup_dir.parent.parent.parent.expanduser().resolve()
    root = (
        base
        / "multi-transactions"
        / paths.profile.profile_id
        / paths.install_id
        / _path_key(paths.product.commit)
        / _path_key(transaction_id)
    ).resolve()
    try:
        contained = os.path.commonpath((str(base), str(root))) == str(base)
    except ValueError as exc:
        raise MultiPatchError(f"invalid transaction path: {exc}") from exc
    if not contained:
        raise MultiPatchError("multi transaction path escapes backup root")
    return root


def _manifest_bytes(value: Dict[str, Any]) -> bytes:
    return json.dumps(value, ensure_ascii=False, indent=2).encode("utf-8") + b"\n"


def _sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def _conditional_write(path: Path, expected: str, target: bytes) -> None:
    current = path.read_bytes()
    if _sha256_bytes(current) != expected:
        raise MultiPatchError(f"live file changed before write: {path}")
    _atomic_write(path, target, mode=0o644)
    if checksum(path) != _sha256_bytes(target):
        raise MultiPatchError(f"write verification failed: {path}")


def _exact_server_origin(base_url: str) -> str:
    parsed = urlparse(base_url)
    try:
        port = parsed.port
    except ValueError as exc:
        raise MultiPatchError(f"invalid server port: {exc}") from exc
    if (
        parsed.scheme != "http"
        or parsed.hostname != "127.0.0.1"
        or port is None
        or parsed.path not in {"", "/"}
        or parsed.params
        or parsed.query
        or parsed.fragment
        or parsed.username
        or parsed.password
    ):
        raise MultiPatchError(
            "exact-build Devin patch requires http://127.0.0.1:<port>"
        )
    return f"http://127.0.0.1:{port}"


def _verify_host_executable(paths: IDEPaths) -> None:
    executable = paths.app_root / "Devin.exe"
    try:
        value = executable.read_bytes()
        pe_offset = struct.unpack_from("<I", value, 0x3C)[0]
        machine = struct.unpack_from("<H", value, pe_offset + 4)[0]
    except (OSError, IndexError, struct.error) as exc:
        raise MultiPatchError(f"cannot verify Devin executable: {exc}") from exc
    if _sha256_bytes(value) != TRUSTED_DEVIN_EXE_SHA256 or machine != 0x8664:
        raise MultiPatchError("Devin executable is not the trusted Windows x64 baseline")


def _patch_sessions_csp(value: bytes, server_origin: str) -> bytes:
    text = value.decode("utf-8")
    if server_origin in text:
        return value
    patched, count = re.subn(
        r"(connect-src\s+'self')",
        rf"\1\n\t\t\t\t\t\t{server_origin}",
        text,
        count=1,
    )
    if count != 1:
        raise MultiPatchError("sessions.html connect-src anchor not found")
    return patched.encode("utf-8")


def _build_targets(
    paths: IDEPaths,
    payload: str,
    transaction_id: str,
) -> List[Artifact]:
    descriptor = read_descriptor()
    info = verify_server(descriptor)
    validate_profile_cors(info, DEVIN_PROFILE.cors_origins, DEVIN_PROFILE.display_name)
    app = paths.app_root / "resources" / "app"
    sessions = app / "out" / "vs" / "sessions" / "sessions.desktop.main.js"
    sessions_html = app / "out" / "vs" / "sessions" / "electron-browser" / "sessions.html"
    workbench = paths.bundle_file
    product = paths.product_file
    for candidate in (sessions, sessions_html, workbench, product):
        if not candidate.is_file():
            raise MultiPatchError(f"required artifact missing: {candidate}")
    if platform.system() != "Windows" or platform.machine().casefold() not in {
        "amd64",
        "x86_64",
    }:
        raise MultiPatchError("exact-build Devin patch requires Windows x64")
    _verify_host_executable(paths)
    trusted = paths.profile.supported_build(platform.system(), paths.product)
    if trusted is None:
        raise MultiPatchError("Devin build is not trusted")
    verify_trusted_baseline(
        product,
        workbench,
        trusted.product_sha256,
        trusted.bundle_sha256,
        WORKBENCH_RELPATH,
    )
    if checksum(sessions) != TRUSTED_SESSIONS_SHA256:
        raise MultiPatchError("sessions bundle is not the trusted baseline")
    if checksum(sessions_html) != TRUSTED_SESSIONS_HTML_SHA256:
        raise MultiPatchError("sessions HTML is not the trusted baseline")
    server_origin = _exact_server_origin(descriptor.base_url)
    config = {
        "baseUrl": server_origin,
        "token": descriptor.token,
        "version": descriptor.version or "unknown",
        "hostProfileId": DEVIN_PROFILE.profile_id,
        "productCommit": paths.product.commit,
        "transactionId": transaction_id,
        "client": DEVIN_PROFILE.client,
        "mode": DEVIN_PROFILE.mode,
        "historySource": "none",
        "patchKind": "devin-exact-multi-v1",
        "multiBundlePatch": True,
    }
    prelude = (
        "/* === OPENPE-BOOTSTRAP === */\n"
        f"globalThis.__openpe = {json.dumps(config, ensure_ascii=False)};\n"
    )
    target_dir = Path(tempfile.mkdtemp(prefix="openpe-multi-target-"))
    try:
        target_sessions = target_dir / "sessions.desktop.main.js"
        target_workbench = target_dir / "workbench.desktop.main.js"
        target_html = target_dir / "sessions.html"
        target_product = target_dir / "product.json"
        target_sessions.write_bytes(sessions.read_bytes())
        target_workbench.write_bytes(workbench.read_bytes())
        target_html.write_bytes(
            _patch_sessions_csp(sessions_html.read_bytes(), server_origin)
        )
        target_product.write_bytes(product.read_bytes())
        inject(target_sessions, prelude + payload, expected_sha256=checksum(sessions))
        inject(target_workbench, prelude + payload, expected_sha256=checksum(workbench))
        for relpath, target in (
            (SESSIONS_RELPATH, target_sessions),
            (WORKBENCH_RELPATH, target_workbench),
            (SESSIONS_HTML_RELPATH, target_html),
        ):
            patch_product_json(
                target_product,
                bundle_relpath=relpath,
                new_value=vscode_checksum(target),
                expected_sha256=checksum(target_product),
            )
            verify_bundle_checksum(target_product, target, relpath)
        values = (
            (
                "sessions",
                sessions,
                SESSIONS_RELPATH,
                "sessions.original",
                target_sessions,
                TRUSTED_SESSIONS_SHA256,
            ),
            (
                "workbench",
                workbench,
                WORKBENCH_RELPATH,
                "workbench.original",
                target_workbench,
                trusted.bundle_sha256,
            ),
            (
                "sessions_html",
                sessions_html,
                SESSIONS_HTML_RELPATH,
                "sessions-html.original",
                target_html,
                TRUSTED_SESSIONS_HTML_SHA256,
            ),
            (
                "product",
                product,
                "product.json",
                "product.original",
                target_product,
                trusted.product_sha256,
            ),
        )
        return [
            Artifact(
                name=name,
                path=path,
                relpath=relpath,
                backup_name=backup_name,
                before_sha256=before_sha256,
                target_sha256=checksum(target),
                target=target.read_bytes(),
            )
            for name, path, relpath, backup_name, target, before_sha256 in values
        ]
    finally:
        shutil.rmtree(target_dir, ignore_errors=True)


def _write_manifest(root: Path, manifest: Dict[str, Any]) -> None:
    _atomic_write(root / "manifest.json", _manifest_bytes(manifest), mode=0o600)


def _rollback_known(artifacts: List[Artifact], root: Path) -> None:
    live = {artifact.name: checksum(artifact.path) for artifact in artifacts}
    for artifact in artifacts:
        if live[artifact.name] not in {
            artifact.before_sha256,
            artifact.target_sha256,
        }:
            raise MultiPatchError(
                f"unknown live state; rollback refused: {artifact.path}"
            )
    for artifact in reversed(artifacts):
        backup = (root / artifact.backup_name).read_bytes()
        if _sha256_bytes(backup) != artifact.before_sha256:
            raise MultiPatchError(f"backup checksum mismatch: {artifact.backup_name}")
        _conditional_write(artifact.path, live[artifact.name], backup)


def install(app_dir: Path, payload_path: Path) -> str:
    paths = resolve_paths(override=str(app_dir))
    if paths is None or paths.product is None:
        raise MultiPatchError("cannot resolve IDE")
    profile = require_profile(paths.product, "devin")
    if profile != DEVIN_PROFILE:
        raise MultiPatchError("target is not Devin Desktop")
    require_host_stopped(profile)
    if has_marker(paths.bundle_file):
        raise MultiPatchError("workbench is already injected")
    transaction_id = (
        datetime.now(tz=timezone.utc).strftime("%Y%m%dT%H%M%S%fZ")
        + "-"
        + secrets.token_hex(4)
    )
    payload = payload_path.read_text(encoding="utf-8")
    artifacts = _build_targets(paths, payload, transaction_id)
    root = _transaction_root(paths, transaction_id)
    if root.exists():
        raise MultiPatchError(f"transaction already exists: {root}")
    with mutation_lock(paths):
        require_host_stopped(profile)
        if root.exists():
            raise MultiPatchError(f"transaction already exists: {root}")
        _durable_mkdir(root, mode=0o700)
        for artifact in artifacts:
            live = artifact.path.read_bytes()
            if _sha256_bytes(live) != artifact.before_sha256:
                raise MultiPatchError(f"artifact changed before backup: {artifact.path}")
            backup = root / artifact.backup_name
            _atomic_write(backup, live, mode=0o600)
            if checksum(backup) != artifact.before_sha256:
                raise MultiPatchError(f"backup verification failed: {artifact.path}")
        manifest: Dict[str, Any] = {
            "schema_version": 1,
            "state": "prepared",
            "transaction_id": transaction_id,
            "profile_id": profile.profile_id,
            "install_id": paths.install_id,
            "app_root": str(paths.app_root),
            "product_commit": paths.product.commit,
            "installer_version": __version__,
            "payload_sha256": checksum(payload_path),
            "artifacts": [
                {
                    "name": artifact.name,
                    "path": str(artifact.path),
                    "relpath": artifact.relpath,
                    "backup_name": artifact.backup_name,
                    "before_sha256": artifact.before_sha256,
                    "target_sha256": artifact.target_sha256,
                }
                for artifact in artifacts
            ],
            "created_at": _timestamp(),
            "updated_at": _timestamp(),
        }
        _write_manifest(root, manifest)
        print(
            f"openpe-multi-patch: prepared transaction {transaction_id}",
            flush=True,
        )
        try:
            for artifact in artifacts:
                _conditional_write(
                    artifact.path,
                    artifact.before_sha256,
                    artifact.target,
                )
            for artifact in artifacts:
                if checksum(artifact.path) != artifact.target_sha256:
                    raise MultiPatchError(f"target verification failed: {artifact.path}")
        except Exception:
            _rollback_known(artifacts, root)
            raise
        manifest["state"] = "installed"
        manifest["updated_at"] = _timestamp()
        try:
            _write_manifest(root, manifest)
        except OSError as exc:
            raise MultiPatchError(
                "files were installed but the final manifest commit failed; "
                f"recover with --restore {transaction_id}: {exc}"
            ) from exc
    return transaction_id


def restore(app_dir: Path, transaction_id: str) -> None:
    paths = resolve_paths(override=str(app_dir))
    if paths is None or paths.product is None or paths.profile != DEVIN_PROFILE:
        raise MultiPatchError("cannot resolve Devin transaction target")
    require_host_stopped(paths.profile)
    root = _transaction_root(paths, transaction_id)
    manifest_path = root / "manifest.json"
    try:
        manifest_bytes = manifest_path.read_bytes()
        manifest = json.loads(manifest_bytes.decode("utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise MultiPatchError(f"cannot read multi transaction: {exc}") from exc
    manifest_sha256 = _sha256_bytes(manifest_bytes)
    if not isinstance(manifest, dict):
        raise MultiPatchError("multi transaction manifest must be an object")
    expected_identity = (
        (manifest.get("schema_version"), 1, "schema"),
        (manifest.get("transaction_id"), transaction_id, "transaction"),
        (manifest.get("profile_id"), DEVIN_PROFILE.profile_id, "profile"),
        (manifest.get("install_id"), paths.install_id, "install"),
        (manifest.get("app_root"), str(paths.app_root), "app root"),
        (manifest.get("product_commit"), paths.product.commit, "product commit"),
    )
    for current, expected, label in expected_identity:
        if current != expected:
            raise MultiPatchError(f"multi transaction {label} mismatch")
    if manifest.get("state") not in {"prepared", "installed", "restoring"}:
        raise MultiPatchError("multi transaction is not restorable")
    app = paths.app_root / "resources" / "app"
    expected_paths = {
        "sessions": app / "out" / "vs" / "sessions" / "sessions.desktop.main.js",
        "workbench": paths.bundle_file,
        "sessions_html": app / "out" / "vs" / "sessions" / "electron-browser" / "sessions.html",
        "product": paths.product_file,
    }
    values = manifest.get("artifacts")
    if not isinstance(values, list) or len(values) != len(expected_paths):
        raise MultiPatchError("multi transaction artifact set is invalid")
    if platform.system() != "Windows" or platform.machine().casefold() not in {
        "amd64",
        "x86_64",
    }:
        raise MultiPatchError("exact-build Devin restore requires Windows x64")
    _verify_host_executable(paths)
    trusted = paths.profile.supported_build(platform.system(), paths.product)
    if trusted is None:
        raise MultiPatchError("restored Devin build is not trusted")
    trusted_before = {
        "sessions": TRUSTED_SESSIONS_SHA256,
        "workbench": trusted.bundle_sha256,
        "sessions_html": TRUSTED_SESSIONS_HTML_SHA256,
        "product": trusted.product_sha256,
    }
    artifacts: List[Artifact] = []
    seen = set()
    for value in values:
        if not isinstance(value, dict):
            raise MultiPatchError("multi transaction artifact is invalid")
        name = value.get("name")
        if name not in expected_paths or name in seen:
            raise MultiPatchError("multi transaction artifact identity is invalid")
        seen.add(name)
        path = Path(str(value.get("path", ""))).resolve()
        if path != expected_paths[name].resolve():
            raise MultiPatchError(f"multi transaction artifact path mismatch: {name}")
        before_sha = str(value.get("before_sha256", ""))
        target_sha = str(value.get("target_sha256", ""))
        backup_name = str(value.get("backup_name", ""))
        if not re.fullmatch(r"[0-9a-f]{64}", before_sha) or not re.fullmatch(
            r"[0-9a-f]{64}", target_sha
        ):
            raise MultiPatchError("multi transaction artifact checksum is invalid")
        if before_sha != trusted_before[name]:
            raise MultiPatchError(f"multi transaction trusted baseline mismatch: {name}")
        if backup_name != {
            "sessions": "sessions.original",
            "workbench": "workbench.original",
            "sessions_html": "sessions-html.original",
            "product": "product.original",
        }[name]:
            raise MultiPatchError("multi transaction backup identity is invalid")
        artifacts.append(
            Artifact(
                name=name,
                path=path,
                relpath=str(value.get("relpath", "")),
                backup_name=backup_name,
                before_sha256=before_sha,
                target_sha256=target_sha,
                target=b"",
            )
        )
    with mutation_lock(paths):
        require_host_stopped(paths.profile)
        if checksum(manifest_path) != manifest_sha256:
            raise MultiPatchError("multi transaction manifest changed before restore")
        live = {artifact.name: checksum(artifact.path) for artifact in artifacts}
        recovery_bytes: Dict[str, bytes] = {}
        for artifact in artifacts:
            if live[artifact.name] not in {
                artifact.before_sha256,
                artifact.target_sha256,
            }:
                raise MultiPatchError(f"unknown live artifact state: {artifact.name}")
            backup = root / artifact.backup_name
            value = backup.read_bytes()
            if _sha256_bytes(value) != artifact.before_sha256:
                raise MultiPatchError(
                    f"multi transaction backup mismatch: {artifact.name}"
                )
            recovery_bytes[artifact.name] = value
        manifest["state"] = "restoring"
        manifest["updated_at"] = _timestamp()
        _write_manifest(root, manifest)
        for artifact in reversed(artifacts):
            _conditional_write(
                artifact.path,
                live[artifact.name],
                recovery_bytes[artifact.name],
            )
        for artifact in artifacts:
            if checksum(artifact.path) != artifact.before_sha256:
                raise MultiPatchError(f"restore verification failed: {artifact.name}")
        manifest["state"] = "restored"
        manifest["updated_at"] = _timestamp()
        _write_manifest(root, manifest)


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = argparse.ArgumentParser(prog="python -m installer.multi_bundle_patch")
    parser.add_argument("--app-dir", required=True)
    parser.add_argument("--payload")
    parser.add_argument("--restore")
    args = parser.parse_args(list(argv) if argv is not None else None)
    if bool(args.payload) == bool(args.restore):
        parser.error("pass exactly one of --payload or --restore")
    try:
        if args.restore:
            restore(Path(args.app_dir).expanduser().resolve(), args.restore)
            print(f"openpe-multi-patch: restored transaction {args.restore}")
            return 0
        transaction_id = install(
            Path(args.app_dir).expanduser().resolve(),
            Path(args.payload).expanduser().resolve(),
        )
    except (ChecksumError, MultiPatchError, ProcessError, OSError) as exc:
        print(f"openpe-multi-patch: {exc}", file=os.sys.stderr)
        return 1
    print(f"openpe-multi-patch: installed transaction {transaction_id}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
