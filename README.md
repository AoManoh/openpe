# openPE

`openPE`（open prompt-enhancer）是一个本地优先的 prompt enhancement 工具链。它在 Claude Code、Codex、Cursor、Windsurf 等 Agent CLI/IDE 真正接收 prompt 前，额外调用一次 OpenAI-compatible 模型，把用户原始输入改写成更适合 coding agent 执行的 prompt。

## MVP 范围

- Go CLI：`openpe enhance`
- Codex CLI adapter：`openpe codex`
- Codex UserPromptSubmit hook：`openpe codex hook install`
- Go HTTP server：`POST /v1/prompt-enhance`
- 标准 OpenAI-compatible `/v1/chat/completions`
- 非流式响应
- 不代理完整 agent 请求
- 不实现 MCP/IDE adapter、流式响应或会话存储

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

默认行为是 preview：openPE 会拦截这条消息，不提交给模型，并在 Codex 中显示增强后的 prompt。你可以复制、修改后再正常发送。

如果想跳过预览，直接把增强结果作为 `additionalContext` 注入当前 turn：

```text
pe!: 帮我检查测试失败
```

可用触发前缀：`pe:`、`openpe:`、`增强:`。中文输入法下的全角冒号也支持，如 `pe：`、`增强：`。对应的直接注入前缀是 `pe!:`、`openpe!:`、`增强!:`，也兼容 `pe！：` 这类全角写法。

限制：Codex hook 当前不能直接替换原始输入框内容；preview 模式采用“阻断原 prompt + 显示增强 prompt”的方式实现可见、可控、可编辑。

## Codex 交互限制

Codex CLI `0.132.0` 的交互式 `/` 菜单当前只枚举内置命令，不会显示 `~/.codex/prompts/*.md`、`~/.codex/commands/*.md` 或本地插件 `commands/` 中的自定义命令。因此 openPE 当前不能通过 `/pe`、`/prompts:pe` 或 `/openpe:pe` 直接注入已打开的 Codex TUI。

如果后续仍要实现“进入一次 Codex 后反复输入 `/pe ...`”的体验，PTY 代理是兜底方案：由 `openpe codex-tui` 启动真实 `codex`，拦截用户输入行中的 `/pe ...`，调用 `openpe enhance` 后把增强 prompt 写回 Codex TUI。该方案不修改 Codex 本体，但需要处理终端 raw mode、粘贴、快捷键和异常退出。

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
