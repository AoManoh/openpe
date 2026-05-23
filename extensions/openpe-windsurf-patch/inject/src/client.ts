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
  client?: string;
  mode?: string;
  cwd?: string;
  history?: EnhanceMessage[];
  options?: EnhanceOptions;
}

export interface EnhanceResponse {
  enhanced_prompt?: string;
  metadata?: Record<string, unknown>;
  error?: string;
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
  const url = `${config.baseUrl}/v1/prompt-enhance`;
  const resp = await fetch(url, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      authorization: `Bearer ${config.token}`,
    },
    body: JSON.stringify({
      prompt: body.prompt,
      client: body.client ?? "windsurf",
      // Match the Windsurf hook adapter (internal/adapters/windsurf/hook.go)
      // which sends mode="cascade". The enhancer's prompt assembly puts the
      // mode label into the LLM context verbatim, so a mismatch between hook
      // and button paths produces visibly different output styles even
      // though both routes use the same backend enhancer.
      mode: body.mode ?? "cascade",
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
  if (!payload.enhanced_prompt) {
    throw new ClientError(
      payload.error ?? "server returned an empty enhanced_prompt",
      resp.status,
    );
  }
  return payload;
}
