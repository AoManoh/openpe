/**
 * Cascade trajectory observer — mirrors the messages of the user's current
 * Cascade conversation so the inject layer can populate
 * ``enhancer.Request.History`` for the local ``POST /v1/prompt-enhance``
 * call.
 *
 * # Why this exists
 *
 * The openPE Windsurf hook adapter (``internal/adapters/windsurf/hook.go``)
 * intentionally does not provide conversation history: unlike the Codex
 * adapter (which reads ``~/.codex/history.jsonl``) or the Claude adapter
 * (which reads the ``transcript_path`` the host hook hands it), Cascade
 * exposes no public file-based session log. The inject layer can fill the
 * gap from inside the renderer because Windsurf caches each workspace's
 * active Cascade trajectory in IndexedDB as a length-prefixed protobuf
 * blob, and that blob contains the user prompts and model replies in
 * plaintext.
 *
 * The reverse-engineered byte layout was first published by
 * windsurf-enhance (WSE) under the GPLv3. The original payload is kept
 * locally as ``extensions/windsurf-enhance-v1.0.1.zip`` (gitignored —
 * unzip into any scratch dir to inspect ``src/inject.js``). We
 * re-implement the parser here in TypeScript so the openPE inject keeps
 * its own dependency surface (no runtime imports of WSE code) while
 * matching the schema WSE validated on Windsurf 1.110.x.
 *
 * # Boundaries
 *
 *   - This module is the ONLY consumer of ``IndexedDB`` and the ONLY
 *     piece of inject that monkey-patches a global prototype
 *     (``IDBObjectStore.prototype.put``). The patch is idempotent,
 *     always calls the original first, and never throws into Windsurf's
 *     own code path.
 *   - All parsing errors are swallowed and surface as an empty history
 *     list. Inject continues to call ``/v1/prompt-enhance`` exactly as it
 *     does today, just without the ``history`` field set. The button
 *     remains useful in every failure mode.
 *   - No state is exposed on ``globalThis``. Callers consume history via
 *     the ``getRecentHistory`` getter so future replacements (e.g. a
 *     direct Windsurf IPC tap) can swap the implementation without
 *     touching consumers.
 *
 * # Trajectory schema (observed)
 *
 * ```
 * trajectory_bin
 *   ├── field 1  trajectory_id        (length-delimited string)
 *   ├── field 2  wrapper              (length-delimited message)
 *   │     ├── field 2  Step           (length-delimited, repeated)
 *   │     │     ├── field 1   step_index                (varint)
 *   │     │     ├── field 4   status                    (enum / varint)
 *   │     │     ├── field 5   metadata                  (message)
 *   │     │     ├── field 19  user_input                (message)
 *   │     │     │     └── field 2  user_message_text    (string) ★
 *   │     │     └── field 20  planner_response          (message)
 *   │     │           ├── field 1  assistant_preview    (string)
 *   │     │           └── field 8  assistant_full_md    (string) ★
 *   │     ├── field 4  step_count                       (varint)
 *   │     └── field 6  cascade_uuid                     (bytes)
 *   └── field 3  trailing_varint                        (varint)
 * ```
 *
 * The schema is undocumented; treat it as best-effort. If Windsurf
 * renames Cortex fields, ``extractTrajectoryMessages`` will return zero
 * messages and the dialog will silently fall back to no-history mode.
 */

export interface CascadeMessage {
  role: "user" | "assistant";
  content: string;
}

/**
 * Where the currently cached history came from. ``'latest_trajectory'``
 * is the existing IndexedDB observer path; future implementations
 * (e.g. a fetch tap into Cascade's outbound LLM request) can add new
 * variants (``'fetch_tap'``, ``'merged'``) without changing the wire
 * format — only ``describeHistory`` exposes this label, and it is
 * gated behind the dev/test ``--debug`` install flag.
 */
export type HistorySource = "latest_trajectory" | "none";

export interface HistoryMeta {
  messages: CascadeMessage[];
  source: HistorySource;
  totalChars: number;
  roles: { user: number; assistant: number };
}

const IDB_DB_NAME = "keyval-store";
const IDB_STORE_NAME = "keyval";
const TRAJ_KEY_PREFIX = "windsurf:cache:cachedActiveTrajectory:";
const REFRESH_THROTTLE_MS = 200;
// 与公开的 collector 契约保持一致：每条最多 6000 字符。旧 parser 先在
// 4000 截断，使后续 6000 预算永远不可达。
const STEP_TEXT_TRUNCATE = 6000;
// History budget — empirically tuned against a 287 KB live trajectory
// observed during the Phase 5 bring-up. The previous 8-msg / 1500-char
// cap was leaving 95%+ of the available trajectory on the floor; a
// Cascade task chain commonly emits 10+ tool rounds, so 8 messages
// often capped at the most-recent 1-2 user turns. New defaults:
//   - 32 messages keeps several full user/assistant turn pairs
//   - 6000 chars/message fits most tool results + diffs without truncation
//   - 80_000 char total cap prevents pathological histories (e.g. a
//     trajectory full of giant code blocks) from dwarfing the actual
//     prompt the user is enhancing. Oldest messages are dropped first
//     so the most recent context always survives.
// Exported so `applyHistoryBudget` callers (and tests) can reference the
// canonical budget shape without recomputing the three integers. The
// patch installer / hook adapter / dialog NEVER overrides these in the
// production wire path — they are pure collector-layer empirical
// tuning, not user-facing knobs. The user-facing token budget is the
// separate consumer-layer knob `enhancer.Request.Options.MaxContextTokens`
// (Go side), see AGENTS.md and README §5 "消费层 vs 采集层".
export const DEFAULT_MAX_MESSAGES = 32;
export const DEFAULT_MAX_CHARS_PER_MESSAGE = 6000;
export const DEFAULT_MAX_TOTAL_CHARS = 80_000;
// describeHistory preview width — small enough to be a sanity peek,
// large enough to recognise the conversation. Never grows beyond this.
const PREVIEW_CHARS = 80;

let started = false;
let cachedMessages: CascadeMessage[] = [];
let lastWrittenKey: string | null = null;
let lastTrajectoryId: string | null = null;
let refreshScheduled = false;
let refreshInFlight = false;
let refreshDirty = false;
let lastError: string | null = null;
let lastRefreshAt = 0;
let historySource: HistorySource = "none";
const openDatabases = new Set<IDBDatabase>();
let pagehideHookInstalled = false;
// Dev/test diagnostic gate. Set by ``setDebugEnabled(true)`` which the
// inject boot calls when ``config.debug === true`` (i.e. when the
// installer was run with ``--debug``). When false the ``dbg()`` helper
// is a no-op so production installs are completely silent. The flag
// only affects logging visibility and the ``__openpeDebug`` namespace;
// it does NOT change what data the watcher collects or what the dialog
// sends on the wire.
let debugEnabled = false;

/**
 * Flip the dev/test diagnostic gate. Intended to be called exactly
 * once from ``index.ts`` during boot when ``config.debug`` is true.
 * Safe to call multiple times (idempotent); safe to call with false to
 * silence runtime if a debug session needs to wind down.
 */
export function setDebugEnabled(value: boolean): void {
  debugEnabled = value === true;
}

// Module-private marker so we never double-wrap the IDB prototype, even
// if a different copy of the inject (e.g. a stale build during local
// dev) tries to install a second hook.
interface PatchedPutHost {
  __openpePutHooked?: boolean;
}

/**
 * Start the trajectory watcher. Idempotent. Safe to call on any boot,
 * including environments without IndexedDB (where it becomes a no-op).
 */
export function startCascadeContextWatcher(): void {
  if (started) return;
  if (typeof indexedDB === "undefined" || typeof IDBObjectStore === "undefined") {
    return;
  }
  started = true;
  installDatabaseCloseHook();
  try {
    installIdbPutHook();
  } catch (err) {
    dbg("install IDB hook failed", err);
  }
  // 不再启动时扫描并猜“最近 trajectory”：未亲眼观察到当前 renderer 的
  // put 就没有身份依据，宁可无历史也不能把旧 chat 明文带入新 chat。
}

/**
 * Return the most recent N message turns as ``enhancer.Message``-shaped
 * objects, oldest first. Each message is truncated to ``maxCharsPerMessage``
 * characters with a trailing "..." indicator if the source was longer.
 *
 * Returns an empty array if the watcher has not seen a trajectory yet,
 * has been disabled, or has hit a parse error.
 *
 * Retained for backwards compatibility with callers that only need the
 * message array. New callers should prefer ``getRecentHistoryWithMeta``
 * which surfaces the source label and totals for diagnostic visibility.
 */
export function getRecentHistory(
  maxMessages: number = DEFAULT_MAX_MESSAGES,
  maxCharsPerMessage: number = DEFAULT_MAX_CHARS_PER_MESSAGE,
): CascadeMessage[] {
  return getRecentHistoryWithMeta(maxMessages, maxCharsPerMessage).messages;
}

/**
 * Same as ``getRecentHistory`` but also returns metadata describing the
 * captured slice: which data source it came from, how many characters
 * total, and the role distribution. The metadata is currently consumed
 * by ``__openpeDebug.describeHistory`` (gated by the installer's
 * ``--debug`` flag) and stays off-wire — the local ``POST
 * /v1/prompt-enhance`` payload still receives just ``messages``.
 *
 * Enforces a global ``DEFAULT_MAX_TOTAL_CHARS`` budget on top of the
 * per-message cap by dropping oldest messages first if the budget
 * would be exceeded. The freshest context always survives.
 */
export function getRecentHistoryWithMeta(
  maxMessages: number = DEFAULT_MAX_MESSAGES,
  maxCharsPerMessage: number = DEFAULT_MAX_CHARS_PER_MESSAGE,
  maxTotalChars: number = DEFAULT_MAX_TOTAL_CHARS,
): HistoryMeta {
  if (!cachedMessages.length) {
    return {
      messages: [],
      source: "none",
      totalChars: 0,
      roles: { user: 0, assistant: 0 },
    };
  }
  const limit = Math.max(0, maxMessages | 0);
  if (limit === 0) {
    // Preserve the historical "source label survives even when caller
    // asked for 0 messages" contract — callers like describeHistory
    // distinguish "no cache" (source=none) from "cache exists but
    // budget=0" (source=latest_trajectory).
    return {
      messages: [],
      source: historySource,
      totalChars: 0,
      roles: { user: 0, assistant: 0 },
    };
  }
  const trimmed = applyHistoryBudget(cachedMessages, {
    maxMessages,
    maxCharsPerMessage,
    maxTotalChars,
  });
  return {
    messages: trimmed.messages,
    source: historySource,
    totalChars: trimmed.totalChars,
    roles: trimmed.roles,
  };
}

/**
 * Pure budget enforcement for a cached message array.
 *
 * Extracted from ``getRecentHistoryWithMeta`` so the two-pass shrinking
 * algorithm (tail-slice + per-message truncate, then total-budget
 * oldest-drop) can be unit-tested without driving the IndexedDB hook,
 * the protobuf parser, or the boot lifecycle. The wrapper above still
 * owns:
 *   - "no cached messages" / "limit=0" early-returns whose source label
 *     depends on module-private state (``cachedMessages``/``historySource``),
 *   - merging the watcher-owned ``source`` field into the returned
 *     ``HistoryMeta`` so consumers always see one consistent label.
 *
 * Returns a plain shape (no ``source``) because the source label is a
 * watcher concern, not a budget concern; tests should not have to
 * fabricate a source to verify budgeting.
 *
 * Defaults to ``DEFAULT_HISTORY_BUDGET`` (the production three constants
 * 32 / 6000 / 80000) so a caller that just wants "enforce the canonical
 * budget on this array" needs no parameters.
 */
export interface HistoryBudget {
  maxMessages: number;
  maxCharsPerMessage: number;
  maxTotalChars: number;
}

export const DEFAULT_HISTORY_BUDGET: HistoryBudget = {
  maxMessages: DEFAULT_MAX_MESSAGES,
  maxCharsPerMessage: DEFAULT_MAX_CHARS_PER_MESSAGE,
  maxTotalChars: DEFAULT_MAX_TOTAL_CHARS,
};

export function applyHistoryBudget(
  messages: readonly CascadeMessage[],
  budget: HistoryBudget = DEFAULT_HISTORY_BUDGET,
): {
  messages: CascadeMessage[];
  totalChars: number;
  roles: { user: number; assistant: number };
} {
  const limit = Math.max(0, budget.maxMessages | 0);
  if (limit === 0 || messages.length === 0) {
    return { messages: [], totalChars: 0, roles: { user: 0, assistant: 0 } };
  }
  const charCap = Math.max(0, budget.maxCharsPerMessage | 0);
  const totalCap = Math.max(0, budget.maxTotalChars | 0);

  // First pass: tail-slice + per-message truncate. Tail-slice keeps the
  // freshest turns; per-message truncate caps individual giants (e.g.
  // tool output dumps) so they cannot consume the whole budget alone.
  const tail = messages.slice(-limit);
  const truncated: CascadeMessage[] = tail.map((m) => ({
    role: m.role,
    content: charCap > 0 ? truncate(m.content, charCap) : m.content,
  }));

  // Second pass: enforce total budget by dropping oldest first. Keep
  // dropping until total <= cap or only one message remains (we always
  // ship at least the most recent turn if there is any), matching the
  // "freshest context always survives" invariant documented on
  // getRecentHistoryWithMeta.
  if (totalCap > 0) {
    let total = truncated.reduce((s, m) => s + m.content.length, 0);
    while (total > totalCap && truncated.length > 1) {
      const dropped = truncated.shift()!;
      total -= dropped.content.length;
    }
  }

  let user = 0;
  let assistant = 0;
  let totalChars = 0;
  for (const m of truncated) {
    totalChars += m.content.length;
    if (m.role === "user") user++;
    else assistant++;
  }

  return { messages: truncated, totalChars, roles: { user, assistant } };
}

/**
 * Diagnostic snapshot for the boot log / debugging. Not consumed by the
 * dialog directly; exists so a developer pasting code into DevTools can
 * confirm the watcher state without poking at module internals.
 */
export function describeCascadeContext(): {
  started: boolean;
  messageCount: number;
  lastRefreshAt: number;
  lastWrittenKey: string | null;
  lastError: string | null;
  historySource: HistorySource;
  debugEnabled: boolean;
} {
  return {
    started,
    messageCount: cachedMessages.length,
    lastRefreshAt,
    lastWrittenKey,
    lastError,
    historySource,
    debugEnabled,
  };
}

/**
 * Privacy-aware history shape view. Returns counts, role distribution,
 * a tiny first/last preview, and the source label — never the full
 * message bodies. Exposed via ``window.__openpeDebug.describeHistory``
 * only when the installer was run with ``--debug``. Safe to call in
 * dev/test consoles; intentionally not gated internally because the
 * caller (``__openpeDebug``) is already gated at the namespace level.
 *
 * The preview is bounded to ``PREVIEW_CHARS`` (80) per side: enough to
 * recognise a conversation, far less than a single tweet, and the same
 * width on every install regardless of message size.
 */
export function describeHistory(): {
  source: HistorySource;
  messageCount: number;
  totalChars: number;
  roles: { user: number; assistant: number };
  oldestPreview: string | null;
  newestPreview: string | null;
  truncatedToBudget: boolean;
} {
  const meta = getRecentHistoryWithMeta();
  const oldest = meta.messages[0];
  const newest = meta.messages[meta.messages.length - 1];
  const previewOf = (m: CascadeMessage | undefined): string | null => {
    if (!m) return null;
    const head = m.content.slice(0, PREVIEW_CHARS);
    return `[${m.role}] ${head}${m.content.length > PREVIEW_CHARS ? "\u2026" : ""}`;
  };
  return {
    source: meta.source,
    messageCount: meta.messages.length,
    totalChars: meta.totalChars,
    roles: meta.roles,
    oldestPreview: previewOf(oldest),
    newestPreview: previewOf(newest),
    truncatedToBudget: cachedMessages.length > meta.messages.length,
  };
}

// ---------------------------------------------------------------------------
// IndexedDB observer
// ---------------------------------------------------------------------------

function installIdbPutHook(): void {
  const proto = IDBObjectStore.prototype as IDBObjectStore & PatchedPutHost;
  if (proto.__openpePutHooked) return;
  const original = proto.put;
  proto.put = function patchedPut(
    this: IDBObjectStore,
    value: unknown,
    key?: IDBValidKey,
  ): IDBRequest {
    const request = original.call(this, value as never, key as never);
    try {
      if (typeof key === "string" && key.startsWith(TRAJ_KEY_PREFIX)) {
        // 一观察到写入意图就先把旧 cache 标为不可信；仅在 transaction
        // complete 后读取，失败/延迟 commit 不会重新发布旧值。
        clearCachedTrajectory();
        lastWrittenKey = key;
        this.transaction.addEventListener("complete", scheduleRefresh, { once: true });
      }
    } catch {
      // Never let the openPE observer interfere with Windsurf's own
      // database writes — silently swallow.
    }
    return request;
  } as typeof proto.put;
  proto.__openpePutHooked = true;
}

function clearCachedTrajectory(): void {
  cachedMessages = [];
  historySource = "none";
  lastTrajectoryId = null;
}

function scheduleRefresh(): void {
  if (refreshScheduled) return;
  refreshScheduled = true;
  setTimeout(() => {
    refreshScheduled = false;
    void refreshFromObservedTrajectory();
  }, REFRESH_THROTTLE_MS);
}

async function refreshFromObservedTrajectory(): Promise<void> {
  if (refreshInFlight) {
    // refresh 期间的新 put 不能丢；完成后按最新 key 再跑一轮。
    refreshDirty = true;
    return;
  }
  refreshInFlight = true;
  try {
    do {
      refreshDirty = false;
      const key = lastWrittenKey;
      if (!key) {
        clearCachedTrajectory();
        return;
      }
      const bin = await readTrajectoryBytes(key);
      // 异步读取期间 key 已切换：丢弃旧结果并立即重跑。
      if (key !== lastWrittenKey) {
        refreshDirty = true;
        continue;
      }
      if (!bin || bin.length === 0) {
        clearCachedTrajectory();
        continue;
      }
      const trajectoryId = extractTrajectoryId(bin);
      if (!trajectoryId) {
        clearCachedTrajectory();
        lastError = "trajectory identity is missing";
        continue;
      }
      if (lastTrajectoryId !== null && lastTrajectoryId !== trajectoryId) {
        // 同一 workspace 起新 task 时先清旧 chat，再发布新解析结果。
        clearCachedTrajectory();
      }
      const msgs = extractTrajectoryMessages(bin);
      lastTrajectoryId = trajectoryId;
      cachedMessages = msgs;
      historySource = msgs.length > 0 ? "latest_trajectory" : "none";
      lastRefreshAt = Date.now();
      lastError = null;
    } while (refreshDirty);
  } catch (err) {
    clearCachedTrajectory();
    lastError = describeError(err);
    dbg("refresh failed", err);
  } finally {
    refreshInFlight = false;
  }
}

function installDatabaseCloseHook(): void {
  if (pagehideHookInstalled || typeof window === "undefined") return;
  pagehideHookInstalled = true;
  window.addEventListener("pagehide", () => {
    for (const db of openDatabases) db.close();
    openDatabases.clear();
  });
}

function withTrajectoryStore<T>(
  operation: (store: IDBObjectStore, settle: (value: T) => void) => void,
  fallback: T,
): Promise<T> {
  return new Promise((resolve) => {
    let settled = false;
    let db: IDBDatabase | null = null;
    const settle = (value: T): void => {
      if (settled) return;
      settled = true;
      resolve(value);
    };
    const close = (): void => {
      if (!db) return;
      openDatabases.delete(db);
      db.close();
      db = null;
    };
    try {
      const req = indexedDB.open(IDB_DB_NAME);
      req.onsuccess = () => {
        try {
          db = req.result;
          openDatabases.add(db);
          db.onversionchange = close;
          if (!db.objectStoreNames.contains(IDB_STORE_NAME)) {
            close();
            settle(fallback);
            return;
          }
          const tx = db.transaction(IDB_STORE_NAME, "readonly");
          tx.oncomplete = close;
          tx.onabort = close;
          tx.onerror = close;
          operation(tx.objectStore(IDB_STORE_NAME), settle);
        } catch {
          close();
          settle(fallback);
        }
      };
      req.onerror = () => settle(fallback);
      req.onblocked = () => settle(fallback);
    } catch {
      close();
      settle(fallback);
    }
  });
}

function readTrajectoryBytes(key: string): Promise<Uint8Array | null> {
  return withTrajectoryStore<Uint8Array | null>((store, settle) => {
    try {
      const req = store.get(key);
      req.onsuccess = () => settle(decodeTrajectoryEntry(req.result));
      req.onerror = () => settle(null);
    } catch {
      settle(null);
    }
  }, null);
}

// Windsurf stores trajectory entries either as a JSON-serialised
// ``{ value: <base64> }`` string (older builds) or as an object with a
// ``value`` field containing the base64 payload (newer builds). Treat
// both shapes defensively.
function decodeTrajectoryEntry(raw: unknown): Uint8Array | null {
  try {
    let payload: unknown = raw;
    if (typeof payload === "string") {
      payload = JSON.parse(payload);
    }
    if (!payload || typeof payload !== "object") return null;
    const inner = (payload as { value?: unknown }).value;
    if (typeof inner !== "string" || inner.length === 0) return null;
    return base64ToBytes(inner);
  } catch {
    return null;
  }
}

function base64ToBytes(b64: string): Uint8Array | null {
  try {
    const bin = atob(b64);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) {
      out[i] = bin.charCodeAt(i);
    }
    return out;
  } catch {
    return null;
  }
}

// ---------------------------------------------------------------------------
// Protobuf mini-decoder
// ---------------------------------------------------------------------------

type FieldValue =
  | { kind: "varint"; varint: bigint }
  | { kind: "delim"; bytes: Uint8Array; start: number; end: number };

type FieldVisitor = (
  fieldNumber: number,
  wireType: number,
  value: FieldValue,
) => void;

function pbReadVarint(bytes: Uint8Array, off: number): [bigint, number] {
  let result = 0n;
  let shift = 0n;
  let cursor = off;
  while (cursor < bytes.length) {
    const b = bytes[cursor++]!;
    result |= BigInt(b & 0x7f) << shift;
    if ((b & 0x80) === 0) {
      return [result, cursor];
    }
    shift += 7n;
    if (shift > 64n) {
      throw new Error("varint too long");
    }
  }
  throw new Error("varint truncated");
}

function pbIterFields(
  bytes: Uint8Array,
  off: number,
  end: number,
  visit: FieldVisitor,
): void {
  let cursor = off;
  while (cursor < end) {
    const [tagBig, afterTag] = pbReadVarint(bytes, cursor);
    const tag = Number(tagBig);
    const fieldNumber = tag >>> 3;
    const wireType = tag & 0x7;
    cursor = afterTag;
    if (wireType === 0) {
      // varint
      const [v, afterVal] = pbReadVarint(bytes, cursor);
      visit(fieldNumber, wireType, { kind: "varint", varint: v });
      cursor = afterVal;
    } else if (wireType === 2) {
      // length-delimited
      const [lenBig, afterLen] = pbReadVarint(bytes, cursor);
      const len = Number(lenBig);
      cursor = afterLen;
      visit(fieldNumber, wireType, {
        kind: "delim",
        bytes,
        start: cursor,
        end: cursor + len,
      });
      cursor += len;
    } else if (wireType === 1) {
      // 64-bit fixed
      cursor += 8;
    } else if (wireType === 5) {
      // 32-bit fixed
      cursor += 4;
    } else {
      throw new Error("unsupported wire type " + wireType);
    }
  }
}

// Filter strings that look like binary noise, file URIs, hex
// identifiers, or Cortex metadata labels. We want only actual prose
// authored by the user or the model.
function isLikelyMessageText(s: string): boolean {
  if (!s || s.length < 2) return false;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 0x20 && c !== 0x09 && c !== 0x0a && c !== 0x0d) return false;
    if (c >= 0x7f && c <= 0x9f) return false;
  }
  if (
    s.startsWith("file://") ||
    s.startsWith("http://") ||
    s.startsWith("https://")
  ) {
    return false;
  }
  if (/^[0-9a-f-]{32,}$/.test(s)) return false;
  // Reject short identifier-like tokens (UUIDs, hex IDs, function names,
  // file extensions). Exempt strings that contain CJK characters — Chinese /
  // Japanese / Korean prompts are routinely short and whitespace-free yet
  // are real prose, so the original WSE filter rejected them as a false
  // positive. Verified against a live Cascade trajectory whose user prompt
  // was "如截图嗾使" (5 CJK chars, no whitespace).
  if (
    s.length < 20 &&
    !/\s/.test(s) &&
    !/[\u4e00-\u9fff\u3040-\u30ff\uac00-\ud7af]/.test(s)
  ) {
    return false;
  }
  if (!/[a-zA-Z\u4e00-\u9fff]/.test(s)) return false;
  if (/^claude-[a-z0-9-]+$/i.test(s)) return false;
  const metaLabels = [
    "Response Statistics",
    "Token Usage",
    "Input tokens",
    "Output tokens",
    "Cached input tokens",
    "Agent messages",
    "Model",
  ];
  for (const label of metaLabels) {
    if (s === label) return false;
    if (s.startsWith(label + ":")) return false;
    if (s.startsWith(label + " ")) return false;
  }
  return true;
}

function getFieldString(
  bytes: Uint8Array,
  start: number,
  end: number,
  fieldNumber: number,
): string | null {
  let best: string | null = null;
  try {
    pbIterFields(bytes, start, end, (num, wt, val) => {
      if (num !== fieldNumber || wt !== 2 || val.kind !== "delim") return;
      try {
        const decoded = new TextDecoder("utf-8", { fatal: false }).decode(
          val.bytes.subarray(val.start, val.end),
        );
        if (
          isLikelyMessageText(decoded) &&
          (best === null || decoded.length > best.length)
        ) {
          best = decoded;
        }
      } catch {
        // not utf-8; skip this field instance
      }
    });
  } catch {
    return null;
  }
  return best;
}

function parseStep(
  bytes: Uint8Array,
  start: number,
  end: number,
): CascadeMessage | null {
  let userField: { bytes: Uint8Array; start: number; end: number } | null = null;
  let asstField: { bytes: Uint8Array; start: number; end: number } | null = null;
  try {
    pbIterFields(bytes, start, end, (num, wt, val) => {
      if (wt !== 2 || val.kind !== "delim") return;
      if (num === 19) {
        userField = { bytes: val.bytes, start: val.start, end: val.end };
      } else if (num === 20) {
        asstField = { bytes: val.bytes, start: val.start, end: val.end };
      }
    });
  } catch {
    return null;
  }
  if (userField) {
    const u = userField as { bytes: Uint8Array; start: number; end: number };
    const text = getFieldString(u.bytes, u.start, u.end, 2);
    if (text) {
      return {
        role: "user",
        content: truncate(text, STEP_TEXT_TRUNCATE),
      };
    }
  }
  if (asstField) {
    const a = asstField as { bytes: Uint8Array; start: number; end: number };
    const text =
      getFieldString(a.bytes, a.start, a.end, 8) ??
      getFieldString(a.bytes, a.start, a.end, 1);
    if (text) {
      return {
        role: "assistant",
        content: truncate(text, STEP_TEXT_TRUNCATE),
      };
    }
  }
  return null;
}

export function extractTrajectoryId(bin: Uint8Array): string | null {
  let identity: string | null = null;
  try {
    pbIterFields(bin, 0, bin.length, (num, wt, value) => {
      if (identity !== null || num !== 1 || wt !== 2 || value.kind !== "delim") return;
      const text = new TextDecoder("utf-8", { fatal: true })
        .decode(value.bytes.subarray(value.start, value.end))
        .trim();
      if (text) identity = text;
    });
  } catch {
    return null;
  }
  return identity;
}

function extractTrajectoryMessages(bin: Uint8Array): CascadeMessage[] {
  const out: CascadeMessage[] = [];
  try {
    pbIterFields(bin, 0, bin.length, (topNum, topWt, topVal) => {
      if (topNum !== 2 || topWt !== 2 || topVal.kind !== "delim") return;
      pbIterFields(topVal.bytes, topVal.start, topVal.end, (num, wt, val) => {
        if (num !== 2 || wt !== 2 || val.kind !== "delim") return;
        const m = parseStep(val.bytes, val.start, val.end);
        if (m) out.push(m);
      });
    });
  } catch (err) {
    dbg("extractTrajectoryMessages failed", err);
  }
  return out;
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function truncate(s: string, max: number): string {
  if (s.length <= max) return s;
  if (max <= 3) return s.slice(0, max);
  // 省略号计入硬上限；旧实现先取 max 再追加 "..."，让所谓 6000
  // 字符上限实际变成 6003。
  return s.slice(0, max - 3) + "...";
}

function describeError(err: unknown): string {
  if (err instanceof Error) return err.message;
  try {
    return String(err);
  } catch {
    return "unknown error";
  }
}

/**
 * Dev/test diagnostic log. No-op unless ``setDebugEnabled(true)`` was
 * called (which only happens when the installer was run with
 * ``--debug``). Renamed from ``warn`` to ``dbg`` so the gating is
 * obvious at every call site — if you want a critical always-on
 * boot-time warning, use ``index.ts``'s own ``warn`` helper instead.
 *
 * Never logs prompt or assistant content directly; ``cascade_context``
 * call sites pass only error objects and operation names. The only
 * payload-adjacent data this function ever emits is the ``Error.message``
 * string from a parse failure, which by construction does not contain
 * trajectory bytes (the parser surfaces structural errors, not data).
 */
function dbg(...args: unknown[]): void {
  if (!debugEnabled) return;
  try {
    // eslint-disable-next-line no-console
    console.warn("[openpe-cascade-context]", ...args);
  } catch {
    // running in an environment without console — swallow.
  }
}
