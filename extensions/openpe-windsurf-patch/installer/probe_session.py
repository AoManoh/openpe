from __future__ import annotations

import argparse
import os
import subprocess
import sys
import time
from pathlib import Path
from typing import Optional, Sequence

from .__main__ import EXIT_OK, main as installer_main
from .paths import PathResolutionError, resolve_paths
from .processes import inspect_host_processes
from .profiles import DEVIN_PROFILE, ProfileError
from .runtime_probe import ProbeError, ProbeReceiver

# How long the session waits for the host to exit before giving up on the
# automatic probe-patch restore. The probe payload phones home at startup, so
# the session's purpose is fulfilled shortly after launch; the window only
# needs to cover a prompt manual close, not a full working session.
DEFAULT_CLEANUP_WAIT_SECONDS = 120.0


class ProbeSessionError(Exception):
    pass


def run_session(
    app_dir: Path,
    output: Path,
    launch: Path,
    host_resolver_rules: str,
    timeout: float,
    cleanup_wait: float = DEFAULT_CLEANUP_WAIT_SECONDS,
) -> None:
    try:
        paths = resolve_paths(override=str(app_dir))
    except (PathResolutionError, ProfileError) as exc:
        raise ProbeSessionError(f"cannot resolve Devin: {exc}") from exc
    if paths is None or paths.profile != DEVIN_PROFILE:
        raise ProbeSessionError("target is not a detected Devin Desktop install")
    if not launch.is_file():
        raise ProbeSessionError(f"launch executable not found: {launch}")
    receiver = ProbeReceiver(output, timeout=timeout)
    sys.stdout.write(f"endpoint={receiver.endpoint}\n")
    sys.stdout.flush()
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        process = inspect_host_processes(DEVIN_PROFILE)
        if process.state == "unknown":
            receiver.server.server_close()
            raise ProbeSessionError(f"cannot inspect Devin process: {process.error}")
        if process.state == "stopped":
            time.sleep(1.0)
            if inspect_host_processes(DEVIN_PROFILE).state == "stopped":
                break
        time.sleep(0.5)
    else:
        receiver.server.server_close()
        raise ProbeSessionError("timed out waiting for Devin to stop")
    sys.stdout.write("phase=installing\n")
    sys.stdout.flush()
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
        receiver.server.server_close()
        raise ProbeSessionError(f"probe-only install failed with exit code {code}")
    # From here on a persistent probe patch exists on disk; every exit path
    # must either remove it or say — unmissably — how to (CR-014: launch
    # failures, receive timeouts and even successful sessions used to leave a
    # patch pointing at an already-closed receiver endpoint).
    sys.stdout.write("phase=launching\n")
    sys.stdout.flush()
    command = [str(launch)]
    if host_resolver_rules:
        command.append(f"--host-resolver-rules={host_resolver_rules}")
    try:
        subprocess.Popen(command, close_fds=True)
    except OSError as exc:
        receiver.server.server_close()
        # The host never started (we verified it stopped above), so the
        # patch can be removed right away.
        _restore_probe_patch(paths)
        raise ProbeSessionError(f"cannot launch Devin: {exc}") from exc
    sys.stdout.write("phase=receiving\n")
    sys.stdout.flush()
    try:
        receiver.serve_once()
    except ProbeError:
        _cleanup_after_launch(paths, cleanup_wait)
        raise
    sys.stdout.write(f"phase=complete output={receiver.output}\n")
    sys.stdout.flush()
    _cleanup_after_launch(paths, cleanup_wait)


def _cleanup_after_launch(paths, cleanup_wait: float) -> None:
    """Remove the probe patch once the host verifiably stopped.

    The restore path (backup transactions) refuses to touch a live install,
    so this waits — bounded by ``cleanup_wait`` — for the launched Devin to
    exit. If it keeps running, the manual removal command is printed instead
    of silently leaving the patch behind.
    """
    deadline = time.monotonic() + max(cleanup_wait, 0.0)
    while time.monotonic() < deadline:
        if inspect_host_processes(DEVIN_PROFILE).state == "stopped":
            time.sleep(1.0)
            if inspect_host_processes(DEVIN_PROFILE).state == "stopped":
                _restore_probe_patch(paths)
                return
        time.sleep(0.5)
    _print_manual_cleanup(paths)


def _restore_probe_patch(paths) -> None:
    """Run the probe-aware uninstall; fall back to manual instructions."""
    sys.stdout.write("phase=restoring\n")
    sys.stdout.flush()
    code = installer_main(
        ["uninstall", "--app-dir", str(paths.app_root), "--host", "devin"]
    )
    if code == EXIT_OK:
        sys.stdout.write("phase=restored\n")
        sys.stdout.flush()
        return
    _print_manual_cleanup(paths, uninstall_code=code)


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
        run_session(
            Path(args.app_dir).expanduser().resolve(),
            Path(args.output).expanduser().resolve(),
            Path(args.launch).expanduser().resolve(),
            args.host_resolver_rules,
            args.timeout,
            args.cleanup_wait,
        )
    except (ProbeError, ProbeSessionError) as exc:
        sys.stderr.write(f"openpe-ide-probe-session: {exc}\n")
        return 1
    except OSError as exc:
        # Filesystem/process errors outside the launch step (which is wrapped
        # above) must not escape as tracebacks: the operator still needs the
        # session verdict on stderr.
        sys.stderr.write(f"openpe-ide-probe-session: unexpected OS error: {exc}\n")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
