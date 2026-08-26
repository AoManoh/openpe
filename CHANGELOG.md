# 更新日志

本文件面向 openPE 的使用者，记录每个发布版本可感知的变化。版本号遵循[语义化版本](https://semver.org/lang/zh-CN/)，日期为 tag 推送日。

## [1.0.0] - 2026-08-26

openPE 的首个发布版本。openPE 是本地优先的 prompt 增强工具链：在 Codex CLI、Claude Code、Devin CLI、Windsurf 的对话框输入 `pe <内容>`，它拦截原始消息、结合会话历史调用你配置的模型生成增强提示词，复制到剪贴板供你检查后发送。

### 核心能力（首次发布的完整功能面）

- 四客户端 hook：`openpe codex|claude|devin|windsurf hook install` 一条命令接入；默认 review 模式（拦截 + 剪贴板，绝不代发），Codex/Claude/Devin 可选注入模式。
- 上下文感知：自动读取当前客户端的会话历史（可关），反馈中明确披露"带入了多少条历史、没带入时的原因"，不静默降级。
- 增强质量防护：语言守卫保持输出语言与输入一致；内容警告提示"上下文外数字""你未决策的高风险动作"，提醒先读再发。
- 本地 HTTP API（`openpe-server`，仅 loopback）与裸 CLI（`openpe enhance`）供测试与自动化。

### 相对早期 main 构建的新增（老用户升级后能感知的变化）

- **用户规范加载（`pe+`）**：把反复手打的约束文本（自检清单、写作/提交规范等）保存为 `~/.config/openpe/specs/<名字>.md`，输入 `pe+<名字> <任务>`（可 `pe+a+b` 连用）后，规范原文会逐字追加到增强结果尾部，随剪贴板一起交付；点名的规范不存在、为空或超限时本次增强直接报错阻断，绝不静默丢弃。裸 CLI 对应 `enhance --spec <名字>`。详见 [CLIENTS.md](CLIENTS.md#用户规范点名pe)。
- **版本查询**：`openpe --version` 与 `openpe-server --version` 现在输出真实版本（发布 tag、或本地构建的伪版本），不再恒显 `dev`。
- **一键升级（`openpe update`）**：查询 Go module proxy（遵循你的 `GOPROXY` 设置）后自动重装两个二进制；`--check` 只查不装；远端不比当前新时不安装（永不降级）；失败明确报错并给出手动命令。
- **新版本提醒**：有新发布版本时，`pe` 反馈的披露行会出现一次"发现新版本 …，运行 openpe update 升级"。检查在后台限频进行（默认 24 小时一次）并只写本地缓存，增强过程本身不发起任何网络请求；`OPENPE_UPDATE_NOTICE=false` 可关闭。
- **安装方式变更**：推荐安装/升级统一为 `go install github.com/AoManoh/openpe/cmd/openpe@latest`（README 已更新）；克隆仓库本地构建仍可用，作为开发者路径。
- 新增配置项：`OPENPE_SPECS_DIR`、`OPENPE_SPEC_MAX_CHARS`、`OPENPE_UPDATE_NOTICE`、`OPENPE_UPDATE_CHECK_INTERVAL`，说明见 [CONFIG.md](CONFIG.md)。
- 文档修复：恢复了 README/CLIENTS 在早前重构中丢失的各客户端触发演示截图。

### 升级方法

已安装早期版本的用户执行任一方式：

```bash
openpe update                                              # 新版本内置（推荐）
go install github.com/AoManoh/openpe/cmd/openpe@v1.0.0     # 或显式指定版本
go install github.com/AoManoh/openpe/cmd/openpe-server@v1.0.0
```

升级后用 `openpe --version` 确认输出 `v1.0.0`；`openpe-server` 若在运行需重启。

[1.0.0]: https://github.com/AoManoh/openpe/releases/tag/v1.0.0
