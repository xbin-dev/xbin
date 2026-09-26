// viewport.test.mjs — the app's viewport script for tile pages (plans/
// native.md §6.3; XbinCore's TileViewport.userScript, lifted verbatim from
// the Swift source) in a browser engine with a phone's viewport: Chromium's
// mobile emulation honours <meta name="viewport"> as mobile WebKit does
// (layout width, fit-to-width initial scale). The simulator run of the same
// pages is XbinE2ETests.test02/test06 (native/AGENTS.md → Mac mini).
// Needs Playwright with Chromium (PLAYWRIGHT_DIR, default ~/lcad-wasm);
// without it the tests skip.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { serve, playwright } from './shots.mjs';

const ROOT = new URL('../..', import.meta.url).pathname;
const SWIFT = join(ROOT, 'native/ios/Packages/XbinCore/Sources/XbinCore/Client/TileViewport.swift');
const TILES = join(ROOT, 'native/ios/scripts/testdata/e2e-tiles');

/** The script as Swift has it: the multi-line literal, dedented by its closing delimiter. */
export function viewportScript() {
  const m = readFileSync(SWIFT, 'utf8').match(/static let userScript = """\n([\s\S]*?)\n( *)"""/);
  if (!m) throw new Error('TileViewport.userScript not found in ' + SWIFT);
  const indent = m[2].length;
  return m[1].split('\n').map((l) => l.slice(Math.min(indent, l.length - l.trimStart().length))).join('\n');
}

let pw = null;
try { pw = playwright(); } catch { /* skipped below */ }
const skip = pw ? false : 'Playwright not found (set PLAYWRIGHT_DIR)';

const W = 402, H = 874; // an iPhone 17 Pro / 18 Pro in points
/** The meta the script writes for a layout `w` px wide on a `dev` px screen: that width, fitted. */
const grown = (w, dev = W) => { const s = Math.floor((dev / w) * 10000) / 10000; return `width=${w}, initial-scale=${s}, minimum-scale=${s}`; };
const page = (body, head = '') =>
  `<!doctype html><html><head><meta charset="utf-8"><title>t</title>${head}</head><body>${body}</body></html>`;
const PAGES = {
  '/wide.html': readFileSync(join(TILES, 'wide/index.html'), 'utf8'),
  '/phone.html': readFileSync(join(TILES, 'phone/index.html'), 'utf8'),
  '/fluid.html': page('<h2 style="font-size:13px">the mental model</h2><p>' + 'A fluid card. '.repeat(40) + '</p>'),
  '/late.html': page('<p>a table arrives after load</p><script>addEventListener("load", () => setTimeout(() => {' +
    ' const t = document.createElement("div"); t.style.width = "1000px"; t.textContent = "late"; document.body.append(t); }, 600));</script>'),
  '/huge.html': page('<div style="width:3000px">huge</div>'),
  '/vw.html': page('<div style="width:calc(100vw + 40px)">always a little wider</div>'),
  '/theirs-late.html': page('<p>sets its own viewport from a script at the end of body</p><script>' +
    'const m = document.createElement("meta"); m.name = "viewport"; m.content = "width=500"; document.head.append(m);</script>'),
  '/Case.html': page('<div style="width:1200px">x</div>', '<meta name="Viewport" content="width=device-width">'),
  '/plain.txt': 'a text file opened as a page\n',
};

let srv, browser, base, script;
before(async () => {
  if (skip) return;
  script = viewportScript();
  srv = await serve(PAGES);
  base = `http://127.0.0.1:${srv.address().port}`;
  browser = await pw.chromium.launch();
});
after(async () => { await browser?.close(); srv?.close(); });

async function open(path, size = { width: W, height: H }) {
  const ctx = await browser.newContext({ viewport: size, deviceScaleFactor: 3, isMobile: true, hasTouch: true });
  await ctx.addInitScript({ content: script });
  const p = await ctx.newPage();
  await p.goto(base + path, { waitUntil: 'load' });
  return { ctx, p };
}

/** What the page ends up with, once the script has settled (it re-checks within ~0.5 s). */
async function state(p, wait = 700) {
  await p.waitForTimeout(wait);
  return p.evaluate(() => ({
    layout: document.documentElement.clientWidth,
    scroll: document.documentElement.scrollWidth,
    visible: Math.round(window.visualViewport.width),
    metas: Array.from(document.querySelectorAll('meta[name]')).filter((m) => m.name.toLowerCase() === 'viewport')
      .map((m) => m.getAttribute('content')),
  }));
}

test('the script is lifted verbatim', { skip }, () => {
  assert.match(script, /^\(\(\) => \{/);
  assert.match(script, /\}\)\(\);$/);
  assert.ok(!script.includes('\\'), 'no escapes in the Swift literal');
});

test('a fluid desktop-first page is laid out at the device width, 1:1', { skip }, async () => {
  const { ctx, p } = await open('/fluid.html');
  const s = await state(p);
  assert.deepEqual(s.metas, ['width=device-width, initial-scale=1']);
  assert.equal(s.layout, W);
  assert.equal(s.visible, W, 'not scaled');
  await ctx.close();
});

test('a wide page gets its own width, fitted to the screen', { skip }, async () => {
  const { ctx, p } = await open('/wide.html');
  const s = await state(p);
  assert.deepEqual(s.metas, [grown(1200)]);
  assert.equal(s.layout, 1200);
  assert.equal(s.visible, 1200, 'the whole width on screen (zoom-to-fit)');
  assert.equal(await p.textContent('#report'), 'layout 1200 metas 1');
  await ctx.close();
});

test('a page with its own viewport is left alone', { skip }, async () => {
  const { ctx, p } = await open('/phone.html');
  const s = await state(p);
  assert.deepEqual(s.metas, ['width=device-width, initial-scale=1']);
  assert.equal(await p.textContent('#report'), `viewport 1 width=device-width, initial-scale=1 layout ${W}`);
  await ctx.close();
  const c = await open('/Case.html');
  assert.deepEqual((await state(c.p)).metas, ['width=device-width'], '<meta name="Viewport"> is one too');
  await c.ctx.close();
});

test('a viewport the page sets later wins, and ours goes', { skip }, async () => {
  const { ctx, p } = await open('/theirs-late.html');
  const s = await state(p);
  assert.deepEqual(s.metas, ['width=500']);
  assert.equal(s.layout, 500);
  await ctx.close();
});

test('content that arrives after load still widens the layout', { skip }, async () => {
  const { ctx, p } = await open('/late.html');
  assert.equal((await state(p, 200)).layout, W);
  const s = await state(p, 1200);
  assert.deepEqual(s.metas, [grown(1008)], 'its 1000 px and the body margin');
  assert.equal(s.visible, 1008);
  await ctx.close();
});

test('growth is bounded: at most 1280 px, at most 4 steps', { skip }, async () => {
  const huge = await open('/huge.html');
  const h = await state(huge.p);
  assert.deepEqual(h.metas, [grown(1280)]);
  assert.equal(h.layout, 1280);
  assert.equal(h.visible, 1280);
  await huge.ctx.close();
  // 100vw + 40px outgrows every width: four steps, then it stops.
  const vw = await open('/vw.html');
  const v = await state(vw.p, 1500);
  const w = Number(/^width=(\d+)/.exec(v.metas[0])?.[1]);
  assert.ok(w > W && w <= W + 4 * 60, `grew a little, four times at most (${v.metas[0]})`);
  assert.deepEqual(await state(vw.p, 800), v, 'and stays');
  await vw.ctx.close();
});

test('a rotation starts again from the device width', { skip }, async () => {
  const { ctx, p } = await open('/fluid.html');
  await p.setViewportSize({ width: H, height: W });
  const s = await state(p);
  assert.deepEqual(s.metas, ['width=device-width, initial-scale=1']);
  assert.equal(s.layout, H);
  await ctx.close();
  const wide = await open('/wide.html');
  await wide.p.setViewportSize({ width: H, height: W });
  const w = await state(wide.p);
  assert.deepEqual(w.metas, [grown(1200, H)], 'fitted to the new width');
  assert.equal(w.visible, 1200);
  await wide.ctx.close();
});

test('only HTML documents', { skip }, async () => {
  const { ctx, p } = await open('/plain.txt');
  const s = await state(p);
  assert.deepEqual(s.metas, []);
  await ctx.close();
});
