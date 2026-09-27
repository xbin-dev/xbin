// model/sandbox-store.js — the coding sandboxes' state and calls (D115) for
// both views, as app.sbx: GET /sandboxes as you may see it (read when a view
// first needs it, again when asked), the next new chat's pick, and what a
// view does — pick or bind a sandbox, change the working directory, detach,
// create one (for the open conversation: bound there), start, stop,
// archive, thaw, share with the team, delete. The open conversation's
// binding lives in its view's config: a `run` event that carries `sandbox`
// updates it at once, and after a change of ours it is re-read. What the
// controls show is model/sandboxes.js. No lit, no DOM, no dialogs: a view
// confirms a delete itself, then calls remove().
//
// Every change emits 'sandboxes' on the app. Calls throw as the backend
// refuses (e.message says why).
import * as actions from './actions.js';
import * as S from './sandboxes.js';
import * as classes from './classes.js';

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

  const sbx = {
    list: S.listOf(null), // GET /sandboxes (model/sandboxes.js listOf); loaded once read
    pick: null,           // the next new chat's sandbox: {ref, cwd, name} (sent while its class has the sandbox toolset)
    error: '',            // why the list could not be read

    // cls: the class the picker works for — the open conversation's, else the next new chat's.
    cls() { const v = conv(); return v ? v.class || null : classes.find(app.classes, app.classId); },

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

    // What the views draw (model/sandboxes.js), for where you are.
    picker() { return S.sandboxPicker(sbx.list, conv(), app.me, { cls: classes.find(app.classes, app.classId), pick: sbx.pick }); },
    badge(v = conv()) { return S.sandboxBadge(v, sbx.list); },
    rows() { const v = conv(); return S.sandboxRows(sbx.list, app.me, { conv: v, cls: sbx.cls(), pick: sbx.pick }); },
    // form: the create form for f so far — for the open conversation's class
    // (a team conversation's sandbox is a team one), or the next new chat's.
    form(f = {}) {
      const v = conv();
      return S.createForm(sbx.list.managers, sbx.cls(), f, { team: !!(v && v.run.visibility === 'team') });
    },

    // askPart: what a new ask in class id carries — the pick, while that class has the sandbox toolset.
    askPart(id = app.classId) {
      const p = sbx.pick;
      return p && S.hasSandbox(classes.find(app.classes, id)) ? { sandbox: { ref: p.ref, ...(p.cwd ? { cwd: p.cwd } : {}) } } : {};
    },

    // choose is the picker: in an open conversation it binds ref from the next
    // turn ('' = no active sandbox; the attached stay); at home it is the next
    // new chat's.
    async choose(ref, cwd = '') {
      if (!conv()) {
        const s = find(ref);
        sbx.pick = ref ? { ref, cwd, name: s ? s.name : S.splitRef(ref).id } : null;
        emit();
        return;
      }
      await sbx.bind(ref ? { ref, ...(cwd ? { cwd } : {}) } : null);
    },
    // bind: {ref, cwd?} | null on the open conversation (its root).
    async bind(pick) {
      const id = rootOf(conv());
      if (id == null) return;
      await actions.setRunSandbox(id, { sandbox: pick });
      await sbx.reread(id);
    },
    // setCwd: the active sandbox's working directory ('' = its workdir).
    async setCwd(cwd) {
      const b = S.bindingOf(conv());
      if (!b) return;
      const why = S.cwdCheck(cwd);
      if (why) throw new Error(why);
      await sbx.bind({ ref: b.ref, cwd: String(cwd || '').trim() });
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
      const body = S.createBody(f, { conversation: v ? rootOf(v) : undefined, bind, clientId: cid() });
      const s = await actions.createSandbox(body);
      put(s);
      if (v && bind) await sbx.reread(rootOf(v));
      else if (!v && bind) sbx.pick = { ref: s.ref, cwd: body.cwd || '', name: s.name };
      emit();
      return s;
    },
    // act: start | stop | archive | thaw. One bound to the open conversation
    // that you may neither use nor manage goes through it (as its binder).
    async act(ref, action) {
      const v = conv();
      const s = find(ref);
      const via = v && s && !(s.canUse || s.canManage) && (s.boundTo || []).includes(rootOf(v)) ? rootOf(v) : undefined;
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
    // remove deletes it; every conversation that had it loses it.
    async remove(ref) {
      await actions.deleteSandbox(ref);
      sbx.list = { ...sbx.list, sandboxes: sbx.list.sandboxes.filter((s) => s.ref !== ref) };
      if (sbx.pick && sbx.pick.ref === ref) sbx.pick = null;
      const v = conv();
      if (v && S.attachedOf(v).some((b) => b.ref === ref)) await sbx.reread(rootOf(v)).catch(() => {});
      emit();
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
