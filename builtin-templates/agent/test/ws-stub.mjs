// ws-stub.mjs — the workspace agent's page (ws/page.js), served for a test,
// against an in-page fake of the backend in workspace mode (API.md
// §Workspace mode) and of the one xbind route the page reads itself (GET
// /api/xbin/agents/connection?session=). Not a script: test/ws-page.mjs and
// test/ws-states.mjs import it.
//
// serveWs(ctx) answers the tile's files (index.html, boot.js, the modules
// beside it, model/, ws/), the kit, theme.css (the real one: the pages are
// looked at in both themes), lit and marked. WS_STUB(seed) installs
// window.xbin: its fetch answers the /ws/ routes from seed — {state,
// sessions, conn: {<id>: backend connection}, full: xbind's full view,
// whatIf: {<model>: {backend, full}}, taint: [models a PATCH refuses],
// cards: {<id>}, startDefaults, empty, pages, views: {<id>: run view}} —
// and records every call in window.__calls; the live stream is driven with
// window.__push(event), window.__bye() (the backend is being replaced) and
// window.__drop(on) (the connection is lost until __drop(false)). Messages
// the page posts to its chrome land in window.__posted; window.__event(e)
// feeds xbin.events (xbind's /ws/events: `delegate`).
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { serveKit, tileHtml, MARKED_STUB } from './kit.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const tileDir = join(here, '..');
const web = join(here, '..', '..', '..', 'web');
export const ORIGIN = 'http://tile.test';

export async function serveWs(ctx) {
  const modules = new Set([...readdirSync(tileDir).filter((f) => f.endsWith('.js')),
    ...['model', 'native', 'ws'].flatMap((d) => readdirSync(join(tileDir, d)).filter((f) => f.endsWith('.js')).map((f) => `${d}/${f}`))]);
  await ctx.route(`${ORIGIN}/**`, (route) => {
    const path = new URL(route.request().url()).pathname.replace(/^\//, '') || 'index.html';
    if (path === 'vendor/theme.css') return route.fulfill({ contentType: 'text/css', body: readFileSync(join(web, 'theme.css'), 'utf8') });
    if (/^vendor\/fonts\/[\w.-]+\.woff2$/.test(path)) return route.fulfill({ contentType: 'font/woff2', body: readFileSync(join(web, 'vendor', 'fonts', path.slice('vendor/fonts/'.length))) });
    if (path !== 'index.html' && !modules.has(path)) return route.fulfill({ status: 404, body: '' });
    let body = readFileSync(join(tileDir, path), 'utf8');
    if (path === 'index.html') body = tileHtml(body);
    route.fulfill({ contentType: path.endsWith('.js') ? 'text/javascript' : 'text/html', body });
  });
  await serveKit(ctx);
  await ctx.route('**/vendor/lit-all.min.js', (r) => r.fulfill({ contentType: 'text/javascript', body: readFileSync(join(web, 'vendor', 'lit-all.min.js'), 'utf8') }));
  await ctx.route('**/vendor/marked.esm.js', (r) => r.fulfill({ contentType: 'text/javascript', body: MARKED_STUB }));
}

// WS_STUB runs in the page (addInitScript).
export function WS_STUB(seed) {
  const json = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
  window.__calls = [];
  window.__posted = [];
  window.__seed = seed;
  // what the page posts to the chrome (at the top level the page is its own parent)
  window.addEventListener('message', (e) => { if (e.data && typeof e.data.type === 'string' && e.data.type !== 'xbin:agent-context') window.__posted.push(e.data); });
  const streams = new Set();
  let seq = 100, dropped = false;
  const enc = (o) => new TextEncoder().encode(`id: g.${o.seq || ++seq}\ndata: ${JSON.stringify(o)}\n\n`);
  window.__push = (ev) => { ev.seq = ev.seq || ++seq; ev.ts = ev.ts || Date.now(); for (const c of streams) { try { c.enqueue(enc(ev)); } catch { streams.delete(c); } } };
  window.__bye = () => { for (const c of streams) { try { c.enqueue(enc({ type: 'bye' })); c.close(); } catch { /* gone */ } } streams.clear(); };
  window.__drop = (on) => { dropped = on; if (on) { for (const c of streams) { try { c.error(new Error('dropped')); } catch { /* gone */ } } streams.clear(); } };
  window.__streams = () => streams.size;
  const evs = new Set();
  window.__event = (e) => { for (const cb of evs) cb(e); };

  const S = seed;
  S.sessions = S.sessions || [];
  S.cards = S.cards || {};
  S.pages = S.pages || [];
  S.views = S.views || {};
  const sess = (id) => S.sessions.find((s) => s.id === id);
  const view = (id) => {
    const v = S.views[id] || {};
    const s = sess(id) || {};
    return { cursor: 'g.' + seq, run: { id, title: s.name || 'Main', status: 'idle', pendingState: {}, ...(v.run || {}) }, messages: [], steps: [], links: [], queued: [], drafts: [],
      chain: [], files: [], memory: {}, config: {}, messageFiles: {}, ...v };
  };
  const routes = [
    ['GET', /\/ws\/state$/, () => json(S.state || { user: 'ana', mode: 'workspace', delegation: { workspace: true, paused: false }, models: { default: 'hosted/big', models: [] } })],
    ['GET', /\/ws\/sessions$/, () => json({ sessions: S.sessions })],
    ['POST', /\/ws\/sessions$/, (m, o) => {
      const b = JSON.parse(o.body || '{}');
      if (b.main) { let s = S.sessions.find((x) => x.main); if (!s) { s = { id: 1, name: 'Main', main: true, model: 'hosted/big', handle: 'h1', status: 'idle', connection: { counts: { tiles: 3, orgs: 1 } } }; S.sessions.unshift(s); } return json({ session: s }); }
      const from = sess(b.from) || {};
      const s = { id: 10 + S.sessions.length, name: b.name || '', main: false, model: b.model || from.model || 'hosted/big', handle: 'h' + (10 + S.sessions.length), status: 'idle', connection: from.connection || { counts: {} } };
      S.sessions.push(s);
      return json({ session: s });
    }],
    ['PATCH', /\/ws\/sessions\/(\d+)$/, (m, o) => {
      const s = sess(+m[1]); const b = JSON.parse(o.body || '{}');
      if (b.archived && s.main) return json({ error: 'Main is always here: it can be renamed, not archived' }, 409);
      if (b.model && (S.taint || []).includes(b.model)) return json({ error: 'refused', code: 'model_taint', model: b.model, orgs: ['Finance'], reason: 'Finance allows private models only', offer: { newSession: { model: b.model } } }, 409);
      if (b.name) s.name = b.name;
      if ('archived' in b) s.archivedAt = b.archived ? Date.now() : 0;
      if (b.model) s.model = b.model;
      if ('scope' in b) s.scope = b.scope;
      return json({ session: s });
    }],
    ['GET', /\/ws\/sessions\/(\d+)\/connection(?:\?whatIf=([^&]+))?$/, (m) => {
      const base = (S.conn || {})[m[1]] || { connection: { counts: (sess(+m[1]) || {}).connection?.counts || {} }, summary: '', model: (sess(+m[1]) || {}).model };
      if (!m[2]) return json(base);
      const w = (S.whatIf || {})[decodeURIComponent(m[2])] || {};
      return json({ ...base, whatIf: w.backend || { model: decodeURIComponent(m[2]), connects: 0, disconnects: 0, counts: {}, allowed: true } });
    }],
    ['GET', /\/ws\/sessions\/(\d+)\/cards$/, (m) => json(S.cards[m[1]] || { requests: [], agents: [], briefs: [], plan: [] })],
    ['PUT', /\/ws\/sessions\/(\d+)\/briefs\/(\d+)$/, (m, o) => {
      const b = (S.cards[m[1]].briefs || []).find((x) => x.id === +m[2]);
      Object.assign(b, JSON.parse(o.body || '{}'), { edited: true, updatedMs: Date.now() });
      return json({ brief: b });
    }],
    ['GET', /\/ws\/sessions\/(\d+)\/briefs\/(\d+)\/start-defaults$/, () => json(S.startDefaults || { harness: 'claude', harnessName: 'Claude Code', asks: true, ticket: true, line: 'Claude Code · asks before acting',
      defaults: { harness: 'claude', harnesses: [{ id: 'claude', name: 'Claude Code', asks: true, needsTicket: true }] } })],
    ['POST', /\/ws\/sessions\/(\d+)\/briefs\/(\d+)\/start$/, (m) => json(S.startAnswer || { request: 'rq-brief-' + m[2], id: 77 })],
    ['GET', /\/ws\/needs$/, () => json({ items: [], count: 0 })],
    ['GET', /\/ws\/empty(?:\?.*)?$/, () => json(S.empty || { heading: 'Ask for an outcome', line: 'x', can: [], cant: [], starters: [] })],
    ['GET', /\/ws\/notebook(?:\?.*)?$/, () => json({ pages: S.pages.filter((p) => !p.gone) })],
    ['GET', /\/ws\/notebook\/([^?]+)(?:\?.*)?$/, (m) => { const p = S.pages.find((x) => x.slug === decodeURIComponent(m[1])); return p ? json({ page: p }) : json({ error: 'no such page' }, 404); }],
    ['PUT', /\/ws\/notebook\/([^?]+)$/, (m, o) => {
      const b = JSON.parse(o.body || '{}'); const slug = decodeURIComponent(m[1]);
      let p = S.pages.find((x) => x.slug === slug);
      if (p && b.base !== p.version) return json({ error: 'moved', current: p.version }, 409);
      if (!p) S.pages.push((p = { slug, version: 0, orgs: [] }));
      Object.assign(p, { title: b.title, body: b.body, version: p.version + 1, updatedBy: 'ana', updatedMs: Date.now() });
      if (b.clearStamps) p.orgs = [];
      return json({ page: p });
    }],
    ['DELETE', /\/ws\/notebook\/([^?]+)$/, (m) => { const p = S.pages.find((x) => x.slug === decodeURIComponent(m[1])); if (p) p.gone = true; return json({ ok: 'true' }); }],
    ['GET', /\/ws\/prefs$/, () => json(S.prefs || { model: '', harness: '', lastSession: 0 })],
    ['PUT', /\/ws\/prefs$/, () => json({})],
    ['GET', /\/runs\/(\d+)\/view(?:\?.*)?$/, (m) => json(view(+m[1]))],
    ['GET', /\/stream\b/, (m, o) => (dropped ? Promise.reject(new TypeError('Failed to fetch')) : new Response(new ReadableStream({
      start(c) {
        streams.add(c);
        c.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ type: 'hello', data: { cursor: 'g.' + seq } })}\n\n`));
        o.signal && o.signal.addEventListener('abort', () => { streams.delete(c); try { c.close(); } catch { /* closed */ } });
      },
    }), { headers: { 'Content-Type': 'text/event-stream' } }))],
    ['POST', /\/runs\/(\d+)\/message$/, () => json({ ok: 'true', inboxId: 1, queued: false })],
    ['POST', /\/runs\/(\d+)\/interrupt$/, () => json({ ok: 'true' })],
    ['GET', /\/api\/xbin\/agents\/connection\?session=([^&]+)(?:&whatIf=([^&]+))?$/, (m) => {
      if (!S.full) return json({ error: 'not here' }, 403);
      if (!m[2]) return json(S.full);
      const w = (S.whatIf || {})[decodeURIComponent(m[2])] || {};
      return json({ ...S.full, whatIf: w.full || {} });
    }],
  ];
  window.xbin = {
    self: 'xbin/wsagent',
    fetch: async (url, opt = {}) => {
      const method = opt.method || 'GET';
      window.__calls.push({ method, url, body: typeof opt.body === 'string' ? opt.body : undefined });
      for (const [meth, re, fn] of routes) {
        const mm = meth === method && url.match(re);
        if (mm) return fn(mm, opt);
      }
      return json({ error: 'not found' }, 404);
    },
    events: { on: (cb) => { evs.add(cb); return () => evs.delete(cb); } },
    bus: { on: () => () => {} },
    iface: () => null,
  };
}

// fixtures the scripts share
export const now = Date.now();
const sec = Math.floor(now / 1000);
export const call = (id, name, args) => ({ id, type: 'function', function: { name, arguments: JSON.stringify(args) } });
export const msg = (id, role, content, extra = {}) => ({ id, runId: 1, seq: id, role, content, created: sec - 600 + id * 10, ...extra });

export const FULL = {
  model: { upstream: 'hosted', model: 'big', private: false },
  orgs: [
    { id: 'sales', name: 'Sales', kind: 'org', state: 'connected', tiles: 3, connected: 3 },
    { id: 'finance', name: 'Finance', kind: 'org', state: 'blocked', reason: 'Finance allows private models only', rule: 'models', pinned: true, tiles: 2, connected: 0 },
    { id: 'workspace', name: 'Workspace', kind: 'workspace', state: 'connected', tiles: 1, connected: 1 },
  ],
  tiles: [
    { tile: 'sales/orders', name: 'Orders', org: 'sales', state: 'connected' },
    { tile: 'sales/invoices', name: 'Invoices', org: 'sales', state: 'connected' },
    { tile: 'sales/prices', name: 'Price list', org: 'sales', state: 'hidden' },
    { tile: 'finance/ledger', name: 'Ledger', org: 'finance', state: 'blocked', reason: 'Finance allows private models only', by: 'finance', rule: 'models' },
    { tile: 'apps/supplier', name: 'Supplier portal', org: 'finance', state: 'blocked', reason: 'Finance allows private models only', by: 'finance', via: 'apps/*' },
    { tile: 'people/calendar', name: 'Team calendar', org: 'workspace', state: 'connected' },
  ],
  counts: { tiles: 3, orgs: 2, notConnected: 3, hidden: 1, blocked: 2, noAccess: 0, summary: '3 tiles · 2 orgs connected · 3 not connected' },
};

export const SESSIONS = [
  { id: 1, name: 'Main', main: true, model: 'hosted/big', handle: 'h1', status: 'running', agents: 2, requests: 1, connection: { counts: FULL.counts }, summary: FULL.counts.summary, activityMs: now },
  { id: 2, name: 'Month-end', main: false, model: 'local/llama', handle: 'h2', status: 'waiting_input', agents: 0, requests: 0, connection: { counts: { tiles: 5, orgs: 3 } }, activityMs: now - 3600e3 },
  { id: 3, name: 'Leave tracker idea', main: false, model: 'hosted/big', handle: 'h3', status: 'idle', archivedAt: now - 1e6, connection: { counts: { tiles: 2, orgs: 1 } }, activityMs: now - 9 * 86400e3 },
];

export const EMPTY = {
  heading: 'Ask for an outcome', line: 'The workspace agent plans the work, hands building to each tile’s own agent, and asks you before anything changes.',
  can: ['Look across your 3 connected tiles', 'Start tile agents and keep track of them', 'Do things in a tile for you, once you approve', 'Remember what you tell it'],
  cant: ['Change code itself — a tile’s agent does', 'Answer a tile agent’s questions for you', 'See the 3 tiles that aren’t connected', 'Use the web'],
  connected: 3, notConnected: 3, placeholder: 'Ask for an outcome…', hint: 'Say what you want and how you’ll know it’s done. The agent works out the steps.',
  startersTitle: 'Try one — from your tiles',
  starters: [{ kind: 'Fix', tile: 'people/calendar', text: 'Team calendar isn’t running. Find out why.' }, { kind: 'Ask', tile: 'sales/orders', text: 'What does Orders do, and who can use it?' },
    { kind: 'Change', tile: 'sales/invoices', text: 'Add ___ to Invoices', blank: true }, { kind: 'New', text: 'Make a new tile for ___', blank: true }],
};

// a conversation in progress: a plan, a brief, a question, a call to a tile, two tile agents
export const CONVERSATION = {
  messages: [
    msg(1, 'user', 'About tile "Orders" (sales/orders):\nCustomers keep asking for a list of their open orders. Add a CSV export to Orders.'),
    msg(2, 'assistant', 'That touches Orders and Invoices. Here is the plan.', { toolCalls: [call('p1', 'plan', { steps: [] })] }),
    msg(3, 'tool', 'The plan is in front of your person.', { toolCallId: 'p1', name: 'plan' }),
    msg(4, 'user', 'Go ahead'),
    msg(5, 'assistant', '', { toolCalls: [call('b1', 'tile_agent_brief', { tile: 'sales/orders' })] }),
    msg(6, 'tool', 'Brief 4 for sales/orders is in front of your person, who can edit it. Nothing has started.', { toolCallId: 'b1', name: 'tile_agent_brief' }),
    msg(7, 'assistant', '', { toolCalls: [call('c1', 'tile_call', { tile: 'sales/invoices', method: 'POST', path: '/links/rebuild' })] }),
    msg(8, 'tool', 'Pending — request 12 (rebuild the links) waits for your person\'s decision in xbin. Nothing has run.', { toolCallId: 'c1', name: 'tile_call' }),
    msg(9, 'assistant', '', { toolCalls: [call('l1', 'tile_call', { tile: 'finance/ledger', method: 'POST', path: '/vat' })] }),
    msg(10, 'tool', 'refused for this tile — Finance allows private models only. This is a rule (the tile\'s, its org\'s, or your person can\'t see it), not something to work around: tell your person', { toolCallId: 'l1', name: 'tile_call' }),
    msg(11, 'assistant', 'Which customers should get the link first?', { toolCalls: [call('a1', 'ask_user', { question: 'Which customers should get the link first?', choices: ['The three with overdue invoices', 'Everyone with an open order'] })] }),
    msg(12, 'tool', '(asked the user; awaiting their reply)', { toolCallId: 'a1', name: 'ask_user' }),
  ],
  steps: [{ id: 1, runId: 1, kind: 'ask', detail: JSON.stringify({ kind: 'ask_user', question: 'Which customers should get the link first?' }), created: sec - 600 + 115 }],
};
export const CARDS = {
  plan: [{ text: 'Read how Orders and Invoices work today', kind: 'reads', state: 'done' }, { text: 'Orders’ agent adds the CSV export', kind: 'asks', state: 'working' },
    { text: 'Rebuild the links on the 38 open invoices', kind: 'asks', state: 'waiting' }, { text: 'Check both and report back', kind: 'reads', state: 'todo' }],
  briefs: [{ id: 4, tile: 'sales/orders', goal: 'Add “Export open orders (CSV)” to a customer’s page.', doneWhen: 'A CSV with order no., date, status and total downloads for one customer.',
    leaveAlone: 'The orders table and its existing API.', checkBy: 'Tests for the export; a preview of the customer page.', state: 'started', createdMs: now - 500e3, updatedMs: now - 500e3 }],
  requests: [
    { id: 12, kind: 'call', ref: 'rq-12', tile: 'sales/invoices', title: 'Rebuild the order links on the 38 open invoices', method: 'POST', path: '/links/rebuild', state: 'pending', createdMs: now - 120e3 },
    { id: 11, kind: 'session', ref: 'rq-11', tile: 'sales/orders', title: 'start a tile agent in sales/orders', brief: 4, state: 'used', outcome: 'approved by your person — tile agent #7 started in sales/orders (claude, asks before acting).', createdMs: now - 480e3, decidedMs: now - 470e3 },
  ],
  agents: [
    { id: 7, session: 'ts-7', tile: 'sales/orders', harness: 'claude', asks: true, brief: 4, status: 'running', waits: false, last: 'Writing export_test.go — 3 of 5 cases pass', startedMs: now - 250e3 },
    { id: 8, session: 'ts-8', tile: 'sales/invoices', harness: 'claude', asks: true, status: 'running', waits: true, waiting: 'may I run the export tests?', startedMs: now - 200e3 },
  ],
};

// openWs(browser, seed, {width, height, scheme}): the page in a new context, settled — {ctx, page, errors}
export async function openWs(browser, seed, { width = 432, height = 900, scheme = 'dark' } = {}) {
  const ctx = await browser.newContext({ viewport: { width, height }, colorScheme: scheme });
  await serveWs(ctx);
  await ctx.addInitScript(WS_STUB, seed);
  const page = await ctx.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  await page.goto(`${ORIGIN}/`);
  await page.waitForFunction(() => window.wsAgentPage && window.wsAgentPage.testApi().sel != null, null, { timeout: 8000 }).catch(() => {});
  await page.waitForTimeout(300);
  return { ctx, page, errors };
}

// a fresh copy of a fixture (each page changes its seed)
export const fresh = (o) => JSON.parse(JSON.stringify(o));
