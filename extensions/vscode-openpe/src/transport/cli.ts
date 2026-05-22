import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { OpenPEExtensionConfig, OpenPERequest, OpenPEResponse } from "../core/types";
import { prepareCliRequest } from "../core/context";
import { coerceOpenPEResponse } from "../core/response";

const execFileAsync = promisify(execFile);

export async function enhanceViaCli(
  request: OpenPERequest,
  config: OpenPEExtensionConfig
): Promise<OpenPEResponse> {
  const cliRequest = prepareCliRequest(request);
  const args = [
    "enhance",
    "--json",
    "--prompt",
    cliRequest.prompt,
    "--client",
    cliRequest.client ?? config.client,
    "--mode",
    cliRequest.mode ?? config.defaultMode
  ];

  if (cliRequest.cwd) {
    args.push("--cwd", cliRequest.cwd);
  }

  const env = { ...process.env };
  if (config.envFile) {
    env.OPENPE_ENV_FILE = config.envFile;
  }

  try {
    const { stdout } = await execFileAsync(config.executablePath, args, {
      cwd: cliRequest.cwd,
      env,
      maxBuffer: 20 * 1024 * 1024,
      timeout: config.timeoutMs
    });
    return parseEnhanceResponse(stdout);
  } catch (error) {
    throw new Error(formatCliError(error));
  }
}

export function parseEnhanceResponse(stdout: string): OpenPEResponse {
  let parsed: unknown;
  try {
    parsed = JSON.parse(stdout);
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    throw new Error(`openPE CLI returned invalid JSON: ${message}`);
  }

  return coerceOpenPEResponse(parsed, "openPE CLI");
}

function formatCliError(error: unknown): string {
  if (!isRecord(error)) {
    return String(error);
  }

  const message = typeof error.message === "string" ? error.message : "openPE CLI failed";
  const stderr = typeof error.stderr === "string" ? error.stderr.trim() : "";
  const stdout = typeof error.stdout === "string" ? error.stdout.trim() : "";
  const signal = typeof error.signal === "string" ? ` signal=${error.signal}` : "";
  const code =
    typeof error.code === "number" || typeof error.code === "string"
      ? ` code=${String(error.code)}`
      : "";
  const details = [stderr, stdout].filter(Boolean).join("\n");
  return details ? `${message}${code}${signal}\n${details}` : `${message}${code}${signal}`;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
