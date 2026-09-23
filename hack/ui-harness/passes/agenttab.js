// hack/ui-harness/passes/agenttab.js — the Agent tab (D74): a coding agent
// in the tile's sandbox, driven from the terminal window. The scripted
// "fake" provider (XBIN_AGENT_FAKE, run.sh) stands in for a real adapter.
// What this checks: (a) two browser contexts as the same user, attached to
// one agent session, see the same transcript; (b) a permission answered in
// one context resolves in the other (first answer wins); (c) the composer
// sends, Stop cancels a turn; (d) a reload replays the log from the cursor.
const { URL, login, settle, fr, waitFor, waitSel, openShell, usePersonalScreen, openTile, shotEl, checker } = require('../lib');

const TILE = 'apps/crawler';

async function agentTab(browser) {
  const { check, done } = checker('agent-tab');

  const openWindow = async (page) => {
    await openShell(page);
    await usePersonalScreen(page);
    await openTile(page, TILE);
    await fr(page, TILE, (f) => f.open('term'));
  };
  const blocks = (page) => fr(page, TILE, (f) => f.agent()?.blocks ?? []);

  // ---- A opens an agent tab and starts the "fake" agent with a "perm" turn ----
  const A = await login(browser, 'admin', 'admin');
  // clear any agent sessions a prior run left on the tile
  for (const s of await (await A.ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json()) {
    await A.ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(s.id)}`);
  }
  await openWindow(A.page);
  await fr(A.page, TILE, (f) => f.newAgent());
  await waitSel(A.page, `bx-frame[src="${TILE}"] bx-agent`, { timeout: 15000 });
  // wait for the provider list, pick the fake one, send a turn that asks permission
  await waitFor(A.page, (t) => (t.frameFor('apps/crawler')?.testApi().agent()?.provider) !== undefined, null, { timeout: 10000, label: 'providers loaded' });
  await fr(A.page, TILE, (f) => f.agent().setProvider('fake'));
  await fr(A.page, TILE, (f) => f.agent().send('perm please'));
  await waitFor(A.page, (t) => t.frameFor('apps/crawler')?.testApi().agent()?.status === 'waiting_permission', null, { timeout: 15000, label: "A's turn waits on a permission" });
  const idA = await fr(A.page, TILE, (f) => f.agent().sessionId);
  check(!!idA, `A created an agent session (${idA})`);
  const pendA = await fr(A.page, TILE, (f) => f.agent().pending);
  check(pendA.length === 1 && pendA[0].cmd === '', `A shows one pending permission (${JSON.stringify(pendA)})`); // the fake's run-ls has no rawInput
  const tabKind = await fr(A.page, TILE, (f) => f.tabs[f.activeTab]?.kind);
  check(tabKind === 'agent', `the active tab is an agent tab (${tabKind})`);
  await shotEl(A.page, `bx-frame[src="${TILE}"] .pop`, 'agent-tab-a-permission');

  // ---- B: the same user in a second browser sees the same session + pending ----
  const B = await login(browser, 'admin', 'admin');
  await openWindow(B.page);
  await waitFor(B.page, (t) => t.frameFor('apps/crawler')?.testApi().tabs.some((x) => x.kind === 'agent' && x.id), null, { timeout: 15000, label: "B lists A's agent session" });
  // make the agent tab active in B and let it replay
  await fr(B.page, TILE, (f) => { const i = f.tabs.findIndex((x) => x.kind === 'agent'); f.setActiveTab(i); });
  await waitSel(B.page, `bx-frame[src="${TILE}"] bx-agent`, { timeout: 15000 });
  await waitFor(B.page, (t) => (t.frameFor('apps/crawler')?.testApi().agent()?.pending || []).length === 1, null, { timeout: 15000, label: 'B replays the pending permission' });
  const pB = await fr(B.page, TILE, (f) => f.agent().pending);
  check(pB[0].options.includes('always') && pB[0].scoped, `B sees the agent's real options, scoped (${JSON.stringify(pB[0])})`);
  const idB = await fr(B.page, TILE, (f) => f.agent().sessionId);
  check(idB === idA, `B attached to the same session (${idB})`);
  const blkA = await blocks(A.page), blkB = await blocks(B.page);
  check(blkB.some((b) => b.kind === 'msg' && b.role === 'user' && b.text.includes('perm please')), 'B replays the user prompt');
  check(blkA.filter((b) => b.kind === 'tool').length === blkB.filter((b) => b.kind === 'tool').length, 'both see the same tool call');

  // ---- B answers the permission; A sees it resolve and the turn complete ----
  const pid = (await fr(B.page, TILE, (f) => f.agent().pending))[0].pid;
  await fr(B.page, TILE, (f, t, p) => f.agent().permit(p, 'allow_once'), pid);
  await waitFor(A.page, (t) => t.frameFor('apps/crawler')?.testApi().agent()?.status === 'idle', null, { timeout: 15000, label: "A's turn completes after B answered" });
  const resolvedA = (await blocks(A.page)).find((b) => b.kind === 'perm' && b.by);
  check(!!resolvedA && /user:/.test(resolvedA.by), `A shows the permission resolved by the user (${resolvedA?.by})`);
  check((await blocks(A.page)).some((b) => b.kind === 'turn' && b.stopReason === 'end_turn'), 'A shows the turn ended');
  await shotEl(B.page, `bx-frame[src="${TILE}"] .pop`, 'agent-tab-b-resolved');

  // ---- settings: the agent's options show after start; A picks a model, B sees it ----
  const optsA = await fr(A.page, TILE, (f) => f.agent().options);
  check(optsA.some((o) => o.id === 'model'), `the agent's settings are shown after start (${JSON.stringify(optsA)})`);
  await fr(A.page, TILE, (f) => f.agent().setOption('model', 'fake-fast'));
  await waitFor(B.page, (t) => (t.frameFor('apps/crawler')?.testApi().agent()?.options || []).some((o) => o.id === 'model' && o.current === 'fake-fast'), null, { timeout: 15000, label: 'B sees the model A picked' });
  check(true, 'a setting changed in A shows in B (one status stream)');

  // ---- the composer sends again, and Stop cancels a running turn ----
  await fr(A.page, TILE, (f) => f.agent().send('slow one'));
  await waitFor(A.page, (t) => t.frameFor('apps/crawler')?.testApi().agent()?.status === 'running', null, { timeout: 15000, label: 'the slow turn is running' });
  await fr(A.page, TILE, (f) => f.agent().cancel());
  await waitFor(A.page, (t) => (t.frameFor('apps/crawler')?.testApi().agent()?.blocks || []).some((b) => b.kind === 'turn' && b.stopReason === 'cancelled'), null, { timeout: 15000, label: 'the turn ended cancelled' });
  const cancelled = (await blocks(A.page)).filter((b) => b.kind === 'turn').pop();
  check(cancelled?.stopReason === 'cancelled', `the cancelled turn is recorded as cancelled (${cancelled?.stopReason})`);

  // ---- a reload replays the whole transcript from the cursor ----
  const before = (await blocks(A.page)).length;
  await A.page.reload();
  await openWindow(A.page);
  await fr(A.page, TILE, (f) => { const i = f.tabs.findIndex((x) => x.kind === 'agent'); if (i >= 0) f.setActiveTab(i); });
  await waitSel(A.page, `bx-frame[src="${TILE}"] bx-agent`, { timeout: 15000 });
  await waitFor(A.page, (t) => (t.frameFor('apps/crawler')?.testApi().agent()?.blocks || []).length >= 3, null, { timeout: 15000, label: 'the transcript replays after a reload' });
  const after = (await blocks(A.page)).length;
  check(after >= Math.min(before, 3), `the reload replayed the transcript (${after} blocks)`);
  await shotEl(A.page, `bx-frame[src="${TILE}"] .pop`, 'agent-tab-reload');

  // ---- E: the launcher path — a provider picked from the + menu creates the
  // session eagerly, so its model picker is there BEFORE the first prompt ----
  await fr(A.page, TILE, (f) => f.startKind('agent', 'fake'));
  await waitFor(A.page, (t) => {
    const api = t.frameFor('apps/crawler')?.testApi();
    if (!api) return false;
    const ag = api.agent(api.tabs.length - 1);
    return !!ag && !!ag.sessionId && (ag.options || []).some((o) => o.id === 'model');
  }, null, { timeout: 15000, label: 'the eager agent shows a model picker before any prompt' });
  const eagerIdx = await fr(A.page, TILE, (f) => f.tabs.length - 1);
  const eagerOpts = await fr(A.page, TILE, (f, t, i) => f.agent(i).options, eagerIdx);
  check(eagerOpts.some((o) => o.id === 'model'), `a model picker is available before the first prompt (${JSON.stringify(eagerOpts)})`);
  const eagerBlocks = await fr(A.page, TILE, (f, t, i) => f.agent(i).blocks, eagerIdx);
  check(!eagerBlocks.some((b) => b.kind === 'msg' && b.role === 'user'), 'the model picker shows with no prompt sent yet');

  // ---- a signed-out turn: the fake pushes _auth/status_update{none} and fails
  // with -32000; the tab shows a one-click "Sign in" that opens a shell tab ----
  const nBefore = await fr(A.page, TILE, (f) => f.tabs.length);
  await fr(A.page, TILE, (f, t, i) => f.agent(i).send('please fail'), eagerIdx);
  await waitFor(A.page, (t, i) => !!t.frameFor('apps/crawler')?.testApi().agent(i)?.login, eagerIdx, { timeout: 15000, label: 'the signed-out agent shows a sign-in prompt' });
  const lg = await fr(A.page, TILE, (f, t, i) => f.agent(i).login, eagerIdx);
  check(!!lg && lg.needed === true && /login/.test(lg.command || ''), `the agent offers a one-click sign-in (${JSON.stringify(lg)})`);
  await shotEl(A.page, `bx-frame[src="${TILE}"] .pop`, 'agent-tab-signin');
  await fr(A.page, TILE, (f, t, i) => f.agent(i).signIn(), eagerIdx);
  await waitFor(A.page, (t, n) => t.frameFor('apps/crawler')?.testApi().tabs.length === n + 1, nBefore, { timeout: 10000, label: 'sign-in opened a new shell tab' });
  const newTab = await fr(A.page, TILE, (f) => f.tabs[f.tabs.length - 1]);
  check(newTab.kind === 'shell', `sign-in opened a shell tab in the same window (${JSON.stringify(newTab)})`);
  const ranLogin = await A.page.locator(`bx-frame[src="${TILE}"] bx-terminal[run]`).count();
  check(ranLogin >= 1, `the shell tab is set to run the login command (${ranLogin} terminal(s) with a run cmd)`);

  // ---- F: history + resume — an ended session's transcript is kept, listed
  // under Recent sessions, opens read-only, and resumes (the fake replays) ----
  const eagerId = await fr(A.page, TILE, (f, t, i) => f.agent(i).sessionId, eagerIdx);
  await A.ctx.request.delete(`${URL}/api/xbin/term/sessions/${encodeURIComponent(eagerId)}`);
  await waitFor(A.page, (t, id) => (t.frameFor('apps/crawler')?.testApi().history || []).some((h) => h.id === id), eagerId, { timeout: 15000, label: 'the ended session lists under Recent sessions' });
  const hist = await fr(A.page, TILE, (f) => f.history);
  const row = hist.find((h) => h.id === eagerId);
  check(!!row && row.loadable && row.turns === 1 && /please fail/.test(row.preview || ''), `the history row carries turns, preview, loadable (${JSON.stringify(row)})`);
  check((await fr(A.page, TILE, (f) => f.launcherItems())).includes('Recent sessions'), 'the + menu offers Recent sessions');
  // open it read-only: a history tab with the persisted transcript
  await fr(A.page, TILE, (f, t, id) => f.openHistory(id), eagerId);
  const histIdx = await fr(A.page, TILE, (f) => f.tabs.length - 1);
  await waitFor(A.page, (t, i) => !!t.frameFor('apps/crawler')?.testApi().agent(i)?.history, histIdx, { timeout: 15000, label: 'the past session renders' });
  const histTab = await fr(A.page, TILE, (f, t, i) => f.tabs[i], histIdx);
  const histBlocks = await fr(A.page, TILE, (f, t, i) => f.agent(i).blocks, histIdx);
  check(histTab.history === eagerId && histTab.ended && histBlocks.some((b) => b.kind === 'msg' && b.role === 'user' && /please fail/.test(b.text)), `the transcript reads back read-only (${JSON.stringify(histTab)}, ${histBlocks.length} blocks)`);
  // resume: the tab is replaced by a live session that replays the earlier turns
  await fr(A.page, TILE, (f, t, i) => f.agent(i).resumeHistory(), histIdx);
  // The resumed tab is the NEW live agent session — found by identity, not
  // position: on the next listing tabsFrom re-sorts ended tabs after live
  // ones, so histIdx now points at the old (ended) agent.
  await waitFor(A.page, (t, a) => (t.frameFor('apps/crawler')?.testApi().tabs || []).some((tb) => tb.kind === 'agent' && !tb.history && !tb.ended && tb.id && !a.known.includes(tb.id)), { known: [idA, eagerId] }, { timeout: 15000, label: 'the resumed session is live' });
  const resumedIdx = await fr(A.page, TILE, (f, t, a) => f.tabs.findIndex((tb) => tb.kind === 'agent' && !tb.history && !tb.ended && tb.id && !a.known.includes(tb.id)), { known: [idA, eagerId] });
  const resumedId = await fr(A.page, TILE, (f, t, i) => f.agent(i).sessionId, resumedIdx);
  check(!!resumedId && resumedId !== eagerId, `resume replaced the read-only tab with a new live session (${resumedId})`);
  check((await fr(A.page, TILE, (f) => f.tabs)).every((tb) => !tb.history), 'the read-only tab was replaced, not duplicated');
  await waitFor(A.page, (t, i) => t.frameFor('apps/crawler')?.testApi().agent(i)?.status === 'idle', resumedIdx, { timeout: 15000, label: 'the resumed session is idle' });
  const resumedBlocks = await fr(A.page, TILE, (f, t, i) => f.agent(i).blocks, resumedIdx);
  check(resumedBlocks.some((b) => b.kind === 'msg' && b.role === 'user' && /resumed fake-1/.test(b.text)), `resume replayed the earlier turns into the live session (${resumedBlocks.length} blocks)`);
  await shotEl(A.page, `bx-frame[src="${TILE}"] .pop`, 'agent-tab-resumed');

  await settle(A.page);
  done();
}

module.exports = { agentTab };
