// hack/agent-start-words.test.mjs — one approval, one place (D250), a mode
// a fact and continuing one plain approval (D251): the words and rules of
// web/agent-start-words.js — the start the person picks on xbin's sheet, a
// message to a tile agent that acts without asking, the one batch and the
// follow-ups allowed on its rows. Run with `make js-test`.
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

test('the sheet preselects xbind\'s pick and says where each part came from; a mode is a chip and a fact, Start the one label', () => {
  const req = start({ from: { harness: 'tile', mode: 'tile', model: 'default' } });
  const pick = S.pickOf(req);
  assert.deepEqual([pick.harness, pick.mode, pick.model, pick.remember, pick.cont], ['codex', 'read-only', '', false, false]);
  const v = S.startView(req, pick);
  assert.deepEqual(v.pickers.map((p) => [p.id, p.value, p.from]), [['harness', 'codex', 'remembered for this tile'], ['model', '', 'the default'], ['mode', 'read-only', 'remembered for this tile']]);
  assert.deepEqual(v.pickers[1].options.map((o) => o.label), ['its default', 'xbin/big']);
  assert.equal(v.pickers[1].note, '', 'the connector\'s models: the whole list');
  assert.equal(v.pickers[2].note, '');
  assert.deepEqual([v.asks, v.label, v.modeChip, v.fact, v.danger], [true, 'Start', 'asks before acting', '', undefined]);
  assert.deepEqual(v.pickers[2].options.map((o) => o.label), ['Ask for approval, asks before acting', 'Approve for me, acts without asking', 'Full access, skips every check']);
  assert.equal(v.details, 'POST /api/xbin/term/sessions', 'the call is the sheet\'s Details, not the card\'s');
  noDot(v, 'the start\'s view');
});

test('a mode that acts without asking: the chip and the plain line, the ordinary Start — no warning, no danger (D251)', () => {
  const req = start({ mode: 'agent', asks: false });
  let pick = S.pickOf(req);
  const v = S.startView(req, pick);
  assert.deepEqual([pick.mode, v.asks, v.label, v.modeChip, v.pickers[2].warn, v.pickers[2].note], ['agent', false, 'Start', 'acts without asking', '', '']);
  assert.equal(v.fact, "It edits files and runs commands in this tile's sandbox without asking you. You approve what your agent tells it.");
  // a bypass mode the agent suggested: never preselected, said beside the picker; picked, it is the person's — said what it is
  const b = start({ suggested: { mode: 'agent-full-access' }, modeNotTaken: 'agent-full-access' });
  pick = S.pickOf(b);
  assert.match(S.startView(b, pick).pickers[2].warn, /^Your agent suggested agent-full-access, which skips every check\. It isn't picked for you/);
  pick = S.choose(pick, b, 'mode', 'agent-full-access');
  const w = S.startView(b, pick);
  assert.deepEqual([w.pickers[2].warn, w.pickers[2].from, w.asks, w.label, pick.remember], ['', 'your pick', false, 'Start', true]);
  assert.match(w.pickers[2].note, /^Full access skips every check Codex has of its own/);
  assert.deepEqual(S.pickBody(b, pick), { knows: ['continue'], seen: { signInId: '', net: '' }, harness: 'codex', model: '', mode: 'agent-full-access', remember: true },
    'what the sheet showed rides with the pick (D250 review L2)');
  noDot([v, w], 'the start\'s view');
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
  assert.deepEqual([v.pickers.length, v.label, v.asks, v.remember.shown, v.needsTick], [0, 'Send', false, false, undefined]);
  assert.deepEqual(S.pickBody(req, pick), { knows: ['continue'], seen: { signInId: '', net: '', harness: 'opencode', mode: 'build' } },
    'continuing: the session keeps its own — held to it as shown (D251)');
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
  assert.deepEqual([m.agent, m.text, m.label], [['OpenCode', 'mode build', 'acts without asking'], 'Fix the stray O.', 'Send']);
  assert.match(m.why, /exactly this text to this session, once/);
  const si = S.messageView({ kind: 'message', tile: 'apps/eui', preview: { message: { session: 'ts-1', provider: 'claude', providerName: 'Claude Code', mode: 'auto', text: 'x', signInId: 'si_9' } } });
  assert.deepEqual(si.agent, ['Claude Code', 'mode auto', 'acts without asking', 'uses your Claude Code sign-in']);
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

test('Remember keeps any mode the person picked but a bypass one, OpenCode\'s too (D251)', () => {
  const oc = { id: 'opencode', name: 'OpenCode', defaultMode: '', modelsKnown: false, modes: [{ id: '', name: 'its own default', asks: false }, { id: 'build', name: 'build', asks: false }], models: [{ id: '' }] };
  const req = start({ choices: { harnesses: [...harnesses, oc] } });
  let pick = S.pickOf(req);
  const v = S.startView(req, pick);
  assert.deepEqual([v.remember.shown, v.remember.keepsMode, v.remember.note], [true, true, 'Your next start here preselects what you changed.']);
  pick = S.choose(pick, req, 'mode', 'agent');
  assert.equal(S.startView(req, pick).remember.keepsMode, true, 'a mode that acts without asking is remembered like any other');
  pick = S.choose(pick, req, 'mode', 'agent-full-access');
  const b = S.startView(req, pick);
  assert.equal(b.remember.keepsMode, false);
  assert.match(b.remember.note, /A mode that skips every check isn't remembered: you pick it each time/);
  pick = S.choose(S.choose(pick, req, 'harness', 'opencode'), req, 'mode', 'build');
  const o = S.startView(req, pick);
  assert.deepEqual([o.remember.shown, o.asks, o.pickers[0].warn, o.remember.keepsMode], [true, false, '', true]);
});

test('continuing a session that carries the person\'s saved sign-in: one plain approval — the sign-in a chip, no tick (D251)', () => {
  const req = start({ continue: { session: 'ts-1', provider: 'claude', providerName: 'Claude Code', mode: 'auto', asks: false, model: 'opus', signIn: 'saved', signInId: 'si_9', signInName: 'work' } });
  const pick = S.pickOf(req);
  const v = S.startView(req, pick);
  assert.deepEqual([v.continuing, v.needsTick, v.cont.signIn, v.label], [true, undefined, undefined, 'Send']);
  assert.deepEqual(v.cont.chips, ['Claude Code', 'model opus', 'acts without asking', 'uses your Claude Code sign-in']);
  assert.deepEqual(S.pickBody(req, pick).seen, { signInId: 'si_9', net: '', harness: 'claude', mode: 'auto' }, 'held to the session shown, its sign-in with it');
  const anew = S.contChoice(pick, req, false);
  assert.deepEqual([S.startView(req, anew).label, S.picked(req, anew).preview.session.signInId], ['Start', '']);
  noDot(v, 'a continue with a sign-in');
});

test('a follow-up on its batch row: chips, the first lines and Show all, Send (D251)', () => {
  const long = 'Add a computer player.\nIt plays O.\nIt waits half a second.\nIt never loses.\nKeep the reset button.';
  const cont = start({ continue: { session: 'ts-1', provider: 'claude', providerName: 'Claude Code', mode: 'auto', asks: false, model: 'opus', signInId: 'si_9' } },
    { id: 'rq_c', body: JSON.stringify({ cwd: 'apps/eui', kind: 'agent', prompt: long, continue: 'ts-1' }) });
  const msg = { id: 'rq_m', kind: 'message', tile: 'apps/eui', state: 'pending', created: at('10:49:00'), preview: { message: { session: 'ts-1', provider: 'opencode', providerName: 'OpenCode', mode: 'build', text: 'Fix the stray O.' } } };
  const c = S.inPlace(cont), m = S.inPlace(msg);
  assert.deepEqual([c.kind, c.title, c.chips, c.text], ['continue', 'Continue the Claude Code session in eui', ['Claude Code', 'model opus', 'acts without asking', 'uses your Claude Code sign-in'], long]);
  assert.deepEqual(c.body, { grant: 'once', knows: ['continue'], seen: { signInId: 'si_9', net: '', harness: 'claude', mode: 'auto' } }, 'what the sheet sends, as preselected');
  assert.deepEqual([m.kind, m.chips, m.body], ['message', ['OpenCode', 'mode build', 'acts without asking'], { grant: 'once' }]);
  assert.deepEqual(S.rowText(long), { text: 'Add a computer player.\nIt plays O.\nIt waits half a second.', clipped: true, note: 'first 3 of 5 lines' });
  assert.deepEqual(S.rowText(long, true), { text: long, clipped: false, note: '' }, 'Show all: the whole text, one click before Send');
  assert.equal(S.rowText('x'.repeat(400)).note, 'first 280 of 400 characters');
  // the sheet's to show: hidden characters, a conversation that read the web, a new start, a resumed one
  assert.equal(S.inPlace({ ...msg, preview: { message: { ...msg.preview.message, text: 'a\u202Eb' } } }), null);
  assert.equal(S.inPlace({ ...cont, preview: { ...cont.preview, readWeb: true } }), null);
  assert.equal(S.inPlace(start()), null);
  const rows = S.batch({ requests: [cont, msg] }, NOW);
  assert.deepEqual(rows.map((r) => [r.id, r.action, r.review, !!r.inline]), [['rq_c', 'Send', true, true], ['rq_m', 'Send', true, true]], 'Review all still walks them');
  noDot(rows, 'the follow-up rows');
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

test('a coding agent with no mode that asks (OpenCode): an ordinary choice — the fact, no word that it asks, Remember keeps its mode (D251)', () => {
  const oc = { id: 'opencode', name: 'OpenCode', defaultMode: '', modelsKnown: true, noSignIn: true,
    modes: [{ id: '', name: 'its own default', asks: false }, { id: 'build', name: 'build', asks: false }, { id: 'plan', name: 'plan', asks: false }], models: [{ id: '' }] };
  // the person's own choice for the tile: preselected, warned
  const req = start({ provider: 'opencode', providerName: 'OpenCode', mode: '', asks: false, choices: { harnesses: [...harnesses, oc] }, from: { harness: 'tile', mode: 'default', model: 'default' } });
  const v = S.startView(req, S.pickOf(req));
  assert.deepEqual([v.asks, v.label, v.modeChip, v.danger, v.pickers[2].note], [false, 'Start', 'acts without asking', undefined, '']);
  assert.match(v.fact, /^It edits files and runs commands in this tile's sandbox without asking you/);
  assert.ok(!/asks you/.test(JSON.stringify(v)), `no word that it asks you anywhere on the sheet: ${JSON.stringify(v).match(/.{60}asks you.{20}/)}`);
  assert.deepEqual([v.remember.shown, v.remember.keepsMode], [true, true]);
});
