// hack/ui-harness/passes/adminmap.js — the admin console's access map tab
// (moved out of shots.js, which sits at its size budget; unchanged).
const { URL, login, closeCtx, waitSel, gotoTab, shot, checker } = require('../lib');

// The access map tab (the first admin tab split into its own element): the
// structure + matrix render from /access-matrix, a cell click opens the
// derivation panel, the owner-transfer editor previews before it commits.
async function adminMap(browser) {
  const { check, done } = checker('admin-map');
  const { ctx, page } = await login(browser, 'admin', 'admin');
  await gotoTab(page, 'map', 'effective access');
  await waitSel(page, '.mcell.has, .maprow', { timeout: 15000 });
  const cells = await page.locator('.mcell.has').count();
  check(cells > 0, `matrix renders ${cells} access cells`);
  check((await page.locator('.snode.org').count()) >= 3, 'structure lists the seeded orgs');
  await page.locator('.mcell.has').first().click();
  await waitSel(page, 'text=effective (highest wins)');
  check(true, 'a cell click opens the derivation panel');
  await shot(page, 'admin-map');
  // transfer: pick a different owner → preview → the commit button appears → cancel
  const row = page.locator('tr', { has: page.locator('td.mtile', { hasText: 'apps/pinned' }) }).first();
  await row.locator('button', { hasText: 'transfer' }).click();
  await waitSel(page, 'button:has-text("preview")');
  const sel = page.locator('select', { has: page.locator('option[value=""]') }).last();
  await sel.selectOption('');
  await page.locator('button', { hasText: 'preview' }).click();
  await waitSel(page, 'button.go:has-text("transfer")');
  check(true, 'preview yields the transfer button (no commit made)');
  await shot(page, 'admin-map-transfer');
  await page.locator('button', { hasText: 'cancel' }).last().click();
  await waitSel(page, 'button:has-text("preview")', { state: 'detached' });
  const owner = (await ctx.request.get(`${URL}/api/xbin/owner?tile=apps/pinned`).then((r) => r.json())).owner;
  check(owner === 'org:devs', `cancel left apps/pinned with its owner (${owner})`);
  await closeCtx(ctx, page);
  done();
}

module.exports = { adminMap };
