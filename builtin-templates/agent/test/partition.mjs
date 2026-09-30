// partition.mjs — the page's three layouts (API.md "Partitioned instances";
// model/partition.js), driving the real tile against backend.mjs:
//
//   - unpartitioned (no xbin.partition): today's page — no notice, the
//     Shared view, Share in the row menu and the top bar, a stream that
//     ignores the page's visibility;
//   - a person's partition ("user:alice"): no sharing anywhere, one banner
//     naming a bound sandbox manager that can't keep people apart, and a
//     stream that closes while the page is hidden and comes back when shown;
//   - the global instance ("global"): the note to sign in as a person.
//
//   node test/partition.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';

const { ok, done } = checker();
const seed = {
  runs: [{ id: 1, title: 'my notes', status: 'idle', parentId: 0, rootId: 1 }],
  sbxManagers: [
    { provider: 'apps/cs', title: 'Coding sandboxes', ok: true, caps: ['exec', 'files', 'partitions'], egress: ['none'], images: [], sizes: [] },
    { provider: 'apps/old', title: 'apps/old', ok: false, refusal: 'partitions', caps: [], egress: [], images: [], sizes: [],
      error: 'sandbox manager apps/old: apps/old can\'t keep each person\'s sandboxes apart' },
  ],
};

const browser = await launch();

async function open(partition) {
  const ctx = await browser.newContext();
  await serveTile(ctx);
  await ctx.addInitScript(STUB, seed);
  if (partition) {
    await ctx.addInitScript((p) => {
      window.xbin.partition = p;
      window.xbin.iface = (slot) => (slot === 'sandboxes'
        ? { endpoints: [{ provider: 'apps/cs', url: 'http://xbin.test/api/apps/cs' }, { provider: 'apps/old', url: 'http://xbin.test/api/apps/old' }] } : null);
    }, partition);
  }
  const page = await ctx.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  await page.goto(`${ORIGIN}/`);
  await page.waitForSelector('#runs .run');
  await page.waitForFunction(() => window.__streams() > 0);
  return { ctx, page, errors };
}

const segs = (page) => page.$$eval('#views .seg', (els) => els.map((e) => e.textContent.trim()));
const menu = async (page) => {
  await page.click('#runs .run[data-id="1"]', { button: 'right' });
  const items = await page.$$eval('.rowmenu .mi', (els) => els.map((e) => e.textContent.trim()));
  await page.click('.mback');
  return items;
};
const hide = (page, hidden) => page.evaluate((h) => {
  Object.defineProperty(document, 'hidden', { configurable: true, get: () => h });
  document.dispatchEvent(new Event('visibilitychange'));
}, hidden);

// --- unpartitioned: today's page -----------------------------------------------------
{
  const { ctx, page, errors } = await open('');
  ok('unpartitioned: no notice element at all', !(await page.$('#partnote')));
  ok('unpartitioned: the Shared view', (await segs(page)).includes('Shared'), (await segs(page)).join(','));
  ok('unpartitioned: Share in the row menu', (await menu(page)).includes('Share…'));
  await page.click('#runs .run[data-id="1"]');
  await page.waitForSelector('#top .sharepill');
  ok('unpartitioned: the top bar\'s sharing pill', true);
  ok('unpartitioned: GET /sandboxes isn\'t read at start', !(await page.evaluate(() => window.__calls.some((c) => /\/sandboxes/.test(c.url)))));
  await hide(page, true);
  await page.waitForTimeout(150);
  ok('unpartitioned: a hidden page keeps its stream', (await page.evaluate(() => window.__streams())) > 0);
  ok('unpartitioned: no page errors', !errors.length, errors.join(' | '));
  await ctx.close();
}

// --- a person's partition ----------------------------------------------------------------
{
  const { ctx, page, errors } = await open('user:alice');
  await page.waitForSelector('#partnote .pn.sandbox');
  const text = await page.$eval('#partnote', (e) => e.textContent);
  ok('partition: one banner naming the old manager and the update', /apps\/old/.test(text) && /bx template updates/.test(text) && !/apps\/cs/.test(text), text);
  ok('partition: one banner', (await page.$$('#partnote .pn')).length === 1);
  ok('partition: no Shared view', !(await segs(page)).includes('Shared'), (await segs(page)).join(','));
  ok('partition: no Share in the row menu', !(await menu(page)).includes('Share…'));
  await page.click('#runs .run[data-id="1"]');
  await page.waitForSelector('#top .title');
  ok('partition: no sharing pill in the top bar', !(await page.$('#top .sharepill')));
  await hide(page, true);
  await page.waitForFunction(() => window.__streams() === 0);
  ok('partition: a hidden page closes its stream', true);
  await page.waitForTimeout(200);
  ok('partition: …and doesn\'t reconnect while hidden', (await page.evaluate(() => window.__streams())) === 0);
  await hide(page, false);
  await page.waitForFunction(() => window.__streams() > 0);
  const since = await page.evaluate(() => window.__calls.filter((c) => /\/stream\?/.test(c.url)).pop().url);
  ok('partition: shown again, it reconnects from its cursor', /since=/.test(since) && /run=1/.test(since), since);
  await page.evaluate(() => window.__push({ type: 'run', run: 1, root: 1, data: { id: 1, title: 'my renamed notes', status: 'idle', parentId: 0, rootId: 1, origin: 'chat', mine: true, access: 'owner' } }));
  await page.waitForFunction(() => document.getElementById('runs').textContent.includes('my renamed notes'));
  ok('partition: the reconnected stream delivers', true);
  ok('partition: no page errors', !errors.length, errors.join(' | '));
  await ctx.close();
}

// --- the global instance -------------------------------------------------------------------
{
  const { ctx, page, errors } = await open('global');
  await page.waitForSelector('#partnote .pn.global');
  ok('global: the note to sign in as a person', /sign in as a person/.test(await page.$eval('#partnote', (e) => e.textContent)));
  ok('global: sharing stays', (await segs(page)).includes('Shared') && (await menu(page)).includes('Share…'));
  ok('global: no sandbox banner (the global instance keeps old managers)', !(await page.$('#partnote .pn.sandbox')));
  ok('global: no page errors', !errors.length, errors.join(' | '));
  await ctx.close();
}

await browser.close();
done('partition');
