// hack/ui-harness/passes/partitionswitch.js — covers PD-44 01§2.4 — a pending
// partition mode switch reaches people inside today's shell, which knows
// nothing of partitions (the shell is scaffold: an upgrade never changes it,
// so what an old shell shows is what this one shows):
//   1. apps/pmode (owned by dev1) holds data (a vault key); its code starts
//      asking for user partitions: the switch is pending;
//   2. its frame shows xbind's own page — the switch, "all data … will be
//      deleted", the tile's partitionNote as text (its markup escaped),
//      who decides — and nothing of the tile; the page runs no script;
//   3. the shell's top banner shows the /alerts partition-switch alert to
//      dev1 (a reader and the tile's manager); sales1, an outsider, has none;
//   4. dev1 keeps the current mode from their session (POST
//      /partitions/mode): the frame shows the tile again, and the banner goes.
// The tile's manifest goes back to unpartitioned at the end (withdrawn).
const path = require('path');
const { URL, fs, login, closeCtx, openShell, usePersonalScreen, openTile, tileFrame, sleep, shot, shotEl, checker } = require('../lib');

const WS = process.env.WS || '';
const TILE = 'apps/pmode';
const NOTE = 'Your notes live here. <b>bold</b> & "quoted"';
const manifest = (partition) => JSON.stringify({ title: 'Partition mode', ...(partition ? { partition: ['user'], partitionNote: NOTE } : {}) });

async function until(fn, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(200);
  }
}

// frameText: the tile frame's visible text and a few facts about its page.
const frameFacts = async (page) => (await tileFrame(page, TILE)).evaluate(() => ({
  text: document.body?.innerText || '',
  switchPage: !!document.querySelector('.card[role=alert]'),
  noteMarkup: !!document.querySelector('.note b'),
  tile: !!document.getElementById('pmode-tile'),
  scripts: document.scripts.length,
})).catch(() => ({ text: '', switchPage: false }));

const banner = (page) => page.locator('bx-shell .alerts .alert', { hasText: `partition mode switch is requested for ${TILE}` });

async function partitionSwitch(browser) {
  const { check, done } = checker('partition-switch');
  const A = await login(browser, 'admin', 'admin');
  const api = (method, p, data) => A.ctx.request.fetch(`${URL}/api/xbin${p}`, { method, data });
  const row = async () => ((await (await api('GET', `/components/${TILE}`)).json().catch(() => ({}))).component || {});
  const dir = path.join(WS, TILE);
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'index.html'), '<!doctype html><h1 id="pmode-tile">the pmode tile itself</h1>\n');
  fs.writeFileSync(path.join(dir, 'xbin.json'), manifest(false));
  let B;
  try {
    await until(async () => (await api('GET', `/components/${TILE}`)).status() === 200, 'the tile registered');
    const own = await api('POST', '/owner', { tile: TILE, to: 'user:dev1' });
    const vault = await api('PUT', `/vault/${TILE}/token`, { value: 'kept-until-a-switch' });
    check(own.status() === 200 && vault.status() === 200, `dev1 owns the tile, and it holds data: a vault key (${own.status()} ${vault.status()})`);

    fs.writeFileSync(path.join(dir, 'xbin.json'), manifest(true));
    const r = await until(async () => { const x = await row(); return x.partition?.state === 'pending' && x; }, 'the switch request pending');
    check(r.partition.request?.user === true && r.partition.user === false, `pending: unpartitioned → user (${JSON.stringify(r.partition)})`);

    B = await login(browser, 'dev1', 'devpass123');
    await openShell(B.page);
    await usePersonalScreen(B.page);
    await openTile(B.page, TILE);
    let f = await until(async () => { const x = await frameFacts(B.page); return x.switchPage && x; }, 'the switch page in the frame');
    check(f.text.includes(`A partition mode switch is requested for ${TILE} (unpartitioned → user)`) &&
      f.text.includes('All data in this tile will be deleted for the switch to happen.'), 'the frame shows xbind\'s switch page');
    check(f.text.includes(`${TILE} says: ${NOTE}`) && !f.noteMarkup, 'the partitionNote shows as text, its markup escaped');
    check(f.text.includes(`bx partition switch ${TILE}`) && f.text.includes('user:dev1'), 'it says who decides, and where');
    check(!f.tile && f.scripts === 0, 'nothing of the tile, and no script, runs in the frame');
    await banner(B.page).first().waitFor({ timeout: 15000 });
    check(await banner(B.page).count() === 1, 'the shell\'s top banner shows the partition-switch alert to a reader');
    await shot(B.page, 'partition-switch-shell');
    await shotEl(B.page, `.card[data-path="${TILE}"]`, 'partition-switch-frame');

    const C = await login(browser, 'sales1', 'salespass123');
    await openShell(C.page);
    await sleep(500);
    check(await banner(C.page).count() === 0, 'an outsider\'s shell shows no alert about the tile');
    await closeCtx(C.ctx, C.page);

    const keep = await B.ctx.request.post(`${URL}/api/xbin/partitions/mode`, { data: { tile: TILE, act: 'keep', from: null, to: { user: true } } });
    check(keep.status() === 200, `dev1 keeps the current mode from their session (${keep.status()})`);
    f = await until(async () => { const x = await frameFacts(B.page); return x.tile && x; }, 'the tile back in its frame');
    check(f.tile && !f.switchPage, 'the frame reloads to the tile itself');
    const v = await (await api('GET', `/vault/${TILE}`)).json().catch(() => ({}));
    check(JSON.stringify(v).includes('token'), `keep deleted nothing: the vault key is still there (${JSON.stringify(v).slice(0, 80)})`);
    await openShell(B.page);
    await sleep(500);
    check(await banner(B.page).count() === 0, 'the banner goes once the request is decided');
  } finally {
    if (B) await closeCtx(B.ctx, B.page);
    fs.writeFileSync(path.join(dir, 'xbin.json'), manifest(false)); // withdrawn
    await closeCtx(A.ctx, A.page);
  }
  done();
}

module.exports = { partitionSwitch };
