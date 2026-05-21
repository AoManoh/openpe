# openPE

`openPE`（open prompt-enhancer）是一个本地优先的 prompt enhancement 工具链。它在 Claude Code、Codex、Cursor、Windsurf 等 Agent CLI/IDE 真正接收 prompt 前，额外调用一次 OpenAI-compatible 模型，把用户原始输入改写成更适合 coding agent 执行的 prompt。

## 主路径

openPE 的正式使用方式只有一种：先安装目标工具的 hook，然后在 Codex、Claude Code、Windsurf Cascade 或后续其它支持 hook 的对话终端中输入 `pe <你的原始需求>` 触发增强。

当前已支持：

- Codex UserPromptSubmit hook：`openpe codex hook install`
- Claude Code UserPromptSubmit hook：`openpe claude hook install`
- Windsurf Cascade pre_user_prompt hook：`openpe windsurf hook install`

增强完成后，openPE 默认只输出简短状态，不在终端打印完整 prompt。用户直接粘贴剪贴板中的增强结果，编辑后再发送。

`openpe enhance`、`openpe codex`、`openpe-server` 和脚本入口仅用于测试、调试或自动化集成，不是面向日常使用的正式交互方式。

## 当前状态

openPE 当前处于本地优先的早期可用阶段，已完成 Go CLI、HTTP API、OpenAI-compatible provider、Codex hook、Claude Code hook、Windsurf Cascade hook 以及基础测试。项目重点不是替代 Codex、Claude Code、Cursor 或 Windsurf，而是在这些编码代理接收任务前提供一个可控的 prompt enhancement layer。

当前推荐的使用方式：

- 在 Codex CLI 中输入 `pe <内容>`，openPE 阻断原始消息，将增强结果复制到剪贴板，并缓存完整预览。
- 在 Claude Code 中输入 `pe <内容>`，openPE 阻断原始消息，将增强结果复制到剪贴板。
- 在 Windsurf Cascade 中输入 `pe <内容>`，openPE 通过 `pre_user_prompt` 阻断原始消息，将增强结果复制到剪贴板，并缓存完整预览。
- 不需要增强时，不输入 `pe` 即可，原始消息按宿主工具正常处理。

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
export OPENPE_LANGUAGE="zh"
export OPENPE_LISTEN_ADDR="127.0.0.1:18980"
export OPENPE_TIMEOUT="60s"
```

`OPENPE_BASE_URL` 可以是不带 `/v1` 的 host，也可以是以 `/v1` 结尾的 OpenAI-compatible base URL。

openPE 会读取当前工作目录下的 `.env`，但 shell 环境变量优先级更高。

`OPENPE_LANGUAGE` 默认为 `zh`，终端提示使用中文。需要英文提示时设置为 `en`。

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
| `cmd/openpe` | CLI 入口；正式路径是 hook install，裸命令仅用于测试/调试 |
| `cmd/openpe-server` | HTTP server 入口 |
| `internal/enhancer` | 核心 prompt rewrite 服务与请求/响应类型，不感知具体客户端 |
| `internal/providers/openai` | 最小 OpenAI-compatible `/v1/chat/completions` provider |
| `internal/adapters/codex` | Codex CLI 与 `UserPromptSubmit` hook 适配 |
| `internal/adapters/claude` | Claude Code `UserPromptSubmit` hook 适配 |
| `internal/adapters/windsurf` | Windsurf Cascade `pre_user_prompt` hook 适配 |
| `internal/adapters/clipboard` | hook 预览的剪贴板与 OSC52 交付兜底 |
| `internal/adapters/manual` | `pe` 手动触发关键字解析 |
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

## 增强契约

openPE 的核心增强逻辑通过 canonical request 接收 `prompt`、`client`、`mode`、`cwd`、rules、history 和 context。`client` / `mode` 只用于给模型说明目标运行环境，不把某个宿主的私有能力当作核心依赖。

增强结果必须满足：

- 保留用户原始意图、语言、显式约束和安全边界。
- 输出自包含、可粘贴、适合编码代理执行的 prompt。
- 不依赖宿主一定能替换输入框、追加隐藏上下文、保持剪贴板成功，或识别某个客户端专有 slash command。
- 对 Windsurf、Cursor、VS Code、Composer、Cascade 等 IDE 类环境，默认按“可粘贴到聊天输入框或通过缓存回退取回”的方式生成结果。
- 对 `client=codex` 且 `mode=agent`，仍保持适合终端 coding agent 的清晰任务范围、执行步骤和验证期望。

## 安装 hook

### Codex

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

安装器默认使用 Codex 官方支持的 `stderr + exit 2` block 模式阻断原始 `pe` 消息。Codex CLI `0.132.0` 会把 captured hook feedback 压平成单行；openPE 不再默认向 `/dev/tty` 直写长 Markdown，因为 Codex TUI 拥有终端重绘控制权，直写会导致预览被状态栏打断、截断或错位。

稳定路径是：hook 阻断原始消息、缓存完整预览和可粘贴纯文本，并尽量把增强后的纯 prompt 复制到系统剪贴板。复制会优先使用本机剪贴板命令，失败后尝试 OSC52 终端剪贴板控制序列；复制成功时你可以留在当前 Codex 输入框里粘贴、编辑、再发送。若提示“剪贴板未更新”，不要直接粘贴旧内容，改用 `openpe codex hook last --prompt` 查看本次可粘贴文本。

如果同时安装并信任了用户级 `~/.codex/hooks.json` 和项目级 `.codex/hooks.json`，Codex 会执行两次 `UserPromptSubmit` hook。openPE 以用户级 hook 作为默认全局入口；新安装的项目级 openPE hook 会在检测到用户级 openPE hook 时自动跳过，避免一次 `pe` 出现两条 blocked feedback。已有旧 hook 配置可重新执行一次 `openpe codex hook install` 写入新的 scope 标记。

只预览 hook 配置：

```bash
openpe codex hook install --dry-run
```

测试/调试 hook 输入：

```bash
printf '{"hook_event_name":"UserPromptSubmit","prompt":"pe 帮我检查测试失败","cwd":"%s"}' "$PWD" \
  | openpe codex hook run
```

在 Codex TUI 中主动触发增强：

```text
pe 帮我检查测试失败
```

默认行为：openPE 会拦截这条消息，不提交给模型，并把增强后的 prompt 复制到系统剪贴板（如果本机有 `wl-copy`、`xclip`、`xsel`、`pbcopy`、`clip.exe` 或终端支持 OSC52）。复制成功后，你可以在当前 Codex 输入框里粘贴、修改后再正常发送；复制失败时请按提示运行 `openpe codex hook last --prompt`，不要直接粘贴旧剪贴板内容。

注意：Codex hook 输入中的 `cwd` 来自当前 Codex session。如果你在 `/home/oh/projects/openace-mcp` 启动 Codex，却要求处理 openPE，增强结果也可能把工作区写成 openace-mcp。处理 openPE 自身时，请从 `/home/oh/projects/openPE` 启动 Codex，或使用 `codex -C /home/oh/projects/openPE`。

Codex TUI 会压缩 captured hook feedback 的换行，所以 hook feedback 只保留短状态。查看最近一次完整预览：

```bash
openpe codex hook last
```

查看最近一次可直接粘贴的纯 prompt：

```bash
openpe codex hook last --prompt
```

只查看缓存路径：

```bash
openpe codex hook last --path
```

触发关键字只有 `pe`。推荐写法是 `pe <内容>`；为兼容已有输入习惯，也接受 `pe:` 和 `pe：` 作为分隔符。`openpe`、`增强`、`pe!` 等旧触发写法不再作为正式入口。

如果需要自定义复制命令，可以通过 `OPENPE_COPY_COMMAND` 指定从 stdin 读取内容的命令：

```bash
OPENPE_COPY_COMMAND='xclip -selection clipboard' openpe codex hook run --block-output=stderr --copy-preview=true
```

`/dev/tty` 直写仅保留为测试/调试选项，默认关闭：

```bash
OPENPE_CODEX_TERMINAL_PREVIEW=1 openpe codex hook run --block-output=stderr
```

限制：Codex hook 当前不能直接替换原始输入框内容，也不能稳定渲染多行 Markdown feedback；正式路径采用“阻断原 prompt + 复制增强 prompt + 缓存完整预览和纯文本”的方式实现可控、可编辑。`/dev/tty` 直写是失败风险较高的旁路方案，只适合临时调试。

## Codex 交互限制

Codex CLI `0.132.0` 的交互式 `/` 菜单当前只枚举内置命令，不会显示 `~/.codex/prompts/*.md`、`~/.codex/commands/*.md` 或本地插件 `commands/` 中的自定义命令。因此 openPE 当前不能通过 `/pe`、`/prompts:pe` 或 `/openpe:pe` 直接注入已打开的 Codex TUI。

当前不规划 `/pe`、PTY 代理或自定义 command 作为正式入口；正式入口保持为 hook 中输入 `pe <内容>`。

### Claude Code

Claude Code 的 `UserPromptSubmit` hook 使用同样的 `pe` 关键字主动触发增强。为保持所有 CLI 对话体验一致，openPE 默认只输出短状态，并把增强结果复制到剪贴板。

安装全局 Claude Code hook：

```bash
openpe claude hook install --env-file ~/.config/openpe/.env
```

安装后重启 Claude Code，然后在交互式 TUI 中输入：

```text
pe 帮我检查当前项目下一步怎么做
```

默认行为：openPE 会阻断这条 `pe` 消息，不提交给模型，并把增强后的 prompt 复制到系统剪贴板。复制成功后，你可以在当前输入框粘贴、修改后再正常发送；复制失败时请按提示运行 `openpe claude hook last --prompt`，不要直接粘贴旧剪贴板内容。

查看最近一次可直接粘贴的纯 prompt：

```bash
openpe claude hook last --prompt
```

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

限制：Claude Code hook 当前也不能直接替换原始输入框内容；正式路径采用“阻断原 prompt + 复制增强 prompt + 缓存完整预览和纯文本”的方式实现可控、可编辑。当前验证的 Claude Code CLI `2.1.146` 暴露的 `--effort` 取值是 `low`、`medium`、`high`，未发现可配置的 `xhigh` 参数；1M context window 属于上游模型或网关能力，不是 openPE hook 可强制开启的选项。

### Windsurf Cascade

Windsurf Cascade 官方提供 `pre_user_prompt` hook。openPE 的 Windsurf 适配只使用已公开的阻断能力：检测到 `pe` 后增强 prompt、复制到剪贴板，并用 exit code `2` 阻断原始 `pe` 消息；不承诺直接替换 Cascade 输入框或自动提交增强内容。

安装全局 Windsurf IDE hook：

```bash
openpe windsurf hook install --env-file ~/.config/openpe/.env
```

默认写入 `~/.codeium/windsurf/hooks.json`。本地项目级 hook 仅用于调试或项目限定场景：

```bash
openpe windsurf hook install --scope project
```

项目级配置写入 `.windsurf/hooks.json`。安装后如未立即生效，请重启 Windsurf 或重新打开工作区。

在 Cascade 中主动触发增强：

```text
pe 帮我梳理这个项目下一阶段开发计划
```

默认行为：openPE 会阻断这条 `pe` 消息，不提交给 Cascade，并把增强后的 prompt 复制到系统剪贴板。复制成功后，你可以在 Cascade 输入框粘贴、修改后再正常发送；复制失败时不要直接粘贴旧剪贴板内容，hook feedback 会给出 `last-prompt.txt` 的绝对路径，你可以直接打开该文件获取本次增强 prompt，也可以运行 `openpe windsurf hook last --prompt` 打印纯文本。

测试/调试 hook 输入：

```bash
printf '{"agent_action_name":"pre_user_prompt","tool_info":{"user_prompt":"pe 帮我检查测试失败"}}' \
  | openpe windsurf hook run
```

查看最近一次完整预览：

```bash
openpe windsurf hook last
```

查看最近一次可直接粘贴的纯 prompt：

```bash
openpe windsurf hook last --prompt
```

只查看纯 prompt 缓存文件路径：

```bash
openpe windsurf hook last --path --prompt
```

只查看缓存路径：

```bash
openpe windsurf hook last --path
```

限制：Windsurf `pre_user_prompt` 当前公开契约只证明可阻断原始输入，未证明可替换 prompt 或追加上下文后继续提交。openPE 因此采用“阻断原 prompt + 复制增强 prompt + 缓存完整预览和纯文本”的方式实现可控、可编辑。

## 测试与调试命令

以下命令仅用于测试、调试或自动化集成，不是日常正式交互方式。

只增强 prompt：

```bash
openpe enhance --prompt "帮我检查这个 Go 项目的测试失败"
```

输出完整 JSON：

```bash
openpe enhance --json --prompt "优化这个需求描述"
```

调试 Codex exec 包装：

```bash
openpe codex --dry-run --prompt "帮我检查这个 Go 项目的测试失败"
```

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

## HTTP 调试接口

HTTP 接口仅用于测试、调试或自动化集成，不是日常正式交互方式。

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
