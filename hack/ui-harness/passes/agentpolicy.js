// hack/ui-harness/passes/agentpolicy.js — org constraints on agents, a
// person's agent preferences and opening a session from outside its tile's
// window (D229), end to end with the scripted "fake" provider on
// apps/focusy (org devs's):
//   (a) the launcher's last choice, kept in this browser before, moves to
//       the server on the first window that opens, and a mode picked in a
//       session is what the next session of that harness starts in — never
//       a bypass mode;
//   (b) a row the directory marks origin "agent" reads "started by your
//       agent" on its tab (the listing is served: only xbind sets origin);
//   (c) a link — ?tile=…&session=<id> — lands on the tile with its window
//       open on that session, and the URL drops the request; another
//       tile's page asks for a session (xbin.openSession) and gets it;
//       a session that ended opens as its transcript; an unknown one says so;
//   (d) an org admin narrows their org's harness list: the launcher offers
//       only what is left (the `agents` event), a start of another harness
//       is refused with the reason in the tab, a running session keeps
//       running and can't be restarted; they can't touch another org or the
//       workspace's bounds; the connection view names the block;
//       a models rule makes the tile's sessions connector-only — with no
//       LLM connector on the harness xbind no harness is offered, and the
//       chooser says why;
//   (e) the admin console's organisations tab edits an org's rules, its pin
//       and the workspace's bounds (<bx-org-agents>, /vendor/org-agents.js):
//       a harness list, private models, a save that sends only what
//       changed, the server's refusal of a list past the bounds in its
//       words;
//   (f) a pin: the org's admin sees it in the organisations tile, narrows
//       below it there, can't loosen it, and the connection view's reason
//       says the rule is pinned.
// Everything it set — rules, preferences, sessions — is put back at the end.
const { URL, login, closeCtx, settle, sleep, sh, fr, waitFor, waitSel, openShell, usePersonalScreen, openTile, tileFrame, gotoTab, shot, shotEl, checker, THEME } = require('../lib');

const TILE = 'apps/focusy', OTHER = 'apps/linky', ORG = 'devs';
const VIEW = { viewport: { width: 1400, height: 1300 } };
const FAKE = 'Fake agent (tests)';
const enc = encodeURIComponent;
const LISTING = /\/api\/xbin\/term\/sessions\?cwd=/; // the frame's listing of its tile's sessions

async function agentPolicy(browser) {
  const { check, done } = checker(`agentpolicy-${THEME}`);
  const A = await login(browser, 'admin', 'admin', VIEW);
  const api = (ctx) => ({
    get: async (p) => { const r = await ctx.request.get(`${URL}/api/xbin${p}`); return { status: r.status(), body: await r.json().catch(() => null) }; },
    put: async (p, data) => { const r = await ctx.request.put(`${URL}/api/xbin${p}`, { data }); return { status: r.status(), body: await r.json().catch(() => null) }; },
    post: async (p, data) => { const r = await ctx.request.post(`${URL}/api/xbin${p}`, { data }); return { status: r.status(), body: await r.json().catch(() => null) }; },
    del: async (p) => (await ctx.request.delete(`${URL}/api/xbin${p}`)).status(),
  });
  const a = api(A.ctx);
  const noRules = { workspaceAgent: null, harnesses: null, models: null };
  // the admin's screens and the tile's window as they were: later passes
  // (deepLinks) read the place the shell shows
  const layoutURL = `${URL}/api/xbin/prefs/layout`, windowURL = `${URL}/api/xbin/prefs/${enc(`term:${TILE.replaceAll('/', ':')}`)}`;
  const got0 = await A.ctx.request.get(layoutURL);
  const layout0 = got0.ok() ? await got0.json().catch(() => null) : null; // none saved yet (a fresh workspace): nothing to put back
  const tidy = async () => {
    await a.put(`/orgs/${ORG}/agents`, noRules);
    await a.put(`/orgs/${ORG}/agents/pin`, noRules);
    await a.put('/agents/policy', { ...noRules, credentials: null });
    for (const s of (await a.get(`/term/sessions?cwd=${enc(TILE)}`)).body || []) await a.del(`/term/sessions/${enc(s.id)}`);
    await a.put('/agent/prefs', { harness: '', lastKind: '', harnesses: { fake: null }, agentSessions: { start: 'ask', modes: null, net: null, api: null } });
  };
  await tidy();
  const openWindow = async (page) => {
    await openShell(page);
    await usePersonalScreen(page);
    await openTile(page, TILE);
    await page.locator(`.card[data-path="${TILE}"]`).evaluate((el) => el.scrollIntoView({ block: 'start' }));
    await fr(page, TILE, (f) => f.open('term'));
  };
  const tabs = (page) => fr(page, TILE, (f) => ({ tabs: f.tabs, active: f.activeTab, open: f.terminalOpen }));
  const activeId = async (page) => { const t = await tabs(page); return t.tabs[t.active]?.id ?? null; };
  const waitActive = (page, id, label) => waitFor(page, (t, a) => { const f = t?.frameFor(a.tile)?.testApi(); return !!f && f.terminalOpen && f.tabs[f.activeTab]?.id === a.id; }, { tile: TILE, id }, { timeout: 15000, label });
  const startFake = async (page) => {
    const before = (await tabs(page)).tabs.map((x) => x.id);
    await fr(page, TILE, (f) => f.startKind('agent', 'fake'));
    await waitFor(page, (t, a) => { const f = t.frameFor(a.tile)?.testApi(); const x = f?.tabs[f.activeTab]; return !!x?.id && !a.before.includes(x.id); }, { tile: TILE, before }, { timeout: 20000, label: 'the fake session started' });
    const id = await activeId(page);
    await waitFor(page, (t, a) => t.frameFor(a)?.testApi().agent()?.status === 'idle', TILE, { timeout: 20000, label: 'the agent is idle' });
    return id;
  };

  try {
    // ---- (a) preferences: this browser's record moves to the server ----
    await A.ctx.addInitScript(() => { try { if (!localStorage.getItem('bx-agentpolicy-seeded')) { localStorage.setItem('bx-term-lastkind', JSON.stringify({ kind: 'agent', provider: 'fake' })); localStorage.setItem('bx-agentpolicy-seeded', '1'); } } catch { /* a document without storage */ } });
    check((await a.get('/agent/prefs')).body?.lastKind === '', 'the server keeps no last choice yet');
    await openWindow(A.page);
    let prefs;
    for (const until = Date.now() + 10000; Date.now() < until; await sleep(150)) {
      prefs = (await a.get('/agent/prefs')).body;
      if (prefs?.lastKind) break;
    }
    check(prefs?.lastKind === 'agent' && prefs?.harness === 'fake', `this browser's last choice moved to the server (${JSON.stringify({ lastKind: prefs?.lastKind, harness: prefs?.harness })})`);
    await waitFor(A.page, (t, a) => (t.frameFor(a.tile)?.testApi().launcherItems() || [])[0] === `${a.name}  ·  last`, { tile: TILE, name: FAKE }, { timeout: 10000, label: 'the launcher leads with the last choice' });
    check(true, 'the launcher leads with the last choice');

    const s1 = await startFake(A.page);
    check(!!s1, `a fake agent session on ${TILE} (${s1})`);
    const row1 = (await a.get(`/term/sessions/${enc(s1)}`)).body?.session;
    check(row1?.mode === 'ask' && !row1?.origin, `it starts in the harness's default mode, with no origin (${row1?.mode}, ${row1?.origin})`);
    // what is picked in a session is remembered for the harness; a bypass mode is not
    const kept = async (pred) => {
      for (const until = Date.now() + 10000; Date.now() < until; await sleep(150)) {
        prefs = (await a.get('/agent/prefs')).body;
        if (pred(prefs?.harnesses?.fake || {})) return true;
      }
      return false;
    };
    const modeOf = async (id) => (await a.get(`/term/sessions/${enc(id)}`)).body?.session?.mode;
    await fr(A.page, TILE, (f) => f.agent().setOption('model', 'fake-fast'));
    check(await kept((h) => h.model === 'fake-fast'), `the model picked in a session is kept for the harness (${JSON.stringify(prefs?.harnesses)})`);
    await fr(A.page, TILE, (f) => f.agent().setOption('mode', 'yolo'));
    for (const until = Date.now() + 10000; Date.now() < until; await sleep(150)) if (await modeOf(s1) === 'yolo') break;
    await sleep(500);
    prefs = (await a.get('/agent/prefs')).body;
    check(await modeOf(s1) === 'yolo' && !prefs?.harnesses?.fake?.mode, `a bypass mode is the session's, never kept (${JSON.stringify(prefs?.harnesses)})`);
    const refused = await a.put('/agent/prefs', { harnesses: { fake: { mode: 'yolo' } } });
    check(refused.status === 400 && /picked per session/.test(refused.body?.error || ''), `the server refuses to keep one (${refused.status} ${refused.body?.error})`);
    await fr(A.page, TILE, (f) => f.agent().setOption('mode', 'ask'));
    check(await kept((h) => h.mode === 'ask'), `a mode that keeps asking is kept (${JSON.stringify(prefs?.harnesses)})`);
    const s2 = await startFake(A.page);
    const row2 = (await a.get(`/term/sessions/${enc(s2)}`)).body?.session;
    check(row2?.mode === 'ask' && row2?.model === 'fake-fast', `the next session of the harness starts with what was kept (${row2?.mode}, ${row2?.model})`);
    await shotEl(A.page, `bx-frame[src="${TILE}"] .pop`, `agentpolicy-window-${THEME}`);

    // ---- (b) a session the person's workspace agent started says so ----
    await A.page.route(LISTING, async (route) => {
      const r = await route.fetch();
      const rows = await r.json().catch(() => []);
      await route.fulfill({ response: r, json: Array.isArray(rows) ? rows.map((x) => (x.id === s1 ? { ...x, origin: 'agent' } : x)) : rows });
    });
    await A.ctx.request.patch(`${URL}/api/xbin/term/sessions/${enc(s1)}`, { data: { name: 'triage' } }); // a `term` event: the frame lists again
    await waitFor(A.page, (t, a) => t.frameFor(a.tile)?.testApi().tabs.find((x) => x.id === a.id)?.origin === 'agent', { tile: TILE, id: s1 }, { timeout: 10000, label: 'the tab knows its origin' });
    const tab = await A.page.locator(`bx-frame[src="${TILE}"] .ptab`, { hasText: 'triage' }).first().evaluate((el) => ({ note: el.querySelector('.note')?.textContent || '', title: el.getAttribute('title') || '' }));
    check(tab.note === 'your agent' && /started by your agent/.test(tab.title), `its tab says who started it (${JSON.stringify(tab)})`);
    const others = await A.page.locator(`bx-frame[src="${TILE}"] .ptab .note`).count();
    check(others === 1, `a session the person opened carries no mark (${others} marked)`);
    await shotEl(A.page, `bx-frame[src="${TILE}"] .pop`, `agentpolicy-origin-${THEME}`);
    await A.page.unroute(LISTING);

    // ---- (c) a session opens from outside its tile's window ----
    await fr(A.page, TILE, (f) => f.closeTerminal());
    await A.page.goto(`${URL}/c/root/?tile=${TILE}&session=${s1}`);
    await waitActive(A.page, s1, 'the link opened the window on its session');
    check(true, 'a link with &session= lands on the tile with its window open on that session');
    await waitFor(A.page, () => !new globalThis.URL(location.href).searchParams.has('session'), null, { timeout: 10000, label: 'the request left the URL' });
    const q = new globalThis.URL(A.page.url()).searchParams;
    check(q.get('tile') === TILE && !q.has('session'), `the URL keeps the place and drops the request (${A.page.url()})`);
    check(await sh(A.page, (t) => !t.routeMessage), 'no message for a session that is the viewer\'s');
    // another tile's page asks for the other session
    await openTile(A.page, OTHER);
    const other = await tileFrame(A.page, OTHER);
    await other.waitForFunction(() => !!window.xbin?.openSession, null, { timeout: 10000 });
    const shown = await other.evaluate((x) => window.xbin.openSession(x), { tile: TILE, session: s2 });
    await waitActive(A.page, s2, 'the other tile\'s request showed the session');
    check(shown === true, `xbin.openSession from another tile's page shows it and says so (${shown})`);
    const nope = await other.evaluate((x) => window.xbin.openSession(x), { tile: 'apps/no-such-tile', session: s2 });
    check(nope === false && await activeId(A.page) === s2, `a tile the viewer doesn't list: nothing happens, and the page is told (${nope})`);
    const bad = await other.evaluate(() => window.xbin.openSession({ tile: '../etc', session: 'a b' }));
    check(bad === false, `a malformed request goes nowhere (${bad})`);
    const unknown = await fr(A.page, TILE, (f) => f.open({ session: 'nope1234' }));
    check(unknown === false && await activeId(A.page) === s2, `open({session}) with an id that isn't the viewer's resolves false and moves nothing (${unknown})`);
    // a session that ended: its transcript, in a browser that never had its tab
    await fr(A.page, TILE, (f, t, id) => { const i = f.tabs.findIndex((x) => x.id === id); f.setActiveTab(i); }, s1);
    await waitFor(A.page, (t, a) => t.frameFor(a.tile)?.testApi().agent()?.sessionId === a.id, { tile: TILE, id: s1 }, { timeout: 10000, label: 's1 in view' });
    await fr(A.page, TILE, (f) => f.agent().send('hello'));
    await waitFor(A.page, (t, a) => { const g = t.frameFor(a)?.testApi().agent(); return g?.status === 'idle' && g.blocks.some((b) => b.kind === 'msg' && b.role === 'user'); }, TILE, { timeout: 20000, label: 'the turn ended' });
    check(await a.del(`/term/sessions/${enc(s1)}`) === 204, 'the session is ended');
    const B = await login(browser, 'admin', 'admin', VIEW);
    await B.page.goto(`${URL}/c/root/?tile=${TILE}&session=${s1}`);
    await waitFor(B.page, (t, a) => { const f = t?.frameFor(a.tile)?.testApi(); return !!f && f.terminalOpen && f.tabs[f.activeTab]?.history === a.id; }, { tile: TILE, id: s1 }, { timeout: 20000, label: 'the ended session opened as its transcript' });
    check(true, 'a link to a session that ended opens its transcript, read-only');
    await B.page.goto(`${URL}/c/root/?tile=${TILE}&session=gone4321`);
    await waitFor(B.page, (t) => !!t?.routeMessage, null, { timeout: 15000, label: 'the message for a session that is gone' });
    const msg = await sh(B.page, (t) => t.routeMessage);
    check(msg.length === 1 && /session/.test(msg[0]) && new RegExp(TILE).test(msg[0]), `a link to a session that isn't there says so (${JSON.stringify(msg)})`);
    await closeCtx(B.ctx, B.page);

    // ---- (d) the org narrows its harnesses ----
    const D = await login(browser, 'dev1', 'devpass123', VIEW);
    const d = api(D.ctx);
    check((await d.put('/agents/policy', { harnesses: ['claude'] })).status === 403, 'an org admin can\'t set the workspace\'s bounds');
    check((await d.put('/orgs/sales/agents', { harnesses: ['claude'] })).status === 403, 'nor another org\'s rules');
    const set = await d.put(`/orgs/${ORG}/agents`, { harnesses: ['claude'] });
    check(set.status === 200 && JSON.stringify(set.body?.agents?.harnesses) === '["claude"]', `an org admin narrows their own org's harnesses (${set.status} ${JSON.stringify(set.body?.agents)})`);
    await D.ctx.close(); // (not closeCtx: dev1's shell has shown nothing of ours, and flushing it would save its first layout unfiltered)
    await waitFor(A.page, (t, a) => { const items = t.frameFor(a.tile)?.testApi().launcherItems() || []; return items.includes('Claude Code') && !items.some((x) => x.startsWith(a.name)); }, { tile: TILE, name: FAKE }, { timeout: 10000, label: 'the launcher follows the rule' });
    const items = await fr(A.page, TILE, (f) => f.launcherItems());
    check(items.includes('Bash') && items.includes('Claude Code') && !items.includes('Codex') && !items.some((x) => x.startsWith(FAKE)), `the launcher offers only what the org allows, and no stale "last" (${JSON.stringify(items)})`);
    const provs = (await a.get(`/agent/providers?cwd=${enc(TILE)}`)).body.map((p) => p.id);
    check(provs.join() === 'claude', `GET /agent/providers?cwd= lists the allowed harness (${provs})`);
    check((await a.get('/agent/providers')).body.length > 1, 'without cwd the catalog is whole');
    check((await a.get(`/agent/providers?cwd=${enc('apps/dev1-notes')}`)).body.length > 1, 'a tile in no org is not narrowed');
    // a start of the harness the org dropped: refused, with the reason in the tab
    await fr(A.page, TILE, (f) => f.startKind('agent', 'fake'));
    await waitFor(A.page, (t, a) => /allows these coding agents only/.test(t.frameFor(a)?.testApi().agent()?.error || ''), TILE, { timeout: 15000, label: 'the refusal shows in the tab' });
    const why = await fr(A.page, TILE, (f) => f.agent().error);
    check(why === 'Devs allows these coding agents only: claude (not fake)', `the tab says why it didn't start (${why})`);
    await shotEl(A.page, `bx-frame[src="${TILE}"] .pop`, `agentpolicy-refused-${THEME}`);
    // the session that was running keeps running, and can't be restarted
    const still = await a.get(`/term/sessions/${enc(s2)}`);
    check(still.status === 200 && still.body?.session?.status !== 'exited', `the running session is untouched (${still.status} ${still.body?.session?.status})`);
    const restart = await a.post(`/term/sessions/${enc(s2)}/restart`, { net: 'none' });
    check(restart.status === 403 && /keeps running/.test(restart.body?.error || '') && (await a.get(`/term/sessions/${enc(s2)}`)).status === 200, `a restart is refused before it ends it (${restart.status} ${restart.body?.error})`);
    const resume = await a.post('/term/sessions', { cwd: TILE, kind: 'agent', resume: s1 });
    check(resume.status === 403 && /can't be resumed/.test(resume.body?.error || ''), `a past session of that harness can't be resumed (${resume.status} ${resume.body?.error})`);
    check((await a.get(`/agent/history/${enc(s1)}/events?limit=5`)).status === 200, 'its transcript stays readable');
    // the connection view names the block
    await a.put(`/orgs/${ORG}/agents`, { models: 'private' });
    const conn = (await a.get('/agents/connection?model=hosted/big')).body;
    const row = conn?.tiles?.find((x) => x.tile === TILE);
    check(row?.state === 'blocked' && row.reason === 'Devs allows private models only' && row.by === ORG && JSON.stringify(row.harnesses) === '["claude"]', `the connection view: ${JSON.stringify(row)}`);
    check(/ not connected$/.test(conn?.counts?.summary || '') && conn.counts.blocked >= 1 && conn.orgs.find((o) => o.id === ORG)?.state === 'blocked', `its totals: ${conn?.counts?.summary}`);
    const scoped = (await a.get(`/agents/connection?orgs=${ORG}`)).body;
    check(scoped?.tiles?.some((x) => x.state === 'hidden') && scoped.tiles.find((x) => x.tile === TILE)?.state === 'blocked', 'a scope hides the rest; blocked still says blocked');
    // the models rule binds the tile's own agent sessions: connector-only, and
    // this xbind has no LLM connector, so nothing is offered — and says why
    check(row?.credentials === 'connector-only' && row.credentialsReason === 'Devs allows private models only' && /no LLM connector/.test(row.unavailable?.claude || ''),
      `the connection view says what a session there runs on (${row?.credentials}; ${JSON.stringify(row?.unavailable)})`);
    check((await a.get(`/agent/providers?cwd=${enc(TILE)}`)).body.length === 0, 'no harness is offered on a tile whose sessions must run on a connector there isn\'t');
    const allRows = (await a.get(`/agent/providers?cwd=${enc(TILE)}&all=1`)).body;
    check(allRows.length > 1 && allRows.every((p) => p.unavailable) && /Devs allows private models only, so a coding agent here runs on the workspace's LLM connector — and claude can't: this xbind has no LLM connector set up/.test(allRows.find((p) => p.id === 'claude').unavailable),
      `&all=1 keeps them with the reason (${allRows.find((p) => p.id === 'claude')?.unavailable})`);
    await waitFor(A.page, (t, a) => (t.frameFor(a)?.testApi().notOffered || []).some((x) => x.id === 'claude' && /LLM connector/.test(x.why)), TILE, { timeout: 10000, label: 'the chooser knows why' });
    const li = await fr(A.page, TILE, (f) => f.launcherItems());
    check(li.includes('Bash') && !li.includes('Claude Code'), `the launcher offers Bash alone (${JSON.stringify(li)})`);
    // …and so does the Agent tab's own chooser (an agent tab opened with no
    // harness picked: the chat kit's composer, D224): each harness that is
    // not offered here is listed with its reason, under the pickers
    const tabsBefore = await fr(A.page, TILE, (f) => f.tabs.length);
    await fr(A.page, TILE, (f) => f.startKind('agent'));
    await waitFor(A.page, (t, a) => (t.frameFor(a)?.testApi().agent()?.notOffered || []).some((x) => x.id === 'claude'), TILE, { timeout: 10000, label: 'the Agent tab\'s chooser lists what is not offered' });
    const listed = await fr(A.page, TILE, (f) => f.agent().notOffered);
    check(listed.length === allRows.length && /^Claude Code isn’t offered here: Devs allows private models only, so a coding agent here runs on the workspace's LLM connector — and claude can't/.test(listed.find((x) => x.id === 'claude').text),
      `the Agent tab's chooser says which harnesses are not offered here, and why (${JSON.stringify(listed.find((x) => x.id === 'claude'))})`);
    await fr(A.page, TILE, (f, t, n) => { for (let i = f.tabs.length - 1; i >= n; i--) f.closeTab(i); }, tabsBefore);
    const denied = await a.post('/term/sessions', { cwd: TILE, kind: 'agent', provider: 'claude' });
    check(denied.status === 403 && /runs on the workspace's LLM connector/.test(denied.body?.error || ''), `a start is refused with it (${denied.status})`);
    check((await a.get(`/agents/connection`)).body.tiles.find((x) => x.tile === 'apps/dev1-notes')?.credentials === 'own-first', 'a tile with no models rule runs as the workspace\'s mode says');

    // back to no rules: the launcher offers the fake again
    await a.put(`/orgs/${ORG}/agents`, noRules);
    await waitFor(A.page, (t, a) => (t.frameFor(a.tile)?.testApi().launcherItems() || []).some((x) => x.startsWith(a.name)), { tile: TILE, name: FAKE }, { timeout: 10000, label: 'the launcher offers it again' });
    check(true, 'with the rule gone the launcher offers the harness again');

    // ---- (e) the admin console's editor ----
    const C = await login(browser, 'admin', 'admin', { viewport: { width: 1200, height: 1100 } });
    const until = async (fn, label, timeout = 10000) => {
      for (const end = Date.now() + timeout; Date.now() < end; await sleep(100)) { const v = await fn().catch(() => null); if (v) return v; }
      throw new Error(`timed out: ${label}`);
    };
    await gotoTab(C.page, `orgs/${ORG}`, 'resource ceilings');
    const ed = C.page.locator(`bx-org-agents[org="${ORG}"]:not([pinned])`);
    await until(async () => (await ed.locator('select[data-rule=harnesses]').count()) === 1, 'the org\'s editor');
    check(await ed.locator('select[data-rule=workspaceAgent]').inputValue() === 'on' && await ed.locator('select[data-rule=harnesses]').inputValue() === 'any'
      && await ed.locator('select[data-rule=models]').inputValue() === 'any' && await ed.locator('[data-save]').isDisabled(), 'an org with no rules reads on, any, any — nothing to save');
    await ed.locator('select[data-rule=harnesses]').selectOption('list');
    await until(async () => (await ed.locator('input[data-harness]').count()) >= 4, 'the harness checkboxes');
    await ed.locator('input[data-harness=claude]').check();
    await ed.locator('input[data-harness=codex]').check();
    await ed.locator('select[data-rule=models]').selectOption('private');
    await shotEl(C.page, `bx-org-agents[org="${ORG}"]:not([pinned])`, `agentpolicy-admin-org-${THEME}`);
    const [req] = await Promise.all([C.page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith(`/api/xbin/orgs/${ORG}/agents`)), ed.locator('[data-save]').click()]);
    check(req.postData() === '{"harnesses":["claude","codex"],"models":"private"}', `Save sends only what changed (${req.postData()})`);
    await until(async () => (await ed.locator('.ok').count()) === 1, 'saved');
    const got = (await a.get('/orgs')).body.orgs.find((o) => o.id === ORG)?.agents;
    check(JSON.stringify(got) === '{"workspaceAgent":"on","harnesses":["claude","codex"],"models":"private"}', `the org's rules are stored (${JSON.stringify(got)})`);
    // the workspace's bounds, under the list; then an org list that reaches past them
    await gotoTab(C.page, 'orgs', 'workspace policy');
    const ws = C.page.locator('bx-org-agents:not([org]), bx-org-agents[org=""]');
    await until(async () => (await ws.locator('select[data-rule=credentials]').count()) === 1, 'the workspace\'s editor');
    await ws.locator('select[data-rule=harnesses]').selectOption('list');
    await ws.locator('input[data-harness=claude]').check();
    await ws.locator('select[data-rule=credentials]').selectOption('connector-first');
    await ws.locator('[data-save]').click();
    await until(async () => (await ws.locator('.ok').count()) === 1, 'the bounds saved');
    const bounds = (await a.get('/agents/policy')).body;
    check(JSON.stringify(bounds.harnesses) === '["claude"]' && bounds.credentials === 'connector-first', `the workspace's bounds are stored (${JSON.stringify(bounds)})`);
    await shot(C.page, `agentpolicy-admin-bounds-${THEME}`, { fullPage: false });
    await gotoTab(C.page, `orgs/${ORG}`, 'resource ceilings');
    await until(async () => (await ed.locator('.hint', { hasText: 'the workspace allows: claude' }).count()) === 1, 'the bound shows beside the org\'s rule');
    check(true, 'the org\'s editor shows what the workspace allows');
    await ed.locator('select[data-rule=workspaceAgent]').selectOption('off');
    await ed.locator('[data-save]').click();
    await until(async () => (await ed.locator('.err').count()) === 1, 'the refusal');
    const err = await ed.locator('.err').innerText();
    check(/codex is outside what the workspace allows \(claude\)/.test(err) && /can't widen/.test(err), `a list past the bounds is refused in the server's words (${err})`);
    await shotEl(C.page, `bx-org-agents[org="${ORG}"]:not([pinned])`, `agentpolicy-admin-refused-${THEME}`);

    // ---- (f) a pin ----
    await a.put('/agents/policy', { ...noRules, credentials: null });
    await a.put(`/orgs/${ORG}/agents`, noRules);
    await gotoTab(C.page, `orgs/${ORG}`, 'resource ceilings');
    const pinEd = C.page.locator(`bx-org-agents[org="${ORG}"][pinned]`);
    await until(async () => (await pinEd.locator('select[data-rule=models]').count()) === 1, 'the pin\'s editor');
    await pinEd.locator('select[data-rule=models]').selectOption('private');
    await pinEd.locator('select[data-rule=harnesses]').selectOption('list');
    await pinEd.locator('input[data-harness=claude]').check();
    await pinEd.locator('input[data-harness=codex]').check();
    const [pinReq] = await Promise.all([C.page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith(`/api/xbin/orgs/${ORG}/agents/pin`)), pinEd.locator('[data-save]').click()]);
    check(pinReq.postData() === '{"harnesses":["claude","codex"],"models":"private"}', `the admin console pins rules on the org (${pinReq.postData()})`);
    await until(async () => (await pinEd.locator('.ok').count()) === 1, 'the pin saved');
    await closeCtx(C.ctx, C.page);
    const pinned = (await a.get('/orgs')).body.orgs.find((o) => o.id === ORG);
    check(JSON.stringify(pinned?.agentsPin) === '{"workspaceAgent":"on","harnesses":["claude","codex"],"models":"private"}' && !pinned.agents, `the pin is stored beside the org's own rules (${JSON.stringify(pinned?.agentsPin)})`);
    // the org's admin: sees the pin in the organisations tile, narrows below it, can't loosen it
    const E = await login(browser, 'dev1', 'devpass123', { viewport: { width: 1200, height: 1100 } });
    const e = api(E.ctx);
    check((await e.put(`/orgs/${ORG}/agents/pin`, { models: null })).status === 403, 'an org\'s admin can\'t touch the pin');
    const loosen = await e.put(`/orgs/${ORG}/agents`, { harnesses: ['claude', 'gemini'] });
    check(loosen.status === 400 && /gemini is outside what the pin allows \(claude, codex\)/.test(loosen.body?.error || ''), `…nor set rules past it (${loosen.status} ${loosen.body?.error})`);
    await E.page.goto(`${URL}/c/tiles/organisations/`);
    const card = E.page.locator(`bx-org-agents[org="${ORG}"]`);
    await until(async () => (await card.locator('[data-pin]').count()) === 1, 'the organisations tile\'s agents card');
    const pinLine = await card.locator('[data-pin]').innerText();
    check(/pinned by a workspace admin: coding agents: claude, codex · private models only/.test(pinLine.replace(/\s+/g, ' ')), `the org's admin sees what is pinned (${pinLine.replace(/\s+/g, ' ')})`);
    await card.locator('select[data-rule=harnesses]').selectOption('list');
    await card.locator('input[data-harness=claude]').check();
    await card.locator('[data-save]').click();
    await until(async () => (await card.locator('.ok').count()) === 1, 'the org admin\'s save');
    const narrowed = (await a.get('/orgs')).body.orgs.find((o) => o.id === ORG);
    check(JSON.stringify(narrowed?.agents?.harnesses) === '["claude"]', `the org's admin narrows below the pin in the organisations tile (${JSON.stringify(narrowed?.agents)})`);
    await shotEl(E.page, `bx-org-agents[org="${ORG}"]`, `agentpolicy-orgtile-${THEME}`);
    const mine = (await e.get('/agents/connection?model=hosted/big')).body.tiles.find((x) => x.tile === TILE);
    check(mine?.state === 'blocked' && mine.pinned === true && mine.reason === 'Devs allows private models only (pinned by a workspace admin)', `the connection view says the rule is pinned (${JSON.stringify({ state: mine?.state, reason: mine?.reason })})`);
    await e.put(`/orgs/${ORG}/agents`, { models: 'any', harnesses: 'any' });
    const still2 = (await e.get('/agents/connection?model=hosted/big')).body.tiles.find((x) => x.tile === TILE);
    check(still2?.state === 'blocked' && still2.pinned === true, 'the org\'s own "any" loosens nothing the pin holds');
    await E.ctx.close();
  } finally {
    await tidy();
  }
  await settle(A.page);
  await fr(A.page, TILE, (f) => f?.closeTerminal()).catch(() => { });
  await sh(A.page, (t) => t?.flushSave?.()).catch(() => { });
  await sleep(600); // the window's own save (debounced)
  await A.page.goto('about:blank'); // the shell is gone: nothing writes after what follows
  await A.ctx.request.delete(windowURL).catch(() => { });
  if (layout0 && typeof layout0 === 'object') {
    const put = await A.ctx.request.put(layoutURL, { data: layout0, headers: { 'X-Prefs-Writer': 'harness-agentpolicy' } }).catch(() => null);
    check(!!put?.ok(), `tidy: the admin's layout back (${put?.status()})`);
  } else {
    const del = await A.ctx.request.delete(layoutURL).catch(() => null);
    check(!!del && del.status() < 500, `tidy: the admin had no layout saved, and has none again (${del?.status()})`);
  }
  await A.ctx.close();
  done();
}

module.exports = { agentPolicy };
