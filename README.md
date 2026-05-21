# openPE

`openPE`（open prompt-enhancer）是一个本地优先的 prompt enhancement 工具链。它在 Claude Code、Codex、Cursor、Windsurf 等 Agent CLI/IDE 真正接收 prompt 前，额外调用一次 OpenAI-compatible 模型，把用户原始输入改写成更适合 coding agent 执行的 prompt。

## MVP 范围

- Go CLI：`openpe enhance`
- Codex CLI adapter：`openpe codex`
- Codex UserPromptSubmit hook：`openpe codex hook install`
- Claude Code UserPromptSubmit hook：`openpe claude hook install`
- Go HTTP server：`POST /v1/prompt-enhance`
- 标准 OpenAI-compatible `/v1/chat/completions`
- 非流式响应
- 不代理完整 agent 请求
- 不实现 MCP/IDE adapter、流式响应或会话存储

## 当前状态

openPE 当前处于本地优先的早期可用阶段，已完成 Go CLI、HTTP API、OpenAI-compatible provider、Codex hook、Claude Code hook 以及基础测试。项目重点不是替代 Codex、Claude Code、Cursor 或 Windsurf，而是在这些编码代理接收任务前提供一个可控的 prompt enhancement layer。

当前推荐的使用方式：

- 在 Codex CLI 中使用 `pe:` 主动触发增强，openPE 阻断原始消息，将增强结果复制到剪贴板，并缓存完整 Markdown 预览。
- 在 Claude Code 中使用 `pe:` 主动触发增强，Claude Code TUI 当前能较好展示多行 Markdown 预览。
- 对自动化、脚本或外部工具，使用 `openpe enhance` 或 `openpe-server` 的 `POST /v1/prompt-enhance`。

当前明确不做：

- 不代理完整 agent chat/completion 请求。
- 不保存长期会话状态。
- 不复刻 Augment Code 私有 prompt 或后端逻辑。
- 不把 Openace 作为必选依赖；Openace 未来只能作为可选 context provider。

## 配置

复制 `.env.example` 中的变量到你的 shell 或本地 `.env` 管理工具中：

```bash
export OPENPE_BASE_URL="https://your-openai-compatible-host"
export OPENPE_API_KEY="your-api-key"
export OPENPE_MODEL="your-model"
export OPENPE_LISTEN_ADDR="127.0.0.1:18980"
export OPENPE_TIMEOUT="60s"
```

`OPENPE_BASE_URL` 可以是不带 `/v1` 的 host，也可以是以 `/v1` 结尾的 OpenAI-compatible base URL。

openPE 会读取当前工作目录下的 `.env`，但 shell 环境变量优先级更高。

## 架构与模块

核心数据流：

```text
client / hook / HTTP
  -> adapter layer
  -> enhancer.Request
  -> prompt rewrite core
  -> OpenAI-compatible provider
  -> enhanced prompt
```

主要模块：

| 路径 | 职责 |
|------|------|
| `cmd/openpe` | CLI 入口，包含 `enhance`、`codex`、`codex hook`、`claude hook` 子命令 |
| `cmd/openpe-server` | HTTP server 入口 |
| `internal/enhancer` | 核心 prompt rewrite 服务与请求/响应类型，不感知具体客户端 |
| `internal/providers/openai` | 最小 OpenAI-compatible `/v1/chat/completions` provider |
| `internal/adapters/codex` | Codex CLI 与 `UserPromptSubmit` hook 适配 |
| `internal/adapters/claude` | Claude Code `UserPromptSubmit` hook 适配 |
| `internal/adapters/clipboard` | Codex preview 的剪贴板与 OSC52 交付兜底 |
| `internal/adapters/manual` | `pe:` / `pe!:` 等手动触发前缀解析 |
| `internal/adapters/preview` | hook preview Markdown 包装 |
| `internal/config` | `.env`、环境变量和默认配置读取 |
| `internal/server` | `POST /v1/prompt-enhance` 与健康检查 |
| `skills/` | 项目治理 skill 定义，不存放运行产物 |
| `docs/` | 本地私有治理产物，默认被 `.gitignore` 排除 |

架构边界：

- Core 只处理 canonical prompt enhancement request，不直接依赖 Codex、Claude Code、Cursor 或 Windsurf。
- Adapter 负责各客户端输入/输出差异，不能把客户端交互限制泄漏成核心业务规则。
- Provider 只承接 OpenAI-compatible 调用，避免在核心层绑定具体云厂商 SDK。
- Context pipeline 后续应作为可选扩展点，不应成为默认必选链路。

## CLI

只增强 prompt：

```bash
openpe enhance --prompt "帮我检查这个 Go 项目的测试失败"
```

也可以从 stdin 输入：

```bash
printf '实现一个 prompt enhancer MVP' | openpe enhance
```

输出完整 JSON：

```bash
openpe enhance --json --prompt "优化这个需求描述"
```

直接进入 Codex CLI：

```bash
openpe codex --prompt "帮我检查这个 Go 项目的测试失败"
```

`openpe codex` 会先调用 prompt enhancer，然后把增强后的 prompt 通过 stdin 交给：

```bash
codex exec -C "$PWD" -
```

只查看增强结果、不启动 Codex：

```bash
openpe codex --dry-run --prompt "帮我检查这个 Go 项目的测试失败"
```

传递额外 Codex 参数：

```bash
openpe codex \
  --codex-arg=--ephemeral \
  --codex-arg=--search \
  --prompt "先分析当前仓库结构，再补全 README 使用说明"
```

`--codex-arg` 需要重复传递；如果参数本身以 `-` 开头，推荐使用 `--codex-arg=--flag` 形式。

## Codex hook

Codex CLI `0.132.0` 支持 `UserPromptSubmit` hook。openPE 默认安装用户级 hook，在任意 Codex 项目中接收用户 prompt 时按需调用 enhancer。

安装到全局 Codex 用户配置：

```bash
openpe codex hook install
```

默认写入 `~/.codex/hooks.json`。本地项目级 hook 仅用于调试或项目限定场景：

```bash
openpe codex hook install --scope project
```

默认用户级安装会读取 `~/.config/openpe/.env`。也可以显式指定：

```bash
openpe codex hook install --env-file /absolute/path/to/.env
```

安装或修改 hook 后，在 Codex TUI 中执行 `/hooks`，review 并 trust 这个用户级 hook；否则 Codex 会忽略未信任的 hook。

安装器默认使用 Codex 官方支持的 `stderr + exit 2` block 模式阻断原始 `pe:` 消息。Codex CLI `0.132.0` 会把 captured hook feedback 压平成单行；openPE 不再默认向 `/dev/tty` 直写长 Markdown，因为 Codex TUI 拥有终端重绘控制权，直写会导致预览被状态栏打断、截断或错位。

稳定路径是：hook 阻断原始消息、缓存完整 Markdown、并尽量把增强后的纯 prompt 复制到系统剪贴板。复制会优先使用本机剪贴板命令，失败后尝试 OSC52 终端剪贴板控制序列；你可以留在当前 Codex 输入框里粘贴、编辑、再发送。

如果同时安装并信任了用户级 `~/.codex/hooks.json` 和项目级 `.codex/hooks.json`，Codex 会执行两次 `UserPromptSubmit` hook。openPE 以用户级 hook 作为默认全局入口；新安装的项目级 openPE hook 会在检测到用户级 openPE hook 时自动跳过，避免一次 `pe:` 出现两条 blocked feedback。已有旧 hook 配置可重新执行一次 `openpe codex hook install` 写入新的 scope 标记。

只预览 hook 配置：

```bash
openpe codex hook install --dry-run
```

直接模拟 hook 输入：

```bash
printf '{"hook_event_name":"UserPromptSubmit","prompt":"pe: 帮我检查测试失败","cwd":"%s"}' "$PWD" \
  | openpe codex hook run
```

在 Codex TUI 中主动触发增强：

```text
pe: 帮我检查测试失败
```

默认行为是 preview：openPE 会拦截这条消息，不提交给模型，并把增强后的 prompt 复制到系统剪贴板（如果本机有 `wl-copy`、`xclip`、`xsel`、`pbcopy`、`clip.exe` 或终端支持 OSC52）。你可以在当前 Codex 输入框里粘贴、修改后再正常发送。

注意：Codex hook 输入中的 `cwd` 来自当前 Codex session。如果你在 `/home/oh/projects/openace-mcp` 启动 Codex，却要求处理 openPE，增强结果也可能把工作区写成 openace-mcp。处理 openPE 自身时，请从 `/home/oh/projects/openPE` 启动 Codex，或使用 `codex -C /home/oh/projects/openPE`。

Codex TUI 会压缩 captured hook feedback 的换行，所以 hook feedback 只保留短状态。查看最近一次完整 Markdown 预览：

```bash
openpe codex hook last
```

只查看缓存路径：

```bash
openpe codex hook last --path
```

如果想跳过预览，直接把增强结果作为 `additionalContext` 注入当前 turn：

```text
pe!: 帮我检查测试失败
```

可用触发前缀：`pe:`、`openpe:`、`增强:`。中文输入法下的全角冒号也支持，如 `pe：`、`增强：`。对应的直接注入前缀是 `pe!:`、`openpe!:`、`增强!:`，也兼容 `pe！：` 这类全角写法。

如果需要自定义复制命令，可以通过 `OPENPE_COPY_COMMAND` 指定从 stdin 读取内容的命令：

```bash
OPENPE_COPY_COMMAND='xclip -selection clipboard' openpe codex hook run --block-output=stderr --copy-preview=true
```

`/dev/tty` 直写仍保留为实验选项，但默认关闭：

```bash
OPENPE_CODEX_TERMINAL_PREVIEW=1 openpe codex hook run --block-output=stderr
```

限制：Codex hook 当前不能直接替换原始输入框内容，也不能稳定渲染多行 Markdown feedback；preview 模式采用“阻断原 prompt + 复制增强 prompt + 缓存完整 Markdown”的方式实现可控、可编辑。`/dev/tty` 直写是失败风险较高的旁路方案，只适合临时调试，不建议作为默认交互路径。

## Codex 交互限制

Codex CLI `0.132.0` 的交互式 `/` 菜单当前只枚举内置命令，不会显示 `~/.codex/prompts/*.md`、`~/.codex/commands/*.md` 或本地插件 `commands/` 中的自定义命令。因此 openPE 当前不能通过 `/pe`、`/prompts:pe` 或 `/openpe:pe` 直接注入已打开的 Codex TUI。

如果后续仍要实现“进入一次 Codex 后反复输入 `/pe ...`”的体验，PTY 代理是兜底方案：由 `openpe codex-tui` 启动真实 `codex`，拦截用户输入行中的 `/pe ...`，调用 `openpe enhance` 后把增强 prompt 写回 Codex TUI。该方案不修改 Codex 本体，但需要处理终端 raw mode、粘贴、快捷键和异常退出。

## Claude Code hook

Claude Code 的 `UserPromptSubmit` hook 可以用同样的 `pe:` 前缀主动触发增强。和 Codex 不同，Claude Code 交互式 TUI 当前会保留 hook feedback 的换行，因此更适合直接在当前对话框中预览 Markdown 格式的增强 prompt。

安装全局 Claude Code hook：

```bash
openpe claude hook install --env-file ~/.config/openpe/.env
```

安装后重启 Claude Code，然后在交互式 TUI 中输入：

```text
pe: 帮我检查当前项目下一步怎么做
```

默认行为是 preview：openPE 会阻断这条 `pe:` 消息，不提交给模型，并在 Claude Code 当前界面显示 Markdown 预览。你可以复制、修改后再正常发送。

注意：Claude Code 的 `--print` headless 模式会执行 hook，但不会像交互式 TUI 一样稳定展示被阻断 hook 的 feedback；测试预览效果时请使用交互式 Claude Code。

Claude Code 自身调用哪个模型不由 openPE hook 决定，需要按 Claude Code 支持的方式配置。若使用 Anthropic-compatible 第三方网关，可以在启动 Claude Code 前注入：

```bash
export ANTHROPIC_BASE_URL="https://your-anthropic-compatible-host"
export ANTHROPIC_API_KEY="your-api-key"
export ANTHROPIC_MODEL="gpt-5.4"
export ANTHROPIC_REASONING_MODEL="gpt-5.4"
export CLAUDE_CODE_SUBAGENT_MODEL="gpt-5.4"
export CLAUDE_CODE_EFFORT_LEVEL="high"
claude --model gpt-5.4 --effort high
```

限制：Claude Code hook 当前也不能直接替换原始输入框内容；preview 模式采用“阻断原 prompt + 显示增强 prompt”的方式实现可见、可控、可编辑。当前验证的 Claude Code CLI `2.1.146` 暴露的 `--effort` 取值是 `low`、`medium`、`high`，未发现可配置的 `xhigh` 参数；1M context window 属于上游模型或网关能力，不是 openPE hook 可强制开启的选项。

## AI 协作约束

让 AI 继续协作开发 openPE 时，建议先读取：

- `AGENTS.md`：项目定位、架构边界、事实源和治理规则的权威入口。
- `README.md`：当前功能、使用方式、限制和模块说明。
- `skills/*/SKILL.md` 与 `skills/*/SPEC.md`：对应场景的执行流程和产物规范。
- `docs/requirements/`、`docs/development/`、`docs/debug/`、`docs/references/`、`docs/work-logs/`：本地私有需求、开发、调试、参考和工作日志。

重要约束：

- 所有 openPE 相关文档、日志和治理产物必须写入 `/home/oh/projects/openPE` 下的对应路径，不得误写到 Openace 或其它项目。
- `docs/`、`.codex/`、`.augmentignore`、`.env` 等本地资产默认不提交到公共仓库。
- README 只记录当前已实现或已验证能力；实验方案、失败路径和未来方向必须明确标注，避免污染后续 AI 判断。
- 涉及架构、provider、adapter、context pipeline 的修改，应先说明取舍、边界和验证方式。

## HTTP

启动服务：

```bash
openpe-server
```

请求：

```bash
curl -s http://127.0.0.1:18980/v1/prompt-enhance \
  -H 'content-type: application/json' \
  -d '{"prompt":"帮我实现 openPE MVP","client":"codex","mode":"agent"}'
```

健康检查：

```bash
curl -s http://127.0.0.1:18980/healthz
```

## 验证

```bash
go test ./...
go vet ./...
go build ./cmd/openpe ./cmd/openpe-server
```
