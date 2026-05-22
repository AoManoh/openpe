import type { OpenpeConfig } from "./auth.js";

interface NodeFSPromises {
  readFile(path: string, encoding: "utf8"): Promise<string>;
  stat(path: string): Promise<{ mode: number; size: number; isFile(): boolean }>;
}

type NodeRequire = (id: string) => unknown;

export function runFilesystemProbe(config: OpenpeConfig): void {
  if (!config.fsProbe) {
    return;
  }
  void probe(config);
}

async function probe(config: OpenpeConfig): Promise<void> {
  const descriptorPath = config.descriptorPath;
  if (!descriptorPath) {
    log("fs probe skipped: descriptorPath missing");
    return;
  }
  const fs = loadFSPromises();
  if (!fs) {
    log("fs probe result", { ok: false, reason: "fs module unavailable" });
    return;
  }
  try {
    const info = await fs.stat(descriptorPath);
    const text = await fs.readFile(descriptorPath, "utf8");
    log("fs probe result", {
      ok: true,
      descriptorPath,
      bytes: text.length,
      mode: "0" + (info.mode & 0o777).toString(8),
      isFile: info.isFile(),
      descriptorShape: hasDescriptorShape(text),
    });
  } catch (err) {
    warn("fs probe result", err);
  }
}

function loadFSPromises(): NodeFSPromises | null {
  const req = loadRequire();
  if (!req) {
    return null;
  }
  for (const moduleName of ["node:fs/promises", "fs"]) {
    try {
      const loaded = req(moduleName);
      const candidate =
        moduleName === "fs" && isRecord(loaded) ? loaded.promises : loaded;
      if (isFSPromises(candidate)) {
        return candidate;
      }
    } catch {
      // 继续尝试下一个模块别名，Windsurf 可能暴露任一 Node 形态。
    }
  }
  return null;
}

function loadRequire(): NodeRequire | null {
  try {
    const req = (0, eval)(
      "typeof require === 'function' ? require : undefined",
    ) as NodeRequire | undefined;
    return typeof req === "function" ? req : null;
  } catch {
    return null;
  }
}

function isFSPromises(value: unknown): value is NodeFSPromises {
  return (
    isRecord(value) &&
    typeof value.readFile === "function" &&
    typeof value.stat === "function"
  );
}

function hasDescriptorShape(text: string): boolean {
  try {
    const parsed = JSON.parse(text) as unknown;
    return (
      isRecord(parsed) &&
      typeof parsed.base_url === "string" &&
      typeof parsed.token === "string" &&
      typeof parsed.pid === "number"
    );
  } catch {
    return false;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function log(message: string, details?: unknown): void {
  // eslint-disable-next-line no-console
  console.info(`[openpe-fs-probe] ${message}`, details ?? "");
}

function warn(message: string, err: unknown): void {
  // eslint-disable-next-line no-console
  console.warn(`[openpe-fs-probe] ${message}`, err);
}
