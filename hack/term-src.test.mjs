// hack/term-src.test.mjs — unit tests for where <bx-terminal src> connects
// and when it reconnects (web/term-src.js; docs/elements.md §<bx-terminal>),
// run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { srcTarget, reattachSrc, canReattach, endedByClose, RETRIES, LIVED, backoff, exitWords } from '../web/term-src.js';

const PAGE = 'https://ws.example/c/apps/agent/';

test('srcTarget: a path on this host goes through xbind with the page credential', () => {
  assert.deepEqual(srcTarget('/api/apps/mgr/sbx/sandboxes/b1/tty?cwd=%2Fwork', PAGE), { path: '/api/apps/mgr/sbx/sandboxes/b1/tty?cwd=%2Fwork' });
  assert.deepEqual(srcTarget('https://ws.example/api/apps/mgr/x?a=1', PAGE), { path: '/api/apps/mgr/x?a=1' }, 'an absolute URL on this host is a path');
  assert.deepEqual(srcTarget('wss://ws.example/api/apps/mgr/x', PAGE), { path: '/api/apps/mgr/x' });
  assert.deepEqual(srcTarget('../other/tty', PAGE), { path: '/c/apps/other/tty' }, 'relative: against the document');
});

test('srcTarget: another host is dialled as it is (no credential of ours)', () => {
  assert.deepEqual(srcTarget('wss://term.example/pty?x=1', PAGE), { url: 'wss://term.example/pty?x=1' });
  assert.deepEqual(srcTarget('http://term.example/pty', PAGE), { url: 'ws://term.example/pty' });
  assert.deepEqual(srcTarget('https://term.example/pty', PAGE), { url: 'wss://term.example/pty' });
});

test('srcTarget: anything else is not a terminal address', () => {
  for (const bad of ['', '   ', null, undefined, 'javascript:alert(1)', 'data:text/plain,x', 'file:///etc/passwd', 'ftp://h/x']) {
    assert.equal(srcTarget(bad, PAGE), null, String(bad));
  }
  assert.equal(srcTarget('/x', 'not a url'), null);
});

test('reattachSrc: a sandbox manager\'s terminal reattaches to its exec', () => {
  const src = '/api/apps/mgr/sbx/sandboxes/b1/tty?cwd=%2Fwork&rows=24';
  assert.equal(reattachSrc(src, 'e7'), '/api/apps/mgr/sbx/sandboxes/b1/execs/e7/tty', 'the query was for starting one');
  assert.equal(reattachSrc(src, ''), src, 'no session yet: src');
  assert.equal(reattachSrc(src, 'a b/c'), '/api/apps/mgr/sbx/sandboxes/b1/execs/a%20b%2Fc/tty', 'the id is one path segment');
  assert.equal(reattachSrc('/api/apps/mgr#eu/sbx/sandboxes/b1/tty', 'e1'), '/api/apps/mgr#eu/sbx/sandboxes/b1/tty', 'a fragment is not a path');
  assert.equal(reattachSrc('/api/apps/mgr/eu/sbx/sandboxes/b1/tty', 'e1'), '/api/apps/mgr/eu/sbx/sandboxes/b1/execs/e1/tty', 'an instance prefix');
  assert.equal(reattachSrc('/api/apps/mine/pty', 'e1'), '/api/apps/mine/pty', 'any other src is dialled again');
  assert.equal(reattachSrc('/api/apps/mgr/sbx/sandboxes/b1/execs/e1/tty', 'e1'), '/api/apps/mgr/sbx/sandboxes/b1/execs/e1/tty', 'an attach stays one');
  assert.ok(canReattach('/api/apps/mgr/sbx/sandboxes/b1/tty?cwd=/'));
  assert.ok(!canReattach('/api/apps/mine/pty'));
});

test('endedByClose: a clean close ends it; a drop reconnects', () => {
  assert.ok(endedByClose(1000));
  assert.ok(endedByClose(1005));
  for (const c of [1001, 1006, 1011, 4000]) assert.ok(!endedByClose(c), String(c));
});

test('backoff: 500 ms doubling, capped at 10 s; a handful of tries', () => {
  assert.deepEqual([0, 1, 2, 3, 4, 5, 6].map(backoff), [500, 1000, 2000, 4000, 8000, 10000, 10000]);
  assert.equal(RETRIES, 6);
  assert.equal(LIVED, 5000);
});

test('exitWords: how the exit frame reads', () => {
  assert.equal(exitWords({ op: 'exit', code: 0 }), 'exited');
  assert.equal(exitWords({ op: 'exit', code: 3 }), 'exited with code 3');
  assert.equal(exitWords({ op: 'exit', code: null, signal: 'KILL' }), 'exited on SIGKILL');
  assert.equal(exitWords({ op: 'exit', code: null, signal: 'SIGTERM' }), 'exited on SIGTERM');
  assert.equal(exitWords({ op: 'exit' }), 'exited');
});
