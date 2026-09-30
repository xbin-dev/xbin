// hack/ui-harness/passes/policies.js — covers PD-55 — the admin console's
// workspace → policies tab (workspace-template/tiles/admin/tabs/policies.js),
// on the admin tile's own page as admin:
//   1. both switches render off, each saying "applies to partitioned tiles";
//   2. "Ask each person…" asks before turning on, showing the edges between
//      partitioned tiles it would start asking about with the people who used
//      each in the last 30 days (GET /partitions/edges: the real answer, then
//      a stubbed one with totals — covers F10 06§12.3): cancel leaves it off
//      (the server agrees), Turn on saves it;
//   3. "Credential resets wait…" saves at once;
//   4. a second console follows a change made elsewhere (the `policies`
//      event);
//   5. a user reads the policies but may not set them;
//   6. the approval warning (F10, 05§2 S1) beside a pending grant of a
//      partitioned tile on another's people's data, in the admin console's
//      binding → grants view and in the organisations tile's approvals (dev1,
//      a devs admin).
// The real edge: apps/pedge-z (partitioned, org:devs) holds an approved
// grant on apps/pedge-x (partitioned), apps/pedge-w asks for one (pending);
// all three are removed at the end, with the grant. Then both switches back
// off.
const path = require('path');
const { URL, fs, login, closeCtx, waitFor, shot, checker, sleep } = require('../lib');

const WS = process.env.WS || '';
const EDGE = { x: 'apps/pedge-x', z: 'apps/pedge-z', w: 'apps/pedge-w' };
const edgeManifest = (asks) => JSON.stringify({ title: 'partition edge', partition: ['user'],
  ...(asks ? { uses: [{ target: EDGE.x, role: 'reader' }] } : {}) });
const WARNING = `${EDGE.w}'s code — and everyone who can change it — will be able to read and write the ${EDGE.x} data of every person who can read ${EDGE.x}`;

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

// the tab's state as the page shows it: {key: 'on'|'off'}, the open
// confirmation, and the scope labels
const view = () => {
  const r = document.querySelector('bx-admin')?.renderRoot?.querySelector('bx-admin-policies')?.shadowRoot;
  if (!r || !r.querySelector('[data-policies]')) return null;
  const st = {};
  for (const c of r.querySelectorAll('[data-policy-card]')) st[c.dataset.policyCard] = c.dataset.state;
  const edges = r.querySelector('[data-policy-edges]');
  return { st, asking: r.querySelector('[data-policy-confirm]')?.dataset.policyConfirm || '',
    scopes: [...r.querySelectorAll('.scope')].filter((s) => s.textContent.includes('applies to partitioned tiles')).length,
    edges: edges?.dataset.policyEdges || '', edgesText: (edges?.textContent || '').replace(/\s+/g, ' ').trim() };
};

async function adminPolicies(browser) {
  const { check, done } = checker('admin-policies');
  const A = await login(browser, 'admin', 'admin');
  const api = '/api/xbin/workspace-policies';
  const put = (body) => A.ctx.request.put(`${URL}${api}`, { data: body });
  const get = async () => (await A.ctx.request.get(`${URL}${api}`)).json();
  const read = (page) => page.evaluate(`(${view})()`);
  const until = (page, fn, arg, label) => waitFor(page, new Function('t', 'arg', `const v = (${view})(); return !!v && (${fn})(v, arg);`), arg, { label });
  const xapi = (method, p, data) => A.ctx.request.fetch(`${URL}/api/xbin${p}`, { method, data });
  await put({ partitionConsent: false, credentialResetConfirm: false }); // a clean start
  try {
    const seeded = await seedEdge(xapi);
    check(seeded === '200 200 200 200', `a real edge: three partitioned tiles of org:devs, apps/pedge-z's grant on apps/pedge-x approved (${seeded})`);
    await A.page.goto(`${URL}/c/tiles/admin/#policies`);
    await until(A.page, (v) => Object.keys(v.st).length === 2, null, 'the policies tab');
    let v = await read(A.page);
    check(v.st.partitionConsent === 'off' && v.st.credentialResetConfirm === 'off' && v.scopes === 2,
      `both switches off, each "applies to partitioned tiles" (${JSON.stringify(v)})`);

    const consent = A.page.locator('input[data-policy="partitionConsent"]');
    await consent.click();
    await until(A.page, (x) => x.asking === 'partitionConsent', null, 'the turn-on confirmation');
    check(!(await consent.isChecked()) && (await get()).partitionConsent === false, 'turning consent on asks first; nothing saved yet');
    await until(A.page, (x) => x.edges !== '' && x.edges !== 'loading', null, 'the edges preview');
    v = await read(A.page);
    check(Number(v.edges) > 0 && v.edgesText.includes(`${EDGE.z} → ${EDGE.x}: 0 people`) && !v.edgesText.includes(EDGE.w),
      `the confirmation shows the real edge it would ask about, not the pending one (${v.edges}: ${v.edgesText})`);
    await A.page.locator('[data-policy-confirm] button', { hasText: 'cancel' }).click();
    await until(A.page, (x) => !x.asking && x.st.partitionConsent === 'off', null, 'cancel');
    check((await get()).partitionConsent === false, 'cancel leaves it off');
    // the ledger totals, as an xbind with partitioned tiles answers them
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
    await shot(A.page, 'admin-policies-confirm');
    await A.page.locator('[data-policy-confirm] button', { hasText: 'cancel' }).click();
    await until(A.page, (x) => !x.asking, null, 'cancel again');
    await A.page.unroute(edgesRoute);
    await consent.click();
    await A.page.locator('[data-policy-confirm] button', { hasText: 'Turn on' }).click();
    await until(A.page, (x) => x.st.partitionConsent === 'on', null, 'consent on');
    check((await get()).partitionConsent === true, 'Turn on saves partitionConsent');

    await A.page.locator('input[data-policy="credentialResetConfirm"]').click();
    await until(A.page, (x) => x.st.credentialResetConfirm === 'on', null, 'credential resets on');
    check((await get()).credentialResetConfirm === true, 'credential resets wait: saved at once, no confirmation');
    await shot(A.page, 'admin-policies');

    // a second console follows a change made elsewhere
    const P2 = await A.ctx.newPage();
    await P2.goto(`${URL}/c/tiles/admin/#policies`);
    await until(P2, (x) => x.st.partitionConsent === 'on', null, 'the second console');
    const r = await put({ partitionConsent: false });
    check(r.status() === 200, `PUT as admin (${r.status()})`);
    await until(P2, (x) => x.st.partitionConsent === 'off', null, 'the second console follows the policies event');
    v = await read(P2);
    check(v.st.partitionConsent === 'off' && v.st.credentialResetConfirm === 'on',
      `the second console follows the policies event (${JSON.stringify(v.st)})`);
    await P2.close();

    const B = await login(browser, 'dev1', 'devpass123');
    const bg = await B.ctx.request.get(`${URL}${api}`);
    const bp = await B.ctx.request.put(`${URL}${api}`, { data: { partitionConsent: true } });
    check(bg.status() === 200 && bp.status() === 403, `a user reads (${bg.status()}) but may not set (${bp.status()})`);

    // the approval warning where people approve: dev1's organisations tile
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
  } finally {
    // later passes must never inherit a switch left on (F10/F7b read them)
    await put({ partitionConsent: false, credentialResetConfirm: false });
    await dropEdge(xapi);
    await closeCtx(A.ctx, A.page);
  }
  done();
}

module.exports = { adminPolicies };
