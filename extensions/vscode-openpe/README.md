# openPE VSIX Prompt Manager

这是 openPE 面向 VS Code、Windsurf、Cursor 等兼容 VSIX 的现代 IDE 的提示词增强管理插件初版。

插件只负责 IDE 侧输入采集、调用 openPE、展示和交付增强结果，不重复实现 openPE core，也不拦截 IDE 原生 Chat、Composer、Cascade 的提交事件。

## 功能范围

- 通过命令面板触发提示词增强。
- 将当前选中文本、当前文件任务或手动输入作为增强对象。
- 选择内置模板或用户配置模板。
- 通过 CLI 或 HTTP 调用本地 openPE。
- 展示增强后的 prompt，并支持复制、插入光标或替换选区。
- 使用 `media/openpe-icon.svg` 作为 logo 设计源，并用 `media/openpe-icon.png` 作为 VSIX manifest 图标。

## 前置条件

- 已安装 Node.js 与 npm。
- 已安装并配置 openPE CLI，或已启动 openPE HTTP server。
- CLI 模式需要 `openpe` 可在 PATH 中执行，或在 `openpe.executablePath` 中配置绝对路径。

## 安装依赖与构建

```bash
cd extensions/vscode-openpe
npm install
npm run build:icon
npm run compile
```

`build:icon` 会用 Python + Pillow 生成 VSIX manifest 需要的 `media/openpe-icon.png`。SVG 仍是设计源；PNG 是为了满足 `vsce` 对扩展图标格式的要求。

## 调试

在 VS Code 或兼容 IDE 中打开本仓库，然后运行扩展调试配置或使用命令行编译后加载扩展开发主机。

常用命令：

```bash
npm run watch
```

## 打包 VSIX

```bash
cd extensions/vscode-openpe
npm run package
```

生成的 `vscode-openpe-*.vsix` 可在 VS Code、Windsurf 或其他兼容 VSIX 的 IDE 中手动安装。

## 使用方式

1. 在 IDE 设置中搜索 `openpe`。
2. 根据环境选择传输方式：
   - `openpe.transport = cli`：默认方式，调用 `openpe enhance --json`，无需启动服务。
   - `openpe.transport = http`：调用 `/v1/prompt-enhance`，适合携带当前文件上下文。
3. 如果使用 Windsurf，可将 `openpe.client` 设置为 `windsurf`。
4. 运行命令：
   - `openPE: Enhance Prompt`
   - `openPE: Enhance Selection`
   - `openPE: Enhance Current File`
5. 插件会展示增强后的 prompt，并支持复制、插入或替换。

## 配置项

- `openpe.transport`：`cli` 或 `http`。
- `openpe.executablePath`：CLI 路径，默认 `openpe`。
- `openpe.envFile`：可选 `.env` 路径，会通过 `OPENPE_ENV_FILE` 传给 CLI。
- `openpe.serverUrl`：HTTP server 地址，默认 `http://127.0.0.1:18980`。
- `openpe.client`：`vscode`、`windsurf`、`cursor` 或 `generic`。
- `openpe.defaultMode`：默认增强模式，默认 `agent`。
- `openpe.outputMode`：`preview`、`clipboard`、`insert` 或 `replaceSelection`。
- `openpe.includeActiveFileContext`：是否携带当前文件内容。
- `openpe.maxContextBytes`：当前文件上下文字节上限。
- `openpe.templates`：用户自定义模板列表。

## 当前限制

- 插件不承诺拦截 IDE 原生 Chat、Composer、Cascade 的 pre-submit 行为。
- CLI 传输无法使用完整 canonical request，插件会将文件上下文折叠进 prompt；需要结构化上下文时请使用 HTTP 传输。
- 插入和替换只作用于普通编辑器，不作用于 IDE 私有聊天输入框。
- 当前 logo 的 PNG 由离线脚本生成；如本机缺少 Pillow，可保留仓库中已生成的 `media/openpe-icon.png`。

## 后续方向

- 增加本地服务健康检查与一键启动提示。
- 增加模板管理 UI。
- 增加 VS Code extension test，覆盖真实扩展宿主行为。
- 如 Cursor/Windsurf 暴露稳定公开 API，再新增专用适配层。
