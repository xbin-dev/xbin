// Playwright passes over the seeded harness workspace: screenshots to LOOK at
// the UI, and asserting passes (PASS/FAIL lines under out/, a throw on any
// FAIL) that are the repo's browser-behaviour regression tests. Env: URL, OUT.
//
//   node shots.js                 # every pass
//   node shots.js windows menus   # only these (also: --pass a,b)
//   node shots.js --list
//
// Passes drive the UI through the elements' testApi() (lib.js); a private-
// member access (dot underscore) in this directory fails `make js-check`.
const {
  URL, fs, sleep, log, login, closeCtx, settle, sh, fr, waitFor, waitSel, openShell, usePersonalScreen,
  openTile, closeTile, tileFrame, gotoTab, shot, dumpSelects, checker, pw,
} = require('./lib');

// Screenshots of the admin console's D54 surfaces, the tile popover and a
// terminal on an org tile.
async function admin(browser) {
  const { ctx, page } = await login(browser, 'admin', 'admin');

  // ---- admin tile: network sets tab ----
  await page.goto(`${URL}/c/tiles/admin/#netsets`);
  await waitSel(page, 'text=Rule grammar', { timeout: 15000 });
  await shot(page, 'admin-netsets');

  // open devs-net's editor, break the LAN row, add a host row
  const card = page.locator('.netsetcard[data-netset="devs-net"]');
  await card.getByRole('button', { name: 'edit' }).click();
  await waitSel(page, '.netsetcard[data-netset="devs-net"] .orow input');
  await shot(page, 'admin-netsets-edit');
  const inputs = card.locator('.orow input');
  const n = await inputs.count();
  for (let i = 0; i < n; i++) {
    const v = await inputs.nth(i).inputValue();
    if (v.startsWith('10.42')) { await inputs.nth(i).fill('10.42.0.0/33'); break; }
  }
  await card.getByRole('button', { name: '+ rule' }).click();
  await settle(page);
  const lastSel = card.locator('.orow select').last();
  await lastSel.selectOption('host');
  await settle(page);
  await shot(page, 'admin-netsets-edit-hint');
  await card.getByRole('button', { name: 'cancel' }).click();

  // ---- orgs tab ----
  await gotoTab(page, 'orgs', 'network (ws-admin, D54)');
  await shot(page, 'admin-orgs');

  // ---- binding tab ----
  await gotoTab(page, 'wiring', 'Each component');
  await shot(page, 'admin-wiring');
  await dumpSelects(page, 'admin-wiring-selects', 'table.tbl select');
  // reveal the custom input on apps/pinned
  const pinnedRow = page.locator('tr', { has: page.locator('td.mono', { hasText: 'apps/pinned' }) }).first();
  await pinnedRow.locator('select').selectOption('__custom');
  await settle(page);
  await shot(page, 'admin-wiring-custom');

  // ---- components tab (runtime detail of the node backends) ----
  // spawn the two node backends first (a request through the proxy does it)
  for (const t of ['apps/crawler', 'apps/racks']) await ctx.request.get(`${URL}/api/${t}/`);
  await gotoTab(page, 'components', 'apps/crawler');
  for (const t of ['apps/crawler', 'apps/racks']) {
    const row = page.locator('tr', { has: page.locator('a, span', { hasText: t }).first() }).first();
    await row.locator('span.caret').waitFor({ timeout: 10000 });
    await row.locator('span.caret').click();
  }
  await settle(page);
  await shot(page, 'admin-components');

  // ---- shell: tile popover + terminal on an org tile ----
  await openShell(page);
  await openTile(page, 'apps/crawler');
  const crawler = page.locator('.card[data-path="apps/crawler"]');
  await crawler.locator('button[title^="tile admin"]').click();
  await waitSel(page, '.admin-pop bx-tile-admin details');
  await sh(page, (t) => {
    const ta = t.tileAdminElement();
    ta.renderRoot.querySelectorAll('details').forEach((d) => { d.open = /runtime|interfaces/.test(d.querySelector('summary')?.textContent ?? ''); });
  });
  await settle(page);
  await sleep(400); // the runtime section's first poll
  await shot(page, 'shell-popover-crawler', { fullPage: false });
  await dumpSelects(page, 'shell-popover-selects', 'bx-tile-admin select');
  await page.keyboard.press('Escape');
  await sh(page, (t) => t.closeAdminWindow());

  await crawler.locator('button.term').click();
  await waitSel(page, 'bx-frame[src="apps/crawler"] select.scope', { timeout: 20000 }); // spawn + session frame
  await shot(page, 'term-admin-crawler', { fullPage: false });
  await dumpSelects(page, 'term-admin-crawler-selects', 'bx-frame select.scope');
  await closeCtx(ctx, page);
}

// Org screens (D55): view bar → edit layout → draft → a competing save →
// conflict dialog → reload theirs; hide an org tab and reopen it from the
// sidebar; the share menu's replace target. dev1 is a devs org admin.
async function screens(browser) {
  const { ctx, page } = await login(browser, 'dev1', 'devpass123');
  const admin = await login(browser, 'admin', 'admin'); // the competing saver
  await openShell(page);
  const orgId = await sh(page, (t) => t.orgScreens.find((s) => s.org === 'devs')?.id);
  if (!orgId) { log('no devs org screen seeded'); await closeCtx(ctx, page); await closeCtx(admin.ctx, admin.page); return; }
  // a dirty draft from an earlier pass survives reloads by design — drop it so this pass starts in view mode
  await sh(page, (t, id) => { if (t.orgDraft(id)) t.dropOrgDraft(id); t.openOrgScreen(id); }, orgId);
  await waitSel(page, 'bx-shell .orgbar');
  await shot(page, 'orgscreen-view', { fullPage: false });
  // edit layout → add a tile from the sidebar → dirty draft
  await page.locator('bx-shell .orgbar button', { hasText: 'edit layout' }).click();
  await waitFor(page, (t, id) => !!t.orgDraft(id), orgId, { label: 'edit mode' });
  await sh(page, (t) => t.toggleTile('apps/offline'));
  await waitSel(page, '.card[data-path="apps/offline"]', { state: 'attached' });
  await shot(page, 'orgscreen-edit', { fullPage: false });
  // someone else saves first (admin, against the current rev) → our save conflicts
  const cur = await sh(page, (t, id) => t.orgScreens.find((s) => s.id === id).rev, orgId);
  const r = await admin.ctx.request.put(`${URL}/api/xbin/screens/org`, { data: { id: orgId, org: 'devs',
    tiles: [{ path: 'apps/pinned', x: 0, y: 0, w: 576, h: 384 }], rev: cur } });
  log('competing save:', r.status(), (await r.text()).slice(0, 120));
  // the users event refreshes the org screens → "newer version" note
  await waitFor(page, (t, a) => (t.orgScreens.find((s) => s.id === a.id)?.rev ?? 0) > a.cur, { id: orgId, cur }, { label: 'newer revision seen' });
  await shot(page, 'orgscreen-edit-newer', { fullPage: false });
  await page.locator('bx-shell .orgbar button', { hasText: 'Save and update' }).click();
  await waitSel(page, 'bx-dialog button:has-text("Reload theirs")');
  await shot(page, 'orgscreen-conflict', { fullPage: false });
  await page.locator('bx-dialog button', { hasText: 'Reload theirs' }).click();
  await waitFor(page, (t, id) => !t.orgDraft(id), orgId, { label: 'draft dropped after reload' });
  await shot(page, 'orgscreen-after-reload', { fullPage: false });
  // edit again and save cleanly
  await page.locator('bx-shell .orgbar button', { hasText: 'edit layout' }).click();
  await waitFor(page, (t, id) => !!t.orgDraft(id), orgId, { label: 'edit mode again' });
  await sh(page, (t) => t.toggleTile('apps/crawler'));
  await settle(page);
  const before = await sh(page, (t, id) => t.orgScreens.find((s) => s.id === id).rev, orgId);
  await page.locator('bx-shell .orgbar button', { hasText: 'Save and update' }).click();
  await waitFor(page, (t, a) => !t.orgDraft(a.id) && (t.orgScreens.find((s) => s.id === a.id)?.rev ?? 0) > a.before, { id: orgId, before }, { label: 'clean save published' });
  await shot(page, 'orgscreen-saved', { fullPage: false });
  // sidebar trees: shared folders (devs: Crawling; ws: Docs) + flat roots
  await shot(page, 'sidebar-trees', { fullPage: false, clip: { x: 0, y: 70, width: 230, height: 620 } });
  // curate devs' shared folders: ✎ → draft → new folder → file a tile → save
  await page.locator('bx-shell .group.owner', { hasText: 'devs' }).hover();
  await page.locator('bx-shell .group.owner', { hasText: 'devs' }).locator('button.pen').click();
  await waitSel(page, 'bx-shell .secbar button:has-text("Save for everyone")');
  await sh(page, (t) => {
    const ctx = t.folderCtx('org:devs');
    if (!ctx.folders.some((f) => f.id === 'f2')) ctx.mutate((fs) => [...fs, { id: 'f2', name: 'Pinned', icon: '📌', items: [] }]);
    t.fileInto('f2', 'apps/pinned', t.folderCtx('org:devs'));
  });
  await settle(page);
  await shot(page, 'sidebar-folders-edit', { fullPage: false, clip: { x: 0, y: 70, width: 230, height: 620 } });
  await page.locator('bx-shell .secbar button', { hasText: 'Save for everyone' }).click();
  await waitSel(page, 'bx-shell .secbar button:has-text("Save for everyone")', { state: 'detached' });
  await shot(page, 'sidebar-folders-saved', { fullPage: false, clip: { x: 0, y: 70, width: 230, height: 620 } });
  // hide the org tab → reopen from the sidebar entry
  await sh(page, (t, id) => t.hideOrgTab(id), orgId);
  await settle(page);
  await shot(page, 'orgtab-hidden', { fullPage: false });
  await page.locator('bx-shell .item.screen.org').first().click();
  await waitFor(page, (t, id) => t.activeScreen === id, orgId, { label: 'org screen reopened' });
  // share menu (replace target) as dev1 on a personal screen
  await sh(page, (t) => { t.setScreen(t.screens[0].id); t.openSettings(); });
  await waitSel(page, 'bx-shell .wsmenu');
  await shot(page, 'share-menu', { fullPage: false });
  await dumpSelects(page, 'share-menu-selects', 'bx-shell .wsmenu select');
  await closeCtx(ctx, page);
  await closeCtx(admin.ctx, admin.page);
}

// Context menus (D56): the canvas menu with its open-tile / create submenus,
// the tile menu from a card head and from a sidebar row, a grid square
// opening a panel, and the admin window at "interfaces" with an open
// multiselect list that must escape the window's edge.
async function menus(browser) {
  const { ctx, page } = await login(browser, 'admin', 'admin');
  await openShell(page);
  await usePersonalScreen(page);
  await sh(page, (t) => {
    // keep some tiles closed (recents list) and free the bottom of the canvas for the right-click
    for (const p of ['apps/offline', 'apps/leads', 'tiles/apidocs', 'tiles/admin']) t.closeTile(p);
    t.openTile('apps/crawler');
    // warm the recents list: open + close a few tiles
    for (const p of ['apps/leads', 'apps/pinned', 'apps/racks']) { t.toggleTile(p); t.toggleTile(p); }
  });
  await waitSel(page, '.card[data-path="apps/crawler"] bx-frame', { state: 'attached' });
  await page.mouse.click(1300, 860, { button: 'right' }); // empty canvas (below the cards)
  await waitSel(page, 'bx-menu .it');
  await shot(page, 'menu-canvas', { fullPage: false });
  await page.locator('bx-menu .it', { hasText: 'Open tile' }).hover();
  await waitSel(page, 'bx-menu .panel.sub .q');
  await shot(page, 'menu-canvas-open-tile', { fullPage: false });
  await page.locator('bx-menu .panel.sub .q').fill('le');
  await settle(page);
  await shot(page, 'menu-canvas-open-tile-filter', { fullPage: false });
  await page.locator('bx-menu .it', { hasText: 'Create a new tile' }).hover();
  await sleep(450); // the submenu swap has its own hover delay
  await shot(page, 'menu-canvas-create', { fullPage: false });
  await page.keyboard.press('Escape');
  await waitFor(page, (t) => !t.menuOpen, null, { label: 'menu closed' });
  await page.locator('.card[data-path="apps/crawler"] .head').click({ button: 'right' });
  await waitSel(page, 'bx-menu .it');
  await shot(page, 'menu-tile-card', { fullPage: false });
  await page.keyboard.press('Escape');
  await waitFor(page, (t) => !t.menuOpen, null, { label: 'menu closed' });
  // right-click INSIDE the tile's iframe: the tile page relays it to the shell
  const cb = await page.locator('.card[data-path="apps/crawler"] .cbody').boundingBox();
  await page.mouse.click(cb.x + 120, cb.y + 90, { button: 'right' });
  await waitSel(page, 'bx-menu .it');
  await shot(page, 'menu-tile-body', { fullPage: false });
  await page.keyboard.press('Escape');
  await waitFor(page, (t) => !t.menuOpen, null, { label: 'menu closed' });
  await page.locator('bx-shell .item[data-path="apps/offline"]').click({ button: 'right' });
  await waitSel(page, 'bx-menu .cell');
  await shot(page, 'menu-tile-sidebar', { fullPage: false });
  await page.locator('bx-menu .cell', { hasText: 'logs' }).click();
  await waitSel(page, 'bx-frame[src="apps/offline"] .pop', { timeout: 15000 });
  await shot(page, 'menu-tile-logs-opened', { fullPage: false });
  await sh(page, (t) => t.frameFor('apps/offline')?.toggleTerminal());
  await sh(page, (t) => t.openAdminWindow('apps/consumer', 'interfaces'));
  await waitSel(page, '.admin-pop bx-tile-admin details[data-sec="interfaces"]');
  await shot(page, 'admin-win-interfaces', { fullPage: false });
  const ms = page.locator('bx-tile-admin bx-multiselect .control').first();
  if (await ms.count()) {
    await ms.click();
    await waitSel(page, 'bx-tile-admin bx-multiselect .menu');
    await shot(page, 'admin-win-multiselect-open', { fullPage: false });
    const r = await sh(page, (t) => {
      const ta = t.tileAdminElement();
      const m = ta?.renderRoot.querySelector('bx-multiselect')?.renderRoot.querySelector('.menu')?.getBoundingClientRect();
      const w = t.adminWindowElement()?.getBoundingClientRect();
      return { menu: m && [m.left, m.top, m.right, m.bottom].map(Math.round), win: w && [w.left, w.top, w.right, w.bottom].map(Math.round), vw: innerWidth, vh: innerHeight };
    });
    log('multiselect list rect', JSON.stringify(r));
  } else log('no multiselect in apps/consumer admin window');
  // click outside closes the admin popover
  await page.mouse.click(1300, 860);
  await settle(page);
  await page.mouse.click(1300, 860);
  await settle(page);
  log('admin popover after outside click:', await sh(page, (t) => !!t.adminWindow));
  await closeCtx(ctx, page);
}

// Phone viewport (D56): the trimmed card head with ⋯, the tile menu and
// the canvas menu as bottom sheets, a sidebar row's ⋯ in the drawer, and the
// admin window as a full-screen sheet.
async function mobile(browser) {
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
  await openShell(page);
  await usePersonalScreen(page);
  await sh(page, (t) => {
    for (const p of ['tiles/manager', 'apps/welcome', 'tiles/apidocs', 'tiles/admin']) t.closeTile(p);
    t.openTile('apps/crawler');
  });
  await waitSel(page, '.card[data-path="apps/crawler"] bx-frame', { state: 'attached' });
  await shot(page, 'm-card-more', { fullPage: false });
  await page.locator('.card[data-path="apps/crawler"] .head button[title^="tile menu"]').tap();
  await waitSel(page, 'bx-menu .shead');
  await shot(page, 'm-sheet-tile', { fullPage: false });
  await page.locator('bx-menu .shead button[title=close]').tap();
  await waitFor(page, (t) => !t.menuOpen, null, { label: 'sheet closed' });
  await sh(page, (t) => t.openCanvasMenu({ x: 200, y: 600 }));
  await waitSel(page, 'bx-menu .shead');
  await shot(page, 'm-sheet-canvas', { fullPage: false });
  await page.locator('bx-menu .it', { hasText: 'Open tile' }).tap();
  await waitSel(page, 'bx-menu .q');
  await shot(page, 'm-sheet-canvas-open-tile', { fullPage: false });
  await page.locator('bx-menu .shead button[title=close]').tap();
  await waitFor(page, (t) => !t.menuOpen, null, { label: 'sheet closed' });
  await page.locator('bx-shell .ham').tap();
  await waitSel(page, 'bx-shell .item[data-path="apps/offline"] .more');
  await page.locator('bx-shell .item[data-path="apps/offline"] .more').tap();
  await waitSel(page, 'bx-menu .shead');
  await shot(page, 'm-sheet-sidebar', { fullPage: false });
  await page.locator('bx-menu .shead button[title=close]').tap();
  await waitFor(page, (t) => !t.menuOpen, null, { label: 'sheet closed' });
  await sh(page, (t) => t.setDrawer(false));
  await sh(page, (t) => t.openAdminWindow('apps/crawler', 'interfaces'));
  await waitSel(page, '.admin-pop bx-tile-admin details[data-sec="interfaces"]');
  await shot(page, 'm-admin-sheet', { fullPage: false });
  await closeCtx(ctx, page);
}

// An org admin's view: term-net per tile, the organisations tile, and a
// terminal (scope picker) on each of their tiles.
async function orgAdmin(browser, user, pass, tiles) {
  const { ctx, page } = await login(browser, user, pass);
  for (const t of tiles) {
    const r = await ctx.request.get(`${URL}/api/xbin/term-net?tile=${encodeURIComponent(t)}`);
    fs.appendFileSync(`${require('./lib').OUT}/term-net-${user}.json`, `${t}: ${await r.text()}\n`);
  }
  await page.goto(`${URL}/c/tiles/organisations/`);
  await waitSel(page, 'text=my organisations', { timeout: 15000 });
  await shot(page, `orgs-tile-${user}`);
  await dumpSelects(page, `orgs-tile-${user}-selects`, 'bx-organisations select');

  await openShell(page);
  await usePersonalScreen(page); // a sidebar click on an org screen would open a draft
  for (const t of tiles) {
    await openTile(page, t);
    const card = page.locator(`.card[data-path="${t}"]`);
    if (!(await card.count())) { log(user, 'no card for', t); continue; }
    await card.locator('button.term').click();
    await waitSel(page, `bx-frame[src="${t}"] select.scope`, { timeout: 20000 });
    const slug = t.replace(/\W+/g, '-');
    await shot(page, `term-${user}-${slug}`, { fullPage: false });
    await dumpSelects(page, `term-${user}-${slug}-selects`, 'bx-frame select.scope');
    // close the pop-up so the next tile's terminal is the one on screen
    await fr(page, t, (f) => f?.closeTerminal());
    await settle(page);
  }
  await closeCtx(ctx, page);
}

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

  // 1. a persisted off-screen pop-up restores inside the viewport
  await openShell(page);
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
    t.setSpawnWindows([{ id: 'hw', from: 'apps/crawler', src: 'apps/crawler', reply() {}, title: 'parked', x: 5000, y: 4000, w: 400, h: 300, z: 3000 }]);
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

  // tidy: the next pass starts from the seeded layout
  await sh(page, (t) => { t.setSpawnWindows([]); t.closeTile('apps/offline'); localStorage.removeItem('bx-term:apps/crawler'); });
  await settle(page);
  await closeCtx(ctx, page);
  done();
}

// A tile reload must not touch focus or z-order. apps/focusy focuses its
// input on every load; with the crawler float (terminal pop-up open, focus in
// the terminal) on top of it, a focusy reload must leave the floats' order
// alone and hand the stolen focus back to the terminal. A negative control
// first proves the tile really does grab focus in this browser.
async function reloadFocus(browser) {
  const { check, done } = checker('reload-focus');
  const { ctx, page } = await login(browser, 'admin', 'admin');
  await openShell(page);
  await usePersonalScreen(page);
  await sh(page, (t) => { t.openTile('apps/crawler'); t.openTile('apps/focusy'); });
  await waitSel(page, '.card[data-path="apps/focusy"] bx-frame', { state: 'attached' });
  await sh(page, (t) => t.setGeom((tiles) => tiles.map((o) => {
    if (o.path === 'apps/focusy') return { ...o, float: { x: 300, y: 120, w: 520, h: 380, z: 100 } };
    if (o.path === 'apps/crawler') return { ...o, float: { x: 80, y: 80, w: 520, h: 360, z: 200 } };
    return o;
  })));
  await waitSel(page, '.float[data-path="apps/focusy"] bx-frame', { state: 'attached' });
  await tileFrame(page, 'apps/focusy');
  const state = () => sh(page, (t) => {
    let el = document.activeElement;
    while (el?.shadowRoot?.activeElement) el = el.shadowRoot.activeElement;
    const host = el?.getRootNode?.()?.host;
    return { active: el?.tagName, activeSrc: host?.tagName === 'BX-FRAME' ? host.src : (host?.tagName ?? ''), zCrawler: t.floatOf('apps/crawler')?.z, zFocusy: t.floatOf('apps/focusy')?.z };
  });
  const focusTerm = (p) => fr(page, p, (f) => f.focusTerminal());
  // open the crawler terminal and put the caret in it
  await fr(page, 'apps/crawler', (f) => f.open('term'));
  await waitSel(page, 'bx-frame[src="apps/crawler"] bx-terminal textarea', { timeout: 20000 });
  await focusTerm('apps/crawler');
  await settle(page);
  let s = await state();
  check(s.active === 'TEXTAREA' && s.zCrawler > s.zFocusy, `setup: caret in the crawler terminal, crawler float on top (${JSON.stringify(s)})`);

  // Control: focusing the tile's iframe fronts its float — the exact chain a
  // reloaded document triggers when it grabs focus (iframe focus → window
  // blur → the shell fronts that float). Parent-side iframe.focus() is the
  // deterministic stand-in: a sandboxed tile can't steal focus in a headless
  // browser without a user gesture, but the shell's blur path is identical.
  await fr(page, 'apps/focusy', (f, t) => { f.iframe.focus(); t.raiseFocusedFloat(); });
  await sleep(150); // _raiseFocusedFloat decides on the next tick
  s = await state();
  check(s.zFocusy > s.zCrawler, `control: focusing a tile's iframe fronts its float (${JSON.stringify(s)})`);

  // reset: crawler back on top, caret back in its terminal
  await sh(page, (t) => { t.setFloat('apps/crawler', { z: 200 }); t.setFloat('apps/focusy', { z: 100 }); });
  await focusTerm('apps/crawler');
  await settle(page);

  // Fix: the same focus-into-iframe DURING a reload must not front the float,
  // and the focus the reload stole goes back to the terminal.
  await fr(page, 'apps/focusy', (f, t) => {
    f.beginReload();   // reloading = true; captures the terminal as the prior focus
    f.iframe.focus();  // the reloaded document grabs focus
    t.raiseFocusedFloat();
  });
  await sleep(150);
  s = await state();
  check(s.zCrawler > s.zFocusy, `reload leaves the z-order alone (${JSON.stringify(s)})`);
  // …but a real click into the reloading tile (pointer over it) still fronts it
  await fr(page, 'apps/focusy', (f, t) => { f.setHover(true); f.iframe.focus(); t.raiseFocusedFloat(); });
  await sleep(150);
  s = await state();
  check(s.zFocusy > s.zCrawler, `a real click into a reloading tile still fronts it (${JSON.stringify(s)})`);
  // pointer away again, crawler back on top; finishing the reload hands focus back
  await fr(page, 'apps/focusy', (f, t) => { f.setHover(false); t.setFloat('apps/crawler', { z: 200 }); t.setFloat('apps/focusy', { z: 100 }); });
  await settle(page);
  await fr(page, 'apps/focusy', (f) => f.notifyLoad());
  await sleep(500); // the load handler restores focus after a 350 ms beat
  s = await state();
  check(s.active === 'TEXTAREA', `reload hands focus back to the terminal (${JSON.stringify(s)})`);
  await shot(page, 'reload-focus', { fullPage: false });
  // tidy
  await sh(page, (t) => { t.closeTile('apps/crawler'); t.closeTile('apps/focusy'); localStorage.removeItem('bx-term:apps/crawler'); });
  await settle(page);
  await closeCtx(ctx, page);
  done();
}

// cap:open-links (ND11): a tile's target=_blank links are dead until the
// grant; approving it re-keys the iframe with allow-popups +
// allow-popups-to-escape-sandbox (the attribute AND the CSP header of a
// direct open) and a click opens a page; revoking takes it back. Failures throw.
async function openLinks(browser) {
  const { check, done } = checker('open-links');
  const { ctx, page } = await login(browser, 'admin', 'admin');
  const grant = { from: 'apps/linky', target: 'cap:open-links', role: 'writer' };
  const jget = async (p) => (await ctx.request.get(`${URL}/api/xbin${p}`)).json();
  await ctx.request.delete(`${URL}/api/xbin/grants`, { data: grant }); // clean slate
  const g = await jget('/grants');
  check((g.pending ?? []).some((p) => p.from === grant.from && p.target === grant.target && p.role === 'writer'), 'the declared cap lands pending (never auto-granted)');

  await openShell(page);
  await usePersonalScreen(page);
  await openTile(page, 'apps/linky');
  await tileFrame(page, 'apps/linky');
  const attr = () => fr(page, 'apps/linky', (f) => ({ sandbox: f?.iframe.getAttribute('sandbox') ?? '', credentialless: !!f?.iframe.hasAttribute('credentialless') }));
  const csp = async () => (await ctx.request.get(`${URL}/c/apps/linky/`)).headers()['content-security-policy'] ?? '';

  let a = await attr();
  check(a.sandbox.includes('allow-scripts') && !a.sandbox.includes('allow-popups'), `ungranted: attribute lacks allow-popups (${a.sandbox})`);
  let h = await csp();
  check(h.startsWith('sandbox') && !h.includes('allow-popups'), `ungranted: CSP lacks allow-popups (${h})`);
  const before = ctx.pages().length;
  await (await tileFrame(page, 'apps/linky')).click('#ext');
  await sleep(1200); // a negative: nothing to wait for
  check(ctx.pages().length === before, 'ungranted: a click opens no page');

  check((await ctx.request.post(`${URL}/api/xbin/grants`, { data: grant })).ok(), 'approve via API');
  await waitFor(page, (t) => (t.frameFor('apps/linky')?.testApi().iframe.getAttribute('sandbox') ?? '').includes('allow-popups-to-escape-sandbox'), null, { label: 'grant re-keyed the iframe' });
  a = await attr();
  check(a.sandbox.includes('allow-popups') && a.sandbox.includes('allow-popups-to-escape-sandbox'), `granted: attribute carries both tokens (${a.sandbox}; credentialless=${a.credentialless})`);
  h = await csp();
  check(h.includes('allow-popups allow-popups-to-escape-sandbox'), `granted: CSP extended (${h})`);
  const frame2 = await tileFrame(page, 'apps/linky'); // the re-keyed iframe's document
  await frame2.waitForSelector('#ext', { timeout: 10000 });
  const [popup] = await Promise.all([
    ctx.waitForEvent('page', { timeout: 6000 }).catch(() => null),
    frame2.click('#ext'),
  ]);
  check(!!popup, `granted: a click opens a page (credentialless frame: ${a.credentialless})`);
  if (popup) {
    await popup.waitForLoadState().catch(() => {});
    check(popup.url().startsWith(`${URL}/docs/`), `the page is the docs viewer (${popup.url()})`);
    await popup.close();
  }
  await shot(page, 'open-links-granted', { fullPage: false });

  await ctx.request.delete(`${URL}/api/xbin/grants`, { data: grant });
  await waitFor(page, (t) => !(t.frameFor('apps/linky')?.testApi().iframe.getAttribute('sandbox') ?? '').includes('allow-popups'), null, { label: 'revoke re-keyed the iframe' });
  a = await attr();
  check(!a.sandbox.includes('allow-popups'), `revoked: attribute back to base (${a.sandbox})`);
  await closeTile(page, 'apps/linky');
  await closeCtx(ctx, page);
  done();
}

// Copy from the tile menu (D56 amendment): selected text inside a tile rides
// the relay and the menu leads with Copy; inputs and selected shell text keep
// the native menu (Playwright shows none — ours must simply not open). The
// plain-http path (no navigator.clipboard) goes through execCommand('copy').
async function contextCopy(browser) {
  const { check, done } = checker('context-copy');
  const { ctx, page } = await login(browser, 'admin', 'admin');
  await ctx.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: URL });
  await openShell(page);
  await usePersonalScreen(page);
  await openTile(page, 'apps/focusy');
  const frame = await tileFrame(page, 'apps/focusy');
  await frame.waitForSelector('p', { timeout: 10000 });
  await page.bringToFront();
  const menu = () => sh(page, (t) => t.menuOpen);
  check(!!frame, 'the focusy frame is up');
  const selectP = () => frame.evaluate(() => {
    const p = document.querySelector('p'); const r = document.createRange(); r.selectNodeContents(p);
    const s = getSelection(); s.removeAllRanges(); s.addRange(r); return s.toString();
  });

  // (a) selected text in the tile → our menu with Copy on top; Copy writes the clipboard
  const want = await selectP();
  check(want === 'this tile focuses its input on every load', `selection made in the tile (${JSON.stringify(want)})`);
  const pb = await frame.locator('p').boundingBox();
  await page.mouse.click(pb.x + 12, pb.y + pb.height / 2, { button: 'right' });
  await waitSel(page, 'bx-menu .it');
  check(await menu(), 'right-click on selected tile text opens the tile menu');
  const first = page.locator('bx-menu .it').first();
  check(((await first.locator('.lb').textContent().catch(() => '')) ?? '').trim() === 'Copy', 'Copy is the first row');
  check(((await first.locator('.hint').textContent().catch(() => '')) ?? '').includes('focuses its input'), 'the hint shows the snippet');
  await shot(page, 'menu-tile-copy', { fullPage: false });
  await first.click();
  await waitFor(page, (t) => !t.menuOpen, null, { label: 'menu closed on Copy' });
  check(!(await menu()), 'the menu closed on Copy');
  const clip = await page.evaluate(() => navigator.clipboard.readText());
  check(clip === want, `clipboard holds the selection (${JSON.stringify(clip)})`);
  check(await sh(page, (t) => t.toasts.some((x) => x.message === 'copied')), 'a "copied" toast showed');

  // (d) the plain-http path: no navigator.clipboard → execCommand('copy') on a scratch textarea
  await page.evaluate(() => Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true }));
  await selectP();
  await page.mouse.click(pb.x + 12, pb.y + pb.height / 2, { button: 'right' });
  await waitSel(page, 'bx-menu .it');
  await page.locator('bx-menu .it', { hasText: 'Copy' }).click();
  await waitFor(page, (t) => !t.menuOpen, null, { label: 'menu closed on Copy (fallback)' });
  await page.evaluate(() => { delete navigator.clipboard; }); // back to the prototype getter
  const clip2 = await page.evaluate(() => navigator.clipboard.readText());
  check(clip2 === want, `execCommand fallback wrote the clipboard (${JSON.stringify(clip2)})`);
  check(await page.evaluate(() => !document.querySelector('body > textarea')), 'the scratch textarea is gone');

  // (b) right-click on the tile's <input> → native menu; ours must not open
  const ib = await frame.locator('#i').boundingBox();
  await page.mouse.click(ib.x + 10, ib.y + ib.height / 2, { button: 'right' });
  await sleep(500); // a negative: give a wrong menu time to appear
  check(!(await menu()), 'right-click on an input inside the tile leaves the native menu');

  // (c) selected text in the shell's own chrome → native menu. Card heads and
  // sidebar rows are user-select:none (nobody can select them); the grants /
  // bindings panels carry real selectable text, in a nested shadow root.
  const who = page.locator('bx-shell bx-grants .who, bx-shell bx-bindings .who').first();
  const whoText = ((await who.textContent().catch(() => '')) ?? '').trim();
  const shellSel = await sh(page, (t) => {
    const el = t.query('bx-grants')?.renderRoot?.querySelector('.who') ?? t.query('bx-bindings')?.renderRoot?.querySelector('.who');
    if (!el) return null;
    const r = document.createRange(); r.selectNodeContents(el);
    const s = document.getSelection(); s.removeAllRanges(); s.addRange(r);
    return t.selectedText();
  });
  check(!!whoText && shellSel !== null && shellSel.trim() === whoText, `shell-side selection detected (${JSON.stringify(shellSel)} vs ${JSON.stringify(whoText)})`);
  await who.click({ button: 'right' });
  await sleep(500);
  check(!(await menu()), 'right-click on selected shell text leaves the native menu');
  await page.mouse.click(1300, 860); // a left click on the canvas clears the selection
  await settle(page);
  await who.click({ button: 'right' });
  await waitSel(page, 'bx-menu .it');
  check(await menu(), 'the same text without a selection opens the shell menu');
  await page.keyboard.press('Escape');
  await waitFor(page, (t) => !t.menuOpen, null, { label: 'menu closed' });

  await closeTile(page, 'apps/focusy');
  await closeCtx(ctx, page);
  done();
}

// The permission-set creator (D57): build a set from typed rows, see each
// entry in words, get stopped on a bad field, create + attach in one save,
// reopen it with the rows parsed back, and edit an org's extra entries with
// the same rows. Asserts against the UI and the API. Failures throw.
async function permSets(browser) {
  const { check, done } = checker('perm-sets');
  const { ctx, page } = await login(browser, 'admin', 'admin');
  const LONGP = 'apps/a-provider-with-a-deliberately-long-component-path-for-overflow';
  const jget = async (p) => (await ctx.request.get(`${URL}/api/xbin${p}`)).json();
  // clean slate (a previous run may have died mid-way)
  await ctx.request.patch(`${URL}/api/xbin/orgs/sales`, { data: { sets: [] } });
  await ctx.request.delete(`${URL}/api/xbin/permission-sets/infra`);

  await page.goto(`${URL}/c/tiles/admin/#permsets`);
  await waitSel(page, 'text=new permission set', { timeout: 15000 });
  await shot(page, 'permsets-empty');
  await page.click('button[data-new-set]');
  await waitSel(page, '.seteditor input[name=setname]');
  const ed = page.locator('.seteditor');
  const row = (i) => ed.locator('.allowrow').nth(i);
  await ed.locator('input[name=setname]').fill('infra');
  // row 1 (default kind: use a tile) — the llm-gw case, on a seeded tile
  await row(0).locator('input[name=value]').fill('apps/crawler');
  await row(0).locator('select[name=role]').selectOption('writer');
  await settle(page);
  let desc = await row(0).locator('.allow-desc').textContent();
  check(/let their tiles use apps\/crawler as writer/.test(desc) && /tile:apps\/crawler@writer/.test(desc), `tile row reads back in words + entry ("${desc.trim()}")`);
  // row 2: bind an interface, pinned to the seeded feed provider
  await ed.locator('button[data-add-entry]').click();
  await row(1).locator('select[name=kind]').selectOption('iface');
  await row(1).locator('input[name=value]').fill('feed');
  await row(1).locator('input[name=provider]').fill(LONGP);
  await settle(page);
  desc = await row(1).locator('.allow-desc').textContent();
  check(new RegExp(`bind a "feed" interface slot to ${LONGP.replace(/[-/]/g, '\\$&')}`).test(desc) && desc.includes(`iface:feed@${LONGP}`), `iface row reads back ("${desc.trim().slice(0, 80)}…")`);
  // row 3: a host port — first an impossible one: save must be blocked with a reason
  await ed.locator('button[data-add-entry]').click();
  await row(2).locator('select[name=kind]').selectOption('ingress:listen');
  await row(2).locator('input[name=value]').fill('99999');
  await settle(page);
  const pill = await row(2).locator('.err-pill').textContent().catch(() => '');
  const saveDisabled = await ed.locator('button[data-save-set]').isDisabled();
  check(saveDisabled && /1–65535/.test(pill), `a bad port blocks create with a reason (disabled=${saveDisabled} "${pill}")`);
  await row(2).locator('input[name=value]').fill('8080-8090');
  await settle(page);
  check(!(await ed.locator('button[data-save-set]').isDisabled()), 'fixing the port re-enables create');
  // an instance without a provider is refused too
  await row(1).locator('input[name=provider]').fill('');
  await row(1).locator('input[name=instance]').fill('dev');
  await settle(page);
  check(await ed.locator('button[data-save-set]').isDisabled(), 'an instance without a provider blocks create');
  await row(1).locator('input[name=provider]').fill(LONGP);
  await row(1).locator('input[name=instance]').fill('');
  await settle(page);
  // attach to sales (the multiselect is driven through the draft, as a person's picks would land)
  await page.evaluate(() => { const a = document.querySelector('bx-admin').testApi(); a.setDraft('permset:new', { ...a.draft('permset:new'), orgs: ['sales'] }); });
  await settle(page);
  await shot(page, 'permsets-creator');
  await ed.locator('button[data-save-set]').click();
  await waitSel(page, '.setcard[data-set="infra"]', { timeout: 10000 });
  let ps = await jget('/permission-sets');
  const want = ['tile:apps/crawler@writer', `iface:feed@${LONGP}`, 'ingress:listen:8080-8090'];
  check(JSON.stringify(ps.sets?.infra?.allow) === JSON.stringify(want), `set stored with the exact entries (${JSON.stringify(ps.sets?.infra?.allow)})`);
  check((ps.attachedTo?.infra ?? []).includes('sales'), `attached to sales on create (${JSON.stringify(ps.attachedTo?.infra)})`);
  const sales = (await jget('/orgs')).orgs.find((o) => o.id === 'sales');
  check((sales?.resolvedAllow ?? []).includes('tile:apps/crawler@writer'), `sales' resolved allowance carries the entry (${JSON.stringify(sales?.resolvedAllow)})`);
  // the card shows the entries in words; edit parses them back into typed rows
  const cardText = await page.locator('.setcard[data-set="infra"]').textContent();
  check(/let their tiles use apps\/crawler as writer/.test(cardText) && /publish their tiles on host ports 8080-8090/.test(cardText), 'card describes the entries in words');
  await shot(page, 'permsets-card');
  await page.locator('.setcard[data-set="infra"] button', { hasText: 'edit' }).click();
  await waitSel(page, '.seteditor .allowrow select[name=kind]');
  const kinds = await page.locator('.seteditor .allowrow select[name=kind]').evaluateAll((els) => els.map((e) => e.value));
  check(JSON.stringify(kinds) === JSON.stringify(['tile', 'iface', 'ingress:listen']), `edit reopens the rows typed (${JSON.stringify(kinds)})`);
  const role = await page.locator('.seteditor .allowrow').nth(0).locator('select[name=role]').inputValue();
  check(role === 'writer', `role cap restored (${role})`);
  await page.locator('.seteditor button', { hasText: 'cancel' }).click();
  // the org card's extra entries use the same rows
  await gotoTab(page, 'orgs', 'network (ws-admin, D54)');
  await page.locator('button[data-edit-allow]').first().click();
  await waitSel(page, '.editor:has(button[data-save-allow])');
  const oed = page.locator('.editor:has(button[data-save-allow])');
  await oed.locator('button[data-add-entry]').click();
  const orow = oed.locator('.allowrow').last();
  await orow.locator('select[name=kind]').selectOption('cap');
  await orow.locator('input[name=value]').fill('containers');
  await settle(page);
  await oed.locator('button[data-save-allow]').click();
  await waitSel(page, '.editor:has(button[data-save-allow])', { state: 'detached' });
  let orgs = (await jget('/orgs')).orgs;
  const edited = orgs.find((o) => (o.allow ?? []).includes('cap:containers'));
  check(!!edited, `org extra allow saved through the typed rows (${edited?.id})`);
  await shot(page, 'permsets-org-allow');
  // tidy: detach + delete, drop the org entry
  if (edited) await ctx.request.patch(`${URL}/api/xbin/orgs/${edited.id}`, { data: { allow: (edited.allow ?? []).filter((a) => a !== 'cap:containers') } });
  await ctx.request.patch(`${URL}/api/xbin/orgs/sales`, { data: { sets: [] } });
  const del = await ctx.request.delete(`${URL}/api/xbin/permission-sets/infra`);
  check(del.ok(), `tidy: set deleted after detaching (${del.status()})`);
  await closeCtx(ctx, page);
  done();
}

// Net pickers must never show a refused bind as a success (the "org admin
// could still grant host" report). Asserts, not just screenshots: refused
// options are disabled, a refused custom ref snaps the select back and the
// reason lands inside the interfaces section, a tile the person may not wire
// is read-only, the root prompt lists only approvable slots, and the ⚙
// popover sizes to its content. Failures throw at the end of the pass.
async function netPickers(browser) {
  const { check, done } = checker('net-pickers');
  const popState = (page) => sh(page, (t) => {
    const pop = t.adminWindowElement();
    const ta = t.tileAdminElement();
    const sec = ta?.renderRoot.querySelector('details[data-sec="interfaces"]');
    const sel = sec?.querySelector('select');
    return {
      height: pop?.getBoundingClientRect().height ?? 0, inner: window.innerHeight,
      readonly: !!sec?.querySelector('[data-readonly]'),
      hasSelect: !!sel,
      value: sel?.value ?? null,
      disabled: [...(sel?.options ?? [])].filter((o) => o.disabled).map((o) => o.value),
      err: sec?.querySelector('.err')?.textContent?.trim() ?? '',
    };
  });
  const openPop = async (page, tile) => {
    await sh(page, (t, p) => t.openAdminWindow(p, 'interfaces'), tile);
    await waitSel(page, '.admin-pop bx-tile-admin details[data-sec="interfaces"]');
    await waitFor(page, (t) => { const sec = t.tileAdminElement()?.renderRoot.querySelector('details[data-sec="interfaces"]'); return !!sec && (sec.querySelector('select') || sec.querySelector('[data-readonly]')); }, null, { label: 'interfaces section populated' });
    return popState(page);
  };
  const closePop = async (page) => { await sh(page, (t) => t.closeAdminWindow()); await settle(page); };

  // ---- workspace admin on an org tile (devs-net: internet + lan 10.42/16 + github, no host) ----
  {
    const { ctx, page } = await login(browser, 'admin', 'admin');
    // deterministic start: pinned bound to internet (a fresh seed leaves it unbound)
    await ctx.request.post(`${URL}/api/xbin/bindings`, { data: { component: 'apps/pinned', slot: 'net', provider: 'internet' } });
    await openShell(page);
    let s = await openPop(page, 'apps/pinned');
    check(s.hasSelect, 'admin: org tile offers a net select');
    check(s.disabled.includes('host'), `admin: host is disabled on apps/pinned (disabled=${s.disabled.join(',')})`);
    check(!s.disabled.includes('internet'), 'admin: internet stays enabled (inside devs-net)');
    check(s.height > 120 && s.height < s.inner * 0.7 - 1, `admin: popover sized to content (${Math.round(s.height)}px of ${s.inner})`);
    const before = s.value;
    const sel = page.locator('bx-shell bx-tile-admin details[data-sec="interfaces"] select').first();
    // a successful re-bind must show the NEW value at once (it once jumped
    // back to the old one until a second pick), and the server must agree
    const boundNow = async () => [].concat((await (await ctx.request.get(`${URL}/api/xbin/bindings`)).json()).bindings?.['apps/pinned']?.net ?? [])[0];
    const waitBound = async (v) => { for (let i = 0; i < 40 && (await boundNow()) !== v; i++) await sleep(100); };
    await sel.selectOption('org');
    await waitBound('org');
    await settle(page);
    let v = await sel.inputValue();
    check(v === 'org', `admin: select shows the new value right after a successful bind (now "${v}")`);
    check((await boundNow()) === 'org', 'admin: server bound org');
    await sel.selectOption(before);
    await waitBound(before);
    await settle(page);
    v = await sel.inputValue();
    check(v === before, `admin: switching back shows "${before}" (now "${v}")`);
    check((await boundNow()) === before, `admin: server bound ${before} again`);
    // a refused custom ref: outside the set → 400 → select snaps back, reason in-section
    await sel.selectOption('__custom');
    await waitSel(page, 'bx-shell bx-tile-admin details[data-sec="interfaces"] form input[name="ref"]');
    const form = page.locator('bx-shell bx-tile-admin details[data-sec="interfaces"] form');
    await form.locator('input[name="ref"]').fill('lan:10.0.0.0/8');
    await form.locator('button[type="submit"]').click();
    await waitFor(page, (t) => /not covered/.test(t.tileAdminElement()?.renderRoot.querySelector('details[data-sec="interfaces"] .err')?.textContent ?? ''), null, { label: 'refusal shown' });
    await shot(page, 'net-picker-admin-refused', { fullPage: false });
    s = await popState(page);
    check(/not covered/.test(s.err), `admin: refusal shown inside the section ("${s.err.slice(0, 60)}")`);
    check(s.value === before, `admin: select snapped back to "${before}" (now "${s.value}")`);
    await closePop(page);
    await closeCtx(ctx, page);
  }

  // ---- org admin dev1: own personal tile is read-only; org tile offers the picker minus host ----
  {
    const { ctx, page } = await login(browser, 'dev1', 'devpass123');
    await openShell(page);
    await usePersonalScreen(page);
    let s = await openPop(page, 'apps/dev1-notes');
    await shot(page, 'net-picker-dev1-personal', { fullPage: false });
    check(s.readonly && !s.hasSelect, `dev1: personal tile wiring is read-only (readonly=${s.readonly} select=${s.hasSelect})`);
    await closePop(page);
    s = await openPop(page, 'apps/pinned');
    check(s.hasSelect && s.disabled.includes('host'), `dev1: org tile has a picker with host disabled (select=${s.hasSelect} disabled=${s.disabled.join(',')})`);
    await closePop(page);
    // the root bind prompt: only slots dev1 may wire (never the personal tile's)
    const prompt = await sh(page, (t) => t.query('bx-bindings')?.renderRoot?.textContent ?? '');
    check(!prompt.includes('apps/dev1-notes'), 'dev1: root bind prompt does not offer the personal tile');
    // and the server view says the same
    const r = await ctx.request.get(`${URL}/api/xbin/bindings`);
    const d = await r.json();
    check(d.approvable?.['apps/pinned'] === true && !d.approvable?.['apps/dev1-notes'], `dev1: approvable = ${JSON.stringify(d.approvable)}`);
    const pin = (d.pending ?? []).find((p) => p.component === 'apps/dev1-notes');
    check(!pin || pin.approvable === false, 'dev1: pending row for the personal tile is not approvable');
    await closeCtx(ctx, page);
  }
  done();
}

// The access map tab (the first admin tab split into its own element): the
// structure + matrix render from /access-matrix, a cell click opens the
// derivation panel, the owner-transfer editor previews before it commits.
async function adminMap(browser) {
  const { check, done } = checker('admin-map');
  const { ctx, page } = await login(browser, 'admin', 'admin');
  await gotoTab(page, 'map', 'effective access');
  await waitSel(page, '.mcell.has, .maprow', { timeout: 15000 });
  const cells = await page.locator('.mcell.has').count();
  check(cells > 0, `matrix renders ${cells} access cells`);
  check((await page.locator('.snode.org').count()) >= 3, 'structure lists the seeded orgs');
  await page.locator('.mcell.has').first().click();
  await waitSel(page, 'text=effective (highest wins)');
  check(true, 'a cell click opens the derivation panel');
  await shot(page, 'admin-map');
  // transfer: pick a different owner → preview → the commit button appears → cancel
  const row = page.locator('tr', { has: page.locator('td.mtile', { hasText: 'apps/pinned' }) }).first();
  await row.locator('button', { hasText: 'transfer' }).click();
  await waitSel(page, 'button:has-text("preview")');
  const sel = page.locator('select', { has: page.locator('option[value=""]') }).last();
  await sel.selectOption('');
  await page.locator('button', { hasText: 'preview' }).click();
  await waitSel(page, 'button.go:has-text("transfer")');
  check(true, 'preview yields the transfer button (no commit made)');
  await shot(page, 'admin-map-transfer');
  await page.locator('button', { hasText: 'cancel' }).last().click();
  await waitSel(page, 'button:has-text("preview")', { state: 'detached' });
  const owner = (await ctx.request.get(`${URL}/api/xbin/owner?tile=apps/pinned`).then((r) => r.json())).owner;
  check(owner === 'org:devs', `cancel left apps/pinned with its owner (${owner})`);
  await closeCtx(ctx, page);
  done();
}

// ---- pass registry + CLI ----
const PASSES = {
  admin, adminMap, menus, mobile, screens,
  orgAdmin: async (b) => { await orgAdmin(b, 'dev1', 'devpass123', ['apps/crawler', 'apps/dev1-notes']); await orgAdmin(b, 'sales1', 'salespass123', ['apps/leads']); },
  netPickers, windows, reloadFocus, permSets, openLinks, contextCopy,
};

(async () => {
  const args = process.argv.slice(2);
  if (args.includes('--list')) { console.log(Object.keys(PASSES).join('\n')); return; }
  const picked = args.flatMap((a) => a.startsWith('--pass=') ? a.slice(7).split(',') : a.startsWith('--pass') ? [] : a.startsWith('-') ? [] : a.split(','));
  const names = picked.length ? picked : Object.keys(PASSES);
  for (const n of names) if (!PASSES[n]) throw new Error(`unknown pass ${n} (node shots.js --list)`);
  const browser = await pw.chromium.launch();
  const t0 = Date.now();
  try {
    for (const n of names) {
      const t = Date.now();
      log(`== ${n}`);
      await PASSES[n](browser);
      log(`== ${n} done in ${((Date.now() - t) / 1000).toFixed(1)}s`);
    }
  } finally {
    await browser.close();
  }
  log(`all ${names.length} pass(es) in ${((Date.now() - t0) / 1000).toFixed(1)}s`);
})().catch((e) => { console.error('shots failed:', e); process.exit(1); });
