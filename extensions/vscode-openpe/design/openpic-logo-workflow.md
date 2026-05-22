# openPE 插件 Logo 设计流程

## 当前状态

- 仓库内没有既有 openpic/openPic 代码接入。
- 本次通过 OpenPic MCP 的异步任务生成 logo 概念图，任务编号：
  `tsk_2789785_20260522T022845.079270049Z_2dcb25480001`
- 任务已完成，概念图路径：
  `media/openpic/openpe-logo-concept-20260522-023047-cd27dba3.png`
- 当前 VSIX 直接使用手工整理后的 SVG 资产：
  - `media/openpe-icon.svg`：彩色插件 logo。
  - `media/openpe-activity.svg`：单色 Activity Bar / 侧边栏图标候选。
  - `media/openpe-icon.png`：由离线脚本生成的 VSIX manifest 图标。

## 设计说明

图形语义：

- 对话卡片：prompt enhancement 的输入/输出管理。
- 命令光标：CLI 与 coding agent 场景。
- 高亮星芒：增强、改写与质量提升。
- 青绿色与琥珀色：本地工程工具感与 prompt 增强的动作感。

## 可扩展接入点

当前不在插件运行时引入 openpic 依赖。若后续需要把 logo 生成流程自动化，可新增一个离线工具脚本，并用下面的最小契约隔离实际 openpic 调用细节：

```ts
interface OpenPicLogoRequest {
  prompt: string;
  outputDir: string;
  filenamePrefix: string;
  size: "1024x1024" | "2048x2048";
  outputFormat: "png" | "webp" | "jpeg";
}

interface OpenPicLogoTask {
  taskId: string;
  state: "queued" | "running" | "completed" | "failed" | "cancelled" | "abandoned";
  outputPath?: string;
  error?: string;
}

interface OpenPicLogoGenerator {
  submit(request: OpenPicLogoRequest): Promise<OpenPicLogoTask>;
  getResult(taskId: string): Promise<OpenPicLogoTask>;
}
```

TODO：补充真实 openpic SDK、CLI 或 MCP 调用方式后，再把该契约落成 `tools/` 下的离线脚本。不要让插件运行时依赖 openpic。
