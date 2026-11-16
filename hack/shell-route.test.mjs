// hack/shell-route.test.mjs — unit tests for the grammar of a link into the
// workspace (#7; workspace-template/shell/route.js), run by `make js-test`
// (part of `make check`): node's built-in runner, no dependencies. Every
// place the shell can show survives state → URL → state; the URL stays
// readable (a tile path keeps its slashes); what isn't ours in the query is
// kept; what a link can't carry (control characters, `..`) is refused.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseRoute, buildRoute, sameRoute, validTile, cleanAt, sessionId } from '../workspace-template/shell/route.js';

const places = [
  { screen: '', tile: '', at: '' },
  { screen: 'k3x9a2b', tile: '', at: '' },
  { screen: 'k3x9a2b', tile: 'apps/welcome', at: '' },
  { screen: '', tile: 'tiles/admin', at: 'orgs/acme' },
  { screen: 'org-1', tile: 'apps/files', at: 'src/lib/a b+c&d=e#f%g?h' },
  { screen: 'org:sales/q3', tile: 'apps/x', at: 'ünïcødé/路径' },
  { screen: 's', tile: 'apps/deep/nested/tile', at: 'a/b/c' },
];

test('round trip: state → URL → state', () => {
  for (const p of places) {
    const q = buildRoute(p);
    assert.deepEqual(parseRoute(q), p, `${JSON.stringify(p)} → ${q}`);
    assert.ok(sameRoute(parseRoute(q), p));
  }
});

test('round trip: URL → state → URL', () => {
  for (const q of ['', '?screen=k3x9a2b', '?screen=k3x9a2b&tile=apps/welcome', '?tile=tiles/admin&at=orgs/acme', '?screen=a&tile=apps/x&at=q%3D1%26r%3D2']) {
    assert.equal(buildRoute(parseRoute(q)), q);
  }
});

test('readable: slashes and colons stay, separators are escaped', () => {
  assert.equal(buildRoute({ screen: 'abc', tile: 'apps/notes', at: 'projects/q3' }), '?screen=abc&tile=apps/notes&at=projects/q3');
  assert.equal(buildRoute({ tile: 'apps/a', at: 'k=v&x y#z+w' }), '?tile=apps/a&at=k%3Dv%26x%20y%23z%2Bw');
  assert.equal(buildRoute({}), '');
});

test('at only travels with a tile', () => {
  assert.equal(buildRoute({ screen: 's', at: 'x' }), '?screen=s');
  assert.deepEqual(parseRoute('?screen=s&at=x'), { screen: 's', tile: '', at: '' });
});

test('other query parameters are kept, after ours', () => {
  assert.equal(buildRoute({ screen: 's', tile: 'apps/a' }, '?foo=1&screen=old&at=old&bar=a%20b'), '?screen=s&tile=apps/a&foo=1&bar=a%20b');
  assert.equal(buildRoute({}, '?foo=1&tile=apps/x'), '?foo=1');
});

test('parse is lenient where it is harmless', () => {
  assert.deepEqual(parseRoute('tile=/apps/a/&at=%23top'), { screen: '', tile: 'apps/a', at: 'top' }); // no '?', slashes at the ends, a leading '#'
  assert.deepEqual(parseRoute('?tile=apps/a&at=x+y'), { screen: '', tile: 'apps/a', at: 'x y' }); // '+' is a space in a query
  assert.deepEqual(parseRoute(undefined), { screen: '', tile: '', at: '' });
});

test('control characters never reach the page', () => {
  const r = parseRoute('?screen=a%0Ab&tile=apps/a&at=x%00y%1Bz');
  assert.equal(r.screen, 'ab');
  assert.equal(r.at, 'xyz');
  assert.equal(cleanAt('#a\u007fb'), 'ab');
  assert.equal(cleanAt('x'.repeat(5000)).length, 2048);
});

test('validTile refuses what is not a tile path', () => {
  for (const p of ['apps/a', 'tiles/admin', 'apps/a.b/c']) assert.ok(validTile(p), p);
  for (const p of ['', 'apps//a', 'apps/../root', './apps', 'apps/.', 'a\nb', 'x'.repeat(513)]) assert.ok(!validTile(p), JSON.stringify(p));
  // parse keeps an invalid one, so the shell can say the link is broken
  assert.equal(parseRoute('?tile=apps/../root').tile, 'apps/../root');
});

test('sameRoute: an absent at equals an empty one', () => {
  assert.ok(sameRoute({ screen: 's', tile: 't', at: '' }, { screen: 's', tile: 't' }));
  assert.ok(!sameRoute({ screen: 's', tile: 't', at: 'a' }, { screen: 's', tile: 't', at: 'b' }));
  assert.ok(!sameRoute({ screen: 's', tile: '' }, { screen: 'r', tile: '' }));
});

test('a session in a link (D229): only with a tile, only a session id, never part of the place', () => {
  assert.deepEqual(parseRoute('?tile=apps/notes&session=Ab3_x-9'), { screen: '', tile: 'apps/notes', at: '', session: 'Ab3_x-9' });
  assert.deepEqual(parseRoute('?screen=s&tile=apps/notes&at=a/b&session=abc'), { screen: 's', tile: 'apps/notes', at: 'a/b', session: 'abc' });
  assert.ok(!('session' in parseRoute('?session=abc')), 'no tile: no session');
  assert.ok(!('session' in parseRoute('?tile=apps/x')), 'absent: the route has the shape it always had');
  for (const bad of ['a b', '../x', 'a/b', '', 'x'.repeat(65), 'a%00b', '<script>']) {
    assert.ok(!('session' in parseRoute(`?tile=apps/x&session=${encodeURIComponent(bad)}`)), `refused: ${bad}`);
    assert.equal(sessionId(bad), '');
  }
  // a link to hand out carries it; the shell's own URL (a route without one) never does
  assert.equal(buildRoute({ tile: 'apps/notes', session: 'abc' }), '?tile=apps/notes&session=abc');
  assert.equal(buildRoute({ tile: 'apps/notes', at: 'p', session: 'abc' }), '?tile=apps/notes&at=p&session=abc');
  assert.equal(buildRoute({ session: 'abc' }), '', 'no tile: nothing to open it on');
  assert.equal(buildRoute({ tile: 'apps/notes', session: 'a b' }), '?tile=apps/notes');
  assert.equal(buildRoute({ screen: 's', tile: 'apps/notes', at: '' }, '?tile=apps/notes&session=abc&keep=1'), '?screen=s&tile=apps/notes&keep=1',
    'rewriting the place drops a session the URL carried, and keeps what isn\'t ours');
  assert.ok(sameRoute(parseRoute('?tile=apps/notes&session=abc'), { screen: '', tile: 'apps/notes', at: '' }), 'the same place, with or without it');
});
