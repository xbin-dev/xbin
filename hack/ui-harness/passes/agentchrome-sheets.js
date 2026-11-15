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
}

module.exports = { sheetChecks };
