// hack/ui-harness/passes/personpage.js — covers PD-47 06§12.1 PD-44 PD-13
// PD-16 PD-55 — the partitions page, /xbin/partitions, as a person and a
// tile manager (dev1) uses it:
//   0. it is xbind's page as it ships (the bytes of web/partitions.html, no
//      transform), top-level only (frame-ancestors 'none', X-Frame-Options
//      DENY): framed, nothing of it renders;
//   1. a switch decision: apps/pp-switch (dev1's; recorded user, holds a
//      vault key; its code drops partition) is pending — the page offers
//      Switch and delete all data…, shows the dry run's counts and keep
//      list, refuses a wrong path, and switches on the tile's path: the tile
//      runs unpartitioned; apps/pp-keep (dev1's; holds a vault key; its code
//      asks for user partitions) — Keep the current mode: declined, nothing
//      deleted, and the page still offers Switch… for it;
//   2. consent with the workspace policy partitionConsent on: dev1 lets
//      apps/pp-docs use their apps/pp-notes data, sees it listed, takes it
//      back;
//   3. a personal bind: dev1 binds their own apps/pp-mcp into their
//      partition of apps/pp-notes (slot mcp) and removes it;
//   4. a credential confirmation, with credentialResetConfirm on: an admin
//      mints dev1 a sign-in link; the page shows it held, with the notice;
//      dev1 allows it;
//      another link is refused;
//   5. dev1's own partition of apps/pp-notes (a terminal of theirs made
//      it): its row, the trust panel, a log share on and off, "restore
//      from a backup" (none kept here), and a reset after the typed
//      confirmation.
// The policies go back off, the manifests to what a rerun starts from.
const path = require('path');
const { URL, fs, login, closeCtx, openShell, usePersonalScreen, openTile, closeTile, sleep, shot, shotEl, checker } = require('../lib');

const WS = process.env.WS || '';
const REPO = process.env.REPO || path.join(__dirname, '..', '..', '..');
const NOTES = 'apps/pp-notes', DOCS = 'apps/pp-docs', MCP = 'apps/pp-mcp', SW = 'apps/pp-switch', KEEP = 'apps/pp-keep';
const PAGE = 'bx-partitions-page';

async function until(fn, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(200);
  }
}

function writeTile(tile, manifest) {
  const dir = path.join(WS, tile);
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'index.html'), `<!doctype html><h1>${tile}</h1>\n`);
  fs.writeFileSync(path.join(dir, 'xbin.json'), JSON.stringify({ title: tile, ...manifest }));
}

async function personPage(browser) {
  const { check, done } = checker('person-page');
  const A = await login(browser, 'admin', 'admin');
  const api = (ctx, method, p, data) => ctx.request.fetch(`${URL}/api/xbin${p}`, { method, data });
  const comp = async (tile) => ((await (await api(A.ctx, 'GET', `/components/${tile}`)).json().catch(() => ({}))).component || {});
  const mcp = { mcp: { kind: 'http', service: 'mcp', multi: true } };
  let B;
  try {
    // ---- the tiles ----
    writeTile(NOTES, { partition: ['user', 'global'], interfaces: mcp });
    writeTile(DOCS, { partition: ['user'] });
    writeTile(MCP, { provides: { mcp: { kind: 'http', service: 'mcp' } } });
    writeTile(SW, { partition: ['user'] });
    writeTile(KEEP, {});
    for (const t of [NOTES, DOCS, MCP, SW, KEEP]) await until(async () => (await api(A.ctx, 'GET', `/components/${t}`)).status() === 200, `${t} registered`);
    const own = await Promise.all([NOTES, MCP, SW, KEEP].map((t) => api(A.ctx, 'POST', '/owner', { tile: t, to: 'user:dev1' })));
    const read = await api(A.ctx, 'PUT', '/access', { tile: DOCS, kind: 'user', id: 'dev1', level: 'read' });
    check(own.every((r) => r.status() === 200) && read.status() === 200, `dev1 owns ${NOTES}, ${MCP}, ${SW}, ${KEEP} and reads ${DOCS}`);
    for (const t of [NOTES, DOCS, SW]) await until(async () => (await comp(t)).partition?.state === 'partitioned', `${t} partitioned`);
    const vault = [await api(A.ctx, 'PUT', `/vault/${SW}/token`, { value: 'doomed' }), await api(A.ctx, 'PUT', `/vault/${KEEP}/token`, { value: 'kept' })];
    check(vault.every((r) => r.status() === 200), `${SW} and ${KEEP} hold data: a vault key each (${vault.map((r) => r.status())})`);
    writeTile(SW, {});
    writeTile(KEEP, { partition: ['user'], partitionNote: 'Each person keeps their own list here.' });
    await until(async () => (await comp(SW)).partition?.state === 'pending', `${SW} pending`);
    await until(async () => (await comp(KEEP)).partition?.state === 'pending', `${KEEP} pending`);
    const pol = await api(A.ctx, 'PUT', '/workspace-policies', { partitionConsent: true, credentialResetConfirm: true });
    check(pol.status() === 200, `the admin turns both workspace policies on (${pol.status()})`);

    B = await login(browser, 'dev1', 'devpass123');
    const dev1 = (method, p, data) => api(B.ctx, method, p, data);
    // dev1 gets a partition of NOTES: a terminal of theirs opening on it
    // writes its record (no backend runs here: the harness isn't isolated)
    const term = await api(A.ctx, 'PUT', '/access', { tile: NOTES, kind: 'user', id: 'dev1', level: 'terminal' });
    await B.page.goto(`${URL}/`);
    const sid = await B.page.evaluate((tile) => new Promise((res) => {
      const ws = new WebSocket(`ws://${location.host}/ws/term?cwd=${encodeURIComponent(tile)}&gpu=none&api=0`);
      ws.onmessage = (m) => {
        let c = null;
        try { c = typeof m.data === 'string' ? JSON.parse(m.data) : null; } catch { c = null; }
        if (c?.op === 'session') { ws.close(); res(c.id); }
      };
      ws.onerror = () => res('');
      setTimeout(() => res(''), 10000);
    }), NOTES);
    if (sid) await B.ctx.request.fetch(`${URL}/ws/term?session=${encodeURIComponent(sid)}`, { method: 'DELETE' });
    const row0 = await until(async () => ((await (await dev1('GET', `/partitions?tile=${NOTES}`)).json()).partitions ?? []).find((r) => r.user === 'dev1'), 'dev1\'s partition record');
    check(term.status() === 200 && !!sid && row0?.partition === 'user:dev1', `dev1's terminal on ${NOTES} gives them a partition there (${term.status()}, ${sid ? 'a session' : 'no session'})`);
    const inv = await api(A.ctx, 'POST', '/users/dev1/invite', {});
    const invBody = await inv.json().catch(() => ({}));
    check(inv.status() === 200 && invBody.held === true, `an admin's sign-in link for dev1 is held: dev1 holds partitions (${inv.status()} ${JSON.stringify(invBody)})`);

    // ---- 0. xbind's page, as it ships, top-level only ----
    const raw = await B.ctx.request.get(`${URL}/xbin/partitions`, { headers: { Accept: 'text/html' } });
    const h = raw.headers();
    check(raw.status() === 200 && (await raw.text()) === fs.readFileSync(path.join(REPO, 'web', 'partitions.html'), 'utf8'),
      'GET /xbin/partitions answers web/partitions.html byte for byte (no HTML transform)');
    check(h['x-frame-options'] === 'DENY' && /frame-ancestors 'none'/.test(h['content-security-policy'] || ''),
      `it refuses to be framed (${h['x-frame-options']}; ${h['content-security-policy']})`);
    check((await B.ctx.request.get(`${URL}/vendor/partitions.html`)).status() === 404, '/vendor/ has no copy of the page');
    const p = B.page;
    await p.goto(`${URL}/`);
    const framed = await p.evaluate(async () => {
      const f = document.createElement('iframe');
      f.src = '/xbin/partitions';
      document.body.append(f);
      await new Promise((r) => { f.onload = r; setTimeout(r, 4000); });
      let doc = null;
      try { doc = f.contentDocument; } catch { doc = null; }
      const shown = !!doc?.querySelector('bx-partitions-page')?.shadowRoot?.querySelector('section');
      f.remove();
      return { shown };
    });
    check(!framed.shown, 'framed, the page shows nothing of dev1\'s');

    await p.goto(`${URL}/xbin/partitions`);
    await p.locator(`${PAGE} section#partitions`).waitFor({ timeout: 15000 });
    await p.locator(`${PAGE} [data-decide="${SW}"]`).waitFor({ timeout: 10000 });
    await shot(p, 'person-page');

    // ---- 1. a switch decision (and a keep) ----
    const sw = p.locator(`${PAGE} [data-decide="${SW}"]`);
    check(/user → unpartitioned/.test(await sw.innerText()) && /all data in this tile/.test(await sw.innerText()),
      'the pending switch says R → Q and what it deletes');
    await sw.locator('button[data-act="switch"]').click();
    await sw.locator('[data-confirm="switch"] [data-counts]').waitFor({ timeout: 10000 });
    const conf = await sw.locator('[data-confirm="switch"]').innerText();
    check(/1 vault key/.test(conf), `the dry run's counts: ${conf.split('\n').find((l) => l.startsWith('It deletes')) || conf}`);
    check(await sw.locator('[data-keeps] li').count() >= 4, 'and what the switch keeps');
    await shotEl(p, `${PAGE} [data-decide="${SW}"]`, 'person-page-switch');
    await sw.locator('input[name=confirm]').fill('apps/wrong');
    await sw.locator('button[data-act="switch-go"]').click();
    check(/Type the tile's path exactly/.test(await sw.locator('.err').innerText()), 'a wrong path is refused on the page');
    check((await comp(SW)).partition?.state === 'pending', 'and nothing was switched');
    await sw.locator('input[name=confirm]').fill(SW);
    await sw.locator('button[data-act="switch-go"]').click();
    await until(async () => (await comp(SW)).partition?.state !== 'pending' && true, `${SW} switched`);
    await p.locator(`${PAGE} [data-decided="${SW}"]`).waitFor({ timeout: 10000 });
    check(/Switched apps\/pp-switch to unpartitioned: all data in this tile deleted\./.test(await p.locator(`${PAGE} [data-decided="${SW}"]`).innerText()),
      'the page says what the switch deleted, and the tile leaves the list');
    const swRow = await comp(SW);
    check(!swRow.partition?.user && !swRow.partition?.request, `${SW} switched: unpartitioned (${JSON.stringify(swRow.partition ?? null)})`);
    check(!JSON.stringify(await (await api(A.ctx, 'GET', `/vault/${SW}`)).json().catch(() => ({}))).includes('token'), 'its vault key went with it');

    // the shell's paused card links here (F14's overlay: "details…")
    await openShell(p);
    await usePersonalScreen(p);
    await openTile(p, KEEP);
    const more = p.locator(`bx-canvas .card[data-path="${KEEP}"] .pover a.pmore`);
    await more.waitFor({ timeout: 15000 });
    check(await more.getAttribute('href') === '/xbin/partitions' && await more.getAttribute('target') === '_blank',
      'the shell\'s paused card links the partitions page (details…)');
    await shotEl(p, `bx-canvas .card[data-path="${KEEP}"] .pbox`, 'person-page-shell-card');
    await closeTile(p, KEEP);
    await p.goto(`${URL}/xbin/partitions`);
    const kp = p.locator(`${PAGE} [data-decide="${KEEP}"]`);
    await kp.waitFor({ timeout: 15000 });
    check(/Each person keeps their own list here\./.test(await kp.innerText()), 'the tile\'s partitionNote shows, attributed to it');
    await kp.locator('button[data-act="keep"]').click();
    await until(async () => (await comp(KEEP)).partition?.state === 'unpartitioned' && true, `${KEEP} kept`);
    check(JSON.stringify(await (await api(A.ctx, 'GET', `/vault/${KEEP}`)).json().catch(() => ({}))).includes('token'), 'Keep deletes nothing: the vault key stays');
    await until(async () => /kept unpartitioned/.test(await kp.innerText()), 'the declined card');
    check(await kp.locator('button[data-act="switch"]').count() === 1 && await kp.locator('button[data-act="keep"]').count() === 0,
      'a declined request still offers Switch…, and no Keep');

    // ---- 2. consent, the policy on ----
    const cs = p.locator(`${PAGE} section#consents`);
    check(/asks you before another partitioned tile uses your data/.test(await cs.innerText()), 'the consents section says the workspace asks');
    await cs.locator('[data-consent-new] select[name=from]').selectOption(DOCS);
    await cs.locator('[data-consent-new] select[name=to]').selectOption(NOTES);
    await cs.locator('button[data-act="consent"]').click();
    const row = cs.locator(`tr[data-consent="${DOCS}→${NOTES}"]`);
    await row.waitFor({ timeout: 10000 });
    const got = await (await dev1('GET', '/partitions/consents')).json();
    check(got.consents?.some((c) => c.from === DOCS && c.to === NOTES), `dev1's consent is recorded (${JSON.stringify(got.consents)})`);
    await shotEl(p, `${PAGE} section#consents`, 'person-page-consent');
    await row.locator('button[data-act="revoke"]').click();
    await until(async () => (await row.count()) === 0, 'the consent taken back');
    check(!((await (await dev1('GET', '/partitions/consents')).json()).consents ?? []).length, 'taken back: none left');

    // ---- 3. a personal bind ----
    const bs = p.locator(`${PAGE} section#binds`);
    await bs.locator('select[name=provider]').selectOption(MCP);
    await bs.locator('select[name=requester]').selectOption(NOTES);
    await bs.locator('input[name=slot]').fill('mcp');
    await bs.locator('button[data-act="bind"]').click();
    const brow = bs.locator('tr[data-bind]');
    await brow.first().waitFor({ timeout: 10000 });
    const binds = (await (await dev1('GET', '/partitions/binds')).json()).binds ?? [];
    check(binds.length === 1 && binds[0].requester === NOTES && binds[0].provider === MCP && binds[0].slot === 'mcp', `the bind is dev1's (${JSON.stringify(binds)})`);
    await shotEl(p, `${PAGE} section#binds`, 'person-page-bind');
    await brow.first().locator('button[data-act="unbind"]').click();
    await until(async () => (await brow.count()) === 0, 'the bind removed');
    check(!((await (await dev1('GET', '/partitions/binds')).json()).binds ?? []).length, 'removed: dev1 has none');

    // ---- 4. a credential confirmation ----
    const cr = p.locator(`${PAGE} section#credentials [data-cred]`);
    await cr.first().waitFor({ timeout: 10000 });
    check(/A sign-in link for your account, made by admin/.test(await cr.first().innerText()), 'the held sign-in link shows, with who made it');
    check(/sign-in link for your account was created by admin/.test(await p.locator(`${PAGE} section#notices`).innerText()), 'and its notice');
    await shotEl(p, `${PAGE} section#credentials`, 'person-page-credential');
    await cr.first().locator('button[data-act="allow"]').click();
    await until(async () => /Allowed: it works now\./.test(await p.locator(`${PAGE} section#credentials`).innerText().catch(() => '')) ||
      !((await (await dev1('GET', '/partitions')).json()).credentials ?? []).length, 'allowed');
    check(!((await (await dev1('GET', '/partitions')).json()).credentials ?? []).length, 'allowed: nothing waits any more');
    // another link: refused this time (the page asks once more first)
    const inv2 = await (await api(A.ctx, 'POST', '/users/dev1/invite', {})).json().catch(() => ({}));
    check(inv2.held === true, 'a second sign-in link is held too');
    const refuse = p.locator(`${PAGE} section#credentials [data-cred] button[data-act="refuse"]`);
    await refuse.first().waitFor({ timeout: 10000 });
    await refuse.first().click();
    await p.locator(`${PAGE} section#credentials button[data-act="refuse-yes"]`).click();
    await until(async () => !((await (await dev1('GET', '/partitions')).json()).credentials ?? []).length, 'refused');
    const redeem = await B.ctx.request.fetch(`${URL}/api/xbin/invite/check`, { method: 'POST', data: { invite: inv2.invite } });
    check(redeem.status() !== 200, `refused: the link no longer signs anyone in (POST /invite/check: ${redeem.status()})`);

    // ---- 5. dev1's own partition of apps/pp-notes ----
    const card = p.locator(`${PAGE} section#partitions [data-tile="${NOTES}"]`);
    await until(async () => /stopped — the next request starts it/.test(await card.innerText()), 'dev1\'s row');
    check(/private: only you read it/.test(await card.innerText()), 'the log is private');
    await card.locator('button[data-act="share"]').click();
    await until(async () => /shared with the tile's managers and admins until/.test(await card.innerText()), 'the log shared');
    await card.locator('button[data-act="unshare"]').click();
    await until(async () => /private: only you read it/.test(await card.innerText()), 'the share ended');
    check(true, 'dev1 shares their partition\'s log and stops');
    if (!(await card.locator('details.trust').evaluate((d) => d.open))) await card.locator('details.trust summary').click();
    check(/its code/.test(await card.innerText()) && /global binds/.test(await card.innerText()), 'the trust panel: who changes the code, the global binds');
    // restore: this workspace keeps no backups, and the page says so
    await card.locator('button[data-act="backups"]').click();
    const rb = card.locator('[data-confirm="restore"]');
    await rb.waitFor({ timeout: 10000 });
    check(/No archiver keeps this workspace's backups|holds no backup of your partition|Version/.test(await rb.innerText()),
      `"restore from a backup" answers: ${(await rb.innerText()).split('\n')[0]}`);
    await shot(p, 'person-page-after');
    // reset: the typed confirmation, then the partition goes
    await card.locator('button[data-act="reset"]').click();
    const rs = card.locator('[data-confirm="reset"]');
    await rs.locator('input[name=confirm]').fill(NOTES);
    await rs.locator('button[data-act="reset-go"]').click();
    check(/Type apps\/pp-notes user:dev1 exactly/.test(await rs.innerText()), 'a reset needs "<tile> user:<you>" typed');
    await rs.locator('input[name=confirm]').fill(`${NOTES} user:dev1`);
    await rs.locator('button[data-act="reset-go"]').click();
    await until(async () => /no partition here yet/.test(await card.innerText()), 'dev1\'s partition reset');
    check(!((await (await dev1('GET', `/partitions?tile=${NOTES}`)).json()).partitions ?? []).length, 'reset: dev1 holds no partition of it now');

    // the admin's page: their own view, a switch still offered on the declined tile
    await A.page.goto(`${URL}/xbin/partitions`);
    await A.page.locator(`${PAGE} [data-decide="${KEEP}"]`).waitFor({ timeout: 10000 });
    check(/An admin's bind is always a global bind/.test(await A.page.locator(`${PAGE} section#binds`).innerText()), 'an admin is offered no personal bind');
    await shot(A.page, 'person-page-admin');
  } finally {
    await api(A.ctx, 'PUT', '/workspace-policies', { partitionConsent: false, credentialResetConfirm: false }).catch(() => {});
    writeTile(KEEP, {}); // withdrawn: a rerun starts from an unpartitioned tile
    writeTile(SW, { partition: ['user'] });
    if (B) await closeCtx(B.ctx, B.page);
    await closeCtx(A.ctx, A.page);
  }
  done();
}

module.exports = { personPage };
