// hack/ui-harness/passes/livereload.js — covers D119c D119e PO-10 T12 SC-ZERO
// SC-LIVE-RELOAD-PAUSE (15-test-plan §7.2, 10-ux §14.3) — pausing live reload
// from the terminal window, which is binary-served (web/frame-deploy.js):
//   0. the zero state: at most one entry point (⇈) in the bar or the tools
//      row, no chip, no frame chip, the tile API select's two options of
//      today; the dry run behind its Pause live reload captures nothing;
//   1. dev1 pauses live reload from that entry: the chip, the frame chip over
//      the tile, a grey line in the open terminal; infra1 (read) sees the
//      primary pinned, from the reader view, with no controls;
//   2. a save while paused doesn't reload anyone: the chip counts it, the
//      Reload now offer appears, the empty window's banner says so;
//   3. Reload now (the offer, the frame dialog): both frames reload once with
//      the new content, still paused, nothing pending;
//   4. Resume live reload on ▸ main: the zero state again (no record), no
//      chip, and the next save reloads the frame;
//   5. apps/crawler (node) on an xbind without --isolate: Pause live reload
//      disabled with the server's reason, and the server refuses it too; with
//      HARNESS_ISOLATE steps 1–4 run on it instead;
//   6. restore: the files, the window prefs, the sessions, and the zero state
//      asserted through the deployments state endpoint.
// The fixture is apps/reloady (seed.sh): static, owned by org:devs, so dev1
// has terminal level as a devs admin; infra1 reads it. On an xbind that
// doesn't serve the deployments state (an older binary, a route still
// reserved) part 0 checks that the window is today's, and the rest prints
// SKIP with the status it got.
const path = require('path');
const { URL, fs, sleep, login, closeCtx, settle, fr, waitFor, waitSel, openShell, usePersonalScreen, openTile, tileFrame, shot, checker, showPickers, PICKERS } = require('../lib');

const TILE = 'apps/reloady';
const NODE = 'apps/crawler';
const WS = process.env.WS || '';
const sel = (t) => `bx-frame[src="${t}"]`;
const TODAY = [['on', '🔌 tile API'], ['off', '⛔ no API']];

async function stateOf(ctx, tile) {
  const r = await ctx.request.get(`${URL}/api/xbin/deployments?tile=${encodeURIComponent(tile)}`);
  const body = await r.json().catch(() => null);
  return { status: r.status(), body: body || {}, error: body?.error || '' };
}
async function post(ctx, op, data) {
  const r = await ctx.request.post(`${URL}/api/xbin/deployments/${op}`, { data });
  return { status: r.status(), body: await r.json().catch(() => ({})) };
}

// resetDeploys: the fixture back in the zero state through the API, as a
// tile manager (M1 has one way out: resume live reload onto main). Returns
// the state it ends in.
async function resetDeploys(ctx, tile) {
  let s = await stateOf(ctx, tile);
  if (s.status !== 200 || !s.body.record) return s;
  if (s.body.liveReload === '') await post(ctx, 'live-reload/resume', { tile, deployment: s.body.primary || 'main', seq: s.body.seq });
  for (let i = 0; i < 50; i++) {
    s = await stateOf(ctx, tile);
    if (s.status !== 200 || !s.body.record) break;
    await sleep(200);
  }
  return s;
}

// The user's window on a tile, opened wide enough that the full bar usually
// fits (the chip and the offer are the full bar's; a bar that still degrades
// shows the compact chip, and the pass takes the menu instead of the offer);
// resolves once its live reload state has loaded (null or not), with the
// pickers showing wherever the bar put them.
async function openWindow(page, tile) {
  await openShell(page);
  await usePersonalScreen(page);
  await openTile(page, tile);
  await fr(page, tile, (f) => f.open('term'));
  await fr(page, tile, (f) => { const p = f.pop; if (p && p.w < 1100) f.setPop({ ...p, w: 1100 }); });
  await waitFor(page, (t, a) => !!t.frameFor(a)?.testApi().deploy.loaded, tile, { timeout: 15000, label: `${tile}'s live reload state loaded` });
  await showPickers(page, tile);
  await settle(page);
}
const dep = (page, tile) => fr(page, tile, (f) => {
  const d = f.deploy;
  return { state: d.state, changed: d.changed, chip: d.chip, offer: d.offer, entry: d.entry, frameChip: d.frameChip, banner: d.banner, items: d.chipItems(), reloads: f.reloads, narrow: f.narrow };
});
const waitDep = (page, tile, fn, label, timeout = 15000) => waitFor(page, (t, a) => {
  const d = t.frameFor(a.tile)?.testApi().deploy;
  return !!d && new Function('d', `return (${a.fn})(d)`)(d);
}, { tile, fn: fn.toString() }, { timeout, label });
const waitDialog = (page, tile, label) => waitFor(page, (t, a) => !!t.frameFor(a)?.testApi().dialog, tile, { timeout: 10000, label });
const dialog = (page, tile) => fr(page, tile, (f) => f.dialog);

// press a control: its own click handler, wherever the window put it (a
// window follows its tile, which may sit below the fold — vmtoggle's way)
async function press(loc) {
  await loc.waitFor({ state: 'attached', timeout: 5000 });
  await loc.evaluate((el) => el.click());
}
// press a row of the open <bx-menu> in the tile's window; `sub` then picks a
// row of the flyout it opened
async function menuPick(page, tile, label, sub) {
  await press(page.locator(`${sel(tile)} bx-menu .panel.main .it`).filter({ hasText: label }).first());
  if (sub !== undefined) await press(page.locator(`${sel(tile)} bx-menu .panel.sub .it`).filter({ hasText: new RegExp(`^\\s*${sub}\\s*$`) }).first());
}

// the frame's document (#v), and the lines of the window's terminal
async function pageText(page, tile) {
  const f = await tileFrame(page, tile);
  return (await f.locator('#v').textContent({ timeout: 10000 }).catch(() => '')) || '';
}
async function waitText(page, tile, want, timeout = 20000) {
  const end = Date.now() + timeout;
  let got = '';
  while (Date.now() < end) { got = await pageText(page, tile); if (got.includes(want)) return got; await sleep(200); }
  return got;
}
const termText = (page, tile) => page.locator(`${sel(tile)} bx-terminal`).first()
  .evaluate((el) => { const t = el.testApi(); const out = []; for (let r = 0; r < 80; r++) out.push(t.screenLine(r)); return out.join('\n'); }).catch(() => '');
async function waitTerm(page, tile, re, timeout = 10000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) { if (re.test(await termText(page, tile))) return true; await sleep(200); }
  return false;
}
async function newShell(page, tile) {
  await fr(page, tile, (f) => f.newTerm());
  await waitFor(page, (t, a) => !!t.frameFor(a)?.testApi().tabs.some((x) => x.id && !x.ended), tile, { timeout: 20000, label: `a shell on ${tile}` });
}
// the user's sessions on the tile are gone from the directory (a window
// whose relist finds none closes itself: reopen it only after that)
async function sessionsGone(ctx, tile) {
  for (let i = 0; i < 50; i++) {
    const r = await ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=${encodeURIComponent(tile)}`);
    if (r.ok() && !(await r.json().catch(() => [])).length) break;
    await sleep(200);
  }
  await sleep(800);
}
async function endSessions(ctx, tile) {
  const r = await ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=${encodeURIComponent(tile)}`);
  const rows = r.ok() ? await r.json().catch(() => []) : [];
  for (const s of rows) await ctx.request.delete(`${URL}/api/xbin/term/sessions/${encodeURIComponent(s.id)}`);
}
// the window pref (term-sessions.js prefKey: one path segment, no slashes)
const dropPref = (ctx, tile) => ctx.request.delete(`${URL}/api/xbin/prefs/${encodeURIComponent(`term:${tile.replaceAll('/', ':')}`)}`);
const write = (tile, text) => fs.writeFileSync(path.join(WS, tile, 'index.html'),
  `<!doctype html><meta charset="utf-8"><title>livereload</title>\n<p id="v">${text}</p>\n`);

// Steps 1–4 on one tile, as A (terminal level); R, when given, is a reader's
// context on the same tile.
async function flow(check, A, R, tile) {
  const P = A.page;
  // ---- 1. pause live reload, from the entry point ----
  await newShell(P, tile);
  await press(P.locator(`${sel(tile)} ${PICKERS} button.dentry`).first());
  await menuPick(P, tile, 'Pause live reload');
  await waitDialog(P, tile, 'the pause confirmation');
  const pd = await dialog(P, tile);
  check(pd?.title === `Pause live reload on ${tile}?` && /^Code: main keeps running the code it runs now, pinned to /.test(pd?.message || '') && /\nPauses: live reload/.test(pd?.message || ''),
    `${tile}: Pause live reload asks first, from the dry run (${JSON.stringify(pd).slice(0, 220)})`);
  await fr(P, tile, (f) => f.answerDialog('ok'));
  await waitDep(P, tile, (d) => d.state?.record && d.state.liveReload === '', `${tile}: live reload paused`);
  let d = await dep(P, tile);
  const pin = (d.state.deployments || []).find((x) => x.primary)?.checkpoint?.id || '';
  check(/^c:[0-9a-f]+$/.test(pin), `${tile}: main is pinned to a checkpoint (${pin})`);
  check(!!d.chip && d.chip.text.startsWith('📌 Live reload paused'), `${tile}: the chip reads live reload paused (${JSON.stringify(d.chip)})`);
  check(await P.locator(`${sel(tile)} button.lr`).count() >= 1 && await P.locator(`${sel(tile)} button.dentry`).count() === 0, `${tile}: the chip replaced the entry point`);
  check(d.frameChip?.text === '📌 pinned' && d.frameChip.title.startsWith(`main pinned to ${pin}`) && await P.locator(`${sel(tile)} .frame-wrap .dchip`).count() === 1,
    `${tile}: the chip over the tile reads 📌 pinned (${JSON.stringify(d.frameChip)})`);
  check(await waitTerm(P, tile, /\[live reload paused by dev1 — main is pinned to c:[0-9a-f]+; saves no longer reach it( — new terminals get the xbin-deploy remote)?\]/), `${tile}: the open terminal got the grey line`);
  await shot(P, `livereload-paused-${path.basename(tile)}`, { fullPage: false });
  if (R) {
    const rs = await stateOf(R.ctx, tile);
    check(rs.status === 200 && rs.body.view === 'reader' && rs.body.liveReload === '' && (rs.body.deployments || []).length === 1 && rs.body.seq === undefined,
      `infra1 (read) gets the reader view: main pinned, nothing else (${rs.status} ${JSON.stringify(rs.body).slice(0, 160)})`);
    await openWindow(R.page, tile);
    const r = await dep(R.page, tile);
    check(r.chip?.text === `📌 main pinned to ${pin}` && !r.frameChip && r.items.every((it) => it.kind || !it.enabled),
      `infra1's window shows main pinned, no frame chip, no usable control (${JSON.stringify({ chip: r.chip, frameChip: r.frameChip })})`);
  }

  // ---- 2. a save while paused ----
  const before = { a: d.reloads, r: R ? (await dep(R.page, tile)).reloads : 0 };
  const old = await pageText(P, tile);
  const v1 = `saved while paused ${Date.now()}`;
  write(tile, v1);
  await waitDep(P, tile, (x) => x.changed >= 1, `${tile}: the pending count`, 20000);
  await sleep(1500); // a bounded negative: nobody reloads
  d = await dep(P, tile);
  check(d.reloads === before.a && (!R || (await dep(R.page, tile)).reloads === before.r), `${tile}: no frame reloaded on the save (reloads ${d.reloads})`);
  check(await pageText(P, tile) === old, `${tile}: the frame still shows the pinned page`);
  check(d.chip?.text === `📌 Live reload paused · ${d.changed}` && d.offer, `${tile}: the chip counts it and the offer shows (${d.chip?.text})`);
  if (!d.narrow) check(/Reload now · \d+/.test(await P.locator(`${sel(tile)} .titlebar button.offer`).innerText().catch(() => '')), `${tile}: the bar's ⇡ Reload now offer`);
  await fr(P, tile, (f) => { for (let i = f.tabs.length - 1; i >= 0; i--) f.closeTab(i); });
  await sessionsGone(A.ctx, tile);
  await fr(P, tile, (f) => f.open('term'));
  await waitSel(P, `${sel(tile)} .launcher .ldep.paused`, { timeout: 10000 });
  const banner = await P.locator(`${sel(tile)} .launcher .ldep`).innerText();
  check(/Live reload is paused: \d+ files? changed since c:[0-9a-f]+, the checkpoint main runs\./.test(banner) && await P.locator(`${sel(tile)} .launcher .lreload`).count() === 1,
    `${tile}: the empty window's banner, with ⇡ Reload now (${banner.replace(/\s+/g, ' ')})`);
  await shot(P, `livereload-launcher-${path.basename(tile)}`, { fullPage: false });

  // ---- 3. Reload now ----
  await showPickers(P, tile);
  d = await dep(P, tile);
  if (d.narrow) await fr(P, tile, (f) => f.deploy.chipAction(f.deploy.chipItems().find((x) => /^⇡ Reload now/.test(x.label || '')).label));
  else await press(P.locator(`${sel(tile)} .titlebar button.offer`));
  await waitDialog(P, tile, 'the Reload now confirmation');
  const rd = await dialog(P, tile);
  check(rd?.title === 'Reload main now?' && /^Code: ships the work tree to main once/.test(rd?.message || ''), `${tile}: Reload now asks first (${JSON.stringify(rd).slice(0, 200)})`);
  await fr(P, tile, (f) => f.answerDialog('ok'));
  check((await waitText(P, tile, v1)).includes(v1), `${tile}: the frame shows the new content`);
  if (R) check((await waitText(R.page, tile, v1)).includes(v1), `${tile}: so does infra1's`);
  await waitDep(P, tile, (x) => !(x.changed > 0), `${tile}: nothing pending after Reload now`);
  await sleep(1500);
  d = await dep(P, tile);
  check(d.reloads === before.a + 1 && (!R || (await dep(R.page, tile)).reloads === before.r + 1), `${tile}: each frame reloaded once (${d.reloads - before.a})`);
  check(d.state?.record && d.state.liveReload === '' && !!d.chip, `${tile}: live reload is still paused`);

  // ---- 4. resume live reload ----
  await newShell(P, tile);
  await showPickers(P, tile);
  await press(P.locator(`${sel(tile)} button.lr`).first());
  await menuPick(P, tile, 'Resume live reload on', 'main');
  await waitDialog(P, tile, 'the resume confirmation');
  const md = await dialog(P, tile);
  check(md?.title === 'Resume live reload on main?', `${tile}: Resume asks first (${md?.title})`);
  await fr(P, tile, (f) => f.answerDialog('ok'));
  await waitDep(P, tile, (x) => x.state && !x.state.record, `${tile}: the zero state again`);
  const zs = await stateOf(A.ctx, tile);
  check(zs.status === 200 && zs.body.record === false, `${tile}: the state endpoint reports the zero state (${JSON.stringify(zs.body).slice(0, 80)})`);
  d = await dep(P, tile);
  check(!d.chip && !d.frameChip && !!d.entry && await P.locator(`${sel(tile)} button.lr, ${sel(tile)} .dchip`).count() === 0, `${tile}: the chips are gone, the entry point is back`);
  check(await waitTerm(P, tile, /\[live reload resumed on main by dev1 — saves reach main again\]/), `${tile}: the terminal got the resume line`);
  const n = d.reloads;
  const v2 = `saved after resuming ${Date.now()}`;
  write(tile, v2);
  await waitFor(P, (t, a) => (t.frameFor(a.tile)?.testApi().reloads || 0) > a.n, { tile, n }, { timeout: 15000, label: `${tile}: the next save reloads` });
  check((await waitText(P, tile, v2)).includes(v2), `${tile}: live reload works again`);
  await fr(P, tile, (f) => f.closeTerminal());
}

// Part 0 and the rest; returns early (after a SKIP) where this xbind can't go on.
async function run(browser, { check, skip }, M, A, ctxs) {
  const comps = await (await M.ctx.request.get(`${URL}/api/xbin/components`)).json();
  const row = comps.find((c) => c.path === TILE);
  check(!!row && row.owner === 'org:devs' && !row.runtime, `${TILE} is seeded static, owned by org:devs (${row?.owner}, ${row?.runtime || 'static'})`);
  const st = await stateOf(M.ctx, TILE);
  const served = st.status === 200;
  if (served) check((await resetDeploys(M.ctx, TILE)).body.record === false, `${TILE} starts in the zero state`);

  // ---- 0. the zero state ----
  await openWindow(A.page, TILE);
  let d = await dep(A.page, TILE);
  const opts = await A.page.locator(`${sel(TILE)} ${PICKERS} select.scope`).evaluateAll((ss) => ss.map((s) => [...s.options].map((o) => [o.value, o.textContent.trim()])));
  check(opts.some((o) => JSON.stringify(o) === JSON.stringify(TODAY)), `the tile API select offers exactly today's two options (${JSON.stringify(opts)})`);
  check(JSON.stringify(await fr(A.page, TILE, (f) => f.deploy.apiOptions(0).map((o) => [o.value, o.label]))) === JSON.stringify(TODAY), 'and so do the test names');
  check(!d.chip && !d.offer && !d.banner && await A.page.locator(`${sel(TILE)} button.lr, ${sel(TILE)} button.offer, ${sel(TILE)} .ldep`).count() === 0, 'no chip, no offer, no banner');
  check(!d.frameChip && await A.page.locator(`${sel(TILE)} .dchip`).count() === 0, 'no chip over the tile');
  const entries = await A.page.locator(`${sel(TILE)} ${PICKERS} button.dentry`).count();
  if (!served) {
    check(d.state === null && !d.entry && entries === 0, `no deployments state here, so no entry point: today's window (${st.status})`);
    skip(`GET /api/xbin/deployments answers ${st.status} here (${st.error || 'no JSON state'}): this xbind doesn't serve pause live reload, so parts 1–6 can't run`);
    return;
  }
  check(d.state?.record === false && d.entry?.text === '⇈' && entries === 1, `the zero state shows one entry point, ⇈ (${JSON.stringify(d.entry)}, ${entries} drawn)`);
  const zero = d.items.map((it) => (it.kind ? `<${it.kind}>` : it.label));
  check(JSON.stringify(zero) === JSON.stringify(['<header>', `Live reload: main — every save reaches everyone using ${TILE}.`, 'Pause live reload']) && d.items[2].enabled,
    `its menu offers Pause live reload (${JSON.stringify(zero)})`);
  await shot(A.page, 'livereload-zero', { fullPage: false });
  await press(A.page.locator(`${sel(TILE)} ${PICKERS} button.dentry`).first());
  await menuPick(A.page, TILE, 'Pause live reload');
  await waitDialog(A.page, TILE, 'the zero-state pause confirmation');
  const zd = await dialog(A.page, TILE);
  check(/pinned to a checkpoint of the work tree taken when you confirm/.test(zd?.message || '') && !/c:[0-9a-f]/.test(zd?.message || ''),
    `the zero state's dry run names no checkpoint (${(zd?.message || '').split('\n')[0]})`);
  await fr(A.page, TILE, (f) => f.answerDialog(null));
  check((await stateOf(A.ctx, TILE)).body.record === false, 'the dry run left the zero state (no record)');

  // ---- 1–4 on the static fixture, infra1 reading along ----
  const R = await login(browser, 'infra1', 'infrapass123');
  ctxs.push(R);
  await flow(check, A, R, TILE);

  // ---- 5. a node tile ----
  if (process.env.HARNESS_ISOLATE) {
    await resetDeploys(M.ctx, NODE);
    await openWindow(A.page, NODE);
    await flow(check, A, null, NODE);
    return;
  }
  await openWindow(A.page, NODE);
  d = await dep(A.page, NODE);
  const p = d.items.find((it) => it.label === 'Pause live reload');
  check(!!p && !p.enabled && /--isolate/.test(p.hint), `${NODE} (node, no --isolate): Pause live reload is disabled with the server's reason (${JSON.stringify(p)})`);
  await press(A.page.locator(`${sel(NODE)} ${PICKERS} button.dentry`).first());
  const r = A.page.locator(`${sel(NODE)} bx-menu .it`).filter({ hasText: 'Pause live reload' }).first();
  await r.waitFor({ state: 'attached', timeout: 5000 });
  check(await r.isDisabled() && /--isolate/.test(await r.textContent()), 'its menu row is disabled and shows the reason');
  await A.page.locator(`${sel(NODE)} bx-menu .backdrop`).dispatchEvent('pointerdown'); // dismissed
  const refused = await post(A.ctx, 'live-reload/pause', { tile: NODE, dryRun: true });
  check(refused.status === 409 && /needs isolation \(--isolate\)/.test(refused.body.error || ''), `the server refuses it too (${refused.status} ${refused.body.error})`);
  skip('steps 1–4 on a node tile: needs HARNESS_ISOLATE (xbind --isolate)');
}

async function livereload(browser) {
  const c = checker('livereload');
  const M = await login(browser, 'admin', 'admin'); // a tile manager: puts things back
  const A = await login(browser, 'dev1', 'devpass123'); // a devs admin: terminal level on both tiles
  const ctxs = [];
  const files = {};
  for (const t of [TILE, NODE]) { try { files[t] = fs.readFileSync(path.join(WS, t, 'index.html')); } catch { /* none */ } }
  try {
    await run(browser, c, M, A, ctxs);
  } catch (e) {
    c.check(false, `livereload: ${e.message.split('\n')[0]}`);
  } finally {
    // ---- 6. restore ----
    for (const [t, b] of Object.entries(files)) fs.writeFileSync(path.join(WS, t, 'index.html'), b);
    for (const t of [TILE, NODE]) {
      await endSessions(A.ctx, t).catch(() => { });
      const s = await resetDeploys(M.ctx, t).catch(() => null);
      if (s?.status === 200) c.check(s.body.record === false, `${t} is back in the zero state`);
      for (const x of [A, ...ctxs]) await dropPref(x.ctx, t).catch(() => { });
    }
    for (const x of ctxs) await closeCtx(x.ctx, x.page);
    await closeCtx(A.ctx, A.page);
    await M.ctx.close();
  }
  c.done();
}

module.exports = { livereload };
