from __future__ import annotations

import argparse
import json
import math
import secrets
import sys
import threading
import time
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Dict, Optional, Sequence, Set
from urllib.parse import urlsplit

from .bundle import _atomic_write


class ProbeError(Exception):
    pass


def validate_probe_endpoint(value: str) -> str:
    try:
        parsed = urlsplit(value.strip())
        port = parsed.port
    except ValueError as exc:
        raise ProbeError(f"invalid probe endpoint: {exc}") from exc
    prefix = "/probe/"
    token = parsed.path[len(prefix) :] if parsed.path.startswith(prefix) else ""
    if (
        parsed.scheme != "http"
        or parsed.hostname != "127.0.0.1"
        or port is None
        or port < 1
        or port > 65535
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
        or parsed.path != f"/probe/{token}"
        or len(token) < 32
        or len(token) > 128
        or any(character not in "0123456789abcdef" for character in token)
    ):
        raise ProbeError(
            "probe endpoint must be http://127.0.0.1:<port>/probe/<32-128 lowercase hex token>"
        )
    return value.strip()


def build_probe_payload(endpoint: str) -> str:
    endpoint = validate_probe_endpoint(endpoint)
    endpoint_json = json.dumps(endpoint)
    return f'''(() => {{
  "use strict";
  const endpoint = {endpoint_json};
  const visible = (element) => {{
    const rect = element.getBoundingClientRect();
    const style = getComputedStyle(element);
    return rect.width > 0 && rect.height > 0 && style.display !== "none" && style.visibility !== "hidden";
  }};
  const tagKind = (element) => {{
    const tag = element.tagName.toLowerCase();
    return ["div", "textarea", "input", "button", "span", "a", "section", "form"].includes(tag)
      ? tag
      : "other";
  }};
  const dataAttributeCount = (element) => Array.from(element.attributes)
    .filter((attribute) => attribute.name.startsWith("data-")).length;
  const shape = (element) => {{
    const rect = element.getBoundingClientRect();
    const ancestors = [];
    let current = element;
    for (let index = 0; current && index < 7; index += 1, current = current.parentElement) {{
      ancestors.push({{
        tag: tagKind(current),
        id_present: current.id.length > 0,
        class_count: current.classList.length,
        role_present: current.hasAttribute("role"),
        data_attribute_count: dataAttributeCount(current),
        child_index: current.parentElement
          ? Array.prototype.indexOf.call(current.parentElement.children, current)
          : 0,
        child_count: current.children.length,
      }});
    }}
    return {{
      tag: tagKind(element),
      id_present: element.id.length > 0,
      class_count: element.classList.length,
      role_present: element.hasAttribute("role"),
      aria_label_present: element.hasAttribute("aria-label"),
      placeholder_present: element.hasAttribute("placeholder"),
      contenteditable: element.getAttribute("contenteditable") === "true",
      data_attribute_count: dataAttributeCount(element),
      rect: {{
        x: Math.round(rect.x),
        y: Math.round(rect.y),
        width: Math.round(rect.width),
        height: Math.round(rect.height),
      }},
      ancestors,
    }};
  }};
  const collect = async () => {{
    const editors = Array.from(document.querySelectorAll(
      'textarea, [contenteditable="true"], [role="textbox"]'
    )).filter(visible).slice(0, 40).map(shape);
    const buttons = Array.from(document.querySelectorAll(
      'button, [role="button"]'
    )).filter(visible).slice(0, 160).map(shape);
    let databases = [];
    let database_error = false;
    try {{
      if (typeof indexedDB.databases === "function") {{
        databases = await indexedDB.databases();
      }}
    }} catch (error) {{
      database_error = true;
    }}
    const report = {{
      schema_version: 1,
      location: {{
        kind: ["vscode-file:", "file:", "http:", "https:"].includes(location.protocol)
          ? location.protocol.slice(0, -1)
          : "other",
        is_vscode_app: location.protocol === "vscode-file:" && location.hostname === "vscode-app",
      }},
      document: {{
        ready_state: document.readyState,
        body_class_count: document.body.classList.length,
        iframe_count: document.querySelectorAll("iframe").length,
      }},
      bridge: {{
        vscode: typeof globalThis.vscode === "object",
        vscode_context: typeof globalThis.vscode?.context === "object",
        vscode_process: typeof globalThis.vscode?.process === "object",
      }},
      viewport: {{
        width: window.innerWidth,
        height: window.innerHeight,
        device_pixel_ratio: window.devicePixelRatio,
      }},
      editors,
      buttons,
      indexeddb: {{ database_count: databases.length, error: database_error }},
      globals: {{
        vscode: Object.hasOwn(globalThis, "vscode"),
        acquire_vscode_api: Object.hasOwn(globalThis, "acquireVsCodeApi"),
        openpe: Object.hasOwn(globalThis, "__openpe"),
        windsurf: Object.hasOwn(globalThis, "__windsurf"),
        devin: Object.hasOwn(globalThis, "__devin"),
      }},
    }};
    await fetch(endpoint, {{
      method: "POST",
      headers: {{ "Content-Type": "text/plain;charset=UTF-8" }},
      body: JSON.stringify(report),
      cache: "no-store",
      credentials: "omit",
      mode: "cors",
    }});
  }};
  const start = () => setTimeout(() => void collect().catch(() => undefined), 5000);
  if (document.readyState === "loading") {{
    document.addEventListener("DOMContentLoaded", start, {{ once: true }});
  }} else {{
    start();
  }}
}})();
'''


def _exact_keys(value: Dict[str, Any], expected: Set[str], label: str) -> None:
    if set(value) != expected:
        raise ProbeError(f"invalid {label} fields")


def _bounded_int(value: Any, minimum: int, maximum: int, label: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise ProbeError(f"invalid {label}")
    if value < minimum or value > maximum:
        raise ProbeError(f"invalid {label}")
    return value


_TAG_KINDS = {"div", "textarea", "input", "button", "span", "a", "section", "form", "other"}


def _validate_ancestor(value: Any) -> None:
    if not isinstance(value, dict):
        raise ProbeError("invalid ancestor")
    _exact_keys(
        value,
        {
            "tag",
            "id_present",
            "class_count",
            "role_present",
            "data_attribute_count",
            "child_index",
            "child_count",
        },
        "ancestor",
    )
    if value["tag"] not in _TAG_KINDS:
        raise ProbeError("invalid ancestor tag")
    for key in ("id_present", "role_present"):
        if not isinstance(value[key], bool):
            raise ProbeError(f"invalid ancestor {key}")
    _bounded_int(value["class_count"], 0, 256, "ancestor class count")
    _bounded_int(value["child_index"], 0, 10000, "ancestor child index")
    _bounded_int(value["child_count"], 0, 10000, "ancestor child count")
    _bounded_int(
        value["data_attribute_count"], 0, 1000, "ancestor data attribute count"
    )


def _validate_shape(value: Any) -> None:
    if not isinstance(value, dict):
        raise ProbeError("invalid element shape")
    _exact_keys(
        value,
        {
            "tag",
            "id_present",
            "class_count",
            "role_present",
            "aria_label_present",
            "placeholder_present",
            "contenteditable",
            "data_attribute_count",
            "rect",
            "ancestors",
        },
        "element shape",
    )
    if value["tag"] not in _TAG_KINDS:
        raise ProbeError("invalid element tag")
    for key in (
        "id_present",
        "role_present",
        "aria_label_present",
        "placeholder_present",
        "contenteditable",
    ):
        if not isinstance(value[key], bool):
            raise ProbeError(f"invalid element {key}")
    _bounded_int(value["class_count"], 0, 256, "element class count")
    _bounded_int(
        value["data_attribute_count"], 0, 1000, "element data attribute count"
    )
    rect = value["rect"]
    if not isinstance(rect, dict):
        raise ProbeError("invalid element rect")
    _exact_keys(rect, {"x", "y", "width", "height"}, "element rect")
    for key in ("x", "y", "width", "height"):
        _bounded_int(rect[key], -100000, 100000, f"element rect {key}")
    ancestors = value["ancestors"]
    if not isinstance(ancestors, list) or len(ancestors) > 7:
        raise ProbeError("invalid element ancestors")
    for ancestor in ancestors:
        _validate_ancestor(ancestor)


def validate_probe_report(value: Any) -> Dict[str, Any]:
    if not isinstance(value, dict):
        raise ProbeError("probe report must be an object")
    _exact_keys(
        value,
        {
            "schema_version",
            "location",
            "document",
            "bridge",
            "viewport",
            "editors",
            "buttons",
            "indexeddb",
            "globals",
        },
        "probe report",
    )
    if value["schema_version"] != 1:
        raise ProbeError("invalid probe schema version")
    location = value["location"]
    if not isinstance(location, dict):
        raise ProbeError("invalid location")
    _exact_keys(location, {"kind", "is_vscode_app"}, "location")
    if location["kind"] not in {"vscode-file", "file", "http", "https", "other"}:
        raise ProbeError("invalid location kind")
    if not isinstance(location["is_vscode_app"], bool):
        raise ProbeError("invalid location flag")
    document = value["document"]
    if not isinstance(document, dict):
        raise ProbeError("invalid document")
    _exact_keys(
        document,
        {"ready_state", "body_class_count", "iframe_count"},
        "document",
    )
    if document["ready_state"] not in {"loading", "interactive", "complete"}:
        raise ProbeError("invalid document ready state")
    _bounded_int(document["body_class_count"], 0, 1000, "body class count")
    _bounded_int(document["iframe_count"], 0, 1000, "iframe count")
    bridge = value["bridge"]
    globals_value = value["globals"]
    if not isinstance(bridge, dict) or not isinstance(globals_value, dict):
        raise ProbeError("invalid bridge/globals")
    _exact_keys(bridge, {"vscode", "vscode_context", "vscode_process"}, "bridge")
    _exact_keys(
        globals_value,
        {"vscode", "acquire_vscode_api", "openpe", "windsurf", "devin"},
        "globals",
    )
    if not all(isinstance(item, bool) for item in bridge.values()):
        raise ProbeError("invalid bridge flags")
    if not all(isinstance(item, bool) for item in globals_value.values()):
        raise ProbeError("invalid global flags")
    viewport = value["viewport"]
    if not isinstance(viewport, dict):
        raise ProbeError("invalid viewport")
    _exact_keys(viewport, {"width", "height", "device_pixel_ratio"}, "viewport")
    for key in ("width", "height", "device_pixel_ratio"):
        item = viewport[key]
        if isinstance(item, bool) or not isinstance(item, (int, float)):
            raise ProbeError(f"invalid viewport {key}")
        if not math.isfinite(item) or item < 0 or item > 100000:
            raise ProbeError(f"invalid viewport {key}")
    for key, limit in (("editors", 40), ("buttons", 160)):
        items = value[key]
        if not isinstance(items, list) or len(items) > limit:
            raise ProbeError(f"invalid {key}")
        for item in items:
            _validate_shape(item)
    indexeddb = value["indexeddb"]
    if not isinstance(indexeddb, dict):
        raise ProbeError("invalid indexeddb")
    _exact_keys(indexeddb, {"database_count", "error"}, "indexeddb")
    _bounded_int(indexeddb["database_count"], 0, 10000, "database count")
    if not isinstance(indexeddb["error"], bool):
        raise ProbeError("invalid indexeddb error")
    return value


def _origin_kind(origin: str) -> str:
    if origin == "null":
        return "null"
    if origin == "vscode-file://vscode-app":
        return "vscode-file"
    if origin.startswith("file://"):
        return "file"
    if origin.startswith("http://127.0.0.1:"):
        return "http-loopback"
    return "other"


class ProbeReceiver:
    def __init__(
        self,
        output: Path,
        token: Optional[str] = None,
        port: int = 0,
        timeout: float = 120.0,
    ) -> None:
        self.output = output.expanduser().resolve()
        self.token = token or secrets.token_hex(16)
        self.timeout = timeout
        self.ready = threading.Event()
        self.report: Optional[Dict[str, Any]] = None
        self.claim = threading.Lock()
        if self.output.exists() or self.output.is_symlink():
            raise ProbeError(f"probe output already exists: {self.output}")
        receiver = self

        class Handler(BaseHTTPRequestHandler):
            def do_OPTIONS(self) -> None:
                if self.path != f"/probe/{receiver.token}":
                    self.send_error(404)
                    return
                self.send_response(204)
                self._cors()
                self.send_header("Access-Control-Allow-Methods", "POST, OPTIONS")
                self.send_header("Access-Control-Allow-Headers", "Content-Type")
                if self.headers.get("Access-Control-Request-Private-Network") == "true":
                    self.send_header("Access-Control-Allow-Private-Network", "true")
                self.end_headers()

            def do_POST(self) -> None:
                if self.path != f"/probe/{receiver.token}":
                    self.send_error(404)
                    return
                try:
                    length = int(self.headers.get("Content-Length", "0"))
                except ValueError:
                    self.send_error(400)
                    return
                if length < 2 or length > 262144:
                    self.send_error(413)
                    return
                try:
                    value = json.loads(self.rfile.read(length).decode("utf-8"))
                except (UnicodeDecodeError, json.JSONDecodeError):
                    self.send_error(400)
                    return
                try:
                    value = validate_probe_report(value)
                except ProbeError:
                    self.send_error(422)
                    return
                with receiver.claim:
                    if receiver.report is not None or receiver.output.exists():
                        self.send_error(409)
                        return
                    record = {
                        "received_at": datetime.now(tz=timezone.utc)
                        .isoformat()
                        .replace("+00:00", "Z"),
                        "request_origin_kind": _origin_kind(
                            self.headers.get("Origin", "")
                        ),
                        "report": value,
                    }
                    _atomic_write(
                        receiver.output,
                        json.dumps(record, ensure_ascii=False, indent=2).encode("utf-8")
                        + b"\n",
                        mode=0o600,
                    )
                    receiver.report = record
                self.send_response(204)
                self._cors()
                self.end_headers()

            def _cors(self) -> None:
                origin = self.headers.get("Origin", "")
                if origin:
                    self.send_header("Access-Control-Allow-Origin", origin)
                    self.send_header("Vary", "Origin")

            def log_message(self, format: str, *args: Any) -> None:
                return

        self.server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
        self.server.timeout = 0.25
        self.endpoint = validate_probe_endpoint(
            f"http://127.0.0.1:{self.server.server_port}/probe/{self.token}"
        )

    def serve_once(self) -> None:
        self.ready.set()
        deadline = time.monotonic() + self.timeout
        try:
            while self.report is None and time.monotonic() < deadline:
                self.server.handle_request()
            if self.report is None:
                raise ProbeError("timed out waiting for Devin runtime probe")
        finally:
            self.server.server_close()


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = argparse.ArgumentParser(prog="python -m installer.runtime_probe")
    parser.add_argument("--output", required=True)
    parser.add_argument("--token", default=None)
    parser.add_argument("--port", type=int, default=0)
    parser.add_argument("--timeout", type=float, default=180.0)
    args = parser.parse_args(list(argv) if argv is not None else None)
    receiver = ProbeReceiver(
        Path(args.output),
        token=args.token,
        port=args.port,
        timeout=args.timeout,
    )
    sys.stdout.write(receiver.endpoint + "\n")
    sys.stdout.flush()
    try:
        receiver.serve_once()
    except ProbeError as exc:
        sys.stderr.write(f"openpe-ide-probe: {exc}\n")
        return 1
    sys.stdout.write(str(receiver.output) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
