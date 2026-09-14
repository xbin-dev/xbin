// hack/ui-harness/passes/gridscale.js — the per-browser grid scale (D68):
// the layout stays logical (multiples of 48) while the render multiplies by
// the scale; drags and pushes work in logical units; the scale lives in
// localStorage; the settings menu shows the slider; a browser-zoom shortcut
// earns the one-time tip.
const { login, closeCtx, settle, sh, openShell, usePersonalScreen, openTile, shot, checker } = require('../lib');

async function gridScale(browser) {
  const { check, done } = checker('grid-scale');
  const { ctx, page } = await login(browser, 'admin', 'admin');
  await openShell(page);
  await usePersonalScreen(page);
  await openTile(page, 'apps/crawler');
  await openTile(page, 'apps/offline');
  const layout0 = await sh(page, (t) => t.openTiles);
  await sh(page, (t) => t.setGeom((tiles) => tiles.map((o) => o.path === 'apps/crawler' ? { path: o.path, x: 0, y: 0, w: 576, h: 384 }
    : o.path === 'apps/offline' ? { path: o.path, x: 0, y: 384, w: 576, h: 384 } : o)));
  await settle(page);
  // a card's rendered box, relative to the canvas
  const box = async (p) => {
    const b = await page.locator(`.gtile[data-path="${p}"]`).first().boundingBox();
    const c = await page.locator('.canvas').first().boundingBox();
    return b && c && { left: Math.round(b.x - c.x), top: Math.round(b.y - c.y), width: Math.round(b.width) };
  };
  const logical = (p) => sh(page, (t, q) => { const o = t.openTiles.find((x) => x.path === q && !x.float); return o && { x: o.x, y: o.y, w: o.w }; }, p);

  await sh(page, (t) => t.setGridScale(0.5));
  await settle(page); await settle(page);
  let b = await box('apps/crawler'), l = await logical('apps/crawler');
  check(!!b && Math.abs(b.width - 284) <= 1 && b.left === 0 && b.top === 0, `at 0.5× the card renders at half size (${JSON.stringify(b)})`);
  check(!!l && l.x === 0 && l.y === 0 && l.w === 576, `the stored layout is unchanged (${JSON.stringify(l)})`);
  check(await page.evaluate(() => localStorage.getItem('xbin-grid-scale')) === '0.5', 'the scale is saved in this browser');

  // a 96 px drag at 0.5× is a 192-unit move; offline (at 384) is pushed, its ghost at 288 px
  const head = page.locator('.card[data-path="apps/crawler"] .head').first();
  const hb = await head.boundingBox();
  await page.mouse.move(hb.x + 30, hb.y + hb.height / 2);
  await page.mouse.down();
  await page.mouse.move(hb.x + 30, hb.y + hb.height / 2 + 96, { steps: 6 });
  await settle(page);
  const ghost = await sh(page, (t) => { const g = t.query('.ghost[data-path="apps/offline"]'); return g ? parseInt(g.style.top, 10) : null; });
  await shot(page, 'grid-scale-half', { fullPage: false });
  await page.mouse.up();
  await settle(page);
  l = await logical('apps/crawler');
  const o = await logical('apps/offline');
  b = await box('apps/crawler');
  check(!!l && l.y === 192 && o?.y === 576, `a 96 px drag at 0.5× moves 192 units and pushes offline to 576 (${JSON.stringify(l)}, ${JSON.stringify(o)})`);
  check(ghost === 288, `the push ghost rendered at logical × 0.5 (${ghost})`);
  check(!!b && Math.abs(b.top - 96) <= 1, `the card renders at 96 px (${JSON.stringify(b)})`);

  // the settings menu: slider + label
  await sh(page, (t) => t.openSettings());
  await settle(page);
  const label = await sh(page, (t) => t.query('.wsmenu b.gs')?.textContent ?? '');
  check(/0\.50× · 24 px/.test(label), `the settings menu shows the scale (${label})`);
  check(await sh(page, (t) => !!t.query('.wsmenu input[type=range]')), 'the settings menu has the slider');
  await shot(page, 'grid-scale-menu', { fullPage: false });
  await page.locator('bx-shell .ctx-backdrop').first().dispatchEvent('pointerdown');
  await settle(page);

  await sh(page, (t) => t.setGridScale(1));
  await settle(page); await settle(page);
  b = await box('apps/crawler');
  check(!!b && Math.abs(b.width - 568) <= 1, `back at 1× (${JSON.stringify(b)})`);
  check(await page.evaluate(() => localStorage.getItem('xbin-grid-scale')) === null, 'reset removes the saved scale');

  // the zoom tip: once per browser
  await page.evaluate(() => localStorage.removeItem('xbin-zoom-tip'));
  const tips = () => sh(page, (t) => t.toasts.filter((x) => /grid scale/.test(x.comp)).length);
  await page.keyboard.press('Control+=');
  await settle(page);
  const n1 = await tips();
  await page.keyboard.press('Control+=');
  await settle(page);
  const n2 = await tips();
  check(n1 === 1 && n2 === 1, `a browser-zoom shortcut earns the grid-scale tip once (${n1}, ${n2})`);

  await sh(page, (t, lay) => t.setGeom(() => lay), layout0);
  await sh(page, (t) => t.closeTile('apps/offline'));
  await closeCtx(ctx, page);
  done();
}

module.exports = { gridScale };
