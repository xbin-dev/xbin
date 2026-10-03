// workflow.mjs — the workflow pane (workflow.js): a conversation's tree of
// runs, opened from the top bar's ⑂ chip. Rows in creation order within a
// parent, a header of counts and spend, a value change PATCHES the rows it
// touches (the row under the pointer stays the same element), a new node
// rebuilds; a row opens its run, Stop cancels the subtree, ✕ and Esc close.
//
//   node test/workflow.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';

const { ok, done } = checker();
const root = { id: 1, title: 'fan out', status: 'running', parentId: 0, rootId: 1 };
const node = (id, parentId, depth, more) => ({ id, parentId, depth, created: id, title: 'run ' + id, status: 'running', ...more });
const TREE = {
  root: 1,
  nodes: [
    node(1, 0, 0, { title: 'root task', promptTokens: 1000, completionTokens: 234 }),
    node(3, 1, 1, { status: 'queued', blockReason: 'dep', blockedOn: [2] }),
    node(2, 1, 1, { status: 'error', result: 'boom', promptTokens: 500 }),
    node(4, 2, 2, { status: 'queued', blockReason: 'slot' }),
  ],
  totals: { nodes: 4, byStatus: { running: 1, queued: 2, error: 1 }, promptTokens: 1500, completionTokens: 234, llmCalls: 7, active: 1, limit: 4 },
};

const browser = await launch();
const ctx = await browser.newContext();
await serveTile(ctx);
await ctx.addInitScript(STUB, { runs: [root], views: { 1: { run: root, linkCount: 3 } } });
await ctx.addInitScript((t) => {
  window.__tree = t;
  window.__route('GET', /\/runs\/1\/tree$/, () => window.__json(window.__tree));
}, TREE);
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
page.on('dialog', (d) => d.accept());
await page.goto(`${ORIGIN}/#c=1`);
await page.click('#top .wfchip');
await page.waitForSelector('#wf-body [data-n]');

const rows = () => page.$$eval('#wf-body [data-n]', (els) => els.map((e) => ({
  n: +e.dataset.n, d: e.style.getPropertyValue('--d'), dot: e.querySelector('.dot').className,
  sub: e.querySelector('.sub').textContent, cls: e.querySelector('.sub').className, cost: e.querySelector('.cost').textContent,
  ic: e.querySelector('.sub bx-icon')?.getAttribute('name') || '', // its glyph (D184)
  bar: e.querySelector('.share > i')?.style.width || '' })));
let r = await rows();
ok('the pane opens beside the chat', await page.evaluate(() => !document.getElementById('workflow').hidden && document.getElementById('main').classList.contains('wfon')));
ok('rows in creation order within a parent, depth-first', r.map((x) => x.n).join() === '1,2,4,3', r.map((x) => x.n).join());
ok('…indented by depth', r.map((x) => x.d).join() === '0,1,2,1', r.map((x) => x.d).join());
ok('an error says its result', r[1].sub === 'boom' && r[1].cls === 'sub bad' && r[1].ic === 'warning', JSON.stringify(r[1]));
ok('a dependency wait names what it waits on', r[3].sub === 'waiting on #2' && r[3].cls === 'sub blk' && r[3].ic === 'error', JSON.stringify(r[3]));
ok('a slot wait says so', r[2].sub === 'queued — at the concurrency limit' && r[2].ic === 'wait', r[2].sub);
ok('spend with a share of the largest', r[0].bar === '100%' && r[1].bar === '41%' && r[2].cost === '', JSON.stringify(r.map((x) => [x.cost, x.bar])));
const head = await page.evaluate(() => ['wf-title', 'wf-counts', 'wf-cost'].map((id) => document.getElementById(id).textContent));
ok('the header: the root, counts', head[0] === 'root task' && head[1] === '4 nodes · 1 running · 2 queued · 1 error', head.join(' | '));
ok('…and spend as a rate', /^Σ 1.500↑ 234↓ · 7 calls · 1\/4 running$/.test(head[2]), head[2]);

// a value change patches the row in place; a new node rebuilds
await page.evaluate(() => { document.querySelector('#wf-body [data-n="3"]').__kept = 1; });
const push = (ev) => page.evaluate((e) => window.__push(e), ev);
await page.waitForFunction(() => window.__streams() > 0);
await page.evaluate(() => {
  const n3 = window.__tree.nodes.find((n) => n.id === 3);
  Object.assign(n3, { status: 'running', blockReason: '', lastStep: 'reading files', updated: 2 });
});
await push({ type: 'run', run: 1, root: 1, data: { ...root, updated: 2 } });
await page.waitForFunction(() => document.querySelector('#wf-body [data-n="3"] .sub').textContent === 'reading files');
r = await rows();
ok('a changed value is patched in', r[3].dot === 'dot running' && r[3].cls === 'sub ', JSON.stringify(r[3]));
ok('…into the same element', await page.evaluate(() => document.querySelector('#wf-body [data-n="3"]').__kept === 1));
await page.evaluate(() => window.__tree.nodes.push({ id: 5, parentId: 1, depth: 1, created: 5, title: 'late', status: 'running' }));
await push({ type: 'run', run: 1, root: 1, data: { ...root, updated: 3 } });
await page.waitForSelector('#wf-body [data-n="5"]');
ok('a new node rebuilds the rows', await page.evaluate(() => !document.querySelector('#wf-body [data-n="3"]').__kept));

// Stop cancels the subtree (after a confirm) and reads the tree again
const trees = () => page.evaluate(() => window.__calls.filter((c) => c.url.endsWith('/runs/1/tree')).length);
const before = await trees();
await page.click('#wf-stop');
await page.waitForFunction(() => window.__calls.some((c) => c.method === 'POST' && c.url.endsWith('/runs/1/cancel')));
const body = await page.evaluate(() => window.__calls.find((c) => c.url.endsWith('/runs/1/cancel')).body);
ok('Stop cancels the subtree', body === JSON.stringify({ scope: 'subtree', reason: 'stopped from the tile' }), body);
await page.waitForFunction((n) => window.__calls.filter((c) => c.url.endsWith('/runs/1/tree')).length > n, before);
ok('…and reads the tree again', true);

// ✕ and Esc close it; a row opens its run (and closes the tree)
const shown = () => page.evaluate(() => !document.getElementById('workflow').hidden);
await page.click('#wf-close');
ok('✕ closes the pane', !(await shown()) && !(await page.evaluate(() => document.getElementById('main').classList.contains('wfon'))));
await page.click('#top .wfchip');
await page.waitForFunction(() => !document.getElementById('workflow').hidden);
await page.keyboard.press('Escape');
ok('Esc closes it', !(await shown()));
await page.click('#top .wfchip');
await page.waitForSelector('#wf-body [data-n="2"]');
await page.click('#wf-body [data-n="2"]');
await page.waitForFunction(() => location.hash === '#c=2');
ok('a row opens its run, and the tree closes', !(await shown()));

ok('no page errors', errors.length === 0, errors.join('; '));
await browser.close();
done('workflow');
