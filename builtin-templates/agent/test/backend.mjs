// backend.mjs — the tile, served for a test, against an in-page fake backend.
//
// serveTile(ctx) answers the tile's own files (index.html and every module
// next to it), the kit, lit and marked. STUB is the fake backend a test
// installs with ctx.addInitScript(STUB, seed): a window.xbin whose fetch
// answers the routes the tile uses — the run list, run views, messages, the
// queue, interrupts, approvals — and a live stream the test drives with
// window.__push(event) (the same SSE the real backend writes). Tests add or
// override routes with window.__route(method, regexp, fn) from their own init
// script, and read what the tile sent from window.__calls.
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
  const modules = new Set(readdirSync(tileDir).filter((f) => f.endsWith('.js')));
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
      rows.sort((a, b) => b.activityMs - a.activityMs || b.id - a.id);
      return json({ pinned: rows.filter((r) => r.pinnedAt), items: rows.filter((r) => !r.pinnedAt), next: '' });
    }],
    ['PATCH', /\/runs\/(\d+)$/, (m, o) => {
      const r = window.__runs.find((x) => x.id === +m[1]) || {};
      const b = JSON.parse(o.body);
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
        visibility: b.visibility, config: { cron: b.cron, goal: b.goal }, runs: 0, unread: 0 };
      (seed.automations = seed.automations || []).push(it);
      return json({ id: 99, watcher: !!b.watcher, ...b });
    }],
    ['PUT', /\/schedules\/(\d+)$/, (m) => json({ id: +m[1] })],
    ['POST', /\/schedules\/(\d+)\/trigger$/, () => json({ ok: 'true' })],
    ['GET', /\/runs\/(\d+)\/view$/, (m) => json(view(+m[1]))],
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
  ];
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
