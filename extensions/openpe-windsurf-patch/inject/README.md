# openpe-windsurf-inject

`openpe-windsurf-patch` installer 注入到 Windsurf 的
`workbench.desktop.main.js` 里那段 payload 的 TypeScript 源码。被构建成
单个自包含 IIFE，可以原样塞在 `/* === OPENPE-INJECT-BEGIN === */` 标记
之间。

## 构建

```bash
cd extensions/openpe-windsurf-patch/inject
npm install
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
| `src/index.ts`           | IIFE 入口；单实例守卫；串起 observer + openPE logo 按钮 + Cascade context watcher。 |
| `src/auth.ts`            | 读 `globalThis.__openpe`（installer 写入）拿 bearer token 和 base URL。 |
| `src/fs_probe.ts`        | 可选的 P3 诊断探针，验证 Windsurf renderer 能否通过 Node `fs` 读到本地 0600 descriptor。 |
| `src/client.ts`          | `fetch` 调本地 `POST /v1/prompt-enhance`。有缓存时附带可选的 `history` 字段。 |
| `src/observer.ts`        | `MutationObserver` 定位 Cascade 输入工具栏并触发注入。 |
| `src/button.ts`          | openPE logo 按钮的 DOM 创建；样式委托给 `styles.ts`。图标内联自 VSIX `openpe-icon.svg` 的设计，所以修补后的 bundle 在运行时不依赖任何 VSIX 文件。 |
| `src/dialog.ts`          | 点击增强流程：读 editor → 调 enhancer → 写回。 |
| `src/styles.ts`          | 尊重 `var(--vscode-*)` 主题变量的 CSS 变量。 |
| `src/cascade_context.ts` | 从 IndexedDB trajectory 缓存里镜像当前 Cascade 对话，让 `dialog.ts` 能把最近消息作为 `enhancer.Request.History` 附上。纯 renderer 侧观察；不需要 hook adapter、server 或 enhancer 任何改动。 |

## 真实宿主 DOM 注意

Cascade 聊天输入工具栏是 Windsurf 的私有 DOM。`observer.ts` 维护了一组
best-effort 选择器，命中第一个就停；如果安装 + 重启 Windsurf 后 openPE
logo 按钮没出现，最快的修法是扩充 `findCascadeToolbar()` 里的选择器后
重新构建。

无 telemetry、无第三方网络调用。每次 fetch 都打到 descriptor 里的
`base_url`，installer 保证它指向 `127.0.0.1:<port>`。

## P3 文件系统探针

要验证注入后的 Windsurf renderer 能不能在运行时读到本地 server
descriptor，安装时带 `--fs-probe`：

```bash
python3 -m installer install --i-accept-experimental-risk --fs-probe
```

重启 Windsurf 后点 openPE 按钮，在 DevTools 看 `[openpe-fs-probe]`。
探针只输出 descriptor 路径、字节数、文件 mode 和 schema 形状；不能
打印 bearer token。这是把"安装时嵌入 token"替换成"运行时读 descriptor"
之前的临时诊断网关。

## Dev/test 诊断网关 (`--debug`)

`installer install --debug` 在安装时打开一个 build-time 开关——inject
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

无论怎么安装，**完整 message body、bearer token、`Authorization`
header 都不会出现在 `__openpeDebug` 上**。debug 模式和生产模式的
**历史抓取行为、wire 行为完全一致**，只是诊断可见性不同。`describeLastEnhance()` 在生产模式下（未带 `--debug`）**整个不挂载**，
快照本身在模块内存里也保持私有。

```bash
python3 -m installer install --i-accept-experimental-risk --debug
```

跟 `--fs-probe` 互相独立，可以叠加传。
