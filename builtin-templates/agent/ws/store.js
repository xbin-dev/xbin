// ws/store.js — the workspace agent's page as a model: its state and
// everything it does that isn't drawing. Both views stand on it — the web
// page (ws/page.js, lit and the chat kit) and the app's native view
// (native/ws.js, /vendor/xb-native.js) — so a session switch, a staged
// scope, a model's what-if, a refused change, the notebook, the banners and
// what the composer takes are one behaviour, drawn twice (D233, D235;
// API.md §Workspace mode → The page).
//
// No lit, no DOM. A view gives it:
//   paint()        draw again
//   post(msg)      ask xbin's chrome (ws/dock.js builds the messages): the
//                  web posts to the dock that frames it, the native view
//                  asks the app (xbin.native.agent)
//   frame(fn)      the Session's repaint clock (optional)
//   hooks          what only a view can do, each optional: reset() (a new
//                  session is in view: forget the scroll window),
//                  filled(text) (the composer was filled: put the cursor in
//                  it), context() (a context chip arrived), renaming(id),
//                  whatCan() (the "what can it do" lists are shown)
//
// It grants nothing: a request, a start, a change set are opened in the
// chrome by id; pausing and resuming are the chrome's.
import { Session } from '../model/session.js';
import { busy } from '../model/fold.js';
import { ws, state as readState } from './api.js';
import * as dock from './dock.js';
import { W, CHOICES, quotaOf, composerState, clock, slug as slugOf, agentCard, requestCard, possessive } from './words.js';
import { compose } from './place.js';
import * as SM from './scope-model.js';

export const errOf = (r, dflt) => (r && r.data && r.data.error) || dflt || `error ${r ? r.status : ''}`;

/** A request the chrome can open: xbind's own id (`ref`), not one the brief's own button confirms. */
export const reviewable = (q) => !!(q && q.ref && !/^(brief|resume):/.test(q.ref));

export function createStore({ paint = () => {}, post = () => false, frame = null, hooks = {} } = {}) {
  const S = {
    state: null, sessions: [], sel: null, err: '',
    cards: null, conn: null, full: null, fullErr: '', empty: null, sd: new Map(),
    view: 'chat', pop: '', whatCan: false,
    scope: { view: 'tree', filter: 'all', ticks: new Set(), expanded: new Set(), pick: '', what: null, refused: null, busy: false, err: '', focus: '' },
    ses: { renaming: null, err: '' },
    nb: { pages: [], open: '', page: null, editing: false, draft: null, confirm: false, err: '', busy: false },
    context: null, drafts: new Map(), sending: false, sendErr: '', heldErr: '',
    conn0: 'live', lostAt: 0, updating: false, afterUpdate: null, loadedAt: 0,
    bags: new Map(), rev: 0, narrow: false, said: '',
  };
  const hook = (name, ...a) => (typeof hooks[name] === 'function' ? hooks[name](...a) : undefined);
  let layout = { at: new Map(), tail: [], live: {}, plan: null };

  // a refresh asked for by several events in a row is one read
  const timers = {};
  function soon(what, ms = 120) {
    clearTimeout(timers[what]);
    timers[what] = setTimeout(() => (what === 'cards' ? loadCards() : what === 'sessions' ? loadSessions() : loadState()), ms);
  }

  const session = new Session(`/api/${globalThis.xbin?.self ?? ''}`, {
    change: () => connChanged(),
    runs: () => soon('sessions'),
    event: (ev) => { if (ev.type === 'run') { if (ev.run === S.sel) soon('cards'); soon('sessions'); } },
    reset: () => soon('cards'),
    ...(frame ? { frame } : {}),
  }, { deltas: true, page: 50 });

  const bag = (k) => { let b = S.bags.get(k); if (!b) { if (S.bags.size > 60) S.bags.delete(S.bags.keys().next().value); S.bags.set(k, (b = {})); } return b; };

  // ---- loading ---------------------------------------------------------------------------------

  async function loadState() {
    const r = await readState();
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
    if (f && f.ok) { S.full = f.data; S.fullErr = ''; S.rev++; /* the cards say tiles by xbind's names */ } else S.fullErr = f ? errOf(f, 'xbin could not say which tiles are connected.') : '';
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
    hook('reset');
    paint();
    try { await session.select(id); } catch (e) { S.err = String(e.message || e); }
    S.loadedAt = Date.now();
    ws.putPrefs({ lastSession: id });
    await Promise.all([loadCards(), loadConnection(), loadEmpty()]);
  }

  async function boot() {
    const [st, list, prefs] = await Promise.all([readState(), ws.sessions(), ws.prefs()]);
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

  // ---- the conversation's changes, said once (the web's polite live region) ----------------------

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
  const offline = () => !S.updating && session.conn !== 'live' && !!S.lostAt && Date.now() - S.lostAt > CHOICES.offlineAfterMs;
  const delegation = () => (S.state && S.state.delegation) || {};
  const paused = () => !!delegation().paused;

  // ---- the composer ------------------------------------------------------------------------------

  /** place(): the session's cards set into what the conversation shows now (ws/place.js) — {shown, layout}. */
  function place() {
    const shown = session.shown();
    layout = compose(shown.blocks, S.cards, shown.run, { words: W });
    return { shown, layout };
  }

  function fill(text, focus = true) {
    S.drafts.set(S.sel, text);
    paint();
    if (focus) hook('filled', text);
  }

  async function send(text) {
    text = String(text || '').trim();
    if (!text || S.sel == null || S.sending) return false;
    const cs = composerNow();
    if (cs.disabled || cs.hold) return false;
    const c = S.context && !S.context.none ? (S.context.tile ? { tile: S.context.tile } : { screen: S.context.screen }) : null;
    S.sending = true; S.sendErr = ''; paint();
    const r = await ws.send(S.sel, text, Math.random().toString(36).slice(2), c);
    S.sending = false;
    if (r.status === 409 && r.data && r.data.code === 'agent-off') {
      // the agent's switch is off (xbind said which): the transcript has the line, the words stay here, unsent
      S.sendErr = r.data.error || 'Your agent is off: nothing was sent.';
      await Promise.all([loadState(), loadEmpty()]); // (the empty state says the switch too: GET /ws/empty → off)
      paint();
      return false;
    }
    if (!r.ok) { S.sendErr = errOf(r, 'The message was not sent.'); paint(); return false; }
    S.context = null;
    if ((S.drafts.get(S.sel) || '').trim() === text) S.drafts.set(S.sel, '');
    S.whatCan = false;
    paint();
    return true;
  }

  const hasUserMessage = () => { const v = session.current(); return !!(v && session.shown().blocks.some((b) => b.k === 'user')); };

  function composerNow() {
    const v = session.current();
    const blocks = v ? session.shown().blocks : [];
    return composerState({
      paused: paused(), off: delegation().workspace === false,
      quota: quotaOf(S.state), conn: session.conn, updating: S.updating, offline: offline(),
      empty: !blocks.some((b) => b.k === 'user'), asking: !!(layout.live && layout.live.ask), brief: !!(layout.live && layout.live.brief),
    });
  }

  /** hint(): the standing line under the composer — a state's note, else the nudge until the first message ('' after). */
  function hint() {
    const cs = composerNow();
    if (cs.note) return cs.note;
    return !hasUserMessage() && !cs.disabled ? (S.empty && S.empty.hint) || W.hint : '';
  }

  /** stop(): end the turn; takeBack(id): a queued message comes back into the composer. */
  const stop = () => ws.stop(S.sel);
  function takeBack(id) {
    const q = session.queued().find((x) => String(x.id) === String(id));
    if (q) { ws.takeBack(S.sel, q.id); fill(q.text || '', true); }
  }

  /**
   * replies(): the quick replies above the composer, from what the
   * conversation waits on — "Go ahead" / "Change the plan" while the plan
   * waits for a go, the choices of the question that waits: [{id, label,
   * send | fill, primary}] (a tap sends, or fills the composer).
   */
  function replies() {
    const out = [];
    const take = (b) => { if (b && b.kind === 'host' && b.type === 'replies') (b.data.choices || []).forEach((c, i) => out.push({ id: `go:${i}`, label: c.label, send: c.send || '', fill: c.fill || '', primary: !!c.primary })); };
    for (const e of layout.at.values()) { (e.replace || []).forEach(take); e.after.forEach(take); }
    layout.tail.forEach(take);
    const a = layout.live && layout.live.ask;
    const choices = a && a.kind === 'ask' && a.schema ? a.schema.properties.answer.enum || [] : [];
    choices.forEach((c, i) => out.push({ id: `ask:${i}`, label: c, send: c, fill: '', primary: false }));
    return out;
  }
  function reply(id) {
    const r = replies().find((x) => x.id === id);
    if (!r) return false;
    if (r.send) return send(r.send);
    fill(r.fill || r.label);
    return true;
  }

  // ---- the context chip (xbin:agent-context) ---------------------------------------------------------

  function tileName(path) {
    const t = S.full && (S.full.tiles || []).find((x) => x.tile === path);
    return (t && t.name) || path;
  }
  function onContext(c) {
    // (the tile's name is read when it is drawn and sent: the chrome may say this before xbind's view of the tiles is here)
    S.context = c.none ? null : c.tile ? { tile: c.tile } : { screen: c.screen };
    paint();
    hook('context');
  }
  function dropContext() { S.context = null; paint(); }
  /** contextNow(): the chip as it is drawn — null, {screen}, or {tile, name, not, reason} (not: the session isn't connected to it). */
  function contextNow() {
    const c = S.context;
    if (!c) return null;
    if (!c.tile) return { screen: c.screen };
    const t = S.full ? (S.full.tiles || []).find((x) => x.tile === c.tile) : null;
    const not = !!(t && t.state !== 'connected');
    return { tile: c.tile, name: (t && t.name) || c.name || tileName(c.tile), not, reason: not ? t.reason || '' : '' };
  }

  // ---- requests and briefs ---------------------------------------------------------------------------

  /** review(q): ask the chrome to open a request (or a change set) by xbind's id; the chrome draws the grant. */
  function review(q) {
    const ok = post(q.kind === 'changeset' ? dock.openChangeset(q.ref) : dock.openRequest(q.ref));
    announce('xbin shows the request to review.');
    return ok;
  }
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
      const q = ((S.cards && S.cards.requests) || []).find((x) => x.brief === b.id && x.state === 'pending' && reviewable(x));
      if (q) { review(q); return ''; }
    }
    const r = await ws.startBrief(S.sel, b.id);
    if (!r.ok) return errOf(r, 'It could not be started.');
    if (r.data.request) post(dock.openRequest(r.data.request));
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
  // a held session (GET /ws/sessions → held) moves back to the model it was on
  async function moveHeld(model) {
    const s = current();
    if (!s) return;
    const r = await ws.patch(s.id, { model });
    S.heldErr = r.ok ? '' : errOf(r, 'The session could not move.');
    await loadSessions();
    paint();
  }
  // (from: 'held' — the held banner's offer, whose error the banner shows; else the scope panel's)
  async function newSessionOn(model, from = 'scope') {
    const r = await ws.newSession(S.sel, { model });
    if (!r.ok) { const err = errOf(r, 'A new session could not be started.'); if (from === 'held') S.heldErr = err; else S.scope.err = err; paint(); return; }
    S.heldErr = '';
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
  /** scope: the panel's own moves (staged until Apply); a view adds its keys and its way of closing. */
  const scope = {
    filter: (k) => { S.scope.filter = k; paint(); },
    selectAll: () => { S.scope.ticks = SM.allAllowed(S.full); paint(); },
    expand: (id) => { const x = S.scope.expanded; if (x.has(id)) x.delete(id); else x.add(id); S.scope.focus = 'o:' + id; paint(); },
    tickOrg: (id) => { S.scope.ticks = SM.toggleOrg(S.scope.ticks, S.full, id); S.scope.focus = 'o:' + id; paint(); },
    tickTile: (t) => { S.scope.ticks = SM.toggleTile(S.scope.ticks, S.full, t); S.scope.focus = 't:' + t; paint(); },
    models: () => { S.scope.view = 'models'; paint(); },
    back: () => { Object.assign(S.scope, { view: 'tree', pick: '', what: null }); paint(); },
    pick: (id) => pickModel(id),
    switchModel: () => switchModel(),
    newOn: (m) => newSessionOn(m),
    apply: () => applyScope(),
  };
  /** counts(): the chip's numbers and xbin's sentence for the session in view. */
  function counts() {
    const s = current();
    const conn = (S.conn && S.conn.connection) || (s && s.connection) || {};
    return { ...(conn.counts || {}), summary: (S.conn && S.conn.summary) || (s && s.summary) || '' };
  }
  const isPrivate = () => !!(S.full && S.full.model && S.full.model.private);

  // ---- sessions -----------------------------------------------------------------------------------

  const sessions = {
    pick: (id) => { S.pop = ''; if (id !== S.sel) select(id); else paint(); },
    create: async () => {
      const r = await ws.newSession(S.sel);
      if (!r.ok) { S.ses.err = errOf(r, 'A new session could not be started.'); paint(); return; }
      S.sessions = [...S.sessions, r.data.session];
      await select(r.data.session.id);
    },
    renaming: (id) => { S.ses.renaming = id; paint(); hook('renaming', id); },
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
  const notebook = {
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
      await notebook.read(slug);
    },
    drop: async () => {
      const r = await ws.dropPage(S.nb.open);
      if (!r.ok) { S.nb.err = errOf(r); paint(); return; }
      openNotebook();
    },
  };

  function showWhatCan() { S.whatCan = true; S.pop = ''; S.view = 'chat'; if (!S.empty) loadEmpty(); paint(); hook('whatCan'); }

  // ---- the page's states --------------------------------------------------------------------------------

  /** banners(): what stops the agent, each a glyph, its words and what still works: [{key, tone, icon, bold, text, more?, button?: {label, act}, buttons?: [{key, label, act}]}]. */
  function banners() {
    const out = [];
    const d = delegation();
    if (d.workspace === false) out.push({ key: 'off', tone: 'bad', icon: 'power', bold: 'Turned off for this workspace.', text: 'An admin turned workspace agents off. Nothing an agent started is running on your behalf. Your conversations and notebook are kept for when it is turned on again.' });
    if (d.paused) out.push({ key: 'paused', tone: 'warn', icon: 'pause', bold: `Paused by you${d.pausedAt ? ` at ${clock(Date.parse(d.pausedAt))}` : ''}.`, text: 'Your agent can’t read or change anything in the workspace until you resume. Tile agents that are already running keep going; their questions still come to you.' });
    const q = quotaOf(S.state);
    if (q) out.push({ key: 'quota', tone: 'warn', icon: 'clock', bold: 'Today’s limit is used up.', text: q.words, more: q.raise });
    const held = current() && current().held;
    if (held) { // it can't go on on its model: why, and the ways forward (the backend refuses every call meanwhile)
      const h = SM.heldWords(held);
      out.push({ key: 'held', tone: 'bad', icon: 'lock', bold: h.bold, text: h.text, more: S.heldErr || '',
        buttons: [...(h.back ? [{ key: 'back', label: h.back.label, act: () => moveHeld(h.back.model) }] : []), { key: 'new', label: h.fresh.label, act: () => newSessionOn(h.fresh.model, 'held') }] });
    }
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

  /** needs(): how many things wait for the person in this agent — requests and change sets to review, tile agents that ask, the agent's own question. */
  function needs() {
    const c = S.cards || {};
    const reqs = (c.requests || []).filter((q) => q.state === 'pending' || q.state === 'proposed').length;
    const agents = (c.agents || []).filter((a) => a.waits && ['starting', 'running', 'idle'].includes(a.status)).length;
    return reqs + agents + (layout.live && layout.live.ask ? 1 : 0);
  }

  /** working(): tile agents at work now (the activity line counts them). */
  const working = () => ((S.cards && S.cards.agents) || []).filter((a) => a.status === 'running' || a.status === 'starting').length;

  /**
   * wire(): what keeps the page current without a person — xbind's own
   * events (a request decided, a change set, the switches: read again what
   * they name) and the tick that moves running times and turns a dropped
   * connection into "can't reach". Returns the function that stops both.
   */
  function wire() {
    let off = null;
    try { off = globalThis.xbin?.events?.on((ev) => { if (ev && ev.type === 'delegate') { const op = ev.data && ev.data.op; if (op === 'state') soon('state'); else { soon('cards'); soon('sessions'); } } }); } catch { /* no events here */ }
    const t = setInterval(() => { if (offline() || session.conn !== 'live' || ((S.cards && S.cards.agents) || []).some((a) => a.status === 'running')) paint(); }, CHOICES.tick);
    return () => { clearInterval(t); if (typeof off === 'function') off(); };
  }

  return {
    S, session, bag, soon, get layout() { return layout; }, place,
    loadState, loadSessions, loadCards, loadConnection, loadEmpty, current, select, boot, announce,
    offline, paused, delegation, fill, send, stop, takeBack, composerNow, hint, hasUserMessage, replies, reply,
    tileName, onContext, dropContext, contextNow, review, chrome: (msg) => post(msg), startDefaults, briefSave, briefStart,
    openScope, models, scope, counts, isPrivate, sessions, openNotebook, notebook, showWhatCan,
    banners, needs, working, wire, busy: () => { const v = session.current(); return !!(v && busy(v.run.status)); },
  };
}
