// stub.mjs — the coding-sandbox tile's backend, faked in the page: STUB(seed)
// installs a window.xbin whose fetch answers the routes the page uses
// (API.md): GET /me, the operators' /ops/* (state, config, lifecycle,
// sharing, snapshots, image builds, orphans) and the page's own /sbx/*
// (hello, the list, create, lifecycle, sharing, files, execs). As the
// manager does, it refuses the operators' sharing (PATCH /ops/… with
// visibility, members or shares) and every change on /sbx/ from a person
// who may only look (seed.me.write false). It keeps
// what the page changes, records every call in window.__calls and every
// download in window.__downloads; tests add routes with
// window.__route(method, regexp, fn). Self-contained (Playwright's
// addInitScript serializes it; the native tests run it in node).
//
//   seed: {self, me, ops, hello, mine: [sandbox], files: {<path>: {entries} | {content}}, snaps: {<id>: [snapshot]}}
export function STUB(seed) {
  const w = typeof window !== 'undefined' ? window : globalThis;
  const st = JSON.parse(JSON.stringify(seed || {}));
  const self = st.self || 'apps/coding-sandbox';
  const base = `/api/${self}`;
  w.__calls = [];
  w.__downloads = [];
  const json = (v, status = 200) => new Response(v == null ? null : JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
  const fail = (status, error, refusal) => json({ error, refusal }, status);
  const routes = [];
  const route = (method, re, fn) => routes.push([method, re, fn]);
  w.__route = (method, re, fn) => routes.unshift([method, re, fn]);
  w.__json = json;
  let seq = 0;
  const ops = () => st.ops || (st.ops = { config: {}, sandboxes: [], images: [], orphans: [], usage: {} });
  const opBox = (id) => (ops().sandboxes || []).find((s) => s.id === id);
  const myBox = (id) => (st.mine || []).find((s) => s.id === id);
  const q = (u, k) => new URL(u, 'http://t').searchParams.get(k);

  route('GET', /\/me$/, () => json(st.me || { user: 'admin', operator: true, self }));
  route('GET', /\/ops\/state$/, () => json(ops()));
  route('PUT', /\/ops\/config$/, (m, u, body) => {
    const cfg = ops().config;
    for (const [k, v] of Object.entries(body || {})) cfg[k] = v;
    return json(ops());
  });
  route('PATCH', /\/ops\/sandboxes\/([^/?]+)$/, (m, u, body) => {
    const s = opBox(m[1]);
    if (!s) return fail(404, 'no such sandbox', 'not-found');
    if (body && ('shares' in body || 'visibility' in body || 'members' in body)) {
      return fail(403, 'operators don\'t change who may use a sandbox (visibility, members, shares): its home consumer or its owner does', 'not-allowed');
    }
    Object.assign(s, body);
    return json(s);
  });
  route('POST', /\/ops\/sandboxes\/([^/?]+)\/(start|stop)/, (m) => {
    const s = opBox(m[1]);
    if (!s) return fail(404, 'no such sandbox', 'not-found');
    s.state = m[2] === 'start' ? 'running' : 'stopped';
    return json(s);
  });
  route('DELETE', /\/ops\/sandboxes\/([^/?]+)$/, (m) => {
    ops().sandboxes = ops().sandboxes.filter((s) => s.id !== m[1]);
    return json(null, 204);
  });
  const snaps = (id) => ((st.snaps || (st.snaps = {}))[id] || (st.snaps[id] = []));
  route('GET', /\/ops\/sandboxes\/([^/?]+)\/snapshots$/, (m) => json({ snapshots: snaps(m[1]) }));
  route('POST', /\/ops\/sandboxes\/([^/?]+)\/snapshots$/, (m, u, body) => {
    const s = { id: `s-${++seq}`, name: (body && body.name) || '', created: Date.now() };
    snaps(m[1]).push(s);
    return json(s, 201);
  });
  route('POST', /\/ops\/sandboxes\/([^/?]+)\/snapshots\/([^/?]+)\/restore$/, (m) => json(opBox(m[1])));
  route('DELETE', /\/ops\/sandboxes\/([^/?]+)\/snapshots\/([^/?]+)$/, (m) => {
    st.snaps[m[1]] = snaps(m[1]).filter((s) => s.id !== m[2]);
    return json(null, 204);
  });
  route('POST', /\/ops\/images\/([^/?]+)\/build$/, (m) => {
    const imgs = ops().images || (ops().images = []);
    const b = imgs.find((x) => x.id === m[1]);
    if (b) Object.assign(b, { state: 'building', detail: '' });
    else imgs.push({ id: m[1], state: 'building', started: Date.now() });
    return json(null, 202);
  });
  route('DELETE', /\/ops\/orphans\/([^/?]+)$/, (m) => {
    ops().orphans = (ops().orphans || []).filter((o) => o.name !== m[1]);
    return json(null, 204);
  });

  route('GET', /\/sbx\/hello/, () => json(st.hello || { protocol: 1, caps: ['exec', 'files', 'tar', 'tty'], egress: ['none'], images: [], sizes: [] }));
  route('GET', /\/sbx\/sandboxes$/, () => json({ sandboxes: st.mine || [] }));
  route('POST', /\/sbx\/sandboxes$/, (m, u, body) => {
    const h = st.hello || {};
    const im = (h.images || []).find((x) => x.id === body.image) || {};
    const sz = (h.sizes || []).find((x) => x.id === body.size) || {};
    const s = {
      id: `sb-new${++seq}`, name: body.name, state: 'running', stateDetail: '', image: { id: im.id || body.image, title: im.title || '' },
      size: { id: sz.id || body.size, memMiB: sz.memMiB, vcpus: sz.vcpus, diskGiB: sz.diskGiB }, isolation: 'vm',
      egress: body.egress || 'none', owner: { user: (st.me && st.me.user) || '', via: self }, visibility: body.visibility || 'private',
      members: [], shares: [], labels: {}, workdir: '/work', home: '/home/dev', user: 'dev', shell: '/bin/bash',
      caps: h.caps || [], created: Date.now(), lastActive: Date.now(), version: 1,
    };
    (st.mine || (st.mine = [])).push(s);
    return json(s, 201);
  });
  route('POST', /\/sbx\/sandboxes\/([^/?]+)\/(start|stop)/, (m) => {
    const s = myBox(m[1]);
    if (!s) return fail(404, 'no such sandbox', 'not-found');
    s.state = m[2] === 'start' ? 'running' : 'stopped';
    return json(s);
  });
  route('DELETE', /\/sbx\/sandboxes\/([^/?]+)$/, (m) => {
    st.mine = (st.mine || []).filter((s) => s.id !== m[1]);
    return json(null, 204);
  });
  route('PATCH', /\/sbx\/sandboxes\/([^/?]+)$/, (m, u, body) => {
    const s = myBox(m[1]);
    if (!s) return fail(404, 'no such sandbox', 'not-found');
    Object.assign(s, body);
    s.version = (s.version || 0) + 1;
    return json(s);
  });
  const files = () => st.files || (st.files = {});
  route('GET', /\/sbx\/sandboxes\/([^/?]+)\/files\/list/, (m, u) => {
    const p = q(u, 'path');
    const d = files()[p];
    if (!d || !d.entries) return fail(404, `no directory ${p}`, 'not-found');
    return json({ path: p, entries: d.entries, truncated: false });
  });
  route('GET', /\/sbx\/sandboxes\/([^/?]+)\/files\/stat/, (m, u) => {
    const p = q(u, 'path');
    const f = files()[p];
    if (!f) return fail(404, `no file ${p}`, 'not-found');
    return json({ path: p, type: f.entries ? 'dir' : 'file', size: (f.content || '').length, mode: '0644', etag: 'e' });
  });
  route('GET', /\/sbx\/sandboxes\/([^/?]+)\/files\/content/, (m, u) => {
    const f = files()[q(u, 'path')];
    if (!f || f.content == null) return fail(404, 'no such file', 'not-found');
    const n = Number(q(u, 'length') || -1);
    return new Response(n >= 0 ? f.content.slice(0, n) : f.content, { status: 200, headers: { ETag: '"e"' } });
  });
  route('PUT', /\/sbx\/sandboxes\/([^/?]+)\/files\/content/, (m, u, body, raw) => {
    const p = q(u, 'path');
    files()[p] = { content: raw };
    const dir = p.slice(0, p.lastIndexOf('/')) || '/';
    const d = files()[dir];
    const name = p.slice(p.lastIndexOf('/') + 1);
    if (d && d.entries && !d.entries.some((e) => e.name === name)) d.entries.push({ name, type: 'file', size: raw.length, mtimeMs: Date.now() });
    return json({ path: p, type: 'file', size: raw.length, etag: 'e2' });
  });
  route('POST', /\/sbx\/sandboxes\/([^/?]+)\/files\/(mkdir|remove)$/, (m, u, body) => {
    const p = body.path;
    const dir = p.slice(0, p.lastIndexOf('/')) || '/';
    const name = p.slice(p.lastIndexOf('/') + 1);
    const d = files()[dir];
    if (m[2] === 'mkdir') {
      files()[p] = { entries: [] };
      if (d && d.entries) d.entries.push({ name, type: 'dir', size: 0, mtimeMs: Date.now() });
    } else {
      delete files()[p];
      if (d && d.entries) d.entries = d.entries.filter((e) => e.name !== name);
    }
    return json(null, 204);
  });
  route('POST', /\/sbx\/sandboxes\/([^/?]+)\/execs$/, (m, u, body) => json({ id: `e${++seq}`, tty: !!body.tty, state: 'running', argv: body.argv || [], label: body.label || '' }, 201));
  route('DELETE', /\/sbx\/sandboxes\/([^/?]+)\/execs\/([^/?]+)$/, () => json(null, 204));

  const prev = w.xbin || {};
  w.xbin = Object.assign(prev, {
    self,
    iface: () => null,
    download: (name, data) => { w.__downloads.push({ name, size: data && (data.size ?? String(data).length) }); },
    fetch: async (url, opts = {}) => {
      const method = (opts.method || 'GET').toUpperCase();
      const u = String(url);
      let raw = '';
      if (opts.body != null) raw = typeof opts.body === 'string' ? opts.body : await new Response(opts.body).text();
      let body = null;
      try { body = raw && /json/.test((opts.headers && opts.headers['Content-Type']) || '') ? JSON.parse(raw) : null; } catch { body = null; }
      w.__calls.push({ method, url: u, body: raw });
      if (!u.startsWith(base + '/')) return fail(404, `not this tile's: ${u}`, 'not-found');
      const path = u.slice(base.length).split('?')[0];
      if (st.me && st.me.write === false && path.startsWith('/sbx/') && (method !== 'GET' || /\/tty$/.test(path))) {
        return fail(403, `${st.me.user} has read access to ${self}: that lets them look, not change — changing a sandbox here needs write access`, 'not-allowed');
      }
      for (const [m, re, fn] of routes) {
        const hit = m === method && re.exec(path);
        if (hit) return fn(hit, u, body, raw);
      }
      return fail(404, `no route ${method} ${path}`, 'not-found');
    },
  });
}
