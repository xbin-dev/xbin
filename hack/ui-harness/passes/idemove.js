// hack/ui-harness/passes/idemove.js — the tile window as an IDE (D202):
// a session's element is never re-created or re-parented by the layout. A
// shell keeps answering after its tab moves to a new pane (split, a drop on
// a pane's edge), its element the same one; an element moved in the DOM
// for real (a disconnect and a connect in one task) keeps its socket and
// its screen (bx-terminal's deferred teardown); the logs dock's <bx-logs>
// is mounted once and only hidden. Then the window narrow: the status bar
// folds its pickers into one chip.
const { URL, login, closeCtx, settle, fr, waitSel, waitFor, openShell, openTile, shot, checker } = require('../lib');

const TILE = 'apps/crawler';
const sel = `bx-frame[src="${TILE}"]`;
const termSel = `${sel} bx-terminal`;
// the window's other tabs, side panel and dock shut
const tidy = (f) => {
  for (const p of f.ide.panes) for (const t of p.tabs) if (!t.startsWith('s:')) f.ide.close(t);
  if (f.ide.side) f.ide.openTool(f.ide.side);
  f.ide.setDock(false);
};

async function ideMove(browser) {
  const { check, done } = checker('ide-move');
  const { ctx, page } = await login(browser, 'admin', 'admin');
  for (const s of await (await ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=${encodeURIComponent(TILE)}`)).json()) await ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(s.id)}`);
  await openShell(page);
  await openTile(page, TILE);
  await fr(page, TILE, tidy); // what an earlier run left in the window pref
  await fr(page, TILE, (f) => { f.open('term'); f.setPop({ x: 40, y: 60, w: 1100, h: 640 }); });
  await waitSel(page, `${sel} .pane .pbody .launcher .lcard`);
  await shot(page, 'ide-move-empty'); // no session: the pane offers the launcher, the status bar no pickers
  await fr(page, TILE, (f) => f.startKind('shell'));
  await waitSel(page, `${termSel} textarea`, { timeout: 20000 });
  const term = page.locator(termSel).first();
  const text = () => term.evaluate((el) => el.testApi().text()).catch(() => '');
  const answers = async (re) => {
    for (let i = 0; i < 150; i++) { if (re.test(await text())) return true; await page.waitForTimeout(100); }
    return false;
  };
  const say = async (cmd) => { await term.click(); await page.keyboard.type(cmd); await page.keyboard.press('Enter'); };
  await answers(/[$#] ?$/m);
  await say('echo xbin-ide-$((6*7))');
  check(await answers(/^xbin-ide-42\s*$/m), 'the shell answers');
  await term.evaluate((el) => { el.xbinIdeMark = 'first'; });

  // a file in the focused pane, split off to the right; the session dropped on that pane's bottom edge
  // (its own pane, emptied, goes)
  await fr(page, TILE, (f) => { f.ide.openFile('main.go'); f.ide.split('row'); });
  await settle(page);
  const ref = await fr(page, TILE, (f) => f.ide.sessionRef(0));
  await fr(page, TILE, (f, t, ref) => f.ide.move(ref, f.ide.panes.find((p) => p.tabs.includes('f:main.go')).id, 'b'), ref);
  await settle(page);
  const panes = await fr(page, TILE, (f) => f.ide.panes);
  check(panes.length === 2 && panes.some((p) => p.tabs.length === 1 && p.tabs[0] === ref && p.active === ref), `the session has a pane of its own (${JSON.stringify(panes.map((p) => p.tabs))})`);
  const same = await page.locator(termSel).evaluateAll((els) => els.length === 1 && els[0].xbinIdeMark === 'first' && els[0].testApi().open);
  check(same, 'moved to another pane, the terminal is the same element and still connected');
  // the element sits over its pane's body: inside the pane's box, below its tab strip
  const placed = await page.locator(sel).evaluate((host, ref) => {
    const r = host.shadowRoot, t = r.querySelector('bx-terminal').getBoundingClientRect();
    const pane = [...r.querySelectorAll('.pane')].find((p) => p.querySelector(`.ptab[data-ref="${ref}"]`))?.getBoundingClientRect();
    return pane && t.left >= pane.left - 1 && t.right <= pane.right + 1 && t.top >= pane.top + 28 && t.bottom <= pane.bottom + 1 && t.width > 100;
  }, ref);
  check(placed, 'the terminal covers its new pane\'s body');
  await say('echo xbin-ide-$((7*7))');
  check(await answers(/^xbin-ide-49\s*$/m), 'the shell answers in its new pane');
  await shot(page, 'ide-move-panes');

  // a real move in the DOM: out and back in one task — the deferred teardown keeps it alive
  await term.evaluate((el) => { const p = el.parentNode, n = el.nextSibling; document.body.appendChild(el); p.insertBefore(el, n); });
  await page.waitForTimeout(400);
  check(await term.evaluate((el) => el.testApi().open && el.xbinIdeMark === 'first'), 'moved in the DOM and back, the terminal kept its socket');
  await say('echo xbin-ide-$((8*8))');
  check(await answers(/^xbin-ide-64\s*$/m), 'and its shell still answers');

  // the logs dock: mounted once, hidden while shut
  await fr(page, TILE, (f) => f.ide.openTool('logs'));
  await waitSel(page, `${sel} .dock bx-logs`, { state: 'attached' });
  await page.locator(`${sel} .dock bx-logs`).evaluate((el) => { el.xbinIdeMark = 'logs'; });
  await fr(page, TILE, (f) => f.ide.openTool('logs'));
  await settle(page);
  await fr(page, TILE, (f) => f.ide.openTool('files'));
  await fr(page, TILE, (f) => f.ide.openTool('logs'));
  await settle(page);
  check(await page.locator(`${sel} .dock bx-logs`).evaluate((el) => el.xbinIdeMark === 'logs' && el.getBoundingClientRect().height > 40), 'the dock\'s logs are the same element, shown again');
  await shot(page, 'ide-move-dock');

  // a tab dragged by the mouse onto another pane's middle moves there
  const box = (q) => page.locator(q).first().evaluate((el) => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, w: r.width, h: r.height }; });
  const tab = await box(`${sel} .ptab[data-ref="f:main.go"]`), into = await box(`${sel} bx-terminal`);
  await page.mouse.move(tab.x + tab.w / 2, tab.y + tab.h / 2);
  await page.mouse.down();
  for (let i = 1; i <= 8; i++) { await page.mouse.move(tab.x + ((into.x + into.w / 3) - tab.x) * i / 8, tab.y + ((into.y + into.h / 3) - tab.y) * i / 8); await page.waitForTimeout(30); }
  await page.mouse.up();
  await settle(page);
  const after = await fr(page, TILE, (f) => f.ide.panes.map((p) => p.tabs));
  check(after.length === 1 && after[0].includes('f:main.go') && after[0].includes(ref), `dragged onto the session's pane, the file tab moved there (${JSON.stringify(after)})`);
  // a divider: dragged, then a double-click halves it
  await fr(page, TILE, (f) => f.ide.split('row'));
  await settle(page);
  const vd = await box(`${sel} .vd`);
  await page.mouse.move(vd.x + vd.w / 2, vd.y + vd.h / 2);
  await page.mouse.down();
  await page.mouse.move(vd.x + vd.w / 2 + 40, vd.y + vd.h / 2, { steps: 4 });
  await page.mouse.move(vd.x + vd.w / 2 + 120, vd.y + vd.h / 2, { steps: 4 });
  await page.mouse.up();
  const r1 = await fr(page, TILE, (f) => f.ide.tree.r);
  await page.mouse.dblclick(vd.x + vd.w / 2 + 120, vd.y + vd.h / 2);
  const r2 = await fr(page, TILE, (f) => f.ide.tree.r);
  check(r1 > 0.55 && r2 === 0.5, `the divider drags (${r1}) and a double-click halves it (${r2})`);
  check(await term.evaluate((el) => el.testApi().open && el.xbinIdeMark === 'first'), 'through all of it, the one terminal element, connected');

  // narrow: the pickers fold into one chip on the status bar
  await fr(page, TILE, (f) => f.setPop({ x: 40, y: 60, w: 560, h: 560 }));
  await waitFor(page, (t, s) => t.frameFor(s)?.testApi().narrow, TILE, { label: 'narrow' });
  const st = await fr(page, TILE, (f) => f.ide.status);
  check(st.folded && st.pickers.length === 0, `narrow: the status bar folds its pickers (${JSON.stringify(st)})`);
  await shot(page, 'ide-move-narrow');

  await fr(page, TILE, tidy);
  await fr(page, TILE, (f) => { while (f?.tabs.length) f.closeTab(0); f.closeTerminal(); });
  await settle(page);
  await closeCtx(ctx, page);
  done();
}

module.exports = { ideMove };
