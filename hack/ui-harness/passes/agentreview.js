// hack/ui-harness/passes/agentreview.js — a turn's changes reviewed a file at
// a time (D210, #45). The fake agent's `run:` writes 30 files of 300 lines
// and one of 3000 — far past the turn's inline patch (192 KiB) — and the
// checks: (a) the turn's card is folded; opened, it lists the files and
// renders no diff; (b) a file the inline patch holds opens from it, with no
// request; (c) the file past the cap loads from the full-diff route, shows
// its first 400 lines and "Show all N lines", which shows the rest; (d) the
// shell call's own card reviews the same way (its change set by
// toolCallId). Soft: how long opening the card and a file took.
const { URL, login, closeCtx, fr, waitFor, openShell, usePersonalScreen, openTile, shotEl, checker, log, sleep } = require('../lib');

const TILE = 'apps/crawler';
const VIEW = { viewport: { width: 1400, height: 1300 } }; // agenttab.js: the window must fit
const GEN = 'review-gen';
const CMD = `run: mkdir -p ${GEN} && for i in $(seq -w 1 30); do seq -f "line %g of file $i: the quick brown fox jumps over the lazy dog" 1 300 > ${GEN}/f$i.txt; done; seq -f "big line %g: the quick brown fox jumps over the lazy dog" 1 3000 > ${GEN}/zz-big.txt; echo wrote`;

async function agentReview(browser) {
  const { check, done } = checker('agent-review');
  const A = await login(browser, 'admin', 'admin', VIEW);
  const page = A.page;
  const sessions = async () => (await A.ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json();
  for (const s of await sessions()) await A.ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(s.id)}`);
  await openShell(page);
  await usePersonalScreen(page);
  await openTile(page, TILE);
  await page.locator(`.card[data-path="${TILE}"]`).evaluate((el) => el.scrollIntoView({ block: 'start' }));
  await fr(page, TILE, (f) => { f.open('term'); f.setPop({ x: 40, y: 60, w: 1000, h: 900 }); }); // room to look at a review
  await fr(page, TILE, (f) => f.startKind('agent', 'fake'));
  await waitFor(page, (t) => { const api = t.frameFor('apps/crawler')?.testApi(); const g = api && api.agent(api.tabs.length - 1); return !!g && g.status === 'idle'; }, null, { timeout: 15000, label: 'the agent is idle' });
  await fr(page, TILE, (f) => f.setActiveTab(f.tabs.length - 1));
  const ag = (fn, arg) => fr(page, TILE, (f, t, a) => new Function('ag', 'arg', `return (${a.src})(ag, arg)`)(f.agent(), a.arg), { src: fn.toString(), arg: arg ?? null });
  const agentSel = `bx-frame[src="${TILE}"] bx-agent`;

  // ---- the turn: 31 files, ~13 000 lines ----
  await ag((a, cmd) => a.send(cmd), CMD);
  await waitFor(page, (t) => (t.frameFor('apps/crawler')?.testApi().agent()?.blocks || []).some((b) => b.kind === 'changes' && b.files.length === 31), null, { timeout: 60000, label: "the turn's 31 changed files" });
  await waitFor(page, (t) => t.frameFor('apps/crawler')?.testApi().agent()?.status === 'idle', null, { timeout: 15000, label: 'idle after the turn' });
  await sleep(300);

  // (a) folded; opened: the files, no diff
  const card = page.locator(`${agentSel} .turn-changes`).last();
  const files = card.locator('details.files');
  check(!(await files.evaluate((el) => el.open)), 'the turn\'s changes are folded');
  check(/This turn changed 31 files \+12000/.test(await files.locator(':scope > summary').innerText()), `its summary counts them (${await files.locator(':scope > summary').innerText()})`);
  const t0 = Date.now();
  await files.locator(':scope > summary').click();
  await card.locator('details.frow').first().waitFor({ timeout: 5000 });
  log(`agent-review: the 31-file card opened in ${Date.now() - t0} ms`);
  check(await card.locator('details.frow').count() === 31, `opened, it lists the files (${await card.locator('details.frow').count()})`);
  check(await card.locator('pre.diff').count() === 0, 'opened, it renders no diff');
  await files.evaluate((el) => el.scrollIntoView({ block: 'start' }));
  await sleep(200);
  await shotEl(page, `bx-frame[src="${TILE}"] .pop`, 'agent-review-list');

  // (b) a file the inline patch holds: no request
  const row = (name) => card.locator('details.frow', { has: page.locator(`.fp[title="${GEN}/${name}"]`) });
  await row('f01.txt').locator(':scope > summary').click();
  await row('f01.txt').locator('pre.diff').waitFor({ timeout: 5000 });
  check((await ag((a) => a.diffLoads)).length === 0, 'a file the turn\'s patch holds opens from it (no request)');
  check(await row('f01.txt').locator('pre.diff > span').count() === 301, `its whole diff shows (its hunk: the row names the file) (${await row('f01.txt').locator('pre.diff > span').count()} lines)`);
  await row('f01.txt').locator(':scope > summary').click(); // fold it again

  // (c) past the cap: the route; 400 lines, then all of them
  const t1 = Date.now();
  await row('zz-big.txt').locator(':scope > summary').click();
  await row('zz-big.txt').locator('pre.diff').waitFor({ timeout: 10000 });
  log(`agent-review: the 3000-line file opened in ${Date.now() - t1} ms`);
  const loads = await ag((a) => a.diffLoads);
  check(loads.length === 1 && /\/diff\?turn=\d+&path=review-gen%2Fzz-big\.txt$/.test(loads[0]), `the file past the patch's cap loads from the route (${JSON.stringify(loads)})`);
  check(await row('zz-big.txt').locator('pre.diff > span').count() === 400, `it shows its first 400 lines (${await row('zz-big.txt').locator('pre.diff > span').count()})`);
  const more = row('zz-big.txt').locator('button.more');
  check(/Show all 3001 lines/.test(await more.innerText().catch(() => '')), `"Show all" counts the rest (${await more.innerText().catch(() => 'none')})`);
  await row('zz-big.txt').evaluate((el) => el.scrollIntoView({ block: 'start' })); // (its summary sticks: scroll to the row)
  await sleep(200);
  await shotEl(page, `bx-frame[src="${TILE}"] .pop`, 'agent-review-turn');
  await more.click();
  await row('zz-big.txt').locator('button.more').waitFor({ state: 'detached', timeout: 5000 }).catch(() => {});
  check(await row('zz-big.txt').locator('pre.diff > span').count() === 3001 && await row('zz-big.txt').locator('button.more').count() === 0, `Show all shows every line (${await row('zz-big.txt').locator('pre.diff > span').count()})`);

  // (d) the shell call's own card: its change set by toolCallId
  const call = page.locator(`${agentSel} details.tool.exec`).last();
  await call.locator(':scope > summary').click();
  await call.locator('details.files > summary').click();
  await call.locator('details.frow').first().waitFor({ timeout: 5000 });
  check(await call.locator('pre.diff').count() === 0, 'the call\'s files open as a list too');
  const callRow = call.locator('details.frow', { has: page.locator(`.fp[title="${GEN}/f30.txt"]`) });
  await callRow.locator(':scope > summary').click();
  await callRow.locator('pre.diff').waitFor({ timeout: 10000 });
  const loads2 = await ag((a) => a.diffLoads);
  check(loads2.length === 2 && /\/diff\?toolCallId=[^&]+&path=review-gen%2Ff30\.txt$/.test(loads2[1]), `a file past the call's cap loads by its toolCallId (${JSON.stringify(loads2)})`);
  await shotEl(page, `bx-frame[src="${TILE}"] .pop`, 'agent-review-call');

  // ---- cleanup: the files go, the session ends ----
  await ag((a) => a.send(`run: rm -rf review-gen && echo gone`));
  await waitFor(page, (t) => (t.frameFor('apps/crawler')?.testApi().agent()?.blocks || []).some((b) => b.kind === 'msg' && /run: gone/.test(b.text || '')), null, { timeout: 30000, label: 'the files removed' });
  for (const s of await sessions()) await A.ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(s.id)}`);
  await closeCtx(A.ctx, page);
  done();
}

module.exports = { agentReview };
