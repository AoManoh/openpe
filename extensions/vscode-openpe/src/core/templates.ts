import { EnhanceTemplate } from "./types";

export const builtInTemplates: EnhanceTemplate[] = [
  {
    name: "通用增强",
    description: "保留原始意图，改写为清晰、可执行的提示词。"
  },
  {
    name: "编码代理任务",
    description: "适合 Codex、Windsurf、Cursor 等 coding agent 执行。",
    mode: "agent",
    prefix: "请将下面内容改写为适合 coding agent 执行的任务提示词，保留用户原始意图、约束、上下文和安全边界。"
  },
  {
    name: "调试排查",
    description: "强调复现、根因、影响面、验证与回归风险。",
    mode: "debug",
    prefix: "请将下面内容改写为系统化调试任务，要求覆盖复现、根因定位、影响范围、修复方案、测试验证和残余风险。"
  },
  {
    name: "代码审查",
    description: "强调缺陷、风险、边界条件和测试缺口。",
    mode: "review",
    prefix: "请将下面内容改写为代码审查任务，优先关注 bug、行为回归、安全风险、边界条件和测试缺口。"
  }
];

export function normalizeTemplates(value: unknown): EnhanceTemplate[] {
  if (!Array.isArray(value)) {
    return [];
  }

  const templates: EnhanceTemplate[] = [];
  for (const item of value) {
    if (!isRecord(item) || typeof item.name !== "string" || item.name.trim() === "") {
      continue;
    }
    templates.push({
      name: item.name.trim(),
      description: typeof item.description === "string" ? item.description.trim() : undefined,
      mode: typeof item.mode === "string" ? item.mode.trim() : undefined,
      prefix: typeof item.prefix === "string" ? item.prefix.trim() : undefined
    });
  }
  return templates;
}

export function applyTemplate(prompt: string, template: EnhanceTemplate): string {
  const prefix = template.prefix?.trim();
  if (!prefix) {
    return prompt;
  }
  return `${prefix}\n\n${prompt}`;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

