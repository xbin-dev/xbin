// hack/ui-harness/passes/journeymanual.js — the owner's UX benchmark, by
// hand (plans/wsagent/journey.md): from the first login on a fresh install
// with a host-provided LLM to an agent-template instance whose chat agent
// CALLS a roll_dice tool served by a new MCP tile, on today's shortest path
// through the real UI — every interaction counted (journey.js).
//
// The path: the first-run sheet the login URL lands on (D236) — an account,
// the vault's passphrase, its quiet button that leaves the workspace agent
// off (skipping the sheet and finding the vault's banner costs the same:
// five); a new tile; its window's coding agent, told to
// build the MCP server; an instance of the agent template; its `llm` slot
// bound to the workspace's connector and its `mcp` slot to the new tile; one
// question in its chat. It asserts the journey's end — the answer shows the
// roll, and the roll came from the tile's tool — and writes the step table
// to out/journey-manual.md. Needs HARNESS_ISOLATE=1 and `make sealed`.
'use strict';
const { checker, log } = require('../lib');
const J0 = require('./journey');

const AGENT = 'apps/agent';
const PERSON = { id: 'ada', name: 'Ada Admin', password: require('crypto').randomBytes(12).toString('base64url') }; // (made per run, never logged)

async function journeyManual(browser) {
  const { check, skip, done } = checker('journey-manual');
  const why = J0.noJourney();
  if (why) { skip(`the manual journey: ${why}`); done(); return; }
  const X = await J0.boot('manual');
  let ctx;
  try {
    const o = await J0.openPage(browser, 'journeyManual');
    ctx = o.ctx;
    const page = o.page;
    const J = J0.journey('manual', () => page);

    await J0.firstLogin(J, page, X);
    await J0.firstRunSheet(J, page, PERSON, { agent: false });
    const me = await (await ctx.request.get(`${X.url}/api/xbin/whoami`)).json();
    check(me.kind === 'user' && me.id === PERSON.id, `the sheet signed this browser in as the new account (${me.id})`);
    const llm = await (await ctx.request.get(`${X.url}/api/xbin/llm/config`)).json();
    check(llm.configured === true && llm.host && llm.host.active === true, 'the LLM connector is the host\'s: nothing to set up (no card, no banner, no key)');

    J.section('the MCP tile');
    await J0.makeTile(J, page, 'Dice');
    J0.plant(X.ws); // (out of band: the scripted coding agent and the source a real one would write)
    await J0.openFromSidebar(J, page, J0.DICE);
    const F = `bx-frame[src="${J0.DICE}"]`;
    await J.click('the tile\'s title bar: the terminal button (opens its window)', () => page.locator(`.card[data-path="${J0.DICE}"] .wc.term`).click());
    await page.waitForSelector(`${F} .pop`, { timeout: 20000 });
    await J.click('the window\'s launcher: a coding agent', async () => { await page.locator(`${F} .pop button`, { hasText: 'Fake agent (tests)' }).first().click(); await page.waitForSelector(`${F} bx-agent textarea`, { timeout: 30000 }); },
      { note: 'the scripted agent stands in for Codex or OpenCode, which the launcher offers on the workspace connector with no sign-in; Claude Code and Gemini CLI need the person\'s own sign-in (docs/llm.md)' });
    J.know('what an MCP tile is here, to say it in the prompt: a backend (xbin.json "runtime"), streamable HTTP at /mcp, and "provides": {"mcp": {"kind": "http", "service": "mcp"}} — no doc page, starter or template says it (the agent template\'s xbin.json comments do)');
    const ta = page.locator(`${F} bx-agent textarea`).first();
    await J.field('the coding agent: the prompt (make this tile an MCP server with a roll_dice tool)', () => ta.fill(J0.BUILD), { note: 'the scripted agent copies a prepared source into place (fixtures/journey-dice); a real one writes the four files' });
    await J.key('the coding agent: Enter', () => ta.press('Enter'));
    await J.wait('the coding agent\'s turn ends', () => page.waitForFunction((tile) => { const a = document.querySelector('bx-shell').testApi().frameFor(tile)?.testApi().agent(); return !!a && a.status === 'idle' && (a.blocks || []).some((b) => b.kind === 'turn'); }, J0.DICE, { timeout: 120000, polling: 250 }),
      'scripted: 8 s. A real coding agent on this task: one to three minutes');
    J.hand(3, 'a real coding agent in its asking mode asks before it writes and before it runs: about three permission clicks for four files and a check (one "Allow always" for edits, one or two for commands)');
    await J.click('the tile\'s window: close it (it covers the next tile)', () => page.evaluate((tile) => document.querySelector('bx-shell').testApi().frameFor(tile).testApi().closeTerminal(), J0.DICE), { api: true });
    const tools = await J0.servesDice(ctx, X);
    check(tools.ok, `the new tile serves MCP: tools/list names roll_dice (${tools.status})`);

    J.section('the agent instance');
    await J0.makeFromTemplate(J, page, 'AI Agent', '');
    await J0.openFromSidebar(J, page, AGENT);
    const pop = page.locator(`.pqpop[data-prompts-for="${AGENT}"]`);
    await pop.waitFor({ timeout: 30000 });
    J.know('which of the five slots matter: `llm` must be bound to "LLM by xbin" and `mcp` to the new tile before the chat can do anything; `net`, `sandboxes` and `scm` can wait. The popover lists five rows and a grant request without saying so');
    const bind = async (slot, option, label) => {
      const ms = pop.locator('bx-bindings bx-multiselect').nth(0); // a bound row leaves the list: the next unbound multi slot is first again
      const multis = await pop.locator('bx-bindings bx-multiselect').count();
      await J.click(`the ${slot} slot: "pick providers…"`, () => ms.locator('button.control').click());
      await J.click(`the ${slot} slot: tick ${label}`, () => ms.locator('label.opt', { hasText: option }).click());
      await J.key(`the ${slot} slot: Escape (closes the list)`, () => page.keyboard.press('Escape'));
      await J.click(`the ${slot} slot: "bind"`, () => pop.locator('bx-bindings button', { hasText: /^bind$/ }).first().click());
      await page.waitForFunction(async ([tile, s]) => { const r = await fetch('/api/xbin/bindings'); const b = (await r.json()).bindings || {}; return !!(b[tile] && b[tile][s]); }, [AGENT, slot], { timeout: 20000, polling: 300 });
      await page.waitForFunction(([sel, was]) => document.querySelector('bx-shell').testApi().query(sel).querySelector('bx-bindings').shadowRoot.querySelectorAll('bx-multiselect').length < was, [`.pqpop[data-prompts-for="${AGENT}"]`, multis], { timeout: 20000, polling: 100 });
    };
    await bind('llm', 'LLM by xbin', 'LLM by xbin');
    await bind('mcp', 'Dice', 'Dice');
    const b = (await (await ctx.request.get(`${X.url}/api/xbin/bindings`)).json()).bindings[AGENT] || {};
    check(String(b.llm) === 'xbin:llm' && String(b.mcp) === J0.DICE, `the instance's slots are bound: ${JSON.stringify(b)}`);
    await J.click('the popover: fold it (it covers the chat)', () => pop.locator('.pqx').click());

    J.section('the chat');
    const said = await J0.askAssistant(J, page, AGENT);
    const body = await (await J0.tileFrame(page, AGENT)).locator('body').innerText();
    check(J0.ROLLED.test(said) && /roll_dice · apps\/dice/.test(body), `the journey's end: the chat agent called roll_dice on ${J0.DICE} and the answer shows the roll (${said})`);

    const t = J.write();
    check(t.interactions > 0 && t.deadEnds === 0, `the manual journey: ${t.interactions} interactions (${t.nav} navigation, ${t.click} clicks, ${t.field} fields, ${t.key} keys), +${t.hand} by hand, ${t.know} things to know`);
    log(`journeyManual: ${JSON.stringify(t)}`);
  } finally {
    if (ctx) await ctx.close().catch(() => {});
    await X.stop();
  }
  done();
}

module.exports = { journeyManual };
