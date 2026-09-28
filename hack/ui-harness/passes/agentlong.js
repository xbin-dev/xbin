// hack/ui-harness/passes/agentlong.js — the Agent tab on a long transcript
// (D124, D130). The fake agent's `long N` script streams N units (a markdown
// message, an edit call with a diff, a thought every tenth). Checks: (a) only
// a window of the blocks is in the DOM and the view follows the bottom (the
// turn streams on a throttled CPU, so bursts land in one frame), and the far
// top of the log is let go; (b) scrolling near the top loads earlier rows
// and pages and the block the reader was looking at does not move by a
// pixel, down to the first block — while the tail is let go, and a pill
// offers the latest; (c) scrolled up mid-transcript, a new turn streams
// below: nothing moves, the pill counts it; a late event refolds its page
// without a jump; the pill brings the latest back, followed; (d) a hidden
// tab folds but does not render; a selection in a streaming message
// survives its next paragraphs; at the bottom the view follows and the
// rendered tail is trimmed; (e) a reload reads only the tail page and lands
// at the bottom; (f) the ended session's history view is paged too.
// Soft: long tasks while a turn streams over the long log (logged).
const { URL, login, closeCtx, fr, waitFor, openShell, usePersonalScreen, openTile, shotEl, checker, log, sleep } = require('../lib');

const TILE = 'apps/crawler';
const VIEW = { viewport: { width: 1400, height: 1300 } }; // agenttab.js: the window must fit
const N = 300;

async function agentLong(browser) {
  const { check, done } = checker('agent-long');
  const A = await login(browser, 'admin', 'admin', VIEW);
  const page = A.page;
  for (const s of await (await A.ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json()) {
    await A.ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(s.id)}`);
  }
  const openWindow = async () => {
    await openShell(page);
    await usePersonalScreen(page);
    await openTile(page, TILE);
    // the home screen's other tiles can push the card below the fold, and its
    // pop-up with it: bring the card to the top so the screenshots see it
    await page.locator(`.card[data-path="${TILE}"]`).evaluate((el) => el.scrollIntoView({ block: 'start' }));
    await fr(page, TILE, (f) => f.open('term'));
  };
  const agAt = (idx, fn, arg) => fr(page, TILE, (f, t, a) => new Function('ag', 'arg', `return (${a.src})(ag, arg)`)(f.agent(a.idx ?? undefined), a.arg),
    { src: fn.toString(), arg: arg ?? null, idx });
  const ag = (fn, arg) => agAt(null, fn, arg);
  const win = () => ag((a) => a.window);
  // wait for a condition on the active agent tab's test surface
  const until = (fn, arg, label, timeout = 30000) => waitFor(page, (t, a) => {
    const g = t.frameFor('apps/crawler')?.testApi().agent();
    return !!g && new Function('ag', 'arg', `return (${a.src})(ag, arg)`)(g, a.arg);
  }, { src: fn.toString(), arg: arg ?? null }, { timeout, label });

  await openWindow();
  await fr(page, TILE, (f) => f.startKind('agent', 'fake'));
  await waitFor(page, (t) => { const api = t.frameFor('apps/crawler')?.testApi(); const g = api && api.agent(api.tabs.length - 1); return !!g && g.status === 'idle'; }, null, { timeout: 15000, label: 'the agent is idle' });
  await fr(page, TILE, (f) => f.setActiveTab(f.tabs.length - 1));
  const agentTab = await fr(page, TILE, (f) => f.tabs.length - 1);

  // ---- (a) a long turn: a window in the DOM, following the bottom ----
  // on a throttled CPU, as on a busy machine: slow frames fold bursts of
  // events into one render, and the rendered tail must be trimmed even when
  // the last burst lands in one frame (it once stayed rendered whole)
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Emulation.setCPUThrottlingRate', { rate: 20 });
  const t0 = Date.now();
  await ag((a, n) => a.send(`long ${n}`), N);
  await until((g, n) => g.status === 'idle' && g.blocks.some((b) => b.text === `done: ${n} units`), N, 'the long turn finished', 120000);
  log(`agent-long: ${N} units streamed and rendered in ${Date.now() - t0} ms (CPU throttled 20×)`);
  await cdp.send('Emulation.setCPUThrottlingRate', { rate: 1 });
  await sleep(500);
  let w = await win();
  check(w.paged, 'this xbind pages the log');
  check(w.rendered > 0 && w.rendered <= 150, `only a window is rendered (${w.rendered} of ${w.total} loaded)`);
  check(w.atBottom && w.scrollHeight - w.scrollTop - w.clientHeight < 2, `the view follows the bottom (${JSON.stringify(w)})`);
  check(w.hasOlder && w.events < w.lastSeq / 2, `the live tail was split and its far top let go (${w.events} of ${w.lastSeq} events held, ${w.segments} page(s))`);
  check(await page.locator(`bx-frame[src="${TILE}"] bx-agent .earlier`).count() === 1, 'an "earlier entries" row heads the window');
  await shotEl(page, `bx-frame[src="${TILE}"] .pop`, 'agent-long-bottom');

  // ---- (b) scroll to the top: rows and pages load, the anchor never moves ----
  let drift = 0, steps = 0, pages = 0;
  const fetched = (await ag((a) => a.fetches)).length;
  for (let i = 0; i < 200; i++) {
    const at = await ag((a) => { a.scrollTo(8); const k = a.firstVisible(); const w0 = a.window; return { k, top: a.topOf(k), first: w0.firstKey, older: w0.hasOlder, from: w0.from }; });
    if (!at.from && !at.older) break;
    await until((g, first) => g.window.firstKey !== first, at.first, `the window grows upward (from key ${at.first})`, 8000);
    await sleep(50);
    const top = await ag((a, k) => a.topOf(k), at.k);
    drift = Math.max(drift, Math.abs(top - at.top));
    steps++;
  }
  w = await win();
  pages = (await ag((a) => a.fetches)).slice(fetched).filter((q) => q.startsWith('before=')).length;
  check(w.from === 0 && !w.hasOlder && steps > 0, `scrolling up reached the first entry (${steps} steps, ${pages} older page(s) fetched)`);
  check(pages > 0, 'older pages came from the server as the reader neared the top');
  check(drift <= 1, `the block being read never moved while rows and pages loaded above it (max drift ${drift.toFixed(2)}px)`);
  check(/Unit 1\b/.test(await page.locator(`bx-frame[src="${TILE}"] bx-agent .scroll`).innerText()), 'the first unit is in the DOM at the top');
  check(w.detached && w.hasNewer && w.events < w.lastSeq / 2, `far up, the tail was let go too (${w.events} events held, detached ${w.detached})`);
  check(w.rendered <= 160, `far up, the window stays a window (${w.rendered} rows)`);
  check(/jump to latest/.test((await ag((a) => a.pill)) || ''), 'a pill offers the latest');
  await shotEl(page, `bx-frame[src="${TILE}"] .pop`, 'agent-long-top');

  // ---- (c) scrolled up mid-transcript, a new turn streams below: nothing moves ----
  await ag((a) => { a.scrollTo(Math.round(a.window.scrollHeight / 2)); });
  await sleep(300);
  const before = await ag((a) => { const k = a.firstVisible(); return { k, top: a.topOf(k), w: a.window }; });
  const longTasks = await page.evaluate(() => { window.__lt = []; try { new PerformanceObserver((l) => { for (const e of l.getEntries()) window.__lt.push(e.duration); }).observe({ type: 'longtask' }); } catch { /* no longtask */ } return true; });
  await ag((a) => a.send('long 20'));
  await until((g, last) => g.status === 'idle' && g.window.lastSeq > last + 40, before.w.lastSeq, 'the second turn finished');
  await sleep(300);
  let after = await ag((a, k) => ({ top: a.topOf(k), w: a.window, pill: a.pill }), before.k);
  check(Math.abs(after.top - before.top) <= 1, `a turn streaming below leaves the view still (${before.top.toFixed(1)} → ${after.top.toFixed(1)})`);
  check(!after.w.atBottom && Math.abs(after.w.scrollTop - before.w.scrollTop) <= 1, `the view did not follow the new turn (scrollTop ${before.w.scrollTop} → ${after.w.scrollTop})`);
  const fresh = Number(((after.pill || '').match(/(\d+) new/) || [])[1] || 0);
  check(fresh >= 20, `the pill counts what came meanwhile: "${after.pill}"`);
  if (longTasks) { const lt = await page.evaluate(() => window.__lt || []); log(`agent-long: ${lt.length} long task(s) while 20 units streamed far below, max ${Math.round(Math.max(0, ...lt))} ms`); }
  // a late event in the page on screen (the one after the first visible
  // block's first): that page refolds — same keys, same rows — no jump
  const late = await ag((a) => { const k = a.firstVisible(); const r = { k, top: a.topOf(k), seq: k + 1 }; r.ok = a.late(k + 1); return r; });
  await sleep(300);
  after = await ag((a, k) => ({ top: a.topOf(k), w: a.window }), late.k);
  check(late.ok && Math.abs(after.top - late.top) <= 1 && !after.w.atBottom, `a late event (seq ${late.seq}) refolds its page without a jump (${late.top.toFixed(1)} → ${after.top.toFixed(1)})`);
  // the pill: the latest, followed
  await ag((a) => a.jumpLatest());
  await until((g) => g.window.atBottom && !g.window.detached && g.blocks.some((b) => b.text === 'done: 20 units'), null, 'the latest is back', 10000);
  await sleep(300);
  w = await win();
  check(w.atBottom && w.scrollHeight - w.scrollTop - w.clientHeight < 2 && !(await ag((a) => a.pill)), `jump to latest: at the bottom, following, no pill (${JSON.stringify(w)})`);

  // ---- (d) a hidden tab folds but does not render; back at the bottom it follows ----
  await fr(page, TILE, (f) => f.newTerm());
  await waitFor(page, (t, n) => (t.frameFor('apps/crawler')?.testApi().tabs.length || 0) > n, agentTab, { timeout: 10000, label: 'a shell tab opened' });
  const hiddenRows = await agAt(agentTab, (a) => a.window.rendered);
  await agAt(agentTab, (a) => a.send('long 5'));
  await waitFor(page, (t, i) => { const g = t.frameFor('apps/crawler')?.testApi().agent(i); return !!g && g.status === 'idle' && g.blocks.some((b) => b.text === 'done: 5 units'); }, agentTab, { timeout: 30000, label: 'the hidden tab folded a turn' });
  const hid = await agAt(agentTab, (a) => ({ hidden: a.hidden, rendered: a.window.rendered }));
  check(hid.hidden && hid.rendered === hiddenRows, `a hidden tab folds but does not render (${hiddenRows} → ${hid.rendered} rows, hidden ${hid.hidden})`);
  await fr(page, TILE, (f, t, i) => f.setActiveTab(i), agentTab);
  await until((g) => g.window.atBottom && !g.hidden, null, 'shown again, it renders', 10000);
  await sleep(300);
  check(/done: 5 units/.test(await page.locator(`bx-frame[src="${TILE}"] bx-agent .scroll`).innerText()), 'shown again, it renders what it folded, at the bottom');
  await fr(page, TILE, (f) => { const i = f.tabs.findIndex((x) => x.kind !== 'agent'); if (i >= 0) f.closeTab(i); });
  await fr(page, TILE, (f) => f.setActiveTab(f.tabs.findIndex((x) => x.kind === 'agent')));
  // a message being written re-renders only its last paragraph: a selection
  // in an earlier one survives the next chunks
  await ag((a) => a.send('paras 8'));
  await until((g) => g.blocks.some((b) => /Paragraph 2 of/.test(b.text || '')), null, 'two paragraphs streamed', 10000);
  await sleep(100);
  await page.locator(`bx-frame[src="${TILE}"] bx-agent`).evaluate((el) => {
    const p = [...el.shadowRoot.querySelectorAll('.agent .bubble p')].find((x) => /Paragraph 1 of/.test(x.textContent));
    const r = document.createRange(); r.selectNodeContents(p); const sel = window.getSelection(); sel.removeAllRanges(); sel.addRange(r);
  });
  await until((g) => g.status === 'idle' && g.blocks.some((b) => /Paragraph 8 of/.test(b.text || '')), null, 'eight paragraphs streamed', 15000);
  await sleep(200);
  const sel = await page.evaluate(() => { const s = window.getSelection(); return { text: s.toString(), live: s.rangeCount > 0 && s.getRangeAt(0).startContainer.isConnected }; });
  check(sel.live && /^Paragraph 1 of the answer, streamed\.?$/.test(sel.text.trim()), `a selection in a streaming message survives the next paragraphs ("${sel.text.trim()}", live ${sel.live})`);
  await page.evaluate(() => window.getSelection().removeAllRanges());
  await ag((a) => a.send('long 20'));
  await until((g) => g.status === 'idle' && g.blocks.filter((b) => b.text === 'done: 20 units').length >= 1 && g.window.atBottom, null, 'the third turn finished');
  await sleep(300);
  w = await win();
  check(w.atBottom && w.scrollHeight - w.scrollTop - w.clientHeight < 2, `at the bottom the view follows the new turn (${JSON.stringify(w)})`);
  check(w.rendered <= 150, `following the bottom trims the rendered tail (${w.rendered} of ${w.total})`);

  // ---- (e) a reload reads only the tail page, at the bottom ----
  const sid = await ag((a) => a.sessionId);
  const lastSeq = w.lastSeq;
  await page.reload();
  await openWindow();
  await fr(page, TILE, (f, t, id) => { const i = f.tabs.findIndex((x) => x.id === id); if (i >= 0) f.setActiveTab(i); }, sid);
  const t1 = Date.now();
  await until((g) => g.window.rendered > 0 && g.window.atBottom, null, 'the reload read the tail', 30000);
  await sleep(300);
  w = await win();
  const q = await ag((a) => a.fetches);
  log(`agent-long: reload of a ${lastSeq}-event log: tail rendered in ${Date.now() - t1} ms (${w.events} events, ${w.total} blocks loaded; fetches ${JSON.stringify(q)})`);
  check(q[0] === 'limit=200' && q.filter((x) => !x.startsWith('since=')).length === 1 && !q.includes('since=0') && w.events <= 260 && w.hasOlder,
    `open reads only the tail page, once (${w.events} of ${lastSeq} events; ${JSON.stringify(q)})`);
  check(w.rendered <= 150 && w.rendered > 0, `the tail renders a window (${w.rendered} of ${w.total})`);
  check(w.atBottom && w.scrollHeight - w.scrollTop - w.clientHeight < 2, 'the tail lands at the bottom');

  // ---- (f) the ended session's history view is paged ----
  await A.ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(sid)}`);
  await waitFor(page, (t, id) => (t.frameFor('apps/crawler')?.testApi().history || []).some((h) => h.id === id), sid, { timeout: 15000, label: 'the ended session lists under Recent sessions' });
  await fr(page, TILE, (f, t, id) => f.openHistory(id), sid);
  await waitFor(page, (t) => { const api = t.frameFor('apps/crawler')?.testApi(); const g = api && api.agent(api.tabs.length - 1); return !!g && !!g.history && g.window.total > 0; }, null, { timeout: 15000, label: 'the history view renders' });
  await fr(page, TILE, (f) => f.setActiveTab(f.tabs.length - 1));
  await sleep(300);
  w = await win();
  const hq = await ag((a) => a.fetches);
  check(hq[0] === 'limit=200' && w.hasOlder && w.rendered <= 150 && w.atBottom, `the history view reads its tail page and renders a window (${w.rendered} of ${w.total}; ${JSON.stringify(hq)})`);
  await shotEl(page, `bx-frame[src="${TILE}"] .pop`, 'agent-long-history');

  for (const s of await (await A.ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json()) {
    await A.ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(s.id)}`);
  }
  await A.ctx.request.delete(`${URL}/api/xbin/prefs/term%3Aapps%3Acrawler`);
  await closeCtx(A.ctx, page);
  done();
}

module.exports = { agentLong };
