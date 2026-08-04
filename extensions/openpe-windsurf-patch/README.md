# openpe-ide-patch

> **实验性 — 用户自担全部风险**
>
> 该子项目是 profile-gated 的 Electron bundle patcher。Canonical 入口是
> `openpe-ide-patch`；`openpe-windsurf-patch` 暂时保留为强制绑定 legacy
> Windsurf profile 的兼容入口。
>
> 正式默认集成不是 bundle patch：
>
> - Devin CLI / Devin Local：`openpe devin hook install`
> - legacy Windsurf Cascade：`openpe windsurf hook install`
>
> Bundle patch 只提供显式 opt-in 的输入框按钮，不替代 native hook，不修改
> bundled `devin.exe`，不拦截 ACP。

## 风险

1. 修改 Electron bundle 可能影响 EULA、厂商支持与完整性基线。
2. macOS 重签不能恢复 vendor 原签名，因此当前 mutation 被禁用。
3. IDE 更新会覆盖 patch；旧 build backup 绝不能恢复到新 build。
4. Bootstrap 会把本地 bearer token 快照写入应用 bundle；server 必须只监听
   loopback，CORS 必须使用 profile 的精确 origin。Exact Devin 入口轮换 token 后
   必须 restore + reinstall；canonical refresh 当前不对该入口生效。
5. 私有 DOM、editor 与 storage 契约会随上游更新变化。

## 当前支持矩阵

| Profile | `status` / `doctor` | Mutation | Runtime |
|---|---|---|---|
| `devin-desktop` | 可识别产品身份与 build | 仅 Windows 1.110.1 / `0d4bf12...` exact build 开放独立 `multi_bundle_patch` 入口；canonical `install/uninstall` 仍禁用 | 2026-07-15 实机确认 Origin、按钮请求与 editor 回填；固定 `history=none` |
| `windsurf-legacy` | 可识别并保留 allowlisted `1.110.1/8636ab5...` 基线 | 当前禁用 | 已有 Cascade runtime 资产；等待 crash recovery 与条件回滚闭环 |
| unknown product | 只读报告 unsupported | 拒绝 | 不按目录名猜宿主 |

“当前 Devin exact build 已通过一次实机 E2E”不等于未来 Devin 版本通用支持。
Build version、commit、workbench、sessions、sessions HTML 或 product 任一 baseline
变化时，独立入口都会 fail closed。正式默认集成仍是 native Devin hook。

## 安全模型

- 产品由 `product.json` 的 `nameShort`、`applicationName`、`dataFolderName`、
  `urlProtocol`、`version` 和 `commit` 共同裁决；`--app-dir` 只覆盖位置。
- 首次 install 前校验 product 记录的 bundle checksum 与 live bundle 一致。
- Canonical 单 bundle 路径的 transaction manifest 绑定 profile、install root、
  product version/commit、bundle/product 原始与 patched SHA-256。
- Exact-build Devin 入口在单个 manifest 中绑定 `sessions`、`workbench`、
  `sessions.html` 与 `product.json` 四个 artifact 的原始/patched SHA-256；写前逐个
  验证 backup，失败时只覆盖仍处于已知 before/target 状态的文件。
- Canonical refresh 只复用 marker 指向的 exact transaction；exact-build Devin
  入口当前不提供 refresh，token 变化时需先 exact restore 再 install。
- 恢复只接受同 profile、同 install、同 product build、同路径且 checksum 完整的
  transaction。任一 live/backup 状态未知都拒绝覆盖。
- Mutation 前要求 IDE/updater 已停止；进程探针失败时 fail closed。

## 系统要求

- Python 3.8+
- 从源码构建 payload 时需要 Node.js 18+ 与 npm
- 按钮需要运行中的 loopback `openpe-server`。Exact-build Devin 使用已实测的
  `vscode-file://vscode-app` Origin：

  ```dotenv
  OPENPE_SERVER_TOKEN=<stable-64-hex-token>
  OPENPE_SERVER_LIFECYCLE_ENABLED=true
  OPENPE_SERVER_CORS_ORIGINS=vscode-file://vscode-app
  ```

不要为 Devin 猜测 `app://devin` 或放宽为 `*`。如果同一 server 还服务 legacy
Windsurf，可显式合并为
`null,app://windsurf,vscode-file://vscode-app`；不需要的 Origin 不应加入。

## 注意事项与已知限制

### Devin 上下文与 legacy Windsurf 隔离

Exact-build Devin bootstrap 固定 `client=devin`、`mode=agent`、`historySource=none`。
Inject 不会启动 legacy Windsurf IndexedDB trajectory watcher，请求只携带当前输入框
prompt；当前也不传 native Devin session history 或 workspace `cwd`。这避免把
Windsurf trajectory 错配到 Devin，但意味着按钮增强没有 native hook 路径可获得的
会话历史，也不会触发依赖 `cwd` 的 Openace 检索。

### Legacy Windsurf 对话历史抓取是 best-effort（只能拿到当前 trajectory）

点击 legacy Windsurf 的 openPE 按钮时，若有缓存到消息，payload 会把它们以 `history`
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
preview）。调试入口见 `inject/README.md` § "Dev/test 诊断网关
(`--debug`)"；公开 README 只保留可复现的结论，原始调查日志属于本地私有
治理资产。

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
investigation，**没有**进入计划阶段。后人接手时应先重新用 DevTools
验证 IDB topology 和 `SendUserCascadeMessage` body-shape，避免把同一批
死胡同再趟一遍。

## 使用方式

### 正式推荐路径

Bundle patch 不是默认集成。Devin 用户优先使用：

```bash
openpe devin hook install
```

Canonical 入口继续提供只读诊断；其 `install/uninstall` 仍保持 fail closed：

```bash
python -m installer.cli status --host auto --app-dir "C:\path\to\IDE"
python -m installer.cli doctor --host auto --app-dir "C:\path\to\IDE"
```

### Windows exact-build Devin 按钮

当前独立入口只接受以下 baseline：

- Devin Desktop `1.110.1`
- product commit `0d4bf12ed4a7597cb8ae9016fe8474468aad98a2`
- Windows x64；其它系统、架构和 build 一律拒绝
- renderer Origin `vscode-file://vscode-app`
- descriptor base URL 必须是精确的 `http://127.0.0.1:<port>`；CSP 只加入该端口

所有安装、恢复和 server 命令都应在 **Devin 外部的 Windows Terminal / PowerShell**
执行，不要使用即将被关闭的 IDE 集成终端。磁盘 bundle 变更后必须完整冷启动 Devin；
`Developer: Reload Window` 不会重启 Electron 主进程，也不会刷新主进程已缓存的
integrity 表。

#### 准备 payload 与 server

在外部 PowerShell 进入仓库：

```powershell
cd "C:\path\to\openpe"
git pull --ff-only origin main
go build -o openpe-server.exe ./cmd/openpe-server
cd extensions\openpe-windsurf-patch
npm --prefix inject ci
npm --prefix inject run check
npm --prefix inject run build
```

在 `~/.config/openpe/.env` 配置 provider，并加入本页“系统要求”中的 stable token、
lifecycle 和 Devin CORS。在另一个外部 PowerShell 窗口启动 server：

```powershell
cd "C:\path\to\openpe"
$env:OPENPE_ENV_FILE = "$HOME\.config\openpe\.env"
.\openpe-server.exe
```

该窗口应持续运行并显示监听 `127.0.0.1:18980`（或你的精确 loopback 端口）。

#### 原厂 baseline 首次安装

1. 完整退出 Devin Desktop，并从托盘退出 `WindsurfGate`；确认任务管理器中没有
   `Devin.exe`、bundled `devin.exe`、`WindsurfGate.exe` 或 `inno_updater.exe`。
2. 在另一个外部 PowerShell 回到 patch 子项目并执行：

   ```powershell
   cd "C:\path\to\openpe\extensions\openpe-windsurf-patch"
   python -m installer.multi_bundle_patch `
     --app-dir "C:\path\to\IDE" `
     --payload "inject\dist\inject.js"
   ```

3. 保存命令在首个 live write 前输出的 transaction ID。它是该次安装唯一的恢复凭据。
4. 从正常快捷方式冷启动 Devin；不要用 Reload Window 代替冷启动。

#### 从已有 exact patch 升级

独立入口不支持原地 refresh。直接再次执行 install 会因 marker/trusted baseline 门禁
而失败；必须先恢复旧 transaction，再安装新 payload：

```powershell
cd "C:\path\to\openpe\extensions\openpe-windsurf-patch"

python -m installer.multi_bundle_patch `
  --app-dir "C:\path\to\IDE" `
  --restore "<old-transaction-id>"

python -m installer.multi_bundle_patch `
  --app-dir "C:\path\to\IDE" `
  --payload "inject\dist\inject.js"
```

两个命令之间不要启动 Devin。保存第二个命令输出的新 transaction ID，然后从正常
快捷方式冷启动。

#### 预期验收结果

- 冷启动后不再出现“Devin 安装似乎损坏”提示；该提示若仍出现，应立即 restore，
  不要继续扩大测试范围。
- 无附件、存在截图附件时，每个 prompt editor 都只出现一个 openPE 按钮；当前实现
  会按唯一 editor 分组，只选择最右侧可见的 Devin action 作为 anchor。
- 点击按钮后本地 authenticated `POST /v1/prompt-enhance` 返回成功，且仅在原 editor
  文本未变化时自动回填；请求期间用户继续编辑时只复制结果，不覆盖新输入。
- Devin 路径固定 `history=none`、`cwd=""`，不会混入 legacy Windsurf trajectory，
  也不会触发 Openace 代码检索。

2026-07-15 首轮实机已验证按钮、HTTP 200 和 editor 回填；随后发现运行中 patch 会让
主进程使用旧 integrity 表，并在截图附件场景产生重复 anchor。当前源码已改为冷安装
流程和“每 editor 仅最右 action”选择，仍需按本节完成冷恢复/重装后的场景矩阵复验。

恢复前同样要完整退出 Devin/WindsurfGate；只允许恢复该 install 输出的 exact
transaction。IDE 更新后不得把旧 transaction 恢复到新 build；跨 build、路径、
manifest、live SHA 或 backup SHA 任一不一致都会 fail closed。Legacy timestamp
backup 只作为审计资产，不参与该恢复路径。

### 消费层 token 预算 (`--max-context-tokens` / `OPENPE_MAX_CONTEXT_TOKENS`)

Codex / Claude / Windsurf hook 会把 `OPENPE_MAX_CONTEXT_TOKENS` 写入每次
`enhancer.Request.Options`。按钮虽然共享同一个 `openpe-server` 增强管线，但预算
是请求字段，不是 server 自动套用的全局默认。

当前 exact-build Devin 入口没有 `--max-context-tokens` 参数，也不会从 dotenv
快照该字段，因此按钮请求不发送 `options.max_context_tokens`。仅在 server 进程中
设置同名变量不会给按钮请求启用预算；后续将该选项接入多 renderer transaction 并
完成 wire 测试前，不能宣称按钮与 hook 共享该配置。

注意区分**消费层** vs **采集层**：

- **消费层**（这里描述的 `--max-context-tokens`）是面向用户的 token 预算，
  统一控制 server prompt 拼装。
- **采集层**——cascade trajectory history 抓取的 32 条消息 / 6000 字符每
  条 / 80000 字符总预算——是基于真实 Windsurf trajectory 经验调优的常量，
  硬编码在 `inject/src/cascade_context.ts::DEFAULT_HISTORY_BUDGET`，**不
  暴露成可配**。改这三个值需要修改源码并重新构建 inject payload。

### Openace 代码检索上下文（按钮路径当前需要 workspace CWD）

保留的按钮 wire 设计走本地 `openpe-server` 的 `POST /v1/prompt-enhance`，与
CLI / hook 入口共享同一套 `enhancer.Service` 构造逻辑。Openace context
provider 在处理请求的进程启动或构造 service 时注入，所以按钮路径无需
patch installer 侧再配置任何 Openace 相关变量；但是否真的发起代码检索，
还取决于按钮请求是否带有非空 workspace `cwd`。

例：

```bash
# 启动 openpe-server 时显式启用 Openace（installer 不感知此变量）
OPENPE_SERVER_TOKEN="<stable-64-hex-token>" \
OPENPE_SERVER_LIFECYCLE_ENABLED=true \
OPENPE_SERVER_CORS_ORIGINS=vscode-file://vscode-app \
OPENPE_OPENACE_ENABLED=true \
OPENPE_OPENACE_ADDR=127.0.0.1:8765 \
openpe-server
```

前提：本机 `openace-mcp` daemon 已运行且 8765 端口可联。详见主仓
README § "[Openace 代码检索上下文](../../README.md#openace-代码检索上下文)" 的前置依赖清单。

`enhancer.Service` 只有在请求带有非空 `Request.CWD` 时才会向 Openace
daemon `POST /v1/retrieve` 检索一次，并把结果写入
`Request.Context.Retrieval`。当前按钮 inject 层还没有可靠获取 Cascade
workspace 路径，因此发给 server 的 `cwd` 为空；这意味着即使
`openpe-server` 启用了 Openace，按钮路径也会按“无 workspace”处理并跳过
代码检索。后续若补齐 workspace CWD 传递，Openace query 会把
`Request.Client`、`Request.Mode` 和 `Request.CWD` 一并写入
`information_request`，其中代码库定位仍以 `Request.CWD` 作为 daemon 检索目录。

## 测试

本地测试资产按仓库治理规则位于 ignored `tests/`：

```bash
python3 -m compileall installer
python3 -m unittest discover -v tests
cd inject
npm ci
npm run check
npm run build
```

Python installer 不需要第三方运行时依赖。发布 wheel 必须使用单一构建入口，它会
重建 payload 并检查 wheel 中的 payload 和两个 console entry：

```bash
python3 scripts/build_package.py
```

Python package distribution 在兼容期仍名为 `openpe-windsurf-patch`；canonical
console command 已是 `openpe-ide-patch`，旧 data namespace 保留供已安装版本升级。

## 架构

Installer 复用主仓 `internal/integration/` 的 marker/atomic-write 契约，但宿主
差异只放在 Python/TypeScript profile：

- `installer/profiles.py`：manifest identity、build/runtime policy、CORS、wire、process。
- `installer/paths.py`：位置探测、install identity、transaction namespace。
- `installer/backup_transaction.py`：canonical 成对 backup、manifest、refresh、restore。
- `installer/multi_bundle_patch.py`：exact-build Devin 四 artifact transaction、条件写入与恢复。
- `installer/cli.py`：canonical `openpe-ide-patch`。
- `installer/compat_windsurf.py`：强制 legacy Windsurf profile 的兼容 shim。
- `inject/src/auth.ts`：bootstrap profile fail-closed。

Devin native hook、history、dedup 与 `additionalContext` 继续由主 Go adapter
负责，不复制到 patch installer。

## 许可证

MIT — 见 [`LICENSE`](LICENSE)。
