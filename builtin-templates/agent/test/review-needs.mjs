// review-needs.mjs — a project task whose pull request passed CI waits for
// you (_backend ciPassedNeeds; API.md "Needs you"): Needs you lists it with
// its reason and project, the task's state says it, and the projects' need-
// you count (model/project-pins.js needOf) takes it in.
//
//   node test/review-needs.mjs     (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';
import { needOf } from '../model/project-pins.js';
import { STATES, WAITING } from '../model/project-task.js';
import { REASON } from '../model/home.js';

const { ok, done } = checker();

// the words, without a page
ok('needOf: a ready task needs you too', needOf({ state: 'active', counts: { 'needs-you': 1, ready: 2 } }) === 3);
ok('…and an archived project none', needOf({ state: 'archived', counts: { 'needs-you': 1, ready: 2 } }) === 0);
ok('the state words', STATES['ci-passed'] && STATES['ci-passed'][0].includes('CI passed') && WAITING.you);
ok('the reason words', REASON.review.includes('passed CI'));

const browser = await launch();
const ctx = await browser.newContext({ viewport: { width: 760, height: 900 } });
await serveTile(ctx);
await ctx.addInitScript(STUB, { runs: [{ id: 3, title: 'fix the header', status: 'idle', parentId: 0, rootId: 3, activityMs: 1000, origin: 'project', originId: 7 }], views: {} });
await ctx.addInitScript(() => {
  window.__route('GET', /\/needs$/, () => window.__json({ items: [{ reason: 'review', subRun: 0,
    run: { id: 3, title: 'fix the header', status: 'idle', origin: 'project' }, project: { id: 7, name: 'Web', n: 2 } }] }));
});
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
await page.goto(`${ORIGIN}/`);
await page.waitForSelector('.home .need[data-reason="review"]', { timeout: 5000 }).catch(() => {});
const text = (await page.textContent('.home .need[data-reason="review"]').catch(() => '')) || '';
ok('Needs you lists the task, with why', text.includes('fix the header') && text.includes('passed CI'), text);
ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('review-needs');
