import { OpenPEResponse } from "./types";

export function coerceOpenPEResponse(value: unknown, source: string): OpenPEResponse {
  if (!isRecord(value) || typeof value.enhanced_prompt !== "string") {
    throw new Error(`${source} response is missing enhanced_prompt`);
  }

  return {
    enhanced_prompt: value.enhanced_prompt,
    warnings: coerceStringArray(value.warnings),
    metadata: isRecord(value.metadata) ? (value.metadata as OpenPEResponse["metadata"]) : undefined
  };
}

function coerceStringArray(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) {
    return undefined;
  }
  return value.filter((item): item is string => typeof item === "string");
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

