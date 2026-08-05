/**
 * Tiny HTTP client for the local `POST /v1/prompt-enhance` endpoint.
 *
 * Mirrors the openPE canonical enhancer.Request schema for the fields
 * the dialog actually populates: prompt, client, mode, history. Other
 * fields (rules, guidelines, context.files, options) are still left to
 * server defaults; future inject phases can extend `EnhanceRequest`
 * without touching the wire contract because the server uses the same
 * canonical `enhancer.Request` JSON schema.
 */

import type { OpenpeConfig } from "./auth.js";

export interface EnhanceMessage {
  role: string;
  content: string;
}

/**
 * Wire-level options block; mirrors the Go ``enhancer.Options`` struct
 * (``internal/enhancer/types.go``). Field names use snake_case to match
 * the Go JSON tags exactly so the server unmarshals the body without a
 * remapping layer.
 *
 * Only the consumer-layer ``max_context_tokens`` is currently
 * surfaced — ``return_metadata`` is server-default off and the dialog
 * has no metadata UI today. Extend this interface when adding a new
 * consumer-facing knob.
 */
export interface EnhanceOptions {
  max_context_tokens?: number;
}

export interface EnhanceRequest {
  prompt: string;
  client: string;
  mode: string;
  cwd?: string;
  history?: EnhanceMessage[];
  options?: EnhanceOptions;
}

export interface EnhanceResponse {
  enhanced_prompt?: string;
  warnings?: string[];
  metadata?: Record<string, unknown>;
  error?: string;
}

interface OpenpeBridge {
  enhance(requestId: string, body: EnhanceRequest): Promise<EnhanceResponse>;
  cancel(requestId: string): void;
}

function preloadBridge(): OpenpeBridge | null {
  if (typeof window === "undefined") return null;
  const descriptor = Object.getOwnPropertyDescriptor(window, "__openpeBridge");
  if (!descriptor || !("value" in descriptor)) return null;
  const candidate = descriptor.value as Partial<OpenpeBridge> | null;
  return candidate &&
    typeof candidate.enhance === "function" &&
    typeof candidate.cancel === "function"
    ? (candidate as OpenpeBridge)
    : null;
}

export class ClientError extends Error {
  readonly status: number | null;

  constructor(message: string, status: number | null) {
    super(message);
    this.name = "ClientError";
    this.status = status;
  }
}

export async function enhancePrompt(
  config: OpenpeConfig,
  body: EnhanceRequest,
  signal?: AbortSignal,
): Promise<EnhanceResponse> {
  if (!body.prompt.trim()) {
    throw new ClientError("prompt is empty", null);
  }
  const maxContextTokens = body.options?.max_context_tokens;
  if (
    maxContextTokens !== undefined &&
    (!Number.isSafeInteger(maxContextTokens) || maxContextTokens <= 0)
  ) {
    throw new ClientError("max_context_tokens must be a positive safe integer", null);
  }
  if (config.credentialMode === "preload-capability-v1") {
    if (signal?.aborted) {
      throw signal.reason ?? new DOMException("cancelled", "AbortError");
    }
    const bridge = preloadBridge();
    if (!bridge) {
      throw new ClientError("secure openPE preload capability is unavailable", null);
    }
    const requestId = newRequestId();
    const payload = await raceAbort(
      bridge.enhance(requestId, body),
      signal,
      () => bridge.cancel(requestId),
    );
    return validateResponse(payload, null);
  }
  if (!config.token) {
    throw new ClientError("server token is missing", null);
  }
  let baseURL: URL;
  try {
    baseURL = new URL(config.baseUrl);
  } catch {
    throw new ClientError("server base URL is invalid", null);
  }
  if (
    baseURL.protocol !== "http:" ||
    baseURL.hostname !== "127.0.0.1" ||
    !baseURL.port ||
    !["", "/"].includes(baseURL.pathname) ||
    baseURL.username ||
    baseURL.password ||
    baseURL.search ||
    baseURL.hash
  ) {
    throw new ClientError("server must use http://127.0.0.1:<port>", null);
  }
  const url = `${baseURL.origin}/v1/prompt-enhance`;
  const resp = await fetch(url, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      authorization: `Bearer ${config.token}`,
    },
    body: JSON.stringify({
      prompt: body.prompt,
      client: body.client,
      // Match the selected host adapter's canonical client/mode labels. The
      // installer embeds them from the product profile so this transport
      // never guesses a host from a directory or legacy product name.
      mode: body.mode,
      cwd: body.cwd ?? "",
      // Only attach history when we actually have messages — otherwise the
      // server sees the field absent (matching the existing Windsurf hook
      // behaviour) instead of an empty array, which is friendlier to any
      // future enhancer.Request validators.
      ...(body.history && body.history.length > 0
        ? { history: body.history }
        : {}),
      // Only attach options when at least one field is set; matches the
      // Go side's ``omitempty`` so an absent options block stays
      // wire-equivalent to ``{}``.
      ...(body.options && Object.keys(body.options).length > 0
        ? { options: body.options }
        : {}),
    }),
    signal,
    redirect: "error",
    credentials: "omit",
  });
  let payload: EnhanceResponse;
  try {
    payload = (await resp.json()) as EnhanceResponse;
  } catch {
    throw new ClientError(
      `server returned non-JSON response (status ${resp.status})`,
      resp.status,
    );
  }
  if (!resp.ok) {
    throw new ClientError(
      payload.error ?? `server returned status ${resp.status}`,
      resp.status,
    );
  }
  return validateResponse(payload, resp.status);
}

function validateResponse(
  payload: EnhanceResponse,
  status: number | null,
): EnhanceResponse {
  if (!payload || typeof payload !== "object" || !payload.enhanced_prompt) {
    throw new ClientError(
      payload?.error ?? "server returned an empty enhanced_prompt",
      status,
    );
  }
  payload.warnings = Array.isArray(payload.warnings)
    ? payload.warnings.filter(
        (warning): warning is string =>
          typeof warning === "string" && warning.trim().length > 0,
      )
    : [];
  return payload;
}

function newRequestId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `${Date.now().toString(36)}_${Math.random().toString(36).slice(2)}_${Math.random().toString(36).slice(2)}`;
}

function raceAbort<T>(
  promise: Promise<T>,
  signal?: AbortSignal,
  cancel?: () => void,
): Promise<T> {
  if (!signal) return promise;
  if (signal.aborted) {
    cancel?.();
    return Promise.reject(signal.reason ?? new DOMException("cancelled", "AbortError"));
  }
  return new Promise<T>((resolve, reject) => {
    const abort = (): void => {
      cancel?.();
      reject(signal.reason ?? new DOMException("cancelled", "AbortError"));
    };
    signal.addEventListener("abort", abort, { once: true });
    promise.then(
      (value) => {
        signal.removeEventListener("abort", abort);
        resolve(value);
      },
      (error: unknown) => {
        signal.removeEventListener("abort", abort);
        reject(error);
      },
    );
  });
}
