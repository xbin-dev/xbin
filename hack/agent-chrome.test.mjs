// hack/agent-chrome.test.mjs — the words and rules of xbin's chrome for the
// workspace agent (web/agent-chrome-words.js, D232), run by `make js-test`:
// what a request, a change set, the audit log and the dock's own states say,
// which choices allow what, and what the dock takes from the agent's page.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as W from '../web/agent-chrome-words.js';

const NOW = Date.parse('2026-11-16T10:16:00Z');
const call = (x = {}) => ({ id: 'r1', kind: 'call', method: 'POST', target: '/api/apps/invoices/links/rebuild', tile: 'apps/invoices', body: '{"all":true}', bodySize: 12, bodyShown: 12,
  summary: 'Rebuild the links — it is safe', state: 'pending', created: '2026-11-16T10:14:00Z', expires: '2026-11-17T10:14:00Z',
  preview: { class: 'write', tile: 'apps/invoices', level: 'write', standing: { call: 'POST to /links/rebuild on Invoices, whatever it sends, without asking again.', tile: 'Any write to Invoices.' } }, ...x });

test('a tile call: its heading, what it sends, the three choices and what each approves', () => {
  const r = call();
  assert.deepEqual(W.heading(r), { before: 'Your agent wants to change something in ', strong: 'Invoices', after: '' });
  assert.equal(W.needTitle(r), 'Approve a change to Invoices');
  assert.deepEqual(W.sends(r), { text: '{"all":true}', binary: false, note: '12 bytes · shown in full' });
  assert.equal(W.sends(call({ bodySize: 14336, bodyShown: 2048 })).note, 'showing the first 2 KiB of 14 KiB');
  assert.equal(W.levelWords('write'), 'you can write here');
  assert.equal(W.undoWords(r), "xbin can't undo this. Whether Invoices can is up to Invoices.");
  assert.equal(W.asked(r, NOW), `asked ${W.clock(Date.parse(r.created))} · expires in 23 h 58 min`);
  const ch = W.grantChoices(r);
  assert.deepEqual(ch.map((c) => [c.id, c.wider]), [['once', false], ['call', false], ['tile', true]]);
  assert.equal(ch[1].text, r.preview.standing.call, "xbind's own sentence for the scope");
  assert.equal(ch[2].label, 'Always, every change to Invoices');
  assert.deepEqual([W.allowLabel(r, 'once'), W.allowLabel(r, 'call'), W.allowLabel(r, 'tile')], ['Allow once', 'Allow always', 'Allow every change to Invoices']);
  assert.equal(W.allowSpoken(r, 'call'), 'Allow always, this exact action');
  assert.deepEqual(W.approveBody(r, 'once'), { grant: 'once' });
  assert.deepEqual(W.approveBody(r, 'call'), { grant: 'always', scope: 'call' }, 'the exact action: until taken back by default');
  assert.deepEqual(W.approveBody(r, 'tile'), { grant: 'hour', scope: 'tile' }, 'the whole tile: an hour by default');
  assert.deepEqual(W.approveBody(r, 'tile', 'always'), { grant: 'always', scope: 'tile' });
  assert.equal(W.widerWords(r).heading, 'Allow every change to Invoices?');
  assert.equal(W.widerWords(r).back, 'Go back');
});

test('a body nobody can read says so, and is approved only as unseen', () => {
  const r = call({ body: undefined, bodyBinary: true, bodySize: 3355443, bodySha256: 'ab12', preview: { tile: 'apps/invoices', body: { binary: true, size: 3355443, sha256: 'ab12' } } });
  assert.deepEqual(W.sends(r), { text: '', binary: true, note: 'not text — 3.2 MiB, sha256 ab12' });
  assert.deepEqual(W.approveBody(r, 'once'), { grant: 'once', unseen: true });
});

test('a tile agent start: the mode leads, and one that never asks is the danger wording', () => {
  const s = (session) => ({ id: 's1', kind: 'session', method: 'POST', target: '/api/xbin/term/sessions', body: JSON.stringify({ prompt: 'Fix the export' }), state: 'pending',
    preview: { session: { tile: 'apps/invoices', providerName: 'Claude Code', model: 'sonnet', mode: 'default', asks: true, net: 'none', ...session } } });
  const asks = W.sessionRows(s({}));
  assert.deepEqual([asks.mode.tag, asks.mode.tone, asks.danger], ['asks before acting', 'ok', '']);
  assert.equal(asks.agent, "Claude Code · model sonnet, through the workspace's connector");
  assert.equal(asks.network, 'Its model only. No internet.');
  assert.equal(asks.brief, 'Fix the export');
  assert.deepEqual(W.heading(s({})), { before: 'Start a tile agent in ', strong: 'Invoices', after: '?' });
  assert.equal(W.heading(s({ resume: 'abc' })).before, 'Continue the tile agent in ');
  assert.equal(W.allowLabel(s({})), 'Start');
  const never = s({ asks: false, mode: 'bypassPermissions' });
  assert.equal(W.sessionRows(never).mode.tag, 'acts without asking');
  assert.match(W.sessionRows(never).danger, /a mode that never asks/);
  assert.equal(W.allowLabel(never), 'Start without asking');
  assert.deepEqual(W.grantChoices(never), [], 'asked every time: no standing choice');
  assert.deepEqual(W.approveBody(never, 'tile', 'always'), { grant: 'once' });
});

const set = (items, x = {}) => ({ id: 'c1', title: 'Wire up the export', state: 'proposed', created: '2026-11-16T10:00:00Z', expires: '2026-11-23T10:00:00Z', items: items.map((it, i) => ({ n: i + 1, ...it })), ...x });

test('a change set: a warned item starts unticked, a blocked one cannot be ticked, the button counts', () => {
  const cs = set([{ kind: 'grant.add', line: 'Give Invoices read access to Orders' }, { kind: 'binding.set', line: 'Publish the export', warnings: ['PUBLIC: anyone on the internet can reach it'] },
    { kind: 'limits.set', line: 'Raise the memory', blocked: "It is above Finance's ceiling." }]);
  assert.deepEqual(W.initialTicks(cs), [1]);
  assert.equal(W.tickable(cs.items[2]), false);
  assert.deepEqual(W.warning(cs.items[1].warnings[0]), { tag: 'PUBLIC', icon: 'globe', text: 'anyone on the internet can reach it' });
  assert.equal(W.warning('OPEN PORT: 0.0.0.0:8080').tag, 'OPEN PORT');
  assert.equal(W.kindTag('org.share'), 'org share');
  const w = W.reviewWords(cs, [1]);
  assert.equal(w.heading, 'Your agent proposes 3 admin changes');
  assert.equal(w.apply, 'Apply 1 change…');
  assert.equal(W.reviewWords(cs, []).apply, 'Apply none');
  assert.match(w.footer, /^1 of 3 ticked · applied in order as you/);
  assert.deepEqual(W.needChangeTitle(cs), { title: '3 admin changes proposed', note: "1 has a warning · 1 can't be applied" });
});

test('results: all, some, none — and what can be undone', () => {
  const partial = set([{ kind: 'grant.add', result: 'applied', inverse: {} }, { kind: 'grant.add', result: 'applied', inverse: {} }, { kind: 'binding.set', result: 'failed', error: 'no such slot' },
    { kind: 'limits.set', result: 'skipped' }, { kind: 'org.share', result: 'rejected' }], { state: 'partial' });
  assert.deepEqual(W.resultsWords(partial), { title: '2 of 4 changes applied', banner: 'One change failed, so the one after it was skipped. The two that were applied stay.', undo: 'Undo the 2 applied…' });
  assert.equal(W.resultOf(partial.items[2]).text, 'xbin said: “no such slot”');
  assert.equal(W.resultOf({ result: 'stale' }).word, 'out of date');
  assert.equal(W.resultOf({ result: 'rejected' }).word, 'left out by you');
  assert.equal(W.resultsWords(set([{ result: 'applied' }, { result: 'applied' }, { result: 'applied' }])).title, 'All 3 changes applied');
  assert.deepEqual(W.resultsWords(set([{ result: 'stale' }])), { title: 'Nothing was applied', banner: '', undo: '' });
  assert.equal(W.stepUp('password').heading, "Confirm it's you");
  assert.equal(W.stepUp('signin').action, 'Sign in again');
});

test('needs you: oldest first, only what waits', () => {
  const rows = W.needs({
    requests: [call({ id: 'r2', created: '2026-11-16T10:15:00Z' }), call({ id: 'r1' }), call({ id: 'r0', state: 'approved' })],
    changesets: [set([{ kind: 'grant.add' }], { created: '2026-11-16T10:14:30Z' }), set([{}], { id: 'c0', state: 'applied' })],
    sessions: [{ id: 't1', cwd: 'apps/orders', providerName: 'Claude Code', pending: 1, origin: 'agent', created: '2026-11-16T10:10:00Z' }, { id: 't2', cwd: 'apps/x', pending: 0, questions: 0, status: 'idle' }],
  }, NOW);
  assert.deepEqual(rows.map((r) => `${r.kind}:${r.id}`), ['session:t1', 'request:r1', 'changeset:c1', 'request:r2']);
  assert.equal(rows[0].title, 'Orders · Claude Code asks permission');
  assert.equal(rows[0].note, 'started by your agent');
  assert.equal(rows[2].title, '1 admin change proposed');
  assert.equal(W.needCount({ pendingRequests: 2, openChangeSets: 1 }), 3);
});

test('what my agent did: a sentence a row, three filters', () => {
  const rows = [
    { ts: '2026-11-16T10:16:00Z', method: 'POST', path: '/api/apps/invoices/links/rebuild', tile: 'apps/invoices', class: 'write', status: 200, ticket: 'r1' },
    { ts: '2026-11-16T10:15:00Z', class: 'decision:approve', tile: 'apps/invoices', effect: 'once' },
    { ts: '2026-11-16T10:12:00Z', method: 'POST', path: '/api/apps/ledger/x', tile: 'apps/ledger', class: 'write', status: 403, refused: 'Finance allows private models only' },
    { ts: '2026-11-16T10:11:00Z', method: 'GET', path: '/api/apps/orders/list', tile: 'apps/orders', class: 'read', status: 200 },
    { ts: '2026-11-16T10:10:00Z', method: 'POST', path: '/api/apps/orders/x', tile: 'apps/orders', class: 'write', status: 200, under: 'call' },
    { ts: '2026-11-16T10:09:00Z', class: 'changeset:grant.add', status: 200 },
  ];
  const all = rows.map(W.did);
  assert.deepEqual(all.map((d) => d.text + d.strong), ['Changed Invoices', 'You allowed a request', 'Was refused: Ledger', 'Read Orders', 'Changed Orders', 'Applied an admin change as you']);
  assert.equal(all[0].detail, 'POST /api/apps/invoices/links/rebuild · you allowed it once');
  assert.equal(all[4].detail.endsWith('under “always, this action”'), true);
  const count = (f) => W.didByDay(rows, f).reduce((n, d) => n + d.rows.length, 0);
  assert.deepEqual([count('change'), count('refused'), count('all')], [4, 1, 6]);
});

test('the dock\'s states and the chip', () => {
  const admin = { admin: true }, on = { workspace: true, pendingRequests: 2 };
  assert.equal(W.dockState({ state: { workspace: false }, me: {} }), 'hidden', 'a non-admin before setup sees nothing');
  assert.equal(W.dockState({ state: { workspace: false }, me: admin }), 'setup');
  assert.equal(W.dockState({ state: { workspace: false, canAdminChanges: true }, me: {} }), 'setup');
  assert.equal(W.dockState({ state: on, llm: { configured: false } }), 'unavailable');
  assert.equal(W.dockState({ state: { ...on, paused: true }, llm: { configured: true } }), 'paused');
  assert.equal(W.dockState({ state: { ...on, held: 'locked', agent: 'system/agent' }, llm: { configured: true } }), 'held');
  assert.equal(W.dockState({ state: { ...on, agent: 'system/agent' }, llm: { configured: true } }), 'page');
  assert.equal(W.dockState({ state: on, llm: { configured: true } }), 'soon', 'on, and this xbin ships no agent yet');
  assert.equal(W.dockState({ state: null }), 'hidden', 'no delegation on this xbind');
  assert.equal(W.dockState({ state: on, error: 'offline' }), 'offline');
  assert.deepEqual(W.chip({ state: { workspace: false }, me: {} }), { shown: false, label: '', spoken: '', count: 0, paused: false });
  assert.equal(W.chip({ state: { workspace: false }, me: admin }).label, 'set up workspace agent');
  assert.deepEqual(W.chip({ state: on, count: 2 }), { shown: true, label: 'workspace agent', spoken: 'Workspace agent, 2 need you', count: 2, paused: false });
  assert.equal(W.chip({ state: { ...on, paused: true }, count: 2 }).spoken, 'Workspace agent, paused');
  assert.match(W.stateWords('paused', { at: '2026-11-16T10:31:00Z' }).title, /^Paused by you at \d\d:31\.$/);
  assert.equal(W.stateWords('unavailable', { admins: ['Mira Okafor', 'Jonas Lind', 'A', 'B', 'C'] }).more, 'An admin can fix this: Mira Okafor or Jonas Lind and 3 more.');
  assert.equal(W.stateWords('held', { held: 'older' }).title, 'Waiting for a newer xbin.');
  const steps = W.setupSteps({ llm: { configured: false }, state: { workspace: false } });
  assert.deepEqual(steps.map((s) => [s.id, s.done, s.enabled]), [['model', false, true], ['on', false, false], ['who', false, false]]);
  assert.equal(W.setupSteps({ llm: { configured: true, host: true }, state: { workspace: false } })[0].tag, 'Set by your host');
});

test('what the dock takes from the agent\'s page: a word and an id, nothing else', () => {
  assert.deepEqual(W.pageMessage({ type: 'xbin:agent-review', kind: 'request', id: 'abc-1', summary: 'approve me', html: '<b>x</b>' }), { type: 'review', kind: 'request', id: 'abc-1' });
  assert.deepEqual(W.pageMessage({ type: 'xbin:agent-review', kind: 'changeset', id: 'c9' }), { type: 'review', kind: 'changeset', id: 'c9' });
  assert.deepEqual(W.pageMessage({ type: 'xbin:agent-open', view: 'approvals' }), { type: 'open', view: 'approvals' });
  for (const bad of [null, 'x', { type: 'xbin:agent-review', id: { toString: () => 'x' } }, { type: 'xbin:agent-review', id: '../x' }, { type: 'xbin:agent-review', id: 'a'.repeat(81) },
    { type: 'xbin:agent-review', kind: 'grant', id: 'a' }, { type: 'xbin:agent-open', view: 'allow' }, { type: 'xbin:agent-approve', id: 'r1' }, { type: 'xbin:agent-decide', id: 'r1', verdict: 'allow' }]) {
    assert.equal(W.pageMessage(bad), null, JSON.stringify(bad));
  }
  assert.equal(W.clampDock(100, 1400), 360);
  assert.equal(W.clampDock(900, 1400), 720);
  assert.equal(W.clampDock(500, 600), 600, 'a narrow window: the whole page');
});
