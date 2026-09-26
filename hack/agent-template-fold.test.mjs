// hack/agent-template-fold.test.mjs — the agent template's per-block fold()
// cache (builtin-templates/agent/model/fold.js FoldCache,
// plans/agent-template-native.md §4). With a cache fold() must return exactly
// what it returns without one, after any sequence of the updates the Session
// makes (objects replaced, never edited in place), while rebuilding only the
// blocks whose inputs changed and handing back the same objects for the rest.
// Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fold, FoldCache } from '../builtin-templates/agent/model/fold.js';

// --- a transcript as the Session holds it ------------------------------------------

const call = (id, name, args) => ({ id, type: 'function', function: { name, arguments: JSON.stringify(args) } });
let nextId = 1000;
const msg = (runId, role, content, extra = {}) => {
  const id = extra.id || ++nextId;
  return { id, runId, seq: extra.seq ?? id, role, content, created: extra.created ?? 100 + id, ...extra };
};
const step = (runId, kind, detail, created) => ({ id: ++nextId, runId, kind, detail: JSON.stringify(detail), created });

// upsert is session.js's: an update REPLACES the object.
function upsert(list, item) {
  const i = list.findIndex((x) => x.id === item.id);
  if (i >= 0) list[i] = { ...list[i], ...item };
  else list.push(item);
}

function world() {
  const views = new Map();
  const runs = new Map();
  views.set(1, {
    run: { id: 1, status: 'running' },
    messages: [
      msg(1, 'system', 'sys', { id: 1, seq: 0 }),
      msg(1, 'user', 'plan it\n\n[attached: a.png (image/png, 12 B)]', { id: 2, seq: 1, sender: 'alice' }),
      msg(1, 'assistant', 'On it.', { id: 3, seq: 2, reasoning: 'hmm', reasoningMs: 1200,
        toolCalls: [call('c1', 'xbin_call', { method: 'GET', path: '/api/x', summary: 'look' }), call('s1', 'subagent_spawn', { task: 'dig' }),
          call('s2', 'subagent_spawn', { task: 'bg', background: true })] }),
      msg(1, 'tool', 'HTTP 200 ok', { id: 4, seq: 3, toolCallId: 'c1', name: 'xbin_call' }),
      msg(1, 'tool', '(waiting for subagent #2…)', { id: 5, seq: 4, toolCallId: 's1', name: 'subagent_spawn' }),
      msg(1, 'tool', 'started #3', { id: 6, seq: 5, toolCallId: 's2', name: 'subagent_spawn' }),
      msg(1, 'user', '[subagent results]\n#2 done', { id: 7, seq: 6 }),
      msg(1, 'user', 'daily digest', { id: 8, seq: 7, origin: 'schedule', label: 'morning' }),
    ],
    steps: [step(1, 'note', { text: 'started' }, 100), step(1, 'ask', { kind: 'approval' }, 104), step(1, 'spawn', { toolCallId: 's2', runId: 3 }, 104),
      step(1, 'error', { error: 'boom' }, 107)],
    links: [{ id: 1, parentId: 1, childId: 2, toolCallId: 's1', state: 'running', result: '' }],
  });
  views.set(2, {
    run: { id: 2, status: 'running', parentId: 1 },
    messages: [msg(2, 'user', 'dig', { id: 20, seq: 0 }), msg(2, 'assistant', '', { id: 21, seq: 1, toolCalls: [call('g1', 'subagent_spawn', { task: 'deeper' })] }),
      msg(2, 'tool', '(waiting…)', { id: 22, seq: 2, toolCallId: 'g1' })],
    steps: [],
    links: [{ id: 2, parentId: 2, childId: 4, toolCallId: 'g1', state: 'running' }],
  });
  views.set(4, { run: { id: 4, status: 'waiting_input', pendingState: { kind: 'approval', toolCalls: [call('x', 'file_write', {})] } },
    messages: [msg(4, 'user', 'deeper', { id: 40, seq: 0 }), msg(4, 'assistant', 'writing', { id: 41, seq: 1 })], steps: [], links: [] });
  let draft = null;
  // merged is session.js's: a NEW object every call, run spread from the view
  const merged = (id) => {
    const v = views.get(id);
    if (!v) return null;
    return { ...v, run: { ...v.run, ...(runs.get(id) || {}) }, draft: id === 1 ? draft : null };
  };
  return { views, runs, merged, setDraft: (d) => { draft = d; } };
}

// check folds both ways and compares; returns the cached result.
function check(w, cache, what) {
  const plain = fold(w.merged(1), w.merged);
  const cached = fold(w.merged(1), w.merged, 0, cache);
  assert.deepEqual(cached, plain, `${what}: the cache changes nothing`);
  return cached;
}

const ids = (blocks) => blocks.map((b) => b.id);
// kept: the blocks (at any depth) that are the very same objects in both folds
function kept(a, b) {
  const byId = new Map();
  const walk = (list, f) => { for (const x of list || []) { f(x); walk(x.blocks, f); } };
  walk(a, (x) => byId.set(x.id, x));
  const same = [];
  walk(b, (x) => { if (byId.get(x.id) === x) same.push(x.id); });
  return same;
}

test('the cache: the same output after every kind of update, rebuilding only what changed', () => {
  const w = world();
  const cache = new FoldCache();
  const first = check(w, cache, 'first fold');
  const all = cache.stats.built;
  assert.ok(all >= 12, `the first fold builds every block (${all})`);
  assert.deepEqual(ids(first), ['s' + w.views.get(1).steps[0].id, 'm2', 'r3', 'm3', 'cc1', 'cs1', 'cs2', 'm7', 's' + w.views.get(1).steps[3].id, 'm8']);

  // nothing changed: the very same list
  let n = cache.stats.built;
  const again = check(w, cache, 'no change');
  assert.equal(again, first, 'the same list object');
  assert.equal(cache.stats.built, n, 'nothing rebuilt');

  // a draft streams: only the draft's blocks are new
  const d = { text: 'Hel', thinking: 'so', thinkStart: 1, thinkEnd: 0, tools: { 0: { index: 0, id: 'c9', name: 'file_write', args: '{"pa' } } };
  w.setDraft(d);
  const drafting = check(w, cache, 'a draft');
  d.text += 'lo'; // the Session appends a delta in place
  const streamed = check(w, cache, 'a streamed token');
  assert.equal(cache.stats.built, n, 'a token rebuilds no message block');
  const nonDraft = kept(first, first).length; // every block, subagents' inside included
  assert.equal(nonDraft, ids(first).length + 2, 'a child card holds one block, its grandchild card one');
  assert.deepEqual(kept(drafting, streamed), kept(first, first), 'every block but the draft is the same object, subagents inside included');
  assert.equal(streamed.find((b) => b.id === 'draft-text').text, 'Hello');
  w.setDraft(null);

  // a tool result settles: that card only
  n = cache.stats.built;
  upsert(w.views.get(1).messages, { id: 4, content: 'error: HTTP 500' });
  let cur = check(w, cache, 'a result settles');
  assert.equal(cache.stats.built - n, 1);
  assert.equal(cur.find((b) => b.id === 'cc1').state, 'error');

  // a new message: its blocks only
  n = cache.stats.built;
  w.views.get(1).messages.push(msg(1, 'assistant', 'Done: **three** things.', { id: 9, seq: 8, reasoning: 'ok' }));
  cur = check(w, cache, 'a new message');
  assert.equal(cache.stats.built - n, 2, 'its thinking and its text');

  // a step arrives
  n = cache.stats.built;
  w.views.get(1).steps.push(step(1, 'finish', { result: 'ok' }, 200));
  cur = check(w, cache, 'a step');
  assert.equal(cache.stats.built - n, 1);

  // the subagent's link settles: its card rebuilds, the rest stays
  n = cache.stats.built;
  const before = cur;
  upsert(w.views.get(1).links, { id: 1, state: 'done', result: 'found it' });
  cur = check(w, cache, 'a link');
  assert.equal(cache.stats.built - n, 1);
  assert.equal(cur.find((b) => b.id === 'cs1').result, 'found it');
  assert.equal(kept(before, cur).length, kept(before, before).length - 1, 'only the card changed');

  // deep inside: the grandchild answers — the cards that hold it rebuild, up the chain
  n = cache.stats.built;
  w.views.get(4).messages.push(msg(4, 'assistant', 'written', { id: 42, seq: 2 }));
  cur = check(w, cache, 'a grandchild message');
  assert.equal(cache.stats.built - n, 3, 'the new block, the card in the child, the card in the root');

  // a subagent's run changes (a run event): its card follows
  n = cache.stats.built;
  w.runs.set(4, { status: 'running' });
  cur = check(w, cache, 'a child run event');
  assert.equal(cache.stats.built - n, 2);
  assert.equal(cur.find((b) => b.id === 'cs1').blocks.find((b) => b.id === 'cg1').pendingApproval, null);
  // …a run event that changes nothing shown still arrives as a new object: nothing rebuilt
  n = cache.stats.built;
  w.runs.set(4, { status: 'running' });
  check(w, cache, 'an equal run event');
  assert.equal(cache.stats.built, n);

  // compaction folds messages away: their blocks go, and so does the cache's memory of them
  upsert(w.views.get(1).messages, { id: 7, compacted: true });
  cur = check(w, cache, 'compaction');
  assert.ok(!ids(cur).includes('m7'));
  assert.ok(!cache.blocks.has('m7'), 'forgotten');

  // a subagent view dropped: its cache goes too
  const kid = w.views.get(2);
  w.views.delete(2);
  check(w, cache, 'a child unloaded');
  assert.ok(!cache.kids.has(2));
  w.views.set(2, kid);
  check(w, cache, 'a child loaded again');

  // the whole view re-read (a reset): every object is new, everything rebuilt, same output
  n = cache.stats.built;
  const v = w.views.get(1);
  w.views.set(1, JSON.parse(JSON.stringify(v)));
  check(w, cache, 'a re-read view');
  assert.ok(cache.stats.built - n >= ids(cur).length - 3, 'rebuilt from the new objects');
});

test('the cache holds up under random updates, and does less work', () => {
  // a small deterministic PRNG
  let seed = 12345;
  const rnd = (n) => { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed % n; };
  const w = world();
  const cache = new FoldCache();
  let plainBuilt = 0;
  const kinds = ['newUser', 'newAssistant', 'settle', 'step', 'link', 'draft', 'childMsg', 'childRun', 'compact', 'none', 'reread', 'paged'];
  for (let i = 0; i < 400; i++) {
    const v = w.views.get(1);
    const k = kinds[rnd(kinds.length)];
    switch (k) {
      case 'newUser': v.messages.push(msg(1, 'user', `u${i}` + (rnd(3) ? '' : '\n\n[attached: f.txt (text/plain, 1 B)]'), { seq: 100 + i, created: 100 + i })); break;
      case 'newAssistant': {
        const cid = 'k' + i;
        v.messages.push(msg(1, 'assistant', rnd(2) ? `a${i}` : '', { seq: 100 + i, created: 100 + i, reasoning: rnd(2) ? 'r' : '',
          toolCalls: rnd(2) ? [call(cid, rnd(4) ? 'file_read' : 'subagent_spawn', { path: 'p', task: 't' })] : undefined }));
        if (rnd(2)) v.messages.push(msg(1, 'tool', '(running…)', { seq: 100 + i, toolCallId: cid }));
        break;
      }
      case 'settle': {
        const tools = v.messages.filter((m) => m.role === 'tool');
        if (tools.length) upsert(v.messages, { id: tools[rnd(tools.length)].id, content: rnd(2) ? 'ok' : 'error: no' });
        break;
      }
      case 'step': v.steps.push(step(1, ['note', 'error', 'yield', 'render', 'ask', 'spawn'][rnd(6)], { text: 's', kind: rnd(2) ? 'approval' : 'ask_user' }, 100 + rnd(i + 10))); break;
      case 'link': upsert(v.links, { id: 1, state: ['running', 'done', 'error', 'canceled'][rnd(4)], result: 'r' + i }); break;
      case 'draft': w.setDraft(rnd(3) ? { text: 't' + i, thinking: rnd(2) ? 'th' : '', thinkStart: 1, thinkEnd: rnd(2), tools: {} } : null); break;
      case 'childMsg': w.views.get(2).messages.push(msg(2, 'assistant', 'c' + i, { seq: 100 + i })); break;
      case 'childRun': w.runs.set([2, 4][rnd(2)], { status: ['running', 'done', 'waiting_input'][rnd(3)] }); break;
      case 'compact': { const m = v.messages[rnd(v.messages.length)]; upsert(v.messages, { id: m.id, compacted: true }); break; }
      case 'reread': w.views.set(1, JSON.parse(JSON.stringify(v))); break;
      case 'paged': w.views.set(1, { ...v, hasOlder: !v.hasOlder }); break;
    }
    const plain = fold(w.merged(1), w.merged);
    const count = (list) => (list || []).reduce((n, b) => n + 1 + count(b.blocks), 0);
    plainBuilt += count(plain);
    assert.deepEqual(fold(w.merged(1), w.merged, 0, cache), plain, `step ${i} (${k})`);
  }
  assert.ok(cache.stats.built < plainBuilt / 4, `with the cache ${cache.stats.built} blocks were built, without it ${plainBuilt}`);
  assert.ok(cache.stats.reused > cache.stats.built);
});

test('without a cache fold() is as it was: fresh objects every time', () => {
  const w = world();
  const a = fold(w.merged(1), w.merged);
  const b = fold(w.merged(1), w.merged);
  assert.deepEqual(a, b);
  assert.notEqual(a, b);
  assert.equal(kept(a, b).length, 0);
});
