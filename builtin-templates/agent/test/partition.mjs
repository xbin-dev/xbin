// partition.mjs — the page's three layouts (API.md "Partitioned instances";
// model/partition.js), driving the real tile against backend.mjs:
//
//   - unpartitioned (no xbin.partition): today's page — no notice, the
//     Shared view, Share in the row menu and the top bar, a stream that
//     ignores the page's visibility, an MCP tab of bound servers only;
//   - a person's partition ("user:alice"): the Shared view (the shared
//     space's, B2b), and one of their own conversations (ids from 2^40)
//     shared only by a copy — "Share a copy…" in its row menu and the top
//     bar's pill; one banner naming a bound sandbox manager that can't keep
//     people apart, a stream that closes while the page is hidden and comes
//     back when shown, and the MCP tab's static servers, one with headers
//     marked as working in shared (global) conversations only (test/homes.mjs
//     drives the two homes);
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
const MINE = 2 ** 40 + 1; // a person's own conversation's id (their partition numbers from 2^40)
const userSeed = { ...seed, runs: [{ id: MINE, title: 'my notes', status: 'idle', parentId: 0, rootId: MINE }] };

async function open(partition) {
  const ctx = await browser.newContext();
  await serveTile(ctx);
  await ctx.addInitScript(STUB, String(partition).startsWith('user:') ? userSeed : seed);
  await ctx.addInitScript(() => { // the settings: one static MCP server with headers, one without
    const j = window.__json;
    window.__route('GET', /\/config$/, () => j({ models: {}, mcp: [
      { name: 'gh', url: 'https://mcp.example/gh', headers: { Authorization: 'Bearer secret-token' } },
      { name: 'docs', url: 'https://mcp.example/docs' }] }));
    window.__route('GET', /\/models$/, () => j({ data: [] }));
  });
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
const menu = async (page, id = 1) => {
  await page.click(`#runs .run[data-id="${id}"]`, { button: 'right' });
  const items = await page.$$eval('.rowmenu .mi', (els) => els.map((e) => e.textContent.trim()));
  await page.click('.mback');
  return items;
};
// mcpTab opens ⚙ → MCP and answers its text once the static list had its moment.
const mcpTab = async (page, wantStatic) => {
  await page.click('#gear');
  await page.waitForSelector('#cf-save');
  await page.click('#tabs .tab[data-tab="mcp"]');
  await page.waitForSelector('#sbd .sec h4');
  if (wantStatic) await page.waitForSelector('#mcp-static:not([hidden]) table');
  else await page.waitForTimeout(200);
  return page.$eval('#sbd', (e) => e.textContent);
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
  await hide(page, false);
  const mcp = await mcpTab(page, false);
  ok('unpartitioned: the MCP tab lists the bound servers only, as ever', !(await page.$('#mcp-static')) && !/works in shared/.test(mcp), mcp);
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
  ok('partition: the Shared view (the shared space\'s)', (await segs(page)).includes('Shared'), (await segs(page)).join(','));
  const items = await menu(page, MINE);
  ok('partition: her own conversation is shared by a copy', items.includes('Share a copy…') && !items.includes('Share…'), items.join(','));
  await page.click(`#runs .run[data-id="${MINE}"]`);
  await page.waitForSelector('#top .sharepill');
  ok('partition: the top bar\'s pill offers the copy', /share a copy/.test(await page.$eval('#top .sharepill', (e) => e.title)));
  await hide(page, true);
  await page.waitForFunction(() => window.__streams() === 0);
  ok('partition: a hidden page closes its stream', true);
  await page.waitForTimeout(200);
  ok('partition: …and doesn\'t reconnect while hidden', (await page.evaluate(() => window.__streams())) === 0);
  await hide(page, false);
  await page.waitForFunction(() => window.__streams() > 0);
  const since = await page.evaluate(() => window.__calls.filter((c) => /\/stream\?/.test(c.url)).pop().url);
  ok('partition: shown again, it reconnects from its cursor', /since=/.test(since) && since.includes(`run=${MINE}`), since);
  await page.evaluate((id) => window.__push({ type: 'run', run: id, root: id, data: { id, title: 'my renamed notes', status: 'idle', parentId: 0, rootId: id, origin: 'chat', mine: true, access: 'owner' } }), MINE);
  await page.waitForFunction(() => document.getElementById('runs').textContent.includes('my renamed notes'));
  ok('partition: the reconnected stream delivers', true);
  const rows = async () => { await mcpTab(page, true); return page.$$eval('#mcp-static tr', (trs) => trs.slice(1).map((tr) => tr.textContent.replace(/\s+/g, ' ').trim())); };
  const list = await rows();
  ok('partition: the MCP tab lists the static servers', list.length === 2, list.join(' / '));
  ok('partition: one with headers works in shared (global) conversations only — bind it as a tile or a personal bind',
    /^gh.*works in shared \(global\) conversations only — to use it in your own conversations, bind it as a tile or a personal bind/.test(list[0] || ''), list[0]);
  ok('partition: one without headers works everywhere', /^docs.*every conversation/.test(list[1] || '') && !/shared/.test(list[1] || ''), list[1]);
  ok('partition: never the header itself', !(await page.$eval('#sbd', (e) => e.textContent)).includes('secret-token'));
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
