// model/app.js — the page's state and calls, for both views (the web's
// web.js, the native view's native.js): who you are (GET /me), the
// operators' state (GET /ops/state) and what they do, and your own
// sandboxes — the page as a consumer of its own (/sbx/*): the list, create,
// lifecycle, sharing, files and terminals. No lit, no DOM, no dialogs: a
// view asks "are you sure?" itself, then calls these. Every change emits
// 'change'; a call that is refused throws (e.message says why, e.status and
// e.refusal the rest).
import * as O from './ops.js';
import * as M from './mine.js';
import * as F from './format.js';

// createApp: opts.fetch (default xbin.fetch — the frame token: you, verified,
// to this tile's backend) and opts.self (default xbin.self).
export function createApp(opts = {}) {
  const x = globalThis.xbin;
  const self = opts.self || (x && x.self) || '';
  const base = `/api/${self}`;
  const doFetch = opts.fetch || ((u, o) => (x && x.fetch ? x.fetch(u, o) : fetch(u, o)));
  const listeners = new Set();
  const emit = () => { for (const fn of [...listeners]) fn(); };

  // request: one call to this tile's backend; the parsed JSON (text, null
  // when empty), or a thrown refusal.
  async function request(path, { method = 'GET', body, raw = false, blob = false } = {}) {
    const o = { method };
    if (body !== undefined) {
      if (raw) o.body = body;
      else { o.body = JSON.stringify(body); o.headers = { 'Content-Type': 'application/json' }; }
    }
    const r = await doFetch(base + path, o);
    if (r.ok && blob) return r.blob();
    const text = await r.text();
    let data;
    try { data = text ? JSON.parse(text) : null; } catch { data = text; }
    if (!r.ok) {
      const e = new Error((data && typeof data === 'object' && data.error) || `error ${r.status}`);
      e.status = r.status;
      if (data && typeof data === 'object' && data.refusal) e.refusal = data.refusal;
      throw e;
    }
    return data;
  }
  const q = (v) => encodeURIComponent(v);
  const sbx = (id, tail = '') => `/sbx/sandboxes/${q(id)}${tail}`;

  const app = {
    self, request,
    me: null,        // GET /me: {user, operator, self}
    ops: null,       // GET /ops/state (operators)
    opsErr: '',
    hello: null,     // GET /sbx/hello — what you may make here
    helloErr: '',
    mine: null,      // GET /sbx/sandboxes — the sandboxes you may use (this tile's partition)
    mineErr: '',
    err: '',         // /me failed: the page can't say who you are
    snaps: null,     // an operator's look at one sandbox's snapshots: {id, list, err}
    files: null,     // the file browser: {id, path, listing, err, file}
    loaded: false,

    on(fn) { listeners.add(fn); return () => listeners.delete(fn); },
    emit,

    get operator() { return !!(app.me && app.me.operator); },

    // --- reading --------------------------------------------------------------------

    // load reads who you are, the operators' state (when you are one), what
    // the manager offers and your sandboxes; each part fails on its own.
    async load() {
      try { app.me = await request('/me'); app.err = ''; } catch (e) { app.err = e.message; app.loaded = true; emit(); return; }
      const parts = [
        request('/sbx/hello?protocol=1').then((h) => { app.hello = h; app.helloErr = ''; }, (e) => { app.helloErr = e.message; }),
        request('/sbx/sandboxes').then((l) => { app.mine = l.sandboxes || []; app.mineErr = ''; }, (e) => { app.mineErr = e.message; }),
      ];
      if (app.operator) parts.push(request('/ops/state').then((s) => { app.ops = s; app.opsErr = ''; }, (e) => { app.opsErr = e.message; }));
      await Promise.all(parts);
      app.loaded = true;
      emit();
    },

    // What the views draw (model/ops.js, model/mine.js).
    opRows(now) { return O.sandboxRows(app.ops, now); },
    usage() { return O.usageRows(app.ops); },
    backend() { return O.backendInfo(app.ops); },
    mode() { return O.modeInfo(app.ops); },
    images() { return O.imageRows(app.ops); },
    quotas() { return O.quotaRows(app.ops && app.ops.config); },
    myRows(now) { return M.myRows(app.mine, app.me, now); },
    mySandbox(id) { return (app.mine || []).find((s) => s.id === id) || null; },
    opSandbox(id) { return ((app.ops && app.ops.sandboxes) || []).find((s) => s.id === id) || null; },
    createForm(form) { return M.createForm(app.hello, form); },

    // --- the operators ----------------------------------------------------------------

    // opAct: start | stop | delete any consumer's sandbox.
    async opAct(id, act) {
      if (act === 'delete') await request(`/ops/sandboxes/${q(id)}`, { method: 'DELETE' });
      else await request(`/ops/sandboxes/${q(id)}/${act}?wait=30`, { method: 'POST' });
      await app.load();
    },
    // opShares: a sandbox's shares, set whole.
    async opShares(id, shares) {
      await request(`/ops/sandboxes/${q(id)}`, { method: 'PATCH', body: { shares } });
      await app.load();
    },
    // snapshots of one sandbox (opSnapshots opens the look, the rest act in it).
    async opSnapshots(id) {
      app.snaps = { id, list: null, err: '' };
      emit();
      try { app.snaps.list = (await request(`/ops/sandboxes/${q(id)}/snapshots`)).snapshots || []; } catch (e) { app.snaps.err = e.message; }
      emit();
    },
    async opSnapshot(id, name) {
      await request(`/ops/sandboxes/${q(id)}/snapshots`, { method: 'POST', body: { name: String(name || '').trim() || `by hand ${new Date().toISOString().slice(0, 16).replace('T', ' ')}` } });
      await app.opSnapshots(id);
    },
    async opRestore(id, sid) {
      await request(`/ops/sandboxes/${q(id)}/snapshots/${q(sid)}/restore`, { method: 'POST' });
      await Promise.all([app.opSnapshots(id), app.load()]);
    },
    async opSnapDelete(id, sid) {
      await request(`/ops/sandboxes/${q(id)}/snapshots/${q(sid)}`, { method: 'DELETE' });
      await app.opSnapshots(id);
    },
    closeSnapshots() { app.snaps = null; emit(); },

    // saveConfig: PUT /ops/config — the top-level fields given replace the
    // stored ones; the answer is the new state.
    async saveConfig(fields) {
      app.ops = await request('/ops/config', { method: 'PUT', body: fields });
      emit();
      app.load().catch(() => {});
    },
    async build(imageId) {
      await request(`/ops/images/${q(imageId)}/build`, { method: 'POST' });
      await app.load();
    },
    async dropOrphan(name) {
      await request(`/ops/orphans/${q(name)}`, { method: 'DELETE' });
      await app.load();
    },

    // --- your own sandboxes ---------------------------------------------------------------

    // create makes one (the form: model/mine.js createForm) and answers it.
    async create(f) {
      const s = await request('/sbx/sandboxes?wait=60', { method: 'POST', body: M.createBody(f) });
      await app.load();
      return s;
    },
    // act: start | stop | delete one of yours.
    async act(id, act) {
      if (act === 'delete') {
        await request(sbx(id), { method: 'DELETE' });
        if (app.files && app.files.id === id) app.files = null;
      } else await request(sbx(id, `/${act}?wait=30`), { method: 'POST' });
      await app.load();
    },
    // setShares and setVisibility change who may use one of yours.
    async setShares(id, shares) {
      await request(sbx(id), { method: 'PATCH', body: { shares } });
      await app.load();
    },
    async setVisibility(id, visibility) {
      await request(sbx(id), { method: 'PATCH', body: { visibility } });
      await app.load();
    },

    // --- files ------------------------------------------------------------------------

    // browse lists directory path of sandbox id (a stopped one starts for it).
    async browse(id, path) {
      const p = F.cleanPath(path, (app.mySandbox(id) || {}).workdir || '/');
      // asked: the path this read is for (the listing's own may be spelled otherwise)
      app.files = { id, path: p, asked: p, listing: app.files && app.files.id === id ? app.files.listing : null, err: '', file: null, busy: true };
      emit();
      try {
        app.files.listing = await request(sbx(id, `/files/list?path=${q(p)}`));
        app.files.path = app.files.listing.path || p;
      } catch (e) { app.files.err = e.message; }
      app.files.busy = false;
      emit();
    },
    // readFile opens a file in the viewer: its first VIEW_MAX bytes as text
    // (binary files say so; download them).
    async readFile(id, path) {
      if (!app.files || app.files.id !== id) app.files = { id, path: F.parentPath(path), listing: null, err: '', file: null };
      app.files.file = { path, text: '', binary: false, err: '', busy: true };
      emit();
      const file = app.files.file;
      try {
        const st = await request(sbx(id, `/files/stat?path=${q(path)}`));
        file.size = st.size;
        const text = st.size ? await (await request(sbx(id, `/files/content?path=${q(path)}&length=${M.VIEW_MAX}`), { blob: true })).text() : '';
        file.text = text;
        file.binary = F.looksBinary(text);
        file.truncated = st.size > M.VIEW_MAX;
      } catch (e) { file.err = e.message; }
      file.busy = false;
      emit();
    },
    closeFile() { if (app.files) { app.files.file = null; emit(); } },
    closeFiles() { app.files = null; emit(); },
    // fetchFile: a whole file as a Blob (a download).
    fetchFile(id, path) { return request(sbx(id, `/files/content?path=${q(path)}`), { blob: true }); },
    // upload puts data (a Blob, a File or a string) at dir/name.
    async upload(id, dir, name, data) {
      await request(sbx(id, `/files/content?path=${q(F.joinPath(dir, name))}&mkdirs=1`), { method: 'PUT', body: data, raw: true });
      if (app.files && app.files.id === id) await app.browse(id, app.files.path);
    },
    async mkdir(id, path) {
      await request(sbx(id, '/files/mkdir'), { method: 'POST', body: { path, parents: true } });
      if (app.files && app.files.id === id) await app.browse(id, app.files.path);
    },
    async remove(id, path, recursive) {
      await request(sbx(id, '/files/remove'), { method: 'POST', body: { path, recursive: !!recursive } });
      if (app.files && app.files.id === id) await app.browse(id, app.files.path);
    },

    // --- terminals --------------------------------------------------------------------

    // terminalSrc: what the web's <bx-terminal src> dials (a new shell at cwd;
    // it reattaches to the same one itself after a drop).
    terminalSrc(id, cwd) { return M.terminalSrc(self, id, cwd); },
    // startShell starts a login shell as a tty exec (the native view's
    // terminal attaches to it, so a reconnect is the same shell): its src.
    async startShell(id, cwd) {
      const s = app.mySandbox(id) || {};
      const x = await request(sbx(id, '/execs'), { method: 'POST', body: { argv: [s.shell || '/bin/sh', '-l'], cwd: cwd || undefined, tty: true, label: 'terminal' } });
      return { eid: x.id, src: M.attachSrc(id, x.id) };
    },
    // endShell ends a terminal's shell (the web's session id, the native eid).
    async endShell(id, eid) {
      await request(sbx(id, `/execs/${q(eid)}`), { method: 'DELETE' });
    },
  };
  return app;
}
