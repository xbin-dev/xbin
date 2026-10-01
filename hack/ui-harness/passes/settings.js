// hack/ui-harness/passes/settings.js — covers D180 (D175, PD-55) — the
// admin console's workspace → settings tab
// (workspace-template/tiles/admin/tabs/settings.js), on the admin tile's own
// page as admin:
//   1. two groups — Terminals (base auto-update, on) and Partitioned tiles
//      (both switches off, each "applies to partitioned tiles"); the old
//      #terminals and #policies bookmarks open it;
//   2. base auto-update saves off and back on;
//   3. "Ask each person…" asks before turning on, showing the edges between
//      partitioned tiles it would start asking about with the people who used
//      each in the last 30 days (GET /partitions/edges: the real answer, then
//      a stubbed one with totals — covers F10 06§12.3): cancel leaves it off
//      (the server agrees), Turn on saves it;
//   4. "Credential resets wait…" saves at once;
//   5. a second console follows changes made elsewhere — through
//      /workspace-settings and through v0.3.66's /workspace-policies alias
//      (the `workspace-settings` event);
//   6. a user reads every setting but may set none, on either route;
//   7. the approval warning (F10, 05§2 S1) beside a pending grant of a
//      partitioned tile on another's people's data, in the admin console's
//      binding → grants view and in the organisations tile's approvals (dev1,
//      a devs admin);
//   8. this console against an older xbind (its answers stubbed): v0.3.66's
//      — the switches read and saved through /workspace-policies — and
//      v0.3.65's — "this xbind has no partitioned tiles";
//   9. the console from before this tab (from git: the parent of the commit
//      that added tabs/settings.js) against this xbind: its terminals tab and
//      its policies tab read and save, and follow a change made elsewhere.
// The real edge: apps/pedge-z (partitioned, org:devs) holds an approved
// grant on apps/pedge-x (partitioned), apps/pedge-w asks for one (pending);
// all three are removed at the end, with the grant. Then every setting back
// to its default.
const path = require('path');
const { execFileSync } = require('child_process');
const { URL, fs, login, closeCtx, waitFor, shot, checker, sleep } = require('../lib');

const WS = process.env.WS || '';
const REPO = process.env.REPO || path.join(__dirname, '..', '..', '..');
const EDGE = { x: 'apps/pedge-x', z: 'apps/pedge-z', w: 'apps/pedge-w' };
const edgeManifest = (asks) => JSON.stringify({ title: 'partition edge', partition: ['user'],
  ...(asks ? { uses: [{ target: EDGE.x, role: 'reader' }] } : {}) });
const WARNING = `${EDGE.w}'s code — and everyone who can change it — will be able to read and write the ${EDGE.x} data of every person who can read ${EDGE.x}`;
const DEFAULTS = { baseAutoUpdate: true, partitionConsent: false, credentialResetConfirm: false };

async function untilOK(fn, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    if (await fn()) return;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(200);
  }
}

// seedEdge writes the three tiles (new, no data: recorded partitioned at
// once), gives them to org:devs and approves apps/pedge-z's grant.
async function seedEdge(api) {
  for (const [k, tile] of Object.entries(EDGE)) {
    const dir = path.join(WS, tile);
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(path.join(dir, 'index.html'), `<!doctype html><h1>${tile}</h1>\n`);
    fs.writeFileSync(path.join(dir, 'xbin.json'), edgeManifest(k !== 'x'));
  }
  await untilOK(async () => {
    for (const t of Object.values(EDGE)) {
      const r = await api('GET', `/components/${t}`);
      if (r.status() !== 200 || !((await r.json().catch(() => ({}))).component?.partition?.user)) return false;
    }
    return true;
  }, 'the edge tiles registered, partitioned');
  const owned = [];
  for (const t of Object.values(EDGE)) owned.push((await api('POST', '/owner', { tile: t, to: 'org:devs' })).status());
  const g = await api('POST', '/grants', { from: EDGE.z, target: EDGE.x, role: 'reader' });
  return `${owned.join(' ')} ${g.status()}`;
}

// dropEdge takes the grant back and removes the three tiles (absolute,
// checked paths inside the workspace).
async function dropEdge(api) {
  await api('DELETE', '/grants', { from: EDGE.z, target: EDGE.x, role: 'reader' }).catch(() => {});
  for (const t of Object.values(EDGE)) {
    const dir = path.join(WS, t);
    if (WS && path.isAbsolute(WS) && dir.startsWith(path.join(WS, 'apps', 'pedge-'))) fs.rmSync(dir, { recursive: true, force: true });
  }
}

// adminBefore: the admin scaffold's .js files from the parent of the commit
// that added tabs/settings.js (none yet: this checkout's HEAD).
function adminBefore() {
  const git = (...a) => execFileSync('git', ['-C', REPO, ...a], { encoding: 'utf8', maxBuffer: 64 << 20 });
  const added = git('log', '--diff-filter=A', '--format=%H', '--', 'workspace-template/tiles/admin/tabs/settings.js').trim().split('\n').pop();
  const base = added ? git('rev-parse', '--short', `${added}^`).trim() : git('rev-parse', '--short', 'HEAD').trim();
  const files = new Map();
  for (const n of git('ls-tree', '-r', '--name-only', base, 'workspace-template/tiles/admin/').trim().split('\n')) {
    if (n.endsWith('.js')) files.set(n.slice('workspace-template/tiles/admin/'.length), git('show', `${base}:${n}`));
  }
  return { base, files };
}

// the settings tab as the page shows it: each setting's state, each group's,
// the open confirmation, the scope labels, the edges preview
const view = () => {
  const r = document.querySelector('bx-admin')?.renderRoot?.querySelector('bx-admin-settings')?.shadowRoot;
  if (!r || !r.querySelector('[data-settings]')) return null;
  const st = {}, groups = {};
  for (const c of r.querySelectorAll('[data-setting-card]')) st[c.dataset.settingCard] = c.dataset.state;
  for (const g of r.querySelectorAll('[data-group]')) groups[g.dataset.group] = g.dataset.groupState;
  const edges = r.querySelector('[data-setting-edges]');
  return { st, groups, asking: r.querySelector('[data-setting-confirm]')?.dataset.settingConfirm || '',
    scopes: [...r.querySelectorAll('.scope')].filter((s) => s.textContent.includes('applies to partitioned tiles')).length,
    absent: [...r.querySelectorAll('[data-group-absent]')].map((a) => a.textContent.trim()),
    edges: edges?.dataset.settingEdges || '', edgesText: (edges?.textContent || '').replace(/\s+/g, ' ').trim() };
};

// the console from before this tab: its policies tab ({key: on|off}) and
// its terminals tab (base auto-update on|off)
const oldView = () => {
  const a = document.querySelector('bx-admin')?.renderRoot;
  const pol = a?.querySelector('bx-admin-policies')?.shadowRoot;
  const term = a?.querySelector('bx-admin-terminals')?.shadowRoot;
  const st = {};
  for (const c of pol?.querySelectorAll('[data-policy-card]') ?? []) st[c.dataset.policyCard] = c.dataset.state;
  const base = term?.querySelector('[data-base-auto-update]')?.dataset.baseAutoUpdate;
  if (base) st.baseAutoUpdate = base;
  return st;
};

async function adminSettings(browser) {
  const { check, done } = checker('admin-settings');
  const A = await login(browser, 'admin', 'admin');
  const xapi = (method, p, data) => A.ctx.request.fetch(`${URL}/api/xbin${p}`, { method, data });
  const get = async () => (await xapi('GET', '/workspace-settings')).json();
  const put = (body) => xapi('PUT', '/workspace-settings', body);
  const read = (page) => page.evaluate(`(${view})()`);
  const until = (page, fn, arg, label, src = view) => waitFor(page, new Function('t', 'arg', `const v = (${src})(); return !!v && (${fn})(v, arg);`), arg, { label });
  await put(DEFAULTS); // a clean start
  try {
    const seeded = await seedEdge(xapi);
    check(seeded === '200 200 200 200', `a real edge: three partitioned tiles of org:devs, apps/pedge-z's grant on apps/pedge-x approved (${seeded})`);
    // 1. the groups; the old bookmarks
    for (const hash of ['policies', 'terminals', 'settings']) {
      await A.page.goto('about:blank');
      await A.page.goto(`${URL}/c/tiles/admin/#${hash}`);
      await until(A.page, (v) => Object.keys(v.st).length === 3, null, `the settings tab (#${hash})`);
      const tab = await A.page.evaluate(() => document.querySelector('bx-admin')?.testApi?.().tab);
      check(tab === 'settings', `#${hash} opens the settings tab (${tab})`);
    }
    let v = await read(A.page);
    check(v.groups.terminals === 'ok' && v.groups.partitions === 'ok' && v.st.baseAutoUpdate === 'on' &&
      v.st.partitionConsent === 'off' && v.st.credentialResetConfirm === 'off' && v.scopes === 2,
      `two groups; base auto-update on, both switches off, each "applies to partitioned tiles" (${JSON.stringify(v)})`);
    await shot(A.page, 'admin-settings');

    // 2. base auto-update
    const base = A.page.locator('input[data-setting="baseAutoUpdate"]');
    await base.click();
    await until(A.page, (x) => x.st.baseAutoUpdate === 'off', null, 'base auto-update off');
    check((await get()).baseAutoUpdate === false, 'base auto-update saves off');
    await base.click();
    await until(A.page, (x) => x.st.baseAutoUpdate === 'on', null, 'base auto-update on');
    check((await get()).baseAutoUpdate === true, 'and back on');

    // 3. consent asks first, with the edges
    const consent = A.page.locator('input[data-setting="partitionConsent"]');
    await consent.click();
    await until(A.page, (x) => x.asking === 'partitionConsent', null, 'the turn-on confirmation');
    check(!(await consent.isChecked()) && (await get()).partitionConsent === false, 'turning consent on asks first; nothing saved yet');
    await until(A.page, (x) => x.edges !== '' && x.edges !== 'loading', null, 'the edges preview');
    v = await read(A.page);
    check(Number(v.edges) > 0 && v.edgesText.includes(`${EDGE.z} → ${EDGE.x}: 0 people`) && !v.edgesText.includes(EDGE.w),
      `the confirmation shows the real edge it would ask about, not the pending one (${v.edges}: ${v.edgesText})`);
    await A.page.locator('[data-setting-confirm] button', { hasText: 'cancel' }).click();
    await until(A.page, (x) => !x.asking && x.st.partitionConsent === 'off', null, 'cancel');
    check((await get()).partitionConsent === false, 'cancel leaves it off');
    const edgesRoute = '**/api/xbin/partitions/edges*';
    await A.page.route(edgesRoute, (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({
      days: 30, policy: { partitionConsent: false }, edges: [
        { from: 'apps/q', to: 'apps/pg', granted: true, people: 3, calls: 41, consented: 1 },
        { from: 'apps/q', to: 'apps/old', granted: false, people: 1, calls: 2, consented: 0 },
        { from: 'apps/q', to: 'apps/idle', granted: false, people: 0, calls: 0, consented: 0 }] }) }));
    await consent.click();
    await until(A.page, (x) => x.edges === '2', null, 'the stubbed edges preview');
    v = await read(A.page);
    check(v.edgesText.includes('apps/q → apps/pg: 3 people, 1 already allowed it') && v.edgesText.includes('apps/q → apps/old: 1 person (no grant now)') &&
      !v.edgesText.includes('apps/idle'), `the preview shows each edge's ledger totals (${v.edgesText})`);
    await shot(A.page, 'admin-settings-confirm');
    await A.page.locator('[data-setting-confirm] button', { hasText: 'cancel' }).click();
    await until(A.page, (x) => !x.asking, null, 'cancel again');
    await A.page.unroute(edgesRoute);
    await consent.click();
    await A.page.locator('[data-setting-confirm] button', { hasText: 'Turn on' }).click();
    await until(A.page, (x) => x.st.partitionConsent === 'on', null, 'consent on');
    check((await get()).partitionConsent === true, 'Turn on saves partitionConsent');

    // 4. credential resets
    await A.page.locator('input[data-setting="credentialResetConfirm"]').click();
    await until(A.page, (x) => x.st.credentialResetConfirm === 'on', null, 'credential resets on');
    check((await get()).credentialResetConfirm === true, 'credential resets wait: saved at once, no confirmation');

    // 5. a second console follows changes made elsewhere, on either route
    const P2 = await A.ctx.newPage();
    await P2.goto(`${URL}/c/tiles/admin/#settings`);
    await until(P2, (x) => x.st.partitionConsent === 'on', null, 'the second console');
    let r = await xapi('PUT', '/workspace-policies', { partitionConsent: false });
    check(r.status() === 200, `PUT /workspace-policies (the alias) as admin (${r.status()})`);
    await until(P2, (x) => x.st.partitionConsent === 'off', null, 'the second console follows the alias');
    r = await put({ baseAutoUpdate: false });
    await until(P2, (x) => x.st.baseAutoUpdate === 'off', null, 'the second console follows /workspace-settings');
    v = await read(P2);
    check(v.st.partitionConsent === 'off' && v.st.credentialResetConfirm === 'on' && v.st.baseAutoUpdate === 'off',
      `the second console follows the workspace-settings event (${JSON.stringify(v.st)})`);
    await put({ baseAutoUpdate: true });
    await P2.close();

    // 6. a user reads, never sets
    const B = await login(browser, 'dev1', 'devpass123');
    const bg = await (await B.ctx.request.get(`${URL}/api/xbin/workspace-settings`)).json();
    const bp = await B.ctx.request.put(`${URL}/api/xbin/workspace-settings`, { data: { partitionConsent: true } });
    const bpa = await B.ctx.request.put(`${URL}/api/xbin/workspace-policies`, { data: { partitionConsent: true } });
    check(bg.credentialResetConfirm === true && bg.baseAutoUpdate === true && bp.status() === 403 && bpa.status() === 403,
      `a user reads every setting (${JSON.stringify(bg)}) but may set none (${bp.status()}, alias ${bpa.status()})`);

    // 7. the approval warning where people approve: dev1's organisations tile
    const warnAt = (key) => `[data-grant-warning="${key}"]`;
    await B.page.goto(`${URL}/c/tiles/organisations/`);
    const orgWarn = B.page.locator(warnAt(`${EDGE.w} ${EDGE.x}`));
    await orgWarn.first().waitFor({ timeout: 15000 });
    check((await orgWarn.first().textContent()).includes(WARNING), 'the organisations tile shows the approval warning beside the pending grant');
    await closeCtx(B.ctx, B.page);
    // and the admin console's binding → grants view (a hash-only change
    // doesn't reload the page, whose constructor reads the tab)
    await A.page.goto('about:blank');
    await A.page.goto(`${URL}/c/tiles/admin/#grants`);
    const adminWarn = A.page.locator(warnAt(`${EDGE.w} ${EDGE.x}`));
    await adminWarn.first().waitFor({ timeout: 15000 });
    check((await adminWarn.first().textContent()).includes(WARNING), 'the admin console\'s grants view shows the approval warning');
    check(await A.page.locator(warnAt(`${EDGE.z} ${EDGE.x}`)).count() === 0, 'an approved grant carries no warning');
    await shot(A.page, 'admin-grants-partition-warning');

    // 8. this console against an older xbind: v0.3.66's GET
    // /workspace-settings has no switches; they are at /workspace-policies
    const sent = [];
    const onReq = (q) => { if (q.method() === 'PUT' && q.url().includes('/api/xbin/workspace-')) sent.push(new globalThis.URL(q.url()).pathname); };
    A.page.on('request', onReq);
    const v0366 = (route) => route.request().method() !== 'GET' ? route.continue()
      : route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ baseAutoUpdate: true }) });
    await A.page.route('**/api/xbin/workspace-settings', v0366);
    await A.page.goto('about:blank');
    await A.page.goto(`${URL}/c/tiles/admin/#settings`);
    await until(A.page, (x) => Object.keys(x.st).length === 3 && x.st.credentialResetConfirm === 'on', null, 'v0.3.66: the switches from /workspace-policies');
    await A.page.locator('input[data-setting="credentialResetConfirm"]').click();
    await until(A.page, (x) => x.st.credentialResetConfirm === 'off', null, 'v0.3.66: saved');
    check(sent.join(' ') === '/api/xbin/workspace-policies' && (await get()).credentialResetConfirm === false,
      `against v0.3.66 the switches save through /workspace-policies (${sent.join(' ')})`);
    // v0.3.65: no /workspace-policies either
    await A.page.route('**/api/xbin/workspace-policies', (route) => route.fulfill({ status: 404, contentType: 'text/plain', body: '404 page not found\n' }));
    await A.page.reload();
    await until(A.page, (x) => x.groups.partitions === 'absent' && x.st.baseAutoUpdate === 'on', null, 'v0.3.65');
    v = await read(A.page);
    check(Object.keys(v.st).join() === 'baseAutoUpdate' && v.absent.some((t) => t.includes('no partitioned tiles')),
      `against v0.3.65: base auto-update alone, "no partitioned tiles" (${JSON.stringify(v)})`);
    await shot(A.page, 'admin-settings-v0365');
    await A.page.unroute('**/api/xbin/workspace-policies');
    await A.page.unroute('**/api/xbin/workspace-settings', v0366);
    A.page.off('request', onReq);

    // 9. the console from before this tab, against this xbind
    const old = adminBefore();
    const O = await login(browser, 'admin', 'admin');
    const served = new Set();
    await O.ctx.route((u) => { const p = new globalThis.URL(u.href).pathname; return p.startsWith('/c/tiles/admin/') && old.files.has(p.slice('/c/tiles/admin/'.length)); }, (rt) => {
      const name = new globalThis.URL(rt.request().url()).pathname.slice('/c/tiles/admin/'.length);
      served.add(name);
      return rt.fulfill({ status: 200, contentType: 'text/javascript; charset=utf-8', headers: { 'Cache-Control': 'no-store' }, body: old.files.get(name) });
    });
    await O.page.goto(`${URL}/c/tiles/admin/#terminals`);
    await until(O.page, (x) => x.baseAutoUpdate === 'on', null, 'the old terminals tab', oldView);
    await O.page.locator('bx-admin-terminals input[type=checkbox]').click();
    await until(O.page, (x) => x.baseAutoUpdate === 'off', null, 'the old terminals tab saves', oldView);
    check((await get()).baseAutoUpdate === false && served.has('tabs/terminals.js'), `the old console (${old.base})'s terminals tab saves base auto-update`);
    await put({ baseAutoUpdate: true });
    await O.page.goto('about:blank');
    await O.page.goto(`${URL}/c/tiles/admin/#policies`);
    await until(O.page, (x) => x.partitionConsent === 'off' && x.credentialResetConfirm === 'off', null, 'the old policies tab', oldView);
    await O.page.locator('bx-admin-policies input[data-policy="credentialResetConfirm"]').click();
    await until(O.page, (x) => x.credentialResetConfirm === 'on', null, 'the old policies tab saves', oldView);
    check((await get()).credentialResetConfirm === true && served.has('tabs/policies.js'), 'the old console\'s policies tab saves through the alias; the settings read it');
    await put({ credentialResetConfirm: false });
    await until(O.page, (x) => x.credentialResetConfirm === 'off', null, 'the old policies tab follows the `policies` event', oldView);
    check(true, 'the old console\'s policies tab follows a change made through /workspace-settings (the `policies` event)');
    const oldTabs = await O.page.evaluate(() => customElements.get('bx-admin')?.tabsFlat?.().map((t) => t.id) ?? null);
    check(Array.isArray(oldTabs) && oldTabs.includes('policies') && !oldTabs.includes('settings'), `the old console is the one before this tab (${JSON.stringify(oldTabs)})`);
    await shot(O.page, 'admin-settings-old-console');
    await O.ctx.close();
  } finally {
    // later passes must never inherit a switch left on (F10/F7b read them)
    await put(DEFAULTS);
    await dropEdge(xapi);
    await closeCtx(A.ctx, A.page);
  }
  done();
}

module.exports = { adminSettings };
