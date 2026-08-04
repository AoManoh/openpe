# openpe-ide-inject

Profile-gated IDE patch payload 的 TypeScript 源码。它被构建为单个自包含
IIFE；canonical 路径写入 `workbench.desktop.main.js`，exact-build Devin
入口同时写入 sessions/workbench 两个 renderer bundle。

缺失或未知 `hostProfileId` 一律 fail closed。`windsurf-legacy` 只在
`client=windsurf`、`mode=cascade` 时启用；`devin-desktop` 只在
`client=devin`、`mode=agent` 时启用，且强制 `historySource=none`。因此 Devin
会启动 DOM observer，但不会启动 legacy Windsurf IndexedDB watcher。

## 构建

```bash
cd extensions/openpe-windsurf-patch/inject
npm ci
npm run check
npm run build    # → dist/inject.js
```

Python installer 的 `install` 子命令会读 `dist/inject.js`，缺这个文件就
拒绝继续（`exit 69`）。

## 类型检查

```bash
npm run check    # tsc --noEmit
```

## 文件结构

| 文件 | 职责 |
|---|---|
| `src/anchor_selection.ts` | 按唯一 editor 分组并只选择最右侧 Devin action，避免附件区同 class 按钮产生重复 anchor。 |
| `src/index.ts`           | IIFE 入口；单实例守卫；unknown/mismatched profile 拒绝启动；已验证 profile 启动 observer，只有 legacy Windsurf 启动 history watcher。 |
| `src/auth.ts`            | 解析 installer bootstrap：server、transaction、client/mode、history source 与 runtime gate。 |
| `src/fs_probe.ts`        | 可选的 P3 诊断探针，验证 Windsurf renderer 能否通过 Node `fs` 读到本地 0600 descriptor。 |
| `src/client.ts`          | `fetch` 调本地 `POST /v1/prompt-enhance`。有缓存时附带可选的 `history` 字段。 |
| `src/observer.ts`        | `MutationObserver` 定位 Cascade 输入工具栏并触发注入。 |
| `src/button.ts`          | openPE logo 按钮的 DOM 创建；样式委托给 `styles.ts`。图标 SVG 完整 inline 在本文件内（`OPENPE_LOGO_SVG`），模块加载时编码成 data URI，运行时不依赖任何外部 SVG 文件。 |
| `src/dialog.ts`          | 点击增强流程：读 editor → 调 enhancer → 写回。 |
| `src/styles.ts`          | 尊重 `var(--vscode-*)` 主题变量的 CSS 变量。 |
| `src/cascade_context.ts` | 从 IndexedDB trajectory 缓存里镜像当前 Cascade 对话，让 `dialog.ts` 能把最近消息作为 `enhancer.Request.History` 附上。纯 renderer 侧观察；不需要 hook adapter、server 或 enhancer 任何改动。 |

## 真实宿主 DOM 注意

聊天输入工具栏是宿主私有 DOM。`observer.ts` 会遍历显式 toolbar、通用
submit/send 按钮，以及 exact-build Devin 的
`button[type="button"].bg-bg-accent-neutral` 候选；每个候选都必须在有限祖先范围内
找到 Lexical、textarea 或 contenteditable editor 才允许挂载。宿主更新后如果按钮
消失，应先重新取证 DOM，再扩充 selector 并重新构建，不能放宽到无 editor 边界的
任意按钮。

无 telemetry、无第三方网络调用。Exact-build Devin installer 和 HTTP client 都
要求 `http://127.0.0.1:<port>`，并拒绝 redirect；legacy canonical 诊断仍接受
handshake 模块定义的其它 loopback 形式，不能把该较宽契约外推到 exact 路径。

## P3 文件系统探针

该 canonical Windsurf bring-up 接口当前因 profile mutation gate 而不可达；以下命令
只保留协议参考，会返回 unsupported，不能作为当前安装说明。

要验证注入后的 Windsurf renderer 能不能在运行时读到本地 server
descriptor，安装时带 `--fs-probe`：

```bash
python3 -m installer.cli install --host windsurf --i-accept-experimental-risk --fs-probe
```

重启 Windsurf 后点 openPE 按钮，在 DevTools 看 `[openpe-fs-probe]`。
探针只输出 descriptor 路径、字节数、文件 mode 和 schema 形状；不能
打印 bearer token。这是把"安装时嵌入 token"替换成"运行时读 descriptor"
之前的临时诊断网关。

## Dev/test 诊断网关 (`--debug`)

该选项属于当前不可达的 canonical Windsurf bring-up 接口；exact Devin
`multi_bundle_patch` 尚未暴露 `--debug`。

`openpe-ide-patch install --host windsurf --debug` 在安装时打开一个 build-time 开关——inject
层通过 `window.__openpe.debug` 读到。开启后**只**发生以下两件事：

1. `cascade_context.ts` watcher 内部的 `dbg()` helper 会在 transient
   失败（IDB hook 安装失败、trajectory parse 失败）时输出
   `[openpe-cascade-context]` warning。生产（默认）下这个 helper 是
   静默 no-op。
2. 一个只读的 `window.__openpeDebug` 命名空间被 freeze 挂到 global，
   暴露两个**仅 shape** 的访问器：

```ts
window.__openpeDebug.describeContext()
// → { started, messageCount, lastRefreshAt, lastWrittenKey,
//     lastError, historySource: 'latest_trajectory' | 'none',
//     debugEnabled: true }

window.__openpeDebug.describeHistory()
// → { source, messageCount, totalChars,
//     roles: { user, assistant },
//     oldestPreview, newestPreview,   // 各 ≤ 80 字符，带 role 前缀
//     truncatedToBudget: boolean }

window.__openpeDebug.describeLastEnhance()
// → null（如果还没点过 openPE 按钮）  OR
// → {
//     at: <timestamp ms>,
//     request: {
//       source: 'latest_trajectory' | 'none',
//       messageCount, totalChars,
//       roles: { user, assistant },
//       messagePreviews: [
//         { role, contentChars, preview /* ≤ 200 字符 */, previewTruncated }
//       ],
//       originalPromptChars,
//       originalPromptPreview,      // ≤ 400 字符
//       originalPromptTruncated
//     },
//     response: {
//       ok: boolean,
//       enhancedPromptChars,
//       enhancedPromptPreview,      // ≤ 400 字符
//       enhancedPromptTruncated,
//       metadata: {                 // 透传自 Go enhancer Response.Metadata
//         usedContext?: string[],   // 含 'history' ⇔ enhancer 真用了 history
//         sections?: [{ name, length, truncated }],
//         provider?: string,
//         model?: string
//       } | null,
//       error: string | null
//     }
//   }
```

不同 accessor 的 preview 边界**有意拉开**，对应不同用途：

| accessor | preview 上限 / 条 | 用途 |
|---|---|---|
| `describeHistory()` | **80 字符** | 任何时间安全瞄一眼当前缓存形状 |
| `describeLastEnhance().request.messagePreviews[]` | **200 字符** | 验证"方案1/方案A 这种模糊指代的定义是不是真的被打包进 history 一起发出去了"——一条典型的定义/上下文通常 50-150 字符，200 字符够装下 |
| `describeLastEnhance().request.originalPromptPreview` | **400 字符** | 看用户点 PE 那一刻输入框里写了什么 |
| `describeLastEnhance().response.enhancedPromptPreview` | **400 字符** | 看 enhancer 返回了什么——同一份文本本来就已经在 Cascade 输入框里可见，debug 视图里展示前 400 字符不会带来新泄漏 |

`__openpeDebug` 不暴露 bearer token 或 `Authorization` header，但会暴露上述
有界内容 preview；短于上限的 prompt/message 会完整显示，因此只应在可信本机调试
会话中启用。debug 模式和生产模式的
**历史抓取行为、wire 行为完全一致**，只是诊断可见性不同。`describeLastEnhance()` 在生产模式下（未带 `--debug`）**整个不挂载**，
快照本身在模块内存里也保持私有。

```bash
python3 -m installer.cli install --host windsurf --i-accept-experimental-risk --debug
```

跟 `--fs-probe` 互相独立，可以叠加传。

## 消费层 token 预算 (`maxContextTokens`)

`window.__openpe.maxContextTokens` 是 installer 在 `install` 时按
`--max-context-tokens N` 或 `OPENPE_MAX_CONTEXT_TOKENS` 环境变量
快照进 bundle 的可选字段（仅当 > 0 时 embed，匹配 Go `omitempty` 语义）。

`auth.ts` 与最终 HTTP transport 都只接受**正 safe integer**：

- `undefined` / 不存在 → 不下行到 server，按 server 默认行为（不收缩）。
- 正整数 → 写入 `enhancePrompt` 的 `options.max_context_tokens`，server
  按 ~4 字符/token 近似把 retrieval / history section 收缩到预算之内。
- `0` / 浮点数 / 负数 / unsafe integer / `Infinity` / `NaN` / 非数字 → 静默忽略；保守的回退确保
  hand-edit 的 bundle 不会产生意外行为。

`dialog.ts` 在每次 enhance 时透传该字段。完整跨语言契约：

```
installer/__main__.py::_resolve_max_context_tokens
  → installer/__main__.py::_build_payload_prelude
  → globalThis.__openpe.maxContextTokens  (snapshot at install)
  → inject/src/auth.ts::OpenpeConfig.maxContextTokens  (parse)
  → inject/src/dialog.ts                                (forward)
  → inject/src/client.ts::EnhanceRequest.options.max_context_tokens  (POST body, snake_case)
  → internal/enhancer/types.go::Options.MaxContextTokens  (Go server)
```

字段名故意做了 camelCase（`window.__openpe`）→ snake_case（POST body）
的双重表示：前者匹配 JS 习惯 + 已有 bootstrap config 风格；后者匹配
Go 的 `json:"max_context_tokens,omitempty"` tag，server 端无需额外
remapping。TypeScript typecheck 与 Python fixture 会在发布验证中共同锁定
profile bootstrap 和 snake_case wire 边界。

Server 返回非空 `warnings` 时，按钮不会自动写回 editor；增强结果只复制到
剪贴板供 review，并显示截断后的告警摘要。剪贴板失败时也保持不写回。

这是**消费层** token 预算；不要与 `cascade_context.ts::DEFAULT_HISTORY_BUDGET`
里硬编码的 32 / 6000 / 80000 **采集层**常量混淆——后者是基于真实 Windsurf
trajectory 经验调优的常量，不暴露成可配。
