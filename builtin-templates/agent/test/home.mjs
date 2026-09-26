// home.mjs — the home view: what needs you, a quick ask in, a lane toggle
// that sticks.
//
// Tile frames are sandboxed opaque origins with NO localStorage — touching it
// throws, and at module scope that kills the whole tile. This test makes
// localStorage throw exactly like the real frame, then drives the real
// agent.js against a stubbed transport.
//
//   node test/home.mjs        (needs playwright + a chromium build)
import { readFileSync } from 'node:fs';
import { serveKit, tileHtml } from './kit.mjs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));

let chromium;
try {
  ({ chromium } = await import('/usr/local/node/lib/node_modules/playwright/index.mjs'));
} catch {
  try { ({ chromium } = await import('playwright')); } catch {
    console.log('SKIP: playwright not installed');
    process.exit(0);
  }
}

let failures = 0;
const ok = (name, cond, extra = '') => {
  if (!cond) { console.log(`FAIL  ${name}  ← ${extra}`); failures++; }
  return cond;
};

const ORIGIN = 'http://tile.test';
const MODULES = ['agent.js', 'chat-view.js', 'chat-fold.js', 'chat-cards.js', 'chat-md.js', 'stream.js', 'tool-heads.js',
  'conv-groups.js', 'conv-list.js', 'sidebar.js', 'home.js', 'share.js', 'automations.js', 'auto-channels.js'];
const FILES = { '/': 'index.html', '/index.html': 'index.html', ...Object.fromEntries(MODULES.map((m) => ['/' + m, m])) };

const browser = await chromium.launch();
const ctx = await browser.newContext();
await ctx.route(`${ORIGIN}/**`, (route) => {
  const file = FILES[new URL(route.request().url()).pathname];
  if (!file) return route.fulfill({ status: 404, body: '' });
  let body = readFileSync(join(here, '..', file), 'utf8');
  if (file === 'index.html') {
    body = tileHtml(body).replace(/<link rel="stylesheet" href="\/vendor\/theme.css">/,
      '<style>:root{--bx-border:#ccc;--bx-panel:#fff;--bx-panel-2:#f4f4f4;--bx-text:#111;' +
      '--bx-muted:#777;--bx-accent:#b57e10;--bx-mono:monospace;--bx-red:#c33;--bx-green:#3a3}</style>');
  }
  route.fulfill({ contentType: file.endsWith('.js') ? 'text/javascript' : 'text/html', body });
});
await serveKit(ctx);
await ctx.route('**/vendor/lit-all.min.js', (r) => r.fulfill({ contentType: 'text/javascript',
  body: readFileSync(process.env.BX_VENDOR ? join(process.env.BX_VENDOR, 'lit-all.min.js') : join(here, '..', '..', '..', 'web', 'vendor', 'lit-all.min.js'), 'utf8') }));
await ctx.route('**/vendor/marked.esm.js', (r) =>
  r.fulfill({ contentType: 'text/javascript', body: 'export const marked={parse:(s)=>s,use(){}};' }));

// prefs survive a reload through the opener's storage, like the real
// server-side prefs do.
const prefs = {};
await ctx.exposeBinding('__prefPut', (_, k, v) => { prefs[k] = v; });
await ctx.exposeBinding('__prefGet', (_, k) => prefs[k]);

await ctx.addInitScript(() => {
  // The real frame: no storage, and touching it throws.
  const deny = { get() { throw new DOMException('The document is sandboxed', 'SecurityError'); } };
  Object.defineProperty(window, 'localStorage', deny);
  Object.defineProperty(window, 'sessionStorage', deny);

  window.__calls = [];
  const runs = [
    { id: 1, title: 'what is the weather', kind: 'quick', status: 'done', result: '', last: 'Sunny, 21°C.', updated: Date.now() / 1000 - 30 },
    { id: 2, title: 'plan the offsite', kind: '', status: 'idle', result: '', updated: Date.now() / 1000 - 600 },
    { id: 3, title: 'slow one', kind: '', status: 'idle', result: '', updated: 1 },
  ];
  const json = (v, status = 200) => ({ ok: status < 400, status, json: async () => v });
  window.xbin = {
    self: 'apps/agent',
    fetch: async (url, opt = {}) => {
      const method = opt.method || 'GET';
      window.__calls.push({ method, url, body: opt.body });
      if (url.startsWith('/api/xbin/prefs/')) {
        const k = url.split('/').pop();
        if (method === 'PUT') { await window.__prefPut(k, JSON.parse(opt.body)); return json({}); }
        const v = await window.__prefGet(k);
        return v === undefined ? json({}, 404) : json(v);
      }
      if (url.endsWith('/runs') || url.endsWith('/runs?roots=1')) return json(runs);
      if (url.includes('/conversations')) {
        const items = runs.map((r) => ({ access: 'owner', mine: true, origin: 'chat', activityMs: r.updated * 1000, ...r }))
          .sort((a, b) => b.activityMs - a.activityMs);
        return json({ pinned: [], items, next: '' });
      }
      if (url.endsWith('/needs')) return json({ items: [{ run: runs.find((r) => r.id === 2), reason: 'question', subRun: 0 }] });
      if (url.includes('/stream')) return new Response(new ReadableStream({ start() {} }), { headers: { 'Content-Type': 'text/event-stream' } });
      if (url.endsWith('/ask')) {
        const b = JSON.parse(opt.body);
        runs.unshift({ id: 9, title: b.text, kind: 'quick', status: 'running', updated: Date.now() / 1000 });
        return json(runs[0]);
      }
      const m = url.match(/\/runs\/(\d+)\/view$/);
      if (m) {
        const id = +m[1];
        if (id === 3) await new Promise((r) => setTimeout(r, 700)); // a slow response
        const run = runs.find((r) => r.id === id);
        return json({ cursor: 'g.1', run: { pendingState: {}, ...run }, messages: [], steps: [], links: [], queued: [], drafts: [],
          chain: [], memory: {}, config: {}, files: [], messageFiles: {} });
      }
      return json({});
    },
    bus: { on: () => () => {} },
    iface: () => null,
  };
});

const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
await page.setViewportSize({ width: 800, height: 800 });
await page.goto(`${ORIGIN}/`);
await page.waitForSelector('.home .qa', { timeout: 5000 }).catch(() => {});

ok('the tile boots with localStorage denied', errors.length === 0, errors.join(' | '));
const card = await page.$eval('.home .qa', (e) => e.textContent).catch(() => '');
ok('what needs you shows on home', card.includes('plan the offsite') && card.includes('has a question'), card);
await page.waitForSelector('#runs .run');
const side = await page.$$eval('#runs .run .t', (els) => els.map((e) => e.textContent));
ok('the sidebar lists every conversation, quick asks included', side.includes('plan the offsite') && side.includes('what is the weather'), side.join(' | '));
ok('the composer is enabled on home', !(await page.$eval('#msg', (e) => e.disabled)));

// The lane toggle persists through prefs, and a reload picks it up.
ok('the lane starts private', (await page.textContent('#tset')).includes('🔒'));
await page.click('#tset');
await page.waitForFunction(() => window.__calls.some((c) => c.method === 'PUT' && c.url.endsWith('/prefs/toolset')));
ok('toggling saves the lane as a pref', prefs.toolset === 'web', JSON.stringify(prefs));
await page.reload();
await page.waitForFunction(() => document.getElementById('tset').textContent.includes('🌐'), { timeout: 3000 }).catch(() => {});
ok('the lane survives a reload', (await page.textContent('#tset')).includes('🌐'));

// Asking from home starts a quick ask in the chosen lane and opens it.
await page.fill('#msg', 'is the build green?');
await page.press('#msg', 'Enter');
await page.waitForFunction(() => document.querySelector('#top .title')?.textContent === 'is the build green?', { timeout: 3000 }).catch(() => {});
const ask = await page.evaluate(() => window.__calls.find((c) => c.url.endsWith('/ask')));
ok('home sends POST /ask with the lane', ask && JSON.parse(ask.body).toolset === 'web' && JSON.parse(ask.body).text === 'is the build green?', JSON.stringify(ask));
ok('…and opens the new run', (await page.textContent('#top .title')) === 'is the build green?');

// A slow response for a run you already left must not repaint over the new one.
await page.click('#home');
await page.click('#runs .run:has-text("slow one")');
await page.click('#runs .run:has-text("plan the offsite")');
await page.waitForTimeout(1200);
ok('a stale response does not repaint the run you moved to', (await page.textContent('#top .title')) === 'plan the offsite',
  await page.textContent('#top .title'));

await browser.close();
console.log(failures ? `\n${failures} FAILURE(S)` : 'all home checks passed');
process.exit(failures ? 1 : 0);
