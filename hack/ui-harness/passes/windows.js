// hack/ui-harness/passes/windows.js — floating windows, spawned windows,
// the grid, and the pointer drags on them (bx-canvas + the shell).
const { URL, login, closeCtx, settle, sh, fr, waitFor, waitSel, openShell, usePersonalScreen, openTile, closeTile, shot, checker } = require('../lib');

// Floating windows must always be reachable (the field report: a terminal
// pop-up restored at x 2270 / y 1217 on a smaller viewport — working, and
// invisible). Asserts: a persisted off-screen pop-up restores inside the
// viewport, a shrinking browser window pulls an open pop-up back in, and the
// canvas menu's "Bring windows on-screen" fixes a parked spawned window and
// float tile. Failures throw at the end of the pass.
async function windows(browser) {
  const { check, done } = checker('windows');
  const { ctx, page } = await login(browser, 'admin', 'admin');
  const inside = (r) => !!r && r.width > 0 && r.left >= 0 && r.top >= 0 && r.right <= r.W + 0.5 && r.bottom <= r.H + 0.5;
  const fmt = (r) => r ? `${Math.round(r.left)},${Math.round(r.top)} ${Math.round(r.width)}×${Math.round(r.height)} in ${r.W}×${r.H}` : 'none';
  const rectOf = (sel) => sh(page, (t, s) => {
    const el = s.startsWith('pop:') ? t.frameFor(s.slice(4))?.testApi().popElement() : t.query(s);
    const r = el?.getBoundingClientRect();
    return r ? { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height, W: innerWidth, H: innerHeight } : null;
  }, sel);

  // 1. a persisted off-screen pop-up restores inside the viewport. The
  // planted state is consumed by the FIRST crawler frame to mount, so the
  // reload must start on a personal screen without the crawler: an org
  // screen that holds it (Devs HQ) renders it for a moment while the layout
  // loads, and that frame would take the restore and rewrite the key.
  await openShell(page);
  await usePersonalScreen(page);
  await sh(page, (t) => { t.closeTile('apps/crawler'); return t.flushSave(); });
  await page.evaluate(() => localStorage.setItem('bx-term:apps/crawler', JSON.stringify({
    open: true, active: 0, pop: { x: 2270, y: 1217, w: 1003, h: 868 },
    sessions: [{ key: 'k1', id: null, net: null, gpu: 'none', api: true, name: '' }] })));
  await page.reload();
  await page.waitForSelector('bx-shell', { timeout: 15000 });
  await waitFor(page, (t) => !!t && t.screens.length > 0, null, { timeout: 15000, label: 'shell layout loaded' });
  await usePersonalScreen(page);
  await openTile(page, 'apps/crawler');
  await waitSel(page, 'bx-frame[src="apps/crawler"] .pop', { timeout: 20000 });
  let r = await rectOf('pop:apps/crawler');
  check(inside(r), `restored pop-up lands inside the viewport (${fmt(r)})`);
  await shot(page, 'windows-restored', { fullPage: false });

  // 2. a shrinking browser window pulls an open pop-up back in
  await fr(page, 'apps/crawler', (f) => f.setPop({ x: 820, y: 560, w: 560, h: 320 }));
  await settle(page);
  await page.setViewportSize({ width: 1000, height: 700 });
  await waitFor(page, (t) => { const p = t.frameFor('apps/crawler')?.testApi().pop; return !!p && p.x + p.w <= innerWidth && p.y + p.h <= innerHeight; }, null, { label: 'pop-up refit after resize' });
  r = await rectOf('pop:apps/crawler');
  check(inside(r), `pop-up follows a shrinking browser window (${fmt(r)})`);
  await shot(page, 'windows-shrunk', { fullPage: false });
  await page.setViewportSize({ width: 1400, height: 900 });
  await settle(page);

  // 3. "Bring windows on-screen": a spawned window and a float tile parked off-screen
  const hasItem = await sh(page, (t) => t.canvasMenuItems().some((i) => /on-screen/.test(i.label ?? '')));
  check(hasItem, 'canvas menu offers "Bring windows on-screen"');
  await sh(page, (t) => {
    // src is a tile WITHOUT a terminal state: a spawned bx-frame of apps/crawler
    // would restore the same pop-up and sit over the drag handles below
    t.setSpawnWindows([{ id: 'hw', from: 'apps/crawler', src: 'apps/pinned', reply() {}, title: 'parked', x: 5000, y: 4000, w: 400, h: 300, z: 3000 }]);
    t.openTile('apps/offline');
  });
  await waitSel(page, '.card[data-path="apps/offline"], .float[data-path="apps/offline"]', { state: 'attached' });
  await sh(page, (t) => t.setGeom((tiles) => tiles.map((o) => o.path === 'apps/offline' ? { ...o, float: { x: 5000, y: 4000, w: 400, h: 300, z: 100 } } : o)));
  await waitSel(page, '.float[data-path="apps/offline"]', { state: 'attached' });
  r = await rectOf('.float[data-path="apps/offline"]');
  check(inside(r), `a float saved off-screen renders inside the viewport (${fmt(r)})`);
  await sh(page, (t) => t.fitWindows(true));
  await settle(page);
  r = await rectOf('.spawn');
  check(inside(r), `spawned window brought on-screen (${fmt(r)})`);
  const saved = await sh(page, (t) => t.floatOf('apps/offline'));
  check(saved && saved.x + saved.w <= 1400 && saved.y + saved.h <= 900, `float geometry persisted on-screen (${JSON.stringify(saved)})`);
  await shot(page, 'windows-fitted', { fullPage: false });

  // 4. real pointer drags (every drag goes through bx-kit's dragPointer): a
  // float, a spawned window and a grid tile each move by the mouse delta and
  // the shell persists the new geometry.
  // the restored crawler pop-up (1003×868) would sit over every drag handle
  await fr(page, 'apps/crawler', (f) => f.closeTerminal());
  await waitSel(page, 'bx-frame[src="apps/crawler"] .pop', { state: 'hidden' });
  const drag = async (sel, dx, dy) => {
    const box = await page.locator(sel).first().boundingBox();
    await page.mouse.move(box.x + Math.min(40, box.width / 2), box.y + box.height / 2);
    await page.mouse.down();
    await page.mouse.move(box.x + Math.min(40, box.width / 2) + dx, box.y + box.height / 2 + dy, { steps: 6 });
    await page.mouse.up();
    await settle(page);
  };
  // a deterministic grid for the drag: crawler alone at the origin (the grid
  // is put back at the end). The passes before this one re-place tiles
  // anywhere, and a card near the bottom of the viewport can't be dragged
  // 420 px down — the pointer would leave the window.
  const layout0 = await sh(page, (t) => t.openTiles);
  await sh(page, (t) => t.setGeom((tiles) => tiles.filter((o) => o.float || o.path === 'apps/crawler')
    .map((o) => (o.path === 'apps/crawler' ? { ...o, x: 0, y: 0 } : o))));
  await waitSel(page, '.card[data-path="apps/crawler"] bx-frame', { state: 'attached' });
  await settle(page);
  await sh(page, (t) => t.setFloat('apps/offline', { x: 40, y: 560 })); // bottom-left: below the crawler card (at the origin) and clear of the fitted windows
  await settle(page);
  const fl0 = await sh(page, (t) => t.floatOf('apps/offline'));
  await drag('.float[data-path="apps/offline"] .head', 90, 50);
  const fl1 = await sh(page, (t) => t.floatOf('apps/offline'));
  check(fl1 && Math.abs(fl1.x - fl0.x - 90) <= 2 && Math.abs(fl1.y - fl0.y - 50) <= 2, `float drag persisted the move (${JSON.stringify(fl0)} → ${JSON.stringify(fl1)})`);
  const sw0 = await sh(page, (t) => ({ x: t.spawnWindows[0]?.x, y: t.spawnWindows[0]?.y }));
  await drag('.spawn .shead', -60, 30);
  const sw1 = await sh(page, (t) => ({ x: t.spawnWindows[0]?.x, y: t.spawnWindows[0]?.y }));
  check(Math.abs(sw1.x - sw0.x + 60) <= 2 && Math.abs(sw1.y - sw0.y - 30) <= 2, `spawned window drag moved it (${JSON.stringify(sw0)} → ${JSON.stringify(sw1)})`);
  const g0 = await sh(page, (t) => { const o = t.openTiles.find((x) => x.path === 'apps/crawler'); return o && { x: o.x, y: o.y }; });
  // downwards into empty grid rows: a sideways move can land on an occupied
  // cell, which the grid resolves by leaving the tile where it was
  await drag('.card[data-path="apps/crawler"] .head', 0, 420);
  const g1 = await sh(page, (t) => { const o = t.openTiles.find((x) => x.path === 'apps/crawler'); return o && { x: o.x, y: o.y }; });
  check(g0 && g1 && g1.y > g0.y && g1.x === g0.x, `grid tile drag snapped and persisted (${JSON.stringify(g0)} → ${JSON.stringify(g1)})`);
  await sh(page, (t, l) => t.setGeom(() => l), layout0); // the grid as it was
  check(await page.evaluate(() => !document.querySelector('body > div[style*="2147483647"]')), 'the drag shield is gone after pointerup');

  // tidy: the next pass starts from the seeded layout
  await sh(page, (t) => { t.setSpawnWindows([]); t.closeTile('apps/offline'); localStorage.removeItem('bx-term:apps/crawler'); });
  await settle(page);
  await closeCtx(ctx, page);
  done();
}

module.exports = { windows };
