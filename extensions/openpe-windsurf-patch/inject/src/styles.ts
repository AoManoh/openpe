/**
 * CSS rules for the openPE logo button and the enhancement dialog.
 *
 * All colour / border tokens reference `var(--vscode-*)` variables so
 * the UI inherits Windsurf's current theme (dark / light / custom) with
 * zero manual adaptation.
 */

const STYLE_ID = "openpe-inject-styles";

const CSS = `
.openpe-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  margin: 0 4px;
  padding: 0;
  border: 1px solid var(--vscode-input-border, transparent);
  border-radius: 4px;
  background: transparent;
  color: var(--vscode-input-foreground, #d4d4d4);
  cursor: pointer;
  line-height: 1;
  transition: background 80ms ease-in-out;
}
.openpe-btn-icon {
  width: 18px;
  height: 18px;
  display: block;
  border-radius: 4px;
  pointer-events: none;
}
.openpe-btn:hover {
  background: var(--vscode-toolbar-hoverBackground, rgba(255,255,255,0.08));
}
.openpe-btn:active {
  background: var(--vscode-toolbar-activeBackground, rgba(255,255,255,0.12));
}

.openpe-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0,0,0,0.45);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 2147483647;
}
.openpe-modal {
  background: var(--vscode-editor-background, #1e1e1e);
  color: var(--vscode-editor-foreground, #d4d4d4);
  border: 1px solid var(--vscode-widget-border, #3c3c3c);
  border-radius: 6px;
  width: min(720px, 92vw);
  max-height: 86vh;
  display: flex;
  flex-direction: column;
  padding: 16px 20px;
  font-family: var(--vscode-font-family, ui-sans-serif, system-ui);
  font-size: 13px;
  box-shadow: 0 8px 28px rgba(0,0,0,0.45);
}
.openpe-modal-header {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  margin-bottom: 12px;
}
.openpe-modal-title {
  font-size: 14px;
  font-weight: 600;
}
.openpe-modal-close {
  background: transparent;
  border: 0;
  color: inherit;
  cursor: pointer;
  font-size: 16px;
  line-height: 1;
}
.openpe-modal-body {
  flex: 1 1 auto;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.openpe-input {
  width: 100%;
  min-height: 90px;
  resize: vertical;
  padding: 8px;
  background: var(--vscode-input-background, #1e1e1e);
  color: var(--vscode-input-foreground, #d4d4d4);
  border: 1px solid var(--vscode-input-border, #3c3c3c);
  border-radius: 4px;
  font-family: var(--vscode-editor-font-family, ui-monospace, monospace);
  font-size: 12px;
}
.openpe-diff {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
}
.openpe-diff > div {
  background: var(--vscode-textBlockQuote-background, rgba(255,255,255,0.04));
  border: 1px solid var(--vscode-widget-border, #3c3c3c);
  border-radius: 4px;
  padding: 8px;
  font-family: var(--vscode-editor-font-family, ui-monospace, monospace);
  font-size: 12px;
  white-space: pre-wrap;
  word-wrap: break-word;
  max-height: 40vh;
  overflow-y: auto;
}
.openpe-modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 12px;
}
.openpe-action {
  padding: 6px 12px;
  border: 1px solid var(--vscode-button-border, transparent);
  border-radius: 4px;
  background: var(--vscode-button-background, #0e639c);
  color: var(--vscode-button-foreground, #ffffff);
  cursor: pointer;
  font-size: 13px;
}
.openpe-action:hover {
  background: var(--vscode-button-hoverBackground, #1177bb);
}
.openpe-action.secondary {
  background: var(--vscode-button-secondaryBackground, #3a3d41);
  color: var(--vscode-button-secondaryForeground, #d4d4d4);
}
.openpe-action.secondary:hover {
  background: var(--vscode-button-secondaryHoverBackground, #45494e);
}
.openpe-status {
  color: var(--vscode-descriptionForeground, #9d9d9d);
  font-size: 12px;
}
.openpe-error {
  color: var(--vscode-inputValidation-errorForeground, #f48771);
  border: 1px solid var(--vscode-inputValidation-errorBorder, #f48771);
  background: var(--vscode-inputValidation-errorBackground, rgba(244,135,113,0.1));
  padding: 6px 8px;
  border-radius: 4px;
}
`;

export function ensureStyles(): void {
  if (typeof document === "undefined") {
    return;
  }
  if (document.getElementById(STYLE_ID)) {
    return;
  }
  const style = document.createElement("style");
  style.id = STYLE_ID;
  style.textContent = CSS;
  document.head.appendChild(style);
}
