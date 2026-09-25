// hack/ui-harness/passes/menuopen.js — tiles opened from a right-click menu
// land at the click (D80), and the "Open tile ▸" find box filters only its
// own flyout (it used to thin out the canvas menu it hangs off, "Open tile"
// row included). Real right-clicks on a known layout: a free spot takes the
// tile under the pointer; next to other tiles it takes the nearest free
// spot and overlaps nothing; from a sidebar row it lands at the canvas's
// left edge. The admin's layout is restored at the end.
const { login, closeCtx, settle, sh, waitFor, waitSel, openShell, usePersonalScreen, shot, checker } = require('../lib');

const W = 576, H = 384, CELL = 48;
const overlap = (a, b) => a.x < b.x + b.w && a.x + a.w > b.x && a.y < b.y + b.h && a.y + a.h > b.y;

async function menuOpen(browser) {
  const { check, done } = checker('menu-open');
  // tall: the seeded grants/bindings bar pushes the canvas ~450 px down
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 1300 } });
  await openShell(page);
  await usePersonalScreen(page);
  const saved = await sh(page, (t) => t.openTiles.map((o) => ({ ...o })));
  await sh(page, (t) => { t.setGridScale(1); t.setGeom(() => [{ path: 'apps/crawler', x: 0, y: 0, w: 576, h: 384 }]); return t.flushSave(); });
  await waitSel(page, '.gtile[data-path="apps/crawler"]', { state: 'attached' });
  await settle(page);
  // canvas logical px → viewport px (scale 1)
  const origin = await sh(page, (t) => { const r = t.query('.canvas').getBoundingClientRect(); return { x: r.left, y: r.top }; });
  const rightClick = (p) => page.mouse.click(origin.x + p.x, origin.y + p.y, { button: 'right' });
  const tileOf = (path) => sh(page, (t, p) => t.openTiles.find((o) => o.path === p) ?? null, path);
  const clearOfOthers = async (path) => {
    const all = await sh(page, (t) => t.openTiles.filter((o) => !o.float));
    const me = all.find((o) => o.path === path);
    return !!me && all.every((o) => o.path === path || !overlap(me, o));
  };

  // 1. the canvas menu: the find box filters the flyout, not the menu itself
  const p1 = { x: 700, y: 120 };
  await rightClick(p1);
  await waitSel(page, 'bx-menu .panel.main .it');
  await page.locator('bx-menu .panel.main .it', { hasText: 'Open tile' }).hover();
  await waitSel(page, 'bx-menu .panel.sub .q');
  await page.locator('bx-menu .panel.sub .q').fill('leads');
  await settle(page);
  const root = await page.locator('bx-menu .panel.main .it .lb').allTextContents();
  check(['Open tile', 'New screen', 'Bring windows on-screen'].every((l) => root.includes(l)) && root.some((l) => l.startsWith('Create a new tile')),
    `typing in the find box leaves the canvas menu whole (${JSON.stringify(root)})`);
  check(await page.locator('bx-menu .panel.main .it.open').count() === 1, 'the "Open tile" row the flyout hangs off stays, highlighted');
  const sub = await page.locator('bx-menu .panel.sub .it').allTextContents();
  check(sub.length > 0 && sub.every((s) => /leads/i.test(s)), `the flyout lists only matches (${JSON.stringify(sub)})`);
  await shot(page, 'menu-open-filter', { fullPage: false });
  await page.keyboard.press('Enter');
  await waitFor(page, (t) => t.isOpen('apps/leads'), null, { label: 'apps/leads opened' });
  const leads = await tileOf('apps/leads');
  check(!!leads && p1.x >= leads.x && p1.x < leads.x + W && p1.y >= leads.y && p1.y < leads.y + H && await clearOfOthers('apps/leads'),
    `a free spot: the tile opens under the pointer (${JSON.stringify(leads)} for ${JSON.stringify(p1)})`);

  // 2. crowded: the default size at the click would overlap → the nearest free spot
  const p2 = { x: 300, y: 450 };
  await rightClick(p2);
  await waitSel(page, 'bx-menu .panel.main .it');
  await page.locator('bx-menu .panel.main .it', { hasText: 'Open tile' }).hover();
  await waitSel(page, 'bx-menu .panel.sub .q');
  await page.locator('bx-menu .panel.sub .q').fill('pinned');
  await page.keyboard.press('Enter');
  await waitFor(page, (t) => t.isOpen('apps/pinned'), null, { label: 'apps/pinned opened' });
  const pinned = await tileOf('apps/pinned');
  const near = pinned && Math.abs(pinned.x - Math.floor(p2.x / CELL) * CELL) <= 6 * CELL && Math.abs(pinned.y - Math.floor(p2.y / CELL) * CELL) <= 6 * CELL;
  check(near && await clearOfOthers('apps/pinned'), `next to other tiles: the nearest free spot, overlapping nothing (${JSON.stringify(pinned)} for ${JSON.stringify(p2)})`);
  await settle(page);
  await shot(page, 'menu-open-placed', { fullPage: false });

  // 3. a sidebar row's "Open on this screen": the canvas's left edge, at the row's height
  await page.locator('bx-shell .item[data-path="apps/offline"]').click({ button: 'right' });
  await waitSel(page, 'bx-menu .panel.main .it');
  await page.locator('bx-menu .panel.main .it', { hasText: 'Open on this screen' }).click();
  await waitFor(page, (t) => t.isOpen('apps/offline'), null, { label: 'apps/offline opened' });
  const off = await tileOf('apps/offline');
  check(!!off && off.x <= 2 * CELL && await clearOfOthers('apps/offline'), `from the sidebar: the left edge, overlapping nothing (${JSON.stringify(off)})`);

  await sh(page, (t, tiles) => { t.setGeom(() => tiles); return t.flushSave(); }, saved);
  await closeCtx(ctx, page);
  done();
}

module.exports = { menuOpen };
