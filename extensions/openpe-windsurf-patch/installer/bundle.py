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

import hashlib
import os
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
    """Return True when both delimiters of ``marker`` are present in the bundle."""
    marker = marker or default_marker()
    marker.validate()
    data = _read_bytes(bundle_path)
    return marker.begin.encode("utf-8") in data and marker.end.encode("utf-8") in data


def inject(
    bundle_path: Path,
    payload: str,
    marker: Optional[Marker] = None,
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
    data = _read_bytes(bundle_path)
    block = _build_block(payload, marker)
    existing = _locate_existing_marker(data, marker)
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
    timestamp = datetime.now(tz=timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    target = backup_dir / f"{bundle_path.name}.{timestamp}.original"
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


def _locate_existing_marker(data: bytes, marker: Marker) -> Optional[Tuple[int, int]]:
    begin_bytes = marker.begin.encode("utf-8")
    end_bytes = marker.end.encode("utf-8")
    begin_idx = data.find(begin_bytes)
    if begin_idx < 0:
        return None
    tail_idx = data.find(end_bytes, begin_idx)
    if tail_idx < 0:
        return None
    end = tail_idx + len(end_bytes)
    # Extend the span over a single trailing newline so repeated injects do
    # not accumulate blank lines around the marker region.
    if end < len(data) and data[end : end + 1] == b"\n":
        end += 1
    return (begin_idx, end)


def _atomic_write(path: Path, data: bytes, mode: int = 0o644) -> None:
    parent = path.parent
    parent.mkdir(parents=True, exist_ok=True)
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
    except Exception:
        try:
            os.unlink(tmp_name)
        except OSError:
            pass
        raise
