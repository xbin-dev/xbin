// backend.mjs — the tile, served for a test, against an in-page fake backend.
//
// serveTile(ctx) answers the tile's own files (index.html, every module next
// to it, the shared model under model/ and the native view under native/),
// the kit, lit and marked. STUB is
// the fake backend a test installs with ctx.addInitScript(STUB, seed): a
// window.xbin whose fetch answers the routes the tile uses — the run list,
// run views, messages, the queue, interrupts, approvals, the classes (D116:
// the three built-ins, or seed.classes; PUT refuses a mixed class it was not
// told to confirm), the coding sandboxes (D115: seed.sandboxes and
// seed.sbxManagers — GET/POST/PATCH/DELETE /sandboxes, their lifecycle, and
// PATCH /runs {sandbox, detach} into the view's config) — and a live stream
// the test drives with window.__push(event) (the same SSE the real backend
// writes). Tests add or override routes with window.__route(method, regexp,
// fn) from their own init script, and read what the tile sent from
// window.__calls.
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { serveKit, tileHtml } from './kit.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const tileDir = join(here, '..');
// lit and marked come from the xbin checkout's web/vendor (BX_VENDOR in an instance).
const vendor = process.env.BX_VENDOR || join(here, '..', '..', '..', 'web', 'vendor');
export const ORIGIN = 'http://tile.test';

export const THEME = '<style>:root{--bx-border:#ccc;--bx-panel:#fff;--bx-panel-2:#f4f4f4;--bx-text:#111;' +
  '--bx-muted:#777;--bx-accent:#b57e10;--bx-mono:monospace;--bx-red:#c33;--bx-green:#3a3}</style>';

export async function serveTile(ctx, { realMarked = false } = {}) {
  const modules = new Set([...readdirSync(tileDir).filter((f) => f.endsWith('.js')),
    ...['model', 'native'].flatMap((d) => readdirSync(join(tileDir, d)).filter((f) => f.endsWith('.js')).map((f) => `${d}/${f}`))]);
  await ctx.route(`${ORIGIN}/**`, (route) => {
    let path = new URL(route.request().url()).pathname.replace(/^\//, '') || 'index.html';
    if (path !== 'index.html' && !modules.has(path)) return route.fulfill({ status: 404, body: '' });
    let body = readFileSync(join(tileDir, path), 'utf8');
    if (path === 'index.html') body = tileHtml(body).replace(/<link rel="stylesheet" href="\/vendor\/theme.css">/, THEME);
    route.fulfill({ contentType: path.endsWith('.js') ? 'text/javascript' : 'text/html', body });
  });
  await serveKit(ctx);
  await ctx.route('**/vendor/lit-all.min.js', (r) =>
    r.fulfill({ contentType: 'text/javascript', body: readFileSync(join(vendor, 'lit-all.min.js'), 'utf8') }));
  await ctx.route('**/vendor/marked.esm.js', (r) => r.fulfill({
    contentType: 'text/javascript',
    body: realMarked ? readFileSync(join(vendor, 'marked.esm.js'), 'utf8') : 'export const marked={parse:(s)=>s,use(){}};',
  }));
}

// STUB runs in the page (addInitScript): seed = {runs: [...], views: {id: view}}.
export function STUB(seed) {
  window.__calls = [];
  window.__runs = seed.runs || [];
  window.__views = seed.views || {};
  window.__prefs = {};
  const routes = [];
  window.__route = (method, re, fn) => routes.unshift({ method, re, fn });
  const json = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
  window.__json = json;

  // The live stream: every open stream gets what __push sends.
  const streams = new Set();
  let seq = 100;
  window.__push = (ev) => {
    ev.seq = ev.seq || ++seq;
    ev.ts = ev.ts || Date.now();
    const line = `id: g.${ev.seq}\ndata: ${JSON.stringify(ev)}\n\n`;
    for (const c of streams) { try { c.enqueue(new TextEncoder().encode(line)); } catch { streams.delete(c); } }
  };
  window.__streams = () => streams.size;

  const view = (id) => {
    const v = window.__views[id];
    const run = (v && v.run) || window.__runs.find((r) => r.id === id) || { id, status: 'idle', title: 'run ' + id };
    return { cursor: 'g.' + seq, run: { pendingState: {}, ...run }, messages: [], steps: [], links: [], queued: [], drafts: [],
      chain: [], files: [], memory: {}, config: {}, messageFiles: {}, ...(v || {}) };
  };

  const base = [
    ['GET', /\/api\/xbin\/prefs\/(.+)$/, (m) => (m[1] in window.__prefs ? json(window.__prefs[m[1]]) : json({}, 404))],
    ['PUT', /\/api\/xbin\/prefs\/(.+)$/, (m, o) => { window.__prefs[m[1]] = JSON.parse(o.body); return json({}); }],
    ['GET', /\/runs\?roots=1$/, () => json(window.__runs.filter((r) => !r.parentId))],
    // the conversation list (D83): roots, newest activity first; pins apart
    ['GET', /\/conversations\?(.*)$/, (m) => {
      const q = new URLSearchParams(m[1]);
      const conv = (r) => ({ access: 'owner', mine: true, visibility: 'private', origin: 'chat', activityMs: r.id * 1000, ...r });
      let rows = window.__runs.filter((r) => !r.parentId).map(conv).filter((r) => ['', 'chat', 'api'].includes(r.origin));
      if (q.get('q')) {
        const t = q.get('q').toLowerCase();
        return json({ pinned: [], items: rows.filter((r) => (r.title || '').toLowerCase().includes(t)), next: '' });
      }
      rows = rows.filter((r) => !!r.archivedAt === (q.get('archived') === '1'));
      if (q.get('scope') === 'team') rows = rows.filter((r) => !r.mine && r.visibility === 'team');
      if (q.get('scope') === 'shared') rows = rows.filter((r) => r.visibility === 'team' || (r.members || 0) > 0);
      rows.sort((a, b) => b.activityMs - a.activityMs || b.id - a.id);
      return json({ pinned: rows.filter((r) => r.pinnedAt), items: rows.filter((r) => !r.pinnedAt), next: '' });
    }],
    ['PATCH', /\/runs\/(\d+)$/, (m, o) => {
      const r = window.__runs.find((x) => x.id === +m[1]) || {};
      const b = JSON.parse(o.body);
      if ('sandbox' in b || 'detach' in b) {
        const why = bindRun(+m[1], b);
        if (why) return json({ error: why }, 403);
      }
      if ('pinned' in b) r.pinnedAt = b.pinned ? Date.now() : 0;
      if ('archived' in b) r.archivedAt = b.archived ? Date.now() : 0;
      if (b.title) r.title = b.title;
      if (b.visibility) r.visibility = b.visibility;
      return json({ access: 'owner', mine: true, origin: 'chat', activityMs: r.id * 1000, ...r });
    }],
    ['POST', /\/runs\/(\d+)\/read$/, () => json({ readMs: Date.now() })],
    ['GET', /\/needs$/, () => json({ items: seed.needs || [] })],
    // automations (D83)
    ['GET', /\/automations\?summary=1$/, () => json({ count: (seed.automations || []).length,
      unread: (seed.automations || []).reduce((n, a) => n + (a.access === 'oversee' ? 0 : a.unread || 0), 0), failing: 0,
      attention: (seed.automations || []).reduce((n, a) => n + (['owner', 'claim'].includes(a.access) ? a.attention || 0 : 0), 0) })],
    ['GET', /\/automations$/, () => json({ items: seed.automations || [] })],
    ['GET', /\/automations\/(\w+)\/(\d+)\/runs/, (m) => json({ items: (seed.autoRuns || {})[m[2]] || [], next: '' })],
    ['POST', /\/automations\/(\w+)\/(\d+)\/(read|reset)$/, () => json({ ok: 'true' })],
    ['POST', /\/schedules$/, (m, o) => {
      const b = JSON.parse(o.body);
      const it = { kind: b.watcher ? 'watcher' : 'schedule', id: 99, name: b.name, access: 'owner', enabled: true, mode: b.mode,
        visibility: b.visibility, config: { cron: b.cron, goal: b.goal, class: b.class || '', toolset: b.toolset || 'private' }, runs: 0, unread: 0 };
      (seed.automations = seed.automations || []).push(it);
      return json({ id: 99, watcher: !!b.watcher, ...b });
    }],
    ['PUT', /\/schedules\/(\d+)$/, (m) => json({ id: +m[1] })],
    ['POST', /\/schedules\/(\d+)\/trigger$/, () => json({ ok: 'true' })],
    ['GET', /\/runs\/(\d+)\/view(?:\?.*)?$/, (m) => json(view(+m[1]))], // a paged read (the native view) gets it all
    ['GET', /\/stream\b/, (m, o) => new Response(new ReadableStream({
      start(c) {
        streams.add(c);
        c.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ type: 'hello', data: { cursor: 'g.' + seq } })}\n\n`));
        o.signal && o.signal.addEventListener('abort', () => { streams.delete(c); try { c.close(); } catch { /* closed */ } });
      },
    }), { headers: { 'Content-Type': 'text/event-stream' } })],
    ['POST', /\/runs\/(\d+)\/message$/, () => json({ ok: 'true', inboxId: 1, queued: false })],
    ['POST', /\/runs\/(\d+)\/interrupt$/, () => json({ ok: 'true', returned: [] })],
    ['POST', /\/runs\/(\d+)\/approve$/, () => json({ ok: 'true' })],
    ['DELETE', /\/runs\/(\d+)\/inbox\/(\d+)$/, () => json({ ok: 'true' })],
    ['GET', /\/halt$/, () => json({ on: false })],
    ['GET', /\/me$/, () => json(seed.me || { kind: 'user', user: 'admin', level: 'terminal', manager: true, halted: false })],
    ['GET', /\/runs\/(\d+)\/tree$/, (m) => json({ root: +m[1], nodes: [], totals: {} })],
    // agent classes (D116), as _backend/classes.go answers them
    ['GET', /\/classes$/, () => json(classesView())],
    ['PUT', /\/classes$/, (m, o) => {
      const b = JSON.parse(o.body);
      const mixed = (b.classes || []).filter((c) => reach(c).mixed).map((c) => c.id);
      if (mixed.length && !b.confirmMixed) return json({ error: 'these classes can move internal data out: ' + mixed.join(', '), mixed }, 409);
      window.__classes = { classes: b.classes || [], default: b.default || '' };
      return json(classesView());
    }],
  ];
  // The stored classes (window.__classes: {classes, default}): the built-ins
  // first (as stored, or their default), then the rest. GET filters by who
  // for a non-manager.
  const BUILTIN = [
    { id: 'internal', name: 'Internal', icon: '🔒', description: 'Your workspace\'s systems and data (xbin_call, MCP servers) — no web.',
      toolsets: ['files', 'repl', 'internal', 'subagents', 'schedule', 'threads', 'skills'], mcp: 'all', managers: [], sandboxEgress: [] },
    { id: 'web', name: 'Web', icon: '🌐', description: 'Searches and reads the web — no internal systems.',
      toolsets: ['files', 'repl', 'web', 'subagents', 'schedule', 'threads', 'skills'], mcp: [], managers: [], sandboxEgress: [] },
    { id: 'coding', name: 'Coding', icon: '▣', description: 'Works in a coding sandbox, with the web — no internal systems.',
      toolsets: ['sandbox', 'web', 'files', 'subagents', 'skills'], mcp: [], managers: 'all', sandboxEgress: ['none', 'internet'] },
  ];
  window.__classes = seed.classes || { classes: [], default: '' };
  const reach = (c) => {
    const ts = c.toolsets || [];
    const egress = ts.includes('web') || (ts.includes('sandbox') && (c.sandboxEgress || []).some((e) => e !== 'none'));
    return { mixed: ts.includes('internal') && egress, lane: egress && !ts.includes('internal') ? 'web' : 'private', egress };
  };
  const classesView = () => {
    const st = window.__classes;
    const saved = (id) => st.classes.find((c) => c.id === id);
    const all = [...BUILTIN.map((b) => (saved(b.id) ? { ...saved(b.id), stored: true } : { ...b, stored: false })),
      ...st.classes.filter((c) => !BUILTIN.some((b) => b.id === c.id)).map((c) => ({ ...c, stored: true }))]
      .map((c) => ({ description: '', icon: '', model: '', system: '', who: 'everyone', mcp: [], managers: [], sandboxEgress: [], ...c,
        builtin: ['internal', 'web', 'coding'].includes(c.id), ...reach(c) }));
    const me = seed.me || { manager: true };
    const mine = all.filter((c) => c.who !== 'managers' || me.manager);
    const def = mine.some((c) => c.id === st.default) ? st.default : mine.some((c) => c.id === 'internal') ? 'internal' : (mine[0] || {}).id;
    return { classes: mine, default: def };
  };
  // The coding sandboxes (D115), as _backend/sandbox_routes.go answers them:
  // window.__sbx = {sandboxes, managers}; a sandbox in a route's path is
  // percent-encoded (| → %7C) with its slashes as they are.
  const MGR = { provider: 'apps/coding-sandbox', title: 'Coding sandboxes', ok: true, caps: ['exec', 'files', 'tar', 'archive'],
    egress: ['none', 'internet', 'open'], images: [{ id: 'base', title: 'Debian', default: true }, { id: 'go', title: 'Go' }],
    sizes: [{ id: 'small', memMiB: 2048, vcpus: 2, diskGiB: 20, default: true }], limits: {} };
  window.__sbx = { sandboxes: seed.sandboxes || [], managers: seed.sbxManagers || [MGR] };
  const box = (ref) => window.__sbx.sandboxes.find((x) => x.ref === ref);
  const refOf = (enc) => decodeURIComponent(enc);
  const binding = (s, cwd) => ({ ref: s.ref, cwd: cwd || s.workdir || '/work', name: s.name, manager: s.manager, image: (s.image || {}).id || '',
    egress: s.egress, by: (seed.me || {}).user || 'admin', at: Date.now() });
  const bindRun = (id, b) => {
    const v = window.__views[id] || (window.__views[id] = {});
    const cfg = v.config || (v.config = {});
    if (b.detach) {
      cfg.attached = (cfg.attached || []).filter((a) => a.ref !== b.detach);
      if (cfg.sandbox && cfg.sandbox.ref === b.detach) delete cfg.sandbox;
    }
    if (b.sandbox === null) delete cfg.sandbox;
    else if (b.sandbox) {
      const s = box(b.sandbox.ref);
      if (!s) return 'no such sandbox';
      if (b.sandbox.cwd && !b.sandbox.cwd.startsWith('/')) return 'sandbox.cwd: an absolute path in the sandbox';
      cfg.sandbox = binding(s, b.sandbox.cwd);
      cfg.attached = [...(cfg.attached || []).filter((a) => a.ref !== s.ref), cfg.sandbox];
      s.boundTo = [...new Set([...(s.boundTo || []), id])];
    }
    return '';
  };
  base.push(
    ['GET', /\/sandboxes(\?fresh=1)?$/, () => json(window.__sbx)],
    ['POST', /\/sandboxes$/, (m, o) => {
      const b = JSON.parse(o.body);
      const s = { ref: `${b.provider || MGR.provider}|sb-${b.name}`, provider: b.provider || MGR.provider, manager: MGR.title, id: 'sb-' + b.name,
        name: b.name, state: 'running', egress: b.egress || 'none', visibility: b.visibility || 'private', image: { id: b.image || 'base' },
        owner: { user: (seed.me || {}).user || 'admin' }, mine: true, canUse: true, canManage: true, canEdit: true, workdir: '/work',
        caps: MGR.caps, lastActive: Date.now() };
      window.__sbx.sandboxes.unshift(s);
      if (b.conversation && b.bind !== false) { bindRun(b.conversation, { sandbox: { ref: s.ref, cwd: b.cwd } }); s.binding = window.__views[b.conversation].config.sandbox; }
      return json(s, 201);
    }],
    ['POST', /\/sandboxes\/(.+)\/(start|stop|archive|thaw)\?/, (m) => {
      const s = box(refOf(m[1]));
      if (!s) return json({ error: 'no such sandbox' }, 404);
      s.state = { start: 'running', stop: 'stopped', archive: 'archived', thaw: 'stopped' }[m[2]];
      return json(s);
    }],
    ['PATCH', /\/sandboxes\/(.+)$/, (m, o) => {
      const s = box(refOf(m[1]));
      if (!s) return json({ error: 'no such sandbox' }, 404);
      Object.assign(s, JSON.parse(o.body));
      return json(s);
    }],
    ['DELETE', /\/sandboxes\/(.+)$/, (m) => {
      const ref = refOf(m[1]);
      window.__sbx.sandboxes = window.__sbx.sandboxes.filter((x) => x.ref !== ref);
      for (const [id, v] of Object.entries(window.__views)) if (JSON.stringify(v.config || {}).includes(ref)) bindRun(+id, { detach: ref });
      return json({ ok: true, detached: 0 });
    }],
  );
  window.xbin = {
    self: 'apps/agent',
    fetch: async (url, opt = {}) => {
      const method = opt.method || 'GET';
      window.__calls.push({ method, url, body: typeof opt.body === 'string' ? opt.body : undefined });
      for (const r of routes) {
        const m = r.method === method && url.match(r.re);
        if (m) return r.fn(m, opt);
      }
      for (const [meth, re, fn] of base) {
        const m = meth === method && url.match(re);
        if (m) return fn(m, opt);
      }
      return json({});
    },
    bus: { on: () => () => {} },
    iface: () => null,
    download: () => {},
  };
}

// launch starts a browser, or exits 0 (SKIP) without playwright.
export async function launch() {
  let chromium;
  try {
    ({ chromium } = await import('/usr/local/node/lib/node_modules/playwright/index.mjs'));
  } catch {
    try { ({ chromium } = await import('playwright')); } catch {
      console.log('SKIP: playwright not installed');
      process.exit(0);
    }
  }
  return chromium.launch();
}

export function checker() {
  let failures = 0;
  return {
    ok: (name, cond, extra = '') => {
      if (!cond) { console.log(`FAIL  ${name}  ← ${extra}`); failures++; }
      return cond;
    },
    done: (what) => {
      console.log(failures ? `\n${failures} FAILURE(S)` : `all ${what} checks passed`);
      process.exit(failures ? 1 : 0);
    },
  };
}
