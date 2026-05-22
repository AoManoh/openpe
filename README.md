# openPE

`openPE`（open prompt-enhancer）是一个本地优先的 prompt 增强工具链。你在 Codex CLI、Claude Code、Windsurf Cascade 中输入 `pe <你的需求>`，openPE 会先调用一次 OpenAI-compatible 模型把原文改写成更适合编码代理执行的 prompt，复制到剪贴板，再让你粘贴、按需编辑、正常发送。

- **本地优先**：数据流经你自配的 OpenAI-compatible endpoint（OpenAI、阿里云 DashScope、火山引擎、自建网关均可），不经第三方中转。
- **Hook-first**：通过宿主公开的 `UserPromptSubmit` / `pre_user_prompt` hook 协议接入，不替换、不代理、不劫持其它请求。
- **统一交付**：一条命令为 Codex / Claude Code / Windsurf 安装 hook，三方共用同一个 enhancer 和缓存模型。

## 为什么需要它

- 你写的原始 prompt 经常缺少上下文、约束或验证期望，导致 coding agent 走偏。
- 你已经在用 Codex / Claude Code / Windsurf，不想换主力工具，只想给输入加一层"自动改写"。
- 你想自己决定改写质量与成本，不被任何厂商的私有 prompt 绑死。

## 快速开始

需要 Go 1.23+。

### 1. 构建并安装 binary

```bash
# 推荐：克隆后本地构建
git clone https://github.com/AoManoh/openpe.git
cd openpe
go install ./cmd/openpe ./cmd/openpe-server   # openpe-server 可选，仅用于 HTTP 调试
```

确认 `$GOPATH/bin`（通常 `~/go/bin`）在 `PATH` 中：

```bash
openpe -h
```

### 2. 配置 OpenAI-compatible endpoint

推荐放在 `~/.config/openpe/.env`（hook 默认从这里加载）：

```bash
mkdir -p ~/.config/openpe
cat > ~/.config/openpe/.env <<'EOF'
OPENPE_BASE_URL=https://api.openai.com
OPENPE_API_KEY=replace-with-your-api-key
OPENPE_MODEL=gpt-4o-mini
OPENPE_LANGUAGE=zh
EOF
```

`OPENPE_BASE_URL` 支持 OpenAI 官方、阿里云、火山引擎、Openace/Grok2api 等任意 OpenAI-compatible 网关；带或不带 `/v1` 结尾都可以。

验证 endpoint 连通：

```bash
openpe enhance --prompt "帮我重构这个 handler"
```

正常返回增强 prompt 即说明 endpoint / key / model 三项可用。

### 3. 给目标客户端安装 hook（任选一个或全装）

```bash
openpe codex hook install      # → ~/.codex/hooks.json
openpe claude hook install     # → ~/.claude/settings.json
openpe windsurf hook install   # → ~/.codeium/windsurf/hooks.json
```

> Codex 装完还需要在 TUI 内执行 `/hooks` 并 trust；Windsurf 装完建议重启 IDE。详见 [客户端配置](#客户端配置)。

### 4. 在客户端对话框输入 `pe <你的需求>`

```text
pe 帮我把这个 Go 测试改成 table-driven
```

openPE 会阻断这条原始消息（**不会**发给 LLM），把增强结果复制到系统剪贴板，并给出短状态。

**直接 Ctrl+V 粘贴到原输入框，按需编辑，再发送即可。**

> 若 stderr 显示"剪贴板未更新"——见 [注意事项与已知限制](#注意事项与已知限制)。

## 服务配置

openPE 不需要常驻服务进程：hook 在每次 `pe` 调用时按需启动子进程，完成后退出。HTTP server (`openpe-server`) 只在自动化集成场景下需要，见 [HTTP 与裸 CLI 调试入口](#http-与裸-cli-调试入口)。

### 环境变量

所有运行参数都通过环境变量传入。优先级：**shell 环境变量 > dotenv 文件**。dotenv 文件按以下顺序解析：

1. `OPENPE_ENV_FILE` 指向的文件（hook 安装时自动注入，见各客户端段）。
2. 当前工作目录下的 `.env`（裸 CLI 和 server 启动时读取）。

| 变量 | 默认 | 说明 |
|---|---|---|
| `OPENPE_BASE_URL` | `https://api.openai.com` | OpenAI-compatible host；带或不带 `/v1` 均可 |
| `OPENPE_API_KEY` | （必填） | API key |
| `OPENPE_MODEL` | （必填） | 模型名，如 `gpt-4o-mini`、`qwen-max`、`gpt-5.5` |
| `OPENPE_LANGUAGE` | `zh` | hook 终端反馈语言：`zh` / `en` |
| `OPENPE_TIMEOUT` | `60s` | 单次 provider 调用超时（Go duration） |
| `OPENPE_LISTEN_ADDR` | `127.0.0.1:18980` | `openpe-server` 监听地址；无 token 时只能绑定 `127.0.0.1` / `::1` / `localhost` |
| `OPENPE_SERVER_TOKEN` | 空 | 可选 HTTP bearer token；绑定非 loopback 地址时必填 |
| `OPENPE_SERVER_CORS_ORIGINS` | 空 | 可选 CORS Origin allowlist，逗号分隔；空值禁用 CORS |
| `OPENPE_SERVER_LIFECYCLE_ENABLED` | `false` | 是否写入本地 server descriptor，供 IDE 注入安装器发现 endpoint/token |
| `OPENPE_SERVER_DESCRIPTOR_FILE` | `~/.config/openpe/server.json` | lifecycle descriptor 路径覆盖，仅在 lifecycle 启用时使用 |
| `OPENPE_CACHE_DIR` | `os.UserCacheDir()/openpe`（Linux 通常 `~/.cache/openpe`） | hook 预览与纯文本缓存根目录 |
| `OPENPE_COPY_COMMAND` | 自动探测 | 覆盖剪贴板命令；接收 stdin（如 `xclip -selection clipboard`） |
| `OPENPE_ENV_FILE` | hook 安装时注入 | hook 子进程加载的 dotenv 文件路径 |
| `OPENPE_OPENACE_ENABLED` | `false` | 是否启用 Openace 代码检索上下文 |
| `OPENPE_OPENACE_ADDR` | `127.0.0.1:8765` | Openace daemon 地址；也可沿用 `OPENACE_DAEMON_ADDR` |
| `OPENPE_OPENACE_TOKEN` | 空 | Openace daemon token；也可沿用 `OPENACE_DAEMON_TOKEN` |
| `OPENPE_OPENACE_PROVIDER_PROFILE_ID` | 空 | 可选 ACE provider profile |
| `OPENPE_OPENACE_MAX_OUTPUT_LENGTH` | `12000` | 单次 Openace 检索结果上限 |
| `OPENPE_OPENACE_TIMEOUT` | `30s` | 单次 Openace daemon HTTP 调用超时 |
| `OPENPE_OPENACE_MAX_RETRIES` | `2` | 临时错误最大重试次数，实际总尝试次数为 `1 + max_retries` |
| `OPENPE_OPENACE_RETRY_BASE_DELAY` | `250ms` | Openace 重试指数退避起始延迟 |
| `OPENPE_OPENACE_RETRY_MAX_DELAY` | `2s` | Openace 单次重试最大等待 |
| `OPENPE_OPENACE_RETRY_JITTER` | `100ms` | Openace 重试抖动上限 |

`.env.example` 含常用模板，可 `cp .env.example ~/.config/openpe/.env` 后改值。

### Openace 代码检索上下文

Openace 是可选 context provider。启用后，openPE 会在调用 prompt rewrite 模型前，基于 `prompt`、目标客户端、模式和 `cwd` 向本机 Openace daemon `POST /v1/retrieve` 发起一次代码检索，并把返回内容写入 canonical request 的 `context.retrieval`。如果调用方已经显式传入 `context.retrieval`，openPE 不会重复检索。

```bash
OPENPE_OPENACE_ENABLED=true
OPENPE_OPENACE_ADDR=127.0.0.1:8765
openpe enhance --prompt "帮我修复 provider 超时重试" --cwd /path/to/repo
```

Openace 临时错误只会有限重试：HTTP `408`、`429`、`499`、`5xx`、网络超时和短暂连接错误会按指数退避加抖动重试；`400`、`401`、`403`、`404` 等配置、权限或请求错误不会重试。超过最大重试次数后，openPE 返回清晰错误，不静默降级为无检索上下文。

## 客户端配置

所有客户端都以 hook 形式接入；安装一次后在客户端对话框输入 `pe <内容>` 即可触发。

### Codex CLI

已验证 Codex CLI `0.132.0`。

```bash
# 全局安装（推荐）
openpe codex hook install

# 项目级安装
openpe codex hook install --scope project

# 自定义 dotenv 位置
openpe codex hook install --env-file /absolute/path/to/.env

# 只预览生成的 hooks.json，不写盘
openpe codex hook install --dry-run
```

| 选项 | 默认 | 说明 |
|---|---|---|
| `--scope` | `user` | `user` → 写 `~/.codex/hooks.json`；`project` → 写 `<cwd>/.codex/hooks.json` |
| `--path` | 自动 | 显式 hooks.json 路径（覆盖 scope 推断） |
| `--env-file` | user 模式：`~/.config/openpe/.env`；project 模式：`<cwd>/.env` | hook 子进程加载的 dotenv |
| `--openpe-bin` | `PATH` 中的 `openpe` 或当前可执行文件 | hook 命令中的 openpe binary 绝对路径 |
| `--hook-timeout` | `120` | Codex hook 超时秒数 |

**关键步骤**：安装或修改后，在 Codex TUI 内执行 `/hooks`，review 并 trust 这个 hook。**Codex 会忽略未信任的 hook**。

**关键注意事项**：

- Codex hook 输入里的 `cwd` 来自当前 Codex session，影响 enhancer 推断项目。处理 openPE 自己时请从 `/home/oh/projects/openPE` 启动 Codex，或用 `codex -C /home/oh/projects/openPE`。
- Codex TUI 把 captured hook feedback 压成单行，stderr 只输出短状态。完整预览见 [调用方式](#调用方式) 中的 `openpe codex hook last`。
- 同时安装 user + project hook 会触发两次执行；openPE 在 project hook 安装器中会检测 user hook 并自动跳过去重。
- Codex CLI `0.132.0` 的 `/` 菜单只枚举内置命令，不会列出 `~/.codex/prompts/*.md` 或自定义 commands；openPE 当前**不规划** `/pe` slash command，正式入口保持 hook 触发。

### Claude Code

已验证 Claude Code CLI `2.1.146`。

```bash
# 全局安装
openpe claude hook install

# 自定义 dotenv 位置
openpe claude hook install --env-file /absolute/path/to/.env

# 显式 settings.json 路径
openpe claude hook install --path /absolute/path/to/settings.json

# 只预览生成的 settings.json，不写盘
openpe claude hook install --dry-run
```

| 选项 | 默认 | 说明 |
|---|---|---|
| `--path` | `~/.claude/settings.json` | Claude settings 路径 |
| `--env-file` | `~/.config/openpe/.env` | hook 子进程加载的 dotenv |
| `--openpe-bin` | `PATH` 中的 `openpe` | hook 命令中的 openpe binary 绝对路径 |
| `--hook-timeout` | `120` | Claude hook 超时秒数 |

**关键步骤**：安装后重启 Claude Code 让设置生效。

**关键注意事项**：

- Claude Code `--print` headless 模式会执行 hook，但**不像交互式 TUI 一样稳定展示被阻断 feedback**；调试请用交互式模式。
- Claude Code 自身调哪个模型由 Claude Code 决定，openPE 只负责增强 prompt。若想让 Claude Code 走 Anthropic-compatible 第三方网关：

  ```bash
  export ANTHROPIC_BASE_URL="https://your-anthropic-compatible-host"
  export ANTHROPIC_API_KEY="your-api-key"
  export ANTHROPIC_MODEL="your-model"
  claude --model your-model
  ```

- Claude Code CLI `2.1.146` 暴露的 `--effort` 取值是 `low` / `medium` / `high`；1M context window 属于上游模型/网关能力，不是 openPE hook 能强制开启的选项。

### Windsurf Cascade

```bash
# 全局安装（推荐）
openpe windsurf hook install

# 项目级安装
openpe windsurf hook install --scope project

# 自定义 dotenv 位置
openpe windsurf hook install --env-file /absolute/path/to/.env

# 只预览生成的 hooks.json，不写盘
openpe windsurf hook install --dry-run
```

| 选项 | 默认 | 说明 |
|---|---|---|
| `--scope` | `user` | `user` → 写 `~/.codeium/windsurf/hooks.json`；`project` → 写 `<cwd>/.windsurf/hooks.json` |
| `--path` | 自动 | 显式 hooks.json 路径（覆盖 scope 推断） |
| `--env-file` | user 模式：`~/.config/openpe/.env`；project 模式：`<cwd>/.env` | hook 子进程加载的 dotenv |
| `--openpe-bin` | `PATH` 中的 `openpe` | hook 命令中的 openpe binary 绝对路径 |

**关键步骤**：安装后**重启 Windsurf 或重新打开当前 workspace** 让 hook 生效。

**关键注意事项**：

- Windsurf 公开 hook 协议仅证明可阻断原 prompt，**无法替换 Cascade 输入框内容**。openPE 因此采用"阻断 + 缓存 + 复制"模式。
- Windsurf hook 子进程**没有控制 TTY**，OSC52 剪贴板兜底**必然失败**。本地命令（`wl-copy` / `xclip` / `pbcopy` / `clip.exe`）可用时复制仍能成功；不可用时按 stderr 提示从 `last-prompt.txt` 文件取回。详见 [注意事项与已知限制](#注意事项与已知限制)。

### VS Code / Windsurf / Cursor VSIX 插件

openPE 也提供一个 VS Code 兼容插件初版，面向 Windsurf、Cursor、VS Code 等现代 IDE 的普通编辑器场景。它不是 Chat / Composer / Cascade 的 pre-submit hook，不承诺替换 IDE 原生聊天输入框；它负责在 IDE 内采集选区、当前文件或用户输入，调用本地 openPE，并把增强结果展示给用户。

```bash
cd extensions/vscode-openpe
npm install
npm run compile
npm run package
```

生成的 `vscode-openpe-*.vsix` 可手动安装到兼容 VSIX 的 IDE。常用命令：

- `openPE: Enhance Prompt`
- `openPE: Enhance Selection`
- `openPE: Enhance Current File`

默认传输方式是 `openpe enhance --json`。如果需要把当前文件内容作为结构化上下文传给 core，可启动 `openpe-server` 并把插件配置 `openpe.transport` 改为 `http`。更多说明见 [extensions/vscode-openpe/README.md](extensions/vscode-openpe/README.md)。

## 调用方式

### 基本流程

在任意已装 hook 的客户端对话框输入：

```text
pe 帮我把当前文件的 if-else 改成 early return
```

- 触发关键字只接受 `pe`（兼容 `pe:` / `pe：` 作为分隔符）。
- openPE 阻断这条原始消息，**不会**发给 LLM；增强结果通过剪贴板交付。
- stderr 给出短状态，说明复制是否成功、缓存文件在哪。

### 复制成功时

直接在原输入框 Ctrl+V 粘贴 → 编辑 → 发送。

### 复制失败时（IDE 子进程、纯 TTY、远程 Linux）

stderr 会同时给出 `last-prompt.txt` 绝对路径和回退命令。任选一种取回：

```bash
# 方式 1：直接打开缓存文件
cat ~/.cache/openpe/<client>/last-prompt.txt   # client = codex / claude / windsurf

# 方式 2：通过 CLI 打印
openpe codex hook last --prompt
openpe claude hook last --prompt
openpe windsurf hook last --prompt
```

### 查看完整 Markdown 预览（含元数据）

```bash
openpe codex hook last
openpe claude hook last
openpe windsurf hook last
```

### 查看缓存路径

```bash
openpe <client> hook last --path           # Markdown 预览路径
openpe <client> hook last --path --prompt  # 纯文本 prompt 路径
```

## HTTP 与裸 CLI 调试入口

仅用于测试、调试或自动化集成，**不是日常正式交互方式**。日常请用 hook 模式。

### HTTP server

```bash
openpe-server                                  # 监听 OPENPE_LISTEN_ADDR，默认 127.0.0.1:18980
OPENPE_SERVER_TOKEN=... openpe-server --listen 0.0.0.0:9000  # 非 loopback 监听必须启用 bearer auth
openpe-server --base-url ... --api-key ... --model ... --timeout 90s   # 命令行覆盖配置
```

安全边界：

- 未设置 `OPENPE_SERVER_TOKEN` 时，server 只允许绑定 `127.0.0.1`、`::1` 或 `localhost`；`0.0.0.0`、`::`、LAN IP 和其它主机名会拒绝启动。
- 设置 `OPENPE_SERVER_TOKEN` 后，`/v1/*` 请求必须带 `Authorization: Bearer <token>`；`/healthz` 始终免鉴权。
- provider / Openace / 内部错误对 HTTP 客户端脱敏，响应只包含稳定错误文案和 `request_id`；完整错误写入 server 日志。

可用路由：

```bash
# 健康检查
curl http://127.0.0.1:18980/healthz
# → {"status":"ok"}

# 增强请求（请求体上限 2 MiB，拒绝未知字段）
curl http://127.0.0.1:18980/v1/prompt-enhance \
  -H 'content-type: application/json' \
  -d '{"prompt":"帮我实现 openPE MVP","client":"codex","mode":"agent"}'
```

响应会返回 `enhanced_prompt`，并在 `metadata.sections` 中给出本次 prompt assembly 的 section 级诊断：

```json
{
  "enhanced_prompt": "请在当前仓库中实现 openPE MVP...",
  "warnings": [],
  "metadata": {
    "used_context": ["context.retrieval"],
    "sections": [
      {"name": "original_prompt", "length": 31, "truncated": false},
      {"name": "context_retrieval", "length": 1200, "truncated": false}
    ],
    "provider": "openai-compatible",
    "model": "your-model"
  }
}
```

### 裸 CLI 增强

```bash
openpe enhance --prompt "帮我检查这个 Go 项目的测试失败"
openpe enhance --json --prompt "优化这个需求描述"   # 输出完整 JSON
```

### Codex exec 包装

```bash
openpe codex --dry-run --prompt "..."           # 只打印增强结果，不调 codex
openpe codex --prompt "..." --codex-arg --yes   # 增强后传给 codex exec
```

### 直接走 hook stdin（hook 自身测试）

```bash
printf '{"hook_event_name":"UserPromptSubmit","prompt":"pe fix this","cwd":"'"$PWD"'"}' \
  | openpe codex hook run

printf '{"agent_action_name":"pre_user_prompt","tool_info":{"user_prompt":"pe fix this"}}' \
  | openpe windsurf hook run
```

## 注意事项与已知限制

### 剪贴板交付

- **OSC52 在 IDE 子进程必然失败**：Windsurf、Cursor、VS Code 等 IDE 拉起 hook 子进程时不分配控制 TTY，OSC52 兜底会以 `open /dev/tty: no such device or address` 失败。这是协议与进程模型的硬限制，openPE 无法在自己侧修复。
- **Linux 纯 TTY / 远程 SSH 下系统剪贴板工具不可用**：`XDG_SESSION_TYPE=tty` 或 X server 不可达时 `xclip` / `xsel` 报 `Can't open display`；缺 `WAYLAND_DISPLAY` 时 `wl-copy` 不可用。此时复制必然失败，按 stderr 提示从 `last-prompt.txt` 取回。
- **macOS / Windows / 桌面 Linux 完整会话**：`pbcopy` / `clip.exe` / `wl-copy` / `xclip` 默认可用，复制稳定。
- **强警告文案**：失败时 stderr 首句固定为"剪贴板未更新，请勿直接粘贴旧内容"。**看到这句不要按 Ctrl+V**，先按 stderr 指示从缓存文件取回。

### Hook 阻断模型

- **hook 不能替换 IDE 输入框内容**：Codex / Claude Code / Windsurf 公开的 hook 协议均未承诺 prompt 替换或自动提交。openPE 的"阻断 + 缓存 + 复制"是当前唯一公开可行的模式。
- **多客户端 feedback 压平**：Codex TUI 和 Windsurf Cascade 都会把 hook stderr 压成单行；openPE 已把强警告放在首句，压平后仍能优先看到关键信息。
- **Claude Code `--print` headless 模式**：会执行 hook，但展示行为不稳定；测试请用交互式 TUI。

### IDE 失败排查流程

1. stderr 出现"剪贴板未更新，请勿直接粘贴旧内容" → **不要按 Ctrl+V**。
2. 打开 stderr 给出的 `last-prompt.txt` 绝对路径，或跑 `openpe <client> hook last --prompt`。
3. 复制到 IDE 输入框，按需编辑后再发送。
4. 桌面环境下 stderr 显示"剪贴板已更新"时，按常规 Ctrl+V 粘贴即可。

## 架构概览

```text
client / hook / HTTP
  -> adapter layer (codex / claude / windsurf / manual)
  -> enhancer.Request (prompt + client + mode + cwd + rules + history + context)
  -> prompt rewrite core
  -> OpenAI-compatible provider
  -> enhanced prompt
  -> delivery layer (clipboard + cache markdown + cache plain text)
```

主要模块：

| 路径 | 职责 |
|---|---|
| `cmd/openpe` | CLI 入口；正式路径是 `<client> hook install`，裸命令仅用于测试 |
| `cmd/openpe-server` | HTTP server 入口，暴露 `POST /v1/prompt-enhance` 和 `GET /healthz` |
| `internal/enhancer` | 核心 prompt rewrite 服务、canonical Request / Response 类型 |
| `internal/context/openace` | 可选 Openace daemon 检索 provider，负责 query 组织、结果格式化和临时错误重试 |
| `internal/providers/openai` | 最小 OpenAI-compatible `/v1/chat/completions` provider |
| `internal/adapters/codex` | Codex `UserPromptSubmit` hook 适配 |
| `internal/adapters/claude` | Claude Code `UserPromptSubmit` hook 适配 |
| `internal/adapters/windsurf` | Windsurf Cascade `pre_user_prompt` hook 适配 |
| `internal/adapters/manual` | `pe` 关键字解析 |
| `internal/adapters/clipboard` | 剪贴板复制 + OSC52 兜底 |
| `internal/adapters/preview` | Markdown 预览包装 |
| `internal/adapters/delivery` | 剪贴板复制 + 双缓存（Markdown 预览 + 纯文本）+ 失败 UX 文案的统一交付层，三方 hook 共用 |
| `internal/config` | `.env` 与环境变量读取 |
| `internal/server` | HTTP API、bearer 鉴权、CORS 中间件、`/v1/info` 端点、lifecycle descriptor |
| `internal/integration` | IDE patch installer 与 openpe-server 的握手契约：`LocalServerDescriptor`、token 工具、`BundlePatcher` |
| `extensions/vscode-openpe` | VS Code 兼容 VSIX 插件；调用 CLI/HTTP，负责 IDE 侧输入采集、预览、复制、插入和替换 |
| `extensions/openpe-windsurf-patch` | **实验性** Windsurf bundle 注入式安装器（独立 MIT 子项目，默认禁用，用户自担风险） |

### 增强契约（开发者参考）

核心增强逻辑通过 canonical `enhancer.Request` 接收 `prompt`、`client`、`mode`、`cwd`、`rules`、`history` 和 `context`。`client` / `mode` 只用于告诉模型目标运行环境，不让宿主的私有能力变成核心依赖。

增强结果必须满足：

- 保留用户原始意图、语言、显式约束和安全边界。
- 输出自包含、可粘贴、适合编码代理执行的 prompt。
- 不依赖宿主一定能替换输入框、追加隐藏上下文、保持剪贴板成功，或识别某客户端专有 slash command。
- 对 Windsurf / Cursor / VS Code / Composer / Cascade 等 IDE 类环境，按"可粘贴到聊天输入框或通过缓存回退取回"的方式生成结果。
- 对 `client=codex` 且 `mode=agent`，仍保持适合终端 coding agent 的清晰任务范围、执行步骤和验证期望。
- `options.max_context_tokens` 只裁剪可选上下文 section；原始用户 prompt、目标客户端、工作区和增强契约不会被最终字符串粗暴截断。
- `metadata.sections` 只记录 section 名称、最终长度和是否裁剪，不记录正文，用于诊断 history、files、retrieval 是否真正进入本次增强。

### 架构边界

- Core 只处理 canonical prompt enhancement request，不直接依赖具体客户端。
- Adapter 负责各客户端输入/输出差异，不能把客户端交互限制泄漏成核心规则。
- Provider 只承接 OpenAI-compatible 调用，避免在核心层绑定具体厂商 SDK。
- Context pipeline 应作为可选扩展点，不应成为默认必选链路。

## 项目边界

为避免被误以为是别的东西，明确以下方向：

- 不代理完整 agent chat / completion 请求；只在 prompt 进入宿主前做一次改写。
- 不保存长期会话状态；缓存只保留每个客户端最近一次 hook 的预览和纯文本。
- 不复刻 Augment Code 或任何商用工具的私有 prompt / 后端逻辑。
- 不把 Openace 作为必选依赖；Openace 只作为显式启用的可选 context provider，不承载 prompt rewrite 核心逻辑。

## 开发与贡献

### 验证

```bash
go test ./...
go vet ./...
go build ./cmd/openpe ./cmd/openpe-server
```

### 项目治理参考

让 AI 继续协作开发时，建议先读：

- `AGENTS.md`：项目定位、架构边界、事实源和治理规则的权威入口。
- `skills/*/SKILL.md` 与 `skills/*/SPEC.md`：对应场景的执行流程和产物规范。
- `docs/requirements/`、`docs/development/`、`docs/debug/`、`docs/references/`、`docs/work-logs/`：本地私有需求、开发、调试、参考和工作日志（默认不提交到公共仓库）。

约束：

- 所有 openPE 相关文档、日志和治理产物必须写入 `/home/oh/projects/openPE` 下的对应路径，不得误写到其它项目。
- `docs/`、`.codex/`、`.augmentignore`、`.env` 等本地资产默认不提交。
- README 只记录当前已实现或已验证的能力；实验方案、失败路径和未来方向必须明确标注。
- 涉及架构、provider、adapter、context pipeline 的修改，应先说明取舍、边界和验证方式。
