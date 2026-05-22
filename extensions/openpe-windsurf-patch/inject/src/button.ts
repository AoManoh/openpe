/**
 * The ✨ button element factory.
 *
 * Kept in its own module so the observer (which is the file most likely
 * to need real-host tweaks) stays focused on DOM discovery.
 */

const BUTTON_MARKER_ATTR = "data-openpe-injected-button";

export interface ButtonHandle {
  element: HTMLButtonElement;
  dispose(): void;
}

export function createEnhanceButton(onClick: () => void): ButtonHandle {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "openpe-btn";
  btn.title = "openPE: enhance prompt";
  btn.setAttribute("aria-label", "openPE: enhance prompt");
  btn.setAttribute(BUTTON_MARKER_ATTR, "1");
  btn.textContent = "✨";
  btn.addEventListener("click", (event) => {
    event.preventDefault();
    event.stopPropagation();
    onClick();
  });
  return {
    element: btn,
    dispose: () => {
      btn.remove();
    },
  };
}

export function buttonAlreadyMounted(parent: Element): boolean {
  return parent.querySelector(`[${BUTTON_MARKER_ATTR}]`) !== null;
}
