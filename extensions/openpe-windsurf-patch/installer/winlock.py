"""Windows exclusive file handles for the exact multi-bundle transaction.

The exact-build patch verifies its artifact hashes and then writes new
content. A hash check followed by a separate write is not a compare-and-swap:
the vendor updater can replace an artifact between the two steps, and the
transaction's own mutation lock only excludes other openPE installers, not
the updater. Holding a handle opened with ``dwShareMode=0`` for the whole
mutation makes the exclusion real — every other writer (and reader) gets a
sharing violation until the transaction releases the handle — provided all
of our own reads and writes go THROUGH the held handle, which the helpers
here implement via ReadFile/WriteFile/SetEndOfFile.

POSIX has no mandatory sharing modes, so this module is Windows-only by
contract; importing it is safe everywhere (ctypes is only touched inside the
functions), and callers gate usage on ``os.name == "nt"``.
"""

from __future__ import annotations

import ctypes
import os
import subprocess
from contextlib import contextmanager
from pathlib import Path
from typing import Iterator

GENERIC_READ = 0x80000000
GENERIC_WRITE = 0x40000000
OPEN_EXISTING = 3
FILE_ATTRIBUTE_NORMAL = 0x00000080
FILE_ATTRIBUTE_REPARSE_POINT = 0x00000400
FILE_FLAG_OPEN_REPARSE_POINT = 0x00200000
FILE_BEGIN = 0
INVALID_HANDLE_VALUE = ctypes.c_void_p(-1).value

_CHUNK = 1 << 20


class WinLockError(OSError):
    """Raised when an exclusive-handle operation fails."""


class _BY_HANDLE_FILE_INFORMATION(ctypes.Structure):
    _fields_ = [
        ("FileAttributes", ctypes.c_uint32),
        ("CreationTimeLow", ctypes.c_uint32),
        ("CreationTimeHigh", ctypes.c_uint32),
        ("LastAccessTimeLow", ctypes.c_uint32),
        ("LastAccessTimeHigh", ctypes.c_uint32),
        ("LastWriteTimeLow", ctypes.c_uint32),
        ("LastWriteTimeHigh", ctypes.c_uint32),
        ("VolumeSerialNumber", ctypes.c_uint32),
        ("FileSizeHigh", ctypes.c_uint32),
        ("FileSizeLow", ctypes.c_uint32),
        ("NumberOfLinks", ctypes.c_uint32),
        ("FileIndexHigh", ctypes.c_uint32),
        ("FileIndexLow", ctypes.c_uint32),
    ]


def _kernel32() -> ctypes.WinDLL:  # pragma: no cover - Windows only
    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel32.CreateFileW.argtypes = (
        ctypes.c_wchar_p,
        ctypes.c_uint32,
        ctypes.c_uint32,
        ctypes.c_void_p,
        ctypes.c_uint32,
        ctypes.c_uint32,
        ctypes.c_void_p,
    )
    kernel32.CreateFileW.restype = ctypes.c_void_p
    kernel32.ReadFile.argtypes = (
        ctypes.c_void_p,
        ctypes.c_void_p,
        ctypes.c_uint32,
        ctypes.POINTER(ctypes.c_uint32),
        ctypes.c_void_p,
    )
    kernel32.ReadFile.restype = ctypes.c_int
    kernel32.WriteFile.argtypes = (
        ctypes.c_void_p,
        ctypes.c_void_p,
        ctypes.c_uint32,
        ctypes.POINTER(ctypes.c_uint32),
        ctypes.c_void_p,
    )
    kernel32.WriteFile.restype = ctypes.c_int
    kernel32.SetFilePointerEx.argtypes = (
        ctypes.c_void_p,
        ctypes.c_int64,
        ctypes.POINTER(ctypes.c_int64),
        ctypes.c_uint32,
    )
    kernel32.SetFilePointerEx.restype = ctypes.c_int
    kernel32.SetEndOfFile.argtypes = (ctypes.c_void_p,)
    kernel32.SetEndOfFile.restype = ctypes.c_int
    kernel32.FlushFileBuffers.argtypes = (ctypes.c_void_p,)
    kernel32.FlushFileBuffers.restype = ctypes.c_int
    kernel32.CloseHandle.argtypes = (ctypes.c_void_p,)
    kernel32.CloseHandle.restype = ctypes.c_int
    kernel32.GetFileInformationByHandle.argtypes = (
        ctypes.c_void_p,
        ctypes.POINTER(_BY_HANDLE_FILE_INFORMATION),
    )
    kernel32.GetFileInformationByHandle.restype = ctypes.c_int
    return kernel32


def _raise_last(kernel32, message: str) -> None:  # pragma: no cover
    raise WinLockError(ctypes.get_last_error(), message)


@contextmanager
def _exclusive_handle(path: Path, access: int) -> Iterator[object]:  # pragma: no cover
    kernel32 = _kernel32()
    handle = kernel32.CreateFileW(
        str(path),
        access,
        0,  # dwShareMode=0：持有期间拒绝 read/write/delete open。
        None,
        OPEN_EXISTING,
        FILE_ATTRIBUTE_NORMAL | FILE_FLAG_OPEN_REPARSE_POINT,
        None,
    )
    if handle == INVALID_HANDLE_VALUE:
        _raise_last(kernel32, f"cannot open exclusive handle: {path}")
    try:
        verify_regular_identity(handle, path)
        yield handle
    finally:
        kernel32.CloseHandle(handle)


@contextmanager
def exclusive_handle(path: Path) -> Iterator[object]:  # pragma: no cover
    """以读写模式独占普通、非 hardlink/reparse 文件。"""
    with _exclusive_handle(path, GENERIC_READ | GENERIC_WRITE) as handle:
        yield handle


@contextmanager
def exclusive_read_handle(path: Path) -> Iterator[object]:  # pragma: no cover
    """只读独占 guard（用于 Devin.exe build identity）。"""
    with _exclusive_handle(path, GENERIC_READ) as handle:
        yield handle


def verify_regular_identity(handle: object, path: Path) -> tuple[int, int]:  # pragma: no cover
    info = _BY_HANDLE_FILE_INFORMATION()
    kernel32 = _kernel32()
    if not kernel32.GetFileInformationByHandle(handle, ctypes.byref(info)):
        _raise_last(kernel32, f"cannot inspect exclusive handle: {path}")
    if info.FileAttributes & FILE_ATTRIBUTE_REPARSE_POINT:
        raise WinLockError(f"reparse-point artifact is refused: {path}")
    if info.NumberOfLinks != 1:
        raise WinLockError(f"hard-linked artifact is refused: {path}")
    file_index = (info.FileIndexHigh << 32) | info.FileIndexLow
    return info.VolumeSerialNumber, file_index


def _seek_start(kernel32, handle) -> None:  # pragma: no cover
    if not kernel32.SetFilePointerEx(handle, 0, None, FILE_BEGIN):
        _raise_last(kernel32, "cannot seek exclusive handle")


def read_all(handle: object) -> bytes:  # pragma: no cover
    """Read the whole file through the held handle."""
    kernel32 = _kernel32()
    _seek_start(kernel32, handle)
    chunks = []
    buffer = ctypes.create_string_buffer(_CHUNK)
    read = ctypes.c_uint32(0)
    while True:
        if not kernel32.ReadFile(handle, buffer, _CHUNK, ctypes.byref(read), None):
            _raise_last(kernel32, "cannot read exclusive handle")
        if read.value == 0:
            break
        chunks.append(buffer.raw[: read.value])
    return b"".join(chunks)


def restrict_directory(path: Path) -> None:
    """Windows 上把 transaction root 设为当前用户独占且禁止继承。"""
    if os.name != "nt":
        return
    powershell = Path(os.environ.get("SystemRoot", r"C:\Windows")) / "System32" / "WindowsPowerShell" / "v1.0" / "powershell.exe"
    script = r"""$ErrorActionPreference='Stop';$me=[Security.Principal.WindowsIdentity]::GetCurrent().User;$acl=New-Object Security.AccessControl.DirectorySecurity;$acl.SetOwner($me);$acl.SetAccessRuleProtection($true,$false);$rule=New-Object Security.AccessControl.FileSystemAccessRule($me,[Security.AccessControl.FileSystemRights]::FullControl,[Security.AccessControl.InheritanceFlags]'ContainerInherit,ObjectInherit',[Security.AccessControl.PropagationFlags]::None,[Security.AccessControl.AccessControlType]::Allow);$acl.AddAccessRule($rule);Set-Acl -LiteralPath $args[0] -AclObject $acl"""
    try:
        subprocess.run(
            [str(powershell), "-NoProfile", "-NonInteractive", "-Command", script, str(path)],
            check=True,
            timeout=5,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
    except (OSError, subprocess.SubprocessError) as exc:
        raise WinLockError(f"cannot restrict transaction directory {path}: {exc}") from exc


def write_all(handle: object, data: bytes) -> None:  # pragma: no cover
    """Replace the file content through the held handle and flush to disk.

    Not crash-atomic like temp+rename — the exclusive handle rules out
    concurrent writers instead, and the transaction's durable backups cover
    crash recovery.
    """
    kernel32 = _kernel32()
    _seek_start(kernel32, handle)
    view = memoryview(data)
    offset = 0
    written = ctypes.c_uint32(0)
    while offset < len(view):
        chunk = view[offset : offset + _CHUNK]
        raw = bytes(chunk)
        buffer = ctypes.create_string_buffer(raw, len(raw))
        if not kernel32.WriteFile(handle, buffer, len(raw), ctypes.byref(written), None):
            _raise_last(kernel32, "cannot write exclusive handle")
        if written.value == 0:
            _raise_last(kernel32, "exclusive handle wrote zero bytes")
        offset += written.value
    if not kernel32.SetEndOfFile(handle):
        _raise_last(kernel32, "cannot truncate exclusive handle")
    if not kernel32.FlushFileBuffers(handle):
        _raise_last(kernel32, "cannot flush exclusive handle")
