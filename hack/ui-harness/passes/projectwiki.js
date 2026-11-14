// hack/ui-harness/passes/projectwiki.js — a project's Wiki tab (D222) in the
// real agent tile (apps/agent), in both themes. The harness has no scm
// provider, so a project can't be made here: the project's routes and its
// wiki are answered by this pass (ctx.route) — the wiki with the backend's
// compare-and-swap (a PUT at a stale baseVersion is a 409 with the
// conflict's words). The backend's own behaviour is the Go tests'
// (TestWikiCAS, TestWikiRevisionsArchiveFTS). It pins:
//   - the tab lists the pages, the index first, and opens it; a [[slug]]
//     link opens the page it names; the footer says the wiki's measures;
//   - New page creates one (baseVersion 0), and it is listed;
//   - two writers: an agent writes the page while a person edits it — the
//     save is refused with the conflict's words and Reload; Reload takes
//     the page as it is now and keeps what was typed; the next save lands;
//   - History lists every version and shows a version's change as a diff.
const { URL, login, closeCtx, shot, checker, log, noGocryptfs } = require('../lib');

const CONFLICT = 'Another agent changed the contents of this article since you last read it — read the article again before editing.';
const PID = 4242;

// mockProject answers the project's routes and keeps a small wiki (pages +
// every version), with the backend's compare-and-swap.
function mockProject() {
  const now = Date.now();
  const pv = { id: PID, name: 'Web shop', slug: 'web-shop', kind: 'personal', owner: 'admin', level: 'owner', state: 'active', version: 1,
    visibility: 'private', repos: [{ slug: 'shop', repo: 'acme/shop', defaultBranch: 'main', state: 'ready' }], policy: { dream: true },
    counts: { queued: 0, working: 0, 'needs-you': 0, pr: 0, done: 0 }, slots: { used: 0, max: 3 }, createdMs: now, updatedMs: now };
  const pages = new Map();
  const revs = new Map();
  const write = (slug, title, body, by, note) => {
    const cur = pages.get(slug);
    const p = { project: PID, slug, title: title || (cur && cur.title) || slug, body, version: cur ? cur.version + 1 : 1, updatedBy: by,
      updatedMs: Date.now(), createdMs: cur ? cur.createdMs : Date.now(), reads: cur ? cur.reads : 3, sources: slug === 'build' ? ['Makefile', '.github/workflows/ci.yml'] : [] };
    p.size = body.length;
    pages.set(slug, p);
    const list = revs.get(slug) || [];
    list.unshift({ version: p.version, title: p.title, body, by, note, atMs: p.updatedMs, size: body.length });
    revs.set(slug, list);
    return p;
  };
  write('index', 'Index', '- [[build]]: how to build and test the shop\n- [[layout]]: what lives where', 'coordinator (admin)', 'the map');
  write('build', 'Build and test', 'Build with `make build`.', "research #12 (admin's coordinator)", 'read the Makefile');
  write('build', 'Build and test', 'Build with `make build`; test with `make test`.\n\n```sh\nmake build test\n```\n\nCI runs `make check` (see [[layout]]).', 'task #3 (dream)', 'CI too');
  write('layout', 'Layout', '- `cmd/shop`: the server\n- `web/`: the storefront', "research #12 (admin's coordinator)", 'ls');
  const stats = () => {
    const live = [...pages.values()].filter((p) => !p.archived);
    return { pages: live.length, bytes: live.reduce((n, p) => n + p.size, 0), reads: live.reduce((n, p) => n + p.reads, 0), readers: 4,
      revisions: [...revs.values()].reduce((n, l) => n + l.length, 0), dreams: 2, dreamTokens: 18400 };
  };
  const strip = (p) => ({ ...p, body: undefined });
  return { pv, pages, revs, write, stats, strip };
}

async function route(ctx, m) {
  const json = (r, status, body) => r.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await ctx.route((u) => u.pathname.startsWith('/api/apps/agent/projects'), async (r) => {
    const u = new globalThis.URL(r.request().url());
    const method = r.request().method();
    const path = u.pathname.replace('/api/apps/agent', '');
    const q = u.searchParams;
    if (path === '/projects') return json(r, 200, { items: [m.pv], next: '' });
    if (path === `/projects/${PID}`) return json(r, 200, { project: m.pv });
    if (path === `/projects/${PID}/tasks`) return json(r, 200, { items: [], next: '' });
    if (path === `/projects/${PID}/events`) return json(r, 200, { items: [], next: '' });
    if (path === `/projects/${PID}/coordinator`) return json(r, 200, { run: null });
    if (path === `/projects/${PID}/wiki`) {
      const live = [...m.pages.values()].filter((p) => !p.archived).sort((a, b) => (a.slug === 'index' ? -1 : b.slug === 'index' ? 1 : a.slug.localeCompare(b.slug)));
      const out = { pages: live.map(m.strip), stats: m.stats(), dream: true };
      const s = (q.get('q') || '').toLowerCase();
      if (s) out.hits = live.filter((p) => p.body.toLowerCase().includes(s)).map((p) => ({ slug: p.slug, title: p.title, version: p.version, snippet: p.body.slice(0, 60) }));
      return json(r, 200, out);
    }
    const w = /^\/projects\/\d+\/wiki\/([a-z0-9-]+)(\/history)?$/.exec(path);
    if (w) {
      const [, slug, hist] = w;
      const p = m.pages.get(slug);
      const v = +(q.get('v') || 0);
      if (method === 'PUT') {
        const b = JSON.parse(r.request().postData() || '{}');
        if ((p ? p.version : 0) !== b.baseVersion) return json(r, 409, { error: CONFLICT, refusal: 'conflict', current: p ? p.version : 0 });
        return json(r, 200, { page: m.write(slug, b.title, b.body, 'admin', b.note) });
      }
      if (!p) return json(r, 404, { error: `there is no page "${slug}" in this project's wiki` });
      if (hist) {
        const out = { slug, version: p.version, revisions: (m.revs.get(slug) || []).map((x) => ({ ...x, body: undefined })) };
        if (v) out.revision = (m.revs.get(slug) || []).find((x) => x.version === v);
        return json(r, 200, out);
      }
      p.reads++;
      return json(r, 200, { page: p });
    }
    return json(r, 404, { error: 'not in this mock' });
  });
}

const until = (page, fn, arg, timeout = 15000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 50 });

async function oneTheme(browser, theme, check) {
  const m = mockProject();
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1300, height: 950 }, colorScheme: theme });
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  await route(ctx, m);
  await page.goto(`${URL}/c/apps/agent/#proj=${PID}`);
  await page.waitForSelector('[data-tab="wiki"]', { timeout: 30000 });
  await page.click('[data-tab="wiki"]');
  await page.waitForSelector('#pwiki-page[data-slug="index"]', { timeout: 15000 });
  const rows = await page.$$eval('#pwiki-list .pwrow', (els) => els.map((e) => e.dataset.slug));
  check(rows.join(',') === 'index,build,layout', `${theme}: the pages, the index first (${rows})`);
  check(/3 pages · .* reads .* · 2 dream rounds, 18k tokens/.test(await page.textContent('#pwiki-stats')), `${theme}: the footer says the wiki's measures`);
  await page.click('#pwiki-page a.wlink[data-slug="build"]');
  await page.waitForSelector('#pwiki-page[data-slug="build"]');
  check((await page.textContent('#pwiki-page .pwmeta')).includes('version 2 · by task #3 (dream)'), `${theme}: a [[slug]] link opens its page, with who changed it`);
  check(!!(await page.$('#pwiki-page .pwbody pre code')), `${theme}: the page is markdown (its code fence a code block)`);
  await shot(page, `projectwiki-page-${theme}`);

  // a new page
  await page.click('#pwiki-new');
  await page.fill('#pwiki-title', 'Deploy');
  await page.fill('#pwiki-body', 'Deploys run `make deploy` from main; see [[build]].');
  await page.fill('#pwiki-note', 'how we ship');
  await page.click('#pwiki-save');
  await page.waitForSelector('#pwiki-page[data-slug="deploy"]');
  check(!!(await page.$('#pwiki-list .pwrow[data-slug="deploy"]')), `${theme}: New page creates it (slug from its title) and lists it`);

  // two writers: an agent writes "build" while the person edits it
  await page.click('#pwiki-list .pwrow[data-slug="build"]');
  await page.waitForSelector('#pwiki-page[data-slug="build"][data-version="2"]');
  await page.click('#pwiki-edit');
  await page.waitForSelector('#pwiki-editor');
  await page.fill('#pwiki-body', 'Build with `make build`; test with `make test` (needs Docker).');
  m.write('build', 'Build and test', 'Build with `make build`; test with `make test -j4`.\n\nCI runs `make check`.', 'task #5 (dream)', 'parallel tests');
  await page.click('#pwiki-save');
  await page.waitForSelector('#pwiki-conflict');
  check((await page.textContent('#pwiki-conflict')).includes(CONFLICT), `${theme}: a save over another's write says so, in the conflict's words`);
  await shot(page, `projectwiki-conflict-${theme}`);
  await page.click('#pwiki-reload');
  await until(page, () => document.querySelector('#pwiki-body')?.value.includes('-j4'));
  check(!!(await page.$('.pwmine')) && (await page.textContent('.pwmine')).includes('needs Docker'), `${theme}: Reload takes the page as it is now and keeps what was typed`);
  await page.fill('#pwiki-body', 'Build with `make build`; test with `make test -j4` (needs Docker).\n\nCI runs `make check`.');
  await page.click('#pwiki-save');
  await page.waitForSelector('#pwiki-page[data-slug="build"][data-version="4"]');
  check(m.pages.get('build').body.includes('needs Docker') && m.pages.get('build').body.includes('-j4'), `${theme}: the merged save lands at version 4`);

  // history: every version, a version's change
  await page.click('#pwiki-history');
  await page.waitForSelector('#pwiki-diff');
  const vs = await page.$$eval('#pwiki-hist .pwrev', (els) => els.map((e) => e.dataset.v));
  check(vs.join(',') === '4,3,2,1', `${theme}: History lists every version, newest first (${vs})`);
  const ops = await page.$$eval('#pwiki-diff .pwl', (els) => els.map((e) => e.dataset.op));
  check(ops.includes('add') && ops.includes('del'), `${theme}: the newest version's change as a diff (${ops.filter((o) => o !== 'same').length} lines changed)`);
  await page.click('#pwiki-hist .pwrev[data-v="3"]');
  await until(page, () => document.querySelector('.pwdiffh')?.textContent.includes('v3 against v2'));
  check(true, `${theme}: a version picked shows its change against the one before`);
  await shot(page, `projectwiki-history-${theme}`);
  check(!errors.length, `${theme}: no page errors (${errors.slice(0, 2).join(' | ')})`);
  await closeCtx(ctx, page);
}

async function projectWiki(browser) {
  const { check, skip, done } = checker('project-wiki');
  if (noGocryptfs()) { skip(`apps/agent is held: ${noGocryptfs()}`); return done(); }
  for (const theme of ['dark', 'light']) {
    await oneTheme(browser, theme, check);
    log(`projectWiki: ${theme} done`);
  }
  done();
}

module.exports = { projectWiki };
