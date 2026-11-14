// hack/agent-prefs.test.mjs — unit tests for the browser side of a person's
// agent preferences (web/agent-prefs.js, D229), run by `make js-test`: what
// the launcher reads and writes, the one-time move of this browser's last
// choice to the server, and the fallback to it when the server can't answer.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { makeAgentPrefs, kindOf, startWith, optionPatch, LAST_KIND } from '../web/agent-prefs.js';

const URL_ = '/api/xbin/agent/prefs';
const empty = () => ({ harness: '', lastKind: '', harnesses: {}, agentSessions: { start: 'ask' } });

// a server that keeps prefs like xbind: GET, and PUT as a merge patch
function fakeServer(prefs, { status = 200 } = {}) {
  const calls = [];
  const merge = (t, p) => {
    const out = { ...(t || {}) };
    for (const [k, v] of Object.entries(p)) {
      if (v === null) delete out[k];
      else if (typeof v === 'object') out[k] = merge(out[k], v);
      else out[k] = v;
    }
    return out;
  };
  const f = async (url, init = {}) => {
    calls.push({ url, method: init.method || 'GET', body: init.body ? JSON.parse(init.body) : undefined });
    if (url !== URL_ || status !== 200) return { ok: false, status: status === 200 ? 404 : status, json: async () => ({ error: 'nope' }) };
    if (init.method === 'PUT') prefs = merge(prefs, JSON.parse(init.body));
    return { ok: true, status: 200, json: async () => JSON.parse(JSON.stringify(prefs)) };
  };
  return { f, calls, get prefs() { return prefs; } };
}
function fakeStorage(init = {}) {
  const m = new Map(Object.entries(init));
  return { getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, v), removeItem: (k) => m.delete(k) };
}
const claude = { id: 'claude', modes: [{ id: 'default' }, { id: 'acceptEdits' }, { id: 'bypassPermissions', explicit: true }], defaultMode: 'default' };
const opencode = { id: 'opencode', modes: null, defaultMode: '' };
const tick = () => new Promise((r) => setTimeout(r, 0));

test('kindOf: the launcher\'s last choice from the preferences', () => {
  assert.deepEqual(kindOf({ lastKind: 'shell', harness: 'claude' }), { kind: 'shell' });
  assert.deepEqual(kindOf({ lastKind: 'agent', harness: 'codex' }), { kind: 'agent', provider: 'codex' });
  assert.equal(kindOf({ lastKind: 'agent', harness: '' }), null);
  assert.equal(kindOf(empty()), null);
  assert.equal(kindOf(null), null);
});

test('startWith: a saved mode only while the provider lists it, never an explicit one', () => {
  const p = { harnesses: { claude: { mode: 'acceptEdits', model: 'opus', options: { effort: 'high' } }, opencode: { mode: 'plan' } } };
  assert.deepEqual(startWith(p, claude), { mode: 'acceptEdits', model: 'opus', options: { effort: 'high' } });
  assert.deepEqual(startWith({ harnesses: { claude: { mode: 'bypassPermissions', model: 'opus' } } }, claude), { model: 'opus' });
  assert.deepEqual(startWith({ harnesses: { claude: { mode: 'gone' } } }, claude), {});
  assert.deepEqual(startWith(p, opencode), { mode: 'plan' }, 'a provider with no mode table takes the saved one');
  assert.deepEqual(startWith(p, { id: 'codex', modes: [] }), {});
  assert.deepEqual(startWith(null, claude), {});
});

test('optionPatch: mode, model and other settings; a bypass mode is never remembered', () => {
  assert.deepEqual(optionPatch(claude, { id: 'mode' }, 'acceptEdits'), { harnesses: { claude: { mode: 'acceptEdits' } } });
  assert.equal(optionPatch(claude, { id: 'mode' }, 'bypassPermissions'), null);
  assert.equal(optionPatch(claude, { id: 'mode' }, 'unknown'), null);
  assert.deepEqual(optionPatch(opencode, { id: 'agent', category: 'mode' }, 'plan'), { harnesses: { opencode: { mode: 'plan' } } });
  assert.deepEqual(optionPatch(claude, { id: 'model' }, 'opus'), { harnesses: { claude: { model: 'opus' } } });
  assert.deepEqual(optionPatch(claude, { id: 'm', category: 'model' }, 'opus'), { harnesses: { claude: { model: 'opus' } } });
  assert.deepEqual(optionPatch(claude, { id: 'effort' }, 'high'), { harnesses: { claude: { options: { effort: 'high' } } } });
  assert.equal(optionPatch(claude, { id: 'effort' }, ''), null);
  assert.equal(optionPatch(null, { id: 'effort' }, 'x'), null);
});

test('the server\'s preferences win; this browser\'s record is left in place', async () => {
  const srv = fakeServer({ ...empty(), lastKind: 'agent', harness: 'codex' });
  const storage = fakeStorage({ [LAST_KIND]: JSON.stringify({ kind: 'shell' }) });
  const p = makeAgentPrefs({ fetch: srv.f, storage });
  assert.deepEqual(p.lastKind(), { kind: 'shell' }, 'before the load: this browser\'s');
  await p.load();
  assert.deepEqual(p.lastKind(), { kind: 'agent', provider: 'codex' });
  assert.equal(p.harness, 'codex');
  assert.equal(srv.calls.length, 1, 'nothing to move: the server already has a choice');
  assert.ok(storage.getItem(LAST_KIND), 'the browser record stays, for an older xbind');
});

test('the first load moves this browser\'s last choice to a server that has none', async () => {
  const srv = fakeServer(empty());
  const p = makeAgentPrefs({ fetch: srv.f, storage: fakeStorage({ [LAST_KIND]: JSON.stringify({ kind: 'agent', provider: 'claude' }) }) });
  await p.load();
  assert.deepEqual(srv.calls.map((c) => c.method), ['GET', 'PUT']);
  assert.deepEqual(srv.calls[1].body, { lastKind: 'agent', harness: 'claude' });
  assert.deepEqual(p.lastKind(), { kind: 'agent', provider: 'claude' });
  await p.load();
  assert.equal(srv.calls.length, 2, 'load is cached');
  // a second browser signs in later: nothing left to move
  const q = makeAgentPrefs({ fetch: srv.f, storage: fakeStorage({ [LAST_KIND]: JSON.stringify({ kind: 'shell' }) }) });
  await q.load();
  assert.deepEqual(q.lastKind(), { kind: 'agent', provider: 'claude' });
  assert.equal(srv.calls.filter((c) => c.method === 'PUT').length, 1);
});

test('remembering: at once here, in the browser record, and on the server', async () => {
  const srv = fakeServer(empty());
  const storage = fakeStorage();
  const p = makeAgentPrefs({ fetch: srv.f, storage });
  await p.load();
  p.rememberKind({ kind: 'agent', provider: 'claude' });
  assert.deepEqual(p.lastKind(), { kind: 'agent', provider: 'claude' }, 'synchronously: the menu reads it right after');
  assert.deepEqual(JSON.parse(storage.getItem(LAST_KIND)), { kind: 'agent', provider: 'claude' });
  p.rememberKind({ kind: 'shell' });
  p.rememberOption(claude, { id: 'mode' }, 'acceptEdits');
  p.rememberOption(claude, { id: 'model' }, 'opus');
  p.rememberOption(claude, { id: 'effort' }, 'high');
  p.rememberOption(claude, { id: 'mode' }, 'bypassPermissions');
  assert.deepEqual(p.startWith(claude), { mode: 'acceptEdits', model: 'opus', options: { effort: 'high' } });
  await tick(); await tick();
  assert.deepEqual(srv.prefs.harnesses, { claude: { mode: 'acceptEdits', model: 'opus', options: { effort: 'high' } } });
  assert.equal(srv.prefs.lastKind, 'shell');
  assert.equal(srv.prefs.harness, 'claude', 'the harness stays the default for the next agent session');
  assert.deepEqual(p.lastKind(), { kind: 'shell' });
  p.rememberKind({ kind: 'agent' });
  p.rememberKind(null);
  assert.deepEqual(p.lastKind(), { kind: 'shell' }, 'a choice that names nothing changes nothing');
});

for (const [name, status] of [['an xbind without the route', 404], ['a credential the route refuses', 403]]) {
  test(`${name}: this browser's record answers, as before`, async () => {
    const srv = fakeServer(empty(), { status });
    const storage = fakeStorage({ [LAST_KIND]: JSON.stringify({ kind: 'agent', provider: 'codex' }) });
    const p = makeAgentPrefs({ fetch: srv.f, storage });
    assert.equal(await p.load(), null);
    assert.deepEqual(p.lastKind(), { kind: 'agent', provider: 'codex' });
    assert.equal(p.harness, 'codex');
    p.rememberKind({ kind: 'shell' });
    assert.deepEqual(p.lastKind(), { kind: 'shell' });
    assert.deepEqual(p.startWith(claude), {});
    p.rememberOption(claude, { id: 'model' }, 'opus');
    await tick();
    assert.deepEqual(p.startWith(claude), {}, 'nowhere to keep it: nothing is pretended');
    assert.ok(srv.calls.every((c) => c.method !== 'PUT' || c.body.lastKind), 'only the last choice is offered to the server');
  });
}

test('nothing throws: storage off, the network down, a broken record', async () => {
  const p = makeAgentPrefs({ fetch: async () => { throw new Error('offline'); }, storage: { getItem() { throw new Error('denied'); }, setItem() { throw new Error('denied'); } } });
  assert.equal(await p.load(), null);
  assert.equal(p.lastKind(), null);
  p.rememberKind({ kind: 'shell' });
  p.rememberOption(claude, { id: 'model' }, 'opus');
  assert.equal(p.harness, '');
  const q = makeAgentPrefs({ fetch: async () => ({ ok: false, status: 500 }), storage: fakeStorage({ [LAST_KIND]: '{not json' }) });
  await q.load();
  assert.equal(q.lastKind(), null);
});
