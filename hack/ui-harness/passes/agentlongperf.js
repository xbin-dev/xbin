// hack/ui-harness/passes/agentlongperf.js — timings of the Agent tab on a
// very long session (D130), logged, not asserted: run with AGENT_PERF=<units>
// (e.g. 950 ≈ 2000 blocks; skipped otherwise). It streams the session
// unthrottled, then on a CPU throttled AGENT_PERF_CPU× (CDP; default 4)
// reopens it after a reload and times the transcript at the bottom (from
// the reload, and from the terminal window opening), the heap after it, and
// a turn of 30 more units streaming at the bottom (long tasks). It reads only what
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

  // reopen on a throttled CPU: from the reload to the transcript at the
  // bottom (the shell, the window, the session's tab and its log)
  const cpu = Number(process.env.AGENT_PERF_CPU || 4);
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Emulation.setCPUThrottlingRate', { rate: cpu });
  t = Date.now();
  await page.reload();
  await openShell(page);
  await usePersonalScreen(page);
  await openTile(page, TILE);
  await page.locator(`.card[data-path="${TILE}"]`).evaluate((el) => el.scrollIntoView({ block: 'start' }));
  await page.evaluate(() => { window.__lt = []; try { new PerformanceObserver((l) => { for (const e of l.getEntries()) window.__lt.push(e.duration); }).observe({ type: 'longtask' }); } catch { /* none */ } });
  const tw = Date.now(); // the terminal window opens: its tabs (the session's) mount and read the log
  await fr(page, TILE, (f) => f.open('term'));
  await fr(page, TILE, (f, tt, id) => { const i = f.tabs.findIndex((x) => x.id === id); if (i >= 0) f.setActiveTab(i); }, sid);
  await until((g) => g.status === 'idle' && g.window.rendered > 0 && g.window.atBottom, null, 'reopened at the bottom', 120000);
  const openMs = Date.now() - t, winMs = Date.now() - tw;
  const olt = await page.evaluate(() => window.__lt || []);
  note(`reopen (CPU ${cpu}×): window open → the transcript at the bottom in ${winMs} ms; long tasks meanwhile ${olt.length}, max ${Math.round(Math.max(0, ...olt))} ms, total ${Math.round(olt.reduce((x, y) => x + y, 0))} ms`);
  await sleep(500);
  const w = await ag((a) => a.window);
  await cdp.send('HeapProfiler.collectGarbage');
  const heap = await cdp.send('Runtime.getHeapUsage');
  note(`reopen (CPU ${cpu}×): reload → the transcript at the bottom in ${openMs} ms; ${w.total} blocks held, ${w.rendered} rendered${w.events != null ? `, ${w.events} events` : ''}; JS heap ${(heap.usedSize / 1048576).toFixed(1)} MiB`);

  // the tab's own cost: a fresh <bx-agent> on the session, mounted → its
  // transcript at the bottom (fetch, fold, first render), in the page
  const fresh = await page.evaluate(async (id) => {
    const lt = [];
    const po = new PerformanceObserver((l) => { for (const e of l.getEntries()) lt.push(e.duration); });
    try { po.observe({ type: 'longtask' }); } catch { /* none */ }
    const el = document.createElement('bx-agent');
    el.style.cssText = 'position:fixed;left:0;top:0;width:700px;height:400px;display:flex;z-index:99999';
    el.setAttribute('component', 'apps/crawler');
    el.setAttribute('session', id);
    const t0 = performance.now();
    document.body.appendChild(el);
    await new Promise((res) => { const tick = () => { const w = el.testApi().window; if (w.rendered > 0 && w.atBottom && w.total > 0) res(); else requestAnimationFrame(tick); }; tick(); });
    const ms = performance.now() - t0;
    await new Promise((r) => setTimeout(r, 100));
    po.disconnect();
    const total = el.testApi().window.total;
    el.remove();
    return { ms, total, lt };
  }, sid);
  note(`a fresh tab on it (CPU ${cpu}×): mounted → the transcript at the bottom in ${Math.round(fresh.ms)} ms (${fresh.total} blocks); long tasks ${fresh.lt.length}, max ${Math.round(Math.max(0, ...fresh.lt))} ms`);

  // a turn streaming at the bottom over the long log
  await page.evaluate(() => { window.__lt = []; try { new PerformanceObserver((l) => { for (const e of l.getEntries()) window.__lt.push(e.duration); }).observe({ type: 'longtask' }); } catch { /* none */ } });
  t = Date.now();
  await ag((a) => a.send('long 30'));
  await until((g) => g.status === 'idle' && g.blocks.some((b) => b.text === 'done: 30 units'), null, 'the extra turn finished', 120000);
  const lt = await page.evaluate(() => window.__lt || []);
  note(`30 more units at the bottom (CPU ${cpu}×): ${Date.now() - t} ms, ${lt.length} long task(s), max ${Math.round(Math.max(0, ...lt))} ms, total ${Math.round(lt.reduce((x, y) => x + y, 0))} ms`);
  await cdp.send('Emulation.setCPUThrottlingRate', { rate: 1 });

  fs.writeFileSync(`${OUT}/agent-long-perf.txt`, out.join('\n') + '\n');
  await A.ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(sid)}`);
  await A.ctx.request.delete(`${URL}/api/xbin/prefs/term%3Aapps%3Acrawler`);
  await closeCtx(A.ctx, page);
}

module.exports = { agentLongPerf };
