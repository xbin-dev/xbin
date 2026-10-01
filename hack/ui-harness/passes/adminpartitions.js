// hack/ui-harness/passes/adminpartitions.js — covers 06§12.3 PD-46 PD-54 I1
// I8 I9 — the admin console's runtime → partitions view
// (workspace-template/tiles/admin/tabs/partitions.js, partition-tile.js), on
// the admin tile's own page as admin (its frame reads as the admin driving
// it, AdminFrameDriver):
//   1. apps/padm (user + global) lists with its people: the admin (a vault
//      key), dev1 and sales1 (a terminal each), and porphan, deleted since —
//      an orphan; dev1 shares their log and binds their own apps/padm-mcp
//      into their partition (a personal bind);
//   2. its view shows each person's metadata row — state, data, the log
//      share, never a content — the personal bind (removed from here), the
//      limits (set here), reviewed code only (refused while the primary
//      isn't protected: the reason shows), and the mode history;
//   3. sales1's partition is reset after the typed "<tile> user:sales1"
//      (a wrong text keeps the button off); dev1's restore says there is no
//      backup of it;
//   4. apps/padm-keep (holds data, asks for user partitions): waiting for a
//      manager — Keep the current mode runs it again, nothing deleted;
//      apps/padm-switch (recorded user, holds data, drops partition): Switch…
//      shows the dry run's counts, the typed path switches it;
//   5. the orphans list purges porphan's partition — exactly the rows it
//      listed, one request each ({tile, partition});
//   6. an xbind without partitions (GET /partitions stubbed 404): the view
//      says so; the old admin scaffold (the one before this view, from git)
//      still renders its sandboxes and components tabs over these tiles.
// Besides: "check untracked files" asks once (a poll after it doesn't); dev1
// (no admin) opening the console reads each tile's state, nobody's rows; a
// running person's instance (the listing stubbed: the harness runs no
// person's instance without --isolate) stops from its row; the sandboxes
// tab labels a person's partition backend, session and terminal disk, and
// the Backup tab's "back up" says a person's partition wasn't archived
// (both stubbed: they need --isolate and a sealed vault).
// The tiles stay (a rerun starts them over: their manifests are rewritten);
// their non-admin people's access goes at the end, so no trust warning
// follows into later passes' screenshots.
const path = require('path');
const { execFileSync } = require('child_process');
const { URL, fs, login, closeCtx, gotoTab, shot, checker, sleep } = require('../lib');
const { termOnce } = require('./partitionlogs'); // a terminal opened and ended: the person's partition

const WS = process.env.WS || '';
const REPO = process.env.REPO || path.join(__dirname, '..', '..', '..');
const T = 'apps/padm', MCP = 'apps/padm-mcp', KEEP = 'apps/padm-keep', SW = 'apps/padm-switch';

async function until(fn, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(200);
  }
}

function write(tile, manifest) {
  const dir = path.join(WS, tile);
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'index.html'), `<!doctype html><h1>${tile}</h1>\n`);
  fs.writeFileSync(path.join(dir, 'xbin.json'), JSON.stringify({ title: tile, ...manifest }));
}


// the pre-F12 admin scaffold, from git: the parent of the commit that added
// tabs/partitions.js (none yet: this checkout's HEAD)
function oldAdmin() {
  const git = (...a) => execFileSync('git', ['-C', REPO, ...a], { encoding: 'utf8', maxBuffer: 64 << 20 });
  const added = git('log', '--diff-filter=A', '--format=%H', '--', 'workspace-template/tiles/admin/tabs/partitions.js').trim().split('\n').pop();
  const base = added ? git('rev-parse', '--short', `${added}^`).trim() : git('rev-parse', '--short', 'HEAD').trim();
  const files = new Map();
  for (const n of git('ls-tree', '-r', '--name-only', base, 'workspace-template/tiles/admin/').trim().split('\n')) {
    if (n.endsWith('.js')) files.set(n.slice('workspace-template/tiles/admin/'.length), git('show', `${base}:${n}`));
  }
  return { base, files };
}

const tileView = (page) => page.locator(`bx-admin-partition-tile`).filter({ has: page.locator(`[data-pt-view="${T}"]`) }).first();

// listLoad: the list tab's own load(), as its 10 s poll runs it
const listLoad = (page) => page.evaluate(async () => {
  const find = (root) => {
    for (const el of root.querySelectorAll('*')) {
      if (el.localName === 'bx-admin-partitions') return el;
      const f = el.shadowRoot && find(el.shadowRoot);
      if (f) return f;
    }
    return null;
  };
  await find(document)?.load();
});

// stubRows: GET /partitions?tile=T answered with dev1's instance running
const isTileListing = (u) => u.pathname === '/api/xbin/partitions' && u.searchParams.get('tile') === T;
async function stubRunning(route) {
  const res = await route.fetch();
  const j = await res.json();
  for (const r of j.partitions || []) {
    if (r.user === 'dev1') Object.assign(r, { running: true, instance: { tile: T, deployment: 'main', partition: 'user:dev1', state: 'running', gen: 1, uptimeSec: 42 } });
  }
  return route.fulfill({ response: res, json: j });
}

async function adminPartitions(browser) {
  const { check, done } = checker('admin-partitions');
  const A = await login(browser, 'admin', 'admin');
  const api = (ctx, method, p, data) => ctx.request.fetch(`${URL}/api/xbin${p}`, { method, data });
  const comp = async (tile) => ((await (await api(A.ctx, 'GET', `/components/${tile}`)).json().catch(() => ({}))).component || {});
  const people = [];
  try {
    // ---- the tiles and the people ----
    write(T, { partition: ['user', 'global'], interfaces: { mcp: { kind: 'http', service: 'mcp', multi: true } } });
    write(MCP, { provides: { mcp: { kind: 'http', service: 'mcp' } } });
    write(KEEP, {});
    write(SW, { partition: ['user'] });
    await until(async () => (await comp(T)).partition?.state === 'partitioned' && (await comp(SW)).partition?.state === 'partitioned'
      && (await comp(MCP)).path && (await comp(KEEP)).path, 'the tiles registered, apps/padm and apps/padm-switch partitioned');
    const seeded = [];
    seeded.push((await api(A.ctx, 'POST', '/owner', { tile: MCP, to: 'user:dev1' })).status());
    for (const who of ['dev1', 'sales1']) seeded.push((await api(A.ctx, 'PUT', '/access', { tile: T, kind: 'user', id: who, level: 'terminal' })).status());
    seeded.push((await api(A.ctx, 'PUT', `/vault/${KEEP}/token`, { value: 'kept until a switch' })).status());
    seeded.push((await api(A.ctx, 'PUT', `/vault/${SW}/token`, { value: 'deleted by the switch' })).status());
    await api(A.ctx, 'DELETE', '/users/porphan').catch(() => {});
    seeded.push((await api(A.ctx, 'POST', '/users', { id: 'porphan', role: 'user', password: 'orphanpass123', tiles: { [T]: 'terminal' } })).status());
    check(seeded.every((s) => s === 200), `the tiles' owners, access and data (${seeded.join(' ')})`);
    const own = await termOnce(A.page, T);
    check(own === 'ok', `the admin opens a terminal on ${T}: their own partition (${own})`);
    for (const [who, pass] of [['dev1', 'devpass123'], ['sales1', 'salespass123'], ['porphan', 'orphanpass123']]) {
      const P = await login(browser, who, pass);
      people.push(P);
      const t = await termOnce(P.page, T);
      check(t === 'ok', `${who} opens a terminal on ${T}: their partition (${t})`);
    }
    const [D] = people;
    const share = await api(D.ctx, 'POST', '/partitions/share-log', { tile: T, days: 3 });
    const bind = await api(D.ctx, 'POST', '/partitions/binds', { requester: T, slot: 'mcp', provider: MCP });
    check(share.status() === 200 && bind.status() === 200, `dev1 shares their log and binds ${MCP} into their partition (${share.status()} ${bind.status()} ${await bind.text()})`);
    await closeCtx(people[2].ctx, people[2].page);
    const del = await api(A.ctx, 'DELETE', '/users/porphan');
    check(del.status() === 200, `porphan is deleted: their partition is an orphan (${del.status()})`);
    // the two mode requests: KEEP asks for user partitions while it holds data, SW drops them
    write(KEEP, { partition: ['user'] });
    write(SW, {});
    await until(async () => (await comp(KEEP)).partition?.state === 'pending' && (await comp(SW)).partition?.state === 'pending', 'both requests pending');

    // ---- the list ----
    await gotoTab(A.page, 'partitions', 'Tiles where each person has their own data');
    const row = (tile) => A.page.locator(`tr[data-pt-tile="${tile}"]`);
    await row(T).waitFor({ timeout: 15000 });
    const cells = async (tile) => (await row(tile).locator('td').allInnerTexts()).map((s) => s.trim());
    const tc = await cells(T);
    check(tc[1] === 'user + global' && tc[2].startsWith('partitioned') && tc[3] === '3' && tc[4] === '0',
      `${T}'s row: mode, state, 3 people (the admin, dev1, sales1), none running here (${JSON.stringify(tc)})`);
    const kc = await cells(KEEP), sc = await cells(SW);
    check(kc[2].includes('waiting for a manager') && kc[2].includes('→ user') && sc[2].includes('→ unpartitioned'),
      `the two requests wait for a manager (${kc[2]} / ${sc[2]})`);
    check(await A.page.locator(`[data-pt-request="${KEEP}"]`).count() === 1, 'a request is spelled out above the orphans');
    check(await A.page.locator('[data-pt-orphan]').count() >= 1 && (await A.page.locator('table[data-pt-orphans]').innerText()).includes('porphan'),
      "porphan's partition is listed as an orphan");
    await shot(A.page, 'admin-partitions-list');

    // untracked files: one confined check per click, never by the poll
    const untrackedAsks = [];
    const onReq = (q) => { if (q.url().includes('/api/xbin/partitions?untracked=1')) untrackedAsks.push(q.url()); };
    A.page.on('request', onReq);
    await A.page.locator('[data-pt-untracked]').click();
    await until(async () => (await A.page.locator('[data-pt-untracked="checked"]').count()) === 1, 'the untracked check answered');
    await listLoad(A.page);
    await listLoad(A.page);
    A.page.off('request', onReq);
    const ubar = await A.page.locator('.pt-bar').filter({ has: A.page.locator('[data-pt-untracked]') }).first().innerText();
    check(untrackedAsks.length === 1 && /check untracked files again/.test(ubar) && /checked /.test(ubar),
      `untracked files: asked once, the polls after it don't ask again (${untrackedAsks.length} asks; ${ubar.replace(/\s+/g, ' ').slice(0, 160)})`);

    // dev1 opens the console: no admin, so each tile's state only
    const adm = await api(A.ctx, 'PUT', '/access', { tile: 'tiles/admin', kind: 'user', id: 'dev1', level: 'read' });
    await gotoTab(D.page, 'partitions', "You aren't a workspace admin");
    await D.page.locator(`tr[data-pt-tile="${T}"]`).click();
    await D.page.locator(`[data-pt-view="${T}"]`).waitFor({ timeout: 15000 });
    const others = await D.page.locator('tr[data-pt-person="admin"], tr[data-pt-person="sales1"], tr[data-pt-orphan]').count();
    const dText = await D.page.locator(`[data-pt-view="${T}"]`).innerText();
    check(adm.status() === 200 && others === 0 && await D.page.locator('[data-pt-orphans], [data-pt-untracked], [data-pt-history]').count() === 0,
      `dev1 (no admin) in the console: the tile's state, nobody's rows, orphans, history or checks (${others} rows; ${dText.replace(/\s+/g, ' ').slice(0, 120)})`);
    await shot(D.page, 'admin-partitions-not-admin');

    // ---- the tile's own view ----
    await A.page.route(isTileListing, stubRunning); // dev1's instance "runs": its stop button shows
    await row(T).click();
    const view = tileView(A.page);
    await view.locator('[data-pt-people]').waitFor({ timeout: 15000 });
    const person = (who) => view.locator(`tr[data-pt-person="${who}"]`);
    const dev1Row = await person('dev1').innerText();
    check(/shared until/.test(dev1Row) && await person('admin').count() === 1 && await person('sales1').count() === 1,
      `people's rows: dev1's log share shows, the admin's and sales1's rows are there (${dev1Row.replace(/\s+/g, ' ')})`);
    check(await view.locator(`tr[data-pt-bind]`).count() === 1 && (await view.locator('table[data-pt-binds]').innerText()).includes(MCP),
      "dev1's personal bind is listed");
    check(await view.locator('[data-pt-history] tr').count() >= 1, 'the mode history shows');
    await shot(A.page, 'admin-partitions-tile');
    // stop dev1's instance from its row
    const stopped = A.page.waitForResponse((r) => r.url().endsWith('/api/xbin/partitions/stop') && r.request().method() === 'POST');
    await person('dev1').locator('[data-pt-stop]').click();
    const sr = await stopped;
    check(sr.status() === 200 && JSON.stringify(sr.request().postDataJSON()) === JSON.stringify({ tile: T, partition: 'user:dev1' }),
      `stop: dev1's instance, from its row (${sr.status()} ${sr.request().postData()})`);
    await A.page.unroute(isTileListing, stubRunning);

    // limits
    await view.locator('[data-pt-limits-edit]').click();
    await view.locator('input[name="maxRunning"]').fill('2');
    await view.locator('[data-pt-limits] button[type="submit"]').click();
    await until(async () => (await view.locator('[data-pt-limits]').innerText()).includes('at once: 2'), 'the limit saved');
    check(true, 'the limits save: 2 people\'s instances at once');
    // reviewed code only: refused while the primary isn't protected (the reason shows)
    await view.locator('[data-pt-reviewed-switch]').click();
    const err = A.page.locator('bx-admin').locator('.err').first();
    await until(async () => (await err.count()) && /protect/i.test(await err.innerText()), 'the refusal names what must be protected');
    check(await view.locator('[data-pt-reviewed="off"]').count() === 1, `reviewed code only stays off: ${(await err.innerText()).slice(0, 140)}`);
    // the personal bind's record goes from here
    await view.locator('[data-pt-unbind]').click();
    await until(async () => (await view.locator('tr[data-pt-bind]').count()) === 0, 'the bind removed');
    const left = await (await api(D.ctx, 'GET', '/partitions/binds')).json();
    check(Array.isArray(left.binds) && left.binds.length === 0, `removed: dev1 has no personal bind left (${JSON.stringify(left)})`);

    // reset sales1's partition: the typed text gates the button
    await person('sales1').locator('[data-pt-reset]').click();
    const ask = view.locator('[data-pt-reset-confirm="sales1"]');
    await ask.waitFor();
    await ask.locator('[data-pt-typed]').fill(`${T} user:dev1`);
    check(await ask.locator('[data-pt-reset-go]').isDisabled(), 'another text keeps Reset off');
    await ask.locator('[data-pt-typed]').fill(`${T} user:sales1`);
    await shot(A.page, 'admin-partitions-reset');
    await ask.locator('[data-pt-reset-go]').click();
    await until(async () => (await person('sales1').count()) === 0, "sales1's row goes");
    check(true, "the reset deletes sales1's partition");
    // restore: no backup of dev1's partition here
    await person('dev1').locator('[data-pt-restore]').click();
    const restoreBox = view.locator('[data-pt-restore-confirm="dev1"]');
    await until(async () => (await restoreBox.count()) || (await err.count()), 'the restore answers');
    const rtext = (await restoreBox.count()) ? await restoreBox.innerText() : await err.innerText();
    check(/No backup of dev1's partition of apps\/padm yet/.test(rtext), `the restore says there is no backup to restore (${rtext.replace(/\s+/g, ' ').slice(0, 160)})`);

    // ---- the mode decisions ----
    await gotoTab(A.page, 'partitions', 'Tiles where each person has their own data');
    await row(KEEP).click();
    const kv = A.page.locator('bx-admin-partition-tile').filter({ has: A.page.locator(`[data-pt-view="${KEEP}"]`) }).first();
    await kv.locator('[data-pt-keep]').click();
    await until(async () => (await comp(KEEP)).partition?.state !== 'pending', 'kept');
    const kept = await comp(KEEP);
    const vault = JSON.stringify(await (await api(A.ctx, 'GET', `/vault/${KEEP}`)).json().catch(() => ({})));
    check(kept.partition?.request?.declined === true && vault.includes('token'), `Keep: ${KEEP} runs unpartitioned again, its vault key kept (${JSON.stringify(kept.partition)})`);
    await row(SW).click();
    const sv = A.page.locator('bx-admin-partition-tile').filter({ has: A.page.locator(`[data-pt-view="${SW}"]`) }).first();
    await sv.locator('[data-pt-switch]').click();
    const sw = sv.locator('[data-pt-switch-confirm]');
    await sw.waitFor();
    const st = await sw.innerText();
    check(st.includes(`Switching ${SW} from user to unpartitioned deletes all data in this tile`) && /vault key/.test(st),
      `Switch… shows the dry run (${st.replace(/\s+/g, ' ').slice(0, 200)})`);
    await shot(A.page, 'admin-partitions-switch');
    check(await sw.locator('[data-pt-switch-go]').isDisabled(), 'the destructive button is off until the path is typed');
    await sw.locator('[data-pt-typed]').fill('apps/wrong');
    check(await sw.locator('[data-pt-switch-go]').isDisabled(), 'another path keeps it off, as Reset does');
    await sw.locator('[data-pt-typed]').fill(SW);
    await sw.locator('[data-pt-switch-go]').click();
    await until(async () => (await comp(SW)).partition?.state !== 'pending', 'switched');
    const swv = JSON.stringify(await (await api(A.ctx, 'GET', `/vault/${SW}`)).json().catch(() => ({})));
    check(!(await comp(SW)).partition?.user && !swv.includes('token'), `Switch: ${SW} runs unpartitioned, its data deleted (${swv.slice(0, 80)})`);

    // ---- purge the orphan ----
    await gotoTab(A.page, 'partitions', 'Tiles where each person has their own data');
    await A.page.locator('tr[data-pt-orphan]').filter({ hasText: 'porphan' }).first().waitFor();
    await A.page.locator('[data-pt-purge-all]').click(); // every orphan listed (a rerun leaves an earlier porphan's too)
    const pc = A.page.locator('[data-pt-purge-confirm]');
    await pc.waitFor();
    const listed = await pc.locator('li').count();
    check((await pc.innerText()).includes('porphan'), 'the purge lists what it deletes first');
    await shot(A.page, 'admin-partitions-purge');
    const purges = [];
    const onPurge = (q) => { if (q.url().endsWith('/api/xbin/partitions/purge')) purges.push(q.postDataJSON()); };
    A.page.on('request', onPurge);
    await A.page.locator('[data-pt-purge-go]').click();
    await until(async () => (await A.page.locator('tr[data-pt-orphan]').filter({ hasText: 'porphan' }).count()) === 0, 'purged');
    A.page.off('request', onPurge);
    check(purges.length >= 1 && purges.length <= listed && purges.every((b) => b && b.tile && b.partition),
      `porphan's orphaned partition is purged: exactly the listed rows, one {tile, partition} each (${listed} listed; ${JSON.stringify(purges).slice(0, 200)})`);
    await shot(A.page, 'admin-partitions-after');

    // ---- the sandboxes view's labels; the Backup tab's partitions (stubbed: --isolate, a sealed vault) ----
    const now = new Date().toISOString();
    const sbxStub = async (r) => {
      const res = await r.fetch();
      const j = await res.json();
      j.sandboxes = [...(j.sandboxes || []),
        { id: 'backend:padm-fixture:g1:dev1', kind: 'backend', tile: T, mode: 'namespace', gen: 1, pid: 1, started: now, uptimeSec: 7, partition: 'user:dev1' },
        { id: 'terminal:padm-fixture:dev1', kind: 'terminal', tile: T, mode: 'namespace', pid: 1, started: now, uptimeSec: 7, personal: true }];
      j.disks = [...(j.disks || []), { kind: 'person-terminal', key: 'padm-fixture-layer', tile: T, path: '/fixture', apparentBytes: 4096, allocatedBytes: 1024, inUse: false }];
      return r.fulfill({ response: res, json: j });
    };
    await A.page.route('**/api/xbin/sandboxes', sbxStub);
    await gotoTab(A.page, 'sandboxes', 'sandboxes');
    await A.page.locator('[data-sbx-partition="user:dev1"]').first().waitFor({ timeout: 15000 });
    const personal = await A.page.locator('[data-sbx-personal]').count(), disk = await A.page.locator('[data-sbx-disk-person]').count();
    check(personal >= 1 && disk >= 1 && (await A.page.locator('[data-sbx-personal]').first().innerText()).includes("a person's session"),
      `the sandboxes view: a person's partition backend (user:dev1), a person's session (${personal}), a person's terminal layer (${disk})`);
    await shot(A.page, 'admin-sandboxes-partitions');
    await A.page.unroute('**/api/xbin/sandboxes', sbxStub);
    const bkStub = (r) => (r.request().method() === 'POST'
      ? r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ok: true, partitions: { archived: 1, failed: ['user:sales1: the archiver refused the upload'] } }) })
      : r.fallback());
    const archStub = async (r) => { // an archiver to back up to (the harness installs none)
      if (r.request().method() !== 'GET') return r.fallback();
      const res = await r.fetch();
      const j = await res.json();
      j.components = [...(j.components || []), { component: 'apps/fixture-archiver', provides: { archive: { kind: 'archive' } } }];
      return r.fulfill({ response: res, json: j });
    };
    await A.page.route('**/api/xbin/backup', bkStub);
    await A.page.route('**/api/xbin/bindings', archStub);
    await gotoTab(A.page, 'backup', 'Default archiver');
    await A.page.locator('tr').filter({ has: A.page.locator('td.mono', { hasText: new RegExp(`^${T}$`) }) }).locator('a.link', { hasText: 'back up' }).click();
    await until(async () => (await err.count()) && /wasn't archived/.test(await err.innerText()), "the back up's partition failure shows");
    check((await err.innerText()).includes(`${T}: a person's partition wasn't archived — user:sales1: the archiver refused the upload`),
      `the Backup tab: a person's partition that wasn't archived is said (${(await err.innerText()).slice(0, 160)})`);
    await A.page.unroute('**/api/xbin/backup', bkStub);
    await A.page.unroute('**/api/xbin/bindings', archStub);

    // ---- an older xbind; the older scaffold ----
    await A.page.route('**/api/xbin/partitions', (r) => r.fulfill({ status: 404, contentType: 'text/plain', body: '404 page not found' }));
    await gotoTab(A.page, 'partitions', "doesn't serve partitioned tiles");
    check(await A.page.locator('[data-partitions="unavailable"]').count() === 1, 'an xbind without partitions: the view says so');
    await A.page.unroute('**/api/xbin/partitions');
    const old = oldAdmin();
    const O = await login(browser, 'admin', 'admin');
    const served = new Set();
    await O.ctx.route((u) => { const p = new globalThis.URL(u.href).pathname; return p.startsWith('/c/tiles/admin/') && old.files.has(p.slice('/c/tiles/admin/'.length)); }, (r) => {
      const name = new globalThis.URL(r.request().url()).pathname.slice('/c/tiles/admin/'.length);
      served.add(name);
      return r.fulfill({ status: 200, contentType: 'text/javascript; charset=utf-8', headers: { 'Cache-Control': 'no-store' }, body: old.files.get(name) });
    });
    for (const [tab, text] of [['sandboxes', 'VM'], ['components', T]]) {
      await gotoTab(O.page, tab, text);
      const e = await O.page.locator('bx-admin').locator('.body > .err').count();
      check(e === 0 && served.has('admin.js'), `the old admin scaffold (${old.base}) renders ${tab} over partitioned tiles, no error`);
    }
    const oldTabs = await O.page.evaluate(() => { const c = customElements.get('bx-admin'); return typeof c?.tabsFlat === 'function' ? c.tabsFlat().map((t) => t.id) : null; });
    check(Array.isArray(oldTabs) && oldTabs.includes('sandboxes') && !oldTabs.includes('partitions'), `the old scaffold has no partitions tab (${JSON.stringify(oldTabs)})`);
    await shot(O.page, 'admin-partitions-old-scaffold');
    await O.ctx.close();
  } finally {
    for (const P of people) await P.ctx.close().catch(() => {});
    await api(A.ctx, 'DELETE', '/users/porphan').catch(() => {});
    await api(A.ctx, 'POST', '/partitions/limits', { tile: T, maxRunning: 0 }).catch(() => {});
    // the non-admin people's access goes: no trust warning (their saves run
    // on everyone's data) follows into later passes' screenshots
    for (const [tile, who] of [[T, 'dev1'], [T, 'sales1'], ['tiles/admin', 'dev1']]) {
      await api(A.ctx, 'PUT', '/access', { tile, kind: 'user', id: who, level: '' }).catch(() => {});
    }
    write(KEEP, {}); // withdrawn (it was kept: unpartitioned)
    await A.ctx.close();
  }
  done();
}

module.exports = { adminPartitions };
