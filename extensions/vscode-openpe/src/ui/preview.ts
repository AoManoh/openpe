import * as vscode from "vscode";
import { EnhancementState, OpenPEOutputMode } from "../core/types";

export class PreviewManager {
  private state: EnhancementState = {};
  private targetEditor?: vscode.TextEditor;

  public get last(): EnhancementState {
    return this.state;
  }

  public update(state: EnhancementState, targetEditor?: vscode.TextEditor): void {
    this.state = state;
    this.targetEditor = targetEditor;
  }

  public async showResult(outputMode: OpenPEOutputMode): Promise<void> {
    const prompt = this.state.response?.enhanced_prompt;
    if (!prompt) {
      vscode.window.showWarningMessage("openPE 尚无增强结果。");
      return;
    }

    if (outputMode === "clipboard") {
      await vscode.env.clipboard.writeText(prompt);
      vscode.window.showInformationMessage("openPE 已生成增强提示词并复制到剪贴板。");
      return;
    }

    if (outputMode === "insert") {
      await insertAtCursor(prompt, this.targetEditor);
      return;
    }

    if (outputMode === "replaceSelection") {
      await replaceSelection(prompt, this.targetEditor);
      return;
    }

    await this.openPreview();
  }

  public async openPreview(): Promise<void> {
    const prompt = this.state.response?.enhanced_prompt;
    if (!prompt) {
      vscode.window.showWarningMessage("openPE 尚无增强结果。");
      return;
    }

    const document = await vscode.workspace.openTextDocument({
      content: prompt,
      language: "markdown"
    });
    await vscode.window.showTextDocument(document, { preview: false, viewColumn: vscode.ViewColumn.Beside });
    await vscode.env.clipboard.writeText(prompt);

    const action = await vscode.window.showInformationMessage(
      "openPE 已生成增强提示词，并已复制到 IDE 剪贴板。",
      "插入光标",
      "替换选区"
    );
    if (action === "插入光标") {
      await insertAtCursor(prompt, this.targetEditor);
    }
    if (action === "替换选区") {
      await replaceSelection(prompt, this.targetEditor);
    }
  }
}

export async function insertAtCursor(text: string, targetEditor?: vscode.TextEditor): Promise<void> {
  const editor = targetEditor ?? vscode.window.activeTextEditor;
  if (!editor) {
    vscode.window.showWarningMessage("openPE 无法插入：当前没有活动编辑器。");
    return;
  }

  await editor.edit((builder) => {
    for (const selection of editor.selections) {
      builder.insert(selection.active, text);
    }
  });
}

export async function replaceSelection(text: string, targetEditor?: vscode.TextEditor): Promise<void> {
  const editor = targetEditor ?? vscode.window.activeTextEditor;
  if (!editor) {
    vscode.window.showWarningMessage("openPE 无法替换：当前没有活动编辑器。");
    return;
  }

  await editor.edit((builder) => {
    for (const selection of editor.selections) {
      builder.replace(selection, text);
    }
  });
}
