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
  } finally {
    await browser.close();
  }
})().catch((e) => { console.error('shots failed:', e); process.exit(1); });
