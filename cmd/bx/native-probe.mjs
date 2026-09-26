// native-probe.mjs — the headless-Chromium half of `bx native tree`,
// `bx lint --native` and `bx preview --native` (docs/bx.md "Native UIs").
// bx embeds it (go:embed), writes it to a temp dir and runs it with node.
//
// For each tile it loads the tile's runtime document with the preview host,
// /c/<tile>/?native=1&preview=1, from bx's loopback proxy (which lends bx's
// credential to that document and the tile's own files, nothing else — the
// tile's code talks to xbind with the frame token the document was minted),
// waits for the rendered tree to settle, optionally replays a fixture and
// steps, and prints one JSON object on stdout:
//
//   {"results": [{tile, ok, status, loadError, tree, firstTreeMs, settled,
//     stats: {nodes, depth, bytes, prims}, unknown, needs, features, errors,
//     diagnostics, console, pageErrors, requests, unmatched, warnings, shot}]}
//
// Config (a JSON file named by argv[2]):
//   base      the proxy's origin, e.g. http://127.0.0.1:41234
//   mode      "tree" | "lint" | "preview"
//   tiles     ["apps/counter", …]
//   theme     "light" | "dark" (preview; default light)
//   text      "" | "large"
//   width, height, scale   the viewport (390 × 844 @2x)
//   out, full  preview: the PNG path; full grows the view to its content
//   data      a fixture (hack/xbn/node.mjs's data format): routes answer the
//             tile's /api/ calls instead of the live backend, now pins the
//             clock, tz/locale set the browser's
//   steps     after the first settle: {wait} {tap} {input} {event}
//             {visibility} {resolve} (node.mjs's step format; bus is not
//             available here)
//   timeout   ms to wait for a tile to settle (default 30000)
//   settle    ms of quiet that counts as settled (default 400)
//
// Exit: 0 with the results; 3 when Playwright or its Chromium is missing (a
// message on stderr, nothing on stdout); 1 on anything else.
import { createRequire } from 'node:module';
import { readFileSync } from 'node:fs';
import { dirname, join, resolve as resolvePath } from 'node:path';
import { fileURLToPath } from 'node:url';

const own = (o, k) => o != null && Object.prototype.hasOwnProperty.call(o, k);

// ── Playwright ────────────────────────────────────────────────────────────────

// requireRoots: where playwright may be installed — PLAYWRIGHT_DIR (a
// directory whose node_modules has it), then the global prefix of this node
// (npm i -g); NODE_PATH is honoured by every createRequire lookup. Not the
// working directory: bx runs inside tiles, and a tile's node_modules is not
// bx's to load.
export function requireRoots(env = process.env, execPath = process.execPath) {
  const roots = [];
  if (env.PLAYWRIGHT_DIR) roots.push(resolvePath(env.PLAYWRIGHT_DIR));
  roots.push(join(dirname(execPath), '..', 'lib'));
  return [...new Set(roots)];
}

export function loadPlaywright(env = process.env) {
  const errs = [];
  for (const root of requireRoots(env)) {
    const req = createRequire(join(root, 'noop.js'));
    // PLAYWRIGHT_DIR may also name the package directory itself
    const names = root === resolvePath(env.PLAYWRIGHT_DIR || '/nonexistent') ? ['playwright', 'playwright-core', root] : ['playwright', 'playwright-core'];
    for (const name of names) {
      try {
        const pw = req(name);
        if (pw?.chromium) return pw;
      } catch (e) { errs.push(`${name} from ${root}: ${String(e.message).split('\n')[0]}`); }
    }
  }
  const err = new Error('Playwright was not found. The terminal rootfs ships it; elsewhere run `npm i -g playwright && npx playwright install chromium`, or set PLAYWRIGHT_DIR to a directory whose node_modules has playwright.');
  err.tried = errs;
  throw err;
}

// ── fixtures ──────────────────────────────────────────────────────────────────

// routeKey finds the fixture route answering method + url (path?query), in
// hack/xbn/worker.mjs's order: "METHOD url", "METHOD path", "url", "path".
// A fixture is written for its own xbin.self (data.self, default
// "apps/tile"); the tile under test calls /api/<tile>/…, so that prefix is
// also tried as /api/<self>/….
export function routeKey(routes, method, url, tile, self = 'apps/tile') {
  const forms = [url];
  const pre = `/api/${tile}`;
  if (tile && self !== tile && (url === pre || url.startsWith(`${pre}/`) || url.startsWith(`${pre}?`))) forms.push(`/api/${self}${url.slice(pre.length)}`);
  for (const u of forms) {
    const path = u.split('?')[0];
    for (const k of [`${method} ${u}`, `${method} ${path}`, u, path]) if (own(routes, k)) return k;
  }
  return null;
}

// fixtureBody: a route response spec → {status, headers, body} (node.mjs's
// response format: json | sse | text).
export function fixtureBody(spec = {}) {
  const headers = {};
  for (const [k, v] of Object.entries(spec.headers || {})) headers[k.toLowerCase()] = String(v);
  let body = '';
  if (own(spec, 'json')) { body = JSON.stringify(spec.json); headers['content-type'] ??= 'application/json'; }
  else if (own(spec, 'sse')) {
    body = spec.sse.map((f) => `${f.event ? `event: ${f.event}\n` : ''}${f.id ? `id: ${f.id}\n` : ''}data: ${typeof f.data === 'string' ? f.data : JSON.stringify(f.data)}\n\n`).join('');
    headers['content-type'] ??= 'text/event-stream';
  } else if (own(spec, 'text')) body = String(spec.text);
  const status = spec.status ?? 200;
  return { status, headers, body: status === 204 || status === 304 ? '' : body };
}

// Tile frames are opaque origins: xbind answers them with ACAO: null
// (internal/server nullOriginCORS); a replayed response must too.
const CORS = { 'access-control-allow-origin': 'null', vary: 'Origin' };
const PREFLIGHT = { ...CORS, 'access-control-allow-methods': 'GET, POST, PUT, PATCH, DELETE, OPTIONS',
  'access-control-allow-headers': 'Authorization, Content-Type, X-XBin-Frame-Token', 'access-control-max-age': '600' };

async function installFixture(page, cfg, tile, data, res) {
  const origin = new URL(cfg.base).origin;
  const self = String(data.self ?? 'apps/tile');
  const routes = data.routes || {};
  const used = new Map();
  await page.clock.install({ time: Number(data.now ?? Date.UTC(2026, 0, 1, 12)) });
  await page.route('**/*', async (route) => {
    const req = route.request();
    const u = new URL(req.url());
    if (u.origin !== origin) return route.continue();
    const url = u.pathname + u.search;
    const method = req.method();
    const api = u.pathname.startsWith('/api/') && !u.pathname.startsWith('/api/xbin/');
    if (method === 'OPTIONS') {
      const any = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE'].some((m) => routeKey(routes, m, url, tile, self));
      return any || api ? route.fulfill({ status: 204, headers: PREFLIGHT, body: '' }) : route.continue();
    }
    const key = routeKey(routes, method, url, tile, self);
    if (!key) {
      if (!api) return route.continue();
      res.unmatched.push(`${method} ${url}`);
      return route.fulfill({ status: 404, headers: { ...CORS, 'content-type': 'application/json' }, body: JSON.stringify({ error: `no stub for ${method} ${url}` }) });
    }
    let body = req.postData();
    if (body != null) { try { body = JSON.parse(body); } catch { /* keep text */ } }
    res.requests.push({ method, url, body: body ?? null });
    let spec = routes[key];
    if (Array.isArray(spec)) { const i = used.get(key) ?? 0; used.set(key, i + 1); spec = spec[Math.min(i, spec.length - 1)]; }
    spec = spec || {};
    if (spec.delay) await new Promise((r) => setTimeout(r, Math.min(Number(spec.delay) || 0, 10000)));
    if (spec.error) return route.abort('failed');
    const r = fixtureBody(spec);
    return route.fulfill({ status: r.status, headers: { ...CORS, ...r.headers }, body: r.body });
  });
}

// ── the tree ──────────────────────────────────────────────────────────────────

const hasTable = (v) => (Array.isArray(v) ? v.some(hasTable)
  : v && typeof v === 'object' ? v.t === 'table' || Object.values(v).some(hasTable) : false);

// treeReport: size, the primitives used, which ones the vocabulary doesn't
// know, the app revision each one needs (the highest `since` of the props
// it sets, else 1) and the feature flags the tree relies on.
export function treeReport(tree, vocab) {
  const stats = { nodes: 0, depth: 0, bytes: tree?.root ? Buffer.byteLength(JSON.stringify(tree)) : 0, prims: {} };
  const unknown = new Set();
  const needs = {};
  const features = new Set();
  const walk = (n, d) => {
    if (!n || typeof n !== 'object') return;
    stats.nodes++;
    stats.depth = Math.max(stats.depth, d);
    stats.prims[n.t] = (stats.prims[n.t] || 0) + 1;
    const spec = vocab?.prims?.[n.t];
    if (!spec) unknown.add(String(n.t));
    else {
      let rev = 1;
      for (const [name, v] of Object.entries(n.p || {})) {
        const ps = spec.props?.[name];
        if (!ps) continue;
        if (Number(ps.since) > rev) rev = Number(ps.since);
        if (ps.features && typeof v === 'string' && ps.features[v]) features.add(ps.features[v]);
        if (name === 'tokens' && hasTable(v)) features.add('markdown.tables');
      }
      needs[n.t] = Math.max(needs[n.t] || 0, rev);
    }
    for (const c of n.c || []) walk(c, d + 1);
  };
  walk(tree?.root, 1);
  return { stats, unknown: [...unknown].sort(), needs, features: [...features].sort() };
}

// ── one tile ──────────────────────────────────────────────────────────────────

// Records when the preview host drew its first tree (ms since navigation):
// the first mount the view applies, once it has rendered.
const INIT = `(() => {
  let v;
  const mark = (view) => view.updateComplete.then(() => { if (window.__xbnFirstTree == null) window.__xbnFirstTree = performance.now(); });
  Object.defineProperty(window, 'xbnPreview', { configurable: true, enumerable: true,
    get() { return v; },
    set(x) {
      v = x;
      const view = x && x.view;
      if (!view) return;
      if (view.tree && view.tree.root) { mark(view); return; }
      const apply = view.apply.bind(view);
      view.apply = (m) => { const r = apply(m); if (r && m && m.op === 'mount') mark(view); return r; };
    } });
})();`;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const clip = (s, n = 600) => { const t = String(s ?? ''); return t.length > n ? `${t.slice(0, n)}…` : t; };

// tidy: locations name the tile's files, not the proxy (http://127.0.0.1:…),
// and each problem is reported once — the runtime's own console lines
// ([xb-native] …) and page errors it already reported as {op:"error"}
// uncaught are dropped.
export function tidy(res, base) {
  const loc = (v) => (typeof v === 'string' && base ? v.split(base).join('') : v);
  for (const list of [res.errors, res.diagnostics]) {
    for (const m of list) for (const k of ['message', 'where', 'stack']) if (own(m, k)) m[k] = loc(m[k]);
  }
  res.console = res.console.filter((c) => !c.text.startsWith('[xb-native]')).map((c) => ({ ...c, text: loc(c.text) }));
  const uncaught = res.errors.filter((e) => e.kind === 'uncaught').map((e) => String(e.message));
  res.pageErrors = res.pageErrors.map(loc).filter((t) => !uncaught.some((m) => m && t.includes(m)));
}

async function settle(page, inflight, cfg, deadline) {
  const quiet = cfg.settle ?? 400;
  let last = '';
  let since = Date.now();
  for (;;) {
    const sig = await page.evaluate(() => {
      const p = window.xbnPreview;
      return p ? `${p.view?.n}|${p.messages.length}|${p.errors.length}|${p.diagnostics.length}` : '-';
    }).catch(() => 'gone');
    const busy = inflight.size > 0;
    if (sig !== last || busy) { last = sig; since = Date.now(); }
    if (Date.now() - since >= quiet) return true;
    if (Date.now() >= deadline) return false;
    await sleep(50);
  }
}

async function runStep(page, s, res, clock) {
  if (own(s, 'wait')) {
    const ms = Math.max(0, Number(s.wait) || 0);
    if (clock) await page.clock.fastForward(ms);
    else await sleep(Math.min(ms, 10000));
    return;
  }
  if (own(s, 'bus')) { res.warnings.push(`step ${JSON.stringify(s)}: bus events can't be injected in the browser run (xbin.bus is the live /ws/events) — skipped`); return; }
  const known = ['tap', 'input', 'event', 'visibility', 'resolve'].some((k) => own(s, k));
  if (!known) { res.warnings.push(`unknown step ${JSON.stringify(s)} — skipped`); return; }
  await page.evaluate((st) => {
    const X = window.xbn;
    const has = (k) => Object.prototype.hasOwnProperty.call(st, k);
    if (has('tap')) X.event(st.tap, 'tap', {});
    else if (has('input')) X.event(st.input[0], 'input', { value: st.input[1] });
    else if (has('event')) { const e = st.event; if (Array.isArray(e)) X.event(e[0], e[1], e[2] ?? {}, e[3]); else X.event(e.k, e.type, e.payload ?? {}, e.n); }
    else if (has('visibility')) X.visibility(st.visibility);
    else if (has('resolve')) X.resolve(...st.resolve);
  }, s);
}

export async function probeTile(browser, cfg, tile) {
  const res = { tile, ok: false, status: 0, loadError: '', tree: null, firstTreeMs: null, settled: false,
    stats: null, unknown: [], needs: {}, features: [], errors: [], diagnostics: [], console: [], pageErrors: [],
    requests: [], unmatched: [], warnings: [], shot: '' };
  const timeout = cfg.timeout ?? 30000;
  const deadline = Date.now() + timeout;
  const data = cfg.data && typeof cfg.data === 'object' ? cfg.data : null;
  const ctx = await browser.newContext({
    viewport: { width: cfg.width || 390, height: cfg.height || 844 },
    deviceScaleFactor: cfg.scale || 2,
    colorScheme: cfg.theme === 'dark' ? 'dark' : 'light',
    timezoneId: data?.tz || undefined,
    locale: data?.locale || undefined,
  });
  try {
    const page = await ctx.newPage();
    page.on('console', (m) => {
      // the reference renderer's own lit bundle announces itself; not the tile's business
      if ((m.type() === 'error' || m.type() === 'warning') && !/^Lit (has been loaded from a bundle|is in dev mode)/.test(m.text())) {
        res.console.push({ type: m.type(), text: clip(m.text()) });
      }
    });
    let pageFailed;
    const pageFailure = new Promise((r) => { pageFailed = r; });
    page.on('pageerror', (e) => { res.pageErrors.push(clip(e?.stack || e?.message || e)); pageFailed({ ok: false, pageError: true }); });
    // requests in flight (a live backend still answering) keep the run from
    // settling; streams (SSE, WebSockets) never finish, so they don't count
    const inflight = new Set();
    page.on('request', (r) => { if (!['websocket', 'eventsource'].includes(r.resourceType())) inflight.add(r); });
    page.on('requestfinished', (r) => inflight.delete(r));
    page.on('requestfailed', (r) => inflight.delete(r));
    page.on('response', (r) => { if (/text\/event-stream/i.test(r.headers()['content-type'] || '')) inflight.delete(r.request()); });
    await page.addInitScript(INIT);
    if (data) await installFixture(page, cfg, tile, data, res);

    const q = new URLSearchParams({ native: '1', preview: '1' });
    if (cfg.theme === 'dark' || cfg.theme === 'light') q.set('theme', cfg.theme);
    if (cfg.text === 'large') q.set('text', 'large');
    let resp;
    try {
      resp = await page.goto(`${cfg.base}/c/${tile}/?${q}`, { waitUntil: 'domcontentloaded', timeout });
    } catch (e) { res.loadError = clip(String(e.message).split('\n')[0]); return res; }
    res.status = resp?.status() ?? 0;
    if (!resp || !resp.ok()) {
      res.loadError = clip((await resp?.text().catch(() => ''))?.trim() || `HTTP ${res.status}`);
      return res;
    }
    const left = () => Math.max(1, deadline - Date.now());
    const started = await page.waitForFunction(() => window.xbnPreview, null, { timeout: left() }).then(() => true, () => false);
    if (!started) {
      res.loadError = 'the preview host never started (no window.xbnPreview): does this xbind serve /vendor/xb/preview-host.js?';
      return res;
    }
    // the app waits 5 s for a first tree; give it twice that (the rest of
    // the budget still goes to settling while requests are in flight)
    const firstWait = Math.min(left(), 10000);
    // an uncaught error before the first tree (a module that fails to
    // parse or throws while loading) ends the wait: nothing will come
    const ready = await Promise.race([pageFailure, page.evaluate((ms) => Promise.race([window.xbnPreview.ready,
      new Promise((r) => setTimeout(() => r({ ok: false, timeout: true }), ms))]), firstWait).catch((e) => ({ ok: false, error: { message: e.message } }))]);
    if (!ready?.ok && ready?.timeout) res.warnings.push(`no tree within ${firstWait} ms`);
    res.settled = await settle(page, inflight, cfg, deadline);
    for (const s of Array.isArray(cfg.steps) ? cfg.steps : []) {
      await runStep(page, s, res, !!data);
      res.settled = await settle(page, inflight, cfg, Math.max(deadline, Date.now() + 2000));
    }
    if (!res.settled && inflight.size) res.warnings.push(`still waiting on ${[...inflight].slice(0, 3).map((r) => `${r.method()} ${new URL(r.url()).pathname}`).join(', ')}`);
    const got = await page.evaluate(async () => {
      const p = window.xbnPreview;
      let vocab = null;
      try { vocab = (await import('/vendor/xb/vocab.js')).VOCAB; } catch { /* reported by the caller */ }
      const strip = (m) => { const o = { ...m }; delete o.op; return o; };
      return { tree: p.tree(), errors: p.errors.map(strip), diagnostics: p.diagnostics.map(strip),
        firstTreeMs: window.__xbnFirstTree ?? null, vocab: vocab && JSON.parse(JSON.stringify(vocab)) };
    });
    res.tree = got.tree?.root ? got.tree : null;
    res.errors = got.errors;
    res.diagnostics = got.diagnostics;
    tidy(res, cfg.base);
    res.firstTreeMs = got.firstTreeMs == null ? null : Math.round(got.firstTreeMs);
    if (!got.vocab) res.warnings.push('could not load /vendor/xb/vocab.js — no revision report');
    Object.assign(res, treeReport(res.tree, got.vocab));
    res.ok = !!res.tree && !res.errors.length && !res.diagnostics.some((d) => d.level === 'error') && !res.pageErrors.length;
    if (cfg.mode === 'preview' && cfg.out) {
      await page.evaluate(async () => { await document.fonts?.ready; await window.xbnPreview.view.updateComplete; });
      if (cfg.full) {
        const extra = await page.evaluate(() => {
          const root = document.querySelector('xb-view')?.shadowRoot;
          let more = 0;
          for (const el of root ? root.querySelectorAll('.body, .sheet-body, xb-transcript, .loose') : []) {
            if (el.getClientRects().length) more = Math.max(more, el.scrollHeight - el.clientHeight);
          }
          return more;
        });
        if (extra > 0) await page.setViewportSize({ width: cfg.width || 390, height: (cfg.height || 844) + extra });
      }
      await sleep(80);
      await page.screenshot({ path: cfg.out, animations: 'disabled' });
      res.shot = cfg.out;
    }
    return res;
  } finally {
    await ctx.close().catch(() => {});
  }
}

// ── main ──────────────────────────────────────────────────────────────────────

async function main() {
  const file = process.argv[2];
  if (!file) { console.error('usage: node native-probe.mjs <config.json>'); process.exit(1); }
  const cfg = JSON.parse(readFileSync(file, 'utf8'));
  let pw;
  try { pw = loadPlaywright(); } catch (e) { console.error(e.message); process.exit(3); }
  let browser;
  try { browser = await pw.chromium.launch(); } catch (e) {
    console.error(`Chromium did not start: ${String(e.message).split('\n').find((l) => l.trim()) || e}\n` +
      'Install it with `npx playwright install chromium` (the terminal rootfs ships it).');
    process.exit(3);
  }
  const results = [];
  try {
    for (const tile of cfg.tiles || []) results.push(await probeTile(browser, cfg, tile));
  } finally { await browser.close().catch(() => {}); }
  process.stdout.write(`${JSON.stringify({ results })}\n`);
}

if (process.argv[1] && resolvePath(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((e) => { console.error(String(e?.stack ?? e)); process.exit(1); });
}
