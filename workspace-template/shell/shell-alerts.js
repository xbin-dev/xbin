// shell-alerts.js — the workspace's warning banners (/api/xbin/alerts) and
// the person's dismissals (D188's store, /vendor/bx-dismiss.js):
//
//   - a banner is one line in the page's flow (never over the top bar or
//     the tabs); its message opens to its full text on a click;
//   - every banner has Dismiss: the server's own route when the alert
//     carries one (a workspace-wide acknowledgement), else the person's
//     dismissal — hidden for them on all their devices, shown again when
//     its words change (alertKey), still listed in the admin console;
//   - the settings menu's "Show dismissed (N)" brings back everything the
//     person dismissed: banners, grant requests and interfaces to bind
//     (the strip itself has no restore line, so it can vanish entirely).
//
// The shell keeps two fields: `_dismissed` (the stored dismissals, null
// until read) and `_alertOpen` (the keys of banners shown in full).
import { html, nothing } from 'lit';
import { alertKey, split, dismiss, KINDS, loadDismissed, updateDismissed, dismissedEvent } from '/vendor/bx-dismiss.js';

const fetchFn = (...a) => window.xbin.fetch(...a);

const level = (l) => (l === 'crit' ? { icon: 'error', word: 'Critical' } : { icon: 'warning', word: 'Warning' });

// loadDismissals(shell): read the person's dismissals (on start, and when a
// `prefs` event says another tab or device changed them).
export async function loadDismissals(s) {
  const d = await loadDismissed(fetchFn);
  if (d) { s._dismissed = d; s.requestUpdate(); }
}

// followDismissals(shell, event): the shell's event hook.
export function followDismissals(s, e) {
  if (dismissedEvent(e)) loadDismissals(s);
}

async function dismissAlert(s, a) {
  if (a.dismiss) { s._loadAlerts(a.dismiss); return; } // the server's own acknowledgement
  const key = alertKey(a);
  s._dismissed = dismiss(s._dismissed || {}, 'alerts', key); // at once, then stored
  s.requestUpdate();
  const saved = await updateDismissed(fetchFn, (d) => dismiss(d, 'alerts', key));
  if (saved) { s._dismissed = saved; s.requestUpdate(); }
}

function toggleOpen(s, key) {
  const open = new Set(s._alertOpen || []);
  if (open.has(key)) open.delete(key); else open.add(key);
  s._alertOpen = open;
  s.requestUpdate();
}

// alertsTpl(shell): the banners still shown.
export function alertsTpl(s) {
  const { shown } = split(s._alerts, s._dismissed, 'alerts', alertKey);
  if (!shown.length) return nothing;
  return html`<div class="alerts">${shown.map((a) => {
    const key = alertKey(a), open = !!s._alertOpen?.has(key), l = level(a.level);
    return html`<div class="alert ${a.level} ${open ? 'open' : ''}" role="alert">
      <bx-icon class="ico" name=${l.icon}></bx-icon><span class="lvl">${l.word}</span>
      <button type="button" class="msg" aria-expanded=${open ? 'true' : 'false'} title=${open ? 'show one line' : 'show it all'}
        @click=${() => toggleOpen(s, key)}>${a.message}</button>
      <button type="button" class="dismiss" title=${a.dismiss ? 'dismiss it for the workspace' : 'hide it for you, on all your devices — it shows again if it changes'}
        @click=${() => dismissAlert(s, a)}>Dismiss</button>
    </div>`;
  })}</div>`;
}

// dismissedCount(shell): what the person has dismissed, every kind.
export function dismissedCount(s) {
  const d = s._dismissed;
  return d ? KINDS.reduce((n, k) => n + Object.keys(d[k] || {}).length, 0) : 0;
}

// dismissedRow(shell): the settings menu's way back — nothing when there is
// nothing dismissed.
export function dismissedRow(s) {
  const n = dismissedCount(s);
  if (!n) return nothing;
  return html`<button type="button" class="restore-all" data-restore-all
    title="show the warnings, grant requests and interfaces to bind you dismissed again"
    @click=${async () => {
      const saved = await updateDismissed(fetchFn, (d) => Object.fromEntries(Object.entries(d).map(([k, v]) => [k, KINDS.includes(k) ? {} : v])));
      if (saved) { s._dismissed = saved; s.requestUpdate(); }
    }}>Show dismissed (${n})</button>`;
}
