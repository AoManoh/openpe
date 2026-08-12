# openPE 配置指南

[返回 README](README.md) | [客户端接入](CLIENTS.md) | [常见问题](FAQ.md) | [可复制模板](.env.example)

本文档解释每个配置项会改变什么、什么时候需要修改，以及修改后可以观察到什么结果。

## 配置文件是什么

openPE 使用 `.env` 配置文件。它是普通文本文件：

```dotenv
# 以 # 开头的行是注释
OPENPE_BASE_URL=https://your-api-endpoint
OPENPE_API_KEY=replace-with-your-api-key
OPENPE_MODEL=your-model
```

推荐的用户级路径：

```text
~/.config/openpe/.env
```

加载优先级：

```text
shell 环境变量
  > OPENPE_ENV_FILE 指向的文件
  > 当前工作目录中的 .env
```

Hook 安装器会把 `OPENPE_ENV_FILE` 写入客户端配置，所以修改 `.env` 后，下一次 hook 调用通常就会生效。长期运行的 `openpe-server` 需要重启。

### `OPENPE_ENV_FILE`

**控制什么**：指定 openPE 要读取的 `.env` 文件。

```bash
OPENPE_ENV_FILE="$HOME/.config/openpe/.env" openpe enhance --prompt "整理这个需求"
```

**默认行为**：Hook 安装器会自动写入用户级或项目级路径；裸 CLI 和 server 没有显式指定时会读取当前目录的 `.env`。

**修改后的影响**：模型地址、历史、交付模式等全部配置会从新文件读取。该变量应设置在 shell 或 hook 命令中，不要写在它自己指向的 `.env` 文件里。

要直接复制一份完整模板，请使用 [`.env.example`](.env.example)。

## 按使用结果查参数

| 你想改变的结果 | 参数 |
|---|---|
| 使用 Anthropic `/v1/messages` 端点 | `OPENPE_PROVIDER=anthropic` |
| 模型响应超时 | `OPENPE_TIMEOUT` |
| Anthropic 输出被截断 | `OPENPE_MAX_TOKENS` |
| 会话历史太多、输入成本过高 | `OPENPE_MAX_CONTEXT_TOKENS` 或客户端历史上限 |
| 增强结果太长 | `OPENPE_PROMPT_STYLE=agent` |
| 想得到更详细的任务说明 | `OPENPE_PROMPT_STYLE=human` |
| “继续上面的方案”经常理解不准确 | `OPENPE_MESSAGE_STYLE=hybrid`，并确认历史已带入 |
| 规则、文件资料经常被混进当前任务 | `OPENPE_MESSAGE_STYLE=structured` |
| 不想手动粘贴增强结果 | `OPENPE_HOOK_INJECT=true` |
| 不希望读取本地会话历史 | 对应客户端的 `*_HISTORY_ENABLED=false` |
| 关闭内容风险提醒 | `OPENPE_WARNINGS_ENABLED=false` |
| 语言守卫产生了不需要的额外调用 | `OPENPE_LANGUAGE_GUARD_REANCHOR=false` 或关闭守卫 |
| 剪贴板命令自动探测失败 | `OPENPE_COPY_COMMAND` |
| 修改缓存位置 | `OPENPE_CACHE_DIR` |
| 为 IDE patch 启动本地 server | `OPENPE_SERVER_*` |

## 模型 API

### `OPENPE_BASE_URL`

**控制什么**：模型 API 的基础地址。

**默认值**：无，必填。

OpenAI-compatible 示例：

```dotenv
OPENPE_BASE_URL=https://your-openai-compatible-endpoint
```

Anthropic Messages 示例：

```dotenv
OPENPE_BASE_URL=https://your-anthropic-compatible-endpoint
```

**修改后的影响**：所有增强请求会发往新地址。该参数只改变地址，不会自动判断协议。

**常见错误**：地址提供 `/v1/messages`，但没有设置 `OPENPE_PROVIDER=anthropic`，会按 OpenAI 路径请求并返回 HTTP 404。见 [FAQ Q0.1](FAQ.md#q01-配置-anthropic-地址后为什么返回-openai-compatible-provider-http-404)。

### `OPENPE_API_KEY`

**控制什么**：访问模型 API 使用的密钥。

**默认值**：无，必填。

```dotenv
OPENPE_API_KEY=replace-with-your-api-key
```

**修改后的影响**：OpenAI-compatible 协议使用 `Authorization: Bearer`；Anthropic 协议使用 `x-api-key`。

不要把真实密钥提交到 Git。公开文档和 issue 中应使用占位值。

### `OPENPE_MODEL`

**控制什么**：请求的模型名称。

**默认值**：无，必填。

```dotenv
OPENPE_MODEL=your-model
```

**修改后的影响**：后续增强由新模型生成。输出风格、延迟、成本和遵循指令的能力都可能变化。

模型名称不会决定 API 协议。即使模型名包含 `claude`，只要端点使用 `/v1/chat/completions`，仍应使用 `OPENPE_PROVIDER=openai`。

### `OPENPE_PROVIDER`

**控制什么**：模型 API 的请求格式。

**默认值**：`openai`。

| 值 | 实际请求 | 认证方式 | 适用端点 |
|---|---|---|---|
| `openai` | `POST /v1/chat/completions` | `Authorization: Bearer` | OpenAI-compatible API |
| `anthropic` | `POST /v1/messages` | `x-api-key` + `anthropic-version` | Anthropic Messages API |

```dotenv
OPENPE_PROVIDER=anthropic
```

**修改后的影响**：请求路径、请求 JSON 和认证头都会切换。它不会修改模型名或 API 地址。

**如何选择**：查看网关文档中的请求路径。协议由端点决定，不由供应商名称或模型名称决定。

### `OPENPE_TIMEOUT`

**控制什么**：一次模型 API 调用最多等待多久。

**默认值**：`60s`。

```dotenv
OPENPE_TIMEOUT=120s
```

**修改后的影响**：

- 调大：允许慢模型继续生成，但 hook 等待更久；
- 调小：失败反馈更快，但长响应更容易被提前取消。

它只控制单次模型调用。Devin 整个 hook 的总时间还受 `OPENPE_HOOK_DEADLINE` 和宿主 timeout 限制。

### `OPENPE_MAX_TOKENS`

**控制什么**：模型最多生成多少 token。

**默认值**：`0`。

```dotenv
OPENPE_MAX_TOKENS=4096
```

**修改后的影响**：

- Anthropic：该字段必填；`0` 时 openPE 使用 4096。值过小可能导致增强结果提前结束，调大可能增加响应时间和成本；
- OpenAI-compatible：当前 openPE 不主动发送输出上限，由网关或模型默认值决定。

它不减少输入历史。输入侧预算由 `OPENPE_MAX_CONTEXT_TOKENS` 控制。

## 增强结果的内容和结构

### `OPENPE_PROMPT_STYLE`

**控制什么**：增强结果写得多简洁或多详细。

**默认值**：`agent`。

假设输入：

```text
pe 给这个接口补测试
```

`agent` 的结果通常接近：

```text
为当前接口补充测试，覆盖正常返回、参数校验和错误路径；保留现有测试风格，并运行对应测试命令验证。
```

`human` 的结果通常会展开为：

```text
目标：
为当前接口建立完整的测试覆盖。

测试范围：
1. 正常输入与预期返回；
2. 缺失或非法参数；
3. 依赖失败和内部错误；
4. 现有行为的回归验证。

完成后运行对应测试命令，并说明新增覆盖范围和仍未覆盖的边界。
```

| 值 | 可预期的变化 | 适合场景 |
|---|---|---|
| `agent` | 输出较短，重点是下游编码代理可以直接执行的任务和验证要求 | 日常编码任务，推荐默认 |
| `human` | 输出更长，使用目标、步骤和验证等分段 | 希望人工阅读详细任务说明 |

```dotenv
OPENPE_PROMPT_STYLE=human
```

**不会影响**：历史读取方式、API 协议、是否注入、输入上下文预算。

如果设置了 `OPENPE_SYSTEM_PROMPT` 或 `OPENPE_SYSTEM_PROMPT_FILE`，自定义系统提示会覆盖该风格选择。

### `OPENPE_MESSAGE_STYLE`

**控制什么**：openPE 如何把已经读取到的会话历史、参考资料和当前任务组织后发送给模型。

**默认值**：`flatten`。

该参数不控制历史数量。历史条数和字符数由各客户端的 `*_MAX_MESSAGES`、`*_MAX_CHARS` 控制。

假设历史是：

```text
用户：缓存应该选 Redis 还是进程内缓存？
助手：当前服务只有一个实例，先使用进程内缓存。
当前：pe 按刚才的方案继续，并补充测试
```

#### `flatten`

发送结构接近：

```text
system: 负责增强当前提示词
user:
  会话历史：
  [user] 缓存应该选 Redis 还是进程内缓存？
  [assistant] 当前服务只有一个实例，先使用进程内缓存。

  当前任务：
  按刚才的方案继续，并补充测试
```

**可预期的结果**：

- 对不同 OpenAI-compatible 网关兼容性最好；
- 模型通常能使用历史，但 user/assistant 角色只是文本标签；
- 复杂对话中，区分“用户决定”和“助手建议”可能不如原生多轮消息准确；
- 没有历史时，与其它布局通常没有明显差别。

#### `hybrid`

发送结构接近：

```text
system: 负责增强最终一条用户任务，前文只用于理解
user: 缓存应该选 Redis 还是进程内缓存？
assistant: 当前服务只有一个实例，先使用进程内缓存。
user: 请增强当前任务：按刚才的方案继续，并补充测试
```

**可预期的结果**：

- 模型能直接区分用户消息和助手回复；
- “刚才的方案”“选项二”“按你说的继续”等引用通常更容易解析；
- 更不容易把助手的建议当成用户已经确认的要求；
- 不会增加模型调用次数；相同内容的 token 数可能因 API 消息模板略有变化；
- 某些模型可能倾向继续回答历史对话，而不是改写当前任务，因此 openPE 会额外说明只有最后一条用户消息是当前任务；
- Anthropic 要求首条历史是 user，开头孤立的 assistant 消息会被忽略。

```dotenv
OPENPE_MESSAGE_STYLE=hybrid
```

**什么时候尝试**：增强结果经常把“继续之前的方案”写成没有具体方案的通用任务，且反馈确认本次确实带入了历史。

**怎么判断有效**：修改后再次提交同一类前文引用，增强结果应明确写出历史中的具体方案，例如“继续使用进程内缓存并补充测试”，而不是只写“继续之前的方案”。

#### `structured`

当请求还包含规则、文件或检索资料时，发送结构接近：

```text
system: 负责增强最终一条用户任务
user: 只读参考资料（项目规则、文件内容、检索结果）
user: 历史中的用户消息
assistant: 历史中的助手回复
user: 当前需要增强的任务
```

**可预期的结果**：

- 模型更容易区分参考资料、历史对话和当前任务；
- 更不容易把文件中的文字或历史状态直接改写成新的执行要求；
- 没有 rules、files 或 retrieval 时，与 `hybrid` 的差别很小；
- 不会增加模型调用次数，但消息数量更多，效果更依赖模型对结构化多消息的理解。

```dotenv
OPENPE_MESSAGE_STYLE=structured
```

**什么时候尝试**：增强请求经常包含项目规则、文件片段或检索结果，而且模型会把参考资料中的内容错误地当作用户指令。

`hybrid` 和 `structured` 当前仍是实验选项。它们改变模型看到的信息结构，不保证对所有模型都优于 `flatten`。切换后应使用实际的上下文依赖任务比较结果。

### `OPENPE_MAX_CONTEXT_TOKENS`

**控制什么**：发送给模型的总输入预算，包含系统提示、当前任务、历史、规则、文件和检索资料。

**默认值**：`0`，表示不限制。

```dotenv
OPENPE_MAX_CONTEXT_TOKENS=8000
```

**修改后的影响**：

- 设置正数后，openPE 按约 4 字符/token 估算输入；
- 空间不足时先裁剪较旧历史和可选参考资料；
- 当前任务和必要系统要求不会被静默删除；
- 如果仅必要内容已经超过预算，本次增强返回错误；
- 值越小，输入成本和延迟通常越低，但增强结果可能缺少早期历史或文件细节。

它不限制模型输出长度。输出由 `OPENPE_MAX_TOKENS` 或网关默认值控制。

### `OPENPE_SYSTEM_PROMPT_FILE` / `OPENPE_SYSTEM_PROMPT`

**控制什么**：完全替换 openPE 内置的系统提示词。

优先级：

```text
OPENPE_SYSTEM_PROMPT_FILE
  > OPENPE_SYSTEM_PROMPT
  > OPENPE_PROMPT_STYLE
  > 内置默认
```

```dotenv
OPENPE_SYSTEM_PROMPT_FILE=/absolute/path/to/system-prompt.txt
# OPENPE_SYSTEM_PROMPT=Write only the enhanced prompt...
```

**修改后的影响**：增强规则、输出风格和安全约束都由你的自定义提示决定。`OPENPE_PROMPT_STYLE` 不再生效。

**风险**：错误的自定义提示可能让模型直接回答任务、改变用户语言、加入未要求的动作或输出额外解释。建议先用裸 CLI 测试，再用于 hook 注入模式。

## 状态语言、内容提醒和语言守卫

### `OPENPE_LANGUAGE`

**控制什么**：openPE 自己的状态和错误信息使用中文还是英文。

**默认值**：`zh`。

```dotenv
OPENPE_LANGUAGE=en
```

**修改后的影响**：只改变 openPE 的提示文案，不要求模型用该语言输出。模型输出语言由用户输入和语言守卫决定。

### `OPENPE_WARNINGS_ENABLED`

**控制什么**：是否检查增强结果中的上下文外数字和用户未要求的高风险动作。

**默认值**：`true`。

```dotenv
OPENPE_WARNINGS_ENABLED=false
```

开启时，如果输入是：

```text
pe 整理这个发布计划
```

而增强结果增加了输入中不存在的“在 3 天内发布”或“直接 push 到远程”，交付信息会提醒你核对数字或动作。

**修改后的影响**：关闭后不再显示这些提醒，但增强结果本身不会因此改变。提醒只做检查，不重写、不阻断。

四个正式 hook 和 HTTP `warnings` 字段都会返回提醒。

### `OPENPE_WARNINGS_ACTIONS`

**控制什么**：在内置 push、部署、删除、发布、支付等动作之外，增加你希望检查的动作词。

**默认值**：空。

```dotenv
OPENPE_WARNINGS_ACTIONS=truncate,migrate
```

**修改后的影响**：如果增强结果包含这些词，而原始输入没有提到，对应动作会出现在提醒中。

### `OPENPE_WARNINGS_NUM_MAXLEN`

**控制什么**：参与“上下文外数字”检查的数字串最大长度。

**默认值**：`5`。

```dotenv
OPENPE_WARNINGS_NUM_MAXLEN=8
```

**修改后的影响**：

- 调大：更长的数字也会被检查，可能把 ID、时间戳或哈希片段当成普通数字；
- 调小：减少 ID 类误报，但也可能漏掉真实数值。

标识符中的数字（如 `v2`、`P95`）不按普通数字检查。

### `OPENPE_LANGUAGE_GUARD_ENABLED`

**控制什么**：检测增强输出是否明显偏离用户输入的主导语言。

**默认值**：`true`。

```dotenv
OPENPE_LANGUAGE_GUARD_ENABLED=false
```

**修改后的影响**：关闭后，不再检测语言偏移，也不会因语言问题触发重试或提醒。

混合文本没有明显主导语言时，守卫不动作。例如英文句子夹少量中文词，或中文句子包含较长英文标识符，不会仅凭少数异文字触发重试。

### `OPENPE_LANGUAGE_GUARD_REANCHOR`

**控制什么**：发现明确语言偏移后，是否额外请求一次模型进行修正。

**默认值**：`true`。

| 值 | 可观察结果 |
|---|---|
| `true` | 明确偏移时最多额外调用一次；修正成功则返回正确语言，失败则保留原结果并显示提醒 |
| `false` | 不额外调用模型；直接保留原结果并显示提醒 |

```dotenv
OPENPE_LANGUAGE_GUARD_REANCHOR=false
```

如果你更重视固定调用次数和延迟，可设为 `false`。如果更重视输出语言一致性，保持默认。

## Review、注入、缓存和剪贴板

### `OPENPE_HOOK_INJECT` 与客户端覆盖

**控制什么**：增强完成后是等待用户检查，还是直接注入客户端。

**默认值**：`false`。

| 配置 | 用户看到的结果 |
|---|---|
| `false` | 原始 `pe` 被阻断；增强结果进入剪贴板和缓存；用户检查后手动发送 |
| `true` | Codex、Claude Code、Devin 直接收到增强结果作为附加上下文，通常无需粘贴 |
| Windsurf | 始终保持 review；其 hook 不支持注入 |

```dotenv
OPENPE_HOOK_INJECT=true
```

客户端可覆盖全局值：

```dotenv
OPENPE_CODEX_INJECT=true
OPENPE_CLAUDE_INJECT=false
OPENPE_DEVIN_INJECT=true
```

优先级：客户端值 > 全局值 > `false`。

**副作用**：注入模式减少一次复制粘贴，但用户无法在发送前检查增强结果。结果仍会写入缓存，可用 `hook last --prompt` 审计。

### `OPENPE_CACHE_DIR`

**控制什么**：最近一次增强结果的缓存根目录。

```dotenv
OPENPE_CACHE_DIR=/custom/path/openpe-cache
```

openPE 会自动增加客户端子目录：

```text
<root>/codex/last-prompt.txt
<root>/claude/last-prompt.txt
<root>/devin/last-prompt.txt
<root>/windsurf/last-prompt.txt
```

**修改后的影响**：`hook last`、缓存路径和失败回退都改到新目录。它不改变模型请求。

### `OPENPE_COPY_COMMAND`

**控制什么**：覆盖自动选择的剪贴板命令。

```dotenv
OPENPE_COPY_COMMAND=xclip -selection clipboard
```

**什么时候修改**：openPE 没有检测到正确的桌面剪贴板工具，或你需要使用 WSL 的 `clip.exe`。

原生 Windows 通过 `cmd.exe /C` 执行；POSIX/WSL 通过 `sh -c` 执行。命令从 stdin 接收增强提示词。

### `OPENPE_DISABLE_OSC52_CLIPBOARD`

**控制什么**：是否禁止使用 OSC52 终端剪贴板序列作为回退方式。

**默认值**：`false`。

```dotenv
OPENPE_DISABLE_OSC52_CLIPBOARD=true
```

**修改后的影响**：启用后，系统剪贴板命令失败时不再尝试 OSC52，直接提示使用缓存。适合 IDE 子进程、明确不支持 OSC52 的终端，或不希望终端处理剪贴板序列的环境。

### `OPENPE_OSC52_TTY`

**控制什么**：OSC52 序列写入哪个 TTY。

**默认值**：`/dev/tty`。

```dotenv
OPENPE_OSC52_TTY=/dev/pts/1
```

只在自动检测到的 TTY 不正确时修改。IDE hook 子进程通常没有控制 TTY，修改该值也不一定能使 OSC52 可用。

### `OPENPE_CLAUDE_PROMPT_FALLBACK`

**控制什么**：Claude 剪贴板失败时，是否在 blocked feedback 中显示完整增强提示词。

**默认值**：`true`。

```dotenv
OPENPE_CLAUDE_PROMPT_FALLBACK=false
```

关闭后反馈更短，但用户需要通过 `openpe claude hook last --prompt` 取回结果。

### `OPENPE_WINDSURF_PROMPT_FALLBACK`

**控制什么**：Windsurf 剪贴板失败时，是否在 feedback 中显示完整增强提示词。

**默认值**：`false`。

```dotenv
OPENPE_WINDSURF_PROMPT_FALLBACK=true
```

开启后更容易直接复制结果，但 Windsurf 可能把长 feedback 压成一行，阅读体验可能较差。

### `OPENPE_HOOK_DEADLINE`

**控制什么**：Devin 聚合 hook 的整个处理流程必须在多久内结束。

**默认值**：`100s`。

```dotenv
OPENPE_HOOK_DEADLINE=90s
```

**修改后的影响**：

- 手动 `pe` 超时：阻断原始消息，并说明没有提交；
- 自动注入超时：放行普通消息，避免长期阻塞会话；
- 安装器会根据 `--hook-timeout` 写入更短的实际 deadline，因此 dotenv 值不一定是最终上限。

该值必须为正数。它与 `OPENPE_TIMEOUT` 不同：前者覆盖整个 Devin hook，后者只覆盖一次模型调用。

## 会话历史

所有历史采集都只读本地文件，并且只发送最近的有限内容。无法可靠定位时，openPE 宁可不带历史，并在反馈中说明。

### Codex

| 参数 | 默认值 | 修改后的实际影响 |
|---|---:|---|
| `OPENPE_CODEX_HISTORY_ENABLED` | `true` | 设为 `false` 后，不再读取 Codex history/rollout；“继续上文”会缺少前文信息 |
| `OPENPE_CODEX_HOME` | Codex 默认目录 | 改变 `history.jsonl` 和 session rollout 的查找根目录 |
| `OPENPE_CODEX_HISTORY_MAX_MESSAGES` | `12` | 调小后只保留更近的消息；调大后引用较早内容的成功率可能提高，但输入成本增加 |
| `OPENPE_CODEX_HISTORY_MAX_CHARS` | `12000` | 限制历史总字符数；超出时优先保留较新的内容 |

```dotenv
OPENPE_CODEX_HISTORY_ENABLED=true
OPENPE_CODEX_HISTORY_MAX_MESSAGES=12
OPENPE_CODEX_HISTORY_MAX_CHARS=12000
```

### Claude Code

| 参数 | 默认值 | 修改后的实际影响 |
|---|---:|---|
| `OPENPE_CLAUDE_TRANSCRIPT_ENABLED` | `true` | 设为 `false` 后，不再读取 hook 提供的 transcript |
| `OPENPE_CLAUDE_TRANSCRIPT_MAX_MESSAGES` | `12` | 控制最多保留多少条最近的 user/assistant 消息 |
| `OPENPE_CLAUDE_TRANSCRIPT_MAX_CHARS` | `12000` | 控制 transcript 历史总字符数，超出时保留较新内容 |

```dotenv
OPENPE_CLAUDE_TRANSCRIPT_ENABLED=true
OPENPE_CLAUDE_TRANSCRIPT_MAX_MESSAGES=12
OPENPE_CLAUDE_TRANSCRIPT_MAX_CHARS=12000
```

### Devin

| 参数 | 默认值 | 修改后的实际影响 |
|---|---:|---|
| `OPENPE_DEVIN_HISTORY_ENABLED` | `true` | 设为 `false` 后，不读取 Devin session 数据库 |
| `OPENPE_DEVIN_HISTORY_DB_PATH` | 按平台推断 | 指向另一个 `sessions.db`；路径错误时本次不带历史并显示原因 |
| `OPENPE_DEVIN_HISTORY_MAX_MESSAGES` | `12` | 控制最多带入多少条最近消息 |
| `OPENPE_DEVIN_HISTORY_MAX_CHARS` | `12000` | 控制历史总字符数 |
| `OPENPE_DEVIN_HISTORY_RECENCY` | `6h` | 只影响无法精确识别 session 时的回退路径；调大可覆盖更久前的 session，也增加复用陈旧会话的可能 |

Linux 上优先沿 Devin 进程识别当前 session；识别成功时不受 `RECENCY` 限制。非 Linux 或识别失败时，才按工作目录和最近活跃时间回退。

```dotenv
OPENPE_DEVIN_HISTORY_ENABLED=true
OPENPE_DEVIN_HISTORY_RECENCY=6h
OPENPE_DEVIN_HISTORY_MAX_MESSAGES=12
OPENPE_DEVIN_HISTORY_MAX_CHARS=12000
```

## Devin 多 hook 去重

Devin 当前会加载 Devin 格式和 Claude Code 格式的 hook；不加载 Windsurf 格式 hook。如果两种 openPE hook 都安装，一次提交可能启动多个 openPE 进程。

### `OPENPE_HOOK_DEDUP_ENABLED`

**默认值**：`true`。

```dotenv
OPENPE_HOOK_DEDUP_ENABLED=true
```

开启后：

- 第一个 hook 执行模型增强；
- Review 模式下，后续 hook 重放同一阻断结果、提醒和增强提示词；
- 注入模式下，后续 hook 跳过，避免重复注入；
- 第一个 hook 尚未给出结论时，手动 `pe` 仍保持阻断，不放行原始消息。

关闭后，同一次提交可能重复调用模型、重复复制或重复注入。

### `OPENPE_HOOK_DEDUP_WINDOW`

**默认值**：`5s`。

```dotenv
OPENPE_HOOK_DEDUP_WINDOW=5s
```

窗口应覆盖多个 hook 的启动间隔。调得过小可能失去去重效果；调得过大时，用户短时间内有意重复同一文本也可能被视为同一次提交并重放上次结果。

## 本地 HTTP server 和 IDE patch

普通 hook 不需要运行 `openpe-server`。以下参数仅用于本地 HTTP 自动化或实验性 IDE patch。

### `OPENPE_LISTEN_ADDR`

**默认值**：`127.0.0.1:18980`。

```dotenv
OPENPE_LISTEN_ADDR=127.0.0.1:18980
```

server 只允许 `127.0.0.1`、`::1` 或 `localhost`。`0.0.0.0`、`::`、LAN IP 和其它主机名会启动失败。远程访问需要在外层使用 TLS 反向代理或隧道。

### `OPENPE_SERVER_TOKEN`

**控制什么**：为 `/v1/*` 增加 bearer 认证。

```dotenv
OPENPE_SERVER_TOKEN=replace-with-64-lowercase-hex-characters
```

设置后，客户端必须发送：

```text
Authorization: Bearer <token>
```

`/healthz` 始终免鉴权。启用 lifecycle 时，token 必须是 64 位小写十六进制；未配置时 server 会生成临时 token。

### `OPENPE_SERVER_CORS_ORIGINS`

**控制什么**：允许哪些浏览器或 Electron Origin 调用本地 server。

```dotenv
OPENPE_SERVER_CORS_ORIGINS=vscode-file://vscode-app
```

多个 Origin 用逗号分隔。只添加实际客户端报告的 Origin，不要猜测或使用 `*`。普通 CLI、hook 和 curl 不需要 CORS。

### `OPENPE_SERVER_LIFECYCLE_ENABLED`

**控制什么**：server 成功监听后，是否写出供 IDE installer 发现的本地 descriptor。

**默认值**：`false`。

```dotenv
OPENPE_SERVER_LIFECYCLE_ENABLED=true
```

开启后会写入 server 地址、token、PID 和版本。普通 hook 不需要开启；实验性 IDE patch 需要。

### `OPENPE_SERVER_DESCRIPTOR_FILE`

**控制什么**：覆盖 descriptor 文件路径。

```dotenv
OPENPE_SERVER_DESCRIPTOR_FILE=/custom/path/server.json
```

默认路径是 `~/.config/openpe/server.json`。文件包含 bearer token，openPE 会限制其权限。只在默认路径不适合当前安装环境时修改。

## Openace（已废弃）

Openace 上下文来源默认关闭，不影响核心增强。新部署不建议依赖它。以下参数仅为已有部署兼容保留。

| 参数 | 默认值 | 实际影响 |
|---|---:|---|
| `OPENPE_OPENACE_ENABLED` | `false` | 开启后，在有 cwd 时向 Openace 请求代码检索上下文；失败不会替代核心增强 |
| `OPENPE_OPENACE_ADDR` | `127.0.0.1:8765` | Openace daemon 地址；旧别名为 `OPENACE_DAEMON_ADDR` |
| `OPENPE_OPENACE_TOKEN` | 空 | daemon 需要认证时发送；旧别名为 `OPENACE_DAEMON_TOKEN` |
| `OPENPE_OPENACE_PROVIDER_PROFILE_ID` | 空 | 指定 Openace provider profile |
| `OPENPE_OPENACE_MAX_OUTPUT_LENGTH` | `12000` | 检索结果传入增强请求前的最大字符数 |
| `OPENPE_OPENACE_TIMEOUT` | `30s` | 一次 Openace 请求最长等待时间 |
| `OPENPE_OPENACE_MAX_RETRIES` | `2` | 临时错误的最大重试次数；调大可能延长整体等待 |
| `OPENPE_OPENACE_RETRY_BASE_DELAY` | `250ms` | 第一次重试等待基数 |
| `OPENPE_OPENACE_RETRY_MAX_DELAY` | `2s` | 单次退避等待上限 |
| `OPENPE_OPENACE_RETRY_JITTER` | `100ms` | 给重试等待增加随机偏移，减少并发请求同时重试 |

## 少用的 hook 运行参数

以下环境变量主要用于调试或安装器生成的 hook 命令，普通用户通常无需设置：

| 参数 | 默认值 | 实际影响 |
|---|---:|---|
| `OPENPE_CODEX_TERMINAL_PREVIEW` | `false` | Codex block 时尝试把完整预览写到 `/dev/tty` |
| `OPENPE_CODEX_COPY_PREVIEW` | `false` | Codex runner 是否额外执行剪贴板交付 |
| `OPENPE_DEVIN_TERMINAL_PREVIEW` | `false` | Devin block 时尝试向终端直接写完整预览 |
| `OPENPE_DEVIN_COPY_PREVIEW` | `true` | Devin review 模式是否复制增强提示词 |
| `OPENPE_HOOK_SCOPE` | 空 | 标记 hook 是 user 还是 project scope，用于避免已知重复配置 |

优先使用安装命令的对应 flag，不建议手工维护这些值。

## 如何验证配置修改

### 验证模型 API

```bash
OPENPE_ENV_FILE="$HOME/.config/openpe/.env" \
openpe enhance --prompt "帮我整理这个需求"
```

### 比较提示词风格

```bash
OPENPE_PROMPT_STYLE=agent openpe enhance --prompt "给这个接口补测试"
OPENPE_PROMPT_STYLE=human openpe enhance --prompt "给这个接口补测试"
```

### 比较消息布局

选择一个真正依赖前文的 prompt，例如“按刚才选定的缓存方案继续”。分别设置：

```bash
OPENPE_MESSAGE_STYLE=flatten openpe enhance --prompt "按刚才的方案继续，并补充测试"
OPENPE_MESSAGE_STYLE=hybrid openpe enhance --prompt "按刚才的方案继续，并补充测试"
OPENPE_MESSAGE_STYLE=structured openpe enhance --prompt "按刚才的方案继续，并补充测试"
```

裸 CLI 自身没有客户端会话历史时，三者可能没有明显差别。应在实际 hook 会话中比较，并确认反馈显示本次已带入历史。

### 验证最终生效值

Shell 环境变量优先于 `.env`。如果文件中的修改看似无效，先检查：

```bash
env | grep '^OPENPE_'
```

确认没有旧的 shell 变量覆盖 dotenv。
