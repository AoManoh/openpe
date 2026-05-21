# openPE

`openPE`（open prompt-enhancer）是一个本地优先的 prompt enhancement 工具链。它在 Claude Code、Codex、Cursor、Windsurf 等 Agent CLI/IDE 真正接收 prompt 前，额外调用一次 OpenAI-compatible 模型，把用户原始输入改写成更适合 coding agent 执行的 prompt。

## MVP 范围

- Go CLI：`openpe enhance`
- Codex CLI adapter：`openpe codex`
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

## Codex 交互限制

Codex CLI `0.132.0` 的交互式 `/` 菜单当前只枚举内置命令，不会显示 `~/.codex/prompts/*.md`、`~/.codex/commands/*.md` 或本地插件 `commands/` 中的自定义命令。因此 openPE 当前不能通过 `/pe`、`/prompts:pe` 或 `/openpe:pe` 直接注入已打开的 Codex TUI。

现阶段可用入口仍是外部 CLI adapter：

```bash
openpe codex --prompt "帮我检查这个 Go 项目的测试失败"
```

如果要实现“进入一次 Codex 后反复输入 `/pe ...`”的体验，下一步需要做一个 PTY 代理：由 `openpe codex-tui` 启动真实 `codex`，拦截用户输入行中的 `/pe ...`，调用 `openpe enhance` 后把增强 prompt 写回 Codex TUI。这个方案不修改 Codex 本体，但需要处理终端 raw mode、粘贴、快捷键和异常退出。

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
