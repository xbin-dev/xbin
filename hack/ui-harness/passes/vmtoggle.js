// hack/ui-harness/passes/vmtoggle.js — the terminal title bar's VM toggle
// (plans/vm-sandbox.md). The harness xbind runs without --isolate, so the
// toggle must show disabled with the server's reason; with GET /ws/term/env
// routed to report a usable VM, it is enabled, asks before restarting, and
// the tab reopens with vm=1 (the socket itself then fails here — no KVM
// sandbox in the harness — which is not what this pass pins). The second
// browser's window is RESTORED open from the first one's pref, so it also
// pins that a restored window loads the tile state (the toggle) like one
// opened by hand. The toggle sits on the bar or, when this host's bar
// doesn't fit the window, in the tools row (lib.js showPickers).
const { URL, login, closeCtx, settle, fr, waitFor, waitSel, openShell, usePersonalScreen, openTile, shot, checker, showPickers } = require('../lib');

const TILE = 'apps/crawler';
const sel = `bx-frame[src="${TILE}"]`;

async function openTerm(page) {
  await openShell(page);
  await usePersonalScreen(page);
  await openTile(page, TILE);
  await fr(page, TILE, (f) => f.open('term'));
  await showPickers(page, TILE);
  await fr(page, TILE, (f) => { if (!f.tabs.length) f.newTerm(); });
  await settle(page);
  await waitSel(page, `${sel} bx-terminal`, { timeout: 20000 });
}

async function vmToggle(browser) {
  const { check, skip, done } = checker('vm-toggle');
  const purge = async (ctx) => {
    for (const s of await (await ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json()) await ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(s.id)}`);
  };

  // ---- A: this host can't run VMs: the toggle is there, disabled, and says why ----
  const A = await login(browser, 'admin', 'admin');
  await purge(A.ctx);
  const env = await (await A.ctx.request.get(`${URL}/ws/term/env?cwd=${encodeURIComponent(TILE)}`)).json();
  await openTerm(A.page);
  if (env.vm?.available) {
    // an xbind run with --isolate on a KVM (or emulating) host: nothing to show disabled
    skip(`the disabled toggle: this xbind can run VM sandboxes (${JSON.stringify(env.vm)}) — run the harness without --isolate to pin it`);
  } else {
    check(env.vm && env.vm.available === false && !!env.vm.reason, `GET /ws/term/env reports vm unavailable with a reason (${JSON.stringify(env.vm)})`);
    const btn = A.page.locator(`${sel} button.vm`);
    await btn.waitFor({ timeout: 10000 });
    check(await btn.isDisabled(), 'the VM toggle is disabled without KVM/isolation');
    const tipA = (await btn.getAttribute('title')) || '';
    check(tipA.includes('unavailable') && tipA.includes(env.vm.reason), `its tooltip carries the reason (${tipA})`);
  }
  // the window state reaches the server on a 400 ms debounce: B restores from it
  for (let i = 0; i < 40; i++) {
    const w = await (await A.ctx.request.get(`${URL}/api/xbin/prefs/term%3Aapps%3Acrawler`)).json().catch(() => null);
    if (w && w.open) break;
    await new Promise((r) => setTimeout(r, 100));
  }
  await closeCtx(A.ctx, A.page);

  // ---- B: a host that can (the env answer routed): enabled, confirms, reopens with vm=1 ----
  const B = await login(browser, 'admin', 'admin');
  await B.page.route('**/ws/term/env?**', (route) => (route.request().method() === 'GET'
    ? route.fulfill({ json: { exists: false, baseOutdated: false, vm: { available: true, memMiB: 1024, vcpus: 2 } } })
    : route.continue()));
  // A left its window open: B's is RESTORED from that pref (never opened
  // here), and must still load the tile state the toggle needs
  await openShell(B.page);
  await usePersonalScreen(B.page);
  await openTile(B.page, TILE);
  await waitFor(B.page, (t) => !!t.frameFor('apps/crawler')?.testApi().terminalOpen, null, { timeout: 15000, label: "A's open window restored in B" });
  await showPickers(B.page, TILE);
  await waitSel(B.page, `${sel} bx-terminal`, { timeout: 20000 });
  const vb = B.page.locator(`${sel} button.vm`);
  await vb.waitFor({ timeout: 10000 });
  check(!(await vb.isDisabled()), 'the VM toggle is enabled when the host can run VMs (in a window restored open from A\'s)');
  check(((await vb.getAttribute('title')) || '').includes('1024 MiB'), 'its tooltip gives the VM size');
  await shot(B.page, 'vm-toggle', { fullPage: false });
  await vb.evaluate((b) => b.click()); // the handler is what's pinned, wherever the bar put the button
  await waitFor(B.page, (t) => !!t.frameFor('apps/crawler')?.testApi().dialog, null, { timeout: 5000, label: 'the restart confirmation' });
  const dlg = await fr(B.page, TILE, (f) => f.dialog);
  check(/VM sandbox/.test(JSON.stringify(dlg)), `it asks before restarting in a VM (${JSON.stringify(dlg).slice(0, 120)})`);
  await fr(B.page, TILE, (f) => f.answerDialog('ok'));
  await waitSel(B.page, `${sel} bx-terminal[vm="1"]`, { timeout: 10000 });
  const tabs = await fr(B.page, TILE, (f) => f.tabs);
  check(tabs.length >= 1 && tabs[0].vm === true, `the tab now asks for a VM (${JSON.stringify(tabs.map((t) => t.vm))})`);
  check(await B.page.locator(`${sel} button.vm.on`).count() === 1, 'the toggle shows on');
  const hostOpt = await B.page.locator(`${sel} select.scope option[value="host"]`).count();
  check(hostOpt === 0, 'host networking is not offered while in a VM');
  await purge(B.ctx);
  await closeCtx(B.ctx, B.page);
  // the window pref would restore this window over the tile in later passes
  const T = await login(browser, 'admin', 'admin');
  await T.ctx.request.delete(`${URL}/api/xbin/prefs/term%3Aapps%3Acrawler`);
  await T.ctx.close();
  done();
}

module.exports = { vmToggle };
