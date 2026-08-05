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
import {
  describeCascadeContext,
  describeHistory,
  setDebugEnabled,
  startCascadeContextWatcher,
} from "./cascade_context.js";
import { getLastEnhanceSnapshot } from "./dialog.js";
import { runFilesystemProbe } from "./fs_probe.js";
import { ensureStyles } from "./styles.js";
import { startObserver } from "./observer.js";

declare global {
  // 安装器写入的一次性 bootstrap 交接槽；getConfig 读取后立即删除。
  // Exact Devin 不含 token，只声明 preload capability transport；legacy
  // 兼容路径的 token 也只进入模块闭包，不再常驻 globalThis。
  // eslint-disable-next-line @typescript-eslint/consistent-type-definitions
  interface Window {
    __openpe?: {
      baseUrl?: string;
      token?: string;
      credentialMode?: string;
      descriptorPath?: string;
      fsProbe?: boolean;
      debug?: boolean;
      version?: string;
      hostProfileId?: string;
      productCommit?: string;
      transactionId?: string;
      client?: string;
      mode?: string;
      historySource?: string;
      // Consumer-layer token budget. Snapshotted by the installer from
      // ``--max-context-tokens`` / ``OPENPE_MAX_CONTEXT_TOKENS``;
      // forwarded by the inject layer to ``options.max_context_tokens``
      // on every enhance request. See OpenpeConfig docstring in
      // ``auth.ts`` for the full lifecycle.
      maxContextTokens?: number;
    };
    __openpeBridge?: Readonly<{
      enhance: (requestId: string, body: unknown) => Promise<unknown>;
      cancel: (requestId: string) => void;
    }>;
    __openpeInjected?: boolean;
    /**
     * Read-only dev/test diagnostic namespace. Only attached when the
     * installer was run with ``--debug``. Returns shape-only views of
     * inject internals — never full message bodies, tokens, or
     * Authorization headers. See ``cascade_context.ts.describeHistory``
     * and ``dialog.ts.getLastEnhanceSnapshot`` for the exact preview
     * budgets and privacy contract for each accessor.
     */
    __openpeDebug?: Readonly<{
      describeContext: () => ReturnType<typeof describeCascadeContext>;
      describeHistory: () => ReturnType<typeof describeHistory>;
      describeLastEnhance: () => ReturnType<typeof getLastEnhanceSnapshot>;
    }>;
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
  // 先消费并删除一次性 bootstrap；即使 single-instance marker 已存在，
  // 旧 bearer bootstrap 也不能因早退继续留在 globalThis。
  let config: ReturnType<typeof getConfig>;
  try {
    config = getConfig();
  } catch (err) {
    warn("cannot read bootstrap config; skipping", err);
    return;
  }
  if (window.__openpeInjected) {
    warn("already injected; skipping");
    return;
  }
  try {
    Object.defineProperty(window, "__openpeInjected", {
      value: true,
      writable: false,
      configurable: false,
      enumerable: false,
    });
  } catch (err) {
    warn("cannot claim single-instance marker; skipping", err);
    return;
  }

  if (!config.runtimeEnabled) {
    warn(
      `host profile ${config.hostProfileId || "unknown"} is not runtime-enabled; skipping`,
    );
    return;
  }
  runFilesystemProbe(config);
  const secureBridgeReady =
    config.credentialMode === "preload-capability-v1" &&
    typeof window.__openpeBridge?.enhance === "function" &&
    typeof window.__openpeBridge?.cancel === "function";
  const legacyBearerReady =
    config.credentialMode === "bearer" && !!config.baseUrl && !!config.token;
  if (!secureBridgeReady && !legacyBearerReady) {
    warn("openPE credential transport is unavailable; reinstall the patch");
    return;
  }

  // Wire the dev/test gate BEFORE starting any subsystem so the very
  // first lifecycle event (e.g. an early IDB hook install failure) is
  // visible in debug builds. Production installs (debug=false) flip no
  // flag, so subsystem internals stay completely silent.
  if (config.debug) {
    setDebugEnabled(true);
    try {
      Object.defineProperty(window, "__openpeDebug", {
        value: Object.freeze({
          describeContext: () => describeCascadeContext(),
          describeHistory: () => describeHistory(),
          describeLastEnhance: () => getLastEnhanceSnapshot(),
        }),
        writable: false,
        configurable: false,
        enumerable: false,
      });
    } catch {
      // defineProperty can throw on browsers that already have a
      // matching non-configurable descriptor (e.g. a stale build left
      // one behind); swallow and keep booting.
    }
  }

  try {
    ensureStyles();
    // Cascade context observation is best-effort and entirely client-side:
    // if it fails or the trajectory cache is absent, the inject layer
    // continues to call /v1/prompt-enhance with the prompt only — no
    // hook-side behaviour change, no server-side change.
    if (config.historySource === "legacy_trajectory") {
      startCascadeContextWatcher();
    }
    startObserver(config);
    log("ready", {
      baseUrl: config.baseUrl,
      version: config.version ?? "unknown",
      debug: config.debug,
    });
  } catch (err) {
    warn("boot failed", err);
  }
}

boot();
