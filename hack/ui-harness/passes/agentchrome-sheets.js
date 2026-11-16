// hack/ui-harness/passes/agentchrome-sheets.js — the agentChrome pass's
// checks of the approval sheet's and the change-set dialog's own rules, on
// its faked delegation (agentchrome.js fakeDelegation): while the agent is
// stopped nothing can be allowed (the reason in the place of Allow and
// Apply, a stale sheet's 409 shown, never sent again); characters that
// reorder or hide text are escapes; a long body says how much is shown;
// a state the chrome doesn't know is not decidable. Split out of
// agentchrome.js for its size (the 1000-line cap).

// review(page, kind, id): the dock opens a request or change set — asked, not awaited in the page: the sheet is waited
// for by its selector (an evaluate that awaited the dock's fetch lost its context now and then, with no navigation)
const review = (page, kind, id) => page.evaluate(([k, i]) => { document.querySelector('bx-agent-dock').review(k, i); }, [kind, id]);

async function sheetChecks({ page, S, check, refresh, sentTo, armed, text, shot, width, tag, iso, callReq, changeSet, SHEET, DLG }) {
    // ---- while the agent is stopped: why, in the place of Allow and Apply; Deny and Reject stay ----
    await page.waitForFunction(() => !document.querySelector('bx-agent-changes'));
    S.requests.push(callReq('p1', { created: iso(-1000) }));
    S.changesets.push(changeSet('p2'));
    S.state.paused = true; S.state.pausedAt = new Date().toISOString(); await refresh();
    await review(page, 'request', 'p1');
    await page.waitForSelector(`${SHEET} [data-stopped="paused"]`);
    check(!(await page.$(`${SHEET} button[data-allow]`)) && /Can't be allowed now\. Your agent is paused\. Resume it first; then this can be allowed\. You can still deny it\./.test(await text(`${SHEET} [data-stopped]`))
      && await page.$eval(`${SHEET} button[data-deny]`, (b) => b.classList.contains('primary')), `[${width}] paused: the reason where Allow was, Deny stays (${await text(`${SHEET} .foot`)})`);
    await shot(page, `agent-chrome-stopped${tag}`, { fullPage: false });
    await page.keyboard.press('Escape');
    await page.waitForFunction(() => !document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve'));
    await review(page, 'changeset', 'p2');
    await page.waitForSelector(`${DLG} [data-stopped="paused"]`);
    check(!(await page.$(`${DLG} [data-apply]`)) && !!(await page.$(`${DLG} [data-reject]`)) && /Can't be applied now\..*You can still reject it\./.test(await text(`${DLG} [data-stopped]`)),
      `[${width}] paused: Apply is replaced by why, Reject all stays`);
    await shot(page, `agent-chrome-stopped-changes${tag}`, { fullPage: false });
    await page.keyboard.press('Escape');
    await page.waitForFunction(() => !document.querySelector('bx-agent-changes'));
    // a sheet opened before the stop: xbind's 409 says why, and the Allow is never sent again
    S.state.paused = false; delete S.state.pausedAt; await refresh();
    await review(page, 'request', 'p1');
    await armed(['bx-agent-dock', 'bx-agent-approve', 'button[data-allow]']);
    S.refuse = 'workspace-off'; // turned off at xbind; this page hasn't heard yet
    await page.click(`${SHEET} button[data-allow]`);
    await page.waitForSelector(`${SHEET} [data-stopped="workspace-off"]`);
    await page.waitForTimeout(300); // (a negative: no second Allow)
    check(sentTo(/p1\/approve$/).length === 1 && !(await page.$(`${SHEET} button[data-allow]`)) && /turned off for this workspace/.test(await text(`${SHEET} [data-stopped]`)),
      `[${width}] a stale sheet: the 409 shows the reason in place of Allow, no retry (${sentTo(/p1\/approve$/).length} sent)`);
    await page.keyboard.press('Escape');
    await page.waitForFunction(() => !document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve'));
    S.refuse = ''; S.requests = S.requests.filter((r) => r.id !== 'p1'); S.changesets = S.changesets.filter((c) => c.id !== 'p2'); await refresh();

    // ---- what the agent wrote, never drawn as it came: characters that reorder or hide text are escapes, as chips ----
    S.requests.push(callReq('h2', { created: iso(-500), body: '{"note":"pay \u202Eevil\u202C now"}', bodySize: 33, bodyShown: true, summary: 'Pay \u2066the\u2069 invoice\u200B' }));
    await refresh();
    await review(page, 'request', 'h2');
    await page.waitForSelector(`${SHEET} [data-row="sends"] .hid`);
    const hid = await page.$$eval(`${SHEET} .hid`, (els) => els.map((e) => e.textContent));
    const raw = await page.$eval(`${SHEET} [role="dialog"]`, (d) => /[\u202A-\u202E\u2066-\u2069\u200B-\u200F]/.test(d.shadowRoot ? '' : d.textContent));
    check(JSON.stringify(hid) === JSON.stringify(['⟨U+202E⟩', '⟨U+202C⟩', '⟨U+2066⟩', '⟨U+2069⟩', '⟨U+200B⟩']) && !raw
      && (await page.$$(`${SHEET} [data-hidden]`)).length === 2 && /contains hidden characters — shown as ⟨U\+…⟩/.test(await text(`${SHEET} [data-row="sends"]`)),
      `[${width}] hidden characters in the body and the agent's words: each an escape, as a chip, with the line above (${hid.join(' ')})`);
    await shot(page, `agent-chrome-hidden${tag}`, { fullPage: false });
    await page.keyboard.press('Escape');
    await page.waitForFunction(() => !document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve'));
    S.requests = S.requests.filter((r) => r.id !== 'h2'); await refresh();

    // ---- a long body: the note says how much is shown, and Show all shows the rest ----
    const long = 'line of the body · '.repeat(260); // 4940 characters
    S.requests.push(callReq('l2', { created: iso(-400), body: long, bodySize: long.length + 260, bodyShown: true }));
    await refresh();
    await review(page, 'request', 'l2');
    await page.waitForSelector(`${SHEET} [data-row="sends"] [data-note]`);
    const clipNote = await text(`${SHEET} [data-row="sends"] [data-note]`);
    check(/first 4000 of 4940 characters — Show all$/.test(clipNote) && !/shown in full/.test(clipNote), `[${width}] a long body: "first 4000 of 4940 characters", not "shown in full" (${clipNote})`);
    await shot(page, `agent-chrome-clipped${tag}`, { fullPage: false });
    await page.click(`${SHEET} [data-row="sends"] [data-show-all]`);
    await page.waitForFunction(() => /shown in full/.test(document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve').shadowRoot.querySelector('[data-row="sends"] [data-note]').textContent));
    const whole = await page.$eval(`${SHEET} [data-row="sends"] pre`, (p) => p.textContent.length);
    check(whole === long.length, `[${width}] …Show all: the whole of it, and then "shown in full" (${whole})`);
    await page.keyboard.press('Escape');
    await page.waitForFunction(() => !document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve'));
    S.requests = S.requests.filter((r) => r.id !== 'l2'); await refresh();

    // ---- a state this page doesn't know (a newer xbind's): never one that can be decided ----
    S.requests.push(callReq('u2', { created: iso(-300), state: 'deferred' }));
    await refresh();
    await review(page, 'request', 'u2');
    await page.waitForSelector(`${SHEET} [role="dialog"]`);
    check(!(await page.$(`${SHEET} button[data-allow]`)) && !(await page.$(`${SHEET} button[data-deny]`)) && /This page is older than the workspace — reload it to decide\./.test(await text(`${SHEET} [role="dialog"]`)),
      `[${width}] an unknown state: no Allow, no Deny, and why (${await text(`${SHEET} [role="dialog"] p`)})`);
    await page.keyboard.press('Escape');
    await page.waitForFunction(() => !document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve'));
    S.requests = S.requests.filter((r) => r.id !== 'u2'); await refresh();

    // after a decision the dock shows the next request that waits (another of the matrix's): put it off, "not now"
    const decided = async (id) => {
      await page.waitForFunction((i) => document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve')?.request?.id !== i, id);
      if (await page.$(`${SHEET} [role="dialog"]`)) await page.keyboard.press('Escape');
      await page.waitForFunction(() => !document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve'));
    };
    // ---- the web and MCP tools (D239): a page is approved once, with its whole address; a tool once or always ----
    S.requests.push({ id: 'w2', user: 'admin', agent: 'system/agent', kind: 'web', method: 'POST', target: '/api/xbin/delegate/web/fetch', body: '{"url":"https://status.example.com/incidents?since=today"}',
      bodyShown: true, bodySize: 57, summary: 'To see whether the payment provider reports an outage.', state: 'pending', created: iso(-200), expires: iso(86390000),
      preview: { class: 'web', url: 'https://status.example.com/incidents?since=today', host: 'status.example.com', orgs: ['sales'], readWeb: true,
        why: 'This conversation holds data of Sales: an address can carry it out, so each page it reads is one you approve' } });
    S.requests.push({ id: 'm2', user: 'admin', agent: 'system/agent', kind: 'mcp', method: 'POST', target: '/api/xbin/delegate/mcp/call', tile: 'apps/dice', body: '{"server":"apps/dice","tool":"wipe","arguments":{"table":"main"}}',
      bodyShown: true, bodySize: 62, summary: 'You asked to clear the table.', state: 'pending', created: iso(-100), expires: iso(86390000),
      preview: { class: 'mcp', tile: 'apps/dice', server: 'apps/dice', serverName: 'Dice', tool: 'wipe', title: 'Wipe the table', description: 'Clears every die from the table.', readOnly: false,
        arguments: '{\n  "table": "main"\n}', standing: { tool: 'wipe on Dice — this tool again, with any arguments, without asking' } } });
    await refresh();
    await review(page, 'request', 'w2');
    await page.waitForSelector(`${SHEET} [data-row="url"]`);
    const webText = await text(`${SHEET} [role="dialog"]`);
    check(/Your agent wants to read a page on status\.example\.com/.test(webText) && /https:\/\/status\.example\.com\/incidents\?since=today/.test(webText) && /holds data of Sales/.test(webText)
      && !!(await page.$(`${SHEET} [data-read-web]`)) && !(await page.$(`${SHEET} [data-choice]`)) && /Read this page/.test(await text(`${SHEET} button[data-allow]`)),
      `[${width}] a web page: its whole address, why xbin asks, "has read the web", once only (${webText.slice(0, 160)})`);
    await shot(page, `agent-chrome-web${tag}`, { fullPage: false });
    await armed(['bx-agent-dock', 'bx-agent-approve', 'button[data-allow]']);
    await page.click(`${SHEET} button[data-allow]`);
    await decided('w2');
    check(JSON.stringify(sentTo(/w2\/approve$/).map((c) => c.body)) === '[{"grant":"once"}]', `[${width}] …allowed once ({grant: once})`);
    await review(page, 'request', 'm2');
    await page.waitForSelector(`${SHEET} [data-row="tool"]`);
    const mcpText = await text(`${SHEET} [role="dialog"]`);
    check(/Your agent wants to run wipe on Dice/.test(mcpText) && /doesn’t mark it read-only/.test(mcpText) && /"table": "main"/.test(mcpText) && (await page.$$(`${SHEET} [data-choice]`)).length === 2,
      `[${width}] an MCP tool: the server, the tool, its mark, the arguments as sent; once or always for this tool`);
    await page.click(`${SHEET} [data-choice="tool"] input`);
    await shot(page, `agent-chrome-mcp${tag}`, { fullPage: false });
    await armed(['bx-agent-dock', 'bx-agent-approve', 'button[data-allow]']);
    await page.click(`${SHEET} button[data-allow]`);
    await decided('m2');
    check(JSON.stringify(sentTo(/m2\/approve$/).map((c) => c.body)) === '[{"grant":"always","scope":"tool"}]', `[${width}] …always for this tool ({grant: always, scope: tool})`);
    S.requests = S.requests.filter((r) => r.id !== 'w2' && r.id !== 'm2'); S.approvals = S.approvals.filter((a) => a.scope !== 'tool'); await refresh();

    // the tile's own words — a server's name, a tool's name and title — are never drawn as they came either
    S.requests.push({ id: 'm3', user: 'admin', agent: 'system/agent', kind: 'mcp', method: 'POST', target: '/api/xbin/delegate/mcp/call', tile: 'apps/dice', body: '{}', bodyShown: true, bodySize: 2,
      summary: 'x', state: 'pending', created: iso(-90), expires: iso(86390000),
      preview: { class: 'mcp', tile: 'apps/dice', server: 'apps/dice', serverName: 'Di\u202Ece', tool: 'wi\u200Bpe', title: 'Wipe \u2066the\u2069 table', readOnly: true, arguments: '{}', changed: ['description'] } });
    await refresh();
    await review(page, 'request', 'm3');
    await page.waitForSelector(`${SHEET} [data-row="tool"]`);
    const named = await page.$$eval(`${SHEET} .hid`, (els) => els.map((e) => e.textContent));
    check(named.includes('⟨U+202E⟩') && named.includes('⟨U+200B⟩') && named.includes('⟨U+2066⟩') && !(await page.$eval(`${SHEET} h2`, (h) => /[\u202E\u200B]/.test(h.textContent)))
      && /Changed since you last allowed it or first met it: its description/.test(await text(`${SHEET} [data-row="changed"]`)),
      `[${width}] a tool's and its server's names: hidden characters as escapes, the heading included; what changed, said (${named.join(' ')})`);
    await page.keyboard.press('Escape');
    await page.waitForFunction(() => !document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve'));
    S.requests = S.requests.filter((r) => r.id !== 'm3'); await refresh();
    await signinChecks({ page, S, check, refresh, sentTo, armed, text, shot, width, tag, iso, SHEET });
}

// ---- saved coding-agent sign-ins in the chrome (D230): a start that would use one names it on the sheet — xbind's
// id, the label from the person's own list, and who will hold it; one that would start signed out gets the sign-in
// card; and the agent's page can only ask the dock to show the card (xbin:agent-open-signin), never draw it ----
async function signinChecks({ page, S, check, refresh, sentTo, armed, text, shot, width, tag, iso, SHEET }) {
    const ctx = page.context();
    const mine = { available: true, vault: 'open', policy: { enabled: true }, flows: [], harnesses: [{ id: 'claude', name: 'Claude Code', guided: true, keys: [{ env: 'ANTHROPIC_API_KEY', label: 'Anthropic API key' }],
      signins: [{ id: 'si_harness01', harness: 'claude', name: 'Personal', kind: 'setup-token', default: true, createdAt: Date.now() - 86400000 }] }] };
    const list = (route) => (route.request().method() === 'GET' ? route.fulfill({ contentType: 'application/json', body: JSON.stringify(mine) }) : route.fallback());
    await ctx.route('**/api/xbin/agent/signins', list);
    const start = (id, session) => ({ id, user: 'admin', agent: 'system/agent', kind: 'session', method: 'POST', target: '/api/xbin/term/sessions', tile: 'apps/crawler', body: JSON.stringify({ prompt: 'Fix the export test.' }),
      bodyShown: 30, bodySize: 30, summary: 'I will fix the export', state: 'pending', created: iso(-500), expires: iso(86390000),
      preview: { class: 'session', tile: 'apps/crawler', session: { tile: 'apps/crawler', provider: 'claude', providerName: 'Claude Code', mode: 'default', asks: true, net: 'none', api: true, ...session } } });
    const row = `${SHEET} [data-row="signin"]`;
    // a label the agent put in its request is never what the sheet shows: the id is xbind's, the label the person's own list's
    S.requests.push(start('g1', { signIn: 'saved', signInId: 'si_harness01', savedSignIn: 'FORGED LABEL' }));
    await refresh();
    await review(page, 'request', 'g1');
    await page.waitForSelector(row);
    await page.waitForFunction((sel) => { const q = (root, parts) => parts.reduce((r, p) => r && (r.shadowRoot || r).querySelector(p), root);
      const n = q(document, ['bx-agent-dock', 'bx-agent-approve', '[data-row="signin"] bx-agent-signin']); return n && n.shadowRoot.querySelector('.label')?.dataset.known === 'true'; }, row);
    const label = await page.evaluate(() => { const q = (root, parts) => parts.reduce((r, p) => r && (r.shadowRoot || r).querySelector(p), root);
      return q(document, ['bx-agent-dock', 'bx-agent-approve', '[data-row="signin"] bx-agent-signin']).shadowRoot.textContent.trim(); });
    const uses = await text(row);
    check(/Uses your saved Claude Code sign-in/.test(uses) && label === 'Personal' && /The tile's coding agent will hold that sign-in: it, and anything it runs in Crawler, can read it\./.test(uses) && !/FORGED/.test(uses + label)
      && (await page.$eval(row, (e) => e.dataset.signinId)) === 'si_harness01', `[${width}] a start on a saved sign-in: named on the sheet by its label from your own list, with who will hold it (${uses} / ${label})`);
    await shot(page, `agent-chrome-approve-saved-signin${tag}`, { fullPage: false });
    await page.keyboard.press('Escape');
    await page.waitForFunction(() => !document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve'));
    // a sign-in that is gone by the time the sheet is read says so (the open would be refused)
    S.requests = S.requests.filter((r) => r.id !== 'g1'); S.requests.push(start('g2', { signIn: 'saved', signInId: 'si_gone' })); await refresh();
    await review(page, 'request', 'g2');
    await page.waitForSelector(row);
    await page.waitForFunction(() => { const q = (root, parts) => parts.reduce((r, p) => r && (r.shadowRoot || r).querySelector(p), root);
      const n = q(document, ['bx-agent-dock', 'bx-agent-approve', '[data-row="signin"] bx-agent-signin']); return n && n.shadowRoot.querySelector('.label')?.dataset.known === 'false' && !/…/.test(n.shadowRoot.textContent); });
    check(true, `[${width}] …and one that is no longer saved says so`);
    await page.keyboard.press('Escape');
    await page.waitForFunction(() => !document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve'));
    // a start that would begin signed out: the card, in the sheet — xbin's own, with the guided sign-in and the pasted key
    S.requests = S.requests.filter((r) => r.id !== 'g2'); S.requests.push(start('g3', { signIn: 'needed' })); await refresh();
    await review(page, 'request', 'g3');
    await page.waitForSelector(`${row} bx-agent-signin`);
    await page.waitForFunction(() => { const q = (root, parts) => parts.reduce((r, p) => r && (r.shadowRoot || r).querySelector(p), root);
      const n = q(document, ['bx-agent-dock', 'bx-agent-approve', '[data-row="signin"] bx-agent-signin']); return n && n.shadowRoot.querySelector('button'); });
    const card = await page.evaluate(() => { const q = (root, parts) => parts.reduce((r, p) => r && (r.shadowRoot || r).querySelector(p), root);
      const n = q(document, ['bx-agent-dock', 'bx-agent-approve', '[data-row="signin"] bx-agent-signin']); return { harness: n.harness, buttons: [...n.shadowRoot.querySelectorAll('button')].map((b) => b.textContent.trim()) }; });
    check(/Needs your Claude Code sign-in/.test(await text(row)) && card.harness === 'claude' && card.buttons.some((b) => /^Sign in/.test(b)), `[${width}] a start that would begin signed out: the sign-in card in the sheet (${card.buttons.join(' | ')})`);
    await shot(page, `agent-chrome-approve-needs-signin${tag}`, { fullPage: false });
    await page.keyboard.press('Escape');
    await page.waitForFunction(() => !document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve'));
    S.requests = S.requests.filter((r) => r.id !== 'g3'); await refresh();

    // the dock's overlay: the agent's page names a coding agent; the card is the chrome's, outside the page's frame
    await ctx.route('**/signin-page.html', (route) => route.fulfill({ contentType: 'text/html', body: `<!doctype html><body><h1>the agent's page</h1><script>
      const got = [];
      addEventListener('message', (e) => { if (e.data && e.data.type === 'xbin:agent-signins') { got.push(e.data); parent.postMessage({ type: 'signin-page', got }, '*'); } });
      parent.postMessage({ type: 'xbin:agent-open-signin', harness: 'claude', label: 'FORGED', url: 'https://evil.example', secret: 'x' }, '*');
      parent.postMessage({ type: 'signin-page', got }, '*');
      </script>` }));
    const was = await page.evaluate(() => { const d = document.querySelector('bx-agent-dock'); const was = d.page; window.signinPage = null;
      addEventListener('message', (e) => { if (e.data && e.data.type === 'signin-page') window.signinPage = e.data.got; });
      d.show('chat'); d.page = '/signin-page.html'; return was; });
    const OVER = 'bx-agent-dock [data-signin]';
    await page.waitForSelector(`${OVER} bx-agent-signin`);
    await page.waitForFunction(() => { const n = document.querySelector('bx-agent-dock').shadowRoot.querySelector('[data-signin] bx-agent-signin'); return n && n.shadowRoot.querySelector('button'); });
    const over = await page.evaluate(() => { const d = document.querySelector('bx-agent-dock').shadowRoot, n = d.querySelector('[data-signin] bx-agent-signin'), f = d.querySelector('iframe');
      return { harness: n.harness, inFrame: !!(f && f.contains(n)), words: n.shadowRoot.textContent.replace(/\s+/g, ' ').trim(), buttons: [...n.shadowRoot.querySelectorAll('button')].map((b) => b.textContent.trim()) }; });
    check(over.harness === 'claude' && !over.inFrame && !/FORGED|evil\.example/.test(over.words) && over.buttons.some((b) => /^Sign in/.test(b)),
      `[${width}] the page asks for a sign-in by the coding agent's id alone: the card is drawn by the dock, not in the page's frame (${over.buttons.join(' | ')})`);
    await shot(page, `agent-chrome-dock-signin${tag}`, { fullPage: false });
    // a change to the list is told to the page — that it changed, nothing of what
    await page.evaluate(() => { document.querySelector('bx-agent-dock').shadowRoot.querySelector('[data-signin] bx-agent-signin').dispatchEvent(new CustomEvent('bx-signins-changed', { bubbles: true, composed: true })); });
    await page.waitForFunction(() => window.signinPage && window.signinPage.length > 0);
    const told = await page.evaluate(() => window.signinPage);
    check(told.length === 1 && Object.keys(told[0]).join() === 'type', `[${width}] …and the page is told only that sign-ins changed (${JSON.stringify(told)})`);
    await page.click(`${OVER} [data-signin-close]`);
    await page.waitForFunction(() => !document.querySelector('bx-agent-dock').shadowRoot.querySelector('[data-signin]'));
    check(sentTo(/signins/).length === 0, `[${width}] nothing was saved or sent by any of it`);
    await page.evaluate((p) => { document.querySelector('bx-agent-dock').page = p; }, was);
    await ctx.unroute('**/api/xbin/agent/signins', list);
}

module.exports = { sheetChecks };
