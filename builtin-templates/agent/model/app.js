// model/app.js — the agent tile's model in one object, and the one place its
// parts are wired together: the open conversation (Session), your
// conversation list (ConvList), the Automations page (AutoPage), who you are
// (GET /me), the tool mode for new asks, what needs you, the halt switch, the
// composer's attachments and sending, and where you are — home, a
// conversation, the Automations page — all kept current by the one live
// stream. Both views drive it: agent.js on the web (lit) and a native view;
// neither keeps model state of its own. No lit, no DOM, no dialogs.
//
//   const app = createApp({ route: (hash) => …, visible: () => … });
//   app.on('*', () => paint());   // or the finer events below
//   app.start(); app.follow(address);
//
// Events (app.on(type, fn) → unsubscribe; '*' hears every one as fn(type, …args)):
//   change    the open conversation changed (batched per frame)
//   runs      the run list changed          list    the conversation list changed
//   autos     the Automations page changed  needs   GET /needs landed
//   me        GET /me landed                halt    the halt switch changed
//   toolset   the tool mode changed         attach  an attachment chip changed
//   sending   a send started or settled (app.sending)
//   select(id)   a conversation is being opened (before it loads)
//   selected(id) …and is open
//   home      back home (no conversation, no page)
//   page      the Automations page opened
//   error(e)  something the person should hear about failed
import { Session } from './session.js';
import { ConvList } from './conv-list.js';
import { AutoPage } from './auto.js';
import './auto-channels.js'; // registers the Channels kind
import './auto-triggers.js'; // …and Triggers
import * as actions from './actions.js';
import * as rules from './rules.js';
import * as router from './router.js';
import { HOME } from './home.js';

/**
 * createApp builds the model.
 * @param {object} opts
 *   base       this backend's prefix (default /api/<xbin.self>)
 *   Session    the Session class to use (a view may add its own drawing: chat-view.js)
 *   AutoPage   the AutoPage class to use (automations.js asks before deleting)
 *   route(h)   keep the address: h is 'c=<id>', 'auto…' or '' (home) — the web writes the hash
 *   visible()  is the tile on screen (an unread conversation you look at is read)
 *   frame(fn)  the repaint batcher (default: requestAnimationFrame)
 *   deltas     stream drafts as deltas (the native view; API.md "Deltas")
 *   page       read the open conversation in pages of this many messages (Session.loadOlder)
 */
export function createApp(opts = {}) {
  const base = opts.base || `/api/${globalThis.xbin?.self ?? ''}`;
  const route = opts.route || (() => {});
  const visible = opts.visible || (() => true);
  const listeners = new Map();
  const emit = (type, ...args) => {
    for (const fn of [...(listeners.get(type) || [])]) fn(...args);
    for (const fn of [...(listeners.get('*') || [])]) fn(type, ...args);
  };
  let needsT = null, autosT = null;

  const app = {
    base, actions, rules, router, HOME,
    me: { manager: true }, // an older backend has no /me: everything, as before
    toolset: 'private',    // for NEW asks; a run keeps its own
    needs: [],
    halted: false,
    draft: actions.draftKey(), // where the app uploads what is picked at home (API.md "Attachments")
    sel: null,             // the open conversation (a run id), null = home
    page: null,            // what the main pane shows with no conversation: null (home) | 'automations'
    sending: false,

    on(type, fn) {
      if (!listeners.has(type)) listeners.set(type, new Set());
      listeners.get(type).add(fn);
      return () => listeners.get(type).delete(fn);
    },
    emit,
    fail(e) { emit('error', e); },

    // root: the conversation the open run belongs to (a subagent's is its root's).
    get root() { const c = app.session.current(); return c ? (c.run.rootId || c.run.id) : null; },
    // place: where the composer is — the open run, or 'home' (a new ask).
    get place() { return app.sel == null ? 'home' : app.sel; },

    // uploadTarget is where an app that uploads a picked file itself puts it
    // ({method, path}, {name} its name): into the open run, or at home into
    // the new ask's draft, which Send then sends (a held ask).
    uploadTarget() {
      return { method: 'PUT', path: app.sel == null ? `${base}/ask/upload?draft=${app.draft}&name={name}` : `${base}/runs/${app.sel}/upload?name={name}` };
    },

    // --- start ------------------------------------------------------------

    // start loads what the tile shows first and opens the stream.
    start() {
      app.loadToolset();
      app.session.start().catch(() => {});
      app.loadMe().then(() => app.convs.load()).catch(() => {});
      app.loadHalt();
      app.loadNeeds();
      app.autos.loadSummary();
    },

    async loadMe() {
      try { app.me = await actions.me(); } catch { /* an older backend: everything, as before */ }
      emit('me');
    },
    async loadNeeds() {
      try { app.needs = await actions.needs(); } catch { app.needs = []; }
      emit('needs');
    },
    async loadHalt() {
      try { app.halted = !!(await actions.getHalt()).on; emit('halt'); } catch { /* ignore */ }
    },
    // setHalt: one call, no confirm — during a runaway every dialog is another
    // second of spend. The undo is the same switch. Throws on failure.
    async setHalt(on) {
      await actions.setHalt(on);
      app.halted = on;
      emit('halt');
    },
    async loadToolset() {
      try { if ((await actions.loadToolset()) === 'web') { app.toolset = 'web'; emit('toolset'); } } catch { /* keep default */ }
    },
    toggleToolset() {
      app.toolset = app.toolset === 'private' ? 'web' : 'private';
      actions.saveToolset(app.toolset).catch(() => {});
      emit('toolset');
    },

    // --- where you are -------------------------------------------------------------

    async select(id) {
      if (id == null) return app.home();
      app.sel = +id;
      app.page = null;
      emit('select', app.sel);
      try { await app.session.select(app.sel); } catch (e) { app.fail(e); return app.home(); }
      route(router.convHash(app.sel));
      const root = app.root;
      if (root != null) app.convs.read(root);
      emit('selected', app.sel);
    },

    home() {
      app.page = null;
      app.sel = null;
      app.session.select(null);
      route('');
      app.loadNeeds();
      emit('home');
    },

    // openAutomations shows the Automations page (one automation's, with kind/id).
    async openAutomations(kind, id) {
      app.home();
      app.page = 'automations';
      route(router.autoHash(kind, id));
      await app.autos.load();
      if (kind) await app.autos.show(kind, +id); else app.autos.show(null);
      emit('page');
    },

    // follow goes where an address says (a hash, a deep link's fragment).
    follow(address) {
      const to = router.parse(address);
      if (to.conv != null && to.conv !== app.sel) return app.select(to.conv);
      if (to.join) return app.join(to.join);
      if (to.auto && app.page !== 'automations') return app.openAutomations(to.auto.kind, to.auto.id);
      return Promise.resolve();
    },

    // join redeems an invite link (#join=… — an address, or pasted text).
    async join(text) {
      try {
        const r = await actions.joinFrom(text);
        if (r) { await app.convs.load(); app.select(r.runId); }
      } catch (e) { app.fail(e); }
    },

    // --- talking ----------------------------------------------------------------------

    // ask starts a conversation from the "new chat with options" form
    // ({text, title, system, toolset}) and opens it. Throws on failure.
    async ask(body) {
      const run = await actions.ask(body);
      app.session.runs.set(run.id, run);
      await app.select(run.id);
      return run;
    },

    // send is the composer's Send. At home it starts a conversation (with
    // attachments: created held, uploaded into, then sent — or, when the app
    // already uploaded them into the draft, the draft is sent); in a
    // conversation it is a message — queued while the run works, delivered
    // at its next step. clear() empties the view's text box once the text is
    // on its way. Only the chips of where you are go (Attachments.here).
    async send(text, clear = () => {}) {
      if (app.sending) return;
      const t = String(text ?? '').trim();
      const att = app.attach;
      const place = app.place;
      const items = att.here(place);
      if (!t && !items.length) return;
      if (att.tooBig(place)) return app.fail(new Error('Remove the files that are too large first.'));
      app.sending = true;
      emit('sending');
      try {
        if (app.sel == null && items.some((a) => a.at === 'home')) {
          // the app uploaded them into the draft already: send it
          let run;
          try {
            run = await actions.ask({ text: t, toolset: app.toolset, draft: app.draft, files: items.map((a) => a.path) });
          } catch (e) {
            // the draft is gone (sent from elsewhere, or expired): those chips can't go
            if (/attach them again/.test(e.message)) { att.clear('home'); app.draft = actions.draftKey(); emit('attach'); }
            throw e;
          }
          clear(); att.clear('home');
          app.draft = actions.draftKey();
          app.session.runs.set(run.id, run);
          await app.select(run.id);
          return;
        }
        if (app.sel == null) {
          if (!items.length) {
            clear();
            const run = await actions.ask({ text: t, toolset: app.toolset });
            app.session.runs.set(run.id, run);
            await app.select(run.id);
            return;
          }
          // With attachments there is no run to upload into yet: create it held
          // (no message, no drive), upload, then send the message into it.
          const title = t || items.map((a) => a.name).join(', ');
          const run = await actions.ask({ text: title, toolset: app.toolset, hold: true });
          try {
            const files = await att.upload(base, run.id, place);
            await actions.message(run.id, { text: t, files });
          } catch (e) {
            // Don't leave an empty run behind; its uploads go with it, so the
            // chips must upload again next time.
            await actions.deleteRun(run.id).catch(() => {});
            att.unupload();
            throw e;
          }
          clear(); att.clear(place);
          app.session.runs.set(run.id, run);
          await app.select(run.id);
          return;
        }
        const files = items.length ? await att.upload(base, app.sel, place) : undefined;
        await app.session.send(t, files);
        clear(); att.clear(place);
      } catch (e) {
        app.fail(e);
      } finally {
        app.sending = false;
        emit('sending');
      }
    },

    // stop interrupts the open run; what was still queued comes back as text
    // for the composer. Throws on failure.
    async stop() { return actions.returnedText(await app.session.stop()); },

    // --- the stream ----------------------------------------------------------------------

    // event sees every stream event: the list keeps itself current, and the
    // conversation you are looking at stays read.
    event(ev) {
      app.convs.apply(ev);
      if (ev.type === 'revoked' && ev.run === app.root) {
        app.home();
        globalThis.xbin?.notify?.('info', 'That conversation is no longer shared with you.');
      }
      if (ev.type === 'run' && ev.run === ev.root) {
        const r = app.convs.find(ev.run);
        if (r && r.unread && ev.run === app.root && visible()) app.convs.read(ev.run);
        if (app.sel == null) { clearTimeout(needsT); needsT = setTimeout(() => app.loadNeeds(), 300); }
      }
      if (ev.type === 'automation' || (ev.type === 'run' && ev.run === ev.root && ['schedule', 'watcher', 'channel', 'trigger'].includes((ev.data || {}).origin))) {
        clearTimeout(autosT);
        autosT = setTimeout(() => (app.page ? app.autos.load() : app.autos.loadSummary()), 300);
      }
    },
  };

  const S = opts.Session || Session;
  const A = opts.AutoPage || AutoPage;
  app.session = new S(base, {
    change: () => emit('change'),
    runs: () => emit('runs'),
    gone: () => app.home(),
    event: (ev) => app.event(ev),
    reset: () => { app.convs.load().catch(() => {}); app.loadNeeds(); },
    frame: opts.frame,
  }, { deltas: opts.deltas, page: opts.page });
  app.convs = new ConvList({ change: () => emit('list'), epoch: () => app.me.epochMs || 0 });
  // The Automations page; its route() keeps the address of what is open there.
  app.autos = new A({
    change: () => emit('autos'),
    select: (id) => app.select(id),
    me: () => app.me,
    route: (kind, id) => { if (app.page === 'automations') route(router.autoHash(kind, id)); },
  });
  app.attach = new actions.Attachments({ change: () => emit('attach') });
  app.session.ui.act.select = (id) => app.select(id);
  app.session.ui.me = () => app.me.user;
  return app;
}
