// hack/ui-harness/passes/policies.js — covers PD-55 — the admin console's
// workspace → policies tab (workspace-template/tiles/admin/tabs/policies.js),
// on the admin tile's own page as admin:
//   1. both switches render off, each saying "applies to partitioned tiles";
//   2. "Ask each person…" asks before turning on: cancel leaves it off (the
//      server agrees), Turn on saves it;
//   3. "Credential resets wait…" saves at once;
//   4. a second console follows a change made elsewhere (the `policies`
//      event);
//   5. a user reads the policies but may not set them.
// Then both switches back off.
const { URL, login, closeCtx, waitFor, shot, checker } = require('../lib');

// the tab's state as the page shows it: {key: 'on'|'off'}, the open
// confirmation, and the scope labels
const view = () => {
  const r = document.querySelector('bx-admin')?.renderRoot?.querySelector('bx-admin-policies')?.shadowRoot;
  if (!r || !r.querySelector('[data-policies]')) return null;
  const st = {};
  for (const c of r.querySelectorAll('[data-policy-card]')) st[c.dataset.policyCard] = c.dataset.state;
  return { st, asking: r.querySelector('[data-policy-confirm]')?.dataset.policyConfirm || '',
    scopes: [...r.querySelectorAll('.scope')].filter((s) => s.textContent.includes('applies to partitioned tiles')).length };
};

async function adminPolicies(browser) {
  const { check, done } = checker('admin-policies');
  const A = await login(browser, 'admin', 'admin');
  const api = '/api/xbin/workspace-policies';
  const put = (body) => A.ctx.request.put(`${URL}${api}`, { data: body });
  const get = async () => (await A.ctx.request.get(`${URL}${api}`)).json();
  const read = (page) => page.evaluate(`(${view})()`);
  const until = (page, fn, arg, label) => waitFor(page, new Function('t', 'arg', `const v = (${view})(); return !!v && (${fn})(v, arg);`), arg, { label });
  await put({ partitionConsent: false, credentialResetConfirm: false }); // a clean start
  try {
    await A.page.goto(`${URL}/c/tiles/admin/#policies`);
    await until(A.page, (v) => Object.keys(v.st).length === 2, null, 'the policies tab');
    let v = await read(A.page);
    check(v.st.partitionConsent === 'off' && v.st.credentialResetConfirm === 'off' && v.scopes === 2,
      `both switches off, each "applies to partitioned tiles" (${JSON.stringify(v)})`);

    const consent = A.page.locator('input[data-policy="partitionConsent"]');
    await consent.click();
    await until(A.page, (x) => x.asking === 'partitionConsent', null, 'the turn-on confirmation');
    check(!(await consent.isChecked()) && (await get()).partitionConsent === false, 'turning consent on asks first; nothing saved yet');
    await shot(A.page, 'admin-policies-confirm');
    await A.page.locator('[data-policy-confirm] button', { hasText: 'cancel' }).click();
    await until(A.page, (x) => !x.asking && x.st.partitionConsent === 'off', null, 'cancel');
    check((await get()).partitionConsent === false, 'cancel leaves it off');
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
    await closeCtx(B.ctx, B.page);
  } finally {
    // later passes must never inherit a switch left on (F10/F7b read them)
    await put({ partitionConsent: false, credentialResetConfirm: false });
    await closeCtx(A.ctx, A.page);
  }
  done();
}

module.exports = { adminPolicies };
