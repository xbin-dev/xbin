// hack/ui-harness/passes/windows.js — floating windows, spawned windows,
// the grid, and the pointer drags on them (bx-canvas + the shell).
const { URL, login, closeCtx, settle, sh, fr, waitFor, waitSel, openShell, usePersonalScreen, openTile, closeTile, shot, checker } = require('../lib');

// Windows must always be reachable (the field report: a terminal pop-up
// restored at x 2270 / y 1217 on a smaller viewport — working, and
// invisible). Asserts: a terminal pop-up is positioned relative to its tile
// and stays inside the canvas — the scroll area — wherever it is planted,
// dragged to or how the window shrinks (D66); it follows the card when the
// card moves; the canvas menu's "Bring windows on-screen" fixes a parked
// spawned window and float tile; real pointer drags move a float, a spawned
// window and a grid tile — and a grid tile dropped onto another pushes it
// aside with a ghost preview. Failures throw at the end of the pass.
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
  // the canvas (the scroll area) in viewport coordinates
  const canvasRect = () => sh(page, (t) => { const r = t.query('.canvas')?.getBoundingClientRect(); return r && { left: r.left, top: r.top, right: r.right, bottom: r.bottom }; });
  const inCanvas = (r, c) => !!r && !!c && r.left >= c.left - 0.5 && r.top >= c.top - 0.5 && r.right <= c.right + 0.5 && r.bottom <= c.bottom + 0.5;

  // 1. a persisted pop-up planted left of / above the canvas origin restores
  // inside the canvas (never outside the scroll area, D66), and the canvas
  // grows to contain it. The window state is the user's pref (D73); a live
  // session on the tile makes it restore with a tab. The reload must start
  // on a personal screen without the crawler: an org screen that holds it
  // (Devs HQ) renders it for a moment while the layout loads, and that
  // frame's own save would rewrite the pref.
  await openShell(page);
  await usePersonalScreen(page);
  await sh(page, (t) => { t.closeTile('apps/crawler'); return t.flushSave(); });
  await ctx.request.put(`${URL}/api/xbin/prefs/term%3Aapps%3Acrawler`, { data: { open: true, active: 0, pop: { dx: -2270, dy: -1217, w: 1003, h: 868 } } });
  await page.evaluate(() => new Promise((res, rej) => { // a live session on the tile: opened from the page, then left running server-side
    const ws = new WebSocket(`${location.origin.replace(/^http/, 'ws')}/ws/term?cwd=apps%2Fcrawler`);
    ws.onmessage = () => { ws.close(); res(); }; ws.onerror = rej;
  }));
  await page.reload();
  await page.waitForSelector('bx-shell', { timeout: 15000 });
  await waitFor(page, (t) => !!t && t.screens.length > 0, null, { timeout: 15000, label: 'shell layout loaded' });
  await usePersonalScreen(page);
  await openTile(page, 'apps/crawler');
  await waitSel(page, 'bx-frame[src="apps/crawler"] .pop', { timeout: 20000 });
  await settle(page);
  let r = await rectOf('pop:apps/crawler');
  let c = await canvasRect();
  check(inCanvas(r, c), `restored pop-up lands inside the canvas (${fmt(r)} in ${JSON.stringify(c)})`);
  await shot(page, 'windows-restored', { fullPage: false });

  // 2. the pop-up follows its card, and cannot leave the canvas: placed far
  // past the tiles it stays with its top-left over them; a shrinking browser
  // window keeps it inside the (scrollable) canvas
  // (the restored one sits pinned at the canvas's top edge — place it freely
  // beside its card first, so it has room to follow)
  await sh(page, (t) => { const r = t.query('.card[data-path="apps/crawler"]').getBoundingClientRect(); t.frameFor('apps/crawler').testApi().setPop({ x: r.left + 24, y: r.top + 48, w: 560, h: 320 }); });
  await settle(page); await settle(page);
  const before = await rectOf('pop:apps/crawler');
  await sh(page, (t) => t.setGeom((tiles) => tiles.map((o) => (o.path === 'apps/crawler' && !o.float ? { ...o, y: o.y + 96 } : o))));
  await settle(page); await settle(page);
  r = await rectOf('pop:apps/crawler');
  check(!!before && !!r && Math.abs(r.top - before.top - 96) <= 1 && Math.abs(r.left - before.left) <= 1, `pop-up follows its card (+96 → ${Math.round(r.top - before.top)})`);
  await fr(page, 'apps/crawler', (f) => f.setPop({ x: 9000, y: 8000, w: 560, h: 320 }));
  await settle(page); await settle(page);
  r = await rectOf('pop:apps/crawler'); c = await canvasRect();
  check(inCanvas(r, c), `a pop-up placed far away stays inside the canvas (${fmt(r)} in ${JSON.stringify(c)})`);
  await page.setViewportSize({ width: 1000, height: 700 });
  await settle(page); await settle(page);
  r = await rectOf('pop:apps/crawler'); c = await canvasRect();
  check(inCanvas(r, c), `pop-up stays inside the canvas after the window shrinks (${fmt(r)})`);
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
  // drag(sel, dx, dy) — or a path of [dx, dy] stops; beforeUp runs with the
  // button still down (to look at the push ghosts)
  const drag = async (sel, dx, dy, { beforeUp } = {}) => {
    const box = await page.locator(sel).first().boundingBox();
    const x0 = box.x + Math.min(40, box.width / 2), y0 = box.y + box.height / 2;
    await page.mouse.move(x0, y0);
    await page.mouse.down();
    for (const [mx, my] of (Array.isArray(dx) ? dx : [[dx, dy]])) await page.mouse.move(x0 + mx, y0 + my, { steps: 6 });
    await settle(page);
    if (beforeUp) await beforeUp();
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
  const gridPos = (path) => sh(page, (t, p) => { const o = t.openTiles.find((x) => x.path === p && !x.float); return o && { x: o.x, y: o.y }; }, path);
  const g0 = await gridPos('apps/crawler');
  // downwards into empty grid rows (nothing to push)
  await drag('.card[data-path="apps/crawler"] .head', 0, 420);
  const g1 = await gridPos('apps/crawler');
  check(g0 && g1 && g1.y > g0.y && g1.x === g0.x, `grid tile drag snapped and persisted (${JSON.stringify(g0)} → ${JSON.stringify(g1)})`);

  // 5. push on drag (D66): offline on the grid right below crawler (0,384);
  // dragging crawler 200px down (→ y 192) overlaps it → a ghost previews
  // offline's landing spot, and the drop pushes it to y 576 — in the
  // direction it was hit. Dragging down and back up in one gesture leaves
  // it where it was, with no ghost at release.
  const place = () => sh(page, (t) => t.setGeom((tiles) => tiles.map((o) => o.path === 'apps/crawler' ? { path: o.path, x: 0, y: 0, w: 576, h: 384 }
    : o.path === 'apps/offline' ? { path: o.path, x: 0, y: 384, w: 576, h: 384 } : o)));
  await place();
  await waitSel(page, '.card[data-path="apps/offline"] bx-frame', { state: 'attached' });
  await settle(page);
  let ghost = null;
  await drag('.card[data-path="apps/crawler"] .head', 0, 200, { beforeUp: async () => {
    ghost = await sh(page, (t) => { const g = t.query('.ghost[data-path="apps/offline"]'); return g && { top: parseInt(g.style.top, 10) }; });
  } });
  check(ghost && ghost.top === 576, `a push ghost previews offline's landing spot while dragging (${JSON.stringify(ghost)})`);
  const c1 = await gridPos('apps/crawler'), o1 = await gridPos('apps/offline');
  check(c1?.y === 192 && o1?.y === 576 && o1?.x === 0, `the drop pushes offline down out of the way (crawler ${JSON.stringify(c1)}, offline ${JSON.stringify(o1)})`);
  await place();
  await settle(page);
  await drag('.card[data-path="apps/crawler"] .head', [[0, 200], [0, 0]], 0, { beforeUp: async () => {
    ghost = await sh(page, (t) => !!t.query('.ghost'));
  } });
  const o2 = await gridPos('apps/offline');
  check(ghost === false && o2?.y === 384, `backing off drops the ghost and leaves offline where it was (${JSON.stringify(o2)})`);

  // 6. the swap (D69): offline to crawler's right (576,0). Dragging crawler
  // fully onto it makes offline yield into the space crawler left — its
  // ghost at x 0 — and the drop swaps the two.
  await sh(page, (t) => t.setGeom((tiles) => tiles.map((o) => o.path === 'apps/offline' ? { path: o.path, x: 576, y: 0, w: 576, h: 384 } : o)));
  await settle(page);
  await drag('.card[data-path="apps/crawler"] .head', 576, 0, { beforeUp: async () => {
    ghost = await sh(page, (t) => { const g = t.query('.ghost[data-path="apps/offline"]'); return g && { left: parseInt(g.style.left, 10) }; });
  } });
  const c3 = await gridPos('apps/crawler'), o3 = await gridPos('apps/offline');
  check(ghost?.left === 0 && c3?.x === 576 && o3?.x === 0 && o3?.y === 0, `dragging a tile fully onto its neighbour swaps them (ghost ${JSON.stringify(ghost)}, crawler ${JSON.stringify(c3)}, offline ${JSON.stringify(o3)})`);
  await sh(page, (t, l) => t.setGeom(() => l), layout0); // the grid as it was
  check(await page.evaluate(() => !document.querySelector('body > div[style*="2147483647"]')), 'the drag shield is gone after pointerup');

  // tidy: the next pass starts from the seeded layout
  await sh(page, (t) => { t.setSpawnWindows([]); t.closeTile('apps/offline'); });
  await ctx.request.delete(`${URL}/api/xbin/prefs/term%3Aapps%3Acrawler`);
  for (const s of await (await ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json()) await ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(s.id)}`);
  await settle(page);
  await closeCtx(ctx, page);
  done();
}

module.exports = { windows };
