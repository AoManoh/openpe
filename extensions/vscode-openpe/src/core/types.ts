export type OpenPETransport = "cli" | "http";

export type OpenPEOutputMode = "preview" | "clipboard" | "insert" | "replaceSelection";

export interface OpenPEExtensionConfig {
  transport: OpenPETransport;
  executablePath: string;
  envFile: string;
  serverUrl: string;
  client: string;
  defaultMode: string;
  outputMode: OpenPEOutputMode;
  includeActiveFileContext: boolean;
  maxContextBytes: number;
  timeoutMs: number;
  templates: EnhanceTemplate[];
}

export interface EnhanceTemplate {
  name: string;
  description?: string;
  mode?: string;
  prefix?: string;
}

export interface OpenPEContextFile {
  path: string;
  content: string;
  language?: string;
}

export interface OpenPERequest {
  prompt: string;
  client?: string;
  cwd?: string;
  mode?: string;
  history?: Array<{
    role: string;
    content: string;
  }>;
  rules?: string[];
  guidelines?: string[];
  context?: {
    files?: OpenPEContextFile[];
    retrieval?: string[];
  };
  options?: {
    max_context_tokens?: number;
    return_metadata?: boolean;
  };
}

export interface OpenPEResponse {
  enhanced_prompt: string;
  warnings?: string[];
  metadata?: {
    used_context?: string[];
    sections?: Array<{
      name: string;
      length: number;
      truncated: boolean;
    }>;
    provider?: string;
    model?: string;
    latency_ms?: number;
    prompt_chars?: number;
    output_chars?: number;
    raw?: unknown;
  };
}

export interface EnhancementState {
  request?: OpenPERequest;
  response?: OpenPEResponse;
  template?: EnhanceTemplate;
}
