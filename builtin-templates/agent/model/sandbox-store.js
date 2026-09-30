// model/sandbox-store.js — the coding sandboxes' state and calls (D115) for
// both views, as app.sbx: GET /sandboxes as you may see it (read when a view
// first needs it, again when asked), the next new chat's pick, and what a
// view does — pick or bind a sandbox, change the working directory, detach,
// create one (for the open conversation: bound there), start, stop,
// archive, thaw, share with the team or a terminal tile, delete — and a
// terminal onto one (its manager's `tty`, where a view sets tty: the web's).
// The open conversation's binding lives in its view's config: a `run` event
// that carries `sandbox` updates it at once, and after a change of ours it
// is re-read. What the controls show is model/sandboxes.js. No lit, no DOM, no dialogs: a view
// confirms a delete itself, then calls remove().
//
// Every change emits 'sandboxes' on the app. Calls throw as the backend
// refuses (e.message says why).
import * as actions from './actions.js';
import * as S from './sandboxes.js';
import * as classes from './classes.js';
import { fitsWhy, createPrefill } from './harness-start.js';
import { isHarness, harnessOf, nameOf } from './harness.js';

const cid = () => 's' + Math.random().toString(36).slice(2) + Date.now().toString(36);

export function createSandboxStore(app) {
  let loadedAt = 0;
  let inflight = null;
  const emit = () => app.emit('sandboxes');
  const conv = () => app.session.current();
  const rootOf = (v) => (v ? v.run.rootId || v.run.id : null);
  const find = (ref) => sbx.list.sandboxes.find((s) => s.ref === ref) || null;
  // put takes a sandbox a call answered into the list (replacing it, or first)
  const put = (s) => {
    if (!s || !s.ref) return;
    const l = sbx.list.sandboxes;
    sbx.list = { ...sbx.list, sandboxes: find(s.ref) ? l.map((x) => (x.ref === s.ref ? { ...x, ...s } : x)) : [s, ...l] };
  };

  // recheck: a binding of v's that the list read before doesn't have — one
  // the agent just made (sandbox_create), or bound elsewhere since — is read
  // again (fresh, once per ref) rather than shown as gone.
  const checked = new Set();
  const recheck = (v) => {
    if (!v || !loadedAt || inflight) return;
    const missing = [S.bindingOf(v), ...S.attachedOf(v)].filter((b) => b && !find(b.ref) && !checked.has(b.ref));
    if (!missing.length) return;
    missing.forEach((b) => checked.add(b.ref));
    sbx.load(true).catch(() => {});
  };

  // patchShares PATCHes ref's whole shares list as bodyOf(sandbox) computes
  // it from the sandbox as the list has it — with its version, so a change
  // made since (someone else's share) isn't overwritten: on a 412 the
  // sandbox is read again, the body computed afresh from it, and sent once
  // more. The answer lands in the list.
  const patchShares = async (ref, bodyOf) => {
    let s = find(ref);
    for (let tries = 0; ; tries++) {
      try {
        put(await actions.patchSandbox(ref, bodyOf(s)));
        return;
      } catch (e) {
        if (e.status !== 412 || tries) throw e;
      }
      s = await actions.getSandbox(ref);
      put(s);
      s = find(ref);
    }
  };

  const sbx = {
    list: S.listOf(null), // GET /sandboxes (model/sandboxes.js listOf); loaded once read
    pick: null,           // the next new chat's sandbox: {ref, cwd, name} (sent while its class has the sandbox toolset)
    error: '',            // why the list could not be read
    // the page's endpoints for its `sandboxes` slot (xbin.iface) — set by a
    // view that opens terminals (the web's); S.RELAY: through the tile's own
    // relay (the native view: the app's terminal dials only the tile's own
    // routes, D-harness §4.2.8); null: none offered
    tty: null,

    // cls: the class the picker works for — the open conversation's, else the next new chat's.
    cls() { const v = conv(); return v ? v.class || null : classes.find(app.classes, app.newClassId()); },
    // coding: at home, the coding agent answering new chats (D-harness): the
    // picker keeps sandboxes it fits, and New sandbox starts as one it fits
    coding() { return conv() ? null : app.harness.picked(); },

    // load reads the list (fresh: past the backend's 15 s cache); one read at a time.
    load(fresh = false) {
      if (inflight) return inflight;
      inflight = actions.sandboxes(fresh)
        .then((r) => { sbx.list = S.listOf(r); sbx.error = ''; }, (e) => { sbx.error = e.message; })
        .finally(() => { inflight = null; loadedAt = Date.now(); emit(); });
      return inflight;
    },
    // ensure reads it once a view needs it; refresh again when it is older than 15 s.
    ensure() { if (!loadedAt && !inflight) sbx.load().catch(() => {}); },
    refresh() { if (Date.now() - loadedAt > 15e3) sbx.load().catch(() => {}); },

    // What the views draw (model/sandboxes.js), for where you are. rows:
    // order — the refs as the view shows them (kept while its list is open).
    picker() {
      const h = sbx.coding(), v = conv();
      const p = S.sandboxPicker(sbx.list, v, app.me, { cls: classes.find(app.classes, app.newClassId()), pick: sbx.pick, fits: h ? fitsWhy(h) : null });
      // a coding agent's conversation keeps the sandbox it started in (D-harness §2.2): no picker
      return v && isHarness(v.run) ? { ...p, shown: false } : p;
    },
    // a coding agent's conversation keeps its sandbox and cwd (D-harness §2.2): the badge offers no change
    badge(v = conv()) { recheck(v); return S.sandboxBadge(v, sbx.list, undefined, { fixed: v && isHarness(v.run) ? nameOf(harnessOf(v)) : '' }); },
    rows(order) { const v = conv(); return S.sandboxRows(sbx.list, app.me, { conv: v, cls: sbx.cls(), pick: sbx.pick, order, tty: sbx.tty }); },
    // terminal: "Open terminal" for ref at cwd, running cmd ('' = the login
    // shell) (model/sandboxes.js terminal): {shown, why, src, …} — src is
    // what <bx-terminal src> (or the app's terminal, through the relay) dials.
    terminal(ref, cwd = '', cmd = '') { return S.terminal(sbx.list, ref, sbx.tty, cwd, cmd); },
    // endTerminal ends the shell a terminal t (terminal()) started — its
    // session frame named the exec eid; a view calls it when it closes the
    // terminal. The sandbox's lastActive moved: the list is read again.
    async endTerminal(t, eid) {
      if (!t || !t.base || !eid) return;
      await actions.endManagerExec(S.execSrc({ url: t.base }, t.id, eid));
      sbx.refresh();
    },
    // createWhy: why New sandbox can't be offered here ('' = it can).
    createWhy() { return S.createWhy(sbx.list, conv()); },
    // confirmBind: what a view confirms before choose(ref) ('' = nothing) —
    // a private sandbox into a conversation other people are in.
    confirmBind(ref) { return S.bindConfirm(sbx.list, conv(), ref); },
    // form: the create form for f so far — for the open conversation's class
    // (a team conversation's sandbox is a team one), or the next new chat's.
    form(f = {}) {
      const v = conv();
      const h = sbx.coding();
      const pre = h && !f.provider ? createPrefill(h, sbx.list, sbx.cls()) : null;
      return S.createForm(sbx.list.managers, sbx.cls(), pre ? { ...pre, ...f } : f, { team: !!(v && v.run.visibility === 'team') });
    },

    // askPart: what a new ask in class id carries — the pick, while that class has the sandbox toolset.
    askPart(id = app.newClassId()) {
      const p = sbx.pick;
      return p && S.hasSandbox(classes.find(app.classes, id)) ? { sandbox: { ref: p.ref, ...(p.cwd ? { cwd: p.cwd } : {}) } } : {};
    },

    // choose is the picker: in an open conversation it binds ref from the next
    // turn ('' = no active sandbox; the attached stay) — one it has attached
    // keeps the working directory it had there, unless cwd names another; at
    // home it is the next new chat's.
    async choose(ref, cwd = '') {
      const v = conv();
      if (!v) {
        const s = find(ref);
        sbx.pick = ref ? { ref, cwd, name: s ? s.name : S.splitRef(ref).id } : null;
        emit();
        return;
      }
      const dir = cwd || (ref ? (S.attachedOf(v).find((a) => a.ref === ref) || {}).cwd || '' : '');
      await sbx.bind(ref ? { ref, ...(dir ? { cwd: dir } : {}) } : null);
    },
    // bind: {ref, cwd?} | null on the open conversation (its root).
    async bind(pick) {
      const id = rootOf(conv());
      if (id == null) return;
      await actions.setRunSandbox(id, { sandbox: pick });
      await sbx.reread(id);
    },
    // setCwd: the active sandbox's working directory ('' = its workdir: named
    // when the list knows it, as a rebind that names none keeps the one it had).
    async setCwd(cwd) {
      const b = S.bindingOf(conv());
      if (!b) return;
      const why = S.cwdCheck(cwd);
      if (why) throw new Error(why);
      const dir = String(cwd || '').trim() || (find(b.ref) || {}).workdir || '';
      await sbx.bind({ ref: b.ref, cwd: dir });
    },
    // detach takes one off the open conversation (the active one too).
    async detach(ref) {
      const id = rootOf(conv());
      if (id == null) return;
      await actions.setRunSandbox(id, { detach: ref });
      await sbx.reread(id);
    },

    // create makes one from the create form's values: for the open
    // conversation (bound there unless bind is false), or at home as the next
    // new chat's pick (unless bind is false).
    async create(f, { bind = true } = {}) {
      const v = conv();
      const why = S.createWhy(null, v);
      if (why) throw new Error(S.sentence(why));
      const body = S.createBody(f, { conversation: v ? rootOf(v) : undefined, bind, clientId: cid() });
      const s = await actions.createSandbox(body);
      put(s);
      if (v && bind) await sbx.reread(rootOf(v));
      else if (!v && bind) sbx.pick = { ref: s.ref, cwd: body.cwd || '', name: s.name };
      emit();
      return s;
    },
    // act: start | stop | archive | thaw. One the open conversation holds
    // that you may neither use nor manage goes through it (as its binder;
    // model/sandboxes.js viaConv).
    async act(ref, action) {
      const via = action === 'archive' ? undefined : S.viaConv(find(ref), conv());
      const r = await actions.sandboxAction(ref, action, { conversation: via });
      put(r);
      emit();
      return r;
    },
    // share: 'team' | 'private' (its owner).
    async share(ref, visibility) {
      put(await actions.patchSandbox(ref, { visibility }));
      emit();
    },
    // shareForm: "Share with a terminal tile…" for ref (model/sandboxes.js
    // shareForm) — f: what was entered so far ({tile}).
    shareForm(ref, f = {}) { return S.shareForm(find(ref), app.me, f, (globalThis.xbin && globalThis.xbin.self) || ''); },
    // shareTerminal shares ref with the terminal tile f.tile (its owner; the
    // contract's PATCH {shares}), and says so.
    async shareTerminal(ref, f = {}) {
      let vm = null;
      await patchShares(ref, (s) => {
        vm = S.shareForm(s, app.me, f, (globalThis.xbin && globalThis.xbin.self) || '');
        if (!vm.ok) throw new Error(S.sentence(vm.error));
        return vm.body;
      });
      emit();
      const s = find(ref);
      return `${(s && s.name) || S.splitRef(ref).id} is shared with ${vm.tile} — ${vm.users === '*' ? 'everyone who may use it' : 'you'} can open terminals onto it there ✓`;
    },
    // unshare takes consumer's share of ref away.
    async unshare(ref, consumer) {
      await patchShares(ref, (s) => S.unshareBody(s, consumer));
      emit();
    },
    // perform: a Sandboxes row's action (model/sandboxes.js sandboxRows'
    // actions[].id) as both views' lists do it — a view confirms first where
    // the action says so — and what to say once it is done ('' = the row
    // says it).
    async perform(ref, id, name = S.splitRef(ref).id) {
      if (id === 'use') {
        await sbx.choose(ref);
        return conv() ? `${name} is this conversation's sandbox from its next turn ✓` : `${name} is your next new chat's sandbox ✓`;
      }
      if (id === 'delete') { await sbx.remove(ref); return `deleted ${name}`; }
      if (id === 'team' || id === 'private') { await sbx.share(ref, id); return ''; }
      await sbx.act(ref, id);
      return '';
    },
    // created: what to say once create() made s (bind: as it was asked).
    created(s, bind) {
      return `created ${s.name}${bind ? (conv() ? ' — this conversation works in it from its next turn' : ' — your next new chat starts in it') : ''} ✓`;
    },
    // remove deletes it; every conversation that had it loses it.
    async remove(ref) {
      await actions.deleteSandbox(ref);
      sbx.list = { ...sbx.list, sandboxes: sbx.list.sandboxes.filter((s) => s.ref !== ref) };
      if (sbx.pick && sbx.pick.ref === ref) sbx.pick = null;
      const v = conv();
      if (v && S.attachedOf(v).some((b) => b.ref === ref)) await sbx.reread(rootOf(v)).catch(() => {});
      emit();
    },

    // refused: an ask that named the next new chat's pick failed (e): when
    // it was refused for that sandbox (model/sandboxes.js askRefusal) the
    // pick is dropped — the next ask goes without one — and e says so.
    // True when it was.
    refused(e) {
      const why = S.askRefusal(e);
      if (!why || !sbx.pick) return false;
      const name = sbx.pick.name || S.splitRef(sbx.pick.ref).id;
      sbx.pick = null;
      e.message = `The sandbox ${name} can't be used: ${why}. Your next new chat starts without one — pick another, or send again.`;
      emit();
      return true;
    },
    // classesChanged: the classes were saved or read afresh — the open
    // conversation's class (its badge, the mixed warning, what its sandboxes
    // may be) is read again, as the backend applies an edit from its next step.
    async classesChanged() {
      const ids = new Set([app.sel, app.root].filter((id) => id != null && app.session.views.has(id)));
      await Promise.all([...ids].map((id) => sbx.reread(id).catch(() => {})));
    },

    // reread takes a conversation's stored config (and class) afresh.
    async reread(id) {
      const r = await actions.runConfig(id);
      const sv = app.session.views.get(id);
      if (sv) { sv.config = r.config; if (r.class) sv.class = r.class; }
      app.session.changed();
      emit();
    },
    // fromEvent: a `run` event that carries its binding ({sandbox: {ref, name,
    // cwd, egress, manager} | null, attached: <count>}) updates a held view at
    // once; the attached list is re-read when its count says it changed.
    fromEvent(ev) {
      const d = ev.data || {};
      if (ev.type !== 'run' || !('sandbox' in d)) return;
      const sv = app.session.views.get(ev.run);
      if (!sv) return;
      const cfg = sv.config || (sv.config = {});
      const old = cfg.sandbox && d.sandbox && cfg.sandbox.ref === d.sandbox.ref ? cfg.sandbox : {};
      if (d.sandbox && d.sandbox.ref) cfg.sandbox = { ...old, ...d.sandbox };
      else delete cfg.sandbox;
      const known = (cfg.attached || []).filter((a) => a && a.ref);
      const stale = (typeof d.attached === 'number' && d.attached !== S.attachedOf(sv).length)
        || (cfg.sandbox && !known.some((a) => a.ref === cfg.sandbox.ref));
      if (stale) sbx.reread(ev.run).catch(() => {});
      emit();
    },
  };
  return sbx;
}
