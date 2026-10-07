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
 * The page restyles only when it follows the person (the workspace's root
 * page carries data-bx-theme="auto"): a root page from before D184 — the
 * shell updated alone (`bx builtin update scaffold:shell`), or a root of the
 * workspace's own — doesn't, so the rows show the choice, disabled, and say
 * what brings the root page up to date; the shell never opts that page in
 * itself (its own token overrides would mix with Concrete Day).
 * The row under them, "This device" (Follow my setting · Light · Dark), is
 * this browser's own override of the theme (D188; /vendor/bx-theme.js):
 * kept in localStorage, never synced, so a dark laptop can sit beside a
 * light office screen. While it is set the page and its tiles show it, the
 * Theme row still shows (and saves) the person's choice, and another tab of
 * this browser follows a change of it (the `storage` event). A root page
 * from before D188 (no /vendor/theme-boot.js) gets the override here, when
 * the shell loads, instead of at its first paint.
 * Extracted from bx-shell, which is at its size budget: bx-shell renders
 * appearanceRows(this) in its settings menu, calls followAppearance(this, e)
 * on a `prefs` event, and keeps the state (_look, _who, _writer).
 */
import { html, nothing } from 'lit';
import { follows, personAppearance, setPersonAppearance, deviceTheme, setDeviceTheme, syncDeviceTheme, DEVICE_KEY, THEMES, DENSITIES } from '/vendor/bx-theme.js';
import { foreignWrite } from './layout-sync.js';

// This browser's override, applied as the shell loads (a root page without
// theme-boot.js) and followed from this browser's other tabs.
syncDeviceTheme();
addEventListener('storage', (e) => { if (e.key === DEVICE_KEY || e.key === null) syncDeviceTheme(); });

// The appearance keys of the shell's prefs bucket: absent = the default.
const APPEARANCE = [['theme', 'system', THEMES], ['density', 'compact', DENSITIES]];
const xfetch = (...a) => (window.xbin?.fetch ?? fetch)(...a);

// pickAppearance(shell, key, value): the person picked one in the menu.
export function pickAppearance(shell, key, value) {
  const def = APPEARANCE.find(([k]) => k === key);
  if (shell._who?.readOnly || !follows() || !def || !def[2].includes(value) || personAppearance()[key] === value) return;
  setPersonAppearance({ [key]: value });
  shell._look = personAppearance();
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
    setPersonAppearance({ [key]: v });
    shell._look = personAppearance();
  }
}

// pickDevice(shell, v): this browser's override picked ('' follows the
// person again): stored here only, the page and its tiles restyled.
export function pickDevice(shell, v) {
  if (!follows() || deviceTheme() === v) return;
  setDeviceTheme(v);
  shell._look = personAppearance();
}

// The words for a root page that doesn't follow the person (an admin
// updates it: docs/changelog.md, D184).
const OLD_ROOT = "this workspace's root page is older than its shell: an admin brings it up to date with bx builtin update scaffold:root";

// appearanceRows(shell): the settings menu's Theme and Density, square
// segmented controls, the pressed one the person's choice (aria-pressed),
// and This device, the pressed one this browser's override.
export function appearanceRows(shell) {
  const look = { ...personAppearance(), device: deviceTheme() }, ro = !!shell._who?.readOnly, old = !follows();
  const seg = (key, label, opts, mine = false) => html`<div class="row"><span id=${`ap-${key}`}>${label}</span>
    <span class="seg" role="group" aria-labelledby=${`ap-${key}`}>${opts.map(([v, word, tip]) => html`<button
      aria-pressed=${look[key] === v ? 'true' : 'false'} ?disabled=${(ro && !mine) || old}
      data-appearance=${mine ? nothing : `${key}:${v}`} data-device-theme=${mine ? v || 'person' : nothing}
      title=${ro && !mine ? 'read-only while you view the workspace as someone: this is their choice' : old ? OLD_ROOT : tip}
      @click=${() => (mine ? pickDevice(shell, v) : pickAppearance(shell, key, v))}>${word}</button>`)}</span></div>`;
  return html`
    ${seg('theme', 'Theme', [['system', 'System', "follow this device's light or dark setting"],
      ['light', 'Light', 'Concrete Day, whatever the device says'], ['dark', 'Dark', 'Concrete Night, whatever the device says']])}
    ${seg('device', 'This device', [['', 'Follow my setting', 'this browser shows your Theme, as your other devices do'],
      ['light', 'Light', 'this browser shows Concrete Day, whatever your Theme says; your other devices are unchanged'],
      ['dark', 'Dark', 'this browser shows Concrete Night, whatever your Theme says; your other devices are unchanged']], true)}
    ${seg('density', 'Density', [['compact', 'Compact', '28 px rows, 13 px text'], ['comfortable', 'Comfortable', '32 px rows, 14 px text']])}
    ${old ? html`<div class="gshint" data-appearance-old>Theme and density need this workspace's root page from this xbind — an admin runs
      <code>bx builtin update scaffold:root</code></div>` : nothing}`;
}
