// sidebar.mjs — the run list is top-level runs, and only them.
//
// Subagents live inside their parent's session (and the workflow tree), never
// as rows here: a fan-out used to flood the list, and quick-ask subagents
// showed up as "orphans" whenever their quick ask was not selected. This
// drives the real tile against backend.mjs and throws everything at the list
// the stream can carry — a subagent's run events, a new top-level run, a
// status change, a deletion — and checks what the list shows.
//
//   node test/sidebar.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';

const { ok, done } = checker();
const seed = {
  runs: [
    { id: 1, title: 'plan the quarter', status: 'awaiting', parentId: 0, rootId: 1, kind: '' },
    { id: 5, title: 'unrelated task', status: 'idle', parentId: 0, rootId: 5, kind: '' },
    { id: 6, title: 'a quick question', status: 'idle', parentId: 0, rootId: 6, kind: 'quick' },
  ],
};

const browser = await launch();
const ctx = await browser.newContext();
await serveTile(ctx);
await ctx.addInitScript(STUB, seed);
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
await page.goto(`${ORIGIN}/`);
await page.waitForSelector('#runs .run');
await page.waitForFunction(() => window.__streams() > 0);
const rows = () => page.$$eval('#runs .run .t', (els) => els.map((e) => e.textContent.trim()));
const push = (ev) => page.evaluate((e) => window.__push(e), ev);

ok('top-level tasks are listed', JSON.stringify(await rows()) === JSON.stringify(['unrelated task', 'plan the quarter']), (await rows()).join(' | '));
ok('the list asked for roots only', await page.evaluate(() => window.__calls.some((c) => c.url.endsWith('/runs?roots=1'))));

// A subagent's events: never a row.
for (const id of [2, 3, 4]) {
  await push({ type: 'run', run: id, root: 1, data: { id, title: 'subagent ' + id, status: 'running', parentId: 1, rootId: 1 } });
}
await page.waitForTimeout(150);
ok('a subagent never becomes a row', !(await rows()).some((r) => r.startsWith('subagent')), (await rows()).join(' | '));

// A new top-level run appears; a status change repaints its badge.
await push({ type: 'run', run: 9, root: 9, data: { id: 9, title: 'new task', status: 'running', parentId: 0, rootId: 9 } });
await page.waitForFunction(() => document.getElementById('runs').textContent.includes('new task'));
ok('a new top-level run appears at the top', (await rows())[0] === 'new task');
await push({ type: 'run', run: 5, root: 5, data: { id: 5, status: 'error', parentId: 0 } });
await page.waitForFunction(() => [...document.querySelectorAll('#runs .run')]
  .find((r) => r.textContent.includes('unrelated task'))?.querySelector('.badge')?.textContent === 'error');
ok('a status change repaints the badge', true);

// Quick asks stay on home, unless open.
ok('a quick ask is not a row', !(await rows()).includes('⚡ a quick question'));
await page.click('.home .qa');
await page.waitForFunction(() => document.querySelector('#top .title')?.textContent === 'a quick question');
ok('…except the one you have open', (await rows()).includes('⚡ a quick question'), (await rows()).join(' | '));

// Deletion removes the row.
await push({ type: 'run', run: 9, root: 9, data: { id: 9, deleted: true } });
await page.waitForFunction(() => !document.getElementById('runs').textContent.includes('new task'));
ok('a deleted run leaves the list', true);

ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('sidebar');
