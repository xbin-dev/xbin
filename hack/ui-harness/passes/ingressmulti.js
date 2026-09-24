// hack/ui-harness/passes/ingressmulti.js — one exposed endpoint, many routes
// (D79). The admin tile's ingress → services / expose tab lists every route
// of an endpoint with its own remove button and an add row: publish
// apps/racks.web under one hostname, add a second from the UI, remove the
// first — and the shell's binding list spells routes out (it used to print
// "[object Object]").
const { URL, login, settle, shot, checker } = require('../lib');

async function ingressMulti(browser) {
  const { check, done } = checker('ingress-multi');
  const { ctx, page } = await login(browser, 'admin', 'admin');
  const api = (method, path, body) => ctx.request.fetch(`${URL}/api/xbin${path}`, { method, data: body });
  const racksRoutes = async () => {
    const d = await (await api('GET', '/ingress')).json();
    return ((d.exposes || []).find((e) => e.component === 'apps/racks' && e.slot === 'web')?.routes || []).map((r) => `${r.source} ${r.host}`);
  };
  await api('DELETE', '/bindings', { component: 'apps/racks', slot: 'web' }); // a rerun starts clean
  const r = await api('POST', '/bindings', { component: 'apps/racks', slot: 'web', provider: 'runtime', host: 'a.racks.test' });
  check(r.ok(), `published apps/racks.web as a.racks.test (${r.status()})`);

  await page.goto(`${URL}/c/tiles/admin/#expose`);
  await page.reload();
  const tab = page.locator('bx-admin-ingress');
  await tab.locator('tr.ing-route').first().waitFor({ timeout: 10000 });
  const racksAdd = tab.locator('tr.ing-add[data-ep="apps/racks.web"]');
  await racksAdd.locator('select').selectOption('runtime');
  await racksAdd.locator('input.ing-host').fill('b.racks.test');
  await racksAdd.locator('button', { hasText: 'add' }).click();
  await page.waitForTimeout(800);
  check(JSON.stringify(await racksRoutes()) === JSON.stringify(['runtime a.racks.test', 'runtime b.racks.test']),
    `the add row put a second hostname on the endpoint (${JSON.stringify(await racksRoutes())})`);
  const rows = tab.locator('tr.ing-route[data-ep="apps/racks.web"]');
  check(await rows.count() === 2, `both routes are listed, one row each (${await rows.count()})`);
  await settle(page);
  await shot(page, 'ingress-multi-routes');
  await rows.filter({ hasText: 'a.racks.test' }).locator('button', { hasText: 'remove' }).click();
  await page.waitForTimeout(800);
  check(JSON.stringify(await racksRoutes()) === JSON.stringify(['runtime b.racks.test']), `remove dropped just that route (${JSON.stringify(await racksRoutes())})`);

  // the shell's binding list: routes spelled out
  await page.goto(`${URL}/`);
  const bb = page.locator('bx-bindings');
  await bb.waitFor({ timeout: 15000 });
  const more = bb.locator('a', { hasText: /show all bindings|interface binding/ }).first();
  if (await more.count()) await more.click();
  await page.waitForTimeout(300);
  const text = await bb.evaluate((el) => el.shadowRoot?.textContent || '');
  check(!text.includes('[object Object]') && text.includes('runtime (b.racks.test)'), `the shell's binding list shows the route, not "[object Object]"`);

  await api('DELETE', '/bindings', { component: 'apps/racks', slot: 'web' });
  await ctx.close();
  done();
}

module.exports = { ingressMulti };
