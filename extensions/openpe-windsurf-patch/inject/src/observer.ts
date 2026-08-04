/**
 * MutationObserver that locates the Cascade chat input toolbar and
 * attaches the openPE logo button to it.
 *
 * The Cascade input toolbar is private Windsurf DOM. We use a small
 * cascade of best-effort selectors and stop at the first match. If
 * Windsurf renames its DOM, widen `TOOLBAR_SELECTORS` and rebuild.
 */

import { selectRightmostByGroup } from "./anchor_selection.js";
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

interface ObserverState {
  config: OpenpeConfig;
}

export function startObserver(config: OpenpeConfig): void {
  if (typeof document === "undefined" || typeof MutationObserver === "undefined") {
    return;
  }
  const state: ObserverState = { config };
  // Try immediately in case the toolbar is already in the DOM.
  tryMount(state);
  const observer = new MutationObserver(() => tryMount(state));
  observer.observe(document.documentElement, {
    childList: true,
    subtree: true,
  });
}

function tryMount(state: ObserverState): void {
  for (const anchor of findAllInjectionAnchors()) {
    if (buttonAlreadyMounted(anchor.parent)) {
      continue;
    }
    mountButton(state, anchor);
  }
}

function mountButton(state: ObserverState, anchor: InjectionAnchor): void {
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
  if (anchor.before && anchor.before.parentElement === anchor.parent) {
    anchor.parent.insertBefore(handle.element, anchor.before);
  } else {
    anchor.parent.appendChild(handle.element);
  }
}

// Walk up ancestors from the openPE button until we find a Lexical chat
// editor (preferred) or a plain textarea (fallback for older Windsurf
// builds). Returns the editor element itself so dialog.ts can read +
// write its text content directly.
function findEditorForButton(start: Element, maxDepth = 16): HTMLElement | null {
  return findUniqueNearbyEditor(start, maxDepth);
}

function findUniqueNearbyEditor(start: Element, maxDepth: number): HTMLElement | null {
  let parent: Element | null = start.parentElement;
  for (let depth = 0; parent && depth < maxDepth; depth++) {
    const editors = parent.querySelectorAll<HTMLElement>(
      '[data-lexical-editor="true"], textarea, [contenteditable="true"]',
    );
    if (editors.length === 1) return editors[0] ?? null;
    if (editors.length > 1) return null;
    parent = parent.parentElement;
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
  return findUniqueNearbyEditor(btn, maxDepth);
}

function findAllCascadeSubmitButtons(): HTMLButtonElement[] {
  const matches: HTMLButtonElement[] = [];
  const candidates = document.querySelectorAll<HTMLButtonElement>(
    'button[type="submit"]',
  );
  for (const btn of candidates) {
    if (btn.hasAttribute("data-openpe-injected-button")) continue;
    if (findNearbyLexicalEditor(btn)) matches.push(btn);
  }
  const devinCandidates = document.querySelectorAll<HTMLButtonElement>(
    'button[type="button"].bg-bg-accent-neutral',
  );
  const rankedCandidates: Array<{
    group: HTMLElement;
    value: HTMLButtonElement;
    right: number;
  }> = [];
  for (const btn of devinCandidates) {
    if (btn.hasAttribute("data-openpe-injected-button")) continue;
    const editor = findUniqueNearbyEditor(btn, 12);
    if (!editor) continue;
    const rect = btn.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) continue;
    rankedCandidates.push({ group: editor, value: btn, right: rect.right });
  }
  matches.push(...selectRightmostByGroup(rankedCandidates));
  return matches;
}

function findAllInjectionAnchors(): InjectionAnchor[] {
  const anchors: InjectionAnchor[] = [];
  const seen = new Set<Element>();
  const push = (parent: Element, before: Element | null): void => {
    if (seen.has(parent)) return;
    seen.add(parent);
    anchors.push({ parent, before });
  };
  for (const selector of TOOLBAR_SELECTORS) {
    for (const toolbar of document.querySelectorAll(selector)) {
      if (findUniqueNearbyEditor(toolbar, 12)) {
        push(toolbar, toolbar.firstElementChild);
      }
    }
  }
  // Windsurf 1.110.x: insert before the submit button's wrapper so the
  // openPE button lands next to the mic / model / file-picker icons
  // instead of right against the arrow. submitBtn.parentElement is the
  // inner pl-1 wrapper; its parentElement is the actual toolbar row.
  for (const submitBtn of findAllCascadeSubmitButtons()) {
    const wrapper = submitBtn.parentElement;
    const toolbar = wrapper?.parentElement ?? null;
    if (wrapper && toolbar) {
      push(toolbar, wrapper);
    }
  }
  return anchors;
}
