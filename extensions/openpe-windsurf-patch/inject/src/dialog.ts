/**
 * Auto-enhance Cascade input.
 *
 * Click → read the Cascade input text directly from the nearest Lexical
 * editor (or textarea fallback) → POST /v1/prompt-enhance → replace the
 * input contents in place. No modal, no manual re-typing, no Apply step.
 *
 * Status is surfaced via a small toast in the bottom-right corner so the
 * user can tell when enhance is in flight, when it succeeded, or when it
 * fell back to the clipboard. Toast styling is scoped to ``.openpe-toast``
 * classes so it can never interact with the openPE logo button styles.
 *
 * Lexical / Slate / ProseMirror editors only sync their internal state in
 * response to ``beforeinput`` / ``input`` events. ``document.execCommand
 * ('insertText', false, text)`` is the only cross-implementation way to
 * dispatch those events; the API is deprecated but Chromium still
 * supports it and it's exactly what other Cascade integrations rely on.
 */

import type { OpenpeConfig } from "./auth.js";
import type { HistoryMeta, HistorySource } from "./cascade_context.js";
import type { EnhanceResponse } from "./client.js";
import { ClientError, enhancePrompt } from "./client.js";
import { editorTextsEqual, normalizeEditorText } from "./editor_state.js";
import { historyForEnhance } from "./history_policy.js";

const TOAST_CONTAINER_ID = "openpe-toast-container";

// Preview budgets used only by ``describeLastEnhance``. Wider than the
// 80-char ``describeHistory`` previews because the express purpose of
// describeLastEnhance is to let a dev verify that ambiguous references
// (e.g. "方案1"/"方案A") in the user's recent turns actually carry their
// definitions into the enhance request. 80 chars often slices the
// definition; 200 / 400 fit a typical turn-level definition or short
// rewritten prompt. Still bounded so a stray request body cannot leak
// a giant document.
const PREVIEW_MAX_HISTORY_CHARS = 200;
const PREVIEW_MAX_PROMPT_CHARS = 400;

type ToastLevel = "info" | "ok" | "warn" | "error";

interface ToastOptions {
  persist?: boolean;
  autoDismiss?: number;
}

/**
 * Per-message shape view captured in ``LastEnhanceSnapshot.request``.
 * Contains a bounded text preview (≤ ``PREVIEW_MAX_HISTORY_CHARS``)
 * plus the raw character count so the inspector can tell how much of
 * the message body was clipped.
 */
export interface LastEnhanceMessagePreview {
  role: string;
  contentChars: number;
  preview: string;
  previewTruncated: boolean;
}

/**
 * Trimmed, dev/test-friendly mirror of the ``Response.Metadata`` the
 * Go enhancer sends back. The names mirror
 * ``internal/enhancer/types.go`` field tags (snake_case → camelCase).
 * Anything the server might add in the future is silently dropped by
 * the extractor so a schema change cannot crash the inspector.
 */
export interface LastEnhanceMetadata {
  usedContext?: string[];
  sections?: Array<{ name: string; length: number; truncated: boolean }>;
  provider?: string;
  model?: string;
}

/**
 * Snapshot of the most recent openPE button click. Recorded after every
 * ``runAutoEnhance`` invocation (both success and failure) so
 * ``window.__openpeDebug.describeLastEnhance()`` can show what was sent,
 * what came back, and how the server attributed the result.
 *
 * Recorded unconditionally — the privacy gate is at the
 * ``__openpeDebug`` namespace level in ``index.ts``, which only
 * attaches when the installer was run with ``--debug``. The snapshot
 * itself stays in module-private memory until then.
 */
export interface LastEnhanceSnapshot {
  at: number;
  request: {
    source: HistorySource;
    messageCount: number;
    totalChars: number;
    roles: { user: number; assistant: number };
    messagePreviews: LastEnhanceMessagePreview[];
    originalPromptChars: number;
    originalPromptPreview: string;
    originalPromptTruncated: boolean;
  };
  response: {
    ok: boolean;
    enhancedPromptChars: number;
    enhancedPromptPreview: string;
    enhancedPromptTruncated: boolean;
    metadata: LastEnhanceMetadata | null;
    error: string | null;
  };
}

const REQUEST_DEADLINE_MS = 30_000;

interface EditorFlight {
  controller: AbortController;
  revision: number;
}

// 每个 editor 独立 single-flight；一个慢 editor 不再锁死其它输入框。
const flights = new WeakMap<HTMLElement, EditorFlight>();
const activeControllers = new Set<AbortController>();
const editorRevisions = new WeakMap<HTMLElement, number>();
const trackedEditors = new WeakSet<HTMLElement>();
const composingEditors = new WeakSet<HTMLElement>();
const internalWriteEditors = new WeakSet<HTMLElement>();
let pagehideHookInstalled = false;
let lastEnhanceSnapshot: LastEnhanceSnapshot | null = null;

/**
 * Return the most recent enhance click's request/response shape, or
 * ``null`` if the button has not been clicked since boot. Intended for
 * dev/test use via ``window.__openpeDebug.describeLastEnhance``.
 */
export function getLastEnhanceSnapshot(): LastEnhanceSnapshot | null {
  return lastEnhanceSnapshot;
}

export async function runAutoEnhance(
  config: OpenpeConfig,
  editorEl: HTMLElement,
): Promise<void> {
  trackEditorRevision(editorEl);
  if (flights.has(editorEl)) {
    showToast("openPE: this input is already being enhanced.", "info", {
      autoDismiss: 2500,
    });
    return;
  }
  const original = readEditorText(editorEl);
  if (!original.trim()) {
    showToast("openPE: input is empty — type a prompt first.", "warn");
    return;
  }
  installPagehideAbortHook();
  const controller = new AbortController();
  const revision = currentRevision(editorEl);
  flights.set(editorEl, { controller, revision });
  activeControllers.add(controller);
  const deadline = setTimeout(() => {
    controller.abort(new DOMException("openPE request timed out", "TimeoutError"));
  }, REQUEST_DEADLINE_MS);
  const dismissLoading = showToast("openPE: enhancing…", "info", {
    persist: true,
  });
  // renderer 内没有可证明的 editor/chat ↔ trajectory 身份映射。旧逻辑
  // 猜“最近 trajectory”并把全局缓存交给任意 editor，新 chat 首次增强会
  // 泄漏旧 chat 明文。无法证明身份时按隐私契约 fail closed：collector
  // 仍可供 debug 形态诊断，但 enhancement wire 永不携带这份未绑定历史。
  const historyMeta = historyForEnhance();
  try {
    const resp = await enhancePrompt(
      config,
      {
        prompt: original,
        client: config.client,
        mode: config.mode,
        history: historyMeta.messages,
        // Consumer-layer token budget; absent → omit on the wire (server
        // default = no shrinking). When the installer was run with
        // ``--max-context-tokens N`` or with ``OPENPE_MAX_CONTEXT_TOKENS``
        // set, ``getConfig()`` surfaces the positive int here and we
        // forward it via ``options.max_context_tokens`` so the server
        // shrinks retrieval / history sections to fit.
        ...(config.maxContextTokens
          ? { options: { max_context_tokens: config.maxContextTokens } }
          : {}),
      },
      controller.signal,
    );
    // Snapshot BEFORE writing back / showing toasts so the inspection
    // surface is populated even if the editor write path or the toast
    // helper throws. Success is defined by whether
    // ``enhancePrompt`` resolved (i.e. server returned 2xx + parseable
    // JSON); empty-prompt and editor-write failures are still ``ok``
    // from the wire perspective and surface that way to the inspector.
    recordSnapshot(historyMeta, original, { kind: "ok", resp });
    dismissLoading();
    const enhanced = (resp.enhanced_prompt ?? "").trim();
    if (!enhanced) {
      showToast("openPE: server returned empty text.", "error");
      return;
    }
    if (resp.warnings && resp.warnings.length > 0) {
      const copyOk = await copyToClipboard(enhanced);
      const warningSummary = resp.warnings.slice(0, 3).join("; ").slice(0, 240);
      showToast(
        copyOk
          ? `openPE: safety warning — copied for review (${warningSummary})`
          : `openPE: safety warning — clipboard failed (${warningSummary})`,
        "warn",
        { autoDismiss: 8000 },
      );
      return;
    }
    if (!editorStateMatches(editorEl, original, revision)) {
      const copyOk = await copyToClipboard(enhanced);
      showToast(
        copyOk
          ? "openPE: input changed while enhancing — copied for review instead."
          : "openPE: input changed while enhancing and clipboard failed.",
        "warn",
        { autoDismiss: 6000 },
      );
      return;
    }
    const writeOk = await writeEditorText(
      editorEl,
      enhanced,
      original,
      revision,
      controller.signal,
    );
    if (writeOk) {
      showToast("openPE: enhanced ✓  (Ctrl+Z to undo)", "ok", {
        autoDismiss: 3500,
      });
      return;
    }
    if (controller.signal.aborted) return;
    const copyOk = await copyToClipboard(enhanced);
    showToast(
      copyOk
        ? "openPE: couldn't write to Cascade — copied to clipboard, paste manually."
        : "openPE: couldn't write to Cascade and clipboard failed too.",
      "warn",
      { autoDismiss: 6000 },
    );
  } catch (err) {
    recordSnapshot(historyMeta, original, { kind: "error", err });
    dismissLoading();
    showToast("openPE: " + describeError(err), "error", { autoDismiss: 6000 });
  } finally {
    clearTimeout(deadline);
    dismissLoading();
    activeControllers.delete(controller);
    if (flights.get(editorEl)?.controller === controller) {
      flights.delete(editorEl);
    }
  }
}

// ---------------------------------------------------------------------------
// LastEnhance snapshot — internal helpers
// ---------------------------------------------------------------------------

function previewText(
  text: string,
  max: number,
): { preview: string; truncated: boolean } {
  if (text.length <= max) {
    return { preview: text, truncated: false };
  }
  return { preview: text.slice(0, max) + "\u2026", truncated: true };
}

/**
 * Defensive ``Response.Metadata`` extractor. The wire schema is owned
 * by the Go enhancer (``internal/enhancer/types.go``); we mirror what
 * the inspector cares about (used_context tags, per-section length /
 * truncated flags, provider, model) and silently drop anything else so
 * a future server field cannot crash the inspector. Returns ``null``
 * if the metadata block is absent or not an object.
 */
function extractMetadata(
  raw: Record<string, unknown> | undefined,
): LastEnhanceMetadata | null {
  if (!raw || typeof raw !== "object") return null;
  const out: LastEnhanceMetadata = {};
  const usedCtxRaw = (raw as { used_context?: unknown }).used_context;
  if (Array.isArray(usedCtxRaw)) {
    out.usedContext = usedCtxRaw.filter(
      (x): x is string => typeof x === "string",
    );
  }
  const sectionsRaw = (raw as { sections?: unknown }).sections;
  if (Array.isArray(sectionsRaw)) {
    const sections: NonNullable<LastEnhanceMetadata["sections"]> = [];
    for (const s of sectionsRaw) {
      if (typeof s !== "object" || s === null) continue;
      const obj = s as Record<string, unknown>;
      sections.push({
        name: typeof obj.name === "string" ? obj.name : "unknown",
        length: typeof obj.length === "number" ? obj.length : 0,
        truncated: obj.truncated === true,
      });
    }
    out.sections = sections;
  }
  const providerRaw = (raw as { provider?: unknown }).provider;
  if (typeof providerRaw === "string") out.provider = providerRaw;
  const modelRaw = (raw as { model?: unknown }).model;
  if (typeof modelRaw === "string") out.model = modelRaw;
  return out;
}

type EnhanceOutcome =
  | { kind: "ok"; resp: EnhanceResponse }
  | { kind: "error"; err: unknown };

function recordSnapshot(
  historyMeta: HistoryMeta,
  originalPrompt: string,
  outcome: EnhanceOutcome,
): void {
  const originalPreview = previewText(originalPrompt, PREVIEW_MAX_PROMPT_CHARS);
  const messagePreviews: LastEnhanceMessagePreview[] = historyMeta.messages.map(
    (m) => {
      const p = previewText(m.content, PREVIEW_MAX_HISTORY_CHARS);
      return {
        role: m.role,
        contentChars: m.content.length,
        preview: p.preview,
        previewTruncated: p.truncated,
      };
    },
  );
  const request: LastEnhanceSnapshot["request"] = {
    source: historyMeta.source,
    messageCount: historyMeta.messages.length,
    totalChars: historyMeta.totalChars,
    roles: historyMeta.roles,
    messagePreviews,
    originalPromptChars: originalPrompt.length,
    originalPromptPreview: originalPreview.preview,
    originalPromptTruncated: originalPreview.truncated,
  };
  if (outcome.kind === "ok") {
    const enhanced = (outcome.resp.enhanced_prompt ?? "").trim();
    const enhancedPreview = previewText(enhanced, PREVIEW_MAX_PROMPT_CHARS);
    lastEnhanceSnapshot = {
      at: Date.now(),
      request,
      response: {
        ok: true,
        enhancedPromptChars: enhanced.length,
        enhancedPromptPreview: enhancedPreview.preview,
        enhancedPromptTruncated: enhancedPreview.truncated,
        metadata: extractMetadata(outcome.resp.metadata),
        error: null,
      },
    };
  } else {
    lastEnhanceSnapshot = {
      at: Date.now(),
      request,
      response: {
        ok: false,
        enhancedPromptChars: 0,
        enhancedPromptPreview: "",
        enhancedPromptTruncated: false,
        metadata: null,
        error: describeError(outcome.err),
      },
    };
  }
}

function readEditorText(el: HTMLElement): string {
  if (el instanceof HTMLTextAreaElement) {
    return el.value ?? "";
  }
  // Lexical / Slate / ProseMirror render into contenteditable elements;
  // innerText preserves the visual line breaks the user actually sees.
  return el.innerText ?? el.textContent ?? "";
}

function currentRevision(el: HTMLElement): number {
  return editorRevisions.get(el) ?? 0;
}

function trackEditorRevision(el: HTMLElement): void {
  if (trackedEditors.has(el)) return;
  trackedEditors.add(el);
  editorRevisions.set(el, 0);
  const bump = (event: Event): void => {
    if (event.isTrusted && !internalWriteEditors.has(el)) {
      editorRevisions.set(el, currentRevision(el) + 1);
    }
  };
  el.addEventListener("beforeinput", bump);
  el.addEventListener("input", bump);
  el.addEventListener("compositionstart", (event) => {
    if (!event.isTrusted || internalWriteEditors.has(el)) return;
    composingEditors.add(el);
    editorRevisions.set(el, currentRevision(el) + 1);
  });
  el.addEventListener("compositionend", (event) => {
    if (!event.isTrusted || internalWriteEditors.has(el)) return;
    composingEditors.delete(el);
    editorRevisions.set(el, currentRevision(el) + 1);
  });
}

function editorStateMatches(
  el: HTMLElement,
  expected: string,
  revision: number,
): boolean {
  return (
    el.isConnected &&
    !composingEditors.has(el) &&
    currentRevision(el) === revision &&
    editorTextsEqual(el instanceof HTMLTextAreaElement, readEditorText(el), expected)
  );
}

async function writeEditorText(
  el: HTMLElement,
  text: string,
  expected: string,
  revision: number,
  signal: AbortSignal,
): Promise<boolean> {
  if (signal.aborted || !editorStateMatches(el, expected, revision)) {
    return false;
  }
  // Path 1: plain HTMLTextAreaElement (older Windsurf builds / VS Code
  // derivatives). Use the prototype value setter so React notices.
  if (el instanceof HTMLTextAreaElement) {
    try {
      const setter = Object.getOwnPropertyDescriptor(
        HTMLTextAreaElement.prototype,
        "value",
      )?.set;
      internalWriteEditors.add(el);
      try {
        if (setter) {
          setter.call(el, text);
        } else {
          el.value = text;
        }
        el.dispatchEvent(new Event("input", { bubbles: true }));
        el.dispatchEvent(new Event("change", { bubbles: true }));
      } finally {
        internalWriteEditors.delete(el);
      }
      el.focus({ preventScroll: false });
      return el.value === text;
    } catch {
      return false;
    }
  }
  // Path 2: contenteditable Lexical / Slate / ProseMirror.
  //
  // execCommand('insertText') is misleading on Lexical: it returns true
  // even when Lexical's beforeinput handler swallows the event without
  // mutating state. The reliable pattern (also used by WSE on the same
  // Windsurf build) is to dispatch the beforeinput events Lexical's
  // command pipeline listens to, wait for its async reconcile, then
  // verify by reading the editor text back. If the dispatch didn't take,
  // fall back to execCommand under a fresh selection; if that also
  // doesn't change the text, the caller falls back to clipboard.
  try {
    el.focus({ preventScroll: false });
    // One animation frame so focus actually lands on the editor before
    // we start dispatching beforeinput events into it.
    await rafTick();
    if (signal.aborted || !editorStateMatches(el, expected, revision)) {
      return false;
    }
    el.focus({ preventScroll: false });
    selectAllRange(el);

    const userRevisionBeforeDispatch = currentRevision(el);
    internalWriteEditors.add(el);
    try {
      el.dispatchEvent(
        new InputEvent("beforeinput", {
          bubbles: true,
          cancelable: true,
          inputType: "insertText",
          data: text,
        }),
      );
    } finally {
      internalWriteEditors.delete(el);
    }

    await delay(50);
    if (
      signal.aborted ||
      !el.isConnected ||
      composingEditors.has(el) ||
      currentRevision(el) !== userRevisionBeforeDispatch
    ) {
      return false;
    }
    if (verifyEditorContains(el, text)) return true;
    if (!editorTextsEqual(false, readEditorText(el), expected)) {
      return false;
    }

    selectAllRange(el);
    const userRevisionBeforeFallback = currentRevision(el);
    internalWriteEditors.add(el);
    try {
      document.execCommand("insertText", false, text);
    } finally {
      internalWriteEditors.delete(el);
    }
    await delay(50);
    return (
      !signal.aborted &&
      el.isConnected &&
      !composingEditors.has(el) &&
      currentRevision(el) === userRevisionBeforeFallback &&
      verifyEditorContains(el, text)
    );
  } catch {
    return false;
  }
}

function selectAllRange(el: HTMLElement): void {
  const sel = window.getSelection();
  if (!sel) return;
  const range = document.createRange();
  range.selectNodeContents(el);
  sel.removeAllRanges();
  sel.addRange(range);
}

function verifyEditorContains(el: HTMLElement, text: string): boolean {
  return normalizeEditorText(readEditorText(el)) === normalizeEditorText(text);
}

function rafTick(): Promise<void> {
  return new Promise((resolve) => {
    if (typeof requestAnimationFrame === "function") {
      requestAnimationFrame(() => resolve());
    } else {
      setTimeout(resolve, 16);
    }
  });
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function installPagehideAbortHook(): void {
  if (pagehideHookInstalled || typeof window === "undefined") return;
  pagehideHookInstalled = true;
  window.addEventListener("pagehide", () => {
    for (const controller of activeControllers) {
      controller.abort(new DOMException("page hidden", "AbortError"));
    }
    activeControllers.clear();
  });
}

async function copyToClipboard(text: string): Promise<boolean> {
  try {
    if (
      typeof navigator !== "undefined" &&
      navigator.clipboard &&
      typeof navigator.clipboard.writeText === "function"
    ) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* fall through */
  }
  return false;
}

function describeError(err: unknown): string {
  if (err instanceof ClientError) {
    return err.status ? `${err.message} (HTTP ${err.status})` : err.message;
  }
  if (err instanceof DOMException && err.name === "TimeoutError") {
    return "request timed out";
  }
  if (err instanceof DOMException && err.name === "AbortError") {
    return "cancelled";
  }
  if (err instanceof Error) {
    return err.message || "unknown error";
  }
  return "unknown error";
}

function ensureToastContainer(): HTMLElement {
  const existing = document.getElementById(TOAST_CONTAINER_ID);
  if (existing) return existing;
  const c = document.createElement("div");
  c.id = TOAST_CONTAINER_ID;
  c.className = "openpe-toast-container";
  c.setAttribute("aria-live", "polite");
  document.body.appendChild(c);
  return c;
}

function showToast(
  message: string,
  level: ToastLevel = "info",
  opts: ToastOptions = {},
): () => void {
  const container = ensureToastContainer();
  const el = document.createElement("div");
  el.className = "openpe-toast openpe-toast-" + level;
  el.textContent = message;
  container.appendChild(el);
  let dismissed = false;
  const dismiss = (): void => {
    if (dismissed) return;
    dismissed = true;
    el.remove();
  };
  if (!opts.persist) {
    const ms = opts.autoDismiss ?? 4000;
    setTimeout(dismiss, ms);
  }
  return dismiss;
}
