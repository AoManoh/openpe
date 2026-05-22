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
  version?: string;
}

export function getConfig(): OpenpeConfig {
  const raw =
    typeof window !== "undefined" && window.__openpe ? window.__openpe : {};
  return {
    baseUrl: typeof raw.baseUrl === "string" ? raw.baseUrl.replace(/\/+$/, "") : "",
    token: typeof raw.token === "string" ? raw.token : "",
    descriptorPath:
      typeof raw.descriptorPath === "string" ? raw.descriptorPath : undefined,
    fsProbe: raw.fsProbe === true,
    version: typeof raw.version === "string" ? raw.version : undefined,
  };
}
