// hack/ui-harness/passes/vmtoggle.js — the terminal title bar's VM toggle
// (plans/vm-sandbox.md). The harness xbind runs without --isolate, so the
// toggle must show disabled with the server's reason; with GET /ws/term/env
// routed to report a usable VM, it is enabled, asks before restarting, and
// the tab reopens with vm=1 (the socket itself then fails here — no KVM
// sandbox in the harness — which is not what this pass pins).
const { URL, login, closeCtx, settle, fr, waitFor, waitSel, openShell, usePersonalScreen, openTile, shot, checker } = require('../lib');

const TILE = 'apps/crawler';
const sel = `bx-frame[src="${TILE}"]`;

async function openTerm(page) {
  await openShell(page);
  await usePersonalScreen(page);
  await openTile(page, TILE);
  await fr(page, TILE, (f) => f.open('term'));
  await fr(page, TILE, (f) => { if (!f.tabs.length) f.newTerm(); });
  await settle(page);
  await waitSel(page, `${sel} bx-terminal`, { timeout: 20000 });
}

async function vmToggle(browser) {
  const { check, done } = checker('vm-toggle');
  const purge = async (ctx) => {
    for (const s of await (await ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json()) await ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(s.id)}`);
  };

  // ---- A: this host can't run VMs: the toggle is there, disabled, and says why ----
  const A = await login(browser, 'admin', 'admin');
  await purge(A.ctx);
  const env = await (await A.ctx.request.get(`${URL}/ws/term/env?cwd=${encodeURIComponent(TILE)}`)).json();
  check(env.vm && env.vm.available === false && !!env.vm.reason, `GET /ws/term/env reports vm unavailable with a reason (${JSON.stringify(env.vm)})`);
  await openTerm(A.page);
  const btn = A.page.locator(`${sel} .titlebar button.vm`);
  await btn.waitFor({ timeout: 10000 });
  check(await btn.isDisabled(), 'the VM toggle is disabled without KVM/isolation');
  const tipA = (await btn.getAttribute('title')) || '';
  check(tipA.includes('unavailable') && tipA.includes(env.vm.reason), `its tooltip carries the reason (${tipA})`);
  await closeCtx(A.ctx, A.page);

  // ---- B: a host that can (the env answer routed): enabled, confirms, reopens with vm=1 ----
  const B = await login(browser, 'admin', 'admin');
  await B.page.route('**/ws/term/env?**', (route) => (route.request().method() === 'GET'
    ? route.fulfill({ json: { exists: false, baseOutdated: false, vm: { available: true, memMiB: 1024, vcpus: 2 } } })
    : route.continue()));
  await openTerm(B.page);
  const vb = B.page.locator(`${sel} .titlebar button.vm`);
  await vb.waitFor({ timeout: 10000 });
  check(!(await vb.isDisabled()), 'the VM toggle is enabled when the host can run VMs');
  check(((await vb.getAttribute('title')) || '').includes('1024 MiB'), 'its tooltip gives the VM size');
  await shot(B.page, 'vm-toggle', { fullPage: false });
  await vb.evaluate((b) => b.click()); // the narrow window may clip the bar; the handler is what's pinned
  await waitFor(B.page, (t) => !!t.frameFor('apps/crawler')?.testApi().dialog, null, { timeout: 5000, label: 'the restart confirmation' });
  const dlg = await fr(B.page, TILE, (f) => f.dialog);
  check(/VM sandbox/.test(JSON.stringify(dlg)), `it asks before restarting in a VM (${JSON.stringify(dlg).slice(0, 120)})`);
  await fr(B.page, TILE, (f) => f.answerDialog('ok'));
  await waitSel(B.page, `${sel} bx-terminal[vm="1"]`, { timeout: 10000 });
  const tabs = await fr(B.page, TILE, (f) => f.tabs);
  check(tabs.length >= 1 && tabs[0].vm === true, `the tab now asks for a VM (${JSON.stringify(tabs.map((t) => t.vm))})`);
  check(await B.page.locator(`${sel} .titlebar button.vm.on`).count() === 1, 'the toggle shows on');
  const hostOpt = await B.page.locator(`${sel} .titlebar select.scope option[value="host"]`).count();
  check(hostOpt === 0, 'host networking is not offered while in a VM');
  await purge(B.ctx);
  await closeCtx(B.ctx, B.page);
  done();
}

module.exports = { vmToggle };
