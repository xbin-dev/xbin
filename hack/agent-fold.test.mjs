// hack/agent-fold.test.mjs — the Agent tab's incremental fold (web/agent-fold.js,
// D124), run by `make js-test`: it must build exactly the blocks the old
// whole-log fold built (kept below as the reference), keep block identity and
// keys stable as events arrive, and bump a block's version only when it (or a
// block nested in it) changed.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { Fold, cached } from '../web/agent-fold.js';
import { newTool, foldTool } from '../web/agent-tools.js';

// bx-agent's _blocks() before D124, verbatim but for `this._events`
function refFold(events) {
  const blocks = [];
  const tools = new Map();
  const byId = new Map();
  const perms = new Map();
  const asks = new Map();
  let plan = null, cur = null, turn = 0;
  for (const e of events) {
    const d = e.data || {};
    if (cur && cur.kind === 'thought' && e.type !== 'thought.delta' && e.type !== 'status' && e.type !== 'files.changed') { cur.t1 = e.ts || cur.t1; cur.done = true; }
    switch (e.type) {
      case 'message.delta': case 'thought.delta': {
        const box = d.parent ? byId.get(d.parent) : null;
        const into = box && box.children ? box : null;
        const list = into ? into.children : blocks;
        let c = into ? into.cur : cur;
        if (c && c.kind === 'thought' && e.type !== 'thought.delta') { c.t1 = e.ts || c.t1; c.done = true; }
        if (e.type === 'thought.delta') {
          if (c && c.kind === 'thought') { c.text += d.text || ''; c.t1 = e.ts || c.t1; }
          else { c = { kind: 'thought', text: d.text || '', t0: e.ts || 0, t1: e.ts || 0, done: false }; list.push(c); }
        } else {
          const role = d.role || 'agent';
          if (c && c.kind === 'msg' && c.role === role && c.mid === (d.messageId || '') && !c.files && !d.attachments) c.text += d.text || '';
          else { c = { kind: 'msg', role, mid: d.messageId || '', text: d.text || '', files: d.attachments }; list.push(c); }
        }
        if (into) into.cur = c; else cur = c;
        break;
      }
      case 'tool.call': case 'tool.update': {
        const tkey = turn + '/' + d.id;
        let t = tools.get(tkey);
        const box = d.parent ? byId.get(d.parent) : null;
        const into = box && box.children && box !== t ? box : null;
        if (into) { if (into.cur && into.cur.kind === 'thought') into.cur.done = true; into.cur = null; } else cur = null;
        if (!t) { t = newTool(d.id); t.t0 = e.ts || 0; tools.set(tkey, t); (into ? into.children : blocks).push(t); }
        foldTool(t, d);
        byId.set(d.id, t);
        if (t.children && t.status !== 'pending' && t.status !== 'in_progress') {
          for (const ch of t.children) if (ch.kind === 'thought') ch.done = true;
        }
        break;
      }
      case 'files.changed': {
        const f = { changes: d.changes || [], patch: d.patch || null };
        if (d.toolCallId) { const t = tools.get(turn + '/' + d.toolCallId) || byId.get(d.toolCallId); if (t) t.files = f; break; }
        const blk = { kind: 'changes', turn: d.turn, ...f };
        const at = blocks.findLastIndex((b) => b.kind === 'turn' && b.turn === d.turn);
        if (at >= 0) blocks.splice(at, 0, blk); else blocks.push(blk);
        break;
      }
      case 'plan':
        cur = null;
        if (!plan) { plan = { kind: 'plan', entries: [] }; blocks.push(plan); }
        plan.entries = d.entries || [];
        break;
      case 'permission.request': {
        cur = null;
        const p = { kind: 'perm', pid: d.pid, tool: d.toolCall || {}, options: d.options || [], rule: d.rule || null, meta: d.meta || null, by: null, optionId: null };
        perms.set(d.pid, p); blocks.push(p);
        break;
      }
      case 'elicitation.request': {
        cur = null;
        const q = { kind: 'ask', eid: d.eid, toolCallId: d.toolCallId || '', message: d.message || '', schema: d.schema || null, action: null, by: null, content: null };
        asks.set(d.eid, q); blocks.push(q);
        break;
      }
      case 'elicitation.resolved': { const q = asks.get(d.eid); if (q) { q.action = d.action; q.by = d.by; q.content = d.content || null; } break; }
      case 'permission.resolved': { const p = perms.get(d.pid); if (p) { p.by = d.by; p.optionId = d.optionId; } break; }
      case 'turn.end':
        if (cur && cur.kind === 'thought') cur.done = true;
        cur = null; plan = null;
        turn = (d.turn || turn) + 0.5;
        blocks.push({ kind: 'turn', turn: d.turn, stopReason: d.stopReason, usage: d.usage, error: d.error });
        break;
      case 'gap': cur = null; blocks.push({ kind: 'gap' }); break;
    }
  }
  return blocks;
}

const strip = (blocks) => JSON.parse(JSON.stringify(blocks, (k, v) => (['key', 'v', 'up', '$memo', 'cur'].includes(k) ? undefined : v)));

let seq = 0;
const ev = (type, data, ts) => ({ seq: ++seq, ts: ts ?? seq * 100, type, data });
const synthetic = () => {
  seq = 0;
  return [
    ev('status', { status: 'running', currentMode: 'default', commands: [{ name: 'review' }], options: [{ id: 'model', type: 'select', options: [] }] }),
    ev('message.delta', { role: 'user', text: 'fix it' }),
    ev('thought.delta', { text: 'hmm ' }), ev('thought.delta', { text: 'let me see' }),
    ev('status', { detail: 'thinking' }),
    ev('message.delta', { role: 'agent', messageId: 'm1', text: 'On ' }), ev('message.delta', { role: 'agent', messageId: 'm1', text: 'it.' }),
    ev('plan', { entries: [{ content: 'a', status: 'pending' }] }),
    ev('tool.call', { id: 't1', title: 'Edit x', kind: 'edit', status: 'pending', content: [{ type: 'diff', path: 'x', oldText: 'a\n', newText: 'b\n' }] }),
    ev('permission.request', { pid: 'p1', toolCall: { title: 'Edit x' }, options: [{ optionId: 'ok', kind: 'allow_once' }] }),
    ev('permission.resolved', { pid: 'p1', by: 'user:me', optionId: 'ok' }),
    ev('tool.update', { id: 't1', status: 'completed' }),
    ev('files.changed', { toolCallId: 't1', changes: [{ path: 'x', status: 'modified', add: 1, del: 1 }] }),
    ev('plan', { entries: [{ content: 'a', status: 'completed' }] }),
    ev('tool.call', { id: 'sub', title: 'Task', kind: 'think', status: 'in_progress', subagent: true, rawInput: { prompt: 'look' } }),
    ev('thought.delta', { parent: 'sub', text: 'inner' }),
    ev('message.delta', { parent: 'sub', role: 'agent', text: 'found' }),
    ev('tool.call', { id: 'r1', parent: 'sub', title: 'Read y', kind: 'read', status: 'in_progress' }),
    ev('tool.update', { id: 'r1', parent: 'sub', status: 'completed' }),
    ev('tool.update', { id: 'sub', status: 'completed' }),
    ev('tool.call', { id: 'run', title: 'ls', kind: 'execute', status: 'in_progress' }),
    ev('tool.update', { id: 'run', outputDelta: 'a\n' }), ev('tool.update', { id: 'run', outputDelta: 'b\n', status: 'completed', exitCode: 0 }),
    ev('elicitation.request', { eid: 'e1', message: 'which?', schema: { type: 'object', properties: {} } }),
    ev('elicitation.resolved', { eid: 'e1', action: 'accept', by: 'user:me', content: {} }),
    ev('turn.end', { turn: 1, stopReason: 'end_turn', usage: { used: 10, size: 100 } }),
    ev('files.changed', { turn: 1, changes: [{ path: 'x', status: 'modified' }], patch: { text: '--- a/x\n+++ b/x\n' } }), // spliced before turn 1's marker
    ev('status', { status: 'idle', login: { needed: false } }),
    ev('message.delta', { role: 'user', text: 'again' }),
    ev('tool.call', { id: 't1', title: 'Edit x again', kind: 'edit', status: 'in_progress' }), // the agent reuses an id in turn 2
    ev('tool.update', { id: 't1', status: 'failed' }),
    ev('gap', {}),
    ev('thought.delta', { text: 'late' }),
    ev('turn.end', { turn: 2, stopReason: 'cancelled' }),
    ev('status', { status: 'idle', modes: [{ id: 'plan' }] }),
  ];
};

const fixtureDir = new URL('../native/ios/Packages/XbinAgent/Tests/XbinAgentTests/Fixtures/', import.meta.url);
const fixtures = () => readdirSync(fixtureDir).filter((f) => f.endsWith('.json')).map((f) => {
  const d = JSON.parse(readFileSync(new URL(f, fixtureDir), 'utf8'));
  const events = Array.isArray(d.events) ? d.events : (d.events && d.events.events) || [];
  return [f, events];
}).filter(([, e]) => e.length);

const logs = () => [['synthetic', synthetic()], ...fixtures()];

test('folds to exactly the blocks the whole-log fold built', () => {
  for (const [name, events] of logs()) {
    assert.deepEqual(strip(new Fold(events).blocks), strip(refFold(structuredClone(events))), name);
  }
});

test('incremental: every prefix matches, block identity and keys stay stable', () => {
  for (const [name, events] of logs()) {
    const f = new Fold();
    const keyOf = new Map();
    events.forEach((e, i) => {
      f.push(e);
      assert.deepEqual(strip(f.blocks), strip(refFold(structuredClone(events.slice(0, i + 1)))), `${name} after event ${i}`);
      const all = [];
      const walk = (bs) => { for (const b of bs) { all.push(b); if (b.children) walk(b.children); } };
      walk(f.blocks);
      for (const b of all) {
        if (keyOf.has(b)) assert.equal(b.key, keyOf.get(b), `${name}: a block's key changed`);
        else keyOf.set(b, b.key);
      }
      assert.equal(new Set(all.map((b) => b.key)).size, all.length, `${name}: keys are unique`);
    });
  }
});

test('a version moves only when its block (or a nested one) changed', () => {
  const events = synthetic();
  const f = new Fold();
  let prev = new Map();
  for (const e of events) {
    const before = new Map([...prev].map(([b]) => [b, JSON.stringify(strip([b]))]));
    f.push(e);
    const now = new Map();
    const walk = (bs) => { for (const b of bs) { now.set(b, b.v); if (b.children) walk(b.children); } };
    walk(f.blocks);
    for (const [b, v] of prev) {
      const changed = JSON.stringify(strip([b])) !== before.get(b);
      if (changed) assert.ok(b.v > v, `${e.type}: a changed ${b.kind} kept its version`);
      else assert.equal(b.v, v, `${e.type}: an unchanged ${b.kind} bumped its version`);
    }
    prev = now;
  }
});

test('the status digest matches a scan of the log', () => {
  for (const [name, events] of logs()) {
    const st = new Fold(events).st;
    let status = '', detail = '', mode = '', options = null, modes = null, commands = null, last = null, usage = null;
    for (const e of events) {
      const d = e.data || {};
      if (e.type === 'status') {
        if (d.status) status = d.status;
        detail = d.detail || detail;
        if (d.currentMode) mode = d.currentMode;
        if (Array.isArray(d.options)) options = d.options;
        if (Array.isArray(d.modes)) modes = d.modes;
        if (Array.isArray(d.commands)) commands = d.commands;
        last = d;
      }
      if (e.type === 'turn.end' && d.usage) usage = d.usage;
    }
    assert.deepEqual({ ...st }, { status, detail, currentMode: mode, options, modes, commands, last, usage }, name);
  }
});

test('reset refolds; lastSeq tracks the newest event', () => {
  const events = synthetic();
  const f = new Fold(events.slice(0, 5));
  assert.equal(f.lastSeq, 5);
  f.reset(events);
  assert.equal(f.lastSeq, events.length);
  assert.deepEqual(strip(f.blocks), strip(refFold(structuredClone(events))));
});

test('cached memoizes per version', () => {
  const b = { v: 0 };
  let calls = 0;
  const fn = () => ++calls;
  assert.equal(cached(b, 'md', fn), 1);
  assert.equal(cached(b, 'md', fn), 1);
  b.v++;
  assert.equal(cached(b, 'md', fn), 2);
  assert.equal(cached(b, 'other', fn), 3);
  assert.equal(cached({}, 'x', fn), 4); // no version: computes
  assert.equal(cached({}, 'x', fn), 5);
});
