"""Marker-based Electron bundle patcher.

Python mirror of ``internal/integration/bundle.go`` in the main openPE
repository. The two implementations are intentionally feature-equivalent
so that the Python installer and any Go-side tooling can produce / read
the same on-disk artefact.

Highlights:

  * Idempotent marker injection: a second ``inject`` call detects the
    existing marker region and replaces its body in place rather than
    nesting markers.
  * Atomic file writes via temp file + ``os.replace``.
  * SHA-256 checksums returned as lower-case hex strings.
  * Backup files always written with mode ``0o600``.
"""

from __future__ import annotations

import ctypes
import hashlib
import os
import secrets
import tempfile
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Optional, Tuple


class BundleError(Exception):
    """Raised for any bundle-patcher failure (invalid marker, missing file, ...)."""


@dataclass(frozen=True)
class Marker:
    begin: str
    end: str

    def validate(self) -> None:
        if not self.begin.strip() or not self.end.strip():
            raise BundleError("marker: begin/end required")
        if self.begin == self.end:
            raise BundleError("marker: begin and end must differ")


def default_marker() -> Marker:
    """Canonical markers shared with the Go ``DefaultMarker`` implementation."""
    return Marker(
        begin="/* === OPENPE-INJECT-BEGIN === */",
        end="/* === OPENPE-INJECT-END === */",
    )


def has_marker(bundle_path: Path, marker: Optional[Marker] = None) -> bool:
    """Return True for one complete marker region and reject malformed markers."""
    marker = marker or default_marker()
    marker.validate()
    data = _read_bytes(bundle_path)
    return _validate_marker_layout(data, marker) is not None


def inject(
    bundle_path: Path,
    payload: str,
    marker: Optional[Marker] = None,
    expected_sha256: str = "",
) -> None:
    """Write ``payload`` into ``bundle_path`` wrapped by ``marker``.

    Idempotent: if an existing marker region is found, its body is replaced
    in place; otherwise the new block is appended at end-of-file. The
    write is atomic via temp file + rename.
    """
    if not payload.strip():
        raise BundleError("inject: payload is empty")
    marker = marker or default_marker()
    marker.validate()
    # payload 自带 marker 分隔符会在写入后形成重复 marker——之后的每一次
    # has_marker/inject 都会把 bundle 判为畸形并拒绝处理，先在源头拒绝。
    if marker.begin in payload or marker.end in payload:
        raise BundleError("inject: payload must not contain the marker delimiters")
    data = _read_bytes(bundle_path)
    if expected_sha256 and hashlib.sha256(data).hexdigest() != expected_sha256:
        raise BundleError("inject: live bundle changed before compare-and-swap")
    block = _build_block(payload, marker)
    existing = _validate_marker_layout(data, marker)
    if existing is not None:
        start, end = existing
        new_data = data[:start] + block + data[end:]
    else:
        prefix = data
        if prefix and not prefix.endswith(b"\n"):
            prefix = prefix + b"\n"
        new_data = prefix + block
    _atomic_write(bundle_path, new_data, mode=0o644)


def restore(bundle_path: Path, backup_path: Path) -> None:
    """Atomically replace ``bundle_path`` with the contents of ``backup_path``."""
    data = _read_bytes(backup_path)
    _atomic_write(bundle_path, data, mode=0o644)


def backup(bundle_path: Path, backup_dir: Path) -> Path:
    """Copy ``bundle_path`` into ``backup_dir`` with a UTC timestamp suffix.

    Backups are written with mode ``0o600`` to discourage tampering and the
    parent directory is created with mode ``0o700``.
    """
    data = _read_bytes(bundle_path)
    backup_dir.mkdir(parents=True, exist_ok=True)
    try:
        os.chmod(backup_dir, 0o700)
    except OSError:
        # Best-effort: tightening mode is non-critical on platforms where
        # we don't own the directory (e.g. /tmp under some configurations).
        pass
    # 微秒 + 随机后缀：秒级时间戳曾让同一秒内的两次备份互相覆盖，
    # 丢失其中一份原始文件。
    timestamp = datetime.now(tz=timezone.utc).strftime("%Y%m%dT%H%M%S%fZ")
    target = backup_dir / f"{bundle_path.name}.{timestamp}-{secrets.token_hex(4)}.original"
    _atomic_write(target, data, mode=0o600)
    return target


def checksum(path: Path) -> str:
    """Return the lower-case hex SHA-256 of ``path``."""
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(64 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


# --- internals ----------------------------------------------------------


def _read_bytes(path: Path) -> bytes:
    try:
        return path.read_bytes()
    except FileNotFoundError as exc:
        raise BundleError(f"bundle file not found: {path}") from exc
    except OSError as exc:
        raise BundleError(f"read bundle {path}: {exc}") from exc


def _build_block(payload: str, marker: Marker) -> bytes:
    body = payload if payload.endswith("\n") else payload + "\n"
    return f"{marker.begin}\n{body}{marker.end}\n".encode("utf-8")


def _validate_marker_layout(data: bytes, marker: Marker) -> Optional[Tuple[int, int]]:
    begin_bytes = marker.begin.encode("utf-8")
    end_bytes = marker.end.encode("utf-8")
    begin_count = data.count(begin_bytes)
    end_count = data.count(end_bytes)
    if begin_count == 0 and end_count == 0:
        return None
    if begin_count != 1 or end_count != 1:
        raise BundleError(
            f"marker: expected one begin/end pair, got {begin_count}/{end_count}"
        )
    begin_idx = data.find(begin_bytes)
    end_idx = data.find(end_bytes)
    if begin_idx > end_idx:
        raise BundleError("marker: end appears before begin")
    end = end_idx + len(end_bytes)
    if end < len(data) and data[end : end + 1] == b"\n":
        end += 1
    return (begin_idx, end)


def _flush_directory(path: Path) -> None:
    if os.name == "nt":
        kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
        create_file = kernel32.CreateFileW
        create_file.argtypes = (
            ctypes.c_wchar_p,
            ctypes.c_uint32,
            ctypes.c_uint32,
            ctypes.c_void_p,
            ctypes.c_uint32,
            ctypes.c_uint32,
            ctypes.c_void_p,
        )
        create_file.restype = ctypes.c_void_p
        flush_file_buffers = kernel32.FlushFileBuffers
        flush_file_buffers.argtypes = (ctypes.c_void_p,)
        flush_file_buffers.restype = ctypes.c_int
        close_handle = kernel32.CloseHandle
        close_handle.argtypes = (ctypes.c_void_p,)
        close_handle.restype = ctypes.c_int
        handle = create_file(
            str(path),
            0x40000000,
            0x00000001 | 0x00000002 | 0x00000004,
            None,
            3,
            0x02000000,
            None,
        )
        invalid_handle = ctypes.c_void_p(-1).value
        if handle == invalid_handle:
            raise OSError(ctypes.get_last_error(), f"cannot open directory for flush: {path}")
        try:
            if not flush_file_buffers(handle):
                raise OSError(
                    ctypes.get_last_error(),
                    f"cannot flush directory metadata: {path}",
                )
        finally:
            close_handle(handle)
        return
    descriptor = os.open(path, os.O_RDONLY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def _durable_mkdir(path: Path, mode: int = 0o700) -> None:
    if path.exists():
        if not path.is_dir():
            raise OSError(f"directory path is not a directory: {path}")
        return
    parent = path.parent
    _durable_mkdir(parent, mode)
    path.mkdir(mode=mode)
    _flush_directory(parent)
    _flush_directory(path)


def _atomic_write(path: Path, data: bytes, mode: int = 0o644) -> None:
    parent = path.parent
    _durable_mkdir(parent)
    tmp = tempfile.NamedTemporaryFile(
        delete=False,
        dir=parent,
        prefix=path.name + ".tmp-",
        suffix=".bin",
    )
    tmp_name = tmp.name
    try:
        try:
            tmp.write(data)
            tmp.flush()
            os.fsync(tmp.fileno())
        finally:
            tmp.close()
        os.chmod(tmp_name, mode)
        os.replace(tmp_name, path)
        _flush_directory(parent)
    except Exception:
        try:
            os.unlink(tmp_name)
        except OSError:
            pass
        raise
