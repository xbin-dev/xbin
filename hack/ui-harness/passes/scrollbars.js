// hack/ui-harness/passes/scrollbars.js — thin themed scrollbars and the
// focused-scroll tint (D123). Checks: (a) the shell's canvas scroller (its
// shadow root), the Agent tab's transcript (bx-agent's) and a scroller in a
// tile document that links theme.css are 6px wide (the 3px-drawn thumb's grab
// zone), not the ~15px native bar; (b) the tracker keeps exactly one
// [data-bx-scroll] per document: the innermost scroller under the mouse; a
// wheel the inner one can't take (at its end) moves the tint to the outer one,
// which the gesture then latches; a scroller that contains its overscroll
// keeps it; (c) moving onto a tile's iframe clears the shell's tint
// (xbin:scroll-focus) and the tile's own tracker marks inside it; (d) a touch
// device keeps its native bars. Screenshots: scrollbars-*.png.
//
// Headless Chromium hides every scrollbar (--hide-scrollbars), so this pass
// runs its own browser that draws them.
const { pw, URL, login, closeCtx, settle, fr, waitFor, openShell, usePersonalScreen, openTile, tileFrame, shot, shotEl, checker, sleep } = require('../lib');

const TILE = 'apps/crawler';

// a nested pair of scrollers planted in a document: outer 220×160 over 600px,
// inner 160×100 over 400px, fixed at (x, y)
const plant = ([x, y]) => {
  const outer = document.createElement('div');
  outer.id = 'bxs-outer';
  outer.style.cssText = `position:fixed;left:${x}px;top:${y}px;width:220px;height:160px;overflow:auto;z-index:99999;background:#333`;
  outer.innerHTML = '<div style="height:30px">outer</div><div id="bxs-inner" style="width:160px;height:100px;overflow:auto;background:#444"><div style="height:400px">inner</div></div><div style="height:470px"></div>';
  document.body.appendChild(outer);
};
const barWidth = (el) => el.offsetWidth - el.clientWidth - (parseFloat(getComputedStyle(el).borderLeftWidth) || 0) - (parseFloat(getComputedStyle(el).borderRightWidth) || 0);
// who carries the tint in a document (Playwright's CSS engine pierces shadow roots)
const marks = (where) => where.locator('[data-bx-scroll]').evaluateAll((els) => els.map((e) => e.id || e.className || e.localName));
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);

async function scrollbars() {
  const { check, skip, done } = checker('scrollbars');
  const browser = await pw.chromium.launch({ ignoreDefaultArgs: ['--hide-scrollbars'] });
  try { await run(browser, check, skip); } finally { await browser.close(); }
  done();
}

async function run(browser, check, skip) {
  const A = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 1300 } });
  const page = A.page;
  await openShell(page);
  await usePersonalScreen(page);
  await openTile(page, TILE);
  const waitMarks = async (where, want) => {
    const deadline = Date.now() + 3000;
    let got;
    do { got = await marks(where); if (same(got, want)) return got; await sleep(50); } while (Date.now() < deadline);
    return got;
  };

  // ---- (a) widths ----
  const main = page.locator('bx-shell main').first();
  if (await main.evaluate((el) => el.scrollHeight > el.clientHeight + 1)) {
    const w = await main.evaluate(barWidth);
    check(w === 6, `the shell's canvas scroller has the 6px bar (${w})`);
  } else skip('the canvas does not overflow at this size: no bar to measure');
  await page.mouse.move(700, 400); // over the canvas: its bar takes the tint
  await sleep(150);
  await shot(page, 'scrollbars-shell', { fullPage: false });
  const tf = await tileFrame(page, TILE);
  await tf.waitForFunction(() => !!window[Symbol.for('bx-scroll-focus')], null, { timeout: 5000 }).catch(() => {});
  const tileW = await tf.evaluate(() => {
    const d = document.createElement('div');
    d.style.cssText = 'position:fixed;left:0;top:0;width:100px;height:100px;overflow:scroll';
    document.body.appendChild(d);
    const w = d.offsetWidth - d.clientWidth;
    d.remove();
    return w;
  });
  check(tileW === 6, `a scroller in a tile document that links theme.css is 6px wide (${tileW})`);
  check(await tf.evaluate(() => !!window[Symbol.for('bx-scroll-focus')]), 'xbin-client loaded the tracker in the theme-linking tile');

  // ---- (b) the tint follows the scroller the next scroll moves ----
  await page.evaluate(plant, [1100, 900]);
  await settle(page);
  const inner = page.locator('#bxs-inner');
  const box = await inner.boundingBox();
  const mid = { x: box.x + box.width / 2, y: box.y + box.height / 2 };
  await page.mouse.move(mid.x - 30, mid.y - 60); // over the outer one only
  await page.mouse.move(mid.x, mid.y, { steps: 4 });
  let m = await waitMarks(page, ['bxs-inner']);
  check(same(m, ['bxs-inner']), `hovering the inner scroller marks it, and only it (${JSON.stringify(m)})`);
  await shotEl(page, '#bxs-outer', 'scrollbars-nested-inner');
  // the inner one at its end: a wheel down goes to the outer one
  await inner.evaluate((el) => { el.scrollTop = el.scrollHeight; });
  await page.mouse.wheel(0, 60);
  m = await waitMarks(page, ['bxs-outer']);
  check(same(m, ['bxs-outer']), `a wheel the inner scroller can't take marks the outer one (${JSON.stringify(m)})`);
  await page.mouse.move(mid.x + 2, mid.y + 2);
  await sleep(120);
  m = await marks(page);
  check(same(m, ['bxs-outer']), `the wheel gesture latches: a small move keeps the outer mark (${JSON.stringify(m)})`);
  await shotEl(page, '#bxs-outer', 'scrollbars-nested-outer');
  await sleep(400); // the latch runs out
  await page.mouse.wheel(0, -40); // up: the inner one can move
  m = await waitMarks(page, ['bxs-inner']);
  check(same(m, ['bxs-inner']), `a wheel the inner scroller can take marks it again (${JSON.stringify(m)})`);
  await sleep(400);
  await inner.evaluate((el) => { el.style.overscrollBehavior = 'contain'; el.scrollTop = el.scrollHeight; });
  await page.mouse.wheel(0, 60);
  await sleep(150);
  m = await marks(page);
  check(same(m, ['bxs-inner']), `a scroller that contains its overscroll keeps the mark at its end (${JSON.stringify(m)})`);
  await page.evaluate(() => document.getElementById('bxs-outer')?.remove());

  // ---- (c) onto the tile's iframe: the shell lets go, the tile's tracker marks ----
  await page.locator(`.card[data-path="${TILE}"]`).evaluate((el) => el.scrollIntoView({ block: 'start' }));
  await tf.evaluate(plant, [20, 20]);
  const ib = await tf.locator('#bxs-inner').boundingBox();
  if (!ib) skip('the tile frame has no room for the fixture');
  else {
    await page.mouse.move(ib.x - 40, ib.y - 40); // the shell (the card's head, the canvas)
    await page.mouse.move(ib.x + ib.width / 2, ib.y + ib.height / 2, { steps: 6 });
    await sleep(250);
    m = await marks(page);
    check(m.length === 0, `over a tile's iframe the shell marks nothing (${JSON.stringify(m)})`);
    const tm = await marks(tf);
    check(same(tm, ['bxs-inner']), `the tile document marks its own innermost scroller (${JSON.stringify(tm)})`);
    await shotEl(page, `.card[data-path="${TILE}"]`, 'scrollbars-tile');
  }
  await tf.evaluate(() => document.getElementById('bxs-outer')?.remove());

  // ---- the agent pop-up with a scrolling transcript ----
  await fr(page, TILE, (f) => f.open('term'));
  await fr(page, TILE, (f) => f.startKind('agent', 'fake'));
  await waitFor(page, (t) => { const api = t.frameFor('apps/crawler')?.testApi(); const ag = api && api.agent(api.tabs.length - 1); return !!ag && ag.status === 'idle'; }, null, { timeout: 15000, label: 'the agent is idle' });
  await fr(page, TILE, (f) => { f.setActiveTab(f.tabs.length - 1); f.agent().send('long 12'); });
  await waitFor(page, (t) => /done: 12 units/.test((t.frameFor('apps/crawler')?.testApi().agent()?.blocks || []).map((b) => b.text || '').join(' ')), null, { timeout: 20000, label: 'the long turn answered' });
  const sc = page.locator(`bx-frame[src="${TILE}"] bx-agent .scroll`);
  const agentW = await sc.evaluate(barWidth);
  check(agentW === 6, `the Agent tab's transcript (bx-agent's shadow root) has the 6px bar (${agentW})`);
  const sb = await sc.boundingBox();
  await page.mouse.move(sb.x + sb.width / 2, sb.y + sb.height / 2, { steps: 3 });
  m = await waitMarks(page, ['scroll']);
  check(same(m, ['scroll']), `hovering the transcript tints its bar (${JSON.stringify(m)})`);
  await shotEl(page, `bx-frame[src="${TILE}"] .pop`, 'scrollbars-agent');
  const sid = await fr(page, TILE, (f) => f.agent().sessionId);
  await A.ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(sid)}`);
  await A.ctx.request.delete(`${URL}/api/xbin/prefs/term%3Aapps%3Acrawler`);
  await closeCtx(A.ctx, page);

  // ---- (d) touch keeps its native bars ----
  const M = await login(browser, 'admin', 'admin', { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 2 });
  await openShell(M.page);
  const fine = await M.page.evaluate(() => matchMedia('(hover: hover) and (pointer: fine)').matches);
  if (fine) skip('this emulation reports a fine pointer; the touch fallback is not observable');
  else {
    const mm = M.page.locator('bx-shell main').first();
    const w = await mm.evaluate((el) => { const d = el.ownerDocument.createElement('div'); d.style.cssText = 'height:4000px'; el.appendChild(d); const w = el.offsetWidth - el.clientWidth; d.remove(); return w; });
    check(w !== 6, `on a touch device the bars stay native (${w}px)`);
  }
  await closeCtx(M.ctx, M.page);
}

module.exports = { scrollbars };
