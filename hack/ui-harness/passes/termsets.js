// hack/ui-harness/passes/termsets.js — named network sets on terminals and as
// a tile binding (D65). Terminal half: the scope menu on apps/crawler
// (org:devs, devs-net attached) lists every workspace set for the admin and
// only the attached one for dev1; picking one restarts the terminal under it
// (bx-terminal's `net` attribute is written from the SESSION FRAME, so it
// proves the server honoured the pick). Binding half through the API: the
// coverage rule and the workspace-admin gate. The two halves disagree on
// purpose — the admin may OPEN a terminal under infra-net on a devs tile (a
// human act, outside the tile ceiling) but may not BIND the tile to it
// (uncovered) — that asymmetry is documented in auth.md.
const { URL, login, closeCtx, settle, waitSel, sh, fr, openShell, usePersonalScreen, openTile, shot, dumpSelects, checker } = require('../lib');

async function termSets(browser) {
  const { check, done } = checker('term-sets');
  const picker = (page) => page.locator('bx-frame[src="apps/crawler"] select.scope').first();
  const pickerState = (page) => picker(page).evaluate((sel) => ({
    value: sel.value,
    ids: [...sel.options].map((o) => o.value),
    titles: Object.fromEntries([...sel.options].map((o) => [o.value, o.title])),
  }));
  const jget = async (ctx, p) => (await ctx.request.get(`${URL}/api/xbin${p}`)).json();

  // ---- admin: every workspace set, pick one, the session follows ----
  {
    const { ctx, page } = await login(browser, 'admin', 'admin');
    await openShell(page);
    await usePersonalScreen(page);
    await openTile(page, 'apps/crawler');
    await page.locator('.card[data-path="apps/crawler"] button.term').click();
    // the picker renders the classic three until the session frame lands (org appears then)
    await page.locator('bx-frame[src="apps/crawler"] select.scope option[value="org"]').first().waitFor({ state: 'attached', timeout: 20000 });
    let s = await pickerState(page);
    check(s.value === 'org', `admin: the default on the org tile stays org (now "${s.value}")`);
    for (const id of ['set:devs-net', 'set:infra-net', 'set:sales-net']) check(s.ids.includes(id), `admin: menu lists ${id} (${s.ids.join(',')})`);
    check(!s.ids.includes('set:vpn-only'), 'admin: a provider-only set is not a scope');
    check(/host networking/.test(s.titles['set:infra-net'] ?? ''), `admin: the host set says so in its tooltip (${s.titles['set:infra-net']})`);
    check(s.ids.indexOf('set:devs-net') === s.ids.indexOf('org') + 1, 'admin: sets sit right after org');
    await picker(page).selectOption('set:infra-net');
    await waitSel(page, 'bx-frame[src="apps/crawler"] bx-terminal[net="set:infra-net"]', { state: 'attached', timeout: 20000 });
    await page.locator('bx-frame[src="apps/crawler"] select.scope option[value="org"]').first().waitFor({ state: 'attached', timeout: 20000 });
    s = await pickerState(page);
    check(s.value === 'set:infra-net', `admin: the picker shows the set after the restart ("${s.value}")`);
    await settle(page);
    await shot(page, 'term-admin-crawler-set', { fullPage: false });
    await dumpSelects(page, 'term-admin-crawler-set-selects', 'bx-frame select.scope');
    await picker(page).selectOption('org');
    await waitSel(page, 'bx-frame[src="apps/crawler"] bx-terminal[net="org"]', { state: 'attached', timeout: 20000 });
    await fr(page, 'apps/crawler', (f) => f?.closeTerminal());
    await settle(page);

    // Binding half (the API): covered ok, uncovered 400, provider-only 400,
    // delete-while-bound 409, the option rows say why.
    const bind = (provider) => ctx.request.post(`${URL}/api/xbin/bindings`, { data: { component: 'apps/crawler', slot: 'net', provider } });
    let r = await bind('set:devs-net');
    check(r.status() === 200, `admin: binds the covered set on the org tile (${r.status()} ${await r.text()})`);
    r = await bind('set:infra-net');
    check(r.status() === 400 && /not covered/.test(await r.text()), `admin: an uncovered set is refused at bind — though pickable in a terminal above (${r.status()})`);
    r = await bind('set:vpn-only');
    check(r.status() === 400 && /provider rules only/.test(await r.text()), `admin: a provider-only set is not bindable (${r.status()})`);
    r = await ctx.request.delete(`${URL}/api/xbin/net-sets/devs-net`);
    check(r.status() === 409 && /apps\/crawler/.test(await r.text()), `admin: deleting a bound set is 409 naming the tile (${r.status()})`);
    const opts = (await jget(ctx, '/bindings')).netOptions?.['apps/crawler'] ?? [];
    const infra = opts.find((o) => o.id === 'set:infra-net');
    check(!!infra?.blocked && /not covered/.test(infra.label), `admin: the uncovered set's option row is blocked (${JSON.stringify(infra)})`);
    check(opts.some((o) => o.id === 'set:devs-net' && !o.blocked), 'admin: the covered set\'s option row is open');
    const st = (await jget(ctx, '/tile-status?component=apps/crawler')).net ?? {};
    check(st.netRef === 'set:devs-net' && /network set devs-net/.test(st.netSource ?? ''), `admin: tile-status names the set (${JSON.stringify(st)})`);
    // the popover on a personal tile (no ceiling) offers the sets to the admin
    await sh(page, (t, p) => t.openAdminWindow(p, 'interfaces'), 'apps/dev1-notes');
    await waitSel(page, '.admin-pop bx-tile-admin details[data-sec="interfaces"] select');
    const pop = await page.locator('.admin-pop bx-tile-admin details[data-sec="interfaces"] select').first().evaluate((sel) => [...sel.options].map((o) => [o.value, o.disabled]));
    check(pop.some(([v, dis]) => v === 'set:sales-net' && !dis), `admin: the tile popover offers set:sales-net on a personal tile (${JSON.stringify(pop)})`);
    check(pop.some(([v, dis]) => v === 'set:vpn-only' && dis), 'admin: …and greys the provider-only one');
    await sh(page, (t) => t.closeAdminWindow());
    // tidy: the seed leaves crawler unbound (default org)
    r = await ctx.request.delete(`${URL}/api/xbin/bindings`, { data: { component: 'apps/crawler', slot: 'net' } });
    check(r.ok(), `tidy: unbound apps/crawler (${r.status()})`);
    await closeCtx(ctx, page);
  }

  // ---- dev1 (org admin of devs): the attached set only; binding is not theirs ----
  {
    const { ctx, page } = await login(browser, 'dev1', 'devpass123');
    const tn = await jget(ctx, '/term-net?tile=apps/crawler');
    const ids = (tn.scopes ?? []).map((s) => s.id);
    check(ids.includes('set:devs-net') && !ids.includes('set:infra-net') && !ids.includes('set:sales-net'), `dev1: only the attached set is a scope on apps/crawler (${ids.join(',')})`);
    check(tn.default === 'org', `dev1: default stays org (${tn.default})`);
    const own = await jget(ctx, '/term-net?tile=apps/dev1-notes');
    check(!(own.scopes ?? []).some((s) => s.id.startsWith('set:')), `dev1: no sets on their personal tile (${(own.scopes ?? []).map((s) => s.id).join(',')})`);
    const r = await ctx.request.post(`${URL}/api/xbin/bindings`, { data: { component: 'apps/crawler', slot: 'net', provider: 'set:devs-net' } });
    check(r.status() === 403 && /workspace-admin/.test(await r.text()), `dev1: binding a set is refused as a workspace-admin act (${r.status()})`);
    const opts = (await jget(ctx, '/bindings')).netOptions?.['apps/crawler'] ?? [];
    const devs = opts.find((o) => o.id === 'set:devs-net');
    check(!!devs?.blocked && /workspace admins only/.test(devs.label), `dev1: the set row is blocked for an org admin (${JSON.stringify(devs)})`);
    await closeCtx(ctx, page);
  }
  done();
}

module.exports = { termSets };
