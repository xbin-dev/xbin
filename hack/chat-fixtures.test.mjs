// hack/chat-fixtures.test.mjs — the shared chat transcripts
// (sdk/acp/testdata/transcripts) against the Agent tab's fold, run by
// `make js-test`. Each <name>.json is an event log in xbind's served shape;
// beside it are its expectations in the chat kit's block model
// (/vendor/chat/blocks.js): <name>.records.json — its tool calls as
// ToolRecords — and <name>.blocks.json — its transcript after each
// checkpoint. This test holds
//   - the expectations to what hack/chat-fixtures/expect.mjs makes of the
//     log (so they cannot be edited by hand out of step with the rules);
//   - web/agent-fold.js to them, through its adapter
//     (hack/chat-fixtures/web.mjs) and the gaps expect.mjs lists for it — a
//     call's record is the kit's own (web/chat/tools.js mergeTool), kept by
//     the fold;
//   - the record's rules (web/chat/tools.js) where no log reaches them, and
//     the model's helpers (web/chat/blocks.js).
// The agent template's side is hack/chat-fixtures-agtt.test.mjs.
//
// Regenerating, in this order (review each diff):
//   1. the logs — when sdk/acp's events or acptest's scripts changed:
//        (cd sdk && ACP_TRANSCRIPTS_WRITE=1 go test ./acp/acptest -run TestTranscripts -count=1)
//      (xbind-shell.json is the native capture's events: copy them again
//      when that capture is regenerated)
//   2. the template backend's views (<name>.agtt.json):
//        CHAT_TRANSCRIPTS_WRITE=1 TILE_TEST_FLAGS="-count=1 -run TestChatTranscripts" ./hack/tile-check.sh agent
//   3. the expectations:
//        CHAT_FIXTURES_WRITE=1 node --test hack/chat-fixtures.test.mjs
// The files are plain JSON (no JS-only values): the Swift port's parity
// tests and sdk/acp's tool record read them too.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { Fold } from '../web/agent-fold.js';
import * as model from '../web/chat/blocks.js';
import { KIT } from '../web/chat/rev.js';
import { records, ToolRecords, mergeTool, startsNewCall, lineCounts, textLines, OUTPUT_MAX } from '../web/chat/tools.js';
import { DIR, load, expectedFiles, shown, through, json, canon, each, compact, GAPS } from './chat-fixtures/expect.mjs';
import { webTranscript } from './chat-fixtures/web.mjs';

const WRITE = !!process.env.CHAT_FIXTURES_WRITE;
const all = load();

test('the transcripts are there, each with its checkpoints', () => {
  assert.deepEqual(all.map((t) => t.name), ['id-reuse', 'output', 'permission', 'plan', 'questions', 'signin', 'text', 'tools', 'xbind-shell']);
  for (const t of all) {
    assert.equal(t.log.name, t.name);
    assert.deepEqual(t.log.events.map((e) => e.seq), t.log.events.map((_, i) => i + 1), `${t.name}: seqs count from 1`);
    assert.equal(t.log.checkpoints.at(-1), t.log.events.length, `${t.name}: the last event is a checkpoint`);
    for (const e of t.log.events) assert.deepEqual(Object.keys(e), ['seq', 'ts', 'type', 'data'], `${t.name}: an event is {seq, ts, type, data}`);
  }
});

test('xbind-shell.json holds the native capture\'s events', () => {
  const native = JSON.parse(readFileSync(path.join(DIR, '../../../../native/ios/Packages/XbinAgent/Tests/XbinAgentTests/Fixtures/shell.json'), 'utf8'));
  assert.deepEqual(all.find((t) => t.name === 'xbind-shell').log.events, native.events.map((e) => ({ seq: e.seq, ts: e.ts, type: e.type, data: e.data })),
    'the capture was regenerated: copy its events into sdk/acp/testdata/transcripts/xbind-shell.json, then regenerate the expectations (this file\'s header)');
});

for (const t of all) {
  test(`${t.name}: the expectations are what the rules make of the log`, () => {
    const want = expectedFiles(t);
    if (WRITE) {
      writeFileSync(path.join(DIR, `${t.name}.records.json`), json(want.records));
      writeFileSync(path.join(DIR, `${t.name}.blocks.json`), json(want.blocks));
      return;
    }
    assert.ok(t.records && t.blocks, `${t.name}.records.json / .blocks.json are missing — see this file's header`);
    assert.deepEqual(t.records, JSON.parse(json(want.records)), `${t.name}.records.json is stale — regenerate (this file's header)`);
    assert.deepEqual(t.blocks, JSON.parse(json(want.blocks)), `${t.name}.blocks.json is stale — regenerate (this file's header)`);
  });

  test(`${t.name}: the Agent tab's fold makes the expected blocks`, { skip: WRITE }, () => {
    for (const cp of t.blocks.checkpoints) {
      const fold = new Fold();
      for (const e of t.log.events) if (e.seq <= cp.seq) fold.push(e); // one event at a time, as the tab folds
      const got = shown(webTranscript(fold), cp.seq);
      assert.deepEqual(through('web', got, 'actual'), through('web', cp, 'expected'), `${t.name} at seq ${cp.seq}`);
    }
  });

  test(`${t.name}: keys are unique and each is the seq of the event that opened its block`, { skip: WRITE }, () => {
    const types = new Map(t.log.events.map((e) => [String(e.seq), e.type]));
    const opens = { user: 'message.delta', assistant: 'message.delta', thought: 'thought.delta', tool: ['tool.call', 'tool.update'], plan: 'plan', perm: 'permission.request',
      ask: 'elicitation.request', turn: 'turn.end', changes: 'files.changed', notice: 'notice' };
    for (const cp of t.blocks.checkpoints) {
      const seen = new Set();
      each(cp, (b) => {
        assert.ok(!seen.has(b.k), `${t.name} at ${cp.seq}: key ${b.k} twice`);
        seen.add(b.k);
        if (b.kind === 'signin') return; // (not a row of the log: the last status asks for it)
        assert.ok([].concat(opens[b.kind]).includes(types.get(b.k)), `${t.name} at ${cp.seq}: ${b.kind} ${b.k} is keyed by a ${types.get(b.k)} event`);
      });
    }
  });

  test(`${t.name}: a block keeps its key from checkpoint to checkpoint, and its version moves only with it`, { skip: WRITE }, () => {
    const fold = new Fold();
    let before = new Map();
    for (const e of t.log.events) {
      fold.push(e);
      const now = new Map();
      model.walk(webTranscript(fold).blocks, (b) => now.set(b.k, { v: b.v, s: JSON.stringify(compact(model.plain({ ...b, v: 0, children: null }))) }));
      for (const [k, was] of before) {
        const is = now.get(k);
        if (!is) continue;
        if (was.s !== is.s) assert.notEqual(is.v, was.v, `${t.name} seq ${e.seq}: block ${k} changed, its version did not`);
      }
      before = now;
    }
  });
}

test('the transcripts reach every kind the Agent tab makes, and every state of a call', { skip: WRITE }, () => {
  const kinds = new Set(), states = new Set(), toolKinds = new Set();
  for (const t of all) for (const cp of t.blocks.checkpoints) each(cp, (b) => { kinds.add(b.kind); if (b.kind === 'tool') { states.add(b.state); toolKinds.add(b.rec.kind); } });
  // (draft and agent are the agent template's; notice and gap are xbind's own lines — no log here has one: hack/agent-fold.test.mjs folds them)
  assert.deepEqual([...kinds].sort(), ['ask', 'assistant', 'changes', 'perm', 'plan', 'signin', 'thought', 'tool', 'turn', 'user']);
  assert.deepEqual([...states].sort(), ['approval', 'done', 'error', 'pending', 'running']);
  assert.deepEqual([...toolKinds].sort(), [...model.TOOL_KINDS].sort());
});

test('the expectations are plain JSON a port can read', { skip: WRITE }, () => {
  for (const t of all) {
    for (const r of t.records.records) {
      assert.deepEqual(Object.keys(r), ['seq', 'turn', 'rec']);
      for (const key of ['id', 'kind', 'title', 'status']) assert.equal(typeof r.rec[key], 'string', `${t.name}: a record always says its ${key}`);
      assert.ok(model.TOOL_KINDS.includes(r.rec.kind) && model.TOOL_STATUS.includes(r.rec.status));
      // a record filled back to every field is a ToolRecord: nothing the model doesn't name
      assert.deepEqual(Object.keys({ ...model.newToolRecord(''), ...r.rec }), Object.keys(model.newToolRecord('')));
    }
    for (const cp of t.blocks.checkpoints) {
      each(cp, (b) => {
        assert.ok(model.KINDS.includes(b.kind), `${t.name}: ${b.kind} is a kind of the model`);
        assert.equal(typeof b.k, 'string');
        if (b.kind === 'tool') assert.ok(model.TOOL_STATES.includes(b.state));
      });
    }
  }
});

test('every gap says why, and each of the Agent tab\'s is one the transcripts show', { skip: WRITE }, () => {
  for (const side of Object.keys(GAPS)) for (const g of GAPS[side]) assert.ok(g.id && g.why && (g.both || g.expected || g.actual), `${side} ${g.id}`);
  // without a gap, some checkpoint differs: none is listed that today's fold has closed
  for (const g of GAPS.web) {
    const differs = all.some((t) => t.blocks.checkpoints.some((cp) => {
      const got = shown(webTranscript(new Fold(t.log.events.filter((e) => e.seq <= cp.seq))), cp.seq);
      return canon(through('web', got, 'actual', g.id)) !== canon(through('web', cp, 'expected', g.id));
    }));
    assert.ok(differs, `gap ${g.id} (${g.why}) is closed: delete it from hack/chat-fixtures/expect.mjs`);
  }
});

// ---- the record's rules where no log reaches them -----------------------------------

const ev = (seq, type, data) => ({ seq, ts: seq, type, data });

test('a content list without diffs keeps the diffs — in the Agent tab\'s record too (its card still draws the last list whole)', () => {
  const events = [
    ev(1, 'tool.call', { id: 'e', kind: 'edit', status: 'in_progress', content: [{ type: 'diff', path: 'a.txt', oldText: 'a\n', newText: 'b\n' }] }),
    ev(2, 'tool.update', { id: 'e', status: 'completed', content: [{ type: 'content', content: { type: 'text', text: 'edited' } }] }),
  ];
  const [r] = records(events);
  assert.deepEqual([r.rec.text, r.rec.diffs.length, r.rec.files], ['edited', 1, ['a.txt']]);
  const fold = new Fold(events);
  const web = webTranscript(fold).blocks[0].rec;
  assert.deepEqual(web, r.rec, 'the fold keeps the kit\'s record');
  assert.deepEqual(fold.blocks[0].content.map((it) => it.type), ['content'], 'the old record holds the last list: the card loses the diff until it reads the record');
});

test('the Agent tab\'s record: locations and files, what a call changed on disk, one output text', () => {
  const fold = new Fold([
    ev(1, 'tool.call', { id: 'r', kind: 'read', status: 'in_progress', title: 'Read a.go', locations: [{ path: 'a.go', line: 3 }, { path: 'b.go' }, {}] }),
    ev(2, 'tool.update', { id: 'r', outputDelta: 'x' }),
    ev(3, 'tool.update', { id: 'r', outputDelta: 'y', status: 'completed' }),
    ev(4, 'files.changed', { toolCallId: 'r', changes: [{ path: 'a.go', status: 'modified', add: 1, del: 0 }], patch: { text: 'p', truncated: true } }),
  ]);
  const t = fold.blocks[0];
  assert.deepEqual([t.rec.locations, t.rec.files], [[{ path: 'a.go', line: 3 }, { path: 'b.go' }], ['a.go', 'b.go']]);
  assert.deepEqual(t.rec.changed, { files: [{ path: 'a.go', status: 'modified', add: 1, del: 0 }], patch: { text: 'p', truncated: true } });
  assert.deepEqual([t.output, t.rec.output], ['xy', 'xy']);
  // an event that carries a whole output and a delta: the whole output, then the delta — one text for both records (the transcript `output` pins it for sdk/acp too)
  const both = new Fold([ev(1, 'tool.call', { id: 'o', kind: 'execute', outputDelta: 'a' }), ev(2, 'tool.update', { id: 'o', output: 'W', outputDelta: 'd' }), ev(3, 'tool.update', { id: 'o', outputDelta: '!' })]).blocks[0];
  assert.deepEqual([both.output, both.rec.output], ['Wd!', 'Wd!']);
  assert.equal(all.find((x) => x.name === 'output').records.records[0].rec.output, '$ make build\ncompiling\nlinking\ndone\n');
});

test('an id used again: after its call ended, or in a later turn (each reducer has one half today)', () => {
  // within one turn, after the first call ended: the template's backend starts a new call, the Agent tab's fold updates the old one
  const again = [ev(1, 'tool.call', { id: 'x', status: 'in_progress', title: 'one' }), ev(2, 'tool.update', { id: 'x', status: 'completed' }), ev(3, 'tool.call', { id: 'x', status: 'in_progress', title: 'two' })];
  assert.deepEqual(records(again).map((r) => [r.seq, r.rec.title, r.rec.status]), [[1, 'one', 'completed'], [3, 'two', 'in_progress']]);
  const held = new ToolRecords();
  assert.deepEqual(again.map((e) => held.apply(e).seq), [1, 1, 3], 'apply answers the entry an event touched');
  assert.equal(held.apply(ev(4, 'message.delta', { text: 'hi' })), null);
  assert.equal(webTranscript(new Fold(again)).blocks.length, 1, 'today the Agent tab folds both into one card');
  // in a later turn, the first call never ended: the Agent tab's fold starts a new call, the template's backend updates the old one
  const later = [ev(1, 'tool.call', { id: 'x', status: 'in_progress', title: 'one' }), ev(2, 'turn.end', { turn: 1, stopReason: 'end_turn' }), ev(3, 'tool.call', { id: 'x', status: 'in_progress', title: 'two' })];
  assert.deepEqual(records(later).map((r) => [r.seq, r.turn, r.rec.title]), [[1, 1, 'one'], [3, 2, 'two']]);
  assert.equal(webTranscript(new Fold(later)).blocks.filter((b) => b.kind === 'tool').length, 2);
  // an update never starts a call
  assert.equal(startsNewCall({ status: 'completed' }, 'tool.update'), false);
});

test('xbind\'s own lines — a notice, a gap — are blocks of the model (no agent\'s log has one: xbind writes them)', () => {
  const tr = webTranscript(new Fold([ev(1, 'gap', {}), ev(2, 'notice', { text: 'The session moved\nto a new base' }), ev(3, 'message.delta', { role: 'agent', text: 'hi' })]));
  assert.deepEqual(shown(tr, 3).blocks, [{ k: '1', kind: 'gap' }, { k: '2', kind: 'notice', text: 'The session moved\nto a new base' }, { k: '3', kind: 'assistant', text: 'hi' }]);
  for (const b of tr.blocks) assert.ok(model.KINDS.includes(b.kind));
});

test('output: deltas append, a whole output replaces, a capping reducer keeps the newest bytes', () => {
  const rec = model.newToolRecord('o');
  mergeTool(rec, { outputDelta: 'a' });
  mergeTool(rec, { outputDelta: 'b' });
  assert.equal(rec.output, 'ab');
  mergeTool(rec, { output: 'whole', exitCode: 1 });
  assert.deepEqual([rec.output, rec.exitCode, rec.outputTruncated], ['whole', 1, 0]);
  const big = model.newToolRecord('big');
  mergeTool(big, { outputDelta: 'é'.repeat(OUTPUT_MAX) }, { cap: true }); // 2 bytes each
  assert.deepEqual([big.output.length, big.outputTruncated], [OUTPUT_MAX / 2, OUTPUT_MAX]);
});

test('a diff\'s counts are its changed lines', () => {
  assert.deepEqual(lineCounts(textLines('hello\n'), textLines('hello, world\n')), { add: 1, del: 1 });
  assert.deepEqual(lineCounts(textLines('a\nb\nc\n'), textLines('a\nc\nd\n')), { add: 1, del: 1 });
  assert.deepEqual(lineCounts([], textLines('x\ny')), { add: 2, del: 0 });
  assert.deepEqual(lineCounts(textLines('same'), textLines('same')), { add: 0, del: 0 });
});

// ---- the model's helpers --------------------------------------------------------------

test('blocks.js: the kit\'s revision, kinds and a record\'s zero value', () => {
  assert.equal(KIT, 3);
  assert.equal(model.KINDS.length, 15);
  const rec = model.newToolRecord('t');
  assert.deepEqual(compact(rec), { id: 't', kind: 'other', title: '', status: 'pending' });
  assert.deepEqual(['pending', 'in_progress', 'completed', 'failed', 'cancelled'].map((status) => model.toolState({ status })), ['pending', 'running', 'done', 'error', 'stopped']);
  assert.equal(model.toolState({ status: 'pending' }, 'approval'), 'approval');
  assert.equal(model.toolState({ status: 'completed' }, 'approval'), 'done', 'a hint holds only while the call is not over');
});

test('blocks.js: bump moves a block\'s version and its parents\'; plain drops what is not data', () => {
  const sub = model.block('tool', 1, { rec: { ...model.newToolRecord('task'), subagent: true }, state: 'running', children: [] });
  const thought = model.adopt(sub, model.block('thought', 2, { text: 'hm', live: true, ms: 0 }));
  assert.deepEqual([sub.v, thought.v, thought.parent, thought.k], [1, 0, 'task', '2']);
  model.bump(thought);
  assert.deepEqual([sub.v, thought.v], [2, 1]);
  assert.equal(model.find([sub], '2'), thought);
  thought.$memo = { html: '<p>' };
  const p = model.plain([sub]);
  assert.equal(JSON.stringify(p).includes('$'), false);
  assert.deepEqual(JSON.parse(JSON.stringify(p)), p, 'no cycle, nothing JSON loses');
});

test('blocks.js: nest puts a subagent\'s blocks under its call, leaves an orphan flat', () => {
  const tool = (k, id, parent = '') => model.block('tool', k, { rec: { ...model.newToolRecord(id), parent }, state: 'done', children: null, parent });
  const flat = [model.block('user', 'u', { text: 'go' }), tool('a', 'task1'), model.block('thought', 't', { text: '…', parent: 'task1' }), tool('b', 'read1', 'task1'),
    tool('c', 'lost1', 'gone'), model.block('assistant', 'm', { text: 'done' })];
  const out = model.nest(flat);
  assert.deepEqual(out.map((b) => b.k), ['u', 'a', 'c', 'm']);
  assert.deepEqual(out[1].children.map((b) => b.k), ['t', 'b']);
  assert.equal(out[2].parent, 'gone', 'an orphan keeps its parent: a view marks it');
  model.bump(out[1].children[1]);
  assert.equal(out[1].v, 1, 'a nested block\'s change is its parent\'s');
  const seen = [];
  model.walk(out, (b, depth) => seen.push(b.k + depth));
  assert.deepEqual(seen, ['u0', 'a0', 't1', 'b1', 'c0', 'm0']);
  assert.equal(flat[1].children, null, 'the flat list\'s blocks are as they were');
  assert.deepEqual(model.nest(flat)[1].children.map((b) => b.k), ['t', 'b'], 'nested again: the same, not twice');
  const plainList = [model.block('user', 'u2', { text: 'hi' })];
  assert.equal(model.nest(plainList), plainList, 'nothing to nest: the list itself');
});

test('blocks.js: Versions moves a key\'s version when what it was made from is another object', () => {
  const vs = new model.Versions();
  const row = { id: 1 }, same = row, next = { id: 1 };
  assert.deepEqual([vs.of('m1', row), vs.of('m1', same), vs.of('m1', next), vs.of('m1', next), vs.of('m2', row)], [0, 0, 1, 1, 0]);
  vs.sweep(new Set(['m2']));
  assert.equal(vs.of('m1', next), 0, 'a forgotten key starts over');
});
