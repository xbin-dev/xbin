// hack/ui-harness/passes/admindeploy.js — covers D127m (extended by the owner
// 2026-09-28) T9 — the admin console's runtime → deployments tab
// (workspace-template/tiles/admin/tabs/deployments.js), the admin tile
// framed in the shell as admin (a workspace admin: every tile's manager), on
// the deployments pass's fixture apps/deployy (static, owned by org:sales):
//   1. with live reload paused and `dev` added through the API, the tab
//      lists apps/deployy: primary main, not protected, live reload paused,
//      both deployments, the last deploy, the full view; dev's deliveries
//      switch is enabled; the tab's own requests act through the admin
//      tile's frame (no cookie), which the server lets stand in for admin;
//   2. Protect the primary: the shell's dialog carries the terminal window's
//      title and text; confirming protects it (the state, the card's 🛡);
//   3. dev's deliveries: turning them on confirms, the switch then reads on
//      and the state agrees; off again without a dialog;
//   4. Unprotect the primary;
//   5. Reassign the primary… — enabled for a static tile's dev, whose
//      "static" status is healthy: the loud confirmation, "Make dev the
//      primary of apps/deployy?" with the danger button and the "data does
//      not move" box; OK unticked asks again; ticked, dev becomes the
//      primary;
//   6. ⇈ Deployments panel opens apps/deployy's terminal window on its
//      Deployments layout (xbin:open-deployments).
// Then the fixture back in the zero state (the deployments pass's reset).
const { login, closeCtx, settle, fr, waitFor, openShell, usePersonalScreen, openTile, tileFrame, gotoTab, shot, checker } = require('../lib');
const { resetDeploys, stateOf, post } = require('./deployments');

const TILE = 'apps/deployy';
const ADMIN = 'tiles/admin';

async function adminDeployments(browser) {
  const { check, skip, done } = checker('admin-deployments');
  const { ctx, page } = await login(browser, 'admin', 'admin');
  try {
    await resetDeploys(ctx);
    const paused = await post(ctx, 'live-reload/pause', { tile: TILE });
    const added = paused.status === 200 ? await post(ctx, 'add', { tile: TILE, deployment: 'dev' }) : paused;
    if (added.status !== 200) {
      skip(`this xbind can't set up the fixture: ${added.status} ${added.error}`);
      return;
    }
    // Where the tab's requests come from: the admin tile's frame, token only.
    const seen = [];
    page.on('request', (r) => {
      if (r.url().includes('/api/xbin/deployments/') && r.method() === 'POST') {
        seen.push(r.allHeaders().then((h) => ({ frame: !!h['x-xbin-frame-token'], cookie: !!h.cookie })).catch(() => ({})));
      }
    });

    await openShell(page);
    await usePersonalScreen(page);
    await openTile(page, ADMIN);
    const af = await tileFrame(page, ADMIN);
    await af.getByRole('button', { name: 'deployments', exact: true }).click();
    const card = af.locator(`bx-admin-deployments [data-dep-tile="${TILE}"]`);
    await card.waitFor({ timeout: 15000 });
    const text = async () => (await card.textContent()).replace(/\s+/g, ' ');

    // 1. the listing
    let t = await text();
    check(await card.getAttribute('data-view') === 'full', `the full view of ${TILE} (${await card.getAttribute('data-view')})`);
    check(t.includes('primary: main') && t.includes('not protected'), `primary main, not protected: ${t.slice(0, 160)}`);
    check(/Live reload paused/.test(t), 'live reload paused, as the chip says it');
    check(await card.locator('[data-dep-row="main"]').count() === 1 && await card.locator('[data-dep-row="dev"]').count() === 1, 'both deployments listed');
    check(await card.locator('[data-dep-last]').count() === 1, `the last deploy: ${(await card.locator('[data-dep-last]').textContent().catch(() => '')).trim().replace(/\s+/g, ' ')}`);
    const sw = card.locator('[data-dep-switch="dev/deliveries"]');
    check(await sw.isEnabled() && !(await sw.isChecked()), "dev's deliveries switch: enabled, off");
    await shot(page, 'admin-deployments-shell');

    // the shell's dialog: its title, then OK (values: checkboxes to tick)
    const dlg = page.locator('bx-dialog[open]');
    const answer = async (ok, ticks = []) => {
      for (const n of ticks) await dlg.locator(`input[name="${n}"]`).check();
      await dlg.locator('.btns button').filter({ hasText: ok }).click();
      await settle(page);
    };
    const until = async (pred, label) => {
      let s;
      for (let i = 0; i < 50; i++) { s = await stateOf(ctx); if (s.status === 200 && pred(s.body)) return true; await new Promise((r) => setTimeout(r, 200)); }
      check(false, `${label} (state: ${JSON.stringify(s?.body).slice(0, 200)})`);
      return false;
    };

    // 2. protect
    await card.locator('[data-dep-act="protect"]').click();
    await dlg.waitFor({ timeout: 15000 });
    const pt = (await dlg.locator('h3').textContent()).trim(), pm = (await dlg.locator('.msg').textContent()) || '';
    check(pt === 'Protect main?' && pm.includes('Only tile managers'), `the protect confirmation: ${pt} / ${pm.slice(0, 80)}`);
    await shot(page, 'admin-deployments-protect');
    await answer('Protect main');
    if (await until((b) => b.protectedPrimary, 'protecting through the admin tile')) {
      await card.locator('[data-dep-protected]').waitFor({ timeout: 10000 });
      check(true, 'protected: the state and the card');
    }

    // 3. deliveries on (confirmed), then off (no dialog)
    await sw.click();
    await dlg.waitFor({ timeout: 15000 });
    check((await dlg.locator('h3').textContent()).trim() === 'Turn on deliveries for dev?', 'the deliveries confirmation');
    await answer('Turn on deliveries');
    if (await until((b) => (b.deployments || []).find((d) => d.name === 'dev')?.deliveries === true, "dev's deliveries on")) {
      await card.locator('[data-dep-switch="dev/deliveries"]:checked').waitFor({ timeout: 10000 });
      check(true, "dev's deliveries on: the state and the switch");
    }
    await card.locator('[data-dep-switch="dev/deliveries"]').click();
    await until((b) => (b.deployments || []).find((d) => d.name === 'dev')?.deliveries === false, "dev's deliveries off again, no dialog");
    check(await dlg.count() === 0, 'turning deliveries off asks nothing');

    // 4. unprotect
    await card.locator('[data-dep-act="unprotect"]').click();
    await dlg.waitFor({ timeout: 15000 });
    check((await dlg.locator('h3').textContent()).trim() === 'Unprotect main?', 'the unprotect confirmation');
    await answer('Unprotect');
    await until((b) => !b.protectedPrimary, 'unprotecting through the admin tile');

    // 5. reassign — a static tile's deployment ("static", no backend) may become the primary
    const re = card.locator('[data-dep-act="reassign"]');
    await re.waitFor({ timeout: 10000 });
    for (let i = 0; i < 50 && !(await re.isEnabled()); i++) await new Promise((r) => setTimeout(r, 100));
    check(await re.isEnabled(), `Reassign the primary… enabled (${await re.getAttribute('title')})`);
    await re.click();
    await dlg.waitFor({ timeout: 15000 });
    const rt = (await dlg.locator('h3').textContent()).trim(), rm = (await dlg.locator('.msg').textContent()) || '';
    const danger = await dlg.locator('.btns button.danger').filter({ hasText: 'Reassign the primary' }).count();
    const box = (await dlg.locator('label.chk').first().textContent() || '').trim();
    check(rt === `Make dev the primary of ${TILE}?` && danger === 1 && rm.startsWith(`Everything that reaches ${TILE} moves to dev at once`)
      && box === "I understand main's data does not move to dev", `the loud confirmation: ${rt} · danger ${danger} · ${box}`);
    await shot(page, 'admin-deployments-reassign');
    await answer('Reassign the primary');
    await dlg.waitFor({ timeout: 10000 });
    check(((await dlg.locator('.err').textContent().catch(() => '')) || '').includes('Tick the box'), 'OK without the box asks again');
    await answer('Reassign the primary', ['ok']);
    if (await until((b) => b.primary === 'dev', 'dev is the primary after the confirmed reassignment')) check(true, 'confirmed with the box ticked: dev is the primary');
    const via = await Promise.all(seen);
    check(via.length > 0 && via.every((r) => r.frame && !r.cookie), `every act went through the admin tile's frame, token only (${JSON.stringify(via)})`);

    // 6. the link to the tile's Deployments panel
    await card.locator('[data-dep-open]').click();
    try {
      await waitFor(page, (sh, p) => sh.frameFor(p)?.testApi().layout === 'deployments', TILE, { timeout: 15000, label: `${TILE}'s window on its Deployments layout` });
      check(true, `⇈ Deployments panel opened ${TILE}'s terminal window on its Deployments layout`);
      await fr(page, TILE, (f) => f.closeTerminal());
    } catch (e) {
      check(false, String(e.message).split('\n')[0]);
    }

    // the tab as its own document, for the eye
    await gotoTab(page, 'deployments', 'tile deployments');
    await shot(page, 'admin-deployments');
  } finally {
    const s = await resetDeploys(ctx).catch((e) => ({ status: 0, error: String(e) }));
    check(s.status === 200 && !s.body?.record, `the fixture back in the zero state (${s.status} ${s.error || ''})`);
    await closeCtx(ctx, page);
  }
  done();
}

module.exports = { adminDeployments };
