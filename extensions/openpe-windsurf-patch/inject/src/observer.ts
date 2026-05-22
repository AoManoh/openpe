/**
 * MutationObserver that locates the Cascade chat input toolbar and
 * attaches the openPE logo button to it.
 *
 * The Cascade input toolbar is private Windsurf DOM. We use a small
 * cascade of best-effort selectors and stop at the first match. If
 * Windsurf renames its DOM, widen `TOOLBAR_SELECTORS` and rebuild.
 */

import type { OpenpeConfig } from "./auth.js";
import { buttonAlreadyMounted, createEnhanceButton } from "./button.js";
import { openEnhanceDialog } from "./dialog.js";

// Selectors are ordered most-specific → most-generic. Add new candidates
// at the BEGINNING so a real-host hotfix can win without disturbing the
// existing fallback chain.
const TOOLBAR_SELECTORS: string[] = [
  '[data-testid="cascade-input-toolbar"]',
  '[aria-label="Cascade input toolbar"]',
  '[data-cascade-input-toolbar]',
];

// When the explicit toolbar selectors miss, we fall back to "find any
// element that looks like a Submit button and inject just before it".
const SUBMIT_BUTTON_SELECTORS: string[] = [
  'button[aria-label*="Submit" i]',
  'button[title*="Submit" i]',
  'button[aria-label*="Send" i]',
  'button[title*="Send" i]',
];

interface ObserverState {
  config: OpenpeConfig;
  mounted: boolean;
}

export function startObserver(config: OpenpeConfig): void {
  if (typeof document === "undefined" || typeof MutationObserver === "undefined") {
    return;
  }
  const state: ObserverState = { config, mounted: false };
  // Try immediately in case the toolbar is already in the DOM.
  tryMount(state);
  const observer = new MutationObserver(() => tryMount(state));
  observer.observe(document.documentElement, {
    childList: true,
    subtree: true,
  });
}

function tryMount(state: ObserverState): void {
  if (state.mounted) {
    // Re-check that the button still exists; Cascade may rerender the
    // toolbar (e.g. when the user opens a new chat) and tear it down.
    if (document.querySelector("[data-openpe-injected-button]")) {
      return;
    }
    state.mounted = false;
  }
  const anchor = findInjectionAnchor();
  if (!anchor) {
    return;
  }
  if (buttonAlreadyMounted(anchor.parent)) {
    state.mounted = true;
    return;
  }
  const handle = createEnhanceButton(() => {
    openEnhanceDialog(state.config);
  });
  if (anchor.before) {
    anchor.parent.insertBefore(handle.element, anchor.before);
  } else {
    anchor.parent.appendChild(handle.element);
  }
  state.mounted = true;
}

interface InjectionAnchor {
  parent: Element;
  before: Element | null;
}

function findInjectionAnchor(): InjectionAnchor | null {
  for (const selector of TOOLBAR_SELECTORS) {
    const toolbar = document.querySelector(selector);
    if (toolbar) {
      return { parent: toolbar, before: toolbar.firstElementChild };
    }
  }
  for (const selector of SUBMIT_BUTTON_SELECTORS) {
    const submit = document.querySelector(selector);
    if (submit && submit.parentElement) {
      return { parent: submit.parentElement, before: submit };
    }
  }
  return null;
}
