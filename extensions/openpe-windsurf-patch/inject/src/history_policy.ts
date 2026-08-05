import type { HistoryMeta } from "./cascade_context.js";

export function historyForEnhance(): HistoryMeta {
  // renderer 没有稳定 chat identity；未绑定 trajectory cache 只允许进入
  // debug 形态视图，不允许进入 provider 请求。
  return {
    messages: [],
    source: "none",
    totalChars: 0,
    roles: { user: 0, assistant: 0 },
  };
}
