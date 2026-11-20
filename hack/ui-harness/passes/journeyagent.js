// hack/ui-harness/passes/journeyagent.js — the owner's UX benchmark through
// the workspace agent (plans/wsagent/journey.md): the same start as
// journeymanual.js — a fresh install, the host's LLM, the first login —
// then the first-run sheet (the account, the vault and the agent turned
// on, in one: D237), ONE prompt — "Make a tool that rolls dice and give it
// to a new assistant I can chat with." — and ONE review: the agent
// proposes the whole of it as a plan (D238, D243), five lines in xbind's words.
// Every interaction is counted the same way (journey.js); a place where
// the agent stops and the person has to do something by hand would be a
// "dead-end" row (there is none left).
//
// The model is scripted (hack/fakeopenai's "ws:" lines, with "wait" steps
// for the review and for the tile agent's report): the plan is the one a
// model reading the agent's prompt and tools makes now that the tool is
// BUILT (D243) — an empty tile, its tile agent with the mcp-tool brief, the
// assistant, its model, and its mcp slot as a line that waits. The coding
// agent is scripted too (hack/fakeacp, journey.js BUILD): it writes the MCP
// server from /docs/mcp.md inside its sandbox and asks before each command.
//
// What this pass counts: Review…, the tick of the model's binding (an
// admin's line with a SPEND warning), Apply — the results show in place and
// the review closes itself —, then the tile agent's permission request
// answered in the dock's Needs you (open, "Allow always", close). xbind
// binds the assistant by itself when the tile provides mcp; the agent
// checks and says done. The person's own roll comes after and is counted
// apart. Screenshots: out/journey-shots/plan-*.png. Needs
// HARNESS_ISOLATE=1, `make sealed`.
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
// the one prompt, and the plan a model makes of it (D243: the tool is BUILT —
// there is no template to stamp an MCP tile from): it looks, reads the
// templates, and proposes ONE plan — an empty tile, that tile's coding agent
// with the mcp-tool brief, the assistant, its model, and its mcp slot bound
// to the tool's tile, a line that waits for what the coding agent writes. It
// waits for the review, then for the tile agent's report, and checks the
// hook-up itself: the tile agent's result and the assistant's own /tools (a
// read) — which also shows the waiting line was applied.
// (harness "fake": the scripted coding agent stands in for the one a
// person's preferences and the connector resolve — Codex or OpenCode with
// no sign-in. Its script rides in the brief's own words.)
const SAY1 = 'Make a tool that rolls dice and give it to a new assistant I can chat with.';
const PLAN = { title: 'A dice tool and an assistant that can use it', changes: [
  { kind: 'tile.create', args: { template: 'starter', name: 'Dice' }, why: 'The dice tool lives in a tile of its own' },
  { kind: 'agent.start', args: { tile: 'Dice', template: 'mcp-tool', harness: 'fake', goal: J0.BUILD, tool: { name: 'roll_dice', arguments: 'sides: integer, default 6; count: integer, default 1', result: 'each roll and their sum' } },
    why: 'The tile\'s own coding agent builds the tool' },
  { kind: 'tile.create', args: { template: 'agent', name: 'Assistant' }, why: 'The assistant you will chat with' },
  { kind: 'binding.set', args: { component: 'Assistant', slot: 'llm', provider: 'xbin:llm' }, why: 'The assistant needs a language model to answer at all' },
  { kind: 'binding.set', args: { component: 'Assistant', slot: 'mcp', provider: 'Dice' }, why: 'This is what gives the assistant the dice tool, once it is built' }] };
const PROMPT1 = ws(SAY1, ['workspace_map', 'tile_create', j('plan_propose', PLAN), 'wait your person decided', 'wait ended its turn',
  j('tile_agent_result', { agent: '#1' }), j('tile_status', { tile: ASSISTANT }), j('tile_call', { tile: ASSISTANT, method: 'GET', path: '/tools' })]);

async function journeyAgent(browser) {
  const { check, skip, done } = checker('journey-agent');
  const why = J0.noJourney();
  if (why) { skip(`the workspace agent's journey: ${why}`); done(); return; }
  const X = await J0.boot('agent', { plant: true });
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
    const tpl = await call('GET', '/templates');
    check(!/mcp-server/.test(JSON.stringify(tpl.data || '')), 'there is no MCP template to stamp: the tool is built');

    J.section('the one review');
    await J.click('the plan\'s card: "Review…"', async () => { await f.locator('.wsc-req[data-kind="changeset"] [data-marker]').last().click(); await page.waitForSelector(`${DLG} [role="dialog"][data-view="review"]`, { timeout: 20000 }); });
    const review = await dlg();
    const lines = await page.$$eval(`${DLG} .item .line > span:first-child`, (els) => els.map((e) => e.textContent.trim()));
    check(/xbin · plan/i.test(review) && /Your agent proposes 5 changes/.test(review) && lines[0] === `Create the tile ${J0.DICE} from the template Starter (starter) — yours`
      && /^Start a tile agent in apps\/dice — Fake agent \(tests\), in its mode/.test(lines[1]) && lines[2] === `Create the tile ${ASSISTANT} from the template AI Agent (agent) — yours`
      && lines[3] === `Bind ${ASSISTANT}'s llm to xbin:llm` && lines[4] === `Bind ${ASSISTANT}'s mcp to ${J0.DICE}`,
    `xbin's review of the plan: five lines in xbind's words (${JSON.stringify(lines)})`);
    check(!/\[object Object\]/.test(review) && /Before not bound/i.test(review), 'a binding\'s before and after are drawn in words (G11)');
    check((await ticksOf()) === '1+ 2+ 3+ 4- 5+', `the person's own lines start ticked — the line that waits among them; the model's (an admin's, with its SPEND warning) doesn't (${await ticksOf()})`);
    const notes = await page.$$eval(`${DLG} .item`, (els) => els.map((e) => `${(e.querySelector('[data-admin]') || { dataset: {} }).dataset.admin || '-'}|${(e.querySelector('[data-needs]') || {}).textContent || ''}`));
    check(/^-\|Needed by line 2 and 5/.test(notes[0]) && /^-\|Needs line 1.*Needed by line 5/.test(notes[1]) && /^-\|Needed by line 4 and 5/.test(notes[2]) && /^admin\|Needs line 3/.test(notes[3]) && /^-\|Needs line 1, 2 and 3/.test(notes[4]),
      `each line says which lines it needs and which need it; the admin's is marked (${JSON.stringify(notes)})`);
    // the start line: the coding agent, a mode xbind vouches for, the brief as it will be sent — which tells an empty tile's agent what to write
    const sr = (await page.locator(`${DLG} .item[data-n="2"]`).innerText()).replace(/\s+/g, ' ');
    check(/ASKS BEFORE IT EDITS OR RUNS ANYTHING/i.test(sr) && /Coding agent Fake agent \(tests\)/.test(sr) && /sent exactly like this/.test(sr) && /Done when: this tile's xbin\.json declares "provides": \{"mcp"/.test(sr),
      `the start line: the coding agent, the mode, the brief "sent exactly like this" (${sr.slice(0, 160)})`);
    // the line that waits, in xbind's words
    const wl = (await page.locator(`${DLG} .item[data-n="5"]`).innerText()).replace(/\s+/g, ' ');
    check(/applies once apps\/dice provides mcp; nothing is bound until then/.test(wl) && /waits at most 24 hours/.test(wl) && /After apps\/dice, once it provides mcp/i.test(wl) && !(await page.$eval(`${DLG} .item[data-n="5"] input`, (e) => e.disabled)),
      `the binding to a tile that provides nothing yet is a line that waits, and says so (${wl.slice(0, 200)})`);
    check(!(await page.$eval(`${DLG} .item[data-n="4"] input`, (e) => e.disabled)) && !(await page.$(`${DLG} [data-optin]`))
      && /An admin change — applied as you\./.test(await page.$eval(`${DLG} .item[data-n="4"] [data-why]`, (e) => e.textContent)),
    'the admin line is the admin\'s to tick, "applied as you": no switch to turn on first');
    await planShot('review');
    // unticking a line unticks the lines that need it (and ticking them back is the person's)
    await page.uncheck(`${DLG} .item[data-n="1"] input`);
    check((await ticksOf()) === '1- 2- 3+ 4- 5-', `unticking the dice tile unticks its tile agent and the binding that waits for it (${await ticksOf()})`);
    await page.check(`${DLG} .item[data-n="1"] input`); await page.check(`${DLG} .item[data-n="2"] input`); await page.check(`${DLG} .item[data-n="5"] input`);
    await J.click('the review: tick the llm binding (it starts unticked: an admin\'s line, and it spends the workspace\'s quota)', () => page.check(`${DLG} .item[data-n="4"] input`));
    await planShot('review-ticked');
    await J.click('the review: "Apply 5 changes…"', async () => { await page.click(`${DLG} button[data-apply][aria-disabled="false"]`, { timeout: 20000 }); await page.waitForSelector(`${DLG} [role="dialog"]:is([data-view="results"], [data-view="stepup"])`, { timeout: 180000 }); });
    const stepUp = (await page.getAttribute(`${DLG} [role="dialog"]`, 'data-view')) === 'stepup';
    if (stepUp) { // (a sign-in older than ten minutes: a scripted run never is)
      await J.field('the review: the password (a recent sign-in)', () => page.fill(`${DLG} #pw`, PERSON.password));
      await J.click('the review: apply', async () => { await page.click(`${DLG} button[data-apply][aria-disabled="false"]`); await page.waitForSelector(`${DLG} [role="dialog"][data-view="results"]`, { timeout: 180000 }); });
    } else J.hand(2, 'applying an admin\'s line asks for the password when the sign-in is older than ten minutes: one field and one click (a plan of personal lines never asks)');
    // the results show in place and the review closes itself: four applied, one waiting — nothing to press
    const shown = await page.evaluate(() => { const r = document.querySelector('bx-agent-changes').shadowRoot; return { results: [...r.querySelectorAll('.item [data-result]')].map((e) => e.dataset.result).join(' '), text: r.querySelector('[role="dialog"]').innerText.replace(/\s+/g, ' '), done: !!r.querySelector('button[data-done]'), closing: !!r.querySelector('[data-closing]') }; });
    check(shown.results === 'applied applied applied applied waiting' && /All 4 changes applied/.test(shown.text) && /1 line waits: xbin applies it by itself/.test(shown.text) && shown.closing && !shown.done,
      `the plan applied in order; its last line waits; the results ask for no click (${shown.results})`);
    await planShot('results');
    await J.wait('the review closes itself into the plan\'s card', () => page.waitForFunction(() => !document.querySelector('bx-agent-changes'), null, { timeout: 10000 }));
    await f.waitForSelector('.wsc-req[data-kind="changeset"] [data-lines] li[data-result]', { timeout: 30000 });
    const kept = () => f.$$eval('.wsc-req[data-kind="changeset"] [data-lines] li', (els) => els.map((e) => `${e.dataset.n}:${e.dataset.result}`).join(' '));
    const card0 = (await f.locator('.wsc-req[data-kind="changeset"]').last().innerText()).replace(/\s+/g, ' ');
    check((await kept()) === '1:applied 2:applied 3:applied 4:applied 5:waiting' && /waits for apps\/dice to provide mcp/.test(card0), `the plan's card keeps each line's outcome, the waiting one with what it waits for (${await kept()})`);
    check(((await call('GET', '/delegate/state')).data || {}).adminChanges !== true, 'applying an admin\'s line of a plan did not turn on "Let my agent propose admin changes"');
    const b0 = ((await call('GET', '/bindings')).data || {}).bindings || {};
    check(!!b0[ASSISTANT] && String(b0[ASSISTANT].llm) === 'xbin:llm' && !b0[ASSISTANT].mcp, `the tiles are made and the model bound; the mcp slot is NOT bound yet: ${JSON.stringify(b0[ASSISTANT])}`);

    const sessionsOf = async () => [].concat((await call('GET', '/term/sessions?origin=agent&compact=1')).data || []).filter((x) => x && x.cwd === J0.DICE);
    J.section('the tile agent\'s permissions');
    // the coding agent asks before its first command: answered where the person is — the dock's Needs you — never in the agent's page
    await J.wait('the tile agent asks for a permission', () => page.waitForFunction(async () => { const r = await fetch('/api/xbin/term/sessions?origin=agent&compact=1'); return [].concat(await r.json()).some((s) => s && (s.pending > 0 || s.status === 'waiting_permission')); }, null, { timeout: 120000, polling: 500 }));
    await J.click('the dock: "Needs you"', async () => { await page.click(`${DOCK} button[data-needs]`); await page.waitForSelector(`${DOCK} bx-agent-permits .bxc-perm button.bxc-primary`, { timeout: 30000 }); });
    const perm = (await page.locator(`${DOCK} bx-agent-permits .bxc-perm`).first().innerText()).replace(/\s+/g, ' ');
    const offered = await page.locator(`${DOCK} bx-agent-permits .bxc-btns button`).allInnerTexts();
    check(/curl -fsS/.test(perm) && /docs\/mcp\.md/.test(perm) && /asks to run a command/.test(perm) && offered.join('|') === 'Reject|Allow always|Allow once',
      `the permission is drawn by xbin in the dock, as the tile's window draws it: the command exactly, and its choices — the rejection left, the primary allow right (D250) (${offered.join(', ')}; ${perm.slice(0, 120)})`);
    check((await f.locator('bx-agent-permits, .bxc-perm').count()) === 0, 'the agent\'s page draws no permission card: it can\'t answer for the person');
    const waitingRow = (await page.locator(`${DOCK} bx-agent-needs li[data-kind="changeset"]`).first().innerText().catch(() => '')).replace(/\s+/g, ' ');
    check(/Waiting: Bind apps\/assistant's mcp to apps\/dice/.test(waitingRow) && /xbin applies it once apps\/dice provides mcp\s+lapses in 23 h/.test(waitingRow) && !waitingRow.includes('·'), `Needs you also lists the line that waits (${waitingRow})`);
    await page.screenshot({ path: path.join(J0.SHOTS, 'plan-permission.png') }).catch(() => {});
    // the card is armed like every control of xbin's that allows something (review finding 3): the agent's page can
    // open the tray when it likes and the coding agent it briefed decides when a request appears, so a click that
    // was on its way elsewhere must not land on "Allow". Not a script's click, not Enter, not before it is armed —
    // and a page that opens the tray again finds the card unarmed again.
    const pendingNow = async () => ([].concat((await call('GET', '/term/sessions?origin=agent&compact=1')).data || []).find((x) => x && x.cwd === J0.DICE) || {}).pending || 0;
    const allowBtn = `${DOCK} bx-agent-permits .bxc-perm button[data-kind="allow_always"]`;
    await page.locator(allowBtn).first().evaluate((b) => b.click());
    await page.locator(`${DOCK} bx-agent-permits .bxc-perm button.bxc-primary`).first().evaluate((b) => b.click());
    await page.waitForTimeout(600);
    check((await pendingNow()) === 1, 'a script\'s click on "Allow once" or "Allow always" answers nothing');
    await page.waitForSelector(`${DOCK} bx-agent-permits[armed]`, { timeout: 5000 }).catch(() => {});
    await page.locator(allowBtn).first().focus();
    await page.keyboard.press('Enter');
    await page.waitForTimeout(600);
    check((await pendingNow()) === 1, 'Enter on a focused "Allow always" answers nothing');
    // the agent's page asks the dock to open the session (what its card's Answer does): the tray is open already — the card is unarmed again
    const sid = (await sessionsOf())[0].id;
    await f.evaluate((id) => window.parent.postMessage({ type: 'xbin:open-session', tile: 'apps/dice', session: id }, '*'), sid);
    const unarmed = await page.waitForSelector(`${DOCK} bx-agent-permits:not([armed])`, { timeout: 3000 }).then(() => true, () => false);
    check(unarmed, 'a message from the agent\'s page finds no armed card: it waits again');
    await page.click(allowBtn, { timeout: 2000, force: true }).catch(() => {}); // a real click, at once: not yet
    await page.waitForTimeout(300);
    check((await pendingNow()) === 1, 'a click before the card is armed answers nothing');
    await page.waitForSelector(`${DOCK} bx-agent-permits[armed]`, { timeout: 5000 }).catch(() => {});
    // the page asks for the sign-in card (D230): the chrome draws it for the coding agent the page named and takes nothing else
    // of the message — no field filled, no tile, no saved sign-in picked — and the permission card beside it is unarmed again
    await f.evaluate(() => window.parent.postMessage({ type: 'xbin:agent-open-signin', harness: 'claude', component: 'apps/dice', sid: 'sg-1', view: 'manage',
      spec: { command: 'x' }, code: 'ABCD-1234', key: 'not-a-key', name: 'pre-filled', value: 'pre-filled' }, '*'));
    const cardUp = await page.waitForSelector(`${DOCK} [data-signin] bx-agent-signin`, { timeout: 3000 }).then(() => true, () => false);
    check(cardUp, 'the page\'s message opens the chrome\'s sign-in card');
    const unarmedBySignin = await page.waitForSelector(`${DOCK} bx-agent-permits:not([armed])`, { timeout: 3000 }).then(() => true, () => false);
    check(unarmedBySignin, 'the sign-in card opening leaves no permission card armed');
    if (cardUp) {
      const card = await page.locator(`${DOCK} [data-signin] bx-agent-signin`).evaluate((el) => ({
        harness: el.harness || '', component: el.component || '', sid: el.sid || '', view: el.view || '', spec: el.spec == null,
        filled: [...el.shadowRoot.querySelectorAll('input:not([readonly]), textarea')].map((i) => i.value).filter(Boolean) }));
      check(card.harness === 'claude' && !card.component && !card.sid && !card.view && card.spec && card.filled.length === 0,
        `the sign-in card holds the coding agent's id and nothing else of the page's message (${JSON.stringify(card)})`);
      await page.click(`${DOCK} [data-signin-close]`, { timeout: 3000 }).catch(() => {});
    }
    check((await pendingNow()) === 1, 'nothing was answered meanwhile');
    await page.waitForSelector(`${DOCK} bx-agent-permits[armed]`, { timeout: 5000 }).catch(() => {});
    // it asks before each command; every answer is the person's click, right here. "Allow always" is the fewest:
    // the scripted agent then stops asking for the rest of its turn (answered one by one it is four clicks)
    let answers = 0;
    const sessionsNow = sessionsOf;
    for (let i = 0; i < 1200; i++) {
      const s = (await sessionsNow())[0];
      if (s && s.status === 'idle' && !(s.pending > 0)) break;
      const card = page.locator(`${DOCK} bx-agent-permits .bxc-perm`).first();
      const always = card.locator('button[data-kind="allow_always"]:not([disabled])'), once = card.locator('button.bxc-primary:not([disabled])');
      const btn = (await always.count()) ? always.first() : (await once.count()) ? once.first() : null;
      if (!btn) { await page.waitForTimeout(250); continue; }
      const label = (await btn.innerText()).trim();
      const cmd = (await card.locator('.bxc-cmd').first().innerText().catch(() => '')).replace(/\s+/g, ' ').slice(0, 60);
      const pid = await card.getAttribute('data-pid');
      await page.waitForSelector(`${DOCK} bx-agent-permits[armed]`, { timeout: 5000 });
      await J.click(`Needs you: "${label}" (${cmd}…)`, async () => { await btn.click(); await page.locator(`${DOCK} bx-agent-permits .bxc-perm[data-pid="${pid}"]`).waitFor({ state: 'detached', timeout: 20000 }); });
      answers++;
    }
    check(answers === 1, `the person answered the coding agent's permission request in the dock (${answers} answer(s): "Allow always")`);
    J.hand(1, 'a real coding agent asks for edits and for commands separately, and "always" holds for one kind: about one more answer (the scripted one runs four commands — the server from the docs, go.mod, the manifest, the check — and takes "always" for all)');

    J.section('built, bound, checked');
    const end = await J.wait('the tile agent builds the MCP server from /docs/mcp.md; xbin binds the assistant by itself; the agent checks and reports', () => doneAfter(SAY1, 600000),
      'scripted. A real coding agent on this task: one to three minutes; the assistant\'s backend builds first (about a minute on a cold cache)');
    log(`journeyAgent: the turn told the model: ${end.slice(0, 900)}`);
    const b = ((await call('GET', '/bindings')).data || {}).bindings || {};
    const owners = (((await call('GET', '/components')).data || {}).components || []).filter((c) => [J0.DICE, ASSISTANT].includes(c.path)).map((c) => `${c.path}:${c.owner || ''}`).join(' ');
    check(!!b[ASSISTANT] && String(b[ASSISTANT].mcp) === J0.DICE, `xbin applied the waiting line by itself once the tile agent declared the provide: ${JSON.stringify(b[ASSISTANT])} (${owners})`);
    const served = await J0.servesDice(ctx, X);
    check(served.ok, `the tile the coding agent wrote serves MCP: tools/list names roll_dice (${served.status})`);
    const tools = await call('GET', `/../${ASSISTANT}/tools`).catch(() => ({ status: 0 }));
    const listed = /roll_dice/.test(end) || /roll_dice/.test(JSON.stringify(tools.data || ''));
    check(/steps done: tools\/list names roll_dice/.test(end) && /GET \/tools → 200/.test(end) && listed, `the agent verified before "done", with no further approval: the tile agent's result, then the assistant's GET /tools naming roll_dice (${end.slice(-200)})`);
    await f.waitForFunction(() => [...document.querySelectorAll('.wsc-req[data-kind="changeset"] [data-lines] li')].map((e) => e.dataset.result).join(' ') === 'applied applied applied applied applied', null, { timeout: 60000 }).catch(() => {});
    check((await kept()) === '1:applied 2:applied 3:applied 4:applied 5:applied', `the plan's card says the waiting line was applied (${await kept()})`);
    await page.screenshot({ path: path.join(J0.SHOTS, 'plan-card.png') }).catch(() => {});
    const audit = ((await call('GET', '/delegate/audit')).data || {}).calls || [];
    check(audit.filter((r) => r.under === 'waited' && r.status === 200 && r.class === 'changeset:binding.set').length === 1, 'the log says the waiting line was applied, once, as a line of the plan');
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
    check(J0.ROLLED.test(said) && /roll_dice · apps\/dice/.test(body), `the journey's end: the assistant called roll_dice on ${J0.DICE} — a tool its coding agent wrote — and the answer shows the roll (${said})`);

    const t = J.write();
    check(t.interactions > 0 && t.deadEnds === 0, `the workspace agent's journey: ${t.interactions} interactions (${t.nav} navigation, ${t.click} clicks, ${t.field} fields, ${t.key} keys), +${t.hand} by hand, ${t.know} things to know, ${t.deadEnds} dead ends, 1 prompt, 1 review — ${done1.interactions} to the agent's "done"`);
    log(`journeyAgent: ${JSON.stringify({ ...t, toDone: done1.interactions })}`);
  } finally {
    if (ctx) await ctx.close().catch(() => {});
    await X.stop();
  }
  done();
}

module.exports = { journeyAgent };
