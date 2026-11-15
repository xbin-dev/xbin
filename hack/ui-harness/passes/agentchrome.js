// hack/ui-harness/passes/agentchrome.js — xbin's chrome for the workspace
// agent (D232): the dock, the approval sheet, the change-set dialog, the
// tray and the lists, on /xbin/agent at desktop width and at 430 px, and
// the chip in the shell.
//
// THE DELEGATION STATE IS FAKED BY ROUTE INTERCEPTION. xbind on main ships
// no sealed workspace-agent component, so no credential can post a request
// or propose a change set, and the delegation package's test hooks are Go's
// (they do not reach a running harness). This pass therefore answers
// /api/xbin/delegate/…, /api/xbin/llm and /api/xbin/agent/prefs itself, in
// the shapes of docs/protocol/delegation.md, and asserts what the chrome
// draws and SENDS. Everything it asserts about the wire must be run again
// against a real holder once one exists: that an approval reaches xbind and
// the ticket works, the `delegate` events (here the store is told to read
// again), the recent-sign-in step, and the holder's own frame.
//
// It asserts: each request kind approved, denied and expired; Allow not
// armed at once and never by Enter; the wider grant's second step; a
// session start in a mode that never asks; a change set with a warning and
// a blocked item, the sign-in step, partial results, Undo; pause and
// resume; the tray's count; Approvals, What my agent did, Preferences; the
// states shown in place of the page; and that a hostile page in the dock's
// frame cannot grant — forged messages, an overlay, focus and keys.
// In the shell: a tile's ⋯ menu "Ask my agent about this tile" opens the
// dock about it (not for a non-admin before setup); the page's "Hand this
// to <tile>'s own agent" opens the tile's window on a new session's
// launcher with the words as plain text, nothing started until a coding
// agent is picked, whose composer then holds them, unsent — and where no
// shell takes it (/xbin/agent) the dock gives a plain link.
// Run it in both themes (HARNESS_THEME=light). Screenshots: out/agent-chrome-*.png.
const { URL, login, shot, checker, log, sh, fr, waitFor, waitSel, usePersonalScreen, openTile, closeTile } = require('../lib');

const NOW = Date.now();
const iso = (ms) => new Date(NOW + ms).toISOString();
const callReq = (id, x = {}) => ({ id, user: 'admin', agent: 'system/agent', kind: 'call', method: 'POST', target: '/api/apps/crawler/links/rebuild?all=1', tile: 'apps/crawler',
  contentType: 'application/json', body: '{"all":true,"note":"from the agent"}', bodyShown: 35, bodySize: 35, bodySha256: 'ab', summary: 'TRUST ME this is safe <img src=x onerror=alert(1)> [click](http://evil.example)',
  preview: { class: 'write', tile: 'apps/crawler', level: 'write', standing: { call: 'POST to /api/apps/crawler/links/rebuild on Crawler, whatever it sends, without asking again.', tile: 'Any create, change or delete in Crawler that you could do yourself, without asking again.' } },
  state: 'pending', created: iso(-120000), expires: iso(86280000), ...x });
const sessionReq = (id, asks) => ({ id, user: 'admin', agent: 'system/agent', kind: 'session', method: 'POST', target: '/api/xbin/term/sessions', tile: 'apps/crawler', body: JSON.stringify({ prompt: 'Fix the export test.\nLeave the schema alone.' }),
  bodyShown: 60, bodySize: 60, summary: 'I will fix the export', preview: { class: 'session', tile: 'apps/crawler', session: { tile: 'apps/crawler', provider: 'claude', providerName: 'Claude Code', mode: asks ? 'default' : 'bypassPermissions', asks, net: 'none', api: true, model: 'sonnet' } },
  state: 'pending', created: iso(-60000), expires: iso(86340000) });
const templateReq = (id) => ({ id, user: 'admin', agent: 'system/agent', kind: 'template', method: 'POST', target: '/api/xbin/templates/new', body: JSON.stringify({ source: 'counter-go', path: 'apps/tally' }), bodyShown: 44, bodySize: 44,
  summary: 'A tally tile', preview: { class: 'template', owner: 'admin' }, state: 'pending', created: iso(-30000), expires: iso(86370000) });
const changeSet = (id) => ({ id, user: 'admin', agent: 'system/agent', title: 'Wire the export <b>NOW</b>', state: 'proposed', created: iso(-600000), expires: iso(6 * 86400000), items: [
  { n: 1, kind: 'grant.add', args: {}, line: 'Give Crawler read access to Orders', before: 'no grant', after: 'apps/crawler → apps/orders: read', effects: ['Crawler restarts (about 3 s).'], why: 'It needs the orders', inverse: {} },
  { n: 2, kind: 'binding.set', args: {}, line: "Publish Crawler's export at export.example", before: 'not connected', after: 'host export.example', warnings: ['PUBLIC: anyone on the internet can reach /export'] },
  { n: 3, kind: 'limits.set', args: {}, line: "Raise Crawler's memory to 2048 MiB", before: '512 MiB', after: '2048 MiB', blocked: "It is above the ceiling for one tile (1024 MiB)." },
  { n: 4, kind: 'org.share', args: {}, line: 'Share Crawler with Finance to read', before: 'not shared', after: 'Finance: read', inverse: {} },
] });

// the fake of docs/protocol/delegation.md, for one browser context
async function fakeDelegation(ctx) {
  const S = { state: { user: 'admin', workspace: true, paused: false, adminChanges: false, canAdminChanges: true }, llm: { configured: true, enabled: true }, requests: [], approvals: [], changesets: [],
    audit: [], prefs: { agentSessions: { start: 'ask' } }, sent: [], stepUp: '', n: 0 };
  const json = (route, v, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(v) });
  const state = () => ({ ...S.state, pendingRequests: S.requests.filter((r) => r.state === 'pending').length, standingApprovals: S.approvals.length, openChangeSets: S.changesets.filter((c) => c.state === 'proposed').length });
  await ctx.route('**/api/xbin/llm', (route) => (route.request().method() === 'GET' ? json(route, S.llm) : route.fallback()));
  await ctx.route('**/api/xbin/agent/prefs', (route) => {
    if (route.request().method() === 'PUT') { const p = route.request().postDataJSON(); S.prefs = { ...S.prefs, agentSessions: { ...S.prefs.agentSessions, ...(p.agentSessions || {}) } }; S.sent.push({ m: 'PUT', p: '/agent/prefs', body: p }); }
    return json(route, S.prefs);
  });
  await ctx.route('**/api/xbin/delegate/**', (route) => {
    const req = route.request(), m = req.method(), p = new globalThis.URL(req.url()).pathname.replace('/api/xbin/delegate', '');
    let body = null;
    try { body = req.postDataJSON(); } catch { /* none */ }
    const top = req.frame() === req.frame().page().mainFrame();
    if (m !== 'GET') S.sent.push({ m, p, body, top });
    // as xbind does by the credential: the agent's page reads, and may pause — it grants nothing (403)
    if (m !== 'GET' && !top && !(p === '/state' && body && body.paused === true && Object.keys(body).length === 1)) return json(route, { error: "the agent's page can't do this" }, 403);
    const seg = p.split('/').filter(Boolean);
    if (p === '/state') { if (m === 'PUT') Object.assign(S.state, body, body.paused ? { pausedAt: new Date().toISOString() } : {}); return json(route, state()); }
    if (p === '/workspace' && m === 'PUT') { S.state.workspace = !!body.enabled; return json(route, { enabled: S.state.workspace }); }
    if (p === '/audit') return json(route, { user: 'admin', calls: S.audit });
    if (seg[0] === 'requests') {
      if (seg.length === 1) return json(route, { requests: S.requests.filter((r) => r.state === 'pending') });
      const r = S.requests.find((x) => x.id === seg[1]);
      if (!r) return json(route, { error: 'no such request' }, 404);
      if (seg.length === 2) return json(route, r);
      if (r.state !== 'pending') return json(route, { error: 'no longer pending' }, 409);
      if (seg[2] === 'deny') { r.state = 'denied'; r.decided = new Date().toISOString(); return json(route, { request: r }); }
      r.state = 'approved'; r.grant = body.grant || 'once'; r.decided = new Date().toISOString();
      if (body.grant && body.grant !== 'once') S.approvals.push({ id: `a${++S.n}`, user: 'admin', tile: r.tile, scope: body.scope, ...(body.scope === 'call' ? { method: r.method, path: '/links/rebuild' } : {}), created: new Date().toISOString(), ...(body.grant === 'hour' ? { until: iso(3600000) } : {}) });
      return json(route, { request: r });
    }
    if (seg[0] === 'approvals') {
      if (m === 'DELETE') { S.approvals = S.approvals.filter((a) => a.id !== seg[1]); return route.fulfill({ status: 204, body: '' }); }
      return json(route, { approvals: S.approvals });
    }
    if (seg[0] === 'changesets') {
      if (seg.length === 1) return json(route, { changesets: S.changesets });
      const c = S.changesets.find((x) => x.id === seg[1]);
      if (!c) return json(route, { error: 'no such change set' }, 404);
      if (seg.length === 2) return json(route, c);
      if (seg[2] === 'reject') { c.state = 'rejected'; return json(route, c); }
      if (seg[2] === 'undo') { const u = { ...changeSet(`u${++S.n}`), title: '', undoOf: c.id, items: c.items.filter((it) => it.result === 'applied').map((it, i) => ({ n: i + 1, kind: it.kind, args: {}, line: `Undo: ${it.line}`, before: it.after, after: it.before })) }; S.changesets.push(u); return json(route, u); }
      if (S.stepUp && !(S.stepUp === 'password' && body.password === 'right')) return json(route, { error: body.password ? 'wrong password' : 'a recent sign-in is needed', stepUp: S.stepUp }, 403);
      // apply: item 1 applies, a later binding fails, what follows is skipped; the rest is left out
      let failed = false;
      for (const it of c.items) {
        if (it.blocked) it.result = 'blocked'; else if (!body.items.includes(it.n)) it.result = 'rejected';
        else if (failed) it.result = 'skipped'; else if (it.kind === 'binding.set') { it.result = 'failed'; it.error = 'the host name is taken'; failed = true; } else it.result = 'applied';
      }
      c.state = c.items.some((it) => ['failed', 'skipped'].includes(it.result)) ? 'partial' : 'applied';
      return json(route, c);
    }
    return json(route, { error: 'not found' }, 404);
  });
  return S;
}

const DOCK = 'bx-agent-dock';
const SHEET = `${DOCK} bx-agent-approve`;
const DLG = 'bx-agent-changes';

async function agentChrome(browser) {
  const { check, done } = checker('agent-chrome');
  for (const width of [1280, 430]) {
    const tag = width < 640 ? '-430' : '';
    const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width, height: 900 } });
    const S = await fakeDelegation(ctx);
    const refresh = () => page.evaluate(() => import('/vendor/agent-chrome-store.js').then((m) => m.agentStore().refresh()));
    const sentTo = (re) => S.sent.filter((c) => re.test(`${c.m} ${c.p}`));
    const armed = (sel) => page.waitForFunction((s) => { const q = (root, parts) => parts.reduce((r, p) => r && (r.shadowRoot || r).querySelector(p), root);
      const el = q(document, s); return el && el.getAttribute('aria-disabled') === 'false'; }, sel, { timeout: 5000 });
    const open = async (query = '') => { await page.goto(`${URL}/xbin/agent${query}`); await page.waitForSelector(`${DOCK} header`); };
    const text = (sel) => page.textContent(sel).then((t) => (t || '').replace(/\s+/g, ' ').trim());

    // ---- the states shown in place of the page (§10) ----
    S.state.workspace = false;
    await open();
    await page.waitForSelector(`${DOCK} bx-agent-setup [data-state="setup"]`);
    check((await page.$$(`${DOCK} bx-agent-setup [data-step]`)).length === 3 && /Set up the workspace agent/.test(await text(`${DOCK} bx-agent-setup .st`)), `[${width}] an admin before setup: the three steps`);
    check(!(await page.$(`${DOCK} [data-needs]`)), `[${width}] …and no Needs you, Pause or menu yet`);
    await shot(page, `agent-chrome-setup${tag}`, { fullPage: false });
    await page.click(`${DOCK} bx-agent-setup [data-step="on"] button`);
    await page.waitForSelector(`${DOCK} bx-agent-setup [data-state="soon"]`);
    check(sentTo(/PUT \/workspace/).length === 1 && S.state.workspace, `[${width}] "Turn on" turns workspace agents on (PUT /delegate/workspace)`);
    S.llm.configured = false; await refresh();
    await page.waitForSelector(`${DOCK} bx-agent-setup [data-state="unavailable"]`);
    check(/Not available right now\./.test(await text(`${DOCK} bx-agent-setup .st`)) && !!(await page.$(`${DOCK} [data-copy]`)) && !!(await page.$(`${DOCK} [data-again]`)), `[${width}] no model later: not available, a message for an admin, Check again`);
    S.llm.configured = true; S.state.held = 'locked'; await refresh();
    await page.waitForSelector(`${DOCK} bx-agent-setup [data-state="held"]`);
    check(/Locked after a restart\./.test(await text(`${DOCK} bx-agent-setup .st`)), `[${width}] held: locked after a restart`);
    await shot(page, `agent-chrome-states${tag}`, { fullPage: false });
    delete S.state.held; S.state.workspace = false; S.state.workspaceAt = new Date().toISOString(); await refresh();
    await page.waitForSelector(`${DOCK} bx-agent-setup [data-state="off"]`);
    check(/Turned off for this workspace\./.test(await text(`${DOCK} bx-agent-setup .st`)), `[${width}] turned off by an admin (the switch was set once): it says so, not the setup steps`);
    S.state.workspace = true; delete S.state.workspaceAt; await refresh();

    // ---- pause and resume ----
    await page.waitForSelector(`${DOCK} [data-pause]`);
    await page.click(`${DOCK} [data-pause]`);
    await page.waitForSelector(`${DOCK} bx-agent-setup [data-state="paused"]`);
    check(S.state.paused && /Paused by you at \d\d:\d\d\./.test(await text(`${DOCK} bx-agent-setup .st`)) && (await text(`${DOCK} [data-pause]`)) === 'Resume', `[${width}] Pause: at once, and the dock says so (${await text(`${DOCK} [data-pause]`)})`);
    await page.click(`${DOCK} bx-agent-setup [data-resume]`);
    await page.waitForSelector(`${DOCK} bx-agent-setup [data-state="soon"]`);
    check(!S.state.paused, `[${width}] Resume resumes`);

    // ---- the tray, and a tile call approved once ----
    S.requests = [callReq('r1'), callReq('r2', { created: iso(-60000) }), sessionReq('s1', true)];
    S.changesets = [changeSet('c1')];
    await refresh();
    await page.waitForSelector(`${DOCK} [data-needs] [data-count]`);
    check((await text(`${DOCK} [data-needs] [data-count]`)) === '4', `[${width}] Needs you counts what waits (${await text(`${DOCK} [data-needs] [data-count]`)})`);
    check(/^\(4\) /.test(await page.title()), `[${width}] …and the page's title carries it (${await page.title()})`);
    await page.click(`${DOCK} [data-needs]`);
    await page.waitForSelector(`${DOCK} bx-agent-needs li`);
    const rows = await page.$$eval(`${DOCK} bx-agent-needs li`, (els) => els.map((e) => `${e.dataset.kind}:${e.dataset.id}`));
    check(rows.join(' ') === 'changeset:c1 request:r1 request:r2 request:s1', `[${width}] the tray lists them oldest first (${rows.join(' ')})`);
    await shot(page, `agent-chrome-tray${tag}`, { fullPage: false });
    await page.click(`${DOCK} bx-agent-needs li[data-id="r1"] button`);
    await page.waitForSelector(`${SHEET} [role="dialog"]`);
    const sheet = await text(`${SHEET} [role="dialog"]`);
    check(/xbin · approval/i.test(sheet) && /Your agent wants to change something in Crawler/.test(sheet) && /POST \/api\/apps\/crawler\/links\/rebuild\?all=1/.test(sheet) && /35 bytes · shown in full/.test(sheet)
      && /you can write here/.test(sheet) && /xbin can't undo this/.test(sheet) && /1 of 3/.test(sheet), `[${width}] the sheet: xbin's reading of the call (${sheet.slice(0, 160)})`);
    check(/The agent's own words — not checked by xbin/i.test(sheet) && !(await page.$(`${SHEET} img`)) && !(await page.$(`${SHEET} a`)) && /<img src=x/.test(sheet),
      `[${width}] the agent's sentence is quoted as text: no image, no link`);
    check((await page.evaluate(() => { const s = document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve'); return s.shadowRoot.activeElement?.localName; })) === 'h2', `[${width}] focus lands on the heading`);
    // not armed at once; never by Enter
    await page.click(`${SHEET} button[data-allow]`, { force: true });
    check(sentTo(/approve$/).length === 0, `[${width}] a click before it is armed allows nothing`);
    await armed(['bx-agent-dock', 'bx-agent-approve', 'button[data-allow]']);
    await page.focus(`${SHEET} button[data-allow]`);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(150); // (a negative: nothing may be sent)
    check(sentTo(/approve$/).length === 0, `[${width}] Enter on Allow allows nothing`);
    await shot(page, `agent-chrome-approve${tag}`, { fullPage: false });
    await page.click(`${SHEET} button[data-allow]`);
    await page.waitForFunction(() => document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve')?.request?.id === 'r2');
    check(JSON.stringify(sentTo(/approve$/).map((c) => [c.p, c.body])) === '[["/requests/r1/approve",{"grant":"once"}]]', `[${width}] Allow once sends {grant: once} (${JSON.stringify(sentTo(/approve$/).map((c) => c.body))})`);
    check(true, `[${width}] …and the next request that waits is shown`);

    // ---- always this action; then the wider grant's second step ----
    await page.click(`${SHEET} [data-choice="call"] input`);
    check((await text(`${SHEET} button[data-allow]`)) === 'Allow always' && (await page.$$(`${SHEET} .dur input`)).length === 2, `[${width}] "always, this exact action": the button follows, a duration is offered`);
    await page.click(`${SHEET} [data-choice="tile"] input`);
    check(/wider/i.test(await text(`${SHEET} [data-choice="tile"]`)) && (await text(`${SHEET} button[data-allow]`)) === 'Allow every change to Crawler', `[${width}] "every change" is marked wider`);
    await armed(['bx-agent-dock', 'bx-agent-approve', 'button[data-allow]']);
    await page.click(`${SHEET} button[data-allow]`);
    await page.waitForSelector(`${SHEET} [data-step="wider"]`);
    check(sentTo(/r2\/approve$/).length === 0 && /Allow every change to Crawler\?/.test(await text(`${SHEET} [role="dialog"]`))
      && await page.$eval(`${SHEET} [data-back]`, (b) => b.classList.contains('primary')) && await page.$eval(`${SHEET} button[data-allow="tile"]`, (b) => b.classList.contains('danger')),
    `[${width}] the wider grant asks again: Go back is the primary button, nothing sent yet`);
    await shot(page, `agent-chrome-wider${tag}`, { fullPage: false });
    await page.click(`${SHEET} [data-back]`);
    await page.waitForSelector(`${SHEET} [data-step="main"]`);
    await page.click(`${SHEET} [data-choice="tile"] input`);
    await armed(['bx-agent-dock', 'bx-agent-approve', 'button[data-allow]']);
    await page.click(`${SHEET} button[data-allow]`);
    await page.waitForSelector(`${SHEET} [data-step="wider"]`);
    await armed(['bx-agent-dock', 'bx-agent-approve', 'button[data-allow="tile"]']);
    await page.click(`${SHEET} button[data-allow="tile"]`);
    await page.waitForFunction(() => document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve')?.request?.id === 's1');
    check(JSON.stringify(sentTo(/r2\/approve$/).map((c) => c.body)) === '[{"grant":"hour","scope":"tile"}]', `[${width}] "Allow every change" sends {grant: hour, scope: tile} (${JSON.stringify(sentTo(/r2\/approve$/).map((c) => c.body))})`);

    // ---- a session start that asks: denied; one that never asks: Deny is primary ----
    const st = await text(`${SHEET} [role="dialog"]`);
    check(/Start a tile agent in Crawler\?/.test(st) && /asks before acting/i.test(st) && /Claude Code · model sonnet/.test(st) && /Fix the export test\./.test(st) && /sent exactly like this/.test(st) && !(await page.$(`${SHEET} [data-choice]`)),
      `[${width}] a tile agent start: the mode, the coding agent, the brief; no standing choice`);
    await page.click(`${SHEET} button[data-deny]`);
    await page.waitForFunction(() => !document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve'));
    check(sentTo(/s1\/deny$/).length === 1, `[${width}] Deny denies`);
    S.requests.push(sessionReq('s2', false), templateReq('t1'), callReq('r3', { state: 'expired' }));
    await refresh();
    await page.evaluate(() => document.querySelector('bx-agent-dock').review('request', 's2'));
    await page.waitForSelector(`${SHEET} [role="dialog"][data-kind="session"]`);
    check(/acts without asking/i.test(await text(`${SHEET} [data-row="mode"]`)) && /a mode that never asks/.test(await text(`${SHEET} .banner.bad`))
      && await page.$eval(`${SHEET} button[data-deny]`, (b) => b.classList.contains('primary')) && (await text(`${SHEET} button[data-allow]`)) === 'Start without asking'
      && await page.$eval(`${SHEET} button[data-allow]`, (b) => b.classList.contains('danger')), `[${width}] a mode that never asks: the danger wording, Deny is the primary button`);
    await shot(page, `agent-chrome-session-danger${tag}`, { fullPage: false });
    await armed(['bx-agent-dock', 'bx-agent-approve', 'button[data-allow]']);
    await page.click(`${SHEET} button[data-allow]`);
    await page.waitForFunction(() => document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve')?.request?.id === 't1');
    check(sentTo(/s2\/approve$/).length === 1 && /Your agent wants to create a tile/.test(await text(`${SHEET} [role="dialog"]`)) && /counter-go/.test(await text(`${SHEET} [role="dialog"]`)), `[${width}] a tile create: its own sheet, asked every time`);
    await page.keyboard.press('Escape');
    await page.waitForFunction(() => !document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve'));
    check(sentTo(/t1\//).length === 0, `[${width}] Escape is "not now": nothing is sent`);
    await page.evaluate(() => document.querySelector('bx-agent-dock').review('request', 'r3'));
    await page.waitForSelector(`${SHEET} [role="dialog"]`);
    check(/Expired — not approved within 24 hours\./.test(await text(`${SHEET} [role="dialog"]`)) && !(await page.$(`${SHEET} button[data-allow]`)), `[${width}] an expired request says so and offers no Allow`);
    await page.click(`${SHEET} .foot button`);
    // (closing it shows the next one that waits — the tile create again: not now)
    await page.waitForFunction(() => document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve')?.request?.id === 't1');
    await page.keyboard.press('Escape');
    await page.waitForFunction(() => !document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve'));
    await page.evaluate(() => document.querySelector('bx-agent-dock').review('request', 'nope'));
    await page.waitForTimeout(200); // (a negative)
    check(!(await page.$(`${SHEET}`)), `[${width}] an id that is not the person's shows nothing`);

    // ---- the change set: a warning, a blocked item, the sign-in step, partial results, Undo ----
    S.stepUp = 'password';
    await page.evaluate(() => document.querySelector('bx-agent-dock').review('changeset', 'c1'));
    await page.waitForSelector(`${DLG} [role="dialog"][data-view="review"]`);
    const ticks = await page.$$eval(`${DLG} .item`, (els) => els.map((e) => `${e.dataset.n}${e.querySelector('input').checked ? '+' : '-'}${e.querySelector('input').disabled ? 'x' : ''}`));
    check(ticks.join(' ') === '1+ 2- 3-x 4+', `[${width}] a warned item starts unticked, a blocked one can't be ticked (${ticks.join(' ')})`);
    const dl = await text(`${DLG} [role="dialog"]`);
    check(/xbin · admin changes/i.test(dl) && /Your agent proposes 4 admin changes/.test(dl) && /The agent's title — not checked by xbin/i.test(dl) && /<b>NOW<\/b>/.test(dl) && /PUBLIC/.test(dl) && /Can't be applied\./.test(dl)
      && /2 of 4 ticked/.test(dl) && (await text(`${DLG} [data-apply]`)) === 'Apply 2 changes…', `[${width}] the review: xbin's lines, the warning, the blocked item, the count on the button`);
    await shot(page, `agent-chrome-changes${tag}`, { fullPage: false });
    await page.check(`${DLG} .item[data-n="2"] input`);
    check((await text(`${DLG} [data-apply]`)) === 'Apply 3 changes…', `[${width}] ticking the warned item is the person's own act`);
    await armed(['bx-agent-changes', 'button[data-apply]']);
    await page.click(`${DLG} [data-apply]`);
    await page.waitForSelector(`${DLG} [data-stepup="password"]`);
    check(/Confirm it's you/.test(await text(`${DLG} [role="dialog"]`)) && /3 of 4 ticked/.test(await text(`${DLG} [role="dialog"]`)), `[${width}] xbind asks for a recent sign-in: the password step, the ticks kept`);
    await page.fill(`${DLG} #pw`, 'right');
    await armed(['bx-agent-changes', 'button[data-apply]']);
    await page.click(`${DLG} [data-apply]`);
    await page.waitForSelector(`${DLG} [role="dialog"][data-view="results"]`);
    const res = await page.$$eval(`${DLG} .item [data-result]`, (els) => els.map((e) => e.dataset.result));
    check(res.join(' ') === 'applied failed blocked skipped' && /1 of 3 changes applied/.test(await text(`${DLG} h2`)) && /One change failed, so the one after it was skipped\. The one that was applied stays\./.test(await text(`${DLG} .banner.warn`))
      && /xbin said: “the host name is taken”/.test(await text(`${DLG} [role="dialog"]`)), `[${width}] partial results, in words (${res.join(' ')}; ${await text(`${DLG} h2`)})`);
    check(JSON.stringify(sentTo(/c1\/approve$/).map((c) => c.body)) === '[{"items":[1,2,4]},{"items":[1,2,4],"password":"right"}]', `[${width}] apply sent the ticked items, then again with the password`);
    await shot(page, `agent-chrome-results${tag}`, { fullPage: false });
    await page.click(`${DLG} [data-undo]`);
    await page.waitForFunction(() => document.querySelector('bx-agent-changes')?.set?.undoOf === 'c1');
    check(/Undo: Give Crawler read access to Orders/.test(await text(`${DLG} [role="dialog"]`)) && (await page.getAttribute(`${DLG} [role="dialog"]`, 'data-view')) === 'review', `[${width}] Undo proposes a new batch, to review like any other`);
    await page.click(`${DLG} [data-reject]`);
    await page.waitForFunction(() => !document.querySelector('bx-agent-changes'));
    S.stepUp = 'signin'; S.changesets.push(changeSet('c2')); await refresh();
    await page.evaluate(() => document.querySelector('bx-agent-dock').review('changeset', 'c2'));
    await page.waitForSelector(`${DLG} [data-apply]`);
    await armed(['bx-agent-changes', 'button[data-apply]']);
    await page.click(`${DLG} [data-apply]`);
    await page.waitForSelector(`${DLG} [data-stepup="signin"]`);
    check(/Sign in again first\./.test(await text(`${DLG} [role="dialog"]`)) && !!(await page.$(`${DLG} [data-signin]`)), `[${width}] …or signing in again, when that is what xbind asks`);
    await page.click(`${DLG} [data-notnow]`);
    await page.keyboard.press('Escape');
    S.stepUp = '';

    // ---- Approvals, What my agent did, Preferences ----
    await page.waitForFunction(() => !document.querySelector('bx-agent-changes'));
    log('before the menu: ' + JSON.stringify(await page.evaluate(() => { const d = document.querySelector('bx-agent-dock'); const b = d.shadowRoot.querySelector('[data-menu]');
      return { menu: !!b, box: b && b.getBoundingClientRect().toJSON(), err: d.store.error, ws: d.store.state && d.store.state.workspace, sheet: !!d.shadowRoot.querySelector('bx-agent-approve') }; })));
    await page.click(`${DOCK} [data-menu]`);
    await page.click(`${DOCK} .menu [data-view="approvals"]`);
    await page.waitForSelector(`${DOCK} bx-agent-approvals li`);
    check((await page.$$(`${DOCK} bx-agent-approvals li`)).length === 1 && /every change/.test(await text(`${DOCK} bx-agent-approvals li`)) && /ends in/.test(await text(`${DOCK} bx-agent-approvals li`)), `[${width}] Approvals lists the standing one (${await text(`${DOCK} bx-agent-approvals li`)})`);
    await shot(page, `agent-chrome-approvals${tag}`, { fullPage: false });
    await page.click(`${DOCK} bx-agent-approvals li button`);
    await page.waitForSelector(`${DOCK} bx-agent-approvals [data-empty]`);
    check(sentTo(/DELETE \/approvals\//).length === 1, `[${width}] Take back takes it back`);
    S.audit = [{ ts: iso(-1000), method: 'POST', path: '/api/apps/crawler/links/rebuild', tile: 'apps/crawler', class: 'write', status: 200, ticket: 'r1' },
      { ts: iso(-2000), method: 'POST', path: '/api/apps/ledger/x', tile: 'apps/ledger', class: 'write', status: 403, refused: 'Finance allows private models only' },
      { ts: iso(-3000), method: 'GET', path: '/api/apps/orders/list', tile: 'apps/orders', class: 'read', status: 200 }];
    await page.evaluate(() => document.querySelector('bx-agent-dock').show('activity'));
    await page.waitForSelector(`${DOCK} bx-agent-activity li`);
    const n1 = (await page.$$(`${DOCK} bx-agent-activity li`)).length;
    await page.click(`${DOCK} bx-agent-activity [data-filter="all"]`);
    await page.waitForFunction(() => document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-activity').shadowRoot.querySelectorAll('li').length === 3);
    await page.click(`${DOCK} bx-agent-activity [data-filter="refused"]`);
    await page.waitForFunction(() => document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-activity').shadowRoot.querySelectorAll('li').length === 1);
    check(n1 === 1 && /Was refused: Ledger/.test(await text(`${DOCK} bx-agent-activity li`)) && /Kept by xbin for 13 months/.test(await text(`${DOCK} bx-agent-activity .pf`)), `[${width}] What my agent did: sentences, three filters`);
    await shot(page, `agent-chrome-activity${tag}`, { fullPage: false });
    await page.evaluate(() => document.querySelector('bx-agent-dock').show('prefs'));
    await page.waitForSelector(`${DOCK} bx-agent-prefs [data-admin-changes]`);
    await page.click(`${DOCK} bx-agent-prefs [data-admin-changes]`);
    await page.click(`${DOCK} bx-agent-prefs [data-start="allow"]`);
    for (let i = 0; i < 50 && !(S.state.adminChanges === true && sentTo(/PUT \/agent\/prefs/).length); i++) await page.waitForTimeout(40);
    check(S.state.adminChanges === true && sentTo(/PUT \/agent\/prefs/).some((c) => c.body.agentSessions.start === 'allow'), `[${width}] Preferences: the admin opt-in and the start preference are sent`);
    await shot(page, `agent-chrome-prefs${tag}`, { fullPage: false });

    // ---- a hostile page in the dock's frame cannot grant ----
    S.requests = [callReq('h1')]; S.sent.length = 0;
    await ctx.route('**/hostile.html', (route) => route.fulfill({ contentType: 'text/html', body: `<!doctype html><body style="margin:0;background:#400"><h1>the agent's page</h1>
      <div style="position:fixed;inset:-4000px;z-index:2147483647;background:rgba(255,0,0,.4)">COVER</div><script>
      const out = [];
      addEventListener('message', (e) => { if (e.data && e.data.type === 'xbin:agent-context') { out.push('about ' + e.data.tile); parent.postMessage({ type: 'xbin:open-session', session: 's-9', tile: e.data.tile, grant: 'always' }, '*'); } });
      try { parent.document.querySelector('bx-agent-dock'); out.push('READ THE PARENT'); } catch (e) { out.push('parent closed'); }
      for (const m of [{ type: 'xbin:agent-approve', id: 'h1' }, { type: 'xbin:agent-decide', id: 'h1', verdict: 'allow', grant: 'always', scope: 'tile' },
        { type: 'xbin:agent-review', kind: 'request', id: 'h1' }, { type: 'xbin:agent-open-request', id: 'h1', request: { method: 'GET', target: '/harmless', summary: 'PAGE TEXT' }, approve: true, grant: 'always' }]) parent.postMessage(m, '*');
      fetch('/api/xbin/delegate/requests/h1/approve', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{"grant":"always","scope":"tile"}' }).then((r) => out.push('fetch ' + r.status), () => out.push('fetch refused'));
      setInterval(() => { window.focus(); for (const k of ['Enter', ' ']) { parent.postMessage({ type: 'keydown', key: k }, '*'); document.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true })); } }, 50);
      setTimeout(() => parent.postMessage({ type: 'hostile-report', out }, '*'), 400);
      </script>` }));
    await page.evaluate(() => { window.hostile = null; window.asked = []; addEventListener('message', (e) => { if (e.data && e.data.type === 'hostile-report') window.hostile = e.data.out; });
      // (taken at the window, before the page's own listener would follow the link)
      addEventListener('bx-agent-open-session', (e) => { e.stopImmediatePropagation(); window.asked.push(e.detail); }, true);
      const d = document.querySelector('bx-agent-dock'); d.show('chat'); d.context = { tile: 'apps/crawler', note: 'not sent' }; d.page = '/hostile.html'; });
    await page.waitForFunction(() => window.hostile);
    await page.waitForSelector(`${SHEET} [role="dialog"]`);
    const hs = await text(`${SHEET} [role="dialog"]`);
    const report = await page.evaluate(() => window.hostile);
    const asked = await page.evaluate(() => window.asked);
    check(report.includes('about apps/crawler') && asked.length === 1 && asked[0].tile === 'apps/crawler' && asked[0].session === 's-9' && Object.keys(asked[0]).sort().join() === 'page,session,tile',
      `[${width}] the page is told what the dock was opened about (xbin:agent-context), and may ask for a session's window by ids alone (${JSON.stringify(asked)})`);
    check(report.includes('parent closed') && !report.includes('READ THE PARENT'), `[${width}] the page in the frame cannot read or script the chrome (${report.join(', ')})`);
    check(/POST \/api\/apps\/crawler\/links\/rebuild/.test(hs) && !/PAGE TEXT|\/harmless/.test(hs), `[${width}] a review message opens xbind's reading of the id — nothing the page sent is shown`);
    await armed(['bx-agent-dock', 'bx-agent-approve', 'button[data-allow]']);
    await page.waitForTimeout(400); // the page keeps posting, focusing and pressing: nothing may come of it
    const fromFrame = S.sent.filter((c) => !c.top);
    check(sentTo(/approve$/).filter((c) => c.top).length === 0 && fromFrame.every((c) => /approve$/.test(c.p)), `[${width}] forged messages, focus and keys approve nothing (${JSON.stringify(S.sent.map((c) => [c.p, c.top]))})`);
    check(fromFrame.length === 1 && report.includes('fetch 403') && S.requests[0].state === 'pending', `[${width}] the page's own fetch is the page's credential's, which xbind refuses (${report.join(', ')}); the request still waits`);
    const covered = await page.evaluate(() => { const b = document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve').shadowRoot.querySelector('button[data-allow]'); const r = b.getBoundingClientRect();
      const top = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2); return top && top.localName; });
    check(covered === 'bx-agent-dock', `[${width}] the page's overlay stays inside its frame: the sheet is on top (${covered})`);
    await shot(page, `agent-chrome-hostile${tag}`, { fullPage: false });
    await page.click(`${SHEET} button[data-allow]`);
    await page.waitForFunction(() => !document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve'));
    check(sentTo(/h1\/approve$/).filter((c) => c.top).length === 1, `[${width}] …and the person's own click still allows`);
    await ctx.close();
  }

  // ---- the shell: the chip, its count, the dock beside the canvas; nothing for a non-admin before setup ----
  {
    const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 900 } });
    const S = await fakeDelegation(ctx);
    S.requests = [callReq('r1'), callReq('r2')];
    await page.goto(`${URL}/`);
    await page.waitForSelector('bx-shell');
    await page.waitForSelector('bx-shell button.chip.agent');
    const chip = await page.textContent('bx-shell button.chip.agent');
    check(/workspace agent/.test(chip) && /2/.test(chip) && (await page.getAttribute('bx-shell button.chip.agent', 'aria-label')) === 'Workspace agent, 2 need you', `the shell's chip: its label and count (${chip.replace(/\s+/g, ' ').trim()})`);
    check(!(await page.$('bx-shell bx-agent-dock')), 'the dock is not loaded until the chip is pressed');
    const before = await page.$eval('bx-shell main', (m) => m.getBoundingClientRect().width);
    await page.click('bx-shell button.chip.agent');
    await page.waitForSelector('bx-shell bx-agent-dock[open] header');
    const after = await page.$eval('bx-shell main', (m) => m.getBoundingClientRect().width);
    const dockW = await page.$eval('bx-shell bx-agent-dock', (d) => d.getBoundingClientRect().width);
    check(dockW >= 360 && dockW <= 720 && Math.abs(before - after - dockW) < 12, `the dock pushes the canvas (${Math.round(before)} → ${Math.round(after)}, dock ${Math.round(dockW)})`);
    await shot(page, 'agent-chrome-shell', { fullPage: false });
    await page.click('bx-shell bx-agent-dock [data-close]');
    await page.waitForFunction(() => !document.querySelector('bx-shell').shadowRoot.querySelector('bx-agent-dock').open);
    const wasOpen = await dockAbout(page, ctx, check);
    await handOff(page, ctx, check, wasOpen);
    // the alert that names /xbin/agent is for shells without the chip: this one leaves it out
    let alertsRead = 0;
    await ctx.route('**/api/xbin/alerts', async (route) => { const resp = await route.fetch(); const j = await resp.json().catch(() => ({})); alertsRead++;
      await route.fulfill({ response: resp, json: { ...j, alerts: [...(j.alerts || []), { level: 'warn', kind: 'agent-page', system: false, dismiss: '/delegate/page/dismiss', message: 'Your workspace agent is on: open it at /xbin/agent' }] } }); });
    S.state.paused = true;
    await page.reload();
    await page.waitForSelector('bx-shell button.chip.agent');
    for (let i = 0; i < 100 && !alertsRead; i++) await page.waitForTimeout(50);
    await page.waitForTimeout(200);
    check(alertsRead > 0 && !(await page.locator('bx-shell .alerts').allInnerTexts()).join(' ').includes('/xbin/agent'), `a shell with the chip shows no alert of kind agent-page (${alertsRead} read)`);
    check((await page.getAttribute('bx-shell button.chip.agent', 'aria-label')) === 'Workspace agent, paused', 'paused: the chip says so');
    S.state.paused = false; S.state.workspace = false;
    await page.reload();
    await page.waitForSelector('bx-shell button.chip.agent');
    check(/set up workspace agent/.test(await page.textContent('bx-shell button.chip.agent')), 'an admin before setup: "set up workspace agent"');
    await ctx.close();
    const U = await login(browser, 'dev1', 'devpass123', { viewport: { width: 1400, height: 900 } });
    const S2 = await fakeDelegation(U.ctx);
    S2.state.workspace = false; S2.state.canAdminChanges = false;
    await U.page.goto(`${URL}/`);
    await U.page.waitForSelector('bx-shell button.chip.settings');
    await U.page.waitForTimeout(500); // (a negative: the chip must not appear)
    check(!(await U.page.$('bx-shell button.chip.agent')), 'a non-admin before setup sees no chip');
    const own = await sh(U.page, (t) => (t.openTiles || []).map((o) => o.path)[0] || '');
    if (own) {
      await U.page.click(`.card[data-path="${own}"] button.wc[aria-label="tile menu"]`);
      await waitSel(U.page, 'bx-menu .panel.main .it');
      const ls = await U.page.locator('bx-menu .panel.main .it .lb').allTextContents();
      check(ls.length > 0 && !ls.some((l) => /Ask my agent/.test(l)), `…nor "Ask my agent about this tile" in a tile's menu (${ls.join(' · ')})`);
      await U.page.keyboard.press('Escape');
    } else check(false, 'dev1 has a tile on their screen to open the menu of');
    await U.ctx.close();
  }
  log('agentChrome: the delegation state was faked by route interception (no holder on this xbind): re-run against a real one');
  done();
}

// ---- the tile menu's "Ask my agent about this tile", and the page's "Hand this to <tile>'s own agent" ----
// (the shell, as admin with the delegation faked: the chip is shown)
const HAND_TILE = 'apps/crawler';
const DRAFT = 'In export.go change line 40 to use a semicolon.\n<b>not markup</b>\u202e';
const DRAFT_SEEN = 'In export.go change line 40 to use a semicolon.\n<b>not markup</b>'; // plain text: the bidi control dropped, nothing interpreted
// the agent's page, as a test page in the dock's frame: it reports what the dock tells it and, asked, hands the words on
const HAND_PAGE = `<!doctype html><body style="margin:0"><p>the agent's page</p><script>
  addEventListener('message', (e) => {
    if (e.data && e.data.type === 'xbin:agent-context') parent.postMessage({ type: 'harness-context', context: e.data }, '*');
    if (e.data === 'hand-off') parent.postMessage({ type: 'xbin:open-session', session: 'new', tile: ${JSON.stringify(HAND_TILE)}, draft: ${JSON.stringify(DRAFT)} }, '*');
  });</script>`;
const dockFrame = (page, inShell) => page.evaluate((inShell) => {
  const d = inShell ? document.querySelector('bx-shell').shadowRoot.querySelector('bx-agent-dock') : document.querySelector('bx-agent-dock');
  d.shadowRoot.querySelector('iframe').contentWindow.postMessage('hand-off', '*');
}, inShell);

// the dock with the test page in its frame, about HAND_TILE; → whether the tile was open before
async function dockAbout(page, ctx, check) {
  await ctx.route('**/harness-hand.html', (route) => route.fulfill({ contentType: 'text/html; charset=utf-8', body: HAND_PAGE }));
  await page.evaluate(() => { window.harnessContexts = []; addEventListener('message', (e) => { if (e.data && e.data.type === 'harness-context') window.harnessContexts.push(e.data.context); }); });
  await usePersonalScreen(page);
  const wasOpen = await sh(page, (t, p) => t.isOpen(p), HAND_TILE);
  if (!wasOpen) await openTile(page, HAND_TILE);
  await page.locator(`.card[data-path="${HAND_TILE}"]`).evaluate((el) => el.scrollIntoView({ block: 'start' }));
  await page.evaluate(() => { const d = document.querySelector('bx-shell').shadowRoot.querySelector('bx-agent-dock'); if (d) d.page = '/harness-hand.html'; });
  await askAbout(page, check);
  return wasOpen;
}

async function askAbout(page, check) {
  // the tile menu: the entry sits before the admin section and opens the dock about that tile
  await page.click(`.card[data-path="${HAND_TILE}"] button.wc[aria-label="tile menu"]`);
  await waitSel(page, 'bx-menu .panel.main .it');
  const labels = await page.locator('bx-menu .panel.main .it .lb').allTextContents();
  const at = labels.indexOf('Ask my agent about this tile');
  check(at >= 0 && (labels.indexOf('Access…') < 0 || at < labels.indexOf('Access…')), `the tile menu offers "Ask my agent about this tile", before its admin section (${labels.join(' · ')})`);
  await page.locator('bx-menu .panel.main .it', { hasText: 'Ask my agent about this tile' }).click();
  await page.waitForSelector('bx-shell bx-agent-dock[open] header');
  await page.evaluate(() => { document.querySelector('bx-shell').shadowRoot.querySelector('bx-agent-dock').page = '/harness-hand.html'; });
  await page.waitForFunction((p) => (window.harnessContexts || []).some((c) => c.tile === p), HAND_TILE, { timeout: 10000 });
  const told = await page.evaluate(() => window.harnessContexts);
  check(told.some((c) => c.tile === HAND_TILE) && told.every((c) => Object.keys(c).sort().join() === 'tile,type'), `…which opens the dock about that tile: the agent's page is told xbin:agent-context {tile} (${JSON.stringify(told)})`);
  await shot(page, 'agent-chrome-ask-about', { fullPage: false });
}

async function handOff(page, ctx, check, wasOpen) {
  const sent = [];
  page.on('request', (r) => { if (r.method() === 'POST' && /\/api\/xbin\/term\/sessions($|\/[^/]+\/queue)/.test(new globalThis.URL(r.url()).pathname)) sent.push(new globalThis.URL(r.url()).pathname); });
  // "Hand this to <tile>'s own agent": the tile's window on a new session's launcher, the words beside it as text, nothing started
  const tabs0 = (await fr(page, HAND_TILE, (f) => f.tabs)).length;
  await dockFrame(page, true);
  await waitFor(page, (t, p) => { const f = t.frameFor(p)?.testApi(); return !!f && f.terminalOpen && !!f.handoff; }, HAND_TILE, { timeout: 10000, label: 'the window opens on a new session\'s launcher' });
  const hand = await fr(page, HAND_TILE, (f) => f.handoff);
  const shown = await page.$eval(`.card[data-path="${HAND_TILE}"] bx-frame [data-handoff] [data-draft]`, (e) => ({ text: e.textContent, markup: e.children.length }));
  check(hand.draft === DRAFT_SEEN && shown.text === DRAFT_SEEN && shown.markup === 0, `xbin:open-session {session: "new", draft}: the tile's window opens on a new session's launcher, the words as plain text (${JSON.stringify(shown)})`);
  await page.waitForTimeout(300); // (a negative: nothing may start by the message alone)
  check(sent.length === 0 && (await fr(page, HAND_TILE, (f) => f.tabs)).length === tabs0, `…and nothing is started or sent by the message alone (${sent.join(' ')})`);
  check(!(await page.evaluate(() => document.querySelector('bx-shell').shadowRoot.querySelector('bx-agent-dock').shadowRoot.querySelector('[data-handoff]'))), '…the shell took it: no link in the dock');
  // the launcher draws from the theme's tokens, in this theme
  const tokens = await page.$eval(`.card[data-path="${HAND_TILE}"] bx-frame [data-handoff]`, (el) => {
    const probe = document.createElement('div'); probe.style.cssText = 'background: var(--bx-panel); color: var(--bx-text)'; el.append(probe);
    const want = getComputedStyle(probe), got = getComputedStyle(el); const r = [got.backgroundColor, want.backgroundColor, got.color, want.color]; probe.remove(); return r;
  });
  check(tokens[0] === tokens[1] && tokens[2] === tokens[3], `…drawn in the theme's own panel and text colours (${tokens.join(' / ')})`);
  await shot(page, 'agent-chrome-handoff', { fullPage: false });
  const fake = await page.$(`.card[data-path="${HAND_TILE}"] bx-frame [data-handoff] [data-provider="fake"]`);
  if (!fake) check(false, 'the launcher offers the scripted "fake" coding agent (XBIN_AGENT_FAKE)');
  else {
    await fake.click();
    await waitFor(page, (t, p) => { const f = t.frameFor(p)?.testApi(); const g = f && f.agent(f.tabs.length - 1); return !!g && g.status === 'idle' && !f.handoff; }, HAND_TILE, { timeout: 20000, label: 'the picked agent\'s session is idle' });
    const after = await fr(page, HAND_TILE, (f) => { const i = f.tabs.length - 1; return { tab: f.tabs[i], draft: f.agent(i).draft, n: f.tabs.length }; });
    await page.waitForTimeout(300); // (a negative: the words wait for Send)
    check(after.n === tabs0 + 1 && after.tab.provider === 'fake' && after.draft === DRAFT_SEEN && !sent.some((p) => /\/queue$/.test(p)),
      `picking a coding agent starts its session with the words in its composer, unsent (${JSON.stringify({ n: after.n, provider: after.tab.provider, draft: after.draft, sent })})`);
    if (after.tab.id) await ctx.request.delete(`${URL}/api/xbin/term/sessions/${encodeURIComponent(after.tab.id)}`);
  }
  await fr(page, HAND_TILE, (f) => f.closeTerminal());
  if (!wasOpen) await closeTile(page, HAND_TILE);

  // where no shell takes it (/xbin/agent, an older shell): the plain link to the tile, the words to copy
  await page.goto(`${URL}/xbin/agent`);
  await page.waitForSelector(`${DOCK} header`);
  await page.evaluate(() => { document.querySelector('bx-agent-dock').page = '/harness-hand.html'; });
  await page.waitForFunction(() => document.querySelector('bx-agent-dock').shadowRoot.querySelector('iframe'));
  await page.waitForTimeout(300); // (the frame loads)
  await dockFrame(page, false);
  await page.waitForSelector(`${DOCK} [data-handoff]`);
  const link = await page.$eval(`${DOCK} [data-handoff] a`, (a) => a.getAttribute('href'));
  check(link === `/?tile=${HAND_TILE}` && (await text(page, `${DOCK} [data-handoff] [data-draft]`)) === DRAFT_SEEN.replace(/\s+/g, ' '),
    `a dock no shell answers gives the plain link to the tile, and the words (${link})`);
  await shot(page, 'agent-chrome-handoff-link', { fullPage: false });
  await page.goto(`${URL}/`); // (the shell again, for what follows)
  await page.waitForSelector('bx-shell button.chip.agent');
}
const text = (page, sel) => page.textContent(sel).then((t) => (t || '').replace(/\s+/g, ' ').trim());

module.exports = { agentChrome };
