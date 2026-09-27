// hack/ui-harness/passes/sandboxnet.js — a sandbox manager's network
// classes (sandbox-net slots, plans/tile-sandbox-runtime.md §4) in the two
// wiring UIs:
//
//   - the server's bindings answer carries sandboxNetOptions for the tile
//     (no host, no provider tiles) and its pending class rows claim no
//     org/personal default;
//   - admin → binding → wiring renders each class as a net-style row
//     (tr[data-kind=sandbox-net]): unbound reads "no network", host is not
//     offered, and picking internet binds it;
//   - the shell's tile popover renders the same rows.
//
// The manager tile is created here (a rerun reuses it) and both classes end
// bound to none, so no pending row is left for later passes.
const path = require('path');
const { URL, fs, sleep, login, closeCtx, settle, sh, waitSel, gotoTab, openShell, shot, dumpSelects, checker } = require('../lib');

const TILE = 'apps/sbxmgr';
const CLASSES = ['internet', 'lab'];

async function sandboxNet(browser) {
  const { check, done } = checker('sandbox-net');
  const A = await login(browser, 'admin', 'admin', { viewport: { width: 1500, height: 1000 } });
  const aj = async (method, p, data) => {
    const r = await A.ctx.request.fetch(`${URL}/api/xbin${p}`, { method, data });
    let body = null;
    try { body = await r.json(); } catch { /* not json */ }
    return { status: r.status(), body };
  };
  const bindings = async () => (await aj('GET', '/bindings')).body ?? {};
  const isClass = (b) => ((b.components ?? []).find((c) => c.component === TILE)?.interfaces?.internet?.kind === 'sandbox-net');

  // the manager: two classes, nothing else
  if (!(await bindings()).components?.some((c) => c.component === TILE)) {
    const r = await aj('POST', '/create', { path: TILE, runtime: 'static', title: 'sandbox manager' });
    check(r.status < 300, `create ${TILE} (${r.status} ${JSON.stringify(r.body).slice(0, 120)})`);
  }
  fs.writeFileSync(path.join(process.env.WS, TILE, 'xbin.json'),
    JSON.stringify({ interfaces: Object.fromEntries(CLASSES.map((c) => [c, { kind: 'sandbox-net' }])) }, null, 2) + '\n');
  let b = {};
  for (let i = 0; i < 100 && !isClass(b); i++) { await sleep(100); b = await bindings(); }
  check(isClass(b), `${TILE}'s sandbox-net slots are listed`);
  for (const slot of CLASSES) await aj('DELETE', '/bindings', { component: TILE, slot }); // a rerun starts unbound
  b = await bindings();

  const opts = b.sandboxNetOptions?.[TILE] ?? [];
  check(opts.length > 0 && opts.some((o) => o.id === 'internet') && !opts.some((o) => o.id === 'host'),
    `sandboxNetOptions: internet, no host (${opts.map((o) => o.id).join(',')})`);
  const pend = (b.pending ?? []).filter((p) => p.component === TILE);
  check(pend.length === 2 && pend.every((p) => p.kind === 'sandbox-net' && !p.default && !p.options.some((o) => o.id === 'host')),
    `pending class rows, no default, no host (${JSON.stringify(pend.map((p) => [p.slot, p.kind, p.default ?? '']))})`);

  // admin → binding → wiring
  const { page } = A;
  await gotoTab(page, 'wiring', 'Each component');
  const rows = page.locator('bx-admin-binding tr[data-kind="sandbox-net"]', { has: page.locator('td.mono', { hasText: TILE }) });
  await rows.first().waitFor({ timeout: 10000 });
  check(await rows.count() === 2, `wiring: a row per class (${await rows.count()})`);
  const sel = rows.first().locator('select');
  const o = await sel.evaluate((s) => [...s.options].map((x) => [x.value, x.disabled, x.textContent.trim()]));
  check(!o.some(([v]) => v === 'host') && o.some(([v, dis]) => v === 'internet' && !dis) && /no network/.test(o[0][2]),
    `wiring picker: "${o[0][2]}" first, internet offered, no host (${o.map(([v]) => v || '""').join(',')})`);
  await dumpSelects(page, 'sandbox-net-wiring-selects', 'bx-admin-binding tr[data-kind="sandbox-net"] select');
  await shot(page, 'sandbox-net-wiring');
  await sel.selectOption('internet');
  let bound = '';
  for (let i = 0; i < 50 && bound !== 'internet'; i++) { await sleep(100); bound = [].concat((await bindings()).bindings?.[TILE]?.internet ?? [])[0]; }
  check(bound === 'internet', `picking internet binds the class (${bound})`);
  await settle(page);

  // the shell's tile popover
  await openShell(page);
  await sh(page, (t, p) => t.openAdminWindow(p, 'interfaces'), TILE);
  const popSel = '.admin-pop bx-tile-admin details[data-sec="interfaces"] tr[data-kind="sandbox-net"] select';
  await waitSel(page, popSel);
  const pop = await page.locator(popSel).evaluateAll((sels) => sels.map((s) => ({ value: s.value, opts: [...s.options].map((x) => x.value) })));
  check(pop.length === 2 && pop.every((p) => !p.opts.includes('host')) && pop.some((p) => p.value === 'internet'),
    `popover: two class rows, no host, internet bound (${JSON.stringify(pop)})`);
  await shot(page, 'sandbox-net-popover', { fullPage: false });
  await sh(page, (t) => t.closeAdminWindow());

  // tidy: both classes decided, nothing pending
  for (const slot of CLASSES) {
    const r = await aj('POST', '/bindings', { component: TILE, slot, provider: 'none' });
    check(r.status === 200, `tidy: ${slot} → none (${r.status})`);
  }
  await closeCtx(A.ctx, page);
  done();
}

module.exports = { sandboxNet };
