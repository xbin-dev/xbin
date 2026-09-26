#!/usr/bin/env node
// bridge-check.mjs — the app's tile bridge (plans/native.md §6.2) in a real
// browser engine: a tile page from a running xbind, loaded with its frame
// token, with the app's user script (TileBridge.userScript, printed by
// `swift run app-live bridge-js`) and a stand-in for WebKit's `xbin` message
// handler. It checks that xbin-client.js sends xbin.dialog / xbin.window to
// the app when it is top-level, that the app's xbin:reply (TileBridge.
// replyScript) resolves them, and that a frame inside the page can't speak
// for the tile.
//
//   PLAYWRIGHT_DIR=~/lcad-wasm node native/tools/bridge-check.mjs http://127.0.0.1:9461 admin admin
//
// Needs an xbind serving this checkout's web/ (--dev) and `swift` on PATH.
// Prints "BRIDGE OK" and exits 0 when every check passes.
import { execFileSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { join, resolve } from 'node:path';

const [base = 'http://127.0.0.1:9461', user = 'admin', pass = 'admin'] = process.argv.slice(2);
const repo = resolve(new URL('../..', import.meta.url).pathname);
const require = createRequire(join(process.env.PLAYWRIGHT_DIR || repo, 'node_modules', 'x.js'));
const { chromium } = require('playwright');

let failed = 0;
const check = (ok, what) => { console.log(`${ok ? '  ok  ' : '  FAIL'} ${what}`); if (!ok) failed++; };

const bridge = JSON.parse(execFileSync('swift', ['run', '-q', 'app-live', 'bridge-js'],
  { cwd: join(repo, 'native/tools/app-check'), encoding: 'utf8' }));

const login = await (await fetch(`${base}/api/xbin/login`, {
  method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: user, password: pass }),
})).json();
const comps = await (await fetch(`${base}/api/xbin/components`, { headers: { Authorization: `Bearer ${login.token}` } })).json();
const tile = comps.find((c) => !c.chrome && c.hasIndex && c.path !== 'root' && c.path !== 'shell').path;
const { token } = await (await fetch(`${base}/api/xbin/frame-token?component=${encodeURIComponent(tile)}`,
  { headers: { Authorization: `Bearer ${login.token}` } })).json();

const browser = await chromium.launch();
const ctx = await browser.newContext();
// WebKit's message handler, as the app registers it (page world).
await ctx.addInitScript(() => {
  window.__posted = [];
  window.webkit = { messageHandlers: { xbin: { postMessage: (s) => window.__posted.push(s) } } };
});
await ctx.addInitScript({ content: bridge.userScript });
const page = await ctx.newPage();
await page.goto(`${base}/c/${tile}/?frame=${encodeURIComponent(token)}`);
await page.waitForFunction(() => !!window.xbin);

// xbin.dialog → the app → xbin:reply → resolved.
await page.evaluate(() => {
  window.__dialog = null;
  window.xbin.dialog({ title: 'Name?', fields: [{ name: 'name' }] }).then((r) => { window.__dialog = r; });
});
await page.waitForFunction(() => window.__posted.length > 0);
const posted = JSON.parse((await page.evaluate(() => window.__posted))[0]);
check(posted.type === 'xbin:dialog' && posted.spec?.title === 'Name?' && typeof posted.id === 'string',
  `xbin.dialog reached the app handler as ${posted.type}`);
await page.evaluate(bridge.reply.replace('"__ID__"', JSON.stringify(posted.id)));
await page.waitForFunction(() => window.__dialog !== null, null, { timeout: 3000 }).catch(() => {});
const res = await page.evaluate(() => window.__dialog);
check(res?.button === 'ok' && res?.values?.name === 'Ada', `the app's reply resolved it: ${JSON.stringify(res)}`);

// xbin.window → posted; the app's close reply resolves `closed`.
await page.evaluate(() => {
  window.__closed = false;
  const h = window.xbin.window({ path: 'editor', title: 'Edit' });
  window.__win = h.id;
  h.closed.then(() => { window.__closed = true; });
});
await page.waitForFunction(() => window.__posted.length > 1);
const win = JSON.parse((await page.evaluate(() => window.__posted))[1]);
check(win.type === 'xbin:window' && win.spec?.path === 'editor', 'xbin.window reached the app handler');
await page.evaluate(bridge.closeReply.replace('"__ID__"', JSON.stringify(win.id)));
await page.waitForFunction(() => window.__closed, null, { timeout: 3000 }).catch(() => {});
check(await page.evaluate(() => window.__closed), "the app's close reply resolved handle.closed");

// A frame inside the page posting to it is not relayed (only the page speaks for the tile).
const before = (await page.evaluate(() => window.__posted)).length;
await page.evaluate(() => new Promise((done) => {
  const f = document.createElement('iframe');
  f.srcdoc = "<script>parent.postMessage({type:'xbin:dialog', id:'evil', spec:{title:'x'}}, '*')</script>";
  f.onload = () => setTimeout(done, 200);
  document.body.appendChild(f);
}));
check((await page.evaluate(() => window.__posted)).length === before, 'a nested frame cannot post xbin:* for the tile');

// Without the app's handler (a browser), xbin-client keeps its in-frame fallback.
const plain = await browser.newPage();
await plain.goto(`${base}/c/${tile}/?frame=${encodeURIComponent(token)}`);
await plain.waitForFunction(() => !!window.xbin);
const standalone = await plain.evaluate(() => { const h = window.xbin.window({ path: 'x' }); return h.id === null; });
check(standalone, 'in a plain top-level page xbin.window stays a no-op (unchanged)');

await browser.close();
console.log(failed === 0 ? 'BRIDGE OK' : `BRIDGE FAILED (${failed})`);
process.exit(failed === 0 ? 0 : 1);
