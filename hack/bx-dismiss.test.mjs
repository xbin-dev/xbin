// hack/bx-dismiss.test.mjs — a person's dismissed grant requests and
// "interfaces to bind" rows (D188, web/bx-dismiss.js), run by `make js-test`:
// the keys, the stored shape, pruning against what the server lists, the
// strip's split, the organisations badge, and the read-change-write that
// keeps the strip's two elements from undoing each other.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  PREF, KINDS, grantKey, bindingKey, normDismissed, isDismissed, dismiss, restore, prune, split,
  pendingCount, dismissedEvent, loadDismissed, updateDismissed,
} from '../web/bx-dismiss.js';

const req = (from, target, role, extra = {}) => ({ from, target, role, ...extra });
const slot = (component, s, kind, extra = {}) => ({ component, slot: s, kind, ...extra });

test('the keys: a request by from|target|role, a slot by component|slot|kind', () => {
  assert.equal(PREF, 'dismissed');
  assert.deepEqual([...KINDS], ['grants', 'bindings']);
  assert.equal(grantKey(req('apps/a', 'apps/b', 'reader')), 'apps/a|apps/b|reader');
  assert.notEqual(grantKey(req('apps/a', 'apps/b', 'reader')), grantKey(req('apps/a', 'apps/b', 'writer')), 'another role: another request');
  assert.notEqual(grantKey(req('apps/a', 'apps/b', 'reader')), grantKey(req('apps/a', 'apps/c', 'reader')), 'another target: another request');
  assert.equal(bindingKey(slot('apps/agent', 'mcp', 'http')), 'apps/agent|mcp|http');
  assert.notEqual(bindingKey(slot('apps/agent', 'mcp', 'http')), bindingKey(slot('apps/agent', 'mcp', 'net')));
  assert.equal(grantKey(undefined), '||');
});

test('normDismissed(): anything stored reads as the two maps of strings', () => {
  const empty = { grants: {}, bindings: {} };
  for (const v of [null, undefined, 5, 'x', [], { grants: [] }, { grants: 'x', bindings: null }]) {
    assert.deepEqual(normDismissed(v), empty, JSON.stringify(v));
  }
  assert.deepEqual(normDismissed({ grants: { a: 't1', b: 5, '': 't' }, bindings: { c: 't2' } }), { grants: { a: 't1' }, bindings: { c: 't2' } });
  assert.deepEqual(normDismissed({ grants: {}, later: { x: 1 } }), { grants: {}, bindings: {}, later: { x: 1 } }, 'a later kind is kept');
});

test('dismiss(), isDismissed(), restore(): never mutate, only the named kind', () => {
  const d0 = normDismissed(null);
  const d1 = dismiss(d0, 'grants', 'a|b|reader', 't1');
  assert.deepEqual(d0, { grants: {}, bindings: {} }, 'the old value is untouched');
  assert.equal(isDismissed(d1, 'grants', 'a|b|reader'), true);
  assert.equal(isDismissed(d1, 'bindings', 'a|b|reader'), false);
  assert.equal(isDismissed(d1, 'grants', 'toString'), false, 'own keys only');
  assert.equal(isDismissed(null, 'grants', 'x'), false);
  const d2 = dismiss(dismiss(d1, 'grants', 'c|d|writer', 't2'), 'bindings', 's|net|net', 't3');
  assert.deepEqual(restore(d2, 'grants', ['a|b|reader']), { grants: { 'c|d|writer': 't2' }, bindings: { 's|net|net': 't3' } });
  assert.deepEqual(restore(d2, 'grants'), { grants: {}, bindings: { 's|net|net': 't3' } }, 'all of a kind');
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

// a fake /api/xbin/prefs/dismissed: a stored value, every call recorded
function server(stored, { fail = false } = {}) {
  const calls = [];
  const res = (status, body) => ({ ok: status < 300, status, json: async () => body });
  const fetchFn = async (url, opts = {}) => {
    const method = opts.method ?? 'GET';
    calls.push({ url, method, body: opts.body });
    if (fail) throw new TypeError('offline');
    if (url !== '/api/xbin/prefs/dismissed') return res(404, {});
    if (method === 'GET') return stored === undefined ? res(404, { error: 'not found' }) : res(200, stored);
    if (method === 'PUT') { stored = JSON.parse(opts.body); return res(200, {}); }
    if (method === 'DELETE') { const had = stored !== undefined; stored = undefined; return res(had ? 204 : 404, {}); }
    return res(405, {});
  };
  return { fetchFn, calls, get stored() { return stored; } };
}

test('loadDismissed(): absent is nothing dismissed; a failure is null (show all, prune nothing)', async () => {
  assert.deepEqual(await loadDismissed(server(undefined).fetchFn), { grants: {}, bindings: {} });
  assert.deepEqual(await loadDismissed(server({ grants: { k: 't' } }).fetchFn), { grants: { k: 't' }, bindings: {} });
  assert.equal(await loadDismissed(server(undefined, { fail: true }).fetchFn), null);
  assert.equal(await loadDismissed(async () => ({ ok: false, status: 502, json: async () => ({}) })), null);
});

test('updateDismissed(): read, change, write — another element\'s kind survives; empty deletes the key', async () => {
  const s = server({ grants: {}, bindings: { 's|mcp|http': 't0' } });
  // bx-grants dismisses while it still holds an old copy without the binding
  const next = await updateDismissed(s.fetchFn, (d) => dismiss(d, 'grants', 'a|b|reader', 't1'));
  assert.deepEqual(next, { grants: { 'a|b|reader': 't1' }, bindings: { 's|mcp|http': 't0' } });
  assert.deepEqual(s.stored, next);
  assert.deepEqual(s.calls.map((c) => c.method), ['GET', 'PUT']);
  // restoring everything leaves no key behind
  const none = await updateDismissed(s.fetchFn, (d) => restore(restore(d, 'grants'), 'bindings'));
  assert.deepEqual(none, { grants: {}, bindings: {} });
  assert.equal(s.stored, undefined);
  assert.equal(s.calls.at(-1).method, 'DELETE');
  assert.deepEqual(await updateDismissed(s.fetchFn, (d) => d), { grants: {}, bindings: {} }, 'deleting nothing is fine');
  assert.equal(await updateDismissed(server(undefined, { fail: true }).fetchFn, (d) => d), null, 'offline: not saved');
});
