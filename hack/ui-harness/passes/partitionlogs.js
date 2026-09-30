// hack/ui-harness/passes/partitionlogs.js — covers I5 06§5 — the partition
// switcher of a tile's logs panel (web/bx-logs.js, web/logs-partition.js) in
// the shell's terminal window:
//   1. apps/plogs (user + global): the admin and dev1 each hold a partition,
//      each with its own backend log, and the global instance has its own;
//      dev1 shares their log;
//   2. the admin's panel shows their own partition's log ("your partition"),
//      and switches to the global instance's and to dev1's shared one — each
//      the log it names, nothing of the others;
//   3. dev1's panel offers their own and the global instance's, not the
//      admin's (a person sees no one else's), and shows their own;
//   4. an unpartitioned tile's panel is as it always was: no switcher, the
//      plain badge.
// The logs are written straight into the workspace (the harness runs no
// person's instance without --isolate); the tile stays.
const path = require('path');
const crypto = require('crypto');
const { URL, fs, login, closeCtx, openShell, usePersonalScreen, openTile, sh, fr, settle, shotEl, checker, sleep } = require('../lib');

const WS = process.env.WS || '';
const T = 'apps/plogs', PLAIN = 'apps/pinned';

async function until(fn, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(200);
  }
}

// util.CompKey and util.TileKey, as xbind names a tile's log files
const sha = (s) => crypto.createHash('sha256').update(s).digest('hex');
const compKey = (p) => `${p.replace(/\//g, '~').slice(0, 24)}-${sha(p).slice(0, 8)}`;
const tileKey = (p) => sha(`xbin-tile-key-v1\u0000${p}`).slice(0, 32);
function writeLog(rel, text) {
  const f = path.join(WS, rel);
  fs.mkdirSync(path.dirname(f), { recursive: true });
  fs.writeFileSync(f, text);
}

// the panel's facts: bx-logs' test surface in tile's terminal window
const logs = (page, tile) => sh(page, (t, src) => {
  const a = t.frameFor(src)?.renderRoot?.querySelector('bx-logs')?.testApi?.();
  return a ? { choices: a.choices.map((c) => c.value), labels: a.choices.map((c) => c.label), partition: a.partition, def: a.defaultPartition, switcher: a.switcher, badge: a.badge, text: a.text() } : null;
}, tile);

// termOnce: a terminal session on tile from page's login, opened and ended —
// on a partitioned tile it makes the person's partition (its record)
const termOnce = (page, tile) => page.evaluate(async (tile) => {
  const ws = new WebSocket(`${location.origin.replace(/^http/, 'ws')}/ws/term?cwd=${encodeURIComponent(tile)}`);
  let id = '';
  const got = await new Promise((res) => {
    const t = setTimeout(() => res('timeout'), 8000);
    ws.onmessage = (m) => {
      try { id = JSON.parse(typeof m.data === 'string' ? m.data : '')?.id || id; } catch { /* output */ }
      if (id) { clearTimeout(t); res('ok'); }
    };
    ws.onclose = (e) => { clearTimeout(t); res(`closed ${e.code}`); };
  });
  ws.close();
  if (id) await fetch(`/api/xbin/term/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' });
  return got;
}, tile);

// winShot: the tile's terminal window, its card scrolled into view first
// (the window follows its tile through the canvas's scroll)
async function winShot(page, name) {
  await page.locator(`.card[data-path="${T}"]`).first().scrollIntoViewIfNeeded().catch(() => {});
  await settle(page);
  await sleep(300);
  await shotEl(page, `bx-frame[src="${T}"] .pop`, name);
}

async function openLogs(page, tile) {
  await openShell(page);
  await usePersonalScreen(page);
  await openTile(page, tile);
  await fr(page, tile, (f) => { f.open('logs'); return true; });
  await settle(page);
}

async function partitionLogs(browser) {
  const { check, done } = checker('partition-logs');
  const A = await login(browser, 'admin', 'admin');
  const api = (ctx, method, p, data) => ctx.request.fetch(`${URL}/api/xbin${p}`, { method, data });
  const dir = path.join(WS, T);
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'index.html'), '<!doctype html><h1>partition logs</h1>\n');
  fs.writeFileSync(path.join(dir, 'xbin.json'), JSON.stringify({ title: 'Partition logs', partition: ['user', 'global'] }));
  let D;
  try {
    await until(async () => ((await (await api(A.ctx, 'GET', `/components/${T}`)).json().catch(() => ({}))).component?.partition?.state === 'partitioned'), 'apps/plogs partitioned');
    const acc = await api(A.ctx, 'PUT', '/access', { tile: T, kind: 'user', id: 'dev1', level: 'terminal' });
    const mine = await termOnce(A.page, T);
    D = await login(browser, 'dev1', 'devpass123');
    const term = await termOnce(D.page, T);
    check(acc.status() === 200 && mine === 'ok' && term === 'ok', `the admin and dev1 each hold a partition: a terminal each (${acc.status()} ${mine} ${term})`);
    const rows = (await (await api(A.ctx, 'GET', `/partitions?tile=${T}`)).json()).partitions || [];
    const pk = Object.fromEntries(rows.map((r) => [r.user, r.partitionId]));
    check(pk.admin && pk.dev1, `their partition ids (${JSON.stringify(pk)})`);
    writeLog(`.xbin/log/${compKey(T)}.log`, 'GLOBAL-INSTANCE-LINE the global instance logged this\n');
    writeLog(`.xbin/partition/${tileKey(T)}/main/${pk.admin}/backend.log`, 'ADMIN-PARTITION-LINE the admin\'s partition logged this\n');
    writeLog(`.xbin/partition/${tileKey(T)}/main/${pk.dev1}/backend.log`, 'DEV1-PARTITION-LINE dev1\'s partition logged this\n');
    writeLog(`.xbin/log/${compKey(PLAIN)}.log`, 'PLAIN-TILE-LINE an unpartitioned tile logged this\n');
    const share = await api(D.ctx, 'POST', '/partitions/share-log', { tile: T, days: 2 });
    check(share.status() === 200, `dev1 shares their log (${share.status()})`);

    // the admin: their own, global's, dev1's shared one
    await openLogs(A.page, T);
    let v = await until(async () => { const x = await logs(A.page, T); return x?.choices.length === 3 && x.text.includes('ADMIN-PARTITION-LINE') && x; }, "the admin's panel with its switcher");
    check(v.def === 'user:admin' && v.badge === 'read-only logs' && v.switcher && JSON.stringify(v.choices) === JSON.stringify(['', 'global', 'user:dev1']),
      `the admin's panel: their own partition's log, and global's and dev1's to pick (${JSON.stringify({ ...v, text: undefined })})`);
    check(!v.text.includes('GLOBAL-INSTANCE-LINE') && !v.text.includes('DEV1-PARTITION-LINE'), 'only their own log shows');
    await winShot(A.page, 'partition-logs-own');
    const sel = A.page.locator(`bx-frame[src="${T}"] bx-logs select.part`);
    await sel.selectOption('global');
    v = await until(async () => { const x = await logs(A.page, T); return x?.text.includes('GLOBAL-INSTANCE-LINE') && x; }, "global's log");
    check(v.partition === 'global' && !v.text.includes('ADMIN-PARTITION-LINE'), `global picked: the global instance's log alone (${v.partition})`);
    await winShot(A.page, 'partition-logs-global');
    await sel.selectOption('user:dev1');
    v = await until(async () => { const x = await logs(A.page, T); return x?.text.includes('DEV1-PARTITION-LINE') && x; }, "dev1's shared log");
    check(v.partition === 'user:dev1' && !v.text.includes('GLOBAL-INSTANCE-LINE'), `dev1's shared log (${v.partition})`);
    await winShot(A.page, 'partition-logs-shared');

    // dev1: their own and global's — never the admin's
    await openLogs(D.page, T);
    v = await until(async () => { const x = await logs(D.page, T); return x?.choices.length >= 2 && x.text.includes('DEV1-PARTITION-LINE') && x; }, "dev1's panel");
    check(JSON.stringify(v.choices) === JSON.stringify(['', 'global']) && v.def === 'user:dev1' && !v.text.includes('ADMIN-PARTITION-LINE'),
      `dev1's panel: their own and global's, no one else's (${JSON.stringify({ ...v, text: undefined })})`);
    await winShot(D.page, 'partition-logs-person');

    // an unpartitioned tile: as it always was
    await openLogs(A.page, PLAIN);
    v = await until(async () => { const x = await logs(A.page, PLAIN); return x?.text.includes('PLAIN-TILE-LINE') && x; }, "the unpartitioned tile's log");
    await sleep(300);
    v = await logs(A.page, PLAIN);
    check(v.choices.length === 0 && !v.switcher && v.badge === 'read-only logs' && v.def === '', `an unpartitioned tile: no switcher, the plain badge (${JSON.stringify({ ...v, text: undefined })})`);
  } finally {
    if (D) await closeCtx(D.ctx, D.page);
    await closeCtx(A.ctx, A.page);
  }
  done();
}

module.exports = { partitionLogs, termOnce };
