"""macOS ``codesign`` helpers.

Patching ``workbench.desktop.main.js`` invalidates Apple's notarisation
on the Windsurf.app bundle. macOS Gatekeeper will then refuse to launch
the IDE until either:

* the bundle is ad-hoc re-signed with ``codesign --force --deep --sign -``
  (the default behaviour exposed here), or
* the user manually removes the quarantine attribute with
  ``xattr -d com.apple.quarantine /Applications/Windsurf.app``.

This module is platform-aware: every public function is a no-op on
non-macOS hosts so the rest of the installer can call it
unconditionally.
"""

from __future__ import annotations

import platform
import subprocess
from pathlib import Path
from typing import Callable, Sequence


class CodesignError(Exception):
    """Raised for any codesign failure."""


def is_macos() -> bool:
    return platform.system() == "Darwin"


# Type alias for an injected subprocess runner. Tests pass a stub that
# never touches the real ``codesign`` binary.
Runner = Callable[..., "subprocess.CompletedProcess[str]"]


def codesign_app(
    app_root: Path,
    *,
    identity: str = "-",
    runner: Runner = subprocess.run,
    timeout_seconds: float = 120.0,
) -> None:
    """Re-sign a macOS application bundle.

    Defaults to ad-hoc signing (``identity="-"``) which does not require
    an Apple Developer certificate. Pass a Common Name string to use a
    Developer ID certificate instead.

    Behaviour:

    * On non-macOS hosts this is a no-op.
    * Missing ``codesign`` binary raises ``CodesignError`` with a hint
      to install the Xcode Command Line Tools.
    * Non-zero exit, timeout, or any OSError raises ``CodesignError``
      with the stderr captured.
    """
    if not is_macos():
        return
    if not app_root.is_dir():
        raise CodesignError(f"app root does not exist: {app_root}")
    cmd: Sequence[str] = [
        "codesign",
        "--force",
        "--deep",
        "--sign",
        identity,
        str(app_root),
    ]
    try:
        result = runner(
            cmd,
            capture_output=True,
            text=True,
            timeout=timeout_seconds,
        )
    except FileNotFoundError as exc:
        raise CodesignError(
            "codesign binary not found; install Xcode Command Line Tools "
            "via `xcode-select --install`"
        ) from exc
    except subprocess.TimeoutExpired as exc:
        raise CodesignError(
            f"codesign timed out after {timeout_seconds:.0f}s: {exc}"
        ) from exc
    except OSError as exc:
        raise CodesignError(f"codesign invocation failed: {exc}") from exc
    if getattr(result, "returncode", 0) != 0:
        stderr = (getattr(result, "stderr", "") or "").strip()
        raise CodesignError(
            f"codesign failed (exit {result.returncode}): {stderr or 'no stderr captured'}"
        )


def remove_quarantine_hint(app_root: Path) -> str:
    """Return the user-facing command to drop ``com.apple.quarantine``.

    The installer never runs this automatically; the user must opt in
    because the attribute exists for a reason.
    """
    return f"xattr -d com.apple.quarantine {app_root}"
