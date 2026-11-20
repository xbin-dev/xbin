// hack/agent-start-words.test.mjs — one approval, one place (D250): the
// words and rules of web/agent-start-words.js — the start the person picks
// on xbin's sheet, a message to a tile agent that acts without asking, the
// one batch. Run with `make js-test`.
import test from 'node:test';
import assert from 'node:assert/strict';
import * as S from '../web/agent-start-words.js';

const NOW = Date.parse('2026-11-21T10:52:00Z');
const at = (s) => `2026-11-21T${s}Z`;
const harnesses = [
  { id: 'codex', name: 'Codex', defaultMode: 'read-only', modelsKnown: true, noSignIn: true, signIn: 'connector',
    modes: [{ id: 'read-only', name: 'Ask for approval', asks: true }, { id: 'agent', name: 'Approve for me', asks: false }, { id: 'agent-full-access', name: 'Full access', asks: false, explicit: true }],
    models: [{ id: '' }, { id: 'xbin/big', from: 'connector' }] },
  { id: 'gemini', name: 'Gemini CLI', defaultMode: 'default', modelsKnown: false, signIn: 'needed', signInId: 'si_1',
    modes: [{ id: 'default', name: 'Ask before acting', asks: true }, { id: 'autoEdit', name: 'Auto edit', asks: false }], models: [{ id: '' }, { id: 'm-agent', from: 'agent' }] },
];
const start = (session = {}, o = {}) => ({ id: 'rq_1', kind: 'session', method: 'POST', target: '/api/xbin/term/sessions', tile: 'apps/eui', state: 'pending',
  created: at('10:47:00'), expires: '2026-11-22T10:47:00Z', body: JSON.stringify({ cwd: 'apps/eui', kind: 'agent', prompt: 'Goal: tic tac toe' }),
  preview: { session: { tile: 'apps/eui', provider: 'codex', providerName: 'Codex', mode: 'read-only', asks: true, model: '', choices: { harnesses },
    from: { harness: 'default', mode: 'default', model: 'default' }, ...session } }, ...o });
const noDot = (v, what) => assert.ok(!JSON.stringify(v).includes('·'), `${what}: no "·" in what a person reads`);

test('the sheet preselects xbind\'s pick, says where each part came from, and the default mode asks', () => {
  const req = start({ from: { harness: 'tile', mode: 'tile', model: 'default' } });
  const pick = S.pickOf(req);
  assert.deepEqual([pick.harness, pick.mode, pick.model, pick.remember, pick.cont], ['codex', 'read-only', '', false, false]);
  const v = S.startView(req, pick);
  assert.deepEqual(v.pickers.map((p) => [p.id, p.value, p.from]), [['harness', 'codex', 'remembered for this tile'], ['model', '', 'the default'], ['mode', 'read-only', 'remembered for this tile']]);
  assert.deepEqual(v.pickers[1].options.map((o) => o.label), ['its default', 'xbin/big']);
  assert.equal(v.pickers[1].note, '', 'the connector\'s models: the whole list');
  assert.match(v.pickers[2].note, /^The default: in it, it asks you before it edits a file or runs a command/);
  assert.deepEqual([v.asks, v.label, v.danger], [true, 'Start', '']);
  assert.equal(v.details, 'POST /api/xbin/term/sessions', 'the call is the sheet\'s Details, not the card\'s');
  noDot(v, 'the start\'s view');
});

test('a mode the agent suggested that acts without asking is never picked for the person — only by their own pick', () => {
  const req = start({ suggested: { mode: 'agent' }, modeNotTaken: 'agent' });
  let pick = S.pickOf(req);
  const v = S.startView(req, pick);
  assert.equal(pick.mode, 'read-only');
  assert.match(v.pickers[2].warn, /^Your agent suggested agent, which acts without asking\. It isn't picked for you/);
  // the person picks it: it is theirs — the warning goes, the danger shows, Allow says what it is
  pick = S.choose(pick, req, 'mode', 'agent');
  const w = S.startView(req, pick);
  assert.deepEqual([w.pickers[2].warn, w.pickers[2].from, w.asks, w.label, pick.remember], ['', 'your pick', false, 'Start without asking', true]);
  assert.match(w.danger, /^Codex in this mode acts without asking: it edits files and runs commands in eui without asking you/);
  assert.deepEqual(S.pickBody(req, pick), { knows: ['continue'], seen: { signInId: '', net: '' }, harness: 'codex', model: '', mode: 'agent', remember: true },
    'what the sheet showed rides with the pick (D250 review L2)');
});

test('picking another coding agent: its own default mode, its models, ITS sign-in offer; models xbin knows said so', () => {
  const req = start();
  const pick = S.choose(S.pickOf(req), req, 'harness', 'gemini');
  assert.deepEqual([pick.harness, pick.mode, pick.model], ['gemini', 'default', '']);
  const v = S.startView(req, pick);
  assert.deepEqual(v.pickers[1].options.map((o) => o.label), ['its default', 'm-agent (suggested by your agent)']);
  assert.match(v.pickers[1].note, /^Gemini CLI's own list shows in its session once it runs/);
  const p = S.picked(req, pick).preview.session;
  assert.deepEqual([p.provider, p.providerName, p.mode, p.asks, p.signInId, p.signIn], ['gemini', 'Gemini CLI', 'default', true, 'si_1', 'needed']);
  // the coding agent is the person's; its model and mode, reset to its defaults, are the defaults — not theirs to remember (D250 review M1)
  assert.deepEqual(v.pickers.map((x) => x.from), ['your pick', 'the default', 'the default']);
});

test('a start that would continue the agent\'s own idle session: continue first, a new session the other choice', () => {
  const req = start({ continue: { session: 'ts-1', provider: 'opencode', providerName: 'OpenCode', mode: 'build', asks: false, model: 'm1', started: at('10:30:00') } });
  const pick = S.pickOf(req);
  assert.deepEqual([pick.harness, pick.cont], ['opencode', true], 'continuing is the first choice: that session\'s coding agent');
  const v = S.startView(req, pick);
  assert.equal(v.continuing, true);
  assert.match(v.cont.label, /^Continue the OpenCode session from \d\d:\d\d with this follow-up$/);
  assert.deepEqual(v.cont.chips, ['OpenCode', 'model m1', 'acts without asking']);
  assert.deepEqual([v.pickers.length, v.label, v.asks, v.remember.shown], [0, 'Continue', false, false]);
  assert.deepEqual(S.pickBody(req, pick), { knows: ['continue'], seen: { signInId: '', net: '' } }, 'continuing: the session keeps its own');
  const anew = S.contChoice(pick, req, false);
  const fresh = S.startView(req, anew);
  assert.deepEqual([anew.harness, fresh.continuing, fresh.pickers.length, fresh.label], ['codex', false, 3, 'Start'], 'a new session: on what xbind preselected for one');
  assert.deepEqual(S.pickBody(req, anew), { knows: ['continue'], seen: { signInId: '', net: '' }, continue: false, harness: 'codex', model: '', mode: 'read-only' });
  assert.equal(S.contChoice(anew, req, true).harness, 'opencode');
  noDot(v, 'a continue');
});

test('an older xbind\'s start (no choices): nothing picked, nothing sent but what it took before', () => {
  const req = start({ choices: undefined, from: undefined });
  const v = S.startView(req, S.pickOf(req));
  assert.deepEqual([v.pickers.length, v.label], [0, 'Start']);
  assert.deepEqual(S.pickBody(req, S.pickOf(req)), {});
  assert.deepEqual(S.pickBody({ kind: 'call' }, {}), {});
});

test('a message to a tile agent that acts without asking: the whole text, what it is, the one action', () => {
  const m = S.messageView({ kind: 'message', tile: 'apps/eui', preview: { message: { session: 'ts-1', provider: 'opencode', providerName: 'OpenCode', mode: 'build', text: 'Fix the stray O.' } } });
  assert.deepEqual(m.heading, { before: 'Send this message to the OpenCode session in ', strong: 'eui', after: '?' });
  assert.deepEqual([m.agent, m.text, m.label], [['OpenCode', 'mode build', 'acts without asking'], 'Fix the stray O.', 'Send it']);
  assert.match(m.why, /exactly this text, once/);
});

test('the one batch: every pending act once, oldest first, one action each — and Review all when more than one opens a review', () => {
  const store = {
    requests: [start({}, { id: 'rq_b', created: at('10:48:00') }), { id: 'rq_a', kind: 'call', method: 'POST', target: '/api/apps/eui/scores/reset', tile: 'apps/eui', state: 'pending', created: at('10:47:30'), expires: '2026-11-22T10:47:30Z', preview: { tile: 'apps/eui' } }],
    failed: [{ ...start({}, { id: 'rq_f', created: at('10:40:00') }), state: 'failed', failure: { why: 'the tile\'s sandbox didn\'t start', retry: true } },
      { ...start({}, { id: 'rq_g' }), state: 'failed', failure: { why: 'refused', retry: false } }],
    changesets: [{ id: 'cs_1', state: 'proposed', plan: true, created: at('10:49:00'), expires: '2026-11-27T10:49:00Z', items: [{ n: 1, warnings: ['x'] }, { n: 2 }] }],
    sessions: [{ id: 'ts-9', cwd: 'apps/eui', provider: 'opencode', providerName: 'OpenCode', pending: 1, origin: 'agent', created: at('10:50:00') }],
  };
  const rows = S.batch(store, NOW);
  assert.deepEqual(rows.map((r) => [r.kind, r.id, r.action]), [['retry', 'rq_f', 'Try again'], ['request', 'rq_a', 'Review…'], ['request', 'rq_b', 'Review…'],
    ['changeset', 'cs_1', 'Review…'], ['session', 'ts-9', 'Open']]);
  assert.deepEqual(rows[2].chips, ['Codex', 'asks before acting']);
  assert.deepEqual(rows[2].parts, ['asked ' + new Date(Date.parse(at('10:48:00'))).toTimeString().slice(0, 5), 'expires in 23 h 55 min']);
  assert.equal(rows[0].title, 'Didn\'t start: Start a tile agent in Eui');
  assert.deepEqual(rows[3].parts, ['1 has a warning', 'expires in 5 days'], 'a plan is ONE review: its notes are parts');
  assert.deepEqual(S.batchTitle(rows), { title: 'Waiting for you', count: 5, reviewAll: 'Review all' });
  assert.equal(S.batchTitle(rows.slice(0, 2)).reviewAll, '', 'one review: nothing to walk');
  noDot(rows, 'the batch');
  assert.deepEqual(S.askedParts({ created: at('10:47:00'), expires: '2026-11-22T10:47:00Z' }, NOW).length, 2);
});

test('Remember keeps no mode that acts without asking, and nothing of a coding agent with no mode that asks (D250 review M1, M2)', () => {
  const oc = { id: 'opencode', name: 'OpenCode', defaultMode: '', modelsKnown: false, modes: [{ id: '', name: 'its own default', asks: false }, { id: 'build', name: 'build', asks: false }], models: [{ id: '' }] };
  const req = start({ choices: { harnesses: [...harnesses, oc] }, harnessNotTaken: 'opencode' });
  let pick = S.pickOf(req);
  const v = S.startView(req, pick);
  assert.match(v.pickers[0].warn, /^Your agent suggested OpenCode, which acts without asking in every mode\. It isn't picked for you/);
  assert.deepEqual([v.remember.shown, v.remember.keepsMode], [true, true]);
  pick = S.choose(pick, req, 'mode', 'agent');
  const w = S.startView(req, pick);
  assert.equal(w.remember.keepsMode, false);
  assert.match(w.remember.note, /^This mode acts without asking: it isn't remembered/);
  pick = S.choose(pick, req, 'harness', 'opencode');
  const o = S.startView(req, pick);
  assert.deepEqual([o.remember.shown, o.asks, o.pickers[0].warn], [false, false, '']);
  assert.match(o.remember.note, /^OpenCode has no mode that asks before it acts: nothing of it is remembered/);
});

test('a follow-up into a session that carries the person\'s saved sign-in: the sheet says so, and only a tick lets it go in (D250 review L1)', () => {
  const req = start({ continue: { session: 'ts-1', provider: 'codex', providerName: 'Codex', mode: 'read-only', asks: true, model: '', signIn: 'saved', signInId: 'si_9', signInName: 'work' } });
  const pick = S.pickOf(req);
  const v = S.startView(req, pick);
  assert.deepEqual([v.continuing, v.needsTick, v.cont.signIn.id], [true, true, 'si_9']);
  assert.deepEqual(v.cont.chips, ['Codex', 'asks before acting', 'your saved sign-in']);
  assert.match(v.cont.signIn.warn, /a follow-up into it uses it/);
  assert.equal(S.picked(req, pick).preview.session.signInId, 'si_9', 'the tick is for the session\'s own sign-in');
  const anew = S.contChoice(pick, req, false);
  assert.deepEqual([S.startView(req, anew).needsTick, S.picked(req, anew).preview.session.signInId], [false, '']);
  // a continued session that doesn't ask: the danger is said, whatever a new session would be
  const nb = start({ continue: { session: 'ts-2', provider: 'codex', providerName: 'Codex', mode: 'agent', asks: false } });
  const w = S.startView(nb, S.pickOf(nb));
  assert.deepEqual([w.continuing, w.asks], [true, false]);
  assert.match(w.danger, /^Codex in this mode acts without asking/);
});

test('the sheet\'s basis: the sign-in offered and the network of the coding agent picked (D250 review L2)', () => {
  const hs = [{ ...harnesses[0], net: 'none' }, { ...harnesses[1], net: 'internet' }];
  const req = start({ choices: { harnesses: hs }, net: 'none', signInId: '' });
  let pick = S.pickOf(req);
  assert.deepEqual(S.pickBody(req, pick).seen, { signInId: '', net: 'none' });
  pick = S.choose(pick, req, 'harness', 'gemini');
  assert.deepEqual(S.pickBody(req, pick).seen, { signInId: 'si_1', net: 'internet' }, 'the picked coding agent\'s own');
});

test('Try again: the earlier picks preselected — never the sign-in tick (D250 review L3)', () => {
  const req = { ...start(), state: 'failed', failure: { why: 'the sandbox didn\'t start', retry: true }, chosen: { provider: 'gemini', model: 'm-agent', mode: 'default' } };
  const pick = S.pickOf(req);
  assert.deepEqual([pick.harness, pick.model, pick.mode, pick.remember, pick.cont], ['gemini', 'm-agent', 'default', false, false]);
});
