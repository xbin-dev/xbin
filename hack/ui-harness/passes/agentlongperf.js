// hack/ui-harness/passes/agentlongperf.js — timings of the Agent tab on a
// very long session (D130), logged, not asserted: run with AGENT_PERF=<units>
// (e.g. 950 ≈ 2000 blocks; skipped otherwise). It streams the session
// unthrottled, then on a CPU throttled 4× (CDP) reopens it after a reload
// and times the first render at the bottom, the heap after it, and a turn
// of 30 more units streaming at the bottom (long tasks). It reads only what
// every bx-agent's testApi has had since D124, so the same pass times an
// older web/ against this xbind: the before/after numbers of D130.
const fs = require('fs');
const { URL, OUT, login, closeCtx, fr, waitFor, openShell, usePersonalScreen, openTile, log, sleep } = require('../lib');

const TILE = 'apps/crawler';

async function agentLongPerf(browser) {
  const N = Number(process.env.AGENT_PERF || 0);
  if (!N) { log('agentLongPerf: skipped (AGENT_PERF=<units> runs it)'); return; }
  const out = [];
  const note = (s) => { out.push(s); log(`agent-long-perf: ${s}`); };
  const A = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 1300 } });
  const page = A.page;
  for (const s of await (await A.ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json()) {
    await A.ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(s.id)}`);
  }
  const openWindow = async () => {
    await openShell(page);
    await usePersonalScreen(page);
    await openTile(page, TILE);
    await page.locator(`.card[data-path="${TILE}"]`).evaluate((el) => el.scrollIntoView({ block: 'start' }));
    await fr(page, TILE, (f) => f.open('term'));
  };
  const ag = (fn, arg) => fr(page, TILE, (f, t, a) => new Function('ag', 'arg', `return (${a.src})(ag, arg)`)(f.agent(), a.arg), { src: fn.toString(), arg: arg ?? null });
  const until = (fn, arg, label, timeout) => waitFor(page, (t, a) => {
    const g = t.frameFor('apps/crawler')?.testApi().agent();
    return !!g && new Function('ag', 'arg', `return (${a.src})(ag, arg)`)(g, a.arg);
  }, { src: fn.toString(), arg: arg ?? null }, { timeout, label });

  await openWindow();
  await fr(page, TILE, (f) => f.startKind('agent', 'fake'));
  await waitFor(page, (t) => { const api = t.frameFor('apps/crawler')?.testApi(); const g = api && api.agent(api.tabs.length - 1); return !!g && g.status === 'idle'; }, null, { timeout: 15000, label: 'the agent is idle' });
  await fr(page, TILE, (f) => f.setActiveTab(f.tabs.length - 1));
  let t = Date.now();
  await ag((a, n) => a.send(`long ${n}`), N);
  await until((g, n) => g.status === 'idle' && g.blocks.some((b) => b.text === `done: ${n} units`), N, 'the long turn finished', 600000);
  note(`${N} units streamed (unthrottled) in ${Date.now() - t} ms`);
  const sid = await ag((a) => a.sessionId);

  // reopen on a throttled CPU
  await page.reload();
  await openWindow();
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Emulation.setCPUThrottlingRate', { rate: 4 });
  t = Date.now();
  await fr(page, TILE, (f, tt, id) => { const i = f.tabs.findIndex((x) => x.id === id); if (i >= 0) f.setActiveTab(i); }, sid);
  await until((g) => g.status === 'idle' && g.window.rendered > 0 && g.window.atBottom, null, 'reopened at the bottom', 120000);
  const openMs = Date.now() - t;
  await sleep(500);
  const w = await ag((a) => a.window);
  await cdp.send('HeapProfiler.collectGarbage');
  const heap = await cdp.send('Runtime.getHeapUsage');
  note(`reopen (CPU 4×): first render at the bottom in ${openMs} ms; ${w.total} blocks held, ${w.rendered} rendered${w.events != null ? `, ${w.events} events` : ''}; JS heap ${(heap.usedSize / 1048576).toFixed(1)} MiB`);

  // a turn streaming at the bottom over the long log
  await page.evaluate(() => { window.__lt = []; try { new PerformanceObserver((l) => { for (const e of l.getEntries()) window.__lt.push(e.duration); }).observe({ type: 'longtask' }); } catch { /* none */ } });
  t = Date.now();
  await ag((a) => a.send('long 30'));
  await until((g) => g.status === 'idle' && g.blocks.some((b) => b.text === 'done: 30 units'), null, 'the extra turn finished', 120000);
  const lt = await page.evaluate(() => window.__lt || []);
  note(`30 more units at the bottom (CPU 4×): ${Date.now() - t} ms, ${lt.length} long task(s), max ${Math.round(Math.max(0, ...lt))} ms, total ${Math.round(lt.reduce((x, y) => x + y, 0))} ms`);
  await cdp.send('Emulation.setCPUThrottlingRate', { rate: 1 });

  fs.writeFileSync(`${OUT}/agent-long-perf.txt`, out.join('\n') + '\n');
  await A.ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(sid)}`);
  await A.ctx.request.delete(`${URL}/api/xbin/prefs/term%3Aapps%3Acrawler`);
  await closeCtx(A.ctx, page);
}

module.exports = { agentLongPerf };
