// sidebar.mjs — the conversation list (D83): your conversations, newest
// activity first in date groups, your pins on top; search; a row menu to
// rename, pin and archive; and never a subagent.
//
// Subagents live inside their parent's session (and the workflow tree), never
// as rows here: a fan-out used to flood the list. This drives the real tile
// against backend.mjs and throws everything at the list the stream can carry
// — a subagent's run events, a new conversation, a status change, your own
// pin state, a revocation, a deletion — and checks what the list shows.
//
//   node test/sidebar.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';

const { ok, done } = checker();
const now = Date.now();
const DAY = 86400000;
const seed = {
  runs: [
    { id: 1, title: 'plan the quarter', status: 'awaiting', parentId: 0, rootId: 1, activityMs: now - 60000 },
    { id: 5, title: 'unrelated task', status: 'idle', parentId: 0, rootId: 5, activityMs: now - DAY },
    { id: 6, title: 'a quick question', status: 'idle', parentId: 0, rootId: 6, kind: 'quick', activityMs: now - 3 * DAY },
    { id: 7, title: 'a report', status: 'idle', parentId: 0, rootId: 7, origin: 'schedule', activityMs: now },
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
const groups = () => page.$$eval('#runs .grp', (els) => els.map((e) => e.textContent.trim()));
const push = (ev) => page.evaluate((e) => window.__push(e), ev);

ok('conversations, newest activity first — quick asks included, automation runs not',
  JSON.stringify(await rows()) === JSON.stringify(['plan the quarter', 'unrelated task', 'a quick question']), (await rows()).join(' | '));
ok('in date groups', JSON.stringify(await groups()) === JSON.stringify(['Today', 'Yesterday', 'Previous 7 days']), (await groups()).join(' | '));
ok('the list came from GET /conversations', await page.evaluate(() => window.__calls.some((c) => c.url.includes('/conversations?'))));

// A subagent's events: never a row.
for (const id of [2, 3, 4]) {
  await push({ type: 'run', run: id, root: 1, data: { id, title: 'subagent ' + id, status: 'running', parentId: 1, rootId: 1, origin: 'chat', mine: true } });
}
await page.waitForTimeout(150);
ok('a subagent never becomes a row', !(await rows()).some((r) => r.startsWith('subagent')), (await rows()).join(' | '));

// A new conversation of yours appears at the top; someone else's private one doesn't.
await push({ type: 'run', run: 9, root: 9, data: { id: 9, title: 'new task', status: 'running', parentId: 0, rootId: 9, origin: 'chat', mine: true, access: 'owner', activityMs: Date.now() } });
await page.waitForFunction(() => document.getElementById('runs').textContent.includes('new task'));
ok('a new conversation appears at the top', (await rows())[0] === 'new task');
ok('…with a spinner while it works', !!(await page.$('#runs .run[data-id="9"] .spin')));
await push({ type: 'run', run: 11, root: 11, data: { id: 11, title: 'bob stuff', status: 'idle', parentId: 0, rootId: 11, origin: 'chat', mine: false, owner: 'bob', visibility: 'team', access: 'viewer' } });
await page.waitForTimeout(150);
ok('someone else\'s team conversation stays out of "Mine"', !(await rows()).includes('bob stuff'));

// Pin moves it to the top group; the menu drives PATCH.
await page.click('#runs .run[data-id="5"]', { button: 'right' });
await page.click('.rowmenu .mi:has-text("Pin")');
await page.waitForFunction(() => document.querySelector('#runs .grp')?.textContent === 'Pinned');
ok('pinning moves it under Pinned', (await rows())[0] === 'unrelated task');
ok('…through PATCH /runs/5', await page.evaluate(() => window.__calls.some((c) => c.method === 'PATCH' && c.url.endsWith('/runs/5') && c.body.includes('pinned'))));

// Rename inline.
await page.hover('#runs .run[data-id="1"]'); // the ⋯ shows on hover
await page.click('#runs .run[data-id="1"] .rmenu');
await page.click('.rowmenu .mi:has-text("Rename")');
await page.fill('#runs .ren', 'Q3 plan');
await page.press('#runs .ren', 'Enter');
await page.waitForFunction(() => document.getElementById('runs').textContent.includes('Q3 plan'));
ok('rename in place', true);

// Archive takes it out of the list.
await page.click('#runs .run[data-id="6"]', { button: 'right' });
await page.click('.rowmenu .mi:has-text("Archive")');
await page.waitForFunction(() => !document.getElementById('runs').textContent.includes('a quick question'));
ok('archive removes it from the list', true);
await page.click('#sfoot a:has-text("Archived")');
await page.waitForFunction(() => document.getElementById('runs').textContent.includes('a quick question'));
ok('…and it is in Archived', true);
await page.click('#sfoot a:has-text("Mine")');
await page.waitForFunction(() => document.getElementById('runs').textContent.includes('Q3 plan'));

// Search.
await page.fill('#csearch', 'unrel');
await page.waitForFunction(() => document.querySelectorAll('#runs .run').length === 1);
ok('search narrows to matches', (await rows())[0] === 'unrelated task');
await page.fill('#csearch', '');
await page.waitForFunction(() => document.getElementById('runs').textContent.includes('Q3 plan'));

// A status change repaints its glyph; losing access or a deletion removes the row.
await push({ type: 'run', run: 5, root: 5, data: { id: 5, status: 'error', parentId: 0, origin: 'chat' } });
await page.waitForSelector('#runs .run[data-id="5"] .gl.err');
ok('a failure shows on the row', true);
await push({ type: 'revoked', run: 1, root: 1, data: { id: 1 } });
await page.waitForFunction(() => !document.getElementById('runs').textContent.includes('Q3 plan'));
ok('a conversation you lost access to leaves the list', true);
await push({ type: 'run', run: 9, root: 9, data: { id: 9, title: 'new task', status: 'idle', parentId: 0, rootId: 9, origin: 'chat', mine: true, activityMs: Date.now() } });
await page.waitForFunction(() => document.getElementById('runs').textContent.includes('new task'));
await push({ type: 'run', run: 9, root: 9, data: { id: 9, deleted: true } });
await page.waitForFunction(() => !document.getElementById('runs').textContent.includes('new task'));
ok('a deleted conversation leaves the list', true);

ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('sidebar');
