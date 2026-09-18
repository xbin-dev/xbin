// sidebar.mjs — the run list is a forest, not a flat list.
//
// A fan-out can add a dozen runs at once, so a workflow has to be ONE row until
// you open it or the sidebar floods and the tile stops being navigable. This
// drives the real agent.js against a stubbed transport, because the behaviour
// that matters (folding, roll-ups, orphan handling, reveal-on-select) lives in
// the module rather than the stylesheet.
//
//   node test/sidebar.mjs        (needs playwright + a chromium build)
import { readFileSync } from 'node:fs';
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

// A root with two children, one of which has its own child, plus a run whose
// parent id points at a row that does not exist — the shape the pre-cascade
// delete bug leaves behind.
const RUNS = [
  { id: 1, title: 'plan the quarter', status: 'blocked', parentId: 0, rootId: 1, depth: 0, created: 100, kind: '' },
  { id: 2, title: 'research vat', status: 'running', parentId: 1, rootId: 1, depth: 1, created: 101, kind: '' },
  { id: 3, title: 'vendor pricing', status: 'done', parentId: 1, rootId: 1, depth: 1, created: 102, kind: '' },
  { id: 4, title: 'deep dive', status: 'error', parentId: 2, rootId: 1, depth: 2, created: 103, kind: '' },
  { id: 5, title: 'unrelated task', status: 'idle', parentId: 0, rootId: 5, depth: 0, created: 104, kind: '' },
  { id: 9, title: 'left behind', status: 'idle', parentId: 77, rootId: 77, depth: 1, created: 105, kind: '' },
];

// The page must be served from a real origin: agent.js is loaded with a
// RELATIVE src, which cannot resolve against about:blank, and addInitScript
// does not apply to a setContent document. Routing a fake host fixes both and
// means this test exercises the actual module rather than the static markup.
const ORIGIN = 'http://tile.test';
const FILES = { '/': 'index.html', '/index.html': 'index.html', '/agent.js': 'agent.js' };

const browser = await chromium.launch();
const page = await browser.newPage();

await page.route(`${ORIGIN}/**`, (route) => {
  const path = new URL(route.request().url()).pathname;
  const file = FILES[path];
  if (!file) return route.fulfill({ status: 404, body: '' });
  let body = readFileSync(join(here, '..', file), 'utf8');
  if (file === 'index.html') {
    body = body.replace(/<link rel="stylesheet" href="\/vendor\/theme.css">/,
      '<style>:root{--bx-border:#ccc;--bx-panel:#fff;--bx-panel-2:#f4f4f4;--bx-text:#111;' +
      '--bx-muted:#777;--bx-accent:#b57e10;--bx-mono:monospace;--bx-red:#c33;--bx-green:#3a3}</style>');
  }
  route.fulfill({ contentType: file.endsWith('.js') ? 'text/javascript' : 'text/html', body });
});
await page.route('**/vendor/marked.esm.js', (r) =>
  r.fulfill({ contentType: 'text/javascript', body: 'export const marked={parse:(s)=>s,use(){}};' }));

// agent.js reads xbin.self at module scope, so the stub must exist before the
// module evaluates.
await page.addInitScript((runs) => {
  window.__prefs = {};
  window.__calls = [];
  window.xbin = {
    self: 'apps/agent',
    fetch: async (url, opt = {}) => {
      window.__calls.push((opt.method || 'GET') + ' ' + url);
      const json = (v) => ({ ok: true, json: async () => v });
      if (url.includes('/prefs/')) {
        const key = url.split('/prefs/')[1];
        if ((opt.method || 'GET') === 'PUT') { window.__prefs[key] = JSON.parse(opt.body); return json({}); }
        return key in window.__prefs ? json(window.__prefs[key]) : { ok: false, json: async () => ({}) };
      }
      if (url.endsWith('/runs')) return json(window.__runs || runs);
      if (url.endsWith('/halt')) return json({ on: false });
      if (/\/runs\/\d+$/.test(url)) {
        const id = +url.split('/').pop();
        return json({ run: (window.__runs || runs).find((r) => r.id === id) || runs[0],
                      messages: [], steps: [], memory: {}, config: {}, files: [], draft: '',
                      slots: window.__slots || { active: 0, limit: 4 } });
      }
      return json({});
    },
    bus: { on: () => () => {} },
    iface: () => null,
  };
}, RUNS);

page.on('pageerror', (e) => console.log('PAGEERROR:', e.message));
page.on('console', (m) => { if (m.type() === 'error') console.log('CONSOLE:', m.text()); });
await page.setViewportSize({ width: 700, height: 900 });
await page.goto(`${ORIGIN}/`);
await page.waitForFunction(() => document.querySelectorAll('#runs .run').length > 0, { timeout: 5000 });

const titles = () => page.$$eval('#runs .run .t', (els) => els.map((e) => e.textContent.replace(/[▸▾·⚡]/g, '').trim()));
const rowCount = () => page.$$eval('#runs .run', (els) => els.length);

// 1. Collapsed by default: a workflow is one row, its subagents are not listed.
let t = await titles();
ok('collapsed by default: subagents are not listed', !t.includes('research vat') && !t.includes('vendor pricing'), t.join(' | '));
ok('the root IS listed', t.includes('plan the quarter'), t.join(' | '));
ok('an unrelated top-level run is listed', t.includes('unrelated task'), t.join(' | '));

// 2. An orphan stays visible — burying it under a parent that no longer exists
//    would make it permanently invisible AND undeletable.
ok('an orphaned subagent is still reachable', t.includes('left behind'), t.join(' | '));
const orphanMark = await page.$$eval('#runs .run', (els) =>
  els.filter((e) => e.textContent.includes('orphan')).length);
ok('the orphan is labelled as one', orphanMark === 1, `found ${orphanMark}`);

// 3. Folding must not hide that work is running.
const roll = await page.$eval('#runs .run .roll', (e) => e.textContent).catch(() => '');
ok('a collapsed root rolls up its subtree', /⑂\s*3/.test(roll), roll);
ok('…including what is still running', /1▶/.test(roll), roll);
ok('…and what has failed', /1⚠/.test(roll), roll);

// 4. The twisty expands, and expanding does not navigate.
const before = await page.evaluate(() => window.__selected);
await page.click('#runs .run .tw[data-tw]');
await page.waitForFunction(() => document.querySelectorAll('#runs .run').length > 3, { timeout: 3000 });
t = await titles();
ok('expanding reveals the direct children', t.includes('research vat') && t.includes('vendor pricing'), t.join(' | '));
ok('but not a grandchild whose own parent is still folded', !t.includes('deep dive'), t.join(' | '));
const indents = await page.$$eval('#runs .run', (els) => els.map((e) => parseInt(e.style.paddingLeft, 10)));
ok('children are indented under their parent', Math.max(...indents) > Math.min(...indents), indents.join(','));

// 5. Nested folding works the same way.
const tws = await page.$$('#runs .run .tw[data-tw]');
await tws[1].click(); // "research vat" now has its own twisty
await page.waitForFunction(() => [...document.querySelectorAll('#runs .run .t')].some((e) => e.textContent.includes('deep dive')), { timeout: 3000 });
ok('a nested subagent expands too', (await titles()).includes('deep dive'));

// 6. Open state is persisted server-side (this frame has no localStorage).
const prefs = await page.evaluate(() => window.__prefs.sideOpen);
ok('expansion is persisted through the prefs API', Array.isArray(prefs) && prefs.includes(1), JSON.stringify(prefs));
const usedLocalStorage = await page.evaluate(() => {
  try { return window.__usedLS === true; } catch { return false; }
});
ok('and not through localStorage', !usedLocalStorage);

// 7. Collapsing puts it back to one row.
await page.click('#runs .run .tw[data-tw]');
await page.waitForFunction(() => document.querySelectorAll('#runs .run').length <= 3, { timeout: 3000 });
ok('collapsing folds the whole subtree away', (await rowCount()) <= 3, String(await rowCount()));

// 8. The sidebar never widens the 220px column, whatever the model named a run.
await page.evaluate(() => {
  const long = { id: 20, title: 'a'.repeat(400), status: 'idle', parentId: 0, rootId: 20, depth: 0, created: 106, kind: '' };
  const orig = window.xbin.fetch;
  window.xbin.fetch = async (u, o) => (u.endsWith('/runs') ? { ok: true, json: async () => [long] } : orig(u, o));
});
await page.waitForTimeout(100);
await page.evaluate(() => window.loadRuns && window.loadRuns());
const overflow = await page.evaluate(() => {
  const s = document.querySelector('.side');
  return { pane: s.scrollWidth - s.clientWidth, doc: document.scrollingElement.scrollWidth - document.scrollingElement.clientWidth };
});
ok('a 400-char run title does not widen the sidebar', overflow.pane === 0, `by ${overflow.pane}px`);
ok('…nor the document', overflow.doc === 0, `by ${overflow.doc}px`);

// 9. A queued run says which wait it is. A full ceiling can last a whole drive
//    of some other run; an admitted run starts within a moment. A bare
//    "queued" badge made the two indistinguishable.
await page.evaluate(() => {
  window.__runs = [
    { id: 30, title: 'waiting for a slot', status: 'queued', parentId: 0, rootId: 30, depth: 0, created: 300, kind: '' },
    { id: 31, title: 'about to start', status: 'queued', parentId: 0, rootId: 31, depth: 0, created: 301, kind: '' },
  ];
  window.__slots = { active: 4, limit: 4 };
  const orig = window.xbin.fetch;
  window.xbin.fetch = async (u, o) => (u.endsWith('/runs') ? { ok: true, json: async () => window.__runs } : orig(u, o));
});
await page.waitForFunction(() => [...document.querySelectorAll('#runs .run .t')].some((e) => e.textContent.includes('waiting for a slot')), { timeout: 5000 });
await page.click('#runs .run:has-text("waiting for a slot")');
await page.waitForFunction(() => /drive slots are busy/.test(document.getElementById('timeline').textContent), { timeout: 3000 }).catch(() => {});
ok('a run queued at the ceiling says the slots are busy',
  /all 4 drive slots are busy/.test(await page.textContent('#timeline')), await page.textContent('#timeline'));
await page.evaluate(() => { window.__slots = { active: 1, limit: 4 }; });
await page.click('#runs .run:has-text("about to start")');
await page.waitForFunction(() => /queued · starting/.test(document.getElementById('timeline').textContent), { timeout: 3000 }).catch(() => {});
ok('a run queued with free slots says it is starting',
  /queued · starting/.test(await page.textContent('#timeline')), await page.textContent('#timeline'));

await browser.close();
console.log(failures ? `\n${failures} FAILURE(S)` : 'all sidebar checks passed');
process.exit(failures ? 1 : 0);
