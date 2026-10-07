// native/nav.js — where the person is in the native view (D190): a stack of
// route entries over the conversation list, which is always the root. The
// model (model/app.js) keeps one selection for both views — the open
// conversation (app.sel) or a page (app.page) — so the history is this
// layer's: ui.stack holds every entry, and the model follows the top one.
//
//   ui.stack = [{kind: 'project', pid: 7}, {kind: 'chat', run: 102}, {kind: 'files', run: 102}]
//     → the list ‹ Web ‹ task 2's conversation ‹ its files; app.sel = 102
//
// Entries are plain objects {kind, …} (what a screen loaded lives on its
// entry). The kinds that say where the model is — PLACES — are a
// conversation ({kind: 'chat', run}), the new chat screen ('new'), the
// Automations page ('auto') and Projects ('projects', 'project' and
// 'project-team' {pid}); every other kind is a tool screen pushed over
// whatever place is under it (memory, files, settings, the Coding agents
// board, …: native/tools.js and the seams' screens). With no place on the
// stack the list is the place: the model is home.
//
// How it moves:
//   push(entry)     (native/ui.js) a screen on top — a place among them is
//                   followed by the model at the next paint (follow())
//   open(entry)     a place: back to it when it is already on the stack,
//                   else pushed; a conversation replaces the new chat screen
//                   it was started from
//   the model       app.select() from anywhere (a row, a card, a board, a
//                   started chat) pushes the conversation (wire(): the
//                   'select' event); app.home() on its own (the conversation
//                   was deleted, revoked, could not be read) takes it off
//   pop             the nav's back (native.js) cuts the stack; the model
//                   follows the new top
//   link(address)   a deep link (#c=, #auto, #proj, #join=) builds the stack
//                   afresh: #c=42 is [list, chat 42] (its parents under a
//                   subagent once its view says them)
//   saved()/restore a restarted runtime comes back to the same stack
//                   (xbin.native.saveState: {hash, nav})
//
// How it is drawn is native.js's: on a phone a `nav` of the list and the
// stack's screens. The iPad/Duo split (D190 B3) draws the same stack — the
// list in the primary column, the stack in the detail one — where the app
// has the split of vocabulary rev 2 (rev2('split')); the stack model here
// does not change for it.
import { native } from '/vendor/xb-native.js';
import { ui, ctx } from './ui.js';
import * as router from '../model/router.js';

export const PLACES = new Set(['chat', 'new', 'auto', 'projects', 'project', 'project-team']);
const PROJECT = new Set(['project', 'project-team']);

// rev2: the app renders `name` at vocabulary revision 2 (D189) — the gate
// for what the layout uses of it (the split on wide screens, anchors…). An
// app of rev 1 gets today's phone stack everywhere.
export function rev2(name) {
  try { return !!native.supports?.(name, 2); } catch { return false; }
}

// same: two entries are the same place (a tool screen never is: each push
// is its own).
export function same(a, b) {
  if (!a || !b || a.kind !== b.kind && !(PROJECT.has(a.kind) && PROJECT.has(b.kind))) return false;
  if (a.kind === 'chat') return a.run === b.run;
  if (PROJECT.has(a.kind)) return a.pid === b.pid;
  return a.kind === 'new' || a.kind === 'auto' || a.kind === 'projects';
}

const lastIndex = (pred) => { for (let i = ui.stack.length - 1; i >= 0; i--) if (pred(ui.stack[i], i)) return i; return -1; };
const paint = () => ctx.paint();
// setStack: the whole stack at once (in place: modules hold ui, not the array).
const setStack = (list) => { ui.stack.splice(0, ui.stack.length, ...list); };

// cut leaves the first n entries (what was pushed over them goes).
export function cut(n) {
  if (n < ui.stack.length) ui.stack.length = Math.max(0, n);
  paint();
}

// open: a place — back to it when it is on the stack already, else pushed
// (a conversation started from the new chat screen takes that screen's place).
export function open(entry) {
  const i = lastIndex((x) => same(x, entry));
  if (i >= 0) { cut(i + 1); return ui.stack[i]; }
  const t = ui.stack[ui.stack.length - 1];
  if (t && t.kind === 'new' && entry.kind === 'chat') ui.stack.pop();
  ui.stack.push(entry);
  paint();
  return entry;
}

// place: the entry that says where the model is (null: the list — home).
export function place() {
  const i = lastIndex((x) => PLACES.has(x.kind));
  return i < 0 ? null : ui.stack[i];
}

// chatAt: the open conversation's entry (the topmost conversation on the stack).
export function chatAt() {
  const i = lastIndex((x) => x.kind === 'chat');
  return i < 0 ? null : ui.stack[i];
}

// abovePlace: the entries pushed over the place on top (its tool screens).
export function abovePlace() {
  const i = lastIndex((x) => PLACES.has(x.kind));
  return ui.stack.slice(i + 1);
}

// --- the model follows the stack ---------------------------------------------------------

let driving = 0; // the model's events while this layer drives it are its own echo
function drive(fn) {
  driving++;
  try { return fn(); } finally { driving--; }
}

// follow: the model goes where the top place is — idempotent, run before each
// paint draws (native.js), so every way the stack moved is followed.
export function follow() {
  const app = ctx.app;
  if (!app) return;
  const p = place();
  drive(() => {
    if (!p || p.kind === 'new') {
      if (app.sel != null || app.page != null) app.home();
    } else if (p.kind === 'chat') {
      if (app.sel !== p.run) app.select(p.run).catch(() => {});
    } else if (p.kind === 'auto') {
      if (app.sel != null || app.page !== 'automations') {
        const at = app.autos.open || p.at; // the automation that was open (a restarted runtime: the saved one)
        app.openAutomations(at && at.kind, at && at.id).catch(() => {});
      }
    } else {
      const pid = p.kind === 'projects' ? null : p.pid;
      if (app.sel != null || app.page !== 'projects') app.openProjects(pid).catch(() => {});
      else if (app.projects.opened !== pid) app.projects.open(pid).catch(() => {});
    }
  });
}

// wire: what the model does on its own reaches the stack.
export function wire(app) {
  app.on('select', (id) => {
    if (driving || id == null) return;
    const t = chatAt();
    // a conversation that never opened (it moved to your own space, or went) is replaced
    if (t && place() === t && !t.loaded && t.run !== id) ui.stack.splice(ui.stack.indexOf(t));
    const p = place();
    if (p && p.kind === 'chat' && p.run === id) return;
    open({ kind: 'chat', run: id, up: !ui.stack.length });
  });
  app.on('selected', (id) => {
    const t = chatAt();
    if (!t || t.run !== id) return;
    t.loaded = true;
    if (t.up) parents(t);
  });
  app.on('home', () => {
    if (driving) return;
    // the open conversation went (deleted, revoked, unreadable): it leaves the stack
    const i = lastIndex((x) => x.kind === 'chat');
    if (i >= 0) cut(i);
  });
  app.on('page', () => {
    if (driving) return;
    if (app.page === 'automations' && place()?.kind !== 'auto') open({ kind: 'auto' });
    if (app.page === 'projects' && !PROJECT.has(place()?.kind) && place()?.kind !== 'projects') {
      open({ kind: 'projects' });
      if (app.projects.opened != null) open(projectEntry(app.projects.opened));
    }
  });
}

// parents: a conversation opened from the list or a link has its parents
// (a subagent's chain) under it, so back goes up — unless one of them is on
// the stack already (it was opened from there: back returns there).
function parents(t) {
  t.up = false;
  const v = ctx.app.session.current();
  const chain = (v && v.chain) || [];
  if (!chain.length || ui.stack.some((x) => x.kind === 'chat' && chain.some((c) => c.id === x.run))) return;
  ui.stack.splice(ui.stack.indexOf(t), 0, ...chain.map((c) => ({ kind: 'chat', run: c.id, title: c.title, loaded: true })));
  paint();
}

// --- the ways in ---------------------------------------------------------------------------

export const newChat = () => open({ kind: 'new' });
export const openChat = (run) => open({ kind: 'chat', run: +run });

// openAutos: the Automations page, one automation's with kind/id.
export function openAutos(kind, id) {
  open({ kind: 'auto' });
  drive(() => ctx.app.openAutomations(kind, id).catch(() => {}));
}

// projectEntry: a project's screen — a team project's is the team board.
export function projectEntry(pid) {
  const pv = ctx.app.projects.find(pid) || {};
  return { kind: pv.kind === 'team' ? 'project-team' : 'project', pid: +pid };
}

// openProjects: the Projects list, or one project — over where you are (a
// task's conversation leads to its project: back returns to the task), or
// back to it when the list or the project is on the stack already.
export function openProjects(pid) {
  if (pid == null) open({ kind: 'projects' });
  else open(projectEntry(pid));
}

// link: an address (a deep link's fragment, a hash) builds the stack afresh.
export function link(address) {
  const app = ctx.app;
  const to = router.parse(address);
  if (to.join) { setStack([]); paint(); return app.join(to.join); }
  if (to.conv != null) setStack([{ kind: 'chat', run: to.conv, up: true }]);
  else if (to.auto) setStack([{ kind: 'auto', at: to.auto.kind ? { kind: to.auto.kind, id: +to.auto.id } : null }]);
  else if (to.proj) setStack([{ kind: 'projects' }, ...(to.proj.id != null ? [projectEntry(to.proj.id)] : [])]);
  else setStack([]);
  if (to.auto) drive(() => app.openAutomations(to.auto.kind, to.auto.id).catch(() => {}));
  else follow();
  paint();
  return Promise.resolve();
}

// --- saved across a restarted runtime -------------------------------------------------------

// What of an entry is saved (a screen's loaded data never is): the ids its
// kind needs. A kind not here ends the saved stack — a form or a seam's
// screen comes back as the screen it was pushed from.
const SAVE = {
  chat: ['run'], new: [], projects: [], project: ['pid'], 'project-team': ['pid'],
  task: ['run'], memory: ['run'], files: ['run'], file: ['run', 'path'], skills: [], tree: ['root'],
  settings: [], hboard: ['root'], sandboxes: [],
};

export function saved() {
  const out = [];
  for (const s of ui.stack) {
    if (s.kind === 'auto') { const o = ctx.app.autos.open; out.push({ kind: 'auto', ...(o ? { at: { kind: o.kind, id: o.id } } : {}) }); continue; }
    const keys = SAVE[s.kind];
    if (!keys) break;
    const e = { kind: s.kind };
    for (const k of keys) if (s[k] != null) e[k] = s[k];
    out.push(e);
  }
  return out;
}

// restore: the saved stack (nothing when it is not one).
export function restore(list) {
  if (!Array.isArray(list)) return false;
  setStack(list.filter((e) => e && typeof e === 'object' && (SAVE[e.kind] || e.kind === 'auto'))
    .map((e) => ({ ...e, ...(e.kind === 'chat' ? { loaded: true } : {}) })));
  const p = place();
  if (p && p.kind === 'auto') drive(() => ctx.app.openAutomations(p.at && p.at.kind, p.at && p.at.id).catch(() => {}));
  else follow();
  paint();
  return true;
}
