import { OpenPEExtensionConfig, OpenPERequest, OpenPEResponse } from "../core/types";
import { coerceOpenPEResponse } from "../core/response";

export async function enhanceViaHttp(
  request: OpenPERequest,
  config: OpenPEExtensionConfig
): Promise<OpenPEResponse> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), config.timeoutMs);

  try {
    const response = await fetch(new URL("/v1/prompt-enhance", config.serverUrl), {
      method: "POST",
      headers: {
        "content-type": "application/json"
      },
      body: JSON.stringify(request),
      signal: controller.signal
    });

    const text = await response.text();
    if (!response.ok) {
      throw new Error(formatHttpError(response.status, text));
    }

    return parseHttpResponse(text);
  } finally {
    clearTimeout(timer);
  }
}

function parseHttpResponse(text: string): OpenPEResponse {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    throw new Error(`openPE HTTP returned invalid JSON: ${message}`);
  }

  return coerceOpenPEResponse(parsed, "openPE HTTP");
}

function formatHttpError(status: number, text: string): string {
  if (!text.trim()) {
    return `openPE HTTP request failed with status ${status}`;
  }

  try {
    const parsed = JSON.parse(text) as { error?: string };
    if (parsed.error) {
      return `openPE HTTP request failed with status ${status}: ${parsed.error}`;
    }
  } catch {
    // 非 JSON 错误体直接展示原文，方便诊断服务端或代理返回。
  }

  return `openPE HTTP request failed with status ${status}: ${text.trim()}`;
}
