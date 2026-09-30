// hack/ui-harness/passes/oldscaffold.js — covers 10 §A.4 (C23; work pack
// I1): a workspace's scaffold is what `bx builtin update` last wrote, so an
// xbind with partitioned tiles meets an older shell and admin console. The
// pass puts the last release's shell/ and tiles/admin/ (git tag
// HARNESS_OLD_SCAFFOLD, default v0.3.64: before partitions) in place of the
// workspace's, and checks, as the admin:
//   - apps/opart (user + global; holds nothing, so partitioned at once) is
//     one tile, one card: the old shell renders it, its frame shows the
//     tile's own page (the admin's partition), and no marker or chip comes
//     from anywhere;
//   - apps/opend (a vault key, then its code asks for user partitions, with
//     a partitionNote) is pending: the old shell's /alerts banner names it
//     (level and message, all an old shell reads), and its frame shows
//     xbind's in-frame switch page with the tile's note;
//   - the old admin console opens and lists both tiles;
//   - no page error anywhere.
// The workspace's own scaffold comes back at the end, byte for byte, and
// apps/opend's request is withdrawn (its code asks nothing again).
const path = require('path');
const { execFileSync } = require('child_process');
const { URL, fs, login, closeCtx, openShell, usePersonalScreen, openTile, tileFrame, settle, sleep, shot, shotEl, checker } = require('../lib');

const WS = process.env.WS || '';
const HARNESS_DIR = process.env.HARNESS_DIR || '';
const REPO = process.env.REPO || path.join(__dirname, '..', '..', '..');
const OLD = process.env.HARNESS_OLD_SCAFFOLD || 'v0.3.64';
const PART = 'apps/opart', PEND = 'apps/opend';
const NOTE = 'Each person keeps their own notes here.';
const DIRS = ['shell', 'tiles/admin'];

async function until(fn, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(200);
  }
}

function writeTile(tile, partition, note = '') {
  const dir = path.join(WS, tile);
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'index.html'), `<!doctype html><h1 id="t">${tile} itself</h1>\n`);
  fs.writeFileSync(path.join(dir, 'xbin.json'), JSON.stringify({ title: tile, ...(partition ? { partition } : {}), ...(note ? { partitionNote: note } : {}) }));
}

// the tile's git repository stays as it is: never copied, never replaced
const noGit = (src) => path.basename(src) !== '.git';

// files under dir, relative, without .git
function files(dir) {
  const out = [];
  const walk = (d) => {
    for (const e of fs.readdirSync(d, { withFileTypes: true })) {
      if (e.name === '.git') continue;
      const p = path.join(d, e.name);
      if (e.isDirectory()) walk(p); else out.push(path.relative(dir, p));
    }
  };
  if (fs.existsSync(dir)) walk(dir);
  return out.sort();
}

// the scaffold swap: the workspace's own copied aside, the old release's
// written over it (files the old one lacks removed); restore undoes it
function scaffold() {
  if (!WS.endsWith('/ws') || !HARNESS_DIR || !WS.startsWith(HARNESS_DIR)) throw new Error(`refusing to swap the scaffold of WS=${WS}`);
  const base = path.join(HARNESS_DIR, 'old-scaffold');
  const backup = path.join(base, 'backup'), old = path.join(base, 'old');
  fs.rmSync(base, { recursive: true, force: true });
  fs.mkdirSync(old, { recursive: true });
  const tar = execFileSync('git', ['-C', REPO, 'archive', OLD, ...DIRS.map((d) => `workspace-template/${d}`)]);
  execFileSync('tar', ['-x', '-C', old], { input: tar });
  const swap = () => {
    for (const d of DIRS) {
      const ws = path.join(WS, d), was = path.join(old, 'workspace-template', d);
      fs.cpSync(ws, path.join(backup, d), { recursive: true, filter: noGit });
      const keep = new Set(files(was));
      for (const f of files(ws)) if (!keep.has(f)) fs.rmSync(path.join(ws, f));
      fs.cpSync(was, ws, { recursive: true, force: true });
    }
  };
  const restore = () => {
    for (const d of DIRS) {
      const ws = path.join(WS, d), mine = path.join(backup, d);
      if (!fs.existsSync(mine)) continue;
      const keep = new Set(files(mine));
      for (const f of files(ws)) if (!keep.has(f)) fs.rmSync(path.join(ws, f));
      fs.cpSync(mine, ws, { recursive: true, force: true, filter: noGit });
    }
  };
  return { swap, restore, oldFiles: (d) => files(path.join(old, 'workspace-template', d)), backup };
}

async function oldScaffold(browser) {
  const { check, skip, done } = checker('old-scaffold');
  const sc = scaffold();
  const oldShell = sc.oldFiles('shell');
  check(oldShell.length > 0 && !oldShell.includes('partition-mode.js') && !oldShell.includes('bx-part-consent.js'),
    `${OLD}'s shell predates partitions: no partition-mode.js, no bx-part-consent.js (${oldShell.length} files)`);
  const A = await login(browser, 'admin', 'admin');
  const api = (method, p, data) => A.ctx.request.fetch(`${URL}/api/xbin${p}`, { method, data });
  const comp = async (tile) => ((await (await api('GET', `/components/${tile}`)).json().catch(() => ({}))).component || {});
  const errors = [];
  let S;
  try {
    // ---- the tiles ----
    writeTile(PART, ['user', 'global']);
    writeTile(PEND, null);
    for (const t of [PART, PEND]) await until(async () => (await api('GET', `/components/${t}`)).status() === 200, `${t} registered`);
    await until(async () => (await comp(PART)).partition?.state === 'partitioned', `${PART} partitioned`);
    const v = await api('PUT', `/vault/${PEND}/token`, { value: 'kept' });
    check(v.status() === 200, `${PEND} holds data: a vault key (${v.status()})`);
    writeTile(PEND, ['user'], NOTE);
    const p = await until(async () => { const c = await comp(PEND); return c.partition?.state === 'pending' && c; }, `${PEND} pending`);
    check(p.partition.request?.user && !p.partition.user, `${PEND} asks for user partitions and is pending (${JSON.stringify(p.partition)})`);

    // ---- the old shell ----
    sc.swap();
    S = await login(browser, 'admin', 'admin');
    S.page.on('pageerror', (e) => errors.push(`shell: ${e.message}`));
    await openShell(S.page);
    const src = await S.page.evaluate(async () => (await (await fetch('/c/shell/bx-shell.js', { cache: 'no-store' })).text()).includes('partition-mode.js'));
    if (src) { // --dev-overlay serves the repo's scaffold over the workspace's
      skip(`the shell served is the source tree's (--dev-overlay): run the harness with HARNESS_NO_OVERLAY=1 for ${OLD}'s`);
      return;
    }
    check(!src, `the shell served is ${OLD}'s (its bx-shell.js imports no partition-mode.js)`);
    await usePersonalScreen(S.page);
    const alerts = S.page.locator('bx-shell .alerts .alert');
    await until(async () => (await alerts.allInnerTexts()).some((x) => x.includes(PEND)), 'the /alerts banner names the pending tile', 20000);
    const banner = (await alerts.allInnerTexts()).find((x) => x.includes(PEND)) || '';
    check(/partition mode switch/.test(banner) && banner.includes(PEND), `the old shell's /alerts banner names ${PEND}'s switch request (${banner.slice(0, 200)})`);
    await shot(S.page, 'old-scaffold-shell');
    for (const t of [PART, PEND]) await openTile(S.page, t);
    const pf = await tileFrame(S.page, PART);
    await pf.waitForSelector('#t', { timeout: 15000 });
    check((await pf.locator('#t').innerText()).includes(`${PART} itself`), `the old shell opens ${PART} — one card, its own page`);
    const meta = await pf.evaluate(() => document.querySelector('meta[name="xbin-partition"]')?.content || '');
    check(meta === 'user:admin', `… in the admin's own partition (the document's xbin-partition: ${meta})`);
    const ef = await tileFrame(S.page, PEND);
    await until(async () => /partition mode switch is requested/.test(await ef.evaluate(() => document.body?.innerText || '').catch(() => '')), `${PEND}'s frame shows the switch page`);
    const page = await ef.evaluate(() => document.body.innerText);
    check(page.includes('All data in this tile will be deleted') && page.includes(NOTE), `${PEND}'s frame shows xbind's switch page with the tile's note (${page.slice(0, 200).replace(/\n/g, ' ')})`);
    await settle(S.page);
    check(await S.page.locator('bx-shell .pm, bx-shell .pchip, bx-shell .pover').count() === 0,
      'the old shell ignores the new fields: no marker, no chip, no card overlay');
    await shotEl(S.page, `bx-canvas .card[data-path="${PEND}"]`, 'old-scaffold-pending');
    await shotEl(S.page, `bx-canvas .card[data-path="${PART}"]`, 'old-scaffold-partitioned');

    // ---- the old admin console ----
    await S.page.goto(`${URL}/c/tiles/admin/`);
    await S.page.locator('text=components').first().waitFor({ timeout: 15000 });
    await settle(S.page);
    check(await S.page.locator('text=/^\\s*partitions\\s*$/').count() === 0, `the admin console served is ${OLD}'s: no partitions tab`);
    for (const t of [PART, PEND]) {
      check(await S.page.locator(`text=${t}`).count() > 0, `the old admin console renders and lists ${t}`);
    }
    await shot(S.page, 'old-scaffold-admin');
    check(errors.length === 0, `no page error in the old shell or admin console (${errors.join(' | ')})`);
  } finally {
    sc.restore();
    if (S) await closeCtx(S.ctx, S.page).catch(() => {});
    writeTile(PEND, null); // the request withdrawn
    await until(async () => (await comp(PEND)).partition?.state !== 'pending', `${PEND}'s request withdrawn`).catch(() => {});
    await A.ctx.close();
  }
  // the workspace's own scaffold is back, byte for byte
  for (const d of DIRS) {
    const mine = path.join(sc.backup, d), ws = path.join(WS, d);
    const same = files(mine).join('\n') === files(ws).join('\n') && files(mine).every((f) => fs.readFileSync(path.join(mine, f)).equals(fs.readFileSync(path.join(ws, f))));
    check(same, `the workspace's own ${d}/ is restored byte for byte`);
  }
  done();
}

module.exports = { oldScaffold };
