// hack/ui-harness/passes/partitionswitch.js — covers PD-44 01§2.4 and
// 10-compat's old-shell rule — a pending partition mode switch reaches
// people inside a shell that knows nothing of partitions. The shell is
// scaffold (an xbind upgrade never changes a workspace's copy), so the
// browsers here run the shell as it was before partitioned tiles: every
// /c/shell/*.js it loads is served from git — the parent of the commit that
// added workspace-template/shell/partition-mode.js — over the harness's
// dev overlay (the current shell is partitionMark's):
//   1. apps/pmode (owned by dev1) holds data (a vault key); its code starts
//      asking for user partitions: the switch is pending;
//   2. its frame shows xbind's own page — the switch, "all data … will be
//      deleted", the tile's partitionNote as text (its markup escaped),
//      who decides — and nothing of the tile; the page runs no script; the
//      old shell draws no overlay and no marker over it;
//   3. the shell's top banner shows the /alerts partition-switch alert to
//      dev1 (a reader and the tile's manager); sales1, an outsider, has none;
//   4. dev1 keeps the current mode from their session (POST
//      /partitions/mode): the frame shows the tile again, and the banner goes.
// The page is on the Base Two tokens (D184): it opts in, loads xbind's
// /vendor/theme.css under its sandbox CSP (no violation), and follows the
// system's light or dark live, inside the old shell as well.
// The tile's manifest goes back to unpartitioned at the end (withdrawn).
const path = require('path');
const { execFileSync } = require('child_process');
const { URL, fs, login, closeCtx, openShell, usePersonalScreen, openTile, tileFrame, sleep, shot, shotEl, checker } = require('../lib');

const WS = process.env.WS || '';
const REPO = process.env.REPO || path.join(__dirname, '..', '..', '..');
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

// oldShell() → {base, files: name → source}: the shell's scripts from
// before partitioned tiles, read from git. Without that history (a shallow
// clone) the pass fails, saying so: it never quietly runs today's shell.
function oldShell() {
  const git = (...a) => execFileSync('git', ['-C', REPO, ...a], { encoding: 'utf8', maxBuffer: 64 << 20 });
  let base;
  try {
    const added = git('log', '--diff-filter=A', '--format=%H', '--', 'workspace-template/shell/partition-mode.js').trim().split('\n').pop();
    base = git('rev-parse', '--short', `${added}^`).trim();
  } catch (e) {
    throw new Error(`the old-shell pass needs the repo's history (the commit that added shell/partition-mode.js and its parent): ${e.message}`);
  }
  const files = new Map();
  for (const n of git('ls-tree', '--name-only', base, 'workspace-template/shell/').trim().split('\n')) {
    if (n.endsWith('.js')) files.set(path.basename(n), git('show', `${base}:${n}`));
  }
  return { base, files };
}

// useOldShell(ctx, old) → the set of old files served: the context's
// /c/shell/*.js requests answer from old.files.
async function useOldShell(ctx, old) {
  const served = new Set();
  const nameOf = (u) => { const p = new globalThis.URL(u).pathname; return p.startsWith('/c/shell/') ? p.slice('/c/shell/'.length) : ''; };
  await ctx.route((u) => old.files.has(nameOf(u.href)), (route) => {
    const name = nameOf(route.request().url());
    served.add(name);
    return route.fulfill({ status: 200, contentType: 'text/javascript; charset=utf-8', headers: { 'Cache-Control': 'no-store' }, body: old.files.get(name) });
  });
  return served;
}

// frameText: the tile frame's visible text and a few facts about its page.
const frameFacts = async (page) => (await tileFrame(page, TILE)).evaluate(() => ({
  text: document.body?.innerText || '',
  switchPage: !!document.querySelector('.plate[role=alert]'),
  noteMarkup: !!document.querySelector('.note b'),
  tile: !!document.getElementById('pmode-tile'),
  scripts: document.scripts.length,
  auto: document.documentElement.getAttribute('data-bx-theme'),
  sheet: [...document.styleSheets].some((s) => (s.href || '').endsWith('/vendor/theme.css')),
  scheme: getComputedStyle(document.documentElement).getPropertyValue('--bx-scheme').trim(),
})).catch(() => ({ text: '', switchPage: false }));

const banner = (page) => page.locator('bx-shell .alerts .alert', { hasText: `partition mode switch is requested for ${TILE}` });

async function partitionSwitch(browser) {
  const { check, done } = checker('partition-switch');
  const old = oldShell();
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
    const cspErrors = [];
    B.page.on('console', (m) => { if (/Content Security Policy/i.test(m.text())) cspErrors.push(m.text().slice(0, 160)); });
    const served = await useOldShell(B.ctx, old);
    await openShell(B.page);
    check(served.has('bx-shell.js') && served.has('bx-canvas.js') && !old.files.has('partition-mode.js'),
      `dev1's browser runs the shell from before partitioned tiles (${old.base}: ${[...served].sort().join(', ')})`);
    await usePersonalScreen(B.page);
    await openTile(B.page, TILE);
    let f = await until(async () => { const x = await frameFacts(B.page); return x.switchPage && x; }, 'the switch page in the frame');
    check(f.text.includes(`A partition mode switch is requested for ${TILE} (unpartitioned → user)`) &&
      f.text.includes('All data in this tile will be deleted for the switch to happen.'), 'the frame shows xbind\'s switch page');
    check(f.text.includes(`${TILE} says: ${NOTE}`) && !f.noteMarkup, 'the partitionNote shows as text, its markup escaped');
    check(f.text.includes(`bx partition switch ${TILE}`) && f.text.includes('user:dev1'), 'it says who decides, and where');
    check(!f.tile && f.scripts === 0, 'nothing of the tile, and no script, runs in the frame');
    check(await B.page.locator(`bx-canvas .card[data-path="${TILE}"] .pover`).count() === 0 && await B.page.locator('bx-canvas .pm, bx-side .pm').count() === 0,
      'the old shell draws no overlay and no marker: the frame\'s page and the banner carry the request');
    await banner(B.page).first().waitFor({ timeout: 15000 });
    check(await banner(B.page).count() === 1, 'the shell\'s top banner shows the partition-switch alert to a reader');
    await shot(B.page, 'partition-switch-shell');
    // the page follows the system, live: dark, then light (no hint cookie
    // here); then the context's own scheme again
    const initial = await B.page.evaluate(() => (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'));
    for (const scheme of ['dark', 'light']) {
      await B.page.emulateMedia({ colorScheme: scheme });
      f = await until(async () => { const x = await frameFacts(B.page); return x.scheme === scheme && x; }, `the switch page in ${scheme}`, 5000)
        .catch(() => frameFacts(B.page));
      check(f.auto === 'auto' && f.sheet && f.scheme === scheme, `the switch page opts in, loads theme.css and follows a ${scheme} system (${JSON.stringify({ auto: f.auto, sheet: f.sheet, scheme: f.scheme })})`);
      await shotEl(B.page, `.card[data-path="${TILE}"]`, `partition-switch-frame-${scheme}`);
    }
    await B.page.emulateMedia({ colorScheme: initial });
    await shotEl(B.page, `.card[data-path="${TILE}"]`, 'partition-switch-frame');
    check(cspErrors.length === 0, `the page's sandbox CSP lets its sheet and fonts load (${cspErrors.join(' | ') || 'no violation'})`);

    const C = await login(browser, 'sales1', 'salespass123');
    await useOldShell(C.ctx, old);
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
