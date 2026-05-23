# openpe-windsurf-patch

> **⚠️ 实验性 — 用户自担全部风险 ⚠️**
>
> 该子项目会**就地修补 Windsurf IDE 的 Electron bundle**，在 Cascade
> 聊天输入工具栏上注入一个 openPE logo Enhance 按钮。它是**显式
> opt-in**、**默认关闭**、与 openPE 主 hook 路径**完全独立**。
>
> 运行 installer 即表示你确认：
>
> 1. **EULA 风险** — 修改 Windsurf 应用 bundle 可能违反 Windsurf /
>    Codeium 终端用户许可协议。你的账号可能被暂停或拒绝技术支持。
>    安装前请阅读当前 EULA。
> 2. **代码签名风险** — 在 macOS 上，重签修补后的 bundle 会使 Apple
>    notarization 失效；Gatekeeper 可能拒绝启动 IDE，直到手动移除
>    quarantine 属性。
> 3. **绕过 checksum** — `product.json` 会被修改以满足 Electron 的资源
>    完整性校验；这会**仅**禁用修补文件这一项安全网。
> 4. **升级脆弱性** — Windsurf 每次更新都会覆盖修补后的 bundle；你
>    必须在每次 IDE 升级后重新跑 `install`。之前的备份会保留，但
>    可能与新版 IDE 不匹配。
> 5. **无任何担保** — installer 按 AS IS 提供。出问题请从备份还原
>    （或重新干净安装 Windsurf）。
>
> **如果以上任何一条无法接受，请改用 openPE 默认集成路径：**
>
> - `openpe windsurf hook install`（终端 `pe ...` 关键字）——跨平台默认推荐。
>
> （早期的 VSIX 编辑器命令插件路径 `extensions/vscode-openpe/` 已整目录删除，
> 历史原因见主仓 README “VSIX 编辑器命令路径” 节。）

---

## 这是什么

一个独立的、MIT 许可的 installer，它会：

- **修改** Windsurf 应用 bundle 内的 `workbench.desktop.main.js`，注入一
  段约 30 KB 的 JavaScript payload。
- 注入的 payload 用 `MutationObserver` 监听 Cascade 聊天输入工具栏，在
  Submit 旁边添加一个 openPE logo 按钮，点击后调用**你本地**的
  `openpe-server`（仅 loopback），并把增强后的 prompt 写回 Cascade 输入
  框。按钮 SVG 图案已完整 inline 到 `inject/src/button.ts`，注入 payload
  为自包含，不依赖任何外部 SVG 文件。
- 不与 `127.0.0.1` 之外的任何地方通信。无 telemetry、无第三方 gate
  server、无商业 license key。

## 这不是什么

- **不是** WSE（`windsurf-enhance`）的 fork — 不共享代码、不共享 key、
  不共享 server。只借用“修补 bundle 加按钮”这个公开思路。
- **不是** openPE hook 路径的替代。hook 仍然是 EULA 安全、跨平台稳定的默认选项。
- **不会**自动更新。Windsurf 升级 ⇒ 重新跑 `install`。
- **不是** openPE 主 Go build 的一部分。它住在自己的子项目里，有自己的
  Python + Node.js 工具链。

## 系统要求

- Python 3.8 或更新版本
- （可选，从源码构建 inject.js 用）Node.js 18+ 和 npm
- 一个启用了 lifecycle descriptor 的运行中 `openpe-server`。对于按钮
  路径，推荐固定本地 token，让按钮在 server 重启后仍能用：

  ```bash
  # 生成一次后持续复用，例如放在 ~/.config/openpe/.env。
  export OPENPE_SERVER_TOKEN="<stable-64-hex-token>"
  export OPENPE_SERVER_LIFECYCLE_ENABLED=true
  export OPENPE_SERVER_CORS_ORIGINS=null,app://windsurf
  openpe-server
  ```

  installer 会读 `~/.config/openpe/server.json` 拿到 loopback base URL 和
  bearer token，再把它们快照进修补后的 bundle。

## 当前状态

- **现状**：`status`、`install`、`uninstall`、`doctor` 都是真实可用的命令。
  `install` 会解析 Windsurf bundle、通过 `GET /v1/info` 校验本地
  `openpe-server` descriptor、备份 bundle 和 `product.json`、注入构建好的
  `inject/dist/inject.js` payload 和 `globalThis.__openpe` bootstrap、移除
  bundle 的 checksum 条目、并在 macOS 上重签 app。
- 注入的 payload 会监听 Cascade 工具栏、添加 openPE logo 按钮、调本地
  `POST /v1/prompt-enhance`、预览原文/增强文本、并尽力把增强后的
  prompt 写回 Cascade 输入框。
- **平台端到端验证状态**：目前**只在 Windows 本地 Windsurf 上验证过完整
  install → 按钮注入 → 增强 → 回填 → uninstall 流程**。`installer/paths.py`
  代码层面对 macOS（`/Applications/Windsurf.app`）和 Linux（`/opt/Windsurf`
  等）都有路径探测分支，但**尚未做端到端验证**，macOS 的 codesign
  重签、权限处理、bundle 布局差异、以及 Cascade DOM 选择器适配都可能
  需要进一步调整。**Remote 场景明确不支持**——Windsurf IDE 在
  Windows/macOS 本地运行，remote Linux 服务器找不到本地 IDE bundle，
  无法自然完成本地 IDE 注入。
- 仍然是实验性、默认关闭的，因为它会修改 Windsurf 应用 bundle，并依赖
  Cascade 的私有 DOM 选择器。

工作日志详见主 openPE 仓库的
`docs/development/2026-05-22-windsurf-patch-installer.md`。

## 注意事项与已知限制

### 对话历史抓取是 best-effort（只能拿到当前 trajectory）

点击注入的 openPE 按钮时，若有缓存到消息，payload 会把它们以 `history`
字段附在本地 `POST /v1/prompt-enhance` 请求里。该字段由
`inject/src/cascade_context.ts` 里的 renderer 侧 watcher 提供：watcher
监听 Windsurf 的 IndexedDB（`keyval-store` /
`windsurf:cache:cachedActiveTrajectory:<workspace>`）里**当前进行中那个
trajectory** 的完整字节，按尾切片返回——最多 32 条消息，每条截到 6 000
字符，总字符数硬上限 80 000（超额时优先丢最旧的）。

它**抓不到**同一 chat session 里早期已经结束的 task。Phase 5 bring-up
（2026-05-22）逆向了 renderer 可见的状态，给出了原因：

| 客户端数据源 | 是否含完整多轮内容 |
|---|---|
| `cachedActiveTrajectory:<workspace>`（IDB） | 是——只有最近一个 trajectory；Cascade 起新 task 时被覆盖 |
| `cachedTrajectorySummaries:*`（IDB） | 否——只有 trajectory ID / 标题；无 `user_input` / `planner_response` 字段 |
| `SendUserCascadeMessage` RPC 请求 body（`localhost:<port>/exa.language_server_pb.LanguageServerService/...`） | 否——实测每轮约 458 字节，仅含 IDs + 新一条 user message |

完整 session 状态保存在本地 **Codeium daemon 进程**里（`localhost:<port>`
上的 `exa.language_server_pb` 那批 Connect-RPC / gRPC-web 服务），**不在
任何 renderer 可访问的存储中**。Cascade 没有 `~/.codex/history.jsonl`
那种本地全文 transcript 文件，所以本 patch **纯 renderer 侧无法**给出
codex CLI / Claude CLI 级别的完整历史上下文。

这个限制是**可观测**的，不是被掩盖的：用 `--debug` 安装后，
`window.__openpeDebug.describeHistory()` 会显示 `source:
'latest_trajectory' | 'none'` 以及形状级统计（counts、roles、80 字符
preview）。完整调查日志见 `inject/README.md` § "Dev/test 诊断网关
(`--debug`)" 和 `docs/architecture.md` § "Best-effort 抓取范围与 IDB
拓扑"。

### 后续调查方向 — 通过 daemon 响应流榨出 full session

如果 codex / Claude-CLI 级别的完整 transcript 成为硬需求，唯一还值得
试的客户端侧路径是 **streaming response tap**：在
`SendUserCascadeMessage` 端点上 hook `window.fetch`，读 chunked
protobuf 响应流，看 daemon 推回来的数据有没有超出"新 assistant 一轮"
的内容。实现规划是在 `cascade_context.ts` 旁边加一个 `fetch_tap.ts`
模块，新增 `HistorySource` 变体（`'fetch_tap'` 或 `'merged'`），全程
**不动 wire 契约**——`POST /v1/prompt-enhance` 的 `history` 字段
shape 不变，server、hook adapter、enhancer 都不需要改。

回收到有意义多轮数据的概率估计**偏低**（daemon 是 server-stateful 的，
没有任何架构动机往每轮响应里塞自己已有的状态），所以它停留在 open
investigation，**没有**进入计划阶段。后人接手时建议先看
`docs/architecture.md` 里 IDB topology 和 `SendUserCascadeMessage`
body-shape 探针的原始记录，避免把同一批死胡同再趟一遍。

## 使用方式

```bash
# 1. 安装前先启动 openpe-server，启用 lifecycle 和 CORS。
OPENPE_SERVER_TOKEN="<stable-64-hex-token>" \
OPENPE_SERVER_LIFECYCLE_ENABLED=true \
OPENPE_SERVER_CORS_ORIGINS=null,app://windsurf \
openpe-server

# 2. 修改 TypeScript 源码后，重新构建 inject payload。
cd extensions/openpe-windsurf-patch/inject
npm install
npm run build

# 3. 从子项目根目录安装。
cd ..
python3 -m installer doctor
python3 -m installer status
python3 -m installer install --i-accept-experimental-risk
```

为了能日常稳定使用，请**不要**每次启动 server 都重新生成
`OPENPE_SERVER_TOKEN`。生成一次后放在用户级 openPE env 文件或 shell
profile 里复用，这样已经安装的按钮在 `openpe-server` 重启后仍然保持鉴
权有效。如果浏览器 console 报 CORS 错误，在 Windsurf DevTools 里看请求
的 `Origin`，把这个精确 origin 加到 `OPENPE_SERVER_CORS_ORIGINS`。

安装后重启 Windsurf。openPE logo 按钮应该出现在 Cascade submit 控件旁
边。点它、查看增强后的 prompt、用 `Apply to input`。如果 Windsurf 改了
私有 DOM 导致按钮不出现，见 `inject/README.md`，扩充
`findCascadeToolbar()` / `SUBMIT_BUTTON_SELECTORS` 后重新构建
`inject/dist/inject.js`，再重跑 `install`。

所有子命令都接受 `--help`。`status` 和 `doctor --app-dir <path>` 还会汇报
`button config`。`stale` 结果表示安装时嵌入的 token 或 base URL 与当前
server descriptor 不一致；用同一个 `OPENPE_SERVER_TOKEN` 重启
`openpe-server`，或重跑 `install` 刷新 bundle bootstrap。

P3 descriptor-read 试验需要带 `--fs-probe` 安装；先用
`OPENPE_SERVER_LIFECYCLE_ENABLED=true` 启动 `openpe-server`，重启 Windsurf
后点 openPE 按钮，在 DevTools 看 `[openpe-fs-probe]` 日志。该探针只输出非
敏感的 descriptor 元信息和 renderer 能否通过 Node `fs` 读到 0600
descriptor 的状态；它还没改 token 传输方式。

## 测试

```bash
python3 -m unittest discover -v tests
```

不需要任何第三方 Python 依赖。

## 架构

见 [`docs/architecture.md`](docs/architecture.md)。简要：installer 实现了主
openPE 仓库 `internal/integration/` 包定义的 `Injector` 和
`BundlePatcher` 契约，所以同样的模式可以通过新增同级子项目复用到未
来的 IDE（Cursor、VS Code Composer）。

## 许可证

MIT — 见 [`LICENSE`](LICENSE)。
