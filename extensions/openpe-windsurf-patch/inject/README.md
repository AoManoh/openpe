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
| `src/index.ts`    | IIFE entry; single-instance guard; wires observer + ✨ button. |
| `src/auth.ts`     | Reads `globalThis.__openpe` (written by the installer) for the bearer token and base URL. |
| `src/client.ts`   | `fetch` against the local `POST /v1/prompt-enhance`. |
| `src/observer.ts` | `MutationObserver` that locates the Cascade input toolbar and dispatches injection. |
| `src/button.ts`   | ✨ button DOM creation; styling delegated to `styles.ts`. |
| `src/dialog.ts`   | Three-page modal (config → loading → diff). |
| `src/styles.ts`   | CSS variables that respect `var(--vscode-*)` theme tokens. |

## Real-host DOM caveat

The Cascade chat input toolbar is private Windsurf DOM. `observer.ts`
ships a list of best-effort selectors and stops at the first match; if
the ✨ button does not appear after install + Windsurf restart, the
fastest fix is to widen the selectors in `findCascadeToolbar()` and
rebuild.

No telemetry, no third-party network calls. Every fetch goes to
`base_url` from the descriptor, which the installer guarantees points
at `127.0.0.1:<port>`.
