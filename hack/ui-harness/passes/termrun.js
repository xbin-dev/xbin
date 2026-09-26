// hack/ui-harness/passes/termrun.js — a terminal's one-shot `run` command
// (D75: the Agent tab's one-click sign-in opens a shell that types the
// login command) reaches the shell. It must go as typed input — a binary
// frame; a text frame is control JSON on /ws/term and anything else in one
// is dropped (internal/term/attach.go), which is how it was lost before.
// And it goes ONCE: closing the terminal window and opening it again makes
// a new <bx-terminal> for the same live session, which must not type the
// command into whatever the shell runs by then.
const { URL, login, closeCtx, settle, fr, waitSel, openShell, openTile, checker } = require('../lib');

async function termRun(browser) {
  const { check, done } = checker('term-run');
  const { ctx, page } = await login(browser, 'admin', 'admin');
  // a fresh start: end the crawler sessions earlier passes left (restored as tabs, D73)
  for (const s of await (await ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json()) await ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(s.id)}`);
  await openShell(page);
  await openTile(page, 'apps/crawler');
  // the launcher path the sign-in takes (bx-frame _signIn → _startKind('shell', null, {run}))
  // each run adds 42: a second one would print xbin-run-84
  await fr(page, 'apps/crawler', (f) => { f.open('term'); f.startKind('shell', null, { run: 'N=$((N+6*7)); echo xbin-run-$N' }); });
  const termSel = 'bx-frame[src="apps/crawler"] bx-terminal';
  await waitSel(page, `${termSel} textarea`, { timeout: 20000 });
  // the output, not the echoed command line ($N is only expanded by the shell)
  const screen = (ms) => page.locator(termSel).first().evaluate(async (el, ms) => {
    const t = el.testApi(), seen = new Set(), end = Date.now() + ms;
    do {
      for (let row = 0; row < 40; row++) { const m = /^(xbin-run-\d+)\s*$/.exec(t.screenLine(row)); if (m) seen.add(m[1]); }
      if (!ms && seen.size) break;
      await new Promise((r) => setTimeout(r, 40));
    } while (Date.now() < end || (!ms && Date.now() < end + 10000));
    return [...seen];
  }, ms);
  const first = await screen(0);
  check(first.includes('xbin-run-42'), `the run command was typed into the new shell and ran (${first})`);
  // spent once typed: the tab no longer carries it, whatever listing lands
  let run = 'unset';
  for (let i = 0; i < 100 && run !== null; i++) {
    run = await fr(page, 'apps/crawler', (f) => f.tabs[0]?.run ?? null);
    if (run !== null) await page.waitForTimeout(50);
  }
  check(run === null, `the tab drops its one-shot command once typed (run=${run})`);
  // close the window, open it again: the same session, a new element
  await fr(page, 'apps/crawler', (f) => f.closeTerminal());
  await settle(page);
  await fr(page, 'apps/crawler', (f) => f.open('term'));
  await waitSel(page, `${termSel} textarea`, { timeout: 20000 });
  const again = await screen(1500);
  check(again.includes('xbin-run-42') && !again.includes('xbin-run-84'), `reopening the window does not type the command again (${again})`);
  await fr(page, 'apps/crawler', (f) => { while (f?.tabs.length) f.closeTab(0); });
  await settle(page);
  await closeCtx(ctx, page);
  done();
}

module.exports = { termRun };
