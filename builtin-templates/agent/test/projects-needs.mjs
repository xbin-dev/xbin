// projects-needs.mjs — what waits on you in a project (#56; API.md §Projects
// in the UI): the conversation list's count split — red, what really waits
// (counts.asks + failed + coord), beside the pull requests ready for review
// (an older backend's needs-you column as before) — and "Waiting for you"
// atop a project's page from GET /projects/{pid}/needs: a task's question
// and the coordinator's own, each with its text and age, red before a
// yellow failure, Open selecting the conversation it waits in, read again
// on the project's `project` event; nothing drawn while nothing waits.
//
//   node test/projects-needs.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';
import { PROJ_STUB, projSeed } from './projects-stub.mjs';
import { needSplit, needOf, pinWords } from '../model/project-pins.js';
import { needRows, toneOf, ageWords } from '../model/project-needs.js';

const { ok, done } = checker();
const NOW = Date.now();

// --- the words, without a page ---------------------------------------------------------------------
const split = needSplit({ state: 'active', counts: { 'needs-you': 5, asks: 2, failed: 1, coord: 1, ready: 2 } });
ok('needSplit: red = asks + failed + coord, never the idle ones', split.asks === 4 && split.ready === 2 && split.total === 6 && split.split, JSON.stringify(split));
ok('…its words say which', split.tip.includes('2 waiting for your answer') && split.tip.includes('1 failed') && split.tip.includes('coordinator'), split.tip);
const old = needSplit({ state: 'active', counts: { 'needs-you': 3, ready: 1 } });
ok('an older backend: its needs-you column, as before', old.asks === 3 && old.ready === 1 && !old.split && needOf({ state: 'active', counts: { 'needs-you': 3, ready: 1 } }) === 4);
ok('an archived project: none', needSplit({ state: 'archived', counts: { asks: 2 } }).total === 0);
const w = pinWords({ id: 7, name: 'Web', state: 'active', counts: { asks: 1, failed: 0, coord: 0, ready: 2 } });
ok('pinWords carries the split', w.asks === 1 && w.ready === 2 && w.need === 3, JSON.stringify(w));

const items = [
  { reason: 'failed', subRun: 0, run: { id: 108, title: 'Workspace failed' }, project: { id: 7, n: 8 }, text: 'git clone: repository not found', since: NOW - 3 * 3600e3 },
  { reason: 'question', subRun: 103, run: { id: 103, title: 'Bump deps' }, project: { id: 7, n: 3 }, text: 'Deploy the fix to staging now?', since: NOW - 2 * 86400e3 },
  { reason: 'question', subRun: 0, run: { id: 300, title: 'Coordinator · Web' }, project: { id: 7, n: 0 }, text: 'Which repo first: web or api?', since: NOW - 5 * 60e3 },
  { reason: 'review', subRun: 0, run: { id: 101, title: 'Fix the login loop' }, project: { id: 7, n: 1 } },
];
const rows = needRows(items, NOW);
ok('needRows: no row for a review (it stays a count)', rows.length === 3 && !rows.some((r) => r.reason === 'review'));
ok('…red first, the longest waiting first', rows.map((r) => r.who).join(',') === '#3,Coordinator,#8', rows.map((r) => r.who).join(','));
ok('…a task\'s: its number, title, why, what, how long', rows[0].title === 'Bump deps' && rows[0].words === 'has a question for you' && rows[0].text === 'Deploy the fix to staging now?'
  && rows[0].age === '2 d ago' && rows[0].run === 103 && rows[0].tone === 'bad', JSON.stringify(rows[0]));
ok('…the coordinator\'s', rows[1].who === 'Coordinator' && !rows[1].title && rows[1].run === 300 && rows[1].age === '5 m ago');
ok('…a failure is yellow', rows[2].tone === 'warn' && rows[2].words === 'failed');
ok('toneOf: a workspace sign-in red, its failure yellow', toneOf({ reason: 'project', ws: 'signin' }) === 'bad' && toneOf({ reason: 'project', ws: 'failed' }) === 'warn'
  && toneOf({ reason: 'login' }) === 'bad' && toneOf({ reason: 'action' }) === 'bad' && toneOf({ reason: 'unfinished' }) === 'warn');
ok('ageWords', ageWords(NOW - 10e3, NOW) === 'just now' && ageWords(0, NOW) === '');

// --- the page ----------------------------------------------------------------------------------------
const browser = await launch();
const seed = projSeed();
seed.projects[0].counts = { queued: 1, working: 1, 'needs-you': 3, pr: 1, done: 1, ready: 1, asks: 1, failed: 1, coord: 1 };
const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
await serveTile(ctx);
await ctx.addInitScript(STUB, seed);
await ctx.addInitScript(PROJ_STUB, seed);
await ctx.addInitScript((list) => {
  window.__needs = list;
  window.__route('GET', /\/projects\/7\/needs$/, () => window.__json({ items: window.__needs }));
}, items);
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
await page.goto(`${ORIGIN}/`);
const until = (fn, arg) => page.waitForFunction(fn, arg, { timeout: 5000 }).then(() => true, () => false);

ok('the list: a red count of what really waits, then the ready one', await until(() => {
  const row = document.querySelector('#projsec .projrow[data-pid="7"]');
  const b = row && [...row.querySelectorAll('.badge')].map((x) => `${x.className}:${x.textContent}`);
  return b && b.join('|') === 'badge pneedb:3|badge unread:1';
}), await page.innerHTML('#projsec').catch(() => ''));
ok('…its tooltip says which', (await page.getAttribute('#projsec .projrow[data-pid="7"] .pneedb', 'title') || '').includes('1 failed'));

await page.goto(`${ORIGIN}/#proj=7`);
ok('Waiting for you atop the project\'s page', await until(() => document.querySelectorAll('#pneeds .pneed').length === 3));
const strip = await page.$$eval('#pneeds .pneed', (els) => els.map((e) => ({ tone: e.dataset.tone, run: e.dataset.run, text: e.textContent.replace(/\s+/g, ' ').trim() })));
ok('…the task\'s question first, with its text and age', strip[0].tone === 'bad' && strip[0].text.includes('#3') && strip[0].text.includes('Deploy the fix to staging now?')
  && strip[0].text.includes('2 d ago'), JSON.stringify(strip[0]));
ok('…the coordinator\'s question', strip[1].tone === 'bad' && strip[1].text.includes('Coordinator') && strip[1].text.includes('Which repo first'), JSON.stringify(strip[1]));
ok('…the failure yellow, last', strip[2].tone === 'warn' && strip[2].text.includes('repository not found'), JSON.stringify(strip[2]));
ok('…the strip lit red', await page.$eval('#pneeds', (e) => e.dataset.tone === 'bad' && e.getBoundingClientRect().top < 200));
ok('…above the board', await page.evaluate(() => {
  const s = document.querySelector('#pneeds'), b = document.querySelector('.pboard');
  return !!s && !!b && (s.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0;
}));

// a `project` event reads it again: the coordinator's question answered
await page.evaluate(() => { window.__needs = window.__needs.filter((n) => n.run.id !== 300); });
await page.evaluate(() => window.__push({ type: 'project', run: 0, root: 0, data: { id: 7, change: 'coordinator' } }));
ok('a project event reads it again', await until(() => document.querySelectorAll('#pneeds .pneed').length === 2));

await page.click('#pneeds .pneed[data-run="103"] .pnopen');
ok('Open selects the conversation it waits in', await until(() => location.hash === '#c=103'), await page.evaluate(() => location.hash));

// nothing waits: no strip
await page.evaluate(() => { window.__needs = []; });
await page.goto(`${ORIGIN}/#proj=7`);
await page.waitForSelector('.pboard');
await page.waitForTimeout(300);
ok('nothing waits: no strip', !(await page.$('#pneeds')));
ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('projects-needs');
