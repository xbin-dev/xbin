// native/ws.js — a person's workspace agent in the xbin app (API.md
// §Workspace mode → the native view; D233): what native.js loads in place
// of the template's own view when the backend says it is a workspace agent.
// It draws the SAME model the web page does (ws/store.js: the sessions, the
// scope, the cards, the notebook, the banners, what the composer takes; the
// run's rows through model/session.js and model/fold.js) with the app's
// platform controls:
//
//   the session in view — its name in the bar, Sessions and ⋯ beside it
//   ├─ the connection line (what it is connected to, the model): the scope panel
//   ├─ what stops it: paused, a used-up quota, turned off, xbin updating, no connection
//   ├─ the conversation, the agent's cards set where it made them (native/ws-chat.js)
//   └─ the composer: the context chip, quick replies, the hint
//   pushed: the notebook, one tool call in full
//   sheets: Sessions, the scope panel, "What can it do?" (native/ws-panels.js)
//
// It grants nothing. A request, a tile agent's start and an admin change
// set are the app's own to draw and to decide (the `request` node; the
// app's sheets); this view asks the app's chrome by id — xbin.native.agent,
// with the messages of ws/dock.js, the web page's own — and takes from it
// what it was opened about (the context chip). Pausing shows; Resume is the
// app's, where the composer was.
//
// Addresses (the fragment the app opens the view with, or navigates to):
//   #s=<session id>     that session in view
//   #about=<tile path>  the composer's context chip (as xbin:agent-context {tile})
//   #screen=<name>      …about a screen
//   #scope · #models    the scope panel, or its models
//   #sessions · #notebook · #can
import { html, render, repeat, nothing, native } from '/vendor/xb-native.js';
import { createStore } from '../ws/store.js';
import * as dock from '../ws/dock.js';
import { W, chipWords, short } from '../ws/words.js';
import { ui, ctx, fail, nextFrame } from './ui.js';
import { chatDrawn } from './win.js';
import * as chat from './ws-chat.js';
import * as panels from './ws-panels.js';

const { has } = chat;

// chrome(msg): ask the app's own chrome (an approval or a change set by id, a tile agent's session, one of its
// panels). An app that doesn't show this tile as an agent has none: say so.
async function chrome(msg) {
  if (!msg) return false;
  if (!has('agent')) { fail(W.needsApp); return false; }
  try { return !!(await agentApi().agent(msg)); } catch (e) { fail(e); return false; }
}
// xbin.native as the runtime merged it into the page's xbin (the whole API), else the template layer's own export
const agentApi = () => { const n = globalThis.xbin?.native; return n && typeof n.agent === 'function' ? n : native; };

// --- painting ------------------------------------------------------------------

let queued = false;
function paint() {
  if (queued) return;
  queued = true;
  nextFrame(() => { queued = false; draw(); });
}

const store = createStore({ paint, post: chrome, frame: nextFrame, hooks: { reset: () => { ui.win = null; ui.stack = []; } } });
// what the shared rows read of their host (native/blocks.js, native/win.js)
ctx.app = { session: store.session, get sel() { return store.S.sel; } };
ctx.paint = paint;
chat.bind(store);
panels.bind(store);

let screens = [];
let meta = '';
function draw() {
  screens = [{ key: 'main', tpl: mainScreen }, ...panels.notebookScreens(), ...panels.callScreens()];
  render(html`<nav @pop=${pop}>${repeat(screens, (s) => s.key, (s) => s.tpl())}</nav>
    ${panels.sessionsSheet()}${panels.scopeSheet()}${panels.whatCanSheet()}`);
  // the tile's name and how many things wait for the person, for the app's own button
  const n = store.needs();
  const m = JSON.stringify({ title: W.title, badge: n ? String(n) : null });
  if (m !== meta) { meta = m; try { native.meta(JSON.parse(m)); } catch { /* no app */ } }
  chatDrawn();
}

// pop: the person went back to `depth` screens; the ones above leave, top first
function pop(e) {
  const depth = Math.max(1, Math.min(screens.length, Number(e.depth) || 1));
  for (const s of screens.slice(depth).reverse()) s.back?.();
  paint();
}

// --- the session in view --------------------------------------------------------

const TONE = { bad: 'danger', warn: 'warn', info: 'info', ok: 'ok' };

function mainScreen() {
  const S = store.S;
  const s = store.current();
  const name = s ? s.name || (s.main ? 'Main' : 'New session') : W.title;
  const { tpl, pill } = chat.transcriptTpl(); // first: it places the cards the composer's replies come from
  const composer = chat.composerTpl(pill);
  const ask = (view) => () => chrome(dock.openView(view));
  const open = (name2) => () => { S.pop = name2; paint(); };
  return html`<screen title=${name} style="scroll">
    <toolbar>
      ${s && !has('connection') ? html`<badge>${connWords(s).summary}</badge>` : nothing}
      <button icon="list" @tap=${open('sessions')}>Sessions</button>
      <menu icon="ellipsis" label="More">
        <button icon="list" @tap=${open('sessions')}>Sessions</button>
        <button icon="plus" @tap=${() => store.sessions.create()}>New session…</button>
        <button icon="filter" @tap=${() => store.openScope({})}>Scope…</button>
        <button icon="doc" @tap=${() => store.openNotebook()}>Notebook</button>
        <button icon="question" @tap=${() => store.showWhatCan()}>${W.whatCanItDo}</button>
        <divider/>
        <button icon="bell" @tap=${ask('needs')}>${W.needsYou}</button>
        <button icon="shield" @tap=${ask('approvals')}>Approvals</button>
        <button icon="clock" @tap=${ask('activity')}>What my agent did</button>
      </menu>
    </toolbar>
    ${s && has('connection') ? connectionTpl(s) : nothing}
    ${[...store.banners().filter(appSays), ...switchBanner()].map(bannerTpl)}
    ${S.err ? html`<notice tone="danger" text=${S.err}/><button @tap=${() => { S.err = ''; store.boot(); }}>Try again</button>` : nothing}
    ${ui.err ? html`<notice tone="danger" text=${ui.err}/>` : nothing}
    ${tpl}
    ${S.sendErr ? html`<notice tone="warn" text=${S.sendErr}/>` : nothing}
    ${composer}
  </screen>`;
}

// connWords: the chip's segments in xbind's own numbers (ws/words.js chipWords), and one line of them
function connWords(s) {
  const w = chipWords(store.counts(), s.model, chat.compact(), store.isPrivate(), s.reach);
  return { ...w, summary: [w.summary || [w.conn, w.not].filter(Boolean).join(' · '), w.net].filter(Boolean).join(' · ') };
}
// the connection line: a tap opens the scope panel, its model part the models; the web and MCP tools, when on,
// ride the connected part ("14 tiles · 3 orgs · web")
function connectionTpl(s) {
  const w = connWords(s);
  return html`<connection connected=${[w.conn, w.net].filter(Boolean).join(' · ')} not=${w.not || nothing} model=${short(s.model) || nothing} ?private=${store.isPrivate()} label=${w.label}
    @tap=${() => store.openScope({})} @model=${() => store.openScope({ models: true })}/>`;
}

// what the app's own chrome says itself, in one place: paused is its bar under the composer, with Resume (a grant,
// never this view's) — the same words in a banner here too said it twice, and a banner pinned above the conversation
// covered what it scrolled over
const appSays = (b) => b.key !== 'paused';

// a state of the page: its words, and what to do about it — one button, or a held session's ways forward, a row of
// them under the notice (a notice has no children) — never Resume: that is the app's
function bannerTpl(b) {
  const acts = [...(b.button ? [b.button] : []), ...(b.buttons || [])];
  const btn = (x, i) => html`<button role=${acts.length > 1 && i === 0 ? 'primary' : 'plain'} @tap=${() => x.act()}>${x.label}</button>`;
  return html`<notice tone=${TONE[b.tone] || 'info'} title=${b.bold} text=${[b.text, b.more].filter(Boolean).join(' ') || nothing}/>
    ${acts.length > 1 ? html`<stack axis="h" wrap>${acts.map(btn)}</stack>` : acts.length ? btn(acts[0], 0) : nothing}`;
}

// the agent's switch where no banner of the store's says it (xbind's `off` on the empty state: no agent given, the
// agent held on this xbin, no sandboxes) once the conversation has rows — an empty one says it in its empty state
function switchBanner() {
  const e = store.S.empty;
  const off = e && e.off;
  if (!off || !W.offTitle[off] || !store.session.current() || !store.session.shown().blocks.length) return [];
  return [{ key: 'switch', tone: 'warn', bold: W.offTitle[off], text: e.line || W.offWhy[off] }];
}

// --- what the app says, and where it opens the view ------------------------------------

// the app's chrome: what the view was opened about (xbin:agent-context), read as the web page reads it
try { native.on('agent', (m) => { const c = dock.contextOf(m); if (c) store.onContext(c); }); } catch { /* a runtime from before the agent bridge */ }
try { native.on('width', () => paint()); } catch { /* a runtime from before width */ }

// link: an address of the view (the header lists them)
async function link(hash) {
  const q = new URLSearchParams(String(hash || '').replace(/^#/, ''));
  const S = store.S;
  if (q.has('s')) { const id = Number(q.get('s')); if (S.sessions.some((x) => x.id === id) && id !== S.sel) await store.select(id); }
  if (q.has('about')) { const c = dock.contextOf({ type: dock.MSG.context, tile: q.get('about') }); if (c) store.onContext(c); }
  if (q.has('screen')) { const c = dock.contextOf({ type: dock.MSG.context, screen: q.get('screen') }); if (c) store.onContext(c); }
  if (q.has('scope')) await store.openScope({});
  if (q.has('models')) await store.openScope({ models: true });
  if (q.has('sessions')) { S.pop = 'sessions'; paint(); }
  if (q.has('notebook')) await store.openNotebook();
  if (q.has('can')) store.showWhatCan();
}

// --- start --------------------------------------------------------------------------------

paint();
store.wire();
store.boot().then(() => link(globalThis.location?.hash || '')).then(paint, fail);
globalThis.addEventListener?.('hashchange', () => { link(globalThis.location.hash).catch(fail); });

export { store as app };
