// hack/ui-harness/passes/personalbinds.js — covers PD-16 PD-54 — bind types
// on a partitioned tile (plans/partitions/05 §3, docs/partitions.md §Bind
// types):
//   1. apps/pbind (user + global) requests a multi http slot mcp; the admin
//      binds apps/pbind-g there globally; dev1 owns apps/dev1-mcp;
//   2. an admin can't make a personal bind of dev1's tile; dev1, with their
//      own session, binds apps/dev1-mcp into their own partition;
//   3. dev1's document of apps/pbind lists the global bind and their
//      personal one (personal: true); the admin's lists the global bind
//      alone;
//   4. the admin console's wiring view labels apps/pbind's slot "global" and
//      lists dev1's personal bind; its ✕ removes it.
const path = require('path');
const { URL, fs, login, closeCtx, gotoTab, shot, checker, sleep } = require('../lib');

const WS = process.env.WS || '';
const TILE = 'apps/pbind', MINE = 'apps/dev1-mcp', SHARED = 'apps/pbind-g';

async function until(fn, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(200);
  }
}

// ifaces: the xbin-interfaces meta of tile's document as page's login sees it
const ifaces = async (page, tile) => {
  await page.goto(`${URL}/c/${tile}/`);
  return page.evaluate(() => JSON.parse(document.querySelector('meta[name="xbin-interfaces"]')?.content || '{}'));
};

async function personalBinds(browser) {
  const { check, done } = checker('personal-binds');
  const A = await login(browser, 'admin', 'admin');
  const api = (ctx, method, p, data) => ctx.request.fetch(`${URL}/api/xbin${p}`, { method, data });
  const write = (tile, manifest) => {
    const dir = path.join(WS, tile);
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(path.join(dir, 'index.html'), `<!doctype html><h1>${tile}</h1>\n`);
    fs.writeFileSync(path.join(dir, 'xbin.json'), JSON.stringify(manifest));
  };
  write(TILE, { title: 'Personal binds', partition: ['user', 'global'], interfaces: { mcp: { kind: 'http', service: 'mcp', multi: true } } });
  write(MINE, { title: "dev1's mcp", provides: { mcp: { kind: 'http', service: 'mcp' } } });
  write(SHARED, { title: 'Shared mcp', provides: { mcp: { kind: 'http', service: 'mcp' } } });
  let B;
  try {
    const row = await until(async () => {
      const r = await api(A.ctx, 'GET', `/components/${TILE}`);
      const c = r.status() === 200 ? (await r.json()).component : null;
      return c?.partition?.state === 'partitioned' ? c.partition : null;
    }, 'apps/pbind recorded partitioned');
    check(row.user && row.global, `${TILE} is partitioned at once, user + global (${JSON.stringify(row)})`);
    await until(async () => (await api(A.ctx, 'GET', `/components/${MINE}`)).status() === 200 && (await api(A.ctx, 'GET', `/components/${SHARED}`)).status() === 200, 'the providers registered');
    const own = await api(A.ctx, 'POST', '/owner', { tile: MINE, to: 'user:dev1' });
    const read = await api(A.ctx, 'PUT', '/access', { tile: TILE, kind: 'user', id: 'dev1', level: 'read' });
    check(own.status() === 200 && read.status() === 200, `dev1 owns ${MINE} and reads ${TILE} (${own.status()} ${read.status()})`);
    const g = await api(A.ctx, 'POST', '/bindings', { component: TILE, slot: 'mcp', providers: [SHARED] });
    check(g.status() === 200, `the admin binds ${SHARED} globally (${g.status()} ${await g.text()})`);
    const body = { requester: TILE, slot: 'mcp', provider: MINE };
    const adm = await api(A.ctx, 'POST', '/partitions/binds', body);
    check(adm.status() === 403, `an admin can't make a personal bind of dev1's tile (${adm.status()})`);

    B = await login(browser, 'dev1', 'devpass123');
    const mine = await api(B.ctx, 'POST', '/partitions/binds', body);
    check(mine.status() === 200, `dev1 binds ${MINE} into their own partition (${mine.status()} ${await mine.text()})`);
    const eps = (m) => (m.mcp?.endpoints ?? []).map((e) => `${e.provider}${e.personal ? ' (personal)' : ''}`);
    const dev1 = eps(await ifaces(B.page, TILE));
    check(dev1.length === 2 && dev1[0] === SHARED && dev1[1] === `${MINE} (personal)`, `dev1's document: the global bind, then theirs (${JSON.stringify(dev1)})`);
    const admin = eps(await ifaces(A.page, TILE));
    check(admin.length === 1 && admin[0] === SHARED, `the admin's document: the global bind alone (${JSON.stringify(admin)})`);

    await gotoTab(A.page, 'wiring', 'Each component');
    const tr = A.page.locator('tr', { has: A.page.locator('td.mono', { hasText: TILE }) }).first();
    const pill = tr.locator('span.pill', { hasText: `personal · dev1 → ${MINE}` });
    await pill.waitFor({ timeout: 10000 });
    check(await tr.locator('span.pill', { hasText: /^global$/ }).count() === 1, 'the slot is labelled global');
    check(await pill.count() === 1, "dev1's personal bind is listed, labelled personal");
    await shot(A.page, 'admin-wiring-personal');
    // the remove control is a drawn xmark (D184), found by its name
    await pill.locator('button[aria-label="remove this personal bind"]').click();
    await until(async () => (await pill.count()) === 0, 'the remove control removes it');
    const left = await (await api(B.ctx, 'GET', '/partitions/binds')).json();
    check(Array.isArray(left.binds) && left.binds.length === 0, `removed: dev1 has none (${JSON.stringify(left)})`);
  } finally {
    await api(A.ctx, 'DELETE', '/bindings', { component: TILE, slot: 'mcp' }).catch(() => {});
    if (B) await closeCtx(B.ctx, B.page);
    await closeCtx(A.ctx, A.page);
  }
  done();
}

module.exports = { personalBinds };
