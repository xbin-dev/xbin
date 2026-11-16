// hack/agent-plan.test.mjs — the plan review's rules (web/agent-plan-words.js;
// docs/protocol/delegation.md §Plans): which lines start ticked, what a tick
// needs, what is an admin's, what a page that is older than xbind may do.
import test from 'node:test';
import assert from 'node:assert/strict';

const W = await import(new URL('../web/agent-plan-words.js', import.meta.url).href);
const C = await import(new URL('../web/agent-chrome-words.js', import.meta.url).href);

const dice = (o = {}) => ({ id: 'cs', plan: true, state: 'proposed', title: 't', items: [
  { n: 1, kind: 'tile.create', line: 'Create the tile apps/dice from the template MCP server (mcp-server) — yours' },
  { n: 2, kind: 'tile.create', line: 'Create the tile apps/assistant from the template AI Agent (agent) — yours' },
  { n: 3, kind: 'binding.set', line: "Bind apps/assistant's llm to xbin:llm", admin: true, deferred: true, needs: [2], warnings: ['SPEND: it spends the quota'] },
  { n: 4, kind: 'binding.set', line: "Bind apps/assistant's mcp to apps/dice", deferred: true, needs: [1, 2] },
  { n: 5, kind: 'agent.start', line: 'Start a tile agent in apps/dice — Codex, in its mode "default"', deferred: true, needs: [1],
    start: { tile: 'apps/dice', provider: 'codex', providerName: 'Codex', mode: 'default', asks: true, prompt: 'Goal: roll_dice.', net: '' } },
], ...o });
const admin = { canAdminChanges: true, adminChanges: false }, opted = { canAdminChanges: true, adminChanges: true }, user = { canAdminChanges: false, adminChanges: false };

test('what starts ticked: never a warned line, an admin line before the opt-in, or a line whose needed line is not ticked', () => {
  const cs = dice();
  assert.deepEqual(W.planTicks(cs, admin), [1, 2, 4, 5]);
  assert.deepEqual(W.planTicks(cs, opted), [1, 2, 4, 5], 'the SPEND line stays unticked after the opt-in too');
  assert.equal(W.lineState(cs.items[2], admin), 'opt-in');
  assert.equal(W.lineState(cs.items[2], opted), 'ok');
  assert.equal(W.canTick(cs, [1, 2], cs.items[2], opted), true);
  assert.equal(W.canTick(cs, [1], cs.items[2], opted), false, 'not without the line it needs');
  assert.equal(W.canTick(cs, [1, 2], cs.items[2], admin), false, 'not before the opt-in');
  // a warned creation: what needs it starts unticked with it
  const warned = dice();
  warned.items[0].warnings = ['PUBLIC: x'];
  assert.deepEqual(W.planTicks(warned, opted), [2]);
});

test('unticking a line unticks the lines that need it, and says so', () => {
  const cs = dice();
  assert.deepEqual(W.neededBy(cs, 1), [4, 5]);
  assert.deepEqual(W.neededBy(cs, 2), [3, 4]);
  assert.deepEqual(W.setTick(cs, [1, 2, 4, 5], 1, false), [2]);
  assert.deepEqual(W.setTick(cs, [1, 2, 3, 4, 5], 2, false), [1, 5]);
  assert.deepEqual(W.setTick(cs, [1, 2], 5, true), [1, 2, 5]);
  const n = W.lineNotes(cs, cs.items[0], admin);
  assert.equal(n.neededBy, 'Needed by line 4 and 5 — unticking this unticks them');
  assert.equal(W.lineNotes(cs, cs.items[3], admin).needs, 'Needs line 1 and 2 — checked again once that exists');
  // through another line
  const chain = { items: [{ n: 1, kind: 'tile.create' }, { n: 2, kind: 'binding.set', needs: [1] }, { n: 3, kind: 'agent.start', needs: [2] }] };
  assert.deepEqual(W.neededBy(chain, 1), [2, 3]);
  // ticks kept across a sign-in still obey the rules
  assert.deepEqual(W.keepTicks(cs, [3, 4, 5], opted), [], 'kept ticks without the lines they need are dropped');
  assert.deepEqual(W.keepTicks(cs, [1, 2, 3], admin), [1, 2]);
});

test('an admin is offered the opt-in on the review; anyone else is offered to ask an admin', () => {
  const cs = dice();
  const w = W.planWords(cs, [1, 2, 4, 5], admin);
  assert.equal(w.bar, 'xbin · plan');
  assert.equal(w.heading, 'Your agent proposes 5 changes');
  assert.equal(w.apply, 'Apply 4 changes…');
  assert.equal(w.optIn.text, 'This plan has 1 admin change.');
  assert.equal(w.optIn.label, 'Let my agent propose admin changes');
  assert.equal(W.planWords(cs, [], opted).optIn, null);
  assert.equal(w.ask, null);
  // a person who is no admin: the admin line needs one, and is asked once what it needs is applied
  const mine = dice();
  Object.assign(mine.items[2], { needsAdmin: true, warnings: undefined });
  assert.equal(W.lineState(mine.items[2], user), 'needs-admin');
  assert.deepEqual(W.planTicks(mine, user), [1, 2, 4, 5]);
  assert.equal(W.lineNotes(mine, mine.items[2], user).tag, 'needs an admin');
  let a = W.planWords(mine, [1, 2, 4, 5], user);
  assert.equal(a.optIn, null);
  assert.equal(a.ask.text, '1 line needs an admin.');
  assert.equal(a.ask.can, false);
  assert.equal(a.ask.why, 'Apply line 2 first: what waits for an admin needs it.');
  mine.items[1].result = 'applied';
  a = W.planWords(mine, [], user);
  assert.equal(a.ask.can, true);
  assert.equal(a.ask.button, 'Ask an admin');
  mine.items[2].result = 'asked';
  assert.equal(W.planWords(mine, [], user).ask, null);
  assert.equal(W.askedWords(mine), "1 line was sent to the workspace's admins. You are told here when one of them decides.");
  assert.equal(C.resultOf(mine.items[2]).word, 'sent to an admin');
});

test('a request from another person says who asks, and needs no opt-in of the admin\'s own', () => {
  const cs = { id: 'a', plan: true, from: 'bob', state: 'proposed', items: [{ n: 1, of: 3, kind: 'binding.set', line: "Bind apps/helper's llm to xbin:llm", admin: true, warnings: ['SPEND: x'] }] };
  const w = W.planWords(cs, [], admin);
  assert.equal(w.bar, 'xbin · request from bob');
  assert.equal(w.heading, 'bob asks for 1 admin change');
  assert.equal(w.reject, 'Decline');
  assert.equal(w.optIn, null);
  assert.equal(W.lineState(cs.items[0], admin, true), 'ok');
  assert.deepEqual(W.planTicks(cs, admin, true), [], 'a warned line starts unticked for the admin too');
  assert.equal(C.needChangeTitle(cs).title, 'bob asks for 1 admin change');
  assert.equal(C.needChangeTitle(dice()).title, 'A plan of 5 changes');
});

test('a kind this page does not know is shown and never ticked', () => {
  const cs = dice();
  cs.items.push({ n: 6, kind: 'tile.delete', line: 'Delete the tile apps/dice' });
  assert.equal(W.lineState(cs.items[5], opted), 'unknown');
  assert.equal(W.canTick(cs, [1, 2], cs.items[5], opted), false);
  assert.ok(!W.planTicks(cs, opted).includes(6));
  assert.deepEqual(W.keepTicks(cs, [6], opted), []);
  assert.match(W.lineNotes(cs, cs.items[5], opted).why, /does not know this kind of change/);
  // a blocked line, and one already decided
  cs.items[0].blocked = 'a tile is there';
  assert.equal(W.lineState(cs.items[0], opted), 'blocked');
  assert.deepEqual(W.planTicks(cs, opted), [2]);
});

test('a start line is drawn as the single start is: only xbind\'s word that the mode asks earns the green tag', () => {
  const cs = dice();
  let r = W.startRows(cs.items[4]);
  assert.equal(r.mode.tag, 'asks before acting');
  assert.equal(r.brief, 'Goal: roll_dice.');
  assert.match(r.agent, /^Codex/);
  cs.items[4].start.asks = 'yes';
  r = W.startRows(cs.items[4]);
  assert.equal(r.mode.tag, 'acts without asking');
  assert.equal(r.mode.tone, 'bad');
  assert.equal(W.startRows({}).brief, '');
  assert.equal(C.kindTag('agent.start'), 'tile agent');
  assert.equal(C.kindTag('tile.create'), 'new tile');
});
