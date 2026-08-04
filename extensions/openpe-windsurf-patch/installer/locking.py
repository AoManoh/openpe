from __future__ import annotations

import ctypes
import hashlib
import os
import tempfile
import threading
from contextlib import contextmanager
from pathlib import Path
from typing import Iterator, Set

from .bundle import _durable_mkdir
from .paths import IDEPaths


class LockError(Exception):
    pass


_HELD_LOCKS: Set[str] = set()
_HELD_LOCKS_GUARD = threading.Lock()


def _lock_name(paths: IDEPaths) -> str:
    return f"openpe-ide-patch-{paths.install_id}"


@contextmanager
def _windows_mutex(name: str) -> Iterator[Path]:
    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    create_mutex = kernel32.CreateMutexW
    create_mutex.argtypes = (ctypes.c_void_p, ctypes.c_int, ctypes.c_wchar_p)
    create_mutex.restype = ctypes.c_void_p
    wait = kernel32.WaitForSingleObject
    wait.argtypes = (ctypes.c_void_p, ctypes.c_uint32)
    wait.restype = ctypes.c_uint32
    release = kernel32.ReleaseMutex
    release.argtypes = (ctypes.c_void_p,)
    release.restype = ctypes.c_int
    close = kernel32.CloseHandle
    close.argtypes = (ctypes.c_void_p,)
    close.restype = ctypes.c_int
    mutex_name = "Global\\" + name
    with _HELD_LOCKS_GUARD:
        if mutex_name in _HELD_LOCKS:
            raise LockError(f"another patch process holds mutation lock {mutex_name}")
        _HELD_LOCKS.add(mutex_name)
    handle = create_mutex(None, False, mutex_name)
    if not handle:
        with _HELD_LOCKS_GUARD:
            _HELD_LOCKS.discard(mutex_name)
        raise LockError(
            f"cannot create mutation mutex {mutex_name}: Windows error {ctypes.get_last_error()}"
        )
    acquired = False
    try:
        result = wait(handle, 0)
        if result not in {0x00000000, 0x00000080}:
            if result == 0x00000102:
                raise LockError(f"another patch process holds mutation lock {mutex_name}")
            raise LockError(
                f"cannot acquire mutation mutex {mutex_name}: result 0x{result:08x}"
            )
        acquired = True
        yield Path(mutex_name)
    finally:
        if acquired:
            release(handle)
        close(handle)
        with _HELD_LOCKS_GUARD:
            _HELD_LOCKS.discard(mutex_name)


@contextmanager
def _posix_lock(name: str) -> Iterator[Path]:
    import fcntl

    lock_dir = Path(tempfile.gettempdir()) / "openpe-ide-patch-locks"
    try:
        _durable_mkdir(lock_dir, mode=0o700)
    except OSError as exc:
        raise LockError(f"cannot create mutation lock directory {lock_dir}: {exc}") from exc
    lock_path = lock_dir / (hashlib.sha256(name.encode("utf-8")).hexdigest() + ".lock")
    # The handle must be closed on EVERY path: when open() succeeds but
    # chmod/flock fails (lock contention), the old single try block raised
    # straight out of the except and leaked the descriptor (CR-017; the
    # nested-lock test reproduced it as a ResourceWarning).
    handle = None
    try:
        handle = open(lock_path, "a+b")
        os.chmod(lock_path, 0o600)
        fcntl.flock(handle.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError as exc:
        if handle is not None:
            handle.close()
        raise LockError(f"another patch process holds mutation lock {lock_path}") from exc
    try:
        yield lock_path
    finally:
        try:
            fcntl.flock(handle.fileno(), fcntl.LOCK_UN)
        finally:
            handle.close()


@contextmanager
def mutation_lock(paths: IDEPaths) -> Iterator[Path]:
    name = _lock_name(paths)
    manager = _windows_mutex(name) if os.name == "nt" else _posix_lock(name)
    with manager as lock_path:
        yield lock_path
