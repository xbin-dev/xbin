// native/ui.js — what the native view keeps of its own (the model keeps the
// rest, model/app.js): the root screen's tab, the screens pushed over it,
// the open sheets, drafts and the last error — plus the helpers every native
// screen uses. No lit, no DOM: native.js draws with /vendor/xb-native.js,
// and node tests run it against the fake backend (test/native-stub.mjs).

export const ui = {
  tab: '',          // the root screen's tab: ops | images | settings | mine
  stack: [],        // screens pushed over the root: {kind, …}
  sheet: null,      // the open sheet: {kind: 'create' | 'mkdir', …}
  forms: {},        // drafts by key (the fields are controlled props)
  err: '',          // the last failure, said at the top of the screen on top
  msg: '',          // the last thing done
  busy: '',         // what is being done (its button spins)
};

// The model and the repaint, set once by native.js.
export const ctx = { app: null, paint: () => {} };

let errT = null;
// fail says why something did not work (a notice on the screen on top).
export function fail(e) {
  ui.err = String((e && e.message) || e || 'something went wrong');
  ui.msg = '';
  clearTimeout(errT);
  errT = setTimeout(() => { ui.err = ''; ctx.paint(); }, 8000);
  ctx.paint();
}

// act does one thing a control asked for: busy while it runs, the refusal
// (or msg) said after.
export async function act(key, fn, msg = '') {
  ui.busy = key; ui.err = ''; ui.msg = '';
  ctx.paint();
  try { const r = await fn(); ui.msg = typeof r === 'string' ? r : msg; } catch (e) { fail(e); }
  ui.busy = '';
  ctx.paint();
}

// push a screen over whatever is showing; top is the one on top.
export function push(screen) { ui.stack.push(screen); ui.err = ''; ui.msg = ''; ctx.paint(); }
export const top = () => ui.stack[ui.stack.length - 1] || null;
// back leaves the screen on top (after a delete, say).
export function back() { ui.stack.pop(); ctx.paint(); }

// set(key, k) is a controlled field's @input: the draft keeps what is typed.
export const set = (key, k) => (e) => { ui.forms[key] = { ...(ui.forms[key] || {}), [k]: e.value }; ctx.paint(); };
export const form = (key, init) => ui.forms[key] || (ui.forms[key] = { ...init });

// nextFrame(fn): once, at the next display frame — or within 50 ms where
// frames stall (a hidden runtime document).
export function nextFrame(fn) {
  let done = false;
  const run = () => { if (!done) { done = true; fn(); } };
  if (typeof requestAnimationFrame === 'function') requestAnimationFrame(run);
  setTimeout(run, 50);
}
