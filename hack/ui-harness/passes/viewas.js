// hack/ui-harness/passes/viewas.js — "view as user" (docs/auth.md §Viewing
// the workspace as a user, D64): the admin console mints a link from a
// user's more ▾ menu, the browser opens it, the shell loads AS that user
// under the banner, every write is refused, the sessions tab names the
// viewer, and exit hands the browser back to the admin.
const { URL, login, closeCtx, settle, waitSel, gotoTab, shot, checker } = require('../lib');

async function viewAs(browser) {
  const { check, done } = checker('viewas');
  const { ctx, page } = await login(browser, 'admin', 'admin');
  const jget = async (c, p) => (await c.request.get(`${URL}/api/xbin${p}`)).json();

  await gotoTab(page, 'users', 'add user');
  const row = page.locator('tr', { hasText: 'dev1' }).first();
  await row.locator('details.menu summary').click();
  // The tile opens the link in a new tab (allowed here: the console is loaded
  // top-level); fall back to the box's link when the popup never comes.
  const popupP = ctx.waitForEvent('page', { timeout: 8000 }).catch(() => null);
  await row.locator('details.menu button', { hasText: 'view as user' }).click();
  await waitSel(page, '[data-viewas] input');
  const url = await page.locator('[data-viewas] input').inputValue();
  check(/\/login\?impersonate=/.test(url), `the box shows the one-shot link (${url})`);
  await shot(page, 'viewas-box');
  let view = await popupP;
  if (view) await view.waitForLoadState('domcontentloaded');
  else { view = page; await page.goto(url); }
  await waitSel(view, '.viewas', { timeout: 15000 });
  const banner = await view.locator('.viewas').textContent();
  check(/viewing as .*dev1/.test(banner), `the shell shows the banner as dev1 (${banner.trim().slice(0, 60)})`);
  await settle(view);
  await shot(view, 'viewas-banner', { fullPage: false });

  // The whole browser is dev1 now: whoami names the viewer, writes are refused.
  const who = await jget(ctx, '/whoami');
  check(who.id === 'dev1' && who.impersonatedBy === 'admin' && who.readOnly === true, `whoami is dev1, impersonatedBy admin (${JSON.stringify(who)})`);
  const w = await ctx.request.put(`${URL}/api/xbin/prefs/harness-viewas`, { data: { x: 1 } });
  const wb = await w.json().catch(() => ({}));
  check(w.status() === 403 && /read-only/.test(wb.error ?? ''), `a write is refused 403 read-only (${w.status()} ${JSON.stringify(wb)})`);
  const m = await ctx.request.post(`${URL}/api/xbin/impersonate`, { data: { user: 'sales1' } });
  check(m.status() === 403, `no nesting from inside the view (${m.status()})`);
  // A second admin browser sees the viewer on the sessions list.
  const other = await login(browser, 'admin', 'admin');
  const sess = (await jget(other.ctx, '/sessions')).sessions ?? [];
  check(sess.some((s) => s.user === 'dev1' && s.impersonatedBy === 'admin'), `sessions lists dev1 viewed by admin (${JSON.stringify(sess.map((s) => [s.user, s.impersonatedBy]))})`);
  await closeCtx(other.ctx, other.page);

  // Exit from the banner: back to the admin's own session, banner gone.
  await view.locator('.viewas button').click();
  await view.waitForFunction(() => !document.querySelector('bx-shell')?.shadowRoot?.querySelector('.viewas'), null, { timeout: 15000 });
  const back = await jget(ctx, '/whoami');
  check(back.id === 'admin' && !back.impersonatedBy, `exit restores the admin session (${JSON.stringify(back)})`);
  const w2 = await ctx.request.put(`${URL}/api/xbin/prefs/harness-viewas`, { data: { x: 1 } });
  check(w2.ok(), `writes work again after exit (${w2.status()})`);
  await ctx.request.delete(`${URL}/api/xbin/prefs/harness-viewas`);
  if (view !== page) await view.close();
  await closeCtx(ctx, page);
  done();
}

module.exports = { viewAs };
