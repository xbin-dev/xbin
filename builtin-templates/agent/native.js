// native.js — the agent tile's native view: what the xbin app draws with
// platform UI instead of the web page (API.md "The frontend"). It
// renders the SAME model the web view drives (model/: the Session and its
// blocks, the conversation list, the Automations page, rules, actions, the
// router), one surface at a time:
//
//   home ─ or ─ a conversation (a subagent's parents under it: back goes up)
//     ├─ the Automations screens (the page, one automation, a form)
//     └─ pushed tools: memory, files (+ editor), skills, the workflow tree,
//        settings, one tool call in full, the render preview
//   + the conversations drawer (a sheet from the leading edge), and the
//     sheets: new chat with options, rename, share
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
import { ui, ctx, fail, nextFrame } from './native/ui.js';
import { chatScreens } from './native/chat.js';
import { homeScreen } from './native/home.js';
import { drawerSheet, newChatSheet, renameSheet } from './native/convs.js';
import { shareSheet } from './native/share.js';
import { toolScreens, treeDirty, openRender } from './native/tools.js';
import { autoScreens } from './native/auto.js';

const visible = () => (globalThis.document?.visibilityState ?? 'visible') === 'visible';

// route keeps the address of where you are: in the runtime document's hash
// when it may change it, and in the app's saved state (a restarted runtime
// comes back to the same conversation).
function route(h) {
  try { globalThis.history?.replaceState?.(null, '', h ? '#' + h : globalThis.location.pathname + globalThis.location.search); } catch { /* sandboxed */ }
  try { native.saveState({ hash: h }); } catch { /* no app */ }
}

const app = createApp({ deltas: true, page: 50, route, visible, frame: nextFrame });
ctx.app = app;

// --- painting ------------------------------------------------------------------

let queued = false;
function paint() {
  if (queued) return;
  queued = true;
  nextFrame(() => { queued = false; draw(); });
}
ctx.paint = paint;

let screens = [];
let meta = '';
function draw() {
  syncPreview();
  screens = [
    ...(app.sel == null ? [{ key: 'home', tpl: homeScreen, back: () => { if (app.page) app.home(); } }] : chatScreens()),
    ...(app.sel == null && app.page === 'automations' ? autoScreens() : []),
    ...toolScreens(),
  ];
  render(html`<nav @pop=${pop}>${repeat(screens, (s) => s.key, (s) => s.tpl())}</nav>
    ${drawerSheet()}${newChatSheet()}${renameSheet()}${shareSheet()}`);
  // the tile's title and badge in the app's navigator and switcher
  const v = app.session.current();
  const m = { title: v ? app.rules.topBar(v).title : app.HOME.title, badge: app.needs.length ? String(app.needs.length) : null };
  const key = JSON.stringify(m);
  if (key !== meta) { meta = key; try { native.meta(m); } catch { /* no app */ } }
}

// pop: the person went back to `depth` screens; the screens above leave
// (a pushed tool closes, a dismissed render preview stays closed) and the
// new top one says where the model is (a subagent's parent is opened).
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
// Opening a conversation (or going home) closes what was pushed over the last one.
app.on('select', () => { ui.stack.length = 0; });
app.on('home', () => { ui.stack.length = 0; ui.opening = null; prevSeen = null; prevDismissed = 0; });
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
  const rs = (v.steps || []).filter((s) => s.kind === 'render');
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
  const r = ui.stack.find((x) => x.kind === 'render');
  if (ui.stack.length && ui.stack[ui.stack.length - 1] !== r) return; // don't yank an open screen away
  if (r && !r.live) return;                                          // the person pinned an older one
  openRender(v.run.id, det.path, det.version, true);
}

// --- start --------------------------------------------------------------------------------

paint();
app.start();
// Where to start: the address the app opened (#c=<id>, #auto[=kind:id],
// #join=<token> — model/router.js), else where the runtime was last.
const saved = native.state && typeof native.state === 'object' && native.state.hash ? '#' + native.state.hash : '';
app.follow(globalThis.location?.hash || saved).catch(fail);
globalThis.addEventListener?.('hashchange', () => { app.follow(globalThis.location.hash).catch(fail); });

export { app };
