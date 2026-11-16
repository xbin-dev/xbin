// hack/ui-harness/passes/journeymanual.js — the owner's UX benchmark, by
// hand (plans/wsagent/journey.md): from the first login on a fresh install
// with a host-provided LLM to an agent-template instance whose chat agent
// CALLS a roll_dice tool served by a new MCP tile, on today's shortest path
// through the real UI — every interaction counted (journey.js).
//
// The path: the first-run sheet the login URL lands on (D237) — an account,
// the vault's passphrase, its quiet button that leaves the workspace agent
// off (skipping the sheet and finding the vault's banner costs the same:
// five); an MCP tile from the mcp-server template (its roll_dice is the
// template's example tool: no coding agent); an instance of the agent
// template; its `mcp` slot bound to the new tile and its `llm` slot (marked
// required) to the workspace's connector — after which the rest of its
// slots fold into one line above it (G10); one question in its chat. It
// asserts the journey's end — the answer shows the
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
    // the mcp-server template (docs/mcp.md): its example tool is roll_dice,
    // so the tile serves it from its first build — no coding agent, no
    // prompt, nothing to know about MCP
    await J0.makeFromTemplate(J, page, 'MCP server', J0.DICE);
    const tools = await J.wait('the MCP tile\'s backend is built and answers', () => J0.servesDice(ctx, X));
    check(tools.ok, `the new tile serves MCP: tools/list names roll_dice (${tools.status})`);

    J.section('the agent instance');
    await J0.makeFromTemplate(J, page, 'AI Agent', '', { tabOpen: true });
    await J0.openFromSidebar(J, page, AGENT);
    const pop = page.locator(`.pqpop[data-prompts-for="${AGENT}"]`);
    await pop.waitFor({ timeout: 30000 });
    const req = await pop.locator('bx-bindings .row', { hasText: 'required' }).locator('.slot').allInnerTexts();
    check(req.length === 1 && req[0] === 'llm', `the popover marks the llm slot required (${JSON.stringify(req)})`);
    J.know('that the mcp slot is the one the new tile goes in, and to bind it before llm: once llm (marked "required") is bound, the other slots fold into one line above the chat ("show" opens them again, one more click)');
    const bind = async (slot, option, label) => {
      const row = pop.locator('bx-bindings .row', { has: page.locator('.slot', { hasText: new RegExp(`^${slot}$`) }) });
      await J.click(`the ${slot} slot: "pick providers…"`, () => row.locator('bx-multiselect button.control').click());
      await J.click(`the ${slot} slot: tick ${label}`, () => row.locator('bx-multiselect label.opt', { hasText: option }).click());
      await J.key(`the ${slot} slot: Escape (closes the list)`, () => page.keyboard.press('Escape'));
      await J.click(`the ${slot} slot: "bind"`, () => row.locator('button', { hasText: /^bind$/ }).click());
      await page.waitForFunction(async ([tile, s]) => { const r = await fetch('/api/xbin/bindings'); const b = (await r.json()).bindings || {}; return !!(b[tile] && b[tile][s]); }, [AGENT, slot], { timeout: 20000, polling: 300 });
      await page.waitForFunction(([tile, s]) => { const p = document.querySelector('bx-shell').testApi().query(`.pqpop[data-prompts-for="${tile}"]`); return !p || ![...p.querySelector('bx-bindings').shadowRoot.querySelectorAll('.row .slot')].some((x) => x.textContent.trim() === s); }, [AGENT, slot], { timeout: 20000, polling: 100 });
    };
    await bind('mcp', 'Dice', 'Dice');
    await bind('llm', 'LLM by xbin', 'LLM by xbin');
    const b = (await (await ctx.request.get(`${X.url}/api/xbin/bindings`)).json()).bindings[AGENT] || {};
    check(String(b.llm) === 'xbin:llm' && String(b.mcp) === J0.DICE, `the instance's slots are bound: ${JSON.stringify(b)}`);
    const line = page.locator(`.pqline[data-prompts-for="${AGENT}"]`);
    await line.waitFor({ timeout: 20000 }).catch(() => {});
    check(await line.count() === 1 && await pop.count() === 0 && /optional interfaces/.test(await line.innerText()), `its required slot bound, the rest fold into one line above the chat — no popover to fold (${await line.innerText().catch(() => '')})`);

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
