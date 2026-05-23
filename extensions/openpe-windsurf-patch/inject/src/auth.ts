/**
 * Auth + config plumbing: read the descriptor metadata the installer
 * embeds onto `globalThis.__openpe` before invoking the IIFE.
 *
 * The shape mirrors openPE's `LocalServerDescriptor`; we only pull the
 * fields the UI actually needs to call /v1/prompt-enhance.
 */

export interface OpenpeConfig {
  baseUrl: string;
  token: string;
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

export function getConfig(): OpenpeConfig {
  const raw =
    typeof window !== "undefined" && window.__openpe ? window.__openpe : {};
  // Accept maxContextTokens only when it's a finite positive number.
  // The installer never emits the field for value 0 (matches Go's
  // omitempty), so seeing a 0 here means a hand-edited bundle — silently
  // ignore it to keep this layer permissive but conservative.
  const mct =
    typeof raw.maxContextTokens === "number" &&
    Number.isFinite(raw.maxContextTokens) &&
    raw.maxContextTokens > 0
      ? raw.maxContextTokens
      : undefined;
  return {
    baseUrl: typeof raw.baseUrl === "string" ? raw.baseUrl.replace(/\/+$/, "") : "",
    token: typeof raw.token === "string" ? raw.token : "",
    descriptorPath:
      typeof raw.descriptorPath === "string" ? raw.descriptorPath : undefined,
    fsProbe: raw.fsProbe === true,
    debug: raw.debug === true,
    version: typeof raw.version === "string" ? raw.version : undefined,
    maxContextTokens: mct,
  };
}
