// hack/ui-harness/passes/topbarmenu.js — D247: the top bar stays put in every
// screen mode and at a phone's width (it used to slide away in Document and
// Tiled mode), and a tile's menu — from inside the tile, its title bar or
// its ⋯ — carries the canvas menu's workspace section (Open tile, Create a
// new tile, Add heading or text, New screen) above an Admin submenu, in
// Canvas, Fixed width, Document and Tiled. What a moved line does from
// there: a tile opened from a Tiled tile's menu splits THAT tile (not the
// focused one), from a Document tile's menu it is the row after that
// tile's; a new tile created from it lands the same way; New screen makes
// one. The keyboard through the Admin submenu; a phone's sheet inside
// 390 px. Every menu is photographed (UX2_SHOTS, else OUT), so the pass
// doubles as the before/after record. Run it in both themes
// (HARNESS_THEME=light).
const path = require('path');
const { sleep, login, settle, sh, waitFor, waitSel, openShell, usePersonalScreen, checker, THEME, OUT, fs, log } = require('../lib');

const [A, B, C, D] = ['apps/pinned', 'apps/offline', 'apps/leads', 'apps/crawler'];
const SHOTS = process.env.UX2_SHOTS || OUT;
fs.mkdirSync(SHOTS, { recursive: true });
const snap = async (page, name) => {
  await page.screenshot({ path: path.join(SHOTS, `${name}-${THEME}.png`) });
  log('wrote', `${name}-${THEME}.png`);
};
const rectIn = (page, sel) => sh(page, (t, sel) => { const r = t.query(sel)?.getBoundingClientRect(); return r ? { x: r.x, y: r.y, w: r.width, h: r.height } : null; }, sel);
const leafPath = (t, p, np = '') => (!t ? null : t.leaf !== undefined ? (t.leaf === p ? np : null) : leafPath(t.a, p, np + 'a') ?? leafPath(t.b, p, np + 'b'));
const siblings = (tree, p, q) => { const a = leafPath(tree, p), b = leafPath(tree, q); return a != null && b != null && a.length === b.length && a.slice(0, -1) === b.slice(0, -1); };

// the bar as drawn: in the page's flow above <main>, untransformed, and the
// thing actually under its middle
const bar = (page) => sh(page, (t) => {
  const b = t.query('.top'), m = t.query('main');
  if (!b || !m) return null;
  const r = b.getBoundingClientRect(), mr = m.getBoundingClientRect();
  const hit = document.querySelector('bx-shell').shadowRoot.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
  return { top: Math.round(r.top), bottom: Math.round(r.bottom), main: Math.round(mr.top), tf: getComputedStyle(b).transform,
    under: !!hit?.closest?.('.top'), scrolled: m.scrollTop };
});
const shown = (b) => !!b && b.top >= 0 && b.bottom - b.top >= 30 && b.tf === 'none' && b.under && b.main >= b.bottom - 1;

// the open menu's top-level rows (labels), desktop panel or phone sheet
const rows = (page) => page.locator('bx-menu .panel.main > .it .lb, bx-menu .sheet .sbody > .it .lb').allTextContents();
const closeMenu = async (page) => {
  await page.keyboard.press('Escape');
  await waitFor(page, (t) => !t.menuOpen, null, { label: 'menu closed' }).catch(() => sh(page, (t) => t.closeMenu()));
  await settle(page);
};
// a right-click inside a tile's page (relayed by xbin-client, D56)
async function inTile(page, p, tries = 3) {
  await sh(page, (t, p) => t.query(`.card[data-path="${p}"] .cbody`)?.scrollIntoView({ block: 'nearest' }), p);
  await settle(page);
  const r = await rectIn(page, `.card[data-path="${p}"] .cbody`);
  if (!r) return false;
  const y = Math.min(r.y + r.h - 24, (await page.evaluate(() => innerHeight)) - 24);
  await page.mouse.click(r.x + Math.min(r.w - 24, 160), y, { button: 'right' });
  if (await waitSel(page, 'bx-menu .it', { timeout: 3000 }).then(() => true, () => false)) return true;
  // a page re-laid out (Tiled) may be reloading: its xbin-client relays the click once it is back
  return tries > 1 ? (await sleep(1000), inTile(page, p, tries - 1)) : false;
}
// the workspace section reachable from a tile's menu, above Admin
function sectionOk(ls) {
  const at = (re) => ls.findIndex((l) => re.test(l));
  const o = at(/^Open tile$/), cr = at(/^Create a new tile/), ns = at(/^New screen$/), ad = at(/^Admin$/), add = at(/^Add heading or text$/);
  return o >= 0 && cr > o && add > cr && ns > add && ad > ns;
}

async function topbarMenu(browser) {
  const { check, skip, done } = checker(`topbar-menu-${THEME}`);
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 900 } });
  const prefs = `${process.env.URL || 'http://127.0.0.1:8697'}/api/xbin/prefs/layout`;
  const original = await (await ctx.request.get(prefs)).json();
  await openShell(page);
  await usePersonalScreen(page);
  const id = await sh(page, (t) => t.addScreen('canvas'));
  await settle(page);
  // A and B side by side, D far down so the page scrolls in Canvas and Fixed width
  await sh(page, (t, [A, B, D]) => t.setGeom(() => [{ path: A, x: 0, y: 0, w: 576, h: 384 }, { path: B, x: 624, y: 0, w: 576, h: 384 },
    { path: D, x: 0, y: 1488, w: 576, h: 384 }]), [A, B, D]);
  await waitSel(page, `.card[data-path="${A}"] bx-frame`, { state: 'attached' });
  await sleep(800); // the tile pages load (their xbin-client relays the right-click)

  // ---- the canvas menu, the tab menu, a title bar, the ⋯, a sidebar row ----
  await sh(page, (t) => t.query('.canvas').scrollIntoView({ block: 'start' }));
  await settle(page);
  const cv = await rectIn(page, '.canvas'); // bare canvas: under A, above D
  await page.mouse.click(cv.x + 300, Math.min(cv.y + 440, 860), { button: 'right' });
  await waitSel(page, 'bx-menu .it');
  const canvasRows = await rows(page);
  check(sectionOk([...canvasRows, 'Admin']) && canvasRows.indexOf('New screen') < canvasRows.indexOf('Layout'),
    `the canvas menu: Open tile, Create a new tile, Add heading or text, New screen, then the screen's lines (${JSON.stringify(canvasRows)})`);
  await snap(page, 'canvas-menu-desktop');
  await closeMenu(page);
  await page.locator(`bx-shell .tab[data-screen="${id}"]`).click({ button: 'right' });
  await waitSel(page, 'bx-menu .it');
  await snap(page, 'tab-menu-desktop');
  await closeMenu(page);
  await page.locator(`.card[data-path="${A}"] .head`).click({ button: 'right', position: { x: 60, y: 10 } });
  await waitSel(page, 'bx-menu .it');
  const headRows = await rows(page);
  check(sectionOk(headRows), `a title bar's menu holds the workspace section above Admin (${JSON.stringify(headRows)})`);
  check(headRows.length <= 12, `a tile menu has at most twelve rows at its top level (${headRows.length})`);
  await snap(page, 'tile-head-menu-desktop');
  await closeMenu(page);
  await page.locator(`.card[data-path="${A}"] .head button[aria-label="tile menu"]`).click();
  await waitSel(page, 'bx-menu .it');
  await snap(page, 'tile-more-menu-desktop');
  await closeMenu(page);
  const side = page.locator(`bx-shell .item[data-path="${C}"]`);
  if (await side.count()) {
    await side.click({ button: 'right' });
    await waitSel(page, 'bx-menu .it');
    const sideRows = await rows(page);
    check(sectionOk(sideRows), `a sidebar row's tile menu holds the workspace section too (${JSON.stringify(sideRows)})`);
    await snap(page, 'sidebar-menu-desktop');
    await closeMenu(page);
  } else skip('no sidebar row for apps/leads');

  // ---- per mode: the bar after scrolling, and the menu inside a tile ----
  for (const m of ['canvas', 'fixed', 'doc', 'tiled']) {
    await sh(page, (t, [id, m]) => t.setScreenMode(id, m), [id, m]);
    await sleep(500);
    await settle(page);
    check(shown(await bar(page)), `${m}: the top bar is there on arrival (${JSON.stringify(await bar(page))})`);
    if (m === 'doc') await sh(page, (t) => t.setGeom((ts) => ts.map((o, i) => (o.float ? o : { ...o, doc: { row: i, col: 0, cols: 1, h: 700 } })))); // rows taller than the window
    if (m === 'tiled') { // nothing scrolls: the pointer leaves the bar downwards, onto the tiles
      await page.mouse.move(700, 20); await page.mouse.move(700, 600, { steps: 12 });
    } else {
      await page.mouse.move(700, 600); await page.mouse.wheel(0, 700);
    }
    await sleep(500);
    const b = await bar(page);
    check(shown(b) && (m === 'tiled' || b.scrolled > 0), `${m}: the top bar stays after ${m === 'tiled' ? 'the pointer moved onto the tiles' : 'a scroll down'} (${JSON.stringify(b)})`);
    await snap(page, `topbar-${m}-scrolled-desktop`);
    await sh(page, (t) => { t.query('main').scrollTop = 0; });
    await settle(page);
    if (await inTile(page, A)) {
      const ls = await rows(page);
      check(sectionOk(ls), `${m}: the menu inside a tile holds the workspace section above Admin (${JSON.stringify(ls)})`);
      check(ls.length <= 12, `${m}: at most twelve rows at its top level (${ls.length})`);
      await snap(page, `tile-inside-menu-${m}-desktop`);
      const admin = page.locator('bx-menu .panel.main > .it', { hasText: /^\s*Admin\s*$/ });
      if (m === 'canvas' && await admin.count()) {
        await admin.hover();
        await waitSel(page, 'bx-menu .panel.sub .it');
        await snap(page, 'tile-admin-submenu-desktop');
        await page.locator('bx-menu .panel.main > .it', { hasText: /^\s*Open tile\s*$/ }).hover();
        await waitSel(page, 'bx-menu .panel.sub .q');
        await snap(page, 'tile-open-tile-submenu-desktop');
      }
      await closeMenu(page);
    } else check(false, `${m}: a right-click inside ${A} opened no menu`);
  }

  // ---- what the moved lines do, from a tile's menu ----
  const pick = async (label) => page.locator('bx-menu .panel.main > .it', { hasText: label }).first();
  // Tiled: A | B; B has the focus; Open tile ▸ leads from INSIDE A splits A
  await sh(page, (t, id) => t.setScreenMode(id, 'tiled'), id);
  await sh(page, (t, D) => t.closeTile(D), D);
  await sleep(400);
  const headB = await rectIn(page, `.ttile[data-path="${B}"] .card .head`);
  if (headB) { await page.mouse.click(headB.x + 40, headB.y + headB.h / 2); await settle(page); }
  if (await inTile(page, A)) {
    const ot = await pick(/^\s*Open tile\s*$/);
    if (await ot.count()) {
      await ot.hover();
      await waitSel(page, 'bx-menu .panel.sub .q');
      await page.locator('bx-menu .panel.sub .q').fill('leads');
      await page.keyboard.press('Enter');
      await waitFor(page, (t, p) => t.isOpen(p), C, { label: 'leads opened' });
      await sleep(300);
      const tr = await sh(page, (t) => t.tiling());
      check(siblings(tr, A, C), `Tiled: a tile opened from a tile's menu splits that tile, not the focused one (${JSON.stringify(tr)})`);
      await snap(page, 'tiled-opened-beside-desktop');
    } else check(false, 'Tiled: no Open tile in the menu inside a tile');
    await sh(page, (t) => t.closeMenu());
  } else check(false, `Tiled: a right-click inside ${A} opened no menu`);
  await sh(page, (t, C) => t.closeTile(C), C);
  await settle(page);

  // Tiled: Create a new tile… from inside A → the new tile splits A
  if (await inTile(page, A)) {
    const cr = await pick(/Create a new tile/);
    if (await cr.count()) {
      const slug = `harness-ux-${Date.now().toString(36)}`;
      await cr.hover(); await sleep(250);
      const sub = page.locator('bx-menu .panel.sub .it').first();
      if (await sub.count()) await sub.click(); else await cr.click();
      await waitSel(page, 'bx-dialog[open] input[name="name"]');
      await page.locator('bx-dialog[open] input[name="name"]').fill(slug);
      await page.locator('bx-dialog[open] button', { hasText: /^Create$/ }).click();
      await waitFor(page, (t, p) => t.isOpen(p), `apps/${slug}`, { timeout: 15000, label: 'new tile opened' });
      await sleep(300);
      const tr = await sh(page, (t) => t.tiling());
      check(siblings(tr, A, `apps/${slug}`), `Tiled: a tile created from a tile's menu splits that tile (${JSON.stringify(tr)})`);
      await sh(page, (t, p) => t.closeTile(p), `apps/${slug}`);
    } else check(false, 'Tiled: no Create a new tile in the menu inside a tile');
  } else check(false, `Tiled: a right-click inside ${A} opened no menu`);
  await settle(page);

  // New screen from inside a tile
  const nScreens = await sh(page, (t) => t.screens.length);
  if (await inTile(page, A)) {
    const ns = await pick(/^\s*New screen\s*$/);
    if (await ns.count()) {
      await ns.click();
      await waitFor(page, (t, n) => t.screens.length === n + 1, nScreens, { label: 'a new screen' }).catch(() => {});
      const now = await sh(page, (t) => ({ n: t.screens.length, active: t.activeScreen }));
      check(now.n === nScreens + 1 && now.active !== id, `New screen from a tile's menu makes one and shows it (${nScreens} → ${now.n})`);
    } else check(false, 'no New screen in the menu inside a tile');
    await sh(page, (t) => t.closeMenu());
  }
  await sh(page, (t, id) => t.setScreen(id), id);
  await sleep(300);

  // Document: A, B, D in rows; Open tile ▸ leads from inside A → the row after A's
  await sh(page, (t, id) => t.setScreenMode(id, 'doc'), id);
  await sh(page, (t, D) => t.openTile(D), D);
  await sleep(500);
  const before = await sh(page, (t) => t.docRows());
  if (await inTile(page, A)) {
    const ot = await pick(/^\s*Open tile\s*$/);
    if (await ot.count()) {
      await ot.hover();
      await waitSel(page, 'bx-menu .panel.sub .q');
      await page.locator('bx-menu .panel.sub .q').fill('leads');
      await page.keyboard.press('Enter');
      await waitFor(page, (t, p) => t.isOpen(p), C, { label: 'leads opened' });
      await sleep(300);
      const after = await sh(page, (t) => t.docRows());
      const ra = after.findIndex((r) => r.paths.includes(A)), rc = after.findIndex((r) => r.paths.includes(C));
      check(rc === ra + 1 && after.length === before.length + 1, `Document: a tile opened from a tile's menu is the row after that tile's (${JSON.stringify(after)})`);
    } else check(false, 'Document: no Open tile in the menu inside a tile');
    await sh(page, (t) => t.closeMenu());
  } else check(false, `Document: a right-click inside ${A} opened no menu`);
  await sh(page, (t, C) => t.closeTile(C), C);

  // ---- the keyboard: down to Admin, into its submenu, back, out ----
  await sh(page, (t, id) => t.setScreenMode(id, 'canvas'), id);
  await sleep(400);
  await page.locator(`.card[data-path="${A}"] .head button[aria-label="tile menu"]`).click();
  await waitSel(page, 'bx-menu .it');
  const focused = () => sh(page, () => {
    const m = document.querySelector('bx-shell').shadowRoot.querySelector('bx-menu');
    const a = m?.shadowRoot?.activeElement;
    return a ? { label: a.querySelector('.lb')?.textContent ?? a.textContent.trim(), sub: !!a.closest('.panel.sub'), pop: a.getAttribute('aria-haspopup'), exp: a.getAttribute('aria-expanded') } : null;
  });
  await page.keyboard.press('End');
  const last = await focused();
  check(last?.label === 'Admin' && last.pop === 'menu' && last.exp === 'false', `End: the last row is Admin, a submenu to assistive tech (${JSON.stringify(last)})`);
  await page.keyboard.press('ArrowRight');
  await sleep(200);
  const inSub = await focused();
  check(inSub?.sub && /^(Disable|Enable|Unhide)$/.test(inSub.label), `ArrowRight: into the Admin submenu, its first line focused (${JSON.stringify(inSub)})`);
  await page.keyboard.press('ArrowDown');
  const second = await focused();
  check(second?.sub && second.label === 'Hide', `ArrowDown moves within it (${JSON.stringify(second)})`);
  await page.keyboard.press('ArrowLeft');
  await sleep(150);
  const back = await focused();
  check(back?.label === 'Admin' && !back.sub && await page.locator('bx-menu .panel.sub').count() === 0, `ArrowLeft: back on Admin, the submenu closed (${JSON.stringify(back)})`);
  await page.keyboard.press('Escape');
  await waitFor(page, (t) => !t.menuOpen, null, { label: 'menu closed' }).catch(() => {});
  check(!(await sh(page, (t) => t.menuOpen)), 'Escape closes the menu');

  // ---- a phone: the bar after a scroll, the sheets inside 390 px ----
  await page.setViewportSize({ width: 390, height: 844 });
  await sleep(500);
  for (const m of ['canvas', 'tiled']) {
    await sh(page, (t, [id, m]) => t.setScreenMode(id, m), [id, m]);
    await sleep(400);
    await page.mouse.move(200, 500); await page.mouse.wheel(0, 600); await sleep(400);
    const b = await bar(page);
    check(shown(b), `phone, ${m}: the top bar stays (${JSON.stringify(b)})`);
    await snap(page, `topbar-${m}-scrolled-phone`);
    await sh(page, (t) => { t.query('main').scrollTop = 0; });
  }
  await sh(page, (t, id) => t.setScreenMode(id, 'canvas'), id);
  await sleep(400);
  const vw = () => page.evaluate(() => ({ doc: document.documentElement.scrollWidth, w: innerWidth }));
  const sheetFits = () => sh(page, () => {
    const m = document.querySelector('bx-shell').shadowRoot.querySelector('bx-menu');
    const s = m?.shadowRoot?.querySelector('.sheet'), r = s?.getBoundingClientRect();
    const over = s ? [...s.querySelectorAll('.it, .cell')].filter((e) => { const q = e.getBoundingClientRect(); return q.left < -0.5 || q.right > innerWidth + 0.5; }).length : -1;
    return r ? { l: Math.round(r.left), r: Math.round(r.right), top: Math.round(r.top), over } : null;
  });
  await page.locator(`.card[data-path="${A}"] .head button[aria-label="tile menu"]`).click();
  await waitSel(page, 'bx-menu .sheet .it');
  const ph = await rows(page), fit = await sheetFits(), w0 = await vw();
  check(sectionOk(ph), `phone: the tile sheet holds the workspace section above Admin (${JSON.stringify(ph)})`);
  check(fit && fit.l >= 0 && fit.r <= 390 && fit.top >= 0 && fit.over === 0 && w0.doc <= w0.w, `phone: the sheet and every row inside 390 px (${JSON.stringify({ fit, w0 })})`);
  await snap(page, 'tile-menu-phone');
  const adm = page.locator('bx-menu .sheet .sbody > .it', { hasText: /^\s*Admin\s*$/ });
  if (await adm.count()) {
    await adm.click();
    await waitSel(page, 'bx-menu .sheet .shead button[title=back]');
    const inner = await page.locator('bx-menu .sheet .sbody > .it .lb').allTextContents();
    check(inner.includes('Access…') && inner.includes('Cron…'), `phone: Admin drills in to the admin lines (${JSON.stringify(inner)})`);
    await snap(page, 'tile-admin-submenu-phone');
  } else check(false, 'phone: no Admin row');
  await sh(page, (t) => t.closeMenu());
  await settle(page);
  await sh(page, () => document.querySelector('bx-shell').testApi().openCanvasMenu({ x: 200, y: 600 }));
  await waitSel(page, 'bx-menu .sheet .it');
  await snap(page, 'canvas-menu-phone');
  await sh(page, (t) => t.closeMenu());
  await page.setViewportSize({ width: 1400, height: 900 });
  await sleep(300);

  // tidy: the layout as it was
  await sh(page, (t) => t.flushSave());
  const put = await ctx.request.put(prefs, { data: original, headers: { 'X-Prefs-Writer': 'harness-topbarmenu' } });
  check(put.ok(), `tidy: the original layout back (${put.status()})`);
  await sleep(500);
  await ctx.close();
  done();
}

module.exports = { topbarMenu };
