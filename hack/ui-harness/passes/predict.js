// hack/ui-harness/passes/predict.js — the terminal's predictive echo (D70):
// this xbind acks input and answers pings, typed text is predicted and
// confirmed, a wrong prediction is withdrawn, the 🔧 menu has the toggle.
// A local PTY echoes within a millisecond, so the pass holds the acks to see
// a prediction pending, and types into `read -s` to see one drawn.
const { URL, login, closeCtx, settle, fr, waitSel, openShell, openTile, shot, checker } = require('../lib');

async function predict(browser) {
  const { check, done } = checker('predict');
  const { ctx, page } = await login(browser, 'admin', 'admin');
  // the pass types into a FRESH shell: end the crawler sessions earlier passes
  // left (the directory would restore every one of them as a tab, D73)
  for (const s of await (await ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json()) await ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(s.id)}`);
  await openShell(page);
  await openTile(page, 'apps/crawler');
  await fr(page, 'apps/crawler', (f) => f.open('term'));
  const termSel = 'bx-frame[src="apps/crawler"] bx-terminal';
  await waitSel(page, `${termSel} textarea`, { timeout: 20000 });
  const term = page.locator(termSel).first();
  // the terminal's own testApi (bx-frame is at its size budget and stays out of this)
  const api = (fn, arg) => term.evaluate((el, a) => new Function('t', 'arg', `return (${a.fsrc})(t, arg)`)(el.testApi(), a.arg), { fsrc: fn.toString(), arg: arg ?? null });
  const until = async (fn, label, timeout = 10000) => {
    const t0 = Date.now();
    for (;;) {
      if (await api(fn)) return true;
      if (Date.now() - t0 > timeout) { check(false, `timed out waiting for ${label}`); return false; }
      await page.waitForTimeout(40);
    }
  };
  const line = () => api((t) => t.screenLine(t.cursor.row));

  await until((t) => t.echoAck, 'the session frame with echoAck');
  await until((t) => t.rtt != null, 'the first pong');
  const rtt = await api((t) => t.rtt);
  check(typeof rtt === 'number' && rtt >= 0 && rtt < 5000, `the round trip is measured (${rtt} ms)`);
  await until((t) => t.cursor.col > 0, 'a shell prompt');
  await fr(page, 'apps/crawler', (f) => f.focusTerminal());

  // 1. on, echo on: typed text is predicted (pending until the ack) and confirmed
  await api((t) => t.setPredict('on'));
  check(await api((t) => t.predicting), 'mode on: predictions are displayed');
  check(await page.evaluate(() => localStorage.getItem('bx-term-predict')) === 'on', 'the mode is saved in this browser');
  await api((t) => t.holdAcks(true));
  await page.keyboard.type('abc');
  const pend = await api((t) => t.pending);
  check(pend > 0, `typed text is predicted and pending while the ack is held (${pend} cells)`);
  await until((t) => t.screenLine(t.cursor.row).includes('abc'), 'the shell echo');
  await api((t) => t.holdAcks(false));
  await until((t) => t.pending === 0, 'the ack to confirm the predictions');
  check((await line()).includes('abc'), `confirmed text stays on screen (${JSON.stringify(await line())})`);
  await page.keyboard.press('Control+u');

  // 2. on, no echo (`read -s` swallows the keystrokes; the shell's line editor
  // would echo them itself, whatever stty says): the overlay draws the
  // prediction; the ack proves it wrong and withdraws it
  await page.keyboard.type('read -s x');
  await page.keyboard.press('Enter');
  await until((t) => t.pending === 0 && t.cursor.col === 0, 'read -s waiting on a fresh line');
  await api((t) => t.holdAcks(true));
  await page.keyboard.type('xyz');
  await settle(page);
  const ov = await api((t) => t.overlay);
  const cur = await api((t) => t.cursor);
  check(ov.length === 1 && ov[0].text === 'xyz' && ov[0].row === cur.row, `the overlay draws the prediction the shell will not echo (${JSON.stringify(ov)})`);
  check(await api((t) => t.pending) > 0, 'and it is pending');
  await shot(page, 'term-predict', { fullPage: false });
  await api((t) => t.holdAcks(false));
  await until((t) => t.pending === 0, 'the ack to judge the predictions');
  check((await api((t) => t.overlay)).length === 0 && !(await line()).includes('xyz'), `the wrong prediction is withdrawn and the screen untouched (${JSON.stringify(await line())})`);
  await page.keyboard.press('Enter'); // ends the read
  await until((t) => t.pending === 0 && t.cursor.col > 0, 'the prompt after read');

  // 2b. a full-screen program that hides the cursor and echoes into its own
  // field (an Ink app such as Claude Code): the first keystroke teaches the
  // engine where typed text lands, the next is predicted there (D71)
  const tui = "node -e \"const o=process.stdout;process.stdin.setRawMode(true);o.write('\\x1b[2J\\x1b[?25l\\x1b[5;3H> \\x1b[10;1H');let c=5;process.stdin.on('data',d=>{const s=d.toString();if(s=='q'){o.write('\\x1b[?25h\\x1b[2J\\x1b[H');process.exit(0)}o.write('\\x1b[5;'+c+'H'+s+'\\x1b[10;1H');c+=s.length})\"";
  await page.keyboard.type(tui);
  await page.keyboard.press('Enter');
  await until((t) => t.cursorHidden && t.pending === 0, 'the TUI to hide the cursor');
  await api((t) => t.holdAcks(true));
  await page.keyboard.type('a');
  check(await api((t) => t.pending) === 0 && await api((t) => t.anchor) === null, 'hidden cursor: the first keystroke is not predicted (nowhere to put it yet)');
  await until((t) => t.screenLine(4).includes('> a'), 'the TUI to echo a');
  await api((t) => t.holdAcks(false));
  await until((t) => !!t.anchor, 'the anchor to be learned from the echo');
  const anchor = await api((t) => t.anchor);
  check(anchor?.row === 4 && anchor?.col === 5, `the anchor is where the echo landed plus one (${JSON.stringify(anchor)})`);
  await api((t) => t.holdAcks(true));
  await page.keyboard.type('b');
  const ov2 = await api((t) => t.overlay);
  check(ov2.length === 1 && ov2[0].row === 4 && ov2[0].col === 5 && ov2[0].text === 'b', `the next keystroke is predicted at the anchor (${JSON.stringify(ov2)})`);
  await shot(page, 'term-predict-tui', { fullPage: false });
  await api((t) => t.holdAcks(false));
  await until((t) => t.pending === 0, 'the ack to confirm b');
  check((await api((t) => t.screenLine(4))).includes('> ab'), `confirmed in the field (${JSON.stringify(await api((t) => t.screenLine(4)))})`);
  await page.keyboard.type('q');
  await until((t) => !t.cursorHidden && t.cursor.col > 0, 'the TUI to quit and the prompt to return');

  // 3. off: nothing is predicted
  await api((t) => t.setPredict('off'));
  check(!(await api((t) => t.predicting)), 'mode off: nothing displayed');
  await page.keyboard.type('q');
  check(await api((t) => t.pending) === 0, 'mode off: nothing predicted');
  await page.keyboard.press('Control+u');
  await api((t) => t.setPredict('auto'));
  check(await api((t) => t.mode) === 'auto', 'back to auto');

  // 4. the 🔧 menu: the toggle and the RTT line
  await term.locator('.gear').click();
  await settle(page);
  const opts = await term.locator('select.predict option').allTextContents();
  const stat = await term.locator('.pstat').textContent();
  check(opts.length === 3 && /auto/.test(opts[0]) && /100 ms/.test(opts[0]), `the menu offers auto / on / off (${JSON.stringify(opts)})`);
  check(/RTT \d+ ms/.test(stat), `the menu shows the round trip (${JSON.stringify(stat)})`);
  await shot(page, 'term-predict-menu', { fullPage: false });
  await page.keyboard.press('Escape');

  // tidy: end the session so the next pass starts a fresh shell
  const sid = await term.getAttribute('session');
  await fr(page, 'apps/crawler', (f) => f.closeTerminal());
  if (sid) await ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(sid)}`);
  await page.evaluate(() => localStorage.removeItem('bx-term-predict'));
  await ctx.request.delete(`${URL}/api/xbin/prefs/term%3Aapps%3Acrawler`);
  await closeCtx(ctx, page);
  done();
}

module.exports = { predict };
