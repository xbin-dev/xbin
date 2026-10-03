/**
 * shell-account.js — the "my account" block of the shell's settings menu
 * (D38): who you are, the self-service password change, devices… (the xbin
 * app's device list, bx-devices.js) and — once you see a partitioned tile —
 * **your partitions**, a link to xbind's own partitions page
 * (/xbin/partitions; docs/partitions.md §Your partitions page; owner ruling
 * I13). The page opens only top-level (it refuses frames), so the link
 * opens it in a new tab. Extracted from bx-shell, which is at its size
 * budget: bx-shell renders accountMenu(this) inside its settings menu and
 * keeps the menu's state (_who, _components, _settingsOpen, _menuMsg).
 */
import { html, nothing } from 'lit';
import { openDevices } from './bx-devices.js';
import { markShape } from './shell-kit.js';
import { pageEntry, PARTITIONS_PAGE, PAGE_ENTRY, PAGE_ENTRY_TITLE } from './partition-mode.js';

// accountMenu(shell) → the block, for a signed-in person only (nothing for
// the workspace token).
export function accountMenu(shell) {
  if (shell._who?.kind !== 'user') return nothing;
  const w = shell._who;
  return html`
      <div class="hd">my account — ${w.id}${w.name && w.name !== w.id ? ` (${w.name})` : ''} · ${w.role}</div>
      <form class="pw" @submit=${(e) => changePassword(shell, e)}>
        <input name="cur" type="password" placeholder="current password" aria-label="current password" autocomplete="current-password" required>
        <input name="nw" type="password" placeholder="new password (min 8)" aria-label="new password" minlength="8" autocomplete="new-password" required>
        <input name="nw2" type="password" placeholder="repeat new password" aria-label="repeat the new password" minlength="8" autocomplete="new-password" required>
        <label class="rmdev" title="the xbin app on your phones signs in with its own key — a new password alone doesn't sign it out"><input type="checkbox" name="rmdev">and remove my app devices</label>
        <button class="act" type="submit">change password</button>
      </form>
      <button class="act wide" title="the xbin app on your phones and tablets — add one with a QR code, or remove one"
              @click=${() => { shell._settingsOpen = false; openDevices(); }}>devices…</button>
      ${partitionsEntry(shell)}`;
}

// partitionsEntry(shell) → the menu's link to the partitions page, shown
// while the shell's /components listing holds a partitioned tile and the
// viewer is a signed-in person (partition-mode.js pageEntry): it follows
// the listing live, as the tiles' markers do. Styled as the block's buttons,
// with the partitioned marker's shape (shell-css.js .parts).
function partitionsEntry(shell) {
  if (!pageEntry(shell._components, shell._who)) return nothing;
  return html`<a class="act parts" data-partitions href=${PARTITIONS_PAGE} target="_blank" rel="noopener" title=${PAGE_ENTRY_TITLE}
      @click=${() => { shell._settingsOpen = false; }}><span class="pm">${markShape}</span><span>${PAGE_ENTRY}</span><bx-icon class="ext" name="popout"></bx-icon></a>`;
}

async function changePassword(shell, e) {
  e.preventDefault();
  const f = e.target;
  const say = (m) => { shell._menuMsg = m; setTimeout(() => { shell._menuMsg = null; }, 4000); };
  if (f.nw.value !== f.nw2.value) return say({ ok: false, text: "new passwords don't match" });
  try {
    const r = await fetch('/api/xbin/account/password', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ current: f.cur.value, new: f.nw.value, ...(f.rmdev.checked ? { removeDevices: true } : {}) }),
    });
    const d = await r.json().catch(() => ({}));
    say(r.ok ? { ok: true, text: `password changed${d.devicesRemoved ? ` · ${d.devicesRemoved} device(s) removed` : ''}` }
      : { ok: false, text: d.error ?? `failed (${r.status})` });
    if (r.ok) f.reset();
  } catch { say({ ok: false, text: 'offline — try again' }); }
}
