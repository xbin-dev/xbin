// hack/ui-harness/passes/agenttab.js — the Agent tab (D74): a coding agent
// in the tile's sandbox, driven from the terminal window. The scripted
// "fake" provider (XBIN_AGENT_FAKE, run.sh) stands in for a real adapter.
// What this checks: (a) two browser contexts as the same user, attached to
// one agent session, see the same transcript; (b) a permission answered in
// one context resolves in the other (first answer wins); (c) the composer
// sends, Stop cancels a turn; (d) a reload replays the log from the cursor;
// (e) a plan approval (Claude's ExitPlanMode) renders as a plan card — the
// plan as markdown, the agent's mode options, no JSON, no session rule — and
// "keep planning" with feedback sends the feedback as the next message;
// (f) a streaming thought is open ("Thinking…"), then folds to "Thought for Ns";
// (g) a shell write reads as "Write <file>", shows its output, and the
// snapshot diff (files.changed) names the file on the card and for the turn;
// (h) a subagent's thought, calls and text nest under its Task card;
// (i) "/" in the composer offers the agent's slash commands, Tab completes;
// (j) a question (AskUserQuestion → elicitation) is a form: radios, checkboxes,
// an "Other" box; Submit sends the values and the card settles as answered;
// (k) an agent tab has the same bar as a shell: the layout switcher (the code
// panel beside the agent) and the net/API pickers; (l) switching the network
// restarts the agent in a new sandbox and resumes its conversation; (m) a
// prompt with attachments (POST …/prompt {attachments}) names its files in
// the user's message and reaches the agent inline and as dropped files;
// (n) messages while a turn runs (D197): held as chips both browsers see, ×
// and Stop take them back, the turn's end sends them, Ctrl+Enter stops the
// turn and sends; (o) the composer grows with its text up to 40% of the tab
// and shrinks back after a send (#31); (p) a fenced code block in a known
// language is highlighted (#26).
const { URL, log, login, closeCtx, settle, sh, fr, waitFor, waitSel, openShell, usePersonalScreen, openTile, shotEl, checker, showPickers, PICKERS } = require('../lib');

const TILE = 'apps/crawler';
// Tall: the seeded pending-access and interfaces-to-bind bars push the canvas
// ~500 px down, and at 900 px the window (anchored to its tile, D66) runs
// past the bottom of the viewport — its composer can't be clicked, and a
// fixed window is never scrolled into view.
const VIEW = { viewport: { width: 1400, height: 1300 } };

async function agentTab(browser) {
  const { check, skip, done } = checker('agent-tab');
  // not written for sandboxes (D251 C): the scripted agent is a host path (XBIN_AGENT_FAKE=$REPO/bin/fakeacp), which a
  // tile's sandbox can't run — run.sh says so, and agentChrome skips the same step. test/isolated runs it in the tile.
  if (process.env.HARNESS_ISOLATE === '1') { skip('the scripted agent (XBIN_AGENT_FAKE, a host path) can\'t start inside a tile\'s sandbox under HARNESS_ISOLATE=1: run it unisolated'); return done(); }

  const openWindow = async (page) => {
    await openShell(page);
    await usePersonalScreen(page);
    await openTile(page, TILE);
    // the home screen's default tiles can push the card (and its window,
    // anchored to it) below the fold: bring it to the top so clicks land
    await page.locator(`.card[data-path="${TILE}"]`).evaluate((el) => el.scrollIntoView({ block: 'start' }));
    await fr(page, TILE, (f) => f.open('term'));
    await showPickers(page, TILE); // the bar's pickers, wherever this host's bar puts them (lib.js)
  };
  const blocks = (page) => fr(page, TILE, (f) => f.agent()?.blocks ?? []);

  // ---- A opens an agent tab and starts the "fake" agent with a "perm" turn ----
  const A = await login(browser, 'admin', 'admin', VIEW);
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
  const B = await login(browser, 'admin', 'admin', VIEW);
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

  // ---- messages while a turn runs (D197; the harness's fake takes none
  // mid-turn, so they are held): Send stays beside Stop, held messages are
  // chips both browsers see, × takes one back, the turn's end sends the
  // rest; Stop takes them back first; Ctrl+Enter stops the turn, then sends ----
  // fn(agentTestApi, arg) in the page (its source travels, as fr's does)
  const ag = (page, fn, arg) => fr(page, TILE, (f, t, a) => new Function('g', 'a', `return (${a.src})(g, a)`)(f.agent(), a.arg), { src: fn.toString(), arg: arg ?? null });
  const agWait = (page, pred, label, arg) => waitFor(page, (t, [src, a]) => {
    const g = t.frameFor('apps/crawler')?.testApi().agent();
    return !!g && new Function('g', 'a', `return (${src})(g, a)`)(g, a);
  }, [pred.toString(), arg], { timeout: 15000, label });
  const userSaid = (bs, text) => bs.some((b) => b.kind === 'msg' && b.role === 'user' && b.text === text);
  await agWait(A.page, (g) => g.status === 'idle', 'idle before the queue');
  await ag(A.page, (g) => g.send('slow two'));
  await agWait(A.page, (g) => g.status === 'running', 'the slow turn runs');
  check(JSON.stringify(await ag(A.page, (g) => g.buttons)) === '["Stop","Send"]', `while a turn runs Send stays beside Stop (${JSON.stringify(await ag(A.page, (g) => g.buttons))})`);
  await ag(A.page, (g) => g.send('first follow-up'));
  await ag(A.page, (g) => g.send('second follow-up'));
  await agWait(A.page, (g) => g.queued.length === 2, 'two messages held');
  await agWait(B.page, (g) => g.queued.length === 2, 'B sees the held messages');
  check((await ag(A.page, (g) => g.draft)) === '', 'a held message leaves the composer');
  await shotEl(A.page, `bx-frame[src="${TILE}"] .pop`, 'agent-tab-queued');
  const held = await ag(A.page, (g) => g.queued);
  await ag(A.page, (g, id) => g.takeBack(id), held[1].id);
  await agWait(A.page, (g) => g.queued.length === 1, 'the chip taken back');
  check((await ag(A.page, (g) => g.draft)) === 'second follow-up', `× puts its text back into the composer (${await ag(A.page, (g) => g.draft)})`);
  await ag(A.page, (g) => g.setDraft(''));
  await agWait(A.page, (g, t) => g.blocks.some((b) => b.kind === 'msg' && b.role === 'agent' && b.text === 'echo: ' + t), 'the held message went in at the turn\'s end', 'first follow-up');
  const afterQ = await blocks(A.page);
  check(userSaid(afterQ, 'first follow-up') && !userSaid(afterQ, 'second follow-up') && !(await ag(A.page, (g) => g.queued.length)),
    'the turn\'s end sent what was held, nothing taken back, and the chips are gone');
  await agWait(A.page, (g) => g.status === 'idle', 'idle after the held message');
  // Stop: what waits comes back into the composer, then the turn ends
  await ag(A.page, (g) => g.send('slow three'));
  await agWait(A.page, (g) => g.status === 'running', 'the third slow turn runs');
  await ag(A.page, (g) => g.send('keep me'));
  await agWait(A.page, (g) => g.queued.length === 1, 'one held before Stop');
  await ag(A.page, (g) => g.stop());
  await agWait(A.page, (g) => g.blocks.filter((b) => b.kind === 'turn').pop()?.stopReason === 'cancelled' && g.status === 'idle', 'Stop cancelled the turn');
  check((await ag(A.page, (g) => g.draft)) === 'keep me' && !userSaid(await blocks(A.page), 'keep me'), 'Stop took the held message back into the composer — it was not sent');
  await ag(A.page, (g) => g.setDraft(''));
  // Ctrl+Enter: stop the running turn, then send
  await ag(A.page, (g) => g.send('slow four'));
  await agWait(A.page, (g) => g.status === 'running', 'the fourth slow turn runs');
  await ag(A.page, (g) => g.sendInterrupt('instead, this'));
  await agWait(A.page, (g, t) => g.blocks.some((b) => b.kind === 'msg' && b.role === 'agent' && b.text === 'echo: ' + t), 'the interrupting message went in', 'instead, this');
  const turnsI = (await blocks(A.page)).filter((b) => b.kind === 'turn');
  check(turnsI.at(-2)?.stopReason === 'cancelled' || turnsI.at(-1)?.stopReason === 'cancelled', 'Ctrl+Enter cancelled the running turn before it sent');
  await agWait(A.page, (g) => g.status === 'idle', 'idle after the interrupt');

  // ---- the composer's height (#31): grows with its text up to 40% of the
  // tab, never scrolls a sliver, and shrinks back once the text is sent ----
  const box0 = await ag(A.page, (g) => g.composer);
  await ag(A.page, (g) => g.setDraft('one\ntwo\nthree'));
  await settle(A.page);
  const box3 = await ag(A.page, (g) => g.composer);
  check(box3.height > box0.height && !box3.scrolls, `three lines fit without scrolling (${box0.height} → ${box3.height} px; ${JSON.stringify(box3)})`);
  await ag(A.page, (g) => g.setDraft(Array.from({ length: 80 }, (_, i) => `pasted line ${i + 1}`).join('\n')));
  await settle(A.page);
  const boxL = await ag(A.page, (g) => g.composer);
  check(boxL.scrolls && boxL.height <= Math.ceil(boxL.hostHeight * 0.4) + 1, `a long paste stops at 40% of the tab and scrolls inside (${boxL.height} px of ${boxL.hostHeight})`);
  await ag(A.page, (g) => g.send(Array.from({ length: 20 }, (_, i) => `line ${i + 1}`).join('\n')));
  await settle(A.page);
  const boxS = await ag(A.page, (g) => g.composer);
  check(boxS.height === box0.height, `after sending, the composer is one line again (${boxS.height} px, was ${box0.height})`);
  await agWait(A.page, (g) => g.status === 'idle', 'idle after the long message');

  // ---- code in the agent's answers is coloured (#26): a closed fence in a
  // language highlight.js knows (the fake's unit: a Go fence) ----
  await ag(A.page, (g) => g.send('long 1'));
  await agWait(A.page, (g) => g.status === 'idle' && g.blocks.some((b) => b.kind === 'msg' && b.role === 'agent' && /```go/.test(b.text || '')), 'the Go fence arrived');
  const fence = A.page.locator(`bx-frame[src="${TILE}"] bx-agent pre code.hljs.language-go`).last();
  await fence.scrollIntoViewIfNeeded().catch(() => { });
  check(await fence.locator('.hljs-keyword').count() >= 1 && /func helper1/.test(await fence.innerText()), 'a Go fence is highlighted (keywords in their colour), its text unchanged');
  await shotEl(A.page, `bx-frame[src="${TILE}"] .pop`, 'agent-tab-code');

  // ---- a prompt with attachments (the native app's composer, over the API):
  // the user's message names the files; the agent got the image inline and
  // the others as files dropped in its sandbox ----
  await waitFor(A.page, (t) => t.frameFor('apps/crawler')?.testApi().agent()?.status === 'idle', null, { timeout: 15000, label: 'idle before the attachments' });
  const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64');
  const att = await A.ctx.request.post(`${URL}/api/xbin/term/sessions/${idA}/prompt`, { data: { text: 'what is in these?', attachments: [
    { name: 'screenshot.png', mime: 'image/png', data: png.toString('base64') },
    { name: 'build.log', mime: 'text/plain', data: Buffer.from('ok 1\nFAIL 2\n').toString('base64') }] } });
  check(att.ok(), `a prompt with attachments is accepted (${att.status()})`);
  await waitFor(A.page, (t) => (t.frameFor('apps/crawler')?.testApi().agent()?.blocks || []).some((b) => b.kind === 'msg' && /\[image image\/png \d+B\]/.test(b.text || '')), null, { timeout: 15000, label: 'the agent echoed the attachments' });
  const withFiles = (await blocks(A.page)).filter((b) => b.kind === 'msg' && b.role === 'user' && b.files).pop();
  check(withFiles?.files?.map((f) => f.name).join(' ') === 'screenshot.png build.log', `the user's message names its files (${JSON.stringify(withFiles?.files)})`);
  const echoed = (await blocks(A.page)).filter((b) => b.kind === 'msg' && b.role === 'agent').pop()?.text || '';
  check(/\[file screenshot\.png \d+B\]/.test(echoed) && /\[resource build\.log \d+B\]/.test(echoed), `the agent got the image inline and its file, and the log embedded (${echoed})`);
  await shotEl(A.page, `bx-frame[src="${TILE}"] .pop`, 'agent-tab-attachments');
  await waitFor(A.page, (t) => t.frameFor('apps/crawler')?.testApi().agent()?.status === 'idle', null, { timeout: 15000, label: 'idle after the attachments' });
  // …and from the tab's own message box (the chat kit's composer, D224): a
  // file staged there goes with the message — the session API took
  // attachments all along, the tab never offered them
  await fr(A.page, TILE, (f) => { const a = f.agent(); a.attach([{ name: 'notes.txt', text: 'remember the milk\n', mime: 'text/plain' }]); a.setDraft('and this one?'); });
  await waitFor(A.page, (t) => (t.frameFor('apps/crawler')?.testApi().agent()?.staged || []).length === 1, null, { timeout: 5000, label: 'the file is staged in the message box' });
  check(await A.page.locator(`bx-frame[src="${TILE}"] bx-agent .bxc-staged .bxc-qchip`).count() === 1 && await A.page.locator(`bx-frame[src="${TILE}"] bx-agent .bxc-compose .bxc-attach`).count() === 1,
    'the message box has an attach button, and shows the staged file as a chip');
  await fr(A.page, TILE, (f) => f.agent().sendBox());
  await waitFor(A.page, (t) => (t.frameFor('apps/crawler')?.testApi().agent()?.blocks || []).some((b) => b.kind === 'msg' && /\[resource notes\.txt \d+B\]/.test(b.text || '')), null, { timeout: 15000, label: 'the agent got the file staged in the message box' });
  const fromBox = (await blocks(A.page)).filter((b) => b.kind === 'msg' && b.role === 'user' && b.files).pop();
  const boxAfter = await fr(A.page, TILE, (f) => ({ staged: f.agent().staged.length, draft: f.agent().draft }));
  check(fromBox?.text === 'and this one?' && fromBox.files.map((f) => f.name).join(' ') === 'notes.txt' && boxAfter.staged === 0 && boxAfter.draft === '',
    `a message sent from the box carries its file; the box is empty again (${JSON.stringify(fromBox?.files)} ${JSON.stringify(boxAfter)})`);
  await waitFor(A.page, (t) => t.frameFor('apps/crawler')?.testApi().agent()?.status === 'idle', null, { timeout: 15000, label: 'idle after the message with a file' });
  // the usage badge (D224): the share of the context in use, where the tab said "42/1 000 tokens"
  const usage = await fr(A.page, TILE, (f) => f.agent().usage);
  check(usage && usage.text === 'ctx 4%' && usage.pct === 4 && /42 of 1 000 tokens of context/.test(usage.title), `the status line says how full the context is (${JSON.stringify(usage)})`);

  // ---- plan approval: a plan card, never a session rule ----
  const agentSel = `bx-frame[src="${TILE}"] bx-agent`;
  const pendingPlan = async (label) => {
    await waitFor(A.page, (t) => (t.frameFor('apps/crawler')?.testApi().agent()?.pending || []).some((p) => p.plan), null, { timeout: 15000, label });
    return (await fr(A.page, TILE, (f) => f.agent().pending)).find((p) => p.plan);
  };
  await fr(A.page, TILE, (f) => f.agent().send('plan one'));
  const plan1 = await pendingPlan('the plan approval is pending');
  check(plan1.scoped === false, `a plan approval is never scoped to a session rule (${JSON.stringify(plan1)})`);
  const card = A.page.locator(`${agentSel} .bxc-plan-card`).last();
  const cardText = await card.innerText();
  check(await card.locator('.bxc-plan-md h1').count() === 1 && await card.locator('.bxc-plan-md strong').count() >= 1, 'the plan renders as markdown');
  check(/Ready to code\?/.test(cardText) && !/"planFilePath"|"plan":/.test(cardText) && await card.locator('.bxc-rulenote').count() === 0,
    'the card is headed "Ready to code?" — no raw JSON, no "allow for the session" note');
  check(await card.locator('.bxc-btns button').count() === 3 && await card.locator('button.bxc-primary').count() === 1 && await card.locator('button.bxc-deny').count() === 1,
    'every option shows (three approvals, the first primary, one keep-planning)');
  await shotEl(A.page, `bx-frame[src="${TILE}"] .pop`, 'agent-tab-plan');
  // an option that takes the agent past its own asks (`explicit`, D225) is
  // drawn as a caution and confirmed before it is sent (D224): the window's
  // own dialog — Cancel sends nothing, Allow it sends the option
  const RAISE = 'exit-plan-clear-auto';
  const optButtons = await fr(A.page, TILE, (f, t, p) => f.agent().optionButtons(p), plan1.pid);
  check(plan1.explicit.includes(RAISE) && optButtons.find((o) => o.id === RAISE)?.explicit && optButtons.filter((o) => o.explicit).length === plan1.explicit.length,
    `the option that raises the mode is marked (${JSON.stringify(plan1.explicit)}; ${JSON.stringify(optButtons)})`);
  const dialog = A.page.locator(`bx-frame[src="${TILE}"] bx-dialog`);
  const permPosts = []; // every answer this page sends (a refused confirmation must send none)
  const onReq = (r) => { if (r.method() === 'POST' && /\/permissions\//.test(r.url())) permPosts.push(r.url()); };
  A.page.on('request', onReq);
  await fr(A.page, TILE, (f, t, p) => f.agent().choose(p.pid, p.opt), { pid: plan1.pid, opt: RAISE });
  await dialog.waitFor({ timeout: 5000 });
  check(await dialog.getByText(/Yes, clear context .* then stops asking before it edits files or runs commands/).count() === 1 && await dialog.getByText('Let the agent stop asking?').count() === 1,
    'choosing it asks first, in the window\'s dialog: what the option is, and what the agent then does');
  await dialog.locator('button', { hasText: 'Cancel' }).click();
  await dialog.waitFor({ state: 'detached', timeout: 5000 });
  await A.page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)))); // the click's own continuation has run
  check(permPosts.length === 0 && (await fr(A.page, TILE, (f) => f.agent().pending)).some((p) => p.pid === plan1.pid), `Cancel sends nothing: the request still waits (${permPosts.length} answers sent)`);
  await fr(A.page, TILE, (f, t, p) => f.agent().choose(p.pid, p.opt), { pid: plan1.pid, opt: RAISE });
  await dialog.locator('button', { hasText: 'Allow it' }).click();
  await waitFor(A.page, (t, pid) => !(t.frameFor('apps/crawler')?.testApi().agent()?.pending || []).some((p) => p.pid === pid), plan1.pid, { timeout: 15000, label: 'the confirmed option was sent' });
  A.page.off('request', onReq);
  check(permPosts.length === 1, `Allow it sends the option, once (${permPosts.length})`);
  await waitFor(A.page, (t) => (t.frameFor('apps/crawler')?.testApi().agent()?.blocks || []).some((b) => b.kind === 'msg' && /plan approved: exit-plan-clear-auto/.test(b.text || '')), null, { timeout: 15000, label: 'the plan was approved with the confirmed option' });
  await waitFor(A.page, (t) => t.frameFor('apps/crawler')?.testApi().agent()?.status === 'idle', null, { timeout: 15000, label: 'idle after the plan' });
  // the same plan again: the option chosen was allow_always — a mode,
  // not "remember" — so it must be asked again, not approved unseen
  await fr(A.page, TILE, (f) => f.agent().send('plan two'));
  const plan2 = await pendingPlan('the second plan is asked again');
  const autoAnswered = (await blocks(A.page)).filter((b) => b.kind === 'perm' && b.plan && b.by === 'auto');
  check(plan2.pid !== plan1.pid && autoAnswered.length === 0, `the second plan waits for an answer (${plan2.pid}; auto-answered: ${autoAnswered.length})`);
  // keep planning, with feedback: the reject ends the turn, then the feedback goes in
  await fr(A.page, TILE, (f, t, p) => f.agent().rejectPlan(p, 'reject', 'make it shorter'), plan2.pid);
  await waitFor(A.page, (t) => (t.frameFor('apps/crawler')?.testApi().agent()?.blocks || []).some((b) => b.kind === 'msg' && b.role !== 'user' && /echo: make it shorter/.test(b.text || '')), null, { timeout: 20000, label: 'the plan feedback was sent as the next message' });
  const kept = A.page.locator(`${agentSel} .bxc-plan-card.bxc-settled`).last();
  check(/Kept planning/.test(await kept.innerText()), 'the rejected plan settles as "Kept planning"');

  // ---- thinking: open while it streams, then folded to its duration ----
  await fr(A.page, TILE, (f) => f.agent().send('think it over'));
  const openThought = A.page.locator(`${agentSel} details.bxc-thought[open]`);
  await openThought.first().waitFor({ timeout: 15000 });
  check(/Thinking…/.test(await openThought.first().locator('summary').innerText()), 'a streaming thought is open, marked "Thinking…"');
  await shotEl(A.page, `bx-frame[src="${TILE}"] .pop`, 'agent-tab-thinking');
  await waitFor(A.page, (t) => (t.frameFor('apps/crawler')?.testApi().agent()?.blocks || []).some((b) => b.kind === 'msg' && /thought it through/.test(b.text || '')), null, { timeout: 15000, label: 'the thinking turn answered' });
  await waitFor(A.page, (t) => t.frameFor('apps/crawler')?.testApi().agent()?.status === 'idle', null, { timeout: 15000, label: 'idle after thinking' });
  const th = (await blocks(A.page)).filter((b) => b.kind === 'thought').pop();
  const lastThought = A.page.locator(`${agentSel} details.bxc-thought`).last();
  const summary = await lastThought.locator('summary').innerText();
  check(th && th.done && th.ms >= 1000 && !(await lastThought.evaluate((el) => el.open)) && /Thought for \d+s/.test(summary),
    `the finished thought folds to its duration (${summary}, ${th && th.ms} ms)`);

  // ---- a shell write: a readable headline, the output, the real diff ----
  await fr(A.page, TILE, (f, t, stamp) => f.agent().send(`run: echo hello-${stamp} | tee made-by-agent.txt`), Date.now());
  await waitFor(A.page, (t) => (t.frameFor('apps/crawler')?.testApi().agent()?.blocks || []).some((b) => b.kind === 'changes' && b.files.includes('made-by-agent.txt')), null, { timeout: 20000, label: "the turn's changed files" });
  const wrote = (await blocks(A.page)).filter((b) => b.kind === 'tool' && b.tk === 'execute').pop();
  check(wrote.headline === 'Write made-by-agent.txt' && /hello-/.test(wrote.output) && wrote.exitCode === 0,
    `the shell call reads as what it does, with its output (${JSON.stringify({ h: wrote.headline, out: wrote.output, exit: wrote.exitCode })})`);
  check((wrote.files || []).includes('made-by-agent.txt'), `the call's snapshot diff names the file (${JSON.stringify(wrote.files)})`);
  const wroteCard = A.page.locator(`${agentSel} details.bxc-tool.bxc-exec`).last();
  await wroteCard.locator(':scope > summary').click();
  await wroteCard.locator('details.bxc-files > summary').click();
  // a folded card's body renders once opened (D124), a file's diff once its row is (D210): wait for it
  await wroteCard.locator('details.bxc-frow > summary').first().click();
  await wroteCard.locator('pre.bxc-diff .d').first().waitFor({ timeout: 5000 }).catch(() => {});
  check(await wroteCard.locator('pre.bxc-diff .d').count() >= 1, 'the card shows the file\'s diff (+ lines)');
  await shotEl(A.page, `bx-frame[src="${TILE}"] .pop`, 'agent-tab-shell-write');

  // ---- a subagent: what it does nests under its Task card ----
  await fr(A.page, TILE, (f) => f.agent().send('subagent please'));
  await waitFor(A.page, (t) => (t.frameFor('apps/crawler')?.testApi().agent()?.blocks || []).some((b) => b.kind === 'msg' && /the subagent found it/.test(b.text || '')), null, { timeout: 15000, label: 'the subagent turn answered' });
  const top = await blocks(A.page);
  const task = top.filter((b) => b.kind === 'tool' && b.id === 'task1').pop();
  const kids = (task && task.children) || [];
  check(kids.some((c) => c.kind === 'thought' && c.done) && kids.some((c) => c.kind === 'tool' && c.id === 'read1') && kids.some((c) => c.kind === 'msg' && /main starts/.test(c.text || '')),
    `the subagent's thought, call and text are its children (${JSON.stringify(kids)})`);
  check(!top.some((b) => b.kind === 'tool' && b.id === 'read1') && !top.some((b) => b.kind === 'msg' && /main starts/.test(b.text || '')), 'nothing of the subagent leaks to the top level');
  const subCard = A.page.locator(`${agentSel} details.bxc-tool.bxc-sub`).last();
  check(!(await subCard.evaluate((el) => el.open)), 'the finished subagent folds');
  await subCard.locator(':scope > summary').click();
  await subCard.locator('.bxc-answer').waitFor({ timeout: 5000 }).catch(() => {});
  check(await subCard.locator('.bxc-children details.bxc-tool').count() === 1 && /main\.go/.test(await subCard.locator('.bxc-answer').innerText()), 'opened: the nested call and the answer show');
  await shotEl(A.page, `bx-frame[src="${TILE}"] .pop`, 'agent-tab-subagent');

  // ---- slash commands: "/" offers the agent's commands; Tab completes ----
  await waitFor(A.page, (t) => (t.frameFor('apps/crawler')?.testApi().agent()?.commands || []).includes('review'), null, { timeout: 10000, label: "the agent's slash commands" });
  const ta = A.page.locator(`${agentSel} .bxc-compose textarea`);
  await ta.click();
  await A.page.keyboard.type('/re');
  await settle(A.page); // the tab renders once a frame (D124)
  const menu = await fr(A.page, TILE, (f) => f.agent().slashMenu);
  check(menu[0] === 'review' && await A.page.locator(`${agentSel} .bxc-slash .bxc-sc`).count() === menu.length, `"/re" offers /review first (${JSON.stringify(menu)})`);
  await shotEl(A.page, `bx-frame[src="${TILE}"] .pop`, 'agent-tab-slash');
  await A.page.keyboard.press('Tab');
  const drafted = await fr(A.page, TILE, (f) => f.agent().draft);
  check(drafted === '/review ' && /what to focus on/.test(await A.page.locator(`${agentSel} .bxc-slash-hint`).innerText()), `Tab completes "/review " and shows its hint (${JSON.stringify(drafted)})`);
  await A.page.keyboard.type('tests');
  await A.page.keyboard.press('Enter');
  await waitFor(A.page, (t) => (t.frameFor('apps/crawler')?.testApi().agent()?.blocks || []).some((b) => b.kind === 'msg' && /echo: \/review tests/.test(b.text || '')), null, { timeout: 15000, label: 'the slash command went in as the prompt' });
  check(true, 'the completed command is sent as the prompt text');
  await waitFor(A.page, (t) => t.frameFor('apps/crawler')?.testApi().agent()?.status === 'idle', null, { timeout: 15000, label: 'idle after the slash command' });

  // ---- a question as a form ----
  await fr(A.page, TILE, (f) => f.agent().send('ask me something'));
  await waitFor(A.page, (t) => (t.frameFor('apps/crawler')?.testApi().agent()?.questions || []).length === 1, null, { timeout: 15000, label: 'the question is pending' });
  const qs = (await fr(A.page, TILE, (f) => f.agent().questions))[0];
  check(qs.fields.map((f) => `${f.key}:${f.kind}:${f.other}`).join(' ') === 'question_0:radio:question_0_custom question_1:check:question_1_custom',
    `the form reads single- and multi-select questions, each with its Other box (${JSON.stringify(qs.fields)})`);
  const ask = A.page.locator(`${agentSel} .bxc-perm.bxc-ask`).last();
  await ask.locator('label.bxc-opt', { hasText: 'SQLite' }).click();
  await ask.locator('label.bxc-opt', { hasText: 'Metrics' }).click();
  await ask.locator('.bxc-field').nth(1).locator('input.bxc-other').fill('Admin UI');
  await shotEl(A.page, `bx-frame[src="${TILE}"] .pop`, 'agent-tab-question');
  await ask.locator('button.bxc-primary').click();
  await waitFor(A.page, (t) => (t.frameFor('apps/crawler')?.testApi().agent()?.blocks || []).some((b) => b.kind === 'msg' && /^answers:/.test(b.text || '')), null, { timeout: 15000, label: 'the agent got the answers' });
  const got = (await blocks(A.page)).filter((b) => b.kind === 'msg' && /^answers:/.test(b.text || '')).pop().text;
  const sent = JSON.parse(got.replace(/^answers:\s*/, ''));
  check(sent.question_0 === 'SQLite' && JSON.stringify(sent.question_1) === '["Metrics"]' && sent.question_1_custom === 'Admin UI' && !('question_0_custom' in sent),
    `the agent got exactly the form's values (${got})`);
  const settledAsk = A.page.locator(`${agentSel} .bxc-perm.bxc-ask.bxc-settled`).last();
  check(/answered by/.test(await settledAsk.innerText()) && /SQLite/.test(await settledAsk.innerText()), 'the question settles showing the answers');
  await waitFor(A.page, (t) => t.frameFor('apps/crawler')?.testApi().agent()?.status === 'idle', null, { timeout: 15000, label: 'idle after the question' });

  // ---- an agent tab is a tab of the IDE window (D202): the tools, a file beside it ----
  const bar = A.page.locator(`bx-frame[src="${TILE}"] ${PICKERS}`);
  const agentEl = A.page.locator(`bx-frame[src="${TILE}"] bx-agent`).first();
  const tools = await A.page.locator(`bx-frame[src="${TILE}"] .tools button.tool .tl`).allTextContents();
  check(['Files', 'Changes', 'PRs', 'Logs'].every((t) => tools.includes(t)) && await bar.locator('select.scope').count() >= 2,
    `the agent's window has the tool strip (${tools.join(', ')}) and the status bar its net/API pickers`);
  await A.page.locator(`bx-frame[src="${TILE}"] .tools button.tool`, { hasText: 'Files' }).click();
  await waitSel(A.page, `bx-frame[src="${TILE}"] .side bx-code-tree`, { timeout: 10000 });
  check(await fr(A.page, TILE, (f) => f.layout) === 'code' && await agentEl.isVisible(), 'Files opens the file list beside the agent, which stays in view');
  // a file opens in the agent's pane; dragged to its right edge it sits beside the agent
  await fr(A.page, TILE, (f) => f.ide.openFile('index.html'));
  await waitSel(A.page, `bx-frame[src="${TILE}"] bx-code-view`, { timeout: 10000 });
  check(!(await agentEl.isVisible()), 'the file is a tab in the agent\'s pane: the agent is behind it');
  await fr(A.page, TILE, (f) => { const p = f.ide.panes[0]; f.ide.move('f:index.html', p.id, 'r'); });
  await A.page.waitForTimeout(250);
  const sides = await sh(A.page, (t, src) => {
    const root = t.frameFor(src).renderRoot, box = (sel) => { const r = root.querySelector(sel)?.getBoundingClientRect(); return r && r.width ? [Math.round(r.left), Math.round(r.right)] : null; };
    return { agent: box('bx-agent'), file: box('bx-code-view'), panes: t.frameFor(src).testApi().ide.panes.length };
  }, TILE);
  check(sides.panes === 2 && sides.agent && sides.file && sides.agent[1] <= sides.file[0] + 2, `the agent and the file side by side (${JSON.stringify(sides)})`);
  const vd = A.page.locator(`bx-frame[src="${TILE}"] .vd`).first();
  check(await vd.getAttribute('role') === 'separator' && await vd.getAttribute('tabindex') === '0', 'the divider between them is a focusable separator');
  check(await bar.locator('.who .lbl').textContent() !== '' && await bar.locator('select.scope').count() >= 2, 'the status bar still speaks for the agent');
  await shotEl(A.page, `bx-frame[src="${TILE}"] .pop`, 'agent-tab-split');
  await fr(A.page, TILE, (f) => { f.ide.close('f:index.html'); f.ide.openTool('files'); });
  await A.page.waitForTimeout(200);
  check(await fr(A.page, TILE, (f) => f.ide.panes.length === 1 && !f.ide.side) && await agentEl.isVisible(), 'the file closed: one pane, the agent alone again');

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
  // the login command is typed into the new shell (once: the tab drops it
  // after — termRun checks that — so look at the screen, not the attribute)
  const typed = await A.page.locator(`bx-frame[src="${TILE}"] bx-terminal`).evaluateAll(async (els, cmd) => {
    for (let i = 0; i < 250; i++) {
      for (const el of els) {
        const t = el.testApi?.();
        if (!t) continue;
        for (let row = 0; row < 40; row++) if ((t.screenLine(row) || '').includes(cmd)) return true;
      }
      await new Promise((r) => setTimeout(r, 40));
    }
    return false;
  }, lg.command);
  check(typed, `the shell tab runs the login command (${lg.command})`);

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
  // …and it is asked for by its id every time from here on: a tab's position
  // is only good within one look at the tabs (a listing between two looks
  // re-sorts them, and index i is then another tab — this pass once waited
  // 15 s for "idle" on the ended tab that had moved into the resumed one's
  // place, on a machine slow enough for a listing to land in between)
  const resumedId = await fr(A.page, TILE, (f, t, a) => f.tabs.find((tb) => tb.kind === 'agent' && !tb.history && !tb.ended && tb.id && !a.known.includes(tb.id))?.id || null, { known: [idA, eagerId] });
  check(!!resumedId && resumedId !== eagerId, `resume replaced the read-only tab with a new live session (${resumedId})`);
  check((await fr(A.page, TILE, (f) => f.tabs)).every((tb) => !tb.history), 'the read-only tab was replaced, not duplicated');
  // The wait records what it sees, each time it changes — the resumed tab's
  // status and error, how many blocks it has, and every agent tab the frame
  // lists — and prints that history if "idle" never comes: this wait timed
  // out once at a load nobody could reproduce, with nothing to say why.
  try {
    await waitFor(A.page, (t, id) => {
      const api = t.frameFor('apps/crawler')?.testApi();
      const i = api ? api.tabs.findIndex((tb) => tb.id === id) : -1;
      const ag = i >= 0 ? api.agent(i) : null;
      const now = { tab: i, status: ag?.status ?? null, error: ag?.error || '', blocks: ag?.blocks?.length ?? null, hidden: ag?.hidden ?? null,
        tabs: (api?.tabs || []).filter((tb) => tb.kind === 'agent').map((tb) => `${tb.id}${tb.ended ? ' ended' : ''}${tb.history ? ' history' : ''}`).join(', ') };
      const tr = (window.resumeTrace ||= { t0: performance.now(), rows: [] });
      const line = JSON.stringify(now);
      if (tr.last !== line) { tr.last = line; tr.rows.push(`+${Math.round(performance.now() - tr.t0)}ms ${line}`); }
      return now.status === 'idle';
    }, resumedId, { timeout: 15000, label: 'the resumed session is idle' });
  } catch (e) {
    const rows = await A.page.evaluate(() => window.resumeTrace?.rows || []).catch((err) => [`(the page would not say: ${err.message.split('\n')[0]})`]);
    log(`the resumed session ${resumedId} never read idle; what the tab showed, each time it changed:`);
    for (const r of rows) log(`  ${r}`);
    throw e;
  }
  const resumedBlocks = await fr(A.page, TILE, (f, t, id) => f.agent(f.tabs.findIndex((tb) => tb.id === id)).blocks, resumedId);
  check(resumedBlocks.some((b) => b.kind === 'msg' && b.role === 'user' && /resumed fake-1/.test(b.text)), `resume replayed the earlier turns into the live session (${resumedBlocks.length} blocks)`);
  await shotEl(A.page, `bx-frame[src="${TILE}"] .pop`, 'agent-tab-resumed');

  // ---- (l) a picker change restarts the agent and resumes the conversation ----
  await fr(A.page, TILE, (f, t, id) => f.setActiveTab(f.tabs.findIndex((tb) => tb.id === id)), resumedId);
  const netSel = A.page.locator(`bx-frame[src="${TILE}"] ${PICKERS} select.scope`).first();
  check((await netSel.locator('option').allTextContents()).some((o) => /offline/.test(o)), 'the agent tab offers the network scopes');
  await netSel.selectOption('none');
  await A.page.locator('bx-dialog button', { hasText: 'Restart' }).click();
  await waitFor(A.page, (t, old) => { const ag = t.frameFor('apps/crawler')?.testApi().agent(); return !!ag && !!ag.sessionId && ag.sessionId !== old && ag.status === 'idle'; }, resumedId,
    { timeout: 20000, label: 'the agent restarted in a new sandbox' });
  const restarted = await fr(A.page, TILE, (f) => ({ tab: f.tabs[f.activeTab], blocks: f.agent().blocks }));
  check(restarted.tab.net === 'none' && restarted.tab.kind === 'agent' && !restarted.tab.ended, `the restarted tab runs offline (${JSON.stringify(restarted.tab)})`);
  check(restarted.blocks.some((b) => b.kind === 'msg' && b.role === 'user' && /resumed/.test(b.text || '')), 'the conversation resumed in the new session (the agent replayed it)');
  check((await fr(A.page, TILE, (f) => f.tabs)).filter((tb) => tb.kind === 'agent' && tb.ended && tb.id === resumedId).length === 0, 'no ghost tab for the old session');
  await shotEl(A.page, `bx-frame[src="${TILE}"] .pop`, 'agent-tab-restarted');

  // ---- (m) the chat kit's views in a DOCUMENT, with a plain ChatCtx (D224) ----
  // What no agent sends this tab today (xbind does not advertise url
  // questions; the scripted agent's form has no defaults), the kinds only
  // another host emits (a draft, a sub-conversation, a host's own card), and
  // the roles the tab does not have (a participant, a viewer) — drawn by the
  // same functions the tab uses, in the shell's document: no shadow root, no
  // <bx-agent>, the kit's one stylesheet in a <style>.
  const kit = await A.page.evaluate(async () => {
    const { blockView, usageView } = await import('/vendor/chat/views.js');
    const { block, newToolRecord } = await import('/vendor/chat/blocks.js');
    const { chatCssText } = await import('/vendor/chat/css.js');
    const { render, html } = await import('/vendor/lit-all.min.js');
    await import('/vendor/chat/composer.js');
    const root = document.createElement('div');
    root.id = 'kit-probe';
    root.innerHTML = `<style>${chatCssText}</style>`;
    document.body.append(root);
    const log = [];
    const open = new Set();
    let confirmAnswer = false;
    const mk = (more = {}) => ({
      running: false, ended: false, agentName: 'Fable', role: 'owner',
      isOpen: (b, slot, dflt) => !!dflt || open.has(b.k + ':' + slot), setOpen: (b, slot, o) => { if (o) open.add(b.k + ':' + slot); },
      permit: async (b, ans) => { log.push(['permit', b.pid, ans]); return true; },
      answer: async (b, ans) => { log.push(['answer', b.eid, ans]); return true; },
      confirm: async (text) => { log.push(['confirm', text]); return confirmAnswer; },
      host: (b) => (b.type === 'session' ? html`<b class="mine">session ${b.data.id}</b>` : null),
      signin: () => html`<button class="mine">Sign in</button>`,
      repaint: () => {}, ...more,
    });
    const draw = (b, ctx = mk()) => { const el = document.createElement('div'); root.append(el); render(blockView(b, ctx), el); return el; };
    const tick = () => new Promise((r) => setTimeout(r, 30));
    const out = {};
    const ask = (more) => block('ask', 'q' + Math.random(), { eid: 'e-url', callId: '', message: 'Open the page', mode: 'form', schema: null, url: '', action: null, by: null, content: null, ...more });

    // a question that is a page to open
    const url = draw(ask({ mode: 'url', url: 'https://example.test/device' }));
    const link = url.querySelector('a');
    out.url = { href: link && link.getAttribute('href'), target: link && link.target, rel: link && link.rel, buttons: [...url.querySelectorAll('button')].map((x) => x.textContent.trim()), inputs: url.querySelectorAll('input').length };
    url.querySelector('button').click();
    await tick();
    out.urlSent = log.splice(0);
    const bad = draw(ask({ mode: 'url', url: 'javascript:alert(1)' }));
    out.bad = { links: bad.querySelectorAll('a').length, text: bad.textContent.includes('javascript:alert(1)') };
    out.settled = draw(ask({ mode: 'url', url: 'https://example.test/device', action: 'complete', by: 'agent' })).textContent.replace(/\s+/g, ' ').trim();
    out.text = draw(ask({ mode: 'text', message: 'Which **branch**?' })).textContent.replace(/\s+/g, ' ').trim();

    // a form: anyOf, enumNames, defaults, a required field
    const formBlock = ask({ eid: 'e-form', message: 'Which?', schema: { type: 'object', required: ['name'], properties: {
      db: { title: 'DB', anyOf: [{ const: 'pg', title: 'Postgres' }, { const: 'sqlite', title: 'SQLite' }], default: 'sqlite' },
      region: { type: 'string', enum: ['eu', 'us'], enumNames: ['Europe', 'United States'] },
      port: { type: 'integer', title: 'Port', default: 8080 }, name: { type: 'string', title: 'Name' } } } });
    const form = draw(formBlock);
    await form.querySelector('bx-chat-form').updateComplete;
    out.form = { radios: [...form.querySelectorAll('input[type=radio]')].map((x) => x.checked), labels: [...form.querySelectorAll('.bxc-opt b')].map((x) => x.textContent), port: form.querySelector('input[type=number]').value };
    form.querySelector('button.bxc-primary').click();
    await tick();
    out.form.missing = form.querySelector('.bxc-err')?.textContent || '';
    out.form.sentEarly = log.splice(0).length;
    const name = form.querySelector('[data-field="name"] input');
    name.value = 'ledger'; name.dispatchEvent(new Event('input', { bubbles: true }));
    form.querySelector('button.bxc-primary').click();
    await tick();
    out.form.answer = log.splice(0)[0];
    const viewerForm = draw(formBlock, mk({ role: 'viewer' }));
    await viewerForm.querySelector('bx-chat-form').updateComplete;
    out.form.viewer = { buttons: viewerForm.querySelectorAll('button').length, note: /view only/.test(viewerForm.textContent), disabled: [...viewerForm.querySelectorAll('input')].every((x) => x.disabled) };

    // a permission request with an option that stops the agent asking
    const perm = () => block('perm', 'p' + Math.random(), { pid: 'p9', callId: 't1', tool: { title: 'rm -rf build', kind: 'execute', name: 'Bash', command: 'rm -rf build', rawInput: { command: 'rm -rf build' } },
      options: [{ optionId: 'once', name: 'Allow', kind: 'allow_once' }, { optionId: 'bypass', name: 'Yes, and stop asking', kind: 'allow_always', explicit: true }, { optionId: 'no', name: 'Deny', kind: 'reject_once' }],
      rule: null, description: '', defaultToNo: false, plan: null, by: null, optionId: null });
    const owner = draw(perm());
    const btn = (el, id) => el.querySelector(`button[data-option="${id}"]`);
    out.perm = { owner: [...owner.querySelectorAll('button[data-option]')].map((x) => `${x.dataset.option}${x.classList.contains('bxc-explicit') ? '!' : ''}${x.classList.contains('bxc-primary') ? '*' : ''}`) };
    confirmAnswer = false; btn(owner, 'bypass').click(); await tick();
    out.perm.refused = log.splice(0);
    confirmAnswer = true; btn(owner, 'bypass').click(); await tick();
    out.perm.confirmed = log.splice(0);
    btn(owner, 'once').click(); await tick();
    out.perm.plain = log.splice(0);
    const part = draw(perm(), mk({ role: 'participant' }));
    out.perm.participant = { buttons: [...part.querySelectorAll('button[data-option]')].map((x) => x.dataset.option), note: part.querySelector('.bxc-note')?.textContent.replace(/\s+/g, ' ').trim() || '' };
    const viewer = draw(perm(), mk({ role: 'viewer' }));
    out.perm.viewer = { buttons: viewer.querySelectorAll('button[data-option]').length, note: /view only/.test(viewer.textContent) };

    // the kinds only another host emits
    const rec = { ...newToolRecord('sub1'), kind: 'read', title: 'Read a.go', status: 'completed', text: 'package a' };
    const kinds = [
      block('draft', 'd1', { text: 'Writing **now**' }),
      block('agent', 'a1', { title: 'Bug hunt', ref: 7, state: 'approval', phase: 'reading', steps: 2, task: 'find the bug', answer: '', provider: 'Claude Code',
        blocks: [block('assistant', 'a1m', { text: 'looking' }), block('tool', 'a1t', { rec, state: 'done', children: null })], tail: [perm()] }),
      block('host', 'h1', { type: 'session', data: { id: 's-42' } }),
      block('host', 'h2', { type: 'unknown', title: 'A card of a newer host' }),
      block('signin', 'si', { provider: 'Codex', command: 'codex login' }),
      block('notice', 'n1', { text: 'The session moved\nto a new base' }),
      block('user', 'u1', { text: 'hi', sender: 'bob', files: [{ name: 'plan.md', path: 'notes/plan.md', sizeText: '2 KB' }] }),
    ];
    out.kinds = {};
    for (const b of kinds) {
      try { const el = draw(b, mk({ me: 'alice' })); out.kinds[b.k] = { cls: el.firstElementChild?.className || '', text: el.textContent.replace(/\s+/g, ' ').trim().slice(0, 160), mine: el.querySelectorAll('.mine').length }; } catch (e) { out.kinds[b.k] = { error: String(e.message || e) }; }
    }
    out.agentKids = root.querySelectorAll('.bxc-agent .bxc-children > *').length;

    // the usage badge
    const u = document.createElement('div'); root.append(u);
    render(html`${usageView({ used: 52000, size: 200000, cost: { amount: 0.41, currency: 'USD' } })}${usageView({ used: 184000, size: 200000 })}${usageView({ used: 12400 })}`, u);
    out.usage = [...u.querySelectorAll('.bxc-usage')].map((x) => `${x.textContent}|${x.className.replace('bxc-usage', '').trim()}|${x.title}`);

    // the composer, on its own: files staged, a limit said, what Send hands the host
    const c = document.createElement('bx-chat-composer');
    c.attach = true; c.limits = { files: 2, bytes: 16, total: 24 };
    root.append(c);
    await c.updateComplete;
    c.addFiles([new File(['hello'], 'a.txt', { type: 'text/plain' }), new File(['x'.repeat(40)], 'big.bin'), new File(['wor'], 'b.txt', { type: 'text/plain' }), new File(['z'], 'c.txt')]);
    await c.updateComplete;
    out.composer = { staged: c.files.map((f) => f.name), note: c.querySelector('.bxc-staged .bxc-err')?.textContent || '', chips: c.querySelectorAll('.bxc-staged .bxc-qchip').length, attachButton: !!c.querySelector('.bxc-attach') };
    const sentP = new Promise((r) => c.addEventListener('bx-chat-send', (e) => r(e.detail), { once: true }));
    c.draft = 'with files'; await c.updateComplete;
    c.send();
    const d = await sentP;
    out.composer.sent = { text: d.text, files: d.files.map((f) => `${f.name}:${f.mime}:${f.size}:${atob(f.data)}`), interrupt: d.interrupt };
    out.composer.keptUntilSent = c.files.length;
    d.sent(); await c.updateComplete;
    out.composer.afterSent = c.files.length;
    out.styled = getComputedStyle(root.querySelector('.bxc-perm')).borderTopStyle; // the <style> in the document reaches the cards
    root.remove();
    return out;
  });
  check(kit.url.href === 'https://example.test/device' && kit.url.target === '_blank' && /noopener/.test(kit.url.rel) && kit.url.inputs === 0, `a url question is its link, opened in a new tab (${JSON.stringify(kit.url)})`);
  check(kit.url.buttons.join('|') === 'Done|Skip' && JSON.stringify(kit.urlSent) === '[["answer","e-url",{"action":"accept","content":{}}]]', `…with Done (an accept without content) and Skip (${JSON.stringify(kit.urlSent)})`);
  check(kit.bad.links === 0 && kit.bad.text, 'a url that is not http(s) is text, never a link');
  check(/done by agent/.test(kit.settled), `a url question the agent completed says so (${kit.settled})`);
  check(/Which branch\?/.test(kit.text) && /next message answers/.test(kit.text), `a question in words says the next message answers it (${kit.text})`);
  check(JSON.stringify(kit.form.radios) === '[false,true,false,false]' && kit.form.port === '8080', `a form draws its schema's defaults chosen — an anyOf choice, a number (${JSON.stringify(kit.form.radios)} ${kit.form.port})`);
  check(kit.form.labels.join('|') === 'Postgres|SQLite|Europe|United States', `anyOf titles and enumNames label the choices (${kit.form.labels})`);
  check(/Answer Name first/.test(kit.form.missing) && kit.form.sentEarly === 0, `Submit without a required answer says which, and sends nothing (${kit.form.missing})`);
  check(JSON.stringify(kit.form.answer) === '["answer","e-form",{"action":"accept","content":{"db":"sqlite","port":8080,"name":"ledger"}}]', `a form answers its defaults and what was typed (${JSON.stringify(kit.form.answer)})`);
  check(kit.form.viewer.buttons === 0 && kit.form.viewer.note && kit.form.viewer.disabled, `a viewer sees the form without its buttons (${JSON.stringify(kit.form.viewer)})`);
  check(kit.perm.owner.join(' ') === 'no bypass! once*', `an option that stops the agent asking is drawn as a caution, never the primary one; the rejection left, the primary right (D250) (${kit.perm.owner})`);
  check(kit.perm.refused.length === 1 && kit.perm.refused[0][0] === 'confirm' && /Yes, and stop asking: Fable then stops asking/.test(kit.perm.refused[0][1]), `choosing it asks first — refused, nothing is sent (${JSON.stringify(kit.perm.refused)})`);
  check(kit.perm.confirmed.length === 2 && kit.perm.confirmed[1][0] === 'permit' && kit.perm.confirmed[1][2].optionId === 'bypass' && kit.perm.confirmed[1][2].verdict === 'allow', `confirmed, it is sent (${JSON.stringify(kit.perm.confirmed)})`);
  check(kit.perm.plain.length === 1 && kit.perm.plain[0][0] === 'permit' && kit.perm.plain[0][2].optionId === 'once', 'an ordinary option is sent without a question');
  check(kit.perm.participant.buttons.join(' ') === 'no once' && /One option that would stop Fable asking/.test(kit.perm.participant.note), `a participant is not offered it, and is told one is hidden (${JSON.stringify(kit.perm.participant)})`);
  check(kit.perm.viewer.buttons === 0 && kit.perm.viewer.note, 'a viewer sees the request without its answers');
  check(!Object.values(kit.kinds).some((k) => k.error), `every kind draws (${JSON.stringify(Object.fromEntries(Object.entries(kit.kinds).filter(([, v]) => v.error)))})`);
  check(/bxc-draft/.test(kit.kinds.d1.cls) && /Writing now/.test(kit.kinds.d1.text), `a draft is a message being written (${JSON.stringify(kit.kinds.d1)})`);
  check(/bxc-agent/.test(kit.kinds.a1.cls) && /Bug hunt/.test(kit.kinds.a1.text) && /needs approval/i.test(kit.kinds.a1.text) && kit.agentKids === 2, `a sub-conversation is a card with its blocks and what it waits on (${JSON.stringify(kit.kinds.a1)}, ${kit.agentKids} nested)`);
  check(kit.kinds.h1.mine === 1 && /session s-42/.test(kit.kinds.h1.text) && /A card of a newer host/.test(kit.kinds.h2.text), `a host's own card is drawn by the host, its title when the host draws none (${JSON.stringify([kit.kinds.h1, kit.kinds.h2])})`);
  check(/Not signed in to Codex/.test(kit.kinds.si.text) && kit.kinds.si.mine === 1, 'the sign-in card says so, and the host fills it');
  check(/^bob/.test(kit.kinds.u1.text) && /plan\.md/.test(kit.kinds.u1.text), `another person's message carries their name (${kit.kinds.u1.text})`);
  check(kit.usage.length === 3 && kit.usage[0] === 'ctx 26% · $0.41||52 000 of 200 000 tokens of context · $0.41' && /^ctx 92%\|bxc-bad\|/.test(kit.usage[1]) && /^12k tokens\|\|/.test(kit.usage[2]), `the usage badge: share of the context, cost, tint (${JSON.stringify(kit.usage)})`);
  check(kit.composer.staged.join(' ') === 'a.txt b.txt' && kit.composer.chips === 2 && /big\.bin is 40 B/.test(kit.composer.note) && /c\.txt: at most 2 files/.test(kit.composer.note) && kit.composer.attachButton,
    `the composer stages files and says what is past a limit (${JSON.stringify(kit.composer)})`);
  check(kit.composer.sent.text === 'with files' && kit.composer.sent.files.join(' ') === 'a.txt:text/plain:5:hello b.txt:text/plain:3:wor' && kit.composer.keptUntilSent === 2 && kit.composer.afterSent === 0,
    `Send hands the host the text and the files as base64, and keeps them until the host says they went (${JSON.stringify(kit.composer.sent)})`);
  check(kit.styled === 'solid', 'the kit\'s stylesheet in a document styles the cards (no shadow root needed)');

  // ---- (n) the highlighter is optional (D224): a page with no import map at all ----
  // The sign-in page has none, so /vendor/bx-code.js (a lit element behind a
  // bare `lit`) cannot load there — as on a tile page of a workspace whose
  // import map lacks it. The kit's views still load (they import by URL
  // only) and draw a diff plain, with the line classes the stylesheet styles.
  const bare = await browser.newContext();
  const barePage = await bare.newPage();
  const bareErrors = [];
  barePage.on('pageerror', (e) => bareErrors.push(e.message));
  await barePage.goto(`${URL}/login`);
  const plain = await barePage.evaluate(async () => {
    const out = { importMap: !!document.querySelector('script[type=importmap]') };
    out.code = await import('/vendor/bx-code.js').then(() => 'loaded', () => 'cannot load');
    const { blockView } = await import('/vendor/chat/views.js');
    const cards = await import('/vendor/chat/cards.js');
    const { block, newToolRecord } = await import('/vendor/chat/blocks.js');
    const { toolDiff } = await import('/vendor/chat/tools.js');
    const { chatCssText } = await import('/vendor/chat/css.js');
    const { render } = await import('/vendor/lit-all.min.js');
    await import('/vendor/chat/composer.js');
    await import('/vendor/chat/window.js');
    const root = document.createElement('div');
    root.innerHTML = `<style>${chatCssText}</style>`;
    document.body.append(root);
    let repaints = 0;
    const ctx = { running: false, ended: false, agentName: 'Fable', isOpen: () => true, setOpen() {}, permit() {}, answer() {}, repaint: () => { repaints++; } };
    const rec = { ...newToolRecord('e1'), kind: 'edit', title: 'Edit a.go', status: 'completed' };
    rec.diffs = [toolDiff({ type: 'diff', path: 'a.go', oldText: 'package a\nvar x = 1\n', newText: 'package a\nvar x = 2\n' }, rec)];
    const el = document.createElement('div'); root.append(el);
    render(blockView(block('tool', 't1', { rec, state: 'done', children: null }), ctx), el);
    await new Promise((r) => setTimeout(r, 100)); // (the import of bx-code.js was refused above: nothing arrives)
    const pre = el.querySelector('pre.bxc-diff');
    out.diff = pre ? { hljs: pre.classList.contains('hljs'), lines: [...pre.children].map((x) => x.className), text: pre.textContent,
      addBg: getComputedStyle(pre.querySelector('.d')).backgroundColor, delBg: getComputedStyle(pre.querySelector('.a')).backgroundColor } : null;
    out.epoch = cards.diffEpoch;
    out.chip = el.querySelector('.bxc-oc')?.textContent.trim();
    out.state = el.querySelector('.bxc-st')?.textContent.trim() || '';
    return out;
  });
  check(!plain.importMap && plain.code === 'cannot load', `a page without an import map cannot load the code highlighter (${plain.code})`);
  check(plain.diff && !plain.diff.hljs && plain.diff.lines.join(' ') === 'fh fh fh h ctx a d ctx' && /-var x = 1/.test(plain.diff.text) && /\+var x = 2/.test(plain.diff.text) && plain.epoch === 0,
    `the kit's views load there all the same and draw the diff plain, a class a line (${JSON.stringify(plain.diff)})`);
  check(plain.diff && plain.diff.addBg !== plain.diff.delBg && plain.diff.addBg !== 'rgba(0, 0, 0, 0)', `…tinted by the one stylesheet (${plain.diff?.addBg} / ${plain.diff?.delBg})`);
  check(plain.chip === '+1 −1' && plain.state === '' && bareErrors.length === 0, `the card's line says what it came to, and no state once it is done; no page errors (${plain.chip}; ${plain.state}; ${bareErrors.join(' | ')})`);
  await bare.close();

  await settle(A.page);
  // tidy: close both browsers, then end the sessions opened here and drop the
  // window pref — it would restore this window over the tile in the next
  // passes (menuOpen right-clicks the canvas right where it would sit)
  await closeCtx(A.ctx, A.page);
  await closeCtx(B.ctx, B.page);
  const T = await login(browser, 'admin', 'admin');
  for (const s of await (await T.ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json()) {
    await T.ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(s.id)}`);
  }
  await T.ctx.request.delete(`${URL}/api/xbin/prefs/term%3Aapps%3Acrawler`);
  await T.ctx.close();
  done();
}

module.exports = { agentTab };
