// native/ui.js — what the native view keeps of its own (the model keeps the
// rest, model/app.js): the composer's text, which sheets are open, the
// screens pushed over the conversation (memory, files, skills, the workflow
// tree, settings, the render preview), and the last error — plus the small
// helpers every native screen uses. No lit, no DOM: native.js draws with
// /vendor/xb-native.js, and node tests run it against a stubbed backend.

// The state that is the view's, not the model's.
export const ui = {
  draft: '',        // the composer's text (a controlled prop: the app reports each keystroke)
  drawer: false,    // the conversations drawer is open
  q: '',            // the drawer's search field
  newChat: null,    // the "new chat with options" sheet: {text, title, system, toolset}
  rename: null,     // the rename sheet: {id, title}
  share: null,      // the share sheet: {run: {id, title}, data, link, err, user, role, linkRole, linkExp}
  stack: [],        // screens pushed over the conversation or home: {kind, …} (native/tools.js)
  opening: null,    // a subagent being opened full screen (its view is loading)
  err: '',          // the last failure, said at the top of the screen on top
};

// The model and the repaint, set once by native.js.
export const ctx = { app: null, paint: () => {} };

// fail says why something did not work (the web's alert(); a notice here).
let errT = null;
export function fail(e) {
  ui.err = String((e && e.message) || e || 'something went wrong');
  clearTimeout(errT);
  errT = setTimeout(() => { ui.err = ''; ctx.paint(); }, 8000);
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
  time: 'clock', agent: 'branch', done: 'check', ask: 'question', mcp: 'gear', other: 'wrench',
};

// A tool card's state (model/fold.js) as the chat family says it, and the
// word the web shows beside it.
export function cardState(st) {
  switch (st) {
    case 'writing': return { state: 'writing', chip: null };
    case 'running': return { state: 'running', chip: null };
    case 'waiting': return { state: 'running', chip: { text: 'waiting', tone: 'muted' } };
    case 'approval': return { state: 'running', chip: { text: 'needs approval', tone: 'warn' } };
    case 'error': return { state: 'error', chip: { text: 'failed', tone: 'danger' } };
    case 'stopped': return { state: 'canceled', chip: { text: 'stopped', tone: 'muted' } };
    default: return { state: 'ok', chip: null };
  }
}

// thumb: a sized image of a run's session file (GET /runs/{id}/thumb), which
// the app loads itself with the tile's frame token; raw: the file's bytes.
export const thumb = (runId, path, w = 480) => `${ctx.app.base}/runs/${runId}/thumb?path=${encodeURIComponent(path)}&w=${w}`;
export const raw = (runId, path) => `${ctx.app.base}/runs/${runId}/raw?path=${encodeURIComponent(path)}`;
export const IMAGE = /^image\/(png|jpeg|gif|webp)$/;
