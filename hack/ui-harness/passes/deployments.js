// hack/ui-harness/passes/deployments.js — covers P20 P21 P24 T9 T16 PO-10
// SC-PROTECT (15-test-plan §7.3, 10-ux §14.3) — tile deployments from the
// terminal window's Deployments layout (<bx-deployments>, web/bx-deploy.js),
// on apps/deployy:
//   1. dev1 adds `dev` (data empty, live reload attached): the rows (main
//      pinned, dev following the work tree), the chip, the launcher's line,
//      the tile API select's entries and P24's default; a session switched to
//      dev says XBIN_DEPLOYMENT=dev, and switching restarts it;
//   2. a save reloads the view tab (<bx-frame src="apps/deployy+dev">), not
//      the tile's pinned frame; /c/apps/deployy+dev/ serves the work tree;
//      infra1 (read) gets 403 there, and its view of the tile — the state,
//      the panel, the chip, the select — names no non-primary deployment (a
//      filtered view, not a 403);
//   3. Promote dev → main: the diff names the file; the bare URL then serves
//      it and the tile's frame reloads once; dev has no git line, main's
//      names deploy/main;
//   4. main rolls back from its deploy log: the bare URL serves the old page;
//      the header offers Undo when the move paused live reload;
//   5. the edges table: dev1 sees the apps/leads edge disabled with the
//      manager reason, sales1 blocks it and both see block; a routed state
//      renders an edge that can't be limited to reading as text, with its
//      refusal count, and a host-sharing net slot blocked with its reason;
//   6. a routed state renders the dormant registrations, "would notify" and
//      Run now, which confirms (seeded data) and sends the documented
//      request; Reassign the primary… (sales1) confirms with its
//      missing-secrets box, and cancelling sends nothing;
//   7. sales1 protects main: dev1's deploy and promotion onto main are
//      disabled with the managers named, Deploy to dev stays enabled; no
//      session of dev1 still follows main; dev1's new tab offers no main and
//      calls dev; a direct POST as dev1 is 403; sales1's promotion without
//      expect is 400, and through the panel (the reviewed checkpoint) it
//      succeeds; after dev1 pauses live reload a new tab has the tile API off;
//   8. restore through the panel (Remove with the box unticked asks again),
//      then, whatever happened, resetDeploys, the fixture's bytes, the prefs
//      and the sessions, and the zero state asserted through the state
//      endpoint.
// The fixture (seed.sh): static, owned by org:sales, so sales1 manages it;
// dev1 has terminal level without managing it; infra1 reads it; a granted
// `uses` edge on apps/leads. A static tile needs no --isolate. Each part this
// xbind or this build can't exercise — an operation route still reserved
// (501), a frame without the Deployments layout — prints SKIP with what it
// got, never a timeout and never a silent pass.
const path = require('path');
const { URL, fs, sleep, login, closeCtx, settle, fr, waitFor, openShell, usePersonalScreen, openTile, tileFrame, shot, checker, showPickers } = require('../lib');

const TILE = 'apps/deployy';
const DEV = 'dev';
const EDGE = { from: TILE, target: 'apps/leads', role: 'reader', id: 'grant:apps/leads' };
const WS = process.env.WS || '';
const sel = `bx-frame[src="${TILE}"]`;
const FILE = path.join(WS, TILE, 'index.html');

// ---- the API ----

async function stateOf(ctx) {
  const r = await ctx.request.get(`${URL}/api/xbin/deployments?tile=${encodeURIComponent(TILE)}`);
  const body = await r.json().catch(() => null);
  return { status: r.status(), body: body || {}, error: body?.error || '' };
}
async function post(ctx, op, data) {
  const r = await ctx.request.post(`${URL}/api/xbin/deployments/${op}`, { data });
  const body = await r.json().catch(() => ({}));
  return { status: r.status(), body, error: body?.error || '' };
}
const dep = (s, name) => (s?.deployments || []).find((d) => d.name === name) || null;
const same = (a, b) => !!a && !!b && (a.startsWith(b) || b.startsWith(a)); // two prefixes of one checkpoint id
async function until(ctx, pred, tries = 75) {
  let s = await stateOf(ctx);
  for (let i = 0; i < tries && !(s.status === 200 && pred(s.body)); i++) { await sleep(200); s = await stateOf(ctx); }
  return s;
}

// resetDeploys: the fixture back in the zero state through the API, as a
// manager (10-ux §14.4): unprotect, main primary again, every edge back to
// its default, every other deployment removed, live reload resumed on main;
// then the record is gone (P5's opt-out doubles as the check). A route this
// xbind doesn't build (501) is never needed. Returns the last state.
async function resetDeploys(ctx) {
  let s = await stateOf(ctx);
  for (let round = 0; round < 3 && s.status === 200 && s.body.record; round++) {
    if (s.body.protectedPrimary) { await post(ctx, 'protect', { tile: TILE, on: false, seq: s.body.seq }); s = await stateOf(ctx); }
    if (s.body.primary && s.body.primary !== 'main') { await post(ctx, 'primary', { tile: TILE, deployment: 'main', confirm: 'data-stays', seq: s.body.seq }); s = await stateOf(ctx); }
    for (const e of s.body.edges || []) if (e.set) { await post(ctx, 'edge', { tile: TILE, edge: e.id, policy: 'default', seq: s.body.seq }); s = await stateOf(ctx); }
    for (const d of s.body.deployments || []) if (d.name !== 'main') { await post(ctx, 'remove', { tile: TILE, deployment: d.name, confirm: 'erase', seq: s.body.seq }); s = await stateOf(ctx); }
    if (s.body.record && s.body.liveReload !== 'main') await post(ctx, 'live-reload/resume', { tile: TILE, deployment: 'main', seq: s.body.seq });
    s = await until(ctx, (b) => !b.record, 50);
  }
  return s;
}

// ---- the window and the panel ----

async function openWindow(page) {
  await openShell(page);
  await usePersonalScreen(page);
  await openTile(page, TILE);
  await fr(page, TILE, (f) => f.open('term'));
  await fr(page, TILE, (f) => { const p = f.pop; if (p && p.w < 1100) f.setPop({ ...p, w: 1100 }); });
  await waitFor(page, (t, a) => !!t.frameFor(a)?.testApi().deploy.loaded, TILE, { timeout: 15000, label: `${TILE}'s deployments state loaded` });
  await showPickers(page, TILE);
  await settle(page);
}
// whether this build's frame has the Deployments layout and its test names
const hasLayout = (page) => fr(page, TILE, (f) => f.layouts.includes('deployments') && typeof f.deploy.panel === 'function'
  && typeof f.deploy.setTarget === 'function');
async function openPanel(page) {
  await fr(page, TILE, (f) => f.open('deployments'));
  await waitFor(page, (t, a) => !!t.frameFor(a)?.testApi().deploy.panel()?.state, TILE, { timeout: 15000, label: 'the Deployments panel loaded' });
}
// pn(page, (p, arg) => …, arg): against the panel's test surface. Never
// return an action's promise from here: it waits on a dialog the pass has
// yet to answer.
const pn = (page, fn, arg) => fr(page, TILE, (f, t, a) => new Function('p', 'arg', `return (${a.fn})(p, arg)`)(f.deploy.panel(), a.arg), { fn: fn.toString(), arg: arg ?? null });
const waitPanel = (page, fn, arg, label, timeout = 20000) => waitFor(page, (t, a) => {
  const p = t.frameFor(a.tile)?.testApi().deploy.panel();
  return !!p && new Function('p', 'arg', `return (${a.fn})(p, arg)`)(p, a.arg);
}, { tile: TILE, fn: fn.toString(), arg: arg ?? null }, { timeout, label });
// act waits for the panel to be idle first: its actions drop while a
// previous one still awaits its answer (the panel's busy flag).
const act = async (page, id) => {
  await waitPanel(page, (p) => !p.busy, null, `the panel is idle before ${id}`);
  return pn(page, (p, i) => { p.act(i); return true; }, id);
};
const waitDialog = (page, label) => waitFor(page, (t, a) => !!t.frameFor(a)?.testApi().dialog, TILE, { timeout: 15000, label });
const dialog = (page) => fr(page, TILE, (f) => f.dialog);
const answer = (page, button, values) => fr(page, TILE, (f, t, a) => { f.answerDialog(a.button, a.values || {}); return true; }, { button, values: values || {} });
// start a dialog-backed panel action, read its dialog, answer it
async function confirm(page, id, title, values, label) {
  await act(page, id);
  await waitDialog(page, label);
  const d = await dialog(page);
  await answer(page, 'ok', values);
  return { d, ok: d?.title === title };
}

// a new shell tab; resolves with its index once it has a session
async function newShell(page) {
  const n = await fr(page, TILE, (f) => f.tabs.length);
  await fr(page, TILE, (f) => f.newTerm());
  await waitFor(page, (t, a) => { const x = t.frameFor(a.tile)?.testApi().tabs; return !!x && x.length > a.n && !!x[x.length - 1].id; }, { tile: TILE, n }, { timeout: 20000, label: 'a new shell' });
  return n;
}
// what the shell on tab i says $XBIN_DEPLOYMENT is ('' unset, null: no answer)
async function envDeployment(page, i) {
  await fr(page, TILE, (f, t, idx) => { f.setActiveTab(idx); return true; }, i);
  await settle(page);
  await fr(page, TILE, (f) => f.focusTerminal());
  const needle = `DEP${Date.now() % 100000}=[`;
  await page.keyboard.type(`echo "${needle}$XBIN_DEPLOYMENT]"`);
  await page.keyboard.press('Enter');
  for (let k = 0; k < 50; k++) {
    const got = await page.locator(`${sel} bx-terminal`).evaluateAll((els, want) => {
      for (const el of els) {
        const t = el.testApi?.();
        for (let row = 0; t && row < 80; row++) {
          const line = t.screenLine(row) || '';
          const at = line.lastIndexOf(want);
          if (at >= 0 && !line.includes('$XBIN_DEPLOYMENT')) return line.slice(at + want.length).split(']')[0];
        }
      }
      return null;
    }, needle);
    if (got !== null) return got;
    await sleep(200);
  }
  return null;
}
async function sessionsOf(ctx) {
  const r = await ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=${encodeURIComponent(TILE)}`);
  return r.ok() ? await r.json().catch(() => []) : [];
}
async function endSessions(ctx) {
  for (const s of await sessionsOf(ctx)) await ctx.request.delete(`${URL}/api/xbin/term/sessions/${encodeURIComponent(s.id)}`);
}
// the window pref (term-sessions.js prefKey: one path segment, no slashes)
const dropPref = (ctx) => ctx.request.delete(`${URL}/api/xbin/prefs/${encodeURIComponent(`term:${TILE.replaceAll('/', ':')}`)}`);
const write = (text) => fs.writeFileSync(FILE, `<!doctype html><meta charset="utf-8"><title>deployy</title>\n<p id="v">${text}</p>\n`);
// the tile's own frame (the primary) and its document text
async function frameText(page) {
  const f = await tileFrame(page, TILE);
  return (await f.locator('#v').textContent({ timeout: 10000 }).catch(() => '')) || '';
}
async function waitFrameText(page, want, timeout = 20000) {
  const end = Date.now() + timeout;
  let got = '';
  while (Date.now() < end) { got = await frameText(page); if (got.includes(want)) return got; await sleep(200); }
  return got;
}
// another frame's document text, found by URL (the view tab's frame)
async function docText(page, urlPart, want = '', timeout = 20000) {
  const end = Date.now() + timeout;
  let got = '';
  while (Date.now() < end) {
    const f = page.frames().find((x) => x.url().includes(urlPart));
    got = (f ? await f.locator('#v').textContent({ timeout: 2000 }).catch(() => '') : '') || '';
    if (got && got.includes(want)) return got;
    await sleep(200);
  }
  return got;
}

// ---- routed states (steps 5 and 6): what the harness xbind can't produce ----

const cp = (id) => ({ id, hash: id.slice(2).padEnd(40, '0'), feed: 'work-tree', at: new Date(Date.now() - 3600e3).toISOString(), by: 'user:dev1' });
const yes = { ok: true };
const managersOnly = { ok: false, why: "Only tile managers change this: the tile's owner, its org's admins, or a workspace admin", kind: 'authority' };
function routedState(base, { seeded = false, manager = false } = {}) {
  const now = new Date().toISOString();
  const real = dep(base, 'main');
  const main = { ...(real || {}), name: 'main', primary: true, liveReload: false, checkpoint: real?.checkpoint || cp('c:1111111'),
    status: { state: 'healthy', gen: 2, serving: real?.checkpoint?.id || 'c:1111111' }, url: `/c/${TILE}/`, registrations: [], can: real?.can || {} };
  const dev = {
    name: DEV, primary: false, liveReload: true, checkpoint: null, url: `/c/${TILE}+${DEV}/`,
    status: { state: 'healthy', gen: 3, serving: 'work-tree' },
    data: seeded ? { state: 'seeded', from: 'main', at: now, by: 'user:sales1' } : { state: 'empty', at: now, by: 'user:dev1' },
    vault: { keys: 2, placeholders: 1 }, deliveries: false, alwaysOn: false, alwaysOnDeclared: false,
    registrations: [
      { kind: 'cron', name: 'nightly', schedule: '0 3 * * *', path: '/tick', dormant: true },
      { kind: 'bus', name: 'trig-1', resource: 'res:apps/leads/events', prefix: 'orders/', path: '/trigger/bus/1', dormant: true },
      { kind: 'ingress-host', name: 'deployy.example.com', dormant: true },
    ],
    wouldNotify: [{ at: now, to: 'user:sales1', title: 'Daily digest' }],
    can: { open: yes, deploy: yes, restart: yes, promoteTo: yes, rollback: yes, attach: yes, remove: yes, reset: yes, runNow: yes,
      seed: manager ? yes : managersOnly, primary: manager ? yes : managersOnly },
  };
  return {
    tile: TILE, record: true, schema: 1, seq: 900, features: ['deployments/1'], owner: 'org:sales', view: 'full',
    primary: 'main', liveReload: DEV, lastLiveReload: DEV, protectedPrimary: false,
    allowed: { pause: yes, deployments: yes }, deployments: [main, dev],
    edges: [
      { id: EDGE.id, kind: 'http', to: EDGE.target, role: 'reader', policy: 'read', default: 'read', values: ['read', 'block'], set: false, refused: 0, clamped: 0 },
      { id: 'grant:apps/custom', kind: 'http', to: 'apps/custom', role: 'operator', policy: 'block', default: 'block', values: ['block'], set: false, refused: 4, clamped: 0 },
      { id: 'slot:net', kind: 'net', to: '', policy: 'inherit', default: 'inherit', values: ['inherit', 'block'], set: false, effective: 'block',
        why: "this tile's network shares the host's: non-primary deployments get no egress", refused: 3, clamped: 0 },
    ],
    caller: { ...(base.caller || {}), manager, can: { pause: yes, resume: yes, reloadNow: yes, add: yes, edges: manager ? yes : managersOnly, protect: manager ? yes : managersOnly } },
  };
}
// route TILE's state, and the named POSTs, on one page; returns the undo
async function routeState(page, state, posts = {}) {
  const PFX = '/api/xbin/deployments/';
  const isState = (u) => u.pathname === '/api/xbin/deployments' && u.searchParams.get('tile') === TILE;
  const isPost = (u) => u.pathname.startsWith(PFX) && Object.hasOwn(posts, u.pathname.slice(PFX.length));
  const onState = (route) => route.fulfill({ json: state });
  const onPost = (route) => {
    const u = new globalThis.URL(route.request().url());
    return posts[u.pathname.slice(PFX.length)](route, JSON.parse(route.request().postData() || '{}'));
  };
  await page.route(isState, onState);
  await page.route(isPost, onPost);
  return async () => { await page.unroute(isState, onState); await page.unroute(isPost, onPost); };
}

// ---- the steps ----

// 1. add dev, as dev1, with live reload attached
async function stepAdd(X) {
  const { check, A } = X, P = A.page;
  await openPanel(P);
  const zero = await pn(P, (p) => ({ record: p.state.record, text: p.text() }));
  check(zero.record === false && zero.text.includes('Add deployment…'), `the zero-state panel offers Add deployment… (${zero.text.slice(0, 120)})`);
  await act(P, 'add');
  await waitDialog(P, 'the Add deployment form');
  const form = await dialog(P);
  check(form?.title === `Add deployment to ${TILE}` && (form.fields || []).some((x) => x.name === 'attach'), `the add form (${form?.title})`);
  await answer(P, 'ok', { name: DEV, from: 'work-tree', data: 'empty', attach: true });
  await waitDialog(P, 'the add confirmation');
  const c = await dialog(P);
  check(c?.title === `Add deployment ${DEV} to ${TILE}?` && /Live reload moves to dev/.test(c?.message || ''), `Add asks first, from the dry run (${c?.title})`);
  await answer(P, 'ok');
  await waitPanel(P, (p, d) => p.state?.record && p.rows.some((r) => r.name === d) && p.state.liveReload === d, DEV, 'dev added, live reload on it');
  const rows = await pn(P, (p) => p.rows);
  const main = rows.find((r) => r.name === 'main'), dv = rows.find((r) => r.name === DEV);
  check(!!main?.primary && /^📌 c:[0-9a-f]{7,}$/.test(main?.code || '') && dv?.code === '● work tree',
    `the rows: main pinned to a checkpoint, dev following the work tree (${JSON.stringify(rows.map((r) => [r.name, r.code]))})`);
  // the window's own state follows the add's deployments event (debounced)
  await waitFor(P, (t, a) => t.frameFor(a.tile)?.testApi().deploy.chip?.text === a.want, { tile: TILE, want: `● Live reload: ${DEV}` },
    { timeout: 10000, label: 'the chip follows the add' }).catch(() => { });
  const chip = await fr(P, TILE, (f) => f.deploy.chip);
  check(chip?.text === `● Live reload: ${DEV}`, `the chip reads ● Live reload: dev (${chip?.text})`);
  // the launcher (the window without sessions) says what a new session calls
  await fr(P, TILE, (f) => f.open('term'));
  await settle(P);
  const launcher = (await P.locator(`${sel} .launcher`).innerText().catch(() => '')).replace(/\s+/g, ' ');
  check(launcher.includes('· target: main'), `the launcher's line reads · target: main (${launcher.slice(0, 160)})`);
  // a new tab: the tile API select's entries, P24's default
  const i = await newShell(P);
  const opts = await fr(P, TILE, (f, t, idx) => f.deploy.apiOptions(idx).map((o) => [o.value, o.label]), i);
  check(JSON.stringify(opts) === JSON.stringify([['primary', '🔌 target: main (primary)'], [DEV, `🔌 target: ${DEV}`], ['off', '⛔ no API']]),
    `the tile API select offers main (primary), dev and no API (${JSON.stringify(opts)})`);
  check(await fr(P, TILE, (f, t, idx) => f.deploy.target(idx), i) === 'primary', "the new tab calls the primary (P24's default)");
  check(await envDeployment(P, i) === '', 'its shell has no XBIN_DEPLOYMENT (it follows the primary)');
  // switching the entry restarts the session onto dev, and back
  for (const [to, env] of [[DEV, DEV], ['primary', '']]) {
    const before = await fr(P, TILE, (f, t, idx) => f.tabs[idx].id, i);
    await fr(P, TILE, (f, t, a) => { f.deploy.setTarget(a.i, a.to); return true; }, { i, to });
    await waitDialog(P, `the restart confirmation (${to})`);
    await answer(P, 'ok');
    await waitFor(P, (t, a) => { const f = t.frameFor(a.tile)?.testApi(); return !!f && !!f.tabs[a.i]?.id && f.tabs[a.i].id !== a.before && f.deploy.target(a.i) === a.to; },
      { tile: TILE, i, before, to }, { timeout: 20000, label: `the session restarted onto ${to}` });
    const said = await envDeployment(P, i);
    check(said === env, `switched to ${to}, the restarted shell says XBIN_DEPLOYMENT=${env} (${said === null ? 'no answer' : `[${said}]`})`);
  }
  X.followerTab = i; // follows the primary: step 7 watches it
  await shot(P, 'deployments-added', { fullPage: false });
}

// 2. a save reaches the view tab; its URL; what a reader sees
async function stepURL(X) {
  const { check, A, R } = X, P = A.page;
  const pinned = await frameText(P);
  const v1 = `saved for dev ${Date.now()}`;
  await openPanel(P);
  await pn(P, (p, d) => { p.select(d); p.tab('view'); return true; }, DEV);
  await waitPanel(P, (p) => !!p.view(), null, 'the view tab mounted its frame');
  const r0 = await pn(P, (p) => p.view().reloads);
  const primaryReloads = await fr(P, TILE, (f) => f.reloads);
  write(v1);
  X.v1 = v1;
  await waitPanel(P, (p, n) => (p.view()?.reloads || 0) > n, r0, 'the save reloads the view tab', 20000);
  check((await docText(P, `/c/${TILE}+${DEV}/`, v1)).includes(v1), 'the view tab shows the save');
  await sleep(1200); // a bounded negative: the pinned primary doesn't reload
  check(await fr(P, TILE, (f) => f.reloads) === primaryReloads && await frameText(P) === pinned, "the tile's own frame (main, pinned) neither reloads nor changes");
  const page = await A.ctx.newPage();
  await page.goto(`${URL}/c/${TILE}+${DEV}/`);
  check(((await page.locator('#v').textContent({ timeout: 10000 }).catch(() => '')) || '').includes(v1), `/c/${TILE}+${DEV}/ serves the work tree`);
  await page.close();
  // infra1 reads the tile: refused at the dev URL, shown the primary only
  const dr = await R.ctx.request.get(`${URL}/c/${TILE}+${DEV}/`);
  check(dr.status() === 403, `infra1 (read) is refused at the dev URL (${dr.status()})`);
  const rs = await stateOf(R.ctx);
  const raw = JSON.stringify(rs.body);
  check(rs.status === 200 && rs.body.view === 'reader' && (rs.body.deployments || []).length === 1 && !raw.includes(`"${DEV}"`) && !raw.includes(`+${DEV}`),
    `infra1's state is the reader view: the primary alone, no dev (${rs.status} ${raw.slice(0, 160)})`);
  await openWindow(R.page);
  await openPanel(R.page);
  const rv = await pn(R.page, (p) => ({ rows: p.rows.map((r) => r.name), text: p.text() }));
  const rchip = await fr(R.page, TILE, (f) => f.deploy.chip);
  const ropts = await fr(R.page, TILE, (f) => f.deploy.apiOptions(0).map((o) => o.label));
  const said = [rv.text, rchip?.text || '', rchip?.title || '', ...ropts].join(' | ');
  check(JSON.stringify(rv.rows) === '["main"]' && !/\bdev\b/.test(said),
    `infra1's panel, chip and tile API select name the primary only (${JSON.stringify(rv.rows)}; ${said.slice(0, 200)})`);
  await shot(R.page, 'deployments-reader', { fullPage: false });
}

// 3. promote dev → main
async function stepPromote(X) {
  const { check, A } = X, P = A.page;
  await openPanel(P);
  await pn(P, (p, d) => { p.select(d); p.tab('overview'); return true; }, DEV);
  const n = await fr(P, TILE, (f) => f.reloads);
  await act(P, 'promote');
  await waitPanel(P, (p) => p.diff && p.diff.files !== null, null, 'the promotion diff', 30000);
  const diff = await pn(P, (p) => ({ ...p.diff, text: p.text() }));
  check(diff.from === DEV && diff.to === 'main' && diff.files >= 1 && /^c:[0-9a-f]+/.test(diff.checkpoint || '') && diff.text.includes('index.html'),
    `the diff names the file (${JSON.stringify({ ...diff, text: undefined })})`);
  const r = await confirm(P, 'confirm', `Promote ${DEV} → main?`, {}, 'the promotion confirmation');
  check(r.ok, `Promote asks first (${r.d?.title})`);
  check((await waitFrameText(P, X.v1)).includes(X.v1), 'the bare URL serves the promoted page');
  await waitFor(P, (t, a) => (t.frameFor(a.tile)?.testApi().reloads || 0) > a.n, { tile: TILE, n }, { timeout: 15000, label: "the tile's frame reloads once" });
  const git = { dev: await pn(P, (p, d) => { p.select(d); return p.gitLine; }, DEV), main: await pn(P, (p) => { p.select('main'); return p.gitLine; }) };
  check(git.dev === null && /deploy\/main/.test(git.main || ''), `dev has no git line, main's names deploy/main (${JSON.stringify(git)})`);
}

// 4. roll main back from its deploy log
async function stepRollback(X) {
  const { check, A } = X, P = A.page;
  await openPanel(P);
  await pn(P, (p) => { p.select('main'); p.tab('log'); return true; });
  // the panel's state has caught up with step 3 once main's newest entry
  // (the promote) is the one running; before that the promoted checkpoint
  // itself reads as a roll back target
  await waitPanel(P, (p) => (p.log || [])[0]?.state === 'running' && p.log.some((r) => r.rollback?.enabled), null,
    "main's deploy log offers a roll back", 20000);
  const row = await pn(P, (p) => p.log.filter((r) => r.rollback?.enabled).pop()); // the oldest: the seeded page
  const r = await confirm(P, `rollback/${row.checkpoint}`, `Roll back main to ${row.checkpoint}?`, {}, 'the roll back confirmation');
  check(r.ok, `Roll back asks first (${r.d?.title})`);
  check((await waitFrameText(P, X.seededText)).includes(X.seededText), `the bare URL serves the old page again (${row.checkpoint})`);
  const s = await until(A.ctx, (b) => same(dep(b, 'main')?.checkpoint?.id, row.checkpoint));
  check(same(dep(s.body, 'main')?.checkpoint?.id, row.checkpoint), `main runs ${row.checkpoint} (${dep(s.body, 'main')?.checkpoint?.id})`);
  await waitPanel(P, (p, id) => p.state?.seq === id, s.body.seq, 'the panel caught up');
  const h = await pn(P, (p) => p.header);
  if (s.body.liveReload === '') {
    // the move paused live reload (main was its target): the Undo header
    check(h.actions.includes('undo') && h.actions.includes('resume') && /The work tree still holds the code you rolled back from/.test(h.text),
      `the header offers Undo and Resume live reload (${JSON.stringify(h)})`);
  } else {
    check(h.text.includes(`The primary, main, is pinned to ${dep(s.body, 'main').checkpoint.id}`) && !h.actions.includes('undo'),
      `live reload stays on ${s.body.liveReload}, and the header names main's rolled-back checkpoint (${h.text})`);
  }
}

// 5a. the edges table, for real
async function stepEdges(X) {
  const { check, A, S } = X;
  await openPanel(A.page);
  await pn(A.page, (p) => { p.select(''); return true; });
  const row = (await pn(A.page, (p) => p.edges)).find((e) => e.id === EDGE.id);
  check(!!row && !row.enabled && /tile managers|owner/.test(row.why || ''), `dev1 sees the ${EDGE.target} edge disabled with the manager reason (${JSON.stringify(row)})`);
  await salesPanel(X);
  await pn(S.page, (p) => { p.select(''); return true; });
  const sr = (await pn(S.page, (p) => p.edges)).find((e) => e.id === EDGE.id);
  check(!!sr?.enabled && sr.values.includes('block'), `sales1 may set it (${JSON.stringify(sr)})`);
  await pn(S.page, (p, id) => { p.setEdge(id, 'block'); return true; }, EDGE.id);
  await waitPanel(S.page, (p, id) => p.edges.find((e) => e.id === id)?.value === 'block', EDGE.id, "sales1's row shows block");
  await waitPanel(A.page, (p, id) => p.edges.find((e) => e.id === id)?.value === 'block', EDGE.id, "dev1's row shows block too");
  check(((await stateOf(S.ctx)).body.edges || []).find((e) => e.id === EDGE.id)?.policy === 'block', 'the state holds the block');
  // back to its default: an edge override keeps the record, so step 8's
  // resume could never return the tile to the zero state
  const back = await post(S.ctx, 'edge', { tile: TILE, edge: EDGE.id, policy: 'default' });
  const e = ((await stateOf(S.ctx)).body.edges || []).find((x) => x.id === EDGE.id);
  check(back.status === 200 && e && !e.set, `the edge goes back to its default (${back.status} ${JSON.stringify(e)})`);
}

async function salesPanel(X) {
  if (!X.salesWindow) { await openWindow(X.S.page); X.salesWindow = true; }
  await openPanel(X.S.page);
}

// 5b and 6: routed states on dev1's and sales1's panels
async function stepRouted(X) {
  const { check, A, S } = X, P = A.page;
  const base = (await stateOf(A.ctx)).body;
  // 5b: edges that can't be limited to reading, and a host-sharing net slot
  let undo = await routeState(P, routedState(base));
  try {
    await openPanel(P);
    await pn(P, (p) => { p.refresh(); p.select(''); return true; });
    await waitPanel(P, (p) => p.state?.seq === 900, null, 'the routed state (edges)');
    const edges = await pn(P, (p) => p.edges);
    const custom = edges.find((e) => e.id === 'grant:apps/custom'), net = edges.find((e) => e.id === 'slot:net');
    check(!!custom && custom.values.length === 0 && /^blocked — /.test(custom.text) && /refused 4/.test(custom.refused),
      `an edge that can't be limited to reading renders as blocked text, with its refusal count (${JSON.stringify(custom)})`);
    check(!!net && net.values.length === 0 && net.text === "blocked — this tile's network shares the host's: non-primary deployments get no egress",
      `a host-sharing net slot renders blocked, with its reason (${JSON.stringify(net)})`);
    check(await P.locator(`${sel} bx-deployments .tbl select`).count() === 1, 'only the edge that can be limited has a control');
  } finally { await undo(); }
  // 6: dormant registrations, "would notify", Run now (seeded data: it asks)
  let sent = null;
  const seededState = routedState(base, { seeded: true });
  undo = await routeState(P, seededState, {
    'run-now': (route, body) => {
      if (body.dryRun) return route.fulfill({ json: { state: seededState, impact: { data: 'none', affects: 'deployment' } } });
      sent = body;
      return route.fulfill({ json: { state: seededState, delivery: { status: 200, ms: 12 } } });
    },
  });
  try {
    await pn(P, (p, d) => { p.refresh(); p.select(d); p.tab('registrations'); return true; }, DEV);
    await waitPanel(P, (p) => p.state?.seq === 900 && p.registrations.length === 3, null, 'the routed state (registrations)');
    const regs = await pn(P, (p) => p.registrations);
    const wn = await pn(P, (p) => p.wouldNotify);
    const pill = (k) => regs.find((r) => r.kind === k)?.pill;
    check(pill('cron') === 'dormant' && pill('bus') === 'dormant' && pill('ingress-host') === 'dormant — routes reach the primary only',
      `the dormant registrations (${JSON.stringify(regs.map((r) => [r.kind, r.pill]))})`);
    check(wn.length === 1 && /^would notify sales1 · "Daily digest"/.test(wn[0]), `"would notify" (${JSON.stringify(wn)})`);
    check(!!regs.find((r) => r.kind === 'cron')?.runNow?.enabled, 'Run now is offered on the cron job');
    await pn(P, (p) => { p.runNow('nightly'); return true; });
    await waitDialog(P, 'the Run now confirmation');
    const d = await dialog(P);
    check(d?.title === `Run nightly on ${DEV} once?` && /seeded from main: real people's data/.test(d?.message || ''), `Run now asks first, naming the seeded data (${d?.title})`);
    await answer(P, 'ok');
    for (let k = 0; k < 50 && !sent; k++) await sleep(100);
    check(!!sent && sent.tile === TILE && sent.deployment === DEV && sent.job === 'nightly' && !('dryRun' in sent), `Run now sends the documented request (${JSON.stringify(sent)})`);
  } finally { await undo(); }
  // flow F: Reassign the primary…, as sales1, with a routed dry run
  let asked = false, applied = false;
  const mgrState = routedState(base, { manager: true });
  undo = await routeState(S.page, mgrState, {
    primary: (route, body) => {
      if (!body.dryRun) { applied = true; return route.fulfill({ status: 409, json: { error: 'routed: never sent' } }); }
      asked = true;
      return route.fulfill({ json: { state: mgrState, impact: { code: null, data: 'none', placeholders: ['STRIPE_KEY'], affects: 'everyone', reloads: ['main'] } } });
    },
  });
  try {
    await salesPanel(X);
    await pn(S.page, (p) => { p.refresh(); p.select(''); return true; });
    await waitPanel(S.page, (p) => p.state?.seq === 900, null, "sales1's routed state");
    await act(S.page, 'reassign');
    await waitDialog(S.page, 'the reassign confirmation');
    const d = await dialog(S.page);
    const boxes = (d?.fields || []).map((f) => f.name);
    check(asked && d?.title === `Make ${DEV} the primary of ${TILE}?` && boxes.includes('ok') && boxes.includes('secrets') && /STRIPE_KEY/.test(d?.message || ''),
      `Reassign the primary… confirms with its missing-secrets box (${d?.title}; ${JSON.stringify(boxes)})`);
    await answer(S.page, null);
    await sleep(500);
    check(!applied, 'cancelling sends nothing');
  } finally { await undo(); }
  await pn(P, (p) => { p.refresh(); return true; });
  await pn(S.page, (p) => { p.refresh(); return true; });
}

// 7. a protected primary
async function stepProtect(X) {
  const { check, A, S } = X, P = A.page;
  await salesPanel(X);
  await pn(S.page, (p) => { p.refresh(); p.select(''); return true; });
  await waitPanel(S.page, (p) => p.actions().some((a) => a.id === 'protect' && a.enabled), null, 'sales1 may protect main');
  const r = await confirm(S.page, 'protect', 'Protect main?', {}, 'the protect confirmation');
  check(r.ok, `Protect asks first (${r.d?.title})`);
  const s = await until(S.ctx, (b) => b.protectedPrimary === true);
  check(s.body.protectedPrimary === true, 'main is protected');
  // dev1: code moves onto main are disabled, naming the managers; dev stays open
  await openPanel(P);
  await waitPanel(P, (p) => p.state?.protectedPrimary === true, null, "dev1's panel sees the protection");
  const onDev = await pn(P, (p, d) => { p.select(d); return p.actions(); }, DEV);
  const onMain = await pn(P, (p) => { p.select('main'); return p.actions(); });
  const promote = onDev.find((a) => a.id === 'promote'), deployDev = onDev.find((a) => a.id === 'deploy'), deployMain = onMain.find((a) => a.id === 'deploy');
  const managers = /tile managers|owner|org's admins/;
  check(!!promote && !promote.enabled && managers.test(promote.why), `Promote dev → main is disabled, naming the managers (${JSON.stringify(promote)})`);
  check(!!deployMain && !deployMain.enabled && managers.test(deployMain.why), `Deploy to main is disabled, naming the managers (${JSON.stringify(deployMain)})`);
  check(!!deployDev?.enabled, `Deploy to dev stays enabled (${JSON.stringify(deployDev)})`);
  // the session that followed main no longer does (restarted onto dev, or ended)
  if (X.followerTab !== undefined) {
    let rows = [];
    for (let k = 0; k < 50; k++) { rows = await sessionsOf(A.ctx); if (rows.every((x) => x.api === false || x.deployment)) break; await sleep(200); }
    check(rows.every((x) => x.api === false || x.deployment === DEV), `no session of dev1 follows the protected main (${JSON.stringify(rows.map((x) => [x.id, x.api, x.deployment || '']))})`);
  }
  // a new tab: no main entry, calls dev
  await fr(P, TILE, (f) => f.open('term'));
  const i = await newShell(P);
  const opts = await fr(P, TILE, (f, t, idx) => f.deploy.apiOptions(idx).map((o) => o.value), i);
  check(!opts.includes('primary') && opts.includes(DEV) && await fr(P, TILE, (f, t, idx) => f.deploy.target(idx), i) === DEV,
    `dev1's new tab offers no main entry and calls dev (${JSON.stringify(opts)})`);
  // a direct POST as dev1, well formed, is refused
  const direct = await post(A.ctx, 'deploy', { tile: TILE, deployment: 'main', checkpoint: dep(s.body, 'main')?.checkpoint?.id, seq: s.body.seq });
  check(direct.status === 403, `a direct deploy to main as dev1 answers 403 (${direct.status} ${direct.error})`);
  // sales1's promotion: without expect 400; through the panel it succeeds
  const now = await stateOf(S.ctx);
  const bare = await post(S.ctx, 'promote', { tile: TILE, from: DEV, to: 'main', seq: now.body.seq });
  check(bare.status === 400, `sales1's promotion without expect is refused with 400 (${bare.status} ${bare.error})`);
  const before = dep(now.body, 'main')?.lastDeploy?.id;
  await pn(S.page, (p, d) => { p.refresh(); p.select(d); return true; }, DEV);
  await waitPanel(S.page, (p) => p.actions().some((a) => a.id === 'promote' && a.enabled), null, "sales1's Promote dev → main");
  await act(S.page, 'promote');
  await waitPanel(S.page, (p) => p.diff && p.diff.files !== null, null, "sales1's promotion diff", 30000);
  const pr = await confirm(S.page, 'confirm', `Promote ${DEV} → main?`, {}, "sales1's promotion confirmation");
  check(pr.ok, `sales1's promotion asks first (${pr.d?.title})`);
  const after = await until(S.ctx, (b) => { const l = dep(b, 'main')?.lastDeploy; return !!l && l.id !== before && l.how === 'promote' && l.result !== 'running' && l.result !== 'queued'; });
  check(dep(after.body, 'main')?.lastDeploy?.result === 'ok', `through the panel, with the reviewed checkpoint, it succeeds (${JSON.stringify(dep(after.body, 'main')?.lastDeploy)})`);
  // dev1 pauses live reload: a new tab now has the tile API off
  await openPanel(P);
  await waitPanel(P, (p) => (p.header?.actions || []).includes('pause'), null, "dev1's Pause live reload");
  const pz = await confirm(P, 'pause', `Pause live reload on ${TILE}?`, {}, 'the pause confirmation');
  check(pz.ok, `Pause asks first (${pz.d?.title})`);
  await until(A.ctx, (b) => b.liveReload === '');
  await fr(P, TILE, (f) => f.open('term'));
  const j = await newShell(P);
  check(await fr(P, TILE, (f, t, idx) => f.deploy.target(idx), j) === 'off', 'with main protected and live reload paused, a new tab has the tile API off');
  await shot(P, 'deployments-protected', { fullPage: false });
}

// 8. restore through the panel: unprotect, remove dev (the box first unticked), resume
async function stepRestore(X) {
  const { check, S } = X;
  await salesPanel(X);
  let s = await stateOf(S.ctx);
  if (s.body.protectedPrimary) {
    await pn(S.page, (p) => { p.refresh(); p.select(''); return true; });
    await waitPanel(S.page, (p) => p.actions().some((a) => a.id === 'unprotect' && a.enabled), null, 'Unprotect the primary');
    await confirm(S.page, 'unprotect', 'Unprotect main?', {}, 'the unprotect confirmation');
    s = await until(S.ctx, (b) => !b.protectedPrimary);
  }
  if (dep(s.body, DEV)) {
    await pn(S.page, (p, d) => { p.refresh(); p.select(d); return true; }, DEV);
    await waitPanel(S.page, (p) => p.actions().some((a) => a.id === 'remove' && a.enabled), null, 'Remove deployment…');
    await act(S.page, 'remove');
    await waitDialog(S.page, 'the remove confirmation');
    const d1 = await dialog(S.page);
    await answer(S.page, 'ok', {});
    await waitFor(S.page, (t, a) => !!t.frameFor(a)?.testApi().dialog?.error, TILE, { timeout: 10000, label: 'the remove dialog asks again' });
    const d2 = await dialog(S.page);
    check(d1?.title === `Remove deployment ${DEV}?` && d2?.error === 'Tick the box to confirm.', `Remove with the box unticked re-opens the dialog with its error (${d2?.error})`);
    await answer(S.page, 'ok', { ok: true });
    s = await until(S.ctx, (b) => !dep(b, DEV));
    check(!dep(s.body, DEV), 'dev is removed');
  }
  if (s.body.record && s.body.liveReload !== 'main') {
    await pn(S.page, (p) => { p.refresh(); return true; });
    await waitPanel(S.page, (p) => (p.header?.actions || []).includes('resume'), null, 'Resume live reload');
    await confirm(S.page, 'resume/main', 'Resume live reload on main?', {}, 'the resume confirmation');
  }
  s = await until(S.ctx, (b) => !b.record);
  check(s.status === 200 && s.body.record === false, `the panel's restore ends in the zero state (${JSON.stringify(s.body).slice(0, 100)})`);
}

// ---- the pass ----

async function run(X) {
  const { check, skip, M, A, S } = X;
  // the fixture, as the seed laid it out
  const comps = await (await M.ctx.request.get(`${URL}/api/xbin/components`)).json();
  const row = comps.find((c) => c.path === TILE);
  check(!!row && row.owner === 'org:sales' && !row.runtime, `${TILE} is seeded static, owned by org:sales (${row?.owner}, ${row?.runtime || 'static'})`);
  check((row?.uses || []).some((u) => u.target === EDGE.target && u.role === EDGE.role), `${TILE} uses ${EDGE.target} (${JSON.stringify(row?.uses)})`);
  const grants = (await (await M.ctx.request.get(`${URL}/api/xbin/grants`)).json()).grants || [];
  check(grants.some((g) => g.from === EDGE.from && g.target === EDGE.target && g.role === EDGE.role), `the ${EDGE.target} edge is granted, not pending`);
  const users = (await (await M.ctx.request.get(`${URL}/api/xbin/users`)).json()).users || [];
  const level = (id) => users.find((u) => u.id === id)?.tiles?.[TILE];
  check(level('dev1') === 'terminal' && level('infra1') === 'read', `dev1 has terminal level on ${TILE}, infra1 reads it (${level('dev1')}, ${level('infra1')})`);

  const st = await stateOf(M.ctx);
  if (st.status !== 200) { skip(`GET /api/xbin/deployments answers ${st.status} here (${st.error || 'no JSON state'}): this xbind has no tile deployments, so steps 1–8 can't run`); return; }
  check((await resetDeploys(M.ctx)).body.record === false, `${TILE} starts in the zero state`);
  // session targets are a tile-API choice (P24): dev1 needs the terminal
  // tile-API grant (D17) for its tab to call dev; the seed leaves it off
  X.devTermApi = !!users.find((u) => u.id === 'dev1')?.termApi;
  if (!X.devTermApi) {
    const r = await M.ctx.request.patch(`${URL}/api/xbin/users/dev1`, { data: { termApi: true } });
    check(r.ok(), `dev1 gets the terminal tile-API grant for this pass (${r.status()})`);
  }
  X.seededText = /<p id="v">([^<]*)</.exec(fs.readFileSync(FILE, 'utf8'))?.[1] || 'deployy';

  // what this xbind and this build can do
  const addProbe = await post(A.ctx, 'add', { tile: TILE, deployment: DEV, attach: true, dryRun: true });
  const canAdd = addProbe.status === 200;
  await openWindow(A.page);
  const layout = await hasLayout(A.page);
  if (!layout) skip("the terminal window has no Deployments layout in this build (testApi().layouts, deploy.panel, deploy.setTarget): steps 1–8's panel parts can't run");
  if (!canAdd) skip(`POST /deployments/add answers ${addProbe.status} here (${addProbe.error}): steps 1–4, 5's edge policy for real, 7 and 8's panel restore need a dev deployment`);
  if (!layout) return;

  const steps = [];
  if (canAdd) {
    steps.push(['1 (add dev)', stepAdd], ['2 (a save reaches dev; readers)', stepURL], ['3 (promote)', stepPromote], ['4 (roll back)', stepRollback]);
    const edgeProbe = await post(S.ctx, 'edge', { tile: TILE, edge: EDGE.id, policy: 'default', dryRun: true });
    if (edgeProbe.status === 501) skip(`POST /deployments/edge answers 501 here (${edgeProbe.error}): step 5's edge policy for real can't run`);
    else steps.push(['5 (edge policy)', stepEdges]);
  }
  steps.push(['5–6 (routed states)', stepRouted]);
  if (canAdd) {
    const protectProbe = await post(S.ctx, 'protect', { tile: TILE, on: true, dryRun: true });
    if (protectProbe.status === 501) skip(`POST /deployments/protect answers 501 here (${protectProbe.error}): step 7 (a protected primary) can't run`);
    else steps.push(['7 (protected primary)', stepProtect]);
    steps.push(['8 (restore through the panel)', stepRestore]);
  }
  for (const [name, fn] of steps) {
    try {
      await fn(X);
    } catch (e) {
      check(false, `step ${name}: ${e.message.split('\n')[0]}`);
      if (fn === stepAdd) { skip("steps 2–8 need step 1's dev deployment"); break; }
    }
  }
}

async function deployments(browser) {
  const c = checker('deployments');
  const M = await login(browser, 'admin', 'admin'); // puts things back
  const A = await login(browser, 'dev1', 'devpass123'); // terminal level, not a manager
  const S = await login(browser, 'sales1', 'salespass123'); // the tile's manager
  const R = await login(browser, 'infra1', 'infrapass123'); // reads it
  const X = { ...c, M, A, S, R };
  let bytes = null;
  try { bytes = fs.readFileSync(FILE); } catch { /* none */ }
  try {
    await run(X);
  } catch (e) {
    c.check(false, `deployments: ${e.message.split('\n')[0]}`);
  } finally {
    // ---- 8, whatever happened: the fixture's bytes, sessions, zero state, prefs ----
    if (bytes) fs.writeFileSync(FILE, bytes);
    if (X.devTermApi === false) await M.ctx.request.patch(`${URL}/api/xbin/users/dev1`, { data: { termApi: false } }).catch(() => { });
    for (const x of [A, S, R]) await endSessions(x.ctx).catch(() => { });
    const s = await resetDeploys(M.ctx).catch(() => null);
    if (s?.status === 200) c.check(s.body.record === false, `${TILE} is back in the zero state (${JSON.stringify(s.body).slice(0, 100)})`);
    for (const x of [A, S, R]) await dropPref(x.ctx).catch(() => { });
    for (const x of [A, S, R]) await closeCtx(x.ctx, x.page).catch(() => { });
    await M.ctx.close();
  }
  c.done();
}

module.exports = { deployments, resetDeploys, stateOf, post };
