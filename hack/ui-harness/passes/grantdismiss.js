// hack/ui-harness/passes/grantdismiss.js — covers D188: the shell's grant
// strip — a person dismisses grant requests and "interfaces to bind" rows,
// and there is no "N grants active" line. Two tiles of org:devs:
// apps/gd-y asks for apps/gd-x's reader role and has an unbound net slot.
// As the admin:
//   1. the strip shows the request and the row, each with a quiet Dismiss,
//      and no "grants active" line (shot: grantdismiss-strip-<theme>);
//   2. Dismiss hides both, a "dismissed (1) · show" line in each element
//      takes their place (shot: grantdismiss-restore-<theme>), the organisations
//      badge drops by two, and the shell's `dismissed` pref holds the keys
//      (from|target|role, component|slot|kind);
//   3. a reload, and a second context (another device), keep them hidden;
//   4. dev1, another admin of org:devs, still sees both;
//   5. "show" in the second context brings them back, and the first follows
//      live (the prefs event), badge included;
//   6. a request whose key changes (another role) shows again, and the old
//      key's dismissal is pruned.
// The two tiles and the pref are removed at the end.
const path = require('path');
const { URL, fs, login, closeCtx, openShell, usePersonalScreen, settle, shotEl, checker, sleep, THEME } = require('../lib');

const WS = process.env.WS || '';
const X = 'apps/gd-x', Y = 'apps/gd-y';
const manifestY = (role) => JSON.stringify({ title: 'gd y', uses: [{ target: X, role }], interfaces: { net: { kind: 'net' } } });
const GKEY = (role) => `${Y}|${X}|${role}`, BKEY = `${Y}|net|net`;

async function until(fn, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(150);
  }
}

function writeTiles(role) {
  for (const [t, man] of [[X, JSON.stringify({ title: 'gd x', expose: { roles: { reader: 'read gd-x', writer: 'write gd-x' } } })], [Y, manifestY(role)]]) {
    const dir = path.join(WS, t);
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(path.join(dir, 'index.html'), `<!doctype html><h1>${t}</h1>\n`);
    fs.writeFileSync(path.join(dir, 'xbin.json'), man + '\n');
  }
}

// what one page's strip shows of apps/gd-y
const strip = (page) => page.evaluate((y) => {
  const sh = document.querySelector('bx-shell')?.shadowRoot;
  const g = sh?.querySelector('bx-grants')?.shadowRoot, b = sh?.querySelector('bx-bindings')?.shadowRoot;
  const txt = (r) => r?.textContent.replace(/\s+/g, ' ') ?? '';
  return {
    grant: [...(g?.querySelectorAll('.plate') ?? [])].some((p) => p.textContent.includes(y)),
    grantDismiss: [...(g?.querySelectorAll('.plate') ?? [])].some((p) => p.textContent.includes(y) && p.querySelector('[data-dismiss]')),
    binding: [...(b?.querySelectorAll('.row') ?? [])].some((r) => r.textContent.includes(y)),
    bindingDismiss: [...(b?.querySelectorAll('.row') ?? [])].some((r) => r.textContent.includes(y) && r.querySelector('[data-dismiss]')),
    grantsRestore: txt(g?.querySelector('[data-restore]')),
    bindingsRestore: txt(b?.querySelector('[data-restore]')),
    activeLine: /grants? active/.test(txt(g)),
    badge: Number(sh?.querySelector('bx-side')?.shadowRoot?.querySelector('.orgbtn .n')?.textContent ?? 0),
  };
}, Y);

async function grantDismiss(browser) {
  const { check, done } = checker(`grantdismiss-${THEME}`);
  check(!!WS, 'WS is set (run.sh exports it)');
  const A = await login(browser, 'admin', 'admin');
  const api = (method, p, data) => A.ctx.request.fetch(`${URL}/api/xbin${p}`, {
    method, headers: { 'Content-Type': 'application/json' }, ...(data === undefined ? {} : { data: JSON.stringify(data) }),
  });
  const stored = async () => { const r = await api('GET', '/prefs/dismissed'); return r.status() === 200 ? r.json() : null; };
  await api('DELETE', '/prefs/dismissed');
  writeTiles('reader');
  let A2 = null, D = null;
  try {
    await until(async () => (await (await api('GET', '/components')).json()).filter((c) => [X, Y].includes(c.path)).length === 2, 'the tiles registered');
    for (const t of [X, Y]) await api('POST', '/owner', { tile: t, to: 'org:devs' });

    // 1. the strip
    await openShell(A.page);
    await usePersonalScreen(A.page);
    let s = await until(async () => { const v = await strip(A.page); return v.grant && v.binding ? v : null; }, 'the request and the row on the strip');
    check(s.grantDismiss && s.bindingDismiss, `the request and the row each have a Dismiss (${JSON.stringify(s)})`);
    check(!s.activeLine, 'no "N grants active" line');
    const badge0 = s.badge;
    check(badge0 >= 2, `the organisations badge counts them (${badge0})`);
    await shotEl(A.page, 'bx-shell .grants', `grantdismiss-strip-${THEME}`);

    // 2. dismiss both
    await A.page.locator('bx-grants .plate', { hasText: Y }).locator('[data-dismiss]').click();
    await A.page.locator('bx-bindings .row', { hasText: Y }).locator('[data-dismiss]').click();
    s = await until(async () => { const v = await strip(A.page); return !v.grant && !v.binding && v.badge === badge0 - 2 ? v : null; }, 'both hidden, the badge down by two')
      .catch(async (e) => { check(false, `${e.message}: ${JSON.stringify(await strip(A.page))}`); return strip(A.page); });
    check(!s.grant && !s.binding, 'Dismiss hides the request and the row');
    check(s.grantsRestore === 'dismissed (1) · show' && s.bindingsRestore === 'dismissed (1) · show', `a "dismissed (1) · show" line in each (${JSON.stringify([s.grantsRestore, s.bindingsRestore])})`);
    check(s.badge === badge0 - 2, `the badge drops by two (${badge0} → ${s.badge})`);
    const d = await until(async () => { const v = await stored(); return v?.grants?.[GKEY('reader')] && v?.bindings?.[BKEY] ? v : null; }, 'the pref saved').catch(() => null);
    check(!!d, `the shell's "dismissed" pref holds ${GKEY('reader')} and ${BKEY} (${JSON.stringify(await stored())})`);
    await shotEl(A.page, 'bx-shell .grants', `grantdismiss-restore-${THEME}`);

    // 3. a reload, another device
    await A.page.reload();
    await A.page.waitForSelector('bx-shell');
    s = await until(async () => { const v = await strip(A.page); return v.grantsRestore && v.bindingsRestore ? v : null; }, 'the strip after a reload');
    check(!s.grant && !s.binding, 'after a reload they stay hidden');
    A2 = await login(browser, 'admin', 'admin');
    await openShell(A2.page);
    s = await until(async () => { const v = await strip(A2.page); return v.grantsRestore && v.bindingsRestore ? v : null; }, 'the strip on another device');
    check(!s.grant && !s.binding && s.badge === badge0 - 2, `another device: hidden too, the same badge (${s.badge})`);

    // 4. another admin
    D = await login(browser, 'dev1', 'devpass123');
    await openShell(D.page);
    s = await until(async () => { const v = await strip(D.page); return v.grant && v.binding ? v : null; }, "dev1's strip").catch(async () => strip(D.page));
    check(s.grant && s.binding && !s.grantsRestore, `dev1 (a devs admin) still sees both (${JSON.stringify(s)})`);

    // 5. restore on the other device; the first follows live
    await A2.page.locator('bx-grants [data-restore] a').click();
    await A2.page.locator('bx-bindings [data-restore] a').click();
    s = await until(async () => { const v = await strip(A.page); return v.grant && v.binding && v.badge === badge0 ? v : null; }, 'the first context follows the restore')
      .catch(async () => strip(A.page));
    check(s.grant && s.binding && !s.grantsRestore && !s.bindingsRestore, `"show" brings them back, and another open shell follows live (${JSON.stringify(s)})`);
    check(s.badge === badge0, `…the badge too (${s.badge})`);

    // 6. a changed request shows again; the old dismissal goes
    await A.page.locator('bx-grants .plate', { hasText: Y }).locator('[data-dismiss]').click();
    await until(async () => (await stored())?.grants?.[GKEY('reader')], 'dismissed again');
    writeTiles('writer');
    const writer = await until(async () => (await A.page.locator('bx-grants .plate', { hasText: Y }).filter({ hasText: 'writer' }).count()) > 0, 'the changed request').catch(() => false);
    check(writer, 'a request for another role (writer) shows again');
    // (the last dismissal pruned: the key itself is deleted)
    const pruned = await until(async () => { const v = await stored(); return !v?.grants?.[GKEY('reader')] ? { v } : null; }, 'pruned').catch(() => null);
    check(!!pruned, `the old request's dismissal is pruned (${JSON.stringify(await stored())})`);
  } finally {
    await api('DELETE', '/prefs/dismissed').catch(() => {});
    for (const t of [X, Y]) {
      const dir = path.join(WS, t);
      if (WS && path.isAbsolute(WS) && dir.startsWith(path.join(WS, 'apps', 'gd-'))) fs.rmSync(dir, { recursive: true, force: true });
    }
    if (D) await closeCtx(D.ctx, D.page);
    if (A2) await closeCtx(A2.ctx, A2.page);
    await settle(A.page).catch(() => {});
    await closeCtx(A.ctx, A.page);
  }
  done();
}

module.exports = { grantDismiss };
