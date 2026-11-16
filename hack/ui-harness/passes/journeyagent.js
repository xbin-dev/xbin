// hack/ui-harness/passes/journeyagent.js — the owner's UX benchmark through
// the workspace agent (plans/wsagent/journey.md): the same start as
// journeymanual.js — a fresh install, the host's LLM, the first login —
// then the first-run sheet (the account, the vault and the agent turned
// on, in one: D237), ONE prompt — "Make a tool that rolls dice and give it
// to a new assistant I can chat with." — and ONE review: the agent
// proposes the whole of it as a plan (D238), four lines in xbind's words.
// Every interaction is counted the same way (journey.js); a place where
// the agent stops and the person has to do something by hand would be a
// "dead-end" row (there is none left).
//
// The model is scripted (hack/fakeopenai's "ws:" lines, with a "wait" step
// for the review): the plan is the one a model reading the agent's prompt
// and tools makes — look, read the templates (the mcp-server template's
// example tool is roll_dice: nothing to build), plan_propose, wait, then
// check the hook-up itself by reading the assistant's GET /tools.
//
// What the review costs is what this pass counts: Review…, the opt-in for
// admin changes offered on the review (the first time; the model's binding
// is an admin's), the tick of the line with the SPEND warning, Apply, Done.
// The person's own roll in the assistant comes after the agent's "done" and
// is counted apart. Then, not counted, a second plan with a tile agent's
// start (the scripted ACP agent): the start line as the single start's
// sheet draws it, applied with no sign-in step. Screenshots of the review:
// out/journey-shots/plan-*.png. Needs HARNESS_ISOLATE=1, `make sealed`.
'use strict';
const path = require('path');
const { fs, checker, log } = require('../lib');
const J0 = require('./journey');

const DOCK = 'bx-agent-dock';
const DLG = 'bx-agent-changes';
const TA = 'bx-chat-composer textarea';
const ASSISTANT = 'apps/assistant';
const PERSON = { id: 'ada', name: 'Ada Admin', password: require('crypto').randomBytes(12).toString('base64url') }; // (made per run, never logged)

const ws = (words, steps) => `${words}\nws: ${steps.join(' ;; ')}`;
const j = (name, args) => `${name} ${JSON.stringify(args)}`;
// the one prompt, and the plan a model makes of it: it looks, reads the
// templates (the mcp-server template's example tool IS roll_dice: nothing
// to build), proposes ONE plan — the tool tile, the assistant, its two
// slots — waits for the person's review, then checks the hook-up itself:
// the assistant's own /tools (a read)
const SAY1 = 'Make a tool that rolls dice and give it to a new assistant I can chat with.';
const PLAN = { title: 'A dice tool and an assistant that can use it', changes: [
  { kind: 'tile.create', args: { template: 'mcp-server', name: 'Dice' }, why: 'The dice tool: the MCP server template ships roll_dice' },
  { kind: 'tile.create', args: { template: 'agent', name: 'Assistant' }, why: 'The assistant you will chat with' },
  { kind: 'binding.set', args: { component: 'Assistant', slot: 'llm', provider: 'xbin:llm' }, why: 'The assistant needs a language model to answer at all' },
  { kind: 'binding.set', args: { component: 'Assistant', slot: 'mcp', provider: 'Dice' }, why: 'This is what gives the assistant the dice tool' }] };
const PROMPT1 = ws(SAY1, ['workspace_map', 'tile_create', j('plan_propose', PLAN), 'wait your person decided',
  j('tile_status', { tile: ASSISTANT }), j('tile_call', { tile: ASSISTANT, method: 'GET', path: '/tools' })]);
// afterwards, not counted: a plan with a tile agent's start, through the same review
const SAY2 = 'Have Dice\'s own agent add a "count" argument to roll_dice. Use the test agent.';
const START = { title: 'A count argument for roll_dice', changes: [
  { kind: 'agent.start', args: { tile: J0.DICE, harness: 'fake', goal: 'roll_dice takes a count (1 to 20) and rolls that many dice.', done_when: 'tools/call roll_dice {"sides":6,"count":3} returns three rolls and their sum.',
    leave_alone: 'the manifest\'s provides' }, why: 'A coding agent changes the tool' }] };
const PROMPT2 = ws(SAY2, [j('plan_propose', START), 'wait started tile agent']);

async function journeyAgent(browser) {
  const { check, skip, done } = checker('journey-agent');
  const why = J0.noJourney();
  if (why) { skip(`the workspace agent's journey: ${why}`); done(); return; }
  const X = await J0.boot('agent');
  let ctx;
  try {
    const o = await J0.openPage(browser, 'journeyAgent');
    ctx = o.ctx;
    const page = o.page;
    const J = J0.journey('agent', () => page);
    const call = async (method, p, data) => { const r = await ctx.request.fetch(`${X.url}/api/xbin${p}`, { method, ...(data !== undefined ? { data } : {}), failOnStatusCode: false }); return { status: r.status(), data: await r.json().catch(() => null) }; };

    await J0.firstLogin(J, page, X);
    const root = await call('GET', '/delegate/state');
    check(root.status === 403, `the owner token's session has no workspace agent: an agent is a person's (${root.status})`);
    // the first-run sheet (D237): the account, the vault and the agent in one
    const started = await J0.firstRunSheet(J, page, PERSON);
    check(started, 'the first-run sheet starts in the name field');
    const me = await call('GET', '/whoami');
    check(me.data && me.data.kind === 'user' && me.data.id === PERSON.id && me.data.role === 'admin', `the sheet signed this browser in as the new admin account (${me.data && me.data.id})`);
    const sw = await call('GET', '/delegate/workspace');
    check(sw.data && sw.data.enabled === true && sw.data.who && sw.data.who.everyone === false && sw.data.who.entries === 1, `…and turned workspace agents on for this person alone (${JSON.stringify(sw.data && sw.data.who)})`);
    const frame = async () => { const el = await page.waitForSelector(`${DOCK} iframe`, { state: 'attached', timeout: 60000 }); const f = await el.contentFrame(); await f.waitForSelector('.wsp-main[aria-busy="false"]', { timeout: 120000 }); return f; };
    let f = await J.wait('the dock opens by itself; the agent\'s page is up (its backend starts in its sandbox)', async () => { await page.waitForSelector(`${DOCK}[open]`, { timeout: 30000 }); return frame(); });
    let focus = '';
    for (let i = 0; i < 40 && focus !== 'textarea'; i++) {
      focus = await f.evaluate(() => { let a = document.activeElement; while (a && a.shadowRoot && a.shadowRoot.activeElement) a = a.shadowRoot.activeElement; return document.hasFocus() && a ? a.localName : ''; });
      if (focus !== 'textarea') await page.waitForTimeout(250);
    }
    check(focus === 'textarea', `the agent's composer has the keys: the prompt is typed straight in (${focus || 'nothing focused'})`);
    const main = () => f.locator('.wsp-main').innerText();
    const say = async (what, text) => { await J.field(`the agent: ${what}`, () => f.fill(TA, text)); await J.key('the agent: Enter', () => f.press(TA, 'Enter')); };
    // the scripted model's "Done: …" after the person's message `after`
    const doneAfter = async (after, timeout = 120000) => { const h = await f.waitForFunction((a) => { const m = /Done: [^\n]*/.exec(document.querySelector('.wsp-main').innerText.split(a).pop()); return m ? m[0] : null; }, after, { timeout, polling: 250 }); return h.jsonValue(); };
    const dlg = () => page.locator(`${DLG} [role="dialog"]`).innerText().then((t) => t.replace(/\s+/g, ' '));
    const ticksOf = () => page.$$eval(`${DLG} .item`, (els) => els.map((e) => `${e.dataset.n}${e.querySelector('input').checked ? '+' : '-'}`).join(' '));
    const planShot = (name) => page.screenshot({ path: path.join(J0.SHOTS, `plan-${name}.png`) }).catch(() => {});

    J.section('the one prompt');
    await say(`the prompt ("${SAY1}")`, PROMPT1);
    await J.wait('the agent looked and proposes one plan', () => f.waitForSelector('.wsc-req[data-kind="changeset"]', { timeout: 120000 }));
    const filed = await call('GET', '/delegate/requests');
    check(((filed.data && filed.data.requests) || []).length === 0, 'one plan, and not one single request: nothing else waits for approval');

    J.section('the one review');
    await J.click('the plan\'s card: "Review…"', async () => { await f.locator('.wsc-req[data-kind="changeset"] button.primary').last().click(); await page.waitForSelector(`${DLG} [role="dialog"][data-view="review"]`, { timeout: 20000 }); });
    const review = await dlg();
    const lines = await page.$$eval(`${DLG} .item .line > span:first-child`, (els) => els.map((e) => e.textContent.trim()));
    check(/xbin · plan/i.test(review) && /Your agent proposes 4 changes/.test(review) && lines[0] === `Create the tile ${J0.DICE} from the template MCP server (mcp-server) — yours`
      && lines[1] === `Create the tile ${ASSISTANT} from the template AI Agent (agent) — yours` && lines[2] === `Bind ${ASSISTANT}'s llm to xbin:llm` && lines[3] === `Bind ${ASSISTANT}'s mcp to ${J0.DICE}`,
    `xbin's review of the plan: four lines in xbind's words (${JSON.stringify(lines)})`);
    check(!/\[object Object\]/.test(review) && /Before not bound/i.test(review) && /After apps\/dice/i.test(review), 'a binding\'s before and after are drawn in words (G11)');
    check((await ticksOf()) === '1+ 2+ 3- 4+', `the lines that are the person's own start ticked; the model's — an admin's, with its SPEND warning — doesn't (${await ticksOf()})`);
    const notes = await page.$$eval(`${DLG} .item`, (els) => els.map((e) => `${(e.querySelector('[data-admin]') || { dataset: {} }).dataset.admin || '-'}|${(e.querySelector('[data-needs]') || {}).textContent || ''}`));
    check(/^-\|Needed by line 4/.test(notes[0]) && /^-\|Needed by line 3 and 4/.test(notes[1]) && /^admin\|Needs line 2/.test(notes[2]) && /^-\|Needs line 1 and 2/.test(notes[3]),
      `each line says which lines it needs and which need it; the admin's is marked (${JSON.stringify(notes)})`);
    check(await page.$eval(`${DLG} .item[data-n="3"] input`, (e) => e.disabled) && !!(await page.$(`${DLG} [data-optin]`)) && /This plan has 1 admin change\. Let my agent propose admin changes\?/.test(review),
      'the admin line can\'t be ticked yet: the review itself offers "Let my agent propose admin changes" (no trip to Preferences)');
    await planShot('review');
    // unticking a line unticks the lines that need it (and ticking them back is the person's)
    await page.uncheck(`${DLG} .item[data-n="1"] input`);
    check((await ticksOf()) === '1- 2+ 3- 4-', `unticking the dice tile unticks the binding that needs it (${await ticksOf()})`);
    await page.check(`${DLG} .item[data-n="1"] input`); await page.check(`${DLG} .item[data-n="4"] input`);
    await J.click('the review: "Let my agent propose admin changes" (offered here the first time)', async () => {
      await page.waitForSelector(`${DLG} [data-optin] input[aria-disabled="false"]`, { timeout: 20000 });
      await page.click(`${DLG} [data-optin] input`);
      await page.waitForFunction(async () => (await (await fetch('/api/xbin/delegate/state')).json()).adminChanges === true, null, { timeout: 20000, polling: 300 });
      await page.waitForSelector(`${DLG} .item[data-n="3"] input:not([disabled])`, { timeout: 20000 });
    });
    check((await ticksOf()) === '1+ 2+ 3- 4+' && !(await page.$(`${DLG} [data-optin]`)), 'the opt-in ticks nothing by itself');
    await J.click('the review: tick the llm binding (it starts unticked: it spends the workspace\'s quota)', () => page.check(`${DLG} .item[data-n="3"] input`));
    await planShot('review-ticked');
    await J.click('the review: "Apply 4 changes…"', async () => { await page.click(`${DLG} button[data-apply][aria-disabled="false"]`, { timeout: 20000 }); await page.waitForSelector(`${DLG} [role="dialog"]:is([data-view="results"], [data-view="stepup"])`, { timeout: 180000 }); });
    const stepUp = (await page.getAttribute(`${DLG} [role="dialog"]`, 'data-view')) === 'stepup';
    if (stepUp) { // (a sign-in older than ten minutes: a scripted run never is)
      await J.field('the review: the password (a recent sign-in)', () => page.fill(`${DLG} #pw`, PERSON.password));
      await J.click('the review: apply', async () => { await page.click(`${DLG} button[data-apply][aria-disabled="false"]`); await page.waitForSelector(`${DLG} [role="dialog"][data-view="results"]`, { timeout: 180000 }); });
    } else J.hand(2, 'applying an admin\'s line asks for the password when the sign-in is older than ten minutes: one field and one click (a plan of personal lines never asks)');
    const results = await page.$$eval(`${DLG} .item [data-result]`, (els) => els.map((e) => e.dataset.result).join(' '));
    check(results === 'applied applied applied applied' && /All 4 changes applied/.test(await dlg()), `the plan applied in order (${results})`);
    await planShot('results');
    await J.click('the results: "Done"', () => page.click(`${DLG} button[data-done]`));
    const b = ((await call('GET', '/bindings')).data || {}).bindings || {};
    const owners = (((await call('GET', '/components')).data || {}).components || []).filter((c) => [J0.DICE, ASSISTANT].includes(c.path)).map((c) => `${c.path}:${c.owner || ''}`).join(' ');
    check(!!b[ASSISTANT] && String(b[ASSISTANT].llm) === 'xbin:llm' && String(b[ASSISTANT].mcp) === J0.DICE, `the plan made the tiles and bound the assistant's slots: ${JSON.stringify(b[ASSISTANT])} (${owners})`);
    const end = await J.wait('the agent goes on by itself: it checks the hook-up and reports', () => doneAfter(SAY1, 300000), 'scripted. A real model: a few seconds a turn; the assistant\'s backend builds first (about a minute on a cold cache)');
    log(`journeyAgent: the turn told the model: ${end.slice(0, 900)}`);
    const tools = await call('GET', `/../${ASSISTANT}/tools`).catch(() => ({ status: 0 }));
    const listed = /roll_dice/.test(await main()) || /roll_dice/.test(JSON.stringify(tools.data || ''));
    check(/GET \/tools → 200/.test(end) && listed, `the agent verified the hook-up itself, with no further approval: the assistant's GET /tools lists roll_dice (${end.slice(-160)})`);
    const done1 = J.totals();
    check(done1.deadEnds === 0, `from the login URL to the agent's "done": ${done1.interactions} interactions, one prompt, one review, no dead end`);

    J.section('the chat (the person\'s own try: no longer part of "done")');
    await J.click('top bar: the workspace agent chip (closes the dock: it covers the tile)', () => page.locator('button.chip.agent').click());
    await J0.openFromSidebar(J, page, ASSISTANT);
    const pop = page.locator(`.pqpop[data-prompts-for="${ASSISTANT}"]`);
    await pop.waitFor({ timeout: 5000 }).catch(() => {});
    if (await pop.count()) await J.click('the popover: fold it (it covers the chat)', () => pop.locator('.pqx').click());
    const said = await J0.askAssistant(J, page, ASSISTANT);
    const body = await (await J0.tileFrame(page, ASSISTANT)).locator('body').innerText();
    check(J0.ROLLED.test(said) && /roll_dice · apps\/dice/.test(body), `the journey's end: the assistant called roll_dice on ${J0.DICE} and the answer shows the roll (${said})`);

    const t = J.write();
    check(t.interactions > 0 && t.deadEnds === 0, `the workspace agent's journey: ${t.interactions} interactions (${t.nav} navigation, ${t.click} clicks, ${t.field} fields, ${t.key} keys), +${t.hand} by hand, ${t.know} things to know, ${t.deadEnds} dead ends, 1 prompt, 1 review — ${done1.interactions} to the agent's "done"`);
    log(`journeyAgent: ${JSON.stringify({ ...t, toDone: done1.interactions })}`);

    // ---- not counted: a tile agent's start is a line of a plan too, drawn as the single start's sheet draws it ----
    J0.plant(X.ws); // (out of band: the scripted coding agent, inside the dice tile)
    await page.locator('button.chip.agent').click();
    f = await frame();
    await f.fill(TA, PROMPT2); await f.press(TA, 'Enter');
    await f.waitForFunction(() => document.querySelectorAll('.wsc-req[data-kind="changeset"][data-state="proposed"], .wsc-req[data-kind="changeset"] button.primary').length > 0 && /A count argument|a plan of 1 change/.test(document.querySelector('.wsp-main').innerText), null, { timeout: 120000 });
    await f.locator('.wsc-req[data-kind="changeset"] button.primary').last().click();
    await page.waitForSelector(`${DLG} [role="dialog"][data-view="review"] .item[data-kind="agent.start"] [data-start]`, { timeout: 20000 });
    const sr = await dlg();
    check(/Start a tile agent in apps\/dice — Fake agent \(tests\), in its mode/.test(sr) && /ASKS BEFORE ACTING/i.test(sr) && /Coding agent Fake agent \(tests\)/.test(sr) && sr.includes(START.changes[0].args.done_when) && /sent exactly like this/.test(sr)
      && (await ticksOf()) === '1+' && !(await page.$(`${DLG} [data-optin]`)),
    `a start as a plan's line: the coding agent, the mode xbind vouches for, the brief as sent — a personal line, ticked, no opt-in asked (${sr.slice(0, 200)})`);
    await planShot('start');
    await page.click(`${DLG} button[data-apply][aria-disabled="false"]`, { timeout: 20000 });
    await page.waitForSelector(`${DLG} [role="dialog"][data-view="results"]`, { timeout: 120000 });
    check((await page.getAttribute(`${DLG} .item [data-result]`, 'data-result')) === 'applied', `the start applied with no sign-in step: a plan of personal lines asks for none (${(await dlg()).slice(0, 160)})`);
    await planShot('start-results');
    await page.click(`${DLG} button[data-done]`);
    const sess = [].concat((await call('GET', '/term/sessions?origin=agent&compact=1')).data || []).filter((x) => x && x.cwd === J0.DICE);
    check(sess.length === 1 && sess[0].provider === 'fake', `the tile agent runs in ${J0.DICE}, started by the plan (${JSON.stringify(sess.map((x) => `${x.provider}:${x.status}`))})`);
    await f.waitForFunction(() => /tile agent #1|Fake agent/.test(document.querySelector('.wsp-main').innerText), null, { timeout: 60000 }).catch(() => {});
    check(/started tile agent #1 in apps\/dice/.test(await doneAfter(SAY2, 120000).catch(() => '')) || /Fake agent/.test(await main()), 'the conversation follows the tile agent its plan started');
  } finally {
    if (ctx) await ctx.close().catch(() => {});
    await X.stop();
  }
  done();
}

module.exports = { journeyAgent };
