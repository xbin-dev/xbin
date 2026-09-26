// automations.mjs — the Automations page (D83): the sidebar entry with what
// is new, automations grouped by kind (someone else's shown to a manager for
// oversight, without what it does), one automation's runs (reading marks them
// read; a run opens with a way back), and the new-schedule form.
//
//   node test/automations.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';

const { ok, done } = checker();
const now = Date.now();
const seed = {
  runs: [
    { id: 1, title: 'a chat', status: 'idle', parentId: 0, rootId: 1, activityMs: now },
    { id: 20, title: '⏱ Morning digest', status: 'idle', parentId: 0, rootId: 20, origin: 'schedule', originId: 3, activityMs: now },
  ],
  automations: [
    { kind: 'schedule', id: 3, name: 'Morning digest', owner: 'admin', access: 'owner', enabled: true, mode: 'isolated', visibility: 'private',
      config: { cron: '0 9 * * *', goal: 'digest the inbox' }, runs: 1, unread: 2, lastStatus: 'ok', lastRunAt: now / 1000 - 60 },
    { kind: 'watcher', id: 4, name: 'Watch invoices', owner: 'admin', access: 'owner', enabled: true, visibility: 'private',
      config: { cron: '@every 1h', goal: 'new invoices?' }, runs: 1, unread: 0 },
    { kind: 'schedule', id: 5, name: 'bob report', owner: 'bob', access: 'oversee', enabled: true, summary: '@daily', runs: 0, unread: 0 },
  ],
  autoRuns: { 3: [{ id: 20, title: '⏱ Morning digest', status: 'idle', activityMs: now, unread: true }] },
};

const browser = await launch();
const ctx = await browser.newContext();
await serveTile(ctx);
await ctx.addInitScript(STUB, seed);
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
await page.goto(`${ORIGIN}/`);
await page.waitForSelector('#autos .autos-entry');
await page.waitForFunction(() => document.querySelector('#autos .badge.unread')?.textContent === '2');
ok('the sidebar entry says what is new', true);
ok('automation runs are not in the conversation list', !(await page.textContent('#runs')).includes('Morning digest'));

await page.click('#autos .autos-entry');
await page.waitForSelector('.autos-page .acard2');
const groups = await page.$$eval('.autos-page h5', (els) => els.map((e) => e.textContent));
ok('grouped by kind', JSON.stringify(groups) === JSON.stringify(['Channels', 'Schedules', 'Watchers']), groups.join(' | '));
const bob = await page.textContent('.acard2[data-auto="schedule:5"]');
ok('someone else\'s, overseen: whose it is, not what it does', bob.includes("bob's") && !bob.includes('Run now'), bob);
ok('#auto is in the address', await page.evaluate(() => location.hash === '#auto'));

await page.click('.acard2[data-auto="schedule:3"]');
await page.waitForSelector('.autos-page .agoal');
ok('an automation shows what it does', (await page.textContent('.autos-page .agoal')).includes('digest the inbox'));
await page.waitForFunction(() => window.__calls.some((c) => c.method === 'POST' && c.url.endsWith('/automations/schedule/3/read')));
ok('opening it marks its runs read', true);
await page.click('.autos-page .run[data-id="20"]');
await page.waitForFunction(() => document.querySelector('#top .title')?.textContent === '⏱ Morning digest');
ok('a run opens in the chat view', true);
ok('…with the way back to its automation', (await page.textContent('#top .crumb')).includes('Automations'));
await page.click('#top .crumb');
await page.waitForSelector('.autos-page .agoal');
ok('the crumb returns to the automation', (await page.textContent('.autos-page')).includes('Morning digest'));

// a new schedule
await page.click('.autos-page .crumb');
await page.waitForSelector('.autos-page button:has-text("New schedule")');
await page.click('.autos-page button:has-text("New schedule")');
await page.fill('.autos-page input[placeholder="Morning digest"]', 'Weekly report');
await page.selectOption('.autos-page select >> nth=0', '0 9 * * 1-5');
await page.fill('.autos-page textarea', 'sum up the week');
await page.click('.autos-page button:has-text("Create")');
await page.waitForFunction(() => window.__calls.some((c) => c.method === 'POST' && c.url.endsWith('/schedules')));
const body = await page.evaluate(() => JSON.parse(window.__calls.find((c) => c.method === 'POST' && c.url.endsWith('/schedules')).body));
ok('the form creates it', body.name === 'Weekly report' && body.cron === '0 9 * * 1-5' && body.goal === 'sum up the week'
  && body.mode === 'isolated' && body.visibility === 'private', JSON.stringify(body));

ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('automations');
