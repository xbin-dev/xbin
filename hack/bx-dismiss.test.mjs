// hack/bx-dismiss.test.mjs — a person's dismissed grant requests and
// "interfaces to bind" rows (D188, web/bx-dismiss.js), run by `make js-test`:
// the keys, the stored shape, pruning against what the server lists, the
// strip's split, the organisations badge, and the read-change-write that
// keeps the strip's two elements from undoing each other.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  PREF, KINDS, grantKey, bindingKey, alertKey, normDismissed, isDismissed, dismiss, restore, prune, split,
  pendingCount, dismissedEvent, loadDismissed, updateDismissed, dismissStore, RETRY_MS,
} from '../web/bx-dismiss.js';

const req = (from, target, role, extra = {}) => ({ from, target, role, ...extra });
const slot = (component, s, kind, extra = {}) => ({ component, slot: s, kind, ...extra });

test('the keys: a request by from|target|role, a slot by component|slot|kind', () => {
  assert.equal(PREF, 'dismissed');
  assert.deepEqual([...KINDS], ['grants', 'bindings', 'alerts', 'limits']);
  // a banner is keyed by its kind and its words: changed words show again
  assert.equal(alertKey({ kind: 'partition-trust', message: 'm' }), 'partition-trust|m');
  assert.notEqual(alertKey({ kind: 'k', message: 'one person' }), alertKey({ kind: 'k', message: 'two people' }));
  assert.equal(alertKey(null), '|');
  assert.equal(grantKey(req('apps/a', 'apps/b', 'reader')), 'apps/a|apps/b|reader');
  assert.notEqual(grantKey(req('apps/a', 'apps/b', 'reader')), grantKey(req('apps/a', 'apps/b', 'writer')), 'another role: another request');
  assert.notEqual(grantKey(req('apps/a', 'apps/b', 'reader')), grantKey(req('apps/a', 'apps/c', 'reader')), 'another target: another request');
  assert.equal(bindingKey(slot('apps/agent', 'mcp', 'http')), 'apps/agent|mcp|http');
  assert.notEqual(bindingKey(slot('apps/agent', 'mcp', 'http')), bindingKey(slot('apps/agent', 'mcp', 'net')));
  assert.equal(grantKey(undefined), '||');
});

test('normDismissed(): anything stored reads as the two maps of strings', () => {
  const empty = { grants: {}, bindings: {}, alerts: {}, limits: {} };
  for (const v of [null, undefined, 5, 'x', [], { grants: [] }, { grants: 'x', bindings: null }]) {
    assert.deepEqual(normDismissed(v), empty, JSON.stringify(v));
  }
  assert.deepEqual(normDismissed({ grants: { a: 't1', b: 5, '': 't' }, bindings: { c: 't2' } }), { grants: { a: 't1' }, bindings: { c: 't2' }, alerts: {}, limits: {} });
  assert.deepEqual(normDismissed({ grants: {}, later: { x: 1 } }), { grants: {}, bindings: {}, alerts: {}, limits: {}, later: { x: 1 } }, 'a later kind is kept');
});

test('dismiss(), isDismissed(), restore(): never mutate, only the named kind', () => {
  const d0 = normDismissed(null);
  const d1 = dismiss(d0, 'grants', 'a|b|reader', 't1');
  assert.deepEqual(d0, { grants: {}, bindings: {}, alerts: {}, limits: {} }, 'the old value is untouched');
  assert.equal(isDismissed(d1, 'grants', 'a|b|reader'), true);
  assert.equal(isDismissed(d1, 'bindings', 'a|b|reader'), false);
  assert.equal(isDismissed(d1, 'grants', 'toString'), false, 'own keys only');
  assert.equal(isDismissed(null, 'grants', 'x'), false);
  const d2 = dismiss(dismiss(d1, 'grants', 'c|d|writer', 't2'), 'bindings', 's|net|net', 't3');
  assert.deepEqual(restore(d2, 'grants', ['a|b|reader']), { grants: { 'c|d|writer': 't2' }, bindings: { 's|net|net': 't3' }, alerts: {}, limits: {} });
  assert.deepEqual(restore(d2, 'grants'), { grants: {}, bindings: { 's|net|net': 't3' }, alerts: {}, limits: {} }, 'all of a kind');
  assert.match(dismiss(d0, 'grants', 'k').grants.k, /^\d{4}-\d\d-\d\dT/, 'at: now, by default');
});

test('prune(): dismissals of what the server no longer lists go; the same object when none does', () => {
  const d = { grants: { 'a|b|reader': 't1', 'a|b|writer': 't2' }, bindings: { 's|mcp|http': 't3' } };
  assert.equal(prune(d, 'grants', ['a|b|reader', 'a|b|writer', 'x|y|z']), d);
  const p = prune(d, 'grants', ['a|b|reader']);
  assert.deepEqual(p, { grants: { 'a|b|reader': 't1' }, bindings: { 's|mcp|http': 't3' } });
  assert.deepEqual(d.grants, { 'a|b|reader': 't1', 'a|b|writer': 't2' }, 'never mutated');
  assert.equal(p.bindings, d.bindings, 'the other kind untouched');
  assert.deepEqual(prune(d, 'bindings', []), { ...d, bindings: {} });
  const empty = { grants: {}, bindings: {} };
  assert.equal(prune(empty, 'grants', []), empty);
});

test('split(): what the strip shows and what it hides, in the server order', () => {
  const pending = [req('a', 'b', 'reader'), req('a', 'b', 'writer'), req('c', 'd', 'reader')];
  const d = dismiss(null, 'grants', 'a|b|writer', 't');
  const { shown, hidden } = split(pending, d, 'grants', grantKey);
  assert.deepEqual(shown, [pending[0], pending[2]]);
  assert.deepEqual(hidden, [pending[1]]);
  assert.deepEqual(split(pending, null, 'grants', grantKey), { shown: pending, hidden: [] }, 'not read yet: everything shows');
  assert.deepEqual(split(undefined, d, 'grants', grantKey), { shown: [], hidden: [] });
});

test('pendingCount(): the badge leaves dismissed requests and rows out, keeps the rest of its rules', () => {
  const g = { pending: [req('a', 'b', 'reader'), req('a', 'b', 'writer'), req('c', 'd', 'reader', { blocked: 'policy' })] };
  const b = { pending: [slot('s', 'mcp', 'http'), slot('s', 'net', 'net'), slot('t', 'net', 'net', { approvable: false })] };
  const q = { requests: [{ manage: true }, { manage: false }] };
  assert.equal(pendingCount(g, b, q, null), 2 + 2 + 1, 'nothing dismissed: today\'s count (blocked and unapprovable left out)');
  let d = dismiss(null, 'grants', 'a|b|writer', 't');
  d = dismiss(d, 'bindings', 's|net|net', 't');
  assert.equal(pendingCount(g, b, q, d), 1 + 1 + 1);
  // a scoped (non-admin) view counts what they may approve and their own
  const scoped = { scope: 'mine', pending: [req('a', 'b', 'r'), req('a', 'b', 's', { approvable: true }), req('m', 'n', 'r', { direction: 'mine' })] };
  assert.equal(pendingCount(scoped, null, null, null), 2);
  assert.equal(pendingCount(scoped, null, null, dismiss(null, 'grants', 'm|n|r', 't')), 1);
  assert.equal(pendingCount(null, null, null, null), 0, 'every call failed');
});

test('dismissedEvent(): a prefs event about the key, from any bucket', () => {
  assert.equal(dismissedEvent({ type: 'prefs', component: 'root', data: { key: 'dismissed' } }), true);
  assert.equal(dismissedEvent({ type: 'prefs', component: 'root', data: { key: 'layout' } }), false);
  assert.equal(dismissedEvent({ type: 'grants' }), false);
  assert.equal(dismissedEvent(null), false);
});

// a fake /api/xbin/prefs/dismissed: a stored value, every call recorded;
// tick() lets every pending request answer (a real network interleaves them)
function server(stored, { fail = false } = {}) {
  const calls = [];
  const res = (status, body) => ({ ok: status < 300, status, json: async () => body });
  const fetchFn = async (url, opts = {}) => {
    const method = opts.method ?? 'GET';
    calls.push({ url, method, body: opts.body });
    await new Promise((r) => setTimeout(r, 1)); // every request takes a turn of the event loop
    if (typeof fail === 'function' ? fail(method, calls.length) : fail) throw new TypeError('offline');
    if (url !== '/api/xbin/prefs/dismissed') return res(404, {});
    if (method === 'GET') return stored === undefined ? res(404, { error: 'not found' }) : res(200, stored);
    if (method === 'PUT') { stored = JSON.parse(opts.body); return res(200, {}); }
    if (method === 'DELETE') { const had = stored !== undefined; stored = undefined; return res(had ? 204 : 404, {}); }
    return res(405, {});
  };
  return { fetchFn, calls, get stored() { return stored; } };
}

test('loadDismissed(): absent is nothing dismissed; a failure is null (show all, prune nothing)', async () => {
  assert.deepEqual(await loadDismissed(server(undefined).fetchFn), { grants: {}, bindings: {}, alerts: {}, limits: {} });
  assert.deepEqual(await loadDismissed(server({ grants: { k: 't' } }).fetchFn), { grants: { k: 't' }, bindings: {}, alerts: {}, limits: {} });
  assert.equal(await loadDismissed(server(undefined, { fail: true }).fetchFn), null);
  assert.equal(await loadDismissed(async () => ({ ok: false, status: 502, json: async () => ({}) })), null);
});

test('updateDismissed(): read, change, write — another element\'s kind survives; empty deletes the key', async () => {
  const s = server({ grants: {}, bindings: { 's|mcp|http': 't0' } });
  // bx-grants dismisses while it still holds an old copy without the binding
  const next = await updateDismissed(s.fetchFn, (d) => dismiss(d, 'grants', 'a|b|reader', 't1'));
  assert.deepEqual(next, { grants: { 'a|b|reader': 't1' }, bindings: { 's|mcp|http': 't0' }, alerts: {}, limits: {} });
  assert.deepEqual(s.stored, next);
  assert.deepEqual(s.calls.map((c) => c.method), ['GET', 'PUT']);
  // restoring everything leaves no key behind
  const none = await updateDismissed(s.fetchFn, (d) => restore(restore(d, 'grants'), 'bindings'));
  assert.deepEqual(none, { grants: {}, bindings: {}, alerts: {}, limits: {} });
  assert.equal(s.stored, undefined);
  assert.equal(s.calls.at(-1).method, 'DELETE');
  assert.deepEqual(await updateDismissed(s.fetchFn, (d) => d), { grants: {}, bindings: {}, alerts: {}, limits: {} }, 'deleting nothing is fine');
  assert.equal(await updateDismissed(server(undefined, { fail: true }).fetchFn, (d) => d), null, 'offline: not saved');
});

test('#11: two changes in flight — bare updateDismissed loses one, the store keeps both', async () => {
  // the bug: each click's read-change-write reads before the other's write
  const raw = server({ grants: {}, bindings: {} });
  await Promise.all([
    updateDismissed(raw.fetchFn, (d) => dismiss(d, 'bindings', 'a|net|net', 't1')),
    updateDismissed(raw.fetchFn, (d) => dismiss(d, 'bindings', 'b|net|net', 't2')),
  ]);
  assert.equal(Object.keys(raw.stored.bindings).length, 1, 'the race this store fixes: one dismissal lost');
  // the store: one chain, both stored, both shown at once
  const s = server({ grants: {}, bindings: {} });
  const st = dismissStore(s.fetchFn);
  await st.load();
  const seen = [];
  st.subscribe((v) => seen.push(Object.keys(v.bindings).sort().join(',')));
  const p = Promise.all(['a', 'b', 'c', 'd', 'e'].map((x, i) => st.change((d) => dismiss(d, 'bindings', `${x}|net|net`, `t${i}`))));
  assert.deepEqual(Object.keys(st.value.bindings).sort(), ['a|net|net', 'b|net|net', 'c|net|net', 'd|net|net', 'e|net|net'], 'all five hidden at once');
  await p;
  assert.deepEqual(Object.keys(s.stored.bindings).sort(), ['a|net|net', 'b|net|net', 'c|net|net', 'd|net|net', 'e|net|net'], 'all five stored');
  assert.ok(seen.every((x, i) => i === 0 || x.split(',').length >= seen[i - 1].split(',').length), `never shown again on the way (${seen.join(' / ')})`);
  // another page's change read meanwhile keeps the queued one applied
  const q = st.change((d) => dismiss(d, 'grants', 'g|h|reader', 'tg'));
  s.stored.alerts = { 'k|m': 'ta' }; // another device's banner dismissal, stored
  await st.load();
  assert.ok(st.value.grants['g|h|reader'] && st.value.alerts['k|m'], 'a load mid-queue: both');
  await q;
  assert.deepEqual(s.stored.grants, { 'g|h|reader': 'tg' });
  assert.deepEqual(s.stored.alerts, { 'k|m': 'ta' }, 'the other device\'s change survived');
});

test('#11: a change that can\'t be stored is tried again, then undone here and reported', async () => {
  const waits = [];
  // the first PUT fails, the retry succeeds
  let puts = 0;
  const s1 = server({ grants: {}, bindings: {} }, { fail: (m) => m === 'PUT' && ++puts === 1 });
  const st1 = dismissStore(s1.fetchFn, { sleep: async (ms) => { waits.push(ms); } });
  await st1.load();
  assert.ok(await st1.change((d) => dismiss(d, 'grants', 'a|b|r', 't')));
  assert.deepEqual(waits, [RETRY_MS[0]]);
  assert.deepEqual(s1.stored.grants, { 'a|b|r': 't' });
  // every PUT fails: given up after the retries, undone here, onError told
  const s2 = server({ grants: {}, bindings: {} }, { fail: (m) => m === 'PUT' });
  const st2 = dismissStore(s2.fetchFn, { sleep: async () => {} });
  await st2.load();
  const errs = [];
  st2.onError((e) => errs.push(e.message));
  const p = st2.change((d) => dismiss(d, 'bindings', 'x|y|z', 't'));
  assert.ok(st2.value.bindings['x|y|z'], 'hidden at once');
  assert.equal(await p, null);
  assert.equal(st2.value.bindings['x|y|z'], undefined, 'not stored: shown again');
  assert.equal(errs.length, 1);
  assert.equal(s2.calls.filter((c) => c.method === 'PUT').length, RETRY_MS.length + 1);
});

test('#11: seen() prunes only what two fresh reads in a row no longer list', async () => {
  const s = server({ grants: {}, bindings: { 'a|net|net': 't1', 'b|net|net': 't2' } });
  const st = dismissStore(s.fetchFn, { sleep: async () => {} });
  await st.load();
  // b missing once (a tile briefly gone): kept
  assert.deepEqual(st.seen('bindings', ['a|net|net']), []);
  // b back: the count starts over
  assert.deepEqual(st.seen('bindings', ['a|net|net', 'b|net|net']), []);
  assert.deepEqual(st.seen('bindings', ['a|net|net']), []);
  // missing twice in a row (bound, or gone for good): pruned and stored
  assert.deepEqual(st.seen('bindings', ['a|net|net']), ['b|net|net']);
  await new Promise((r) => setTimeout(r, 50));
  assert.deepEqual(s.stored.bindings, { 'a|net|net': 't1' });
  // nothing read yet: nothing pruned
  const st2 = dismissStore(server({ grants: { k: 't' } }).fetchFn);
  assert.deepEqual(st2.seen('grants', []), []);
});
