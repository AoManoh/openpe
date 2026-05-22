/**
 * openPE Windsurf inject payload — IIFE entry.
 *
 * Bundled by esbuild into `dist/inject.js` and appended verbatim to
 * Windsurf's `workbench.desktop.main.js` between the OPENPE-INJECT markers.
 *
 * Design constraints:
 *   - Single-instance guard via `__openpeInjected` flag.
 *   - No top-level side effects beyond invoking `boot()`.
 *   - Failures are logged and swallowed; we never let an injection bug
 *     crash the host IDE.
 */

import { getConfig } from "./auth.js";
import { runFilesystemProbe } from "./fs_probe.js";
import { ensureStyles } from "./styles.js";
import { startObserver } from "./observer.js";

declare global {
  // Marker the installer writes onto globalThis to expose the bearer
  // token + base URL. Kept loose so future protocol revisions can extend
  // without breaking the type signature.
  // eslint-disable-next-line @typescript-eslint/consistent-type-definitions
  interface Window {
    __openpe?: {
      baseUrl?: string;
      token?: string;
      descriptorPath?: string;
      fsProbe?: boolean;
      version?: string;
    };
    __openpeInjected?: boolean;
  }
}

function log(message: string, ...rest: unknown[]): void {
  // eslint-disable-next-line no-console
  console.info(`[openpe-inject] ${message}`, ...rest);
}

function warn(message: string, err?: unknown): void {
  // eslint-disable-next-line no-console
  console.warn(`[openpe-inject] ${message}`, err);
}

function boot(): void {
  if (typeof window === "undefined") {
    return;
  }
  if (window.__openpeInjected) {
    warn("already injected; skipping");
    return;
  }
  window.__openpeInjected = true;

  const config = getConfig();
  runFilesystemProbe(config);
  if (!config.baseUrl || !config.token) {
    warn(
      "globalThis.__openpe missing baseUrl/token; installer should have set these before injection",
    );
    return;
  }

  try {
    ensureStyles();
    startObserver(config);
    log("ready", { baseUrl: config.baseUrl, version: config.version ?? "unknown" });
  } catch (err) {
    warn("boot failed", err);
  }
}

boot();
