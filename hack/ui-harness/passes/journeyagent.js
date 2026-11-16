// hack/ui-harness/passes/journeyagent.js — the owner's UX benchmark through
// the workspace agent, as far as today's agent gets (plans/wsagent/
// journey.md): the same start as journeymanual.js — a fresh install, the
// host's LLM, the first login — then the first-run sheet (the account, the
// vault and the agent turned on, in one: D236) and ONE prompt, "Make
// a tool that rolls dice and give it to a new assistant I can chat with."
// Every interaction is counted the same way (journey.js); every place the
// agent stops and the person has to do something by hand is a "dead-end"
// row, and what they then do is counted too.
//
// The model is scripted (hack/fakeopenai's "ws:" lines, with "wait" steps
// for the approvals between them): the plan is the one a model reading the
// agent's tool descriptions would make — look, create a tile, brief and
// start its agent, create the assistant from the agent template, propose
// the two bindings, check. The tile agent is the scripted ACP agent; told
// the brief, it asks one permission (a real one asks several), and the
// workspace agent's follow-up message has it copy the prepared source.
//
// What today's agent can't do is what this pass finds: on this tree an
// admin's agent can't create a tile at all (xbind refuses the approved call),
// so the person makes both tiles by hand; the admin opt-in is off until they
// find it in Preferences; the default coding agent is one the connector
// can't serve. The table (out/journey-agent.md) has each. It asserts the
// journey's end like the manual pass. Needs HARNESS_ISOLATE=1, `make sealed`.
'use strict';
const path = require('path');
const { fs, checker, log } = require('../lib');
const J0 = require('./journey');

const DOCK = 'bx-agent-dock';
const SHEET = `${DOCK} bx-agent-approve`;
const DLG = 'bx-agent-changes';
const TA = 'bx-chat-composer textarea';
const ASSISTANT = 'apps/assistant';
const PERSON = { id: 'ada', name: 'Ada Admin', password: require('crypto').randomBytes(12).toString('base64url') }; // (made per run, never logged)

const BRIEF = { tile: J0.DICE, goal: 'Turn this tile into an MCP server with one tool, roll_dice (sides, count), served as streamable HTTP at /mcp, and declare it in xbin.json as provides.mcp (kind http, service mcp, role reader).',
  done_when: 'POST /mcp tools/list names roll_dice and tools/call roll_dice returns a roll.', check_by: 'Call tools/list and tools/call against the tile\'s own API.' };
const BINDINGS = { title: 'Connect the assistant to a model and to the dice tool', changes: [
  { kind: 'binding.set', args: { component: ASSISTANT, slot: 'llm', providers: ['xbin:llm'] }, why: 'The assistant needs a language model' },
  { kind: 'binding.set', args: { component: ASSISTANT, slot: 'mcp', providers: [J0.DICE] }, why: 'Give the assistant the dice tool' }] };
const ws = (words, steps) => `${words}\nws: ${steps.join(' ;; ')}`;
const j = (name, args) => `${name} ${JSON.stringify(args)}`;
// the one prompt, and the plan a model makes of it
const SAY1 = 'Make a tool that rolls dice and give it to a new assistant I can chat with.';
const PROMPT1 = ws(SAY1, ['workspace_map', j('docs_search', { query: 'build an MCP server tile for an agent' }), 'tile_create',
  j('tile_create', { template: 'starter', name: 'Dice', why: 'A new tile to hold the dice tool' }), 'wait request']);
// …what the person has to say after making the tiles by hand
const SAY2 = 'I made apps/dice and apps/assistant by hand. Go on.';
const PROMPT2 = ws(SAY2, [j('tile_agent_brief', BRIEF), j('tile_agent_start', { brief: 1 }), 'wait ended its turn',
  j('tile_agent_message', { agent: '#1', text: J0.BUILD }), 'wait ended its turn', j('tile_agent_result', { agent: '#1' }), j('admin_propose', BINDINGS)]);
// …and after finding the opt-in
const SAY3 = 'I turned that on. Try again.';
const PROMPT3 = ws(SAY3, [j('admin_propose', BINDINGS), 'wait applied', j('tile_api_doc', { tile: ASSISTANT }), j('tile_call', { tile: J0.DICE, method: 'GET', path: '/roll' }),
  j('tile_call', { tile: J0.DICE, method: 'POST', path: '/mcp', body: '{"jsonrpc":"2.0","id":1,"method":"tools/list"}', dry_run: true })]);

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
    // the first-run sheet (D236): the account, the vault and the agent in one
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
    const allow = async (what) => { // the page's Review… → xbin's sheet → its Allow (armed a moment after it shows)
      await J.click(`${what}: "Review…" on the request's card`, async () => { await f.locator('.wsc-req[data-state="pending"] button.primary').last().click(); await page.waitForSelector(`${SHEET} [role="dialog"]`, { timeout: 20000 }); });
      const sheet = (await page.locator(`${SHEET} [role="dialog"]`).innerText()).replace(/\s+/g, ' ');
      await J.click(`${what}: the sheet's Allow`, async () => { await page.click(`${SHEET} button[data-allow][aria-disabled="false"]`, { timeout: 20000 }); await page.waitForSelector(`${SHEET} [role="dialog"]`, { state: 'detached', timeout: 20000 }); });
      return sheet;
    };

    J.section('the one prompt');
    await say(`the prompt ("${SAY1}")`, PROMPT1);
    await J.wait('the agent looked, and asks to create the tile', () => f.waitForSelector('.wsc-req[data-state="pending"]', { timeout: 120000 }));
    const sheet1 = await allow('create the dice tile');
    check(/Your agent wants to create a tile/.test(sheet1) && /Owner you/.test(sheet1), `xbin's sheet for the creation says the tile will be the person's (${sheet1.slice(0, 120)})`);
    const told1 = await J.wait('the approved creation runs', () => doneAfter(SAY1));
    const failed = ((await main()).match(/it failed[\s\S]{0,200}/) || [''])[0].replace(/\s+/g, ' ');
    const made = fs.existsSync(path.join(X.ws, J0.DICE));
    check(/workspace-admin action/.test(failed) && !made, `DEAD END: the creation the person approved fails for an admin — xbind's dry run had said it would be allowed (${failed.slice(0, 160)})`);
    log(`journeyAgent: the first turn told the model: ${told1.slice(0, 700)}`);
    J.deadEnd('tile_create fails for an admin\'s agent: the approved POST /templates/new is refused ("creating workspace-owned tiles is a workspace-admin action") — a delegate call is never an admin\'s, and an admin\'s new tile defaults to the workspace\'s. The sheet had said "Owner: you". The person makes both tiles by hand');

    J.section('by hand: the two tiles');
    await J0.makeTile(J, page, 'Dice');
    J0.plant(X.ws); // (out of band: the scripted coding agent and the source a real one would write)
    await J0.makeFromTemplate(J, page, 'AI Agent', ASSISTANT);

    J.section('the tile agent');
    f = await frame();
    await say(`"${SAY2}"`, PROMPT2);
    await J.wait('the brief, and a request to start the tile agent', () => f.waitForSelector('.wsc-req[data-state="pending"]', { timeout: 120000 }));
    const picked = (await main()).match(/(Claude Code|Codex|Gemini CLI|OpenCode|Fake agent \(tests\)) · asks before acting/);
    check(!!picked && picked[1] === 'Claude Code', `the start is filed for the default coding agent, ${picked && picked[1]}: one the workspace connector can't serve, and nobody is signed in to`);
    J.know('which coding agent to pick on the brief: the default (Claude Code) needs the person\'s own sign-in; Codex and OpenCode run on the workspace connector. The card lists five names and says neither');
    await J.field('the brief\'s card: the coding agent', async () => { await f.locator('.wsp-main select').last().selectOption('fake'); await f.waitForFunction(() => /Fake agent \(tests\) · asks before acting/.test(document.querySelector('.wsp-main').innerText), null, { timeout: 20000 }); },
      { note: 'the scripted agent stands in for Codex or OpenCode' });
    await page.waitForFunction(async () => { const r = await fetch('/api/xbin/delegate/requests'); const p = ((await r.json()).requests || []).filter((x) => x.state === 'pending'); return p.length === 1 && !!p[0].preview && !!p[0].preview.session && p[0].preview.session.provider === 'fake'; }, null, { timeout: 20000, polling: 300 });
    const sheet2 = await allow('start the tile agent');
    check(/Start a tile agent in Dice\?/.test(sheet2) && /ASKS BEFORE ACTING/i.test(sheet2) && sheet2.includes(BRIEF.done_when), 'xbin\'s sheet for the start: the mode, the coding agent, the brief as sent');
    await J.wait('the tile agent asks its first permission', () => f.waitForSelector('button.wsc-open:has-text("Answer")', { timeout: 120000 }));
    const TILE = `bx-frame[src="${J0.DICE}"]`;
    await J.click('the tile agent\'s card: "Answer" (opens the tile\'s window on its session)', async () => { await f.locator('button.wsc-open', { hasText: 'Answer' }).click(); await page.waitForSelector(`${TILE} bx-agent button[data-kind="allow_once"]`, { timeout: 30000 }); });
    await J.click('the tile agent\'s permission: "Allow once"', () => page.locator(`${TILE} bx-agent button[data-kind="allow_once"]`).click());
    J.hand(2, 'a real coding agent asks more than once (edits, then commands): about two more permission clicks, each in the tile\'s window');
    const refused = await J.wait('the tile agent builds; the agent reads its result and proposes the bindings', () => doneAfter(SAY2, 180000), 'scripted: under a minute. A real coding agent: one to three minutes');
    const tools = await J0.servesDice(ctx, X);
    check(tools.ok, `the tile agent built the MCP server: tools/list names roll_dice (${tools.status})`);
    check(/hasn't turned on "Let my agent propose admin changes"/.test(refused), `DEAD END: binding is an admin change, and the opt-in is off (${refused.slice(-200)})`);
    J.deadEnd('admin_propose is refused until the person turns on "Let my agent propose admin changes": the agent can only say so, and the switch is two menus away in the dock');

    J.section('the admin opt-in');
    await J.click('the dock\'s menu', () => page.click(`${DOCK} [data-menu]`));
    await J.click('the menu: "Preferences"', async () => { await page.click(`${DOCK} [data-view="prefs"]`); await page.waitForSelector(`${DOCK} bx-agent-prefs [data-admin-changes][aria-disabled="false"]`, { timeout: 20000 }); });
    await J.click('Preferences: "Let my agent propose admin changes"', async () => { await page.click(`${DOCK} bx-agent-prefs [data-admin-changes]`); await page.waitForFunction(async () => (await (await fetch('/api/xbin/delegate/state')).json()).adminChanges === true, null, { timeout: 20000, polling: 300 }); });
    await J.click('Preferences: back to the conversation', () => page.click(`${DOCK} bx-agent-prefs [data-back]`));

    J.section('the bindings');
    f = await frame();
    await say(`"${SAY3}"`, PROMPT3);
    await J.wait('the proposed admin changes', () => f.waitForSelector('.wsc-req[data-kind="changeset"]', { timeout: 120000 }));
    await J.click('the change set\'s card: "Review…"', async () => { await f.locator('.wsc-req[data-kind="changeset"] button.primary').last().click(); await page.waitForSelector(`${DLG} [role="dialog"][data-view="review"]`, { timeout: 20000 }); });
    const review = (await page.locator(`${DLG} [role="dialog"]`).innerText()).replace(/\s+/g, ' ');
    const ticks = await page.$$eval(`${DLG} .item`, (els) => els.map((e) => `${e.dataset.n}${e.querySelector('input').checked ? '+' : '-'}`));
    check(/Your agent proposes 2 admin changes/.test(review) && ticks.join(' ') === '1- 2+', `xbin's review: two bindings, the model's one unticked under its SPEND warning (${ticks.join(' ')})`);
    if (/\[object Object\]/.test(review)) log('journeyAgent: the review draws a binding\'s before and after as "[object Object]" (web/bx-agent-changes.js)');
    await J.click('the review: tick the llm binding (it starts unticked: it spends the workspace\'s quota)', () => page.check(`${DLG} .item[data-n="1"] input`));
    await J.click('the review: "Apply 2 changes…"', async () => { await page.click(`${DLG} button[data-apply][aria-disabled="false"]`, { timeout: 20000 }); await page.waitForSelector(`${DLG} [role="dialog"]:is([data-view="results"], [data-view="stepup"])`, { timeout: 30000 }); });
    const stepUp = (await page.getAttribute(`${DLG} [role="dialog"]`, 'data-view')) === 'stepup';
    if (stepUp) { // (a sign-in older than ten minutes: a scripted run never is)
      await J.field('the review: the password (a recent sign-in)', () => page.fill(`${DLG} #pw`, PERSON.password));
      await J.click('the review: apply', async () => { await page.click(`${DLG} button[data-apply][aria-disabled="false"]`); await page.waitForSelector(`${DLG} [role="dialog"][data-view="results"]`, { timeout: 30000 }); });
    } else J.hand(2, 'applying asks for the password when the sign-in is older than ten minutes — it is, once a real coding agent has built the tile: one field and one click');
    await J.click('the results: "Done"', () => page.click(`${DLG} button[data-done]`));
    const b = ((await call('GET', '/bindings')).data || {}).bindings || {};
    check(!!b[ASSISTANT] && String(b[ASSISTANT].llm) === 'xbin:llm' && String(b[ASSISTANT].mcp) === J0.DICE, `the change set bound the assistant's slots: ${JSON.stringify(b[ASSISTANT])}`);
    const end = await J.wait('the agent goes on and reports', () => doneAfter(SAY3));
    log(`journeyAgent: the last turn told the model: ${end.slice(0, 900)}`);
    check(/GET \/roll → 200/.test(end), 'the agent read the dice tile\'s GET /roll itself (a read: nobody is asked)');
    check(/must allow it first/.test(end), 'DEAD END: it can\'t check the tool the way the assistant will use it without another approval — MCP is POST, and every POST is a change to xbin');
    J.deadEnd('the agent can\'t verify the hook-up: tools/list and tools/call are POSTs (an approval each), and asking the assistant is a POST too. It reports the bindings as applied; whether the assistant can roll is the person\'s to try');

    J.section('the chat');
    await J.click('top bar: the workspace agent chip (closes the dock: it covers the tile)', () => page.locator('button.chip.agent').click());
    await J0.openFromSidebar(J, page, ASSISTANT);
    const pop = page.locator(`.pqpop[data-prompts-for="${ASSISTANT}"]`);
    await pop.waitFor({ timeout: 5000 }).catch(() => {});
    if (await pop.count()) await J.click('the popover: fold it (it covers the chat)', () => pop.locator('.pqx').click());
    const said = await J0.askAssistant(J, page, ASSISTANT);
    const body = await (await J0.tileFrame(page, ASSISTANT)).locator('body').innerText();
    check(J0.ROLLED.test(said) && /roll_dice · apps\/dice/.test(body), `the journey's end: the assistant called roll_dice on ${J0.DICE} and the answer shows the roll (${said})`);

    const t = J.write();
    check(t.interactions > 0, `the workspace agent's journey: ${t.interactions} interactions (${t.nav} navigation, ${t.click} clicks, ${t.field} fields, ${t.key} keys), +${t.hand} by hand, ${t.know} things to know, ${t.deadEnds} dead ends, 3 prompts where the benchmark wants 1`);
    log(`journeyAgent: ${JSON.stringify(t)}`);
  } finally {
    if (ctx) await ctx.close().catch(() => {});
    await X.stop();
  }
  done();
}

module.exports = { journeyAgent };
