// ci.mjs — CI in the conversation on the web (API.md §CI in the
// conversation: ci-dock.js, ci-cards.js, the dock host in harness-board.js)
// over kidsSeed()'s #25 (three coding agents, test/harness-fixtures.mjs)
// with CI (test/ci-stub.mjs): the chip after the coding agents chip, read
// once when the conversation opens; the dock's tabs, the CI section — the
// watch, its PR, a run, its jobs with progress, a job's steps, its
// annotations as text, another check, a status, links only http(s); the
// child glyph on the coding agent that pushed; the log viewer — a running
// job's steps and Open live log, then the finished log: plain text (a
// token masked, markup inert), search with next/previous, Earlier, the dock
// wider, ← Back; Re-run failed (confirmed); Watch CI for… and ✕; the
// outcome card from a `ci` event, Open logs, dismissed for good; the
// board's CI chip (ext.card) opening the task on CI; a phone's width.
//
//   node test/ci.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';
import { kidsSeed } from './harness-fixtures.mjs';
import { CI_STUB, ciSeed, ciView, SHA1, TOKEN } from './ci-stub.mjs';

const { ok, done } = checker();
const browser = await launch();

async function open(seed, { width = 1280, hash = '#c=25' } = {}) {
  const ctx = await browser.newContext({ viewport: { width, height: 900 } });
  await serveTile(ctx);
  await ctx.addInitScript(STUB, seed);
  await ctx.addInitScript(CI_STUB, seed);
  const page = await ctx.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
  page.on('dialog', (d) => d.accept());
  await page.goto(`${ORIGIN}/${hash}`);
  return { page, errors, ctx };
}
const waitText = (page, sel, want) => page.waitForFunction(([s, w]) => (document.querySelector(s)?.textContent || '').includes(w), [sel, want], { timeout: 5000 })
  .then(() => true, () => false);
const noHScroll = (page) => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth && document.body.scrollWidth <= window.innerWidth);

function ciSeedFor() {
  const seed = kidsSeed();
  const v = ciView(25, { run: 26 });
  seed.views[25].ci = { summary: v.summary, canWatch: true };
  seed.ci = ciSeed(25, { run: 26 });
  return seed;
}

// === the chip, the dock, the section ========================================================================
const { page, errors } = await open(ciSeedFor());
await page.waitForSelector('#cichip');
ok('the chip comes after the coding agents chip', await page.evaluate(() => {
  const a = document.getElementById('hbchip'), b = document.getElementById('cichip');
  return !!a && !!(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING);
}));
ok('the chip says what failed', await waitText(page, '#cichip', 'CI ✗ codecov/patch'), await page.textContent('#cichip'));
ok('…in the failing tone', (await page.getAttribute('#cichip', 'data-tone')) === 'bad');
ok('the conversation\'s CI is read once on opening (not fresh)', await page.evaluate(() => window.__ci.reads.length === 1 && !window.__ci.reads[0].fresh),
  JSON.stringify(await page.evaluate(() => window.__ci.reads)));
ok('the coding agent that pushed has a CI glyph', await page.waitForFunction(() => document.querySelector('.hkid[data-child="26"] .cichild')?.textContent === 'CI ✗',
  null, { timeout: 5000 }).then(() => true, () => false));
ok('…the others none', !(await page.$('.hkid[data-child="27"] .cichild')));

await page.click('#cichip');
await page.waitForSelector('#hboard .hbsec[data-sec="ci"] #cisec');
ok('the dock opens on CI, its tabs Coding agents · CI', JSON.stringify(await page.$$eval('#hboard .hbtab', (els) => els.map((e) => `${e.dataset.tab}:${e.getAttribute('aria-selected')}`)))
  === '["agents:false","ci:true"]');
ok('…a fresh read when the dock shows it', await page.waitForFunction(() => window.__ci.reads.some((r) => r.fresh), null, { timeout: 5000 }).then(() => true, () => false));
const watch = await page.textContent('.ciwatch[data-watch="4"] .ciwh');
ok('the watch: repo · branch, its PR, its state', watch.includes('acme/web · feature') && watch.includes('PR #42') && watch.includes('failure'), watch);
ok('…the branch and PR are links to the platform', JSON.stringify(await page.$$eval('.ciwatch[data-watch="4"] .ciwh a', (els) => els.map((a) => [a.textContent, a.href, a.target, a.rel])))
  === JSON.stringify([['acme/web · feature', 'https://github.com/acme/web/tree/feature', '_blank', 'noopener noreferrer'], ['PR #42', 'https://github.com/acme/web/pull/42', '_blank', 'noopener noreferrer']]));
const run = await page.textContent('.cirun[data-run="7001"] .cirunh');
ok('a run: name, event, state', run.includes('ci') && run.includes('push') && run.includes('in_progress'), run);
ok('jobs with progress bars', JSON.stringify(await page.$$eval('.cijob', (els) => els.map((e) => [e.dataset.job, e.querySelector('.cibar > span')?.style.width || ''])))
  === '[["88000","100%"],["88001","33%"],["88002",""]]');
ok('…the running job\'s current step', (await page.textContent('.cijob[data-job="88001"] .cicur')).startsWith('go test ./...'));
await page.click('.cijob[data-job="88001"] .cirow');
const steps = await page.$$eval('.cijob[data-job="88001"] .cistep', (els) => els.map((e) => [e.querySelector('.cist').textContent, e.querySelector('.ciname').textContent,
  e.querySelector('.muted').textContent]));
ok('expanded: its steps with their marks and times', JSON.stringify(steps.map((x) => x.slice(0, 2))) === JSON.stringify([['✓', 'Set up job'], ['●', 'go test ./...'], ['○', 'upload']])
  && steps[0][2] === '2s' && steps[2][2] === '', JSON.stringify(steps));
const check = await page.textContent('.cicheck[data-check="88100"]');
ok('another check: its name, its title as text', check.includes('codecov/patch') && check.includes('62% of diff hit'), check);
await page.click('.cicheck[data-check="88100"] .cinotesbtn');
await page.waitForSelector('.cinote');
ok('annotations: path:line, level, message — as text', JSON.stringify(await page.$$eval('.cinote', (els) => els.map((e) => e.querySelector('.mono').textContent + ' ' + e.dataset.level)))
  === '["auth/login.go:12 failure","auth/session.go:3 warning"]');
ok('…markup in a message stays text', (await page.$$eval('.cinote .cimsg', (els) => els[1].textContent)) === '<img src=x onerror=alert(1)>'
  && !(await page.$('.cinote img')));
ok('a status: jenkins passed', (await page.textContent('.cistatus')).includes('ci/jenkins'));
ok('no Re-run on a run still going', !(await page.$('.cirerun')));
ok('the dock\'s badge says the state', (await page.textContent('.hbtab[data-tab="ci"]')).includes('✗'));

// --- the log viewer ----------------------------------------------------------------------------------
await page.click('.cijob[data-job="88001"] .cilog');
await page.waitForSelector('#cilogwait');
ok('a running job (logs once it finishes): its steps, the log is ready when it finishes', (await page.textContent('#cilogwait')).includes('the log is ready when the job finishes')
  && (await page.$$('#cilogwait .cistep')).length === 3);
ok('…Open live log ↗', (await page.getAttribute('#cilogwait a.cilink', 'href')) === 'https://github.com/acme/web/actions/runs/7001/job/88001');
ok('…the dock wider while the viewer is shown', await page.evaluate(() => document.querySelector('.wrap').classList.contains('ciwide')
  && document.getElementById('hboard').getBoundingClientRect().width > 400));
await page.click('#ci-back');
ok('← Back: the section again, the dock its width', !!(await page.$('#cisec')) && !(await page.evaluate(() => document.querySelector('.wrap').classList.contains('ciwide'))));
// the job finishes: a ci event
await page.evaluate(() => {
  const S = window.__ci;
  S.logs['88001'].running = false;
  const w = S.views[25].watches[0];
  const j = w.checks.workflowRuns[0].jobs[1];
  Object.assign(j, { status: 'completed', conclusion: 'failure' });
  j.steps[1].status = 'completed'; j.steps[1].conclusion = 'failure'; j.steps[2].status = 'completed'; j.steps[2].conclusion = 'skipped';
  Object.assign(w.checks.workflowRuns[0], { status: 'completed', conclusion: 'failure' });
  w.outcome = '9fceb02d0ae598e95dc970b74767f19372d61af8:failure';
  S.views[25].summary = { state: 'failure', jobs: { total: 5, done: 4, failed: 2, running: 0, queued: 1 } };
  window.__push({ type: 'ci', run: 25, root: 25, data: { root: 25, watch: 4, state: 'failure', outcome: w.outcome, summary: S.views[25].summary,
    watches: [{ id: 4, state: 'failure', outcome: w.outcome, run: 26, repo: 'acme/web', ref: 'feature' }] } });
});
ok('the outcome card arrives with the event', await waitText(page, '.cicard-out', 'CI failed on feature — test (ubuntu) › go test ./...'), await page.textContent('.cicards').catch(() => ''));
ok('Re-run failed on the failed run', await page.waitForSelector('.cirerun', { timeout: 5000 }).then(() => true, () => false));
await page.click('.cicard-out [data-act="logs"]');
await page.waitForSelector('#cilog');
const log = await page.textContent('#cilog');
ok('Open logs: the job\'s log, plain text', log.includes('--- FAIL: TestLogin') && !log.includes('\x1b'), log.slice(0, 200));
ok('Earlier pages back', !!(await page.$('#ci-earlier')));
for (let i = 0; i < 12 && await page.$('#ci-earlier'); i++) {
  const before = (await page.textContent('#cilog')).length;
  await page.click('#ci-earlier');
  await page.waitForFunction((n) => document.getElementById('cilog').textContent.length > n || !document.getElementById('ci-earlier'), before, { timeout: 5000 });
}
const all = await page.textContent('#cilog');
ok('…prepending what came before, to its start', all.startsWith('##[group]Run go test ./...\npassword='), all.slice(0, 80));
ok('…the token masked, no escape codes', !all.includes(TOKEN) && all.includes('password=ghs_[redacted]') && !all.includes('\x1b'));
await page.fill('#ci-search', 'pkg1');
const hits = await page.textContent('#ci-hits');
ok('search: hits counted and marked', /^1\/\d+$/.test(hits) && (await page.$$('#cilog mark')).length > 1, hits);
await page.click('#ci-next');
ok('…next moves to the second', (await page.textContent('#ci-hits')).startsWith('2/') && (await page.getAttribute('#cilog mark.cur', 'data-hit')) === '1');
await page.click('#ci-prev');
ok('…previous back to the first', (await page.textContent('#ci-hits')).startsWith('1/'));
ok('a finished job: no Follow', !(await page.$('#ci-follow')));
ok('no horizontal scroll', await noHScroll(page));
await page.click('#ci-back');

// --- re-run, watch, unwatch ---------------------------------------------------------------------------
await page.click('.cirerun');
ok('Re-run failed: confirmed, as you, its failed jobs', await page.waitForFunction(() => window.__ci.reruns.length === 1, null, { timeout: 5000 }).then(() => true, () => false)
  && JSON.stringify(await page.evaluate(() => window.__ci.reruns[0])) === '{"watch":4,"runId":"7001","failedOnly":true}');
await page.fill('#ci-repo', 'acme/web');
await page.fill('#ci-ref', '#17');
await page.click('#ci-watch');
ok('Watch CI for… a PR number', await page.waitForFunction(() => window.__ci.watches.length === 1, null, { timeout: 5000 }).then(() => true, () => false)
  && JSON.stringify(await page.evaluate(() => window.__ci.watches[0])) === '{"repo":"acme/web","pr":17}');
await page.waitForSelector('.ciwatch[data-watch="101"]');
await page.click('.ciwatch[data-watch="101"] .ciun');
ok('✕ stops watching it', await page.waitForFunction(() => window.__ci.unwatched.includes(101), null, { timeout: 5000 }).then(() => true, () => false));
await page.fill('#ci-repo', 'nope');
await page.click('#ci-watch');
ok('a bad repo is said, not sent', (await page.textContent('#ciform .err')).includes('owner/name') && (await page.evaluate(() => window.__ci.watches.length)) === 1);

// --- the card dismissed; the agents tab ------------------------------------------------------------------
await page.click('.cicard-out [data-act="dismiss"]');
ok('✕ dismisses the card', await page.waitForFunction(() => !document.querySelector('.cicard-out'), null, { timeout: 5000 }).then(() => true, () => false));
ok('…kept in your prefs', await page.waitForFunction(() => window.__calls.some((c) => c.method === 'PUT' && /prefs\/ci-dismissed/.test(c.url)), null, { timeout: 5000 })
  .then(() => true, () => false));
await page.click('.hbtab[data-tab="agents"]');
ok('the Coding agents tab: its rows', !!(await page.waitForSelector('#hboard .hbrow[data-row="26"]', { timeout: 5000 }).catch(() => null)));
await page.click('#hboard [data-act="close"]');
ok('✕ closes the dock', !(await page.$('#hboard:not([hidden])')));
ok('no errors', errors.length === 0, errors.join(' | '));

// === no CI: no chip; a sandbox but nothing watched: CI —, the section only the form ====================================
{
  const seed = kidsSeed();
  const { page: p2, errors: e2 } = await open(seed);
  await p2.waitForSelector('#hbchip');
  await p2.waitForTimeout(200);
  ok('nothing watched and none may be: no chip', !(await p2.$('#cichip')));
  ok('…and CI isn\'t read', (await p2.evaluate(() => window.__ci.reads.length)) === 0);
  await p2.close();
  const seed3 = kidsSeed();
  seed3.views[25].ci = { summary: null, canWatch: true };
  const { page: p3, errors: e3 } = await open(seed3);
  await p3.waitForSelector('#cichip');
  ok('a sandbox, nothing watched: CI —', (await p3.textContent('#cichip')) === 'CI —');
  await p3.click('#cichip');
  await p3.waitForSelector('#ciform');
  ok('…its section: only "Watch CI for…"', (await p3.textContent('#cisec')).includes('Nothing watched yet') && (await p3.$$('.ciwatch')).length === 0);
  ok('no errors either', e2.length === 0 && e3.length === 0, [...e2, ...e3].join(' | '));
  await p3.close();
}

// === a phone: the dock over the chat ==============================================================
{
  const { page: p4, errors: e4 } = await open(ciSeedFor(), { width: 390 });
  await p4.waitForSelector('#cichip');
  const width = () => p4.evaluate(() => document.documentElement.scrollWidth);
  const before = await width(); // the conversation's own (its long rows)
  await p4.click('#cichip');
  await p4.waitForSelector('#cisec');
  ok('a phone: the dock is an overlay', await p4.evaluate(() => getComputedStyle(document.getElementById('hboard')).position === 'fixed'));
  ok('a phone: the dock fits, adding no horizontal scroll', (await width()) <= before
    && await p4.evaluate(() => document.getElementById('hboard').getBoundingClientRect().right <= window.innerWidth + 1), `${before} → ${await width()}`);
  ok('a phone: no errors', e4.length === 0, e4.join(' | '));
  await p4.close();
}

// === the board's CI chip (ext.card) opens the task on CI ===========================================
{
  const { page: p5 } = await open(ciSeedFor(), { hash: '' });
  await p5.waitForFunction(() => !!window.__ci, null, { timeout: 5000 });
  const chip = await p5.evaluate(async () => {
    const { ext } = await import('/web-ext.js');
    const { render } = await import('/vendor/lit-all.min.js');
    const host = document.createElement('div');
    host.id = 'cardhost';
    document.body.append(host);
    render(ext.card({ n: 1, run: 25, ci: { state: 'pending', jobs: { total: 5, done: 3 }, startedAt: Date.now() - 134000 } }), host);
    return host.textContent.trim();
  });
  ok('a task card\'s CI chip', /^CI ● 3\/5 jobs · 2:1\d$/.test(chip), chip);
  await p5.click('#cardhost .cicard');
  ok('…opens the task with the dock on CI', await p5.waitForSelector('#hboard .hbsec[data-sec="ci"]', { timeout: 5000 }).then(() => true, () => false)
    && await p5.evaluate(() => location.hash === '#c=25'));
  await p5.close();
}

await browser.close();
done('CI');
