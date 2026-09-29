// hack/ui-harness/passes/agenttask.js — the agent template keeps its task
// through compaction (D133), end to end: a real instance (apps/agent) talking
// through llm-gw to hack/fakeopenai. "huge context" makes the fake model
// report a 5M-token prompt, so the agent's next step compacts; the fake's
// GET /debug/requests records each call's system prompt and task reminder.
// It pins:
//   - the run header pins the current request — the latest one it was
//     given — and unfolds to the ledger;
//   - after a compaction, the next call still carries the request verbatim
//     under "# Your task", and is told once that the context was compacted;
//   - the compaction budget came from the model's context window, as llm-gw
//     passes the provider's model list through (60% of 128000);
//   - the summarizer was asked, and the chat says what was compacted.
const { login, log, shot, checker, noGocryptfs } = require('../lib');

const URL = process.env.URL || 'http://127.0.0.1:8697';
const FAKE = `http://${process.env.FAKEOPENAI_ADDR || '127.0.0.1:18977'}`;

const requests = async (since) => (await (await fetch(`${FAKE}/debug/requests`)).json()).filter((r) => r.start >= since);
const until = (page, fn, arg, timeout = 20000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 50 });
const answered = (page, t, timeout = 30000) => until(page, ([t]) => [...document.querySelectorAll('#timeline .msg.assistant:not(.live)')]
  .some((e) => e.textContent.includes(t)), [t], timeout);
async function say(page, t) {
  await page.fill('#msg', t);
  await page.press('#msg', 'Enter');
}
// api: the agent's own routes, as the tile frame calls them.
const api = (page, path) => page.evaluate(async (path) => {
  const base = `/api/${location.pathname.split('/').slice(2, -1).join('/')}`;
  const r = await xbin.fetch(base + path);
  if (!r.ok) throw new Error(`GET ${path}: ${r.status}`);
  return r.json();
}, path);

async function agentTask(browser) {
  const { check, skip, done } = checker('agent-task');
  if (noGocryptfs()) { skip(`apps/agent is held: ${noGocryptfs()}`); return done(); }
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1300, height: 950 } });
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const since = Date.now();
  await page.goto(`${URL}/c/apps/agent/`);
  await page.waitForSelector('#msg', { timeout: 30000 });
  // the internal class, the fake chat model (as agentTemplate sets them)
  await page.waitForSelector('#tset', { timeout: 30000 });
  if (!(await page.$('.clsmenu'))) await page.click('#tset');
  await page.click('.clsmenu .mi[data-class="internal"]');
  await until(page, () => !document.querySelector('.clsmenu'));
  await page.evaluate(async () => {
    const base = `/api/${location.pathname.split('/').slice(2, -1).join('/')}`;
    const c = await (await xbin.fetch(`${base}/config`)).json();
    c.model = 'fake/fake-chat';
    await xbin.fetch(`${base}/config`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(c) });
  });

  // 1. a conversation: its request is pinned in the header
  const goal = `TASK-PIN ${since}: reconcile the ledger with the bank export and flag every mismatch`;
  await page.click('#newopts');
  await page.fill('#n-title', 'pinned task');
  await page.fill('#n-goal', goal);
  await page.click('#n-create');
  await until(page, () => document.querySelector('#top .title')?.textContent === 'pinned task');
  await answered(page, 'ok: task-pin');
  await until(page, () => document.querySelector('#top .taskpin .taskline'), null, 10000);
  check((await page.textContent('#top .taskpin .taskline')) === goal, 'the header pins the request, verbatim, on one line');

  // 2. the model reports a huge prompt; the next step compacts
  await say(page, 'pretend a huge context');
  await answered(page, 'ok: pretend a huge context');
  const at = Date.now();
  await say(page, 'and now carry on');
  await answered(page, 'ok: and now carry on');
  const rs = await requests(at);
  const compacts = rs.filter((r) => r.purpose === 'compact');
  const next = rs.find((r) => r.purpose === 'turn' && r.last === 'and now carry on');
  log(`agentTask: ${rs.length} request(s) since the huge prompt: ${rs.map((r) => r.purpose).join(', ')}`);
  check(compacts.length === 1, `the huge prompt made the agent compact (${compacts.length} summarizer call(s))`);
  check(!!next && next.system.includes('# Your task (verbatim') && next.system.includes(goal),
    'the call after the compaction still carries the request verbatim under "# Your task"');
  check(!!next && next.reminder.includes('Earlier turns were compacted'), `…and is told once that the context was compacted (${next && next.reminder.slice(0, 120)})`);
  const later = rs.filter((r) => r.purpose === 'turn' && r.start > (next ? next.start : 0));
  check(later.every((r) => !r.reminder.includes('Earlier turns were compacted')), 'the note is one-time');

  // 3. where the budget came from, and what the chat says
  const id = await page.evaluate(() => Number((location.hash.match(/c=(\d+)/) || [])[1]));
  const view = await api(page, `/runs/${id}/view`);
  const step = (view.steps || []).filter((s) => s.kind === 'compaction').map((s) => JSON.parse(s.detail || '{}')).pop() || {};
  check(step.budget === 76800 && /128000-token window/.test(step.budgetFrom || ''),
    `the budget is 60% of the model's context window, as the provider lists it (${step.budget}, ${step.budgetFrom})`);
  check(step.messages > 0 && (view.run.summary || '').startsWith('SUMMARY:'), `the oldest turns were summarised (${step.messages} message(s): ${view.run.summary})`);
  await until(page, () => [...document.querySelectorAll('#timeline *')].some((e) => /compacted \d+ message\(s\) into the summary/.test(e.textContent)), null, 10000)
    .then(() => check(true, 'the chat says what was compacted'), () => check(false, 'the chat says what was compacted'));

  // 4. the header pins the current request (the latest), and unfolds to the ledger
  await until(page, () => document.querySelector('#top .taskpin .taskline')?.textContent === 'and now carry on', null, 10000)
    .then(() => check(true, 'the header pins the current request — the latest one, not the first'),
      async () => check(false, `the header pins the current request — the latest one, not the first (${await page.textContent('#top .taskpin .taskline')})`));
  check(/\+2/.test(await page.textContent('#top .taskpin .tasktoggle')), 'its toggle says how many others there are (+2)');
  await shot(page, 'agent-task-folded', { fullPage: false });
  await page.click('#top .taskpin .tasktoggle');
  await until(page, () => document.querySelectorAll('#top .taskpin .taskreq').length >= 3, null, 10000);
  const asks = await page.$$eval('#top .taskpin .taskreq', (els) => els.map((e) => e.textContent));
  check(asks[0].includes(goal) && asks[0].includes('compacted') && asks.some((a) => a.includes('and now carry on')),
    `unfolded: every request, the first one marked compacted (${asks.map((a) => a.slice(0, 60)).join(' | ')})`);
  const ledger = await api(page, `/runs/${id}/asks`);
  check(ledger.asks.length === asks.length && ledger.asks[0].text === goal, 'GET /runs/{id}/asks serves the same ledger');
  await shot(page, 'agent-task-pinned', { fullPage: false });
  await page.click('#top .taskpin .tasktoggle');

  check(errors.length === 0, `no page errors (${errors.join(' | ')})`);
  await ctx.close();
  done();
}

module.exports = { agentTask };
