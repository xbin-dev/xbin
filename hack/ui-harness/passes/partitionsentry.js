// hack/ui-harness/passes/partitionsentry.js — covers 90 §I13 and §I3 (SH):
// the shell's settings menu links the partitions page for a person who sees
// a partitioned tile, and the partition chip's `yours` on a tile with a
// global instance says where what the tile shares comes from:
//   1. pentry (a new person: no orgs, one tile of their own, apps/pentry,
//      unpartitioned) — nothing partitioned in their /components: the
//      settings menu's my-account block has no "your partitions";
//   2. apps/pentry's code asks for user + global (it holds nothing: recorded
//      at once) — with the menu open, the entry appears live, after
//      devices…: the marker's shape in its teal, "your partitions", a link
//      to /xbin/partitions in a new tab (target _blank, rel noopener) with
//      its tooltip; the tile's window says `yours`, its tooltip naming the
//      global instance as where what the tile shares comes from;
//   3. the entry opens the page: a new top-level tab on /xbin/partitions,
//      xbind's page rendering for pentry (not the framed refusal), and the
//      menu closes;
//   4. the admin (a person who sees it) has the entry; an admin viewing the
//      workspace as pentry doesn't (the page shows view-as none of their
//      partitions);
//   5. the code drops partition: the entry goes, live.
// The tile and pentry are removed at the end (a rerun starts over).
const path = require('path');
const { URL, OUT, fs, login, closeCtx, openShell, usePersonalScreen, openTile, closeTile, settle, sleep, shotEl, checker } = require('../lib');

const WS = process.env.WS || '';
const TILE = 'apps/pentry', WHO = 'pentry', PASS = 'pentrypass123';
// the partition marker's teal (--bx-part): Concrete Night's, or Day's where
// the shell follows a light system (D184)
const TEALS = ['rgb(63, 181, 163)', 'rgb(31, 135, 120)'];
const tealOf = (page) => page.evaluate(() => {
  const s = document.createElement('span');
  s.style.color = 'var(--bx-part, #3FB5A3)';
  document.body.append(s);
  const c = getComputedStyle(s).color;
  s.remove();
  return c;
});
const SETTINGS = 'bx-shell .top button.chip.settings';
const MENU = 'bx-shell .wsmenu', ENTRY = 'bx-shell .wsmenu a[data-partitions]';

async function until(fn, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(200);
  }
}

function writeTile(partition) {
  const dir = path.join(WS, TILE);
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'index.html'), `<!doctype html><h1>${TILE}</h1>\n`);
  fs.writeFileSync(path.join(dir, 'xbin.json'), JSON.stringify({ title: TILE, ...(partition ? { partition } : {}) }));
}

// openMenu(page) → the settings menu, open (a click on the chip toggles it)
async function openMenu(page) {
  if (!await page.locator(MENU).count()) await page.locator(SETTINGS).click();
  await page.locator(MENU).waitFor({ timeout: 10000 });
  await settle(page);
}
// closeMenu(page): the menu's backdrop takes the press, as a click beside the menu would
async function closeMenu(page) {
  if (await page.locator(MENU).count()) await page.locator('bx-shell .ctx-backdrop').dispatchEvent('pointerdown');
  await page.locator(MENU).waitFor({ state: 'detached', timeout: 5000 });
}
const entryFacts = (page) => page.locator(ENTRY).first().evaluate((a) => {
  const pm = a.querySelector('.pm'), cs = getComputedStyle(a);
  return { text: [...a.children].map((c) => c.textContent.trim()).filter(Boolean).join(' '), ext: a.querySelector('bx-icon.ext')?.getAttribute('name') || '',
    href: a.getAttribute('href'), target: a.target, rel: a.rel, title: a.title,
    mark: !!pm?.querySelector('svg'), color: pm ? getComputedStyle(pm).color : '', prev: a.previousElementSibling?.textContent.trim() || '',
    underline: cs.textDecorationLine, w: Math.round(a.getBoundingClientRect().width), menuW: Math.round(a.closest('.wsmenu').getBoundingClientRect().width) };
}).catch(() => null);

async function partitionsEntry(browser) {
  const { check, done } = checker('partitions-entry');
  const A = await login(browser, 'admin', 'admin');
  const api = (ctx, method, p, data) => ctx.request.fetch(`${URL}/api/xbin${p}`, { method, data });
  const comp = async (t) => ((await (await api(A.ctx, 'GET', `/components/${t}`)).json().catch(() => ({}))).component || {});
  let P, V;
  try {
    // ---- the person and their tile ----
    const made = await api(A.ctx, 'POST', '/users', { id: WHO, name: 'P Entry', role: 'user', password: PASS });
    writeTile(null);
    await until(async () => (await api(A.ctx, 'GET', `/components/${TILE}`)).status() === 200, `${TILE} registered`);
    const own = await api(A.ctx, 'POST', '/owner', { tile: TILE, to: `user:${WHO}` });
    await until(async () => !(await comp(TILE)).partition?.user, `${TILE} unpartitioned`);
    check([200, 201, 409].includes(made.status()) && own.status() === 200, `${WHO} (no orgs) owns ${TILE}, unpartitioned (${made.status()}, ${own.status()})`);

    // ---- 1. nothing partitioned in sight: no entry ----
    P = await login(browser, WHO, PASS);
    const mine = await (await api(P.ctx, 'GET', '/components')).json().catch(() => []);
    const seen = (Array.isArray(mine) ? mine : []).filter((c) => c?.partition?.user).map((c) => c.path);
    check(seen.length === 0, `${WHO} sees no partitioned tile (${JSON.stringify(seen)})`);
    await openShell(P.page);
    await usePersonalScreen(P.page);
    await openMenu(P.page);
    const hd = await P.page.locator(`${MENU} .hd`, { hasText: 'my account' }).count();
    check(hd === 1 && await P.page.locator(ENTRY).count() === 0, `the settings menu's my-account block has no "your partitions" (account block ${hd})`);
    await shotEl(P.page, MENU, 'partitions-entry-absent');

    // ---- 2. the tile asks for user + global: the entry appears, live ----
    writeTile(['user', 'global']);
    const p = await until(async () => { const c = await comp(TILE); return c.partition?.state === 'partitioned' && c; }, `${TILE} partitioned`);
    check(p.partition.user && p.partition.global && !p.partition.request, `${TILE} holds nothing: user + global recorded at once (${JSON.stringify(p.partition)})`);
    await openMenu(P.page);
    const live = await P.page.locator(ENTRY).waitFor({ timeout: 15000 }).then(() => true).catch(() => false);
    check(live, 'with the menu open, "your partitions" appears once the listing holds a partitioned tile');
    const e = await entryFacts(P.page);
    check(e && e.text === 'your partitions' && e.ext === 'popout' && e.href === '/xbin/partitions' && e.target === '_blank' && /noopener/.test(e.rel),
      `a link to the partitions page, in a new tab: its words and the pop-out glyph (${JSON.stringify(e)})`);
    const teal = await tealOf(P.page);
    check(e && /^Your partitions page: /.test(e.title) && e.mark && TEALS.includes(teal) && e.color === teal,
      `its tooltip, and the marker's shape in the marker's teal (${e?.title}; ${e?.color} = ${teal})`);
    check(e && e.prev === 'devices…' && e.underline === 'none' && e.w >= e.menuW - 28, `in the account block after devices…, a button like it (${e?.prev}; ${e?.w}/${e?.menuW})`);
    await shotEl(P.page, MENU, 'partitions-entry');
    // the window of a tile with a global instance: `yours`, its tooltip naming the global instance
    await closeMenu(P.page);
    await openTile(P.page, TILE);
    const chipSel = `bx-canvas .card[data-path="${TILE}"] .head .pchip`;
    await P.page.locator(chipSel).waitFor({ timeout: 15000 });
    const chip = await P.page.locator(chipSel).evaluate((el) => ({ text: el.textContent.trim(), title: el.title }));
    check(chip.text === 'yours' && /^Your partition: this window shows your own data/.test(chip.title)
      && /\(a shared chat, for example\) comes from its global instance, not from your partition$/.test(chip.title),
    `the chip says yours; on a tile with a global instance its tooltip says what the tile shares comes from there (${JSON.stringify(chip)})`);
    await shotEl(P.page, `bx-canvas .card[data-path="${TILE}"] .head`, 'partitions-entry-chip');
    await closeTile(P.page, TILE);

    // ---- 3. it opens the page, top-level, in a new tab ----
    await openMenu(P.page);
    const [tab] = await Promise.all([P.ctx.waitForEvent('page', { timeout: 15000 }), P.page.locator(ENTRY).click()]);
    await tab.waitForLoadState('domcontentloaded');
    const h1 = await tab.locator('bx-partitions-page h1').first().waitFor({ timeout: 15000 }).then(() => tab.locator('bx-partitions-page h1').first().innerText()).catch(() => '');
    const top = await tab.evaluate(() => ({ top: window.top === window, opener: window.opener === null, refused: /opened on its own/.test(document.querySelector('bx-partitions-page')?.shadowRoot?.textContent || '') }));
    check(tab.url() === `${URL}/xbin/partitions` && /Your partitions/.test(h1) && top.top && !top.refused,
      `the entry opens xbind's page for ${WHO} in a new top-level tab (${tab.url()}; "${h1}"; ${JSON.stringify(top)})`);
    check(top.opener, 'the new tab keeps no handle on the shell (noopener)');
    await settle(P.page);
    check(await P.page.locator(MENU).count() === 0, 'the menu closes behind it');
    await tab.screenshot({ path: `${OUT}/partitions-entry-page.png` });
    await tab.close();

    // ---- 4. the admin has it; view-as doesn't ----
    await openShell(A.page);
    await openMenu(A.page);
    check(await A.page.locator(ENTRY).count() === 1, 'the admin (a person who sees a partitioned tile) has it too');
    await closeMenu(A.page);
    V = await login(browser, 'admin', 'admin');
    const tk = await (await V.ctx.request.post(`${URL}/api/xbin/impersonate`, { data: { user: WHO } })).json().catch(() => ({}));
    if (tk.url) {
      await V.page.goto(tk.url.startsWith('http') ? tk.url : `${URL}${tk.url}`);
      await openShell(V.page);
      await openMenu(V.page);
      check(await V.page.locator(`${MENU} .hd`, { hasText: 'my account' }).count() === 1 && await V.page.locator(ENTRY).count() === 0,
        `viewing the workspace as ${WHO}: the account block, without "your partitions"`);
      await V.ctx.request.post(`${URL}/api/xbin/impersonate/stop`).catch(() => {});
    } else {
      check(false, `a view-as ticket for ${WHO} (${JSON.stringify(tk)})`);
    }
    await V.ctx.close();
    V = null;

    // ---- 5. the code drops partition: the entry goes, live ----
    writeTile(null);
    await until(async () => { const c = await comp(TILE); return !c.partition?.user && !c.partition?.request && c; }, `${TILE} unpartitioned again`);
    await openMenu(P.page);
    const gone = await P.page.locator(ENTRY).waitFor({ state: 'detached', timeout: 15000 }).then(() => true).catch(() => false);
    check(gone, `${TILE} back to unpartitioned (nothing held): "your partitions" goes`);
  } finally {
    if (V) await V.ctx.close().catch(() => {});
    if (P) await closeCtx(P.ctx, P.page).catch(() => {});
    const dir = path.join(WS, TILE);
    if (WS && path.isAbsolute(WS) && dir === path.join(WS, 'apps', 'pentry')) {
      writeTile(null);
      await until(async () => !(await comp(TILE)).partition?.user, `${TILE} unpartitioned`).catch(() => {});
      fs.rmSync(dir, { recursive: true, force: true });
    }
    await api(A.ctx, 'DELETE', `/users/${WHO}`).catch(() => {});
    await closeCtx(A.ctx, A.page);
  }
  done();
}

module.exports = { partitionsEntry };
