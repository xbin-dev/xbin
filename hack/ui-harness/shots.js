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
  await page.evaluate(() => { document.querySelector('bx-shell')._adminFor = null; });

  await crawler.locator('button.term').click();
  await sleep(4000); // spawn + session frame
  await shot(page, 'term-admin-crawler', { fullPage: false });
  await dumpSelects(page, 'term-admin-crawler-selects', 'bx-frame select.scope');
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

(async () => {
  const browser = await pw.chromium.launch();
  try {
    await admin(browser);
    await orgAdmin(browser, 'dev1', 'devpass123', ['apps/crawler', 'apps/dev1-notes']);
    await orgAdmin(browser, 'sales1', 'salespass123', ['apps/leads']);
  } finally {
    await browser.close();
  }
})().catch((e) => { console.error('shots failed:', e); process.exit(1); });
