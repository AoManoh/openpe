from __future__ import annotations

import shutil
import subprocess
import sys
import zipfile
from datetime import datetime, timezone
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
INJECT = ROOT / "inject"
DIST = ROOT / "dist"
REQUIRED_WHEEL_ENTRIES = (
    "installer/multi_bundle_patch.py",
    "share/openpe-windsurf-patch/inject/dist/inject.js",
    "entry_points.txt",
)


def run(*args: str, cwd: Path) -> None:
    executable = shutil.which(args[0])
    if executable is None:
        raise RuntimeError(f"required command not found: {args[0]}")
    subprocess.run((executable,) + args[1:], cwd=str(cwd), check=True)


def verify_wheel(directory: Path) -> Path:
    wheels = sorted(directory.glob("openpe_windsurf_patch-*.whl"))
    if len(wheels) != 1:
        raise RuntimeError(f"expected one wheel, found {len(wheels)}")
    wheel = wheels[0]
    with zipfile.ZipFile(wheel) as archive:
        names = tuple(archive.namelist())
        for suffix in REQUIRED_WHEEL_ENTRIES:
            if not any(name.endswith(suffix) for name in names):
                raise RuntimeError(f"wheel is missing {suffix}")
        entry_name = next(name for name in names if name.endswith("entry_points.txt"))
        entries = archive.read(entry_name).decode("utf-8")
        for command in ("openpe-ide-patch", "openpe-windsurf-patch"):
            if command not in entries:
                raise RuntimeError(f"wheel is missing console entry {command}")
    return wheel


def main() -> int:
    output = DIST / datetime.now(tz=timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    output.mkdir(parents=True)
    run("npm", "ci", cwd=INJECT)
    run("npm", "run", "check", cwd=INJECT)
    run("npm", "run", "build", cwd=INJECT)
    run(
        sys.executable,
        "-m",
        "pip",
        "wheel",
        ".",
        "--no-deps",
        "--wheel-dir",
        str(output),
        cwd=ROOT,
    )
    wheel = verify_wheel(output)
    print(wheel)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
