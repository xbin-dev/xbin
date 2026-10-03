/**
 * shell-appearance.js — the person's appearance in the shell (D184): the
 * settings menu's Theme (System · Light · Dark) and Density (Compact ·
 * Comfortable) rows, the pick, and following another tab's or device's.
 * A pick restyles this page and every frame in it at once (setAppearance in
 * /vendor/bx-theme.js: bx-frame relays it to its iframe), then is saved in
 * the shell's prefs bucket — PUT the key, or DELETE it for the default —
 * with the X-Prefs-Writer header, so this tab skips its own `prefs` event
 * while the person's other tabs and devices follow it (followAppearance).
 * Viewing the workspace as someone shows their choice and changes nothing.
 * Extracted from bx-shell, which is at its size budget: bx-shell renders
 * appearanceRows(this) in its settings menu, calls followAppearance(this, e)
 * on a `prefs` event, and keeps the state (_look, _who, _writer).
 */
import { html } from 'lit';
import { appearance, setAppearance, THEMES, DENSITIES } from '/vendor/bx-theme.js';
import { foreignWrite } from './layout-sync.js';

// The appearance keys of the shell's prefs bucket: absent = the default.
const APPEARANCE = [['theme', 'system', THEMES], ['density', 'compact', DENSITIES]];
const xfetch = (...a) => (window.xbin?.fetch ?? fetch)(...a);

// pickAppearance(shell, key, value): the person picked one in the menu.
export function pickAppearance(shell, key, value) {
  const def = APPEARANCE.find(([k]) => k === key);
  if (shell._who?.readOnly || !def || !def[2].includes(value) || appearance()[key] === value) return;
  setAppearance({ [key]: value });
  shell._look = appearance();
  const url = `/api/xbin/prefs/${key}`, headers = { 'X-Prefs-Writer': shell._writer };
  (value === def[1]
    ? xfetch(url, { method: 'DELETE', headers })
    : xfetch(url, { method: 'PUT', headers: { ...headers, 'Content-Type': 'application/json' }, body: JSON.stringify(value) }))
    .catch(() => { /* offline: this page has it; the next pick saves */ });
}

// followAppearance(shell, e): another client changed the theme or density
// (a `prefs` event): read the key, follow it.
export async function followAppearance(shell, e) {
  for (const [key, def, values] of APPEARANCE) {
    if (!foreignWrite(e, key, shell._writer)) continue;
    let v = def;
    try {
      const r = await xfetch(`/api/xbin/prefs/${key}`);
      if (r.ok) { const j = await r.json(); if (values.includes(j)) v = j; } else if (r.status !== 404) continue;
    } catch { continue; /* offline: the next event, or a reload, has it */ }
    setAppearance({ [key]: v });
    shell._look = appearance();
  }
}

// appearanceRows(shell): the settings menu's Theme and Density, square
// segmented controls, the pressed one the person's choice (aria-pressed).
export function appearanceRows(shell) {
  const look = shell._look ?? appearance(), ro = !!shell._who?.readOnly;
  const seg = (key, label, opts) => html`<div class="row"><span id=${`ap-${key}`}>${label}</span>
    <span class="seg" role="group" aria-labelledby=${`ap-${key}`}>${opts.map(([v, word, tip]) => html`<button
      aria-pressed=${look[key] === v ? 'true' : 'false'} ?disabled=${ro} data-appearance=${`${key}:${v}`}
      title=${ro ? 'read-only while you view the workspace as someone: this is their choice' : tip}
      @click=${() => pickAppearance(shell, key, v)}>${word}</button>`)}</span></div>`;
  return html`
    ${seg('theme', 'Theme', [['system', 'System', "follow this device's light or dark setting"],
      ['light', 'Light', 'Concrete Day, whatever the device says'], ['dark', 'Dark', 'Concrete Night, whatever the device says']])}
    ${seg('density', 'Density', [['compact', 'Compact', '28 px rows, 13 px text'], ['comfortable', 'Comfortable', '32 px rows, 14 px text']])}`;
}
