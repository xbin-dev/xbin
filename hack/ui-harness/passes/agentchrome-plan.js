// hack/ui-harness/passes/agentchrome-plan.js — the agentChrome pass's checks
// of a PLAN's review (web/bx-agent-changes.js, agent-plan-words.js; D238),
// on its faked delegation (agentchrome.js fakeDelegation): the lines that
// start ticked; a line and the lines that need it; an admin's line —
// unticked, "applied as you", no switch asked for; the review closing
// itself after an Apply in which nothing failed, and staying open on a
// failure; a start drawn as the single start's sheet draws it, a mode that
// doesn't ask in the danger wording and unticked; a person who is no admin
// — "needs an admin", Ask an admin; the admin's review of that request; a
// kind this page doesn't know; hidden characters in a brief as escapes.
// Screenshots: out/agent-plan-*.png (both widths; run it in both themes).

const review = (page, id) => page.evaluate((i) => { document.querySelector('bx-agent-dock').review('changeset', i); }, id);

const BRIEF = 'Goal: an MCP tool roll_dice.\nDone when: tools/list names it.\nLeave alone: the manifest\'s provides.';
const start = (asks) => ({ tile: 'apps/dice', provider: 'codex', providerName: 'Codex', mode: asks ? 'default' : 'full-auto', asks, net: 'none', api: true, prompt: BRIEF });
const plan = (id, iso, o = {}) => ({ id, user: 'admin', agent: 'system/agent', plan: true, session: 'h1', title: 'A dice tool and an assistant <b>NOW</b>', state: 'proposed', created: iso(-60000), expires: iso(6 * 86400000), items: [
  { n: 1, kind: 'tile.create', args: {}, line: 'Create the tile apps/dice from the template Starter (starter) — yours', before: { tile: null }, after: { tile: 'apps/dice' }, beforeText: 'nothing at apps/dice', afterText: 'a new tile, yours',
    effects: ['it starts with nothing bound'], why: 'The tool', undoByHand: 'xbin doesn\'t delete tiles: deleting apps/dice is yours to do, from the tile\'s own menu' },
  { n: 2, kind: 'tile.create', args: {}, line: 'Create the tile apps/assistant from the template AI Agent (agent) — yours', beforeText: 'nothing at apps/assistant', afterText: 'a new tile, yours', before: {}, after: {}, why: 'The assistant' },
  { n: 3, kind: 'binding.set', args: {}, line: "Bind apps/assistant's llm to xbin:llm", admin: true, deferred: true, needs: [2], before: { binding: [] }, after: { binding: [{ provider: 'xbin:llm' }] }, beforeText: 'not bound', afterText: "the workspace's LLM connector (xbin:llm)",
    warnings: ["SPEND: apps/assistant's calls to the LLM connector spend the workspace's LLM quota"], why: 'A model' },
  { n: 4, kind: 'binding.set', args: {}, line: "Bind apps/assistant's mcp to apps/dice", deferred: true, needs: [1, 2], before: { binding: [] }, after: { binding: [{ provider: 'apps/dice' }] }, beforeText: 'not bound', afterText: 'apps/dice', why: 'The tool' },
  { n: 5, kind: 'agent.start', args: {}, line: 'Start a tile agent in apps/dice — Codex, in its mode "default"', deferred: true, needs: [1], start: start(true),
    effects: ['It asks you before it edits a file or runs a command: its questions and permission requests come to you, never to your workspace agent.'], why: 'A coding agent builds it' },
], ...o });

async function planChecks({ page, S, check, refresh, sentTo, armed, text, shot, width, tag, iso, DLG }) {
  const ticks = () => page.$$eval(`${DLG} .item`, (els) => els.map((e) => `${e.dataset.n}${e.querySelector('input') && e.querySelector('input').checked ? '+' : '-'}`).join(' '));
  const close = async () => { await page.keyboard.press('Escape'); await page.waitForFunction(() => !document.querySelector('bx-agent-changes')); };
  await page.waitForFunction(() => !document.querySelector('bx-agent-changes'));
  const admin0 = S.state.adminChanges;

  // ---- an admin's plan: personal lines ticked; the admin's line is theirs to tick — no switch is asked for ----
  S.state.adminChanges = false; S.state.canAdminChanges = true;
  S.changesets.push(plan('pl1', iso));
  await refresh();
  await review(page, 'pl1');
  await page.waitForSelector(`${DLG} [role="dialog"][data-view="review"] .item[data-n="5"]`);
  const dl = await text(`${DLG} [role="dialog"]`);
  check(/xbin · plan/i.test(dl) && /Your agent proposes 5 changes/.test(dl) && /<b>NOW<\/b>/.test(dl) && !/\[object Object\]/.test(dl) && /Before\s*not bound/i.test(dl) && /After\s*apps\/dice/i.test(dl),
    `[${width}] a plan's review: xbin's bar and heading, the agent's title quoted as text, a binding's before and after in words`);
  check((await ticks()) === '1+ 2+ 3- 4+ 5+' && !(await page.$eval(`${DLG} .item[data-n="3"] input`, (e) => e.disabled)) && (await page.getAttribute(`${DLG} .item[data-n="3"]`, 'data-line')) === 'ok'
    && !(await page.$(`${DLG} [data-optin]`)) && !/Let my agent propose admin changes/.test(dl),
  `[${width}] the person's own lines start ticked; the admin's starts unticked and is theirs to tick — the review asks for no switch (${await ticks()})`);
  check(/An admin change — applied as you\./.test(await text(`${DLG} .item[data-n="3"] [data-why]`)) && /Needed by line 4 and 5 — unticking this unticks them/.test(await text(`${DLG} .item[data-n="1"] [data-needs]`))
    && /Needs line 2 — checked again once that exists/.test(await text(`${DLG} .item[data-n="3"] [data-needs]`)) && (await text(`${DLG} .item[data-n="3"] [data-admin]`)) === 'admin',
  `[${width}] the admin's line says whose rights apply it; a line says what it needs and what needs it`);
  const st = await text(`${DLG} .item[data-n="5"] [data-start]`);
  check(/asks before it edits or runs anything/i.test(st) && /Coding agent\s*Codex/.test(st) && /Done when: tools\/list names it\./.test(st) && /sent exactly like this/.test(st), `[${width}] a start line: the mode, the coding agent, the brief as sent (${st.slice(0, 80)})`);
  await shot(page, `agent-plan-review${tag}`, { fullPage: false });
  // a line and the lines that need it
  await page.uncheck(`${DLG} .item[data-n="1"] input`);
  check((await ticks()) === '1- 2+ 3- 4- 5-' && await page.$eval(`${DLG} .item[data-n="4"] input`, (e) => e.disabled) && (await text(`${DLG} [data-apply]`)) === 'Apply 1 change…',
    `[${width}] unticking a line unticks the lines that need it, and they can't be ticked without it (${await ticks()})`);
  await page.check(`${DLG} .item[data-n="1"] input`); await page.check(`${DLG} .item[data-n="4"] input`); await page.check(`${DLG} .item[data-n="5"] input`);
  // the admin's own tick and Apply are the consent for that line: nothing else is clicked, and the standing opt-in stays off
  await page.check(`${DLG} .item[data-n="3"] input`);
  await armed(['bx-agent-changes', 'button[data-apply]']);
  await page.click(`${DLG} button[data-apply]`);
  await page.waitForSelector(`${DLG} [role="dialog"][data-view="results"] [data-closing]`);
  check(/All 5 changes applied/.test(await text(`${DLG} h2`)) && /deleting apps\/dice is yours to do/.test(await text(`${DLG} .item[data-n="1"] [data-byhand]`)) && !(await page.$(`${DLG} button[data-done]`))
    && /closes by itself — each line’s outcome stays in the conversation/.test(await text(`${DLG} [data-closing]`)),
  `[${width}] the results show in place, with no Done to press (${await text(`${DLG} h2`)}; "${await text(`${DLG} [data-closing]`)}")`);
  await shot(page, `agent-plan-results${tag}`, { fullPage: false });
  const t0 = Date.now();
  await page.waitForFunction(() => !document.querySelector('bx-agent-changes'), null, { timeout: 6000 });
  check(Date.now() - t0 < 5000 && S.state.adminChanges === false && sentTo(/PUT \/state$/).filter((c) => c.body && c.body.adminChanges === true).length === 0,
    `[${width}] the review closed itself after ${Date.now() - t0} ms; the standing opt-in was neither asked for nor turned on`);
  // opened again from its card: the results, with Undo and a Done of its own (nothing closes under the person's hand)
  await review(page, 'pl1');
  await page.waitForSelector(`${DLG} [role="dialog"][data-view="results"] button[data-done]`);
  await page.waitForTimeout(2600);
  check(!!(await page.$(`${DLG} button[data-done]`)) && !(await page.$(`${DLG} [data-closing]`)), `[${width}] a decided plan opened again stays until it is closed`);
  await page.click(`${DLG} button[data-done]`);
  await page.waitForFunction(() => !document.querySelector('bx-agent-changes'));
  S.changesets = S.changesets.filter((c) => c.id !== 'pl1');

  // ---- a line fails: the results stay open, say what failed and what was skipped, and are closed by the person ----
  const bad = plan('pl5', iso);
  bad.items[1].failsAtApply = 'a tile is at apps/assistant already';
  S.changesets.push(bad);
  await refresh();
  await review(page, 'pl5');
  await page.waitForSelector(`${DLG} [role="dialog"][data-view="review"] .item[data-n="5"]`);
  await armed(['bx-agent-changes', 'button[data-apply]']);
  await page.click(`${DLG} button[data-apply]`);
  await page.waitForSelector(`${DLG} [role="dialog"][data-view="results"]`);
  await page.waitForTimeout(2600);
  const failed = await text(`${DLG} [role="dialog"]`);
  check(!!(await page.$('bx-agent-changes')) && !(await page.$(`${DLG} [data-closing]`)) && (await text(`${DLG} button[data-done]`)) === 'Close' && /1 of 4 changes applied/.test(failed) && /a tile is at apps\/assistant already/.test(failed)
    && (await page.$$eval(`${DLG} .item [data-result]`, (els) => els.map((e) => e.dataset.result).join(' '))) === 'applied failed rejected skipped skipped',
  `[${width}] a failure keeps the results open, with xbind's reason and an explicit Close`);
  await shot(page, `agent-plan-failed${tag}`, { fullPage: false });
  await page.click(`${DLG} button[data-done]`);
  await page.waitForFunction(() => !document.querySelector('bx-agent-changes'));
  S.changesets = S.changesets.filter((c) => c.id !== 'pl5');

  // ---- a start in a mode that doesn't ask: the danger wording, unticked; hidden characters in the brief as escapes; a kind this page doesn't know ----
  S.changesets.push({ ...plan('pl2', iso), items: [
    { n: 1, kind: 'agent.start', args: {}, line: 'Start a tile agent in apps/dice — Codex, in its mode "full-auto"', start: { ...start(false), prompt: 'Goal: pay ‮evil‬ now' },
      warnings: ['ACTS WITHOUT ASKING: in the mode "full-auto" Codex edits files and runs commands in apps/dice without asking you. You will not get a chance to stop a single edit or command.'], why: 'faster' },
    { n: 2, kind: 'tile.delete', args: {}, line: 'Delete the tile apps/old' }] });
  await refresh();
  await review(page, 'pl2');
  await page.waitForSelector(`${DLG} [role="dialog"][data-view="review"] .item[data-n="2"]`);
  const hid = await page.$$eval(`${DLG} .item[data-n="1"] .hid`, (els) => els.map((e) => e.textContent).join(' '));
  check((await ticks()) === '1- 2-' && /acts without asking/i.test(await text(`${DLG} .item[data-n="1"] [data-row="mode"]`)) && /ACTS WITHOUT ASKING/.test(await text(`${DLG} .item[data-n="1"] [data-warning]`)) && hid === '⟨U+202E⟩ ⟨U+202C⟩',
    `[${width}] a mode that doesn't ask: the danger wording, a warning, unticked; the brief's hidden characters are escapes (${hid})`);
  check((await page.getAttribute(`${DLG} .item[data-n="2"]`, 'data-line')) === 'unknown' && await page.$eval(`${DLG} .item[data-n="2"] input`, (e) => e.disabled) && /does not know this kind of change/.test(await text(`${DLG} .item[data-n="2"] [data-why]`))
    && (await text(`${DLG} [data-apply]`)) === 'Apply none', `[${width}] a kind this page doesn't know is shown and can't be ticked`);
  await shot(page, `agent-plan-danger${tag}`, { fullPage: false });
  await close();
  S.changesets = S.changesets.filter((c) => c.id !== 'pl2');

  // ---- a person who is no admin: "needs an admin", and Ask an admin once what it needs is applied ----
  S.state.adminChanges = false; S.state.canAdminChanges = false;
  const mine = plan('pl3', iso);
  mine.items[2] = { n: 3, kind: 'binding.set', args: {}, line: "Bind apps/assistant's llm to xbin:llm", admin: true, needsAdmin: true, needs: [2], why: 'A model' };
  S.changesets.push(mine);
  await refresh();
  await review(page, 'pl3');
  await page.waitForSelector(`${DLG} [role="dialog"][data-view="review"] [data-ask]`);
  const na = await text(`${DLG} .item[data-n="3"]`);
  check((await ticks()) === '1+ 2+ 3- 4+ 5+' && await page.$eval(`${DLG} .item[data-n="3"] input`, (e) => e.disabled) && /needs an admin/.test(na) && /Not yours to apply/.test(na) && !/Before|SPEND/.test(na),
    `[${width}] a line that needs an admin: marked, not tickable, xbind's sentence and nothing of the state behind it (${await ticks()})`);
  check(/1 line needs an admin\./.test(await text(`${DLG} [data-ask]`)) && /Apply line 2 first/.test(await text(`${DLG} [data-ask]`)) && !(await page.$(`${DLG} [data-ask-admin]`)), `[${width}] asking waits for the line it needs: "${await text(`${DLG} [data-ask]`)}"`);
  await shot(page, `agent-plan-needs-admin${tag}`, { fullPage: false });
  await armed(['bx-agent-changes', 'button[data-apply]']);
  await page.click(`${DLG} button[data-apply]`);
  await page.waitForSelector(`${DLG} [role="dialog"][data-view="results"] [data-ask-admin]`);
  check(/1 line needs an admin and was not applied\./.test(await text(`${DLG} [data-asked]`)) && (await page.$$eval(`${DLG} .item [data-result]`, (els) => els.map((e) => e.dataset.result).join(' '))) === 'applied applied  applied applied',
    `[${width}] the rest applied with no sign-in step; the line that needs an admin waits (${await text(`${DLG} [data-asked]`)})`);
  await armed(['bx-agent-changes', 'button[data-ask-admin]']);
  await page.click(`${DLG} button[data-ask-admin]`);
  await page.waitForFunction(() => /sent to an admin/.test(document.querySelector('bx-agent-changes').shadowRoot.querySelector('.item[data-n="3"]').textContent));
  check(sentTo(/POST \/changesets\/pl3\/ask$/).length === 1 && /1 line was sent to the workspace's admins/.test(await text(`${DLG} [data-asked]`)) && !(await page.$(`${DLG} [data-ask-admin]`)), `[${width}] Ask an admin: one request, and the line says it was sent`);
  await shot(page, `agent-plan-asked${tag}`, { fullPage: false });
  await page.click(`${DLG} button[data-done]`);
  await page.waitForFunction(() => !document.querySelector('bx-agent-changes'));

  // ---- the admin's side: a request FROM that person, in xbin's words, applied with the admin's own rights ----
  S.state.canAdminChanges = true; S.state.paused = true; // (their own agent is paused: a request from someone else isn't held by that)
  S.changesets.push({ id: 'pl4', user: 'admin', agent: 'system/agent', plan: true, from: 'bob', askOf: 'pl3', askId: 'a1', title: 'A dice tool and an assistant', state: 'proposed', created: iso(-1000), expires: iso(6 * 86400000), items: [
    { n: 1, of: 3, kind: 'binding.set', args: {}, line: "Bind apps/assistant's llm to xbin:llm", admin: true, before: { binding: [] }, after: {}, beforeText: 'not bound', afterText: "the workspace's LLM connector (xbin:llm)",
      warnings: ["SPEND: apps/assistant's calls to the LLM connector spend the workspace's LLM quota"], why: 'A model' }] });
  await refresh();
  const row = await page.evaluate(() => import('/vendor/agent-chrome-words.js').then((m) => m.needs({ changesets: document.querySelector('bx-agent-dock').store.changesets }).map((r) => r.title).join(' | ')));
  await review(page, 'pl4');
  await page.waitForSelector(`${DLG} [role="dialog"][data-view="review"] .item[data-n="1"]`);
  const fr = await text(`${DLG} [role="dialog"]`);
  check(/xbin · request from bob/i.test(fr) && /bob asks for 1 admin change/.test(fr) && /Their agent's title — not checked by xbin/i.test(fr) && /bob asks for 1 admin change/.test(row) && (await ticks()) === '1-'
    && !(await page.$(`${DLG} [data-stopped]`)) && (await text(`${DLG} [data-reject]`)) === 'Decline',
  `[${width}] a request from another person: who asks, their agent's words quoted, a warned line unticked, Decline (${row})`);
  await shot(page, `agent-plan-request${tag}`, { fullPage: false });
  await close();
  S.changesets = S.changesets.filter((c) => !/^pl/.test(c.id));
  S.state.paused = false; S.state.adminChanges = admin0; S.state.canAdminChanges = true;
  await refresh();
}

module.exports = { planChecks };
