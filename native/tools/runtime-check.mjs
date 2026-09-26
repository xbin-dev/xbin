#!/usr/bin/env node
// runtime-check.mjs — a native tile the way the app runs it (plans/native.md
// §7.2), minus SwiftUI: its runtime document (`/c/<tile>/?native=1`) from a
// running xbind, loaded with the tile's frame token in headless Chromium,
// with the app's document-start script (RuntimeScript.documentStart, printed
// by `swift run app-live runtime-js`) and a stand-in for WebKit's `xbn`
// handler. The runtime's messages (JSON strings) go through XbinCore's
// TreeStore (`app-live tree-check`); a tap is sent back as the app sends it
// (`app-live event-js` → the callAsyncJavaScript body) and the patch it
// causes goes through TreeStore too.
//
//   PLAYWRIGHT_DIR=~/lcad-wasm node native/tools/runtime-check.mjs http://127.0.0.1:9462 apps/counter admin admin
//
// Needs an xbind with native runtime documents (web/xb-native.js, ?native=1)
// and a tile whose native UI has a button with @tap — the counter below:
//   import { html, render } from '/vendor/xb-native.js';
//   let n = 0; const paint = () => render(html`<screen title="Counter" style="form"><section>
//     <row title="Count" detail=${String(n)}/><button role="primary" @tap=${() => { n++; paint(); }}>+1</button>
//   </section></screen>`); paint();
// VOCAB=<vocab.json> overrides native/spec/vocab.json (the runtime WP's).
// Prints "RUNTIME OK" and exits 0 when every check passes.
import { execFileSync } from 'node:child_process';
import { writeFileSync, mkdtempSync } from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

const [base = 'http://127.0.0.1:9462', tile = 'apps/counter', user = 'admin', pass = 'admin'] = process.argv.slice(2);
const repo = resolve(new URL('../..', import.meta.url).pathname);
const require = createRequire(join(process.env.PLAYWRIGHT_DIR || repo, 'node_modules', 'x.js'));
const { chromium } = require('playwright');
const live = (...args) => execFileSync('swift', ['run', '-q', 'app-live', ...args],
  { cwd: join(repo, 'native/tools/app-check'), encoding: 'utf8' });

let failed = 0;
const check = (ok, what) => { console.log(`${ok ? '  ok  ' : '  FAIL'} ${what}`); if (!ok) failed++; };

const login = await (await fetch(`${base}/api/xbin/login`, {
  method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: user, password: pass }),
})).json();
const { token } = await (await fetch(`${base}/api/xbin/frame-token?component=${encodeURIComponent(tile)}`,
  { headers: { Authorization: `Bearer ${login.token}` } })).json();
const docStart = live('runtime-js', process.env.VOCAB || join(repo, 'native/spec/vocab.json'));

const browser = await chromium.launch();
const ctx = await browser.newContext({ extraHTTPHeaders: { 'X-XBin-Client': 'app/runtime-check' } });
await ctx.addInitScript(() => {
  window.__xbn = [];
  window.webkit = { messageHandlers: { xbn: { postMessage: (s) => window.__xbn.push(s) } } };
});
await ctx.addInitScript({ content: docStart });
// The app's scheme handler: every request the document makes carries the
// tile's frame token (plans/native.md §6.1).
await ctx.route(`${base}/**`, (route) => route.continue({
  headers: { ...route.request().headers(), 'x-xbin-frame-token': token, 'x-xbin-client': 'app/runtime-check' },
}));
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(String(e)));
await page.goto(`${base}/c/${tile}/?native=1`);
await page.waitForFunction(() => window.__xbn.some((s) => s.includes('"op":"mount"')), null, { timeout: 5000 })
  .catch(() => {});

const dir = mkdtempSync(join(tmpdir(), 'rtcheck-'));
const run = (msgs) => { writeFileSync(join(dir, 'm.json'), JSON.stringify(msgs)); return JSON.parse(live('tree-check', join(dir, 'm.json'))); };

let msgs = await page.evaluate(() => window.__xbn.slice());
check(msgs.every((m) => typeof m === 'string'), `the runtime posts JSON strings to xbn (${msgs.length} messages)`);
check(await page.evaluate(() => window.xbin?.native?.caps?.renderer === 'ios' && window.xbin?.native?.state?.scroll === 3),
  'the injected caps and saved state reached xbin.native');
let r = run(msgs);
check(r.failure === null && r.root?.t === 'screen', `TreeStore mounted the tree: ${r.events.join(', ')}`);

// The button's key, then a tap exactly as the app sends it.
const find = (n, t) => (n.t === t ? n : (n.c || []).map((c) => find(c, t)).find(Boolean));
const button = find(r.root, 'button');
const row = find(r.root, 'row');
check(!!button && (button.e || []).includes('tap'), `a button listening to tap: ${button?.k}`);
const before = row?.p?.detail;
const body = live('event-js', button.k, 'tap', String(r.n));
const handled = await page.evaluate(`(async () => { ${body} })()`);
check(handled === true, 'xbn.event(...) ran the tile\'s handler');
await page.evaluate(`(async () => { ${live('event-js', button.k, 'tap', String(r.n)).replace(/xbn\.event\(.*\)/, 'xbn.frame()')} })()`);
await page.waitForFunction((n) => window.__xbn.length > n, msgs.length, { timeout: 3000 }).catch(() => {});
msgs = await page.evaluate(() => window.__xbn.slice());
r = run(msgs);
const after = find(r.root, 'row')?.p?.detail;
check(r.failure === null && after !== before, `the patch applied: detail ${JSON.stringify(before)} → ${JSON.stringify(after)} (${r.events.join(', ')})`);
check(errors.length === 0, `no page errors${errors.length ? ': ' + errors.join('; ') : ''}`);

await browser.close();
console.log(failed === 0 ? 'RUNTIME OK' : `RUNTIME FAILED (${failed})`);
process.exit(failed === 0 ? 0 : 1);
