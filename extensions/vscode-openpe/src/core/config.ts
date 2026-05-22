import {
  EnhanceTemplate,
  OpenPEExtensionConfig,
  OpenPEOutputMode,
  OpenPETransport
} from "./types";
import { normalizeTemplates } from "./templates";

export interface RawOpenPEConfig {
  transport?: unknown;
  executablePath?: unknown;
  envFile?: unknown;
  serverUrl?: unknown;
  client?: unknown;
  defaultMode?: unknown;
  outputMode?: unknown;
  includeActiveFileContext?: unknown;
  maxContextBytes?: unknown;
  timeoutMs?: unknown;
  templates?: unknown;
}

const transports = new Set<OpenPETransport>(["cli", "http"]);
const outputModes = new Set<OpenPEOutputMode>([
  "preview",
  "clipboard",
  "insert",
  "replaceSelection"
]);

export function normalizeConfig(raw: RawOpenPEConfig): OpenPEExtensionConfig {
  return {
    transport: normalizeEnum(raw.transport, transports, "cli"),
    executablePath: normalizeString(raw.executablePath, "openpe"),
    envFile: normalizeString(raw.envFile, ""),
    serverUrl: normalizeServerUrl(raw.serverUrl),
    client: normalizeString(raw.client, "vscode"),
    defaultMode: normalizeString(raw.defaultMode, "agent"),
    outputMode: normalizeEnum(raw.outputMode, outputModes, "preview"),
    includeActiveFileContext: raw.includeActiveFileContext === true,
    maxContextBytes: normalizeNumber(raw.maxContextBytes, 20000, 1000, 200000),
    timeoutMs: normalizeNumber(raw.timeoutMs, 60000, 1000, 300000),
    templates: normalizeTemplates(raw.templates)
  };
}

export function mergeTemplates(
  builtIn: EnhanceTemplate[],
  configured: EnhanceTemplate[]
): EnhanceTemplate[] {
  const names = new Set<string>();
  const merged: EnhanceTemplate[] = [];
  for (const template of [...builtIn, ...configured]) {
    const name = template.name.trim();
    if (name === "" || names.has(name)) {
      continue;
    }
    names.add(name);
    merged.push({ ...template, name });
  }
  return merged;
}

function normalizeString(value: unknown, fallback: string): string {
  return typeof value === "string" && value.trim() !== "" ? value.trim() : fallback;
}

function normalizeServerUrl(value: unknown): string {
  const raw = normalizeString(value, "http://127.0.0.1:18980");
  return raw.endsWith("/") ? raw.slice(0, -1) : raw;
}

function normalizeEnum<T extends string>(value: unknown, allowed: Set<T>, fallback: T): T {
  return typeof value === "string" && allowed.has(value as T) ? (value as T) : fallback;
}

function normalizeNumber(
  value: unknown,
  fallback: number,
  min: number,
  max: number
): number {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    return fallback;
  }
  return Math.min(max, Math.max(min, Math.trunc(value)));
}

