from __future__ import annotations

import argparse
import contextlib
import hashlib
import json
import os
import platform
import re
import secrets
import shutil
import struct
import sys
import tempfile
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, List, Optional, Sequence, Set
from urllib.parse import urlparse

from . import __version__, winlock
from .backup_transaction import _path_key
from .bundle import BundleError, _atomic_write, _durable_mkdir, checksum, has_marker, inject
from .checksum import (
    ChecksumError,
    patch_product_json,
    verify_bundle_checksum,
    verify_trusted_baseline,
    vscode_checksum,
)
from .handshake import (
    DescriptorError,
    HandshakeError,
    default_descriptor_path,
    read_descriptor,
    validate_profile_cors,
    verify_server,
)
from .locking import LockError, mutation_lock
from .paths import IDEPaths, resolve_paths
from .processes import ProcessError, require_host_stopped
from .profiles import DEVIN_PROFILE, ProfileError, require_profile


SESSIONS_RELPATH = "vs/sessions/sessions.desktop.main.js"
SESSIONS_HTML_RELPATH = "vs/sessions/electron-browser/sessions.html"
WORKBENCH_RELPATH = "vs/workbench/workbench.desktop.main.js"
PRELOAD_RELPATH = "vs/base/parts/sandbox/electron-browser/preload.js"
MAIN_RELPATH = "out/main.js"
TRUSTED_SESSIONS_SHA256 = "594ddc67fd1d19962a8b5e47851f0a076f67c38e1312a7c526c88683c672eef5"
TRUSTED_SESSIONS_HTML_SHA256 = "13b1b5583229d7ca1797bf917d60f6e42a26776b0b2331c7fded71ea1ee06523"
TRUSTED_PRELOAD_SHA256 = "a449e453c99c219d03be70a454255d482d76c5e18cbb73145ca2a5b8dc7f2e79"
TRUSTED_MAIN_SHA256 = "04d0fb5c1055510d3006f5e2cfaacb8a35aa81c3716215cd3fce41ef1c9dccd7"
TRUSTED_DEVIN_EXE_SHA256 = "b98b7638153362ca1a57541c74427fa8b3ec522cf656247bd151acc324f1911a"

# patchKind marker embedded in the bundle bootstrap for exact multi-bundle
# installs. Diagnostics dispatch on it: an exact patch's manifest lives under
# multi-transactions/, and running the canonical transactions/ lookup against
# it used to misreport a healthy exact install as a transaction error.
EXACT_PATCH_KIND = "devin-exact-multi-v1"


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


def _use_exclusive_handles() -> bool:
    """Exclusive sharing modes exist on Windows only; POSIX callers keep the
    historical read-verify-replace flow (advisory locking cannot exclude a
    vendor updater there anyway)."""
    return os.name == "nt"


def _read_artifact(path: Path, handle: Optional[object]) -> bytes:
    if handle is not None:
        return winlock.read_all(handle)
    return path.read_bytes()


def _conditional_write(
    path: Path,
    expected: str,
    target: bytes,
    handle: Optional[object] = None,
) -> None:
    """Verify-then-write one artifact.

    With an exclusive Windows handle the sequence is a true exclusion: the
    verify read, the write, and the verification re-read all go through the
    held handle, and no other process (vendor updater included) can open the
    file in between. Without a handle (POSIX) this remains the historical
    read-verify-replace with its documented small window.
    """
    current = _read_artifact(path, handle)
    if _sha256_bytes(current) != expected:
        raise MultiPatchError(f"live file changed before write: {path}")
    if handle is not None:
        winlock.write_all(handle, target)
        written = winlock.read_all(handle)
    else:
        _atomic_write(path, target, mode=0o644)
        written = path.read_bytes()
    if _sha256_bytes(written) != _sha256_bytes(target):
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


def _verify_host_executable_bytes(value: bytes) -> None:
    try:
        pe_offset = struct.unpack_from("<I", value, 0x3C)[0]
        machine = struct.unpack_from("<H", value, pe_offset + 4)[0]
    except (IndexError, struct.error) as exc:
        raise MultiPatchError(f"cannot verify Devin executable: {exc}") from exc
    if _sha256_bytes(value) != TRUSTED_DEVIN_EXE_SHA256 or machine != 0x8664:
        raise MultiPatchError("Devin executable is not the trusted Windows x64 baseline")


def _verify_host_executable(paths: IDEPaths) -> None:
    executable = paths.app_root / "Devin.exe"
    try:
        value = executable.read_bytes()
    except OSError as exc:
        raise MultiPatchError(f"cannot verify Devin executable: {exc}") from exc
    _verify_host_executable_bytes(value)


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


def _build_preload_bridge() -> str:
    """构造 sandbox-safe preload：只使用 Electron 允许的 IPC API。"""
    return """;(() => {
const { contextBridge, ipcRenderer } = require("electron");
const enhanceChannel = "vscode:openpe-enhance-v1";
const cancelChannel = "vscode:openpe-cancel-v1";
contextBridge.exposeInMainWorld("__openpeBridge", Object.freeze({
  enhance: (requestId, input) => {
    const prompt = typeof input?.prompt === "string" ? input.prompt : "";
    const max = input?.options?.max_context_tokens;
    const projected = { prompt, ...(max === undefined ? {} : { options: { max_context_tokens: max } }) };
    return ipcRenderer.invoke(enhanceChannel, requestId, projected);
  },
  cancel: (requestId) => ipcRenderer.send(cancelChannel, requestId),
}));
})();"""


def _build_main_bridge(descriptor_path: Path) -> str:
    """构造 main-process handler；token/descriptor/HTTP 永不进入 renderer。"""
    descriptor_literal = json.dumps(str(descriptor_path), ensure_ascii=False)
    return f"""
import {{ ipcMain as __openpeIpcMain }} from "electron";
import {{ closeSync as __openpeCloseSync, fstatSync as __openpeFstatSync, lstatSync as __openpeLstatSync, openSync as __openpeOpenSync, readFileSync as __openpeReadFileSync, statSync as __openpeStatSync }} from "node:fs";
import {{ execFile as __openpeExecFile }} from "node:child_process";
import {{ request as __openpeRequest }} from "node:http";
const __openpeDescriptorPath = {descriptor_literal};
const __openpeEnhanceChannel = "vscode:openpe-enhance-v1";
const __openpeCancelChannel = "vscode:openpe-cancel-v1";
const __openpeInflight = new Map();
const __openpePerSenderLimit = 2;
const __openpeGlobalLimit = 16;
const __openpePowerShell = `${{process.env.SystemRoot || "C:/Windows"}}/System32/WindowsPowerShell/v1.0/powershell.exe`;
const __openpeAclScript = `$ErrorActionPreference='Stop';$item=Get-Item -LiteralPath $args[0];if(($item.Attributes -band [IO.FileAttributes]::ReparsePoint)-ne 0){{throw 'reparse'}};$acl=Get-Acl -LiteralPath $args[0];$me=[Security.Principal.WindowsIdentity]::GetCurrent().User;$owner=(New-Object Security.Principal.NTAccount($acl.Owner)).Translate([Security.Principal.SecurityIdentifier]);$rules=@($acl.GetAccessRules($true,$true,[Security.Principal.SecurityIdentifier]));$rw=[Security.AccessControl.FileSystemRights]::Read -bor [Security.AccessControl.FileSystemRights]::Write;if($owner.Value-ne$me.Value-or-not$acl.AreAccessRulesProtected-or$rules.Count-ne1-or$rules[0].IdentityReference.Value-ne$me.Value-or$rules[0].IsInherited-or$rules[0].AccessControlType-ne[Security.AccessControl.AccessControlType]::Allow-or($rules[0].FileSystemRights-band$rw)-ne$rw){{throw 'insecure ACL'}}`;
const __openpeAclCheck = (state) => new Promise((resolve, reject) => {{
  state.aclProcess = __openpeExecFile(__openpePowerShell, ["-NoProfile", "-NonInteractive", "-Command", __openpeAclScript, __openpeDescriptorPath], {{ windowsHide: true, timeout: 5000 }}, (error) => {{ state.aclProcess = null; error ? reject(error) : resolve(); }});
}});
const __openpeAllowedSender = (event) => {{
  const url = String(event.senderFrame?.url || event.sender?.getURL?.() || "");
  return url.startsWith("vscode-file://vscode-app/");
}};
const __openpeKey = (event, requestId) => `${{event.sender.id}}:${{requestId}}`;
const __openpeOpenDescriptor = () => {{
  const stat = __openpeLstatSync(__openpeDescriptorPath);
  if (!stat.isFile() || stat.isSymbolicLink()) throw new Error("openPE descriptor must be a regular file");
  const fd = __openpeOpenSync(__openpeDescriptorPath, "r");
  if (!__openpeFstatSync(fd).isFile()) {{ __openpeCloseSync(fd); throw new Error("openPE descriptor handle is not a file"); }}
  return fd;
}};
const __openpeDescriptorFromFd = (fd) => {{
  const opened = __openpeFstatSync(fd);
  const current = __openpeStatSync(__openpeDescriptorPath);
  if (opened.dev !== current.dev || opened.ino !== current.ino) throw new Error("openPE descriptor changed during ACL validation");
  const raw = __openpeReadFileSync(fd, "utf8");
  const value = JSON.parse(raw);
  const url = new URL(String(value.base_url || ""));
  const token = String(value.token || "");
  if (url.protocol !== "http:" || url.hostname !== "127.0.0.1" || !url.port ||
      url.pathname !== "/" || url.username || url.password || url.search || url.hash) {{
    throw new Error("openPE descriptor must use http://127.0.0.1:<port>");
  }}
  if (!/^[0-9a-f]{{64}}$/.test(token)) throw new Error("openPE descriptor token is malformed");
  return {{ port: Number(url.port), token }};
}};
__openpeIpcMain.handle(__openpeEnhanceChannel, async (event, requestId, input) => {{
  if (!__openpeAllowedSender(event)) throw new Error("openPE request sender is not allowed");
  if (typeof requestId !== "string" || !/^[A-Za-z0-9_-]{{16,128}}$/.test(requestId)) throw new Error("openPE request id is invalid");
  const prompt = typeof input?.prompt === "string" ? input.prompt : "";
  if (!prompt.trim()) throw new Error("prompt is empty");
  const max = input?.options?.max_context_tokens;
  if (max !== undefined && (!Number.isSafeInteger(max) || max <= 0)) throw new Error("max_context_tokens must be a positive safe integer");
  const key = __openpeKey(event, requestId);
  const senderPrefix = `${{event.sender.id}}:`;
  const senderCount = [...__openpeInflight.keys()].filter((value) => value.startsWith(senderPrefix)).length;
  if (__openpeInflight.has(key)) throw new Error("duplicate openPE request id");
  if (__openpeInflight.size >= __openpeGlobalLimit || senderCount >= __openpePerSenderLimit) throw new Error("too many concurrent openPE requests");
  const fd = __openpeOpenDescriptor();
  const state = {{ cancelled: false, aclProcess: null, request: null, fd }};
  __openpeInflight.set(key, state);
  const deadlineAt = Date.now() + 30000;
  try {{
    await __openpeAclCheck(state);
    if (state.cancelled) throw new Error("openPE request cancelled");
    const auth = __openpeDescriptorFromFd(fd);
    const body = Buffer.from(JSON.stringify({{
      prompt, client: "devin", mode: "agent",
      ...(max === undefined ? {{}} : {{ options: {{ max_context_tokens: max }} }}),
    }}), "utf8");
    if (body.length > 2 * 1024 * 1024) throw new Error("openPE request exceeds 2 MiB");
    return await new Promise((resolve, reject) => {{
      let settled = false;
      let responseEnded = false;
      const finish = (error, value) => {{
        if (settled) return;
        settled = true;
        clearTimeout(wallTimer);
        error ? reject(error) : resolve(value);
      }};
      const wallTimer = setTimeout(() => state.request?.destroy(new Error("openPE request timed out")), Math.max(1, deadlineAt - Date.now()));
      state.request = __openpeRequest({{
        hostname: "127.0.0.1", port: auth.port, path: "/v1/prompt-enhance", method: "POST", agent: false,
        headers: {{ "content-type": "application/json", "content-length": body.length, authorization: `Bearer ${{auth.token}}` }},
      }}, (response) => {{
        const chunks = [];
        let size = 0;
        response.on("data", (chunk) => {{ size += chunk.length; if (size > 2 * 1024 * 1024) state.request.destroy(new Error("openPE response exceeds 2 MiB")); else chunks.push(chunk); }});
        response.on("aborted", () => finish(new Error("openPE response aborted")));
        response.on("error", (error) => finish(error));
        response.on("end", () => {{
          responseEnded = true;
          let payload;
          try {{ payload = JSON.parse(Buffer.concat(chunks).toString("utf8")); }} catch {{ finish(new Error(`openPE returned invalid JSON (HTTP ${{response.statusCode || 0}})`)); return; }}
          if ((response.statusCode || 500) < 200 || (response.statusCode || 500) >= 300) {{ finish(new Error(typeof payload?.error === "string" ? payload.error : `openPE returned HTTP ${{response.statusCode || 0}}`)); return; }}
          finish(null, payload);
        }});
        response.on("close", () => {{ if (!responseEnded) finish(new Error("openPE response closed early")); }});
      }});
      state.request.on("error", (error) => finish(error));
      if (state.cancelled) state.request.destroy(new Error("openPE request cancelled")); else state.request.end(body);
    }});
  }} finally {{
    __openpeInflight.delete(key);
    __openpeCloseSync(fd);
  }}
}});
__openpeIpcMain.on(__openpeCancelChannel, (event, requestId) => {{
  if (!__openpeAllowedSender(event) || typeof requestId !== "string") return;
  const state = __openpeInflight.get(__openpeKey(event, requestId));
  if (!state) return;
  state.cancelled = true;
  state.aclProcess?.kill();
  state.request?.destroy(new Error("openPE request cancelled"));
}});
"""


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
    preload = app / "out" / "vs" / "base" / "parts" / "sandbox" / "electron-browser" / "preload.js"
    main_bundle = app / "out" / "main.js"
    workbench = paths.bundle_file
    product = paths.product_file
    for candidate in (sessions, sessions_html, preload, main_bundle, workbench, product):
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
    if checksum(preload) != TRUSTED_PRELOAD_SHA256:
        raise MultiPatchError("sandbox preload is not the trusted baseline")
    if checksum(main_bundle) != TRUSTED_MAIN_SHA256:
        raise MultiPatchError("Electron main bundle is not the trusted baseline")
    server_origin = _exact_server_origin(descriptor.base_url)
    config = {
        "baseUrl": server_origin,
        "credentialMode": "preload-capability-v1",
        "version": descriptor.version or "unknown",
        "hostProfileId": DEVIN_PROFILE.profile_id,
        "productCommit": paths.product.commit,
        "transactionId": transaction_id,
        "client": DEVIN_PROFILE.client,
        "mode": DEVIN_PROFILE.mode,
        "historySource": "none",
        "patchKind": EXACT_PATCH_KIND,
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
        target_preload = target_dir / "preload.js"
        target_main = target_dir / "main.js"
        target_product = target_dir / "product.json"
        target_sessions.write_bytes(sessions.read_bytes())
        target_workbench.write_bytes(workbench.read_bytes())
        target_html.write_bytes(
            _patch_sessions_csp(sessions_html.read_bytes(), server_origin)
        )
        target_preload.write_bytes(preload.read_bytes())
        target_main.write_bytes(main_bundle.read_bytes())
        target_product.write_bytes(product.read_bytes())
        inject(target_sessions, prelude + payload, expected_sha256=checksum(sessions))
        inject(target_workbench, prelude + payload, expected_sha256=checksum(workbench))
        inject(
            target_preload,
            _build_preload_bridge(),
            expected_sha256=checksum(preload),
        )
        inject(
            target_main,
            _build_main_bridge(default_descriptor_path()),
            expected_sha256=checksum(main_bundle),
        )
        for relpath, target in (
            (SESSIONS_RELPATH, target_sessions),
            (WORKBENCH_RELPATH, target_workbench),
            (SESSIONS_HTML_RELPATH, target_html),
            (PRELOAD_RELPATH, target_preload),
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
                "preload",
                preload,
                PRELOAD_RELPATH,
                "preload.original",
                target_preload,
                TRUSTED_PRELOAD_SHA256,
            ),
            (
                "main",
                main_bundle,
                MAIN_RELPATH,
                "main.original",
                target_main,
                TRUSTED_MAIN_SHA256,
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


def _rollback_forced(
    artifacts: List[Artifact],
    root: Path,
    handles: Dict[str, object],
    manifest: Dict[str, Any],
) -> None:
    """独占句柄内补偿；每次原位写前持久化 restore_intent，可再次恢复。"""
    manifest["state"] = "restoring"
    manifest["restore_intent"] = manifest.get("write_intent")
    manifest["updated_at"] = _timestamp()
    _write_manifest(root, manifest)
    ordered = list(reversed(artifacts))
    intent = str(manifest.get("restore_intent", ""))
    if intent:
        ordered.sort(key=lambda artifact: artifact.name != intent)
    for artifact in ordered:
        backup = (root / artifact.backup_name).read_bytes()
        if _sha256_bytes(backup) != artifact.before_sha256:
            raise MultiPatchError(f"backup checksum mismatch: {artifact.backup_name}")
        handle = handles.get(artifact.name)
        if handle is None:
            raise MultiPatchError(f"missing exclusive handle for rollback: {artifact.name}")
        manifest["restore_intent"] = artifact.name
        manifest["updated_at"] = _timestamp()
        _write_manifest(root, manifest)
        winlock.write_all(handle, backup)
        if _sha256_bytes(winlock.read_all(handle)) != artifact.before_sha256:
            raise MultiPatchError(f"forced rollback verification failed: {artifact.name}")
        manifest["restore_intent"] = None
        manifest["updated_at"] = _timestamp()
        _write_manifest(root, manifest)
    manifest["state"] = "rolled_back"
    manifest["write_intent"] = None
    manifest["updated_at"] = _timestamp()
    _write_manifest(root, manifest)


def _rollback_known(
    artifacts: List[Artifact],
    root: Path,
    handles: Optional[Dict[str, object]] = None,
) -> None:
    handles = handles or {}
    live = {
        artifact.name: _sha256_bytes(
            _read_artifact(artifact.path, handles.get(artifact.name))
        )
        for artifact in artifacts
    }
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
        _conditional_write(
            artifact.path,
            live[artifact.name],
            backup,
            handle=handles.get(artifact.name),
        )


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
    # One payload snapshot: the bytes read here feed the injected targets AND
    # the manifest hash. Re-reading the file for the hash (the old
    # checksum(payload_path) call) let a payload swapped between the two reads
    # record a hash that never matches the injected content.
    payload_bytes = payload_path.read_bytes()
    try:
        payload = payload_bytes.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise MultiPatchError(f"payload is not valid UTF-8: {exc}") from exc
    payload_sha256 = _sha256_bytes(payload_bytes)
    artifacts = _build_targets(paths, payload, transaction_id)
    root = _transaction_root(paths, transaction_id)
    if root.exists():
        raise MultiPatchError(f"transaction already exists: {root}")
    with mutation_lock(paths):
        require_host_stopped(profile)
        if root.exists():
            raise MultiPatchError(f"transaction already exists: {root}")
        with contextlib.ExitStack() as stack:
            # Windows 上对五个目标文件在备份、写入、最终 manifest 提交的
            # 整个区间持有独占句柄，vendor updater 无法插入并发写。
            handles: Dict[str, object] = {}
            if _use_exclusive_handles():
                executable_handle = stack.enter_context(
                    winlock.exclusive_read_handle(paths.app_root / "Devin.exe")
                )
                _verify_host_executable_bytes(winlock.read_all(executable_handle))
                for artifact in artifacts:
                    handles[artifact.name] = stack.enter_context(
                        winlock.exclusive_handle(artifact.path)
                    )
            try:
                _durable_mkdir(root, mode=0o700)
                winlock.restrict_directory(root)
                for artifact in artifacts:
                    live = _read_artifact(artifact.path, handles.get(artifact.name))
                    if _sha256_bytes(live) != artifact.before_sha256:
                        raise MultiPatchError(
                            f"artifact changed before backup: {artifact.path}"
                        )
                    backup = root / artifact.backup_name
                    _atomic_write(backup, live, mode=0o600)
                    if checksum(backup) != artifact.before_sha256:
                        raise MultiPatchError(
                            f"backup verification failed: {artifact.path}"
                        )
                manifest: Dict[str, Any] = {
                    "schema_version": 1,
                    "state": "prepared",
                    "transaction_id": transaction_id,
                    "profile_id": profile.profile_id,
                    "install_id": paths.install_id,
                    "app_root": str(paths.app_root),
                    "product_commit": paths.product.commit,
                    "installer_version": __version__,
                    "payload_sha256": payload_sha256,
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
            except BaseException:
                # Nothing live has been mutated yet: remove the half-built
                # transaction directory instead of leaving an orphan with no
                # manifest and no printed transaction id.
                try:
                    shutil.rmtree(root)
                except OSError as cleanup_exc:
                    raise MultiPatchError(
                        f"failed to clean unpublished transaction {transaction_id}: {cleanup_exc}"
                    ) from cleanup_exc
                raise
            try:
                print(
                    f"openpe-multi-patch: prepared transaction {transaction_id}",
                    flush=True,
                )
                manifest["state"] = "installing"
                manifest["completed_artifacts"] = []
                for artifact in artifacts:
                    manifest["write_intent"] = artifact.name
                    manifest["updated_at"] = _timestamp()
                    _write_manifest(root, manifest)
                    _conditional_write(
                        artifact.path,
                        artifact.before_sha256,
                        artifact.target,
                        handle=handles.get(artifact.name),
                    )
                    manifest["completed_artifacts"].append(artifact.name)
                    manifest["write_intent"] = None
                    manifest["updated_at"] = _timestamp()
                    _write_manifest(root, manifest)
                for artifact in artifacts:
                    written = _read_artifact(
                        artifact.path, handles.get(artifact.name)
                    )
                    if _sha256_bytes(written) != artifact.target_sha256:
                        raise MultiPatchError(
                            f"target verification failed: {artifact.path}"
                        )
            except BaseException:
                if handles:
                    _rollback_forced(artifacts, root, handles, manifest)
                else:
                    _rollback_known(artifacts, root, handles)
                    manifest["state"] = "rolled_back"
                    manifest["write_intent"] = None
                    manifest["updated_at"] = _timestamp()
                    _write_manifest(root, manifest)
                raise
            # 在释放独占句柄前提交 installed 状态；否则 updater 可在
            # 句柄关闭与 manifest 提交之间改写文件，留下谎报 installed 的事务。
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


@dataclass(frozen=True)
class _RestoreManifestSnapshot:
    root: Path
    path: Path
    value: Dict[str, Any]
    sha256: str


@dataclass(frozen=True)
class _RecoverySnapshot:
    live_sha256: Dict[str, str]
    backup_bytes: Dict[str, bytes]


def _read_restore_manifest(
    paths: IDEPaths,
    transaction_id: str,
) -> _RestoreManifestSnapshot:
    root = _transaction_root(paths, transaction_id)
    manifest_path = root / "manifest.json"
    try:
        manifest_bytes = manifest_path.read_bytes()
        manifest = json.loads(manifest_bytes.decode("utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise MultiPatchError(f"cannot read multi transaction: {exc}") from exc
    if not isinstance(manifest, dict):
        raise MultiPatchError("multi transaction manifest must be an object")
    return _RestoreManifestSnapshot(
        root,
        manifest_path,
        manifest,
        _sha256_bytes(manifest_bytes),
    )


def _validate_restore_manifest_identity(
    snapshot: _RestoreManifestSnapshot,
    paths: IDEPaths,
    transaction_id: str,
    allow_terminal: bool = False,
) -> None:
    assert paths.product is not None
    manifest = snapshot.value
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
    allowed_states = {"prepared", "installing", "installed", "restoring"}
    if allow_terminal:
        allowed_states.update({"restored", "rolled_back"})
    if manifest.get("state") not in allowed_states:
        raise MultiPatchError("multi transaction state is invalid")


def _expected_restore_paths(paths: IDEPaths) -> Dict[str, Path]:
    app = paths.app_root / "resources" / "app"
    return {
        "sessions": app / "out" / "vs" / "sessions" / "sessions.desktop.main.js",
        "workbench": paths.bundle_file,
        "sessions_html": app
        / "out"
        / "vs"
        / "sessions"
        / "electron-browser"
        / "sessions.html",
        "preload": app
        / "out"
        / "vs"
        / "base"
        / "parts"
        / "sandbox"
        / "electron-browser"
        / "preload.js",
        "main": app / "out" / "main.js",
        "product": paths.product_file,
    }


def _restore_artifact_values(
    manifest: Dict[str, Any],
    expected_paths: Dict[str, Path],
) -> List[Any]:
    values = manifest.get("artifacts")
    if not isinstance(values, list):
        raise MultiPatchError("multi transaction artifact set is invalid")
    names = {
        value.get("name")
        for value in values
        if isinstance(value, dict) and isinstance(value.get("name"), str)
    }
    supported_sets = (
        {"sessions", "workbench", "sessions_html", "product"},
        {"sessions", "workbench", "sessions_html", "preload", "product"},
        set(expected_paths),
    )
    if len(names) != len(values) or names not in supported_sets:
        raise MultiPatchError("multi transaction artifact set is invalid")
    return values


def _trusted_restore_baselines(paths: IDEPaths) -> Dict[str, str]:
    assert paths.profile is not None
    assert paths.product is not None
    if platform.system() != "Windows" or platform.machine().casefold() not in {
        "amd64",
        "x86_64",
    }:
        raise MultiPatchError("exact-build Devin restore requires Windows x64")
    _verify_host_executable(paths)
    trusted = paths.profile.supported_build(platform.system(), paths.product)
    if trusted is None:
        raise MultiPatchError("restored Devin build is not trusted")
    return {
        "sessions": TRUSTED_SESSIONS_SHA256,
        "workbench": trusted.bundle_sha256,
        "sessions_html": TRUSTED_SESSIONS_HTML_SHA256,
        "preload": TRUSTED_PRELOAD_SHA256,
        "main": TRUSTED_MAIN_SHA256,
        "product": trusted.product_sha256,
    }


def _validate_restore_artifact(
    value: Any,
    expected_paths: Dict[str, Path],
    trusted_before: Dict[str, str],
    seen: Set[str],
) -> Artifact:
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
    expected_backup_names = {
        "sessions": "sessions.original",
        "workbench": "workbench.original",
        "sessions_html": "sessions-html.original",
        "preload": "preload.original",
        "main": "main.original",
        "product": "product.original",
    }
    if backup_name != expected_backup_names[name]:
        raise MultiPatchError("multi transaction backup identity is invalid")
    return Artifact(
        name=name,
        path=path,
        relpath=str(value.get("relpath", "")),
        backup_name=backup_name,
        before_sha256=before_sha,
        target_sha256=target_sha,
        target=b"",
    )


def _validate_restore_artifacts(
    values: List[Any],
    expected_paths: Dict[str, Path],
    trusted_before: Dict[str, str],
) -> List[Artifact]:
    artifacts: List[Artifact] = []
    seen = set()
    for value in values:
        artifacts.append(
            _validate_restore_artifact(
                value,
                expected_paths,
                trusted_before,
                seen,
            )
        )
    return artifacts


def _open_restore_handles(
    stack: contextlib.ExitStack,
    paths: IDEPaths,
    artifacts: List[Artifact],
    trusted_before: Dict[str, str],
) -> Dict[str, object]:
    handles: Dict[str, object] = {}
    if _use_exclusive_handles():
        executable_handle = stack.enter_context(
            winlock.exclusive_read_handle(paths.app_root / "Devin.exe")
        )
        _verify_host_executable_bytes(winlock.read_all(executable_handle))
        for artifact in artifacts:
            handles[artifact.name] = stack.enter_context(
                winlock.exclusive_handle(artifact.path)
            )
        listed = {artifact.name for artifact in artifacts}
        expected_paths = _expected_restore_paths(paths)
        missing = sorted(set(expected_paths) - listed)
        for name in missing:
            guard = stack.enter_context(
                winlock.exclusive_read_handle(expected_paths[name])
            )
            if _sha256_bytes(winlock.read_all(guard)) != trusted_before[name]:
                raise MultiPatchError(f"unlisted artifact baseline mismatch: {name}")
    return handles


def _capture_recovery_snapshot(
    artifacts: List[Artifact],
    root: Path,
    handles: Dict[str, object],
    manifest: Dict[str, Any],
) -> _RecoverySnapshot:
    live = {
        artifact.name: _sha256_bytes(
            _read_artifact(artifact.path, handles.get(artifact.name))
        )
        for artifact in artifacts
    }
    recovery_bytes: Dict[str, bytes] = {}
    state = manifest.get("state")
    write_intent = ""
    if state == "installing":
        write_intent = str(manifest.get("write_intent", ""))
    elif state == "restoring":
        write_intent = str(manifest.get("restore_intent", ""))
    for artifact in artifacts:
        if live[artifact.name] not in {
            artifact.before_sha256,
            artifact.target_sha256,
        } and artifact.name != write_intent:
            raise MultiPatchError(f"unknown live artifact state: {artifact.name}")
        backup = root / artifact.backup_name
        value = backup.read_bytes()
        if _sha256_bytes(value) != artifact.before_sha256:
            raise MultiPatchError(
                f"multi transaction backup mismatch: {artifact.name}"
            )
        recovery_bytes[artifact.name] = value
    return _RecoverySnapshot(live, recovery_bytes)


def _write_restore_state(
    root: Path,
    manifest: Dict[str, Any],
    state: str,
) -> None:
    manifest["state"] = state
    manifest["updated_at"] = _timestamp()
    _write_manifest(root, manifest)


def _apply_restore_snapshot(
    artifacts: List[Artifact],
    root: Path,
    manifest: Dict[str, Any],
    snapshot: _RecoverySnapshot,
    handles: Dict[str, object],
) -> None:
    if manifest.get("state") == "installing" and not manifest.get("restore_intent"):
        manifest["restore_intent"] = manifest.get("write_intent")
    _write_restore_state(root, manifest, "restoring")
    ordered = list(reversed(artifacts))
    existing_intent = str(manifest.get("restore_intent", ""))
    if existing_intent:
        ordered.sort(key=lambda artifact: artifact.name != existing_intent)
    for artifact in ordered:
        manifest["restore_intent"] = artifact.name
        manifest["updated_at"] = _timestamp()
        _write_manifest(root, manifest)
        handle = handles.get(artifact.name)
        if handle is not None and snapshot.live_sha256[artifact.name] not in {
            artifact.before_sha256,
            artifact.target_sha256,
        }:
            winlock.write_all(handle, snapshot.backup_bytes[artifact.name])
        else:
            _conditional_write(
                artifact.path,
                snapshot.live_sha256[artifact.name],
                snapshot.backup_bytes[artifact.name],
                handle=handle,
            )
        manifest["restore_intent"] = None
        manifest["updated_at"] = _timestamp()
        _write_manifest(root, manifest)
    for artifact in artifacts:
        restored = _read_artifact(artifact.path, handles.get(artifact.name))
        if _sha256_bytes(restored) != artifact.before_sha256:
            raise MultiPatchError(f"restore verification failed: {artifact.name}")
    # restored 状态必须在独占句柄释放前提交，避免恢复验证与 manifest
    # 提交之间被 updater 改写。
    _write_restore_state(root, manifest, "restored")


def _restore_artifacts_under_lock(
    paths: IDEPaths,
    manifest_snapshot: _RestoreManifestSnapshot,
    artifacts: List[Artifact],
    trusted_before: Optional[Dict[str, str]] = None,
) -> None:
    assert paths.profile is not None
    with mutation_lock(paths):
        require_host_stopped(paths.profile)
        if checksum(manifest_snapshot.path) != manifest_snapshot.sha256:
            raise MultiPatchError("multi transaction manifest changed before restore")
        with contextlib.ExitStack() as stack:
            # 独占句柄覆盖快照、恢复写入、校验及最终 manifest 提交。
            baseline = trusted_before or {
                artifact.name: artifact.before_sha256 for artifact in artifacts
            }
            handles = _open_restore_handles(stack, paths, artifacts, baseline)
            recovery_snapshot = _capture_recovery_snapshot(
                artifacts,
                manifest_snapshot.root,
                handles,
                manifest_snapshot.value,
            )
            _apply_restore_snapshot(
                artifacts,
                manifest_snapshot.root,
                manifest_snapshot.value,
                recovery_snapshot,
                handles,
            )


def restore(app_dir: Path, transaction_id: str) -> None:
    paths = resolve_paths(override=str(app_dir))
    if paths is None or paths.product is None or paths.profile != DEVIN_PROFILE:
        raise MultiPatchError("cannot resolve Devin transaction target")
    require_host_stopped(paths.profile)
    manifest_snapshot = _read_restore_manifest(paths, transaction_id)
    winlock.restrict_directory(manifest_snapshot.root)
    _validate_restore_manifest_identity(manifest_snapshot, paths, transaction_id)
    expected_paths = _expected_restore_paths(paths)
    values = _restore_artifact_values(manifest_snapshot.value, expected_paths)
    trusted_before = _trusted_restore_baselines(paths)
    artifacts = _validate_restore_artifacts(
        values,
        expected_paths,
        trusted_before,
    )
    _restore_artifacts_under_lock(paths, manifest_snapshot, artifacts, trusted_before)


def _diagnostic_trusted_baselines(paths: IDEPaths) -> Dict[str, str]:
    assert paths.profile is not None and paths.product is not None
    trusted = paths.profile.supported_build("Windows", paths.product)
    if trusted is None:
        raise MultiPatchError("current build is not the trusted exact Windows baseline")
    return {
        "sessions": TRUSTED_SESSIONS_SHA256,
        "workbench": trusted.bundle_sha256,
        "sessions_html": TRUSTED_SESSIONS_HTML_SHA256,
        "preload": TRUSTED_PRELOAD_SHA256,
        "main": TRUSTED_MAIN_SHA256,
        "product": trusted.product_sha256,
    }


def describe_transaction(paths: IDEPaths, transaction_id: str) -> Dict[str, Any]:
    """校验 exact manifest、backup 与六个 live artifact 的只读健康状态。"""
    snapshot = _read_restore_manifest(paths, transaction_id)
    _validate_restore_manifest_identity(snapshot, paths, transaction_id, allow_terminal=True)
    expected_paths = _expected_restore_paths(paths)
    values = _restore_artifact_values(snapshot.value, expected_paths)
    trusted = _diagnostic_trusted_baselines(paths)
    artifacts = _validate_restore_artifacts(values, expected_paths, trusted)
    _verify_host_executable(paths)
    state = str(snapshot.value.get("state", "unknown"))
    write_intent = str(snapshot.value.get("write_intent", ""))
    problems: List[str] = []
    listed = {artifact.name for artifact in artifacts}
    for name in sorted(set(expected_paths) - listed):
        try:
            if checksum(expected_paths[name]) != trusted[name]:
                problems.append(f"unlisted baseline mismatch: {name}")
        except (ChecksumError, OSError):
            problems.append(f"unlisted artifact unreadable: {name}")
    for artifact in artifacts:
        backup = snapshot.root / artifact.backup_name
        try:
            if checksum(backup) != artifact.before_sha256:
                problems.append(f"backup mismatch: {artifact.name}")
        except (ChecksumError, OSError):
            problems.append(f"backup unreadable: {artifact.name}")
        try:
            live_sha = checksum(artifact.path)
        except (ChecksumError, OSError):
            problems.append(f"live unreadable: {artifact.name}")
            continue
        expected = artifact.target_sha256 if state == "installed" else artifact.before_sha256
        if live_sha != expected:
            if not (state == "installing" and artifact.name == write_intent):
                problems.append(f"live mismatch: {artifact.name}")
    healthy = state in {"installed", "restored", "rolled_back"} and not problems
    active = state in {"prepared", "installing", "restoring"}
    return {
        "root": snapshot.root,
        "state": state,
        "transaction_id": transaction_id,
        "healthy": healthy,
        "active": active,
        "problems": problems,
    }


def discover_transactions(paths: IDEPaths) -> List[Dict[str, Any]]:
    """不依赖 workbench marker，扫描当前 install/build 的 exact journals。"""
    parent = _transaction_root(paths, "scan-placeholder").parent
    if not parent.is_dir():
        return []
    results: List[Dict[str, Any]] = []
    for candidate in sorted(parent.iterdir()):
        if candidate.is_symlink() or not candidate.is_dir():
            continue
        transaction_id = candidate.name
        try:
            raw = json.loads((candidate / "manifest.json").read_text(encoding="utf-8"))
            if not isinstance(raw, dict):
                raise MultiPatchError("multi transaction manifest must be an object")
            transaction_id = str(raw.get("transaction_id", "")).strip()
            if not transaction_id:
                raise MultiPatchError("multi transaction id is missing")
            if _transaction_root(paths, transaction_id).resolve() != candidate.resolve():
                raise MultiPatchError("multi transaction directory identity mismatch")
            results.append(describe_transaction(paths, transaction_id))
        except (OSError, UnicodeDecodeError, json.JSONDecodeError, MultiPatchError) as exc:
            results.append({
                "root": candidate,
                "state": "invalid",
                "transaction_id": transaction_id,
                "healthy": False,
                "active": True,
                "problems": [str(exc)],
            })
    return results


# CLI exit codes, value-aligned with installer.__main__'s EXIT_* table (this
# module cannot import __main__ — __main__ imports us for the diagnostics
# dispatch, and the reverse edge would be a cycle).
_EXIT_DESCRIPTOR_ERROR = 67
_EXIT_HANDSHAKE_ERROR = 68
_EXIT_BUNDLE_ERROR = 70
_EXIT_PROFILE_ERROR = 73
_EXIT_TRANSACTION_ERROR = 74
_EXIT_PROCESS_RUNNING = 75


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
    # Every expected domain error maps to a stable exit code and a one-line
    # message; only truly unexpected failures may traceback. DescriptorError /
    # HandshakeError / ProfileError / LockError used to escape uncaught.
    except DescriptorError as exc:
        print(f"openpe-multi-patch: {exc}", file=sys.stderr)
        return _EXIT_DESCRIPTOR_ERROR
    except HandshakeError as exc:
        print(f"openpe-multi-patch: {exc}", file=sys.stderr)
        return _EXIT_HANDSHAKE_ERROR
    except ProfileError as exc:
        print(f"openpe-multi-patch: {exc}", file=sys.stderr)
        return _EXIT_PROFILE_ERROR
    except LockError as exc:
        print(f"openpe-multi-patch: {exc}", file=sys.stderr)
        return _EXIT_TRANSACTION_ERROR
    except ProcessError as exc:
        print(f"openpe-multi-patch: {exc}", file=sys.stderr)
        return _EXIT_PROCESS_RUNNING
    except winlock.WinLockError as exc:
        print(f"openpe-multi-patch: exclusive file lock failed: {exc}", file=sys.stderr)
        return _EXIT_TRANSACTION_ERROR
    except (BundleError, ChecksumError, MultiPatchError) as exc:
        print(f"openpe-multi-patch: {exc}", file=sys.stderr)
        return _EXIT_BUNDLE_ERROR
    except OSError as exc:
        print(f"openpe-multi-patch: {exc}", file=sys.stderr)
        return 1
    print(f"openpe-multi-patch: installed transaction {transaction_id}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
