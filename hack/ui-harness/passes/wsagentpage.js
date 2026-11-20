// hack/ui-harness/passes/wsagentpage.js — the workspace agent's own page
// (builtin-templates/agent/ws/page.js; docs/workspace-agent.md §The agent's
// page) inside xbin's dock, against the REAL sealed component xbin/wsagent:
// one backend per person, the scripted model (hack/fakeopenai's "ws:"
// script, the message itself carries the tool calls), nothing intercepted.
//
// It needs sandboxes — the agent is a partition per person — so it SKIPs
// without HARNESS_ISOLATE=1, said so:
//   HARNESS_ISOLATE=1 hack/ui-harness/run.sh --keep wsAgentPage
// The first open compiles the agent's backend from the checkout (a --dev
// xbind): the pass waits for the page, however long that is.
//
// It asserts, as the admin at /xbin/agent: the session switcher (Main
// first, never archived; New session…); a new session's empty state and
// starters (a starter fills the message box, sends nothing); the scope chip
// and its panel (xbind's connection line, the totals, the tree with the
// tiles' names from xbind itself, the model; a tile left out and applied);
// a scripted turn — the plan, tool cards, a refusal of a tile outside the
// scope, a request card in place of the call that filed it, then its
// outcome once the person allowed it in xbin's sheet; the notebook (the
// agent's page, the person's own: add, edit, delete); the page's paused
// banner (where the page stays in view: in the dock xbin's paused state
// replaces it); and archiving. Screenshots: out/wsagent-page-*.png, dark and
// light, at 1280 and at 430 px.
//
// The helpers at the end are shared with the agentChrome pass's real-holder
// section.
const path = require('path');
const { URL, fs, login, shot, checker, log, sleep } = require('../lib');

const AGENT = 'xbin/wsagent';
const NOTE = 'apps/wsnote'; // a note with a backend, in no org: what the scripted agent reads and changes
const WS = process.env.WS || '';
const FAKE = process.env.FAKEOPENAI_ADDR || '';
const MODEL = 'fake/fake-chat';
const DOCK = 'bx-agent-dock';
const SHEET = `${DOCK} bx-agent-approve`;
const TA = 'bx-chat-composer textarea';

// the note tile's backend: one note, and who wrote it (the person, and their agent when the call is its)
const NOTE_GO = `// The UI harness's note tile (hack/ui-harness/passes/wsagentpage.js): one note, and who wrote it.
package main

import (
	"fmt"
	"io"
	"net/http"
	"sync"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func main() {
	var mu sync.Mutex
	note, by, writes := "first", "nobody", 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /note", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "note=%s writes=%d last write by %s", note, writes, by)
	})
	mux.HandleFunc("PUT /note", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c := xbin.Caller(r)
		mu.Lock()
		defer mu.Unlock()
		note, by = string(b), fmt.Sprintf("user=%s agent=%s", c.User, c.Agent)
		writes++
		fmt.Fprintf(w, "written by %s", by)
	})
	xbin.Serve(mux)
}
`;

// ---- shared with agentChrome's real-holder section ------------------------------------------

async function until(fn, label, timeout = 15000, every = 200) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(every);
  }
}

/** caller(ctx): the person's own session over HTTP — call(method, path, body) → {status, data, text}; path under /api/xbin unless it starts /api/. */
function caller(ctx) {
  return async (method, p, data) => {
    const r = await ctx.request.fetch(`${URL}${p.startsWith('/api/') ? p : `/api/xbin${p}`}`, { method, ...(data !== undefined ? { data } : {}), failOnStatusCode: false, timeout: 300000 });
    const text = await r.text();
    let json = null;
    try { json = JSON.parse(text); } catch { /* not JSON */ }
    return { status: r.status(), data: json, text };
  };
}

/** why the real holder can't be driven here ('' when it can). */
function noHolder() {
  if (!process.env.HARNESS_ISOLATE) return 'no sandboxes (HARNESS_ISOLATE=1): a person\'s workspace agent is a partition of xbin/wsagent';
  if (!FAKE) return 'no FAKEOPENAI_ADDR: the scripted model the agent runs on';
  if (!WS) return 'no WS: the note tile is written into the workspace';
  return '';
}

/** connectModel(call): the LLM connector's one upstream is hack/fakeopenai, the default model its fake-chat (an admin's call). */
async function connectModel(call) {
  const llm = (await call('GET', '/llm')).data || {};
  if (llm.configured && llm.defaults && llm.defaults.model === MODEL) return;
  const a = await call('PUT', '/llm/upstreams/fake', { baseURL: `http://${FAKE}/v1`, key: 'sk-fake-UIHARNESS-0000' });
  const b = await call('PUT', '/llm/config', { defaults: { model: MODEL } });
  if (a.status !== 200 || b.status !== 200) throw new Error(`connecting the scripted model: ${a.status} ${a.text.slice(0, 200)} / ${b.status} ${b.text.slice(0, 200)}`);
}

/** agentOn(call): workspace agents on, the admin has one, not paused — by the API (the agentChrome pass does it by the chrome's own buttons, on a fresh workspace). → the state. */
/** shipped(call): the component this xbind ships as the agent — the workspace switch's own read says it to an admin, given an agent
 *  or not (the state's `agent` is there only for a person who was given one: administering it isn't having one). '' when none. */
async function shipped(call) {
  const ws = (await call('GET', '/delegate/workspace')).data || {};
  return ws.agent || ((await call('GET', '/delegate/state')).data || {}).agent || '';
}

/** snapshot(call, tile): what an admin may change of a system component (AGENT by default) — its own access entries and, when it
 *  is the agent this xbind ships, the workspace switch, whether everyone was given one, and the admin's pause. For putBack. */
async function snapshot(call, tile = AGENT) {
  const acc = (await call('GET', `/access?tile=${encodeURIComponent(tile)}`)).data || {};
  const snap = { tile, entries: (acc.entries || []).filter((e) => e.source === 'exact').map(({ kind, id, level }) => ({ kind, id, level })) };
  const ws = (await call('GET', '/delegate/workspace')).data || {};
  if (ws.agent !== tile) return snap;
  const st = (await call('GET', '/delegate/state')).data || {};
  return { ...snap, agent: true, enabled: !!ws.enabled, everyone: !!(ws.who && ws.who.everyone), paused: !!st.paused };
}

/** putBack(call, want): make the component's state what snapshot read (or what want says, e.g. nobody given it). → what failed. */
async function putBack(call, want) {
  const bad = [];
  const put = async (p, body) => { const r = await call('PUT', p, body); if (r.status >= 300) bad.push(`PUT ${p} ${JSON.stringify(body)}: ${r.status} ${r.text.slice(0, 160)}`); };
  const now = await snapshot(call, want.tile);
  if (want.agent && now.paused !== want.paused) await put('/delegate/state', { paused: want.paused });
  const key = (e) => `${e.kind}:${e.id}`;
  const wanted = new Map(want.entries.map((e) => [key(e), e]));
  for (const e of now.entries) if (!wanted.has(key(e))) await put('/access', { tile: want.tile, kind: e.kind, id: e.id, level: '' });
  for (const e of want.entries) if ((now.entries.find((n) => key(n) === key(e)) || {}).level !== e.level) await put('/access', { tile: want.tile, kind: e.kind, id: e.id, level: e.level });
  if (want.agent && now.everyone !== want.everyone) await put('/delegate/workspace', { who: want.everyone ? 'everyone' : 'chosen' });
  if (want.agent && now.enabled !== want.enabled) await put('/delegate/workspace', { enabled: want.enabled });
  const after = await snapshot(call, want.tile);
  const same = (a) => JSON.stringify({ ...a, entries: [...a.entries].sort((x, y) => key(x).localeCompare(key(y))) });
  if (same(after) !== same(want)) bad.push(`now ${same(after)}, want ${same(want)}`);
  return bad;
}

/** putBackChecked(browser, found, check): putBack in an admin session of its own (the pass's may be closed), checked. */
async function putBackChecked(browser, found, check) {
  const K = await login(browser, 'admin', 'admin');
  try {
    const bad = await putBack(caller(K.ctx), found);
    check(bad.length === 0, `${found.tile}: what the pass changed is put back as it found it (${bad.join(' | ') || JSON.stringify(found)})`);
  } finally { await K.ctx.close(); }
}

async function agentOn(call) {
  let st = (await call('GET', '/delegate/state')).data || {};
  if (!st.workspace) await call('PUT', '/delegate/workspace', { enabled: true });
  st = (await call('GET', '/delegate/state')).data || {};
  if (st.access === false || (st.who && !st.who.chosen)) await call('PUT', '/delegate/workspace', { who: 'me' });
  if (st.paused) await call('PUT', '/delegate/state', { paused: false });
  return (await call('GET', '/delegate/state')).data || {};
}

/** noteTile(call): apps/wsnote is there and answers (made once: a Go tile, compiled in its sandbox). → its GET /note. */
async function noteTile(call) {
  const read = async () => { const r = await call('GET', `/api/${NOTE}/note`); return r.status === 200 && /^note=/.test(r.text) ? r.text : ''; };
  let now = await read();
  if (now) return now;
  const dir = path.join(WS, NOTE);
  if (!fs.existsSync(dir)) {
    const r = await call('POST', '/create', { path: NOTE, runtime: 'go', title: 'Agent note' });
    if (r.status !== 200) throw new Error(`creating ${NOTE}: ${r.status} ${r.text.slice(0, 200)}`);
  }
  fs.mkdirSync(path.join(dir, 'backend'), { recursive: true });
  fs.writeFileSync(path.join(dir, 'backend', 'main.go'), NOTE_GO);
  now = await until(read, `${NOTE} answers GET /note (its backend compiles in its sandbox)`, 240000, 1000);
  return now;
}

/** settle what earlier runs left waiting, so counts start at none: the person denies and rejects (their own session). */
async function nothingWaits(call) {
  for (const r of ((await call('GET', '/delegate/requests')).data || {}).requests || []) if (r.state === 'pending') await call('POST', `/delegate/requests/${r.id}/deny`, {});
  for (const c of ((await call('GET', '/delegate/changesets')).data || {}).changesets || []) if (c.state === 'proposed') await call('POST', `/delegate/changesets/${c.id}/reject`, {});
  await until(async () => { const s = (await call('GET', '/delegate/state')).data || {}; return !s.pendingRequests && !s.openChangeSets; }, 'nothing waits for the person');
}

/**
 * agentFrame(page, dock): the Frame of the agent's own page in the dock's
 * iframe, booted (a session in view). The first one compiles the backend:
 * the wait is long, and polled.
 */
async function agentFrame(page, dock = DOCK, timeout = 600000) {
  const el = await page.waitForSelector(`${dock} iframe`, { state: 'attached', timeout: 60000 });
  const f = await el.contentFrame();
  await f.waitForFunction(() => !!(globalThis.wsAgentPage && globalThis.wsAgentPage.testApi().sel != null), null, { timeout, polling: 250 });
  await f.waitForSelector('.wsp-main[aria-busy="false"]', { timeout: 60000 });
  return f;
}

const api = (f) => f.evaluate(() => globalThis.wsAgentPage.testApi());
const text = (f, sel) => f.textContent(sel).then((t) => (t || '').replace(/\s+/g, ' ').trim());
const doneKeys = (f) => f.$$eval('.wsp-main .bxc-row[data-kind="assistant"]', (els) => els.filter((e) => /^\s*Done: /.test(e.textContent)).map((e) => e.dataset.k));

/** say(f, script): type a message into the page's own box and send it; → the scripted model's "Done: …" answer once the turn ended. */
/** PENDING: how the scripted model answers a tool result that says a request waits for the person (hack/fakeopenai wsPendingWords). */
const PENDING = /That needs your approval\. Nothing has run yet\./;

async function say(f, message, timeout = 90000) {
  const before = await doneKeys(f);
  await f.fill(TA, message);
  await f.press(TA, 'Enter');
  const h = await f.waitForFunction((seen) => { const e = [...document.querySelectorAll('.wsp-main .bxc-row[data-kind="assistant"]')].find((x) => /^\s*Done: /.test(x.textContent) && !seen.includes(x.dataset.k));
    return e ? e.textContent.replace(/\s+/g, ' ').trim() : null; }, before, { timeout, polling: 100 });
  return h.jsonValue();
}

/** armed(page, sel…): wait until a chrome button takes a click (aria-disabled false) — through the shadow roots named. */
const armed = (page, parts) => page.waitForFunction((s) => { const q = (root, ps) => ps.reduce((r, p) => r && (r.shadowRoot || r).querySelector(p), root);
  const el = q(document, s); return el && el.getAttribute('aria-disabled') === 'false'; }, parts, { timeout: 8000 });

/** pickMain(f): the session in view is Main (the switcher's first row). */
async function pickMain(f) {
  await f.click('.wsp-sess');
  await f.waitForSelector('.wsp-sessions .wsp-srow');
  await f.click('.wsp-sessions .wsp-slist .wsp-srow:first-child .wsp-sbtn');
  await f.waitForFunction(() => { const t = globalThis.wsAgentPage.testApi(); return t.pop === '' && t.sel === t.sessions[0]; });
  await f.waitForSelector('.wsp-main[aria-busy="false"]');
}

// ---- the pass -----------------------------------------------------------------------------------

async function wsAgentPage(browser) {
  const { check, skip, done } = checker('wsagent-page');
  const why = noHolder();
  if (why) { skip(`the workspace agent's page against the real holder: ${why}`); done(); return; }
  const stamp = Date.now().toString(36);
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1280, height: 900 } });
  const call = caller(ctx);
  const ships = await shipped(call);
  if (ships !== AGENT) { skip(`the workspace agent's page: this xbind ships no ${AGENT} (GET /delegate/workspace says agent ${JSON.stringify(ships)})`); await ctx.close(); done(); return; }
  const found = await snapshot(call); // put back at the end, however the pass ends: no pass depends on another's leftovers
  try {
    await drive(browser, { check, ctx, page, call, stamp });
  } finally {
    await putBackChecked(browser, found, check);
  }
  done();
}

async function drive(browser, { check, ctx, page, call, stamp }) {
  await connectModel(call);
  await agentOn(call);
  await nothingWaits(call);
  const note0 = await noteTile(call);
  log(`wsAgentPage: ${NOTE} says ${note0}`);

  await page.goto(`${URL}/xbin/agent`);
  await page.waitForSelector(`${DOCK} header`);
  const t0 = Date.now();
  let f = await agentFrame(page);
  log(`wsAgentPage: the agent's page was up ${Math.round((Date.now() - t0) / 1000)} s after the dock`);
  const wsCall = (m, p, d) => call(m, `/api/${AGENT}${p}`, d);

  // ---- the session switcher: Main first, never archived; a new session ----
  await pickMain(f);
  const sessions0 = ((await wsCall('GET', '/ws/sessions')).data || {}).sessions || [];
  check(sessions0.length > 0 && sessions0[0].main === true && (await text(f, '.wsp-sess .nm')) === sessions0[0].name, `the session in view is Main, the backend's first (${await text(f, '.wsp-sess .nm')})`);
  await f.click('.wsp-sess');
  await f.waitForSelector('.wsp-sessions .wsp-srow');
  const rows = await f.$$eval('.wsp-sessions .wsp-slist:not(.wsp-older .wsp-slist) .wsp-srow', (els) => els.map((e) => [e.dataset.session, e.getAttribute('aria-selected'), e.textContent.replace(/\s+/g, ' ').trim()]));
  check(rows.length >= 1 && rows[0][0] === String(sessions0[0].id) && rows[0][1] === 'true' && /always here/.test(rows[0][2]) && /fake-chat/.test(rows[0][2]),
    `the switcher lists Main first, with its scope and model, "always here" (${rows[0] && rows[0][2]})`);
  const archiveMain = await f.$eval('.wsp-sessions .wsp-sacts button:nth-of-type(3)', (b) => [b.textContent.trim(), b.disabled, b.title]);
  check(archiveMain[0] === 'Archive' && archiveMain[1] === true && /renamed, not archived/.test(archiveMain[2]), `Main can't be archived (${JSON.stringify(archiveMain)})`);
  await shot(page, 'wsagent-page-sessions', { fullPage: false });
  await f.click('.wsp-sessions .wsp-sacts button:first-child'); // New session…
  await f.waitForFunction((n) => { const t = globalThis.wsAgentPage.testApi(); return t.sessions.length === n + 1 && t.sel === t.sessions[t.sessions.length - 1] && t.pop === ''; }, sessions0.length);
  await f.waitForSelector('.wsp-empty h2');
  const sid = (await api(f)).sel;
  check((await text(f, '.wsp-sess .nm')) === 'New session' && sid !== sessions0[0].id, `New session… starts one and shows it (${await text(f, '.wsp-sess .nm')})`);

  // ---- the empty state and its starters ----
  const empty = (await wsCall('GET', `/ws/empty?session=${sid}`)).data || {};
  const es = await f.evaluate(() => ({ h: document.querySelector('.wsp-empty h2').textContent, can: document.querySelectorAll('.wsp-empty .can li').length, cant: document.querySelectorAll('.wsp-empty .cant li').length,
    starters: [...document.querySelectorAll('.wsp-starter')].map((b) => [b.dataset.kind, b.textContent.replace(/\s+/g, ' ').trim()]), hint: (document.querySelector('.wsp-hint') || {}).textContent || '' }));
  check(es.h === empty.heading && es.can === (empty.can || []).length && es.can >= 3 && es.cant === (empty.cant || []).length && es.cant >= 2, `the empty state: the backend's heading, what it can and can't (${es.h}; ${es.can} / ${es.cant})`);
  check(es.starters.length === (empty.starters || []).length && es.starters.length >= 1 && es.starters.every((s, i) => s[0] === empty.starters[i].kind && s[1].includes(empty.starters[i].text.slice(0, 20))) && es.hint === empty.hint,
    `…and starters from the person's own tiles (${es.starters.map((s) => s[0]).join(', ')}), the hint under the box`);
  check((await f.getAttribute(TA, 'placeholder')) === empty.placeholder, `the message box asks for an outcome (${await f.getAttribute(TA, 'placeholder')})`);
  await shot(page, 'wsagent-page-empty', { fullPage: false });
  await f.click('.wsp-starter');
  await f.waitForFunction((sel) => document.querySelector(sel).value !== '', TA);
  await sleep(300); // (a negative: a starter must not send)
  const filled = await f.$eval(TA, (t) => t.value);
  const view0 = (await wsCall('GET', `/runs/${sid}/view`)).data || {};
  check(filled === empty.starters[0].text && (await f.$$('.wsp-main .bxc-user')).length === 0 && !JSON.stringify(view0.messages || []).includes(filled.slice(0, 24)), `a starter fills the message box and sends nothing (${filled.slice(0, 60)})`);
  await f.fill(TA, '');

  // ---- the scope chip and its panel ----
  const conn = (await wsCall('GET', `/ws/sessions/${sid}/connection`)).data || {};
  const counts = (conn.connection || {}).counts || {};
  const chip = [await f.getAttribute('.wsp-chip', 'aria-label'), await text(f, '.wsp-chip')];
  check(chip[0].startsWith(`Scope: ${counts.tiles} tiles in ${counts.orgs} orgs connected`) && chip[0].endsWith('Model fake-chat.') && chip[1].includes(`${counts.tiles} tiles · ${counts.orgs} orgs`) && chip[1].includes('fake-chat'),
    `the scope chip: xbind's connection line and the model (${chip[0]})`);
  await f.click('.wsp-chip');
  await f.waitForSelector('.wsp-scope .wsp-tree .wsp-org');
  const totals = await f.$$eval('.wsp-scope .wsp-total', (els) => els.map((e) => e.textContent.replace(/\s+/g, ' ').trim()));
  check(totals.join(' | ') === `${counts.tiles}connected | 0hidden by you | ${counts.blocked || 0}blocked | ${counts.noAccess || 0}no access`, `the panel's four totals (${totals.join(' | ')})`);
  check(/^Model fake-chat not private/.test(await text(f, '.wsp-scope .wsp-modelrow')), `…the model, and that it is not private (${await text(f, '.wsp-scope .wsp-modelrow')})`);
  const orgs = await f.$$eval('.wsp-scope .wsp-org', (els) => els.map((e) => e.dataset.org));
  check(JSON.stringify(orgs.slice().sort()) === JSON.stringify(((conn.connection || {}).orgs || []).map((o) => o.id).sort()) && orgs.includes('workspace'), `…the orgs, and the tiles in none (${orgs.join(', ')})`);
  check(await f.$eval('.wsp-scope .wsp-pop-f button.primary', (b) => b.disabled) && /not a permission/.test(await text(f, '.wsp-scope .wsp-pop-f p')), 'Apply waits for a change; "Scope is where this session looks. It is not a permission"');
  // the tiles' names are xbind's own answer to this page's frame: unfold "not in an org", leave one tile out
  await f.click('.wsp-org[data-org="workspace"] .wsp-ti.org b');
  await f.waitForSelector(`.wsp-org[data-org="workspace"] .wsp-ti.tile[data-tile="${NOTE}"]`);
  const HIDE = 'apps/welcome';
  const tiles = await f.$$eval('.wsp-org[data-org="workspace"] .wsp-ti.tile', (els) => els.map((e) => [e.dataset.tile, e.getAttribute('aria-checked'), e.getAttribute('aria-label')]));
  check(tiles.some((t) => t[0] === NOTE && t[1] === 'true' && /connected/.test(t[2])) && tiles.some((t) => t[0] === HIDE), `the tree names the tiles (from xbind's full view): ${NOTE} connected (${tiles.length} tiles in no org)`);
  await shot(page, 'wsagent-page-scope', { fullPage: false });
  await f.click(`.wsp-ti.tile[data-tile="${HIDE}"]`);
  await f.waitForFunction(() => !document.querySelector('.wsp-scope .wsp-pop-f button.primary').disabled);
  await f.click('.wsp-scope .wsp-pop-f button.primary');
  await f.waitForFunction(() => globalThis.wsAgentPage.testApi().pop === '');
  await f.waitForFunction((n) => (document.querySelector('.wsp-chip').getAttribute('aria-label') || '').includes(`${n} tiles`), counts.tiles - 1);
  const s1 = (((await wsCall('GET', '/ws/sessions')).data || {}).sessions || []).find((s) => s.id === sid) || {};
  check(s1.connection && s1.connection.counts.tiles === counts.tiles - 1 && s1.connection.counts.hidden === 1 && /1 not connected/.test(await f.getAttribute('.wsp-chip', 'aria-label')),
    `a tile left out and applied: the session's scope at xbind, and the chip (${await f.getAttribute('.wsp-chip', 'aria-label')})`);
  // the model: the list (the session's own), nothing to switch to on this connector
  await f.click('.wsp-chip');
  await f.waitForSelector('.wsp-scope .wsp-modelrow button');
  await f.click('.wsp-scope .wsp-modelrow button');
  await f.waitForSelector('.wsp-models .wsp-model');
  const models = await f.$$eval('.wsp-models .wsp-model', (els) => els.map((e) => [e.dataset.model, e.textContent.replace(/\s+/g, ' ').trim()]));
  check(models.some((m) => m[0] === MODEL && /current/.test(m[1])) && await f.$eval('.wsp-models .wsp-pop-f button.primary', (b) => b.disabled), `Change… lists the models, the session's marked current (${JSON.stringify(models)})`);
  await f.click('.wsp-models .wsp-pop-f button:first-child'); // Cancel: back to the tree
  await f.waitForSelector('.wsp-scope .wsp-tree');
  await f.click('.wsp-scope .wsp-pop-h button'); // close
  await f.waitForFunction(() => globalThis.wsAgentPage.testApi().pop === '');

  // ---- a scripted turn: the plan, tool cards, a refusal, a request in place of its call ----
  const body = `by the page ${stamp}`;
  const turn = await say(f, 'Read the note, then set it.\nws: plan {"steps":[{"text":"Read the note","kind":"reads"},{"text":"Set the note","kind":"asks"}]} ;; workspace_map'
    + ` ;; tile_call {"tile":"${NOTE}","method":"GET","path":"/note"} ;; tile_call {"tile":"${HIDE}","method":"GET","path":"/"}`
    + ` ;; tile_call {"tile":"${NOTE}","method":"PUT","path":"/note","body":"\\"${body}\\"","why":"set the note for the page"}`);
  await f.waitForSelector('.wsc-req[data-state="pending"]');
  const cards = (await wsCall('GET', `/ws/sessions/${sid}/cards`)).data || {};
  const reqs = ((await call('GET', '/delegate/requests')).data || {}).requests || [];
  log(`wsAgentPage: the turn's answer: ${turn.slice(0, 300)}`);
  log(`wsAgentPage: rows: ${JSON.stringify(await f.$$eval('.wsp-main .bxc-row', (els) => els.map((e) => `${e.dataset.kind}:${(e.firstElementChild && (e.firstElementChild.className || e.firstElementChild.localName)) || ''}:${(e.querySelector('.bxc-title') || {}).textContent || ''}`)))}`);
  check(/^Done: /.test(turn) && /GET \/note → 200/.test(turn) && PENDING.test(turn), `the scripted model ran its tools and answered (${turn.slice(0, 140)})`);
  const tools = await f.$$eval('.wsp-main details.bxc-tool', (els) => els.map((e) => `${e.querySelector('.bxc-title').textContent.trim()}:${e.dataset.state}`));
  check(tools.join(' ') === 'workspace_map:done tile_call:done tile_call:error', `tool cards for what it looked at, and for the call xbin refused (${tools.join(', ')})`);
  const planText = await text(f, '.wsp-main .bxc-plan');
  check((cards.plan || []).length === 2 && /Read the note/.test(planText) && /Set the note/.test(planText) && (await f.$$('.wsp-main .bxc-plan')).length === 1 && !tools.some((t) => /^plan:/.test(t)),
    `the plan in place of its call, from the session's cards (${planText.slice(0, 100)})`);
  const refusal = await f.$$eval('.wsc-refusal', (els) => els.map((e) => [e.dataset.refusal, e.textContent.replace(/\s+/g, ' ').trim()]));
  check((cards.refusals || []).some((r) => r.kind === 'hidden' && r.tile === HIDE) && refusal.some((r) => r[0] === 'hidden' && /Not connected in this session/.test(r[1]) && /Open scope/.test(r[1])),
    `a call to the tile left out is xbin's refusal, drawn with the way forward (${JSON.stringify(refusal).slice(0, 160)})`);
  const pend = await f.$$eval('.wsc-req', (els) => els.map((e) => ({ ...e.dataset, row: e.closest('.bxc-row') ? e.closest('.bxc-row').dataset.kind : '', text: e.textContent.replace(/\s+/g, ' ').trim() })));
  check(reqs.length === 1 && pend.length === 1 && pend[0].state === 'pending' && pend[0].ref === reqs[0].id && pend[0].kind === 'call' && pend[0].row === 'tool'
    && /Waiting for your approval/.test(pend[0].text) && pend[0].text.includes('PUT /note') && pend[0].text.includes(NOTE) && /expires in 2[34] h/.test(pend[0].text) && !/Review…|Allow/.test(pend[0].text)
    && (await f.$$('.wsc-req[data-state="pending"] button')).length === 1 && !!(await f.$('.wsc-req[data-state="pending"] [data-marker]')),
  `a request card in place of the call that filed it: xbind's id, what, when it expires, and one control — the marker that opens it (D250) (${pend[0] && pend[0].text.slice(0, 120)})`);
  check(/A request waits for your approval/.test((await api(f)).said), `the page says so once, politely (${(await api(f)).said})`);
  check(!(await text(f, '.wsc-req')).includes(body), 'the card shows the call, not what it sends: that is xbin\'s to show');
  await shot(page, 'wsagent-page-turn', { fullPage: false });

  // ---- allowed in xbin's sheet: the card becomes what came of it ----
  await f.click('.wsc-req[data-state="pending"] [data-marker]');
  await page.waitForSelector(`${SHEET} [role="dialog"]`);
  const sheet = await page.textContent(`${SHEET} [role="dialog"]`).then((t) => t.replace(/\s+/g, ' '));
  check(sheet.includes(`PUT /api/${NOTE}/note`) && sheet.includes(body) && /set the note for the page/.test(sheet), `Review… opens xbin's sheet by the request's id: the call, what it sends, the agent's words (${sheet.slice(0, 160)})`);
  await armed(page, ['bx-agent-dock', 'bx-agent-approve', 'button[data-allow]']);
  await page.click(`${SHEET} button[data-allow]`);
  await f.waitForSelector('.wsc-req[data-state="used"]', { timeout: 30000 });
  const used = await text(f, '.wsc-req[data-state="used"]');
  const after = ((await wsCall('GET', `/ws/sessions/${sid}/cards`)).data || {}).requests || [];
  log(`wsAgentPage: the request after the decision: ${JSON.stringify(after[0])}`);
  check(/^Allowed once by you/.test(used) && used.includes(`PUT /api/${NOTE}/note`) && !/Review…/.test(used), `allowed: the card says who allowed it and what ran (${used.slice(0, 160)})`);
  check(after.length === 1 && after[0].state === 'used' && after[0].by === 'person' && after[0].grant === 'once' && after[0].resultStatus === 200 && !!after[0].result && used.includes('200'),
    `…with the tile's answer, as the backend's fields say (${after[0] && after[0].result}; card: ${used.slice(-80)})`);
  const note1 = (await call('GET', `/api/${NOTE}/note`)).text;
  check(note1.includes(`note="${body}"`) && note1.includes(`user=admin agent=${AGENT}`), `the tile's note changed, written by the person's agent (${note1})`);
  await f.waitForFunction(() => /Updates from xbin and your tile agents/.test(document.querySelector('.wsp-main').textContent), null, { timeout: 30000 });
  check(true, 'the outcome reaches the conversation as an update from xbin, not as the person\'s message');
  await shot(page, 'wsagent-page-outcome', { fullPage: false });

  // ---- the notebook: the agent's page, the person's own ----
  await say(f, `ws: notebook_write {"slug":"note-tile-${stamp}","title":"The note tile","body":"## What it is\\n\\nIt holds **one** note.","summary":"what ${NOTE} is for"}`);
  await f.click('.wsp-menu-b');
  await f.click('.wsp-menu [role="menuitem"]:first-child');
  await f.waitForSelector(`.wsp-nb .wsp-pg[data-slug="note-tile-${stamp}"]`);
  const pages0 = ((await wsCall('GET', `/ws/notebook?session=${sid}`)).data || {}).pages || [];
  const pg = await text(f, `.wsp-pg[data-slug="note-tile-${stamp}"]`);
  const mine0 = pages0.find((p) => p.slug === `note-tile-${stamp}`) || {};
  check((await api(f)).view === 'notebook' && (await f.$$('.wsp-pg')).length === pages0.length && /The note tile/.test(pg) && pg.includes(`what ${NOTE} is for`) && /by the workspace agent/.test(pg)
    && (mine0.orgs || []).length > 0 && pg.endsWith(mine0.orgs.join(', ')), `the notebook lists what the agent wrote, marked with the orgs whose data its session held (${pg.slice(0, 140)}; ${pages0.length} pages)`);
  await f.click(`.wsp-pg[data-slug="note-tile-${stamp}"] button`);
  await f.waitForSelector('.wsp-pbody h2');
  check(/^What it is\s*It holds one note\.$/.test(await text(f, '.wsp-pbody')) && !!(await f.$('.wsp-pbody strong')) && /To clear them, delete the page/.test(await text(f, '.wsp-marks')) && !/Clear org marks/.test(await text(f, '.wsp-nb')), `a page reads as Markdown (${await text(f, '.wsp-pbody')})`);
  await f.click('.wsp-nb-h button[aria-label="back to the notebook"]');
  await f.waitForSelector('.wsp-nb .wsp-pages');
  await f.click('.wsp-nb-h button:has-text("Add a note")');
  await f.waitForSelector('.wsp-edit input');
  await f.fill('.wsp-edit input', `Mine ${stamp}`);
  await f.fill('.wsp-edit textarea', 'Ask before you rename anything.');
  await f.click('.wsp-edit button.primary');
  await f.waitForSelector('.wsp-pbody');
  const mine = `mine-${stamp}`;
  const p1 = (await wsCall('GET', `/ws/notebook/${mine}`)).data || {};
  check((await text(f, '.wsp-pbody')) === 'Ask before you rename anything.' && p1.page && p1.page.version === 1 && p1.page.body === 'Ask before you rename anything.', `Add a note: the person's own page, saved (${mine} v${p1.page && p1.page.version})`);
  await f.click('.wsp-nb-h button:has-text("Edit")');
  await f.fill('.wsp-edit textarea', 'Ask before you rename or delete anything.');
  await f.click('.wsp-edit button.primary');
  await f.waitForFunction(() => /or delete/.test((document.querySelector('.wsp-pbody') || {}).textContent || ''));
  const p2 = (await wsCall('GET', `/ws/notebook/${mine}`)).data || {};
  check(p2.page && p2.page.version === 2, `Edit saves a new version (v${p2.page && p2.page.version})`);
  await shot(page, 'wsagent-page-notebook-page', { fullPage: false });
  await f.click('.wsp-nb-h button.danger');
  await f.waitForSelector('.wsp-confirm[role="alertdialog"]');
  check(/The agent forgets it; its history is kept/.test(await text(f, '.wsp-confirm')), 'Delete asks first, and says what it means');
  await f.click('.wsp-confirm button.danger');
  await f.waitForSelector('.wsp-nb .wsp-pages');
  await f.waitForFunction((slug) => !document.querySelector(`.wsp-pg[data-slug="${slug}"]`), mine);
  const pages1 = ((await wsCall('GET', `/ws/notebook?session=${sid}`)).data || {}).pages || [];
  check(!pages1.some((p) => p.slug === mine) && pages1.some((p) => p.slug === `note-tile-${stamp}`), 'deleted: the page is gone from the notebook, the agent\'s stays');
  await shot(page, 'wsagent-page-notebook', { fullPage: false });
  await f.click('.wsp-nb-h button[aria-label="back to the conversation"]');
  await f.waitForFunction(() => globalThis.wsAgentPage.testApi().view === 'chat');

  // ---- paused: in the dock xbin's own state replaces the page; the page's banner shows where the page stays in view ----
  const alone = await ctx.newPage();
  await alone.goto(`${URL}/c/${AGENT}/`);
  await alone.waitForFunction(() => !!(globalThis.wsAgentPage && globalThis.wsAgentPage.testApi().sel != null), null, { timeout: 60000 });
  check((await alone.$$('.wsp-banner')).length === 0 && !(await alone.$eval(TA, (t) => t.disabled)), 'the page on its own (the person\'s tab): no banner, the box takes a message');
  await page.click(`${DOCK} [data-pause]`);
  await page.waitForSelector(`${DOCK} bx-agent-setup [data-state="paused"]`);
  check(!(await page.$(`${DOCK} iframe`)), 'Pause in the dock: xbin\'s paused state stands in place of the page');
  await alone.waitForSelector('.wsp-banner[data-state="paused"]', { timeout: 15000 });
  const wsState = (await wsCall('GET', '/ws/state')).data || {};
  const banner = await alone.textContent('.wsp-banner[data-state="paused"]').then((t) => t.replace(/\s+/g, ' ').trim());
  check(/^Paused by you at \d\d:\d\d\./.test(banner) && /until you resume/.test(banner) && await alone.$eval(TA, (t) => t.disabled) && (await alone.getAttribute(TA, 'placeholder')) === 'Resume to send a message',
    `the page's own paused banner, without a reload; the box takes nothing (${banner.slice(0, 80)})`);
  check(!wsState.delegation && /paused/.test(wsState.delegationError || ''), `(the backend itself is refused its state while paused — ${JSON.stringify(wsState.delegationError || '').slice(0, 80)} — so the page reads xbind's)`);
  await shot(alone, 'wsagent-page-paused', { fullPage: false });
  await page.click(`${DOCK} bx-agent-setup [data-resume]`);
  await alone.waitForFunction(() => !document.querySelector('.wsp-banner[data-state="paused"]'), null, { timeout: 15000 });
  check(!(await alone.$eval(TA, (t) => t.disabled)), 'Resume (xbin\'s button): the banner goes and the box takes a message again');
  await alone.close();
  f = await agentFrame(page);
  check((await api(f)).sel === sid, 'the dock shows the page again, on the session it last showed');

  // ---- light and dark, 1280 and 430: the same session, its panels ----
  await ctx.close();
  for (const [scheme, width] of [['light', 1280], ['dark', 430], ['light', 430]]) {
    const tag = `${scheme === 'light' ? '-light' : ''}${width < 640 ? '-430' : ''}`;
    const V = await login(browser, 'admin', 'admin', { viewport: { width, height: 900 }, colorScheme: scheme });
    await V.page.goto(`${URL}/xbin/agent`);
    const vf = await agentFrame(V.page);
    await vf.waitForSelector('.wsc-req[data-state="used"]');
    const look = await vf.evaluate(() => ({ scheme: getComputedStyle(document.documentElement).getPropertyValue('--bx-scheme').trim(), wide: document.documentElement.scrollWidth - document.documentElement.clientWidth,
      chip: document.querySelector('.wsp-chip').textContent.replace(/\s+/g, ' ').trim(), name: document.querySelector('.wsp-sess .nm').textContent, w: innerWidth }));
    check(look.scheme === scheme && look.wide <= 1 && (await vf.evaluate(() => globalThis.wsAgentPage.testApi().sel)) === sid, `[${scheme} ${width}] the page follows the person's theme and fits its frame (--bx-scheme ${look.scheme}, ${look.w} px, overflow ${look.wide})`);
    check(/fake-chat/.test(look.chip) && (width >= 640 ? /tiles · \d+ orgs/.test(look.chip) : /\d+/.test(look.chip)), `[${scheme} ${width}] the scope chip stays in view (${look.chip})`);
    const cut = await vf.$$eval('.wsp-chip .seg', (els) => els.filter((e) => e.scrollWidth > e.clientWidth + 1).map((e) => e.textContent.trim()));
    if (cut.length) log(`wsAgentPage: [${scheme} ${width}] the scope chip's words are cut short at this width (${cut.join(' | ')}): it shortens below ws/words.js CHOICES.narrow only`);
    await shot(V.page, `wsagent-page-turn${tag}`, { fullPage: false });
    await vf.click('.wsp-chip');
    await vf.waitForSelector('.wsp-scope .wsp-tree .wsp-org');
    const box = await vf.$eval('.wsp-scope', (e) => { const r = e.getBoundingClientRect(); return [Math.round(r.left), Math.round(r.right), innerWidth]; });
    check(box[0] >= 0 && box[1] <= box[2] + 1, `[${scheme} ${width}] the scope panel fits the page (${box.join(', ')})`);
    await shot(V.page, `wsagent-page-scope${tag}`, { fullPage: false });
    await vf.click('.wsp-scope .wsp-pop-h button'); // close
    await vf.waitForFunction(() => globalThis.wsAgentPage.testApi().pop === '');
    await vf.click('.wsp-sess');
    await vf.waitForSelector('.wsp-sessions .wsp-srow');
    await shot(V.page, `wsagent-page-sessions${tag}`, { fullPage: false });
    if (scheme === 'light' && width < 640) {
      // the last look also tidies up: the pass's session is archived (it folds under Older), Main is shown again
      await vf.click('.wsp-sessions .wsp-sacts button:nth-of-type(3)');
      await vf.waitForFunction(() => { const t = globalThis.wsAgentPage.testApi(); return t.sel === t.sessions[0]; }, null, { timeout: 15000 });
      const left = (((await caller(V.ctx)('GET', `/api/${AGENT}/ws/sessions`)).data || {}).sessions || []).find((s) => s.id === sid) || {};
      await vf.click('.wsp-sess');
      await vf.waitForSelector('.wsp-sessions .wsp-older');
      check(left.archivedAt > 0 && /^Older \(\d+\)$/.test((await vf.textContent('.wsp-sessions .wsp-older summary')).trim()) && (await vf.textContent('.wsp-sess .nm')).trim() === sessions0[0].name,
        `Archive folds a session under Older and shows Main again (${(await vf.textContent('.wsp-sessions .wsp-older summary')).trim()})`);
    }
    await V.ctx.close();
  }
}

module.exports = { snapshot, putBack, putBackChecked, shipped, wsAgentPage, PENDING, AGENT, NOTE, DOCK, SHEET, TA, until, caller, noHolder, connectModel, agentOn, noteTile, nothingWaits, agentFrame, say, armed, pickMain };
