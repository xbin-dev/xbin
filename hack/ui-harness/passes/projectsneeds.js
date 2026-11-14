// hack/ui-harness/passes/projectsneeds.js — #56: what waits on you in a
// project, on the real agent template (apps/agent) in the shell's own theme,
// light and dark: "Waiting for you" atop the project's page — a task's
// question and the coordinator's own ask_user (red, each with its text and
// age), a failed task (yellow) — Open selecting the conversation it waits
// in, and the conversation list's count split (red: what really waits; then
// the ready-for-review one). The project's routes are stubbed with
// page.route from the browser tests' seed (projects-stub.mjs): driving a
// real question needs an scm provider and a coding agent in a sandbox, and
// the Go tests (TestProjectNeedsHasCoordinatorAsk, TestNeedsItemsCarryText,
// TestCoordSeesQuestionAndAge) pin the backend's side. The native screens are
// hack/agent-template-native-projects.test.mjs's.
// Shots: projects-needs-{light,dark}, projects-needs-open-{light,dark}.
const path = require('path');
const { URL, login, checker, shot, settle, noGocryptfs } = require('../lib');

const STUBS = path.join(__dirname, '../../../builtin-templates/agent/test');
const API = '/api/apps/agent';
const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

// world(): the stubbed project — its view (the split counts), tasks, needs, overview and the two conversations that ask
async function world() {
  const { projSeed } = await import(path.join(STUBS, 'projects-stub.mjs'));
  const { overviewSeed } = await import(path.join(STUBS, 'projects-more-stub.mjs'));
  const now = Date.now();
  const seed = projSeed();
  const web = { ...seed.projects[0], level: 'owner', slots: { used: 1, max: 3 },
    counts: { queued: 1, working: 1, 'needs-you': 3, pr: 1, done: 1, ready: 1, asks: 1, failed: 1, coord: 1 } };
  delete web.home;
  const tasks = seed.tasks[7];
  const ov = overviewSeed(7, 'day');
  ov.coordinator = { ...ov.coordinator, state: 'waiting', run: { id: 300, status: 'waiting_input' } };
  const needs = [
    { reason: 'failed', subRun: 0, run: { id: 108, title: 'Workspace failed', status: 'idle' }, project: { id: 7, name: 'Web', n: 8 },
      text: 'git clone: repository not found', since: now - 3 * 3600e3 },
    { reason: 'question', subRun: 103, run: { id: 103, title: 'Bump deps', status: 'waiting_input' }, project: { id: 7, name: 'Web', n: 3 },
      text: 'Deploy the fix to staging now?', since: now - 2 * 86400e3 },
    { reason: 'question', subRun: 0, run: { id: 300, title: 'Coordinator · Web', status: 'waiting_input' }, project: { id: 7, name: 'Web', n: 0 },
      text: 'Which repo should I start with: acme/web or acme/api?', since: now - 5 * 60e3 },
    { reason: 'review', subRun: 0, run: { id: 101, title: 'Fix the login loop', status: 'idle' }, project: { id: 7, name: 'Web', n: 1 } },
  ];
  const s = Math.floor(now / 1000);
  const view = (run, said, extra = {}) => ({ cursor: 'g.1', run: { pendingState: {}, parentId: 0, rootId: run.id, origin: 'project', originId: 7, ...run },
    messages: said.map(([role, content], i) => ({ id: run.id * 10 + i, runId: run.id, seq: i + 1, role, content, created: s - 600 + i * 30 })),
    steps: [], links: [], queued: [], drafts: [], chain: [], files: [], memory: {}, config: {}, messageFiles: {}, ...extra });
  const views = {
    103: view({ id: 103, title: 'Bump deps', status: 'waiting_input', result: 'Deploy the fix to staging now?' },
      [['user', 'Bump the dependencies and check the build.'], ['assistant', 'The build passes with the new versions. Deploy the fix to staging now?']],
      { project: { id: 7, name: 'Web', n: 3, role: 'task' }, projectTask: tasks.find((t) => t.n === 3) }),
    300: view({ id: 300, title: 'Coordinator · Web', status: 'waiting_input', result: 'Which repo should I start with: acme/web or acme/api?' },
      [['user', 'Plan the work for the release.'], ['assistant', 'Which repo should I start with: acme/web or acme/api?']],
      { project: { id: 7, name: 'Web', n: 0, role: 'coordinator' } }),
  };
  return { web, tasks, ov, needs, views };
}

async function stub(page, w) {
  await page.route((u) => u.pathname.startsWith(`${API}/projects`) || /^\/api\/apps\/agent\/runs\/(103|300|108)\//.test(u.pathname), (route) => {
    const req = route.request();
    const p = new globalThis.URL(req.url()).pathname.slice(API.length);
    if (req.method() !== 'GET') return json(route, { error: 'not in this pass' }, 405);
    if (p === '/projects') return json(route, { items: [w.web], next: '' });
    if (p === '/projects/scm') return json(route, { providers: [] });
    if (p === '/projects/7') return json(route, { project: w.web });
    if (p === '/projects/7/tasks') return json(route, { items: w.tasks, next: '' });
    if (p === '/projects/7/needs') return json(route, { items: w.needs });
    if (p === '/projects/7/overview') return json(route, w.ov);
    if (p === '/projects/7/coordinator') return json(route, { run: { id: 300, title: 'Coordinator · Web', status: 'waiting_input', rootId: 300 } });
    if (p === '/projects/7/events') return json(route, { items: [], next: '' });
    let m = p.match(/^\/runs\/(\d+)\/view$/);
    if (m && w.views[m[1]]) return json(route, w.views[m[1]]);
    m = p.match(/^\/runs\/(\d+)\/task$/);
    if (m && w.views[m[1]] && w.views[m[1]].projectTask) return json(route, w.views[m[1]].projectTask);
    return json(route, { error: 'no such thing in this pass' }, 404);
  });
}

async function projectsNeeds(browser) {
  const { check, skip, done } = checker('projects-needs');
  if (noGocryptfs()) { skip(`apps/agent is held: ${noGocryptfs()}`); return done(); }
  const w = await world();
  for (const scheme of ['light', 'dark']) {
    const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1360, height: 980 }, colorScheme: scheme });
    const errors = [];
    page.on('pageerror', (e) => errors.push(e.message));
    await ctx.request.delete(`${URL}/api/xbin/prefs/theme`); // the person's theme follows the system: this context's scheme
    await stub(page, w);
    const until = (fn, arg, timeout = 15000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 50 }).then(() => true, () => false);
    await page.goto(`${URL}/c/apps/agent/#proj=7`);

    // the conversation list: the red count, then the ready one
    check(await until(() => {
      const b = [...document.querySelectorAll('#projsec .projrow[data-pid="7"] .badge')].map((x) => `${x.classList.contains('pneedb') ? 'red' : 'ready'}:${x.textContent}`);
      return b.join(',') === 'red:3,ready:1';
    }), `${scheme}: the list's badge splits — 3 waiting (red), 1 ready for review`);

    // the strip atop the page
    check(await until(() => document.querySelectorAll('#pneeds .pneed').length === 3), `${scheme}: Waiting for you shows the three needs`);
    const rows = await page.$$eval('#pneeds .pneed', (els) => els.map((e) => ({ tone: e.dataset.tone, run: e.dataset.run, text: e.textContent.replace(/\s+/g, ' ').trim() })));
    check(rows[0] && rows[0].tone === 'bad' && rows[0].run === '103' && rows[0].text.includes('Deploy the fix to staging now?') && /2 d ago/.test(rows[0].text),
      `${scheme}: the task's question first — red, its text and age (${rows[0] && rows[0].text})`);
    check(rows[1] && rows[1].tone === 'bad' && rows[1].run === '300' && rows[1].text.includes('Coordinator') && rows[1].text.includes('Which repo should I start with'),
      `${scheme}: the coordinator's own question (${rows[1] && rows[1].text})`);
    check(rows[2] && rows[2].tone === 'warn' && rows[2].text.includes('repository not found'), `${scheme}: the failed task yellow, last`);
    const lit = await page.$eval('#pneeds', (e) => {
      const cs = getComputedStyle(e), probe = document.createElement('span');
      probe.style.color = 'var(--bx-danger)'; e.append(probe);
      const danger = getComputedStyle(probe).color; probe.remove();
      return { border: cs.borderTopColor, danger, top: e.getBoundingClientRect().top };
    });
    check(lit.border === lit.danger && lit.top < 200, `${scheme}: lit red, at the top of the page (${JSON.stringify(lit)})`);
    await settle(page);
    await shot(page, `projects-needs-${scheme}`, { fullPage: false });

    // Open: the conversation it waits in
    await page.click('#pneeds .pneed[data-run="103"] .pnopen');
    check(await until(() => location.hash === '#c=103'), `${scheme}: Open selects the task's conversation`);
    check(await until(() => (document.querySelector('#timeline')?.textContent || '').includes('Deploy the fix to staging now?')), `${scheme}: …its question in view`);
    await settle(page);
    await shot(page, `projects-needs-open-${scheme}`, { fullPage: false });
    await page.goto(`${URL}/c/apps/agent/#proj=7`);
    await until(() => document.querySelectorAll('#pneeds .pneed').length === 3);
    await page.click('#pneeds .pneed[data-run="300"] .pnopen');
    check(await until(() => location.hash === '#c=300'), `${scheme}: Open on the coordinator's row selects the coordinator`);
    check(errors.length === 0, `${scheme}: no page errors (${errors.join(' | ')})`);
    await ctx.close();
  }
  done();
}

module.exports = { projectsNeeds };
