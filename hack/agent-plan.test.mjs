// hack/agent-plan.test.mjs — the plan review's rules (web/agent-plan-words.js;
// docs/protocol/delegation.md §Plans): which lines start ticked, what a tick
// needs, what is an admin's, what a page that is older than xbind may do.
import test from 'node:test';
import assert from 'node:assert/strict';

const W = await import(new URL('../web/agent-plan-words.js', import.meta.url).href);
const C = await import(new URL('../web/agent-chrome-words.js', import.meta.url).href);

const dice = (o = {}) => ({ id: 'cs', plan: true, state: 'proposed', title: 't', items: [
  { n: 1, kind: 'tile.create', line: 'Create the tile apps/dice from the template Starter (starter) — yours' },
  { n: 2, kind: 'tile.create', line: 'Create the tile apps/assistant from the template AI Agent (agent) — yours' },
  { n: 3, kind: 'binding.set', line: "Bind apps/assistant's llm to xbin:llm", admin: true, deferred: true, needs: [2], warnings: ['SPEND: it spends the quota'] },
  { n: 4, kind: 'binding.set', line: "Bind apps/assistant's mcp to apps/dice", deferred: true, needs: [1, 2] },
  { n: 5, kind: 'agent.start', line: 'Start a tile agent in apps/dice — Codex, in its mode "default"', deferred: true, needs: [1],
    start: { tile: 'apps/dice', provider: 'codex', providerName: 'Codex', mode: 'default', asks: true, prompt: 'Goal: roll_dice.', net: '' } },
], ...o });

test('what starts ticked: never a warned line, an admin\'s line of one\'s own plan, or a line whose needed line is not ticked', () => {
  const cs = dice();
  assert.deepEqual(W.planTicks(cs), [1, 2, 4, 5]);
  // an admin's line is the person's to tick: no switch stands before it — ticking it is the consent
  assert.equal(W.lineState(cs.items[2]), 'ok');
  assert.equal(W.canTick(cs, [1, 2], cs.items[2]), true);
  assert.equal(W.canTick(cs, [1], cs.items[2]), false, 'not without the line it needs');
  assert.equal(W.lineNotes(cs, cs.items[2]).tag, 'admin');
  assert.equal(W.lineNotes(cs, cs.items[2]).why, 'An admin change — applied as you.');
  assert.equal(W.lineNotes(cs, cs.items[0]).why, '');
  // …and it starts unticked with no warning too, with what needs it
  const plain = dice();
  plain.items[2].warnings = undefined;
  plain.items[3].needs = [1, 2, 3];
  assert.deepEqual(W.planTicks(plain), [1, 2, 5]);
  // a warned creation: what needs it starts unticked with it
  const warned = dice();
  warned.items[0].warnings = ['PUBLIC: x'];
  assert.deepEqual(W.planTicks(warned), [2]);
  assert.equal('optIn' in W.planWords(cs, [1, 2, 4, 5]), false, 'the review offers no switch');
});

test('unticking a line unticks the lines that need it, and says so', () => {
  const cs = dice();
  assert.deepEqual(W.neededBy(cs, 1), [4, 5]);
  assert.deepEqual(W.neededBy(cs, 2), [3, 4]);
  assert.deepEqual(W.setTick(cs, [1, 2, 4, 5], 1, false), [2]);
  assert.deepEqual(W.setTick(cs, [1, 2, 3, 4, 5], 2, false), [1, 5]);
  assert.deepEqual(W.setTick(cs, [1, 2], 5, true), [1, 2, 5]);
  const n = W.lineNotes(cs, cs.items[0]);
  assert.equal(n.neededBy, 'Needed by line 4 and 5 — unticking this unticks them');
  assert.equal(W.lineNotes(cs, cs.items[3]).needs, 'Needs line 1 and 2 — checked again once that exists');
  // through another line
  const chain = { items: [{ n: 1, kind: 'tile.create' }, { n: 2, kind: 'binding.set', needs: [1] }, { n: 3, kind: 'agent.start', needs: [2] }] };
  assert.deepEqual(W.neededBy(chain, 1), [2, 3]);
  // ticks kept across a sign-in still obey the rules
  assert.deepEqual(W.keepTicks(cs, [3, 4, 5]), [], 'kept ticks without the lines they need are dropped');
  assert.deepEqual(W.keepTicks(cs, [1, 2, 3]), [1, 2, 3]);
});

test('anyone who is no admin is offered to ask one', () => {
  const cs = dice();
  const w = W.planWords(cs, [1, 2, 4, 5]);
  assert.equal(w.bar, 'xbin · plan');
  assert.equal(w.heading, 'Your agent proposes 5 changes');
  assert.equal(w.apply, 'Apply 4 changes…');
  assert.equal(w.ask, null);
  // a person who is no admin: the admin line needs one, and is asked once what it needs is applied
  const mine = dice();
  Object.assign(mine.items[2], { needsAdmin: true, warnings: undefined });
  assert.equal(W.lineState(mine.items[2]), 'needs-admin');
  assert.deepEqual(W.planTicks(mine), [1, 2, 4, 5]);
  assert.equal(W.lineNotes(mine, mine.items[2]).tag, 'needs an admin');
  assert.match(W.lineNotes(mine, mine.items[2]).why, /Not yours to apply/);
  let a = W.planWords(mine, [1, 2, 4, 5]);
  assert.equal(a.ask.text, '1 line needs an admin.');
  assert.equal(a.ask.can, false);
  assert.equal(a.ask.why, 'Apply line 2 first: what waits for an admin needs it.');
  mine.items[1].result = 'applied';
  a = W.planWords(mine, []);
  assert.equal(a.ask.can, true);
  assert.equal(a.ask.button, 'Ask an admin');
  mine.items[2].result = 'asked';
  assert.equal(W.planWords(mine, []).ask, null);
  assert.equal(W.askedWords(mine), "1 line was sent to the workspace's admins. You are told here when one of them decides.");
  assert.equal(C.resultOf(mine.items[2]).word, 'sent to an admin');
});

test('a request from another person says who asks; its lines start as any line does', () => {
  const cs = { id: 'a', plan: true, from: 'bob', state: 'proposed', items: [{ n: 1, of: 3, kind: 'binding.set', line: "Bind apps/helper's llm to xbin:llm", admin: true, warnings: ['SPEND: x'] }] };
  const w = W.planWords(cs, []);
  assert.equal(w.bar, 'xbin · request from bob');
  assert.equal(w.heading, 'bob asks for 1 admin change');
  assert.equal(w.reject, 'Decline');
  assert.equal(W.lineState(cs.items[0]), 'ok');
  assert.deepEqual(W.planTicks(cs), [], 'a warned line starts unticked for the admin too');
  assert.deepEqual(W.planTicks({ ...cs, items: [{ ...cs.items[0], warnings: [] }] }), [1], 'an admin opened a request to apply it: its lines start ticked');
  assert.equal(W.lineNotes(cs, cs.items[0]).why, 'An admin change — applied as you.');
  assert.equal(C.needChangeTitle(cs).title, 'bob asks for 1 admin change');
  assert.equal(C.needChangeTitle(dice()).title, 'A plan of 5 changes');
});

test('a kind this page does not know is shown and never ticked', () => {
  const cs = dice();
  cs.items.push({ n: 6, kind: 'tile.delete', line: 'Delete the tile apps/dice' });
  assert.equal(W.lineState(cs.items[5]), 'unknown');
  assert.equal(W.canTick(cs, [1, 2], cs.items[5]), false);
  assert.ok(!W.planTicks(cs).includes(6));
  assert.deepEqual(W.keepTicks(cs, [6]), []);
  assert.match(W.lineNotes(cs, cs.items[5]).why, /does not know this kind of change/);
  // a blocked line, and one already decided
  cs.items[0].blocked = 'a tile is there';
  assert.equal(W.lineState(cs.items[0]), 'blocked');
  assert.deepEqual(W.planTicks(cs), [2]);
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

test('the review closes itself only when every chosen line was applied and nothing is left to do', () => {
  const done = (results, o = {}) => { const cs = dice({ state: 'applied', ...o }); results.forEach((r, i) => { cs.items[i].result = r; }); return cs; };
  assert.equal(W.closesItself(done(['applied', 'applied', 'applied', 'applied', 'applied']), [1, 2, 3, 4, 5]), true);
  assert.equal(W.closesItself(done(['applied', 'applied', 'rejected', 'applied', 'applied']), [1, 2, 4, 5]), true, 'a line left out by choice is no failure');
  assert.equal(W.closesItself(done(['applied', 'applied', 'rejected', 'applied', 'failed']), [1, 2, 4, 5]), false, 'a failure stays open');
  assert.equal(W.closesItself(done(['applied', 'stale', 'rejected', 'skipped', 'skipped']), [1, 2, 4, 5]), false);
  assert.equal(W.closesItself(done(['applied', 'applied', 'blocked', 'applied', 'applied']), [1, 2, 3, 4, 5]), false, 'a chosen line xbind blocked');
  const waits = done(['applied', 'applied', '', 'applied', 'applied']);
  Object.assign(waits.items[2], { needsAdmin: true, result: undefined });
  assert.equal(W.closesItself(waits, [1, 2, 4, 5]), false, 'a line still to send to an admin: Ask an admin is on the results');
  waits.items[2].result = 'asked';
  assert.equal(W.closesItself(waits, [1, 2, 4, 5]), true);
  assert.equal(W.closesItself(done(['applied', 'applied', 'applied', 'applied', 'applied'], { from: 'bob' }), [1, 2, 3, 4, 5]), false, 'a request from someone else has no card of the admin\'s to close into');
  assert.equal(W.closesItself(done(['applied', 'applied', 'applied', 'applied', 'applied'], { undoOf: 'x' }), [1, 2, 3, 4, 5]), false);
  assert.equal(W.closesItself({ id: 'x', state: 'applied', items: [{ n: 1, kind: 'grant.add', result: 'applied' }] }, [1]), false, 'an admin change set (no plan) keeps its Done');
  assert.equal(W.closesItself(done(['rejected', 'rejected', 'rejected', 'rejected', 'rejected']), []), false);
  assert.ok(W.PLAN_CLOSE_MS >= 1000);
});

test('a line that waits: approved, xbin\'s to apply — the review closes itself, the results and Needs you say what it waits for', () => {
  const cs = dice({ state: 'applied' });
  ['applied', 'applied', 'applied', 'waiting', 'applied'].forEach((r, i) => { cs.items[i].result = r; });
  cs.items[3].wait = { component: 'apps/assistant', slot: 'mcp', tile: 'apps/dice', kind: 'http', service: 'mcp', role: 'reader', until: '2026-11-17T10:00:00Z' };
  assert.equal(W.closesItself(cs, [1, 2, 3, 4, 5]), true, 'nothing is left for the person to do');
  assert.equal(W.waitingWords(cs), '1 line waits: xbin applies it by itself once the tile provides what you approved. Rejecting the plan from here cancels it.');
  assert.deepEqual([C.resultOf(cs.items[3]).word, C.resultOf(cs.items[3]).tone], ['waiting', 'warn']);
  assert.equal(C.resultOf(cs.items[3]).text, 'xbin applies it by itself once apps/dice provides mcp; nothing is bound until then.');
  const now = Date.parse('2026-11-16T10:00:00Z');
  const row = C.needs({ changesets: [cs] }, now)[0];
  assert.deepEqual([row.kind, row.waits, row.action], ['changeset', true, 'Open']);
  assert.match(row.title, /^Waiting: Bind apps\/assistant's mcp to apps\/dice$/);
  assert.match(row.note, /xbin applies it once apps\/dice provides mcp · lapses in /);
  // held: the tile declared something else than was approved
  cs.items[3].wait.held = 'apps/dice declares mcp for the role "writer": what you approved is the role reader';
  assert.match(C.resultOf(cs.items[3]).text, /^Not bound — apps\/dice declares mcp for the role "writer"/);
  assert.match(C.needs({ changesets: [cs] }, now)[0].note, /^not bound — apps\/dice declares mcp for the role "writer": what you approved is the role reader · lapses in /, 'the person reads the whole mismatch in Needs you');
  // ended
  Object.assign(cs.items[3], { result: 'lapsed', error: 'apps/dice did not provide mcp within 24 hours of the plan being applied: nothing was bound' });
  assert.deepEqual([C.resultOf(cs.items[3]).word, C.resultOf(cs.items[3]).text], ['lapsed — never applied', cs.items[3].error]);
  assert.equal(C.needs({ changesets: [cs] }, now).length, 0);
  assert.equal(W.waitingWords(cs), '');
});
