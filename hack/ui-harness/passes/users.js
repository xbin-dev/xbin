// hack/ui-harness/passes/users.js — the admin console's users tab.
//
// The click-through editors on a user row write through the tab element's
// _orgAPI (shared.js WithRouter): open tiles… on dev1, add an entry, save,
// and the user's ACL carries it — then put it back. This is the pass that
// would have caught the router losing _orgAPI when the organisations tab
// moved out (I7 slice 5): the save threw and the editor never closed.
const { URL, login, closeCtx, settle, waitSel, gotoTab, shot, checker } = require('../lib');

async function users(browser) {
  const { check, done } = checker('users');
  const { ctx, page } = await login(browser, 'admin', 'admin');
  const jget = async (p) => (await ctx.request.get(`${URL}/api/xbin${p}`)).json();
  const before = (await jget('/users')).users.find((u) => u.id === 'dev1')?.tiles ?? {};
  check(!('apps/pinned' in before), `clean slate: dev1 has no apps/pinned entry (${Object.keys(before).join(', ')})`);

  await gotoTab(page, 'users', 'add user');
  const row = page.locator('tr', { hasText: 'dev1' }).first();
  await row.locator('button[title="per-tile access outside orgs"]').click();
  await waitSel(page, 'input[list="tile-targets"]');
  await page.locator('button', { hasText: '+ entry' }).click();
  await page.locator('input[list="tile-targets"]').last().fill('apps/pinned');
  await settle(page);
  await shot(page, 'users-tiles-editor');
  await page.locator('button', { hasText: /^save$/ }).click();
  await waitSel(page, 'input[list="tile-targets"]', { state: 'detached', timeout: 10000 });
  const after = (await jget('/users')).users.find((u) => u.id === 'dev1')?.tiles ?? {};
  check(after['apps/pinned'] === 'write', `tiles… editor saved apps/pinned=write through the tab element (${JSON.stringify(after)})`);
  const err = await page.evaluate(() => document.querySelector('bx-admin')?.renderRoot.querySelector('.body > .err')?.textContent ?? '');
  check(!err, `no error slot after the save (${err})`);

  // tidy: the seeded ACL back
  const put = await ctx.request.patch(`${URL}/api/xbin/users/dev1`, { data: { tiles: before } });
  check(put.ok(), `tidy: dev1 tiles restored (${put.status()})`);
  await closeCtx(ctx, page);
  done();
}

module.exports = { users };
