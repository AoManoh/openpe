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
from ipaddress import ip_address
from pathlib import Path
from typing import Any, Dict, Optional
from urllib.error import HTTPError, URLError
from urllib.parse import urlparse
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener

DEFAULT_DESCRIPTOR_NAME = "server.json"
WINDSURF_CORS_ORIGINS = ("null", "app://windsurf")

# Upper bound for the /v1/info response body. The endpoint returns a small
# JSON document; anything larger is not our server. Mirrors the bounded-read
# posture of runtime_probe (256 KiB) and the Go client-side read limits.
MAX_INFO_BODY_BYTES = 256 * 1024


class _RefuseRedirects(HTTPRedirectHandler):
    """Refuse every 3xx: the bearer token must never follow a Location.

    ``build_opener`` installs a default ``HTTPRedirectHandler`` that copies
    request headers — including ``Authorization`` — onto the redirected
    request, so a compromised or squatted loopback port could bounce the
    token to an arbitrary origin (CR-001). ``/v1/info`` never legitimately
    redirects; returning ``None`` here makes urllib surface the 3xx as an
    ``HTTPError``, which ``verify_server`` maps to a redirect-specific
    ``HandshakeError``. The TypeScript client enforces the same policy with
    ``redirect: "error"``.
    """

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


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
        validate_loopback_base_url(self.base_url)
        if not self.token:
            raise DescriptorError("descriptor: token is required")
        if self.pid <= 0:
            raise DescriptorError("descriptor: pid must be positive")

    def info_url(self) -> str:
        return self.base_url.rstrip("/") + "/v1/info"


def validate_loopback_base_url(base_url: str) -> None:
    """Reject descriptor URLs that would send the bearer token off-host."""
    try:
        parsed = urlparse(base_url.strip())
    except ValueError as exc:
        raise DescriptorError(f"descriptor: invalid base_url: {exc}") from exc
    if parsed.scheme not in {"http", "https"}:
        raise DescriptorError("descriptor: base_url must use http or https")
    if not parsed.hostname:
        raise DescriptorError("descriptor: base_url must include a host")
    try:
        _ = parsed.port
    except ValueError as exc:
        raise DescriptorError(f"descriptor: invalid base_url port: {exc}") from exc
    if parsed.username or parsed.password:
        raise DescriptorError("descriptor: base_url must not include credentials")
    if parsed.params or parsed.query or parsed.fragment:
        raise DescriptorError(
            "descriptor: base_url must not include params, query, or fragment"
        )
    if parsed.path not in {"", "/"}:
        raise DescriptorError("descriptor: base_url must not include a path")
    host = parsed.hostname.strip().lower()
    if host == "localhost":
        return
    try:
        if ip_address(host).is_loopback:
            return
    except ValueError:
        pass
    raise DescriptorError("descriptor: base_url must point to a loopback host")


def validate_profile_cors(
    info: Dict[str, Any],
    required_origins: tuple,
    display_name: str,
) -> None:
    """Ensure the running server allows the selected host's Electron origin."""
    if not required_origins:
        raise HandshakeError(
            f"{display_name} renderer origin is not verified; bundle install is disabled"
        )
    if info.get("auth_enabled") is not True:
        raise HandshakeError(
            "openpe-server auth is disabled; start it with OPENPE_SERVER_TOKEN set"
        )
    if info.get("cors_enabled") is not True:
        required = ",".join(required_origins)
        raise HandshakeError(
            "openpe-server CORS is disabled; start it with "
            f"OPENPE_SERVER_CORS_ORIGINS={required}"
        )
    raw_origins = info.get("cors_origins")
    if not isinstance(raw_origins, list):
        raise HandshakeError("openpe-server /v1/info did not include cors_origins")
    origins = {str(origin).strip() for origin in raw_origins if str(origin).strip()}
    if "*" in origins:
        raise HandshakeError(
            f"openpe-server CORS wildcard origin is not accepted for {display_name} patch installs"
        )
    required_set = set(required_origins)
    missing = sorted(required_set - origins)
    if not missing:
        return
    required = ", ".join(required_origins)
    raise HandshakeError(
        f"openpe-server CORS origins must exactly include all of: {required}"
    )


def validate_windsurf_cors(info: Dict[str, Any]) -> None:
    validate_profile_cors(info, WINDSURF_CORS_ORIGINS, "Windsurf")


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
    # NTFS does not honour POSIX mode bits: Go's os.Chmod(0o600) only toggles
    # the read-only flag on Windows, and Path.stat().st_mode reports 0o666
    # for any writable file. The check would unconditionally reject any
    # descriptor on Windows; rely on the default %USERPROFILE% ACL instead,
    # matching OpenSSH's StrictModes-on-Windows behaviour.
    if os.name == "posix" and mode & 0o077 != 0:
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
    try:
        descriptor.validate()
    except DescriptorError as exc:
        raise HandshakeError(str(exc)) from exc
    req = Request(
        descriptor.info_url(),
        headers={"Authorization": f"Bearer {descriptor.token}"},
        method="GET",
    )
    # Always bypass the system HTTP proxy: openpe-server lives on the
    # loopback interface and routing through an HTTP/HTTPS proxy would
    # only delay the request or return 502 if the proxy refuses to
    # forward to 127.0.0.1. Redirects are refused outright (CR-001): the
    # Authorization header must never travel to a Location target.
    opener = build_opener(ProxyHandler({}), _RefuseRedirects())
    try:
        with opener.open(req, timeout=timeout) as resp:  # nosec B310 (loopback only)
            status = getattr(resp, "status", None)
            if status is None:
                status = resp.getcode()
            if status != 200:
                raise HandshakeError(f"server /v1/info returned status {status}")
            # Defense in depth alongside _RefuseRedirects: the final URL must
            # still be the descriptor origin (same strictness as the
            # TypeScript client's URL gate).
            _require_descriptor_origin(resp.geturl(), descriptor)
            raw = resp.read(MAX_INFO_BODY_BYTES + 1)
            if len(raw) > MAX_INFO_BODY_BYTES:
                raise HandshakeError(
                    f"/v1/info response exceeds {MAX_INFO_BODY_BYTES} bytes; not an openpe-server"
                )
            body = raw.decode("utf-8")
    except HTTPError as exc:
        if 300 <= exc.code < 400:
            raise HandshakeError(
                f"refusing to follow HTTP {exc.code} redirect from /v1/info; "
                "the bearer token is only ever sent to the descriptor origin"
            ) from exc
        raise HandshakeError(f"server rejected request: HTTP {exc.code}") from exc
    except URLError as exc:
        raise HandshakeError(f"cannot reach openpe-server: {exc.reason}") from exc
    except TimeoutError as exc:
        raise HandshakeError(f"openpe-server /v1/info timed out: {exc}") from exc
    except UnicodeDecodeError as exc:
        raise HandshakeError(f"malformed /v1/info response: {exc}") from exc
    try:
        parsed = json.loads(body)
    except json.JSONDecodeError as exc:
        raise HandshakeError(f"malformed /v1/info response: {exc}") from exc
    if not isinstance(parsed, dict):
        raise HandshakeError("malformed /v1/info response: root must be a JSON object")
    return parsed


def _require_descriptor_origin(final_url: str, descriptor: LocalServerDescriptor) -> None:
    """Reject responses whose final URL left the descriptor origin."""
    expected = urlparse(descriptor.base_url.strip())
    actual = urlparse(str(final_url).strip())
    if (
        actual.scheme != expected.scheme
        or (actual.hostname or "").lower() != (expected.hostname or "").lower()
        or actual.port != expected.port
    ):
        raise HandshakeError(
            f"/v1/info answered from unexpected origin {final_url!r}; "
            f"expected {descriptor.base_url!r}"
        )
