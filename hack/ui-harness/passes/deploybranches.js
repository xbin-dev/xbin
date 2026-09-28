// hack/ui-harness/passes/deploybranches.js — covers D131 — branch-assigned
// deployments in the terminal window, on apps/deployy (static: no --isolate
// needed), as dev1 (terminal level). Beside deployments.js, whose helpers it
// uses (that pass is at its size budget):
//   1. the add form's Branch control (none / current (main) / a new branch):
//      feat, attached, on a new branch — the confirmation names it, the tile's
//      repository is then on it; the side list and the overview's Branch row
//      show it, with Set branch… and Clear branch;
//   2. a mismatch pause: the work tree switched (git switch -c) and a save —
//      nothing reaches feat, live reload pauses, the chip reads ⎇, its menu
//      and the panel's header offer "Keep feat on … this time" and "Add a
//      deployment for …", and the open terminal prints the grey line;
//   3. the follow offer: the chip's "Add a deployment for …" opens the
//      panel's add form preset (current, attached); back on feat's branch a
//      save pauses qa, and
//      "Resume live reload on feat (…)" follows it;
//   4. a 409 mismatch asks to use the branch this time, and the resume then
//      keeps feat on it (branchOverride); the deploy log names each entry's
//      branch; Clear branch.
// The tile's repository is put back (its branch, the branches made here
// deleted) and the fixture's bytes, whatever happened. Without branches/1 or
// a repository in the tile, it prints SKIP.
const path = require('path');
const { execFileSync } = require('child_process');
const { fs, sleep, login, closeCtx, settle, fr, waitFor, shot, checker } = require('../lib');
const D = require('./deployments');

const TILE = 'apps/deployy';
const WS = process.env.WS || '';
const DIR = path.join(WS, TILE);
const FILE = path.join(DIR, 'index.html');
const sel = `bx-frame[src="${TILE}"]`;
const FEAT = 'wpd-feat', OTHER = 'wpd-other';
const git = (...args) => execFileSync('git', ['-C', DIR, '-c', 'user.email=h@h', '-c', 'user.name=h', ...args], { encoding: 'utf8' }).trim();
const head = () => fs.readFileSync(path.join(DIR, '.git', 'HEAD'), 'utf8').trim();
const save = (text) => fs.writeFileSync(FILE, `<!doctype html><meta charset="utf-8"><title>deployy</title>\n<p id="v">${text}</p>\n`);
const dep = (s, n) => (s?.deployments || []).find((d) => d.name === n) || null;
const until = async (ctx, pred, label, tries = 100) => {
  let s = await D.stateOf(ctx);
  for (let i = 0; i < tries && !(s.status === 200 && pred(s.body)); i++) { await sleep(200); s = await D.stateOf(ctx); }
  return s;
};
const termText = (page) => page.locator(`${sel} bx-terminal`).first()
  .evaluate((el) => { const t = el.testApi(); const out = []; for (let r = 0; r < 80; r++) out.push(t.screenLine(r)); return out.join('\n'); }).catch(() => '');
const offersOf = (page) => D.pn(page, (p) => p.header?.offers || []);

async function run(X) {
  const { check, skip, M, A } = X, P = A.page;
  const st = await D.stateOf(M.ctx);
  if (st.status !== 200 || !(st.body.features || []).includes('branches/1')) { skip(`this xbind doesn't speak branches/1 (${st.status} ${JSON.stringify(st.body.features)})`); return; }
  if (!fs.existsSync(path.join(DIR, '.git', 'HEAD'))) { skip(`${TILE} has no repository of its own here`); return; }
  X.head = head();
  check((await D.resetDeploys(M.ctx)).body.record === false, `${TILE} starts in the zero state`);
  const wt0 = git('branch', '--show-current');

  // 1. the add form's Branch control: a new branch
  await D.openWindow(P);
  await D.openPanel(P);
  await D.act(P, 'add');
  await D.waitDialog(P, 'the add form');
  const form = await D.dialog(P);
  const bf = (form?.fields || []).find((x) => x.name === 'branch');
  check(!!bf && JSON.stringify((bf.options || []).map((o) => o.value)) === '["none","current","new"]' && bf.options[1].label === `current (${wt0}) — it requires ${wt0}`
    && (form.fields || []).some((x) => x.name === 'newBranch'), `the add form's Branch control: none, current (${wt0}), a new branch (${JSON.stringify(bf?.options)})`);
  await shot(P, 'deploybranches-add-form', { fullPage: false });
  await D.answer(P, 'ok', { name: 'feat', from: 'work-tree', data: 'empty', branch: 'new', newBranch: FEAT, attach: true });
  await D.waitDialog(P, 'the add confirmation');
  const c = await D.dialog(P);
  check(/Branch: wpd-feat is created at the work tree's HEAD and checked out \(no file changes\); feat requires it\./.test(c?.message || ''), `the confirmation names the new branch (${(c?.message || '').split('\n')[1]})`);
  await D.answer(P, 'ok');
  let s = await until(A.ctx, (b) => dep(b, 'feat')?.branch === FEAT && b.liveReload === 'feat', 'feat added on wpd-feat');
  check(dep(s.body, 'feat')?.branch === FEAT && s.body.workTree?.branch === FEAT && head() === `ref: refs/heads/${FEAT}`,
    `feat requires wpd-feat, and the tile's repository is on it (${dep(s.body, 'feat')?.branch}, ${s.body.workTree?.branch}, ${head()})`);
  await D.pn(P, (p) => { p.refresh(); p.select('feat'); return true; });
  await D.waitPanel(P, (p) => p.selected === 'feat' && p.rows.some((r) => r.name === 'feat' && r.branch === 'wpd-feat'), null, 'feat selected, its row naming wpd-feat');
  const acts = await D.pn(P, (p) => p.actions().map((a) => a.id));
  const ov = await D.pn(P, (p) => p.overview);
  const text = await D.pn(P, (p) => p.text());
  check(acts.includes('branch') && acts.includes('clearBranch') && ov.some(([k, v]) => k === 'branch' && v === FEAT) && text.includes('⎇ wpd-feat'),
    `the overview's Branch row and its Set branch… / Clear branch; the side list names it (${JSON.stringify(acts)}, ${JSON.stringify(ov)})`);
  await shot(P, 'deploybranches-overview', { fullPage: false });

  // 2. a mismatch pause: another branch, then a save
  await fr(P, TILE, (f) => f.open('term'));
  await D.newShell(P);
  await fr(P, TILE, (f) => f.open('deployments'));
  git('switch', '-q', '-c', OTHER);
  save('deployy: a save on wpd-other');
  s = await until(A.ctx, (b) => b.liveReload === '' && b.lastLiveReload === 'feat', 'live reload paused by the switch');
  check(s.body.liveReload === '' && dep(s.body, 'feat')?.lastDeploy?.by === 'xbind', `a save on wpd-other paused live reload on feat, xbind's own pause (${JSON.stringify(dep(s.body, 'feat')?.lastDeploy)})`);
  await waitFor(P, (t, a) => /⎇/.test(t.frameFor(a)?.testApi().deploy.chip?.text || ''), TILE, { timeout: 10000, label: 'the chip reads ⎇' }).catch(() => { });
  const chip = await fr(P, TILE, (f) => f.deploy.chip);
  check(chip?.text === `📌 Live reload paused · ⎇ ${OTHER}`, `the chip names the work tree's branch (${chip?.text})`);
  const items = await fr(P, TILE, (f) => f.deploy.chipItems().map((x) => x.label).filter(Boolean));
  check(items.includes(`Keep feat on ${OTHER} this time`) && items.includes(`Add a deployment for ${OTHER}…`), `the chip's menu offers keep and add (${JSON.stringify(items.slice(0, 5))})`);
  await D.pn(P, (p) => { p.refresh(); return true; });
  await D.waitPanel(P, (p) => (p.header?.offers || []).length === 2, null, "the panel's offers");
  check(JSON.stringify((await offersOf(P)).map((o) => o.id)) === JSON.stringify(['keep/feat', `addFor/${OTHER}`]), `the panel's header offers keep/feat and addFor/${OTHER}`);
  await fr(P, TILE, (f) => f.open('term'));
  let line = '';
  const grey = /live reload paused — the work tree is on wpd-other/;
  for (let k = 0; k < 50 && !grey.test(line); k++) { line = (await termText(P)).replace(/\n/g, ''); await sleep(200); }
  check(grey.test(line), 'the terminal prints the grey line for the switch');
  await P.locator(`${sel} button.lr`).first().evaluate((el) => el.click());
  await settle(P);
  await shot(P, 'deploybranches-chip-menu', { fullPage: false });
  await P.locator(`${sel} bx-menu .backdrop`).first().dispatchEvent('pointerdown').catch(() => { });
  await D.openPanel(P);
  await D.waitPanel(P, (p) => (p.header?.offers || []).length === 2, null, 'the panel shows its offers');
  await shot(P, 'deploybranches-paused-panel', { fullPage: false });

  // 3. the follow offer: add a deployment for wpd-other (the chip's offer:
  // it opens the panel's form), then follow feat back
  check(await fr(P, TILE, (f, t, l) => f.deploy.chipAction(l), `Add a deployment for ${OTHER}…`), "the chip's Add a deployment for wpd-other… runs");
  await D.waitDialog(P, 'the add form for wpd-other');
  const pre = await D.dialog(P);
  const pv = Object.fromEntries((pre?.fields || []).map((x) => [x.name, x.value]));
  check(pv.name === OTHER && pv.branch === 'current' && pv.attach === true, `"Add a deployment for wpd-other…" presets the form (${JSON.stringify(pv)})`);
  await D.answer(P, 'ok', { name: 'qa', from: 'work-tree', data: 'empty', branch: 'current', newBranch: '', attach: true });
  await D.waitDialog(P, 'the add confirmation for qa');
  await D.answer(P, 'ok');
  s = await until(A.ctx, (b) => dep(b, 'qa')?.branch === OTHER && b.liveReload === 'qa', 'qa follows wpd-other');
  check(s.body.liveReload === 'qa', `qa requires wpd-other, live reload on it (${s.body.liveReload})`);
  git('switch', '-q', FEAT);
  save('deployy: back on wpd-feat');
  s = await until(A.ctx, (b) => b.liveReload === '' && b.lastLiveReload === 'qa', 'the switch back pauses qa');
  check(s.body.liveReload === '' && s.body.lastLiveReload === 'qa', `back on wpd-feat, a save paused live reload on qa (${s.body.liveReload}, ${s.body.lastLiveReload})`);
  await D.pn(P, (p) => { p.refresh(); return true; });
  await D.waitPanel(P, (p) => (p.header?.offers || []).some((o) => o.id === 'follow/feat' && /^Resume/.test(o.label)), null, 'the follow offer for feat, paused');
  const follow = (await offersOf(P)).find((o) => o.id === 'follow/feat');
  check(follow?.label === `Resume live reload on feat (${FEAT})` && follow.enabled, `the offer follows feat (${follow?.label})`);
  await D.act(P, 'follow/feat');
  await D.waitDialog(P, 'the resume confirmation');
  const rc = await D.dialog(P);
  check(rc?.title === 'Resume live reload on feat?' && /Branch: feat requires wpd-feat — the work tree is on it\./.test(rc?.message || ''), `the resume confirmation names the branch (${rc?.title})`);
  await D.answer(P, 'ok');
  s = await until(A.ctx, (b) => b.liveReload === 'feat', 'live reload follows feat again');
  check(s.body.liveReload === 'feat', 'live reload follows feat again');

  // 4. a mismatch's question: pause, switch, resume feat through the chip
  await D.post(A.ctx, 'live-reload/pause', { tile: TILE, seq: s.body.seq });
  s = await until(A.ctx, (b) => b.liveReload === '', 'paused');
  git('switch', '-q', OTHER);
  await fr(P, TILE, (f) => f.deploy.refresh());
  await fr(P, TILE, (f) => { f.deploy.chipAction('Resume live reload on ▸/feat'); return true; });
  await waitFor(P, (t, a) => /requires branch/.test(t.frameFor(a)?.testApi().dialog?.title || ''), TILE, { timeout: 15000, label: 'the mismatch question' });
  const q = await D.dialog(P);
  check(q?.title === 'feat requires branch wpd-feat' && (q.buttons || []).some((b) => b.label === `Use ${OTHER} this time`), `a 409 mismatch asks to use it this time (${q?.title})`);
  await shot(P, 'deploybranches-mismatch', { fullPage: false });
  await D.answer(P, 'ok');
  await waitFor(P, (t, a) => /^Resume live reload on feat/.test(t.frameFor(a)?.testApi().dialog?.title || ''), TILE, { timeout: 15000, label: 'the resume confirmation, this time' });
  const oc = await D.dialog(P);
  check(/it takes the work tree's wpd-other this time/.test(oc?.message || ''), `the confirmation says "this time" (${(oc?.message || '').split('\n')[1]})`);
  await D.answer(P, 'ok');
  s = await until(A.ctx, (b) => b.liveReload === 'feat' && dep(b, 'feat')?.branchOverride === OTHER, 'feat on wpd-other this time');
  check(dep(s.body, 'feat')?.branchOverride === OTHER, `feat takes wpd-other this time (${dep(s.body, 'feat')?.branchOverride})`);
  await fr(P, TILE, (f) => f.deploy.refresh());
  check(!(await fr(P, TILE, (f) => f.deploy.chipItems().some((x) => /^(Attach|Resume) live reload .* \(/.test(x.label || '')))), 'while feat takes wpd-other this time, nothing offers to follow qa');
  await fr(P, TILE, (f) => f.open('deployments'));
  await D.pn(P, (p) => { p.refresh(); p.select('feat'); p.tab('log'); return true; });
  await D.waitPanel(P, (p) => (p.log || []).length > 0, null, "feat's deploy log");
  const log = await D.pn(P, (p) => p.log.map((r) => [r.how, r.branch]));
  check(log[0]?.[0] === 'resume live reload' && log[0][1] === OTHER && log.some(([h, b]) => h === 'added' && b === FEAT),
    `the deploy log names each entry's branch: the resume this time on wpd-other, the add on wpd-feat (${JSON.stringify(log)})`);
  await shot(P, 'deploybranches-log', { fullPage: false });
  await D.pn(P, (p) => { p.tab('overview'); return true; });
  await D.act(P, 'clearBranch');
  await D.waitDialog(P, 'the clear confirmation');
  check((await D.dialog(P))?.title === "Clear feat's branch?", 'Clear branch asks first');
  await D.answer(P, 'ok');
  s = await until(A.ctx, (b) => dep(b, 'feat') && !dep(b, 'feat').branch, 'feat without a branch');
  check(!dep(s.body, 'feat')?.branch, 'feat takes any branch again');
}

async function deployBranches(browser) {
  const c = checker('deployBranches');
  const M = await login(browser, 'admin', 'admin');
  const A = await login(browser, 'dev1', 'devpass123');
  const X = { ...c, M, A };
  let bytes = null;
  try { bytes = fs.readFileSync(FILE); } catch { /* none */ }
  try {
    await run(X);
  } catch (e) {
    c.check(false, `deployBranches: ${e.message.split('\n')[0]}`);
  } finally {
    await D.endSessions(A.ctx).catch(() => { });
    const s = await D.resetDeploys(M.ctx).catch(() => null);
    if (s?.status === 200) c.check(s.body.record === false, `${TILE} is back in the zero state`);
    if (X.head) {
      const b = X.head.replace(/^ref: refs\/heads\//, '');
      try { git('switch', '-q', b); } catch { /* left as it is */ }
      for (const x of [FEAT, OTHER]) { try { git('branch', '-q', '-D', x); } catch { /* none */ } }
      c.check(head() === X.head, `the tile's repository is back on ${b}`);
    }
    if (bytes) fs.writeFileSync(FILE, bytes);
    await D.dropPref(A.ctx).catch(() => { });
    await closeCtx(A.ctx, A.page).catch(() => { });
    await M.ctx.close();
  }
  c.done();
}

module.exports = { deployBranches };
