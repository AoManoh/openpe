"""Handshake with the local openpe-server.

This module reads the :class:`LocalServerDescriptor` that
``openpe-server`` writes when started with
``OPENPE_SERVER_LIFECYCLE_ENABLED=true``, validates the on-disk file
permissions, and optionally performs a live ``GET /v1/info`` to confirm
the server is reachable and the bearer token still works.

The descriptor schema and resolution rules mirror the canonical Go
implementation in ``internal/integration`` of the main openPE repository.
"""

from __future__ import annotations

import json
import os
import stat
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Dict, Optional
from urllib.error import HTTPError, URLError
from urllib.request import ProxyHandler, Request, build_opener

DEFAULT_DESCRIPTOR_NAME = "server.json"


class DescriptorError(Exception):
    """Raised for any local descriptor read / validation failure."""


class HandshakeError(Exception):
    """Raised for any network / authentication failure against /v1/info."""


@dataclass(frozen=True)
class LocalServerDescriptor:
    """Python mirror of ``integration.LocalServerDescriptor``."""

    base_url: str
    token: str
    pid: int
    started_at: str = ""
    version: str = ""

    @classmethod
    def from_dict(cls, payload: Dict[str, Any]) -> "LocalServerDescriptor":
        try:
            return cls(
                base_url=str(payload["base_url"]).strip(),
                token=str(payload["token"]).strip(),
                pid=int(payload["pid"]),
                started_at=str(payload.get("started_at", "")).strip(),
                version=str(payload.get("version", "")).strip(),
            )
        except (KeyError, ValueError, TypeError) as exc:
            raise DescriptorError(f"malformed descriptor: {exc}") from exc

    def validate(self) -> None:
        if not self.base_url:
            raise DescriptorError("descriptor: base_url is required")
        if not self.token:
            raise DescriptorError("descriptor: token is required")
        if self.pid <= 0:
            raise DescriptorError("descriptor: pid must be positive")

    def info_url(self) -> str:
        return self.base_url.rstrip("/") + "/v1/info"


def default_descriptor_path() -> Path:
    """Mirror of ``integration.DefaultDescriptorPath``.

    Resolution order:
      1. ``OPENPE_SERVER_DESCRIPTOR_FILE`` env override
      2. ``$XDG_CONFIG_HOME/openpe/server.json``
      3. ``$HOME/.config/openpe/server.json``
    """
    explicit = os.environ.get("OPENPE_SERVER_DESCRIPTOR_FILE", "").strip()
    if explicit:
        return Path(explicit)
    xdg = os.environ.get("XDG_CONFIG_HOME", "").strip()
    base = Path(xdg) if xdg else (Path.home() / ".config")
    return base / "openpe" / DEFAULT_DESCRIPTOR_NAME


def read_descriptor(path: Optional[Path] = None) -> LocalServerDescriptor:
    """Read and validate a descriptor file.

    Refuses to read files whose mode is broader than 0600, mirroring the
    Go reader, because such a file may have leaked the bearer token to
    other local users.
    """
    if path is None:
        path = default_descriptor_path()
    if not path.is_file():
        raise DescriptorError(f"descriptor file not found: {path}")
    try:
        st = path.stat()
    except OSError as exc:
        raise DescriptorError(f"stat descriptor {path}: {exc}") from exc
    mode = stat.S_IMODE(st.st_mode)
    if mode & 0o077 != 0:
        raise DescriptorError(
            f"descriptor file {path} has insecure mode {oct(mode)} (want 0o600 or stricter)"
        )
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise DescriptorError(f"parse descriptor {path}: {exc}") from exc
    if not isinstance(payload, dict):
        raise DescriptorError(f"descriptor {path}: root must be a JSON object")
    descriptor = LocalServerDescriptor.from_dict(payload)
    descriptor.validate()
    return descriptor


def verify_server(
    descriptor: LocalServerDescriptor,
    timeout: float = 3.0,
) -> Dict[str, Any]:
    """Call ``GET /v1/info`` on the descriptor's base URL.

    Returns the parsed response body on success. Raises
    :class:`HandshakeError` on any network, HTTP, or decoding failure so
    callers can surface a single error type.
    """
    req = Request(
        descriptor.info_url(),
        headers={"Authorization": f"Bearer {descriptor.token}"},
        method="GET",
    )
    # Always bypass the system HTTP proxy: openpe-server lives on the
    # loopback interface and routing through an HTTP/HTTPS proxy would
    # only delay the request or return 502 if the proxy refuses to
    # forward to 127.0.0.1.
    opener = build_opener(ProxyHandler({}))
    try:
        with opener.open(req, timeout=timeout) as resp:  # nosec B310 (loopback only)
            status = getattr(resp, "status", None)
            if status is None:
                status = resp.getcode()
            if status != 200:
                raise HandshakeError(f"server /v1/info returned status {status}")
            body = resp.read().decode("utf-8")
    except HTTPError as exc:
        raise HandshakeError(f"server rejected request: HTTP {exc.code}") from exc
    except URLError as exc:
        raise HandshakeError(f"cannot reach openpe-server: {exc.reason}") from exc
    except TimeoutError as exc:
        raise HandshakeError(f"openpe-server /v1/info timed out: {exc}") from exc
    try:
        parsed = json.loads(body)
    except json.JSONDecodeError as exc:
        raise HandshakeError(f"malformed /v1/info response: {exc}") from exc
    if not isinstance(parsed, dict):
        raise HandshakeError("malformed /v1/info response: root must be a JSON object")
    return parsed
