// hack/ui-harness/passes/agenttemplatelong.js — the agent template's chat on
// a long conversation (D130, E4), end to end: the seeded apps/agent against
// hack/fakeopenai, whose `long N` script writes N units (a markdown answer
// and a note call each) without pauses and `paras N` streams an answer a
// paragraph at a time. Checks: (a) a long turn on a throttled CPU renders a
// window, follows the bottom, and lets go of the far top; (b) a reload reads
// only the newest page and lands at the bottom; (c) scrolling to the top
// loads rows and older pages and the row being read never moves by more than
// a pixel — the live tail let go meanwhile, and a pill offers the latest;
// (d) scrolled up, a turn streaming below moves nothing and the pill counts
// it; a late event above the view moves nothing; the pill brings the latest
// back, followed; (e) a selection in the answer being streamed survives its
// next paragraphs; (f) at the bottom a new turn is followed and the window
// stays a window. Timings (throttled CPU, logged to
// $OUT/agent-template-long-perf.txt): the long turn's long tasks, a reload
// to the bottom, 30 more units at the bottom. AGENT_TEMPLATE_PERF_ONLY=1
// runs only the timings, reading nothing but the DOM — so the same pass
// times an older frontend (copied into $WS/apps/agent) for before/after.
const { login, fs, sleep, log, shot, checker, noGocryptfs, OUT } = require('../lib');

const URL = process.env.URL || 'http://127.0.0.1:8697';
const N = Number(process.env.AGENT_TEMPLATE_UNITS || 240);
const CPU = Number(process.env.AGENT_TEMPLATE_CPU || 4);
const PERF_ONLY = !!process.env.AGENT_TEMPLATE_PERF_ONLY;

const until = (page, fn, arg, timeout = 20000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 50 });
// the agent's config, patched through its own API (the frame's owner is admin of it)
const setConfig = (page, patch) => page.evaluate(async (patch) => {
  const b = `/api/${location.pathname.split('/').slice(2, -1).join('/')}`;
  const c = { ...(await (await xbin.fetch(`${b}/config`)).json()), ...patch };
  const r = await xbin.fetch(`${b}/config`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(c) });
  if (!r.ok) throw new Error(`PUT config: ${r.status}`);
}, patch);
// a turn's last answer, read from the backend (the view may not hold it)
const lastAnswer = (page, run) => page.evaluate(async ([run]) => {
  const b = `/api/${location.pathname.split('/').slice(2, -1).join('/')}`;
  const v = await (await xbin.fetch(`${b}/runs/${run}/view?limit=3`)).json();
  return { status: v.run.status, text: (v.messages || []).filter((m) => m.role === 'assistant').map((m) => m.content).pop() || '' };
}, [run]);
const answered = (page, t, timeout) => until(page, ([t]) => [...document.querySelectorAll('#timeline .msg.assistant:not(.live)')]
  .some((e) => e.textContent.includes(t)), [t], timeout);
// the chat's rows and scroller, from the DOM (what an older frontend has too)
const dom = (page) => page.evaluate(() => {
  const tl = document.getElementById('timeline');
  const rows = [...tl.querySelectorAll(':scope > [data-k], :scope > .msg, :scope > .tcard, :scope > .acard, :scope > .think, :scope > .step, :scope > .notice')];
  return { rows: rows.length, gap: tl.scrollHeight - tl.scrollTop - tl.clientHeight, scrollTop: tl.scrollTop, scrollHeight: tl.scrollHeight };
});
// the chat window's own account (chat-window.js testApi), with the first visible row
const probe = (page) => page.evaluate(() => {
  const { late, ...t } = globalThis.agentChat.testApi();
  const tl = document.getElementById('timeline');
  const top = tl.getBoundingClientRect().top;
  const first = [...tl.querySelectorAll(':scope > [data-k]')].find((r) => r.getBoundingClientRect().bottom > top);
  return { ...t, firstKey: first ? first.dataset.k : null, firstTop: first ? first.getBoundingClientRect().top : 0 };
});
// a window: some of the blocks held, and no more than scroll-window.js's 6
// views (TRIM_AT) of rows past the view on either side
const windowed = (w) => w.rendered > 0 && w.rendered < w.total && w.scrollTop <= 6.5 * w.clientHeight
  && w.scrollHeight - w.scrollTop - w.clientHeight <= 6.5 * w.clientHeight;
const span = (w) => `${w.rendered} of ${w.total} blocks held; ${(w.scrollTop / w.clientHeight).toFixed(1)} views above the view, ${((w.scrollHeight - w.scrollTop - w.clientHeight) / w.clientHeight).toFixed(1)} below`;
const topOf = (page, k) => page.evaluate((k) => document.querySelector(`#timeline > [data-k="${k}"]`)?.getBoundingClientRect().top ?? null, k);
const scrollTo = (page, y) => page.evaluate((y) => { document.getElementById('timeline').scrollTop = y; }, y);
const longTasks = {
  start: (page) => page.evaluate(() => { window.__lt = []; try { new PerformanceObserver((l) => { for (const e of l.getEntries()) window.__lt.push(e.duration); }).observe({ type: 'longtask' }); } catch { /* none */ } }),
  read: async (page) => { const lt = await page.evaluate(() => window.__lt || []); return `${lt.length} long task(s), max ${Math.round(Math.max(0, ...lt))} ms, total ${Math.round(lt.reduce((a, b) => a + b, 0))} ms`; },
};

// internalClass: new chats in the internal class (the note tool), picked as a person does
async function internalClass(page) {
  await page.waitForSelector('#tset', { timeout: 30000 });
  if (!(await page.$('.clsmenu'))) await page.click('#tset');
  await page.click('.clsmenu .mi[data-class="internal"]');
  await until(page, () => !document.querySelector('.clsmenu'));
}

async function agentTemplateLong(browser) {
  const { check, skip, done } = checker('agent-template-long');
  if (noGocryptfs()) { skip(`apps/agent is held: ${noGocryptfs()}`); return done(); }
  const out = [];
  const note = (s) => { out.push(s); log(`agent-template-long: ${s}`); };
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1300, height: 950 } });
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const views = []; // the transcript reads: {run, q}
  page.on('request', (r) => { const m = r.url().match(/\/runs\/(\d+)\/view(\?[^#]*)?$/); if (m) views.push({ run: +m[1], q: m[2] || '' }); });
  const cdp = await ctx.newCDPSession(page);
  const cpu = (rate) => cdp.send('Emulation.setCPUThrottlingRate', { rate });

  await page.goto(`${URL}/c/apps/agent/`);
  await page.waitForSelector('#msg', { timeout: 30000 });
  await internalClass(page);
  await setConfig(page, { model: 'fake/fake-chat', maxTurnSteps: 500 });

  // ---- (a) a long turn on a throttled CPU: a window, following the bottom ----
  await page.click('#home');
  await page.fill('#msg', `long ${N}`);
  await longTasks.start(page);
  await cpu(CPU);
  let t = Date.now();
  await page.press('#msg', 'Enter');
  await until(page, () => /^#c=\d+/.test(location.hash), null, 30000);
  const run = await page.evaluate(() => +location.hash.match(/c=(\d+)/)[1]);
  await answered(page, `done: ${N} units`, 900000);
  note(`${N} units (${2 * N + 2} messages) streamed and rendered in ${Date.now() - t} ms (CPU ${CPU}×): ${await longTasks.read(page)}`);
  await cpu(1);
  await sleep(500);
  let d = await dom(page);
  if (!PERF_ONLY) {
    const w = await probe(page);
    check(w.windowed, 'this xbind serves the scroll window');
    check(windowed(w), `only a window is rendered (${span(w)})`);
    check(w.atBottom && d.gap < 2, `the view follows the bottom (${JSON.stringify(d)})`);
    check(w.hasOlder && w.held < N, `following the bottom, the far top was let go (${w.held} of ${2 * N + 2} messages held)`);
    check(await page.locator('#timeline > .earlier').count() === 1, 'an "earlier messages" line heads the window');
  }
  await shot(page, 'agent-template-long-bottom', { fullPage: false });

  // ---- (b) a reload reads the newest page only, at the bottom ----
  views.length = 0;
  await cpu(CPU);
  t = Date.now();
  await page.reload();
  await page.waitForSelector('#msg', { timeout: 30000 });
  await longTasks.start(page);
  await answered(page, `done: ${N} units`, 60000);
  await until(page, () => { const tl = document.getElementById('timeline'); return tl.scrollHeight - tl.scrollTop - tl.clientHeight < 2; }, null, 30000);
  const reopenMs = Date.now() - t;
  const reopenLt = await longTasks.read(page);
  await cpu(1);
  await cdp.send('HeapProfiler.collectGarbage');
  const heap = await cdp.send('Runtime.getHeapUsage');
  d = await dom(page);
  note(`reload → the conversation at its bottom in ${reopenMs} ms (CPU ${CPU}×; ${d.rows} rows in the DOM; ${reopenLt}); JS heap ${(heap.usedSize / 1048576).toFixed(1)} MiB`);
  const reads = views.filter((v) => v.run === run).map((v) => v.q);
  if (!PERF_ONLY) {
    // the newest page, then older ones only while the rows do not fill the view (the window's fill)
    check(reads[0] === '?limit=50' && reads.length <= 2 && reads.every((q) => /^\?limit=50(&before=\d+)?$/.test(q)),
      `open reads the newest page, not the whole conversation (${JSON.stringify(reads)})`);
    check(d.gap < 2, 'the reload lands at the bottom');
  }

  if (!PERF_ONLY) {
    // ---- (c) up to the top: rows and pages load above, the row being read stays put ----
    views.length = 0;
    let drift = 0, steps = 0, w;
    for (let i = 0; i < 400; i++) {
      w = await probe(page);
      if (!w.start && !w.hasOlder) break;
      await scrollTo(page, 8);
      const at = await probe(page);
      await until(page, ([start, total]) => { const x = globalThis.agentChat.testApi(); return x.start !== start || x.total !== total; }, [at.start, at.total], 8000);
      await sleep(40);
      const top = await topOf(page, at.firstKey);
      if (top != null) drift = Math.max(drift, Math.abs(top - at.firstTop));
      steps++;
    }
    await sleep(300);
    w = await probe(page);
    const pages = views.filter((v) => v.run === run && /before=/.test(v.q)).length;
    check(w.start === 0 && !w.hasOlder && steps > 0, `scrolling up reached the first message (${steps} steps, ${pages} older page(s) read)`);
    check(pages > 0, 'older pages came from the backend as the reader neared the top');
    check(drift <= 1, `the row being read never moved while rows and pages loaded above it (max drift ${drift.toFixed(2)} px)`);
    check(/Unit 1 of the long turn/.test(await page.locator('#timeline').innerText()), 'the first unit is in the DOM at the top');
    check(w.detached && w.hasNewer && w.held < 2 * N + 2 - 50, `far up, the live tail was let go too (${w.held} of ${2 * N + 2} messages held, detached ${w.detached})`);
    check(windowed(w), `far up, the window stays a window (${span(w)})`);
    check(/jump to latest/.test(w.pill) && await page.locator('#timeline .jump').isVisible(), `a pill offers the latest ("${w.pill}")`);
    await shot(page, 'agent-template-long-top', { fullPage: false });

    // ---- (d) scrolled up, a turn streams below: nothing moves; a late event above neither ----
    await scrollTo(page, Math.round((await probe(page)).scrollHeight / 2));
    await sleep(400);
    const before = await probe(page);
    await page.evaluate(async ([run]) => {
      const b = `/api/${location.pathname.split('/').slice(2, -1).join('/')}`;
      const r = await xbin.fetch(`${b}/runs/${run}/message`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ text: 'long 20' }) });
      if (!r.ok) throw new Error(`POST message: ${r.status}`);
    }, [run]);
    for (let i = 0; i < 300; i++) { const a = await lastAnswer(page, run); if (a.status === 'idle' && a.text === 'done: 20 units') break; await sleep(200); }
    await sleep(400);
    let after = await probe(page);
    const moved = (await topOf(page, before.firstKey)) - before.firstTop;
    check(Math.abs(moved) <= 1, `a turn streaming below leaves the view still (moved ${moved.toFixed(2)} px)`);
    check(!after.atBottom && Math.abs(after.scrollTop - before.scrollTop) <= 1, `the view did not follow the new turn (scrollTop ${before.scrollTop} → ${after.scrollTop})`);
    const fresh = Number((after.pill.match(/(\d+) new/) || [])[1] || 0);
    check(fresh >= 20, `the pill counts what came meanwhile ("${after.pill}")`);
    // a late event: the message of the row just above the view grows
    const late = await page.evaluate(() => {
      const tl = document.getElementById('timeline'), top = tl.getBoundingClientRect().top;
      const above = [...tl.querySelectorAll(':scope > .msg.assistant[data-k]')].filter((r) => r.getBoundingClientRect().bottom <= top).pop();
      const first = [...tl.querySelectorAll(':scope > [data-k]')].find((r) => r.getBoundingClientRect().bottom > top);
      if (!above || !first) return null;
      const h = above.getBoundingClientRect().height;
      return { key: above.dataset.k, h, first: first.dataset.k, top: first.getBoundingClientRect().top, ok: globalThis.agentChat.testApi().late(above.dataset.k) };
    });
    await sleep(400);
    const grew = late ? await page.evaluate((k) => document.querySelector(`#timeline > [data-k="${k}"]`)?.getBoundingClientRect().height ?? 0, late.key) : 0;
    const lateMoved = late ? (await topOf(page, late.first)) - late.top : NaN;
    check(!!late && late.ok && grew > late.h + 20 && Math.abs(lateMoved) <= 1,
      `a late event grows a message above the view (${late && late.h.toFixed(0)} → ${grew.toFixed(0)} px) and the row being read stays (moved ${Number(lateMoved).toFixed(2)} px)`);
    // the pill: the latest, followed
    views.length = 0;
    await page.click('#timeline .jump');
    await answered(page, 'done: 20 units', 15000);
    await until(page, () => { const x = globalThis.agentChat.testApi(); return x.atBottom && !x.detached && !x.pill; }, null, 10000);
    await sleep(300);
    after = await probe(page);
    d = await dom(page);
    check(d.gap < 2 && after.atBottom && !after.pill, `jump to latest: at the bottom, following, no pill (${JSON.stringify(d)})`);
    check(views.some((v) => v.run === run && v.q === '?limit=50'), `…the newest page read again (${JSON.stringify(views.map((v) => v.q))})`);

    // ---- (e) a selection in the answer being streamed survives its next paragraphs ----
    await page.fill('#msg', 'paras 9');
    await page.press('#msg', 'Enter');
    await until(page, () => /Paragraph 2 of/.test(document.querySelector('#timeline .msg.assistant.live')?.textContent || ''), null, 20000);
    await page.evaluate(() => {
      const p = [...document.querySelectorAll('#timeline .msg.assistant.live .md p')].find((x) => /Paragraph 1 of/.test(x.textContent));
      const r = document.createRange(); r.selectNodeContents(p); const s = window.getSelection(); s.removeAllRanges(); s.addRange(r);
    });
    await until(page, () => /Paragraph 8 of/.test(document.querySelector('#timeline .msg.assistant.live')?.textContent || ''), null, 20000);
    const sel = await page.evaluate(() => { const s = window.getSelection(); return { text: s.toString().trim(), live: s.rangeCount > 0 && s.getRangeAt(0).startContainer.isConnected }; });
    check(sel.live && sel.text === 'Paragraph 1 of the answer, streamed.', `a selection in the answer being written survives its next paragraphs ("${sel.text}", live ${sel.live})`);
    await page.evaluate(() => window.getSelection().removeAllRanges());
    await answered(page, 'Paragraph 9 of', 20000);
  }

  // ---- (f) at the bottom, a new turn is followed; the window stays a window ----
  await sleep(300);
  await longTasks.start(page);
  await cpu(CPU);
  t = Date.now();
  await page.fill('#msg', 'long 30');
  await page.press('#msg', 'Enter');
  await answered(page, 'done: 30 units', 300000);
  note(`30 more units at the bottom (CPU ${CPU}×): ${Date.now() - t} ms, ${await longTasks.read(page)}`);
  await cpu(1);
  await sleep(500);
  d = await dom(page);
  if (!PERF_ONLY) {
    const w = await probe(page);
    check(d.gap < 2 && w.atBottom, `at the bottom the view follows the new turn (${JSON.stringify(d)})`);
    check(windowed(w), `following the bottom keeps a window (${span(w)})`);
  }
  note(`the DOM at the bottom: ${d.rows} rows`);

  fs.writeFileSync(`${OUT}/agent-template-long-perf${PERF_ONLY ? '-before' : ''}.txt`, out.join('\n') + '\n');
  if (!PERF_ONLY) check(errors.length === 0, `no page errors (${errors.join(' | ')})`);
  await ctx.close();
  done();
}

module.exports = { agentTemplateLong };
