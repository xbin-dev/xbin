// hack/agent-template-ws-page.test.mjs — the pure parts of the workspace
// agent's page (builtin-templates/agent/ws/; D235), run by `make js-test`:
// the message contract with xbin's chrome (ws/dock.js), the words and
// readings of the backend's shapes (ws/words.js), the scope panel's model
// (ws/scope-model.js), where the agent's cards sit in its transcript
// (ws/place.js), boot.js's choice of page — and that what the page reads
// is what the backend writes: every field it reads is a JSON name in the
// backend's workspace routes, and every refusal it recognises is a phrase
// the backend says. The page itself is driven in a browser:
// builtin-templates/agent/test/ws-page.mjs, ws-scope.mjs, ws-states.mjs.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';

const T = new URL('../builtin-templates/agent/', import.meta.url);
const dock = await import(new URL('ws/dock.js', T));
const W = await import(new URL('ws/words.js', T));
const SM = await import(new URL('ws/scope-model.js', T));
const { compose, askBlock, UPDATES } = await import(new URL('ws/place.js', T));
const { pageMode, NEEDS } = await import(new URL('boot.js', T));
const src = (f) => readFileSync(new URL(f, T), 'utf8');
const backend = readdirSync(new URL('_backend/', T)).filter((f) => /^ws_.*\.go$/.test(f) && !f.endsWith('_test.go')).map((f) => src('_backend/' + f)).join('\n')
  + src('_backend/coord_workspace.go') + src('_backend/inbox.go'); // inbox.go: the message route's `context`

// ---- the contract with the chrome --------------------------------------------------------------

test('dock: the messages out carry ids only, and only ids that are ids', () => {
  assert.deepEqual(dock.openRequest('rq-12'), { type: 'xbin:agent-open-request', id: 'rq-12' });
  assert.deepEqual(dock.openChangeset('cs_1'), { type: 'xbin:agent-open-changeset', id: 'cs_1' });
  assert.deepEqual(dock.openSession('ts-7', '/sales/orders/'), { type: 'xbin:open-session', session: 'ts-7', tile: 'sales/orders' });
  assert.deepEqual(dock.openSession('new', 'sales/orders', 'change line 40'), { type: 'xbin:open-session', session: 'new', tile: 'sales/orders', draft: 'change line 40' }, 'a redirect: the person\'s words as a draft');
  assert.deepEqual(dock.openSession('ts-7', 'sales/orders', 'x'), { type: 'xbin:open-session', session: 'ts-7', tile: 'sales/orders' }, 'a draft rides only on a new session');
  assert.equal(dock.openSession('new', 'sales/orders', 'y'.repeat(9000)).draft.length, dock.DRAFT_MAX);
  for (const bad of ['', 'a b', '<x>', 'x'.repeat(81), null, 7]) assert.equal(dock.openRequest(bad), null, String(bad));
  assert.equal(dock.openSession('ts 7', 'a/b'), null);
  assert.equal(dock.openSession('ts-7', 'a/../b'), null);
  assert.deepEqual(Object.values(dock.MSG).sort(), ['xbin:agent-context', 'xbin:agent-open-changeset', 'xbin:agent-open-request', 'xbin:open-session']);
});

test('dock: a context message read strictly — a tile path or a screen, nothing else', () => {
  assert.deepEqual(dock.contextOf({ type: 'xbin:agent-context', tile: 'sales/orders', name: '<b>x</b>', grant: true }), { tile: 'sales/orders' });
  assert.deepEqual(dock.contextOf({ type: 'xbin:agent-context', screen: ' Sales desk ' }), { screen: 'Sales desk' });
  assert.deepEqual(dock.contextOf({ type: 'xbin:agent-context' }), { none: true });
  assert.deepEqual(dock.contextOf({ type: 'xbin:agent-context', tile: '../etc' }), { none: true });
  assert.equal(dock.contextOf({ type: 'xbin:agent-open-request', id: 'x' }), null);
  assert.equal(dock.contextOf('xbin:agent-context'), null);
});

test('dock: post and listen — out to the framing window, in only from it', () => {
  const sent = [];
  const handlers = [];
  const parent = { postMessage: (m, o) => sent.push([m, o]) };
  const win = { parent, document: { querySelector: () => null }, addEventListener: (t, h) => handlers.push(h), removeEventListener: () => {} };
  assert.equal(dock.post(dock.openRequest('rq-1'), win), true);
  assert.deepEqual(sent, [[{ type: 'xbin:agent-open-request', id: 'rq-1' }, '*']]);
  assert.equal(dock.post(null, win), false);
  const got = [];
  dock.listen((c) => got.push(c), win);
  handlers[0]({ source: {}, data: { type: 'xbin:agent-context', tile: 'evil/x' } });
  handlers[0]({ source: parent, data: { type: 'xbin:agent-context', tile: 'sales/orders' } });
  assert.deepEqual(got, [{ tile: 'sales/orders' }]);
});

// ---- the words ---------------------------------------------------------------------------------

test('a tile agent\'s card: status as glyph and word, Answer only when it waits for the person', () => {
  const t0 = 1_000_000;
  const card = (a) => W.agentCard({ tile: 'sales/orders', harness: 'claude', startedMs: t0, ...a }, t0 + 250_000, () => 'Orders');
  assert.deepEqual([card({ status: 'running' }).word, card({ status: 'running' }).button], ['working 4:10', 'Open']);
  const w = card({ status: 'running', waits: true, waiting: 'may I run the tests?' });
  assert.deepEqual([w.word, w.button, w.line, w.note, w.tone], ['waiting for you', 'Answer', 'Asks: may I run the tests?', 'Only you can answer.', 'warn']);
  assert.equal(card({ status: 'ended', waits: true }).button, 'Open', 'an ended session waits for nobody');
  assert.equal(card({ status: 'idle' }).word, 'finished');
  assert.equal(card({ status: 'failed' }).tone, 'bad');
  assert.equal(card({ status: 'stopped' }).word, 'stopped for the update');
  assert.equal(card({ status: 'lost' }).word, 'couldn’t continue');
  assert.equal(card({ status: 'starting' }).harness, 'Claude Code');
  assert.equal(W.running(3_723_000), '1:02:03');
});

test('a request\'s card from its fields: waiting with Review… and its expiry; settled — who, how wide, where, what ran; never an Allow', () => {
  const t0 = 10_000_000;
  const r = (q) => W.requestCard({ id: 1, kind: 'call', ref: 'rq-1', tile: 'sales/invoices', method: 'POST', path: '/x', createdMs: t0, expiresMs: t0 + 86_400_000, decidedMs: t0, ...q }, t0 + 120_000, () => 'Invoices');
  const p = r({ state: 'pending' });
  assert.equal(p.button, 'Review…');
  assert.match(p.line, /expires in 23 h 58 min/);
  assert.equal(r({ state: 'pending', expiresMs: t0 + 3_600_000 }).line.split(' · ')[1], 'expires in 58 min', 'the expiry is the field, not 24 h from asking');
  assert.equal(r({ state: 'proposed', kind: 'changeset', title: '5 admin changes proposed' }).button, 'Review…');
  const used = r({ state: 'used', by: 'person', grant: 'once', via: 'app', resultStatus: 200, result: 'POST /x → 200', outcome: 'never drawn' });
  assert.deepEqual([used.head, used.result], ['Allowed once by you', 'POST /x → 200']);
  assert.match(used.line, / · in the app · POST \/x · 200$/);
  assert.equal(r({ state: 'used', grant: 'call' }).head, 'Allowed always, this exact action, by you');
  assert.equal(r({ state: 'used', grant: 'tile', grantUntilMs: 0 }).head, 'Allowed every change to Invoices, by you');
  assert.match(r({ state: 'used', grant: 'tile', grantUntilMs: t0 + 3_600_000 }).head, /^Allowed every change to Invoices until \d\d:\d\d, by you$/);
  assert.equal(r({ state: 'failed', grant: 'once', result: 'the tile answered 500' }).head, 'Allowed once by you — it failed');
  assert.equal(r({ state: 'denied', by: 'person' }).head, 'Denied by you');
  assert.equal(r({ state: 'expired', by: 'xbin' }).head, 'Expired — not approved within 24 hours.');
  assert.equal(r({ state: 'expired' }).button, 'Ask again');
  assert.equal(r({ state: 'withdrawn', by: 'agent' }).head, 'Withdrawn by the agent');
  assert.equal(r({ state: 'applied', kind: 'changeset', result: '2 of 3 changes applied' }).head, '2 of 3 changes applied');
  assert.equal(r({ state: 'pending', ref: 'brief:1:4' }).button, '', 'a start the brief confirms has no Review…');
  for (const st of ['pending', 'proposed', 'approved', 'running', 'used', 'failed', 'denied', 'expired', 'withdrawn', 'applied']) {
    assert.doesNotMatch(r({ state: st }).button, /allow|approve|deny/i, st);
  }
});

test('refusals from their fields: each kind its words and ways forward; the two phrases still read are the backend\'s', () => {
  const card = (kind, reason = '') => W.refusalCard({ callId: 'c', tool: 'tile_call', kind, reason, tile: 'finance/ledger' }, () => 'Ledger');
  assert.deepEqual([card('policy', 'Finance allows private models only').title, card('policy', 'Finance allows private models only').text, card('policy').ways], ['Ledger is blocked by a rule', 'Finance allows private models only.', ['model', 'scope']]);
  assert.deepEqual([card('cannot', 'deleting a tile is its person\'s').title, card('cannot').ways], ['Can’t do this', ['whatCan']]);
  assert.match(card('hidden').text, /You left Ledger out of this session’s scope/);
  assert.equal(card('not-connected').title, 'Ledger isn’t connected in this session');
  assert.deepEqual(card('taint').ways, ['model']);
  // what xbind refuses the agent tells tile agents: what happened and what to do instead, in plain words
  assert.match(card('brief-taint', 'this conversation holds data of finance').title, /Ledger’s agent can’t be told this/);
  assert.match(card('brief-taint').note, /Start the tile agent on the workspace’s connector, on a model the org allows — or ask from a new session/);
  assert.match(card('mode').note, /only you can drive it now/);
  assert.match(card('command').text, /begins with “\/”.*yours to give/);
  assert.ok(card('no-agent').quiet && /an admin chooses who has one/.test(card('no-agent').text));
  for (const k of ['paused', 'off', 'slow']) assert.equal(card(k).quiet, true, k);
  assert.equal(card('something-new').title, 'xbin refused this');
  // a request's and a brief's place is their callId: the page reads nothing of the tool's answer to find it
  assert.doesNotMatch(src('ws/place.js'), /Pending — request|Filed as request|Brief \(|\^Request \(/);
  assert.equal([...src('_backend/ws_store.go').matchAll(/CallID +string +`json:"callId,omitempty"`/g)].length, 4, 'wsReq, wsAgentRow, wsBrief and wsRefused carry callId');
  // a tile agent's place is the request that started it: the words of an outcome are read only for a row that names none
  assert.match(src('_backend/ws_store.go'), /Request +int64 +`json:"request,omitempty"`/, 'a tile agent carries the request that started it');
  assert.match(src('_backend/ws_notify.go'), /Request: r\.ID, CallID: r\.CallID/, 'a tile agent records the request that started it and its call');
  assert.match(src('_backend/ws_tools_agents.go'), /CallID: toolCallOf\(ctx\)/, 'a brief records the call that wrote it');
  assert.match(src('_backend/ws_tools_act.go'), /req\.CallID = toolCallOf\(ctx\)/, 'a request records the call that filed it');
  // the context of a sent message comes back on the run view's message (stream.go messageView)
  assert.match(src('_backend/stream.go'), /v\["context"\] = meta\.Context/);
  assert.ok(backend.includes(`wsUpdatesHead = "${UPDATES}xbin and tile agents reporting, not your person]"`));
});

test('every field the page reads is a JSON name the backend writes', () => {
  // a struct's json tag, a map literal's key, a key set on a map, a request body's field (decoded by name)
  const names = new Set([...backend.matchAll(/json:"([A-Za-z]+)/g), ...backend.matchAll(/"([A-Za-z]+)":/g), ...backend.matchAll(/\["([A-Za-z]+)"\]/g)].map((m) => m[1])
    .concat([...backend.matchAll(/^\s+([A-Z]\w*)(?:, \w+)*\s+\*?(?:int64|bool|string)\b/gm)].map((m) => m[1][0].toLowerCase() + m[1].slice(1))));
  const read = ['user', 'mode', 'delegation', 'workspace', 'paused', 'pausedAt', 'models', 'default', 'upstream', 'private', 'where', 'quota', 'stopped', 'resets', 'limitWords', 'raise',
    'sessions', 'name', 'main', 'model', 'handle', 'connection', 'summary', 'status', 'waiting', 'ask',
    'archivedAt', 'agents', 'requests', 'activityMs', 'held', 'atMs', 'counts', 'tiles', 'orgs', 'notConnected', 'hidden', 'blocked', 'noAccess', 'whatIf', 'connects', 'disconnects', 'allowed', 'taintedOrgs',
    'code', 'reason', 'offer', 'kind', 'ref', 'tile', 'title', 'method', 'path', 'brief', 'state', 'createdMs', 'decidedMs', 'expiresMs', 'grant', 'grantUntilMs', 'by', 'via', 'resultStatus', 'result',
    'refusals', 'callId', 'tool', 'question', 'choices', 'context', 'connected', 'screen',
    'session', 'harness', 'asks', 'waits', 'last', 'startedMs', 'request',
    'goal', 'doneWhen', 'leaveAlone', 'checkBy', 'edited', 'updatedMs', 'plan', 'text', 'started', 'heading', 'line', 'can', 'cant', 'hint', 'startersTitle', 'starters', 'blank',
    'pages', 'slug', 'version', 'updatedBy', 'rev', 'locked', 'lockedWhy', 'body', 'base', 'page', 'lastSession', 'defaults', 'harnesses', 'ticket', 'harnessName', 'needsTicket', 'scope', 'archived'];
  const missing = read.filter((n) => !names.has(n));
  assert.deepEqual(missing, [], 'fields the page reads that no workspace route writes');
});

test('the brief: the backend\'s button, what it runs on, the coding agents to pick from', () => {
  const b = { id: 4, tile: 'sales/orders', goal: 'g', doneWhen: 'd', state: 'draft' };
  const sd = { harness: 'claude', ticket: true, asks: true, line: 'Claude Code · asks before acting', defaults: { harnesses: [{ id: 'claude', name: 'Claude Code' }, { id: 'codex', name: 'Codex' }] } };
  const w = W.briefCard(b, () => 'Orders', sd);
  assert.equal(w.title, 'Brief for Orders’ agent');
  assert.equal(w.button, 'Review and start…');
  assert.equal(w.runsOn, 'Claude Code · asks before acting');
  assert.deepEqual(w.harnesses.map((h) => h.id), ['claude', 'codex']);
  // xbind's noSignIn: the picker names the coding agents that run on the workspace's model with no sign-in
  const sd2 = { ...sd, defaults: { harnesses: [{ id: 'claude', name: 'Claude Code' }, { id: 'codex', name: 'Codex', noSignIn: true }] } };
  assert.deepEqual(W.briefCard(b, undefined, sd2).harnesses.map((h) => h.name), ['Claude Code', 'Codex — no sign-in']);
  assert.match(w.note, /told first/);
  // "asks before acting" only where xbind says the mode asks (ask or plan); not before it said
  assert.match(w.note, /the tile agent asks you before it acts/);
  assert.match(W.briefCard(b, undefined, null).note, /Whether it asks you before it acts shows once xbin says/);
  assert.doesNotMatch(W.briefCard(b, undefined, null).note, /the tile agent asks you before it acts/);
  assert.equal(W.briefCard(b, undefined, { ...sd, ticket: false }).button, 'Start', 'the person\'s preference lets it through: the click is the confirmation');
  assert.match(W.briefCard(b, undefined, { ...sd, asks: false }).note, /acts without asking/);
  assert.equal(W.briefCard(b, undefined, null).button, 'Review and start…', 'before the defaults are read: the asking button');
  assert.equal(W.briefCard({ ...b, state: 'asked' }).button, 'Review…');
  assert.equal(W.briefCard({ ...b, state: 'started' }).button, '');
  assert.equal(W.briefCard({ ...b, state: 'started' }).editable, false);
  assert.deepEqual(W.briefCard(b).fields.map((f) => f.label), ['Goal', 'Done when', 'Leave alone', 'Check by']);
});

test('the plan: numbered, each step tagged, the go-ahead\'s meaning said once', () => {
  const p = W.planBlock([{ text: 'a', kind: 'reads', state: 'todo' }, { text: 'b', kind: 'asks', state: 'todo' }, { text: 'c', kind: 'asks', state: 'todo' }]);
  assert.equal(p.waitsForGo, true);
  assert.deepEqual(p.block.entries.map((e) => e.tag.text), ['reads only', 'asks you', 'asks you']);
  assert.equal(p.block.note, 'Saying go ahead starts step 1. It does not approve steps 2, 3: each asks when it is reached.');
  const q = W.planBlock([{ text: 'a', kind: 'reads', state: 'done' }, { text: 'b', kind: 'asks', state: 'waiting' }, { text: 'c', kind: 'reads', state: 'working' }]);
  assert.equal(q.waitsForGo, false);
  assert.deepEqual(q.block.entries.map((e) => [e.status, e.tag.text]), [['completed', 'done'], ['in_progress', 'needs your OK'], ['in_progress', 'working']]);
  assert.equal(q.block.note, '');
});

test('the context chip: sent as a field, drawn from the one the backend answers back', () => {
  assert.deepEqual(W.aboutOf({ tile: 'sales/orders', name: 'Orders', connected: true }), { tile: 'sales/orders', name: 'Orders', connected: true });
  assert.deepEqual(W.aboutOf({ tile: 'sales/orders' }), { tile: 'sales/orders', name: 'sales/orders', connected: undefined });
  assert.deepEqual(W.aboutOf({ screen: 'Sales desk' }), { screen: 'Sales desk' });
  assert.equal(W.aboutOf(null), null);
  assert.equal(W.aboutOf({}), null);
  assert.deepEqual(W.blankAt('Add ___ to Invoices'), [4, 7]);
  assert.equal(W.blankAt('no blank'), null);
});

test('sessions, the chip, the notebook, the states', () => {
  assert.deepEqual(W.sessionRow({ main: true, name: 'Main', model: 'hosted/big', status: 'running', agents: 2, connection: { counts: { tiles: 14, orgs: 3 } } }),
    { name: 'Main', scope: '14 tiles · 3 orgs · big', status: { glyph: 'clock', word: 'working · 2 tile agents', tone: '' }, main: true, archived: false });
  assert.equal(W.sessionRow({ status: 'idle', requests: 1 }).status.word, 'waiting for you');
  const c = W.chipWords({ tiles: 14, orgs: 3, notConnected: 6 }, 'hosted/big');
  assert.deepEqual([c.conn, c.not, c.model], ['14 tiles · 3 orgs', '6 not connected', 'big']);
  assert.equal(c.label, 'Scope: 14 tiles in 3 orgs connected, 6 not connected. Model big.');
  assert.deepEqual([W.chipWords({ tiles: 14, orgs: 3, notConnected: 6 }, 'x/big', true).conn, W.chipWords({ tiles: 14, orgs: 3, notConnected: 6 }, 'x/big', true).not], ['14 connected', '6 not']);
  assert.equal(W.chipWords({ tiles: 2, orgs: 1, notConnected: 0 }, 'm').not, '');
  assert.equal(W.slug('How Ana likes things done!'), 'how-ana-likes-things-done');
  assert.deepEqual(W.pageClosed({ locked: true, lockedWhy: 'Not available in this session: Finance isn’t connected' }), { closed: true, why: 'Not available in this session: Finance isn’t connected' });
  assert.deepEqual(W.pageClosed({ readable: false, reason: 'r' }), { closed: true, why: 'r' });
  assert.equal(W.pageTag({ orgs: [] }), 'yours');
  // quota: the state's fields
  const q = W.quotaOf({ quota: { stopped: true, resets: '2030-01-02T01:00:00Z', limitWords: 'your 2M tokens for today', raise: 'an admin of sales, or a workspace admin' } });
  assert.ok(q.until.startsWith('Sending resumes at '));
  assert.match(q.words, /^You've used your 2M tokens for today\. It resets at /);
  assert.equal(q.raise, 'An admin of sales, or a workspace admin can raise your limit.');
  assert.equal(W.quotaOf({}), null);
  assert.equal(W.quotaOf({ quota: { stopped: false } }), null);
  // the composer: paused before a quota before the connection; offline keeps the draft
  assert.deepEqual(W.composerState({ paused: true, quota: { until: 'x' } }), { placeholder: 'Resume to send a message', disabled: true, note: '' });
  assert.equal(W.composerState({ quota: { until: 'Sending resumes at 01:00' } }).placeholder, 'Sending resumes at 01:00');
  assert.equal(W.composerState({ updating: true }).placeholder, 'Reconnecting…');
  assert.deepEqual(W.composerState({ offline: true, conn: 'reconnecting', empty: false }), { placeholder: 'Reply, or ask for something else…', disabled: false, hold: true, note: W.W.keptOffline });
  assert.equal(W.composerState({ asking: true, empty: false }).placeholder, 'Or type your answer…');
  assert.equal(W.composerState({}).placeholder, 'Ask for an outcome…');
  assert.equal(W.CHOICES.starterSends, false);
  assert.equal(W.CHOICES.typeWhilePaused, false);
});

// ---- the scope panel's model ---------------------------------------------------------------------

const VIEW = {
  orgs: [{ id: 'sales', name: 'Sales' }, { id: 'finance', name: 'Finance', state: 'blocked', reason: 'Finance allows private models only', rule: 'models' }],
  tiles: [{ tile: 's/o', name: 'Orders', org: 'sales', state: 'connected' }, { tile: 's/p', name: 'Prices', org: 'sales', state: 'hidden' },
    { tile: 'f/l', name: 'Ledger', org: 'finance', state: 'blocked', reason: 'Finance allows private models only' }, { tile: 'w/c', name: 'Calendar', state: 'connected' }],
};

test('scope: the tree, ticks staged, the scope they are', () => {
  let ticks = SM.initialTicks(VIEW);
  const rows = SM.tree(VIEW, ticks);
  assert.deepEqual(rows.map((o) => [o.id, o.box, `${o.ticked}/${o.of}`, o.state]), [['sales', 'mixed', '1/2', ''], ['finance', 'disabled', '0/1', 'blocked'], ['workspace', 'on', '1/1', 'connected']]);
  assert.equal(rows[1].reason, 'Finance allows private models only');
  assert.deepEqual(SM.toggleTile(ticks, VIEW, 'f/l'), ticks, 'a blocked tile can\'t be ticked');
  assert.deepEqual(SM.staged(VIEW, ticks), { connect: 0, disconnect: 0, any: false });
  ticks = SM.toggleOrg(ticks, VIEW, 'sales');
  assert.deepEqual([...ticks].sort(), ['s/o', 's/p', 'w/c']);
  assert.equal(SM.scopeOf(VIEW, ticks), null, 'everything allowed: null');
  ticks = SM.toggleTile(ticks, VIEW, 'w/c');
  assert.deepEqual(SM.scopeOf(VIEW, ticks), { orgs: ['sales'], tiles: [] });
  ticks = SM.toggleTile(ticks, VIEW, 's/p');
  assert.deepEqual(SM.scopeOf(VIEW, ticks), { orgs: [], tiles: ['s/o'] });
  assert.deepEqual(SM.staged(VIEW, ticks), { connect: 0, disconnect: 1, any: true });
  assert.deepEqual(SM.toggleOrg(new Set(['s/o', 's/p']), VIEW, 'sales'), new Set(), 'a whole org\'s box takes them all off');
  assert.deepEqual(SM.tree(VIEW, SM.initialTicks(VIEW), 'not').map((o) => o.tiles.map((t) => t.tile)), [['s/p'], ['f/l']]);
});

test('scope: a model change\'s what-if in words, and a refused one', () => {
  const w = SM.whatIf({ whatIf: { model: 'local/llama', connects: 2, disconnects: 0, allowed: true, summary: '5 tiles' } },
    { whatIf: { model: { model: 'llama', private: true }, connect: [{ tile: 'f/l', name: 'Ledger' }, { tile: 'f/s', name: 'Supplier' }] } }, 'hosted/big');
  assert.deepEqual([w.connectsWords, w.disconnectsWords, w.connects.join(), w.button, w.private, w.summary, w.refused], ['Connects 2 tiles', 'Disconnects none', 'Ledger,Supplier', 'Switch model', true, '5 tiles', null]);
  assert.equal(SM.whatIf({ whatIf: { model: 'm', disconnects: 3, allowed: true } }, null).button, 'Switch and disconnect 3');
  const r = SM.whatIf({ whatIf: { model: 'hosted/small', allowed: false, taintedOrgs: ['Finance'] } }, null, 'local/llama').refused;
  assert.equal(r.title, 'This session can’t move to small');
  assert.equal(r.start, 'Start a new session on small');
  assert.equal(r.stay, 'Stay on llama');
  assert.match(r.after, /starts empty/);
  assert.match(SM.refusedWords({ model: 'x/big', orgs: ['Finance'], reason: 'Finance allows private models only' }).body, /holds data from Finance, and Finance allows private models only/);
});

// ---- the cards in the transcript ----------------------------------------------------------------

const tool = (id, name, result, extra = {}) => ({ k: 'tool', id: 'c' + id, callId: id, name, result, args: '{}', created: 100 + Number(String(id).replace(/\D/g, '') || 0), state: 'done', ...extra });
test('compose: each card where the conversation made it; nothing lost; the redundant rows hidden', () => {
  const blocks = [
    { k: 'user', id: 'm1', text: 'hi' },
    tool('p1', 'plan', 'The plan is in front of your person.'),
    { k: 'user', id: 'm4', text: 'Go ahead' },
    tool('b1', 'tile_agent_brief', 'Brief 4 for sales/orders is in front of your person, who can edit it.'),
    tool('c1', 'tile_call', 'Pending — request 12 (rebuild) waits for your person\'s decision'),
    tool('p2', 'plan', 'The plan is updated.'),
    tool('a1', 'ask_user', '(asked the user; awaiting their reply)', { args: JSON.stringify({ question: 'Which?', choices: ['A', 'B'] }) }),
    { k: 'step', id: 's1', kind: 'ask', detail: { kind: 'ask_user', question: 'Which?' } },
  ];
  const cards = {
    plan: [{ text: 'x', kind: 'asks', state: 'todo' }],
    briefs: [{ id: 4, callId: 'b1', tile: 'sales/orders', state: 'started', createdMs: 0 }],
    requests: [{ id: 12, kind: 'call', ref: 'rq-12', callId: 'c1', state: 'pending', createdMs: 0 }, { id: 11, kind: 'session', ref: 'rq-11', brief: 4, state: 'used', outcome: 'approved by your person — tile agent #7 started in sales/orders', createdMs: 0 }],
    refusals: [{ id: 3, callId: 'c1', tool: 'tile_call', kind: 'slow', reason: '', tile: '' }, { id: 2, callId: 'zz', tool: 'tile_call', kind: 'policy', reason: 'r', tile: 'f/l', createdMs: 0 }],
    agents: [{ id: 7, tile: 'sales/orders', brief: 4, status: 'running', startedMs: 0 }, { id: 9, tile: 'x/y', status: 'running', startedMs: 999_999_999 }],
  };
  const L = compose(blocks, cards, { status: 'idle' }, { words: W.W });
  const at = (id) => L.at.get(id) || { replace: null, after: [], hide: false };
  assert.deepEqual(at('cp1').replace.map((b) => b.kind + ':' + (b.type || '')), ['plan:', 'host:replies'], 'the plan in place of its first call, with Go ahead while nothing started');
  assert.match(at('cp1').replace[0].meta, /^you said go ahead/);
  assert.equal(at('cp2').hide, true, 'a later plan call is hidden');
  assert.deepEqual(at('cb1').replace.map((b) => b.k), ['ws:brief:4']);
  assert.deepEqual(at('cb1').after.map((b) => b.k), ['ws:req:11', 'ws:agent:7'], 'the start request after its brief, the tile agent it started after it');
  assert.deepEqual(at('cc1').replace.map((b) => b.k), ['ws:req:12'], 'a request in place of the call that filed it');
  assert.equal(at('ca1').replace[0].kind, 'ask');
  assert.equal(at('ca1').replace[0].action, null, 'the newest question waits');
  assert.equal(at('s1').hide, true, 'the journal\'s "asked: …" is hidden');
  assert.equal(L.live.ask.k, 'ws:ask:a1');
  // the agent started by request 11 sits with the brief's group; one of no request or brief, by time (the end)
  const placed = [...L.at.values()].flatMap((e) => [...(e.replace || []), ...e.after]).concat(L.tail).map((b) => b.k);
  assert.ok(placed.includes('ws:agent:7') && placed.includes('ws:agent:9'));
  for (const k of ['ws:brief:4', 'ws:req:11', 'ws:req:12', 'ws:agent:7', 'ws:agent:9', 'ws:ref:3', 'ws:ref:2']) assert.equal(placed.filter((x) => x === k).length, 1, `${k} once`);
  assert.deepEqual(at('cc1').after.map((b) => b.k), ['ws:ref:3'], 'a refusal after the call it answers (its callId)');
  // a host block's version moves with what it shows
  const v1 = at('cc1').replace[0].v;
  cards.requests[0].state = 'used';
  assert.notEqual(compose(blocks, cards, {}, { words: W.W }).at.get('cc1').replace[0].v, v1);
  // a tile agent sits after the request that started it, by that request's id (its `request`) — whatever the
  // request's outcome says, and whichever brief it names; one whose request is off the list sits after the
  // tile_agent_start call that filed it (its `callId`); only a row from before those fields is matched by words
  const byId = {
    briefs: [{ id: 4, callId: 'b1', tile: 'sales/orders', state: 'started', createdMs: 0 }],
    requests: [{ id: 12, kind: 'call', ref: 'rq-12', callId: 'c1', state: 'used', outcome: 'approved — tile agent #7 started in sales/orders', createdMs: 0 },
      { id: 11, kind: 'session', ref: 'rq-11', callId: 'p2', brief: 4, state: 'used', outcome: 'approved', createdMs: 0 }],
    agents: [{ id: 7, tile: 'sales/orders', brief: 4, request: 11, callId: 'p2', status: 'running', startedMs: 0 },
      { id: 8, tile: 'sales/orders', request: 99, callId: 'a1', status: 'running', startedMs: 0 },
      { id: 9, tile: 'sales/orders', status: 'idle', startedMs: 0 }],
  };
  const B = compose(blocks, byId, { status: 'idle' }, { words: W.W });
  const where = (k) => [...B.at.entries()].filter(([, e]) => [...(e.replace || []), ...e.after].some((b) => b.k === k)).map(([id]) => id).join();
  assert.equal(where('ws:req:11'), 'cp2', 'the start\'s request in place of the call that filed it');
  assert.equal(where('ws:agent:7'), 'cp2', 'a tile agent after the request that started it, by the request\'s id — not after the request whose outcome names "#7"');
  assert.equal(where('ws:agent:8'), 'ca1', 'its request not on the list: after the call that filed it');
  assert.notEqual(where('ws:agent:9'), 'cc1', 'a row with neither field is not agent #7: the words of request 12 name another');
  byId.agents[2].id = 7; byId.agents.shift();
  assert.equal(compose(blocks, byId, {}, { words: W.W }).at.get('cc1').after.some((b) => b.k === 'ws:agent:7'), true, 'a row from before the fields: by the outcome\'s words, as before');
  // without a callId a card is not placed by the tool's words: by its brief, else by time
  const noIds = { briefs: [{ id: 4, tile: 'sales/orders', state: 'draft', createdMs: 0 }], requests: [{ id: 12, kind: 'call', ref: 'rq-12', state: 'pending', createdMs: 0 }] };
  const N = compose(blocks, noIds, { status: 'idle' }, { words: W.W });
  assert.equal(N.at.get('cb1'), undefined, 'a brief without a callId is not matched by "Brief 4 for"');
  assert.ok(!(N.at.get('cc1') || { replace: null }).replace, 'a request without a callId is not matched by "Pending — request 12"');
  // a callId of a call not in view (an older page of the transcript): by time
  const far = compose(blocks, { requests: [{ id: 12, kind: 'call', ref: 'rq-12', callId: 'gone', state: 'pending', createdMs: 0 }] }, {}, { words: W.W });
  assert.deepEqual([far.at.get('m1').after.map((b) => b.k), far.at.get('cc1')], [['ws:req:12'], undefined]);
  // no transcript yet: everything at the tail
  assert.equal(compose([], cards, {}, { words: W.W }).tail.length, 9);
  // a redirect: a card of its own, carrying the tile and the person's own words before it
  const red = [{ k: 'user', id: 'm1', text: 'change line 40 in export.go' }, tool('r1', 'ask_user', '', { args: JSON.stringify({ question: 'Q', kind: 'redirect', tile: 'sales/orders/' }) })];
  const R = compose(red, { ask: null }, {}, { words: W.W }).at.get('cr1').replace[0];
  assert.deepEqual([R.type, R.data.tile, R.data.draft, R.data.waits], ['redirect', 'sales/orders', 'change line 40 in export.go', true]);
  const C = compose(red, { ask: { callId: 'r1', kind: 'cant', question: 'Q', choices: ['nearest'] } }, {}, { words: W.W }).at.get('cr1').replace[0];
  assert.deepEqual([C.type, C.data.choices], ['cant', ['nearest']], 'the cards\' ask wins over the call\'s arguments');
});

test('a question: choices with something else, answered by the next message', () => {
  const b = tool('a1', 'ask_user', '', { args: JSON.stringify({ question: 'Which?', choices: ['A', 'B'] }) });
  const live = askBlock(b, null, true, W.W);
  assert.deepEqual([live.mode, live.submit, live.skip, live.schema.properties.answer.enum, live.schema.properties.other._meta._askUserQuestionCustomAnswer.questionId], ['form', 'Answer', 'Skip — let the agent look around first', ['A', 'B'], 'answer']);
  assert.deepEqual([askBlock(b, 'A', false, W.W).action, askBlock(b, 'A', false, W.W).content], ['accept', { answer: 'A' }]);
  assert.deepEqual(askBlock(b, 'something else', false, W.W).content, { other: 'something else' });
  assert.equal(askBlock(tool('a2', 'ask_user', '', { args: JSON.stringify({ question: 'Why?' }) }), null, true, W.W).mode, 'text');
});

// ---- boot.js: which page ----------------------------------------------------------------------------

test('boot: the workspace page only when the backend says so; the normal page otherwise', async () => {
  const answer = (status, body) => async () => ({ status, ok: status >= 200 && status < 300, text: async () => (typeof body === 'string' ? body : JSON.stringify(body)) });
  const nowait = async () => {};
  const asked = [];
  assert.equal(await pageMode(async (u) => { asked.push(u); return answer(200, { mode: 'workspace' })(); }, 'apps/agent', nowait), 'normal', 'a tile\'s page: not a sealed component');
  assert.deepEqual(asked, [], '…and its backend is asked nothing more than before');
  assert.equal(await pageMode(answer(404, 'not found'), 'xbin/other', nowait), 'normal');
  assert.equal(await pageMode(answer(200, { mode: 'workspace' }), 'xbin/wsagent', nowait), 'workspace');
  assert.equal(await pageMode(answer(200, {}), 'xbin/wsagent', nowait), 'normal');
  assert.equal(await pageMode(answer(200, 'not json'), 'xbin/wsagent', nowait), 'normal');
  let n = 0;
  const flaky = async () => (++n < 3 ? { status: 502, ok: false, text: async () => '' } : { status: 200, ok: true, text: async () => '{"mode":"workspace"}' });
  assert.equal(await pageMode(flaky, 'xbin/wsagent', nowait), 'workspace', 'a backend that is starting is asked again');
  assert.equal(await pageMode(async () => { throw new Error('down'); }, 'xbin/wsagent', nowait), 'normal');
  const urls = [];
  await pageMode(async (u) => { urls.push(u); return { status: 404, ok: false, text: async () => '' }; }, 'xbin/wsagent', nowait);
  assert.deepEqual(urls, ['/api/xbin/wsagent/ws/state']);
  assert.equal(NEEDS.kit, 4, 'the normal page needs no newer kit');
  assert.equal(NEEDS.ws, 5);
});

test('the page is its own tree: the normal app never imports ws/, ws/ never imports the normal app\'s views', () => {
  const ws = readdirSync(new URL('ws/', T)).filter((f) => f.endsWith('.js'));
  for (const f of ws) {
    const s = src('ws/' + f);
    for (const m of s.matchAll(/^import[^'"]*['"]([^'"]+)['"]/gm)) {
      assert.ok(m[1].startsWith('/vendor/') || m[1].startsWith('./') || /^\.\.\/(model\/|chat-window\.js|chat-copy\.js|chat-md\.js)/.test(m[1]), `ws/${f} imports ${m[1]}`);
    }
    assert.ok(s.split('\n').length <= 1000, `ws/${f} is under the 1000-line cap`);
  }
  for (const f of readdirSync(T).filter((x) => x.endsWith('.js') && x !== 'boot.js')) assert.doesNotMatch(src(f), /['"]\.\/ws\//, `${f} does not load the workspace page`);
});
