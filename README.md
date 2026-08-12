# openPE

中文 | [English](README.en.md)

openPE 是一个本地运行的提示词增强工具。它支持 Codex CLI、Claude Code、Devin CLI 和 Windsurf。

安装 hook 后，你可以直接在客户端对话框输入：

```text
pe 帮我为这个接口补充测试
```

openPE 会在消息发送前拦截原始内容，读取可用的会话历史，生成更明确的任务描述，并把结果复制到剪贴板供你检查和修改。

默认情况下，原始 `pe` 消息不会发送给模型。Codex、Claude Code 和 Devin 也支持注入模式，可让客户端直接使用增强结果。

## 主要特点

- **保留现有工具**：继续使用 Codex、Claude Code、Devin 或 Windsurf，只增加发送前的提示词增强。
- **本地配置**：请求直接发往你配置的模型 API，不经过 openPE 的第三方服务。
- **会话上下文**：在客户端允许的情况下读取最近对话，让“继续上面的方案”这类请求更完整。
- **先检查再发送**：默认阻断原始消息并复制增强结果，由你确认后再提交。
- **明确提示风险**：增强结果出现上下文外数字、未决定的不可逆动作或语言偏移时，会在交付信息中显示提醒。

## 快速开始

需要 Go **1.25.12 或更高版本**。

### 1. 安装

```bash
git clone https://github.com/AoManoh/openpe.git
cd openpe
go install ./cmd/openpe
```

日常 hook 只需要 `openpe`。`openpe-server` 仅用于 HTTP 自动化或实验性 IDE 按钮：

```bash
go install ./cmd/openpe-server
```

确认安装：

```bash
openpe -h
```

如果提示 `openpe：未找到命令`，把 Go 的产物目录加入 `PATH`（zsh 用户改 `~/.zshrc`）：

```bash
echo 'export PATH="$PATH:$(go env GOPATH)/bin"' >> ~/.bashrc
source ~/.bashrc
openpe -h
```

完整排查见 [FAQ Q0](FAQ.md#q0-go-install-成功了但-openpe未找到命令)。

### 2. 配置模型 API

openPE 使用 `.env` 配置文件。它是普通文本：每行填写一个 `变量名=值`，以 `#` 开头的行是注释。Hook 每次运行都会重新读取配置。建议统一保存到：

```bash
mkdir -p ~/.config/openpe
```

#### OpenAI-compatible API（默认）

```bash
cat > ~/.config/openpe/.env <<'EOF'
OPENPE_BASE_URL=https://your-openai-compatible-endpoint
OPENPE_API_KEY=replace-with-your-api-key
OPENPE_MODEL=your-model
EOF
```

支持 OpenAI、DashScope、火山引擎和提供 `/v1/chat/completions` 的自建网关。`OPENPE_BASE_URL` 没有默认值，请填写实际地址。

#### Anthropic Messages API

如果端点提供 `/v1/messages`，必须显式选择 Anthropic 协议：

```bash
cat > ~/.config/openpe/.env <<'EOF'
OPENPE_PROVIDER=anthropic
OPENPE_BASE_URL=https://your-anthropic-compatible-endpoint
OPENPE_API_KEY=replace-with-your-api-key
OPENPE_MODEL=your-model
EOF
```

如果 Anthropic 地址没有设置 `OPENPE_PROVIDER=anthropic`，openPE 会按默认 OpenAI 协议请求 `/v1/chat/completions`，通常会返回 HTTP 404。排查见 [FAQ Q0.1](FAQ.md#q01-配置-anthropic-地址后为什么返回-openai-compatible-provider-http-404)。

验证配置：

```bash
OPENPE_ENV_FILE="$HOME/.config/openpe/.env" \
openpe enhance --prompt "帮我整理这个需求"
```

能正常返回增强后的提示词，就说明 API 地址、密钥、模型名和协议配置可用。

### 3. 安装客户端 hook

选择你使用的客户端，可以安装一个或多个：

```bash
openpe codex hook install
openpe claude hook install
openpe devin hook install
openpe windsurf hook install
```

安装后：

- Codex：在 TUI 中执行 `/hooks`，检查并信任 hook。
- Claude Code：重新启动 Claude Code。
- Devin：用 `/hooks` 确认 openPE 已加载。
- Windsurf：重新启动 IDE 或重新打开 workspace。

项目级安装、自定义配置路径和各客户端限制见 [CLIENTS.md](CLIENTS.md)。

### 4. 使用

在客户端对话框输入以下任一种格式：

```text
pe 帮我把这个测试改成 table-driven
pe:帮我把这个测试改成 table-driven
pe：帮我把这个测试改成 table-driven
```

默认流程：

1. openPE 拦截原始 `pe` 消息；
2. 调用模型生成增强提示词；
3. 把增强结果复制到剪贴板；
4. 你粘贴、检查、修改后再发送。

如果剪贴板不可用，可以直接读取最近一次结果：

```bash
openpe <client> hook last --prompt
```

`<client>` 可用 `codex`、`claude`、`devin` 或 `windsurf`。

## 交付模式

### Review 模式（默认）

原始 `pe` 消息不会发送给模型。增强结果进入剪贴板和本地缓存，由你确认后再发送。

这也是 Windsurf 唯一支持的 hook 交付方式。

### 注入模式

Codex、Claude Code 和 Devin 支持把增强结果作为附加上下文直接注入：

```dotenv
OPENPE_HOOK_INJECT=true
```

也可以只为某个客户端开启：

```dotenv
OPENPE_CODEX_INJECT=true
OPENPE_CLAUDE_INJECT=true
OPENPE_DEVIN_INJECT=true
```

注入模式会跳过人工检查。对包含数字、部署、删除、发布、支付或 push 等动作的任务，建议继续使用默认 review 模式。

## 常用配置

环境变量优先级：shell 环境变量 > `OPENPE_ENV_FILE` 指向的 dotenv > 当前目录 `.env`。

| 变量 | 默认值 | 用途 |
|---|---:|---|
| `OPENPE_PROVIDER` | `openai` | API 协议：`openai` 或 `anthropic` |
| `OPENPE_BASE_URL` | 无 | 模型 API 地址，必填 |
| `OPENPE_API_KEY` | 无 | API 密钥，必填 |
| `OPENPE_MODEL` | 无 | 模型名，必填 |
| `OPENPE_TIMEOUT` | `60s` | 单次模型请求超时 |
| `OPENPE_LANGUAGE` | `zh` | openPE 状态信息语言：`zh` / `en` |
| `OPENPE_MAX_TOKENS` | `0` | 模型输出上限；Anthropic 为 0 时使用 4096 |
| `OPENPE_MAX_CONTEXT_TOKENS` | `0` | 输入上下文预算；0 表示不限制 |
| `OPENPE_PROMPT_STYLE` | `agent` | 改变增强结果的详略：`agent` 较短，`human` 会展开目标、步骤和验证 |
| `OPENPE_MESSAGE_STYLE` | `flatten` | 改变历史和资料发送给模型的方式；`hybrid` 保留对话角色，`structured` 进一步分开参考资料与当前任务 |
| `OPENPE_HOOK_INJECT` | `false` | 为支持的客户端开启直接注入 |
| `OPENPE_CACHE_DIR` | 平台默认缓存目录 | 最近一次增强结果的缓存根目录 |
| `OPENPE_WARNINGS_ENABLED` | `true` | 显示上下文外数字和未决定动作提醒 |
| `OPENPE_LANGUAGE_GUARD_ENABLED` | `true` | 检测明显的输出语言偏移 |

### 按使用结果选择参数

| 你想改变的结果 | 建议查看 |
|---|---|
| Anthropic 端点返回 404 | `OPENPE_PROVIDER=anthropic` |
| 模型输出被截断 | `OPENPE_MAX_TOKENS` |
| 历史太多、输入成本过高 | `OPENPE_MAX_CONTEXT_TOKENS` 或客户端历史上限 |
| 增强结果太长或太简略 | `OPENPE_PROMPT_STYLE` |
| “继续上面的方案”没有带出具体方案 | 先确认历史已带入，再比较 `OPENPE_MESSAGE_STYLE=hybrid` |
| 规则、文件内容被误写成执行要求 | 比较 `OPENPE_MESSAGE_STYLE=structured` |
| 不想手动粘贴 | `OPENPE_HOOK_INJECT=true` |
| 语言偏移时不想额外调用模型 | `OPENPE_LANGUAGE_GUARD_REANCHOR=false` |

具体的修改前后对比、适用场景、副作用和验证方法见 [CONFIG.md](CONFIG.md)。可直接复制的完整模板见 [`.env.example`](.env.example)。

## 会话历史和提醒

openPE 会根据客户端提供的能力读取最近对话：

- Codex：根据当前 prompt 和工作目录定位 session rollout。
- Claude Code：读取 hook 提供的 `transcript_path`。
- Devin：Linux 上优先识别当前 session；无法精确识别时使用保守的工作目录和时效判断。
- Windsurf：hook 协议没有可靠的会话历史绑定，因此默认不发送历史。

openPE 会明确告诉你本次是否带入历史。没有找到历史时，增强仍然可以成功，只是不包含前文。

增强结果出现以下情况时，交付信息会显示提醒：

- 原始输入和上下文中没有出现的数字；
- 你没有明确要求的 push、部署、删除、发布或支付动作；
- 输出语言与输入的主导语言明显不同，而且自动修正失败。

这些提醒不会修改或阻断增强结果。

## HTTP 和裸 CLI

HTTP server 仅用于测试、自动化或实验性 IDE 集成，日常 hook 不需要运行它。

```bash
openpe-server
```

它只允许监听 `127.0.0.1`、`::1` 或 `localhost`。如果确实需要远程访问，请在外层使用 TLS 反向代理或隧道。

最小请求示例：

```bash
curl http://127.0.0.1:18980/v1/prompt-enhance \
  -H 'content-type: application/json' \
  -d '{"prompt":"帮我整理这个需求","client":"codex","mode":"agent"}'
```

裸 CLI：

```bash
openpe enhance --prompt "帮我检查这个 Go 项目的测试失败"
openpe enhance --json --prompt "优化这个需求描述"
```

完整 HTTP、CLI 和调试入口见 [CLIENTS.md](CLIENTS.md#http-和命令行接口)。

## 支持范围

| 集成 | 状态 | 说明 |
|---|---|---|
| Codex CLI hook | 推荐 | 支持 review、注入和会话历史 |
| Claude Code hook | 推荐 | 支持 review、注入和 transcript 历史 |
| Devin CLI hook | 推荐 | 支持 review、注入、去重和会话历史 |
| Windsurf hook | 推荐 | 支持 review；不支持 hook 注入 |
| Devin Desktop bundle patch | 实验性 | 仅支持指定 Windows exact build，不建议作为默认安装方式 |

详细安装选项、配置文件位置和客户端限制见 [CLIENTS.md](CLIENTS.md)。

## 常见问题

优先查看 [FAQ.md](FAQ.md)，其中包括：

- `go install` 成功但找不到 `openpe`；
- Anthropic 地址返回 HTTP 404；
- 为什么显示 `Prompt blocked`；
- 为什么没有带入会话历史；
- 剪贴板没有更新时如何取回增强结果；
- 为什么同一条 prompt 可能触发多个 Devin hook；
- 如何选择 `agent` 或 `human` 提示词风格。

## 工作原理

```text
客户端输入
  -> hook / CLI / HTTP
  -> 客户端适配层
  -> 提示词增强核心
  -> OpenAI-compatible 或 Anthropic provider
  -> 增强结果
  -> 剪贴板 / 缓存 / 注入
```

项目边界：

- openPE 只在 prompt 进入客户端前做一次改写，不代理完整 agent 对话。
- openPE 不保存长期会话状态，只缓存每个客户端最近一次增强结果。
- Openace 是已废弃且默认关闭的可选上下文来源，不影响核心增强功能。
- IDE bundle patch 是独立实验能力，不替代正式 hook。

开发者接口、模块职责和增强契约见 [CLIENTS.md](CLIENTS.md#开发者参考)。

## 文档

| 文档 | 内容 |
|---|---|
| [README.en.md](README.en.md) | English README |
| [CLIENTS.md](CLIENTS.md) | 客户端接入、HTTP、IDE patch 和开发者参考 |
| [CONFIG.md](CONFIG.md) | 配置参数的实际效果、适用场景、副作用和验证方法 |
| [FAQ.md](FAQ.md) | 常见问题与故障排查 |
| [.env.example](.env.example) | 可直接复制的完整配置模板 |
| [IDE patch README](extensions/openpe-windsurf-patch/README.md) | 实验性 Desktop patch 详细说明 |

## 开发

```bash
go test ./...
go vet ./...
go build ./cmd/openpe ./cmd/openpe-server
```

提交改动前，请确保相关测试、构建和文档链接均通过检查。
