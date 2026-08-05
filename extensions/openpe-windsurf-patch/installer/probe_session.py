from __future__ import annotations

import argparse
import os
import subprocess
import sys
import time
from pathlib import Path
from typing import Optional, Sequence

from .__main__ import EXIT_OK, main as installer_main
from .bundle import BundleError, has_marker
from .paths import PathResolutionError, resolve_paths
from .processes import inspect_host_processes
from .profiles import DEVIN_PROFILE, ProfileError
from .runtime_probe import ProbeError, ProbeReceiver

# How long the session waits for the host to exit before giving up on the
# automatic probe-patch restore. The probe payload phones home at startup, so
# the session's purpose is fulfilled shortly after launch; the window only
# needs to cover a prompt manual close, not a full working session.
DEFAULT_CLEANUP_WAIT_SECONDS = 120.0

# Exit code for "the probe itself succeeded but the probe patch is still
# installed" (host would not exit in time, or the automatic uninstall
# failed). Distinct from 0 so automation can never mistake a residual patch
# for a fully cleaned-up run, and from 1 so it is distinguishable from probe
# failures.
PROBE_EXIT_RESIDUAL = 3


class ProbeSessionError(Exception):
    pass


class ProbeResidualError(ProbeSessionError):
    pass


def run_session(
    app_dir: Path,
    output: Path,
    launch: Path,
    host_resolver_rules: str,
    timeout: float,
    cleanup_wait: float = DEFAULT_CLEANUP_WAIT_SECONDS,
) -> bool:
    """Run one probe session. Returns True when the probe patch was removed
    again (fully clean exit) and False when it is still installed — the
    caller maps that to a dedicated non-zero exit code so automation can
    never mistake a residual patch for a clean run."""
    try:
        paths = resolve_paths(override=str(app_dir))
    except (PathResolutionError, ProfileError) as exc:
        raise ProbeSessionError(f"cannot resolve Devin: {exc}") from exc
    if paths is None or paths.profile != DEVIN_PROFILE:
        raise ProbeSessionError("target is not a detected Devin Desktop install")
    if not launch.is_file():
        raise ProbeSessionError(f"launch executable not found: {launch}")
    try:
        if has_marker(paths.bundle_file):
            raise ProbeSessionError("target already has an openPE patch; restore it before probing")
    except BundleError as exc:
        raise ProbeSessionError(f"cannot inspect existing patch: {exc}") from exc
    receiver = ProbeReceiver(output, timeout=timeout)
    sys.stdout.write(f"endpoint={receiver.endpoint}\n")
    sys.stdout.flush()
    install_attempted = False
    try:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            process = inspect_host_processes(DEVIN_PROFILE)
            if process.state == "unknown":
                raise ProbeSessionError(f"cannot inspect Devin process: {process.error}")
            if process.state == "stopped":
                time.sleep(1.0)
                if inspect_host_processes(DEVIN_PROFILE).state == "stopped":
                    break
            time.sleep(0.5)
        else:
            raise ProbeSessionError("timed out waiting for Devin to stop")
        sys.stdout.write("phase=installing\n")
        sys.stdout.flush()
        install_attempted = True
        code = installer_main(
            [
                "install",
                "--app-dir",
                str(paths.app_root),
                "--host",
                "devin",
                "--probe-endpoint",
                receiver.endpoint,
                "--i-accept-experimental-risk",
            ]
        )
        if code != EXIT_OK:
            raise ProbeSessionError(f"probe-only install failed with exit code {code}")
        sys.stdout.write("phase=launching\n")
        sys.stdout.flush()
        command = [str(launch)]
        if host_resolver_rules:
            command.append(f"--host-resolver-rules={host_resolver_rules}")
        try:
            subprocess.Popen(command, close_fds=True)
        except OSError as exc:
            raise ProbeSessionError(f"cannot launch Devin: {exc}") from exc
        sys.stdout.write("phase=receiving\n")
        sys.stdout.flush()
        receiver.serve_once()
        sys.stdout.write(f"phase=complete output={receiver.output}\n")
        sys.stdout.flush()
        restored = _cleanup_after_launch(paths, cleanup_wait)
        return restored
    except BaseException as exc:
        if install_attempted:
            restored = _cleanup_after_launch(paths, cleanup_wait)
            if not restored:
                raise ProbeResidualError(
                    f"probe failed and the patch is still installed: {exc}"
                ) from exc
        raise
    finally:
        receiver.server.server_close()


def _cleanup_after_launch(paths, cleanup_wait: float) -> bool:
    """Remove the probe patch once the host verifiably stopped.

    The restore path (backup transactions) refuses to touch a live install,
    so this waits — bounded by ``cleanup_wait`` — for the launched Devin to
    exit. Returns True when the patch was removed; on a still-running host
    or a failed uninstall it prints the manual removal command and returns
    False so the caller can exit non-zero.
    """
    deadline = time.monotonic() + max(cleanup_wait, 0.0)
    while time.monotonic() < deadline:
        if inspect_host_processes(DEVIN_PROFILE).state == "stopped":
            time.sleep(1.0)
            if inspect_host_processes(DEVIN_PROFILE).state == "stopped":
                return _restore_probe_patch(paths)
        time.sleep(0.5)
    _print_manual_cleanup(paths)
    return False


def _restore_probe_patch(paths) -> bool:
    """Run the probe-aware uninstall; fall back to manual instructions.

    Returns True only when the uninstall reported success."""
    sys.stdout.write("phase=restoring\n")
    sys.stdout.flush()
    code = installer_main(
        ["uninstall", "--app-dir", str(paths.app_root), "--host", "devin"]
    )
    if code == EXIT_OK:
        sys.stdout.write("phase=restored\n")
        sys.stdout.flush()
        return True
    _print_manual_cleanup(paths, uninstall_code=code)
    return False


def _print_manual_cleanup(paths, uninstall_code: Optional[int] = None) -> None:
    detail = ""
    if uninstall_code is not None:
        detail = f" (automatic uninstall exited with {uninstall_code})"
    sys.stderr.write(
        "openpe-ide-probe-session: the probe patch is STILL INSTALLED"
        f"{detail}. Close Devin, then remove it with:\n"
        f'  python -m installer uninstall --app-dir "{paths.app_root}" --host devin\n'
    )
    sys.stderr.flush()


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = argparse.ArgumentParser(prog="python -m installer.probe_session")
    parser.add_argument("--app-dir", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--launch", required=True)
    parser.add_argument(
        "--host-resolver-rules",
        default=os.environ.get("OPENPE_PROBE_HOST_RESOLVER_RULES", ""),
    )
    parser.add_argument("--timeout", type=float, default=600.0)
    parser.add_argument(
        "--cleanup-wait",
        type=float,
        default=DEFAULT_CLEANUP_WAIT_SECONDS,
        help="seconds to wait for Devin to exit before printing manual probe-patch removal instructions",
    )
    args = parser.parse_args(list(argv) if argv is not None else None)
    try:
        restored = run_session(
            Path(args.app_dir).expanduser().resolve(),
            Path(args.output).expanduser().resolve(),
            Path(args.launch).expanduser().resolve(),
            args.host_resolver_rules,
            args.timeout,
            args.cleanup_wait,
        )
    except ProbeResidualError as exc:
        sys.stderr.write(f"openpe-ide-probe-session: {exc}\n")
        return PROBE_EXIT_RESIDUAL
    except (ProbeError, ProbeSessionError) as exc:
        sys.stderr.write(f"openpe-ide-probe-session: {exc}\n")
        return 1
    except OSError as exc:
        # Filesystem/process errors outside the launch step (which is wrapped
        # above) must not escape as tracebacks: the operator still needs the
        # session verdict on stderr.
        sys.stderr.write(f"openpe-ide-probe-session: unexpected OS error: {exc}\n")
        return 1
    except Exception as exc:
        sys.stderr.write(f"openpe-ide-probe-session: unexpected error: {exc}\n")
        return 1
    if not restored:
        # The probe data was captured, but the patch is still on disk: never
        # report a fully clean run (exit 0) for a residual mutation.
        return PROBE_EXIT_RESIDUAL
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
