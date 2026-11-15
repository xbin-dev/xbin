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
import { Session } from '../model/session.js';
import { blockOf, busy } from '../model/fold.js';
import { ChatWindow } from '../chat-window.js';
import { copyAnswer } from '../chat-copy.js';
import { md } from '../chat-md.js';
import { ws } from './api.js';
import * as dock from './dock.js';
import { W, CHOICES, quotaOf, composerState, aboutLine, splitAbout, blankAt, clock, slug as slugOf, agentCard, requestCard, possessive } from './words.js';
import { compose, UPDATES } from './place.js';
import { hostCard } from './cards.js';
import { chipView, panelView } from './scope.js';
import * as SM from './scope-model.js';
import { sessionsView, emptyView, notebookView, bannersView } from './panels.js';
import { WS_CSS } from './css.js';
try { await import('/vendor/bx-icons.js'); } catch { /* an older xbind: words without glyphs */ }

const icon = (name) => (name ? html`<bx-icon name=${name} aria-hidden="true"></bx-icon>` : nothing);
const errOf = (r, dflt) => (r && r.data && r.data.error) || dflt || `error ${r ? r.status : ''}`;

// ---- the page's state ----------------------------------------------------------------------

const S = {
  state: null, sessions: [], sel: null, err: '',
  cards: null, conn: null, full: null, fullErr: '', empty: null, sd: new Map(),
  view: 'chat', pop: '', whatCan: false,
  scope: { view: 'tree', filter: 'all', ticks: new Set(), expanded: new Set(), pick: '', what: null, refused: null, busy: false, err: '', focus: '' },
  ses: { renaming: null, err: '' },
  nb: { pages: [], open: '', page: null, editing: false, draft: null, confirm: false, err: '', busy: false },
  context: null, drafts: new Map(), sending: false, sendErr: '',
  conn0: 'live', lostAt: 0, updating: false, afterUpdate: null, loadedAt: 0,
  bags: new Map(), rev: 0, narrow: false, said: '',
};
const session = new Session(`/api/${globalThis.xbin?.self ?? ''}`, {
  change: () => connChanged(),
  runs: () => soon('sessions'),
  event: (ev) => { if (ev.type === 'run') { if (ev.run === S.sel) soon('cards'); soon('sessions'); } },
  reset: () => soon('cards'),
}, { deltas: true, page: 50 });
const win = new ChatWindow(session);
let layout = { at: new Map(), tail: [], live: {}, plan: null };
let root = null;

// a refresh asked for by several events in a row is one read
const timers = {};
function soon(what, ms = 120) {
  clearTimeout(timers[what]);
  timers[what] = setTimeout(() => (what === 'cards' ? loadCards() : what === 'sessions' ? loadSessions() : loadState()), ms);
}

// ---- the ChatCtx the kit's views reach the page through ---------------------------------------

const slotKey = (b, slot) => (slot === 'body' ? b.k : `${b.k}:${slot}`);
const bag = (k) => { let b = S.bags.get(k); if (!b) { if (S.bags.size > 60) S.bags.delete(S.bags.keys().next().value); S.bags.set(k, (b = {})); } return b; };
const act = {
  now: () => Date.now(),
  tileName,
  get offline() { return offline(); },
  bag,
  repaint: () => { S.rev++; paint(); },
  openSession: (a) => dock.showSession(a.session, a.tile),
  review: (q) => { dock.post(q.kind === 'changeset' ? dock.openChangeset(q.ref) : dock.openRequest(q.ref)); announce('xbin shows the request to review.'); },
  send: (text) => send(text),
  fill: (text) => fill(text),
  briefSave, briefStart, startDefaults,
  openScope: (o) => openScope(o),
  whatCan: () => showWhatCan(),
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
    const { about, text: rest } = splitAbout(text);
    if (!about) return null;
    return html`<div class="bxc-msg bxc-user"><div class="bxc-bubble">${aboutChip(about)}<div class="bxc-text">${rest}</div></div></div>`;
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
    }
    return say('', d.text || d.summary || '');
  }
  return null;
}
const aboutChip = (a, extra = nothing) => html`<span class="wsp-about">${icon(a.tile ? 'grid' : 'columns')}About ${a.tile ? '' : 'the '}<b>${a.tile ? a.name || a.tile : a.screen}</b>${a.tile ? '' : ' screen'}${extra}</span>`;

// ---- loading ---------------------------------------------------------------------------------

async function loadState() {
  const r = await ws.state();
  if (r.ok) S.state = r.data;
  paint();
}

async function loadSessions() {
  const r = await ws.sessions();
  if (!r.ok) { S.err = errOf(r, 'The sessions could not be read.'); paint(); return; }
  S.sessions = r.data.sessions || [];
  paint();
}

async function loadCards() {
  if (S.sel == null) return;
  const id = S.sel;
  const r = await ws.cards(id);
  if (!r.ok || S.sel !== id) return;
  const before = S.cards;
  S.cards = r.data;
  sayChanges(before, S.cards);
  paint();
}

async function loadConnection() {
  const s = current();
  if (!s) return;
  const [b, f] = await Promise.all([ws.connection(s.id), s.handle ? ws.fullConnection(s.handle) : Promise.resolve(null)]);
  if (S.sel !== s.id) return;
  if (b.ok) S.conn = b.data;
  if (f && f.ok) { S.full = f.data; S.fullErr = ''; } else S.fullErr = f ? errOf(f, 'xbin could not say which tiles are connected.') : '';
  paint();
}

async function loadEmpty() {
  const id = S.sel;
  const r = await ws.empty(id);
  if (S.sel === id) { S.empty = r.ok ? r.data : null; paint(); }
}

const current = () => S.sessions.find((s) => s.id === S.sel) || null;

async function select(id) {
  S.sel = id;
  S.cards = null; S.conn = null; S.full = null; S.empty = null; S.sd.clear(); S.pop = ''; S.view = 'chat'; S.whatCan = false;
  win.reset();
  paint();
  try { await session.select(id); } catch (e) { S.err = String(e.message || e); }
  S.loadedAt = Date.now();
  ws.putPrefs({ lastSession: id });
  await Promise.all([loadCards(), loadConnection(), loadEmpty()]);
}

async function boot() {
  const [st, list, prefs] = await Promise.all([ws.state(), ws.sessions(), ws.prefs()]);
  if (st.ok) S.state = st.data;
  let sessions = list.ok ? list.data.sessions || [] : [];
  if (!sessions.some((s) => s.main)) {
    const m = await ws.main();
    if (!m.ok) { S.err = errOf(m, 'Your agent could not start a session.'); paint(); return; }
    sessions = [m.data.session, ...sessions];
  }
  S.sessions = sessions;
  const last = prefs.ok && prefs.data.lastSession;
  await session.start();
  await select(sessions.some((s) => s.id === last) ? last : sessions.find((s) => s.main).id);
}

// ---- the conversation's changes, said once (a polite live region) --------------------------------

// (the region is lit's: its text changes by a paint, never by hand)
function announce(text) { S.said = text; paint(); }
function sayChanges(before, after) {
  if (!before) return;
  const old = new Map((before.requests || []).map((q) => [q.id, q.state]));
  for (const q of after.requests || []) {
    if (!old.has(q.id) && (q.state === 'pending' || q.state === 'proposed')) return announce(`A request waits for your approval: ${q.title || q.kind}.`);
    if (old.has(q.id) && old.get(q.id) !== q.state && q.state !== 'running') return announce(`${q.title || 'A request'}: ${requestCard(q, Date.now(), tileName).head}`);
  }
  const was = new Map((before.agents || []).map((a) => [a.id, `${a.status}:${a.waits}`]));
  for (const a of after.agents || []) {
    if (was.get(a.id) !== `${a.status}:${a.waits}`) { const w = agentCard(a, Date.now(), tileName); return announce(`${w.tile}: ${w.word}.`); }
  }
}

// ---- the connection: reconnecting, xbin updating, back --------------------------------------------

{
  const chunk = session.live.chunk.bind(session.live);
  session.live.chunk = (t) => { const r = chunk(t); if (r === 'bye') { S.updating = true; paint(); } return r; };
}
function connChanged() {
  const c = session.conn;
  if (c !== S.conn0) {
    if (c === 'reconnecting') S.lostAt = Date.now();
    if (c === 'live') {
      if (S.updating) { S.updating = false; S.afterUpdate = { at: Date.now() }; }
      S.lostAt = 0;
      soon('cards'); soon('sessions'); soon('state');
    }
    S.conn0 = c;
  }
  paint();
}
const offline = () => !S.updating && session.conn !== 'live' && S.lostAt && Date.now() - S.lostAt > CHOICES.offlineAfterMs;

// ---- the composer ------------------------------------------------------------------------------

const composer = () => root && root.querySelector('bx-chat-composer');
function fill(text, focus = true) {
  S.drafts.set(S.sel, text);
  paint();
  if (!focus) return;
  requestAnimationFrame(() => {
    const c = composer();
    const ta = c && c.querySelector('textarea');
    if (!ta) return;
    ta.focus();
    const at = blankAt(text);
    if (at) ta.setSelectionRange(at[0], at[1]); else ta.setSelectionRange(text.length, text.length);
  });
}

async function send(text) {
  text = String(text || '').trim();
  if (!text || S.sel == null || S.sending) return false;
  const cs = composerNow();
  if (cs.disabled || cs.hold) return false;
  const about = S.context && !S.context.none ? aboutLine(S.context) : '';
  S.sending = true; S.sendErr = ''; paint();
  const r = await ws.send(S.sel, about ? `${about}\n${text}` : text, Math.random().toString(36).slice(2));
  S.sending = false;
  if (!r.ok) { S.sendErr = errOf(r, 'The message was not sent.'); paint(); return false; }
  S.context = null;
  if ((S.drafts.get(S.sel) || '').trim() === text) S.drafts.set(S.sel, '');
  S.whatCan = false;
  paint();
  return true;
}

function composerNow() {
  const v = session.current();
  const blocks = v ? session.shown().blocks : [];
  return composerState({
    paused: !!(S.state && S.state.delegation && S.state.delegation.paused),
    off: !!(S.state && S.state.delegation && S.state.delegation.workspace === false),
    quota: quotaOf(S.state, v && v.run), conn: session.conn, updating: S.updating, offline: offline(),
    empty: !blocks.some((b) => b.k === 'user'), asking: !!(layout.live && layout.live.ask), brief: !!(layout.live && layout.live.brief),
  });
}

// ---- the context chip (xbin:agent-context) ---------------------------------------------------------

function tileName(path) {
  const t = S.full && (S.full.tiles || []).find((x) => x.tile === path);
  return (t && t.name) || path;
}
function onContext(c) {
  S.context = c.none ? null : c.tile ? { tile: c.tile, name: tileName(c.tile) } : { screen: c.screen };
  paint();
  requestAnimationFrame(() => composer()?.focus());
}
function contextChip() {
  const c = S.context;
  if (!c) return nothing;
  const t = c.tile && S.full ? (S.full.tiles || []).find((x) => x.tile === c.tile) : null;
  const not = t && t.state !== 'connected';
  const chip = not
    ? html`<button class="wsp-about not" title="open the scope panel" @click=${() => openScope({})}>${icon('grid')}About <b>${t.name || c.tile}</b> — not connected${t.reason ? ` (${t.reason})` : ''}</button>`
    : aboutChip({ ...c, name: c.name || tileName(c.tile) });
  return html`<div class="wsp-ctx">${chip}<button class="x quiet" aria-label="remove the context" title="don't send it with the next message" @click=${() => { S.context = null; paint(); }}>${icon('xmark')}</button></div>`;
}

// ---- briefs ------------------------------------------------------------------------------------

function startDefaults(b) {
  const key = `${b.id}:${b.harness || ''}:${b.updatedMs || 0}`;
  if (S.sd.has(key)) return S.sd.get(key);
  S.sd.set(key, null);
  ws.startDefaults(S.sel, b.id).then((r) => { if (r.ok) { S.sd.set(key, r.data); S.rev++; paint(); } });
  return null;
}
async function briefSave(b, vals) {
  const r = await ws.putBrief(S.sel, b.id, vals);
  if (!r.ok) return errOf(r, 'The brief was not saved.');
  await loadCards();
  return '';
}
async function briefStart(b) {
  if (b.state === 'asked') {
    const q = ((S.cards && S.cards.requests) || []).find((x) => x.brief === b.id && x.state === 'pending' && x.ref && !/^(brief|resume):/.test(x.ref));
    if (q) { act.review(q); return ''; }
  }
  const r = await ws.startBrief(S.sel, b.id);
  if (!r.ok) return errOf(r, 'It could not be started.');
  if (r.data.request) dock.post(dock.openRequest(r.data.request));
  await loadCards();
  return r.data.started === false && r.data.outcome ? r.data.outcome : '';
}

// ---- the scope panel -----------------------------------------------------------------------------

async function openScope(o = {}) {
  S.pop = 'scope';
  const sc = S.scope;
  Object.assign(sc, { view: o.models ? 'models' : 'tree', err: '', what: null, refused: null, pick: '', busy: false });
  paint();
  if (!S.full) await loadConnection();
  sc.ticks = SM.initialTicks(S.full);
  sc.expanded = new Set(SM.tree(S.full, sc.ticks).filter((x) => x.box === 'mixed' || x.state === 'blocked').map((x) => x.id).slice(0, 3));
  sc.err = S.fullErr;
  paint();
}
const models = () => {
  const s = current();
  const list = ((S.state && S.state.models && S.state.models.models) || []).slice();
  if (s && s.model && !list.some((m) => m.id === s.model)) list.unshift({ id: s.model });
  return list;
};
async function pickModel(id) {
  const sc = S.scope, s = current();
  Object.assign(sc, { pick: id, what: null, err: '', view: 'whatif' });
  paint();
  if (!s || id === s.model) return;
  const [b, f] = await Promise.all([ws.connection(s.id, id), s.handle ? ws.fullConnection(s.handle, id) : Promise.resolve(null)]);
  if (sc.pick !== id) return;
  if (!b.ok) { sc.err = errOf(b, 'xbin could not say what would change.'); paint(); return; }
  sc.what = SM.whatIf(b.data, f && f.ok ? f.data : null, s.model);
  if (sc.what.refused) { sc.refused = sc.what.refused; sc.view = 'refused'; }
  paint();
}
async function switchModel() {
  const sc = S.scope, s = current();
  if (!s || !sc.pick) return;
  sc.busy = true; paint();
  const r = await ws.patch(s.id, { model: sc.pick });
  sc.busy = false;
  if (r.status === 409 && r.data.code === 'model_taint') {
    sc.refused = SM.refusedWords({ model: r.data.model || sc.pick, orgs: r.data.orgs || [], reason: r.data.reason || '', current: s.model });
    sc.view = 'refused';
  } else if (!r.ok) sc.err = errOf(r, 'The model was not changed.');
  else { S.pop = ''; await loadSessions(); await loadConnection(); }
  paint();
}
async function newSessionOn(model) {
  const r = await ws.newSession(S.sel, { model });
  if (!r.ok) { S.scope.err = errOf(r, 'A new session could not be started.'); paint(); return; }
  S.sessions = [...S.sessions, r.data.session];
  await select(r.data.session.id);
}
async function applyScope() {
  const sc = S.scope, s = current();
  sc.busy = true; paint();
  const r = await ws.patch(s.id, { scope: SM.scopeOf(S.full, sc.ticks) });
  sc.busy = false;
  if (!r.ok) { sc.err = errOf(r, 'The scope was not changed.'); paint(); return; }
  S.pop = '';
  await loadSessions(); await loadConnection(); loadEmpty();
}
const scopeAct = {
  toggle: () => (S.pop === 'scope' ? closePop() : openScope({})),
  close: () => closePop(),
  key: (e) => { if (e.key === 'Escape') { e.stopPropagation(); closePop(); } },
  filter: (k) => { S.scope.filter = k; paint(); },
  selectAll: () => { S.scope.ticks = SM.allAllowed(S.full); paint(); },
  expand: (id) => { const x = S.scope.expanded; if (x.has(id)) x.delete(id); else x.add(id); S.scope.focus = 'o:' + id; paint(); },
  tickOrg: (id) => { S.scope.ticks = SM.toggleOrg(S.scope.ticks, S.full, id); S.scope.focus = 'o:' + id; paint(); },
  tickTile: (t) => { S.scope.ticks = SM.toggleTile(S.scope.ticks, S.full, t); S.scope.focus = 't:' + t; paint(); },
  treeKey: (e) => treeKey(e),
  models: () => { S.scope.view = 'models'; paint(); },
  back: () => { Object.assign(S.scope, { view: 'tree', pick: '', what: null }); paint(); },
  pick: (id) => pickModel(id),
  switchModel: () => switchModel(),
  newOn: (m) => newSessionOn(m),
  apply: () => applyScope(),
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

// ---- sessions -----------------------------------------------------------------------------------

const sessAct = {
  key: (e) => { if (e.key === 'Escape') { e.stopPropagation(); closePop(); } },
  pick: (id) => { S.pop = ''; if (id !== S.sel) select(id); else paint(); },
  create: async () => {
    const r = await ws.newSession(S.sel);
    if (!r.ok) { S.ses.err = errOf(r, 'A new session could not be started.'); paint(); return; }
    S.sessions = [...S.sessions, r.data.session];
    await select(r.data.session.id);
  },
  renaming: (id) => { S.ses.renaming = id; paint(); requestAnimationFrame(() => root.querySelector('.wsp-rename')?.select()); },
  rename: async (id, name) => {
    if (S.ses.renaming !== id) return;
    S.ses.renaming = null;
    name = String(name || '').trim();
    const s = S.sessions.find((x) => x.id === id);
    if (name && s && name !== s.name) { const r = await ws.patch(id, { name }); if (!r.ok) S.ses.err = errOf(r); await loadSessions(); }
    paint();
  },
  archive: async (id, on) => {
    const r = await ws.patch(id, { archived: on });
    if (!r.ok) { S.ses.err = errOf(r); paint(); return; }
    await loadSessions();
    if (on && id === S.sel) select(S.sessions.find((s) => s.main).id);
  },
};

// ---- the notebook ----------------------------------------------------------------------------------

async function openNotebook() {
  S.view = 'notebook'; S.pop = '';
  Object.assign(S.nb, { open: '', page: null, editing: false, draft: null, confirm: false, err: '' });
  paint();
  const r = await ws.notebook(S.sel);
  S.nb.pages = r.ok ? r.data.pages || [] : [];
  S.nb.err = r.ok ? '' : errOf(r, 'The notebook could not be read.');
  paint();
}
const nbAct = {
  close: () => { S.view = 'chat'; paint(); },
  list: () => openNotebook(),
  add: () => { Object.assign(S.nb, { open: '+', page: null, editing: true, draft: {}, err: '' }); paint(); },
  read: async (slug) => {
    Object.assign(S.nb, { open: slug, page: null, editing: false, draft: null, confirm: false, err: '' });
    paint();
    const r = await ws.page(slug, S.sel);
    if (r.ok) S.nb.page = r.data.page; else S.nb.err = errOf(r, 'The page could not be read.');
    paint();
  },
  edit: () => { Object.assign(S.nb, { editing: true, draft: {} }); paint(); },
  cancel: () => { if (S.nb.open === '+') return openNotebook(); Object.assign(S.nb, { editing: false, draft: null }); paint(); },
  confirm: (on) => { S.nb.confirm = on; paint(); },
  save: async () => {
    const n = S.nb, p = n.page, d = n.draft || {};
    const title = (d.title ?? (p ? p.title : '')).trim();
    const body = d.body ?? (p ? p.body || '' : '');
    if (!title && n.open === '+') { n.err = 'Give the page a title.'; paint(); return; }
    n.busy = true; paint();
    const slug = n.open === '+' ? slugOf(title) : n.open;
    const r = await ws.putPage(slug, { title, body, base: p ? p.version : 0 });
    n.busy = false;
    if (r.status === 409) { n.err = 'The page changed since you opened it. Your text is still here: read the page again before you save.'; paint(); return; }
    if (!r.ok) { n.err = errOf(r, 'The page was not saved.'); paint(); return; }
    await nbAct.read(slug);
  },
  drop: async () => {
    const r = await ws.dropPage(S.nb.open);
    if (!r.ok) { S.nb.err = errOf(r); paint(); return; }
    openNotebook();
  },
  // the person's deliberate act, confirmed: the page no longer carries the orgs it was marked with
  clearStamps: async () => {
    const p = S.nb.page;
    const r = await ws.putPage(p.slug, { title: p.title, body: p.body || '', base: p.version, clearStamps: true });
    S.nb.confirm = false;
    if (!r.ok) { S.nb.err = errOf(r); paint(); return; }
    await nbAct.read(p.slug);
  },
  md: (s) => md(s),
};

function showWhatCan() { S.whatCan = true; S.pop = ''; S.view = 'chat'; if (!S.empty) loadEmpty(); paint(); requestAnimationFrame(() => root.querySelector('#wsp-empty-h')?.scrollIntoView({ block: 'nearest' })); }

// ---- pop-overs ---------------------------------------------------------------------------------------

let opener = null;
function openPop(name, e) { opener = e && e.currentTarget; S.pop = S.pop === name ? '' : name; paint(); if (S.pop) requestAnimationFrame(() => root.querySelector('.wsp-pop button, .wsp-pop [tabindex="0"]')?.focus()); }
function closePop() { S.pop = ''; S.ses.renaming = null; paint(); if (opener && opener.isConnected) opener.focus(); opener = null; }

// ---- the page's states --------------------------------------------------------------------------------

function banners() {
  const out = [];
  const d = (S.state && S.state.delegation) || {};
  const v = session.current();
  if (d.workspace === false) out.push({ key: 'off', tone: 'bad', icon: 'power', bold: 'Turned off for this workspace.', text: 'An admin turned workspace agents off. Nothing an agent started is running on your behalf. Your conversations and notebook are kept for when it is turned on again.' });
  if (d.paused) out.push({ key: 'paused', tone: 'warn', icon: 'pause', bold: `Paused by you${d.pausedMs || d.pausedAt ? ` at ${clock(d.pausedMs || Date.parse(d.pausedAt))}` : ''}.`, text: 'Your agent can’t read or change anything in the workspace until you resume. Tile agents that are already running keep going; their questions still come to you.' });
  const q = quotaOf(S.state, v && v.run);
  if (q) out.push({ key: 'quota', tone: 'warn', icon: 'clock', bold: 'Today’s limit is used up.', text: q.words, more: 'An admin can raise your limit.' });
  if (S.updating) out.push({ key: 'updating', tone: 'info', icon: 'refresh', bold: 'xbin is updating.', text: 'Back in about a minute. Running tile agents are stopped and continue afterwards.' });
  else if (offline()) out.push({ key: 'offline', tone: 'warn', icon: 'signal', bold: 'Can’t reach the workspace.', text: `Showing what was loaded at ${clock(S.loadedAt)}. Trying again… Approvals can’t be given until it’s back.`, button: { label: 'Try now', act: () => session.live.resync() } });
  if (S.afterUpdate) {
    const ag = (S.cards && S.cards.agents) || [];
    const cut = ag.filter((a) => a.status === 'stopped' || a.status === 'lost');
    const went = ag.filter((a) => ['starting', 'running', 'idle'].includes(a.status));
    const n = cut.length + went.length;
    out.push({ key: 'updated', tone: 'info', icon: 'refresh', bold: `xbin was updated at ${clock(S.afterUpdate.at)}.`,
      text: n ? `${went.length} of ${n} tile agents continued.${cut.length ? ` ${cut.map((a) => possessive(tileName(a.tile))).join(', ')} could not.` : ''}` : '',
      button: { label: 'OK', act: () => { S.afterUpdate = null; paint(); } } });
  }
  return out;
}

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
  const conn = (S.conn && S.conn.connection) || (s && s.connection) || {};
  const counts = { ...(conn.counts || {}), summary: (S.conn && S.conn.summary) || (s && s.summary) || '' };
  const cs = composerNow();
  const v = session.current();
  return html`<div class="wsp">
    <div class="wsp-strip" role="toolbar" aria-label="this session">
      <button class="wsp-sess" aria-haspopup="dialog" aria-expanded=${S.pop === 'sessions' ? 'true' : 'false'} title="sessions" @click=${(e) => openPop('sessions', e)}>
        <span class="nm">${s ? s.name || (s.main ? 'Main' : 'New session') : '…'}</span>${icon('chevron-down')}</button>
      ${s ? chipView({ counts, model: s.model, private: !!(S.full && S.full.model && S.full.model.private), narrow: S.narrow, open: S.pop === 'scope' }, { toggle: () => scopeAct.toggle() }) : nothing}
      <button class="wsp-menu-b" aria-label="more" aria-haspopup="menu" aria-expanded=${S.pop === 'menu' ? 'true' : 'false'} @click=${(e) => openPop('menu', e)}>${icon('ellipsis')}</button>
      ${S.pop === 'sessions' ? sessionsView({ list: S.sessions, current: S.sel, renaming: S.ses.renaming, err: S.ses.err }, sessAct) : nothing}
      ${S.pop === 'scope' ? panelView({ ...S.scope, full: S.full, counts, model: s && s.model, private: !!(S.full && S.full.model && S.full.model.private), models: models(), err: S.scope.err || (S.full ? '' : S.fullErr) }, scopeAct) : nothing}
      ${S.pop === 'menu' ? html`<div class="wsp-pop wsp-menu" role="menu" @keydown=${(e) => { if (e.key === 'Escape') closePop(); }}>
        <button role="menuitem" @click=${() => openNotebook()}>${icon('doc')}Notebook</button>
        <button role="menuitem" @click=${() => showWhatCan()}>${icon('question')}${W.whatCanItDo}</button></div>` : nothing}
    </div>
    ${bannersView(banners())}
    ${S.err ? html`<div class="wsp-banner bad" role="alert">${icon('warning')}<div class="wsp-bt">${S.err}</div><button @click=${() => { S.err = ''; boot(); }}>Try again</button></div>` : nothing}
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
        @bx-chat-stop=${() => ws.stop(S.sel)}
        @bx-chat-take-back=${(e) => { const q = session.queued().find((x) => String(x.id) === String(e.detail.id)); if (q) { ws.takeBack(S.sel, q.id); fill(q.text || '', true); } }}></bx-chat-composer>
      ${S.sendErr ? html`<p class="wsc-err" role="alert">${S.sendErr}</p>` : nothing}
      ${cs.note ? html`<p class="wsp-hint" role="status">${cs.note}</p>` : !hasUserMessage() && !cs.disabled ? html`<p class="wsp-hint">${(S.empty && S.empty.hint) || W.hint}</p>` : nothing}
    </div>` : nothing}
    <div id="wsp-live" class="wsp-vh" role="status" aria-live="polite">${S.said}</div>
  </div>`;
}

const hasUserMessage = () => { const v = session.current(); return !!(v && session.shown().blocks.some((b) => b.k === 'user')); };

function chatTpl(v) {
  if (!v) return html`<div class="wsc-muted" role="status">Loading…</div>`;
  const s = session.shown();
  layout = compose(s.blocks, S.cards, s.run, { words: W });
  const w = win.place(s);
  const rows = w.start || w.end < s.blocks.length ? s.blocks.slice(w.start, w.end) : s.blocks;
  const atEnd = w.end >= s.blocks.length && !s.hasNewer;
  const empty = !s.blocks.length && !s.hasOlder;
  const agents = ((S.cards && S.cards.agents) || []).filter((a) => a.status === 'running' || a.status === 'starting').length;
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
  dock.listen(onContext);
  // xbind's own events: a request decided, a change set, the switches — read again what they name
  try { globalThis.xbin?.events?.on((ev) => { if (ev && ev.type === 'delegate') { const op = ev.data && ev.data.op; if (op === 'state') soon('state'); else { soon('cards'); soon('sessions'); } } }); } catch { /* no events here */ }
  // running times move; a dropped connection becomes "can't reach" after a while
  setInterval(() => { if (offline() || session.conn !== 'live' || ((S.cards && S.cards.agents) || []).some((a) => a.status === 'running')) paint(); }, CHOICES.tick);
  globalThis.wsAgentPage = { testApi };
  paint();
  boot().then(() => paint());
}

if (typeof document !== 'undefined') mount(document);
