// hack/ui-harness/passes/personalplane.js — the personal plane (D88) end to
// end in the browser:
//
//   - admin → users → dev1's personal… editor attaches a network set through
//     its multiselect, and the row grows a net·<set> pill;
//   - the same editor's "no personal tiles" switch on sales1 takes the
//     shell's "me" owner away (sales1 still creates as org:sales);
//   - dev1, owning apps/dev1-notes, now has a personal network there: the
//     net slot's picker offers `personal`, internet is inside the allowance
//     and host is greyed out ("outside your allowance"); a self-bind of
//     internet goes through and host is refused;
//   - the terminal scope list on that tile leads with `personal` (the
//     default) and offers the set to narrow to.
//
// Everything is put back at the end (the termSets pass expects dev1 with no
// personal sets).
const { URL, login, closeCtx, settle, sh, waitSel, gotoTab, openShell, usePersonalScreen, shot, checker } = require('../lib');

const SET = 'harness-web';

async function personalPlane(browser) {
  const { check, done } = checker('personal-plane');
  const admin = await login(browser, 'admin', 'admin', { viewport: { width: 1500, height: 1100 } });
  const aj = async (ctx, method, p, data) => {
    const r = await ctx.request.fetch(`${URL}/api/xbin${p}`, { method, data });
    let body = null;
    try { body = await r.json(); } catch { /* not json */ }
    return { status: r.status(), body };
  };
  const userRow = async (id) => (await aj(admin.ctx, 'GET', '/users')).body.users.find((u) => u.id === id) ?? {};

  let r = await aj(admin.ctx, 'PUT', `/net-sets/${SET}`, { rules: ['internet'] });
  check(r.status === 200, `seed: network set ${SET} = internet (${r.status})`);

  // 1. admin → users → dev1 → personal… → attach the set through the multiselect
  const { page } = admin;
  await gotoTab(page, 'users', 'add user');
  const row = (id) => page.locator('bx-admin-users tr', { hasText: id }).first();
  await row('dev1').locator('button', { hasText: 'personal…' }).click();
  await waitSel(page, 'bx-admin-users .personal bx-multiselect.nsets');
  await page.locator('bx-admin-users .personal bx-multiselect.nsets button.control').click();
  await page.locator('bx-admin-users .personal bx-multiselect.nsets label.opt', { hasText: SET }).click();
  let dev1 = {};
  for (let i = 0; i < 40 && !(dev1.netSets ?? []).includes(SET); i++) {
    await page.waitForTimeout(100);
    dev1 = await userRow('dev1');
  }
  check((dev1.netSets ?? []).includes(SET), `the personal… multiselect attached ${SET} to dev1 (${JSON.stringify(dev1.netSets)})`);
  check((dev1.personal?.netRules ?? []).includes('internet') && (dev1.personal?.allow ?? []).includes('net:internet'),
    `dev1's resolved plane: personal network + allowance (${JSON.stringify(dev1.personal)})`);
  await page.keyboard.press('Escape');
  await page.locator(`bx-admin-users tr >> text=net·${SET}`).first().waitFor({ timeout: 10000 });
  check(true, `dev1's row shows the net·${SET} pill`);
  await shot(page, 'personal-plane-users', { fullPage: false });

  // 2. the "no personal tiles" switch on sales1, through the same editor
  await row('sales1').locator('button', { hasText: 'personal…' }).click();
  const cb = page.locator('bx-admin-users tr + tr .personal input[name="noPersonalTiles"]').last();
  await cb.check();
  let sales1 = {};
  for (let i = 0; i < 40 && !sales1.noPersonalTiles; i++) {
    await page.waitForTimeout(100);
    sales1 = await userRow('sales1');
  }
  check(!!sales1.noPersonalTiles, `the editor switched sales1's personal tiles off (${sales1.noPersonalTiles})`);
  {
    const s = await login(browser, 'sales1', 'salespass123');
    const who = (await aj(s.ctx, 'GET', '/whoami')).body;
    check(who.personalTiles === false, `sales1 whoami: personalTiles=false (${who.personalTiles})`);
    await openShell(s.page);
    await usePersonalScreen(s.page);
    const owners = await sh(s.page, (t) => {
      const it = t.canvasMenuItems().find((i) => String(i.label).startsWith('Create a new tile'));
      return { hint: it?.hint ?? '', sub: (it?.items ?? []).map((x) => x.label) };
    });
    check(!/me/.test(owners.hint) && !owners.sub.some((l) => /me/.test(l)) && /sales/i.test(owners.hint + owners.sub.join()),
      `sales1's shell offers no "me" owner, only the org (${JSON.stringify(owners)})`);
    r = await aj(s.ctx, 'POST', '/create', { path: 'apps/sales1-own', title: 'x', owner: 'user:sales1' });
    check(r.status === 403 && /turned off for your account/.test(r.body?.error ?? ''), `sales1: a personal create is refused, naming why (${r.status})`);
    await closeCtx(s.ctx, s.page);
  }

  // 3–4. dev1 on their own tile: picker, self-bind, terminal scopes
  {
    const d = await login(browser, 'dev1', 'devpass123', { viewport: { width: 1500, height: 1100 } });
    const b = (await aj(d.ctx, 'GET', '/bindings')).body;
    check(!!b.approvable?.['apps/dev1-notes'], 'dev1 may wire their own personal tile (approvable)');
    const opts = b.netOptions?.['apps/dev1-notes'] ?? [];
    const opt = (id) => opts.find((o) => o.id === id) ?? {};
    check(!!opt('personal').id && !opt('personal').blocked, `the picker offers the personal network (${JSON.stringify(opt('personal'))})`);
    check(!opt('internet').blocked && opt('host').blocked && /outside your network allowance/.test(opt('host').label ?? ''),
      `internet inside the allowance, host greyed out (${JSON.stringify([opt('internet'), opt('host')])})`);
    await openShell(d.page);
    await usePersonalScreen(d.page);
    await sh(d.page, (t, p) => t.openAdminWindow(p, 'interfaces'), 'apps/dev1-notes');
    await waitSel(d.page, '.admin-pop bx-tile-admin details[data-sec="interfaces"] select');
    const pop = await d.page.locator('.admin-pop bx-tile-admin details[data-sec="interfaces"] select').first()
      .evaluate((sel) => [...sel.options].map((o) => [o.value, o.disabled, o.textContent]));
    check(pop.some(([v, dis]) => v === 'personal' && !dis) && pop.some(([v, dis, t]) => v === 'host' && dis && /outside your allowance/.test(t)),
      `the tile popover: personal offered, host "outside your allowance" (${JSON.stringify(pop.map(([v, dis]) => [v, dis]))})`);
    check(await d.page.locator('.admin-pop bx-tile-admin [data-readonly]').count() === 0, 'the popover is not read-only for the owner');
    await shot(d.page, 'personal-plane-popover', { fullPage: false });
    await sh(d.page, (t) => t.closeAdminWindow());
    r = await aj(d.ctx, 'POST', '/bindings', { component: 'apps/dev1-notes', slot: 'net', provider: 'internet' });
    check(r.status === 200, `dev1 binds internet on their own tile (${r.status} ${JSON.stringify(r.body)})`);
    r = await aj(d.ctx, 'POST', '/bindings', { component: 'apps/dev1-notes', slot: 'net', provider: 'host' });
    check(r.status === 403, `dev1 is refused host there (${r.status})`);
    r = await aj(d.ctx, 'DELETE', '/bindings', { component: 'apps/dev1-notes', slot: 'net' });
    check(r.status === 200, `dev1 unbinds again (${r.status})`);
    const tn = (await aj(d.ctx, 'GET', '/term-net?tile=apps/dev1-notes')).body;
    const ids = (tn.scopes ?? []).map((s) => s.id);
    check(tn.default === 'personal' && tn.personal === true && ids[0] === 'personal' && ids.includes(`set:${SET}`) && ids.includes('internet') && ids.includes('none'),
      `terminal scopes on dev1's tile: ${ids.join(',')} (default ${tn.default})`);
    await closeCtx(d.ctx, d.page);
  }

  // tidy: dev1 without personal sets, sales1 back on, the set gone
  r = await aj(admin.ctx, 'PATCH', '/users/dev1', { netSets: [] });
  check(r.status === 200, `tidy: dev1's personal sets cleared (${r.status})`);
  r = await aj(admin.ctx, 'PATCH', '/users/sales1', { noPersonalTiles: false });
  check(r.status === 200, `tidy: sales1's personal tiles back on (${r.status})`);
  r = await aj(admin.ctx, 'DELETE', `/net-sets/${SET}`);
  check(r.status === 200, `tidy: ${SET} deleted (${r.status})`);
  await settle(page);
  await closeCtx(admin.ctx, admin.page);
  done();
}

module.exports = { personalPlane };
