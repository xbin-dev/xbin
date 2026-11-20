// hack/agent-template-native-ws.test.mjs — a workspace agent's native view
// (builtin-templates/agent/native.js → native/ws.js, D233) rendered in node
// against the web page's own fake backend (test/ws-stub.mjs WS_STUB, through
// test/ws-native-stub.mjs), with hack/xbn/node.mjs — xb-native's JSON target.
// The tests assert what a person gets: the agent's cards where the
// conversation made them, a request and a tile agent as nodes THE APP draws
// by xbind's id (never an Allow of the tile's own), the scope panel's staged
// ticks and what-if, a refused model change, the states that stop it, and
// what the view asks of the app's chrome. Every run must end with no runtime
// error and no warning diagnostic. Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { runNative } from './xbn/node.mjs';
import { fullCaps } from '../web/xb/vocab.js';
import { SESSIONS, FULL, EMPTY, CONVERSATION, CARDS, msg, call, fresh, now as NOW } from '../builtin-templates/agent/test/ws-stub.mjs';
import { W, refusalCard } from '../builtin-templates/agent/ws/words.js';
import { VIEWS, openView } from '../builtin-templates/agent/ws/dock.js';
import { PAGE_VIEWS } from '../web/agent-chrome-words.js';

const TPL = new URL('../builtin-templates/agent/', import.meta.url).pathname;

// The apps played: one that shows the tile as the person's agent (the wire
// feature `agent`) on a phone or an iPad; one without the feature; and one
// from before vocabulary D233 (no card, connection, request or session, the
// chat family at its old revisions).
const app = (width = 'compact', agent = true) => { const c = fullCaps(); if (agent) c.features.push('agent'); c.width = width; return c; };
const PHONE = app();
const PAD = app('regular');
const BEFORE = (() => {
  const c = app('compact', false);
  for (const p of ['card', 'connection', 'request', 'session']) delete c.prims[p];
  Object.assign(c.prims, { transcript: 2, question: 1, plan: 1, composer: 1 });
  return c;
})();

const seedOf = (over = {}) => ({ sessions: fresh(SESSIONS), full: fresh(FULL), empty: fresh(EMPTY), ...over });
const talking = (over = {}) => seedOf({ cards: { 1: fresh(CARDS) }, views: { 1: fresh(CONVERSATION) }, ...over });

async function run(seed, steps = [], { caps = PHONE, data = {}, calls } = {}) {
  const r = await runNative({ caps, entry: TPL + 'native.js', steps: [{ wait: 300 }, ...steps],
    data: { now: NOW, self: 'xbin/wsagent', setup: TPL + 'test/ws-native-stub.mjs', seed, ...(calls !== undefined ? { calls } : {}), ...data } });
  assert.equal(r.fatal, null);
  assert.deepEqual(r.errors, [], 'no runtime errors');
  assert.deepEqual(r.diagnostics.filter((d) => d.level !== 'info'), [], 'no diagnostics');
  r.calls = (r.extra && r.extra.calls) || [];
  r.asked = r.messages.filter((m) => m.op === 'call' && m.what === 'agent').map((m) => m.args.msg);
  return r;
}

function all(root, m, out = [], inside = !m.in) {
  if (!root) return out;
  const hit = (n, q) => (!q.t || n.t === q.t) && Object.entries(q.p || {}).every(([k, v]) => JSON.stringify((n.p || {})[k]) === JSON.stringify(v))
    && (q.has == null || JSON.stringify(n.p || {}).includes(q.has));
  if (inside && hit(root, m)) out.push(root);
  const deeper = inside || hit(root, m.in);
  for (const c of root.c || []) all(c, m, out, deeper);
  return out;
}
const find = (tree, m) => all(tree.root || tree, m)[0] || null;
const json = (tree) => JSON.stringify(tree);
const sent = (r, method, re) => r.calls.filter((c) => c.method === method && re.test(c.url));
const labels = (tree, q) => all(tree.root, q).map((n) => n.p.label);

test('the conversation: the agent\'s cards where it made them, a request and a tile agent as the app\'s own nodes', async () => {
  const r = await run(talking());
  const t = r.tree;
  assert.equal(find(t, { t: 'screen' }).p.title, 'Main', 'the session in view names the screen');
  // the plan, in place of its call: numbered, each step tagged, and the line that says what go ahead does not approve
  const plan = find(t, { t: 'plan' });
  assert.equal(plan.p.numbered, true);
  assert.deepEqual(plan.p.entries.map((e) => e.status), ['completed', 'in_progress', 'in_progress', 'pending']);
  assert.deepEqual(plan.p.tags.map((x) => x.text), ['done', 'working', 'needs your OK', 'reads only']);
  assert.match(plan.p.meta, /you said go ahead/);
  // a request xbind holds is named by xbind's id and nothing else: the app draws it
  const reqs = all(t.root, { t: 'request' });
  // (a brief's start is its brief's card — one item a pending act, D250 — never a request node beside it)
  assert.deepEqual(reqs.map((n) => n.p), [{ id: 'rq-12', kind: 'call' }]);
  assert.deepEqual(reqs.map((n) => n.e || []), [[]], 'a request node takes no event of the tile\'s');
  // a tile agent is xbind's session, by id
  assert.deepEqual(all(t.root, { t: 'session' }).map((n) => n.p), [{ id: 'ts-7', tile: 'sales/orders' }, { id: 'ts-8', tile: 'sales/invoices' }]);
  // the brief that started one, and xbin's refusal with the ways forward
  const brief = find(t, { t: 'card', p: { title: 'Brief for Orders’ agent' } });
  assert.equal(brief.p.badge, 'Started');
  assert.ok(all(brief, { t: 'text' }).some((x) => /^Its start: allowed once by you/.test(x.p.text || '')), 'the brief carries what came of its start');
  assert.equal(all(brief, { t: 'button' }).length, 0, 'a started brief has nothing left to press');
  const refusal = find(t, { t: 'card', p: { title: 'Ledger is blocked by a rule' } });
  assert.deepEqual(all(refusal, { t: 'button' }).map((b) => b.p.label), ['Change the model…', 'Open scope']);
  // the question: its choices as rows, "Something else" riding on them, a skip that is not an answer
  const q = find(t, { t: 'question' });
  assert.equal(q.p.style, 'rows');
  assert.deepEqual(q.p.other, { answer: 'other' });
  assert.deepEqual(q.p.schema.properties.answer.enum, ['The three with overdue invoices', 'Everyone with an open order']);
  assert.equal(q.p.schema.properties.other.title, W.somethingElse);
  assert.equal(q.p.skipLabel, W.skip);
  assert.equal(q.p.submitLabel, W.answer);
  // the message's "About …" chip, from what the backend resolved
  assert.equal(find(t, { t: 'message', p: { role: 'user' } }).p.sender, 'About Orders');
  // the composer: the question's choices as quick replies; the placeholder says what it waits on
  const c = find(t, { t: 'composer' });
  assert.equal(c.p.placeholder, W.placeholderQuestion);
  assert.deepEqual(c.p.replies.map((x) => x.label), ['The three with overdue invoices', 'Everyone with an open order']);
  assert.equal(find(t, { t: 'activity' }).p.text, '2 tile agents working');
  // nothing in the tree grants: no Allow, no Deny, no Resume, and never the vocabulary's own approval card
  assert.equal(find(t, { t: 'approval' }), null);
  for (const word of ['"Allow', '"Deny"', '"Resume"', '"Approve"']) assert.ok(!json(t).includes(word), `the view draws no ${word}`);
  // the app's button counts what waits: a request, a tile agent that asks, the agent's question
  assert.deepEqual(r.messages.filter((m) => m.op === 'meta').pop(), { op: 'meta', title: 'Workspace agent', badge: '3' });
});

test('a quick reply is the agent\'s suggestion: a tap fills the composer, and only Send sends it (D241)', async () => {
  const r = await run(talking(), [{ event: { k: { t: 'composer' }, type: 'reply', payload: { id: 'ask:1' } } }, { snapshot: 'filled' }]);
  assert.equal(sent(r, 'POST', /\/runs\/1\/message$/).length, 0, 'a tap sends nothing');
  assert.equal(find(r.snapshots.filled, { t: 'composer' }).p.value, 'Everyone with an open order', 'the composer holds it, to edit or send');
  const rs = await run(talking(), [{ event: { k: { t: 'composer' }, type: 'reply', payload: { id: 'ask:1' } } },
    { event: { k: { t: 'composer' }, type: 'send', payload: { value: 'Everyone with an open order, please' } } }]);
  const [m] = sent(rs, 'POST', /\/runs\/1\/message$/);
  assert.equal(JSON.parse(m.body).text, 'Everyone with an open order, please');
  const r2 = await run(talking(), [{ event: { k: { t: 'question' }, type: 'submit', payload: { content: { other: 'Only Acme' } } } }]);
  assert.equal(JSON.parse(sent(r2, 'POST', /\/message$/)[0].body).text, 'Only Acme');
  const r3 = await run(talking(), [{ event: { k: { t: 'question' }, type: 'skip', payload: {} } }]);
  assert.equal(JSON.parse(sent(r3, 'POST', /\/message$/)[0].body).text, W.skipSent, 'a skip says it is a skip, never a choice');
});

test('the empty state: what it can and can\'t do in three lines, starters that fill the composer', async () => {
  const r = await run(seedOf(), [{ snapshot: 'empty' }, { event: { k: { t: 'transcript' }, type: 'suggestion', payload: { id: '2' } } }]);
  const e = find(r.snapshots.empty, { t: 'transcript' }).p.empty;
  assert.equal(e.title, 'Ask for an outcome');
  assert.deepEqual(e.lists.map((l) => [l.title, l.items.length]), [['It can', 3], ['It can’t', 3]], 'three lines each on a phone');
  assert.deepEqual(e.suggestions.map((s) => s.kind), ['Fix', 'Ask', 'Change'], 'three starters on a phone');
  assert.equal((find(r.snapshots.empty, { t: 'transcript' }).c || []).length, 0, 'the slot shows only in a transcript with no rows');
  const c0 = find(r.snapshots.empty, { t: 'composer' });
  assert.equal(c0.p.placeholder, W.placeholder);
  assert.equal(c0.p.hint, EMPTY.hint, 'the standing hint, until the first message');
  // a starter fills; nothing is sent
  assert.equal(find(r.tree, { t: 'composer' }).p.value, 'Add ___ to Invoices');
  assert.equal(sent(r, 'POST', /\/message$/).length, 0);
  const pad = await run(seedOf(), [], { caps: PAD });
  const e2 = find(pad.tree, { t: 'transcript' }).p.empty;
  assert.equal(e2.suggestions.length, 4, 'four beside the work');
  assert.equal(e2.lists[0].items.length, 5, 'its four, and the web its person approves page by page (D239)');
  assert.equal(find(pad.tree, { t: 'connection' }).p.connected, '3 tiles · 2 orgs · web', 'the whole line where there is room, the web on it (D239)');
});

test('paused: no banner of its own (the app\'s bar says it, with Resume), the draft kept in a read-only composer', async () => {
  const state = { user: 'ana', mode: 'workspace', delegation: { workspace: true, paused: false }, models: { default: 'hosted/big', models: [] } };
  const t = talking({ state });
  const paused = { ...state, delegation: { workspace: true, paused: true, pausedAt: new Date(NOW - 60e3).toISOString() } };
  const r = await run(t, [{ input: [{ t: 'composer' }, 'tell me what changed'] }, { call: ['seed', { state: paused }] },
    { call: ['event', { type: 'delegate', data: { op: 'state', user: 'ana' } }] }, { wait: 400 }]);
  assert.equal(find(r.tree, { t: 'notice', has: 'Paused by you' }), null, 'one place says it is paused: the app\'s bar');
  const c = find(r.tree, { t: 'composer' });
  assert.ok(c, 'the composer stays, with the words in it');
  assert.equal(c.p.value, 'tell me what changed');
  assert.equal(c.p.disabled, true, 'read-only: nothing can be sent while paused');
  assert.equal(c.p.placeholder, W.paused);
  assert.ok(!labels(r.tree, { t: 'button' }).some((l) => /resume/i.test(l)), 'no Resume of the tile\'s own');
});

test('a used-up quota and a turned-off workspace hold the composer, in words', async () => {
  const resets = new Date(NOW + 3600e3).toISOString();
  const quota = { stopped: true, quota: 'user.tokensPerDay', limitWords: 'your 2M tokens for today', resets, raise: 'a workspace admin' };
  const r = await run(talking({ state: { user: 'ana', mode: 'workspace', delegation: { workspace: true, paused: false }, models: { models: [] }, quota } }));
  assert.ok(find(r.tree, { t: 'notice', p: { title: 'Today’s limit is used up.' } }));
  const c = find(r.tree, { t: 'composer' });
  assert.equal(c.p.disabled, true);
  assert.match(c.p.placeholder, /^Sending resumes at \d\d:\d\d$/);
  const off = await run(talking({ state: { user: 'ana', mode: 'workspace', delegation: { workspace: false, paused: false }, models: { models: [] } } }));
  assert.ok(find(off.tree, { t: 'notice', p: { title: 'Turned off for this workspace.' } }));
  assert.equal(find(off.tree, { t: 'composer' }).p.disabled, true);
});

test('no connection: the banner after a while, the draft kept and not sent', async () => {
  const r = await run(talking(), [{ call: ['drop', true] }, { call: ['push', { type: 'ping' }] }, { wait: 9000 },
    { input: [{ t: 'composer' }, 'while offline'] }, { event: { k: { t: 'composer' }, type: 'send', payload: { value: 'while offline' } } }, { wait: 100 }]);
  assert.ok(find(r.tree, { t: 'notice', p: { title: 'Can’t reach the workspace.' } }), 'the banner');
  const c = find(r.tree, { t: 'composer' });
  assert.equal(c.p.hint, W.keptOffline);
  assert.equal(c.p.disabled, false, 'the draft can still be written');
  assert.equal(sent(r, 'POST', /\/message$/).length, 0, 'nothing is queued behind the person\'s back');
  assert.ok(labels(r.tree, { t: 'button' }).includes('Try now'));
});

test('the scope panel: totals, orgs unfolding to tiles in their four states, ticks staged until Apply', async () => {
  const r = await run(talking(), [{ event: { k: { t: 'connection' }, type: 'tap' } }, { snapshot: 'open' },
    { tap: { t: 'row', p: { title: 'Price list' } } }, { snapshot: 'staged' }, { tap: { t: 'button', p: { label: 'Apply' } } }, { wait: 200 }]);
  const sheet = find(r.snapshots.open, { t: 'sheet' });
  assert.equal(sheet.p.open, true);
  const rows = (title) => find(sheet, { t: 'row', p: { title } });
  assert.deepEqual(['Connected', 'Hidden by you', 'Blocked', 'No access'].map((x) => rows(x).p.detail), ['3', '1', '2', '0']);
  assert.equal(rows('big').p.detail, 'Change…');
  const sales = find(sheet, { t: 'section', p: { title: 'Sales' } });
  assert.equal(sales.p.badge, '2 of 3');
  assert.equal(find(sales, { t: 'row', p: { title: 'All of Sales' } }).p.icon, 'minus-square', 'a mixed box: some of its tiles');
  assert.equal(rows('Orders').p.icon, 'check-square');
  assert.deepEqual([rows('Price list').p.icon, rows('Price list').p.detail], ['square', 'hidden by you']);
  // blocked stays blocked whatever is ticked: its row is disabled, the org's sentence is said once
  assert.deepEqual([rows('Ledger').p.detail, rows('Ledger').p.disabled, rows('Ledger').p.icon], ['blocked', true, 'lock']);
  const finance = find(sheet, { t: 'section', p: { title: 'Finance' } });
  assert.equal(all(finance, { t: 'notice' }).length, 1);
  assert.match(find(finance, { t: 'notice' }).p.text, /^Finance allows private models only \(pinned by a workspace admin\)\.$/);
  assert.match(rows('Supplier portal').p.subtitle, /by its share apps\/\*/);
  assert.equal(find(sheet, { t: 'text', has: 'not a permission' }).p.text, W.scopeFoot);
  assert.equal(find(sheet, { t: 'button', p: { label: 'Apply' } }).p.disabled, true, 'nothing staged yet');
  assert.equal(find(sheet, { t: 'toolbar' }).p.place, 'bottom', 'Apply and Cancel where the thumb is');
  // a tick is staged…
  const st = find(r.snapshots.staged, { t: 'sheet' });
  assert.equal(find(st, { t: 'row', p: { title: 'Price list' } }).p.icon, 'check-square');
  assert.equal(find(st, { t: 'button', p: { label: 'Apply' } }).p.disabled, false);
  assert.equal(sent(r, 'PATCH', /\/ws\/sessions\/1$/).length, 1, '…and sent only by Apply');
  // every allowed tile ticked is "everything the rules allow": null
  assert.deepEqual(JSON.parse(sent(r, 'PATCH', /\/ws\/sessions\/1$/)[0].body), { scope: null });
  assert.equal(find(r.tree, { t: 'sheet' }), null, 'applied: the panel closes');
});

test('a model change: the what-if first, then the switch; one the conversation\'s data refuses offers a new session', async () => {
  const state = { user: 'ana', mode: 'workspace', delegation: { workspace: true, paused: false }, models: { default: 'hosted/big',
    models: [{ id: 'hosted/big', name: 'big', where: 'hosted' }, { id: 'local/llama', name: 'local-llama', private: true, where: 'your organisation’s own server' }] } };
  const whatIf = { 'local/llama': { backend: { model: 'local/llama', connects: 2, disconnects: 0, allowed: true, counts: { summary: '5 tiles · 3 orgs connected · 1 not connected' } },
    full: { model: { upstream: 'local', model: 'llama', private: true }, connect: [{ tile: 'finance/ledger', name: 'Ledger' }, { tile: 'apps/supplier', name: 'Supplier portal' }], disconnect: [] } } };
  const steps = [{ event: { k: { t: 'connection' }, type: 'model' } }, { tap: { t: 'row', p: { title: 'local-llama' } } }, { wait: 100 }, { snapshot: 'whatif' },
    { tap: { t: 'button', p: { label: 'Switch model' } } }, { wait: 200 }];
  const r = await run(talking({ state, whatIf }), steps);
  const w = find(r.snapshots.whatif, { t: 'sheet' });
  assert.ok(find(w, { t: 'section', p: { title: 'If you switch to llama · private' } }));
  assert.deepEqual([find(w, { t: 'row', p: { title: 'Connects 2 tiles' } }).p.subtitle, !!find(w, { t: 'row', p: { title: 'Disconnects none' } })], ['Ledger, Supplier portal', true]);
  assert.ok(find(w, { t: 'text', p: { text: '5 tiles · 3 orgs connected · 1 not connected' } }));
  assert.equal(sent(r, 'PATCH', /\/ws\/sessions\/1$/).filter((c) => JSON.parse(c.body).model === 'local/llama').length, 1);
  // refused: the conversation holds data the model may not see
  const no = await run(talking({ state, whatIf, taint: ['local/llama'] }), [...steps, { snapshot: 'refused' }, { tap: { t: 'button', p: { label: 'Start a new session on llama' } } }, { wait: 200 }]);
  const x = find(no.snapshots.refused, { t: 'sheet' });
  assert.equal(find(x, { t: 'screen' }).p.title, 'This session can’t move to llama');
  assert.deepEqual(all(x, { t: 'button' }).map((b) => [b.p.label, b.p.role]), [['Start a new session on llama', 'primary'], ['Stay on big', undefined]]);
  assert.deepEqual(JSON.parse(sent(no, 'POST', /\/ws\/sessions$/).pop().body), { from: 1, model: 'local/llama' });
});

test('the context chip: what the app says the view was opened about, sent with the next message only', async () => {
  const about = { type: 'xbin:agent-context', tile: 'sales/orders' };
  const r = await run(talking(), [{ call: ['agent', about] }, { snapshot: 'chip' }, { event: { k: { t: 'composer' }, type: 'send', payload: { value: 'why is it slow?' } } }, { wait: 100 }]);
  assert.deepEqual(find(r.snapshots.chip, { t: 'composer' }).p.context, [{ id: 'about', label: 'About Orders', icon: 'box' }]);
  assert.deepEqual(JSON.parse(sent(r, 'POST', /\/runs\/1\/message$/)[0].body).context, { tile: 'sales/orders' });
  assert.equal(find(r.tree, { t: 'composer' }).p.context, undefined, 'the next message only');
  // a tile this session isn't connected to says so, and its tap opens the scope panel
  const not = await run(talking(), [{ call: ['agent', { type: 'xbin:agent-context', tile: 'finance/ledger' }] }, { snapshot: 'chip' },
    { event: { k: { t: 'composer' }, type: 'context', payload: { id: 'about' } } }, { snapshot: 'scope' }, { event: { k: { t: 'sheet' }, type: 'dismiss' } },
    { event: { k: { t: 'composer' }, type: 'remove-context', payload: { id: 'about' } } }]);
  assert.deepEqual(find(not.snapshots.chip, { t: 'composer' }).p.context, [{ id: 'about', label: 'About Ledger — not connected', icon: 'box', tone: 'warn' }]);
  assert.ok(find(not.snapshots.scope, { t: 'sheet' }));
  assert.equal(find(not.tree, { t: 'composer' }).p.context, undefined, 'one tap removes it');
  // the address the app opens the view with says the same
  const linked = await run(talking(), [], { data: { hash: 'about=people/calendar' } });
  assert.equal(find(linked.tree, { t: 'composer' }).p.context[0].label, 'About Team calendar');
  // anything else from the chrome is not a context
  const junk = await run(talking(), [{ call: ['agent', { type: 'xbin:agent-open-request', id: 'rq-12' }] }, { call: ['agent', { type: 'xbin:agent-context', tile: '../etc' }] }]);
  assert.equal(find(junk.tree, { t: 'composer' }).p.context, undefined);
});

// a brief that waits, a redirect that waits
const briefSeed = () => {
  const cards = fresh(CARDS);
  cards.briefs[0].state = 'draft';
  cards.requests = []; cards.agents = []; cards.ask = null;
  const view = { messages: [msg(1, 'user', 'Add a CSV export to Orders.'), msg(2, 'assistant', '', { toolCalls: [call('b1', 'tile_agent_brief', { tile: 'sales/orders' })] }),
    msg(3, 'tool', 'Brief 4 is in front of your person.', { toolCallId: 'b1', name: 'tile_agent_brief' })] };
  return seedOf({ cards: { 1: cards }, views: { 1: view } });
};

test('the brief: editable, and its one button files the start and asks the app to show it — never an Allow', async () => {
  const r = await run(briefSeed(), [{ snapshot: 'brief' }, { tap: { t: 'button', p: { label: 'Edit' } } }, { snapshot: 'editing' },
    { input: [{ t: 'field', p: { label: 'Goal' } }, 'Export as CSV and XLSX'] }, { tap: { t: 'button', p: { label: 'Save' } } }, { wait: 100 },
    { tap: { t: 'button', p: { label: 'Review and start…' } } }, { wait: 100 }]);
  const card = find(r.snapshots.brief, { t: 'card', p: { title: 'Brief for Orders’ agent' } });
  assert.deepEqual(all(card, { t: 'button' }).map((b) => [b.p.label, b.p.role]), [['Review and start…', 'primary'], ['Edit', 'plain']]);
  assert.match(card.p.note, /the tile agent asks you before it acts/);
  assert.ok(find(card, { t: 'text', p: { text: 'Claude Code · asks before acting' } }), 'what it will run on, from xbind');
  assert.deepEqual(all(find(r.snapshots.editing, { t: 'card' }), { t: 'field' }).map((f) => [f.p.label, f.p.kind]), [['Goal', 'multiline'], ['Done when', 'multiline'], ['Leave alone', 'multiline'], ['Check by', 'multiline']]);
  assert.equal(JSON.parse(sent(r, 'PUT', /\/briefs\/4$/)[0].body).goal, 'Export as CSV and XLSX');
  assert.equal(sent(r, 'POST', /\/briefs\/4\/start$/).length, 1);
  assert.deepEqual(r.asked, [{ type: 'xbin:agent-open-request', id: 'rq-brief-4' }], 'the app opens xbind\'s request by its id');
  assert.equal(find(r.snapshots.brief, { t: 'composer' }).p.placeholder, W.placeholderBrief);
});

test('what the view asks of the app\'s chrome: its panels, a redirect\'s hand-off — and says so where the app has none', async () => {
  const cards = { requests: [], agents: [], briefs: [], plan: [], refusals: [], ask: { callId: 'a1', kind: 'redirect', question: 'That is a change to code.', tile: 'sales/orders' } };
  const view = { messages: [msg(1, 'user', 'change line 40 in export.go'), msg(2, 'assistant', '', { toolCalls: [call('a1', 'ask_user', { question: 'That is a change to code.', kind: 'redirect', tile: 'sales/orders' })] }),
    msg(3, 'tool', '(asked the user; awaiting their reply)', { toolCallId: 'a1', name: 'ask_user' })] };
  const seed = () => seedOf({ cards: { 1: fresh(cards) }, views: { 1: fresh(view) } });
  const steps = [{ tap: { t: 'button', p: { label: 'Approvals' } } }, { tap: { t: 'button', p: { label: 'What my agent did' } } }, { tap: { t: 'button', p: { label: W.needsYou } } },
    { tap: { t: 'button', p: { label: 'Hand this to Orders’ own agent' } } }];
  const r = await run(seed(), steps);
  assert.deepEqual(r.asked, [{ type: 'xbin:agent-open', view: 'approvals' }, { type: 'xbin:agent-open', view: 'activity' }, { type: 'xbin:agent-open', view: 'needs' },
    { type: 'xbin:open-session', session: 'new', tile: 'sales/orders', draft: 'change line 40 in export.go' }]);
  // the views the chrome takes (web/agent-chrome-words.js PAGE_VIEWS): never Preferences — those are the app's own
  assert.deepEqual([...VIEWS].sort(), [...PAGE_VIEWS].sort());
  assert.equal(openView('prefs'), null);
  const card = find(r.tree, { t: 'card', has: 'doesn’t open or edit code' });
  assert.deepEqual(all(card, { t: 'button' }).map((b) => b.p.label), ['Hand this to Orders’ own agent', 'Tell me the outcome instead']);
  // the ⋯ menu never pauses or resumes: those are the app's
  const menu = labels(r.tree, { t: 'button', in: { t: 'menu' } });
  assert.deepEqual(menu, ['Sessions', 'New session…', 'Scope…', 'Notebook', W.whatCanItDo, W.needsYou, 'Approvals', 'What my agent did']);
  assert.ok(!json(r.tree).includes('Preferences'), 'Preferences live in the app\'s own shield menu');
  // an app that doesn't show the tile as an agent has no chrome to ask: nothing is sent, and the view says why
  const no = await run(seed(), [steps[0]], { caps: app('compact', false) });
  assert.deepEqual(no.asked, []);
  assert.ok(find(no.tree, { t: 'notice', p: { text: W.needsApp } }));
});

test('sessions: Main first, the others with their scope and status; New session…, a switch, Older folded', async () => {
  const r = await run(talking(), [{ tap: { t: 'button', p: { label: 'Sessions' } } }, { snapshot: 'list' }, { tap: { t: 'row', p: { title: 'Month-end' } } }, { wait: 200 }, { snapshot: 'switched' },
    { tap: { t: 'button', p: { label: 'New session…' }, in: { t: 'menu' } } }, { wait: 200 }]);
  const sheet = find(r.snapshots.list, { t: 'sheet' });
  const rows = all(find(sheet, { t: 'section' }), { t: 'row' });
  assert.deepEqual(rows.map((x) => x.p.title), ['Main', 'Month-end']);
  assert.deepEqual(all(find(sheet, { t: 'section', p: { title: 'Older (1)' } }), { t: 'row' }).map((x) => x.p.title), ['Leave tracker idea']);
  assert.deepEqual([rows[0].p.subtitle, rows[0].p.selected, rows[0].p.badge], ['waiting for you · 3 tiles · 2 orgs · big', true, 'always here']);
  assert.deepEqual(all(rows[0], { t: 'button', in: { t: 'actions' } }).map((b) => b.p.label), ['Rename'], 'Main is renamed, never archived');
  assert.deepEqual(all(rows[1], { t: 'button', in: { t: 'actions' } }).map((b) => b.p.label), ['Rename', 'Archive']);
  assert.deepEqual([find(sheet, { t: 'section', p: { title: 'Older (1)' } }).p.collapsed, find(sheet, { t: 'text', has: 'starts empty' }).p.text], [true, W.sessionsFoot]);
  assert.equal(find(r.snapshots.switched, { t: 'screen' }).p.title, 'Month-end');
  assert.equal(find(r.snapshots.switched, { t: 'sheet' }), null);
  assert.deepEqual(JSON.parse(sent(r, 'POST', /\/ws\/sessions$/).pop().body), { from: 2 }, 'a new session copies the scope and model of the one in view');
});

test('the notebook: every page listed, one a session may not read closed with the reason; read, edit, add', async () => {
  const pages = [{ slug: 'preferences', title: 'How Ana likes things done', summary: 'Reports in bullet points', body: '- bullets\n- no email without asking', version: 2, updatedBy: 'agent', updatedMs: NOW - 86400e3, orgs: [] },
    { slug: 'ledger', title: 'Ledger notes', version: 1, orgs: ['Finance'], locked: true, lockedWhy: 'Not available in this session: Finance isn’t connected.' }];
  const r = await run(talking({ pages }), [{ tap: { t: 'button', p: { label: 'Notebook' } } }, { wait: 100 }, { snapshot: 'list' },
    { tap: { t: 'row', p: { title: 'How Ana likes things done' } } }, { wait: 100 }, { snapshot: 'page' }, { tap: { t: 'button', p: { label: 'Edit' } } },
    { input: [{ t: 'field', p: { label: 'Text' } }, '- bullets only'] }, { tap: { t: 'button', p: { label: 'Save' } } }, { wait: 100 },
    { event: { k: { t: 'nav' }, type: 'pop', payload: { depth: 1 } } }, { wait: 100 }]);
  const list = find(r.snapshots.list, { t: 'nav' }).c[1];
  assert.equal(list.p.title, 'Notebook');
  const [mine, closed] = all(list, { t: 'row' });
  assert.deepEqual([mine.p.badge, mine.p.nav, closed.p.badge, closed.p.disabled, closed.p.icon, closed.p.subtitle], ['yours', true, 'Finance', true, 'lock', 'Not available in this session: Finance isn’t connected.']);
  assert.equal(find(list, { t: 'text', has: 'forget' }).p.text, W.notebookFoot);
  const page = find(r.snapshots.page, { t: 'nav' }).c[2];
  assert.equal(page.p.title, 'How Ana likes things done');
  assert.ok(find(page, { t: 'markdown' }));
  assert.deepEqual(find(page, { t: 'button', p: { label: 'Delete' } }).p.confirm, { title: 'Delete this page?', message: 'The agent forgets it; its history is kept.', label: 'Delete', destructive: true });
  assert.deepEqual(JSON.parse(sent(r, 'PUT', /\/ws\/notebook\/preferences$/)[0].body), { title: 'How Ana likes things done', body: '- bullets only', base: 2 });
  assert.equal(find(r.tree, { t: 'nav' }).c.length, 1, 'back to the conversation');
});

test('"What can it do?" shows the lists again; a starter there fills the composer and closes it', async () => {
  const r = await run(talking(), [{ tap: { t: 'button', p: { label: W.whatCanItDo }, in: { t: 'menu' } } }, { wait: 100 }, { snapshot: 'can' },
    { tap: { t: 'row', p: { title: 'What does Orders do, and who can use it?' } } }]);
  const sheet = find(r.snapshots.can, { t: 'sheet' });
  assert.equal(all(find(sheet, { t: 'section', p: { title: 'It can’t' } }), { t: 'row' }).length, 4);
  assert.equal(find(r.tree, { t: 'sheet' }), null);
  assert.equal(find(r.tree, { t: 'composer' }).p.value, 'What does Orders do, and who can use it?');
});

test('an app from before these nodes still gets a native view: every addition has a fallback it can draw', async () => {
  const r = await run(talking(), [], { caps: BEFORE });
  const t = r.tree;
  for (const prim of ['card', 'connection', 'request', 'session']) assert.equal(find(t, { t: prim }), null, `no ${prim}`);
  // a request it can't review says so; a decided one is words; a tile agent is named
  assert.deepEqual(find(t, { t: 'notice', p: { title: W.updateToReview } }).p.text, 'Rebuild the order links on the 38 open invoices');
  assert.ok(find(t, { t: 'markdown', has: 'Its start: allowed once by you' }), 'what came of the brief\'s start, on its card');
  assert.ok(find(t, { t: 'notice', has: 'Orders: Claude Code — working' }));
  assert.ok(find(t, { t: 'notice', has: 'Invoices: Claude Code — waiting for you' }));
  // the plan's tags ride in its lines; the question is the rev-1 form; cards are words
  assert.equal(find(t, { t: 'plan' }).p.entries[3].text, '4. Check both and report back — reads only');
  assert.equal(find(t, { t: 'plan' }).p.tags, undefined);
  assert.deepEqual([find(t, { t: 'question' }).p.style, find(t, { t: 'question' }).p.schema.properties.answer.title], [undefined, 'Choose']);
  assert.ok(find(t, { t: 'markdown', has: 'Ledger is blocked by a rule' }));
  // the connection line is a badge in the bar; replies and the cards' buttons are the composer's chips
  assert.equal(find(t, { t: 'badge', in: { t: 'toolbar' } }).p.text, '3 tiles · 2 orgs connected · 3 not connected · web');
  const c = find(t, { t: 'composer' });
  assert.deepEqual([c.p.replies, c.p.context, c.p.hint], [undefined, undefined, undefined]);
  const chips = all(c, { t: 'button' }).map((b) => b.p.label);
  for (const x of ['Everyone with an open order', 'Change the model…', 'Open scope']) assert.ok(chips.includes(x), `${x} is a chip`);
  // the empty state without the slot: its words, the starters as chips
  const e = await run(seedOf(), [], { caps: BEFORE });
  assert.ok(find(e.tree, { t: 'text', p: { text: 'Ask for an outcome' } }));
  assert.ok(find(e.tree, { t: 'markdown', has: 'It can' }));
  assert.ok(all(find(e.tree, { t: 'composer' }), { t: 'button' }).map((b) => b.p.label).includes('Add ___ to Invoices'));
});

test('xbind\'s own events read the cards again: a request decided elsewhere settles here', async () => {
  const cards = fresh(CARDS);
  const decided = fresh(CARDS);
  decided.requests[0] = { ...decided.requests[0], state: 'denied', by: 'person', decidedMs: NOW };
  const r = await run(talking({ cards: { 1: cards } }), [{ call: ['seed', { cards: { 1: decided } }] }, { call: ['event', { type: 'delegate', data: { op: 'request', id: 'rq-12' } }] }, { wait: 400 }]);
  assert.equal(sent(r, 'GET', /\/ws\/sessions\/1\/cards$/).length, 2, 'read again');
  assert.deepEqual(r.messages.filter((m) => m.op === 'meta').pop().badge, '2', 'one thing less waits');
});

test('a normal instance is asked nothing and loads the template\'s own view', async () => {
  const r = await runNative({ entry: TPL + 'native.js', data: { now: NOW, self: 'apps/agent', setup: TPL + 'test/native-stub.mjs', seed: {} } });
  assert.equal(r.fatal, null);
  assert.deepEqual(r.errors, []);
  const calls = (r.extra && r.extra.calls) || [];
  assert.equal(calls.filter((c) => /\/ws\//.test(c.url)).length, 0, 'no /ws/ route is asked of a tile');
  assert.ok(find(r.tree, { t: 'button', p: { label: 'New chat' } }));
});

// ---- the agent's switch, a held session, the notebook's marks, xbin's refusals, a call no card replaces --------------

const NO_AGENT = 'You haven’t been given a workspace agent. An admin chooses who has one: Admin → People → access, or the workspace agent’s set-up in the dock.';
const HELD_AGENT = 'Your agent is held on this xbin, and does nothing until that is cleared. An admin can see why and clear it: Admin → Runtime, or the workspace agent’s dock.';
const OFF_SEND = `${NO_AGENT} Nothing was sent to the agent or its model. Your message is still in the message box: send it again once it runs.`;
const offSeed = (over = {}) => talking({ offAnswer: { error: OFF_SEND, code: 'agent-off', refusal: 'no-agent', kept: true }, ...over });
const kids = (tree) => find(tree, { t: 'screen' }).c || [];

test('a switched-off agent: the refused message stays in the box, unsent, the reason at the composer, the switch\'s line in the transcript', async () => {
  const words = 'Which invoices are overdue?';
  // the app reports the send alone (no keystrokes before it): the box is controlled, so what stays is the tile's value
  const r = await run(offSeed(), [{ event: { k: { t: 'composer' }, type: 'send', payload: { value: words } } }, { wait: 300 }]);
  assert.equal(sent(r, 'POST', /\/runs\/1\/message$/).length, 1);
  const c = find(r.tree, { t: 'composer' });
  assert.equal(c.p.value, words, 'the words are still the composer\'s value after @send');
  assert.equal(c.p.disabled, false, 'and can be sent again');
  // the backend's sentence right at the composer, as a warning
  const at = kids(r.tree);
  const i = at.findIndex((n) => n.t === 'composer');
  assert.deepEqual([at[i - 1].t, at[i - 1].p.tone, at[i - 1].p.text], ['notice', 'warn', OFF_SEND]);
  // the step of kind off: which switch, who changes it and where — drawn, warn, with the pause glyph
  const step = find(r.tree, { t: 'step', in: { t: 'transcript' }, p: { text: OFF_SEND } });
  assert.deepEqual([step.p.glyph, step.p.tone], ['‖', 'warn']);
  assert.ok(sent(r, 'GET', /\/ws\/state$/).length >= 2 && sent(r, 'GET', /\/ws\/empty/).length >= 2, 'the state and the empty state read again');
  // typed first, then sent: the same
  const typed = await run(offSeed(), [{ input: [{ t: 'composer' }, words] }, { event: { k: { t: 'composer' }, type: 'send', payload: { value: words } } }, { wait: 300 }]);
  assert.equal(find(typed.tree, { t: 'composer' }).p.value, words);
  // a message the backend took clears the box
  const ok = await run(talking(), [{ event: { k: { t: 'composer' }, type: 'send', payload: { value: words } } }, { wait: 300 }]);
  assert.equal(find(ok.tree, { t: 'composer' }).p.value, '');
});

test('the empty state of an agent that can\'t act: the switch\'s sentence, nothing it could do, no starters', async () => {
  const empty = { ...fresh(EMPTY), off: 'no-agent', line: NO_AGENT, can: [], starters: [] };
  const r = await run(seedOf({ empty }));
  assert.deepEqual(find(r.tree, { t: 'transcript' }).p.empty, { title: 'Ask for an outcome', text: NO_AGENT });
  assert.equal(find(r.tree, { t: 'notice', p: { title: W.offTitle['no-agent'] } }), null, 'the empty state says it: no banner as well');
  const old = await run(seedOf({ empty }), [], { caps: BEFORE });
  assert.ok(find(old.tree, { t: 'text', p: { text: NO_AGENT } }));
  assert.equal(find(old.tree, { t: 'markdown', has: 'It can' }), null);
  assert.deepEqual(all(find(old.tree, { t: 'composer' }), { t: 'button' }), [], 'no starters as chips');
  // "What can it do?" says the same
  const can = await run(seedOf({ empty }), [{ tap: { t: 'button', p: { label: W.whatCanItDo }, in: { t: 'menu' } } }, { wait: 100 }]);
  const sheet = find(can.tree, { t: 'sheet' });
  assert.equal(find(sheet, { t: 'section' }).p.footer, NO_AGENT);
  assert.equal(find(sheet, { t: 'row' }), null);
  // a conversation with rows: the switch as a banner, in the backend's words — no agent given, or the agent held here
  for (const [off, line] of [['no-agent', NO_AGENT], ['held', HELD_AGENT]]) {
    const t = await run(talking({ empty: { ...fresh(EMPTY), off, line, can: [], starters: [] } }));
    assert.equal(find(t.tree, { t: 'notice', p: { title: W.offTitle[off] } }).p.text, line, off);
  }
});

test('a held session: why, its two ways forward, and the list marks it', async () => {
  const held = () => { const s = fresh(SESSIONS); s[0].held = { code: 'model_taint', model: 'hosted/big', orgs: ['Sales'], reason: 'Sales allows private models only', offer: { model: 'hosted/big-1', newSession: { model: 'hosted/big' } } }; return s; };
  const r = await run(talking({ sessions: held() }), [{ snapshot: 'held' }, { tap: { t: 'button', p: { label: 'Move back to big-1' } } }, { wait: 200 }]);
  const n = find(r.snapshots.held, { t: 'notice', p: { title: 'This session can’t go on on big.' } });
  assert.equal(n.p.tone, 'danger');
  assert.equal(n.p.text, 'It holds data from Sales, and Sales allows private models only. Nothing is read, changed or sent to a model in it until it moves.');
  const at = kids(r.snapshots.held);
  const row = at[at.indexOf(at.find((x) => x.p && x.p.title === 'This session can’t go on on big.')) + 1];
  assert.deepEqual([row.t, ...all(row, { t: 'button' }).map((b) => [b.p.label, b.p.role])], ['stack', ['Move back to big-1', 'primary'], ['New session on big', 'plain']]);
  assert.deepEqual(JSON.parse(sent(r, 'PATCH', /\/ws\/sessions\/1$/)[0].body), { model: 'hosted/big-1' });
  assert.equal(find(r.tree, { t: 'notice', has: 'can’t go on' }), null, 'moved: the banner goes');
  const fresh2 = await run(talking({ sessions: held() }), [{ tap: { t: 'button', p: { label: 'New session on big' } } }, { wait: 200 }]);
  assert.deepEqual(JSON.parse(sent(fresh2, 'POST', /\/ws\/sessions$/).pop().body), { from: 1, model: 'hosted/big' });
  // a move xbin refuses: its words in the banner
  const no = await run(talking({ sessions: held(), taint: ['hosted/big-1'] }), [{ tap: { t: 'button', p: { label: 'Move back to big-1' } } }, { wait: 200 }]);
  assert.match(find(no.tree, { t: 'notice', p: { title: 'This session can’t go on on big.' } }).p.text, /until it moves\. refused$/);
  // the sessions list marks it
  const list = await run(talking({ sessions: held() }), [{ tap: { t: 'button', p: { label: 'Sessions' } } }]);
  const main = find(find(list.tree, { t: 'sheet' }), { t: 'row', p: { title: 'Main' } });
  assert.deepEqual([main.p.subtitle.split(' · ')[0], main.p.icon, main.p.tone], ['held: can’t go on on big', 'lock', 'danger']);
});

test('the notebook: a marked page says what its marks mean; nothing clears them', async () => {
  const pages = [{ slug: 'q3', title: 'Q3 numbers', body: 'From the ledger', version: 1, orgs: ['Finance'], updatedBy: 'agent', updatedMs: NOW }];
  const r = await run(talking({ pages }), [{ tap: { t: 'button', p: { label: 'Notebook' } } }, { wait: 100 }, { tap: { t: 'row', p: { title: 'Q3 numbers' } } }, { wait: 100 }]);
  const page = find(r.tree, { t: 'nav' }).c[2];
  assert.ok(find(page, { t: 'text', p: { text: W.marksNote(['Finance']) } }));
  assert.deepEqual(all(page, { t: 'button', in: { t: 'menu' } }).map((b) => b.p.label), ['Delete']);
  assert.ok(!json(r.tree).includes('Clear'), 'no way to clear the marks');
});

// xbin's refusals of the review's round: each card in refusalCard's own words (the web's module)
const REFUSALS = [
  { id: 1, callId: 'r1', tool: 'tile_agent_message', kind: 'brief-taint', reason: 'Sales allows private models only', tile: 'sales/orders' },
  { id: 2, callId: 'r2', tool: 'tile_agent_message', kind: 'mode', reason: 'Ana switched it to a mode that acts without asking', tile: 'sales/orders' },
  { id: 3, callId: 'r3', tool: 'tile_agent_message', kind: 'command', reason: '', tile: 'sales/orders' },
  { id: 4, callId: 'r4', tool: 'workspace_map', kind: 'no-agent', reason: '', tile: '' },
  { id: 5, callId: 'r5', tool: 'workspace_map', kind: 'held', reason: '', tile: '' },
];
const refusedSeed = () => {
  const cards = { requests: [], agents: [], briefs: [], plan: [], refusals: fresh(REFUSALS).reverse() };
  const view = { messages: [msg(1, 'user', 'Tell the Orders agent to go on.'), msg(2, 'assistant', '', { toolCalls: REFUSALS.map((f) => call(f.callId, f.tool, { agent: '3', text: 'go on' })) }),
    ...REFUSALS.map((f, i) => msg(3 + i, 'tool', `Refused: ${f.kind}`, { toolCallId: f.callId, name: f.tool }))] };
  return seedOf({ cards: { 1: cards }, views: { 1: view } });
};
const nameOf = (p) => (FULL.tiles.find((t) => t.tile === p) || {}).name || p;

test('xbin\'s refusals: brief-taint, mode and command as cards in refusalCard\'s words; no-agent and held a quiet line', async () => {
  const r = await run(refusedSeed());
  for (const f of REFUSALS) {
    const w = refusalCard(f, nameOf);
    if (w.quiet) {
      assert.deepEqual(find(r.tree, { t: 'step', p: { text: w.text } }).p.tone, 'muted', f.kind);
      assert.equal(find(r.tree, { t: 'card', has: w.text }), null, `${f.kind}: no card`);
      continue;
    }
    const card = find(r.tree, { t: 'card', p: { title: w.title } });
    assert.ok(card, f.kind);
    assert.equal(card.p.note, w.note || undefined, `${f.kind}: its note`);
    assert.deepEqual(all(card, { t: 'text' }).map((x) => x.p.text), [w.text], `${f.kind}: its text`);
    assert.deepEqual(all(card, { t: 'button' }), [], `${f.kind}: no way forward the page could offer`);
  }
  assert.equal(find(r.tree, { t: 'card', p: { title: 'Orders’ agent can’t be told this' } }).p.title, refusalCard(REFUSALS[0], nameOf).title);
  // an app without cards: the same words as markdown
  const old = await run(refusedSeed(), [], { caps: BEFORE });
  for (const f of REFUSALS.slice(0, 3)) {
    const w = refusalCard(f, nameOf);
    const md = find(old.tree, { t: 'markdown', has: w.title });
    assert.ok(md && (!w.note || json(md).includes(w.note)) && json(md).includes(w.text), `${f.kind}: ${json(md)}`);
  }
});

test('a brief whose mode xbin hasn\'t named: it says so, and nothing says "asks before acting"', async () => {
  const seed = briefSeed();
  seed.startDefaults = { harness: 'claude', harnessName: 'Claude Code', ticket: true, line: 'Claude Code', defaults: { harness: 'claude', harnesses: [{ id: 'claude', name: 'Claude Code', needsTicket: true }] } };
  const r = await run(seed, [{ wait: 100 }]);
  const card = find(r.tree, { t: 'card', p: { title: 'Brief for Orders’ agent' } });
  assert.equal(card.p.note, 'This is what the tile agent is told first. What your agent sends it later isn’t approved message by message. Whether it asks you before it acts shows once xbin says what it runs on.');
  assert.ok(!/asks? (you )?before (it )?act/.test(json(r.tree).replace('Whether it asks you before it acts', '')), 'no promise that it asks');
});

test('a tool call no card replaces is the kit\'s tool card — its arguments and result folded, never raw JSON in the conversation', async () => {
  const MAP = 'This session: 3 tiles · 2 orgs connected · 3 not connected (model hosted/big).\nTiles (3):\n- sales/orders — Orders';
  const view = { messages: [msg(1, 'user', 'What tiles do I have?'), msg(2, 'assistant', '', { toolCalls: [call('m1', 'workspace_map', { filter: 'sales' })] }),
    msg(3, 'tool', MAP, { toolCallId: 'm1', name: 'workspace_map' }), msg(4, 'assistant', 'You have 3 tiles connected in this session: Orders, Invoices and Team calendar.')] };
  const r = await run(seedOf({ cards: { 1: { requests: [], agents: [], briefs: [], plan: [], refusals: [] } }, views: { 1: view } }));
  const tc = find(r.tree, { t: 'toolcard', in: { t: 'transcript' } });
  assert.ok(tc && tc.p.title, 'a tool card with its headline');
  assert.deepEqual([tc.p.state, tc.p.open], ['ok', false]);
  assert.ok(find(tc, { t: 'text', p: { text: 'workspace_map' } }), 'its name inside');
  assert.ok(find(tc, { t: 'text', p: { text: 'filter: sales' } }), 'its arguments inside, as key: value');
  assert.equal(find(tc, { t: 'code' }).p.text, MAP, 'its result inside');
  // outside the card: the person's words and the answer — no arguments, no result
  const rows = find(r.tree, { t: 'transcript' }).c.filter((x) => x.t !== 'toolcard');
  assert.ok(!JSON.stringify(rows).includes('"filter"') && !JSON.stringify(rows).includes('This session:'), JSON.stringify(rows));
  assert.deepEqual(all(r.tree.root, { t: 'message' }).map((m) => m.p.text), ['What tiles do I have?', 'You have 3 tiles connected in this session: Orders, Invoices and Team calendar.']);
});

test('an app from before these nodes draws every state of this round without an unsupported node', async () => {
  const held = fresh(SESSIONS);
  held[0].held = { code: 'model_taint', model: 'hosted/big', orgs: ['Sales'], reason: 'Sales allows private models only', offer: { newSession: { model: 'hosted/big' } } };
  const old = app('compact', false);
  for (const p of ['card', 'connection', 'request', 'session']) delete old.prims[p];
  Object.assign(old.prims, { transcript: 1, question: 1, plan: 1, composer: 1, toolbar: 1, menu: 1 });
  for (const [name, seed, steps] of [
    ['off', offSeed(), [{ event: { k: { t: 'composer' }, type: 'send', payload: { value: 'hi' } } }, { wait: 300 }]],
    ['held', talking({ sessions: held }), []],
    ['refusals', refusedSeed(), []],
    ['empty off', seedOf({ empty: { ...fresh(EMPTY), off: 'held', line: HELD_AGENT, can: [], starters: [] } }), []],
  ]) {
    for (const caps of [BEFORE, old]) {
      const r = await run(seed, steps, { caps }); // run() fails on any error or warning diagnostic (an unsupported node is one)
      assert.ok(find(r.tree, { t: 'screen' }), name);
    }
  }
});
