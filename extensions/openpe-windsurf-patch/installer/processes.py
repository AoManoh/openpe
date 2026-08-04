from __future__ import annotations

import ctypes
import platform
import subprocess
from dataclasses import dataclass
from typing import Optional, Tuple

from .profiles import HostProfile


class ProcessError(Exception):
    pass


class _ProcessEntry32W(ctypes.Structure):
    _fields_ = (
        ("dwSize", ctypes.c_uint32),
        ("cntUsage", ctypes.c_uint32),
        ("th32ProcessID", ctypes.c_uint32),
        ("th32DefaultHeapID", ctypes.c_size_t),
        ("th32ModuleID", ctypes.c_uint32),
        ("cntThreads", ctypes.c_uint32),
        ("th32ParentProcessID", ctypes.c_uint32),
        ("pcPriClassBase", ctypes.c_long),
        ("dwFlags", ctypes.c_uint32),
        ("szExeFile", ctypes.c_wchar * 260),
    )


def _windows_process_names() -> Tuple[str, ...]:
    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    create_snapshot = kernel32.CreateToolhelp32Snapshot
    create_snapshot.argtypes = (ctypes.c_uint32, ctypes.c_uint32)
    create_snapshot.restype = ctypes.c_void_p
    process_first = kernel32.Process32FirstW
    process_first.argtypes = (ctypes.c_void_p, ctypes.POINTER(_ProcessEntry32W))
    process_first.restype = ctypes.c_int
    process_next = kernel32.Process32NextW
    process_next.argtypes = (ctypes.c_void_p, ctypes.POINTER(_ProcessEntry32W))
    process_next.restype = ctypes.c_int
    close_handle = kernel32.CloseHandle
    close_handle.argtypes = (ctypes.c_void_p,)
    close_handle.restype = ctypes.c_int
    snapshot = create_snapshot(0x00000002, 0)
    if snapshot == ctypes.c_void_p(-1).value:
        raise OSError(ctypes.get_last_error(), "CreateToolhelp32Snapshot failed")
    names = []
    entry = _ProcessEntry32W()
    entry.dwSize = ctypes.sizeof(_ProcessEntry32W)
    try:
        if not process_first(snapshot, ctypes.byref(entry)):
            error = ctypes.get_last_error()
            if error == 18:
                return ()
            raise OSError(error, "Process32FirstW failed")
        while True:
            names.append(entry.szExeFile)
            entry.dwSize = ctypes.sizeof(_ProcessEntry32W)
            if not process_next(snapshot, ctypes.byref(entry)):
                error = ctypes.get_last_error()
                if error == 18:
                    break
                raise OSError(error, "Process32NextW failed")
    finally:
        close_handle(snapshot)
    return tuple(names)


@dataclass(frozen=True)
class ProcessCheck:
    state: str
    matches: Tuple[str, ...]
    error: str = ""


def inspect_host_processes(
    profile: HostProfile,
    system: Optional[str] = None,
) -> ProcessCheck:
    system = system or platform.system()
    try:
        if system == "Windows":
            names = _windows_process_names()
        else:
            result = subprocess.run(
                ["ps", "-A", "-o", "comm="],
                check=True,
                capture_output=True,
                text=True,
                timeout=10,
            )
            names = tuple(line.strip().rsplit("/", 1)[-1] for line in result.stdout.splitlines() if line.strip())
    except (OSError, UnicodeError, subprocess.SubprocessError) as exc:
        return ProcessCheck(state="unknown", matches=(), error=str(exc))
    targets = {
        name.casefold()
        for name in profile.process_names + profile.updater_names
    }
    matches = tuple(sorted({name for name in names if name.casefold() in targets}))
    return ProcessCheck(
        state="running" if matches else "stopped",
        matches=matches,
    )


def require_host_stopped(profile: HostProfile, system: Optional[str] = None) -> None:
    check = inspect_host_processes(profile, system=system)
    if check.state == "unknown":
        raise ProcessError(f"cannot determine IDE process state: {check.error}")
    if check.state == "running":
        raise ProcessError(
            "close the IDE and updater before patching: " + ", ".join(check.matches)
        )
