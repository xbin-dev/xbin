// hack/ui-harness/passes/termsessions.js — the terminal session directory
// (D73): a user's sessions live on the server, so (a) a second browser
// signed in as the same user shows the same tabs, names included, and
// attaches to the same session; (b) another user on the first browser sees
// none of them and never asks for them; (c) the pref-held window state and
// the legacy browser record are adopted, not trusted.
const { URL, login, closeCtx, settle, sh, fr, waitFor, waitSel, openShell, usePersonalScreen, openTile, shot, checker } = require('../lib');

async function termSessions(browser) {
  const { check, done } = checker('term-sessions');
  const tabs = (page) => page.locator('bx-frame[src="apps/crawler"] .titlebar .tab .lbl').allTextContents();
  const sessionId = (page) => page.locator('bx-frame[src="apps/crawler"] bx-terminal').first().getAttribute('session');
  const openCrawlerTerm = async (page) => {
    await openShell(page);
    await usePersonalScreen(page);
    await openTile(page, 'apps/crawler');
    await fr(page, 'apps/crawler', (f) => f.open('term'));
    await settle(page);
    await waitSel(page, 'bx-frame[src="apps/crawler"] bx-terminal[session]', { timeout: 20000 });
  };
  // The `windows` pass leaves the shared per-user window pref off-screen (it
  // tests clamping) — reset it to a clean on-screen box BEFORE opening, so the
  // restored window (anchored to the tile, D66) is reachable. This pass
  // exercises the tab strip, not geometry.
  const resetWindow = (ctx) => ctx.request.put(`${URL}/api/xbin/prefs/term%3Aapps%3Acrawler`,
    { data: { open: true, active: 0, pop: { dx: 24, dy: 44, w: 680, h: 360 } } });
  // dblclick the tab near its left edge (the label side): a one-char tab's
  // centre lands on the ✕, and the exact label box shifts with the window.
  const renameTab = (page, i = 0) => page.locator('bx-frame[src="apps/crawler"] .titlebar .tab').nth(i).dblclick({ position: { x: 6, y: 9 } });

  // ---- A: admin opens a terminal and names the tab ----
  const A = await login(browser, 'admin', 'admin');
  const purge = async (ctx) => { // sessions earlier passes left on the tile
    for (const s of await (await ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json()) await ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(s.id)}`);
  };
  await purge(A.ctx);
  await resetWindow(A.ctx);
  await openCrawlerTerm(A.page);
  const idA = await sessionId(A.page);
  check(!!idA, `A opened a session (${idA})`);
  await renameTab(A.page);
  // the rename is a bx-dialog (no native prompt any more): answer it through the frame's test surface
  await waitFor(A.page, (t) => !!t.frameFor('apps/crawler')?.testApi().dialog, null, { timeout: 5000, label: 'the rename dialog' });
  await fr(A.page, 'apps/crawler', (f) => f.answerDialog('ok', { v: 'deploy' }));
  await settle(A.page);
  // the window state reaches the server on a 400 ms debounce: wait for it, not for time
  for (let i = 0; i < 40; i++) {
    const w = await (await A.ctx.request.get(`${URL}/api/xbin/prefs/term%3Aapps%3Acrawler`)).json().catch(() => null);
    if (w && w.open && w.pop) break;
    await new Promise((r) => setTimeout(r, 100));
  }
  const dirA = await (await A.ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json();
  check(dirA.length === 1 && dirA[0].id === idA && dirA[0].name === 'deploy' && dirA[0].cwd === 'apps/crawler',
    `the directory lists A's session with its name (${JSON.stringify(dirA.map((s) => [s.id, s.name, s.clients]))})`);
  check(await A.page.evaluate(() => localStorage.getItem('bx-term:apps/crawler')) === null, 'the browser keeps no session record any more');

  // ---- B: the same user in a second browser (fresh storage) sees and attaches to it ----
  const B = await login(browser, 'admin', 'admin');
  await openShell(B.page);
  await usePersonalScreen(B.page);
  await openTile(B.page, 'apps/crawler');
  await waitFor(B.page, (t) => !!t.frameFor('apps/crawler')?.testApi().terminalOpen, null, { timeout: 15000, label: "A's open window restored in B" });
  await waitSel(B.page, 'bx-frame[src="apps/crawler"] bx-terminal[session]', { timeout: 20000 });
  const idB = await sessionId(B.page);
  const tabsB = await tabs(B.page);
  check(idB === idA, `B attached to the same session (${idB})`);
  check(tabsB.length === 1 && tabsB[0] === 'deploy', `B shows A's tab, named (${JSON.stringify(tabsB)})`);
  await shot(B.page, 'term-sessions-b', { fullPage: false });
  // a tab opened in B appears in A (the term event → re-list)
  await B.page.locator('bx-frame[src="apps/crawler"] .titlebar button[title="new terminal"]').click();
  await waitFor(A.page, (t) => t.frameFor('apps/crawler')?.renderRoot.querySelectorAll('.titlebar .tab').length === 2, null, { timeout: 15000, label: "B's new tab to appear in A" });
  const dirAB = await (await A.ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json();
  check(dirAB.length === 2, `two sessions now (${dirAB.length})`);
  check((await tabs(A.page)).length === 2, `A's tab bar followed (${JSON.stringify(await tabs(A.page))})`);

  // ---- C: another user on A's browser: none of admin's tabs, no request for them ----
  const asked = [];
  A.page.on('request', (r) => { if (/\/ws\/term\?session=/.test(r.url())) asked.push(r.url()); });
  await A.page.evaluate(() => fetch('/logout', { method: 'POST' }));
  await A.page.goto(`${URL}/login`);
  await A.page.fill('input[name=username]', 'dev1');
  await A.page.fill('input[name=password]', 'devpass123');
  await Promise.all([A.page.waitForNavigation(), A.page.click('button')]);
  await purge(A.ctx); // dev1's own leftovers from earlier passes — the directory is per user, so this touches none of admin's
  await openShell(A.page);
  await usePersonalScreen(A.page);
  await openTile(A.page, 'apps/crawler');
  await settle(A.page); await settle(A.page);
  const dev1Open = await fr(A.page, 'apps/crawler', (f) => f.terminalOpen);
  const dev1Dir = await (await A.ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json();
  check(dev1Dir.length === 0 && dirAB.length === 2, `dev1's directory on the tile is empty while admin's two live on (${dev1Dir.length}, ${(await (await B.ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json()).length})`);
  check(!dev1Open, 'no window opened for dev1 from admin\'s state');
  check(asked.length === 0, `dev1's browser never asked for admin's sessions (${asked.join(', ')})`);
  await fr(A.page, 'apps/crawler', (f) => f.open('term'));
  await waitSel(A.page, 'bx-frame[src="apps/crawler"] bx-terminal[session]', { timeout: 20000 });
  const idD = await sessionId(A.page);
  const tabsD = await tabs(A.page);
  check(!!idD && idD !== idA && !dirAB.some((s) => s.id === idD), `dev1 got a fresh session of their own (${idD})`);
  check(tabsD.length === 1 && tabsD[0] === '1', `dev1's tab bar is their own (${JSON.stringify(tabsD)})`);
  // and admin, in B, still sees exactly admin's two
  check(dirAB.every((s) => s.id !== idD), 'the two directories do not mix');

  // ---- D: the legacy browser record is adopted once and removed ----
  await fr(A.page, 'apps/crawler', (f) => f.closeTerminal());
  await A.page.evaluate((id) => localStorage.setItem('bx-term:apps/crawler', JSON.stringify({ open: true, active: 0, pop: { dx: 10, dy: 10, w: 500, h: 300 }, sessions: [{ id, name: 'legacy name' }] })), idD);
  await A.ctx.request.delete(`${URL}/api/xbin/prefs/term%3Aapps%3Acrawler`);
  await A.page.reload();
  await A.page.waitForSelector('bx-shell', { timeout: 15000 });
  await waitFor(A.page, (t) => !!t && t.screens.length > 0, null, { timeout: 15000, label: 'shell after reload' });
  await usePersonalScreen(A.page);
  await openTile(A.page, 'apps/crawler');
  await waitFor(A.page, (t) => !!t.frameFor('apps/crawler')?.testApi().terminalOpen, null, { timeout: 15000, label: 'the legacy window state reopens the window' });
  await waitFor(A.page, (t) => (t.frameFor('apps/crawler')?.renderRoot.querySelector('.titlebar .tab .lbl')?.textContent || '') === 'legacy name', null, { timeout: 10000, label: 'the legacy tab name to be adopted' });
  check(await A.page.evaluate(() => localStorage.getItem('bx-term:apps/crawler')) === null, 'the legacy record is gone after adoption');
  const dirD = await (await A.ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json();
  check(dirD[0]?.name === 'legacy name', `the adopted name is on the server (${JSON.stringify(dirD.map((s) => s.name))})`);

  // ---- tidy: end every session opened here, drop the prefs ----
  for (const id of [...dirAB.map((s) => s.id), idD]) await B.ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(id)}`);
  await A.ctx.request.delete(`${URL}/api/xbin/prefs/term%3Aapps%3Acrawler`);
  await B.ctx.request.delete(`${URL}/api/xbin/prefs/term%3Aapps%3Acrawler`);
  await sh(A.page, (t) => t.closeTile('apps/crawler'));
  await closeCtx(A.ctx, A.page);
  await closeCtx(B.ctx, B.page);
  done();
}

module.exports = { termSessions };
