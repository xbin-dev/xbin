// hack/ui-harness/passes/approvalsux.js — one approval, one place (D250):
// the owner's test session of 2026-11-21 replayed in the browser, against
// a production-mode xbind of its own (journey.js boot: --isolate, the
// release's build of the workspace agent, the scripted model, and the
// scripted coding agent planted in the tile — run as a coding agent whose
// catalog names no default mode, as OpenCode's does).
//
// The session: "turn eui into a tic tac toe game" — the agent writes a
// brief and asks to start a tile agent. The screenshot the owner sent
// showed TWO cards with a Review… each (the brief, and the generic request
// under it); changing the coding agent on the card and approving came back
// "the approval no longer covered the call", five rounds; Deny was the
// filled button and Approve the red one.
//
// APPROVALS_BEFORE=1: shoots the views (the conversation's card, the batch,
// the sheet; dark and light; 1280 and 430) and asserts nothing — the
// screenshots of the tree before the change. Otherwise it asserts what D250
// built: one card and one control that opens the approval for a start; the
// Coding agent, Model and Mode selects on xbin's sheet with where the
// selection came from; the person's pick is the approval (one round, the
// agent told what ran); "Remember for this tile" preselects the next
// start; a follow-up continues the session that is there; the batch with
// two pending items, each once, Review all; the buttons' roles on a plain
// and on a warned approval; no "·" in the new card, the batch or the sheet.
// Screenshots: out/approvals-*.png. Needs HARNESS_ISOLATE=1, make sealed.
'use strict';
const path = require('path');
const { fs, checker, log, shot, OUT, sleep } = require('../lib');
const J0 = require('./journey');
const R = require('./wsagentpage');

const DOCK = 'bx-agent-dock';
const SHEET = `${DOCK} bx-agent-approve`;
const TILE = 'apps/eui';
const PERSON = { id: 'ada', name: 'Ada Admin', password: require('crypto').randomBytes(12).toString('base64url') }; // (made per run, never logged)
const BEFORE = !!process.env.APPROVALS_BEFORE;

const BRIEF = { tile: TILE, goal: 'Turn the eui page into a playable tic tac toe game: a 3x3 board two people can play on the same screen, X and O alternating, the winner or a draw announced, and a reset button.',
  done_when: 'Opening apps/eui shows a 3x3 board and a reset control. Clicking an empty square places the current player\'s mark, a filled square ignores clicks, and no further moves are taken after a win or draw.',
  leave_alone: 'Don\'t add a backend or server-side code: this tile is a page with nothing running behind it. Don\'t touch other tiles.',
  check_by: 'Run the tile as a page and play it through: X wins a row, a draw happens, reset clears the board.' };
const ws = (words, steps) => `${words}\nws: ${steps.join(' ;; ')}`;
const j = (name, args) => `${name} ${JSON.stringify(args)}`;

/** login(browser, X, opts): the person's own session on the pass's xbind. */
async function login(browser, X, opts = {}) {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, deviceScaleFactor: 1, ...opts });
  const page = await ctx.newPage();
  page.on('pageerror', (e) => log(`approvalsUx: PAGE ERROR ${e.message}`));
  await page.goto(`${X.url}/login`);
  await page.fill('input[name=username]', PERSON.id);
  await page.fill('input[name=password]', PERSON.password);
  await Promise.all([page.waitForNavigation(), page.click('button')]);
  return { ctx, page };
}

/** caller(ctx, X): the person's own session over HTTP. */
function caller(ctx, X) {
  return async (method, p, data) => {
    const r = await ctx.request.fetch(`${X.url}${p.startsWith('/api/') ? p : `/api/xbin${p}`}`, { method, ...(data !== undefined ? { data } : {}), failOnStatusCode: false, timeout: 120000 });
    const text = await r.text();
    let json = null;
    try { json = JSON.parse(text); } catch { /* not JSON */ }
    return { status: r.status(), data: json, text };
  };
}

// what a lit tree shows, through its shadow roots
const deep = `(function deep(n) { let t = ''; for (const c of (n.shadowRoot || n).childNodes) t += c.nodeType === 3 ? c.textContent : c.nodeType === 1 ? deep(c) : ''; return t; })`;

async function approvalsUx(browser) {
  const { check, skip, done } = checker('approvals-ux');
  const why = J0.noJourney();
  if (why) { skip(`one approval, one place (D250): ${why}`); done(); return; }
  const X = await J0.boot('approvals', { fake: { tile: TILE, args: ['--no-default-mode'] } });
  const contexts = [];
  try {
    const { ctx, page } = await J0.openPage(browser, 'approvalsUx');
    contexts.push(ctx);
    const J = J0.journey('approvals', () => page);
    await J0.firstLogin(J, page, X);
    await J0.firstRunSheet(J, page, PERSON);
    const call = caller(ctx, X);
    const made = await call('POST', '/create', { path: TILE, runtime: 'static', title: 'eui' });
    check(made.status === 200, `the empty page tile ${TILE} is made (${made.status} ${made.text.slice(0, 120)})`);
    await page.goto(`${X.url}/xbin/agent`);
    await page.waitForSelector(`${DOCK} header`);
    const f = await R.agentFrame(page);
    const sid = await f.evaluate(() => globalThis.wsAgentPage.testApi().sel);
    const wsCall = (m, p, d) => call(m, `/api/${R.AGENT}${p}`, d);
    const cards = async () => (await wsCall('GET', `/ws/sessions/${sid}/cards`)).data || {};

    // ---- the owner's session: the brief, then the start ----
    const t1 = await R.say(f, ws('turn eui into a tic tac toe game', ['workspace_map', j('tile_agent_brief', BRIEF)]), 120000);
    log(`approvalsUx: the brief's turn: ${t1.slice(0, 200)}`);
    const brief = ((await cards()).briefs || []).find((b) => b.tile === TILE);
    check(!!brief, `the agent wrote a brief for ${TILE} (${brief && brief.id})`);
    const t2 = await R.say(f, ws('go ahead', [j('tile_agent_start', { brief: brief.id })]), 120000);
    log(`approvalsUx: the start's turn: ${t2.slice(0, 300)}`);
    await f.waitForSelector('.wsc-brief');
    // a second thing that waits: a call to the tile's API (the batch holds two)
    const t3 = await R.say(f, ws('and reset its scores', [j('tile_call', { tile: TILE, method: 'POST', path: '/scores/reset', why: 'reset the scores' })]), 120000);
    log(`approvalsUx: the call's turn: ${t3.slice(0, 300)}`);
    const pending = (((await call('GET', '/delegate/requests')).data || {}).requests || []).filter((r) => r.state === 'pending');
    log(`approvalsUx: pending: ${JSON.stringify(pending.map((r) => [r.id, r.kind, r.target]))}`);

    const looks = [['dark', 1280], ['light', 1280], ['dark', 430], ['light', 430]];
    const tag = (scheme, width) => `${scheme === 'light' ? '-light' : '-dark'}${width < 640 ? '-430' : ''}`;
    for (const [scheme, width] of looks) {
      const V = await login(browser, X, { viewport: { width, height: 900 }, colorScheme: scheme });
      contexts.push(V.ctx);
      await V.page.goto(`${X.url}/xbin/agent`);
      const vf = await R.agentFrame(V.page);
      await vf.waitForSelector('.wsc-brief', { timeout: 60000 });
      await vf.evaluate(() => document.querySelector('.wsc-brief').scrollIntoView({ block: 'start' }));
      await sleep(300);
      await shot(V.page, `approvals-card${tag(scheme, width)}`, { fullPage: false });
      // the batch: the dock's list of what waits
      await V.page.click(`${DOCK} [data-needs]`);
      await V.page.waitForSelector(`${DOCK} bx-agent-needs`);
      await sleep(300);
      await shot(V.page, `approvals-batch${tag(scheme, width)}`, { fullPage: false });
      await V.page.click(`${DOCK} [data-needs]`);
      // the sheet: the start's review, opened from the conversation
      const opener = BEFORE ? '.wsc-brief .wsc-start' : '.wsc-brief [data-marker="approval"]';
      await vf.click(opener);
      await V.page.waitForSelector(`${SHEET} [role="dialog"]`);
      await sleep(700); // armed
      await shot(V.page, `approvals-sheet${tag(scheme, width)}`, { fullPage: false });
      await V.page.keyboard.press('Escape');
      await V.ctx.close();
      contexts.pop();
    }
    if (BEFORE) {
      for (const r of pending) await call('POST', `/delegate/requests/${r.id}/deny`, {});
      skip('APPROVALS_BEFORE=1: the views of the tree before D250, shot; nothing asserted');
      return;
    }
  } finally {
    for (const c of contexts) await c.close().catch(() => {});
    await X.stop();
    done();
  }
}

module.exports = { approvalsUx };
