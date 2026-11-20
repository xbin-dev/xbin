// hack/ui-harness/passes/agentchrome.js — xbin's chrome for the workspace
// agent (D232): the dock, the approval sheet, the change-set dialog, the
// tray and the lists, on /xbin/agent at desktop width and at 430 px, and
// the chip in the shell.
//
// Two halves.
//
// THE MATRIX, WITH THE DELEGATION STATE FAKED BY ROUTE INTERCEPTION: what no
// running xbind can be made to hold on demand — every request kind in every
// state, a change set with a warning, a blocked item and a failure, the
// recent-sign-in step (a --dev xbind asks for none), a hostile page in the
// dock's frame. There the pass answers /api/xbin/delegate/…, /api/xbin/llm
// and /api/xbin/agent/prefs itself, in the shapes of
// docs/protocol/delegation.md, and asserts what the chrome draws and SENDS.
//
// THE REAL HOLDER (realHolder, below): the sealed component xbin/wsagent, a
// person's own backend, the scripted model — nothing of the delegation
// intercepted. It needs sandboxes (HARNESS_ISOLATE=1; SKIP otherwise, said
// so) and, for the setup clicks, a workspace where workspace agents were
// never turned on (a fresh run). It asserts: the setup state's own buttons
// (Turn on; Just me for now) and what they give whom; the holder's page in
// the dock's sandboxed frame; a request the agent really files — the tray's
// count by xbind's `delegate` event, the sheet opened by the page's
// xbin:agent-open-request and from the tray, drawn from xbind's reading;
// Allow reaching xbind, the ticket used, the tile changed, the log's rows; a
// denial from elsewhere arriving live; a standing approval and Take back;
// Pause and Resume; an admin change set proposed, reviewed, applied and
// undone; the shell's chip and dock (the page told what it was opened
// about); a person who is no admin, before and after they are given one, and
// the alert that names /xbin/agent.
//
// The matrix asserts: each request kind approved, denied and expired; Allow not
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
// shell takes it (/xbin/agent) the dock gives a plain link; Preferences'
// mode picker offers only modes that ask (xbind refuses the others).
// Run it in both themes (HARNESS_THEME=light). Screenshots: out/agent-chrome-*.png.
const { URL, login, closeCtx, shot, checker, log, openShell, usePersonalScreen, openTile, closeTile, sh, settle, fr, waitFor, waitSel } = require('../lib');
const R = require('./wsagentpage'); // the real holder's plumbing: the scripted model, the note tile, the page's frame
const { sheetChecks } = require('./agentchrome-sheets');
const { planChecks } = require('./agentchrome-plan');

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
  const S = { state: { user: 'admin', workspace: true, paused: false, adminChanges: false, canAdminChanges: true, web: false, mcp: false, mcpReadOnly: false,
    net: { web: { policy: 'read' }, mcp: { policy: 'off', why: 'Using MCP tools is off for this workspace — an admin can change it (the workspace agent’s settings)' }, search: false } }, llm: { configured: true, enabled: true }, requests: [], approvals: [], changesets: [],
    audit: [], prefs: { agentSessions: { start: 'ask' } }, sent: [], stepUp: '', n: 0, refuse: '' };
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
      // as xbind does while the agent is stopped: nothing is allowed (409 {refusal}); deny still works
      if (S.refuse) return json(route, { error: 'the agent is stopped', refusal: S.refuse }, 409);
      r.state = 'approved'; r.grant = body.grant || 'once'; r.decided = new Date().toISOString();
      if (body.grant && body.grant !== 'once') S.approvals.push({ id: `a${++S.n}`, user: 'admin', tile: r.tile, scope: body.scope, ...(body.scope === 'call' ? { method: r.method, path: '/links/rebuild' } : body.scope === 'tool' ? { method: 'MCP', path: r.preview.tool } : {}), created: new Date().toISOString(), ...(body.grant === 'hour' ? { until: iso(3600000) } : {}) });
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
      if (seg[2] === 'ask') { for (const it of c.items) if (it.needsAdmin && !it.result) it.result = 'asked'; return json(route, c); }
      if (S.refuse) return json(route, { error: 'the agent is stopped', refusal: S.refuse }, 409);
      if (c.plan) { // a plan (agentchrome-plan.js): a line that needs an admin is never the person's to apply; a line marked failsAtApply fails, and what follows is skipped
        let failed = false;
        for (const it of c.items) {
          if (it.needsAdmin || it.result === 'asked') continue;
          if (it.blocked) it.result = 'blocked'; else if (!body.items.includes(it.n)) it.result = 'rejected';
          else if (failed) it.result = 'skipped'; else if (it.failsAtApply) { it.result = 'failed'; it.error = it.failsAtApply; failed = true; } else { it.result = 'applied'; if (it.applyOut) it.out = it.applyOut; } // (out: what xbind says it made — a builtin's update, D248)
        }
        c.state = failed ? 'partial' : 'applied';
        return json(route, c);
      }
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
  const { check, skip, done } = checker('agent-chrome');
  for (const width of [1280, 430]) {
    const tag = width < 640 ? '-430' : '';
    const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width, height: 900 } });
    const S = await fakeDelegation(ctx);
    // the chrome never navigates by itself: a navigation in the middle of the matrix is a bug, said where it happened
    page.on('framenavigated', (f) => { if (f === page.mainFrame()) log(`[${width}] navigated: ${f.url()}`); });
    page.on('crash', () => log(`[${width}] the page crashed`));
    page.on('domcontentloaded', () => log(`[${width}] a document loaded: ${page.url()}`));
    page.on('console', (m) => { if (m.type() !== 'error') log(`[${width}] console.${m.type()}: ${m.text().slice(0, 200)}`); });
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
    S.state.held = 'unavailable'; S.state.heldWhy = 'it keeps people\'s data in partitions, which need sandboxes (--isolate).'; await refresh();
    await page.waitForFunction(() => /Not available on this xbin\./.test(document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-setup').shadowRoot.textContent));
    check(/can't run here right now: it keeps people's data in partitions, which need sandboxes \(--isolate\)\. Your conversations/.test(await text(`${DOCK} bx-agent-setup .st`)), `[${width}] held for another reason: not available on this xbin, with xbind's reason`);
    delete S.state.held; delete S.state.heldWhy; S.state.workspace = false; S.state.workspaceAt = new Date().toISOString(); await refresh();
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
    // (each fact its own element, D250: the bar's words and the body's size and "shown in full" are separate)
    check(/xbin\s*approval/i.test(sheet) && /Your agent wants to change something in Crawler/.test(sheet) && /POST \/api\/apps\/crawler\/links\/rebuild\?all=1/.test(sheet) && /35 bytes\s*shown in full/.test(sheet) && !sheet.includes('·')
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
    // the wider standing scope is armed like Allow: a click as the sheet appears doesn't pick it
    await page.click(`${SHEET} [data-choice="tile"] input`, { force: true });
    const early = { label: await text(`${SHEET} button[data-allow]`), armed: await page.$eval(`${SHEET} [data-choice="tile"] input`, (e) => e.getAttribute('aria-disabled')) };
    check(early.label === 'Allow once' && early.armed === 'true', `[${width}] the wider scope ("every change") isn't armed as the sheet appears: a click then picks nothing (${JSON.stringify(early)})`);
    await armed(['bx-agent-dock', 'bx-agent-approve', '[data-choice="tile"] input']);
    await page.click(`${SHEET} [data-choice="call"] input`);
    check((await text(`${SHEET} button[data-allow]`)) === 'Allow always' && (await page.$$(`${SHEET} .dur input`)).length === 2, `[${width}] "always, this exact action": the button follows, a duration is offered`);
    await page.click(`${SHEET} [data-choice="tile"] input`);
    check(/wider/i.test(await text(`${SHEET} [data-choice="tile"]`)) && (await text(`${SHEET} button[data-allow]`)) === 'Allow every change to Crawler', `[${width}] "every change" is marked wider`);
    await armed(['bx-agent-dock', 'bx-agent-approve', 'button[data-allow]']);
    await page.click(`${SHEET} button[data-allow]`);
    await page.waitForSelector(`${SHEET} [data-step="wider"]`);
    check(sentTo(/r2\/approve$/).length === 0 && /Allow every change to Crawler\?/.test(await text(`${SHEET} [role="dialog"]`))
      && await page.$eval(`${SHEET} [data-back]`, (b) => !b.classList.contains('primary')) && await page.$eval(`${SHEET} button[data-allow="tile"]`, (b) => b.classList.contains('primary') && b.classList.contains('danger')),
    `[${width}] the wider grant asks again: Go back quiet on the left, Allow every change primary in the danger colour (D250), nothing sent yet`);
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

    // ---- a session start that asks: denied; one that never asks: the danger, Allow in its place (D250) ----
    const st = await text(`${SHEET} [role="dialog"]`);
    check(/Start a tile agent in Crawler\?/.test(st) && /asks before it edits or runs anything/i.test(st) && /Claude Code, model sonnet/.test(st) && /Fix the export test\./.test(st) && /sent exactly like this/.test(st) && !(await page.$(`${SHEET} [data-choice]`)),
      `[${width}] a tile agent start: the mode, the coding agent, the brief; no standing choice`);
    await page.click(`${SHEET} button[data-deny]`);
    await page.waitForFunction(() => !document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve'));
    check(sentTo(/s1\/deny$/).length === 1, `[${width}] Deny denies`);
    S.requests.push(sessionReq('s2', false), templateReq('t1'), callReq('r3', { state: 'expired' }));
    await refresh();
    await page.evaluate(() => document.querySelector('bx-agent-dock').review('request', 's2'));
    await page.waitForSelector(`${SHEET} [role="dialog"][data-kind="session"]`);
    check(/acts without asking/i.test(await text(`${SHEET} [data-row="mode"]`)) && /in this mode acts without asking/.test(await text(`${SHEET} .banner.bad`))
      && await page.$eval(`${SHEET} button[data-deny]`, (b) => !b.classList.contains('primary')) && (await text(`${SHEET} button[data-allow]`)) === 'Start without asking'
      && await page.$eval(`${SHEET} button[data-allow]`, (b) => b.classList.contains('primary') && b.classList.contains('danger')), `[${width}] a mode that never asks: the danger wording; Allow keeps its place, primary in the danger colour, and Deny stays quiet (D250)`);
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
    check(/xbin\s*admin changes/i.test(dl) && /Your agent proposes 4 admin changes/.test(dl) && /The agent's title — not checked by xbin/i.test(dl) && /<b>NOW<\/b>/.test(dl) && /PUBLIC/.test(dl) && /Can't be applied\./.test(dl)
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
    check(JSON.stringify(sentTo(/c1\/approve$/).map((c) => c.body)) === '[{"items":[1,2,4],"knows":["access.set","builtin.update"]},{"items":[1,2,4],"knows":["access.set","builtin.update"],"password":"right"}]', `[${width}] apply sent the ticked items (and the newer kinds it shows: D246, D248), then again with the password`);
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

    // ---- the sheets' own rules: stopped, hidden characters, a long body, an unknown state (agentchrome-sheets.js) ----
    await sheetChecks({ page, S, check, refresh, sentTo, armed, text, shot, width, tag, iso, callReq, changeSet, SHEET, DLG });
    // ---- a plan's review: lines and the lines they need, an admin's line, a start, "needs an admin", closing itself (agentchrome-plan.js) ----
    await planChecks({ page, S, check, refresh, sentTo, armed, text: (sel) => text(sel), shot, width, tag, iso, DLG });

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
    await page.click(`${DOCK} [data-menu]`); // Preferences: from the chrome's own menu (the agent's page can't open them)
    await page.click(`${DOCK} .menu [data-view="prefs"]`);
    await page.waitForSelector(`${DOCK} bx-agent-prefs [data-admin-changes]`);
    // what widens the agent's reach answers as Allow does: not at once, not a script's click, never Enter
    const PREF = (s) => ['bx-agent-dock', 'bx-agent-prefs', s];
    const unarmed = await page.$$eval(`${DOCK} bx-agent-prefs [data-widen]`, (els) => els.map((e) => `${Object.keys(e.dataset).filter((k) => k !== 'widen').join()}:${e.getAttribute('aria-disabled')}`));
    await page.click(`${DOCK} bx-agent-prefs [data-start="allow"]`, { force: true });
    await page.click(`${DOCK} bx-agent-prefs [data-net]`, { force: true });
    await armed(PREF('[data-admin-changes]'));
    await page.$eval(`${DOCK} bx-agent-prefs [data-api]`, (e) => { e.checked = false; }); // (as drawn unticked, so a click would widen)
    await page.$eval(`${DOCK} bx-agent-prefs [data-api]`, (e) => e.click()); // a script's click
    await page.focus(`${DOCK} bx-agent-prefs [data-admin-changes]`);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(150); // (a negative: nothing may be sent)
    await page.$eval(`${DOCK} bx-agent-prefs [data-api]`, (e) => { e.checked = true; }); // (back as it is)
    const held = await page.$$eval(`${DOCK} bx-agent-prefs [data-start="allow"], ${DOCK} bx-agent-prefs [data-net], ${DOCK} bx-agent-prefs [data-api], ${DOCK} bx-agent-prefs [data-admin-changes]`, (els) => els.map((e) => e.checked));
    check(unarmed.length === 7 && unarmed.every((u) => u.endsWith(':true')) && sentTo(/PUT \/agent\/prefs/).length === 0 && !S.state.adminChanges && JSON.stringify(held) === '[false,true,true,false]',
      `[${width}] Preferences: what widens the agent's reach — start without asking, the internet, other tiles, the admin opt-in — isn't armed at once, answers no script's click and no Enter (${unarmed.join(' ')}; ${JSON.stringify(held)})`);
    await armed(PREF('[data-start="allow"]'));
    await page.click(`${DOCK} bx-agent-prefs [data-admin-changes]`);
    await page.click(`${DOCK} bx-agent-prefs [data-start="allow"]`);
    for (let i = 0; i < 50 && !(S.state.adminChanges === true && sentTo(/PUT \/agent\/prefs/).length); i++) await page.waitForTimeout(40);
    check(S.state.adminChanges === true && sentTo(/PUT \/agent\/prefs/).some((c) => c.body.agentSessions.start === 'allow'), `[${width}] Preferences: the admin opt-in and the start preference are sent`);
    // the mode a started tile agent gets: a picker of the modes that ask before acting (GET /agent/providers, real);
    // the ones that don't are named with why, and xbind refuses them anyway
    await page.waitForSelector(`${DOCK} bx-agent-prefs select[data-mode="claude"]`);
    const modeOpts = await page.$$eval(`${DOCK} bx-agent-prefs select[data-mode="claude"] option`, (os) => os.map((o) => o.value));
    const never = await text(`${DOCK} bx-agent-prefs [data-never="claude"]`);
    check(modeOpts[0] === '' && modeOpts.includes('plan') && !modeOpts.includes('bypassPermissions') && /Bypass permissions/.test(never) && /without asking you first/.test(never),
      `[${width}] Preferences: the modes for agent-started sessions are a picker of the ones that ask, the others named with why (${modeOpts.join(' ')} · ${never})`);
    await page.selectOption(`${DOCK} bx-agent-prefs select[data-mode="claude"]`, 'plan');
    for (let i = 0; i < 50 && !sentTo(/PUT \/agent\/prefs/).some((c) => c.body.agentSessions.modes); i++) await page.waitForTimeout(40);
    check(JSON.stringify(sentTo(/PUT \/agent\/prefs/).filter((c) => c.body.agentSessions.modes).map((c) => c.body.agentSessions.modes)) === '[{"claude":"plan"}]'
      && (await page.$eval(`${DOCK} bx-agent-prefs select[data-mode="claude"]`, (s) => s.value)) === 'plan', `[${width}] …picking one writes the preferences route ({agentSessions: {modes: {claude: plan}}}) and shows it`);
    await page.selectOption(`${DOCK} bx-agent-prefs select[data-mode="claude"]`, '');
    for (let i = 0; i < 50 && sentTo(/PUT \/agent\/prefs/).filter((c) => c.body.agentSessions.modes).length < 2; i++) await page.waitForTimeout(40);
    check(JSON.stringify(sentTo(/PUT \/agent\/prefs/).filter((c) => c.body.agentSessions.modes).pop().body.agentSessions.modes) === '{"claude":null}', `[${width}] …and "its own asking mode" clears it`);
    const fits = await page.$eval(`${DOCK} bx-agent-prefs select[data-mode="claude"]`, (sel) => { const d = document.querySelector('bx-agent-dock').getBoundingClientRect(), r = sel.getBoundingClientRect(); return r.right <= d.right + 1 && r.left >= d.left - 1; });
    check(fits, `[${width}] …the picker fits the dock`);
    const refused = await ctx.request.put(`${URL}/api/xbin/agent/prefs`, { data: { agentSessions: { modes: { claude: 'bypassPermissions' } } } }); // (the real route: page routes don't reach ctx.request)
    const refusedWhy = (await refused.json().catch(() => ({}))).error || '';
    check(refused.status() === 400 && /can't be saved/.test(refusedWhy), `[${width}] …and xbind itself refuses a mode that never asks (${refused.status()} ${refusedWhy})`);
    // the web and MCP tools (D239): turning one on widens — armed like Allow; one the workspace policy has off says why and can't be ticked
    const netRows = await page.$$eval(`${DOCK} bx-agent-prefs [data-net-pref]`, (els) => els.map((e) => `${e.dataset.netPref}:${e.disabled}`));
    await page.$eval(`${DOCK} bx-agent-prefs [data-net-pref="web"]`, (e) => e.click()); // a script's click
    await page.waitForTimeout(150); // (a negative: nothing may be sent)
    const webEarly = sentTo(/PUT \/state$/).some((c) => c.body && c.body.web === true);
    await armed(PREF('[data-net-pref="web"]'));
    await page.click(`${DOCK} bx-agent-prefs [data-net-pref="web"]`);
    for (let i = 0; i < 50 && S.state.web !== true; i++) await page.waitForTimeout(40);
    const mcpWhy = await text(`${DOCK} bx-agent-prefs [data-net-tools]`);
    check(netRows.join(' ') === 'web:false mcp:true mcpReadOnly:true' && !webEarly && S.state.web === true && /Using MCP tools is off for this workspace — an admin can change it/.test(mcpWhy),
      `[${width}] Preferences: the web is the person's to turn on, armed like Allow; MCP, off by policy, says why (${netRows.join(' ')})`);
    await shot(page, `agent-chrome-prefs${tag}`, { fullPage: false });
    // outside MCP servers (D240): the ones the person's agent may use are listed under the MCP toggle, by name and host
    const netWas = S.state.net;
    S.state.net = { ...netWas, mcp: { policy: 'listed', servers: [{ server: 'ext:tracker', name: 'Issue tracker', host: 'mcp.tracker.example' }, { server: 'ext:wiki', name: 'Wiki', host: 'wiki.example.org' }] } };
    await refresh();
    await page.waitForSelector(`${DOCK} bx-agent-prefs [data-mcp-servers]`);
    const outsideList = await text(`${DOCK} bx-agent-prefs [data-net-tools]`);
    check(/Issue tracker\s*mcp\.tracker\.example/.test(outsideList) && /Wiki\s*wiki\.example\.org/.test(outsideList) && /leaves the workspace/.test(outsideList),
      `[${width}] Preferences: the outside servers the agent may use, by name and host, and that a call to one leaves the workspace (${outsideList.replace(/\s+/g, ' ').slice(-300)})`);
    await shot(page, `agent-chrome-prefs-outside${tag}`, { fullPage: false });
    S.state.net = netWas; await refresh();

    // ---- a hostile page in the dock's frame cannot grant ----
    S.requests = [callReq('0123456789abcdef0123abcd')]; S.sent.length = 0;
    await ctx.route('**/hostile.html', (route) => route.fulfill({ contentType: 'text/html', body: `<!doctype html><body style="margin:0;background:#400"><h1>the agent's page</h1>
      <div style="position:fixed;inset:-4000px;z-index:2147483647;background:rgba(255,0,0,.4)">COVER</div><script>
      const out = [];
      addEventListener('message', (e) => { if (e.data && e.data.type === 'xbin:agent-context') { out.push('about ' + e.data.tile); parent.postMessage({ type: 'xbin:open-session', session: 's-9', tile: e.data.tile, grant: 'always' }, '*'); } });
      try { parent.document.querySelector('bx-agent-dock'); out.push('READ THE PARENT'); } catch (e) { out.push('parent closed'); }
      for (const m of [{ type: 'xbin:agent-open', view: 'prefs' }, { type: 'xbin:agent-approve', id: '0123456789abcdef0123abcd' }, { type: 'xbin:agent-decide', id: '0123456789abcdef0123abcd', verdict: 'allow', grant: 'always', scope: 'tile' },
        { type: 'xbin:agent-review', kind: 'request', id: '0123456789abcdef0123abcd' }, { type: 'xbin:agent-open-request', id: '0123456789abcdef0123abcd', request: { method: 'GET', target: '/harmless', summary: 'PAGE TEXT' }, approve: true, grant: 'always' }]) parent.postMessage(m, '*');
      fetch('/api/xbin/delegate/requests/0123456789abcdef0123abcd/approve', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{"grant":"always","scope":"tile"}' }).then((r) => out.push('fetch ' + r.status), () => out.push('fetch refused'));
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
    check(await page.evaluate(() => { const d = document.querySelector('bx-agent-dock').shadowRoot; return !d.querySelector('bx-agent-prefs') && !!d.querySelector('iframe'); }),
      `[${width}] the page can't open Preferences (xbin:agent-open {view: prefs}): the dock stays on the conversation`);
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
    check(sentTo(/0123456789abcdef0123abcd\/approve$/).filter((c) => c.top).length === 1, `[${width}] …and the person's own click still allows`);
    // what a page may open: a view that changes nothing (Needs you), never Preferences
    await ctx.route('**/opens.html', (route) => route.fulfill({ contentType: 'text/html', body: `<!doctype html><body><script>
      parent.postMessage({ type: 'xbin:agent-open', view: 'prefs' }, '*'); setTimeout(() => parent.postMessage({ type: 'xbin:agent-open', view: 'needs' }, '*'), 150);</script>` }));
    await page.evaluate(() => { document.querySelector('bx-agent-dock').page = '/opens.html'; });
    await page.waitForSelector(`${DOCK} [data-needs][aria-expanded="true"]`);
    check(await page.evaluate(() => !document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-prefs')), `[${width}] …a page opens Needs you (xbin:agent-open {view: needs}), and still not Preferences`);
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
    await handOff(page, ctx, check, wasOpen, skip);
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
    // a tile dev1 can read: the passes before this one leave dev1 a Home with
    // the default tiles, and a card dev1 can't read has no tile menu at all
    const mine = new Set((await (await U.ctx.request.get(`${URL}/api/xbin/components`)).json().catch(() => [])).map?.((c) => c.path) || []);
    const own = (await sh(U.page, (t) => (t.openTiles || []).map((o) => o.path))).find((p) => mine.has(p)) || '';
    if (own) {
      await U.page.click(`.card[data-path="${own}"] button.wc[aria-label="tile menu"]`);
      await waitSel(U.page, 'bx-menu .panel.main .it');
      const ls = await U.page.locator('bx-menu .panel.main .it .lb').allTextContents();
      check(ls.length > 0 && !ls.some((l) => /Ask my agent/.test(l)), `…nor "Ask my agent about this tile" in a tile's menu (${ls.join(' · ')})`);
      await U.page.keyboard.press('Escape');
    } else check(false, 'dev1 has a tile on their screen to open the menu of');
    await U.ctx.close();
  }
  await realHolder(browser, check, skip);
  done();
}

// ---- against the REAL holder: xbin/wsagent, a person's own backend, the scripted model -------------------------
//
// Nothing of /api/xbin/delegate/… is intercepted here: what the chrome
// sends reaches xbind, and what it shows is what xbind and the agent's
// backend answered. The model is hack/fakeopenai, scripted by the message
// ("ws: tool {json} ;; …"); the tile the agent changes is apps/wsnote
// (wsagentpage.js). What it changes (the switch, who has one, pause, the
// access entries) is put back as it found it, however it ends.
async function realHolder(browser, check, skip) {
  if (R.noHolder()) { await realHolderRun(browser, check, skip); return; }
  const K = await login(browser, 'admin', 'admin');
  const kcall = R.caller(K.ctx);
  const found = (await R.shipped(kcall)) === R.AGENT ? await R.snapshot(kcall) : null;
  await K.ctx.close();
  try {
    await realHolderRun(browser, check, skip);
  } finally {
    if (found) await R.putBackChecked(browser, found, check);
  }
}

async function realHolderRun(browser, check, skip) {
  const why = R.noHolder();
  if (why) { skip(`the real holder (${R.AGENT}): ${why} — only the faked matrix ran`); return; }
  const stamp = Date.now().toString(36);
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1280, height: 900 } });
  const call = R.caller(ctx);
  const st0 = (await call('GET', '/delegate/state')).data || {};
  const ships = await R.shipped(call); // (the state names the agent only to someone given one — an admin isn't, by the role alone)
  if (ships !== R.AGENT) { skip(`the real holder: this xbind ships no ${R.AGENT} (GET /delegate/workspace: agent ${JSON.stringify(ships)}) — only the faked matrix ran`); await ctx.close(); return; }
  const D = await login(browser, 'dev1', 'devpass123', { viewport: { width: 1400, height: 900 } }); // a person who is no admin
  const dcall = R.caller(D.ctx);
  const text = (sel) => page.textContent(sel).then((t) => (t || '').replace(/\s+/g, ' ').trim());
  const state = async (c = call) => (await c('GET', '/delegate/state')).data || {};
  const pending = async () => (((await call('GET', '/delegate/requests')).data || {}).requests || []).filter((r) => r.state === 'pending');
  const audit = async () => ((await call('GET', '/delegate/audit')).data || {}).calls || [];
  const note = async () => (await call('GET', `/api/${R.NOTE}/note`)).text;
  const agentAlert = async (c) => (((await c('GET', '/alerts')).data || {}).alerts || []).find((a) => a.kind === 'agent-page');
  const count = async () => { const e = await page.$(`${DOCK} [data-needs] [data-count]`); return e ? (await e.textContent()).trim() : ''; };
  const waitCount = (n) => R.until(async () => (await count()) === (n ? String(n) : ''), `Needs you counts ${n || 'nothing'}`, 20000, 100);
  const sheetId = () => page.evaluate(() => { const s = document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve'); return s && s.request ? s.request.id : ''; });
  const noSheet = () => page.waitForFunction(() => !document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-approve'));
  const put = (body, said) => `ws: tile_call {"tile":"${R.NOTE}","method":"PUT","path":"/note","body":"\\"${body}\\"","why":"${said}"}`;
  const dock = (fn, arg) => page.evaluate(`(${fn.toString()})(document.querySelector('bx-agent-dock'), ${JSON.stringify(arg ?? null)})`);

  // ---- setup, through the chrome's own state: Turn on, then who gets one ----
  await R.connectModel(call); // (the model is /xbin/llm's to connect — the llm pass; the step here only reads it)
  await page.goto(`${URL}/xbin/agent`);
  await page.waitForSelector(`${DOCK} header`);
  if (!st0.workspace && !st0.workspaceAt) {
    await page.waitForSelector(`${DOCK} bx-agent-setup [data-state="setup"] [data-step="on"] button`);
    const steps = await page.$$eval(`${DOCK} bx-agent-setup [data-step]`, (els) => els.map((e) => `${e.dataset.step}${e.classList.contains('done') ? '+' : ''}${e.classList.contains('off') ? '-' : ''}`));
    check(steps.join(' ') === 'model+ on who-' && !(await page.$(`${DOCK} [data-who]`)) && !(await page.$(`${DOCK} iframe`)), `[real] before setup, a model connected: step 1 done, Turn on offered, step 3 waits (${steps.join(' ')})`);
    const d0 = await state(dcall);
    check(d0.workspace === false && d0.access === false && d0.who === undefined && !(await agentAlert(dcall)), `[real] a person who is no admin has nothing of it yet (${JSON.stringify({ workspace: d0.workspace, access: d0.access })})`);
    await page.click(`${DOCK} bx-agent-setup [data-step="on"] button`);
    await page.waitForSelector(`${DOCK} bx-agent-setup [data-state="setup"] [data-who="everyone"]`);
    const s1 = await state();
    const who = await page.$$eval(`${DOCK} bx-agent-setup [data-who]`, (els) => els.map((e) => `${e.dataset.who}:${e.textContent.trim()}${e.classList.contains('primary') ? '*' : ''}`));
    check(s1.workspace === true && s1.who && s1.who.chosen === false && who.join(' | ') === 'everyone:Everyone* | me:Just me for now' && !(await page.$(`${DOCK} iframe`)),
      `[real] "Turn on" turned workspace agents on at xbind; nobody has one yet, so the third step asks who (${who.join(' | ')})`);
    check((await state(dcall)).access === false && !(await agentAlert(dcall)), '[real] …and on alone gives nobody an agent: the other person still has none, and no notice');
    await shot(page, 'agent-chrome-real-setup', { fullPage: false });
    await page.click(`${DOCK} bx-agent-setup [data-who="me"]`);
    await page.waitForSelector(`${DOCK} iframe`, { state: 'attached', timeout: 30000 });
    const s2 = await state();
    check(s2.who && s2.who.chosen === true && s2.who.everyone === false && s2.who.entries === 1 && s2.access === true, `[real] "Just me for now" gave the admin one (${JSON.stringify(s2.who)}): the setup steps make way for the page`);
    check((await state(dcall)).access === false && !(await agentAlert(dcall)), '[real] …and nobody else');
  } else {
    skip('[real] the setup state\'s own buttons (Turn on; Just me for now): a first-run step — once workspace agents were ever turned on here, xbind keeps when (workspaceAt) and the chrome shows Off, never the setup again; a fresh workspace, with this pass before wsAgentPage, clicks them');
    await R.agentOn(call);
    await page.reload();
  }

  // ---- the holder's own page, in the dock's frame ----
  const t0 = Date.now();
  let f = await R.agentFrame(page);
  log(`agentChrome: the holder's page was up ${Math.round((Date.now() - t0) / 1000)} s after it was given (a --dev xbind compiles its backend on first use)`);
  const fr = await page.$eval(`${DOCK} iframe`, (e) => ({ src: e.getAttribute('src'), sandbox: e.getAttribute('sandbox'), title: e.title }));
  check(fr.src.startsWith(`/c/${R.AGENT}/`) && /allow-scripts/.test(fr.sandbox) && !/allow-same-origin|allow-top-navigation/.test(fr.sandbox) && fr.title === 'Your workspace agent',
    `[real] the dock frames the holder's page, sandboxed (${fr.src.replace(/\?.*/, '?…')}; ${fr.sandbox})`);
  const inside = await f.evaluate(() => { let p = 'closed'; try { p = parent.document ? 'READ THE PARENT' : 'closed'; } catch { /* another origin */ } return { p, page: !!document.querySelector('#ws-page .wsp'), self: globalThis.xbin && globalThis.xbin.self }; });
  check(inside.page && inside.self === R.AGENT && inside.p === 'closed', `[real] …the agent's own page (ws/page.js), which cannot read the chrome (${JSON.stringify(inside)})`);
  const frameApprove = await f.evaluate(async () => { const r = await globalThis.xbin.fetch('/api/xbin/delegate/workspace', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: '{"who":"everyone"}' }); return r.status; });
  check(frameApprove >= 400 && (await state()).who.everyone === (st0.who ? !!st0.who.everyone : false), `[real] …nor decide who gets one (its credential: ${frameApprove})`);
  log(`agentChrome: ${R.NOTE} says ${await R.noteTile(call)}`);
  await R.nothingWaits(call);
  await waitCount(0);
  await R.pickMain(f);

  // ---- a request the agent files: the tray by xbind's event, the sheet from the page and from the tray ----
  const w0 = Number((/writes=(\d+)/.exec(await note()) || [])[1]);
  const body1 = `allowed once ${stamp}`;
  const turn1 = await R.say(f, put(body1, `set the note ${stamp}`));
  await waitCount(1);
  const p1 = await pending();
  const id1 = p1.length ? p1[0].id : '';
  check(R.PENDING.test(turn1) && p1.length === 1 && p1[0].agent === R.AGENT && p1[0].kind === 'call' && p1[0].target === `/api/${R.NOTE}/note` && p1[0].body === `"${body1}"`,
    `[real] the agent's tile_call filed a request at xbind, exactly as it will be sent (${id1}: ${p1[0] && p1[0].method} ${p1[0] && p1[0].target}; ${p1.length} pending, body ${p1[0] && JSON.stringify(p1[0].body)}; the turn: ${turn1.slice(0, 160)})`);
  check((await count()) === '1' && /^\(1\) /.test(await page.title()), `[real] Needs you counts it without a reload — xbind's delegate event (${await page.title()})`);
  check((await note()).includes(`writes=${w0}`), '[real] nothing ran: the note is as it was');
  await f.waitForSelector(`.wsc-req[data-ref="${id1}"][data-state="pending"]`);
  await f.click(`.wsc-req[data-ref="${id1}"] [data-marker]`); // (the card's one control: the marker that opens it, D250)
  await page.waitForSelector(`${SHEET} [role="dialog"]`);
  const sheet = await text(`${SHEET} [role="dialog"]`);
  check((await sheetId()) === id1 && /xbin\s*approval/i.test(sheet) && /Your agent wants to change something in Wsnote/.test(sheet) && sheet.includes(`PUT /api/${R.NOTE}/note`) && sheet.includes(`"${body1}"`),
    `[real] the page's Review… (xbin:agent-open-request {id}) opens the sheet, drawn from xbind's reading (${sheet.slice(0, 150)})`);
  check(sheet.includes(`${p1[0] ? p1[0].bodySize : '?'} bytesshown in full`) && /you can write here/.test(sheet) && /xbin can't undo this/.test(sheet), `[real] …the body in full, the person's level in words (${(/Sends.*?Undo/.exec(sheet) || [''])[0].slice(0, 90)})`);
  check(/The agent's own words — not checked by xbin/i.test(sheet) && sheet.includes(`set the note ${stamp}`) && /Always, this exact action/.test(sheet) && /EVERY write to apps\/wsnote's API/.test(sheet),
    '[real] …the agent\'s sentence quoted as the agent\'s; the standing choices in xbind\'s own words');
  await shot(page, 'agent-chrome-real-approve', { fullPage: false });
  await page.keyboard.press('Escape');
  await noSheet();
  check((await pending()).length === 1 && (await count()) === '1', '[real] Escape is "not now": the request still waits at xbind');
  await page.click(`${DOCK} [data-needs]`);
  await page.waitForSelector(`${DOCK} bx-agent-needs li[data-id="${id1}"]`);
  const row = await text(`${DOCK} bx-agent-needs li[data-id="${id1}"]`);
  check(/Approve a change to Wsnote/.test(row) && /Review…/.test(row) && (await page.$$(`${DOCK} bx-agent-needs li`)).length === 1, `[real] the tray lists it (${row})`);
  await shot(page, 'agent-chrome-real-tray', { fullPage: false });
  await page.click(`${DOCK} bx-agent-needs li[data-id="${id1}"] button`);
  await page.waitForSelector(`${SHEET} [role="dialog"]`);
  check((await sheetId()) === id1, '[real] …and its Review… opens the same sheet');
  await R.armed(page, ['bx-agent-dock', 'bx-agent-approve', 'button[data-allow]']);
  await page.click(`${SHEET} button[data-allow]`);
  await noSheet();
  const used = await R.until(async () => { const r = (await call('GET', `/delegate/requests/${id1}`)).data || {}; return r.state === 'used' ? r : null; }, `the request ${id1} is used`, 30000);
  const n1 = await R.until(async () => { const n = await note(); return n.includes(body1) ? n : ''; }, 'the note changed', 15000);
  check(used.grant === 'once' && n1.includes(`note="${body1}" writes=${w0 + 1}`) && n1.includes(`user=admin agent=${R.AGENT}`), `[real] Allow once reached xbind and the ticket was used: the tile changed, once, by the person's agent (${n1})`);
  const rows1 = await audit();
  const ran = rows1.find((r) => r.method === 'PUT' && r.path === `/api/${R.NOTE}/note` && !r.dryRun) || {};
  const decided = rows1.find((r) => /^decision:/.test(r.class || '')) || {};
  log(`agentChrome: the log's rows: ${JSON.stringify(ran)} ${JSON.stringify(decided)}`);
  check(ran.status === 200 && ran.agent === R.AGENT && ran.ticket === id1 && /^decision:/.test(decided.class || ''), `[real] GET /delegate/audit shows the call under its ticket, and the person's decision (${ran.class} ${ran.status} ticket ${ran.ticket}; ${decided.class})`);
  await waitCount(0);
  check(!/^\(/.test(await page.title()), `[real] the tray empties by itself (${await page.title()})`);
  await f.waitForSelector(`.wsc-req[data-ref="${id1}"][data-state="used"]`, { timeout: 30000 });
  check(/Allowed once by you/.test(await f.textContent(`.wsc-req[data-ref="${id1}"]`)), '[real] …and the page\'s card says what came of it');
  await dock((d) => d.show('activity'));
  await page.waitForSelector(`${DOCK} bx-agent-activity li`);
  const did = await page.$$eval(`${DOCK} bx-agent-activity li`, (els) => els.map((e) => e.textContent.replace(/\s+/g, ' ').trim()));
  log(`agentChrome: What my agent did: ${JSON.stringify(did.slice(0, 6))}`);
  check(did.length >= 2 && /^Changed Wsnote/.test(did[0]) && did[0].includes(`PUT /api/${R.NOTE}/note`) && /you allowed it once/.test(did[0]) && /^You allowed a request\s*Wsnote · once/.test(did[1]),
    `[real] What my agent did says the change and the person's decision in sentences — and not its asking (${did.join(' | ')})`);
  await page.click(`${DOCK} bx-agent-activity [data-filter="all"]`);
  const every = await R.until(async () => { const all = await page.$$eval(`${DOCK} bx-agent-activity li`, (els) => els.map((e) => e.textContent.replace(/\s+/g, ' ').trim())); return all.length > did.length ? all : null; }, 'Everything lists more');
  check(every.some((x) => /^Asked for your approval/.test(x)) && every.some((x) => /^Checked what a change would do in Wsnote/.test(x)) && !every.some((x) => /true|undefined|\[object|Changed (POST|DELETE) /.test(x)),
    `[real] …Everything adds what it asked and checked (${every.slice(0, 5).join(' | ')})`);
  await shot(page, 'agent-chrome-real-activity', { fullPage: false });
  await page.click(`${DOCK} bx-agent-activity [data-back]`);
  f = await R.agentFrame(page);

  // ---- a decision made elsewhere arrives live; a standing approval, used and taken back ----
  await R.say(f, put(`denied ${stamp}`, 'a second change'));
  await waitCount(1);
  const id2 = (await pending())[0].id;
  check((await call('POST', `/delegate/requests/${id2}/deny`, {})).status === 200, '[real] the person denies a second request somewhere else (their own session, another client)');
  await waitCount(0);
  await f.waitForSelector(`.wsc-req[data-ref="${id2}"][data-state="denied"]`, { timeout: 30000 });
  check(/Denied by you/.test(await f.textContent(`.wsc-req[data-ref="${id2}"]`)) && (await note()).includes(`writes=${w0 + 1}`), '[real] the tray and the page\'s card follow without a reload; nothing ran');
  const body3 = `always ${stamp}`;
  await R.say(f, put(body3, 'a third change'));
  await waitCount(1);
  const id3 = (await pending())[0].id;
  await page.click(`${DOCK} [data-needs]`);
  await page.click(`${DOCK} bx-agent-needs li[data-id="${id3}"] button`);
  await page.waitForSelector(`${SHEET} [role="dialog"]`);
  await page.click(`${SHEET} [data-choice="call"] input`);
  check((await text(`${SHEET} button[data-allow]`)) === 'Allow always', '[real] "Always, this exact action" on a real request');
  await R.armed(page, ['bx-agent-dock', 'bx-agent-approve', 'button[data-allow]']);
  await page.click(`${SHEET} button[data-allow]`);
  await noSheet();
  await R.until(async () => (await note()).includes(body3), 'the third change ran', 30000);
  const ap = ((await call('GET', '/delegate/approvals')).data || {}).approvals || [];
  const forever = ap.length === 1 && !(Date.parse(ap[0].until) > 0); // (a time never set is year 1)
  check(ap.length === 1 && ap[0].scope === 'call' && ap[0].tile === R.NOTE && (await state()).standingApprovals === 1, `[real] the approval stands at xbind (${JSON.stringify(ap.map((a) => [a.scope, a.method, a.path, forever ? 'until taken back' : a.until]))})`);
  const body4 = `under it ${stamp}`;
  const turn4 = await R.say(f, put(body4, 'a fourth change'));
  const n4 = await note();
  const under = (await audit()).find((r) => r.method === 'PUT' && r.path === `/api/${R.NOTE}/note` && !r.dryRun) || {};
  check(/PUT \/note → 200/.test(turn4) && n4.includes(`note="${body4}" writes=${w0 + 3}`) && (await pending()).length === 0 && (await count()) === '' && under.under === `approval:${ap[0] && ap[0].id}`,
    `[real] the same action again runs without asking, logged as under that standing approval (${turn4.slice(0, 60)}; under ${under.under})`);
  await dock((d) => d.show('approvals'));
  await page.waitForSelector(`${DOCK} bx-agent-approvals li`);
  const standing = await text(`${DOCK} bx-agent-approvals li`);
  check((await page.$$(`${DOCK} bx-agent-approvals li`)).length === 1 && /Wsnote/.test(standing) && standing.includes(`this action: PUT /api/${R.NOTE}/note`) && /Take back/.test(standing)
    && (forever ? /· until you take it back/.test(standing) : /· ends in \d+ min/.test(standing)), `[real] Approvals lists it, and how long it lasts (${standing})`);
  await shot(page, 'agent-chrome-real-approvals', { fullPage: false });
  await page.click(`${DOCK} bx-agent-approvals li button`);
  await page.waitForSelector(`${DOCK} bx-agent-approvals [data-empty]`);
  check((((await call('GET', '/delegate/approvals')).data || {}).approvals || []).length === 0, '[real] Take back ends it at xbind');
  await page.click(`${DOCK} bx-agent-approvals [data-back]`);
  f = await R.agentFrame(page);
  const turn5 = await R.say(f, put(`asks again ${stamp}`, 'a fifth change'));
  await waitCount(1);
  check(R.PENDING.test(turn5) && (await note()).includes(`writes=${w0 + 3}`), `[real] …and the next such change asks again (${turn5.slice(0, 120)}; ${await note()})`);
  await R.nothingWaits(call);
  await waitCount(0);
  await dock((d) => d.show('activity'));
  await page.waitForSelector(`${DOCK} bx-agent-activity li`);
  const did2 = await page.$$eval(`${DOCK} bx-agent-activity li`, (els) => els.map((e) => e.textContent.replace(/\s+/g, ' ').trim()));
  check(did2.some((x) => /^You denied a request\s*Wsnote/.test(x)) && did2.some((x) => /^Changed Wsnote.* · under a standing approval of yours/.test(x))
    && did2.some((x) => /^You allowed a request\s*Wsnote · (until you take it back|for one hour), this action/.test(x)) && !did2.some((x) => /Changed (POST|PATCH|DELETE) |true|undefined/.test(x)),
  `[real] What my agent did tells a denial from an allowance, and a change under a standing approval (${did2.slice(0, 5).join(' | ')})`);
  await page.click(`${DOCK} bx-agent-activity [data-back]`);
  f = await R.agentFrame(page);

  // ---- Pause in the header: at once, xbin's own state in place of the page; Resume ----
  await page.click(`${DOCK} [data-pause]`);
  await page.waitForSelector(`${DOCK} bx-agent-setup [data-state="paused"]`);
  const ps = await state();
  const refused = (await call('GET', `/api/${R.AGENT}/ws/state`)).data || {};
  check(ps.paused === true && !!ps.pausedAt && /Paused by you at \d\d:\d\d\./.test(await text(`${DOCK} bx-agent-setup .st`)) && !(await page.$(`${DOCK} iframe`)) && (await text(`${DOCK} [data-pause]`)) === 'Resume',
    `[real] Pause: paused at xbind, and the dock says so in place of the page (${(await text(`${DOCK} bx-agent-setup .st`)).slice(0, 60)})`);
  check(!refused.delegation && /paused/.test(refused.delegationError || ''), `[real] …it stops the agent: xbind refuses its backend's calls (${String(refused.delegationError || '').slice(0, 70)})`);
  await shot(page, 'agent-chrome-real-paused', { fullPage: false });
  await page.click(`${DOCK} bx-agent-setup [data-resume]`);
  f = await R.agentFrame(page);
  check((await state()).paused === false && (await text(`${DOCK} [data-pause]`)) === 'Pause', '[real] Resume: the person\'s own click, and the page is back');

  // ---- an admin's change set: proposed by the agent, reviewed, applied in xbin's dialog, undone ----
  await dock((d) => d.show('prefs'));
  await page.waitForSelector(`${DOCK} bx-agent-prefs [data-admin-changes]`);
  await page.waitForFunction(() => document.querySelector('bx-agent-dock').shadowRoot.querySelector('bx-agent-prefs').shadowRoot.querySelector('[data-admin-changes]').getAttribute('aria-disabled') === 'false'); // (armed, as Allow)
  if (!(await state()).adminChanges) await page.click(`${DOCK} bx-agent-prefs [data-admin-changes]`);
  await R.until(async () => (await state()).adminChanges === true, 'adminChanges is on at xbind');
  check(true, '[real] Preferences: "Let my agent propose admin changes" is the admin\'s own switch at xbind');
  await page.click(`${DOCK} bx-agent-prefs [data-back]`);
  f = await R.agentFrame(page);
  const grant = { from: 'apps/welcome', target: R.NOTE, role: 'reader' };
  const grants = async () => (((await call('GET', '/grants')).data || {}).grants || []).some((g) => g.from === grant.from && g.target === grant.target && g.role === grant.role);
  if (await grants()) await call('DELETE', '/grants', grant); // (an earlier run that stopped half-way)
  const turn6 = await R.say(f, `ws: admin_propose {"title":"Let welcome read the note ${stamp}","changes":[{"kind":"grant.add","args":${JSON.stringify(grant)},"why":"welcome shows the note"}]}`);
  await waitCount(1);
  const sets = (((await call('GET', '/delegate/changesets')).data || {}).changesets || []).filter((c) => c.state === 'proposed');
  const cid = sets.length ? sets[0].id : '';
  log(`agentChrome: the change set: ${JSON.stringify(sets[0] || null).slice(0, 600)}`);
  check(/^Done: Change set /.test(turn6) && sets.length === 1 && sets[0].items.length === 1 && !!sets[0].items[0].line && !(await grants()), `[real] admin_propose filed a change set at xbind, in xbind's own words; nothing applied (${sets[0] && sets[0].items[0].line})`);
  await f.waitForSelector(`.wsc-req[data-kind="changeset"][data-ref="${cid}"]`);
  await f.click(`.wsc-req[data-ref="${cid}"] [data-marker]`);
  await page.waitForSelector(`${DLG} [role="dialog"][data-view="review"]`);
  const dl = await text(`${DLG} [role="dialog"]`);
  const ticks = await page.$$eval(`${DLG} .item`, (els) => els.map((e) => `${e.dataset.n}${e.querySelector('input').checked ? '+' : '-'}${e.querySelector('input').disabled ? 'x' : ''}`));
  check((await page.evaluate(() => document.querySelector('bx-agent-changes').set.id)) === cid && /xbin\s*admin changes/i.test(dl) && /Your agent proposes 1 admin change/.test(dl) && dl.includes(sets[0].items[0].line)
    && /The agent's title — not checked by xbin/i.test(dl) && dl.includes(`Let welcome read the note ${stamp}`) && /Agent's reason — not checked by xbin/i.test(dl) && /welcome shows the note/.test(dl),
  `[real] the page's Review… (xbin:agent-open-changeset {id}) opens xbin's dialog over the page: xbind's line, the agent's title and reason quoted (${dl.slice(0, 140)}; ${ticks.join(' ')})`);
  await shot(page, 'agent-chrome-real-changes', { fullPage: false });
  await page.keyboard.press('Escape');
  await page.waitForFunction(() => !document.querySelector('bx-agent-changes'));
  await page.click(`${DOCK} [data-needs]`);
  await page.waitForSelector(`${DOCK} bx-agent-needs li[data-kind="changeset"][data-id="${cid}"]`);
  await page.click(`${DOCK} bx-agent-needs li[data-id="${cid}"] button`);
  await page.waitForSelector(`${DLG} [role="dialog"][data-view="review"]`);
  if (!ticks.includes('1+')) await page.check(`${DLG} .item[data-n="1"] input`);
  await R.armed(page, ['bx-agent-changes', 'button[data-apply]']);
  await page.click(`${DLG} [data-apply]`);
  await page.waitForSelector(`${DLG} [role="dialog"]:is([data-view="results"], [data-view="stepup"])`);
  const asked = (await page.getAttribute(`${DLG} [role="dialog"]`, 'data-view')) === 'stepup';
  if (asked) {
    check(!!(await page.$(`${DLG} [data-stepup="password"]`)) && !(await grants()), '[real] xbind asks for a recent sign-in first: the password step, nothing applied yet');
    await page.fill(`${DLG} #pw`, 'admin');
    await R.armed(page, ['bx-agent-changes', 'button[data-apply]']);
    await page.click(`${DLG} [data-apply]`);
    await page.waitForSelector(`${DLG} [role="dialog"][data-view="results"]`);
  } else skip('[real] the recent-sign-in step: this xbind asked for none — a --dev xbind never does (docs/protocol/delegation.md "Developing an agent") — so it stays in the faked matrix');
  const res = await page.$$eval(`${DLG} .item [data-result]`, (els) => els.map((e) => e.dataset.result));
  const applied = (await audit()).some((r) => r.class === 'changeset:grant.add' && r.status < 400);
  check(res.join(' ') === 'applied' && (await grants()) && applied, `[real] Apply applied it as the admin: the grant is there, the log has it (${await text(`${DLG} h2`)}; ${res.join(' ')})`);
  await waitCount(0);
  await f.waitForSelector(`.wsc-req[data-ref="${cid}"][data-state="applied"]`, { timeout: 30000 });
  check(/1 of 1 changes? applied/.test(await f.textContent(`.wsc-req[data-ref="${cid}"]`)), `[real] …and the page's card says what came of it (${(await f.textContent(`.wsc-req[data-ref="${cid}"]`)).replace(/\s+/g, ' ').trim().slice(0, 120)})`);
  await shot(page, 'agent-chrome-real-results', { fullPage: false });
  await page.click(`${DLG} [data-undo]`);
  await page.waitForFunction((id) => { const d = document.querySelector('bx-agent-changes'); return !!(d && d.set && d.set.undoOf === id); }, cid);
  check((await page.getAttribute(`${DLG} [role="dialog"]`, 'data-view')) === 'review' && (await grants()), '[real] Undo proposes a new set at xbind, to review like any other: nothing is undone by the click');
  if (!(await page.$eval(`${DLG} .item input`, (i) => i.checked))) await page.check(`${DLG} .item input`);
  await R.armed(page, ['bx-agent-changes', 'button[data-apply]']);
  await page.click(`${DLG} [data-apply]`);
  await page.waitForSelector(`${DLG} [role="dialog"]:is([data-view="results"], [data-view="stepup"])`);
  if ((await page.getAttribute(`${DLG} [role="dialog"]`, 'data-view')) === 'stepup') { await page.fill(`${DLG} #pw`, 'admin'); await R.armed(page, ['bx-agent-changes', 'button[data-apply]']); await page.click(`${DLG} [data-apply]`); await page.waitForSelector(`${DLG} [role="dialog"][data-view="results"]`); }
  check(!(await grants()), '[real] …and applying that one takes the grant away again');
  await page.click(`${DLG} [data-done]`);
  await page.waitForFunction(() => !document.querySelector('bx-agent-changes'));
  await call('PUT', '/delegate/state', { adminChanges: false });
  await R.nothingWaits(call);

  // ---- the shell: the chip, the dock beside the canvas with the real page, told what it was opened about ----
  await openShell(page);
  await usePersonalScreen(page);
  await page.waitForSelector('bx-shell button.chip.agent');
  check(/workspace agent/.test(await page.textContent('bx-shell button.chip.agent')) && (await page.getAttribute('bx-shell button.chip.agent', 'aria-label')) === 'Workspace agent', '[real] the shell\'s chip, from xbind\'s state');
  check(!(await page.locator('bx-shell .alerts').allInnerTexts()).join(' ').includes('/xbin/agent') && !!(await agentAlert(call)), '[real] xbind sends the admin the notice that names /xbin/agent (for a shell without the chip); this shell leaves it out');
  await openTile(page, R.NOTE);
  await page.click(`.card[data-path="${R.NOTE}"]`, { position: { x: 80, y: 12 } }); // its title bar: the window in front
  await settle(page);
  await page.click('bx-shell button.chip.agent');
  const sf = await R.agentFrame(page, 'bx-shell bx-agent-dock[open]');
  const about = await sf.waitForFunction(() => globalThis.wsAgentPage.testApi().context, null, { timeout: 5000 }).then((h) => h.jsonValue(), () => null);
  check(!!about && about.tile === R.NOTE && /About/.test(await sf.textContent('.wsp-ctx')), `[real] opened with a tile in front, the dock tells the page (xbin:agent-context): the message box offers "About …" (${JSON.stringify(about)})`);
  await shot(page, 'agent-chrome-real-shell', { fullPage: false });
  await page.click('bx-shell bx-agent-dock [data-close]');
  await sh(page, (t, p) => t.closeTile(p), R.NOTE);
  await closeCtx(ctx, page);

  // ---- a person who is no admin: nothing until they are given one; then the chip, their own agent, the notice ----
  await D.page.goto(`${URL}/`);
  await D.page.waitForSelector('bx-shell button.chip.settings');
  const everyone = !!(await state(dcall)).access;
  if (!everyone) {
    await D.page.waitForTimeout(500); // (a negative: the chip must not appear)
    check(!(await D.page.$('bx-shell button.chip.agent')) && !(await agentAlert(dcall)), '[real] workspace agents on, and this person was given none: no chip, no notice');
    await D.page.goto(`${URL}/xbin/agent`);
    await D.page.waitForSelector(`${DOCK} header`);
    await D.page.waitForTimeout(500); // (a negative)
    check(!(await D.page.$(`${DOCK} iframe`)) && !(await D.page.$(`${DOCK} [data-needs]`)) && !(await D.page.$(`${DOCK} bx-agent-setup .st`)), '[real] …and /xbin/agent shows them nothing of it');
    const A = await login(browser, 'admin', 'admin');
    check((await R.caller(A.ctx)('PUT', '/delegate/workspace', { who: 'everyone' })).status === 200, '[real] the admin gives everyone one (PUT /delegate/workspace {who: "everyone"} — the setup step\'s other button)');
    await A.ctx.close();
    await D.page.goto(`${URL}/`);
  } else skip('[real] a person without an agent (no chip, no notice): everyone was given one in this workspace before — a fresh run covers it');
  await D.page.waitForSelector('bx-shell button.chip.agent');
  const ds = await state(dcall);
  check(ds.access === true && ds.agent === R.AGENT && ds.who === undefined && (await D.page.getAttribute('bx-shell button.chip.agent', 'aria-label')) === 'Workspace agent', '[real] given one, the person\'s shell shows the chip (who has one is the admins\' to read)');
  const al = await agentAlert(dcall);
  if (al) {
    check(al.message.includes('/xbin/agent') && al.dismiss === '/delegate/page/dismiss' && !(await D.page.locator('bx-shell .alerts').allInnerTexts()).join(' ').includes('/xbin/agent'),
      `[real] xbind's notice for them names /xbin/agent — what a shell without the chip shows; this one leaves it out (${al.message.slice(0, 60)}…)`);
    check((await dcall('POST', '/delegate/page/dismiss', {})).status < 300 && !(await agentAlert(dcall)), '[real] …and their dismissal ends it, for them');
  } else skip('[real] the notice that names /xbin/agent, for a person given an agent: this person dismissed it in an earlier run');
  await D.page.click('bx-shell button.chip.agent');
  const df = await R.agentFrame(D.page, 'bx-shell bx-agent-dock[open]');
  const mine = await df.evaluate(() => ({ t: globalThis.wsAgentPage.testApi(), text: document.querySelector('.wsp-main').textContent }));
  // (asked from their page: a person who is no admin reaches the agent's backend from its own page only)
  const theirs = await df.evaluate(async (agent) => { const r = await globalThis.xbin.fetch(`/api/${agent}/ws/state`); return r.ok ? (await r.json()).user : `HTTP ${r.status}`; }, R.AGENT);
  const direct = (await dcall('GET', `/api/${R.AGENT}/ws/state`)).status;
  check(theirs === 'dev1' && direct === 403 && mine.t.sessions.length >= 1 && !mine.text.includes(stamp), `[real] their dock frames their own agent — a partition of their own, nothing of the admin's conversation (${theirs}, ${mine.t.sessions.length} session)`);
  await shot(D.page, 'agent-chrome-real-person', { fullPage: false });
  await D.ctx.close();
  log(`agentChrome: the real holder (${R.AGENT}) was driven end to end; the faked matrix covers what it cannot hold on demand`);
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

async function handOff(page, ctx, check, wasOpen, skip) {
  const sent = [];
  page.on('request', (r) => { if (r.method() === 'POST' && /\/api\/xbin\/term\/sessions($|\/[^/]+\/queue)/.test(new globalThis.URL(r.url()).pathname)) sent.push(new globalThis.URL(r.url()).pathname); });
  // "Hand this to <tile>'s own agent": the tile's window on a new session's launcher, the words beside it as text, nothing started
  const tabs0 = (await fr(page, HAND_TILE, (f) => f.tabs)).length;
  await dockFrame(page, true);
  await waitFor(page, (t, p) => { const f = t.frameFor(p)?.testApi(); return !!f && f.terminalOpen && !!f.handoff; }, HAND_TILE, { timeout: 10000, label: 'the window opens on a new session\'s launcher' });
  const hand = await fr(page, HAND_TILE, (f) => f.handoff);
  const shown = await page.$eval(`.card[data-path="${HAND_TILE}"] bx-frame [data-handoff] [data-draft]`, (e) => ({ text: e.textContent, markup: e.children.length }));
  check(hand.draft === DRAFT_SEEN && shown.text === DRAFT_SEEN && shown.markup === 0, `xbin:open-session {session: "new", draft}: the tile's window opens on a new session's launcher, the words as plain text (${JSON.stringify(shown)})`);
  // the draft is the agent's page's — untrusted: labelled as what it suggests, to read and edit, never as the person's words
  const said = await page.$eval(`.card[data-path="${HAND_TILE}"] bx-frame [data-handoff]`, (e) => ({ from: e.querySelector('[data-draft-from]')?.textContent || '', all: e.textContent }));
  check(/^Suggested by your agent’s page — not your words\. Read it and change it as you like\./.test(said.from.trim()) && /nothing is sent until you press Send/.test(said.from)
    && !/your (own )?words(?!\.)/i.test(said.all.replace('not your words', '')), `…labelled as text the agent's page suggests, for the person to read and edit, unsent — never as their own words (${said.from.trim()})`);
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
  // (under --isolate the scripted agent — a host path, XBIN_AGENT_FAKE — can't start inside a tile's sandbox: run.sh says so)
  const sandboxed = await ctx.request.get(`${URL}/api/xbin/runtime`).then((r) => r.json()).then((j) => !!(j.host && j.host.isolate)).catch(() => false);
  if (!fake) check(false, 'the launcher offers the scripted "fake" coding agent (XBIN_AGENT_FAKE)');
  else if (sandboxed) (skip || log)('picking a coding agent on the launcher (its session, the words in its composer): this xbind runs tiles in sandboxes, where the scripted agent can\'t start — a run without HARNESS_ISOLATE covers it');
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
  const linkSaid = { from: await text(page, `${DOCK} [data-handoff] [data-draft-from]`), copy: await text(page, `${DOCK} [data-handoff] [data-copy]`), all: await text(page, `${DOCK} [data-handoff]`) };
  check(linkSaid.from === 'Suggested by your agent’s page — not your words' && linkSaid.copy === 'Copy the suggested text' && !/paste your words|copy your words/i.test(linkSaid.all),
    `…the words labelled as the agent's page's suggestion there too (${JSON.stringify(linkSaid)})`);
  await shot(page, 'agent-chrome-handoff-link', { fullPage: false });
  await page.goto(`${URL}/`); // (the shell again, for what follows)
  await page.waitForSelector('bx-shell button.chip.agent');
}
const text = (page, sel) => page.textContent(sel).then((t) => (t || '').replace(/\s+/g, ' ').trim());

module.exports = { agentChrome };
