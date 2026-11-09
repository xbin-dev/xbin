// hack/ui-harness/passes/projectwizard.js — the new-project wizard (D223) in
// the real agent tile (apps/agent), light and dark: a step header, Back and
// Next (Next disabled, saying why), the repo picker as a combobox (open on
// focus with your repos, archived ones disabled, a choice added at once,
// its setup script folded), Name from the first repo, Sandbox, How it works
// with the wiki's dream rounds, Get started (the starter work, both on),
// Review with an Edit per step, then Create's body. The harness has no scm
// provider: the project routes, the provider's repos and the sandbox
// managers are answered here (as projectsneeds.js and projectwiki.js do);
// the backend's starter is the Go tests' (TestProjectStarter*), the native
// screens hack/agent-template-native-projects.test.mjs's.
// Shots: projectwizard-{1-code,combobox,1-code-chosen,2-name,3-sandbox,4-how,5-start,6-review}-{light,dark}.
const { URL, login, checker, shot, settle, noGocryptfs } = require('../lib');

const API = '/api/apps/agent';
const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

const PROVIDER = { scm: 'apps/scm-github', title: 'GitHub', kind: 'github', hosts: ['github.com'], caps: ['checks', 'issues'], identities: ['bot'],
  you: { identities: ['bot'], default: 'bot', person: null }, app: { configured: true }, events: { healthy: true }, notes: [] };
const REPOS = [
  { host: 'github.com', owner: 'acme', name: 'shop', private: true, permission: 'write', defaultBranch: 'main' },
  { host: 'github.com', owner: 'acme', name: 'shop-api', private: true, permission: 'admin', defaultBranch: 'main' },
  { host: 'github.com', owner: 'acme', name: 'docs', private: false, permission: 'write', defaultBranch: 'main' },
  { host: 'github.com', owner: 'acme', name: 'payments-gateway-legacy', private: true, permission: 'read', archived: true, defaultBranch: 'master' },
  { host: 'github.com', owner: 'acme', name: 'infra', private: true, permission: 'maintain', defaultBranch: 'main' },
];
const MANAGERS = [{ provider: 'apps/coding-sandbox', title: 'Coding sandboxes', ok: true, caps: ['exec', 'files'], egress: ['none', 'internet', 'open'],
  images: [{ id: 'base', title: 'Debian', default: true }, { id: 'go', title: 'Go' }], sizes: [{ id: 'small', default: true }, { id: 'large' }], limits: {} }];

async function stub(page, posted) {
  const now = Date.now();
  const made = (b) => ({ id: 50, uid: 'abc123', name: b.name, slug: 'shop', kind: 'personal', owner: 'admin', level: 'owner', state: 'active', version: 1,
    visibility: 'private', scm: b.scm, repos: [], policy: b.policy, counts: { queued: 0, working: 0, 'needs-you': 0, pr: 0, done: 0 }, slots: { used: 0, max: 3 },
    createdMs: now, updatedMs: now });
  await page.route((u) => u.pathname.startsWith(`${API}/projects`) || u.pathname === `${API}/sandboxes`, (route) => {
    const req = route.request();
    const u = new globalThis.URL(req.url());
    const p = u.pathname.slice(API.length);
    if (p === '/sandboxes') return json(route, { managers: MANAGERS, sandboxes: [] });
    if (p === '/projects' && req.method() === 'POST') {
      const b = JSON.parse(req.postData() || '{}');
      posted.push(b);
      return json(route, { project: made(b), jobs: [], starter: b.starter }, 201);
    }
    if (p === '/projects') return json(route, { items: [], next: '' });
    if (p === '/projects/scm') return json(route, { providers: [PROVIDER] });
    if (p === '/projects/scm/repos') {
      const q = (u.searchParams.get('q') || '').toLowerCase();
      return json(route, { items: REPOS.filter((r) => `${r.owner}/${r.name}`.includes(q)), next: q ? '' : 'page2' });
    }
    if (p === '/projects/0/wiki') return json(route, { error: 'no such project' }, 404); // the wiki's probe: the route is there
    if (p === '/projects/50' && posted.length) return json(route, { project: made(posted[0]) });
    if (/^\/projects\/50\/(tasks|events)$/.test(p)) return json(route, { items: [], next: '' });
    if (p === '/projects/50/coordinator') return json(route, { run: null });
    if (p === '/projects/50/needs') return json(route, { items: [] });
    return json(route, { error: 'not in this pass' }, 404);
  });
}

async function oneTheme(browser, scheme, check) {
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1300, height: 900 }, colorScheme: scheme });
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  await ctx.request.delete(`${URL}/api/xbin/prefs/theme`); // the person's theme follows the system: this context's scheme
  const posted = [];
  await stub(page, posted);
  const until = (fn, arg, timeout = 15000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 50 }).then(() => true, () => false);
  const atStep = (key) => until((k) => document.querySelector('#proj-form')?.dataset.step === k, key);
  const snap = async (name) => { await settle(page); await shot(page, `projectwizard-${name}-${scheme}`, { fullPage: false }); };
  await page.goto(`${URL}/c/apps/agent/#proj`);
  await page.waitForSelector('#proj-new', { timeout: 30000 });
  await page.click('#proj-new');
  check(await atStep('code') && await until(() => !!document.querySelector('#pn-q')), `${scheme}: New project opens the wizard at Code`);
  const steps = await page.$$eval('#pn-steps .pnstep', (els) => els.map((e) => e.textContent.replace(/\s+/g, ' ').trim()));
  check(steps.join(' · ') === '1Code · 2Name · 3Sandbox · 4How it works · 5Get started · 6Review', `${scheme}: the step header (${steps.join(' · ')})`);
  check(await page.isDisabled('#pn-next') && (await page.textContent('#pn-why')) === 'Choose a repo.', `${scheme}: Next disabled, saying why`);
  await page.mouse.move(0, 0);
  await snap('1-code');

  // the combobox: open on focus, your repos, the archived one disabled
  await page.focus('#pn-q');
  check(await until(() => document.querySelectorAll('#pn-list [role="option"][data-repo]').length === 5), `${scheme}: the picker opens on focus with your repos`);
  check((await page.getAttribute('#pn-q', 'aria-expanded')) === 'true' && (await page.getAttribute('#pn-list', 'role')) === 'listbox', `${scheme}: …an ARIA combobox and listbox`);
  check((await page.getAttribute('#pn-list [data-repo="acme/payments-gateway-legacy"]', 'aria-disabled')) === 'true', `${scheme}: …the archived one disabled`);
  check(!!(await page.$('#pn-list [data-more]')), `${scheme}: …"more" at the end while there is a next page`);
  await page.click('#pn-list [data-repo="acme/shop"]');
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('ArrowDown');
  check(await until(() => !!document.querySelector('.pnrepo[data-repo="acme/shop"]')), `${scheme}: choosing adds it at once`);
  await snap('combobox');
  await page.keyboard.press('Escape');
  await page.click('.pnrepo[data-repo="acme/shop"] summary');
  await page.fill('.pnrepo[data-repo="acme/shop"] textarea', 'npm ci');
  await page.mouse.move(0, 0);
  await snap('1-code-chosen');

  await page.click('#pn-next');
  check(await atStep('name') && (await page.inputValue('#pn-name')) === 'shop', `${scheme}: Name from the first repo`);
  await snap('2-name');
  await page.click('#pn-next');
  check(await atStep('sandbox') && await page.isChecked('input[name="pn-sbx"][value="new"]'), `${scheme}: Sandbox: a new one, the manager's defaults`);
  await snap('3-sandbox');
  await page.click('#pn-next');
  check(await atStep('how') && await page.isChecked('#pn-dream'), `${scheme}: How it works, the wiki's dream rounds on`);
  await snap('4-how');
  await page.click('#pn-next');
  check(await atStep('start') && await page.isChecked('#pn-st-build') && await page.isChecked('#pn-st-wiki'), `${scheme}: Get started, both on`);
  await snap('5-start');
  await page.click('#pn-next');
  check(await atStep('review') && (await page.$$('#pn-review .pnedit')).length === 5, `${scheme}: Review, an Edit per step`);
  await snap('6-review');
  await page.click('#pn-create');
  for (const t0 = Date.now(); !posted.length && Date.now() - t0 < 10000;) await new Promise((r) => setTimeout(r, 50));
  check(posted.length === 1, `${scheme}: Create posts it`);
  const b = posted[0] || {};
  check(b.name === 'shop' && JSON.stringify(b.repos) === '[{"repo":"acme/shop","setup":"npm ci"}]' && JSON.stringify(b.starter) === '{"build":true,"wiki":true}'
    && b.sandbox && b.sandbox.new && b.sandbox.new.egress === 'internet', `${scheme}: …its body (${JSON.stringify(b)})`);
  check(errors.length === 0, `${scheme}: no page errors (${errors.slice(0, 2).join(' | ')})`);
  await ctx.close();
}

async function projectWizard(browser) {
  const { check, skip, done } = checker('project-wizard');
  if (noGocryptfs()) { skip(`apps/agent is held: ${noGocryptfs()}`); return done(); }
  for (const scheme of ['light', 'dark']) await oneTheme(browser, scheme, check);
  done();
}

module.exports = { projectWizard };
