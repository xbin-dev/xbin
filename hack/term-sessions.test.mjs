// hack/term-sessions.test.mjs — unit tests for the browser side of the
// terminal session directory (web/term-sessions.js, D73), run by
// `make js-test`: what the frame asks the server, how a listing becomes the
// tab list, and how the legacy browser record is adopted once.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { makeStore, tabsFrom, clampActive, activeIndex, legacyKey, prefKey } from '../web/term-sessions.js';

// a fetch that records calls and answers from a table
function fakeFetch(answers = {}) {
  const calls = [];
  const f = async (url, init = {}) => {
    calls.push({ url, method: init.method || 'GET', body: init.body });
    const a = answers[url] ?? answers[url.split('?')[0]];
    if (a === undefined) return { ok: false, status: 404, text: async () => '{"error":"nope"}' };
    return { ok: true, status: 200, text: async () => (a === null ? '' : JSON.stringify(a)) };
  };
  return { f, calls };
}
function fakeStorage(init = {}) {
  const m = new Map(Object.entries(init));
  return { getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, v), removeItem: (k) => m.delete(k), has: (k) => m.has(k) };
}

test('keys', () => {
  assert.equal(legacyKey('apps/x'), 'bx-term:apps/x');
  assert.equal(prefKey('apps/x'), 'term:apps:x');
  assert.equal(prefKey('apps/deep/one'), 'term:apps:deep:one', 'no slash: the key is one URL segment');
});

test('list asks the directory for this tile and tolerates failure', async () => {
  const rows = [{ id: 'a', cwd: 'apps/x', net: 'org', gpu: 'none', api: true, name: 'build' }];
  const { f, calls } = fakeFetch({ '/api/xbin/term/sessions': rows });
  const st = makeStore({ fetch: f });
  assert.deepEqual(await st.list('apps/x'), rows);
  assert.equal(calls[0].url, '/api/xbin/term/sessions?cwd=apps%2Fx');
  const bad = makeStore({ fetch: async () => { throw new Error('offline'); } });
  assert.deepEqual(await bad.list('apps/x'), [], 'no listing → no tabs, never a throw');
  const old = makeStore({ fetch: fakeFetch({}).f });
  assert.deepEqual(await old.list('apps/x'), [], 'an xbind without the route → []');
});

test('rename and the window pref go where the server expects', async () => {
  const { f, calls } = fakeFetch({ '/api/xbin/prefs/term%3Aapps%3Ax': { open: true, active: 1, pop: { dx: 1, dy: 2, w: 3, h: 4 } } });
  const st = makeStore({ fetch: f });
  await st.rename('a', 'deploy');
  assert.equal(calls[0].method, 'PATCH');
  assert.equal(calls[0].url, '/api/xbin/term/sessions/a');
  assert.deepEqual(JSON.parse(calls[0].body), { name: 'deploy' });
  assert.deepEqual(await st.loadWindow('apps/x'), { open: true, active: 1, pop: { dx: 1, dy: 2, w: 3, h: 4 } });
  await st.saveWindow('apps/x', { open: false, active: 0, pop: null });
  assert.equal(calls[2].method, 'PUT');
  assert.equal(calls[2].url, '/api/xbin/prefs/term%3Aapps%3Ax');
  await st.saveWindow('apps/x', null);
  assert.equal(calls[3].method, 'DELETE', 'no window state → the pref goes');
  const none = makeStore({ fetch: fakeFetch({}).f });
  assert.equal(await none.loadWindow('apps/x'), null, 'never saved → null');
});

test('tabsFrom: server order, keys kept, spawning tabs kept, closed-elsewhere dropped', () => {
  const local = [
    { key: 'k1', id: 'a', net: 'org', gpu: 'none', api: true, name: 'old', baseOutdated: true },
    { key: 'k2', id: 'gone', net: null, gpu: 'none', api: true, name: '' },
    { key: 'k3', id: null, net: null, gpu: 'all', api: true, name: '' },
  ];
  const server = [
    { id: 'b', net: 'internet', gpu: 'none', api: false, name: 'other browser', scopes: [{ id: 'internet' }], label: 'public' },
    { id: 'a', net: 'org', gpu: 'none', api: true, name: 'build', scopes: [{ id: 'org' }], label: 'devs-net' },
  ];
  const tabs = tabsFrom(server, local);
  assert.deepEqual(tabs.map((t) => [t.key, t.id, t.name]), [['k3', 'b', 'other browser'], ['k1', 'a', 'build']],
    'a row this browser never saw is absorbed into the spawning tab; a known id keeps its key and takes the server name');
  assert.equal(tabs[1].baseOutdated, true, 'what the session frame told the tab survives a listing');
  assert.deepEqual([tabs[0].api, tabs[0].gpu, tabs[0].net], [true, 'all', null], 'the absorbing tab keeps what it ASKED for: the server reports the clamped values, and a changed picker would restart the terminal');
  assert.deepEqual(tabs[0].scopes, [{ id: 'internet' }]);
  assert.deepEqual([tabs[1].api, tabs[1].net], [true, 'org'], 'a known tab keeps its pickers too');
  // no spawning tab: the unknown row becomes a new tab with the server's (effective) pickers; spawning tabs trail
  const t2 = tabsFrom(server, [local[0], { key: 'k4', id: null, gpu: 'none' }]);
  assert.deepEqual(t2.map((t) => [t.key === 'k1' || t.key === 'k4' ? t.key : 'new', t.id]), [['k4', 'b'], ['k1', 'a']], 'one spawning tab absorbs the unknown row');
  const t3 = tabsFrom(server, [local[0]]);
  assert.deepEqual(t3.map((t) => t.id), ['b', 'a']);
  assert.deepEqual([t3[0].api, t3[0].net, t3[0].gpu], [false, 'internet', 'none'], 'a tab first seen from the server shows the effective values');
  assert.deepEqual(tabsFrom([], local).map((t) => t.id), [null], 'nothing on the server: only the spawning tab remains');
  assert.equal(clampActive(5, 2), 1); assert.equal(clampActive(-1, 2), 0); assert.equal(clampActive(1, 0), 0);
});

test('the legacy browser record is adopted once and removed', () => {
  const storage = fakeStorage({
    'bx-term:apps/x': JSON.stringify({ open: true, active: 1, pop: { dx: -10, dy: 5, w: 600, h: 300 },
      sessions: [{ id: 'a', name: 'build' }, { id: 'b', name: '' }, { id: null, name: 'pending' }] }),
    'bx-term:apps/old': JSON.stringify({ open: false, sessions: [{ id: 'z', name: 'x' }], pop: { x: 1, y: 2 } }),
  });
  const st = makeStore({ fetch: fakeFetch({}).f, storage });
  const got = st.migrateLegacy('apps/x');
  assert.deepEqual(got, { window: { open: true, active: 1, pop: { dx: -10, dy: 5, w: 600, h: 300 } }, names: { a: 'build' } });
  assert.equal(storage.has('bx-term:apps/x'), false, 'removed');
  assert.equal(st.migrateLegacy('apps/x'), null, 'second time: nothing');
  const old = st.migrateLegacy('apps/old');
  assert.equal(old.window.pop, null, 'a pre-D66 viewport-fixed {x,y} is not a pop');
  assert.equal(old.window.open, false);
  storage.setItem('bx-term:apps/bad', '{not json');
  assert.equal(st.migrateLegacy('apps/bad'), null);
  assert.equal(storage.has('bx-term:apps/bad'), false, 'garbage is removed too');
  assert.equal(makeStore({ fetch: fakeFetch({}).f, storage: undefined }).migrateLegacy('apps/x'), null, 'no storage: fine');
});

test('tabsFrom: an ended agent tab is kept (marked), a vanished shell tab is dropped, kinds carry', () => {
  const local = [
    { key: 'sh', id: 'shell-gone', kind: 'shell' },
    { key: 'ag', id: 'agent-gone', kind: 'agent', name: 'Claude' },
    { key: 'ag2', id: 'agent-live', kind: 'agent' },
  ];
  const server = [{ id: 'agent-live', kind: 'agent', provider: 'claude', status: 'idle' }];
  const tabs = tabsFrom(server, local);
  assert.deepEqual(tabs.map((t) => [t.key, t.id, !!t.ended]), [['ag2', 'agent-live', false], ['ag', 'agent-gone', true]],
    'the live agent leads (server order), the ended agent trails marked, the vanished shell is gone');
  assert.equal(tabs[0].provider, 'claude', 'provider rides the tab');
  assert.equal(tabs[1].name, 'Claude', 'an ended tab keeps its name');
  // idempotent: a second listing keeps the ended tab ended, once
  const again = tabsFrom(server, tabs);
  assert.deepEqual(again.map((t) => [t.key, !!t.ended]), [['ag2', false], ['ag', true]]);
});

test('activeIndex: by identity, falling back to a clamped index', () => {
  const tabs = [{ key: 'a' }, { key: 'b' }, { key: 'c' }];
  assert.equal(activeIndex(tabs, 'c', 0), 2, 'found by key');
  assert.equal(activeIndex(tabs, 'zz', 1), 1, 'unknown key: the fallback index');
  assert.equal(activeIndex(tabs, 'zz', 9), 2, 'the fallback is clamped');
  assert.equal(activeIndex([], 'a', 3), 0, 'no tabs: 0');
  // the case that used to select the wrong shell: tab 0 of three dies
  const after = tabs.filter((t) => t.key !== 'a');
  assert.equal(activeIndex(after, 'b', 1), 0, 'b stays selected although its index moved');
});

test('tabsFrom: a past-session (history) tab is kept and never absorbs a server row', () => {
  const local = [{ key: 'h', id: null, kind: 'agent', history: 'old-1', name: 'old', ended: true }];
  const server = [{ id: 'agent-new', kind: 'agent', provider: 'claude', status: 'idle' }];
  const tabs = tabsFrom(server, local);
  assert.deepEqual(tabs.map((t) => [t.key === 'h' ? 'h' : 'new', t.id, t.history || null]), [['new', 'agent-new', null], ['h', null, 'old-1']],
    'the server row gets its own tab; the history tab stays, id-less, after it');
  assert.equal(tabs[1].ended, true, 'a history tab stays ended (read-only)');
  assert.deepEqual(tabsFrom([], local).map((t) => t.history), ['old-1'], 'nothing on the server: the history tab remains');
});
