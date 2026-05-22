import { applyTemplate } from "./templates";
import { EnhanceTemplate, OpenPEContextFile, OpenPERequest } from "./types";

export interface BuildRequestOptions {
  prompt: string;
  client: string;
  cwd?: string;
  mode: string;
  template: EnhanceTemplate;
  contextFiles?: OpenPEContextFile[];
  maxContextBytes: number;
}

export function buildEnhanceRequest(options: BuildRequestOptions): OpenPERequest {
  const contextFiles = (options.contextFiles ?? []).map((file) => ({
    ...file,
    content: truncateUtf8(file.content, options.maxContextBytes)
  }));

  const request: OpenPERequest = {
    prompt: applyTemplate(options.prompt, options.template),
    client: options.client,
    cwd: options.cwd,
    mode: options.template.mode?.trim() || options.mode
  };

  if (contextFiles.length > 0) {
    request.context = { files: contextFiles };
  }

  return request;
}

export function prepareCliRequest(request: OpenPERequest): OpenPERequest {
  const files = request.context?.files ?? [];
  if (files.length === 0) {
    return request;
  }

  const contextBlock = files
    .map((file) => {
      const language = file.language ? ` language=${file.language}` : "";
      return `文件：${file.path}${language}\n\n${file.content}`;
    })
    .join("\n\n---\n\n");

  return {
    ...request,
    prompt: `${request.prompt}\n\n以下是 IDE 侧采集到的文件上下文，请在增强提示词时作为参考，不要凭空扩大范围：\n\n${contextBlock}`,
    context: undefined
  };
}

export function truncateUtf8(value: string, maxBytes: number): string {
  if (Buffer.byteLength(value, "utf8") <= maxBytes) {
    return value;
  }

  const marker = "\n\n[openPE: 文件内容因长度限制已截断]";
  const markerBytes = Buffer.byteLength(marker, "utf8");
  const limit = Math.max(0, maxBytes - markerBytes);
  let end = value.length;
  while (end > 0 && Buffer.byteLength(value.slice(0, end), "utf8") > limit) {
    end = Math.floor(end * 0.9);
  }
  while (end < value.length && Buffer.byteLength(value.slice(0, end + 1), "utf8") <= limit) {
    end += 1;
  }
  return `${value.slice(0, end)}${marker}`;
}

