// hack/agent-pages.test.mjs — the pages of an agent session's log a client
// holds (web/agent-pages.js, D130), against a fake server that cuts pages at
// turn starts (the Go side's safe cuts are tested in internal/agent): the
// pages fold to exactly the whole-log blocks, with the same keys; pages
// unload and come back; the live tail detaches and returns; a late event
// refolds only its page; an xbind that does not page still works.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Fold } from '../web/agent-fold.js';
import { Transcript } from '../web/agent-pages.js';

const strip = (blocks) => JSON.parse(JSON.stringify(blocks, (k, v) => (['v', 'up', '$memo', 'cur'].includes(k) ? undefined : v)));

let seq = 0;
const ev = (type, data) => ({ seq: ++seq, ts: seq * 10, type, data });
function turns(n, from = 1) {
  const out = [];
  for (let i = from; i < from + n; i++) {
    out.push(ev('message.delta', { role: 'user', text: `prompt ${i}` }),
      ev('status', { status: 'running', ...(i === 1 ? { options: [{ id: 'model' }], commands: [{ name: 'review' }] } : {}) }),
      ev('thought.delta', { text: 'hm' }),
      ev('message.delta', { role: 'agent', messageId: `m${i}`, text: 'a' }), ev('message.delta', { role: 'agent', messageId: `m${i}`, text: 'b' }),
      ev('tool.call', { id: 't', title: 'ls', kind: 'execute', status: 'in_progress' }),
      ev('tool.update', { id: 't', outputDelta: 'x\n' }), ev('tool.update', { id: 't', status: 'completed', exitCode: 0 }),
      ev('turn.end', { turn: i, stopReason: 'end_turn', usage: { used: i, size: 100 } }),
      ev('status', { status: 'idle' }));
  }
  return out;
}

// the fold's state before events[0..i) as the server's header gives it
function stateBefore(events) {
  const st = new Fold(events).st;
  const last = [...events].reverse().find((e) => e.type === 'status');
  const turnEnd = [...events].reverse().find((e) => e.type === 'turn.end');
  const status = last ? { ...last.data } : null;
  if (status) for (const k of ['status', 'detail', 'currentMode', 'options', 'modes', 'commands']) if (st[k]) status[k] = st[k];
  return { status, usage: st.usage || undefined, turn: turnEnd ? turnEnd.data.turn : 0 };
}

// a fake events route over `log` (a live array): pages cut at turn starts
function server(log, { paged = true } = {}) {
  const calls = [];
  const get = async (q) => {
    calls.push(q);
    const p = new URLSearchParams(q);
    if (!paged || p.has('since')) {
      const since = Number(p.get('since') || 0);
      const events = log.filter((e) => e.seq > since);
      return { events, next: events.length ? events[events.length - 1].seq : since, truncated: false };
    }
    const before = Number(p.get('before') || 0), limit = Number(p.get('limit') || 200);
    let hi = log.length;
    while (before && hi > 0 && log[hi - 1].seq >= before) hi--;
    const safe = (i) => i === 0 || (log[i].type === 'message.delta' && log[i].data.role === 'user');
    let lo = -1;
    for (let i = Math.max(0, hi - limit); i < hi && lo < 0; i++) if (safe(i)) lo = i;
    for (let i = hi - limit - 1; lo < 0 && i >= 0; i--) if (safe(i)) lo = i;
    lo = Math.max(lo, 0);
    const events = log.slice(lo, hi);
    return { events, hasOlder: lo > 0, nextBefore: lo > 0 ? log[lo].seq : 0, truncated: false, next: hi ? log[hi - 1].seq : 0,
      last: log.length ? log[log.length - 1].seq : 0, state: stateBefore(log.slice(0, lo)) };
  };
  return { get, calls };
}

test('block keys are the seqs of the events that open them', () => {
  seq = 0;
  const log = turns(2);
  const f = new Fold(log);
  const bySeq = new Map(log.map((e) => [e.seq, e]));
  for (const b of f.blocks) {
    const e = bySeq.get(b.key);
    assert.ok(e, `block ${b.kind} keyed ${b.key}: no such event`);
    const want = { msg: 'message.delta', thought: 'thought.delta', tool: 'tool.call', turn: 'turn.end' }[b.kind];
    assert.equal(e.type, want, `a ${b.kind} is keyed by its first event`);
  }
  // an event without a seq still gets a unique key
  const g = new Fold([{ type: 'gap', data: {} }, { type: 'gap', data: {} }]);
  assert.ok(g.blocks[0].key < 0 && g.blocks[0].key !== g.blocks[1].key);
});

test('a fold from a page and its state header is the whole fold, cut at every turn start', () => {
  seq = 0;
  const log = turns(6);
  const whole = new Fold(log);
  for (let i = 1; i < log.length; i++) {
    if (!(log[i].type === 'message.delta' && log[i].data.role === 'user')) continue;
    const head = new Fold(log.slice(0, i));
    const tail = new Fold().seed(stateBefore(log.slice(0, i)));
    for (const e of log.slice(i)) tail.push(e);
    assert.deepEqual(strip([...head.blocks, ...tail.blocks]), strip(whole.blocks), `cut before ${i}`);
    assert.deepEqual({ ...tail.st, last: null }, { ...whole.st, last: null }, `status digest after a cut before ${i}`);
    assert.equal(tail.turn, whole.turn);
  }
});

test('the tail, then older pages: the whole log, same keys; a page dropped comes back the same', async () => {
  seq = 0;
  const log = turns(30); // 300 events
  const srv = server(log);
  const tx = new Transcript(srv.get);
  await tx.openTail(40);
  assert.deepEqual(srv.calls, ['limit=40'], 'open reads the tail page only');
  assert.ok(tx.hasOlder && tx.segs.length === 1 && tx.segs[0].events.length <= 40);
  assert.equal(tx.lastSeq, 300);
  assert.deepEqual(tx.st.options, [{ id: 'model' }], 'the state header carries the options sent long before the tail');
  assert.deepEqual(tx.st.usage, { used: 30, size: 100 });
  while (tx.hasOlder) await tx.loadOlder(40);
  const whole = new Fold(log);
  assert.deepEqual(strip(tx.blocks()), strip(whole.blocks));
  assert.ok(tx.segs.length >= 8);
  // drop the pages above block 100; they load back identical
  const keys = tx.blocks().map((b) => b.key);
  assert.ok(tx.keep(100, 1e9, false));
  assert.ok(tx.hasOlder && tx.blocks()[0].key > keys[0]);
  while (tx.hasOlder) await tx.loadOlder(40);
  assert.deepEqual(tx.blocks().map((b) => b.key), keys);
  assert.deepEqual(strip(tx.blocks()), strip(whole.blocks));
});

test('the live tail detaches far up, counts what is new, and comes back', async () => {
  seq = 0;
  const log = turns(20);
  const srv = server(log);
  const tx = new Transcript(srv.get);
  await tx.openTail(30);
  while (tx.hasOlder) await tx.loadOlder(30);
  const n = tx.blocks().length;
  // the reader is at the top: the tail may go
  assert.equal(tx.keep(0, 20, false), false, 'following: the tail stays');
  assert.ok(tx.keep(0, 20, true));
  assert.ok(tx.detached && tx.below.length >= 1 && tx.blocks().length < n);
  // two turns stream meanwhile: the digest moves, the count grows, nothing folds
  tx.follow = false;
  const more = turns(2, 21);
  log.push(...more);
  const before = tx.blocks();
  assert.ok(tx.apply(more));
  assert.deepEqual(tx.blocks(), before, 'detached: nothing folds');
  assert.equal(tx.fresh, 2 * 5, 'each turn opens five entries: prompt, thought, answer, call, end');
  assert.equal(tx.lastSeq, log[log.length - 1].seq);
  assert.deepEqual(tx.st.usage, { used: 22, size: 100 });
  // scrolling down brings the pages back, the old tail last — live again
  while (tx.hasNewer) await tx.loadNewer(30);
  assert.ok(!tx.detached && tx.below.length === 0);
  assert.deepEqual(strip(tx.blocks()), strip(new Fold(log).blocks));
  const next = ev('message.delta', { role: 'user', text: 'live again' });
  tx.apply([next]);
  assert.equal(tx.blocks()[tx.blocks().length - 1].text, 'live again');
  tx.seenAll();
  assert.equal(tx.fresh, 0);
});

test('a late event refolds only its page', async () => {
  seq = 0;
  const log = turns(10);
  const missing = log.find((e) => e.type === 'tool.update' && e.data.outputDelta && e.seq > 80); // in the tail page
  const srv = server(log.filter((e) => e !== missing));
  const tx = new Transcript(srv.get);
  await tx.openTail(30);
  while (tx.hasOlder) await tx.loadOlder(30);
  const [first, ...rest] = tx.segs;
  const firstBlocks = first.fold.blocks.slice();
  assert.ok(rest.length >= 1);
  assert.ok(tx.apply([missing]));
  assert.deepEqual(first.fold.blocks, firstBlocks);
  first.fold.blocks.forEach((b, i) => assert.equal(b, firstBlocks[i], 'the other pages keep their block objects'));
  assert.deepEqual(strip(tx.blocks()), strip(new Fold(log).blocks), 'the late output landed in its call');
});

test('a live tail grown past a few pages splits where the server cuts, without refolding', async () => {
  seq = 0;
  const log = turns(3);
  const srv = server(log);
  const tx = new Transcript(srv.get);
  await tx.openTail(30);
  while (tx.hasOlder) await tx.loadOlder(30);
  const more = turns(12, 4);
  log.push(...more);
  tx.apply(more);
  const tail = tx.segs[tx.segs.length - 1];
  assert.ok(tail.events.length > 90);
  const objs = tx.blocks().slice();
  assert.ok(await tx.splitTail(30));
  assert.deepEqual(tx.blocks(), objs, 'the same block objects, in order');
  assert.ok(tx.segs[tx.segs.length - 1].events.length <= 30);
  // the split-off part unloads and loads back like any page
  assert.ok(tx.keep(tx.blocks().length - 5, 1e9, false));
  while (tx.hasOlder) await tx.loadOlder(30);
  assert.deepEqual(strip(tx.blocks()), strip(new Fold(log).blocks));
});

test('an xbind that does not page: everything, one segment, nothing unloads', async () => {
  seq = 0;
  const log = turns(8);
  const srv = server(log, { paged: false });
  const tx = new Transcript(srv.get);
  await tx.openTail(20);
  assert.ok(!tx.paged && !tx.hasOlder && tx.segs.length === 1);
  assert.deepEqual(strip(tx.blocks()), strip(new Fold(log).blocks));
  assert.equal(tx.keep(50, 60, true), false);
  assert.equal(await tx.loadOlder(), false);
  assert.equal(tx.statusAfter(0), 'idle');
  assert.equal(tx.statusAfter(tx.lastSeq), '');
});
