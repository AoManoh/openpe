"""``product.json`` resource-integrity table patcher.

Electron's main process verifies a handful of bundled resources against
checksums stored under the top-level ``checksums`` key in
``product.json`` (sometimes named ``checksumsForResourceFiles``; we
handle either). When we mutate
``out/vs/workbench/workbench.desktop.main.js`` the recorded checksum no
longer matches and the app refuses to launch.

This module exposes a tiny, well-tested surface for:

* ``find_table`` — locate the checksum table in a parsed ``product.json``;
* ``get_checksum`` — read the entry for a given relative path;
* ``patch_product_json`` — update or remove the entry for a path and
  atomically rewrite the file;
* ``backup_product_json`` / ``restore_product_json`` — snapshot pre-patch
  state so ``uninstall`` can revert cleanly.

The module performs no codesign work; that lives in :mod:`installer.codesign`.
"""

from __future__ import annotations

import base64
import hashlib
import json
from pathlib import Path
from typing import Any, Dict, Iterable, Optional, Tuple

from .bundle import _atomic_write  # type: ignore[attr-defined]

# Two field names have been used historically. Newer Electron uses the
# shorter "checksums"; older builds (and a handful of VS Code derivatives)
# still ship "checksumsForResourceFiles". We accept both.
_CHECKSUM_FIELDS: Tuple[str, ...] = ("checksums", "checksumsForResourceFiles")

# Canonical relative path of the Windsurf workbench bundle, used as the
# default key when callers do not supply one.
DEFAULT_BUNDLE_RELPATH = "vs/workbench/workbench.desktop.main.js"


class ChecksumError(Exception):
    """Raised for any product.json parsing / patching failure."""


def _load(path: Path) -> Dict[str, Any]:
    try:
        text = path.read_text(encoding="utf-8")
    except FileNotFoundError as exc:
        raise ChecksumError(f"product.json not found: {path}") from exc
    except OSError as exc:
        raise ChecksumError(f"read product.json {path}: {exc}") from exc
    try:
        payload = json.loads(text)
    except json.JSONDecodeError as exc:
        raise ChecksumError(f"parse product.json {path}: {exc}") from exc
    if not isinstance(payload, dict):
        raise ChecksumError(f"product.json {path}: root must be a JSON object")
    return payload


def find_table(product: Dict[str, Any]) -> Optional[str]:
    """Return the field name holding the checksum table, or None."""
    for name in _CHECKSUM_FIELDS:
        value = product.get(name)
        if isinstance(value, dict):
            return name
    return None


def get_checksum(product: Dict[str, Any], bundle_relpath: str = DEFAULT_BUNDLE_RELPATH) -> Optional[str]:
    """Read the recorded checksum for ``bundle_relpath`` (or None when absent)."""
    field = find_table(product)
    if field is None:
        return None
    table = product[field]
    value = table.get(bundle_relpath)
    return value if isinstance(value, str) else None


def vscode_checksum(path: Path) -> str:
    """Return the VS Code / Windsurf product.json checksum format for ``path``.

    Format: ``base64(SHA-256(bytes))`` with trailing ``=`` padding stripped.
    This is exactly what Windsurf 1.110.x stores in ``product.json``'s
    ``checksums`` table and verifies at bundle load time. Note that vanilla
    VS Code treats a missing entry as "skip verification", but Windsurf
    enforces "missing entry == corrupted install" and surfaces a user-
    visible warning, so the installer MUST update the entry rather than
    delete it.
    """
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(64 * 1024), b""):
            h.update(chunk)
    return base64.b64encode(h.digest()).decode("ascii").rstrip("=")


def patch_product_json(
    product_path: Path,
    *,
    bundle_relpath: str = DEFAULT_BUNDLE_RELPATH,
    new_value: Optional[str] = None,
) -> None:
    """Atomically rewrite ``product.json`` to bypass the integrity check
    for ``bundle_relpath``.

    When ``new_value`` is ``None`` the entry is removed entirely (Electron
    skips integrity verification for any path not listed); when ``new_value``
    is a string the entry is updated in place. If the checksum table does
    not exist the file is left untouched.
    """
    product = _load(product_path)
    field = find_table(product)
    if field is None:
        # Nothing to patch — file already accepts any payload.
        return
    table: Dict[str, Any] = product[field]
    if new_value is None:
        table.pop(bundle_relpath, None)
    else:
        table[bundle_relpath] = new_value
    serialised = json.dumps(product, indent=2, sort_keys=False, ensure_ascii=False)
    _atomic_write(product_path, serialised.encode("utf-8") + b"\n", mode=0o644)


def backup_product_json(product_path: Path, backup_dir: Path) -> Path:
    """Snapshot ``product.json`` next to the bundle backup."""
    from .bundle import backup as _backup

    return _backup(product_path, backup_dir)


def restore_product_json(product_path: Path, backup_path: Path) -> None:
    """Atomically restore ``product.json`` from ``backup_path``."""
    from .bundle import restore as _restore

    _restore(product_path, backup_path)


def list_checksum_keys(product: Dict[str, Any]) -> Iterable[str]:
    """Yield every key in the active checksum table (empty when none)."""
    field = find_table(product)
    if field is None:
        return ()
    return tuple((product[field] or {}).keys())
