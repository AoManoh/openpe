"""Command-line entry point for the openpe-windsurf-patch installer.

Phase-1 skeleton: every mutating subcommand prints the EULA disclaimer and
exits without touching disk. Subsequent commits fill in real path
resolution, bundle patching, and inject.js wiring.
"""

from __future__ import annotations

import argparse
import sys
from typing import List, Optional, Sequence

from . import __version__

EULA_DISCLAIMER = """\
============================================================================
  ⚠️  EXPERIMENTAL — USER ASSUMES ALL RISK
============================================================================

This installer patches the Windsurf IDE Electron bundle in place. By
proceeding you acknowledge that you have read the README and accept:

  • Possible EULA violation — Windsurf / Codeium may suspend your account
    or refuse support for a patched install.
  • Code-signing invalidation on macOS (Gatekeeper may refuse to launch).
  • Disabled checksum integrity check for the patched file only.
  • Upgrade fragility — every Windsurf update overwrites the patch.
  • No warranty whatsoever.

Default openPE paths that DO NOT carry these risks:

  • openpe windsurf hook install         (terminal `pe ...` keyword)
  • extensions/vscode-openpe/             (VS Code / Windsurf VSIX plugin)

If you accept the risk, re-run with --i-accept-experimental-risk.

============================================================================
"""

EXIT_OK = 0
EXIT_USAGE = 2
EXIT_NOT_YET_IMPLEMENTED = 64
EXIT_DISCLAIMER_NOT_ACCEPTED = 65


def _print_disclaimer() -> None:
    sys.stderr.write(EULA_DISCLAIMER)


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="openpe-windsurf-patch",
        description=(
            "Experimental Windsurf IDE bundle patcher for openPE. "
            "See README for the full disclaimer."
        ),
    )
    parser.add_argument(
        "--version", action="version", version=f"%(prog)s {__version__}"
    )
    subparsers = parser.add_subparsers(dest="command", metavar="COMMAND")

    install = subparsers.add_parser(
        "install",
        help="patch the Windsurf bundle (currently a stub; prints EULA and exits)",
    )
    install.add_argument(
        "--app-dir",
        default=None,
        help="override the Windsurf application directory (auto-detected by default)",
    )
    install.add_argument(
        "--dry-run",
        action="store_true",
        help="describe the actions that would be taken without touching disk",
    )
    install.add_argument(
        "--i-accept-experimental-risk",
        action="store_true",
        help="acknowledge the EULA / user-assumes-risk disclaimer non-interactively",
    )

    uninstall = subparsers.add_parser(
        "uninstall",
        help="restore the Windsurf bundle from the most recent backup",
    )
    uninstall.add_argument(
        "--app-dir",
        default=None,
        help="override the Windsurf application directory",
    )

    status = subparsers.add_parser(
        "status",
        help="report whether the bundle is currently patched + backup state",
    )
    status.add_argument(
        "--app-dir",
        default=None,
        help="override the Windsurf application directory",
    )

    subparsers.add_parser(
        "doctor",
        help="environment self-check (Python version, IDE detected, server descriptor)",
    )

    return parser


def _cmd_install(args: argparse.Namespace) -> int:
    if not args.i_accept_experimental_risk:
        _print_disclaimer()
        return EXIT_DISCLAIMER_NOT_ACCEPTED
    sys.stderr.write(
        "openpe-windsurf-patch: install is not yet implemented (Phase 3, see README).\n"
    )
    return EXIT_NOT_YET_IMPLEMENTED


def _cmd_uninstall(args: argparse.Namespace) -> int:
    sys.stderr.write(
        "openpe-windsurf-patch: uninstall is not yet implemented (Phase 3, see README).\n"
    )
    return EXIT_NOT_YET_IMPLEMENTED


def _cmd_status(args: argparse.Namespace) -> int:
    sys.stdout.write(
        f"openpe-windsurf-patch {__version__} (Phase 1 skeleton)\n"
        "  injected:        unknown (path resolution not yet implemented)\n"
        "  backup present:  unknown\n"
        "  ide version:     unknown\n"
        "  server descriptor: not checked yet (Phase 2)\n"
    )
    return EXIT_OK


def _cmd_doctor(args: argparse.Namespace) -> int:
    sys.stdout.write(
        f"openpe-windsurf-patch {__version__} doctor (Phase 1 skeleton)\n"
        f"  python:     {sys.version.split()[0]} ({sys.executable})\n"
        "  paths:      not yet implemented (Phase 2)\n"
        "  ide:        not yet implemented (Phase 2)\n"
        "  descriptor: not yet implemented (Phase 2)\n"
        "  bundle:     not yet implemented (Phase 3)\n"
    )
    return EXIT_OK


_DISPATCH = {
    "install": _cmd_install,
    "uninstall": _cmd_uninstall,
    "status": _cmd_status,
    "doctor": _cmd_doctor,
}


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv if argv is not None else sys.argv[1:])
    if not args.command:
        parser.print_help(sys.stderr)
        return EXIT_USAGE
    handler = _DISPATCH.get(args.command)
    if handler is None:
        parser.print_help(sys.stderr)
        return EXIT_USAGE
    return handler(args)


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
