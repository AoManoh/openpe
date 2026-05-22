/**
 * 3-page modal: config → loading → diff.
 *
 * Plain vanilla DOM, no framework. Keeps the bundled payload small and
 * removes any chance of React-instance leakage into the host IDE.
 */

import type { OpenpeConfig } from "./auth.js";
import { ClientError, enhancePrompt } from "./client.js";

const OVERLAY_ID = "openpe-dialog-overlay";

interface DialogState {
  config: OpenpeConfig;
  overlay: HTMLElement;
  abort: AbortController | null;
}

export function openEnhanceDialog(config: OpenpeConfig): void {
  closeExistingDialog();
  const overlay = buildOverlay();
  const state: DialogState = { config, overlay, abort: null };
  renderConfigPage(state, suggestPrompt());
  document.body.appendChild(overlay);
}

function suggestPrompt(): string {
  const activeInput = findActiveCascadeInput();
  if (!activeInput) {
    return "";
  }
  return activeInput.value.trim();
}

function findActiveCascadeInput(): HTMLTextAreaElement | null {
  // Best-effort: the focused element is usually the Cascade textarea when
  // the user clicks the ✨ button without leaving the chat input.
  const active = document.activeElement;
  if (active instanceof HTMLTextAreaElement) {
    return active;
  }
  return document.querySelector<HTMLTextAreaElement>("textarea");
}

function closeExistingDialog(): void {
  const existing = document.getElementById(OVERLAY_ID);
  if (existing) {
    existing.remove();
  }
}

function buildOverlay(): HTMLElement {
  const overlay = document.createElement("div");
  overlay.id = OVERLAY_ID;
  overlay.className = "openpe-overlay";
  overlay.addEventListener("click", (event) => {
    if (event.target === overlay) {
      closeExistingDialog();
    }
  });
  return overlay;
}

function buildModal(title: string): {
  modal: HTMLElement;
  body: HTMLElement;
  footer: HTMLElement;
} {
  const modal = document.createElement("div");
  modal.className = "openpe-modal";
  modal.addEventListener("click", (event) => event.stopPropagation());

  const header = document.createElement("div");
  header.className = "openpe-modal-header";
  const heading = document.createElement("div");
  heading.className = "openpe-modal-title";
  heading.textContent = title;
  const close = document.createElement("button");
  close.className = "openpe-modal-close";
  close.type = "button";
  close.setAttribute("aria-label", "Close");
  close.textContent = "✕";
  close.addEventListener("click", () => closeExistingDialog());
  header.appendChild(heading);
  header.appendChild(close);

  const body = document.createElement("div");
  body.className = "openpe-modal-body";

  const footer = document.createElement("div");
  footer.className = "openpe-modal-footer";

  modal.appendChild(header);
  modal.appendChild(body);
  modal.appendChild(footer);
  return { modal, body, footer };
}

function renderConfigPage(state: DialogState, initial: string): void {
  state.overlay.innerHTML = "";
  const { modal, body, footer } = buildModal("openPE — Enhance prompt");

  const note = document.createElement("div");
  note.className = "openpe-status";
  note.textContent =
    "Edit the prompt then click Enhance. The result is shown side-by-side; apply when ready.";

  const textarea = document.createElement("textarea");
  textarea.className = "openpe-input";
  textarea.placeholder = "Describe what you want…";
  textarea.value = initial;
  textarea.spellcheck = false;

  body.appendChild(note);
  body.appendChild(textarea);

  const cancel = document.createElement("button");
  cancel.className = "openpe-action secondary";
  cancel.type = "button";
  cancel.textContent = "Cancel";
  cancel.addEventListener("click", () => closeExistingDialog());

  const enhance = document.createElement("button");
  enhance.className = "openpe-action";
  enhance.type = "button";
  enhance.textContent = "Enhance";
  enhance.addEventListener("click", () => {
    const value = textarea.value.trim();
    if (!value) {
      textarea.focus();
      return;
    }
    void renderLoadingPage(state, value);
  });

  footer.appendChild(cancel);
  footer.appendChild(enhance);

  state.overlay.appendChild(modal);
  setTimeout(() => textarea.focus(), 0);
}

async function renderLoadingPage(state: DialogState, prompt: string): Promise<void> {
  state.overlay.innerHTML = "";
  const { modal, body, footer } = buildModal("openPE — Enhancing…");
  const status = document.createElement("div");
  status.className = "openpe-status";
  status.textContent = "Talking to openpe-server on " + state.config.baseUrl;
  body.appendChild(status);

  const cancel = document.createElement("button");
  cancel.className = "openpe-action secondary";
  cancel.type = "button";
  cancel.textContent = "Cancel";
  cancel.addEventListener("click", () => {
    state.abort?.abort();
    closeExistingDialog();
  });
  footer.appendChild(cancel);

  state.overlay.appendChild(modal);

  state.abort = new AbortController();
  try {
    const resp = await enhancePrompt(
      state.config,
      { prompt },
      state.abort.signal,
    );
    renderDiffPage(state, prompt, resp.enhanced_prompt ?? "");
  } catch (err) {
    renderErrorPage(state, prompt, err);
  } finally {
    state.abort = null;
  }
}

function renderDiffPage(state: DialogState, original: string, enhanced: string): void {
  state.overlay.innerHTML = "";
  const { modal, body, footer } = buildModal("openPE — Review");

  const diff = document.createElement("div");
  diff.className = "openpe-diff";
  const beforeBox = document.createElement("div");
  beforeBox.textContent = original;
  const afterBox = document.createElement("div");
  afterBox.textContent = enhanced;
  diff.appendChild(beforeBox);
  diff.appendChild(afterBox);
  body.appendChild(diff);

  const back = document.createElement("button");
  back.className = "openpe-action secondary";
  back.type = "button";
  back.textContent = "Back";
  back.addEventListener("click", () => renderConfigPage(state, original));

  const apply = document.createElement("button");
  apply.className = "openpe-action";
  apply.type = "button";
  apply.textContent = "Apply to input";
  apply.addEventListener("click", () => {
    const ok = writeIntoCascadeInput(enhanced);
    if (ok) {
      closeExistingDialog();
    } else {
      const err = document.createElement("div");
      err.className = "openpe-error";
      err.textContent =
        "Could not locate the Cascade input field; copy the text manually.";
      body.appendChild(err);
    }
  });

  footer.appendChild(back);
  footer.appendChild(apply);

  state.overlay.appendChild(modal);
}

function renderErrorPage(state: DialogState, original: string, err: unknown): void {
  state.overlay.innerHTML = "";
  const { modal, body, footer } = buildModal("openPE — Error");

  const message = document.createElement("div");
  message.className = "openpe-error";
  if (err instanceof ClientError) {
    message.textContent = err.message + (err.status ? ` (status ${err.status})` : "");
  } else if (err instanceof DOMException && err.name === "AbortError") {
    message.textContent = "Cancelled by user.";
  } else if (err instanceof Error) {
    message.textContent = err.message || "Unknown error";
  } else {
    message.textContent = "Unknown error";
  }
  body.appendChild(message);

  const back = document.createElement("button");
  back.className = "openpe-action";
  back.type = "button";
  back.textContent = "Back";
  back.addEventListener("click", () => renderConfigPage(state, original));
  footer.appendChild(back);

  state.overlay.appendChild(modal);
}

function writeIntoCascadeInput(text: string): boolean {
  const input = findActiveCascadeInput();
  if (!input) {
    return false;
  }
  // Use the native setter so React (or any framework Windsurf may use)
  // notices the change and updates its internal state.
  const setter = Object.getOwnPropertyDescriptor(
    HTMLTextAreaElement.prototype,
    "value",
  )?.set;
  if (setter) {
    setter.call(input, text);
  } else {
    input.value = text;
  }
  input.dispatchEvent(new Event("input", { bubbles: true }));
  input.dispatchEvent(new Event("change", { bubbles: true }));
  input.focus();
  return true;
}
