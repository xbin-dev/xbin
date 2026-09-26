// hack/ui-harness/passes/tabstrip.js — the terminal window's tab strip
// scrolls when its tabs don't fit (the degraded bar, frame-titlebar.js
// fitBar), and a tab the user scrolled into view can be clicked or tapped.
// fitBar keeps the ACTIVE tab in view; it used to do so after every render,
// and a press anywhere in the window re-renders it (the window comes to the
// front) — the strip jumped back between pointerdown and pointerup, so the
// click never reached the tab (on a phone the tap landed on another tab's
// ✕ and ended that session).
const { URL, sleep, login, closeCtx, settle, sh, fr, waitFor, openShell, usePersonalScreen, openTile, checker } = require('../lib');

const TILE = 'apps/crawler';
const N = 9;

async function strip(page) {
  return sh(page, (t, src) => {
    const f = t.frameFor(src), s = f.renderRoot.querySelector('.titlebar .tabs');
    return { left: s.scrollLeft, over: s.scrollWidth - s.clientWidth, active: f.testApi().activeTab, narrow: f.testApi().narrow };
  }, TILE);
}

// one run: N shells, the last active, the strip scrolled back to its start
// by hand, then a press on the first tab
async function run(browser, check, name, ctxOpts, press) {
  const { ctx, page } = await login(browser, 'admin', 'admin', ctxOpts);
  for (const s of await (await ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json()) await ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(s.id)}`);
  await ctx.request.put(`${URL}/api/xbin/prefs/term%3Aapps%3Acrawler`, { data: { open: true, active: 0, pop: { dx: 24, dy: 44, w: 560, h: 360 } } });
  await openShell(page);
  await usePersonalScreen(page);
  await openTile(page, TILE);
  await fr(page, TILE, (f) => f.open('term'));
  await settle(page);
  for (let i = 0; i < 60 && (await fr(page, TILE, (f) => f.tabs.length)) < N; i++) {
    await fr(page, TILE, (f) => f.newTerm());
    await sleep(150);
  }
  await waitFor(page, (t, a) => t.frameFor(a.src).testApi().tabs.filter((x) => x.id).length >= a.n, { src: TILE, n: N }, { timeout: 60000, label: 'every tab has its session' });
  await fr(page, TILE, (f, t, n) => f.setActiveTab(n - 1), N);
  await settle(page);
  await page.locator(`bx-frame[src="${TILE}"] .titlebar`).evaluate((el) => el.scrollIntoView({ block: 'center' }));
  await settle(page);
  const s0 = await strip(page);
  check(s0.narrow && s0.over > 40 && s0.left > 0, `${name}: the strip overflows and shows the active last tab (${JSON.stringify(s0)})`);
  // the user scrolls back to the start
  await sh(page, (t, src) => { t.frameFor(src).renderRoot.querySelector('.titlebar .tabs').scrollLeft = 0; }, TILE);
  await sleep(100);
  const ids = await fr(page, TILE, (f) => f.tabs.map((x) => x.id));
  const box = await page.locator(`bx-frame[src="${TILE}"] .titlebar .tab .lbl`).first().boundingBox();
  await press(page, box.x + Math.min(8, box.width / 2), box.y + box.height / 2);
  await sleep(300);
  const s1 = await strip(page);
  const after = await fr(page, TILE, (f) => f.tabs.map((x) => x.id));
  check(s1.active === 0, `${name}: the tab scrolled into view was selected (${JSON.stringify(s1)})`);
  check(after.length === ids.length, `${name}: no session ended by the press (${ids.length} → ${after.length})`);
  // leave nothing behind: the tabs' sessions end, the window closes
  await fr(page, TILE, (f) => { while (f?.tabs.length) f.closeTab(0); });
  await fr(page, TILE, (f) => f.closeTerminal());
  await settle(page);
  await closeCtx(ctx, page);
}

async function tabStrip(browser) {
  const { check, done } = checker('tab-strip');
  await run(browser, check, 'mouse', { viewport: { width: 1400, height: 1800 } }, async (page, x, y) => {
    await page.mouse.move(x, y);
    await page.mouse.down();
    await sleep(100); // the window re-renders on pointerdown (it comes to the front)
    await page.mouse.up();
  });
  await run(browser, check, 'phone', { viewport: { width: 400, height: 800 }, hasTouch: true, isMobile: true },
    (page, x, y) => page.touchscreen.tap(x, y));
  done();
}

module.exports = { tabStrip };
