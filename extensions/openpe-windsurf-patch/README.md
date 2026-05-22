# openpe-windsurf-patch

> **⚠️ EXPERIMENTAL — USER ASSUMES ALL RISK ⚠️**
>
> This subproject **patches the Windsurf IDE Electron bundle in place** to
> inject an openPE logo Enhance button into the Cascade chat input toolbar. It is
> **opt-in**, **off by default**, and **completely independent from the main
> openPE hook + VSIX paths**.
>
> By running the installer you acknowledge:
>
> 1. **EULA risk** — modifying the Windsurf application bundle may violate the
>    Windsurf / Codeium End-User License Agreement. Your account may be
>    suspended or refused technical support. Read the current EULA before
>    proceeding.
> 2. **Code-signing risk** — on macOS, re-signing the patched bundle invalidates
>    Apple notarisation; Gatekeeper may refuse to launch the IDE until the
>    quarantine attribute is removed manually.
> 3. **Checksum bypass** — `product.json` is modified to satisfy Electron's
>    resource-integrity check; this disables that safety net for the patched
>    file only.
> 4. **Upgrade fragility** — every Windsurf update overwrites the patched
>    bundle; you must re-run `install` after every IDE upgrade. The previous
>    backup is preserved and may not match the new IDE version.
> 5. **No warranty** — the installer is provided AS IS. If anything goes
>    wrong, restore from the backup (or reinstall Windsurf cleanly).
>
> **If any of the above is unacceptable to you, use the default openPE
> integration paths instead:**
>
> - `openpe windsurf hook install` (terminal-based `pe ...` keyword)
> - `extensions/vscode-openpe/` VSIX plugin (command palette / right-click)

---

## What this is

A standalone, MIT-licensed installer that:

- **Modifies** `workbench.desktop.main.js` inside the Windsurf application
  bundle to inject a small (~30 KB) JavaScript payload.
- The injected payload watches the Cascade chat input toolbar with
  `MutationObserver`, adds an openPE logo button next to Submit, opens a 3-page modal
  on click, calls **your local** `openpe-server` over HTTP (loopback only),
  and writes the enhanced prompt back into the Cascade input field.
  The button icon is inlined from the same SVG design as
  `extensions/vscode-openpe/media/openpe-icon.svg`; the patched bundle does
  not load resources from the VSIX directory at runtime.
- Talks to nothing outside `127.0.0.1`. No telemetry, no third-party gate
  servers, no commercial license keys.

## What this is NOT

- **NOT** a fork of WSE (`windsurf-enhance`) — no shared code, no shared keys,
  no shared server. We borrow only the public idea of "patch the bundle to
  add a button".
- **NOT** a replacement for the openPE hook or VSIX paths. Those remain the
  default, EULA-safe options.
- **NOT** automatically updated. Windsurf upgrade ⇒ re-run `install`.
- **NOT** part of the main openPE Go build. Lives in its own subproject with
  its own Python + Node.js toolchains.

## System requirements

- Python 3.8 or newer
- (Optional, for building inject.js from source) Node.js 18+ and npm
- A running `openpe-server` with lifecycle descriptor enabled. For the
  injected button path, prefer a fixed local token so the button keeps
  working across server restarts:

  ```bash
  # Generate this once and keep reusing it, e.g. from ~/.config/openpe/.env.
  export OPENPE_SERVER_TOKEN="<stable-64-hex-token>"
  export OPENPE_SERVER_LIFECYCLE_ENABLED=true
  export OPENPE_SERVER_CORS_ORIGINS=null,app://windsurf
  openpe-server
  ```

  The installer reads `~/.config/openpe/server.json` to discover the
  loopback base URL and bearer token, then snapshots them into the patched
  bundle.

## Status

- **Current**: `status`, `install`, `uninstall`, and `doctor` are real
  commands. `install` resolves the Windsurf bundle, verifies the local
  `openpe-server` descriptor via `GET /v1/info`, backs up the bundle and
  `product.json`, injects the built `inject/dist/inject.js` payload with a
  `globalThis.__openpe` bootstrap, removes the bundle checksum entry, and
  re-signs the app on macOS.
- The injected payload watches the Cascade toolbar, adds the openPE logo button,
  calls local `POST /v1/prompt-enhance`, previews original/enhanced text,
  and best-effort writes the enhanced prompt back to the Cascade input.
- This remains experimental and off by default because it modifies the
  Windsurf application bundle and depends on private Cascade DOM selectors.

The work is tracked under `docs/development/2026-05-22-windsurf-patch-installer.md`
in the main openPE repository.

## Usage

```bash
# 1. Start openpe-server with lifecycle + CORS before installing.
OPENPE_SERVER_TOKEN="<stable-64-hex-token>" \
OPENPE_SERVER_LIFECYCLE_ENABLED=true \
OPENPE_SERVER_CORS_ORIGINS=null,app://windsurf \
openpe-server

# 2. Build the injected payload when changing TypeScript sources.
cd extensions/openpe-windsurf-patch/inject
npm install
npm run build

# 3. Install from the subproject root.
cd ..
python3 -m installer doctor
python3 -m installer status
python3 -m installer install --i-accept-experimental-risk
```

For repeatable daily use, do not regenerate `OPENPE_SERVER_TOKEN` on every
server launch. Generate it once, store it in your user-level openPE env file
or shell profile, and reuse it so the already-installed button remains
authenticated after `openpe-server` restarts. If the browser console reports
a CORS error, inspect the request `Origin` in Windsurf DevTools and add that
exact origin to `OPENPE_SERVER_CORS_ORIGINS`.

Restart Windsurf after install. The openPE logo button should appear next to the
Cascade submit controls. Click it, review the enhanced prompt, then use
`Apply to input`. If Windsurf changed its private DOM and the button does
not appear, see `inject/README.md` and widen `findCascadeToolbar()` /
`SUBMIT_BUTTON_SELECTORS`, rebuild `inject/dist/inject.js`, then re-run
`install`.

All subcommands accept `--help`. `status` and `doctor --app-dir <path>`
also report `button config`. A `stale` result means the token or base URL
embedded at install time no longer matches the current server descriptor;
restart `openpe-server` with the same `OPENPE_SERVER_TOKEN` or re-run
`install` to refresh the bundle bootstrap.

For the P3 descriptor-read spike, run install with `--fs-probe` after
starting `openpe-server` with `OPENPE_SERVER_LIFECYCLE_ENABLED=true`.
Restart Windsurf, click the openPE button, and inspect DevTools for
`[openpe-fs-probe]` logs. The probe reports only non-secret descriptor
metadata and whether the renderer can read the 0600 descriptor via Node
`fs`; it does not change token transport yet.

## Testing

```bash
python3 -m unittest discover -v tests
```

No third-party Python dependencies are required.

## Architecture

See [`docs/architecture.md`](docs/architecture.md). The high level: the
installer implements the `Injector` and `BundlePatcher` contracts defined
in the main openPE repository's `internal/integration/` package, so the
same patterns can be reused for future IDEs (Cursor, VS Code Composer)
by creating sibling subprojects.

## License

MIT — see [`LICENSE`](LICENSE).
