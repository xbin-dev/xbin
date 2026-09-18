// collapse.mjs — tool calls and results fold to a line or two, and what you
// opened STAYS open.
//
// The timeline is rebuilt with innerHTML whenever the poll sees a change, which
// is continuously while a run is working. A native <details> snaps shut on each
// rebuild, so the one output you were reading closed under you every 1.5s.
// This drives the real agent.js against a stubbed transport, adds a message
// between polls, and checks the open result survives the rebuild.
//
//   node test/collapse.mjs        (needs playwright + a chromium build)
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

// agent.js is loaded with a RELATIVE src, so the page needs a real origin.
const ORIGIN = 'http://tile.test';
const FILES = { '/': 'index.html', '/index.html': 'index.html', '/agent.js': 'agent.js' };

const browser = await chromium.launch();
const page = await browser.newPage();
await page.route(`${ORIGIN}/**`, (route) => {
  const file = FILES[new URL(route.request().url()).pathname];
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

const LONG = Array.from({ length: 40 }, (_, i) => `line ${i} of a long tool result`).join('\n');
await page.addInitScript((long) => {
  const calls = JSON.stringify([{ id: 'c1', type: 'function', function: { name: 'xbin_call', arguments: '{"path":"/api/apps/x/items","method":"GET"}' } }]);
  window.__msgs = [
    { id: 1, role: 'user', content: 'list the items' },
    { id: 2, role: 'assistant', content: '', toolCalls: calls },
    { id: 3, role: 'tool', name: 'xbin_call', toolCallId: 'c1', content: long },
    { id: 4, role: 'tool', name: 'xbin_call', toolCallId: 'c2', content: 'error: 403 forbidden' },
  ];
  const json = (v) => ({ ok: true, json: async () => v });
  window.xbin = {
    self: 'apps/agent',
    fetch: async (url) => {
      if (url.endsWith('/runs')) return json([{ id: 1, title: 'items', status: 'running', created: 1 }]);
      if (/\/runs\/1$/.test(url)) {
        return json({ run: { id: 1, title: 'items', status: 'running', updated: window.__msgs.length },
                      messages: window.__msgs, steps: [], memory: {}, config: {}, draft: '' });
      }
      return json({});
    },
    bus: { on: () => () => {} },
    iface: () => null,
  };
}, LONG);

page.on('pageerror', (e) => { console.log('PAGEERROR:', e.message); failures++; });
await page.setViewportSize({ width: 700, height: 900 });
await page.goto(`${ORIGIN}/`);
await page.waitForSelector('#runs .run');
await page.click('#runs .run');
await page.waitForSelector('.ev.tool');

const height = (sel) => page.$eval(sel, (e) => e.getBoundingClientRect().height);
const collapsed = await height('.ev.tool .clampable');
ok('a long result starts folded to a couple of lines', collapsed < 60, `h=${collapsed}`);
ok('the fold says how much is hidden', /chars/.test(await page.$eval('.ev.tool .role', (e) => e.textContent)));
ok('a tool call starts on one line', (await height('.tc')) < 30, `h=${await height('.tc')}`);
ok('a failed call is flagged without expanding it', await page.evaluate(() =>
  [...document.querySelectorAll('.ev.tool')].some((e) => e.classList.contains('bad') && /403/.test(e.textContent))));

await page.click('.ev.tool .role');
const open = await height('.ev.tool .clampable');
ok('clicking opens the whole result', open > 400, `h=${open}`);

// The agent keeps working: a new message arrives, the poll rebuilds the timeline.
await page.evaluate(() => { window.__msgs.push({ id: 5, role: 'assistant', content: 'done' }); });
await page.waitForFunction(() => document.querySelectorAll('.ev').length >= 5, { timeout: 4000 });
const after = await height('.ev.tool .clampable');
ok('it stays open across a timeline rebuild', after > 400, `h=${after}`);

await page.click('.ev.tool .role');
ok('clicking again folds it', (await height('.ev.tool .clampable')) < 60);

await browser.close();
console.log(failures ? `\n${failures} FAILURE(S)` : 'all collapse checks passed');
process.exit(failures ? 1 : 0);
