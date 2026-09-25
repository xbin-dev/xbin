// hack/ui-harness/passes/newtile.js — the shell's "Create a new tile" dialog
// as a non-admin (D82). dev1 holds no create pattern: a personal tile at a
// free path is created and owned by them; a taken name and a path carrying a
// removed tile's leftover grant are refused, and the refusal shows in the
// dialog's alert box (it used to replace the intro text and read as a hint).
const { URL, login, closeCtx, settle, sh, waitFor, waitSel, openShell, usePersonalScreen, closeTile, shot, checker } = require('../lib');

async function newTile(browser) {
  const { check, done } = checker('new-tile');
  const admin = await login(browser, 'admin', 'admin');
  const ghost = { from: 'apps/ghost-harness', target: 'apps/welcome', role: 'reader' };
  const seeded = await admin.ctx.request.post(`${URL}/api/xbin/grants`, { data: ghost });
  check(seeded.ok(), `seed a leftover grant row for ${ghost.from} (${seeded.status()})`);

  const { ctx, page } = await login(browser, 'dev1', 'devpass123', { viewport: { width: 1400, height: 1100 } });
  await openShell(page);
  await usePersonalScreen(page);
  // an empty screen, so the right-click always lands on bare canvas
  const saved = await sh(page, (t) => t.openTiles.map((o) => ({ ...o })));
  await sh(page, (t) => { t.setGeom(() => []); return t.flushSave(); });
  await settle(page);

  // Canvas menu → Create a new tile ▸ me (dev1 also holds Create in devs).
  const openDialog = async () => {
    const r = await page.evaluate(() => {
      const t = document.querySelector('bx-shell').testApi();
      const b = t.query('.canvas').getBoundingClientRect();
      return { x: b.left + 120, y: Math.min(b.bottom, innerHeight) - 120 };
    });
    await page.mouse.click(r.x, r.y, { button: 'right' });
    await waitSel(page, 'bx-menu .panel.main .it');
    await page.locator('bx-menu .panel.main .it', { hasText: 'Create a new tile' }).hover();
    await waitSel(page, 'bx-menu .panel.sub .it');
    await page.locator('bx-menu .panel.sub .it', { hasText: /me/ }).click();
    await waitSel(page, 'bx-dialog[open] input[name="name"]');
  };
  const submit = async (name) => {
    await page.locator('bx-dialog[open] input[name="name"]').fill(name);
    await page.locator('bx-dialog[open] button', { hasText: /^Create$/ }).click();
  };
  const alertText = () => page.locator('bx-dialog[open] .err[role="alert"]').textContent({ timeout: 10000 });

  // 1. a free path: created, owned by dev1, no pattern needed
  const slug = `harness-${Date.now().toString(36)}`;
  await openDialog();
  await submit(slug);
  await waitFor(page, (t, p) => t.isOpen(p), `apps/${slug}`, { timeout: 15000, label: 'new tile opened' });
  const own = await (await ctx.request.get(`${URL}/api/xbin/owner?tile=apps/${slug}`)).json();
  check(own.owner === 'user:dev1', `personal tile created without a create pattern, owned by dev1 (${JSON.stringify(own)})`);
  await closeTile(page, `apps/${slug}`); // it opened under the next right-click

  // 2. a taken name: the dialog comes back with the refusal in its alert box
  await openDialog();
  await submit('dev1-notes');
  const taken = await alertText();
  check(/already exists/.test(taken), `taken name → alert "${taken}"`);
  const intro = await page.locator('bx-dialog[open] p.msg').textContent();
  check(/Creates a static tile/.test(intro), `the intro message stays beside the alert ("${intro.slice(0, 40)}…")`);
  await shot(page, 'new-tile-error', { fullPage: false });

  // 3. a path with a removed tile's leftover grant: refused, naming it
  await submit('ghost-harness');
  const left = await alertText();
  check(/leftover|removed tile/.test(left) && left.includes('grant apps/ghost-harness'), `leftover grant → alert names it ("${left}")`);
  await page.locator('bx-dialog[open] button', { hasText: /^Cancel$/ }).click();
  await settle(page);

  // tidy: dev1's layout back, the seeded row dropped (the created tile
  // stays; slugs are unique)
  await sh(page, (t, tiles) => { t.setGeom(() => tiles); return t.flushSave(); }, saved);
  const rm = await admin.ctx.request.delete(`${URL}/api/xbin/grants`, { data: ghost });
  check(rm.ok(), `tidy: leftover grant removed (${rm.status()})`);
  await closeCtx(ctx, page);
  await closeCtx(admin.ctx, admin.page);
  done();
}

module.exports = { newTile };
