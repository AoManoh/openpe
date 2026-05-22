/**
 * Tiny HTTP client for the local `POST /v1/prompt-enhance` endpoint.
 *
 * Mirrors the openPE canonical enhancer.Request schema for the fields
 * the dialog actually populates: prompt, client, mode. Other fields
 * (context, rules, ...) are deliberately omitted in Phase 1 of the
 * inject UI; the server will fall back to its defaults.
 */

import type { OpenpeConfig } from "./auth.js";

export interface EnhanceRequest {
  prompt: string;
  client?: string;
  mode?: string;
  cwd?: string;
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
      mode: body.mode ?? "agent",
      cwd: body.cwd ?? "",
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
