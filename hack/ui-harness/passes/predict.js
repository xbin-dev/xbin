// hack/ui-harness/passes/predict.js — the terminal's predictive echo (D70):
// this xbind acks input and answers pings, typed text is predicted and
// confirmed, a wrong prediction is withdrawn, the 🔧 menu has the toggle.
// A local PTY echoes within a millisecond, so the pass holds the acks to see
// a prediction pending, and types into `read -s` to see one drawn.
const { URL, login, closeCtx, settle, fr, waitSel, openShell, openTile, shot, checker } = require('../lib');

async function predict(browser) {
  const { check, done } = checker('predict');
  const { ctx, page } = await login(browser, 'admin', 'admin');
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
  await page.evaluate(() => { localStorage.removeItem('bx-term-predict'); localStorage.removeItem('bx-term:apps/crawler'); });
  await closeCtx(ctx, page);
  done();
}

module.exports = { predict };
