// ws/page.js — the workspace agent's own page (docs/workspace-agent.md §The
// agent's page; D235): what xbin's dock frames when it shows a person's
// workspace agent. boot.js loads it in place of agent.js when the backend
// says it is in workspace mode (GET /ws/state → mode "workspace"); the
// template's normal page is never loaded here, and nothing of this tree is
// loaded on a normal instance.
//
// What it draws: the session in view and its switcher (ws/panels.js), the
// scope chip and panel (ws/scope.js), the banners of the page's states, the
// conversation — the template's run view folded as ever (model/session.js,
// model/fold.js) and drawn with the chat kit's rows and window, the agent's
// own cards set into it (ws/place.js, ws/cards.js) — the empty state, the
// notebook, and the composer with the context chip.
//
// What it never draws: a grant. A request, a tile agent's start, an admin
// change set are opened in xbin's chrome by id (ws/dock.js); the page only
// shows that something waits and what came of it.
import { html, render, nothing } from '/vendor/lit-all.min.js';
import { chatCssText } from '/vendor/chat/css.js';
import { rowsView, blockView, noticeView } from '/vendor/chat/views.js';
import '/vendor/chat/composer.js';
import { blockOf, busy } from '../model/fold.js';
import { ChatWindow } from '../chat-window.js';
import { copyAnswer } from '../chat-copy.js';
import { md } from '../chat-md.js';
import * as dock from './dock.js';
import { W, CHOICES, aboutOf, blankAt } from './words.js';
import { UPDATES } from './place.js';
import { hostCard } from './cards.js';
import { chipView, panelView } from './scope.js';
import { sessionsView, emptyView, notebookView, bannersView } from './panels.js';
import { WS_CSS } from './css.js';
import { createStore } from './store.js';
try { await import('/vendor/bx-icons.js'); } catch { /* an older xbind: words without glyphs */ }

const icon = (name) => (name ? html`<bx-icon name=${name} aria-hidden="true"></bx-icon>` : nothing);

// ---- the page's state: the model both views stand on (ws/store.js) ------------------------------

let root = null;
let win = null;
const store = createStore({
  paint: () => paint(),
  post: (msg) => dock.post(msg),
  hooks: {
    reset: () => win && win.reset(),
    // a filled composer takes the cursor: in a starter's blank, else at its end
    filled: (text) => requestAnimationFrame(() => {
      const c = composer();
      const ta = c && c.querySelector('textarea');
      if (!ta) return;
      ta.focus();
      const at = blankAt(text);
      if (at) ta.setSelectionRange(at[0], at[1]); else ta.setSelectionRange(text.length, text.length);
    }),
    context: () => requestAnimationFrame(() => composer()?.focus()),
    renaming: () => requestAnimationFrame(() => root.querySelector('.wsp-rename')?.select()),
    whatCan: () => requestAnimationFrame(() => root.querySelector('#wsp-empty-h')?.scrollIntoView({ block: 'nearest' })),
  },
});
const { S, session, bag, current, send, fill, offline, tileName, banners, composerNow, hasUserMessage } = store;
win = new ChatWindow(session);
let layout = { at: new Map(), tail: [], live: {}, plan: null };

// ---- the ChatCtx the kit's views reach the page through ---------------------------------------

const slotKey = (b, slot) => (slot === 'body' ? b.k : `${b.k}:${slot}`);
const act = {
  now: () => Date.now(),
  tileName,
  get offline() { return offline(); },
  bag,
  repaint: () => { S.rev++; paint(); },
  openSession: (a) => dock.showSession(a.session, a.tile),
  review: (q) => store.review(q),
  send: (text) => send(text),
  fill: (text) => fill(text),
  // a redirect's way forward: a new session of the tile's own agent, the person's own words as its draft
  handTo: (tile, draft) => dock.showSession('new', tile, globalThis.window, draft),
  briefSave: store.briefSave, briefStart: store.briefStart, startDefaults: store.startDefaults,
  openScope: (o) => store.openScope(o),
  whatCan: () => store.showWhatCan(),
  md: (s) => md(s),
};
const ctx = {
  get running() { const v = session.current(); return !!(v && busy(v.run.status)); },
  ended: false, readonly: false, role: 'owner', agentName: 'The agent',
  get me() { return S.state ? S.state.user : ''; },
  isOpen: (b, slot, dflt) => session.ui.isOpen(slotKey(b, slot), !!dflt),
  setOpen: (b, slot, open, dflt) => { if (session.ui.isOpen(slotKey(b, slot), !!dflt) !== !!open) { win.keepView(); S.rev++; session.ui.toggle(slotKey(b, slot), !!dflt); } },
  rev: (b) => { const e = b.$src && layout.at.get(b.$src.id); return `${S.rev}:${offline() ? 1 : 0}:${e ? e.rev : ''}:${e && e.after.concat(e.replace || []).some(ticking) ? Math.floor(Date.now() / CHOICES.tick) : 0}`; },
  state: (b) => bag('kit:' + b.k),
  copy: (t) => navigator.clipboard.writeText(t),
  copyMessage: (b, e) => copyAnswer(e, b.text),
  repaint: () => paint(),
  // a question of the agent's: the answer is the person's next message
  answer: async (b, a) => {
    const c = a.content || {};
    const text = a.action === 'accept' ? String(c.other || c.answer || '').trim() : W.skipSent;
    if (text) await send(text);
  },
  host: (b) => hostCard(b, act),
  other: (kb, depth) => {
    if (depth) return plainOther(kb);
    const e = kb.$src && layout.at.get(kb.$src.id);
    if (!e) return plainOther(kb);
    const own = e.hide ? nothing : e.replace ? e.replace.map((x) => blockView(x, ctx)) : plainOther(kb) ?? blockView(kb, plain);
    return html`${own}${e.after.map((x) => blockView(x, ctx))}`;
  },
};
const plain = Object.create(ctx, { other: { value: (kb) => plainOther(kb) } });
const ticking = (x) => x.type === 'wsession' && x.data && x.data.status === 'running';

// what the kit doesn't draw this page's way: a person's message with its chip, xbin's and the tile agents' updates,
// a journal line
function plainOther(kb) {
  if (kb.kind === 'user') {
    const text = String(kb.text || '');
    if (text.startsWith(UPDATES)) {
      const nl = text.indexOf('\n');
      return noticeView({ k: kb.k, v: kb.v, kind: 'notice', icon: 'info', fold: true, title: 'Updates from xbin and your tile agents', text: `updates\n${nl >= 0 ? text.slice(nl + 1) : ''}` }, ctx);
    }
    const about = aboutOf(contextOf(kb));
    if (!about) return null;
    return html`<div class="bxc-msg bxc-user"><div class="bxc-bubble">${aboutChip(about, about.connected === false ? html` — not connected` : nothing)}<div class="bxc-text">${text}</div></div></div>`;
  }
  if (kb.kind === 'step') {
    const d = kb.detail || {};
    const say = (i, t, more = {}) => noticeView({ k: kb.k, v: kb.v, kind: 'notice', icon: i, text: t, ...more }, ctx);
    switch (kb.step) {
      case 'error': return say('warning', d.error || d.text || '', { tone: 'bad' });
      case 'compaction': return say('compact', 'Earlier turns were summarised');
      case 'finish': return d.result ? say('check', d.result, { md: true }) : say('check', 'finished');
      case 'cancel': return say('stop', `stopped${d.reason ? ': ' + d.reason : ''}`);
      case 'ask': return nothing;
      case 'off': return say('pause', d.text || '', { tone: 'warn' }); // the agent's switch: answered without the model (API.md)
    }
    return say('', d.text || d.summary || '');
  }
  return null;
}
// the context the backend answers back on the person's message (`context`; API.md §Workspace mode, "In fields")
function contextOf(kb) {
  const id = kb.$src && kb.$src.msgId;
  const v = id != null ? session.views.get(S.sel) : null;
  const m = v && v.messages.find((x) => x.id === id);
  return (m && m.context) || null; // the run view's message: {tile, name?, connected?} | {screen}
}
const aboutChip = (a, extra = nothing) => html`<span class="wsp-about">${icon(a.tile ? 'grid' : 'columns')}About ${a.tile ? '' : 'the '}<b>${a.tile ? a.name || a.tile : a.screen}</b>${a.tile ? '' : ' screen'}${extra}</span>`;

// ---- the composer ------------------------------------------------------------------------------

const composer = () => root && root.querySelector('bx-chat-composer');

// ---- the context chip (xbin:agent-context) ---------------------------------------------------------

function contextChip() {
  const c = store.contextNow();
  if (!c) return nothing;
  const chip = c.not
    ? html`<button class="wsp-about not" title="open the scope panel" @click=${() => store.openScope({})}>${icon('grid')}About <b>${c.name}</b> — not connected${c.reason ? ` (${c.reason})` : ''}</button>`
    : aboutChip(c);
  return html`<div class="wsp-ctx">${chip}<button class="x quiet" aria-label="remove the context" title="don't send it with the next message" @click=${() => store.dropContext()}>${icon('xmark')}</button></div>`;
}

// ---- the scope panel: the model's moves (ws/store.js), this page's keys and pop-over ------------------

const scopeAct = {
  ...store.scope,
  toggle: () => (S.pop === 'scope' ? closePop() : store.openScope({})),
  close: () => closePop(),
  key: (e) => { if (e.key === 'Escape') { e.stopPropagation(); closePop(); } },
  treeKey: (e) => treeKey(e),
};
// the tree's keys (the ARIA tree pattern): arrows move, right/left unfold and fold, space ticks
function treeKey(e) {
  const items = [...e.currentTarget.querySelectorAll('[role=treeitem]')];
  const i = items.indexOf(e.target.closest('[role=treeitem]'));
  if (i < 0) return;
  const el = items[i], key = el.dataset.key, isOrg = key.startsWith('o:');
  const go = (j) => { const t = items[Math.max(0, Math.min(items.length - 1, j))]; S.scope.focus = t.dataset.key; for (const x of items) x.tabIndex = x === t ? 0 : -1; t.focus(); };
  switch (e.key) {
    case 'ArrowDown': e.preventDefault(); go(i + 1); break;
    case 'ArrowUp': e.preventDefault(); go(i - 1); break;
    case 'Home': e.preventDefault(); go(0); break;
    case 'End': e.preventDefault(); go(items.length - 1); break;
    case 'ArrowRight': if (isOrg && el.getAttribute('aria-expanded') === 'false') { e.preventDefault(); scopeAct.expand(key.slice(2)); } break;
    case 'ArrowLeft':
      e.preventDefault();
      if (isOrg && el.getAttribute('aria-expanded') === 'true') scopeAct.expand(key.slice(2));
      else if (!isOrg) { const org = el.closest('.wsp-org'); go(items.indexOf(org.querySelector('[role=treeitem]'))); }
      break;
    case ' ': case 'Enter':
      e.preventDefault();
      if (el.getAttribute('aria-disabled') === 'true') break;
      if (isOrg) scopeAct.tickOrg(key.slice(2)); else scopeAct.tickTile(key.slice(2));
      break;
  }
}

// ---- sessions, the notebook: the model's (ws/store.js), with this page's keys ----------------------------

const sessAct = { ...store.sessions, key: (e) => { if (e.key === 'Escape') { e.stopPropagation(); closePop(); } } };
const nbAct = { ...store.notebook, md: (s) => md(s) };

// ---- pop-overs ---------------------------------------------------------------------------------------

let opener = null;
function openPop(name, e) { opener = e && e.currentTarget; S.pop = S.pop === name ? '' : name; paint(); if (S.pop) requestAnimationFrame(() => root.querySelector('.wsp-pop button, .wsp-pop [tabindex="0"]')?.focus()); }
function closePop() { S.pop = ''; S.ses.renaming = null; paint(); if (opener && opener.isConnected) opener.focus(); opener = null; }

// ---- drawing ---------------------------------------------------------------------------------------

let painting = false;
function paint() {
  if (!root || painting) return;
  painting = true;
  const sc = root.querySelector('.wsp-main');
  const chat = !!(sc && S.view === 'chat' && session.current());
  if (chat) win.attach(sc); else win.detach();
  try { render(pageTpl(), root); } finally { painting = false; }
  if (chat) win.after();
  if (S.pop === 'scope' && S.scope.focus) {
    const el = root.querySelector(`.wsp-tree [data-key="${CSS.escape(S.scope.focus)}"]`);
    if (el && document.activeElement !== el && root.contains(document.activeElement) && document.activeElement.closest('.wsp-tree')) { el.tabIndex = 0; el.focus(); }
  }
}

function pageTpl() {
  const s = current();
  const counts = store.counts();
  const cs = composerNow();
  const v = session.current();
  return html`<div class="wsp">
    <div class="wsp-strip" role="toolbar" aria-label="this session">
      <button class="wsp-sess" aria-haspopup="dialog" aria-expanded=${S.pop === 'sessions' ? 'true' : 'false'} title="sessions" @click=${(e) => openPop('sessions', e)}>
        <span class="nm">${s ? s.name || (s.main ? 'Main' : 'New session') : '…'}</span>${icon('chevron-down')}</button>
      ${s ? chipView({ counts, model: s.model, private: store.isPrivate(), narrow: S.narrow, open: S.pop === 'scope' }, { toggle: () => scopeAct.toggle() }) : nothing}
      <button class="wsp-menu-b" aria-label="more" aria-haspopup="menu" aria-expanded=${S.pop === 'menu' ? 'true' : 'false'} @click=${(e) => openPop('menu', e)}>${icon('ellipsis')}</button>
      ${S.pop === 'sessions' ? sessionsView({ list: S.sessions, current: S.sel, renaming: S.ses.renaming, err: S.ses.err }, sessAct) : nothing}
      ${S.pop === 'scope' ? panelView({ ...S.scope, full: S.full, counts, model: s && s.model, private: store.isPrivate(), models: store.models(), err: S.scope.err || (S.full ? '' : S.fullErr) }, scopeAct) : nothing}
      ${S.pop === 'menu' ? html`<div class="wsp-pop wsp-menu" role="menu" @keydown=${(e) => { if (e.key === 'Escape') closePop(); }}>
        <button role="menuitem" @click=${() => store.openNotebook()}>${icon('doc')}Notebook</button>
        <button role="menuitem" @click=${() => store.showWhatCan()}>${icon('question')}${W.whatCanItDo}</button></div>` : nothing}
    </div>
    ${bannersView(banners())}
    ${S.err ? html`<div class="wsp-banner bad" role="alert">${icon('warning')}<div class="wsp-bt">${S.err}</div><button @click=${() => { S.err = ''; store.boot(); }}>Try again</button></div>` : nothing}
    <main class="wsp-main" aria-label="conversation with your workspace agent" aria-busy=${v ? 'false' : 'true'}>
      ${S.view === 'notebook' ? notebookView(S.nb, nbAct) : chatTpl(v)}
    </main>
    ${S.view === 'chat' ? html`<div class="wsp-compose">
      ${contextChip()}
      <bx-chat-composer .draft=${S.drafts.get(S.sel) || ''} .placeholder=${cs.placeholder} .label=${'Message to your workspace agent'} ?disabled=${cs.disabled || S.sel == null}
        ?sending=${S.sending || !!cs.hold} ?busy=${!!(v && busy(v.run.status))} .attach=${false} .queue=${v ? session.queued() : []}
        .queueHint=${'go in when the agent’s turn ends'}
        @bx-chat-draft=${(e) => S.drafts.set(S.sel, e.detail.text)}
        @bx-chat-send=${(e) => send(e.detail.text)}
        @bx-chat-stop=${() => store.stop()}
        @bx-chat-take-back=${(e) => store.takeBack(e.detail.id)}></bx-chat-composer>
      ${S.sendErr ? html`<p class="wsc-err" role="alert">${S.sendErr}</p>` : nothing}
      ${cs.note ? html`<p class="wsp-hint" role="status">${cs.note}</p>` : !hasUserMessage() && !cs.disabled ? html`<p class="wsp-hint">${(S.empty && S.empty.hint) || W.hint}</p>` : nothing}
    </div>` : nothing}
    <div id="wsp-live" class="wsp-vh" role="status" aria-live="polite">${S.said}</div>
  </div>`;
}

function chatTpl(v) {
  if (!v) return html`<div class="wsc-muted" role="status">Loading…</div>`;
  const placed = store.place();
  const s = placed.shown;
  layout = placed.layout;
  const w = win.place(s);
  const rows = w.start || w.end < s.blocks.length ? s.blocks.slice(w.start, w.end) : s.blocks;
  const atEnd = w.end >= s.blocks.length && !s.hasNewer;
  const empty = !s.blocks.length && !s.hasOlder;
  const agents = store.working();
  return html`
    ${w.start || s.hasOlder ? html`<div class="wsp-earlier">… earlier messages <button class="quiet" @click=${() => w.older()}>load earlier</button></div>` : nothing}
    ${empty ? emptyView(S.empty, { starter: (st) => fill(st.text) }) : nothing}
    ${rowsView(rows.map((b) => blockOf(b)), ctx)}
    ${atEnd && layout.tail.length ? html`<div class="wsp-tail">${layout.tail.map((b) => html`<div class="bxc-in" data-card=${b.k}>${blockView(b, ctx)}</div>`)}</div>` : nothing}
    ${atEnd && (s.activity || agents) ? html`<div class="wsp-activity" role="status"><span class="sq" aria-hidden="true"></span>${[s.activity, agents ? `${agents} tile agent${agents === 1 ? '' : 's'} working` : ''].filter(Boolean).join(' · ')}</div>` : nothing}
    ${S.whatCan && !empty ? html`<div class="wsp-whatcan">${emptyView(S.empty, { starter: (st) => fill(st.text) })}<button class="quiet" @click=${() => { S.whatCan = false; paint(); }}>Close</button></div>` : nothing}
    ${w.pill ? html`<div class="wsp-jump"><button @click=${() => w.latest()}>${w.pill}</button></div>` : nothing}`;
}

// ---- start --------------------------------------------------------------------------------------

/** testApi: what the browser tests read (never private state they would have to know). */
export function testApi() {
  return { sel: S.sel, view: S.view, pop: S.pop, context: S.context, sessions: S.sessions.map((s) => s.id), offline: !!offline(), updating: S.updating, said: S.said };
}

export function mount(doc = document) {
  doc.title = W.title;
  const style = doc.createElement('style');
  style.dataset.of = 'ws-page';
  style.textContent = chatCssText + WS_CSS;
  doc.head.append(style);
  root = doc.createElement('div');
  root.id = 'ws-page';
  doc.body.replaceChildren(root);
  const narrow = () => { const n = globalThis.innerWidth < CHOICES.narrow; if (n !== S.narrow) { S.narrow = n; paint(); } };
  S.narrow = globalThis.innerWidth < CHOICES.narrow;
  globalThis.addEventListener('resize', narrow);
  doc.addEventListener('keydown', (e) => { if (e.key === 'Escape' && S.pop) closePop(); });
  doc.addEventListener('pointerdown', (e) => { if (S.pop && !e.target.closest('.wsp-pop, .wsp-strip button')) closePop(); });
  dock.listen(store.onContext);
  // xbind's own events (a request decided, a change set, the switches), and the tick of running times (ws/store.js)
  store.wire();
  globalThis.wsAgentPage = { testApi };
  paint();
  store.boot().then(() => paint());
}

if (typeof document !== 'undefined') mount(document);
