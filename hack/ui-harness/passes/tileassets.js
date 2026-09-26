// hack/ui-harness/passes/tileassets.js — strict tile asset gating
// (docs/auth.md §Tile asset gating) in a real browser, under whichever mode
// the harness runs (TILE_ASSETS=legacy|tokens|origins, run.sh). A probe tile
// with a relative module graph, relative images, a stylesheet, one ABSOLUTE
// self-reference, a fragment link and a link to a second page:
//   - every mode: the relative graph loads; fragment links scroll without
//     navigating; pushState/replaceState keep location on the document URL;
//   - tokens: the absolute image fails with xbin-client's diagnostic, the
//     page link navigates (xbin-client re-credentials it);
//   - origins: the tile runs on its own t-<id> origin, the absolute image
//     loads (cookie), and it has its own localStorage — not shared with a
//     second tile;
//   - the tile report lists the absolute reference, `bx fix assets --write`
//     rewrites it, and after a reload it loads in every mode.
const path = require('path');
const { execFileSync } = require('child_process');
const { URL, fs, sleep, log, login, closeCtx, settle, openShell, usePersonalScreen, openTile, closeTile, tileFrame, shot, checker } = require('../lib');

const MODE = process.env.TILE_ASSETS || 'legacy';
const WS = process.env.WS;
const TILE = 'apps/assetprobe', TILE2 = 'apps/assetprobe2';
const DOT = '<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8"><rect width="8" height="8" fill="#f5a623"/></svg>';
const INDEX = `<!doctype html><html><head><meta charset="utf-8">
<link rel="stylesheet" href="style.css">
<script type="module" src="app.js"></script>
</head><body>
<div id="bg" class="bg">asset probe (${MODE})</div>
<img id="rel" src="img/dot.svg" width="8" height="8">
<img id="abs" src="/c/${TILE}/img/dot.svg" width="8" height="8">
<p><a id="frag" href="#target">to target</a> · <a id="page" href="page2.html?from=probe">page 2</a> · <a id="route" href="settings">route</a></p>
<div style="height:3000px"></div>
<div id="target">target</div>
</body></html>
`;
const FILES = {
  [`${TILE}/xbin.json`]: '{}\n',
  [`${TILE}/index.html`]: INDEX,
  [`${TILE}/style.css`]: '.bg { background: url(img/dot.svg) repeat-x; min-height: 24px }\n',
  [`${TILE}/app.js`]: `import { dep } from './lib/dep.js';
window.probe = { dep, loadedAt: performance.timeOrigin };
const im = new Image();
im.onload = () => { window.probe.relJs = true; };
im.onerror = () => { window.probe.relJs = false; };
im.src = 'img/dot.svg';
try { localStorage.setItem('probe', xbin.self); window.probe.storage = localStorage.getItem('probe'); } catch { window.probe.storage = 'none'; }
// a client-side router: handles its own relative links (document-level, like most)
document.addEventListener('click', (e) => {
  const a = e.target.closest?.('a#route');
  if (!a) return;
  e.preventDefault();
  history.pushState(null, '', a.getAttribute('href'));
  window.probe.routed = location.pathname;
});
`,
  [`${TILE}/lib/dep.js`]: "export const dep = 'ok';\n",
  [`${TILE}/img/dot.svg`]: DOT,
  [`${TILE}/page2.html`]: '<!doctype html><html><head></head><body><p id="p2">page two</p><img id="rel2" src="img/dot.svg"></body></html>\n',
  [`${TILE2}/xbin.json`]: '{}\n',
  [`${TILE2}/index.html`]: "<!doctype html><html><head></head><body><script type=\"module\">try { window.probe2 = { storage: localStorage.getItem('probe') }; } catch { window.probe2 = { storage: 'none' }; }</script>probe 2</body></html>\n",
};

function seed() {
  for (const [rel, body] of Object.entries(FILES)) {
    const p = path.join(WS, rel);
    fs.mkdirSync(path.dirname(p), { recursive: true });
    fs.writeFileSync(p, body);
  }
}

const probe = (f) => f.evaluate(() => window.probe ?? null);
// ready: the probe tile's CURRENT document has run (a live reload — the files
// were just written — can replace the first one, so re-find the frame).
async function ready(page) {
  const deadline = Date.now() + 20000;
  for (;;) {
    const f = await tileFrame(page, TILE);
    if (await f.evaluate(() => !!window.probe && 'relJs' in window.probe).catch(() => false)) return f;
    if (Date.now() > deadline) throw new Error('the probe tile never loaded');
    await sleep(200);
  }
}
const loaded = (f, sel) => f.evaluate((s) => { const i = document.querySelector(s); return !!i && i.complete && i.naturalWidth > 0; }, sel);

async function tileAssets(browser) {
  const { check, done } = checker('tile-assets');
  check(!!WS, 'WS is set (run.sh exports it)');
  seed();
  const { ctx, page } = await login(browser, 'admin', 'admin');
  const warnings = [];
  page.on('console', (m) => { if (m.text().includes(`[xbin] ${TILE}`)) warnings.push(m.text()); });
  for (let i = 0; i < 80; i++) { // the watcher registers the new tiles
    const r = await ctx.request.get(`${URL}/api/xbin/components`);
    if (r.ok() && (await r.json()).some((c) => c.path === TILE2)) break;
    await sleep(250);
  }
  await sleep(1000); // let the watcher's reload of the new files pass
  const report = async () => (await (await ctx.request.get(`${URL}/api/xbin/tile-assets?component=${TILE}`)).json()).tiles?.[0];
  const before = await report();
  check(before?.breaking?.tokens === 1 && before.findings.some((f) => f.fix === 'img/dot.svg'), `the tile report lists the absolute self-reference (${JSON.stringify(before?.breaking)})`);

  await openShell(page);
  await usePersonalScreen(page);
  await openTile(page, TILE);
  let f = await ready(page);
  const p0 = await probe(f);
  const frameURL = new globalThis.URL(f.url());
  log(`tile-assets[${MODE}]: frame at ${frameURL.origin}${frameURL.pathname}`);
  check(p0.dep === 'ok', `${MODE}: a relative module graph loads (app.js → ./lib/dep.js)`);
  check(p0.relJs === true && await loaded(f, '#rel'), `${MODE}: relative images load (markup and JS)`);
  if (MODE === 'origins') {
    check(frameURL.hostname.startsWith('t-') && frameURL.hostname.endsWith('.xbin.localhost'), `origins: the tile runs on its own origin (${frameURL.hostname})`);
    check(!frameURL.search.includes('frame='), `origins: the exchange left no token in the URL (${frameURL.search})`);
    check(p0.storage === TILE, `origins: the tile has its own localStorage (${p0.storage})`);
  } else {
    check(p0.storage === 'none', `${MODE}: an opaque-origin tile has no localStorage (${p0.storage})`);
  }
  const absOK = await loaded(f, '#abs');
  if (MODE === 'tokens') {
    check(!absOK, 'tokens: an absolute /c/ self-reference in markup does not load');
    await sleep(300);
    check(warnings.some((w) => w.includes('bx fix assets')), `tokens: xbin-client explains the failure and the fix (${warnings[0] ?? 'no warning'})`);
  } else {
    check(absOK, `${MODE}: the absolute self-reference loads`);
  }

  // Fragment links scroll without navigating; history keeps the document URL.
  await f.click('#frag');
  await settle(page);
  const afterFrag = await f.evaluate(() => ({ hash: location.hash, href: location.href, y: scrollY, at: window.probe?.loadedAt }));
  check(afterFrag.hash === '#target' && afterFrag.at === p0.loadedAt && !afterFrag.href.includes('/c/~'), `${MODE}: a fragment link scrolls in place (${afterFrag.href}, same document: ${afterFrag.at === p0.loadedAt})`);
  check(afterFrag.y > 1000, `${MODE}: …to the target (scrollY ${afterFrag.y})`);
  const hist = await f.evaluate(() => {
    const start = location.href.split('#')[0];
    history.replaceState(null, '', '#x');
    const a = location.href;
    history.pushState(null, '', 'deep/path?q=1');
    const b = location.pathname + location.search;
    history.replaceState(null, '', start);
    return { a, b, start };
  });
  check(hist.a === `${hist.start}#x` && !hist.a.includes('/c/~'), `${MODE}: replaceState('#x') keeps location clean (${hist.a})`);
  check(hist.b === `/c/${TILE}/deep/path?q=1`, `${MODE}: a relative pushState resolves against the document URL (${hist.b})`);
  // A client-side router keeps its relative links (xbin-client decides last).
  await f.click('#route');
  await settle(page);
  const routed = await f.evaluate(() => ({ routed: window.probe?.routed, at: window.probe?.loadedAt, path: location.pathname }));
  check(routed.at === p0.loadedAt && routed.routed === `/c/${TILE}/settings`, `${MODE}: a tile's own router handles its relative links (${JSON.stringify(routed)})`);
  await f.evaluate((u) => history.replaceState(null, '', u), hist.start);
  await shot(page, `tile-assets-${MODE}`, { fullPage: false });

  // A link to a sibling page navigates (tokens: re-credentialed by xbin-client).
  if (MODE !== 'legacy') {
    await f.click('#page');
    await f.waitForSelector('#p2', { timeout: 10000 }).catch(() => {});
    f = await tileFrame(page, TILE);
    const p2 = await f.evaluate(() => ({ text: document.querySelector('#p2')?.textContent ?? '', search: location.search })).catch(() => ({}));
    await sleep(300);
    check(p2.text === 'page two' && p2.search.includes('from=probe'), `${MODE}: a relative link to another page navigates (${JSON.stringify(p2)})`);
    check(await loaded(f, '#rel2').catch(() => false), `${MODE}: the second page's relative assets load`);
  }

  // Storage isolation between two tile origins.
  if (MODE === 'origins') {
    await openTile(page, TILE2);
    const f2 = await tileFrame(page, TILE2);
    await f2.waitForFunction(() => !!window.probe2, null, { timeout: 15000 });
    const s2 = (await f2.evaluate(() => window.probe2)).storage;
    check(s2 === null, `origins: a second tile does not see the first tile's localStorage (${s2})`);
    check(new globalThis.URL(f2.url()).hostname !== frameURL.hostname, 'origins: two tiles, two origins');
    await closeTile(page, TILE2);
  }

  // The codemod fixes the absolute reference; after a reload it loads everywhere.
  const bx = path.join(process.env.REPO || path.join(__dirname, '../../..'), 'bin', 'bx');
  const out = execFileSync(bx, ['fix', 'assets', TILE, '--write'], { env: { ...process.env, XBIN_WORKSPACE: WS }, encoding: 'utf8' });
  check(/rewrote 1 reference/.test(out) && fs.readFileSync(path.join(WS, TILE, 'index.html'), 'utf8').includes('id="abs" src="img/dot.svg"'), `bx fix assets --write rewrote the reference (${out.trim().split('\n').pop()})`);
  check((await report())?.breaking?.tokens === 0, 'the tile report is clean after the codemod');
  await closeTile(page, TILE);
  await openTile(page, TILE);
  f = await ready(page);
  check(await loaded(f, '#abs'), `${MODE}: after the codemod the reference loads`);
  await closeTile(page, TILE);

  // A direct-tab open: the document works top-level too.
  const tab = await ctx.newPage();
  await tab.goto(`${URL}/c/${TILE}/`);
  await tab.waitForFunction(() => window.probe && 'relJs' in window.probe, null, { timeout: 15000 }).catch(() => {});
  const tp = await tab.evaluate(() => ({ probe: window.probe ?? null, host: location.hostname, search: location.search }));
  check(tp.probe?.dep === 'ok' && tp.probe?.relJs === true, `${MODE}: a direct-tab open loads the relative graph (${tp.host})`);
  if (MODE === 'origins') check(tp.host.startsWith('t-') && !tp.search.includes('frame='), `origins: a direct open lands on the tile origin, token exchanged (${tp.host}${tp.search})`);
  await tab.close();

  fs.writeFileSync(path.join(WS, TILE, 'index.html'), INDEX); // re-runnable (--shots)
  await closeCtx(ctx, page);
  done();
}

module.exports = { tileAssets };
