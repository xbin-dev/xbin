// hack/ui-harness/passes/deeplinks.js — links to a place in the workspace
// (#7, D220): the workspace's plain landing writes its screen into the URL
// without a history entry; a link to a screen and a tile not on it opens
// the tile there, in front, its page at the link's fragment (the admin
// console on #users); the tile's own fragment moving rewrites the URL in
// place; another screen is a new history entry and Back lands on the
// linked place again; a link to a tile already open (the welcome notes)
// moves its page to a nested path (#storage/kv), and Back moves it back;
// an unknown screen and tile, and a tile a person may not read, say so in
// the strip above the canvas — the rest of the screen as it was.
const { URL, sleep, settle, sh, waitFor, openShell, usePersonalScreen, login, shot, checker, THEME } = require('../lib');

const query = (page) => new globalThis.URL(page.url()).searchParams;
const frameHash = (page, p) => sh(page, (t, p) => t.frameFor(p)?.hash ?? null, p);
// the tile page's own document (sandboxed: its own frame in Playwright's tree)
async function tilePage(page, p) {
  for (const until = Date.now() + 10000; Date.now() < until; await sleep(100)) {
    const f = page.frames().find((x) => x.url().startsWith(`${URL}/c/${p}/`));
    if (f) return f;
  }
  throw new Error(`no page for ${p}`);
}

async function deepLinks(browser) {
  const { check, done } = checker(`deeplinks-${THEME}`);
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 900 } });
  const prefs = `${URL}/api/xbin/prefs/layout`;
  const original = await (await ctx.request.get(prefs)).json();

  // the plain landing: / → /c/root/, the screen written in, no new entry
  await openShell(page);
  await waitFor(page, () => new globalThis.URL(location.href).searchParams.has('screen'), null, { label: 'the screen in the URL' });
  check(/\/c\/root\/\?screen=/.test(page.url()), `the landing names its screen (${page.url()})`);
  await usePersonalScreen(page);
  const home = await sh(page, (t) => t.activeScreen);
  const linked = await sh(page, (t) => t.addScreen('canvas')); // an empty screen of our own
  await sh(page, (t) => t.flushSave());

  // a link to the new screen and a tile not on it, at a place in its page
  await page.goto(`${URL}/c/root/?screen=${linked}&tile=tiles/admin&at=users`);
  await waitFor(page, (t, s) => t?.activeScreen === s && t.isOpen('tiles/admin') && t.frameFor('tiles/admin')?.hash === 'users', linked, { timeout: 15000, label: 'landed on the linked tile' });
  check(true, 'the linked screen is active and the tile opened on it');
  check(await sh(page, (t) => !t.routeMessage), 'no message for a link that resolves');
  const q1 = query(page);
  check(q1.get('screen') === linked && q1.get('tile') === 'tiles/admin' && q1.get('at') === 'users', `the URL keeps the place (${page.url()})`);
  const admin = await tilePage(page, 'tiles/admin');
  check(!!admin && new globalThis.URL(admin.url()).hash === '#users', `the admin console's page is at #users (${admin?.url()})`);
  await shot(page, `deeplinks-tile-${THEME}`, { fullPage: false });

  // the tile moves inside itself: the URL follows, in place
  const lenA = await page.evaluate(() => history.length);
  await admin.evaluate(() => history.replaceState(null, '', '#settings'));
  await waitFor(page, () => new globalThis.URL(location.href).searchParams.get('at') === 'settings', null, { label: 'at follows the tile' });
  check(await page.evaluate(() => history.length) === lenA, 'a tile\'s own move replaces the entry');

  // another screen: a new entry; Back lands on the linked place again
  await sh(page, (t, id) => t.setScreen(id), home);
  await waitFor(page, (t, id) => new globalThis.URL(location.href).searchParams.get('screen') === id, home, { label: 'the URL follows the screen' });
  check(await page.evaluate(() => history.length) === lenA + 1, 'another screen is a new history entry');
  await page.goBack();
  await waitFor(page, (t, s) => t.activeScreen === s, linked, { label: 'Back: the linked screen' });
  const q2 = query(page);
  check(q2.get('screen') === linked && q2.get('tile') === 'tiles/admin' && q2.get('at') === 'settings', `Back restores the place (${page.url()})`);
  await page.goForward();
  await waitFor(page, (t, s) => t.activeScreen === s, home, { label: 'Forward: the other screen' });
  check(true, 'Forward goes to the other screen again');

  // a nested path in a tile that's already open, then Back
  await sh(page, (t) => t.openTile('apps/welcome'));
  await waitFor(page, (t) => !!t.frameFor('apps/welcome'), null, { label: 'welcome open' });
  await sleep(500);
  await page.goto(`${URL}/c/root/?screen=${home}&tile=apps/welcome&at=storage/kv`);
  await waitFor(page, (t) => t?.frameFor('apps/welcome')?.hash === 'storage/kv', null, { timeout: 15000, label: 'welcome at storage/kv' });
  const notes = await tilePage(page, 'apps/welcome');
  await notes.waitForSelector('nav.crumbs .here', { timeout: 5000 });
  const crumb = await notes.locator('nav.crumbs .here').textContent();
  check(crumb.trim() === 'kv', `the notes show the nested page (${crumb.trim()})`);
  await shot(page, `deeplinks-nested-${THEME}`, { fullPage: false });
  await sh(page, (t, s) => t.setScreen(s), linked);
  await waitFor(page, (t, s) => new globalThis.URL(location.href).searchParams.get('screen') === s, linked, { label: 'moved on' });
  await page.goto(`${URL}/c/root/?screen=${home}&tile=apps/welcome&at=rbac`);
  await waitFor(page, (t) => t?.frameFor('apps/welcome')?.hash === 'rbac', null, { timeout: 15000, label: 'welcome at rbac' });
  await page.goBack();
  await waitFor(page, (t, s) => t.activeScreen === s, linked, { label: 'Back past the welcome link' });
  await page.goBack();
  await waitFor(page, (t) => t.frameFor('apps/welcome')?.hash === 'storage/kv', null, { label: 'Back: welcome at storage/kv again' });
  const crumb2 = await (await tilePage(page, 'apps/welcome')).locator('nav.crumbs .here').textContent();
  check(crumb2.trim() === 'kv', `an open tile follows Back to its earlier place (${crumb2.trim()})`);

  // what can't be linked: an unknown screen and tile
  await page.goto(`${URL}/c/root/?screen=nope123&tile=apps/no-such-tile&at=x`);
  await waitFor(page, (t) => !!t?.routeMessage, null, { timeout: 15000, label: 'the message' });
  const msg = await sh(page, (t) => t.routeMessage);
  check(msg.length === 2 && /screen/.test(msg[0]) && /apps\/no-such-tile/.test(msg[1]), `unknown screen and tile, both said (${JSON.stringify(msg)})`);
  check(await sh(page, (t) => !!t.query('.routemsg') && t.screens.length > 0), 'the strip shows over the person\'s own screen');
  await shot(page, `deeplinks-unknown-${THEME}`, { fullPage: false });
  await sh(page, (t) => t.query('.routemsg button')?.click());
  await settle(page);
  check(await sh(page, (t) => !t.routeMessage), 'dismiss clears it');

  // tidy: the layout as it was
  await sh(page, (t) => t.flushSave());
  const put = await ctx.request.put(prefs, { data: original, headers: { 'X-Prefs-Writer': 'harness-deeplinks' } });
  check(put.ok(), `tidy: the original layout back (${put.status()})`);
  await ctx.close();

  // a tile this person may not read
  const dev = await login(browser, 'dev1', 'devpass123', { viewport: { width: 1400, height: 900 } });
  await dev.page.goto(`${URL}/c/root/?tile=tiles/admin&at=users`);
  await waitFor(dev.page, (t) => !!t?.routeMessage, null, { timeout: 15000, label: 'the no-access message' });
  const m2 = await sh(dev.page, (t) => ({ msg: t.routeMessage, open: t.isOpen('tiles/admin') }));
  check(m2.msg.length === 1 && /tiles\/admin.*access/.test(m2.msg[0]) && !m2.open, `an unreadable tile says so and opens nothing (${JSON.stringify(m2)})`);
  await shot(dev.page, `deeplinks-noaccess-${THEME}`, { fullPage: false });
  await dev.ctx.close();
  done();
}

module.exports = { deepLinks };
