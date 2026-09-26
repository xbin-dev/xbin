// hack/ui-harness/passes/termrun.js — a terminal's one-shot `run` command
// (D75: the Agent tab's one-click sign-in opens a shell that types the
// login command) reaches the shell. It must go as typed input — a binary
// frame; a text frame is control JSON on /ws/term and anything else in one
// is dropped (internal/term/attach.go), which is how it was lost before.
const { URL, login, closeCtx, settle, fr, waitSel, openShell, openTile, checker } = require('../lib');

async function termRun(browser) {
  const { check, done } = checker('term-run');
  const { ctx, page } = await login(browser, 'admin', 'admin');
  // a fresh start: end the crawler sessions earlier passes left (restored as tabs, D73)
  for (const s of await (await ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json()) await ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(s.id)}`);
  await openShell(page);
  await openTile(page, 'apps/crawler');
  // the launcher path the sign-in takes (bx-frame _signIn → _startKind('shell', null, {run}))
  await fr(page, 'apps/crawler', (f) => { f.open('term'); f.startKind('shell', null, { run: 'echo xbin-run-$((6*7))' }); });
  const termSel = 'bx-frame[src="apps/crawler"] bx-terminal';
  await waitSel(page, `${termSel} textarea`, { timeout: 20000 });
  const term = page.locator(termSel).first();
  // the output, not the echoed command line ($((6*7)) is only expanded by the shell)
  const ran = await term.evaluate(async (el) => {
    const t = el.testApi();
    for (let i = 0; i < 250; i++) {
      for (let row = 0; row < 40; row++) if (/^xbin-run-42\s*$/.test(t.screenLine(row))) return true;
      await new Promise((r) => setTimeout(r, 40));
    }
    return false;
  });
  check(ran, 'the run command was typed into the new shell and ran');
  await fr(page, 'apps/crawler', (f) => { while (f?.tabs.length) f.closeTab(0); });
  await settle(page);
  await closeCtx(ctx, page);
  done();
}

module.exports = { termRun };
