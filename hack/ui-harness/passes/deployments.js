// hack/ui-harness/passes/deployments.js — covers D127l D127m D127p T9 T16 PO-10
// SC-PROTECT (15-test-plan §7.3, 10-ux §14.3) — tile deployments from the
// terminal window's Deployments layout (<bx-deployments>, web/bx-deploy.js),
// on apps/deployy:
//   1. dev1 adds `dev` (data empty, live reload attached): the rows (main
//      pinned, dev following the work tree), the chip, the launcher's line,
//      the tile API select's entries and D127p's default; a session switched to
//      dev says XBIN_DEPLOYMENT=dev, and switching restarts it;
//   1b. the window's panes (D129): Deployments opens full width (no
//      launcher beside it); the rows' Dev API and live reload tags, one
//      line each, the Dev API tag following a tab switch; ⇋ puts the
//      terminal beside the panel; the divider drags (the width saved in the
//      window pref), takes → and resets on a double-click; a narrow pane
//      drills down; logs keep the place beside the terminal; >_;
//   2. a save reloads the view tab (<bx-frame src="apps/deployy+dev">), not
//      the tile's pinned frame; /c/apps/deployy+dev/ serves the work tree;
//      infra1 (read) gets 403 there, and its view of the tile — the state,
//      the panel, the chip, the select — names no non-primary deployment (a
//      filtered view, not a 403);
//   2b. the tile window's head: the deploy glyph (a non-primary deployment, main pinned;
//      no pinned chip inside the window) picks what the window shows — dev:
//      its page and a +dev tag, kept in the layout across a reload; main
//      again: no tag;
//   3. Promote dev → main: the diff names the file; the bare URL then serves
//      it and the tile's frame reloads once; dev has no git line, main's
//      names deploy/main;
//   4. main rolls back from its deploy log: the bare URL serves the old page;
//      the header offers Undo when the move paused live reload;
//   5. the edges table: dev1 sees the apps/leads edge disabled with the
//      manager reason, sales1 blocks it and both see block; a routed state
//      renders an edge that can't be limited to reading as text, with its
//      refusal count, and a host-sharing net slot blocked with its reason;
//   6. a routed state renders dev's registrations (its cron job and bus
//      subscription active for dev, its ingress host dormant), the
//      registrations note, "would notify" and Run now, which confirms (seeded data) and sends the documented
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
const { URL, fs, sleep, login, closeCtx, settle, sh, fr, waitFor, openShell, usePersonalScreen, openTile, tileFrame, shot, shotEl, checker, showPickers, PICKERS } = require('../lib');

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
// post waits out the capture rate like the Go tests' postPaced: a tile
// checkpoints a burst of 10, then one per 3 s, and an op over it answers 429
// "… checkpointed too often; retry in Ns", having changed nothing — the
// passes before this one (and the next) spend the same tile's budget.
async function post(ctx, op, data) {
  for (let i = 0; ; i++) {
    const r = await ctx.request.post(`${URL}/api/xbin/deployments/${op}`, { data });
    const body = await r.json().catch(() => ({}));
    const m = r.status() === 429 && /checkpointed too often; retry in (\d+)s/.exec(body?.error || '');
    if (!m || i === 20) return { status: r.status(), body, error: body?.error || '' };
    await sleep(Number(m[1]) * 1000 + 100);
  }
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
// then the record is gone (D119c's opt-out doubles as the check). A route this
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
// panes(page): the window body's boxes (D129) — the panel (bx-deployments,
// or whichever panel shows), the divider and the terminal host
const panes = (page) => page.locator(`${sel} .panels`).evaluate((el) => {
  const box = (e) => { if (!e) return null; const r = e.getBoundingClientRect(); return { l: Math.round(r.left), r: Math.round(r.right), w: Math.round(r.width), shown: getComputedStyle(e).display !== 'none' && r.width > 0 }; };
  return { body: box(el), pane: box(el.querySelector(':scope > .pane')), vsplit: box(el.querySelector(':scope > .vsplit')), host: box(el.querySelector(':scope > .term-host')), panel: el.querySelector(':scope > .pane')?.localName || '' };
});
// tagsOf(page): the Deployments panel's side-list rows as drawn — each row's
// name and its tags, with the line boxes each tag's text takes (1: one line)
const tagsOf = (page) => page.locator(`${sel} bx-deployments nav.side .row .t`).evaluateAll((ts) => ts.map((t) => ({
  name: t.querySelector('.nm')?.textContent.trim() || '',
  shown: t.getBoundingClientRect().width > 0,
  pills: [...t.querySelectorAll('.pill')].map((p) => {
    const r = document.createRange();
    r.selectNodeContents(p);
    return { text: p.textContent.trim(), title: p.title, lines: new Set([...r.getClientRects()].map((x) => Math.round(x.top))).size };
  }),
})));
// the window pref as saved (term-sessions.js prefKey)
const prefOf = async (ctx) => {
  const r = await ctx.request.get(`${URL}/api/xbin/prefs/${encodeURIComponent(`term:${TILE.replaceAll('/', ':')}`)}`);
  return r.ok() ? await r.json().catch(() => null) : null;
};
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
    vault: { keys: 2, placeholders: 1 }, deliveries: true, alwaysOn: false, alwaysOnDeclared: false,
    registrations: [
      { kind: 'cron', name: 'nightly', schedule: '0 3 * * *', path: '/tick', dormant: false },
      { kind: 'bus', name: 'trig-1', resource: 'res:apps/leads/events', prefix: 'orders/', path: '/trigger/bus/1', dormant: false },
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
  // D129: a window without sessions opens Deployments full width — no launcher beside it
  const full = await panes(P);
  check(full.pane?.shown && Math.abs(full.pane.w - full.body.w) <= 1 && !full.host?.shown && !full.vsplit && !(await P.locator(`${sel} .launcher`).isVisible()),
    `Deployments opens full width, no launcher beside it (${JSON.stringify(full)})`);
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
  check(!!main?.primary && /^c:[0-9a-f]{7,}$/.test(main?.code || '') && main.icon === 'pin' && dv?.code === 'work tree' && dv.icon === 'live',
    `the rows: main pinned to a checkpoint, dev following the work tree (${JSON.stringify(rows.map((r) => [r.name, r.code, r.icon]))})`);
  // the window's own state follows the add's deployments event (debounced)
  await waitFor(P, (t, a) => t.frameFor(a.tile)?.testApi().deploy.chip?.text === a.want, { tile: TILE, want: `Live reload: ${DEV}` },
    { timeout: 10000, label: 'the chip follows the add' }).catch(() => { });
  const chip = await fr(P, TILE, (f) => f.deploy.chip);
  check(chip?.text === `Live reload: ${DEV}`, `the chip reads Live reload: dev (${chip?.text})`);
  // the launcher (the window without sessions) says what a new session calls
  await fr(P, TILE, (f) => f.open('term'));
  await settle(P);
  const launcher = (await P.locator(`${sel} .launcher`).innerText().catch(() => '')).replace(/\s+/g, ' ');
  check(launcher.includes('· target: main'), `the launcher's line reads · target: main (${launcher.slice(0, 160)})`);
  // a new tab: the tile API select's entries, D127p's default
  const i = await newShell(P);
  const opts = await fr(P, TILE, (f, t, idx) => f.deploy.apiOptions(idx).map((o) => [o.value, o.label]), i);
  check(JSON.stringify(opts) === JSON.stringify([['primary', 'target: main (primary)'], [DEV, `target: ${DEV}`], ['off', 'no API']]),
    `the tile API select offers main (primary), dev and no API (${JSON.stringify(opts)})`);
  check(await fr(P, TILE, (f, t, idx) => f.deploy.target(idx), i) === 'primary', "the new tab calls the primary (D127p's default)");
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

// 1b. the window's panes and the panel's tags (D129): with a session,
// Deployments still opens full width; its rows tag the active tab's target
// "Dev API" and the live reload target "live reload" (after the live glyph), each on one line,
// and the Dev API tag follows a tab switch; ⇋ puts the terminal beside the
// panel, the divider drags (the width is saved in the window pref, the
// layout is not), takes the arrow keys and resets on a double-click; a
// narrow pane drills down to the side list; another panel keeps the place
// beside the terminal; >_ shows the terminal alone.
async function stepPanes(X) {
  const { check, A } = X, P = A.page, i = X.followerTab;
  // the window follows its card: bring the card to the top, so the window
  // (and the clicks and shots below) are on screen
  await P.locator(`.card[data-path="${TILE}"]`).evaluate((el) => el.scrollIntoView({ block: 'start' }));
  // a second tab, calling dev (the first calls the primary)
  await fr(P, TILE, (f) => f.open('term'));
  const j = await newShell(P);
  const before = await fr(P, TILE, (f, t, idx) => f.tabs[idx].id, j);
  await fr(P, TILE, (f, t, a) => { f.deploy.setTarget(a.j, a.to); return true; }, { j, to: DEV });
  await waitDialog(P, 'the restart confirmation (dev)');
  await answer(P, 'ok');
  await waitFor(P, (t, a) => { const f = t.frameFor(a.tile)?.testApi(); return !!f?.tabs[a.j]?.id && f.tabs[a.j].id !== a.before && f.deploy.target(a.j) === a.to; },
    { tile: TILE, j, before, to: DEV }, { timeout: 20000, label: 'the second tab restarted onto dev' });

  await fr(P, TILE, (f, t, idx) => { f.setActiveTab(idx); return true; }, i);
  await openPanel(P);
  await waitPanel(P, (p) => p.rows.length >= 2, null, 'the rows');
  await settle(P);
  const full = await panes(P);
  check(full.pane?.shown && Math.abs(full.pane.w - full.body.w) <= 1 && !full.host?.shown && !full.vsplit,
    `with a session too, Deployments opens full width (${JSON.stringify(full)})`);
  const tags = await tagsOf(P);
  const pills = (name) => tags.find((r) => r.name === name)?.pills || [];
  const devApi = pills('main').find((p) => p.text === 'Dev API'), lr = pills(DEV).find((p) => p.text === 'live reload');
  check(!!devApi && devApi.lines === 1 && /^Dev API: this tab's API calls and bx commands reach main, the primary/.test(devApi.title) && !pills(DEV).some((p) => p.text === 'Dev API'),
    `the active tab calls the primary: main's row reads Dev API, on one line, with its tooltip (${JSON.stringify(tags)})`);
  check(!!lr && lr.lines === 1 && lr.title === `Live reload: saves reach ${TILE}+${DEV}.`, `dev's row reads live reload, on one line (${JSON.stringify(pills(DEV))})`);
  check(!tags.some((r) => r.pills.some((p) => /target of this terminal/.test(p.text))), 'no row says "target of this terminal"');
  await shotEl(P, `${sel} .pop`, 'deployments-full');
  await fr(P, TILE, (f, t, idx) => { f.setActiveTab(idx); return true; }, j);
  await settle(P);
  const after = await tagsOf(P);
  check(after.find((r) => r.name === DEV)?.pills.some((p) => p.text === 'Dev API') && !after.find((r) => r.name === 'main')?.pills.some((p) => p.text === 'Dev API'),
    `the Dev API tag follows the active tab to dev (${JSON.stringify(after.map((r) => [r.name, r.pills.map((p) => p.text)]))})`);

  // ⇋: the terminal beside the panel
  const lyt = P.locator(`${sel} ${PICKERS} .lyt`).first();
  const toggle = lyt.locator('button.beside');
  check(await toggle.getAttribute('title') === 'Deployments beside the terminal', `⇋ names the panel (${await toggle.getAttribute('title')})`);
  await toggle.click();
  await settle(P);
  const side = await panes(P);
  const w0 = await fr(P, TILE, (f) => f.paneW);
  check(await fr(P, TILE, (f) => f.beside && f.layout === 'deployments') && side.pane?.shown && side.host?.shown && side.vsplit?.shown
    && side.pane.r <= side.vsplit.l + 1 && side.vsplit.r <= side.host.l + 1 && Math.abs(side.pane.w - (side.body.w * w0) / 100) <= 2,
    `Deployments sits beside the terminal, split at ${w0}% by the divider (${JSON.stringify(side)})`);
  const vs = P.locator(`${sel} .vsplit`);
  check(await vs.getAttribute('role') === 'separator' && await vs.evaluate((el) => getComputedStyle(el).touchAction) === 'none', 'the divider is a separator with touch-action: none');
  const beside = await tagsOf(P);
  check(beside.filter((r) => r.shown).every((r) => r.pills.every((p) => p.lines === 1)) && beside.some((r) => r.pills.some((p) => p.text === 'Dev API')),
    `beside the terminal every tag stays on one line (${JSON.stringify(beside.map((r) => [r.name, r.pills.map((p) => `${p.text}:${p.lines}`)]))})`);
  await shotEl(P, `${sel} .pop`, 'deployments-beside');

  // drag the divider 180 px left: the width follows and is saved (the
  // Deployments layout itself never is)
  const vb = await vs.boundingBox();
  const x0 = vb.x + vb.width / 2, y0 = vb.y + vb.height / 2;
  await P.mouse.move(x0, y0);
  await P.mouse.down();
  for (let k = 1; k <= 6; k++) await P.mouse.move(x0 - 30 * k, y0);
  await P.mouse.up();
  await settle(P);
  const w1 = await fr(P, TILE, (f) => f.paneW);
  const dragged = await panes(P);
  const want = ((x0 - 180 - dragged.body.l) / dragged.body.w) * 100;
  check(Math.abs(w1 - want) <= 1.5 && Math.abs(dragged.pane.w - (side.pane.w - 180)) <= 3, `dragging the divider 180 px left narrows the panel (${w0}% → ${w1}%, want ≈${want.toFixed(1)}%; ${side.pane.w} → ${dragged.pane.w} px)`);
  let pref = null;
  for (let k = 0; k < 25 && !(pref && pref.paneW === w1); k++) { await sleep(200); pref = await prefOf(A.ctx); }
  check(pref?.paneW === w1 && pref.layout === 'term' && !('beside' in pref), `the width is saved in the window pref, the Deployments layout is not (${JSON.stringify(pref && { layout: pref.layout, beside: pref.beside, paneW: pref.paneW })})`);
  // narrow now: the panel drills down to its side list, the tags still on one line
  const narrow = await tagsOf(P);
  const mainShown = await P.locator(`${sel} bx-deployments section.main`).evaluate((el) => getComputedStyle(el).display !== 'none');
  check(dragged.pane.w <= 720 && !mainShown && narrow.filter((r) => r.shown).length >= 2 && narrow.every((r) => r.pills.every((p) => p.lines === 1)),
    `a ${dragged.pane.w} px pane shows the side list alone, every tag on one line (${JSON.stringify(narrow.map((r) => [r.name, r.pills.map((p) => `${p.text}:${p.lines}`)]))})`);
  await shotEl(P, `${sel} .pop`, 'deployments-beside-narrow');
  // the keyboard and the double-click
  await vs.focus();
  await P.keyboard.press('ArrowRight');
  await settle(P);
  const w2 = await fr(P, TILE, (f) => f.paneW);
  check(Math.abs(w2 - (w1 + 2)) <= 0.2, `→ on the focused divider widens the panel 2 % (${w1}% → ${w2}%)`);
  await vs.dblclick();
  await settle(P);
  check(await fr(P, TILE, (f) => f.paneW) === 55, `a double-click resets the divider (${await fr(P, TILE, (f) => f.paneW)}%)`);

  // another panel keeps the place beside the terminal; >_ shows it alone
  await lyt.locator('button[title="backend logs (read-only)"]').click();
  await settle(P);
  const logs = await panes(P);
  check(await fr(P, TILE, (f) => f.layout === 'logs' && f.beside) && logs.panel === 'bx-logs' && logs.host?.shown && logs.vsplit?.shown,
    `logs take the place beside the terminal (${JSON.stringify(logs)})`);
  await lyt.locator('button[title="terminal only"]').click();
  await settle(P);
  const alone = await panes(P);
  check(await fr(P, TILE, (f) => f.layout === 'term' && !f.beside) && !alone.pane && !alone.vsplit && alone.host?.shown && Math.abs(alone.host.w - alone.body.w) <= 1,
    `>_ shows the terminal alone (${JSON.stringify(alone)})`);
  await fr(P, TILE, (f, t, idx) => { f.closeTab(idx); return true; }, j);
  await fr(P, TILE, (f, t, idx) => { f.setActiveTab(idx); return true; }, i);
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

// 2b. the window head: the deploy glyph picks the deployment the tile's window shows
async function stepWindow(X) {
  const { check, A } = X, P = A.page;
  const head = P.locator(`bx-canvas .card[data-path="${TILE}"] .head`);
  const frame = P.locator(`bx-canvas .card[data-path="${TILE}"] .cbody > bx-frame`); // not the Deployments panel's view tab
  const menu = P.locator('bx-canvas bx-menu[open]');
  const pick = async (name) => {
    await head.locator('button.dpb').click();
    await menu.waitFor({ timeout: 10000 });
    await menu.locator('button.it').filter({ has: P.locator('.lb', { hasText: new RegExp(`^${name}$`) }) }).click();
    await menu.waitFor({ state: 'detached', timeout: 10000 });
  };
  const icon = head.locator('button.dpb');
  check(await icon.count() === 1, `the window head carries the deploy button (${await icon.getAttribute('title').catch(() => 'none')})`);
  check(await P.locator(`bx-canvas .card[data-path="${TILE}"] .frame-wrap .dchip`).count() === 0, 'no pinned chip inside the tile window');
  await icon.click();
  await menu.waitFor({ timeout: 10000 });
  const labels = await menu.locator('button.it .lb').allTextContents();
  check(labels.includes('main') && labels.includes(DEV) && labels.includes('Deployments…'), `the deploy menu: main, ${DEV}, Deployments… (${JSON.stringify(labels)})`);
  await shot(P, 'deployments-window-menu', { fullPage: false });
  await P.keyboard.press('Escape');
  await menu.waitFor({ state: 'detached', timeout: 10000 });
  await pick(DEV);
  await head.locator('.dtag').waitFor({ timeout: 10000 });
  check((await head.locator('.dtag').textContent()).trim() === `+${DEV}` && await frame.getAttribute('deployment') === DEV, 'picked dev: the head says +dev, the frame shows dev');
  check((await docText(P, `/c/${TILE}+${DEV}/`, X.v1)).includes(X.v1), "the tile's window shows dev's page (the work tree's save)");
  await shot(P, 'deployments-window-dev', { fullPage: false });
  await sh(P, (t) => t?.flushSave?.());
  await openShell(P);
  await head.locator('.dtag').waitFor({ timeout: 15000 }).catch(() => {});
  check(await head.locator('.dtag').count() === 1 && await frame.getAttribute('deployment') === DEV, 'kept in the layout: after a reload the window shows dev again');
  await pick('main');
  await sleep(300);
  check(await head.locator('.dtag').count() === 0 && await frame.getAttribute('deployment') === null, 'picked main: no tag, the frame shows the primary');
  await sh(P, (t) => t?.flushSave?.());
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
  // 6: registrations (cron and bus active for dev, ingress dormant), "would notify", Run now (seeded data: it asks)
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
    check(pill('cron') === 'active' && pill('bus') === 'active' && pill('ingress-host') === 'dormant — routes reach the primary only',
      `dev's cron job and subscription active, its ingress host dormant (${JSON.stringify(regs.map((r) => [r.kind, r.pill]))})`);
    const note = await pn(P, (p) => p.text());
    check(/cron jobs and bus subscriptions fire for dev, with its own data\. Its interface instances and ingress hosts stay dormant/.test(note || ''), 'the registrations tab says what fires and what stays dormant');
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
  // session targets are a tile-API choice (D127p): dev1 needs the terminal
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
    steps.push(['1 (add dev)', stepAdd], ['1b (panes and tags)', stepPanes], ['2 (a save reaches dev; readers)', stepURL], ['2b (the window shows dev)', stepWindow], ['3 (promote)', stepPromote], ['4 (roll back)', stepRollback]);
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

module.exports = { deployments, resetDeploys, stateOf, post, openWindow, openPanel, pn, waitPanel, act, waitDialog, dialog, answer, newShell, endSessions, dropPref };
