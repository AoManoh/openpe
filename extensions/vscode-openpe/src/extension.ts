import * as vscode from "vscode";
import { buildEnhanceRequest } from "./core/context";
import { normalizeConfig, mergeTemplates } from "./core/config";
import { builtInTemplates } from "./core/templates";
import { EnhanceTemplate, OpenPEContextFile, OpenPEExtensionConfig } from "./core/types";
import { enhancePrompt } from "./transport";
import { PreviewManager, insertAtCursor, replaceSelection } from "./ui/preview";

const output = vscode.window.createOutputChannel("openPE");
const preview = new PreviewManager();

export function activate(context: vscode.ExtensionContext): void {
  context.subscriptions.push(output);

  context.subscriptions.push(
    vscode.commands.registerCommand("openpe.enhancePrompt", () => runEnhancement("prompt")),
    vscode.commands.registerCommand("openpe.enhanceSelection", () => runEnhancement("selection")),
    vscode.commands.registerCommand("openpe.enhanceCurrentFile", () => runEnhancement("currentFile")),
    vscode.commands.registerCommand("openpe.copyLastEnhancedPrompt", copyLastEnhancedPrompt),
    vscode.commands.registerCommand("openpe.insertLastEnhancedPrompt", insertLastEnhancedPrompt),
    vscode.commands.registerCommand("openpe.replaceSelectionWithLastEnhancedPrompt", replaceLastSelection),
    vscode.commands.registerCommand("openpe.openLastPreview", () => preview.openPreview()),
    vscode.commands.registerCommand("openpe.configure", openSettings)
  );
}

export function deactivate(): void {
  output.dispose();
}

type EnhanceSource = "prompt" | "selection" | "currentFile";

async function runEnhancement(source: EnhanceSource): Promise<void> {
  const config = readConfig();
  const targetEditor = vscode.window.activeTextEditor;
  const collected = await collectPrompt(source, config);
  if (!collected) {
    return;
  }

  const template = await pickTemplate(config);
  if (!template) {
    return;
  }

  const request = buildEnhanceRequest({
    prompt: collected.prompt,
    client: config.client,
    cwd: collected.cwd,
    mode: config.defaultMode,
    template,
    contextFiles: collected.contextFiles,
    maxContextBytes: config.maxContextBytes
  });

  await vscode.window.withProgress(
    {
      location: vscode.ProgressLocation.Notification,
      title: "openPE 正在生成增强提示词",
      cancellable: false
    },
    async () => {
      try {
        output.appendLine(`[openPE] transport=${config.transport} client=${request.client} mode=${request.mode}`);
        const response = await enhancePrompt(request, config);
        preview.update({ request, response, template }, targetEditor);
        await preview.showResult(config.outputMode);
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        output.appendLine(`[openPE] failed: ${message}`);
        vscode.window.showErrorMessage(`openPE 生成失败：${message}`);
      }
    }
  );
}

async function collectPrompt(
  source: EnhanceSource,
  config: OpenPEExtensionConfig
): Promise<{ prompt: string; cwd?: string; contextFiles?: OpenPEContextFile[] } | undefined> {
  const editor = vscode.window.activeTextEditor;
  const workspaceFolder = editor
    ? vscode.workspace.getWorkspaceFolder(editor.document.uri)
    : vscode.workspace.workspaceFolders?.[0];
  const cwd = workspaceFolder?.uri.fsPath;

  if (source === "selection") {
    const selected = getSelectedText(editor);
    if (selected.trim() !== "") {
      return { prompt: selected, cwd };
    }
    return promptFromInputBox("请输入需要增强的提示词", cwd);
  }

  if (source === "currentFile") {
    if (!editor) {
      vscode.window.showWarningMessage("openPE 无法读取当前文件：当前没有活动编辑器。");
      return undefined;
    }

    const task = await vscode.window.showInputBox({
      title: "openPE",
      prompt: "请输入希望 openPE 基于当前文件增强的任务",
      ignoreFocusOut: true
    });
    if (!task || task.trim() === "") {
      return undefined;
    }

    const file = createContextFile(editor);
    const prompt = `用户任务：${task.trim()}\n\n当前文件：${file.path}`;
    return {
      prompt,
      cwd,
      contextFiles: config.includeActiveFileContext ? [file] : undefined
    };
  }

  const selected = getSelectedText(editor);
  return promptFromInputBox("请输入需要增强的提示词", cwd, selected);
}

async function promptFromInputBox(
  prompt: string,
  cwd?: string,
  value?: string
): Promise<{ prompt: string; cwd?: string } | undefined> {
  const text = await vscode.window.showInputBox({
    title: "openPE",
    prompt,
    value,
    ignoreFocusOut: true
  });
  if (!text || text.trim() === "") {
    return undefined;
  }
  return { prompt: text.trim(), cwd };
}

async function pickTemplate(config: OpenPEExtensionConfig): Promise<EnhanceTemplate | undefined> {
  const templates = mergeTemplates(builtInTemplates, config.templates);
  const picked = await vscode.window.showQuickPick(
    templates.map((template) => ({
      label: template.name,
      description: template.mode ? `mode=${template.mode}` : undefined,
      detail: template.description,
      template
    })),
    {
      title: "openPE",
      placeHolder: "选择提示词增强模板",
      ignoreFocusOut: true
    }
  );
  return picked?.template;
}

function getSelectedText(editor: vscode.TextEditor | undefined): string {
  if (!editor) {
    return "";
  }

  return editor.selections
    .filter((selection) => !selection.isEmpty)
    .map((selection) => editor.document.getText(selection))
    .join("\n\n");
}

function createContextFile(editor: vscode.TextEditor): OpenPEContextFile {
  return {
    path: editor.document.uri.fsPath || editor.document.uri.toString(),
    language: editor.document.languageId,
    content: editor.document.getText()
  };
}

async function copyLastEnhancedPrompt(): Promise<void> {
  const prompt = preview.last.response?.enhanced_prompt;
  if (!prompt) {
    vscode.window.showWarningMessage("openPE 尚无增强结果。");
    return;
  }
  await vscode.env.clipboard.writeText(prompt);
  vscode.window.showInformationMessage("openPE 已复制上次增强提示词。");
}

async function insertLastEnhancedPrompt(): Promise<void> {
  const prompt = preview.last.response?.enhanced_prompt;
  if (!prompt) {
    vscode.window.showWarningMessage("openPE 尚无增强结果。");
    return;
  }
  await insertAtCursor(prompt);
}

async function replaceLastSelection(): Promise<void> {
  const prompt = preview.last.response?.enhanced_prompt;
  if (!prompt) {
    vscode.window.showWarningMessage("openPE 尚无增强结果。");
    return;
  }
  await replaceSelection(prompt);
}

async function openSettings(): Promise<void> {
  await vscode.commands.executeCommand("workbench.action.openSettings", "openpe");
}

function readConfig(): OpenPEExtensionConfig {
  const config = vscode.workspace.getConfiguration("openpe");
  return normalizeConfig({
    transport: config.get("transport"),
    executablePath: config.get("executablePath"),
    envFile: config.get("envFile"),
    serverUrl: config.get("serverUrl"),
    client: config.get("client"),
    defaultMode: config.get("defaultMode"),
    outputMode: config.get("outputMode"),
    includeActiveFileContext: config.get("includeActiveFileContext"),
    maxContextBytes: config.get("maxContextBytes"),
    timeoutMs: config.get("timeoutMs"),
    templates: config.get("templates")
  });
}
