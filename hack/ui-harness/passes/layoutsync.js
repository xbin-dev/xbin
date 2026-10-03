// hack/ui-harness/passes/layoutsync.js — the shell follows its layout pref
// when ANOTHER client writes it (the app making a screen): the write's
// `prefs` event reloads the layout in an open shell without a page reload,
// keeps the screen the tab shows, waits while an edit is under way (a menu
// open), and the shell's own next save keeps what the other client added.
// The same bucket's appearance keys (D184): the settings menu's Theme and
// Density restyle the page and reach the server, and another client's
// theme reaches the open shell.
const { URL, login, closeCtx, settle, sh, waitFor, openShell, usePersonalScreen, checker, sleep } = require('../lib');

async function layoutSync(browser) {
  const { check, done } = checker('layout-sync');
  const { ctx, page } = await login(browser, 'admin', 'admin');
  await openShell(page);
  await usePersonalScreen(page);
  await sh(page, (t) => t.flushSave());
  const active = await sh(page, (t) => t.activeScreen);
  const prefs = `${URL}/api/xbin/prefs/layout`;
  const original = await (await ctx.request.get(prefs)).json();
  // the app: same user, its own client — a write with its own writer id
  const phoneWrite = (layout) => ctx.request.put(prefs, { data: layout, headers: { 'X-Prefs-Writer': 'phone-app' } });

  const withScreen = (l, id, name) => ({ ...l, screens: [...(l.screens ?? []), { id, name, tiles: [] }] });
  const put1 = await phoneWrite(withScreen(original, 'phone1', 'From the phone'));
  check(put1.ok(), `the phone's layout write lands (${put1.status()})`);
  let got = true;
  try { await waitFor(page, (t) => t.screens.some((x) => x.id === 'phone1'), null, { timeout: 5000, label: 'phone screen appears' }); } catch { got = false; }
  check(got, 'the open shell shows the screen the phone made, without a reload');
  check(await sh(page, (t) => t.activeScreen) === active, 'the tab stays on the screen it showed');

  // the shell's next save keeps it
  await sh(page, (t, id) => t.setScreen(id), active);
  await sh(page, (t) => t.flushSave());
  let saved = await (await ctx.request.get(prefs)).json();
  check(saved.screens?.some((x) => x.id === 'phone1'), `the shell's next save keeps the phone's screen (${(saved.screens ?? []).map((x) => x.id).join(', ')})`);

  // an edit under way (a menu open) holds the reload until it ends
  await sh(page, (t) => t.openCanvasMenu({ x: 300, y: 300 }));
  await settle(page);
  check(await sh(page, (t) => t.menuOpen && t.layoutBusy), 'a canvas menu counts as an edit under way');
  const put2 = await phoneWrite(withScreen(saved, 'phone2', 'Second from the phone'));
  check(put2.ok(), `a second phone write lands (${put2.status()})`);
  await sleep(700);
  check(!(await sh(page, (t) => t.screens.some((x) => x.id === 'phone2'))), 'while the menu is open the shell does not reload its layout');
  await sh(page, (t) => t.closeMenu());
  got = true;
  try { await waitFor(page, (t) => t.screens.some((x) => x.id === 'phone2'), null, { timeout: 5000, label: 'second phone screen appears' }); } catch { got = false; }
  check(got, 'once the menu closes the held reload happens');

  // tidy: the layout as it was (the shell follows this write too)
  const put3 = await phoneWrite(original);
  check(put3.ok(), `tidy: the original layout back (${put3.status()})`);
  try { await waitFor(page, (t) => !t.screens.some((x) => x.id === 'phone1' || x.id === 'phone2'), null, { timeout: 5000, label: 'tidy reload' }); } catch { /* checked below */ }
  saved = await (await ctx.request.get(prefs)).json();
  check(!(saved.screens ?? []).some((x) => x.id.startsWith('phone')), 'tidy: no phone screens left');
  await appearance(ctx, page, check);
  await closeCtx(ctx, page);
  done();
}

// The appearance (D184): the theme and density keys of the same bucket. The
// settings menu's Theme and Density are segmented controls (aria-pressed);
// a pick restyles the page at once and reaches the server (PUT, or DELETE
// for the default); another client's write reaches the open shell through
// its `prefs` event.
async function appearance(ctx, page, check) {
  const at = (key) => `${URL}/api/xbin/prefs/${key}`;
  const stored = async (key) => { const r = await ctx.request.get(at(key)); return r.status() === 404 ? null : r.json().catch(() => '?'); };
  const look = () => page.evaluate(() => {
    const cs = getComputedStyle(document.documentElement);
    return { theme: document.head.querySelector('meta[name="xbin-theme"]')?.content || 'system',
      density: document.head.querySelector('meta[name="xbin-density"]')?.content || 'compact',
      scheme: cs.getPropertyValue('--bx-scheme').trim(), row: cs.getPropertyValue('--bx-row').trim() };
  });
  const pressed = () => sh(page, (t) => [...t.query('.wsmenu')?.querySelectorAll('[data-appearance][aria-pressed="true"]') ?? []].map((b) => b.dataset.appearance));
  for (const key of ['theme', 'density']) await ctx.request.delete(at(key));
  await sh(page, (t) => t.openSettings());
  await settle(page);
  const sys = await look();
  check(JSON.stringify(await pressed()) === JSON.stringify(['theme:system', 'density:compact']) && sys.theme === 'system' && sys.row === '28px',
    `the settings menu shows Theme: System and Density: Compact, pressed (${JSON.stringify(await pressed())}; ${JSON.stringify(sys)})`);
  const want = sys.scheme === 'light' ? 'dark' : 'light'; // the one this browser's system isn't
  await page.locator(`bx-shell .wsmenu [data-appearance="theme:${want}"]`).click();
  await page.locator('bx-shell .wsmenu [data-appearance="density:comfortable"]').click();
  await settle(page);
  let l = await look();
  check(l.theme === want && l.scheme === want && l.density === 'comfortable' && l.row === '32px', `a pick restyles the page at once (${JSON.stringify(l)})`);
  let s = null;
  for (let i = 0; i < 30 && s !== want; i++) { s = await stored('theme'); if (s !== want) await sleep(100); }
  check(s === want && await stored('density') === 'comfortable', `… and reaches the server: theme ${JSON.stringify(s)}, density ${JSON.stringify(await stored('density'))}`);
  check(JSON.stringify(await pressed()) === JSON.stringify([`theme:${want}`, 'density:comfortable']), `the pressed segments follow (${JSON.stringify(await pressed())})`);
  await page.locator('bx-shell .wsmenu [data-appearance="theme:system"]').click();
  await page.locator('bx-shell .wsmenu [data-appearance="density:compact"]').click();
  await settle(page);
  for (let i = 0; i < 30 && (s = await stored('theme')) !== null; i++) await sleep(100);
  check(s === null && await stored('density') === null && (await look()).scheme === sys.scheme, 'System and Compact delete the keys: the page follows the system again');
  await page.locator('bx-shell .ctx-backdrop').first().dispatchEvent('pointerdown');
  // another client (the app, another tab) writes the theme: the open shell follows
  const other = await ctx.request.put(at('theme'), { data: JSON.stringify(want), headers: { 'Content-Type': 'application/json', 'X-Prefs-Writer': 'phone-app' } });
  let followed = true;
  try {
    await page.waitForFunction((w) => document.head.querySelector('meta[name="xbin-theme"]')?.content === w, want, { timeout: 5000 });
  } catch { followed = false; }
  l = await look();
  check(other.ok() && followed && l.scheme === want, `another client's theme reaches the open shell without a reload (${other.status()}, ${JSON.stringify(l)})`);
  await ctx.request.delete(at('theme'), { headers: { 'X-Prefs-Writer': 'phone-app' } });
  try {
    await page.waitForFunction(() => !document.head.querySelector('meta[name="xbin-theme"]'), null, { timeout: 5000 });
  } catch { /* checked below */ }
  check((await look()).theme === 'system', 'its delete too: the shell follows the system again');
}

module.exports = { layoutSync };
