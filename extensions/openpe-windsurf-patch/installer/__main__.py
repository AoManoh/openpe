"""Canonical CLI orchestration for the profile-gated IDE patch installer.

The four subcommands compose manifest-based host discovery, local server
handshake, marker injection, checksum verification, transaction recovery,
and platform process/signing gates. Mutation also requires an explicitly
enabled and verified host profile; current profiles remain read-only.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import platform
import shutil
import site
import sys
import sysconfig
import tempfile
from pathlib import Path
from typing import Any, Dict, List, Optional, Sequence

from . import __version__
from .backup_transaction import (
    BackupTransaction,
    TransactionError,
    create_transaction,
    find_restoring_transaction,
    load_transaction,
    recover_restoring_transaction,
    restore_transaction,
    validate_transaction_for_restore,
)
from .bundle import BundleError, has_marker, inject as inject_bundle
from .checksum import (
    ChecksumError,
    DEFAULT_BUNDLE_RELPATH,
    patch_product_json,
    verify_bundle_checksum,
    verify_trusted_baseline,
    vscode_checksum,
)
from .codesign import CodesignError, codesign_app, is_macos
from .handshake import (
    DescriptorError,
    HandshakeError,
    LocalServerDescriptor,
    default_descriptor_path,
    read_descriptor,
    validate_profile_cors,
    verify_server,
)
from .locking import LockError, mutation_lock
from .patch_operation import (
    OperationError,
    PatchOperation,
    apply_operation,
    complete_operation,
    finalize_operation_transaction,
    find_active_operation,
    prepare_operation,
    rollback_operation,
)
from .paths import IDEPaths, PathResolutionError, resolve_paths
from .processes import ProcessError, inspect_host_processes, require_host_stopped
from .profiles import DEVIN_PROFILE, ProfileError, SupportedBuild, require_profile
from .runtime_probe import ProbeError, build_probe_payload, validate_probe_endpoint

EULA_DISCLAIMER = """\
============================================================================
  EXPERIMENTAL — USER ASSUMES ALL RISK
============================================================================

This installer patches a supported IDE Electron bundle in place. By
proceeding you acknowledge that you have read the README and accept:

  - Possible EULA or support-policy impact.
  - Code-signing invalidation on platforms that sign application bundles.
  - A modified checksum baseline for the patched file.
  - Upgrade fragility because IDE updates overwrite the patch.
  - No warranty whatsoever.

Default openPE paths that do not modify the IDE bundle:

  - openpe devin hook install
  - openpe windsurf hook install

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
EXIT_PROFILE_ERROR = 73
EXIT_TRANSACTION_ERROR = 74
EXIT_PROCESS_RUNNING = 75
EXIT_UNSUPPORTED_PROFILE = 76


def _print_disclaimer() -> None:
    sys.stderr.write(EULA_DISCLAIMER)


def _non_negative_int(value: str) -> int:
    parsed = int(value)
    if parsed < 0:
        raise argparse.ArgumentTypeError("must be a non-negative integer")
    return parsed


def _add_host_option(parser: argparse.ArgumentParser) -> None:
    parser.add_argument(
        "--host",
        choices=("auto", "devin", "windsurf"),
        default="auto",
        help="select a detected host profile; --app-dir never overrides product identity",
    )


def _build_parser(prog: str = "openpe-ide-patch") -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog=prog,
        description=(
            "Experimental IDE bundle patcher for openPE. "
            "See README for the full disclaimer."
        ),
    )
    parser.add_argument(
        "--version", action="version", version=f"%(prog)s {__version__}"
    )
    subparsers = parser.add_subparsers(dest="command", metavar="COMMAND")

    install = subparsers.add_parser(
        "install",
        help="patch a supported IDE bundle after explicit experimental-risk acceptance",
    )
    _add_host_option(install)
    install.add_argument(
        "--app-dir",
        default=None,
        help="override the IDE application directory (auto-detected by default)",
    )
    install.add_argument(
        "--dry-run",
        action="store_true",
        help="describe the actions that would be taken without touching disk",
    )
    install.add_argument(
        "--fs-probe",
        action="store_true",
        help=(
            "enable a renderer-side filesystem probe that reads the 0600 "
            "openpe-server descriptor and logs non-secret diagnostics"
        ),
    )
    install.add_argument(
        "--debug",
        action="store_true",
        help=(
            "enable dev/test diagnostics inside the inject layer: verbose "
            "console.warn traces from the cascade-context observer and a "
            "read-only globalThis.__openpeDebug namespace exposing shape-only "
            "views of internal state (no full message bodies, no token, no "
            "Authorization). Default off; production installs stay silent."
        ),
    )
    install.add_argument(
        "--max-context-tokens",
        type=_non_negative_int,
        default=None,
        metavar="N",
        help=(
            "consumer-layer token budget for prompt enhancement. Snapshotted "
            "into the bundle as `globalThis.__openpe.maxContextTokens` and "
            "forwarded by the inject layer to /v1/prompt-enhance via "
            "Options.MaxContextTokens (json: max_context_tokens). The server "
            "applies a ~4-char-per-token approximation to shrink retrieval / "
            "history sections; required sections always survive. Default: "
            "fall back to OPENPE_MAX_CONTEXT_TOKENS env (mirrors the hook "
            "adapter convention); if both unset, send no budget (server uses "
            "0 = no shrinking). Pass an explicit 0 to override env=non-zero "
            "and disable shrinking. Note: this is NOT the cascade history "
            "collector budget (32 msg / 6000 char/msg / 80000 char total) — "
            "those are empirical collector-layer tuning, not configurable."
        ),
    )
    install.add_argument(
        "--probe-endpoint",
        default=None,
        help=(
            "install a probe-only payload that sends structural runtime metadata "
            "to a tokenized http://127.0.0.1 endpoint; bypasses no build/process/transaction gate"
        ),
    )
    install.add_argument(
        "--i-accept-experimental-risk",
        action="store_true",
        help="acknowledge the EULA / user-assumes-risk disclaimer non-interactively",
    )

    uninstall = subparsers.add_parser(
        "uninstall",
        help="restore an injected IDE bundle from its exact backup transaction",
    )
    _add_host_option(uninstall)
    uninstall.add_argument(
        "--app-dir",
        default=None,
        help="override the IDE application directory",
    )

    status = subparsers.add_parser(
        "status",
        help="report product, profile, injection, transaction, and server state",
    )
    _add_host_option(status)
    status.add_argument(
        "--app-dir",
        default=None,
        help="override the IDE application directory",
    )

    doctor = subparsers.add_parser(
        "doctor",
        help="environment self-check (Python, IDE profile, process, and server)",
    )
    _add_host_option(doctor)
    doctor.add_argument(
        "--app-dir",
        default=None,
        help="override the IDE application directory",
    )

    return parser


def _subproject_root() -> Path:
    """Return the on-disk root of the openpe-windsurf-patch subproject."""
    return Path(__file__).resolve().parent.parent


def _load_inject_payload() -> Optional[Path]:
    """Locate the built inject.js payload from source or package data."""
    for candidate in _inject_payload_candidates():
        if candidate.is_file():
            return candidate
    return None


def _inject_payload_candidates() -> List[Path]:
    candidates = [_subproject_root() / "inject" / "dist" / "inject.js"]
    data_roots = [
        sysconfig.get_paths().get("data", "").strip(),
        getattr(site, "USER_BASE", "").strip(),
    ]
    seen = {str(candidates[0])}
    for data_root in data_roots:
        if not data_root:
            continue
        candidate = (
            Path(data_root)
            / "share"
            / "openpe-windsurf-patch"
            / "inject"
            / "dist"
            / "inject.js"
        )
        if str(candidate) in seen:
            continue
        candidates.append(candidate)
        seen.add(str(candidate))
    return candidates


def _resolve_max_context_tokens(cli_value: Optional[int]) -> Optional[int]:
    """Resolve the consumer-layer token budget from CLI flag or env var.

    Resolution rules (CLI wins to honour the principle of least surprise
    for the user actively typing ``--max-context-tokens``):

    1. If ``cli_value`` is not None (i.e. the user passed
       ``--max-context-tokens N`` — including ``--max-context-tokens 0``),
       return ``max(0, cli_value)``. ``0`` explicitly means "no budget"
       (Go side treats ``MaxContextTokens=0`` as "do not shrink").
    2. Else read ``OPENPE_MAX_CONTEXT_TOKENS`` from the environment.
       Empty / unset → return None (no field emitted in the bootstrap,
       so the inject layer omits the wire field, matching omitempty on
       the Go ``Options.MaxContextTokens`` JSON tag).
    3. Else parse as int. Non-int / negative → warn to stderr and return
       None (mirrors Go's "ignore invalid env, keep default" behaviour
       documented in ``internal/config/config.go``).

    Returns:
        Optional[int]: positive int → emit, 0 → emit (explicit disable),
        None → omit field entirely.
    """
    if cli_value is not None:
        return max(0, int(cli_value))
    raw = os.environ.get("OPENPE_MAX_CONTEXT_TOKENS", "").strip()
    if not raw:
        return None
    try:
        parsed = int(raw)
    except ValueError:
        sys.stderr.write(
            f"openpe-ide-patch: ignoring invalid OPENPE_MAX_CONTEXT_TOKENS={raw!r} "
            "(must be a non-negative integer); falling back to no budget.\n"
        )
        return None
    if parsed < 0:
        sys.stderr.write(
            f"openpe-ide-patch: ignoring negative OPENPE_MAX_CONTEXT_TOKENS={parsed} "
            "(must be >= 0); falling back to no budget.\n"
        )
        return None
    return parsed


def _build_payload_prelude(
    descriptor: LocalServerDescriptor,
    descriptor_path: Optional[Path] = None,
    fs_probe: bool = False,
    debug: bool = False,
    max_context_tokens: Optional[int] = None,
    profile_id: str = "",
    product_commit: str = "",
    transaction_id: str = "",
    client: str = "",
    mode: str = "",
    history_source: str = "none",
    legacy_live_patch: bool = False,
) -> str:
    """Render the ``globalThis.__openpe`` bootstrap injected before inject.js.

    ``inject/src/auth.ts`` reads ``window.__openpe`` for the live server
    base_url / token, and ``inject/src/index.ts`` silently aborts when
    either field is missing. Without this prelude the inject IIFE would
    load on every Windsurf launch but never render the button — install
    must snapshot the descriptor into the bundle.

    Caveat: this embeds the bearer token into the on-disk bundle. When
    openpe-server is restarted with a new token (the default ``ephemeral
    (lifecycle auto-generated)`` mode), re-run ``installer install`` so
    the bundle picks up the fresh token. ``uninstall`` byte-restores the
    pre-install bundle.

    The ``max_context_tokens`` argument is the consumer-layer token
    budget resolved by :func:`_resolve_max_context_tokens`. We only
    embed the field when it's a positive int — matching the Go side's
    ``json:"max_context_tokens,omitempty"`` so a value of 0 stays
    indistinguishable from absent on the wire (both mean "no shrinking"
    on the server). When the user explicitly passes
    ``--max-context-tokens 0`` we still omit it because the on-wire
    semantics are identical and the smaller bootstrap is cheaper to
    re-render on every Windsurf launch.
    """
    config = {
        "baseUrl": descriptor.base_url,
        "token": descriptor.token,
        "version": descriptor.version or "unknown",
        "hostProfileId": profile_id,
        "productCommit": product_commit,
        "transactionId": transaction_id,
        "client": client,
        "mode": mode,
        "historySource": history_source,
    }
    if descriptor_path is not None:
        config["descriptorPath"] = str(descriptor_path)
    if fs_probe:
        config["fsProbe"] = True
    if debug:
        config["debug"] = True
    if max_context_tokens is not None and max_context_tokens > 0:
        config["maxContextTokens"] = int(max_context_tokens)
    if legacy_live_patch:
        config["legacyLivePatch"] = True
    return (
        "/* === OPENPE-BOOTSTRAP === */\n"
        "/* rewritten by installer at install time; do not edit by hand */\n"
        f"globalThis.__openpe = {json.dumps(config, ensure_ascii=False)};\n"
    )


def _build_probe_prelude(
    profile_id: str,
    product_commit: str,
    transaction_id: str,
    client: str,
    mode: str,
    history_source: str,
) -> str:
    config = {
        "hostProfileId": profile_id,
        "productCommit": product_commit,
        "transactionId": transaction_id,
        "client": client,
        "mode": mode,
        "historySource": history_source,
        "probeOnly": True,
    }
    return (
        "/* === OPENPE-BOOTSTRAP === */\n"
        f"globalThis.__openpe = {json.dumps(config, ensure_ascii=False)};\n"
    )


def _is_strict_probe_config(config: Optional[Dict[str, Any]]) -> bool:
    if config is None or config.get("probeOnly") is not True:
        return False
    return set(config) == {
        "hostProfileId",
        "productCommit",
        "transactionId",
        "client",
        "mode",
        "historySource",
        "probeOnly",
    }


def _is_legacy_live_config(config: Optional[Dict[str, Any]]) -> bool:
    return (
        config is not None
        and config.get("legacyLivePatch") is True
        and config.get("probeOnly") is not True
        and config.get("hostProfileId") == DEVIN_PROFILE.profile_id
        and config.get("client") == DEVIN_PROFILE.client
        and config.get("mode") == DEVIN_PROFILE.mode
    )


def _read_embedded_openpe_config(bundle_file: Path) -> Optional[Dict[str, Any]]:
    """Return the ``globalThis.__openpe`` object embedded in a patched bundle.

    The injected button uses a snapshot of the server descriptor captured at
    install time. Comparing that snapshot with the current descriptor gives
    users an actionable diagnosis when the button starts returning 401 after
    ``openpe-server`` has been restarted with a fresh ephemeral token.
    """
    try:
        text = bundle_file.read_text(encoding="utf-8", errors="ignore")
    except OSError:
        return None
    needle = "globalThis.__openpe = "
    idx = text.find(needle)
    if idx < 0:
        return None
    decoder = json.JSONDecoder()
    try:
        value, _ = decoder.raw_decode(text[idx + len(needle) :])
    except json.JSONDecodeError:
        return None
    if not isinstance(value, dict):
        return None
    return value


def _button_config_status(
    paths: Optional[IDEPaths],
    descriptor: Optional[LocalServerDescriptor],
) -> str:
    if paths is None or not paths.exists:
        return "not checked (supported-layout IDE install not detected)"
    config = _read_embedded_openpe_config(paths.bundle_file)
    if config is None:
        return "not embedded (bundle is unpatched or missing OPENPE-BOOTSTRAP)"
    if _is_strict_probe_config(config):
        return "probe-only runtime instrumentation; no openPE server credentials embedded"
    if config.get("probeOnly") is True:
        return "invalid mixed probe/regular bootstrap"
    base_url = str(config.get("baseUrl", "")).strip()
    token = str(config.get("token", "")).strip()
    mismatches = []
    if paths.profile is not None:
        expected_profile = {
            "hostProfileId": paths.profile.profile_id,
            "client": paths.profile.client,
            "mode": paths.profile.mode,
            "historySource": paths.profile.history_source,
        }
        for key, expected in expected_profile.items():
            if config.get(key) != expected:
                mismatches.append(f"{key} mismatch")
    if descriptor is None:
        if mismatches:
            return "invalid (" + ", ".join(mismatches) + ")"
        return "embedded, but current server descriptor is unavailable; cannot verify freshness"
    if base_url != descriptor.base_url:
        mismatches.append("baseUrl mismatch")
    if token != descriptor.token:
        mismatches.append("token mismatch")
    if mismatches:
        return (
            "stale ("
            + ", ".join(mismatches)
            + "); restart openpe-server with the same OPENPE_SERVER_TOKEN or re-run install"
        )
    return "fresh (embedded baseUrl/token match current server descriptor)"


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


def _resolve_or_explain(
    override: Optional[str],
    requested_host: str = "auto",
    require_mutation: bool = False,
) -> Optional[IDEPaths]:
    try:
        paths = resolve_paths(override=override)
    except (PathResolutionError, ProfileError) as exc:
        sys.stderr.write(f"openpe-ide-patch: cannot resolve IDE: {exc}\n")
        return None
    if paths is None:
        sys.stderr.write(
            "openpe-ide-patch: could not locate a supported-layout IDE install.\n"
            "  - pass --app-dir /path/to/IDE to override path detection\n"
            "  - or install the IDE at a platform-default location\n"
        )
        return None
    if not paths.exists:
        sys.stderr.write(
            f"openpe-ide-patch: IDE install at {paths.app_root} is incomplete.\n"
            f"  expected bundle:  {paths.bundle_file}\n"
            f"  expected product: {paths.product_file}\n"
        )
        return None
    if paths.product is None:
        sys.stderr.write("openpe-ide-patch: product identity is unavailable.\n")
        return None
    try:
        profile = require_profile(paths.product, requested_host)
    except ProfileError as exc:
        sys.stderr.write(f"openpe-ide-patch: {exc}\n")
        return None
    if require_mutation and not profile.allows_mutation(platform.system(), paths.product):
        sys.stderr.write(
            f"openpe-ide-patch: {profile.display_name} bundle mutation is not verified "
            f"on {platform.system()}; use the native hook path instead.\n"
        )
        return None
    return paths


def _refresh_mutation_preflight(
    selected: IDEPaths,
    requested_host: str,
    probe_only: bool = False,
    legacy_live_patch: bool = False,
) -> IDEPaths:
    current = resolve_paths(override=str(selected.app_root))
    if current is None or not current.exists or current.product is None:
        raise TransactionError("IDE layout changed during preflight")
    profile = require_profile(current.product, requested_host)
    if current.install_id != selected.install_id:
        raise TransactionError("IDE install identity changed during preflight")
    if current.profile != selected.profile or current.product != selected.product:
        raise TransactionError("IDE product identity changed during preflight")
    trusted_build = profile.supported_build(platform.system(), current.product)
    if probe_only or legacy_live_patch:
        if profile != DEVIN_PROFILE or trusted_build is None:
            raise TransactionError("IDE build is not in the trusted Devin allowlist")
    elif not profile.allows_mutation(platform.system(), current.product):
        raise TransactionError("IDE build is not in the trusted mutation allowlist")
    if not legacy_live_patch:
        require_host_stopped(profile)
    return current


def _read_descriptor_or_explain() -> Optional[LocalServerDescriptor]:
    try:
        descriptor = read_descriptor()
    except DescriptorError as exc:
        sys.stderr.write(
            f"openpe-ide-patch: cannot read openpe-server descriptor: {exc}\n"
            "  start openpe-server with OPENPE_SERVER_LIFECYCLE_ENABLED=true,\n"
            f"  or set OPENPE_SERVER_DESCRIPTOR_FILE to override the default path\n"
            f"  ({default_descriptor_path()}).\n"
        )
        return None
    return descriptor


def _print_status(
    paths: Optional[IDEPaths],
    descriptor_outcome: str,
    button_config_outcome: str,
    marker_present: bool,
    backup_path: Optional[Path],
    legacy_backup_path: Optional[Path],
    transaction_error: Optional[str],
) -> None:
    sys.stdout.write(f"openpe-ide-patch {__version__}\n")
    if paths is None:
        sys.stdout.write("  ide:             not detected\n")
    else:
        profile = paths.profile.profile_id if paths.profile is not None else "unsupported"
        version = paths.product.version if paths.product is not None else "unknown"
        commit = paths.product.commit if paths.product is not None else "unknown"
        sys.stdout.write(f"  ide root:        {paths.app_root}\n")
        sys.stdout.write(f"  profile:         {profile}\n")
        sys.stdout.write(f"  product build:   {version} ({commit})\n")
        runtime_verified = (
            paths.profile is not None
            and paths.product is not None
            and paths.profile.allows_mutation(platform.system(), paths.product)
        )
        sys.stdout.write(
            f"  runtime verified: {'yes' if runtime_verified else 'no'}\n"
        )
        sys.stdout.write(f"  bundle:          {paths.bundle_file}\n")
        sys.stdout.write(f"  product:         {paths.product_file}\n")
        sys.stdout.write(f"  injected:        {'yes' if marker_present else 'no'}\n")
        sys.stdout.write(
            f"  transaction:     {'yes (' + backup_path.parent.name + ')' if backup_path else 'no'}\n"
        )
        sys.stdout.write(
            f"  legacy backup:   {'ignored (' + legacy_backup_path.name + ')' if legacy_backup_path else 'none'}\n"
        )
        if transaction_error:
            sys.stdout.write(f"  transaction err: {transaction_error}\n")
    sys.stdout.write(f"  server descriptor: {descriptor_outcome}\n")
    sys.stdout.write(f"  button config:     {button_config_outcome}\n")


def _bound_transaction(paths: IDEPaths) -> BackupTransaction:
    config = _read_embedded_openpe_config(paths.bundle_file)
    if config is None:
        raise TransactionError("injected bundle has no readable openPE bootstrap")
    transaction_id = str(config.get("transactionId", "")).strip()
    product_commit = str(config.get("productCommit", "")).strip()
    profile_id = str(config.get("hostProfileId", "")).strip()
    if not transaction_id or not product_commit or not profile_id:
        raise TransactionError(
            "legacy injection has no transaction metadata; automatic mutation is refused"
        )
    transaction = load_transaction(paths, product_commit, transaction_id)
    if transaction.manifest.profile_id != profile_id:
        raise TransactionError("bundle profile does not match transaction profile")
    if _is_strict_probe_config(config) != (
        transaction.manifest.payload_kind == "probe"
    ):
        raise TransactionError("bundle payload kind does not match transaction provenance")
    validate_transaction_for_restore(transaction, paths)
    return transaction


def _trusted_build_id(build: SupportedBuild) -> str:
    value = "\n".join(
        (
            build.system,
            build.version,
            build.commit,
            build.bundle_sha256,
            build.product_sha256,
        )
    ).encode("utf-8")
    return hashlib.sha256(value).hexdigest()


def _authorize_recovery(transaction: BackupTransaction, paths: IDEPaths) -> None:
    if paths.profile is None or paths.product is None:
        raise TransactionError("cannot authorize recovery for unsupported product")
    manifest = transaction.manifest
    build = paths.profile.supported_build(platform.system(), paths.product)
    if manifest.payload_kind == "regular":
        if paths.profile.allows_mutation(platform.system(), paths.product):
            return
        if build is None:
            raise TransactionError("regular mutation recovery is disabled for this profile")
        trusted_regular = (
            manifest.trusted_build_id == _trusted_build_id(build)
            and manifest.trusted_bundle_original_sha256 == build.bundle_sha256
            and manifest.trusted_product_original_sha256 == build.product_sha256
            and manifest.bundle_original_sha256 == build.bundle_sha256
            and manifest.product_original_sha256 == build.product_sha256
        )
        if not trusted_regular:
            raise TransactionError("regular transaction lacks exact trusted provenance")
        return
    if paths.profile != DEVIN_PROFILE or build is None:
        raise TransactionError("probe recovery build is not trusted")
    expected = (
        (manifest.trusted_build_id, _trusted_build_id(build), "trusted build id"),
        (
            manifest.trusted_bundle_original_sha256,
            build.bundle_sha256,
            "trusted bundle original",
        ),
        (
            manifest.trusted_product_original_sha256,
            build.product_sha256,
            "trusted product original",
        ),
        (
            manifest.bundle_original_sha256,
            build.bundle_sha256,
            "transaction bundle original",
        ),
        (
            manifest.product_original_sha256,
            build.product_sha256,
            "transaction product original",
        ),
    )
    for current, trusted, label in expected:
        if current != trusted:
            raise TransactionError(f"probe recovery {label} mismatch")


def _recover_active_patch_operation(paths: IDEPaths) -> bool:
    operation = find_active_operation(paths)
    if operation is None:
        return False
    if paths.profile is None:
        raise OperationError("cannot recover operation for unsupported profile")
    _authorize_recovery(operation.transaction, paths)
    require_host_stopped(paths.profile)
    recovered = rollback_operation(operation, paths)
    sys.stderr.write(
        f"openpe-ide-patch: recovered interrupted {recovered.manifest.kind} "
        f"operation {recovered.manifest.operation_id}.\n"
    )
    return True


def _cmd_install(args: argparse.Namespace) -> int:
    paths = _resolve_or_explain(
        args.app_dir,
        requested_host=args.host,
        require_mutation=False,
    )
    if paths is None:
        return EXIT_PROFILE_ERROR
    assert paths.profile is not None
    assert paths.product is not None
    probe_endpoint: Optional[str] = None
    if args.probe_endpoint is not None:
        try:
            probe_endpoint = validate_probe_endpoint(args.probe_endpoint)
        except ProbeError as exc:
            sys.stderr.write(f"openpe-ide-patch: {exc}\n")
            return EXIT_USAGE
    probe_only = probe_endpoint is not None
    legacy_live_patch = False
    if probe_only and legacy_live_patch:
        sys.stderr.write(
            "openpe-ide-patch: --probe-endpoint and --legacy-live-patch are mutually exclusive\n"
        )
        return EXIT_USAGE
    try:
        active_operation = find_active_operation(paths)
        restoring = find_restoring_transaction(paths)
        if active_operation is not None and restoring is not None:
            raise TransactionError("patch and restore recovery are both pending")
        read_only_recovery = args.dry_run or not args.i_accept_experimental_risk
        if active_operation is not None and read_only_recovery:
            sys.stderr.write(
                f"openpe-ide-patch: interrupted {active_operation.manifest.kind} "
                f"operation {active_operation.manifest.operation_id} requires recovery; "
                "this command remained read-only.\n"
            )
            return EXIT_TRANSACTION_ERROR
        if restoring is not None and read_only_recovery:
            sys.stderr.write(
                f"openpe-ide-patch: interrupted restore transaction "
                f"{restoring.manifest.transaction_id} requires recovery; "
                "this command remained read-only.\n"
            )
            return EXIT_TRANSACTION_ERROR
        if active_operation is not None and _recover_active_patch_operation(paths):
            sys.stderr.write("  interrupted operation rolled back; rerun install.\n")
            return EXIT_OK
        if restoring is not None:
            _authorize_recovery(restoring, paths)
            require_host_stopped(paths.profile)
            recover_restoring_transaction(restoring, paths)
            sys.stderr.write("  interrupted restore completed; rerun install.\n")
            return EXIT_OK
    except ProcessError as exc:
        sys.stderr.write(f"openpe-ide-patch: {exc}\n")
        return EXIT_PROCESS_RUNNING
    except (OperationError, TransactionError) as exc:
        sys.stderr.write(f"openpe-ide-patch: operation recovery refused: {exc}\n")
        return EXIT_TRANSACTION_ERROR
    if not args.i_accept_experimental_risk:
        _print_disclaimer()
        return EXIT_DISCLAIMER_NOT_ACCEPTED
    trusted_build = paths.profile.supported_build(platform.system(), paths.product)
    if probe_only or legacy_live_patch:
        if paths.profile != DEVIN_PROFILE or trusted_build is None:
            sys.stderr.write(
                "openpe-ide-patch: experimental Devin install requires the exact trusted "
                "Windows Devin build.\n"
            )
            return EXIT_UNSUPPORTED_PROFILE
    elif not paths.profile.allows_mutation(platform.system(), paths.product):
        sys.stderr.write(
            f"openpe-ide-patch: {paths.profile.display_name} bundle mutation is not verified "
            f"on {platform.system()}; use the native hook path instead.\n"
        )
        return EXIT_UNSUPPORTED_PROFILE
    if not args.dry_run and not legacy_live_patch:
        try:
            require_host_stopped(paths.profile)
        except ProcessError as exc:
            sys.stderr.write(f"openpe-ide-patch: {exc}\n")
            return EXIT_PROCESS_RUNNING
    descriptor: Optional[LocalServerDescriptor] = None
    descriptor_path = default_descriptor_path()
    payload_path: Optional[Path] = None
    if probe_only:
        assert probe_endpoint is not None
        payload_text = build_probe_payload(probe_endpoint)
    else:
        descriptor = _read_descriptor_or_explain()
        if descriptor is None:
            return EXIT_DESCRIPTOR_ERROR
        try:
            info = verify_server(descriptor)
            validate_profile_cors(
                info,
                paths.profile.cors_origins,
                paths.profile.display_name,
            )
        except HandshakeError as exc:
            sys.stderr.write(f"openpe-ide-patch: cannot use openpe-server: {exc}\n")
            return EXIT_HANDSHAKE_ERROR
        payload_path = _load_inject_payload()
        if payload_path is None:
            candidate_lines = "\n".join(
                f"  - {candidate}" for candidate in _inject_payload_candidates()
            )
            sys.stderr.write(
                "openpe-ide-patch: inject payload missing.\n"
                "  searched:\n"
                f"{candidate_lines}\n"
                "  source checkout: run `npm install && npm run build` inside inject/.\n"
                "  packaged install: rebuild/reinstall the wheel with inject/dist/inject.js included.\n"
            )
            return EXIT_INJECT_PAYLOAD_MISSING
        try:
            payload_text = payload_path.read_text(encoding="utf-8")
        except OSError as exc:
            sys.stderr.write(f"openpe-ide-patch: cannot read inject payload: {exc}\n")
            return EXIT_INJECT_PAYLOAD_MISSING
    max_context_tokens = _resolve_max_context_tokens(args.max_context_tokens)
    if args.dry_run:
        if max_context_tokens is None:
            budget_label = "none (server default = no shrinking)"
        elif max_context_tokens == 0:
            budget_label = "0 (explicit disable; same wire effect as none)"
        else:
            source = (
                "CLI --max-context-tokens"
                if args.max_context_tokens is not None
                else "OPENPE_MAX_CONTEXT_TOKENS env"
            )
            budget_label = f"{max_context_tokens} (from {source})"
        sys.stdout.write(
            f"DRY RUN - would patch:\n"
            f"  bundle:  {paths.bundle_file}\n"
            f"  product: {paths.product_file}\n"
            f"  backup:  {paths.backup_dir}\n"
            f"  payload: {'probe-only structural collector' if probe_only else str(payload_path)} "
            f"({len(payload_text.encode('utf-8'))} bytes)\n"
            f"  fs probe: {'yes' if args.fs_probe else 'no'}\n"
            f"  debug:    {'yes' if args.debug else 'no'}\n"
            f"  max ctx tokens: {budget_label}\n"
            f"  codesign: {'yes (macOS)' if is_macos() else 'no (non-macOS)'}\n"
        )
        return EXIT_OK
    try:
        paths = _refresh_mutation_preflight(
            paths,
            args.host,
            probe_only=probe_only,
            legacy_live_patch=legacy_live_patch,
        )
    except (OSError, PathResolutionError, ProfileError, ProcessError, TransactionError) as exc:
        sys.stderr.write(f"openpe-ide-patch: final mutation preflight failed: {exc}\n")
        return EXIT_TRANSACTION_ERROR
    assert paths.profile is not None
    assert paths.product is not None
    try:
        already_patched = has_marker(paths.bundle_file)
    except BundleError as exc:
        sys.stderr.write(f"openpe-ide-patch: cannot inspect bundle marker: {exc}\n")
        return EXIT_BUNDLE_ERROR
    try:
        if already_patched:
            embedded = _read_embedded_openpe_config(paths.bundle_file)
            embedded_probe = _is_strict_probe_config(embedded)
            if probe_only != embedded_probe:
                raise TransactionError(
                    "probe-only and regular payloads cannot refresh each other; uninstall first"
                )
            transaction = _bound_transaction(paths)
            if probe_only:
                _authorize_recovery(transaction, paths)
        else:
            trusted_build = paths.profile.supported_build(platform.system(), paths.product)
            if trusted_build is None:
                raise TransactionError("IDE build is not in the trusted mutation allowlist")
            verify_trusted_baseline(
                paths.product_file,
                paths.bundle_file,
                trusted_build.product_sha256,
                trusted_build.bundle_sha256,
            )
            transaction = create_transaction(
                paths,
                __version__,
                payload_kind="probe" if probe_only else "regular",
                trusted_build_id=_trusted_build_id(trusted_build),
                trusted_bundle_original_sha256=trusted_build.bundle_sha256,
                trusted_product_original_sha256=trusted_build.product_sha256,
            )
            if transaction.manifest.bundle_original_sha256 != trusted_build.bundle_sha256:
                raise TransactionError("transaction bundle snapshot is not trusted baseline")
            if transaction.manifest.product_original_sha256 != trusted_build.product_sha256:
                raise TransactionError("transaction product snapshot is not trusted baseline")
    except (ChecksumError, TransactionError, OSError) as exc:
        sys.stderr.write(f"openpe-ide-patch: safety preflight failed: {exc}\n")
        return EXIT_TRANSACTION_ERROR
    target_dir: Optional[Path] = None
    operation: Optional[PatchOperation] = None
    try:
        expected_bundle = (
            transaction.manifest.bundle_patched_sha256
            if already_patched
            else transaction.manifest.bundle_original_sha256
        )
        expected_product = (
            transaction.manifest.product_patched_sha256
            if already_patched
            else transaction.manifest.product_original_sha256
        )
        if probe_only:
            prelude = _build_probe_prelude(
                profile_id=paths.profile.profile_id,
                product_commit=paths.product.commit,
                transaction_id=transaction.manifest.transaction_id,
                client=paths.profile.client,
                mode=paths.profile.mode,
                history_source=paths.profile.history_source,
            )
        else:
            assert descriptor is not None
            prelude = _build_payload_prelude(
                descriptor,
                descriptor_path=descriptor_path if args.fs_probe else None,
                fs_probe=args.fs_probe,
                debug=args.debug,
                max_context_tokens=max_context_tokens,
                profile_id=paths.profile.profile_id,
                product_commit=paths.product.commit,
                transaction_id=transaction.manifest.transaction_id,
                client=paths.profile.client,
                mode=paths.profile.mode,
                history_source=paths.profile.history_source,
                legacy_live_patch=legacy_live_patch,
            )
        target_dir = Path(tempfile.mkdtemp(prefix="openpe-ide-patch-target-"))
        target_bundle_path = target_dir / paths.bundle_file.name
        target_product_path = target_dir / paths.product_file.name
        target_bundle_path.write_bytes(paths.bundle_file.read_bytes())
        target_product_path.write_bytes(paths.product_file.read_bytes())
        inject_bundle(
            target_bundle_path,
            prelude + payload_text,
            expected_sha256=expected_bundle,
        )
        new_sum = vscode_checksum(target_bundle_path)
        patch_product_json(
            target_product_path,
            bundle_relpath=DEFAULT_BUNDLE_RELPATH,
            new_value=new_sum,
            expected_sha256=expected_product,
        )
        verify_bundle_checksum(target_product_path, target_bundle_path)
        target_bundle = target_bundle_path.read_bytes()
        target_product = target_product_path.read_bytes()
        operation = prepare_operation(
            transaction,
            paths,
            "refresh" if already_patched else "install",
            target_bundle,
            target_product,
        )
        if not legacy_live_patch:
            require_host_stopped(paths.profile)
        apply_operation(operation, paths, target_bundle, target_product)
        verify_bundle_checksum(paths.product_file, paths.bundle_file)
        if is_macos():
            codesign_app(paths.app_root)
        transaction = finalize_operation_transaction(operation, paths)
        operation = complete_operation(operation, transaction, paths)
    except (
        BundleError,
        ChecksumError,
        CodesignError,
        OSError,
        OperationError,
        ProcessError,
        TransactionError,
    ) as exc:
        sys.stderr.write(f"openpe-ide-patch: install failed mid-patch: {exc}\n")
        if operation is not None and operation.manifest.state == "active":
            try:
                operation = rollback_operation(operation, paths)
                sys.stderr.write(
                    f"  conditionally rolled back operation {operation.manifest.operation_id}\n"
                )
            except OperationError as rollback_exc:
                sys.stderr.write(
                    f"  conditional rollback refused: {rollback_exc}\n"
                    f"  active recovery journal retained at {operation.root}\n"
                )
                if target_dir is not None:
                    shutil.rmtree(target_dir, ignore_errors=True)
                return EXIT_TRANSACTION_ERROR
        if target_dir is not None:
            shutil.rmtree(target_dir, ignore_errors=True)
        return EXIT_BUNDLE_ERROR
    if target_dir is not None:
        shutil.rmtree(target_dir, ignore_errors=True)
    sys.stdout.write(
        f"openpe-ide-patch: {'probe-only' if probe_only else 'legacy-live' if legacy_live_patch else 'install'} complete.\n"
        f"  profile: {paths.profile.profile_id}\n"
        f"  transaction: {transaction.manifest.transaction_id}\n"
        f"  operation: {operation.manifest.operation_id if operation else 'unknown'}\n"
        f"  {'reload the current window' if legacy_live_patch else 'restart ' + paths.profile.display_name} to "
        f"{'collect the structural probe' if probe_only else 'load the openPE button'}.\n"
    )
    return EXIT_OK


def _cmd_uninstall(args: argparse.Namespace) -> int:
    paths = _resolve_or_explain(args.app_dir, requested_host=args.host)
    if paths is None:
        return EXIT_PROFILE_ERROR
    assert paths.profile is not None
    assert paths.product is not None
    try:
        if _recover_active_patch_operation(paths):
            sys.stdout.write("openpe-ide-patch: interrupted patch operation rolled back.\n")
            return EXIT_OK
    except ProcessError as exc:
        sys.stderr.write(f"openpe-ide-patch: {exc}\n")
        return EXIT_PROCESS_RUNNING
    except (OperationError, TransactionError) as exc:
        sys.stderr.write(f"openpe-ide-patch: operation recovery refused: {exc}\n")
        return EXIT_TRANSACTION_ERROR
    try:
        restoring = find_restoring_transaction(paths)
    except TransactionError as exc:
        sys.stderr.write(f"openpe-ide-patch: recovery discovery failed: {exc}\n")
        return EXIT_TRANSACTION_ERROR
    if restoring is not None:
        try:
            _authorize_recovery(restoring, paths)
            require_host_stopped(paths.profile)
            recover_restoring_transaction(restoring, paths)
        except ProcessError as exc:
            sys.stderr.write(f"openpe-ide-patch: {exc}\n")
            return EXIT_PROCESS_RUNNING
        except TransactionError as exc:
            sys.stderr.write(f"openpe-ide-patch: recovery refused: {exc}\n")
            return EXIT_TRANSACTION_ERROR
        sys.stdout.write("openpe-ide-patch: interrupted restore recovery complete.\n")
        return EXIT_OK
    try:
        marker_present = has_marker(paths.bundle_file)
    except BundleError as exc:
        sys.stderr.write(f"openpe-ide-patch: cannot inspect bundle marker: {exc}\n")
        return EXIT_BUNDLE_ERROR
    if not marker_present:
        sys.stdout.write(
            "openpe-ide-patch: bundle is not injected; legacy backups were not restored.\n"
        )
        return EXIT_OK
    try:
        config = _read_embedded_openpe_config(paths.bundle_file)
        transaction = _bound_transaction(paths)
    except TransactionError as exc:
        sys.stderr.write(
            f"openpe-ide-patch: restore refused: {exc}; use vendor clean reinstall.\n"
        )
        return EXIT_TRANSACTION_ERROR
    probe_only = _is_strict_probe_config(config) and paths.profile == DEVIN_PROFILE
    legacy_live_patch = _is_legacy_live_config(config) and paths.profile == DEVIN_PROFILE
    if probe_only or legacy_live_patch:
        try:
            _authorize_recovery(transaction, paths)
        except TransactionError as exc:
            sys.stderr.write(f"openpe-ide-patch: experimental restore refused: {exc}\n")
            return EXIT_TRANSACTION_ERROR
    if (
        not probe_only
        and not legacy_live_patch
        and not paths.profile.allows_mutation(platform.system(), paths.product)
    ):
        sys.stderr.write(
            f"openpe-ide-patch: uninstall mutation for {paths.profile.display_name} "
            f"is not verified on {platform.system()}; use vendor clean reinstall.\n"
        )
        return EXIT_UNSUPPORTED_PROFILE
    try:
        require_host_stopped(paths.profile)
        restore_transaction(transaction, paths)
    except ProcessError as exc:
        sys.stderr.write(f"openpe-ide-patch: {exc}\n")
        return EXIT_PROCESS_RUNNING
    except TransactionError as exc:
        sys.stderr.write(
            f"openpe-ide-patch: restore refused: {exc}; use vendor clean reinstall.\n"
        )
        return EXIT_TRANSACTION_ERROR
    sys.stdout.write("openpe-ide-patch: uninstall complete.\n")
    return EXIT_OK


def _cmd_status(args: argparse.Namespace) -> int:
    try:
        paths = resolve_paths(override=args.app_dir)
    except (PathResolutionError, ProfileError) as exc:
        sys.stdout.write(f"openpe-ide-patch: IDE detection failed ({exc})\n")
        paths = None
    profile_error: Optional[str] = None
    if paths is not None and paths.product is not None and args.host != "auto":
        try:
            require_profile(paths.product, args.host)
        except ProfileError as exc:
            profile_error = str(exc)
    marker_present = False
    backup_path: Optional[Path] = None
    legacy_backup_path: Optional[Path] = None
    transaction_error: Optional[str] = None
    if paths is not None and paths.exists:
        legacy_backup_path = _find_latest_backup(
            paths.legacy_backup_dir,
            paths.bundle_file.name,
        )
        try:
            active_operation = find_active_operation(paths)
            restoring = find_restoring_transaction(paths)
            if active_operation is not None:
                backup_path = active_operation.bundle_backup
                transaction_error = (
                    f"interrupted {active_operation.manifest.kind} rollback is pending"
                )
            elif restoring is not None:
                backup_path = restoring.bundle_backup
                transaction_error = "interrupted restore recovery is pending"
            marker_present = has_marker(paths.bundle_file)
            if (
                marker_present
                and paths.product is not None
                and active_operation is None
                and restoring is None
            ):
                transaction = _bound_transaction(paths)
                backup_path = transaction.bundle_backup
        except (BundleError, OperationError, TransactionError) as exc:
            backup_path = None
            transaction_error = str(exc)
    descriptor_outcome = "not checked"
    descriptor: Optional[LocalServerDescriptor] = None
    try:
        descriptor = read_descriptor()
        descriptor_outcome = (
            f"present (base_url={descriptor.base_url}, pid={descriptor.pid}, version={descriptor.version or 'unknown'})"
        )
    except DescriptorError as exc:
        descriptor_outcome = f"unavailable ({exc})"
    _print_status(
        paths,
        descriptor_outcome,
        _button_config_status(paths if paths is not None and paths.exists else None, descriptor),
        marker_present,
        backup_path,
        legacy_backup_path,
        transaction_error,
    )
    if profile_error:
        sys.stdout.write(f"  requested host:    mismatch ({profile_error})\n")
    return EXIT_OK


def _cmd_doctor(args: argparse.Namespace) -> int:
    sys.stdout.write(f"openpe-ide-patch {__version__} doctor\n")
    sys.stdout.write(f"  python:           {sys.version.split()[0]} ({sys.executable})\n")
    sys.stdout.write(f"  platform:         {sys.platform}\n")
    sys.stdout.write(f"  codesign needed:  {'yes (macOS)' if is_macos() else 'no'}\n")
    try:
        paths = resolve_paths(override=args.app_dir)
    except (PathResolutionError, ProfileError) as exc:
        paths = None
        sys.stdout.write(f"  ide detection:    FAIL ({exc})\n")
    if paths is None:
        sys.stdout.write("  ide:              not detected at default paths\n")
    else:
        profile = paths.profile
        host_mismatch: Optional[str] = None
        if paths.product is not None and args.host != "auto":
            try:
                require_profile(paths.product, args.host)
            except ProfileError as exc:
                host_mismatch = str(exc)
        sys.stdout.write(f"  ide app root:     {paths.app_root}\n")
        sys.stdout.write(
            f"  ide bundle:       {paths.bundle_file} (exists={paths.bundle_file.is_file()})\n"
        )
        sys.stdout.write(
            f"  ide product:      {paths.product_file} (exists={paths.product_file.is_file()})\n"
        )
        sys.stdout.write(
            f"  profile:          {profile.profile_id if profile is not None else 'unsupported'}\n"
        )
        if paths.product is not None:
            sys.stdout.write(
                f"  product build:    {paths.product.version or 'unknown'} "
                f"({paths.product.commit or 'unknown'})\n"
            )
        if profile is not None:
            process = inspect_host_processes(profile)
            process_detail = ", ".join(process.matches) if process.matches else process.error
            sys.stdout.write(
                f"  process state:    {process.state}"
                f"{(' (' + process_detail + ')') if process_detail else ''}\n"
            )
            sys.stdout.write(
                f"  runtime verified: {'yes' if profile.allows_mutation(platform.system(), paths.product) else 'no'}\n"
            )
        sys.stdout.write(f"  backup dir:       {paths.backup_dir}\n")
        sys.stdout.write(f"  legacy backup:    {paths.legacy_backup_dir}\n")
        try:
            active_operation = find_active_operation(paths)
            if active_operation is None:
                sys.stdout.write("  active operation: none\n")
            else:
                sys.stdout.write(
                    f"  active operation: {active_operation.manifest.kind} "
                    f"({active_operation.manifest.operation_id})\n"
                )
        except OperationError as exc:
            sys.stdout.write(f"  active operation: FAIL ({exc})\n")
        try:
            restoring = find_restoring_transaction(paths)
            if restoring is None:
                sys.stdout.write("  restoring txn:   none\n")
            else:
                sys.stdout.write(
                    f"  restoring txn:   {restoring.manifest.transaction_id}\n"
                )
        except TransactionError as exc:
            sys.stdout.write(f"  restoring txn:   FAIL ({exc})\n")
        if host_mismatch:
            sys.stdout.write(f"  requested host:   mismatch ({host_mismatch})\n")
    descriptor_path = default_descriptor_path()
    sys.stdout.write(f"  descriptor path:  {descriptor_path}\n")
    descriptor: Optional[LocalServerDescriptor] = None
    try:
        descriptor = read_descriptor()
        sys.stdout.write(f"  descriptor:       OK (pid={descriptor.pid}, version={descriptor.version or 'unknown'})\n")
        try:
            info = verify_server(descriptor)
            sys.stdout.write(
                f"  /v1/info:         200 (server version={info.get('version', 'unknown')})\n"
            )
            if paths is not None and paths.profile is not None:
                validate_profile_cors(
                    info,
                    paths.profile.cors_origins,
                    paths.profile.display_name,
                )
                sys.stdout.write("  CORS profile:     OK\n")
        except HandshakeError as exc:
            sys.stdout.write(f"  server profile:   FAIL ({exc})\n")
    except DescriptorError as exc:
        sys.stdout.write(f"  descriptor:       FAIL ({exc})\n")
    sys.stdout.write(f"  button config:    {_button_config_status(paths if paths is not None and paths.exists else None, descriptor)}\n")
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


def main(
    argv: Optional[Sequence[str]] = None,
    forced_host: Optional[str] = None,
    prog: str = "openpe-ide-patch",
) -> int:
    parser = _build_parser(prog=prog)
    values = list(argv if argv is not None else sys.argv[1:])
    if forced_host is not None:
        if any(value == "--host" or value.startswith("--host=") for value in values):
            sys.stderr.write(f"{prog}: --host is not accepted by this compatibility entry\n")
            return EXIT_USAGE
        if values:
            values[1:1] = ["--host", forced_host]
    args = parser.parse_args(values)
    if not args.command:
        parser.print_help(sys.stderr)
        return EXIT_USAGE
    handler = _DISPATCH.get(args.command)
    if handler is None:
        parser.print_help(sys.stderr)
        return EXIT_USAGE
    needs_lock = args.command == "uninstall" or (
        args.command == "install"
        and args.i_accept_experimental_risk
        and not args.dry_run
    )
    if not needs_lock:
        return handler(args)
    try:
        lock_paths = resolve_paths(override=args.app_dir)
    except (PathResolutionError, ProfileError) as exc:
        sys.stderr.write(f"openpe-ide-patch: cannot resolve mutation lock target: {exc}\n")
        return EXIT_PROFILE_ERROR
    if lock_paths is None:
        sys.stderr.write("openpe-ide-patch: cannot resolve mutation lock target.\n")
        return EXIT_PROFILE_ERROR
    args.app_dir = str(lock_paths.app_root)
    try:
        with mutation_lock(lock_paths):
            return handler(args)
    except LockError as exc:
        sys.stderr.write(f"openpe-ide-patch: {exc}\n")
        return EXIT_TRANSACTION_ERROR


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
