"""Cross-platform Windsurf install path resolution.

Returns a :class:`WindsurfPaths` dataclass describing where the Electron
bundle, product manifest and backup directory live for the current host.
An explicit ``--app-dir`` override always wins; otherwise we walk a small
list of platform-specific candidate roots and pick the first one that
exists.

The module intentionally does no I/O beyond ``Path.is_dir`` / ``Path.is_file``
and exposes the candidate lists as helpers so that unit tests can drive
each branch deterministically without monkey-patching ``platform.system``.
"""

from __future__ import annotations

import os
import platform
from dataclasses import dataclass
from pathlib import Path
from typing import List, Optional

# Relative paths inside the Windsurf resources/app directory.
_BUNDLE_REL = Path("out") / "vs" / "workbench" / "workbench.desktop.main.js"
_PRODUCT_REL = Path("product.json")


@dataclass(frozen=True)
class WindsurfPaths:
    """File-system layout for a single Windsurf installation."""

    app_root: Path
    bundle_file: Path
    product_file: Path
    backup_dir: Path

    @property
    def exists(self) -> bool:
        """True when both the bundle and product manifest are present on disk."""
        return self.bundle_file.is_file() and self.product_file.is_file()


def default_backup_root() -> Path:
    """Return the directory used to persist backup snapshots across calls.

    Resolution order:
      1. ``OPENPE_WINDSURF_PATCH_BACKUP_DIR`` env override
      2. ``$XDG_DATA_HOME/openpe-windsurf-patch/backup``
      3. ``$HOME/.local/share/openpe-windsurf-patch/backup``
    """
    explicit = os.environ.get("OPENPE_WINDSURF_PATCH_BACKUP_DIR", "").strip()
    if explicit:
        return Path(explicit)
    xdg = os.environ.get("XDG_DATA_HOME", "").strip()
    if xdg:
        return Path(xdg) / "openpe-windsurf-patch" / "backup"
    return Path.home() / ".local" / "share" / "openpe-windsurf-patch" / "backup"


def macos_candidates() -> List[Path]:
    """Default macOS install locations, system-wide first."""
    return [
        Path("/Applications/Windsurf.app"),
        Path.home() / "Applications" / "Windsurf.app",
    ]


def linux_candidates() -> List[Path]:
    """Default Linux install locations, system-wide first."""
    return [
        Path("/opt/Windsurf"),
        Path("/usr/share/windsurf"),
        Path.home() / ".local" / "share" / "Windsurf",
    ]


def windows_candidates() -> List[Path]:
    """Default Windows install locations under LocalAppData / Program Files."""
    paths: List[Path] = []
    local_app = os.environ.get("LocalAppData") or os.environ.get("LOCALAPPDATA")
    program_files = os.environ.get("ProgramFiles") or os.environ.get("PROGRAMFILES")
    if local_app:
        paths.append(Path(local_app) / "Programs" / "Windsurf")
    if program_files:
        paths.append(Path(program_files) / "Windsurf")
    return paths


def platform_candidates(system: Optional[str] = None) -> List[Path]:
    """Return the candidate list for the supplied OS (defaults to host)."""
    system = system or platform.system()
    if system == "Darwin":
        return macos_candidates()
    if system == "Linux":
        return linux_candidates()
    if system == "Windows":
        return windows_candidates()
    return []


def resources_dir(app_root: Path, system: Optional[str] = None) -> Path:
    """Return the ``resources/app`` (Win/Linux) or
    ``Contents/Resources/app`` (macOS) directory for the given app root."""
    system = system or platform.system()
    if system == "Darwin":
        return app_root / "Contents" / "Resources" / "app"
    return app_root / "resources" / "app"


def detect_app_root(override: Optional[str] = None, system: Optional[str] = None) -> Optional[Path]:
    """Return the most specific Windsurf install root found on this host.

    When ``override`` is set, it must point at an existing directory; we do
    not silently fall back to the default candidates because that would
    surprise users who deliberately picked a non-default location.
    """
    if override:
        candidate = Path(override).expanduser().resolve()
        return candidate if candidate.is_dir() else None
    for candidate in platform_candidates(system):
        if candidate.is_dir():
            return candidate
    return None


def resolve_paths(
    override: Optional[str] = None,
    system: Optional[str] = None,
    backup_root: Optional[Path] = None,
) -> Optional[WindsurfPaths]:
    """Compose a :class:`WindsurfPaths` for the current host.

    Returns ``None`` when no Windsurf install can be located. ``backup_root``
    overrides :func:`default_backup_root` (used by tests).
    """
    app_root = detect_app_root(override, system=system)
    if app_root is None:
        return None
    res = resources_dir(app_root, system=system)
    bundle = res / _BUNDLE_REL
    product = res / _PRODUCT_REL
    root = backup_root if backup_root is not None else default_backup_root()
    backup = root / app_root.name
    return WindsurfPaths(
        app_root=app_root,
        bundle_file=bundle,
        product_file=product,
        backup_dir=backup,
    )
