// hack/ui-harness/passes/agentlong.js — the Agent tab on a long transcript
// (D124). The fake agent's `long N` script streams N units (a markdown
// message, an edit call with a diff, a thought every tenth). Checks: (a) only
// a window of the blocks is in the DOM and the view follows the bottom;
// (b) scrolling near the top loads earlier pages and the block the reader was
// looking at does not move by a pixel, page after page, down to the first
// block; (c) scrolled up mid-transcript, a new turn streaming below moves
// nothing and the view does not follow; back at the bottom it follows again
// and the rendered tail is trimmed; (d) a reload replays the log windowed and
// lands at the bottom; (e) the ended session's history view is windowed too.
// Soft: long tasks while a turn streams over the long log (logged, not asserted).
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
  const ag = (fn, arg) => fr(page, TILE, (f, t, a) => new Function('ag', 'arg', `return (${a.src})(ag, arg)`)(f.agent(), a.arg), { src: fn.toString(), arg: arg ?? null });
  const win = () => ag((a) => a.window);
  // wait for a window condition (polls the agent's test surface)
  const until = (fn, arg, label, timeout = 30000) => waitFor(page, (t, a) => {
    const g = t.frameFor('apps/crawler')?.testApi().agent();
    return !!g && new Function('ag', 'arg', `return (${a.src})(ag, arg)`)(g, a.arg);
  }, { src: fn.toString(), arg: arg ?? null }, { timeout, label });

  await openWindow();
  await fr(page, TILE, (f) => f.startKind('agent', 'fake'));
  await waitFor(page, (t) => { const api = t.frameFor('apps/crawler')?.testApi(); const g = api && api.agent(api.tabs.length - 1); return !!g && g.status === 'idle'; }, null, { timeout: 15000, label: 'the agent is idle' });
  await fr(page, TILE, (f) => f.setActiveTab(f.tabs.length - 1));

  // ---- (a) a long turn: a window in the DOM, following the bottom ----
  const t0 = Date.now();
  await ag((a, n) => a.send(`long ${n}`), N);
  await until((g, n) => g.status === 'idle' && g.blocks.some((b) => b.text === `done: ${n} units`), N, 'the long turn finished', 60000);
  log(`agent-long: ${N} units streamed and rendered in ${Date.now() - t0} ms`);
  await sleep(300);
  let w = await win();
  check(w.total >= 2 * N, `the transcript folded ${w.total} blocks`);
  check(w.rendered > 0 && w.rendered <= 150 && w.rendered < w.total / 3, `only a window is rendered (${w.rendered} of ${w.total})`);
  check(w.atBottom && w.scrollHeight - w.scrollTop - w.clientHeight < 2, `the view follows the bottom (${JSON.stringify(w)})`);
  check(await page.locator(`bx-frame[src="${TILE}"] bx-agent .earlier`).count() === 1, 'an "earlier entries" row heads the window');
  await shotEl(page, `bx-frame[src="${TILE}"] .pop`, 'agent-long-bottom');

  // ---- (b) scroll to the top: pages load, the anchor never moves ----
  let drift = 0, loads = 0;
  for (let i = 0; i < 60; i++) {
    const at = await ag((a) => { a.scrollTo(8); const k = a.firstVisible(); return { k, top: a.topOf(k), from: a.window.from }; });
    if (!at.from) break;
    await until((g, from) => g.window.from < from, at.from, `an earlier page loads (from ${at.from})`, 5000);
    await sleep(50);
    const top = await ag((a, k) => a.topOf(k), at.k);
    drift = Math.max(drift, Math.abs(top - at.top));
    loads++;
  }
  w = await win();
  check(w.from === 0 && loads > 0, `scrolling up loaded every earlier page (${loads} loads, from=${w.from})`);
  check(drift <= 1, `the block being read never moved while pages loaded above it (max drift ${drift.toFixed(2)}px)`);
  check(/Unit 1\b/.test(await page.locator(`bx-frame[src="${TILE}"] bx-agent .scroll`).innerText()), 'the first unit is in the DOM at the top');
  await shotEl(page, `bx-frame[src="${TILE}"] .pop`, 'agent-long-top');

  // ---- (c) scrolled up mid-transcript, a new turn streams below: nothing moves ----
  await ag((a) => { a.scrollTo(Math.round(a.window.scrollHeight / 2)); });
  await sleep(150);
  const before = await ag((a) => { const k = a.firstVisible(); return { k, top: a.topOf(k), w: a.window }; });
  const longTasks = await page.evaluate(() => { window.__lt = []; try { new PerformanceObserver((l) => { for (const e of l.getEntries()) window.__lt.push(e.duration); }).observe({ type: 'longtask' }); } catch { /* no longtask */ } return true; });
  await ag((a) => a.send('long 20'));
  await until((g) => g.status === 'idle' && g.blocks.some((b) => b.text === 'done: 20 units'), null, 'the second turn finished');
  await sleep(200);
  const after = await ag((a, k) => ({ top: a.topOf(k), w: a.window }), before.k);
  check(Math.abs(after.top - before.top) <= 1, `a turn streaming below leaves the view still (${before.top.toFixed(1)} → ${after.top.toFixed(1)})`);
  check(!after.w.atBottom && Math.abs(after.w.scrollTop - before.w.scrollTop) <= 1, `the view did not follow the new turn (scrollTop ${before.w.scrollTop} → ${after.w.scrollTop})`);
  if (longTasks) { const lt = await page.evaluate(() => window.__lt || []); log(`agent-long: ${lt.length} long task(s) while 20 units streamed over ${after.w.total} blocks, max ${Math.round(Math.max(0, ...lt))} ms`); }

  // back at the bottom: it follows, and the tail is trimmed
  await ag((a) => { const w0 = a.window; a.scrollTo(w0.scrollHeight); });
  await until((g) => g.window.atBottom, null, 'back at the bottom', 5000);
  await ag((a) => a.send('long 20'));
  await until((g) => g.status === 'idle' && g.blocks.filter((b) => b.text === 'done: 20 units').length === 2, null, 'the third turn finished');
  await sleep(300);
  w = await win();
  check(w.atBottom && w.scrollHeight - w.scrollTop - w.clientHeight < 2, `at the bottom the view follows the new turn (${JSON.stringify(w)})`);
  check(w.rendered <= 150, `following the bottom trims the rendered tail (${w.rendered} of ${w.total})`);

  // ---- (d) a reload replays the log windowed, at the bottom ----
  const total = w.total;
  const sid = await ag((a) => a.sessionId);
  await page.reload();
  await openWindow();
  await fr(page, TILE, (f, t, id) => { const i = f.tabs.findIndex((x) => x.id === id); if (i >= 0) f.setActiveTab(i); }, sid);
  const t1 = Date.now();
  await until((g, n) => g.window.total >= n && g.window.rendered > 0, total, 'the reload replayed the log', 30000);
  await sleep(300);
  w = await win();
  log(`agent-long: reload replay of ${w.total} blocks rendered in ${Date.now() - t1} ms`);
  check(w.rendered <= 150 && w.from > 0, `the replay renders a window (${w.rendered} of ${w.total}, from ${w.from})`);
  check(w.atBottom && w.scrollHeight - w.scrollTop - w.clientHeight < 2, 'the replay lands at the bottom');

  // ---- (e) the ended session's history view is windowed ----
  await A.ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(sid)}`);
  await waitFor(page, (t, id) => (t.frameFor('apps/crawler')?.testApi().history || []).some((h) => h.id === id), sid, { timeout: 15000, label: 'the ended session lists under Recent sessions' });
  await fr(page, TILE, (f, t, id) => f.openHistory(id), sid);
  await waitFor(page, (t) => { const api = t.frameFor('apps/crawler')?.testApi(); const g = api && api.agent(api.tabs.length - 1); return !!g && !!g.history && g.window.total > 0; }, null, { timeout: 15000, label: 'the history view renders' });
  await fr(page, TILE, (f) => f.setActiveTab(f.tabs.length - 1));
  await sleep(300);
  w = await win();
  check(w.total >= 2 * N && w.rendered <= 150, `the history view renders a window (${w.rendered} of ${w.total})`);
  await shotEl(page, `bx-frame[src="${TILE}"] .pop`, 'agent-long-history');

  for (const s of await (await A.ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json()) {
    await A.ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(s.id)}`);
  }
  await A.ctx.request.delete(`${URL}/api/xbin/prefs/term%3Aapps%3Acrawler`);
  await closeCtx(A.ctx, page);
  done();
}

module.exports = { agentLong };
