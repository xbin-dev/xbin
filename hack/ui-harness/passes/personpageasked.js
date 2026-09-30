// hack/ui-harness/passes/personpageasked.js — covers PD-13 and 06 §12
// (F11's asked consents, node-tested only until now; work pack I1): the
// person page lists an edge xbind refused for want of the person's consent
// and allows it. <x> (partitioned, user) declares a kv resource `notes`;
// <a> (partitioned) holds an approved grant on it; both are dev1's. With
// the workspace policy partitionConsent on, dev1's frame of <a> reads
// <x>'s notes — dev1's own partition of them — and is refused; xbind
// records the ask. On /xbin/partitions dev1 sees it — the tiles, when it
// was refused — with Allow; Allow records the consent (the consents table
// lists it, the asked card goes) and the read goes through. Taking it back
// refuses the read again and the answered ask doesn't come back. No
// isolation needed: the reach is a resource grant, no backend runs. Fresh
// tile names each run (xbind asks at most once a day per edge); the policy
// goes back off and the consent goes at the end.
const path = require('path');
const { URL, fs, login, closeCtx, openShell, usePersonalScreen, openTile, tileFrame, settle, sleep, shotEl, checker } = require('../lib');

const WS = process.env.WS || '';
const RUN = Date.now().toString(36).slice(-5);
const X = `apps/pa${RUN}x`, A = `apps/pa${RUN}a`;
const RES = `res:${X}/notes`;
const PAGE = 'bx-partitions-page';

async function until(fn, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(200);
  }
}

function writeTile(tile, files) {
  const dir = path.join(WS, tile);
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'index.html'), `<!doctype html><h1>${tile}</h1>\n`);
  for (const [n, v] of Object.entries(files)) fs.writeFileSync(path.join(dir, n), JSON.stringify(v));
}

// read(page) → dev1's frame of <a> reads <x>'s notes: {status, text}
const read = async (page) => (await tileFrame(page, A)).evaluate(async (u) => {
  const r = await window.xbin.fetch(u);
  return { status: r.status, text: (await r.text()).slice(0, 300) };
}, `/api/xbin/kv/${RES}/?prefix=`);

async function personPageAsked(browser) {
  const { check, done } = checker('person-page-asked');
  const AD = await login(browser, 'admin', 'admin');
  const api = (ctx, method, p, data) => ctx.request.fetch(`${URL}/api/xbin${p}`, { method, data });
  const policy = async (on) => (await api(AD.ctx, 'PUT', '/workspace-policies', { partitionConsent: on })).status();
  let D;
  try {
    writeTile(X, { 'xbin.json': { title: 'notes, per person', partition: ['user'] }, 'scope.json': { resources: { notes: { type: 'kv' } } } });
    writeTile(A, { 'xbin.json': { title: `${A}: reads ${X}`, partition: ['user'], uses: [{ target: RES, role: 'reader' }] } });
    await until(async () => {
      for (const t of [X, A]) {
        const r = await api(AD.ctx, 'GET', `/components/${t}`);
        if (r.status() !== 200 || (await r.json().catch(() => ({}))).component?.partition?.state !== 'partitioned') return false;
      }
      return true;
    }, 'both tiles registered, partitioned');
    const set = [];
    for (const t of [X, A]) set.push((await api(AD.ctx, 'POST', '/owner', { tile: t, to: 'user:dev1' })).status());
    set.push((await api(AD.ctx, 'POST', '/grants', { from: A, target: RES, role: 'reader' })).status());
    check(set.every((s) => s === 200), `${X} and ${A} are dev1's; ${A} holds an approved grant on ${RES} (${set.join(' ')})`);
    check(await policy(true) === 200, 'the admin turns partitionConsent on');

    // ---- the refused reach: xbind records the ask ----
    D = await login(browser, 'dev1', 'devpass123');
    await openShell(D.page);
    await usePersonalScreen(D.page);
    await openTile(D.page, A);
    let r = await until(async () => { const x = await read(D.page).catch(() => null); return x?.status === 403 && x; }, `${A}'s read refused`);
    check(r.text.includes(`dev1 hasn't let ${A} use their ${X} data (they allow it at /xbin/partitions)`), `the read is refused, naming the page (${r.text})`);
    const asked = await (await api(D.ctx, 'GET', '/partitions/consents')).json().catch(() => ({}));
    check((asked.asked ?? []).some((a) => a.from === A && a.to === X), `xbind lists the ask to dev1 (${JSON.stringify(asked.asked)})`);

    // ---- the page: the asked card, Allow ----
    const pg = await D.ctx.newPage();
    await pg.goto(`${URL}/xbin/partitions`);
    const card = pg.locator(`${PAGE} section#consents [data-asked="${A}→${X}"]`);
    await card.waitFor({ timeout: 15000 });
    const words = (await card.innerText()).replace(/\s+/g, ' ');
    check(words.includes(`${A} asks for your ${X} data`) && /refused .*asked once a day at most/.test(words), `the asked card names both tiles and when (${words.slice(0, 160)})`);
    const allow = card.locator('button[data-act="allow-asked"]');
    check(await allow.count() === 1 && (await allow.innerText()) === 'Allow' && !(await allow.isDisabled()), 'with an Allow button, enabled');
    await shotEl(pg, `${PAGE} section#consents`, 'person-page-asked');
    await allow.click();
    const row = pg.locator(`${PAGE} section#consents tr[data-consent="${A}→${X}"]`);
    await row.waitFor({ timeout: 10000 });
    await until(async () => (await card.count()) === 0 || (await card.locator('button[data-act="allow-asked"]').count()) === 0, 'the asked card answered');
    const got = await (await api(D.ctx, 'GET', '/partitions/consents')).json().catch(() => ({}));
    check((got.consents ?? []).some((c) => c.from === A && c.to === X && c.via), `dev1's consent is recorded, with how it was given (${JSON.stringify(got.consents)})`);
    check(!(got.asked ?? []).some((a) => a.from === A && a.to === X), `the ask is answered: no longer listed (${JSON.stringify(got.asked)})`);
    await shotEl(pg, `${PAGE} section#consents`, 'person-page-asked-allowed');
    r = await until(async () => { const x = await read(D.page); return x.status === 200 && x; }, `${A}'s read after Allow`);
    check(r.text.includes('keys'), `the read goes through (${r.status} ${r.text})`);

    // ---- taken back: refused again, and the answered ask stays answered ----
    // (the new row offers Take back at once — no reload: the asked card
    // keeps its own UI key, W5-wire)
    await row.locator('button[data-act="revoke"]').waitFor({ timeout: 15000 });
    check(!(await row.innerText()).includes('Allowed:'), 'the new row offers Take back, not the Allow\'s answer');
    await row.locator('button[data-act="revoke"]').click();
    await until(async () => (await row.count()) === 0, 'the consent taken back');
    r = await until(async () => { const x = await read(D.page); return x.status === 403 && x; }, `${A}'s read refused again`);
    check(r.text.includes(`dev1 hasn't let ${A} use their ${X} data`), 'taken back: the read is refused again');
    await pg.reload();
    await pg.locator(`${PAGE} section#consents`).waitFor({ timeout: 15000 });
    await settle(pg);
    check(await pg.locator(`${PAGE} section#consents [data-asked="${A}→${X}"]`).count() === 0, 'the answered ask doesn\'t come back (xbind asks again another day)');
    await pg.close();
  } finally {
    await policy(false).catch(() => {});
    if (D) {
      await api(D.ctx, 'DELETE', '/partitions/consents', { from: A, to: X }).catch(() => {});
      await closeCtx(D.ctx, D.page).catch(() => {});
    }
    await AD.ctx.close();
  }
  done();
}

module.exports = { personPageAsked };
