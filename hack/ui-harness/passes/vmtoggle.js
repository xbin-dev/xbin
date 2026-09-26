// hack/ui-harness/passes/vmtoggle.js — the terminal title bar's VM toggle
// (plans/vm-sandbox.md). The harness xbind runs without --isolate, so the
// toggle must show disabled with the server's reason; with GET /ws/term/env
// routed to report a usable VM, it is enabled, asks before restarting, and
// the tab reopens with vm=1 (the socket itself then fails here — no KVM
// sandbox in the harness — which is not what this pass pins). The second
// browser's window is RESTORED open from the first one's pref, so it also
// pins that a restored window loads the tile state (the toggle) like one
// opened by hand. The toggle sits on the bar or, when this host's bar
// doesn't fit the window, in the tools row (lib.js showPickers). The
// launcher's VM switch (only where VMs can run) and the title bar's toggle
// set the tile's per-user choice for new sessions (pref termvm:<tile>):
// shells then open with vm=1, agents create with {vm:true}, and a new
// browser context finds the choice where it was left.
const { URL, login, closeCtx, settle, fr, waitFor, waitSel, openShell, usePersonalScreen, openTile, shot, checker, showPickers } = require('../lib');

const TILE = 'apps/crawler';
const sel = `bx-frame[src="${TILE}"]`;
const VMPREF = `${URL}/api/xbin/prefs/termvm%3Aapps%3Acrawler`;

// the tile's VM choice as the server has it (it's saved fire-and-forget)
async function vmPref(ctx, want) {
  let v = null;
  for (let i = 0; i < 40; i++) {
    const r = await ctx.request.get(VMPREF);
    v = r.ok() ? await r.json().catch(() => null) : null;
    if ((v === true) === want) break;
    await new Promise((res) => setTimeout(res, 100));
  }
  return v === true;
}

async function openTerm(page, onLauncher) {
  await openShell(page);
  await usePersonalScreen(page);
  await openTile(page, TILE);
  await fr(page, TILE, (f) => f.open('term'));
  if (onLauncher) { await waitSel(page, `${sel} .launcher .lcard`, { timeout: 15000 }); await settle(page); await onLauncher(); }
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
  await A.ctx.request.delete(VMPREF);
  const env = await (await A.ctx.request.get(`${URL}/ws/term/env?cwd=${encodeURIComponent(TILE)}`)).json();
  await openTerm(A.page, async () => {
    if (!env.vm?.available) check(await A.page.locator(`${sel} .launcher .lvm`).count() === 0, 'the launcher offers no VM switch where VMs can\'t run');
  });
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
  check(await vmPref(B.ctx, true), 'the switch that went through is now the tile\'s choice (pref termvm:apps:crawler = true)');

  // the launcher (no tabs left): its switch shows that choice and flips it
  await purge(B.ctx);
  // the directory's events drop the purged tab; close it by hand if they're slow
  await waitSel(B.page, `${sel} .launcher`, { timeout: 8000 }).catch(() => fr(B.page, TILE, (f) => { for (let i = f.tabs.length - 1; i >= 0; i--) f.closeTab(i); f.open('term'); }));
  await waitSel(B.page, `${sel} .launcher .lvm.on`, { timeout: 15000 });
  check(((await B.page.locator(`${sel} .launcher .lvm`).getAttribute('title')) || '').includes('remembered'), 'the launcher offers the VM switch, on');
  await shot(B.page, 'vm-launcher', { fullPage: false });
  await B.page.locator(`${sel} .launcher .lvm`).evaluate((b) => b.click());
  await waitSel(B.page, `${sel} .launcher .lvm:not(.on)`, { timeout: 5000 });
  check(!(await vmPref(B.ctx, false)), 'switched off: the pref is gone');
  await B.page.locator(`${sel} .launcher .lvm`).evaluate((b) => b.click());
  check(await vmPref(B.ctx, true), 'switched back on: remembered');
  // new sessions follow it: a shell asks for a VM, an agent creates in one
  const bodies = [];
  await B.page.route('**/api/xbin/term/sessions', (route) => {
    if (route.request().method() === 'POST') bodies.push(route.request().postDataJSON());
    return route.continue();
  });
  await B.page.locator(`${sel} .launcher .lcard`).first().evaluate((b) => b.click()); // Bash
  await waitSel(B.page, `${sel} bx-terminal[vm="1"]`, { timeout: 10000 });
  check(true, 'Bash from the launcher opens with vm=1');
  await fr(B.page, TILE, (f) => f.startKind('agent', 'fake'));
  for (let i = 0; i < 50 && !bodies.length; i++) await new Promise((r) => setTimeout(r, 100));
  check(bodies.length > 0 && bodies[0].vm === true, `an agent started now creates in a VM (${JSON.stringify(bodies[0] || null)})`);
  await purge(B.ctx);
  await closeCtx(B.ctx, B.page);

  // ---- C: another browser context: the tile's choice is where it was left ----
  const C = await login(browser, 'admin', 'admin');
  await C.page.route('**/ws/term/env?**', (route) => (route.request().method() === 'GET'
    ? route.fulfill({ json: { exists: false, baseOutdated: false, vm: { available: true, memMiB: 1024, vcpus: 2 } } })
    : route.continue()));
  await openShell(C.page);
  await usePersonalScreen(C.page);
  await openTile(C.page, TILE);
  await fr(C.page, TILE, (f) => f.open('term'));
  await waitSel(C.page, `${sel} .launcher .lvm.on`, { timeout: 15000 });
  check(true, 'a new browser context shows the tile\'s VM choice on');
  await purge(C.ctx);
  await closeCtx(C.ctx, C.page);
  // the window pref would restore this window over the tile in later passes
  const T = await login(browser, 'admin', 'admin');
  await T.ctx.request.delete(`${URL}/api/xbin/prefs/term%3Aapps%3Acrawler`);
  await T.ctx.request.delete(VMPREF); // termRun, next on this tile, starts outside a VM
  await T.ctx.close();
  done();
}

module.exports = { vmToggle };
