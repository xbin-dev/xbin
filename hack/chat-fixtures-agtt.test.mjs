// hack/chat-fixtures-agtt.test.mjs — the shared chat transcripts
// (sdk/acp/testdata/transcripts) against the agent template, run by `make
// js-test`. The template never folds events: its backend maps them to rows
// (builtin-templates/agent/_backend/harness_map.go — its transcripts_test.go
// applies each log and keeps the run's view at every checkpoint as
// <name>.agtt.json), and model/fold.js folds a view. This test folds those
// views with model/fold.js, says the result in the chat kit's block model
// through hack/chat-fixtures/agtt.mjs — a call and a request by the
// template's own adapters to the kit — and holds it to the same
// <name>.blocks.json the Agent tab's fold is held to
// (hack/chat-fixtures.test.mjs, where regenerating is described) — after
// both went through the gaps hack/chat-fixtures/expect.mjs lists for the
// template: every field that cannot match, with its reason.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { walk, KINDS, plain, TOOL_STATES } from '../web/chat/blocks.js';
import { load, shown, through, canon, each, GAPS } from './chat-fixtures/expect.mjs';
import { installHooks } from './xbn/hooks.mjs';

installHooks(); // /vendor/<name> as xbind serves it: the kit the template's models import (web/<name>)
const { fold, FoldCache } = await import('../builtin-templates/agent/model/fold.js');
const { rawInputOf, acpRecord, acpHeadline } = await import('../builtin-templates/agent/model/harness-heads.js');
const { headline, subline, outcome, stateChip, oneLine } = await import('../web/chat/tools.js');
const { agttTranscript, tileView, callIdOf } = await import('./chat-fixtures/agtt.mjs');

const all = load();

// The journal lines (the template's `step` kind, gap A6) each transcript
// leaves at its checkpoints: only a turn that failed and a sign-in say one.
const STEPS = {
  signin: { 8: ['error', 'note'], 9: ['error', 'note'], 19: ['error', 'note'] },
};

for (const t of all) {
  test(`${t.name}: the template's fold makes the expected blocks`, () => {
    assert.ok(t.agtt, `${t.name}.agtt.json is missing — hack/chat-fixtures.test.mjs's header says how to make it`);
    assert.deepEqual(t.agtt.views.map((v) => v.seq), t.blocks.checkpoints.map((c) => c.seq), `${t.name}: a view per checkpoint`);
    for (const [i, cp] of t.blocks.checkpoints.entries()) {
      const tr = agttTranscript(tileView(t.agtt.views[i].view));
      const got = shown(tr, cp.seq);
      const steps = [];
      each(got, (b) => { if (b.kind === 'step') steps.push(b.step); });
      assert.deepEqual(steps, (STEPS[t.name] || {})[cp.seq] || [], `${t.name} at seq ${cp.seq}: its journal lines`);
      const want = through('agtt', cp, 'expected'), have = through('agtt', got, 'actual');
      assert.deepEqual(have.blocks, want.blocks, `${t.name} at seq ${cp.seq}: blocks`);
      assert.deepEqual(have.tail || [], want.tail || [], `${t.name} at seq ${cp.seq}: what waits on a person`);
      assert.deepEqual(have.usage || null, want.usage || null, `${t.name} at seq ${cp.seq}: usage`);
      // the plan: the summary's is the newest plan block's (gap A3)
      let newest = null;
      each(cp, (b) => { if (b.kind === 'plan') newest = b; });
      assert.deepEqual(tr.plan ? shown({ blocks: [tr.plan] }, 0).blocks[0].entries : null, newest ? newest.entries : null, `${t.name} at seq ${cp.seq}: the plan`);
    }
  });

  test(`${t.name}: the template's keys are unique, and a view folded again keeps them`, () => {
    for (const v of t.agtt.views) {
      const view = tileView(v.view);
      const tr = agttTranscript(view);
      const keys = [];
      walk([...tr.blocks, ...tr.tail], (b) => keys.push(b.k));
      assert.equal(new Set(keys).size, keys.length, `${t.name} at ${v.seq}: ${keys}`);
      const again = [];
      walk(agttTranscript(view).blocks, (b) => again.push(b.k));
      assert.deepEqual(again, keys.slice(0, again.length));
      walk(tr.blocks, (b) => { if (b.kind === 'tool') assert.ok(TOOL_STATES.includes(b.state), `${t.name}: state ${b.state}`); });
    }
  });
}

test('one call, one reading: the kit says a call the same from either reducer\'s record', () => {
  let calls = 0;
  for (const t of all) {
    for (const [i, cp] of t.blocks.checkpoints.entries()) {
      const want = [], have = [];
      each(cp, (b) => { if (b.kind === 'tool') want.push(b); }); // the expectation: the Agent tab's reading of the log
      walk(agttTranscript(tileView(t.agtt.views[i].view)).blocks, (b) => { if (b.kind === 'tool') have.push(b); });
      assert.deepEqual(have.map((b) => b.rec.id), want.map((b) => b.rec.id), `${t.name} at ${cp.seq}: the same calls`);
      for (const [n, b] of have.entries()) {
        const w = want[n], at = `${t.name} at ${cp.seq}, call ${b.rec.id}`;
        calls++;
        assert.equal(headline(b.rec), headline(w.rec), `${at}: headline`);
        assert.equal(subline(b.rec), subline(w.rec), `${at}: the command under it`);
        assert.deepEqual(stateChip(b.rec, b.state), stateChip(w.rec, w.state), `${at}: state chip`);
        // (a call's text is not on its row while it runs, gap A10: an outcome is said of a finished call)
        assert.deepEqual(outcome(b.rec), outcome(w.rec), `${at}: outcome`);
      }
    }
  }
  assert.ok(calls > 40, `${calls} calls compared`);
});

test('the template\'s fold says a coding agent\'s call with the kit\'s headline of its record', () => {
  let seen = 0;
  for (const t of all) {
    for (const v of t.agtt.views) {
      const blocks = [];
      const walkFold = (list) => { for (const b of list || []) { blocks.push(b); walkFold(b.kids); } };
      walkFold(fold(tileView(v.view), () => null));
      for (const b of blocks.filter((x) => x.k === 'tool' && x.acp)) {
        seen++;
        const a = JSON.parse(b.args || '{}');
        assert.equal(b.headline, oneLine(headline(acpRecord(b, a)), 100), `${t.name} at ${v.seq}: ${b.callId}`);
        assert.equal(b.headline, acpHeadline(b.name, a, b.acp));
        assert.equal(b.sub, subline(acpRecord(b, a)));
      }
    }
  }
  assert.ok(seen > 40, `${seen} calls`);
});

test('each of the template\'s gaps is one the transcripts show', () => {
  // without a gap, some checkpoint differs: none is listed that the template has closed
  for (const g of GAPS.agtt) {
    const differs = all.some((t) => t.blocks.checkpoints.some((cp, i) => {
      const got = shown(agttTranscript(tileView(t.agtt.views[i].view)), cp.seq);
      return canon(through('agtt', got, 'actual', g.id)) !== canon(through('agtt', cp, 'expected', g.id));
    }));
    assert.ok(differs, `gap ${g.id} (${g.why}) is closed: delete it from hack/chat-fixtures/expect.mjs`);
  }
});

test('from view to view the template\'s stored blocks keep their keys (a draft becomes its row: another key)', () => {
  for (const t of all) {
    let before = new Set();
    for (const v of t.agtt.views) {
      const now = new Set();
      walk(agttTranscript(tileView(v.view)).blocks, (b) => now.add(b.k));
      for (const k of before) if (!k.startsWith('draft-')) assert.ok(now.has(k), `${t.name} at ${v.seq}: block ${k} is gone`);
      before = now;
    }
  }
});

test('the template\'s fold reaches the kinds it can make of a coding agent\'s log', () => {
  const kinds = new Set();
  for (const t of all) for (const v of t.agtt.views) { const tr = agttTranscript(tileView(v.view)); walk([...tr.blocks, ...tr.tail, ...(tr.plan ? [tr.plan] : [])], (b) => kinds.add(b.kind)); }
  assert.deepEqual([...kinds].sort(), ['ask', 'assistant', 'draft', 'perm', 'plan', 'signin', 'step', 'thought', 'tool', 'user']);
});

// ---- what no coding agent's log reaches: the template's own agent ---------------------

const call = (id, name, args) => ({ id, type: 'function', function: { name, arguments: JSON.stringify(args) } });

test('the template\'s own tools, subagents and parks say themselves in the model too', () => {
  const child = { run: { id: 7, status: 'running', llmCalls: 2 }, messages: [
    { id: 20, seq: 1, role: 'user', content: 'find the bug', created: 5 },
    { id: 21, seq: 2, role: 'assistant', content: 'looking', created: 6 },
  ], steps: [], links: [] };
  const view = {
    run: { id: 1, status: 'waiting_input', result: 'Which **branch**?', pendingState: { kind: 'question', park: 'p9' } },
    messages: [
      { id: 1, seq: 1, role: 'user', content: 'fix it\n\n[attached: notes/plan.md (text/markdown, 2 KB)]', created: 1, sender: 'bob' },
      { id: 2, seq: 2, role: 'user', content: '[subagent results]\n#7 done', created: 2 },
      { id: 3, seq: 3, role: 'assistant', content: 'on it', reasoning: 'hm', reasoningMs: 1500, created: 3, model: 'm-1',
        toolCalls: [call('c1', 'bash', { command: 'ls', summary: 'List files' }), call('c2', 'subagent_spawn', { task: 'find the bug', summary: 'Find the bug' })] },
      { id: 4, seq: 4, role: 'tool', toolCallId: 'c1', content: 'a.txt\n[exit 0 · 1s]', created: 3 },
      { id: 5, seq: 5, role: 'tool', toolCallId: 'c2', content: '(running…)', created: 3 },
    ],
    steps: [{ id: 1, runId: 1, kind: 'compaction', detail: '{}', created: 4 }],
    links: [{ toolCallId: 'c2', childId: 7, state: 'running', label: 'Bug hunt', phase: 'reading' }],
    draft: null,
  };
  const tr = agttTranscript(view, (id) => (id === 7 ? child : null));
  const got = shown(tr, 0);
  assert.deepEqual(got.blocks.map((b) => b.kind), ['user', 'notice', 'thought', 'assistant', 'tool', 'agent', 'step']);
  const [user, notice, thought, , tool, agent] = got.blocks;
  assert.deepEqual(user, { k: 'm1', kind: 'user', text: 'fix it', sender: 'bob', ref: 1, files: [{ name: 'plan.md', path: 'notes/plan.md', mime: 'text/markdown', sizeText: '2 KB' }] });
  assert.equal(notice.text, '[subagent results]\n#7 done');
  assert.deepEqual([thought.ms, thought.live], [1500, undefined]);
  assert.deepEqual(tool.rec, { id: 'c1', kind: 'execute', title: 'List files', name: 'bash', status: 'completed', rawInput: { command: 'ls' }, text: 'a.txt\n[exit 0 · 1s]' });
  assert.equal(tool.state, 'done');
  assert.deepEqual({ ...agent, blocks: agent.blocks.map((b) => b.kind) },
    { k: 'cc2', kind: 'agent', title: 'Bug hunt', ref: 7, state: 'running', phase: 'reading', steps: 2, task: 'find the bug', blocks: ['assistant'] });
  assert.deepEqual(got.tail, [{ k: 'park:p9', kind: 'ask', park: 'p9', message: 'Which **branch**?', mode: 'text' }]);
  for (const b of got.blocks) assert.ok(KINDS.includes(b.kind) || b.kind === 'step');

  // an approval of its own calls: one request for all of them
  const parked = agttTranscript({ ...view, run: { id: 1, status: 'waiting_input', pendingState: { kind: 'approval', park: 'p2', toolCalls: [call('c3', 'bash', {}), call('c4', 'write_file', {})] } } });
  assert.deepEqual(shown(parked, 0).tail[0], { k: 'park:p2', kind: 'perm', park: 'p2', tool: { title: 'bash, write_file', kind: 'other' }, calls: ['bash', 'write_file'],
    options: [{ optionId: 'approve', name: 'Approve', kind: 'allow_once' }, { optionId: 'deny', name: 'Deny', kind: 'reject_once' }] });
});

test('a stored call id and a call\'s arguments read back as the agent\'s own', () => {
  assert.deepEqual(['h1:t1', 'h12:t1#3', 't1'].map(callIdOf), ['t1', 't1', 't1']);
  assert.deepEqual(rawInputOf({ cmd: 'ls', summary: 'run ls' }), { cmd: 'ls' });
  assert.deepEqual(rawInputOf({ summary: 'run ls', rawSummary: "the agent's own" }), { summary: "the agent's own" });
  assert.equal(rawInputOf({ summary: 'x', rawInput: 'plain text' }), 'plain text');
  assert.equal(rawInputOf({ summary: 'x' }), null);
  assert.equal(rawInputOf(null), null);
});

test('the adapter hands the fold\'s own blocks on: a cached fold keeps what did not change', () => {
  // (what model/fold.js's adapter will hang versions on: Versions.of(k, the fold's block))
  const t = all.find((x) => x.name === 'tools');
  const cache = new FoldCache();
  const a = fold(tileView(t.agtt.views[2].view), () => null, 0, cache);
  const b = fold(tileView(t.agtt.views[2].view), () => null, 0, cache);
  assert.equal(a.length, b.length);
  assert.ok(plain(a).length > 0);
});
