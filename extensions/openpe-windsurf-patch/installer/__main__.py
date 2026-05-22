"""Command-line entry point for the openpe-windsurf-patch installer.

Implements the four subcommands (install / uninstall / status / doctor)
on top of the Phase 2-3 modules:

* :mod:`installer.paths` — Windsurf install discovery
* :mod:`installer.handshake` — local openpe-server descriptor + /v1/info
* :mod:`installer.bundle` — marker injection + backup / restore
* :mod:`installer.checksum` — product.json integrity bypass
* :mod:`installer.codesign` — macOS ad-hoc re-signing

The EULA / user-assumes-risk disclaimer is enforced at the CLI boundary;
no mutating operation runs without an explicit user acknowledgement.
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path
from typing import Optional, Sequence

from . import __version__
from .bundle import (
    BundleError,
    backup as backup_bundle,
    has_marker,
    inject as inject_bundle,
    restore as restore_bundle,
)
from .checksum import (
    ChecksumError,
    DEFAULT_BUNDLE_RELPATH,
    backup_product_json,
    patch_product_json,
    restore_product_json,
)
from .codesign import CodesignError, codesign_app, is_macos, remove_quarantine_hint
from .handshake import (
    DescriptorError,
    HandshakeError,
    LocalServerDescriptor,
    default_descriptor_path,
    read_descriptor,
    verify_server,
)
from .paths import WindsurfPaths, resolve_paths

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
EXIT_PATH_NOT_FOUND = 66
EXIT_DESCRIPTOR_ERROR = 67
EXIT_HANDSHAKE_ERROR = 68
EXIT_INJECT_PAYLOAD_MISSING = 69
EXIT_BUNDLE_ERROR = 70
EXIT_NO_BACKUP = 71
EXIT_CODESIGN_ERROR = 72


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


def _subproject_root() -> Path:
    """Return the on-disk root of the openpe-windsurf-patch subproject."""
    return Path(__file__).resolve().parent.parent


def _load_inject_payload() -> Optional[Path]:
    """Locate the built inject.js payload (Phase 4 artefact)."""
    candidate = _subproject_root() / "inject" / "dist" / "inject.js"
    return candidate if candidate.is_file() else None


def _find_latest_backup(backup_dir: Path, bundle_name: str) -> Optional[Path]:
    """Return the most recent ``<bundle>.<ts>.original`` snapshot, or None."""
    if not backup_dir.is_dir():
        return None
    snapshots = sorted(
        backup_dir.glob(f"{bundle_name}.*.original"),
        key=lambda p: p.stat().st_mtime,
        reverse=True,
    )
    return snapshots[0] if snapshots else None


def _resolve_or_explain(override: Optional[str]) -> Optional[WindsurfPaths]:
    paths = resolve_paths(override=override)
    if paths is None:
        sys.stderr.write(
            "openpe-windsurf-patch: could not locate a Windsurf install.\n"
            "  • pass --app-dir /path/to/Windsurf(.app) to override path detection\n"
            "  • or install Windsurf at the platform-default location\n"
        )
        return None
    if not paths.exists:
        sys.stderr.write(
            f"openpe-windsurf-patch: Windsurf install at {paths.app_root} is incomplete.\n"
            f"  expected bundle:  {paths.bundle_file}\n"
            f"  expected product: {paths.product_file}\n"
        )
        return None
    return paths


def _read_descriptor_or_explain() -> Optional[LocalServerDescriptor]:
    try:
        descriptor = read_descriptor()
    except DescriptorError as exc:
        sys.stderr.write(
            f"openpe-windsurf-patch: cannot read openpe-server descriptor: {exc}\n"
            "  start openpe-server with OPENPE_SERVER_LIFECYCLE_ENABLED=true,\n"
            f"  or set OPENPE_SERVER_DESCRIPTOR_FILE to override the default path\n"
            f"  ({default_descriptor_path()}).\n"
        )
        return None
    return descriptor


def _print_status(paths: Optional[WindsurfPaths], descriptor_outcome: str, marker_present: bool, backup_path: Optional[Path]) -> None:
    sys.stdout.write(f"openpe-windsurf-patch {__version__}\n")
    if paths is None:
        sys.stdout.write("  ide:             not detected\n")
    else:
        sys.stdout.write(f"  ide root:        {paths.app_root}\n")
        sys.stdout.write(f"  bundle:          {paths.bundle_file}\n")
        sys.stdout.write(f"  product:         {paths.product_file}\n")
        sys.stdout.write(f"  injected:        {'yes' if marker_present else 'no'}\n")
        sys.stdout.write(
            f"  backup present:  {'yes (' + backup_path.name + ')' if backup_path else 'no'}\n"
        )
    sys.stdout.write(f"  server descriptor: {descriptor_outcome}\n")


def _cmd_install(args: argparse.Namespace) -> int:
    if not args.i_accept_experimental_risk:
        _print_disclaimer()
        return EXIT_DISCLAIMER_NOT_ACCEPTED
    paths = _resolve_or_explain(args.app_dir)
    if paths is None:
        return EXIT_PATH_NOT_FOUND
    descriptor = _read_descriptor_or_explain()
    if descriptor is None:
        return EXIT_DESCRIPTOR_ERROR
    try:
        verify_server(descriptor)
    except HandshakeError as exc:
        sys.stderr.write(
            f"openpe-windsurf-patch: cannot reach openpe-server: {exc}\n"
            "  is openpe-server actually running, and does the descriptor's bearer token match?\n"
        )
        return EXIT_HANDSHAKE_ERROR
    payload_path = _load_inject_payload()
    if payload_path is None:
        sys.stderr.write(
            "openpe-windsurf-patch: inject payload missing.\n"
            f"  expected file: {_subproject_root() / 'inject' / 'dist' / 'inject.js'}\n"
            "  run `npm install && npm run build` inside inject/ (Phase 4 task).\n"
        )
        return EXIT_INJECT_PAYLOAD_MISSING
    if args.dry_run:
        sys.stdout.write(
            f"DRY RUN — would patch:\n"
            f"  bundle:  {paths.bundle_file}\n"
            f"  product: {paths.product_file}\n"
            f"  backup → {paths.backup_dir}\n"
            f"  payload: {payload_path} ({payload_path.stat().st_size} bytes)\n"
            f"  codesign: {'yes (macOS)' if is_macos() else 'no (non-macOS)'}\n"
        )
        return EXIT_OK
    try:
        bundle_backup = backup_bundle(paths.bundle_file, paths.backup_dir)
        product_backup = backup_product_json(paths.product_file, paths.backup_dir)
        sys.stderr.write(f"  ✓ backup bundle  → {bundle_backup}\n")
        sys.stderr.write(f"  ✓ backup product → {product_backup}\n")
        inject_bundle(paths.bundle_file, payload_path.read_text(encoding="utf-8"))
        sys.stderr.write("  ✓ injected payload into bundle\n")
        patch_product_json(paths.product_file, bundle_relpath=DEFAULT_BUNDLE_RELPATH)
        sys.stderr.write("  ✓ patched product.json (checksum entry removed)\n")
    except (BundleError, ChecksumError) as exc:
        sys.stderr.write(f"openpe-windsurf-patch: install failed mid-patch: {exc}\n")
        return EXIT_BUNDLE_ERROR
    if is_macos():
        try:
            codesign_app(paths.app_root)
            sys.stderr.write("  ✓ codesign (ad-hoc) succeeded\n")
        except CodesignError as exc:
            sys.stderr.write(
                f"openpe-windsurf-patch: codesign failed: {exc}\n"
                f"  bundle is patched but Gatekeeper will block launch.\n"
                f"  manual fix: {remove_quarantine_hint(paths.app_root)}\n"
            )
            return EXIT_CODESIGN_ERROR
    sys.stdout.write(
        "openpe-windsurf-patch: install complete.\n"
        "  restart Windsurf to pick up the ✨ button.\n"
        f"  to revert: python3 -m installer uninstall {('--app-dir ' + args.app_dir) if args.app_dir else ''}\n"
    )
    return EXIT_OK


def _cmd_uninstall(args: argparse.Namespace) -> int:
    paths = _resolve_or_explain(args.app_dir)
    if paths is None:
        return EXIT_PATH_NOT_FOUND
    bundle_backup = _find_latest_backup(paths.backup_dir, paths.bundle_file.name)
    product_backup = _find_latest_backup(paths.backup_dir, paths.product_file.name)
    if bundle_backup is None or product_backup is None:
        sys.stderr.write(
            "openpe-windsurf-patch: no backup found; cannot safely uninstall.\n"
            f"  searched: {paths.backup_dir}\n"
            "  if the IDE is unpatched there is nothing to do; otherwise reinstall Windsurf cleanly.\n"
        )
        return EXIT_NO_BACKUP
    try:
        restore_bundle(paths.bundle_file, bundle_backup)
        sys.stderr.write(f"  ✓ restored bundle from {bundle_backup.name}\n")
        restore_product_json(paths.product_file, product_backup)
        sys.stderr.write(f"  ✓ restored product.json from {product_backup.name}\n")
    except BundleError as exc:
        sys.stderr.write(f"openpe-windsurf-patch: restore failed: {exc}\n")
        return EXIT_BUNDLE_ERROR
    if is_macos():
        try:
            codesign_app(paths.app_root)
            sys.stderr.write("  ✓ codesign (ad-hoc) re-applied\n")
        except CodesignError as exc:
            sys.stderr.write(
                f"openpe-windsurf-patch: codesign after restore failed: {exc}\n"
                f"  manual fix: {remove_quarantine_hint(paths.app_root)}\n"
            )
            return EXIT_CODESIGN_ERROR
    sys.stdout.write("openpe-windsurf-patch: uninstall complete.\n")
    return EXIT_OK


def _cmd_status(args: argparse.Namespace) -> int:
    paths = resolve_paths(override=args.app_dir)
    marker_present = False
    backup_path: Optional[Path] = None
    if paths is not None and paths.exists:
        try:
            marker_present = has_marker(paths.bundle_file)
        except BundleError:
            marker_present = False
        backup_path = _find_latest_backup(paths.backup_dir, paths.bundle_file.name)
    descriptor_outcome = "not checked"
    try:
        descriptor = read_descriptor()
        descriptor_outcome = (
            f"present (base_url={descriptor.base_url}, pid={descriptor.pid}, version={descriptor.version or 'unknown'})"
        )
    except DescriptorError as exc:
        descriptor_outcome = f"unavailable ({exc})"
    _print_status(paths, descriptor_outcome, marker_present, backup_path)
    return EXIT_OK


def _cmd_doctor(args: argparse.Namespace) -> int:
    sys.stdout.write(f"openpe-windsurf-patch {__version__} doctor\n")
    sys.stdout.write(f"  python:           {sys.version.split()[0]} ({sys.executable})\n")
    sys.stdout.write(f"  platform:         {sys.platform}\n")
    sys.stdout.write(f"  codesign needed:  {'yes (macOS)' if is_macos() else 'no'}\n")
    paths = resolve_paths()
    if paths is None:
        sys.stdout.write("  ide:              not detected at default paths\n")
    else:
        sys.stdout.write(f"  ide app root:     {paths.app_root}\n")
        sys.stdout.write(
            f"  ide bundle:       {paths.bundle_file} (exists={paths.bundle_file.is_file()})\n"
        )
        sys.stdout.write(
            f"  ide product:      {paths.product_file} (exists={paths.product_file.is_file()})\n"
        )
        sys.stdout.write(f"  backup dir:       {paths.backup_dir}\n")
    descriptor_path = default_descriptor_path()
    sys.stdout.write(f"  descriptor path:  {descriptor_path}\n")
    try:
        descriptor = read_descriptor()
        sys.stdout.write(f"  descriptor:       OK (pid={descriptor.pid}, version={descriptor.version or 'unknown'})\n")
        try:
            info = verify_server(descriptor)
            sys.stdout.write(
                f"  /v1/info:         200 (server version={info.get('version', 'unknown')})\n"
            )
        except HandshakeError as exc:
            sys.stdout.write(f"  /v1/info:         FAIL ({exc})\n")
    except DescriptorError as exc:
        sys.stdout.write(f"  descriptor:       FAIL ({exc})\n")
    payload = _load_inject_payload()
    if payload is None:
        sys.stdout.write("  inject payload:   missing (run `npm run build` inside inject/)\n")
    else:
        sys.stdout.write(f"  inject payload:   {payload} ({payload.stat().st_size} bytes)\n")
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
