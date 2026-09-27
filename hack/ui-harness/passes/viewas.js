// hack/ui-harness/passes/viewas.js — "view as user" (docs/auth.md §Viewing
// the workspace as a user, D64): the admin console mints a link from a
// user's more ▾ menu, the browser opens it, the shell loads AS that user
// under the banner, every write is refused — pausing live reload and
// deploying included, and the terminal window draws those actions disabled
// with "dev1 may do this" (web/frame-deploy.js) —, the sessions tab names the
// viewer, and exit hands the browser back to the admin.
const { URL, login, closeCtx, settle, waitSel, waitFor, sh, fr, gotoTab, shot, checker } = require('../lib');

// covers P21 T9 (15-test-plan §7.4, 10-ux §4.4) — the tile deployments'
// writes are writes: refused from a view, and drawn disabled there.
async function deploymentsReadOnly(check, skip, ctx, view) {
  const TILE = 'apps/reloady';
  for (const [op, data, what] of [['live-reload/pause', { tile: TILE }, 'pausing live reload'], ['deploy', { tile: TILE, deployment: 'main' }, 'a deploy']]) {
    const r = await ctx.request.post(`${URL}/api/xbin/deployments/${op}`, { data });
    const b = await r.json().catch(() => ({}));
    check(r.status() === 403 && /read-only/.test(b.error ?? ''), `${what} is refused 403 read-only (${r.status()} ${JSON.stringify(b)})`);
  }
  const st = await ctx.request.get(`${URL}/api/xbin/deployments?tile=${encodeURIComponent(TILE)}`);
  if (!st.ok()) { skip(`the terminal window's live reload controls in a view: GET /api/xbin/deployments answers ${st.status()} on this xbind`); return; }
  try {
    await sh(view, (t, p) => t.openTile(p), TILE);
    await waitSel(view, `.card[data-path="${TILE}"] bx-frame`, { state: 'attached' });
    await fr(view, TILE, (f) => f.open('term'));
    // the viewed user's name arrives with whoami, after the state
    await waitFor(view, (t, p) => (t.frameFor(p)?.testApi().deploy.chipItems() || []).some((it) => /^dev1 may do this/.test(it.hint || '')), TILE,
      { timeout: 15000, label: 'the viewed window\'s live reload menu' });
    const acts = (await fr(view, TILE, (f) => f.deploy.chipItems())).filter((it) => !it.kind && (it.hint || it.enabled));
    check(acts.length > 0 && acts.every((it) => !it.enabled && /^dev1 may do this — you are viewing as dev1 \(read-only\)\.$/.test(it.hint)),
      `the window draws every live reload action disabled: "dev1 may do this" (${JSON.stringify(acts)})`);
    await fr(view, TILE, (f) => f.closeTerminal());
    await view.waitForTimeout(600); // the window's debounced save goes out (refused) inside the view, not after the exit
  } catch (e) {
    check(false, `the viewed terminal window: ${e.message.split('\n')[0]}`);
  }
}

async function viewAs(browser) {
  const { check, skip, done } = checker('viewas');
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
  await deploymentsReadOnly(check, skip, ctx, view);
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
