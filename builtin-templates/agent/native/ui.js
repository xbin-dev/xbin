// native/ui.js — what the native view keeps of its own (the model keeps the
// rest, model/app.js): the composer's text per place, which sheets are open, the
// navigation stack over the conversation list (conversations, pages and the
// tools pushed over them: native/nav.js), and the last error — plus the small
// helpers every native screen uses. No lit, no DOM: native.js draws with
// /vendor/xb-native.js, and node tests run it against a stubbed backend.
import { homeOf } from '../model/homes.js';
import { ext } from './ext.js';
import { ACP_NATIVE_ICON } from '../model/harness-heads.js';

// The state that is the view's, not the model's.
export const ui = {
  drafts: new Map(), // the composer's text per place (draftKey: a conversation's run id, 'new' for the new chat
                     // screen) — a controlled prop the app reports each keystroke of; going elsewhere keeps it
  get draft() { return draftOf(); },       // the open place's (draftKey)
  set draft(text) { setDraft(text); },
  q: '',            // the conversation list's search field
  newChat: null,    // the "new chat with options" sheet: {text, title, system, class}
  rename: null,     // the rename sheet: {id, title}
  share: null,      // the share sheet: {run: {id, title}, data, link, err, user, role, linkRole, linkExp}
  sbxAsk: null,     // the Sandbox picker's confirmation sheet: {ref, name, text} (native/sandboxes.js)
  cols: 'auto',     // the split's columns on a wide screen (native.js layout): both, or the detail alone
  stack: [],        // the navigation stack over the conversation list: route entries {kind, …} (native/nav.js)
  win: null,        // the open conversation's window of blocks (native/chat.js): {run, fromKey, atBottom, start, n}
  err: '',          // the last failure, said at the top of the screen on top
  note: '',         // what just went right (say(): a copy made, files copied), said like it
};

// draftKey: the place a composer writes to — the open conversation, else
// the new chat screen ('new'; the iPad's empty detail column is it too).
export const draftKey = () => (ctx.app && ctx.app.sel != null ? ctx.app.sel : 'new');
export const draftOf = (key = draftKey()) => ui.drafts.get(key) || '';
export function setDraft(text, key = draftKey()) {
  if (text) ui.drafts.set(key, String(text));
  else ui.drafts.delete(key);
}
// clearer: what a send calls once it went — the draft of the place it was
// written in, wherever the person is by then (a new chat opens its conversation).
export const clearer = (key = draftKey()) => () => setDraft('', key);

// The model and the repaint, set once by native.js; the seams feature
// modules draw through (native/ext.js).
export const ctx = { app: null, paint: () => {}, ext };

// fail says why something did not work (the web's alert(); a notice here).
let errT = null;
export function fail(e) {
  ui.err = String((e && e.message) || e || 'something went wrong');
  clearTimeout(errT);
  errT = setTimeout(() => { ui.err = ''; ctx.paint(); }, 8000);
  ctx.paint();
}

// say tells what just went right (a notice on the open conversation and the list).
let noteT = null;
export function say(text) {
  ui.note = String(text || '');
  clearTimeout(noteT);
  noteT = setTimeout(() => { ui.note = ''; ctx.paint(); }, 8000);
  ctx.paint();
}

// guard runs an action, says why it failed, and repaints either way.
export const guard = (fn) => async (...args) => {
  try { await fn(...args); } catch (e) { fail(e); return; }
  ctx.paint();
};

// nextFrame(fn): once, at the next display frame — or within 50 ms where
// frames stall (a hidden runtime document), like xb-native's own flushes.
export function nextFrame(fn) {
  let done = false;
  const run = () => { if (!done) { done = true; fn(); } };
  if (typeof requestAnimationFrame === 'function') requestAnimationFrame(run);
  setTimeout(run, 50);
}

// push/pop a screen over whatever is showing.
export function push(screen) { ui.stack.push(screen); ctx.paint(); }
export function top() { return ui.stack[ui.stack.length - 1] || null; }

// --- formatting ---------------------------------------------------------------

export const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');
export const clip = (s, n) => { s = String(s ?? ''); return s.length > n ? s.slice(0, n - 1) + '…' : s; };
export const secs = (ms) => Math.max(1, Math.round((Number(ms) || 0) / 1000));
export const base = (p) => String(p || '').split('/').pop();
export const when = (ms) => (ms ? new Date(ms).toLocaleString([], { dateStyle: 'short', timeStyle: 'short' }) : '');

// The icon of a tool family (model/tool-heads.js) — the web draws glyphs.
export const FAMILY_ICON = {
  net: 'network', web: 'globe', file: 'file', code: 'code', mem: 'database', note: 'pencil', skill: 'star',
  time: 'clock', agent: 'branch', done: 'check', ask: 'question', mcp: 'gear', box: 'terminal', other: 'wrench',
  ...ACP_NATIVE_ICON, // a coding harness's families (model/harness-heads.js)
};

// A tool card's state (model/fold.js) as the chat family says it, and the
// word the web shows beside it.
export function cardState(st) {
  switch (st) {
    case 'writing': return { state: 'writing', chip: null };
    case 'running': return { state: 'running', chip: null };
    case 'waiting': return { state: 'running', chip: { text: 'waiting' } };
    case 'approval': return { state: 'running', chip: { text: 'needs approval', tone: 'warn' } };
    case 'error': return { state: 'error', chip: { text: 'failed', tone: 'danger' } };
    case 'stopped': return { state: 'canceled', chip: { text: 'stopped' } };
    default: return { state: 'ok', chip: null };
  }
}

// thumb: a sized image of a run's session file (GET /runs/{id}/thumb), which
// the app loads itself with the tile's frame token; raw: the file's bytes. A
// shared conversation in a person's partition is the global instance's
// (model/homes.js): its URLs ask xbind for it, as model/app.js uploadTarget.
const at = (runId) => (homeOf(runId) === 'global' ? '&xbin-partition=global' : '');
export const thumb = (runId, path, w = 480) => `${ctx.app.base}/runs/${runId}/thumb?path=${encodeURIComponent(path)}&w=${w}${at(runId)}`;
export const raw = (runId, path) => `${ctx.app.base}/runs/${runId}/raw?path=${encodeURIComponent(path)}${at(runId)}`;
export const IMAGE = /^image\/(png|jpeg|gif|webp)$/;
