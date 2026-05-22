# openpe-windsurf-patch

> **⚠️ EXPERIMENTAL — USER ASSUMES ALL RISK ⚠️**
>
> This subproject **patches the Windsurf IDE Electron bundle in place** to
> inject a ✨ Enhance button into the Cascade chat input toolbar. It is
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
  `MutationObserver`, adds a ✨ button next to Submit, opens a 3-page modal
  on click, calls **your local** `openpe-server` over HTTP (loopback only),
  and writes the enhanced prompt back into the Cascade input field.
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
- A running `openpe-server` with `OPENPE_SERVER_LIFECYCLE_ENABLED=true`
  (the installer reads `~/.config/openpe/server.json` to discover the
  bearer token and base URL)

## Status

- **Phase 1 (current)**: Project skeleton. CLI subcommands `status`,
  `install`, `uninstall`, `doctor` are stubs that explain the EULA risk
  and exit. No bundle is touched.
- **Phase 2**: Path resolution + handshake with openpe-server.
- **Phase 3**: Bundle patcher (backup → marker inject → checksum bypass →
  optional codesign).
- **Phase 4**: inject.js TypeScript build (observer → button → dialog →
  client → input refill).
- **Phase 5**: Cross-platform double-click installers + end-to-end fixture
  tests.

The work is tracked under `docs/development/2026-05-22-windsurf-patch-installer.md`
in the main openPE repository.

## Usage (Phase 1 stubs)

```bash
# From the subproject root:
python3 -m installer status      # show what would be done
python3 -m installer install     # currently exits with EULA prompt
python3 -m installer uninstall   # currently a no-op (no patch to revert)
python3 -m installer doctor      # environment self-check
```

All subcommands accept `--help`.

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
