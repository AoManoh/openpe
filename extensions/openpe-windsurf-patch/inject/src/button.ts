/**
 * The openPE logo button element factory.
 *
 * Kept in its own module so the observer (which is the file most likely
 * to need real-host tweaks) stays focused on DOM discovery.
 */

const BUTTON_MARKER_ATTR = "data-openpe-injected-button";
const OPENPE_LOGO_SVG = `
<svg class="openpe-btn-icon" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 128 128" aria-hidden="true" focusable="false">
  <defs>
    <linearGradient id="openpe-btn-bg" x1="18" y1="12" x2="110" y2="116" gradientUnits="userSpaceOnUse">
      <stop offset="0" stop-color="#12343b"/>
      <stop offset="1" stop-color="#061317"/>
    </linearGradient>
    <linearGradient id="openpe-btn-card" x1="28" y1="24" x2="100" y2="104" gradientUnits="userSpaceOnUse">
      <stop offset="0" stop-color="#ecfff8"/>
      <stop offset="1" stop-color="#b8f1e1"/>
    </linearGradient>
    <linearGradient id="openpe-btn-accent" x1="42" y1="38" x2="96" y2="92" gradientUnits="userSpaceOnUse">
      <stop offset="0" stop-color="#2dd4bf"/>
      <stop offset="1" stop-color="#f59e0b"/>
    </linearGradient>
    <filter id="openpe-btn-shadow" x="-20%" y="-20%" width="140%" height="140%" color-interpolation-filters="sRGB">
      <feDropShadow dx="0" dy="10" stdDeviation="8" flood-color="#000000" flood-opacity="0.35"/>
    </filter>
  </defs>
  <rect width="128" height="128" rx="28" fill="url(#openpe-btn-bg)"/>
  <path d="M24 89.5V43c0-8.8 7.2-16 16-16h48.5c8.8 0 16 7.2 16 16v34.5c0 8.8-7.2 16-16 16H53.6L36.1 109c-4.6 4.1-12.1.8-12.1-5.4V89.5Z" fill="#0b2228" filter="url(#openpe-btn-shadow)"/>
  <path d="M32 84.9V43.8C32 38.4 36.4 34 41.8 34h45.9c5.4 0 9.8 4.4 9.8 9.8v29.9c0 5.4-4.4 9.8-9.8 9.8H56.4L38.8 98.9c-2.6 2.3-6.8.4-6.8-3V84.9Z" fill="url(#openpe-btn-card)"/>
  <path d="M45 51.5h24.8" stroke="#0f2d33" stroke-width="7" stroke-linecap="round"/>
  <path d="M45 66.5h16.8" stroke="#0f2d33" stroke-width="7" stroke-linecap="round"/>
  <path d="M74.5 63.5 84 73l-9.5 9.5" fill="none" stroke="#0f2d33" stroke-width="7" stroke-linecap="round" stroke-linejoin="round"/>
  <path d="M58.5 82.5h14" stroke="#0f2d33" stroke-width="7" stroke-linecap="round"/>
  <path d="M94.5 22.5 98.8 34l11.7 4.5-11.7 4.4-4.3 11.6-4.4-11.6-11.6-4.4L90.1 34l4.4-11.5Z" fill="url(#openpe-btn-accent)"/>
  <path d="M105.5 72.5 108 79l6.5 2.5L108 84l-2.5 6.5L103 84l-6.5-2.5L103 79l2.5-6.5Z" fill="#fbbf24"/>
</svg>`;

export interface ButtonHandle {
  element: HTMLButtonElement;
  dispose(): void;
}

// Pre-encoded once at module load so each button creation is allocation-light.
// btoa() is safe here because OPENPE_LOGO_SVG is ASCII-only.
const OPENPE_LOGO_DATA_URI =
  "data:image/svg+xml;base64," +
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ((globalThis as any).btoa
    ? // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (globalThis as any).btoa(OPENPE_LOGO_SVG.replace(/\s+/g, " "))
    : "");

export function createEnhanceButton(onClick: () => void): ButtonHandle {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "openpe-btn";
  btn.title = "openPE: enhance prompt";
  btn.setAttribute("aria-label", "openPE: enhance prompt");
  btn.setAttribute(BUTTON_MARKER_ATTR, "1");
  // Windsurf 1.110.x enables Trusted Types CSP, which rejects plain-string
  // `innerHTML` assignment AND treats `DOMParser.parseFromString` (any mime)
  // as a sink in Chromium 95+. Setting `img.src` to a data URI is NOT a sink
  // — the browser's image loader parses image/svg+xml safely from URLs.
  // If the host's CSP `img-src` forbids `data:`, the img.onerror fallback
  // renders a plain "PE" text label so the button stays usable.
  if (OPENPE_LOGO_DATA_URI.length > "data:image/svg+xml;base64,".length) {
    const img = document.createElement("img");
    img.className = "openpe-btn-icon";
    img.alt = "openPE";
    img.setAttribute("aria-hidden", "true");
    img.addEventListener(
      "error",
      () => {
        img.remove();
        if (!btn.textContent) btn.textContent = "PE";
      },
      { once: true },
    );
    img.src = OPENPE_LOGO_DATA_URI;
    btn.appendChild(img);
  } else {
    btn.textContent = "PE";
  }
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
