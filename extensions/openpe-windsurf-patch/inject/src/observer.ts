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
import { runAutoEnhance } from "./dialog.js";

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
  // The click handler does NOT close over a particular editor at mount
  // time. Instead, at click time we walk up from the openPE button to the
  // nearest Lexical / textarea editor, so the right Cascade input is
  // picked even when multiple chat panels are open and re-rendered.
  // eslint-disable-next-line prefer-const
  let handle: ReturnType<typeof createEnhanceButton>;
  handle = createEnhanceButton(() => {
    const target = findEditorForButton(handle.element);
    if (!target) {
      // Toast lives in dialog.ts; surface a synchronous warning by
      // re-using runAutoEnhance's empty-input path via a dummy editor
      // would be wrong, so just no-op here. The button stays visible and
      // a follow-up click after the DOM settles will succeed.
      return;
    }
    void runAutoEnhance(state.config, target);
  });
  if (anchor.before) {
    anchor.parent.insertBefore(handle.element, anchor.before);
  } else {
    anchor.parent.appendChild(handle.element);
  }
  state.mounted = true;
}

// Walk up ancestors from the openPE button until we find a Lexical chat
// editor (preferred) or a plain textarea (fallback for older Windsurf
// builds). Returns the editor element itself so dialog.ts can read +
// write its text content directly.
function findEditorForButton(start: Element, maxDepth = 16): HTMLElement | null {
  let p: Element | null = start.parentElement;
  for (let depth = 0; p && depth < maxDepth; depth++) {
    const lexical = p.querySelector<HTMLElement>('[data-lexical-editor="true"]');
    if (lexical) return lexical;
    p = p.parentElement;
  }
  p = start.parentElement;
  for (let depth = 0; p && depth < maxDepth; depth++) {
    const ta = p.querySelector<HTMLTextAreaElement>("textarea");
    if (ta) return ta;
    p = p.parentElement;
  }
  return null;
}

interface InjectionAnchor {
  parent: Element;
  before: Element | null;
}

// Windsurf 1.110.x ships a bare <button type="submit"> for Cascade's send
// button with no aria-label, no title, and no test id. The only stable way
// to disambiguate it from arbitrary <form>-style submits elsewhere in the
// app is to require a Lexical chat editor in an ancestor chain.
function findNearbyLexicalEditor(btn: Element, maxDepth = 8): Element | null {
  let p: Element | null = btn.parentElement;
  for (let depth = 0; p && depth < maxDepth; depth++) {
    if (p.querySelector('[data-lexical-editor="true"], textarea')) {
      return p;
    }
    p = p.parentElement;
  }
  return null;
}

function findCascadeSubmitButton(): HTMLButtonElement | null {
  const candidates = document.querySelectorAll<HTMLButtonElement>(
    'button[type="submit"]',
  );
  for (const btn of candidates) {
    if (btn.hasAttribute("data-openpe-injected-button")) continue;
    if (findNearbyLexicalEditor(btn)) return btn;
  }
  return null;
}

function findInjectionAnchor(): InjectionAnchor | null {
  for (const selector of TOOLBAR_SELECTORS) {
    const toolbar = document.querySelector(selector);
    if (toolbar) {
      return { parent: toolbar, before: toolbar.firstElementChild };
    }
  }
  // Windsurf 1.110.x: insert before the submit button's wrapper so the
  // openPE button lands next to the mic / model / file-picker icons
  // instead of right against the arrow. submitBtn.parentElement is the
  // inner pl-1 wrapper; its parentElement is the actual toolbar row.
  const submitBtn = findCascadeSubmitButton();
  if (submitBtn) {
    const wrapper = submitBtn.parentElement;
    const toolbar = wrapper?.parentElement ?? null;
    if (wrapper && toolbar) {
      return { parent: toolbar, before: wrapper };
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
