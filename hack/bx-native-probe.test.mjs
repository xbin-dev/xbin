// cmd/bx/native-probe.mjs — the headless half of `bx native tree`, `bx lint
// --native` and `bx preview --native`:
//   - fixture routing matches hack/xbn/worker.mjs (so a fixture that drives
//     the node runner drives the browser run too), with the tile's own
//     /api/<tile>/ standing in for the fixture's xbin.self;
//   - the tree report (size, unknown primitives, app revisions, features)
//     and the tidying of locations/duplicates;
//   - with Playwright (PLAYWRIGHT_DIR, default ~/lcad-wasm): probeTile end to
//     end against a stand-in runtime document served from a CSP-sandboxed
//     (opaque-origin) page — first tree, fixture replay with a preflighted
//     fetch, a tap step, the pinned clock, diagnostics, a 404, a screenshot.
// The Go side (bx) is tested in cmd/bx/native_test.go and, against a real
// xbind, in test/native_bx_test.go (make integration).
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { existsSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { extname, join } from 'node:path';
import { routeKey, fixtureBody, treeReport, tidy, requireRoots, probeTile, loadPlaywright } from '../cmd/bx/native-probe.mjs';
import { VOCAB } from '../web/xb/vocab.js';

test('routeKey follows the node runner, mapping the tile onto the fixture self', () => {
  const routes = { 'GET /api/apps/tile/count': 1, 'POST /api/apps/tile/count': 2, '/api/apps/tile/any': 3, 'GET /api/apps/tile/q?x=1': 4, 'GET /api/apps/x/own': 5 };
  assert.equal(routeKey(routes, 'GET', '/api/apps/x/count', 'apps/x'), 'GET /api/apps/tile/count');
  assert.equal(routeKey(routes, 'POST', '/api/apps/x/count', 'apps/x'), 'POST /api/apps/tile/count');
  assert.equal(routeKey(routes, 'DELETE', '/api/apps/x/any', 'apps/x'), '/api/apps/tile/any');
  assert.equal(routeKey(routes, 'GET', '/api/apps/x/q?x=1', 'apps/x'), 'GET /api/apps/tile/q?x=1');
  assert.equal(routeKey(routes, 'GET', '/api/apps/x/count?y=2', 'apps/x'), 'GET /api/apps/tile/count');
  assert.equal(routeKey(routes, 'GET', '/api/apps/x/own', 'apps/x'), 'GET /api/apps/x/own', 'a route written for the real path wins');
  assert.equal(routeKey(routes, 'GET', '/api/apps/xy/count', 'apps/x'), null, 'a sibling sharing the prefix is not the tile');
  assert.equal(routeKey(routes, 'GET', '/api/apps/x/count', 'apps/x', 'apps/other'), null);
  assert.equal(routeKey({ 'GET /api/apps/o/n': 1 }, 'GET', '/api/apps/x/n', 'apps/x', 'apps/o'), 'GET /api/apps/o/n');
});

test('fixtureBody renders json, sse and text like the node runner', () => {
  assert.deepEqual(fixtureBody({ json: { a: 1 } }), { status: 200, headers: { 'content-type': 'application/json' }, body: '{"a":1}' });
  const sse = fixtureBody({ sse: [{ event: 'x', id: '7', data: { n: 1 } }, { data: 'plain' }] });
  assert.equal(sse.headers['content-type'], 'text/event-stream');
  assert.equal(sse.body, 'event: x\nid: 7\ndata: {"n":1}\n\ndata: plain\n\n');
  assert.deepEqual(fixtureBody({ status: 204, text: 'ignored', headers: { 'X-A': 'b' } }), { status: 204, headers: { 'x-a': 'b' }, body: '' });
  assert.deepEqual(fixtureBody({ text: 'hi', headers: { 'Content-Type': 'text/csv' } }), { status: 200, headers: { 'content-type': 'text/csv' }, body: 'hi' });
});

test('treeReport: size, unknown primitives, revisions, features', () => {
  const vocab = structuredClone(VOCAB);
  vocab.prims.row.props.future = { type: 'string', since: 3 };
  const tree = { v: 1, root: { k: 'r', t: 'screen', p: { title: 'T' }, c: [
    { k: 'r.0', t: 'section', c: [
      { k: 'r.0.0', t: 'row', p: { title: 'a', future: 'x' } },
      { k: 'r.0.1', t: 'row', p: { title: 'b' } },
      { k: 'r.0.2', t: 'chart', p: { kind: 'area', series: [] } },
      { k: 'r.0.3', t: 'markdown', p: { tokens: [{ t: 'para' }, { t: 'list', items: [[{ t: 'table', rows: [] }]] }] } },
      { k: 'r.0.4', t: 'blink' },
    ] },
  ] } };
  const r = treeReport(tree, vocab);
  assert.deepEqual(r.stats.prims, { screen: 1, section: 1, row: 2, chart: 1, markdown: 1, blink: 1 });
  assert.equal(r.stats.nodes, 7);
  assert.equal(r.stats.depth, 3);
  assert.equal(r.stats.bytes, Buffer.byteLength(JSON.stringify(tree)));
  assert.deepEqual(r.unknown, ['blink']);
  assert.deepEqual(r.needs, { screen: 1, section: 1, row: 3, chart: 1, markdown: 1 });
  assert.deepEqual(r.features, ['chart.area', 'markdown.tables']);
  assert.deepEqual(treeReport(null, VOCAB), { stats: { nodes: 0, depth: 0, bytes: 0, prims: {} }, unknown: [], needs: {}, features: [] });
});

test('tidy: the proxy origin leaves locations; each problem once', () => {
  const base = 'http://127.0.0.1:4321';
  const res = {
    errors: [{ kind: 'uncaught', message: 'boom', where: `${base}/c/apps/x/native.js:3`, stack: `Error: boom\n at ${base}/c/apps/x/native.js:3:7` }],
    diagnostics: [{ level: 'warn', code: 'bad-token', message: 'x', where: `${base}/c/apps/x/native.js:9` }],
    console: [{ type: 'error', text: '[xb-native] uncaught: boom' }, { type: 'error', text: `Failed to load ${base}/api/apps/x/n` }],
    pageErrors: [`Error: boom\n    at ${base}/c/apps/x/native.js:3:7`, 'TypeError: other'],
  };
  tidy(res, base);
  assert.equal(res.errors[0].where, '/c/apps/x/native.js:3');
  assert.match(res.errors[0].stack, /at \/c\/apps\/x\/native\.js:3:7/);
  assert.equal(res.diagnostics[0].where, '/c/apps/x/native.js:9');
  assert.deepEqual(res.console, [{ type: 'error', text: 'Failed to load /api/apps/x/n' }]);
  assert.deepEqual(res.pageErrors, ['TypeError: other']);
});

test('requireRoots: PLAYWRIGHT_DIR first, then the global prefix — never the working directory', () => {
  const roots = requireRoots({ PLAYWRIGHT_DIR: '/opt/pw' }, '/usr/local/bin/node');
  assert.deepEqual(roots, ['/opt/pw', '/usr/local/lib']);
  assert.deepEqual(requireRoots({}, '/usr/bin/node'), ['/usr/lib']);
});

// ── end to end in Chromium ───────────────────────────────────────────────────

const ROOT = new URL('..', import.meta.url).pathname;
const WEB = join(ROOT, 'web');
let pw = null;
try { pw = loadPlaywright({ ...process.env, PLAYWRIGHT_DIR: process.env.PLAYWRIGHT_DIR || join(process.env.HOME || '/', 'lcad-wasm') }); } catch { /* skipped */ }
const skip = pw ? false : 'Playwright not found (set PLAYWRIGHT_DIR)';

// a stand-in for xbind's runtime document (internal/server/native.go)
const DOC = `<!doctype html><html><head><meta charset="utf-8">
<meta name="xbin-native" content="1"><meta name="xbin-native-preview" content="1">
<script type="module">
import '/vendor/xb-native.js';
try { await import('/vendor/xb/preview-host.js'); } catch (e) { console.warn(e); }
await import('./native.js');
</script></head><body></body></html>`;

// fetches with the frame-token header, so every call is preflighted from
// the opaque origin — the replayed answers must satisfy CORS
const COUNTER = `import { html, render } from '/vendor/xb-native.js';
const api = (p, o = {}) => fetch('/api/apps/t' + p, { ...o, headers: { 'X-XBin-Frame-Token': 'ft' } });
let count = null;
const load = async () => { count = (await (await api('/count')).json()).count; paint(); };
const paint = () => render(html\`<screen title="Counter" style="form"><section>
  <row title="Count" detail=\${count ?? '…'}/>
  <row title="Now" detail=\${new Date().toISOString()}/>
  <button role="primary" @tap=\${async () => { await api('/count', { method: 'POST' }); await load(); }}>+1</button>
</section></screen>\`);
paint();
load();
`;
const BROKEN = `import { html, render } from '/vendor/xb-native.js';
render(html\`<screen title="B"><blink/><row title="x" tone="#f00"/></screen>\`);
`;

const TYPES = { '.js': 'text/javascript', '.html': 'text/html', '.json': 'application/json', '.css': 'text/css', '.svg': 'image/svg+xml' };
let srv; let base; let browser; let out;
before(async () => {
  if (skip) return;
  const files = { '/c/apps/t/native.js': COUNTER, '/c/apps/b/native.js': BROKEN };
  srv = createServer((req, res) => {
    const u = new URL(req.url, 'http://x');
    const cors = req.headers.origin === 'null' ? { 'Access-Control-Allow-Origin': 'null', Vary: 'Origin' } : {};
    if ((u.pathname === '/c/apps/t/' || u.pathname === '/c/apps/b/') && u.searchParams.get('native') === '1') {
      res.writeHead(200, { 'Content-Type': 'text/html', 'Content-Security-Policy': 'sandbox allow-scripts' });
      return res.end(DOC);
    }
    if (u.pathname === '/c/apps/web/') { res.writeHead(404, { 'Content-Type': 'text/plain' }); return res.end('this tile has no native app UI'); }
    if (Object.hasOwn(files, u.pathname)) { res.writeHead(200, { 'Content-Type': 'text/javascript', ...cors }); return res.end(files[u.pathname]); }
    if (u.pathname.startsWith('/vendor/') && !u.pathname.includes('..')) {
      const name = u.pathname.slice('/vendor/'.length);
      for (const f of [join(WEB, name), join(WEB, 'vendor', name)]) {
        if (existsSync(f) && statSync(f).isFile()) {
          res.writeHead(200, { 'Content-Type': TYPES[extname(f)] || 'application/octet-stream', ...cors });
          return res.end(readFileSync(f));
        }
      }
    }
    res.writeHead(404, cors); res.end('not found');
  });
  await new Promise((r) => srv.listen(0, '127.0.0.1', r));
  base = `http://127.0.0.1:${srv.address().port}`;
  browser = await pw.chromium.launch();
  out = mkdtempSync(join(tmpdir(), 'bx-probe-'));
});
after(async () => { await browser?.close(); srv?.close(); if (out) rmSync(out, { recursive: true, force: true }); });

test('probeTile replays a fixture, runs a step, pins the clock, takes the picture', { skip }, async () => {
  const now = Date.UTC(2026, 1, 3, 4, 5, 6);
  const shot = join(out, 'shot.png');
  const r = await probeTile(browser, {
    base, mode: 'preview', out: shot, theme: 'dark', timeout: 15000, settle: 300,
    data: { now, routes: { 'GET /api/apps/tile/count': [{ json: { count: 41 } }, { json: { count: 42 } }], 'POST /api/apps/tile/count': { status: 204 } } },
    steps: [{ tap: 'r.0.2' }],
  }, 'apps/t');
  assert.equal(r.loadError, '');
  assert.equal(r.status, 200);
  assert.ok(r.ok, JSON.stringify({ errors: r.errors, diagnostics: r.diagnostics, pageErrors: r.pageErrors, console: r.console }));
  const rows = r.tree.root.c[0].c;
  assert.equal(rows[0].p.detail, '42');
  assert.ok(rows[1].p.detail.startsWith('2026-02-03T04:05:'), rows[1].p.detail);
  assert.deepEqual(r.requests.map((q) => `${q.method} ${q.url}`), ['GET /api/apps/t/count', 'POST /api/apps/t/count', 'GET /api/apps/t/count']);
  assert.deepEqual(r.unmatched, []);
  assert.equal(typeof r.firstTreeMs, 'number');
  assert.deepEqual(r.needs, { screen: 1, section: 1, row: 1, button: 1 });
  assert.equal(r.stats.nodes, 5);
  assert.equal(r.shot, shot);
  const png = readFileSync(shot);
  assert.equal(png.subarray(1, 4).toString(), 'PNG');
  assert.equal(png.readUInt32BE(16), 780); // 390 pt @2x
  assert.equal(png.readUInt32BE(20), 1688);
});

test('probeTile reports what the runtime reports, and a tile without a native UI', { skip }, async () => {
  const r = await probeTile(browser, { base, mode: 'lint', timeout: 15000, settle: 300 }, 'apps/b');
  assert.equal(r.ok, false);
  assert.ok(r.diagnostics.some((d) => d.code === 'unknown-tag' && d.message.includes('<blink>')), JSON.stringify(r.diagnostics));
  assert.ok(r.diagnostics.some((d) => d.code === 'bad-token'), JSON.stringify(r.diagnostics));
  assert.ok(r.diagnostics.every((d) => !String(d.where).includes(base)), 'locations are tidied');
  assert.equal(r.shot, '', 'lint takes no picture');
  const web = await probeTile(browser, { base, mode: 'tree', timeout: 5000 }, 'apps/web');
  assert.equal(web.status, 404);
  assert.match(web.loadError, /no native app UI/);
  assert.equal(web.tree, null);
});
