# openpe-windsurf-inject

TypeScript source for the payload that the openpe-windsurf-patch installer
injects into Windsurf's `workbench.desktop.main.js`. Built as a single
self-contained IIFE so it can be appended verbatim between the
`/* === OPENPE-INJECT-BEGIN === */` markers.

## Building

```bash
cd extensions/openpe-windsurf-patch/inject
npm install
npm run build    # → dist/inject.js
```

The Python installer's `install` subcommand reads `dist/inject.js` and
refuses to proceed (`exit 69`) if it is missing.

## Type-checking

```bash
npm run check    # tsc --noEmit
```

## Layout

| File | Purpose |
|---|---|
| `src/index.ts`    | IIFE entry; single-instance guard; wires observer + openPE logo button. |
| `src/auth.ts`     | Reads `globalThis.__openpe` (written by the installer) for the bearer token and base URL. |
| `src/fs_probe.ts` | Optional P3 diagnostic probe for checking whether the Windsurf renderer can read the local 0600 descriptor via Node `fs`. |
| `src/client.ts`   | `fetch` against the local `POST /v1/prompt-enhance`. |
| `src/observer.ts` | `MutationObserver` that locates the Cascade input toolbar and dispatches injection. |
| `src/button.ts`   | openPE logo button DOM creation; styling delegated to `styles.ts`. The icon is inlined from the VSIX `openpe-icon.svg` design so the patched bundle has no runtime dependency on VSIX files. |
| `src/dialog.ts`   | Three-page modal (config → loading → diff). |
| `src/styles.ts`   | CSS variables that respect `var(--vscode-*)` theme tokens. |

## Real-host DOM caveat

The Cascade chat input toolbar is private Windsurf DOM. `observer.ts`
ships a list of best-effort selectors and stops at the first match; if
the openPE logo button does not appear after install + Windsurf restart, the
fastest fix is to widen the selectors in `findCascadeToolbar()` and
rebuild.

No telemetry, no third-party network calls. Every fetch goes to
`base_url` from the descriptor, which the installer guarantees points
at `127.0.0.1:<port>`.

## P3 filesystem probe

Run the installer with `--fs-probe` when validating whether the injected
Windsurf renderer can read the local server descriptor at runtime:

```bash
python3 -m installer install --i-accept-experimental-risk --fs-probe
```

After restarting Windsurf, click the openPE button and inspect DevTools
for `[openpe-fs-probe]`. The probe logs only descriptor path, byte count,
file mode and schema shape; it must not print the bearer token. This is a
temporary diagnostic gate before replacing install-time token embedding
with descriptor runtime reads.
