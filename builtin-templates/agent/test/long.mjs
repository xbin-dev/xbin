// long.mjs — a long conversation in the web chat (D130): the timeline renders
// a window of its blocks at the bottom, grows it as the reader scrolls up
// without moving the row being read, and follows a streamed answer at the
// bottom; against an xbind without /vendor/scroll-window.js it renders every
// block and still follows the bottom.
//
//   node test/long.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';

const { ok, done } = checker();
const now = Math.floor(Date.now() / 1000);
const msgs = [];
for (let i = 1; i <= 300; i++) {
  msgs.push({ id: i, runId: 1, seq: i, role: i % 2 ? 'user' : 'assistant', content: `message ${i}\n\n${'words '.repeat(i % 7 * 10)}`, created: now - 1000 + i });
}
const seed = {
  runs: [{ id: 1, title: 'long', status: 'idle', parentId: 0, rootId: 1, kind: '', updated: now }],
  views: { 1: { run: { id: 1, title: 'long', status: 'idle', parentId: 0, rootId: 1 }, messages: msgs } },
};

const browser = await launch();
async function open(noWindow) {
  const ctx = await browser.newContext();
  await serveTile(ctx, { noWindow, realMarked: true });
  await ctx.addInitScript(STUB, seed);
  const page = await ctx.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  await page.setViewportSize({ width: 900, height: 700 });
  await page.goto(`${ORIGIN}/#c=1`);
  await page.waitForFunction(() => document.querySelectorAll('#timeline > [data-k]').length > 0);
  await page.waitForTimeout(300);
  return { ctx, page, errors };
}
const geo = (page) => page.evaluate(() => {
  const tl = document.getElementById('timeline');
  const rows = [...tl.querySelectorAll(':scope > [data-k]')];
  const top = tl.getBoundingClientRect().top;
  const first = rows.find((r) => r.getBoundingClientRect().bottom > top);
  return { rows: rows.length, gap: tl.scrollHeight - tl.scrollTop - tl.clientHeight, key: first && first.dataset.k, at: first ? first.getBoundingClientRect().top : 0, firstRow: rows[0] && rows[0].dataset.k };
});

// with the window
{
  const { ctx, page, errors } = await open(false);
  let g = await geo(page);
  ok('a long conversation renders a window of its blocks', g.rows > 0 && g.rows < 100, `${g.rows} rows`);
  ok('…at the bottom', g.gap < 2, JSON.stringify(g));
  ok('an "earlier messages" line heads it', await page.locator('#timeline > .earlier').count() === 1);
  let drift = 0, steps = 0;
  for (let i = 0; i < 60 && g.firstRow !== 'm1'; i++) {
    await page.evaluate(() => { document.getElementById('timeline').scrollTop = 4; });
    const at = await geo(page);
    await page.waitForFunction((k) => document.querySelector('#timeline > [data-k]').dataset.k !== k, at.firstRow, { timeout: 3000 }).catch(() => {});
    await page.waitForTimeout(30);
    const top = await page.evaluate((k) => document.querySelector(`#timeline > [data-k="${k}"]`)?.getBoundingClientRect().top, at.key);
    if (top != null) drift = Math.max(drift, Math.abs(top - at.at));
    g = await geo(page);
    steps++;
  }
  ok('scrolling up grows the window to the first message', g.firstRow === 'm1', `${steps} steps, first ${g.firstRow}`);
  ok('…and the row being read never moves', drift <= 1, `max drift ${drift.toFixed(2)} px`);
  ok('far up, a pill offers the latest', /jump to latest/.test(await page.locator('#timeline .jump').textContent().catch(() => '')));
  ok('…and the window stays a window', g.rows < 300, `${g.rows} rows`);
  await page.click('#timeline .jump');
  await page.waitForTimeout(300);
  g = await geo(page);
  ok('the pill goes back to the bottom', g.gap < 2 && !(await page.$('#timeline .jump')), JSON.stringify(g));
  // an answer streams at the bottom: followed
  await page.evaluate(() => window.__push({ type: 'text', run: 1, root: 1, data: { text: 'Streaming\n\nan answer\n\nthat grows' } }));
  await page.waitForSelector('#timeline .msg.assistant.live');
  await page.waitForTimeout(100);
  ok('a streamed answer is followed at the bottom', (await geo(page)).gap < 2);
  ok('…its paragraphs rendered a block at a time', (await page.$$('#timeline .msg.assistant.live .md > .md-b')).length === 3);
  ok('no page errors (window)', errors.length === 0, errors.join(' | '));
  await ctx.close();
}

// an older xbind: no /vendor/scroll-window.js
{
  const { ctx, page, errors } = await open(true);
  const g = await geo(page);
  ok('without the scroll window every block renders', g.rows === 300, `${g.rows} rows`);
  ok('…at the bottom', g.gap < 2, JSON.stringify(g));
  await page.evaluate(() => window.__push({ type: 'text', run: 1, root: 1, data: { text: 'Streaming' } }));
  await page.waitForSelector('#timeline .msg.assistant.live');
  await page.waitForTimeout(100);
  ok('…and a streamed answer is followed', (await geo(page)).gap < 2);
  ok('no page errors (no window)', errors.length === 0, errors.join(' | '));
  await ctx.close();
}

await browser.close();
done('long');
