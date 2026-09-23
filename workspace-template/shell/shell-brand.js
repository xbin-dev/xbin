/**
 * shell-brand.js — the workspace's title and icon (D76), as the shell shows
 * them. An admin sets both from the admin tile (GET/PUT /api/xbin/branding):
 * the title replaces the word "workspace" in the header and the tab title,
 * the icon replaces xbin's mark as the favicon and the header logo. Nothing
 * set = xbin's own (the mark, X/BIN, the "workspace" chip). Extracted from
 * bx-shell, which is at its size budget.
 */
import { html } from 'lit';

const DEFAULT_ICON = '/vendor/favicon.svg';

// loadBrand: the current brand, read as the human (a raw fetch — xbin.fetch
// would downgrade to the chrome element); xbin's own on any failure.
export async function loadBrand() {
  try {
    const r = await fetch('/api/xbin/branding');
    if (r.ok) { const b = await r.json(); return { title: b.title || '', icon: b.icon || '' }; }
  } catch { /* offline: the defaults */ }
  return { title: '', icon: '' };
}

// applyFavicon: the page's icon link follows the brand. The default is what
// root/index.html links — that file is the workspace's own and is never
// rewritten, so the override happens here, at runtime.
export function applyFavicon(brand) {
  let link = document.querySelector('link[rel="icon"]');
  if (!link) { link = document.createElement('link'); link.rel = 'icon'; document.head.appendChild(link); }
  const want = brand?.icon || DEFAULT_ICON;
  if (link.getAttribute('href') === want) return;
  link.setAttribute('href', want);
  if (brand?.icon) link.removeAttribute('type'); else link.setAttribute('type', 'image/svg+xml');
}

// brandLogo: the header's logo block. Branded: the icon (or xbin's mark)
// and the title, replacing the wordmark and the chip. Unbranded: xbin's
// mark, X/BIN and the "workspace" chip — what the header always showed.
export function brandLogo(shell) {
  const b = shell._brand || {};
  const mark = b.icon
    ? html`<img class="mark" src=${b.icon} alt="" width="20" height="20">`
    : html`<svg class="mark" viewBox="0 0 64 64" width="20" height="20" aria-hidden="true">
        <path d="M18 4H56a4 4 0 0 1 4 4v38L46 60H8a4 4 0 0 1-4-4V18z" fill="var(--bx-accent,#f5a623)"></path>
        <path d="M21 21 43 43M43 21 21 43" stroke="#23272e" stroke-width="9" stroke-linecap="butt"></path>
        <circle cx="53" cy="11" r="2.6" fill="#23272e" opacity=".4"></circle>
        <circle cx="11" cy="53" r="2.6" fill="#23272e" opacity=".4"></circle>
      </svg>`;
  if (b.title) return html`<span class="logo">${mark}<span class="ws-title">${b.title}</span></span>`;
  return html`<span class="logo">${mark}X/BIN</span><span class="ws-chip">${shell.name}</span>`;
}
