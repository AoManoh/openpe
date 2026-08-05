/**
 * Auth + config plumbing: read the descriptor metadata the installer
 * embeds onto `globalThis.__openpe` before invoking the IIFE.
 *
 * The shape mirrors openPE's `LocalServerDescriptor`; we only pull the
 * fields the UI actually needs to call /v1/prompt-enhance.
 */

export type HistorySource = "none" | "legacy_trajectory";

const DEVIN_EXACT_PRODUCT_COMMIT = "0d4bf12ed4a7597cb8ae9016fe8474468aad98a2";
const TRANSACTION_ID_PATTERN = /^\d{8}T\d{12}Z-[0-9a-f]{8}$/;

export type CredentialMode = "bearer" | "preload-capability-v1";

export interface OpenpeConfig {
  baseUrl: string;
  token: string;
  credentialMode: CredentialMode;
  hostProfileId: string;
  productCommit: string;
  transactionId: string;
  client: string;
  mode: string;
  historySource: HistorySource;
  runtimeEnabled: boolean;
  descriptorPath?: string;
  fsProbe: boolean;
  /**
   * Dev/test diagnostic gate. When ``true``, inject modules may emit
   * verbose ``console.warn`` traces and attach a read-only
   * ``globalThis.__openpeDebug`` namespace exposing shape-only views of
   * internal state (history length, role distribution, char counts,
   * short content previews — never full message bodies). Default false
   * so production installs stay silent regardless of what the inject
   * code internally collects. Installer surfaces this via
   * ``installer install --debug``.
   */
  debug: boolean;
  version?: string;
  /**
   * Consumer-layer token budget. Snapshotted into the bundle by the
   * installer (``--max-context-tokens N`` or
   * ``OPENPE_MAX_CONTEXT_TOKENS`` env). When set to a positive integer
   * the inject layer forwards it to ``POST /v1/prompt-enhance`` via
   * ``options.max_context_tokens``, mirroring the hook adapter path and
   * the Go ``enhancer.Request.Options.MaxContextTokens`` (json:
   * ``max_context_tokens``).
   *
   * Resolution rules (mirrored from
   * ``installer/__main__.py::_resolve_max_context_tokens``):
   *
   * - ``undefined`` → omit the wire field entirely (server uses 0 = no
   *   shrinking; matches Go ``omitempty``).
   * - positive int → forward verbatim; server shrinks retrieval /
   *   history sections to fit (required sections always survive).
   *
   * Not configurable at runtime; re-run installer to change.
   *
   * Note: this is NOT the cascade history collector budget
   * (32 msg / 6000 char/msg / 80000 char total) — those live in
   * ``cascade_context.ts::DEFAULT_HISTORY_BUDGET`` and are intentionally
   * hard-coded empirical tuning, not user-facing knobs.
   */
  maxContextTokens?: number;
}

function ownValue(raw: object, key: string): unknown {
  return Object.prototype.hasOwnProperty.call(raw, key)
    ? (raw as Record<string, unknown>)[key]
    : undefined;
}

export function getConfig(): OpenpeConfig {
  let candidate: unknown = {};
  if (typeof window !== "undefined") {
    const descriptor = Object.getOwnPropertyDescriptor(window, "__openpe");
    if (descriptor && "value" in descriptor && descriptor.configurable !== false) {
      candidate = descriptor.value;
      // Bootstrap 只作为一次性交接槽：读取后立即删除，token（legacy）或
      // 非敏感 exact 配置只保留在模块闭包中。同 realm 脚本不再能在启动后
      // 读取或篡改 globalThis.__openpe；不可删除的属性直接 fail closed。
      delete window.__openpe;
    }
  }
  const raw = typeof candidate === "object" && candidate !== null ? candidate : {};
  const profileValue = ownValue(raw, "hostProfileId");
  const clientValue = ownValue(raw, "client");
  const modeValue = ownValue(raw, "mode");
  const historyValue = ownValue(raw, "historySource");
  const productCommitValue = ownValue(raw, "productCommit");
  const transactionIdValue = ownValue(raw, "transactionId");
  const credentialValue = ownValue(raw, "credentialMode");
  const hostProfileId = typeof profileValue === "string" ? profileValue : "";
  const client = typeof clientValue === "string" ? clientValue.trim() : "";
  const mode = typeof modeValue === "string" ? modeValue.trim() : "";
  const credentialMode: CredentialMode =
    credentialValue === "preload-capability-v1"
      ? "preload-capability-v1"
      : "bearer";
  const runtimeEnabled =
    (hostProfileId === "windsurf-legacy" &&
      client === "windsurf" &&
      mode === "cascade" &&
      credentialMode === "bearer") ||
    (hostProfileId === "devin-desktop" &&
      client === "devin" &&
      mode === "agent" &&
      credentialMode === "preload-capability-v1" &&
      productCommitValue === DEVIN_EXACT_PRODUCT_COMMIT &&
      typeof transactionIdValue === "string" &&
      TRANSACTION_ID_PATTERN.test(transactionIdValue));
  const historySource: HistorySource =
    hostProfileId === "windsurf-legacy" &&
    runtimeEnabled &&
    historyValue === "legacy_trajectory"
      ? "legacy_trajectory"
      : "none";
  const maxContextTokens = ownValue(raw, "maxContextTokens");
  const mct =
    typeof maxContextTokens === "number" &&
    Number.isSafeInteger(maxContextTokens) &&
    maxContextTokens > 0
      ? maxContextTokens
      : undefined;
  const baseUrl = ownValue(raw, "baseUrl");
  const token = ownValue(raw, "token");
  const descriptorPath = ownValue(raw, "descriptorPath");
  const version = ownValue(raw, "version");
  return {
    baseUrl: typeof baseUrl === "string" ? baseUrl.replace(/\/+$/, "") : "",
    token: typeof token === "string" ? token : "",
    credentialMode,
    hostProfileId,
    productCommit:
      typeof productCommitValue === "string" ? productCommitValue : "",
    transactionId:
      typeof transactionIdValue === "string" ? transactionIdValue : "",
    client,
    mode,
    historySource,
    runtimeEnabled,
    descriptorPath:
      typeof descriptorPath === "string" ? descriptorPath : undefined,
    fsProbe: runtimeEnabled && ownValue(raw, "fsProbe") === true,
    debug: runtimeEnabled && ownValue(raw, "debug") === true,
    version: typeof version === "string" ? version : undefined,
    maxContextTokens: mct,
  };
}
