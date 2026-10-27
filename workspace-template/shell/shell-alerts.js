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
//     (the strip itself has no restore line, so it can vanish entirely);
//   - a banner about a cap (D203: out of memory, at the disk quota, a
//     sandbox refused) carries `action`, decorated for this person by
//     xbind: its +25 / +50 / +100 % raises — the ones they may approve and
//     that fit are buttons (each POST /api/xbin/limits, source "raise"), the
//     others greyed with the reason (the title; the open banner lists them).
//     Someone who may approve none reads who to ask.
//
// The shell keeps three fields: `_dismissed` (the stored dismissals, null
// until read), `_alertOpen` (the keys of banners shown in full) and
// `_alertBusy` (the banner whose raise is on its way).
import { html, nothing } from 'lit';
import { alertKey, split, dismiss, KINDS } from '/vendor/bx-dismiss.js';
import { dismissalStore } from '/vendor/bx-requests.js';
import { meta, fmt, choiceLabel, choiceState, primaryChoice, askWho, raiseBody, limitsCall } from '/vendor/limits-kit.js';

const level = (l) => (l === 'crit' ? { icon: 'error', word: 'Critical' } : { icon: 'warning', word: 'Warning' });

// loadDismissals(shell): follow the person's dismissals — the page's one
// store (/vendor/bx-requests.js), read on start and again when another tab
// or device changes them; every change on the page goes through its one
// save chain (#11), the decision prompts' too.
export function loadDismissals(s) {
  return dismissalStore().subscribe((d) => { s._dismissed = d; s.requestUpdate(); });
}

// an alert about a cap (D203): its raise offer, or null
const raiseOf = (a) => (a?.action?.action === 'raise-limit' ? a.action : null);

function dismissAlert(s, a) {
  // a cap's hit is cleared by an approver, as the person (the session, not the shell's frame)
  if (a.dismiss && raiseOf(a)) { fetch('/api/xbin' + a.dismiss, { method: 'POST' }).catch(() => {}).finally(() => s._loadAlerts()); return; }
  if (a.dismiss) { s._loadAlerts(a.dismiss); return; } // the server's own acknowledgement
  const key = alertKey(a);
  dismissalStore().change((d) => dismiss(d, 'alerts', key)); // at once, then stored
}

function toggleOpen(s, key) {
  const open = new Set(s._alertOpen || []);
  if (open.has(key)) open.delete(key); else open.add(key);
  s._alertOpen = open;
  s.requestUpdate();
}

// raise(shell, alert, choice): the person approves one of a cap alert's
// raises (their own session: xbind judges it again); the banner goes once
// the hit is cleared, and a toast says what happened.
async function raise(s, a, c) {
  const act = raiseOf(a), what = act.tile || act.scope;
  s._alertBusy = alertKey(a); s.requestUpdate();
  const r = await limitsCall(undefined, 'POST', '/limits', raiseBody(act, act.kind, c.value, 'raise'));
  s._alertBusy = null;
  s._pushToast('limits', r.ok ? { level: 'ok', message: `${what}: ${meta(act.kind).label} raised to ${fmt(act.kind, c.value)}` }
    : { level: 'error', message: `${what}: ${r.error}` });
  s._loadAlerts();
}

// actionTpl(shell, alert): a cap alert's raise buttons, or who to ask.
function actionTpl(s, a) {
  const act = raiseOf(a);
  if (!act) return nothing;
  const choices = act.choices ?? [];
  if (!choices.some((c) => c.approvable)) {
    return html`<span class="aask" title=${`who can raise it: ${(act.approvers ?? []).join(', ') || 'workspace-admin'}`}>ask ${askWho(act.approvers)}</span>`;
  }
  const prim = primaryChoice(choices), busy = s._alertBusy === alertKey(a), label = meta(act.kind).label;
  return html`<span class="achoices" role="group" aria-label=${`raise ${label}`}>${choices.map((c) => {
    const st = choiceState(c);
    return html`<button type="button" class="achoice ${c === prim ? 'primary' : ''}" data-pct=${c.pct} ?disabled=${!st.live || busy}
      title=${st.live ? `raise ${act.tile || act.scope}'s ${label} to ${fmt(act.kind, c.value)}` : st.why} @click=${() => raise(s, a, c)}>${choiceLabel(c, act.kind)}</button>`;
  })}</span>`;
}

// the greyed raises' reasons, under an open banner
function whysTpl(a) {
  const act = raiseOf(a);
  const grey = (act?.choices ?? []).filter((c) => c.approvable && !choiceState(c).live);
  return grey.length ? html`<div class="awhys">${grey.map((c) => html`<div>${choiceLabel(c, act.kind)}: ${choiceState(c).why}</div>`)}</div>` : nothing;
}

// alertsTpl(shell): the banners still shown.
export function alertsTpl(s) {
  const { shown } = split(s._alerts, s._dismissed, 'alerts', alertKey);
  if (!shown.length) return nothing;
  return html`<div class="alerts">${shown.map((a) => {
    const key = alertKey(a), open = !!s._alertOpen?.has(key), l = level(a.level);
    return html`<div class="alert ${a.level} ${open ? 'open' : ''} ${raiseOf(a) ? 'act' : ''}" role="alert" data-kind=${a.kind ?? ''}>
      <bx-icon class="ico" name=${l.icon}></bx-icon><span class="lvl">${l.word}</span>
      <button type="button" class="msg" aria-expanded=${open ? 'true' : 'false'} title=${open ? 'show one line' : 'show it all'}
        @click=${() => toggleOpen(s, key)}>${a.message}</button>
      ${actionTpl(s, a)}
      <button type="button" class="dismiss" title=${a.dismiss ? 'dismiss it for the workspace' : 'hide it for you, on all your devices — it shows again if it changes'}
        @click=${() => dismissAlert(s, a)}>Dismiss</button>
      ${open ? whysTpl(a) : nothing}
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
    title="show the warnings, grant requests, interfaces to bind and limit prompts you dismissed again"
    @click=${() => dismissalStore().change((d) => Object.fromEntries(Object.entries(d).map(([k, v]) => [k, KINDS.includes(k) ? {} : v])))}>Show dismissed (${n})</button>`;
}
