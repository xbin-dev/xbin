// hack/ui-harness/passes/layoutsync.js — the shell follows its layout pref
// when ANOTHER client writes it (the app making a screen): the write's
// `prefs` event reloads the layout in an open shell without a page reload,
// keeps the screen the tab shows, waits while an edit is under way (a menu
// open), and the shell's own next save keeps what the other client added.
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
  await closeCtx(ctx, page);
  done();
}

module.exports = { layoutSync };
