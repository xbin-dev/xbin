// Playwright screenshots of the D54 surfaces. Env: URL, OUT.
// Playwright: a global/NODE_PATH install, else $PLAYWRIGHT_DIR/node_modules.
const pw = (() => {
  try { return require('playwright'); } catch { /* fall through */ }
  const dir = process.env.PLAYWRIGHT_DIR;
  if (!dir) throw new Error('playwright not found: npm i -g playwright (+ npx playwright install chromium) or set PLAYWRIGHT_DIR');
  return require(require('path').join(dir, 'node_modules', 'playwright'));
})();
const fs = require('fs');
const URL = process.env.URL || 'http://127.0.0.1:8697';
const OUT = process.env.OUT || __dirname + '/out';
fs.mkdirSync(OUT, { recursive: true });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const log = (...a) => console.log('[shots]', ...a);

async function login(browser, user, pass) {
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 }, deviceScaleFactor: 1 });
  const page = await ctx.newPage();
  page.on('pageerror', (e) => log(`${user}: PAGE ERROR`, e.message));
  page.on('console', (m) => { if (m.type() === 'error') log(`${user}: console.error`, m.text().slice(0, 200)); });
  await page.goto(`${URL}/login`);
  await page.fill('input[name=username]', user);
  await page.fill('input[name=password]', pass);
  await Promise.all([page.waitForNavigation(), page.click('button')]);
  return { ctx, page };
}

async function gotoTab(page, hash, waitText) {
  await page.goto(`${URL}/c/tiles/admin/#${hash}`);
  await page.reload();
  await page.waitForSelector(`text=${waitText}`, { timeout: 15000 });
  await sleep(600);
}

async function shot(page, name, opts = {}) {
  await page.screenshot({ path: `${OUT}/${name}.png`, fullPage: true, ...opts });
  log('wrote', name + '.png');
}

// Text dump of every <select> under a locator (native dropdowns don't render
// in screenshots) → out/<name>.txt
async function dumpSelects(page, name, selector = 'select') {
  const rows = await page.locator(selector).evaluateAll((sels) => sels.map((s) => ({
    title: s.title, value: s.value,
    options: [...s.options].map((o) => `${o.selected ? '*' : ' '} [${o.value}] ${o.textContent.trim()}${o.title ? '   // ' + o.title.replace(/\n/g, ' ⏎ ') : ''}`),
  })));
  const txt = rows.map((r, i) => `select#${i} value=${JSON.stringify(r.value)}${r.title ? ' title=' + JSON.stringify(r.title) : ''}\n  ${r.options.join('\n  ')}`).join('\n\n');
  fs.writeFileSync(`${OUT}/${name}.txt`, txt + '\n');
  log('wrote', name + '.txt');
}

async function admin(browser) {
  const { ctx, page } = await login(browser, 'admin', 'admin');

  // ---- admin tile: network sets tab ----
  await page.goto(`${URL}/c/tiles/admin/#netsets`);
  await page.waitForSelector('text=Rule grammar', { timeout: 15000 });
  await sleep(500);
  await shot(page, 'admin-netsets');

  // open devs-net's editor, break the LAN row, add a host row
  // the set card = the div whose header row directly holds the ⛭ name
  const card = page.locator('div:has(> div > b.mono:text-is("⛭ devs-net"))').first();
  await card.getByRole('button', { name: 'edit' }).click();
  await sleep(300);
  await shot(page, 'admin-netsets-edit');
  const lanInput = card.locator('.orow input').nth(0); // first row with a value input (lan or internet-to)
  const inputs = card.locator('.orow input');
  const n = await inputs.count();
  for (let i = 0; i < n; i++) {
    const v = await inputs.nth(i).inputValue();
    if (v.startsWith('10.42')) { await inputs.nth(i).fill('10.42.0.0/33'); break; }
  }
  await card.getByRole('button', { name: '+ rule' }).click();
  await sleep(200);
  const lastSel = card.locator('.orow select').last();
  await lastSel.selectOption('host');
  await sleep(300);
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
  await sleep(300);
  await shot(page, 'admin-wiring-custom');

  // ---- components tab (runtime detail of the node backends) ----
  // spawn the two node backends first (a request through the proxy does it)
  for (const t of ['apps/crawler', 'apps/racks']) await ctx.request.get(`${URL}/api/${t}/`);
  await sleep(2500);
  await gotoTab(page, 'components', 'apps/crawler');
  await sleep(1500);
  for (const t of ['apps/crawler', 'apps/racks']) {
    await page.locator('tr', { has: page.locator('a, span', { hasText: t }).first() }).first().locator('span.caret').click();
  }
  await sleep(1500);
  await shot(page, 'admin-components');

  // ---- shell: tile popover + terminal on an org tile ----
  await page.goto(`${URL}/`);
  await page.waitForSelector('bx-shell', { timeout: 15000 });
  await sleep(1500);
  await page.evaluate(() => {
    const sh = document.querySelector('bx-shell');
    for (const p of ['apps/crawler']) if (!sh._isOpen(p)) sh._toggle(p);
  });
  await sleep(1000);
  const crawler = page.locator('.card[data-path="apps/crawler"]');
  await crawler.locator('button[title^="tile admin"]').click();
  await sleep(500);
  await page.evaluate(() => {
    const ta = document.querySelector('bx-shell').shadowRoot.querySelector('bx-tile-admin');
    ta.shadowRoot.querySelectorAll('details').forEach((d) => { d.open = /runtime|interfaces/.test(d.querySelector('summary')?.textContent ?? ''); });
  });
  await sleep(1500);
  await shot(page, 'shell-popover-crawler', { fullPage: false });
  await dumpSelects(page, 'shell-popover-selects', 'bx-tile-admin select');
  await page.keyboard.press('Escape');
  await page.evaluate(() => { document.querySelector('bx-shell')._adminPop = null; });

  await crawler.locator('button.term').click();
  await sleep(4000); // spawn + session frame
  await shot(page, 'term-admin-crawler', { fullPage: false });
  await dumpSelects(page, 'term-admin-crawler-selects', 'bx-frame select.scope');
  await ctx.close();
}

// Org screens (D55): view bar → edit layout → draft → a competing save →
// conflict dialog → reload theirs; hide an org tab and reopen it from the
// sidebar; the share menu's replace target. dev1 is a devs org admin.
async function screens(browser) {
  const { ctx, page } = await login(browser, 'dev1', 'devpass123');
  const admin = await login(browser, 'admin', 'admin'); // the competing saver
  await page.goto(`${URL}/`);
  await page.waitForSelector('bx-shell', { timeout: 15000 });
  await sleep(1500);
  const sh = () => document.querySelector('bx-shell');
  const orgId = await page.evaluate(() => document.querySelector('bx-shell')._orgScreens.find((s) => s.org === 'devs')?.id);
  if (!orgId) { log('no devs org screen seeded'); await ctx.close(); await admin.ctx.close(); return; }
  // a dirty draft from an earlier pass survives reloads by design — drop it so this pass starts in view mode
  await page.evaluate((id) => { const s = document.querySelector('bx-shell'); if (s._orgDrafts?.[id]) s._dropDraft(id); s._openOrgScreen(id); }, orgId);
  await sleep(800);
  await shot(page, 'orgscreen-view', { fullPage: false });
  // edit layout → add a tile from the sidebar → dirty draft
  await page.locator('bx-shell .orgbar button', { hasText: 'edit layout' }).click();
  await sleep(300);
  await page.evaluate(() => document.querySelector('bx-shell')._toggle('apps/offline'));
  await sleep(600);
  await shot(page, 'orgscreen-edit', { fullPage: false });
  // someone else saves first (admin, against the current rev) → our save conflicts
  const cur = await page.evaluate((id) => document.querySelector('bx-shell')._orgScreens.find((s) => s.id === id).rev, orgId);
  const r = await admin.ctx.request.put(`${URL}/api/xbin/screens/org`, { data: { id: orgId, org: 'devs',
    tiles: [{ path: 'apps/pinned', x: 0, y: 0, w: 576, h: 384 }], rev: cur } });
  log('competing save:', r.status(), (await r.text()).slice(0, 120));
  await sleep(1200); // the users event refreshes _orgScreens → "newer version" note
  await shot(page, 'orgscreen-edit-newer', { fullPage: false });
  await page.locator('bx-shell .orgbar button', { hasText: 'Save and update' }).click();
  await sleep(800);
  await shot(page, 'orgscreen-conflict', { fullPage: false });
  await page.locator('bx-dialog button', { hasText: 'Reload theirs' }).click();
  await sleep(800);
  await shot(page, 'orgscreen-after-reload', { fullPage: false });
  // edit again and save cleanly
  await page.locator('bx-shell .orgbar button', { hasText: 'edit layout' }).click();
  await page.evaluate(() => document.querySelector('bx-shell')._toggle('apps/crawler'));
  await sleep(300);
  await page.locator('bx-shell .orgbar button', { hasText: 'Save and update' }).click();
  await sleep(1200);
  await shot(page, 'orgscreen-saved', { fullPage: false });
  // sidebar trees: shared folders (devs: Crawling; ws: Docs) + flat roots
  await shot(page, 'sidebar-trees', { fullPage: false, clip: { x: 0, y: 70, width: 230, height: 620 } });
  // curate devs' shared folders: ✎ → draft → new folder → file a tile → save
  await page.locator('bx-shell .group.owner', { hasText: 'devs' }).hover();
  await page.locator('bx-shell .group.owner', { hasText: 'devs' }).locator('button.pen').click();
  await sleep(300);
  await page.evaluate(() => {
    const s = document.querySelector('bx-shell');
    const ctx = s._folderCtx('org:devs');
    if (!ctx.folders.some((f) => f.id === 'f2')) ctx.mutate((fs) => [...fs, { id: 'f2', name: 'Pinned', icon: '📌', items: [] }]);
    s._fileInto('f2', 'apps/pinned', s._folderCtx('org:devs'));
  });
  await sleep(400);
  await shot(page, 'sidebar-folders-edit', { fullPage: false, clip: { x: 0, y: 70, width: 230, height: 620 } });
  await page.locator('bx-shell .secbar button', { hasText: 'Save for everyone' }).click();
  await sleep(1000);
  await shot(page, 'sidebar-folders-saved', { fullPage: false, clip: { x: 0, y: 70, width: 230, height: 620 } });
  // hide the org tab → reopen from the sidebar entry
  await page.evaluate((id) => document.querySelector('bx-shell')._hideOrgTab(id), orgId);
  await sleep(500);
  await shot(page, 'orgtab-hidden', { fullPage: false });
  await page.locator('bx-shell .item.screen.org').first().click();
  await sleep(500);
  // share menu (replace target) as dev1 on a personal screen
  await page.evaluate(() => { const s = document.querySelector('bx-shell'); s._active = s._screens[0].id; s._settingsOpen = true; });
  await sleep(500);
  await shot(page, 'share-menu', { fullPage: false });
  await dumpSelects(page, 'share-menu-selects', 'bx-shell .wsmenu select');
  await ctx.close();
  await admin.ctx.close();
}

// Context menus (D56): the canvas menu with its open-tile / create submenus,
// the tile menu from a card head and from a sidebar row, a grid square
// opening a panel, and the admin window at "interfaces" with an open
// multiselect list that must escape the window's edge.
async function menus(browser) {
  const { ctx, page } = await login(browser, 'admin', 'admin');
  await page.goto(`${URL}/`);
  await page.waitForSelector('bx-shell', { timeout: 15000 });
  await sleep(1500);
  await page.evaluate(() => {
    const s = document.querySelector('bx-shell');
    const p = s._screens.find((x) => !x.parked); if (p) { s._active = p.id; s._save(); }
    // keep some tiles closed (recents list) and free the bottom of the canvas for the right-click
    for (const t of ['apps/offline', 'apps/leads', 'tiles/apidocs', 'tiles/admin']) if (s._isOpen(t)) s._toggle(t);
    if (!s._isOpen('apps/crawler')) s._toggle('apps/crawler');
    // warm the recents list: open + close a few tiles
    for (const t of ['apps/leads', 'apps/pinned', 'apps/racks']) { s._toggle(t); s._toggle(t); }
  });
  await sleep(800);
  await page.mouse.click(1300, 860, { button: 'right' }); // empty canvas (below the cards)
  await sleep(400);
  await shot(page, 'menu-canvas', { fullPage: false });
  await page.locator('bx-menu .it', { hasText: 'Open tile' }).hover();
  await sleep(450);
  await shot(page, 'menu-canvas-open-tile', { fullPage: false });
  await page.locator('bx-menu .panel.sub .q').fill('le');
  await sleep(300);
  await shot(page, 'menu-canvas-open-tile-filter', { fullPage: false });
  await page.locator('bx-menu .it', { hasText: 'Create a new tile' }).hover();
  await sleep(450);
  await shot(page, 'menu-canvas-create', { fullPage: false });
  await page.keyboard.press('Escape');
  await sleep(300);
  await page.locator('.card[data-path="apps/crawler"] .head').click({ button: 'right' });
  await sleep(400);
  await shot(page, 'menu-tile-card', { fullPage: false });
  await page.keyboard.press('Escape');
  await sleep(300);
  // right-click INSIDE the tile's iframe: the tile page relays it to the shell
  const cb = await page.locator('.card[data-path="apps/crawler"] .cbody').boundingBox();
  await page.mouse.click(cb.x + 120, cb.y + 90, { button: 'right' });
  await sleep(600);
  await shot(page, 'menu-tile-body', { fullPage: false });
  await page.keyboard.press('Escape');
  await sleep(300);
  await page.locator('bx-shell .item[data-path="apps/offline"]').click({ button: 'right' });
  await sleep(400);
  await shot(page, 'menu-tile-sidebar', { fullPage: false });
  await page.locator('bx-menu .cell', { hasText: 'logs' }).click();
  await sleep(2500);
  await shot(page, 'menu-tile-logs-opened', { fullPage: false });
  await page.evaluate(() => { const s = document.querySelector('bx-shell'); s._frameOf('apps/offline')?.toggleTerminal(); });
  await page.evaluate(() => document.querySelector('bx-shell')._openAdminWin('apps/consumer', 'interfaces'));
  await sleep(1500);
  await shot(page, 'admin-win-interfaces', { fullPage: false });
  const ms = page.locator('bx-tile-admin bx-multiselect .control').first();
  if (await ms.count()) {
    await ms.click();
    await sleep(500);
    await shot(page, 'admin-win-multiselect-open', { fullPage: false });
    const r = await page.evaluate(() => {
      const ta = document.querySelector('bx-shell').shadowRoot.querySelector('bx-tile-admin');
      const m = ta?.shadowRoot.querySelector('bx-multiselect')?.shadowRoot.querySelector('.menu')?.getBoundingClientRect();
      const w = document.querySelector('bx-shell').shadowRoot.querySelector('.admin-pop')?.getBoundingClientRect();
      return { menu: m && [m.left, m.top, m.right, m.bottom].map(Math.round), win: w && [w.left, w.top, w.right, w.bottom].map(Math.round), vw: innerWidth, vh: innerHeight };
    });
    log('multiselect list rect', JSON.stringify(r));
  } else log('no multiselect in apps/consumer admin window');
  // click outside closes the admin popover
  await page.mouse.click(1300, 860);
  await sleep(300);
  await page.mouse.click(1300, 860);
  await sleep(300);
  log('admin popover after outside click:', await page.evaluate(() => !!document.querySelector('bx-shell')._adminPop));
  await ctx.close();
}

// Phone viewport (D56): the trimmed card head with ⋯, the tile menu and
// the canvas menu as bottom sheets, a sidebar row's ⋯ in the drawer, and the
// admin window as a full-screen sheet.
async function mobile(browser) {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 1 });
  const page = await ctx.newPage();
  page.on('pageerror', (e) => log('mobile: PAGE ERROR', e.message));
  await page.goto(`${URL}/login`);
  await page.fill('input[name=username]', 'admin');
  await page.fill('input[name=password]', 'admin');
  await Promise.all([page.waitForNavigation(), page.click('button')]);
  await page.goto(`${URL}/`);
  await page.waitForSelector('bx-shell', { timeout: 15000 });
  await sleep(1500);
  await page.evaluate(() => {
    const s = document.querySelector('bx-shell');
    const p = s._screens.find((x) => !x.parked); if (p) { s._active = p.id; s._save(); }
    for (const t of ['tiles/manager', 'apps/welcome', 'tiles/apidocs', 'tiles/admin']) if (s._isOpen(t)) s._toggle(t);
    if (!s._isOpen('apps/crawler')) s._toggle('apps/crawler');
  });
  await sleep(800);
  await shot(page, 'm-card-more', { fullPage: false });
  await page.locator('.card[data-path="apps/crawler"] .head button[title^="tile menu"]').tap();
  await sleep(500);
  await shot(page, 'm-sheet-tile', { fullPage: false });
  await page.locator('bx-menu .shead button[title=close]').tap();
  await sleep(300);
  await page.evaluate(() => document.querySelector('bx-shell')._openCanvasMenu({ clientX: 200, clientY: 600, preventDefault() {} }));
  await sleep(500);
  await shot(page, 'm-sheet-canvas', { fullPage: false });
  await page.locator('bx-menu .it', { hasText: 'Open tile' }).tap();
  await sleep(400);
  await shot(page, 'm-sheet-canvas-open-tile', { fullPage: false });
  await page.locator('bx-menu .shead button[title=close]').tap();
  await sleep(300);
  await page.locator('bx-shell .ham').tap();
  await sleep(500);
  await page.locator('bx-shell .item[data-path="apps/offline"] .more').tap();
  await sleep(500);
  await shot(page, 'm-sheet-sidebar', { fullPage: false });
  await page.locator('bx-menu .shead button[title=close]').tap();
  await sleep(300);
  await page.evaluate(() => { document.querySelector('bx-shell')._drawer = false; });
  await page.evaluate(() => document.querySelector('bx-shell')._openAdminWin('apps/crawler', 'interfaces'));
  await sleep(1200);
  await shot(page, 'm-admin-sheet', { fullPage: false });
  await ctx.close();
}

async function orgAdmin(browser, user, pass, tiles) {
  const { ctx, page } = await login(browser, user, pass);
  for (const t of tiles) {
    const r = await ctx.request.get(`${URL}/api/xbin/term-net?tile=${encodeURIComponent(t)}`);
    fs.appendFileSync(`${OUT}/term-net-${user}.json`, `${t}: ${await r.text()}\n`);
  }
  await page.goto(`${URL}/c/tiles/organisations/`);
  await page.waitForSelector('text=my organisations', { timeout: 15000 });
  await sleep(1200);
  await shot(page, `orgs-tile-${user}`);
  await dumpSelects(page, `orgs-tile-${user}-selects`, 'bx-organisations select');

  await page.goto(`${URL}/`);
  await page.waitForSelector('bx-shell', { timeout: 15000 });
  await sleep(1500);
  // stay on a personal screen: a sidebar click on an org screen would open a draft
  await page.evaluate(() => { const s = document.querySelector('bx-shell'); const p = s._screens.find((x) => !x.parked); if (p) { s._active = p.id; s._save(); } });
  await sleep(300);
  for (const t of tiles) {
    await page.evaluate((p) => { const sh = document.querySelector('bx-shell'); if (!sh._isOpen(p)) sh._toggle(p); }, t);
    await sleep(800);
    const card = page.locator(`.card[data-path="${t}"]`);
    if (!(await card.count())) { log(user, 'no card for', t); continue; }
    await card.locator('button.term').click();
    await sleep(4000);
    const slug = t.replace(/\W+/g, '-');
    await shot(page, `term-${user}-${slug}`, { fullPage: false });
    await dumpSelects(page, `term-${user}-${slug}-selects`, 'bx-frame select.scope');
    // close the pop-up so the next tile's terminal is the one on screen
    await page.evaluate((p) => {
      const sh = document.querySelector('bx-shell');
      const fr = sh.shadowRoot.querySelector(`bx-frame[src="${p}"]`);
      if (fr) fr._termOpen = false;
    }, t);
    await sleep(300);
  }
  await ctx.close();
}

// Floating windows must always be reachable (the field report: a terminal
// pop-up restored at x 2270 / y 1217 on a smaller viewport — working, and
// invisible). Asserts: a persisted off-screen pop-up restores inside the
// viewport, a shrinking browser window pulls an open pop-up back in, and the
// canvas menu's "Bring windows on-screen" fixes a parked spawned window and
// float tile. Failures throw at the end of the pass.
async function windows(browser) {
  const fails = [];
  const check = (cond, msg) => { fs.appendFileSync(`${OUT}/windows.txt`, `${cond ? 'PASS' : 'FAIL'} ${msg}\n`); if (!cond) fails.push(msg); };
  fs.writeFileSync(`${OUT}/windows.txt`, '');
  const { ctx, page } = await login(browser, 'admin', 'admin');
  const inside = (r) => !!r && r.width > 0 && r.left >= 0 && r.top >= 0 && r.right <= r.W + 0.5 && r.bottom <= r.H + 0.5;
  const fmt = (r) => r ? `${Math.round(r.left)},${Math.round(r.top)} ${Math.round(r.width)}×${Math.round(r.height)} in ${r.W}×${r.H}` : 'none';
  const rectOf = (sel) => page.evaluate((s) => {
    const sh = document.querySelector('bx-shell');
    const el = s.startsWith('pop:')
      ? sh.shadowRoot.querySelector(`bx-frame[src="${s.slice(4)}"]`)?.shadowRoot?.querySelector('.pop')
      : sh.shadowRoot.querySelector(s);
    const r = el?.getBoundingClientRect();
    return r ? { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height, W: innerWidth, H: innerHeight } : null;
  }, sel);

  // 1. a persisted off-screen pop-up restores inside the viewport
  await page.goto(`${URL}/`);
  await page.waitForSelector('bx-shell', { timeout: 15000 });
  await page.evaluate(() => localStorage.setItem('bx-term:apps/crawler', JSON.stringify({
    open: true, active: 0, pop: { x: 2270, y: 1217, w: 1003, h: 868 },
    sessions: [{ key: 'k1', id: null, net: null, gpu: 'none', api: true, name: '' }] })));
  await page.reload();
  await page.waitForSelector('bx-shell', { timeout: 15000 });
  await sleep(1500);
  await page.evaluate(() => {
    const sh = document.querySelector('bx-shell');
    const p = sh._screens.find((x) => !x.parked); if (p) { sh._active = p.id; sh._save(); }
    if (!sh._isOpen('apps/crawler')) sh._toggle('apps/crawler');
  });
  await sleep(3000);
  let r = await rectOf('pop:apps/crawler');
  check(inside(r), `restored pop-up lands inside the viewport (${fmt(r)})`);
  await shot(page, 'windows-restored', { fullPage: false });

  // 2. a shrinking browser window pulls an open pop-up back in
  await page.evaluate(() => {
    const fr = document.querySelector('bx-shell').shadowRoot.querySelector('bx-frame[src="apps/crawler"]');
    fr._pop = { x: 820, y: 560, w: 560, h: 320 };
  });
  await sleep(300);
  await page.setViewportSize({ width: 1000, height: 700 });
  await sleep(800);
  r = await rectOf('pop:apps/crawler');
  check(inside(r), `pop-up follows a shrinking browser window (${fmt(r)})`);
  await shot(page, 'windows-shrunk', { fullPage: false });
  await page.setViewportSize({ width: 1400, height: 900 });
  await sleep(500);

  // 3. "Bring windows on-screen": a spawned window and a float tile parked off-screen
  const hasItem = await page.evaluate(() => document.querySelector('bx-shell')._canvasMenuItems().some((i) => /on-screen/.test(i.label ?? '')));
  check(hasItem, 'canvas menu offers "Bring windows on-screen"');
  await page.evaluate(() => {
    const sh = document.querySelector('bx-shell');
    sh._spawnWins = [{ id: 'hw', from: 'apps/crawler', src: 'apps/crawler', reply() {}, title: 'parked', x: 5000, y: 4000, w: 400, h: 300, z: 3000 }];
    if (!sh._isOpen('apps/offline')) sh._toggle('apps/offline');
  });
  await sleep(800);
  await page.evaluate(() => {
    const sh = document.querySelector('bx-shell');
    sh._mutateTiles((tiles) => tiles.map((o) => o.path === 'apps/offline' ? { ...o, float: { x: 5000, y: 4000, w: 400, h: 300, z: 100 } } : o));
  });
  await sleep(800);
  r = await rectOf('.float[data-path="apps/offline"]');
  check(inside(r), `a float saved off-screen renders inside the viewport (${fmt(r)})`);
  await page.evaluate(() => document.querySelector('bx-shell')._fitWindows(true));
  await sleep(800);
  r = await rectOf('.spawn');
  check(inside(r), `spawned window brought on-screen (${fmt(r)})`);
  const saved = await page.evaluate(() => document.querySelector('bx-shell')._tiles.find((o) => o.path === 'apps/offline')?.float);
  check(saved && saved.x + saved.w <= 1400 && saved.y + saved.h <= 900, `float geometry persisted on-screen (${JSON.stringify(saved)})`);
  await shot(page, 'windows-fitted', { fullPage: false });

  // tidy: the next pass starts from the seeded layout
  await page.evaluate(() => {
    const sh = document.querySelector('bx-shell');
    sh._spawnWins = [];
    if (sh._isOpen('apps/offline')) sh._toggle('apps/offline');
    localStorage.removeItem('bx-term:apps/crawler');
  });
  await sleep(500);
  await ctx.close();
  if (fails.length) throw new Error(`windows: ${fails.length} check(s) failed:\n  ${fails.join('\n  ')}`);
}

// A tile reload must not touch focus or z-order. apps/focusy focuses its
// input on every load; with the crawler float (terminal pop-up open, focus in
// the terminal) on top of it, a focusy reload must leave the floats' order
// alone and hand the stolen focus back to the terminal. A negative control
// first proves the tile really does grab focus in this browser.
async function reloadFocus(browser) {
  const fails = [];
  const check = (cond, msg) => { fs.appendFileSync(`${OUT}/reload-focus.txt`, `${cond ? 'PASS' : 'FAIL'} ${msg}\n`); if (!cond) fails.push(msg); };
  fs.writeFileSync(`${OUT}/reload-focus.txt`, '');
  const { ctx, page } = await login(browser, 'admin', 'admin');
  await page.goto(`${URL}/`);
  await page.waitForSelector('bx-shell', { timeout: 15000 });
  await sleep(1500);
  await page.evaluate(() => {
    const sh = document.querySelector('bx-shell');
    const p = sh._screens.find((x) => !x.parked); if (p) { sh._active = p.id; sh._save(); }
    for (const t of ['apps/crawler', 'apps/focusy']) if (!sh._isOpen(t)) sh._toggle(t);
  });
  await sleep(1000);
  await page.evaluate(() => {
    const sh = document.querySelector('bx-shell');
    sh._mutateTiles((tiles) => tiles.map((o) => {
      if (o.path === 'apps/focusy') return { ...o, float: { x: 300, y: 120, w: 520, h: 380, z: 100 } };
      if (o.path === 'apps/crawler') return { ...o, float: { x: 80, y: 80, w: 520, h: 360, z: 200 } };
      return o;
    }));
  });
  await sleep(2500);
  const state = () => page.evaluate(() => {
    const sh = document.querySelector('bx-shell');
    let el = document.activeElement;
    while (el?.shadowRoot?.activeElement) el = el.shadowRoot.activeElement;
    const z = (p) => sh._tiles.find((o) => o.path === p)?.float?.z;
    const host = el?.getRootNode?.()?.host;
    return { active: el?.tagName, activeSrc: host?.tagName === 'BX-FRAME' ? host.src : (host?.tagName ?? ''), zCrawler: z('apps/crawler'), zFocusy: z('apps/focusy') };
  });
  const frame = (p) => `document.querySelector('bx-shell').shadowRoot.querySelector('bx-frame[src="${p}"]')`;
  const focusTerm = (p) => page.evaluate((f) => eval(f).shadowRoot.querySelector('bx-terminal')?.shadowRoot?.querySelector('textarea')?.focus(), frame(p));
  // open the crawler terminal and put the caret in it
  await page.evaluate((f) => eval(f).open('term'), frame('apps/crawler'));
  await sleep(3500);
  await focusTerm('apps/crawler');
  await sleep(300);
  let s = await state();
  check(s.active === 'TEXTAREA' && s.zCrawler > s.zFocusy, `setup: caret in the crawler terminal, crawler float on top (${JSON.stringify(s)})`);

  // Control: focusing the tile's iframe fronts its float — the exact chain a
  // reloaded document triggers when it grabs focus (iframe focus → window
  // blur → the shell fronts that float). Parent-side iframe.focus() is the
  // deterministic stand-in: a sandboxed tile can't steal focus in a headless
  // browser without a user gesture, but the shell's blur path is identical.
  await page.evaluate((f) => { eval(f)._iframe.focus(); document.querySelector('bx-shell')._raiseFocusedFloat(); }, frame('apps/focusy'));
  await sleep(150);
  s = await state();
  check(s.zFocusy > s.zCrawler, `control: focusing a tile's iframe fronts its float (${JSON.stringify(s)})`);

  // reset: crawler back on top, caret back in its terminal
  await page.evaluate(() => { const sh = document.querySelector('bx-shell'); sh._setFloat('apps/crawler', { z: 200 }); sh._setFloat('apps/focusy', { z: 100 }); });
  await focusTerm('apps/crawler');
  await sleep(200);

  // Fix: the same focus-into-iframe DURING a reload must not front the float,
  // and the focus the reload stole goes back to the terminal.
  await page.evaluate((f) => {
    const fr = eval(f);
    fr._beginReload();  // reloading = true; captures the terminal as the prior focus
    fr._iframe.focus(); // the reloaded document grabs focus
    document.querySelector('bx-shell')._raiseFocusedFloat();
  }, frame('apps/focusy'));
  await sleep(150);
  s = await state();
  check(s.zCrawler > s.zFocusy, `reload leaves the z-order alone (${JSON.stringify(s)})`);
  // …but a real click into the reloading tile (pointer over it) still fronts it
  await page.evaluate((f) => { const fr = eval(f); fr._hover = true; fr._iframe.focus(); document.querySelector('bx-shell')._raiseFocusedFloat(); }, frame('apps/focusy'));
  await sleep(150);
  s = await state();
  check(s.zFocusy > s.zCrawler, `a real click into a reloading tile still fronts it (${JSON.stringify(s)})`);
  // pointer away again, crawler back on top; finishing the reload hands focus back
  await page.evaluate((f) => { eval(f)._hover = false; const sh = document.querySelector('bx-shell'); sh._setFloat('apps/crawler', { z: 200 }); sh._setFloat('apps/focusy', { z: 100 }); }, frame('apps/focusy'));
  await sleep(200);
  await page.evaluate((f) => eval(f)._onFrameLoad(), frame('apps/focusy'));
  await sleep(500);
  s = await state();
  check(s.active === 'TEXTAREA', `reload hands focus back to the terminal (${JSON.stringify(s)})`);
  await shot(page, 'reload-focus', { fullPage: false });
  // tidy
  await page.evaluate(() => {
    const sh = document.querySelector('bx-shell');
    for (const t of ['apps/crawler', 'apps/focusy']) if (sh._isOpen(t)) sh._toggle(t);
    localStorage.removeItem('bx-term:apps/crawler');
  });
  await sleep(500);
  await ctx.close();
  if (fails.length) throw new Error(`reload-focus: ${fails.length} check(s) failed:\n  ${fails.join('\n  ')}`);
}

// Net pickers must never show a refused bind as a success (the "org admin
// could still grant host" report). Asserts, not just screenshots: refused
// options are disabled, a refused custom ref snaps the select back and the
// reason lands inside the interfaces section, a tile the person may not wire
// is read-only, the root prompt lists only approvable slots, and the ⚙
// popover sizes to its content. Failures throw at the end of the pass.
async function netPickers(browser) {
  const fails = [];
  const check = (cond, msg) => { fs.appendFileSync(`${OUT}/net-pickers.txt`, `${cond ? 'PASS' : 'FAIL'} ${msg}\n`); if (!cond) fails.push(msg); };
  fs.writeFileSync(`${OUT}/net-pickers.txt`, '');
  const openPop = async (page, tile) => {
    await page.evaluate((p) => { document.querySelector('bx-shell')._openAdminWin(p, 'interfaces'); }, tile);
    await sleep(1500);
    return page.evaluate(() => {
      const sh = document.querySelector('bx-shell');
      const pop = sh.shadowRoot.querySelector('.admin-pop');
      const ta = pop?.querySelector('bx-tile-admin');
      const sec = ta?.shadowRoot.querySelector('details[data-sec="interfaces"]');
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
  };
  const closePop = (page) => page.evaluate(() => { document.querySelector('bx-shell')._adminPop = null; });
  const openShell = async (page) => {
    await page.goto(`${URL}/`);
    await page.waitForSelector('bx-shell', { timeout: 15000 });
    await sleep(1500);
  };

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
    await sel.selectOption('org');
    await sleep(1500);
    let v = await sel.inputValue();
    check(v === 'org', `admin: select shows the new value right after a successful bind (now "${v}")`);
    check((await boundNow()) === 'org', 'admin: server bound org');
    await sel.selectOption(before);
    await sleep(1500);
    v = await sel.inputValue();
    check(v === before, `admin: switching back shows "${before}" (now "${v}")`);
    check((await boundNow()) === before, `admin: server bound ${before} again`);
    // a refused custom ref: outside the set → 400 → select snaps back, reason in-section
    await sel.selectOption('__custom');
    await sleep(300);
    const form = page.locator('bx-shell bx-tile-admin details[data-sec="interfaces"] form');
    await form.locator('input[name="ref"]').fill('lan:10.0.0.0/8');
    await form.locator('button[type="submit"]').click();
    await sleep(1500);
    await shot(page, 'net-picker-admin-refused', { fullPage: false });
    s = await page.evaluate(() => {
      const ta = document.querySelector('bx-shell').shadowRoot.querySelector('.admin-pop bx-tile-admin');
      const sec = ta.shadowRoot.querySelector('details[data-sec="interfaces"]');
      return { value: sec.querySelector('select')?.value, err: sec.querySelector('.err')?.textContent?.trim() ?? '',
        headerErr: !!ta.shadowRoot.querySelector(':host > .err, .hd + .err') };
    });
    check(/not covered/.test(s.err), `admin: refusal shown inside the section ("${s.err.slice(0, 60)}")`);
    check(s.value === before, `admin: select snapped back to "${before}" (now "${s.value}")`);
    await closePop(page);
    await ctx.close();
  }

  // ---- org admin dev1: own personal tile is read-only; org tile offers the picker minus host ----
  {
    const { ctx, page } = await login(browser, 'dev1', 'devpass123');
    await openShell(page);
    await page.evaluate(() => { const s = document.querySelector('bx-shell'); const p = s._screens.find((x) => !x.parked); if (p) { s._active = p.id; s._save(); } });
    let s = await openPop(page, 'apps/dev1-notes');
    await shot(page, 'net-picker-dev1-personal', { fullPage: false });
    check(s.readonly && !s.hasSelect, `dev1: personal tile wiring is read-only (readonly=${s.readonly} select=${s.hasSelect})`);
    await closePop(page);
    s = await openPop(page, 'apps/pinned');
    check(s.hasSelect && s.disabled.includes('host'), `dev1: org tile has a picker with host disabled (select=${s.hasSelect} disabled=${s.disabled.join(',')})`);
    await closePop(page);
    // the root bind prompt: only slots dev1 may wire (never the personal tile's)
    const prompt = await page.evaluate(() => document.querySelector('bx-shell').shadowRoot.querySelector('bx-bindings')?.shadowRoot?.textContent ?? '');
    check(!prompt.includes('apps/dev1-notes'), 'dev1: root bind prompt does not offer the personal tile');
    // and the server view says the same
    const r = await ctx.request.get(`${URL}/api/xbin/bindings`);
    const d = await r.json();
    check(d.approvable?.['apps/pinned'] === true && !d.approvable?.['apps/dev1-notes'], `dev1: approvable = ${JSON.stringify(d.approvable)}`);
    const pin = (d.pending ?? []).find((p) => p.component === 'apps/dev1-notes');
    check(!pin || pin.approvable === false, 'dev1: pending row for the personal tile is not approvable');
    await ctx.close();
  }
  if (fails.length) throw new Error(`net-pickers: ${fails.length} check(s) failed:\n  ${fails.join('\n  ')}`);
}

(async () => {
  const browser = await pw.chromium.launch();
  try {
    await admin(browser);
    await menus(browser);
    await mobile(browser);
    await screens(browser);
    await orgAdmin(browser, 'dev1', 'devpass123', ['apps/crawler', 'apps/dev1-notes']);
    await orgAdmin(browser, 'sales1', 'salespass123', ['apps/leads']);
    await netPickers(browser);
    await windows(browser);
    await reloadFocus(browser);
  } finally {
    await browser.close();
  }
})().catch((e) => { console.error('shots failed:', e); process.exit(1); });
