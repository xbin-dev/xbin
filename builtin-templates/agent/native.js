// native.js — the agent tile's native view: what the xbin app draws with
// platform UI instead of the web page (API.md "The frontend"). It
// renders the SAME model the web view drives (model/: the Session and its
// blocks, the conversation list, the Automations page, rules, actions, the
// router), as a navigation stack over the conversation list (D190):
//
//   the conversation list — the root: search, Needs you, pinned, by date;
//   │   New chat and the main menu (Automations, Projects, Coding agents,
//   │   Settings) in its bar
//   └─ pushed: a conversation (a subagent's over its parent: back goes up),
//      the new chat screen, the Automations screens, Projects (a project's
//      board, a task's conversation over it), and tools over any of them:
//      memory, files (+ editor), skills, the workflow tree, settings, one
//      tool call in full, the render preview, the coding sandboxes, the
//      Coding agents board…
//   + the sheets: new chat with options, rename, share, a person's
//     partition forms (native/homes.js)
//
// On an iPad or a Duo (an app with the rev-2 split) the list and the stack
// sit side by side (layout()); a phone, and any app of rev 1, has the one stack.
//
// The stack is this view's own (native/nav.js: route entries in ui.stack,
// saved for a restarted runtime); the model keeps one selection, as for the
// web, and follows the top entry. Back returns where you came from.
//
// The web's features and this view's are held level by model/features.js
// (native-features.js says what this view implements; the parity test is
// hack/agent-template-features.test.mjs). A UX change lands in the model and
// in both views in the same change.
//
// Streaming uses GET /stream?deltas=1 and the open conversation is read in
// pages (API.md); images come as sized thumbnails the app loads itself.
import { html, render, repeat, native } from '/vendor/xb-native.js';
import { createApp } from './model/app.js';
import { RELAY } from './model/sandboxes.js';
import { ui, ctx, fail, nextFrame } from './native/ui.js';
import { chatRouteScreen, chatDrawn } from './native/chat.js';
import { newScreen } from './native/home.js';
import { listScreen, newChatSheet, renameSheet } from './native/convs.js';
import { shareSheet } from './native/share.js';
import { sandboxAskSheet } from './native/sandboxes.js';
import { hostedWarnSheet } from './native/hosted.js';
import { partitionSheets } from './native/homes.js';
import { toolScreen, treeDirty, openRender, openLive } from './native/tools.js';
import { autoScreens } from './native/auto.js';
import * as nav from './native/nav.js';
import './native/harness-all.js'; // the coding harnesses' modules (their hooks on ctx.ext, native/ext.js)
import './native/project-all.js'; // Projects' modules (native/project-all.js)

const visible = () => (globalThis.document?.visibilityState ?? 'visible') === 'visible';

// route keeps the address of where you are: in the runtime document's hash
// when it may change it, and in the app's saved state with the stack (a
// restarted runtime comes back to the same screens: native/nav.js saved()).
let hash = '';
let savedKey = '';
function route(h) {
  hash = h;
  try { globalThis.history?.replaceState?.(null, '', h ? '#' + h : globalThis.location.pathname + globalThis.location.search); } catch { /* sandboxed */ }
  save();
}
function save() {
  const st = { hash, nav: nav.saved() };
  const key = JSON.stringify(st);
  if (key === savedKey) return;
  savedKey = key;
  try { native.saveState(st); } catch { /* no app */ }
}

const app = createApp({ deltas: true, page: 50, route, visible, frame: nextFrame });
ctx.app = app;
app.sbx.tty = RELAY; // terminals through the tile's own relay (native/terminal.js: the app's terminal dials only the tile's routes)

// --- painting ------------------------------------------------------------------

let queued = false;
function paint() {
  if (queued) return;
  queued = true;
  // the model follows the stack first (native/nav.js), then the screens are drawn
  nextFrame(() => { queued = false; nav.follow(); draw(); });
}
ctx.paint = paint;

let screens = [];
let meta = '';
function draw() {
  syncPreview();
  // the list, then each entry of the stack as its screen(s)
  const list = { key: 'list', tpl: listScreen };
  const stack = ui.stack.flatMap(screensOf);
  screens = [list, ...stack];
  render(html`${layout(list, stack)}
    ${newChatSheet()}${renameSheet()}${shareSheet()}${sandboxAskSheet()}${hostedWarnSheet()}${partitionSheets()}`);
  // the tile's title and badge in the app's navigator and switcher
  const v = app.session.current();
  const m = { title: v ? app.rules.topBar(v).title : app.HOME.title, badge: app.needs.length ? String(app.needs.length) : null };
  const key = JSON.stringify(m);
  if (key !== meta) { meta = key; try { native.meta(m); } catch { /* no app */ } }
  save();
  chatDrawn();
}

// layout: how the screens are drawn. Where the app has the collapsing split
// of vocabulary rev 2 (D189; nav.rev2('split')), the list is its primary
// column and the stack a nav in its detail one (D190 B3): side by side on an
// iPad or a Duo — the list beside the open conversation and what is pushed
// over it, the new chat screen in the detail column while nothing is open
// (the web's home pane) — and on a phone the split collapses to the list
// with the stack pushed over it, as before. From an app of rev 1, anywhere:
// one nav of the list, then the stack.
function layout(list, stack) {
  if (!nav.rev2('split')) return html`<nav @pop=${pop}>${repeat([list, ...stack], (s) => s.key, (s) => s.tpl())}</nav>`;
  const detail = stack.length ? stack : [{ key: 'home', tpl: newScreen }];
  return html`<split detail=${stack.length > 0} columns=${ui.cols} @close=${() => pop({ depth: 1 })}
      @columns=${(e) => { ui.cols = e.value; paint(); }}>
    ${list.tpl()}
    <nav @pop=${(e) => pop({ depth: (Number(e.depth) || 1) + 1 })}>${repeat(detail, (s) => s.key, (s) => s.tpl())}</nav>
  </split>`;
}

// screensOf: a stack entry's screen — the Automations page is up to three
// (the page, one automation, a form: the model's AutoPage holds which).
// leave: the entry goes off the stack, with what was pushed over it.
let seq = 0;
function screensOf(s) {
  if (!s.id) s.id = ++seq;
  const leave = () => { const i = ui.stack.indexOf(s); if (i >= 0) nav.cut(i); };
  switch (s.kind) {
    case 'chat': return [{ key: `chat:${s.id}`, entry: s, tpl: () => chatRouteScreen(s), leave }];
    case 'new': return [{ key: `new:${s.id}`, entry: s, tpl: newScreen, leave }];
    case 'auto': return autoScreens().map((x, i) => (i ? x : { ...x, entry: s, leave }));
  }
  return [{ key: `tool:${s.id}`, entry: s, tpl: () => toolScreen(s), leave }];
}

// pop: the person went back to `depth` screens; the screens above leave
// (their entries go off the stack; a dismissed render preview stays closed)
// and the new top one says where it is (an automation's form closes). The
// model follows the stack before the next draw (nav.follow).
function pop(e) {
  const list = screens;
  const depth = Math.max(1, Math.min(list.length, Number(e.depth) || 1));
  for (const s of list.slice(depth).reverse()) {
    if (s.entry && s.entry.kind === 'render') prevDismissed = prevSeen;
    s.leave?.();
  }
  list[depth - 1].back?.();
  paint();
}

app.on('*', () => paint());
app.on('error', (e) => fail(e));
// What the model does on its own reaches the stack: a conversation opened
// anywhere is pushed, one deleted or revoked leaves it (native/nav.js).
nav.wire(app);
app.on('home', () => { prevSeen = null; prevDismissed = 0; });
// An open workflow tree follows the runs it shows.
app.on('change', () => { for (const s of ui.stack) if (s.kind === 'tree' && s.tree) treeDirty(s); });

// --- the render preview follows the run's newest render ------------------------------

let prevSeen = null;     // the newest render step seen in this run
let prevDismissed = 0;   // the render step the person closed
let prevRun = null;
function syncPreview() {
  const v = app.session.current();
  if (!v) return;
  if (prevRun !== v.run.id) { prevRun = v.run.id; prevSeen = null; }
  const rs = (v.steps || []).filter((s) => s.kind === 'render' || s.kind === 'live');
  const last = rs[rs.length - 1];
  if (!last) { prevSeen = null; return; }
  let det = last.detail;
  try { if (typeof det === 'string') det = JSON.parse(det); } catch { return; }
  const at = last.seq ?? last.id;
  const first = prevSeen === null;
  // landing on an old finished run does not pop it open; one that arrives while you look does
  const fresh = first ? Date.now() / 1000 - last.created < 60 : at > prevSeen;
  prevSeen = at;
  if (!fresh || prevDismissed === at || !visible()) return;
  const p = nav.place();
  if (!p || p.kind !== 'chat' || p.run !== v.run.id) return;      // the conversation is not on top
  const over = nav.abovePlace();
  const r = over.find((x) => x.kind === 'render' || x.kind === 'live');
  if (over.length && over[over.length - 1] !== r) return;         // don't yank an open screen away
  if (r && !r.live) return;                                       // the person pinned an older one
  if (last.kind === 'live') return openLive(v.run.id, det, true); // preview_port (D135)
  openRender(v.run.id, det.path, det.version, true);
}

// --- start --------------------------------------------------------------------------------

paint();
app.start();
// Where to start: the address the app opened (#c=<id>, #auto[=kind:id],
// #proj[=<id>], #join=<token> — model/router.js) builds the stack; else the
// stack the runtime had last; else its address (saved before the stack was).
const st = native.state && typeof native.state === 'object' ? native.state : {};
const opened = globalThis.location?.hash || '';
if (opened.length > 1) nav.link(opened).catch(fail);
else if (!nav.restore(st.nav)) nav.link(st.hash ? '#' + st.hash : '').catch(fail);
// a deep link while the runtime runs (the app's xbn.navigate fires hashchange)
globalThis.addEventListener?.('hashchange', () => { nav.link(globalThis.location.hash).catch(fail); });

export { app };
