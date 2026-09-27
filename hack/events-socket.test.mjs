// hack/events-socket.test.mjs — covers P13 PO-5 — old clients as fixtures:
// the web shell's live-reload targeting (web/events-socket.js
// isReloadTarget) exactly as it ships, run by `make js-test` under node (the
// module imports nothing and opens no socket until onEvent is called). A
// browser tab loaded before an upgrade keeps this code, so what it does with
// the new events is asserted here, not assumed: a qualified component is
// claimed by a mounted ancestor frame (which is why rule C2 keeps
// non-primary activity off `reload`, `build-*` and `status`), a `deployments`
// event never reaches this path, and the event tape of
// TestFailedDeployInvisible (test/deployments_test.go) replays through old
// frames without one frame of an old type naming a non-primary deployment.
// The shipped iOS app's twin is ClientEventsTests.swift (ReloadTargets).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { mountedFrames, isReloadTarget } from '../web/events-socket.js';

// mount(...srcs) replaces the mounted frames with plain {src} objects, in
// that order (bx-frame adds itself on connect, web/bx-frame.js).
function mount(...srcs) {
  mountedFrames.clear();
  const frames = srcs.map((src) => ({ src }));
  for (const f of frames) mountedFrames.add(f);
  return frames;
}

// The srcs of the mounted frames that take a change in component.
const takers = (component) => [...mountedFrames].filter((f) => isReloadTarget(f, component)).map((f) => f.src);

// What a mounted bx-frame does with one /ws/events frame, as far as reloads
// go (web/bx-frame.js _event): a frame without a component is dropped, and
// only `reload` reaches isReloadTarget. 'bx-frame calls isReloadTarget for
// reload only' below holds this model to the source.
const reloads = (e) => (e && e.type === 'reload' && e.component ? takers(e.component) : []);

// The event types an old client acts on (rule C2: they describe the
// primary only, with the bare component).
const OLD_TYPES = new Set(['reload', 'build-start', 'build-error', 'build-ok', 'status']);

// Why frame e names a non-primary deployment, or '' when it doesn't: a
// qualified component (<tile>+<name>, the `+` reservation) or a `deployment`
// field, at the top or in data.
function namesDeployment(e) {
  if (typeof e.component === 'string' && e.component.includes('+')) return `the qualified component ${e.component}`;
  if ('deployment' in e) return `deployment ${JSON.stringify(e.deployment)}`;
  if (e.data && typeof e.data === 'object' && 'deployment' in e.data) return `data.deployment ${JSON.stringify(e.data.deployment)}`;
  return '';
}

// covers PO-5 — isReloadTarget as it ships: the frame itself or its nearest
// mounted ancestor takes a change (the longest covering src wins, a tie goes
// to the first mounted), a sibling that shares only a string prefix never
// does, and a frame without a src or not mounted takes nothing.
test('isReloadTarget as it ships: the most specific mounted frame, ancestors included', () => {
  mount('apps/calendar', 'apps/calendar/widgets', 'apps/cal', 'notes');
  assert.deepEqual(takers('apps/calendar'), ['apps/calendar']);
  assert.deepEqual(takers('apps/calendar/widgets'), ['apps/calendar/widgets']);
  assert.deepEqual(takers('apps/calendar/widgets/x'), ['apps/calendar/widgets'], 'a nested change: the nearest ancestor');
  assert.deepEqual(takers('apps/calendar/backend'), ['apps/calendar'], 'an unmounted child: its ancestor');
  assert.deepEqual(takers('apps/calendarx'), [], 'a string prefix is not an ancestor');
  assert.deepEqual(takers('apps/cal'), ['apps/cal']);
  assert.deepEqual(takers('apps'), [], 'a parent of every frame is inside none');
  assert.deepEqual(takers('notes/sub/deep'), ['notes']);
  assert.deepEqual(takers(''), []);

  const [first, second] = mount('apps/x', 'apps/x');
  assert.equal(isReloadTarget(first, 'apps/x'), true, 'the same src twice: the first mounted takes it');
  assert.equal(isReloadTarget(second, 'apps/x'), false);

  mount('apps/x');
  assert.equal(isReloadTarget({ src: 'apps/x' }, 'apps/x'), false, 'a frame that is not mounted');
  assert.equal(isReloadTarget({ src: '' }, 'apps/x'), false, 'a frame without a src');
  assert.equal(isReloadTarget({}, 'apps/x'), false);
  mountedFrames.clear();
});

// covers P13 PO-5 — why rule C2 keeps non-primary activity off `reload`: an
// open ancestor claims a qualified component. `apps/a/b+dev` reloads a
// mounted `apps/a` even while `apps/a/b`'s own frame is mounted (its src
// never covers the qualified string, so the ancestor is the most specific
// covering frame), and `apps/x+dev` reloads no `apps/x` frame at all. A
// frame of a deployment (<bx-frame src="<tile>+<name>">) never takes a bare
// component, its own tile's or a nested tile's.
test('a qualified component is claimed by a mounted ancestor, never by the tile\'s own frame', () => {
  mount('apps/a', 'apps/a/b', 'apps/x');
  assert.deepEqual(takers('apps/a/b+dev'), ['apps/a'], 'the ancestor reloads for a non-primary deployment');
  assert.deepEqual(takers('apps/a/b+dev/widget'), ['apps/a']);
  assert.deepEqual(takers('apps/x+dev'), [], '+ is not a path separator');
  assert.deepEqual(takers('apps/a+dev'), [], 'a top-level tile\'s qualified name has no ancestor here');
  mount('apps');
  assert.deepEqual(takers('apps/a+dev'), ['apps'], 'one level up it does');

  // A current shell mounts frames of deployments: bare components pass them by.
  mount('apps/a', 'apps/a+dev', 'apps/a/b+dev');
  assert.deepEqual(takers('apps/a'), ['apps/a']);
  assert.deepEqual(takers('apps/a/b'), ['apps/a'], 'a nested tile\'s bare change: its ancestor, not the nested deployment frame');
  mount('apps/a+dev');
  assert.deepEqual(takers('apps/a'), []);
  assert.deepEqual(takers('apps/a/b'), []);
  mountedFrames.clear();
});

// covers P13 PO-5 — a `deployments` event never reloads a frame through
// isReloadTarget: in web/ only bx-frame calls it, and only under its
// `case 'reload':`. Every documented `deployments` form (the contract's
// full and reader forms, each op, naming `dev` and `main`) and a `bus` event
// of a non-main namespace pass a mounted zero-state ancestor, the tile's own
// frame and a deployment frame without a reload.
test('bx-frame calls isReloadTarget for reload only: deployments events reload nothing here', () => {
  const web = new URL('../web/', import.meta.url);
  const callers = [];
  const walk = (dir) => {
    for (const d of readdirSync(dir, { withFileTypes: true })) {
      if (d.isDirectory()) { if (d.name !== 'vendor') walk(new URL(`${d.name}/`, dir)); continue; }
      if (!d.name.endsWith('.js')) continue;
      const src = readFileSync(new URL(d.name, dir), 'utf8');
      for (const m of src.matchAll(/isReloadTarget\(/g)) {
        if (src.slice(Math.max(0, m.index - 16), m.index) === 'export function ') continue; // the definition
        const labels = [...src.slice(0, m.index).matchAll(/case '([^']*)':/g)];
        callers.push(`${new URL(d.name, dir).pathname.slice(web.pathname.length)} under ${labels.length ? `case '${labels.at(-1)[1]}'` : 'no case'}`);
      }
    }
  };
  walk(web);
  assert.deepEqual(callers, ['bx-frame.js under case \'reload\'']);

  mount('apps', 'apps/crm', 'apps/crm+dev', 'apps/crm/widgets');
  const forms = [
    { op: 'record', seq: 19, by: 'user:ana', session: 's1', what: ['liveReload', 'primary', 'protectedPrimary', 'edges', 'deployments', 'deliveries', 'alwaysOn', 'limits'] },
    { op: 'deploy', id: 43, deployment: 'main', how: 'promote', from: 'dev', checkpoint: 'c:3f2a1c9', result: 'running', phase: 'build', by: 'user:ana', session: 's1' },
    { op: 'deploy', id: 44, deployment: 'dev', how: 'deploy', checkpoint: 'c:3f2a1c9', result: 'failed', phase: 'start', by: 'user:ana' },
    { op: 'reload', deployment: 'dev' },
    { op: 'build', deployment: 'dev', phase: 'start' },
    { op: 'build', deployment: 'dev', phase: 'error', text: 'compiler output' },
    { op: 'build', deployment: 'dev', phase: 'ok' },
    { op: 'work-tree', changed: 3 },
    { op: 'data', deployment: 'dev', busy: '', state: 'seeded' },
    { op: 'status', deployment: 'dev', level: 'error', message: 'down', ts: 1790000000, transient: false },
    { op: 'notify', deployment: 'dev', to: 'user:bob', title: 't', at: '2026-09-27T00:00:00Z' },
    { op: 'record', what: ['liveReload'] },
    { op: 'deploy', deployment: 'main', checkpoint: 'c:3f2a1c9', result: 'ok', phase: 'swap', by: 'user:ana' },
  ];
  for (const data of forms) {
    for (const component of ['apps/crm', 'apps/crm/widgets']) {
      const e = JSON.parse(JSON.stringify({ type: 'deployments', component, data }));
      assert.deepEqual(reloads(e), [], `${JSON.stringify(e)} reloaded a frame`);
    }
  }
  assert.deepEqual(reloads({ type: 'bus', topic: 'res:apps/crm/events/orders', deployment: 'dev', data: 1 }), []);
  assert.deepEqual(reloads({ type: 'reload', component: 'apps/crm' }), ['apps/crm'], 'the primary\'s bare reload: today\'s');
  mountedFrames.clear();
});

// checkTape(name, lines) replays an event tape (one JSON frame per line)
// through old frames: the tiles' own frames beside a mounted zero-state
// ancestor of them all, then the ancestor alone. No frame of an old type
// names a non-primary deployment, every frame's component is a bare tile
// path, and only `reload` frames reload, each taken by its own tile's frame
// (or, alone, by the ancestor, as today). Returns the parsed frames.
function checkTape(name, lines) {
  const frames = lines.map((l, i) => {
    try { return JSON.parse(l); } catch (err) { assert.fail(`${name}:${i + 1} is not JSON: ${err.message}`); }
  });
  assert.ok(frames.length > 0, `${name} is empty`);
  const tiles = [...new Set(frames.map((e) => e.component).filter(Boolean))].sort();
  const tops = new Set(tiles.map((t) => t.split('/')[0]));
  assert.ok(tops.size === 1 && tiles.every((t) => t.includes('/')), `${name}: no common ancestor of ${tiles}`);
  const [ancestor] = tops;
  for (const [i, e] of frames.entries()) {
    assert.equal(typeof e.type, 'string', `${name}:${i + 1} has no type`);
    if (e.component !== undefined) assert.ok(!String(e.component).includes('+'), `${name}:${i + 1}: a qualified component: ${lines[i]}`);
    if (OLD_TYPES.has(e.type)) assert.equal(namesDeployment(e), '', `${name}:${i + 1}: ${e.type} names ${namesDeployment(e)}: ${lines[i]}`);
  }
  for (const layout of [[ancestor, ...tiles], [ancestor]]) {
    mount(...layout);
    for (const [i, e] of frames.entries()) {
      const got = reloads(e);
      if (e.type !== 'reload') { assert.deepEqual(got, [], `${name}:${i + 1} (${e.type}) reloaded ${got}`); continue; }
      assert.deepEqual(got, [layout.includes(e.component) ? e.component : ancestor], `${name}:${i + 1} with ${layout} mounted`);
    }
  }
  mountedFrames.clear();
  return frames;
}

// covers P13 PO-5 — TestFailedDeployInvisible's tape (test/deployments_test.go):
// go, node and python tiles written, their live reload paused, then a
// broken build, a crash at start and a health timeout each shipped by
// reload now, a deploy and a rollback, every one failing, with a
// tile-report on the primary between. Replayed through old frames: no frame
// of an old type names a non-primary deployment, so a mounted ancestor's
// prefix match never fires on one; the only reloads are the three first
// writes', each taken by its own tile's frame; nothing after a tile's pause
// reloads anything, nor carries a build-* for it.
test('TestFailedDeployInvisible\'s tape replays through old frames', () => {
  const frames = checkTape('the recorded tape', recordedTape().trim().split('\n'));
  assert.equal(frames.length, 113);
  mount('apps', 'apps/fd-go', 'apps/fd-node', 'apps/fd-python');
  const taken = frames.flatMap(reloads);
  assert.deepEqual(taken.sort(), ['apps/fd-go', 'apps/fd-node', 'apps/fd-python']);
  for (const tile of ['apps/fd-go', 'apps/fd-node', 'apps/fd-python']) {
    const paused = frames.findIndex((e) => e.component === tile && e.type === 'deployments');
    assert.ok(paused > 0, `${tile} never paused`);
    const after = frames.slice(paused).filter((e) => e.component === tile);
    assert.deepEqual(after.flatMap(reloads), [], `${tile}: a reload after the pause`);
    assert.deepEqual(after.filter((e) => e.type.startsWith('build-')).map((e) => e.type), [], `${tile}: build-* after the pause`);
    assert.deepEqual(after.filter((e) => e.type === 'status').map((e) => e.data?.message), ['watching the deploys'], `${tile}: the primary's own report only`);
    const failed = new Set(after.filter((e) => e.data?.op === 'deploy' && e.data.result === 'failed').map((e) => e.data.id));
    assert.equal(failed.size, tile === 'apps/fd-go' ? 10 : 9, `${tile}: the failed attempts`); // go's broken build once more by bx
  }
  mountedFrames.clear();
});

// covers P13 PO-5 — a fresh tape: with XBIN_DEPLOY_TAPE naming the file an
// integration run of TestFailedDeployInvisible wrote, it replays the same
// way (skipped otherwise, with the reason).
test('a fresh tape from XBIN_DEPLOY_TAPE replays through old frames', (t) => {
  const f = process.env.XBIN_DEPLOY_TAPE;
  if (!f || !existsSync(f)) return t.skip(f ? `XBIN_DEPLOY_TAPE names ${f}, which does not exist` : 'XBIN_DEPLOY_TAPE is not set');
  checkTape(f, readFileSync(f, 'utf8').split('\n').filter((l) => l.trim() !== ''));
});

// TestFailedDeployInvisible's tape, verbatim (blank lines dropped): recorded
// with `XBIN_DEPLOY_TAPE=<file> go test -tags=integration -run
// 'TestFailedDeployInvisible$' ./test/` on an isolated daemon, 2026-09-27.
// A function, so the tests above may read it (declarations hoist).
function recordedTape() {
  return String.raw`
{"type":"reload","component":"apps/fd-node"}
{"type":"reload","component":"apps/fd-python"}
{"type":"reload","component":"apps/fd-go"}
{"type":"build-start","component":"apps/fd-node"}
{"type":"build-start","component":"apps/fd-go"}
{"type":"build-start","component":"apps/fd-python"}
{"type":"build-ok","component":"apps/fd-node"}
{"type":"build-ok","component":"apps/fd-python"}
{"type":"deployments","component":"apps/fd-node","data":{"op":"record","seq":1,"by":"owner","what":["liveReload","deployments"]}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"record","seq":1,"by":"owner","what":["liveReload","deployments"]}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:d717143","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:d717143","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:9257cc7","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:9257cc7","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:9257cc7","result":"running","phase":"swap","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:9257cc7","result":"running","phase":"swap","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:9257cc7","result":"ok","phase":"swap","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:d717143","result":"running","phase":"swap","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:d717143","result":"running","phase":"swap","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:d717143","result":"ok","phase":"swap","by":"owner"}}
{"type":"status","component":"apps/fd-python","data":{"level":"warn","message":"watching the deploys","ts":1790538841}}
{"type":"status","component":"apps/fd-node","data":{"level":"warn","message":"watching the deploys","ts":1790538841}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":2,"deployment":"main","how":"reload-now","checkpoint":"c:1201301","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":2,"deployment":"main","how":"reload-now","checkpoint":"c:1201301","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":2,"deployment":"main","how":"reload-now","checkpoint":"c:2a4908a","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":2,"deployment":"main","how":"reload-now","checkpoint":"c:2a4908a","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"work-tree","changed":2}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"work-tree","changed":2}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":2,"deployment":"main","how":"reload-now","checkpoint":"c:2a4908a","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":2,"deployment":"main","how":"reload-now","checkpoint":"c:1201301","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":3,"deployment":"main","how":"deploy","checkpoint":"c:1201301","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":3,"deployment":"main","how":"deploy","checkpoint":"c:1201301","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":3,"deployment":"main","how":"deploy","checkpoint":"c:2a4908a","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":3,"deployment":"main","how":"deploy","checkpoint":"c:2a4908a","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":3,"deployment":"main","how":"deploy","checkpoint":"c:2a4908a","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":3,"deployment":"main","how":"deploy","checkpoint":"c:1201301","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":4,"deployment":"main","how":"rollback","checkpoint":"c:1201301","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":4,"deployment":"main","how":"rollback","checkpoint":"c:1201301","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":4,"deployment":"main","how":"rollback","checkpoint":"c:2a4908a","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":4,"deployment":"main","how":"rollback","checkpoint":"c:2a4908a","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":4,"deployment":"main","how":"rollback","checkpoint":"c:1201301","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":4,"deployment":"main","how":"rollback","checkpoint":"c:2a4908a","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":5,"deployment":"main","how":"reload-now","checkpoint":"c:6d84418","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":5,"deployment":"main","how":"reload-now","checkpoint":"c:6d84418","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":5,"deployment":"main","how":"reload-now","checkpoint":"c:9e50223","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":5,"deployment":"main","how":"reload-now","checkpoint":"c:9e50223","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":5,"deployment":"main","how":"reload-now","checkpoint":"c:9e50223","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":5,"deployment":"main","how":"reload-now","checkpoint":"c:6d84418","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":6,"deployment":"main","how":"deploy","checkpoint":"c:6d84418","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":6,"deployment":"main","how":"deploy","checkpoint":"c:6d84418","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":6,"deployment":"main","how":"deploy","checkpoint":"c:9e50223","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":6,"deployment":"main","how":"deploy","checkpoint":"c:9e50223","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":6,"deployment":"main","how":"deploy","checkpoint":"c:9e50223","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":6,"deployment":"main","how":"deploy","checkpoint":"c:6d84418","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":7,"deployment":"main","how":"rollback","checkpoint":"c:6d84418","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":7,"deployment":"main","how":"rollback","checkpoint":"c:6d84418","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":7,"deployment":"main","how":"rollback","checkpoint":"c:9e50223","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":7,"deployment":"main","how":"rollback","checkpoint":"c:9e50223","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":7,"deployment":"main","how":"rollback","checkpoint":"c:9e50223","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":7,"deployment":"main","how":"rollback","checkpoint":"c:6d84418","result":"failed","phase":"start","by":"owner"}}
{"type":"build-ok","component":"apps/fd-go"}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":8,"deployment":"main","how":"reload-now","checkpoint":"c:8ae17df","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":8,"deployment":"main","how":"reload-now","checkpoint":"c:8ae17df","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":8,"deployment":"main","how":"reload-now","checkpoint":"c:04d2d26","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":8,"deployment":"main","how":"reload-now","checkpoint":"c:04d2d26","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"record","seq":1,"by":"owner","what":["liveReload","deployments"]}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:2c066f2","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:2c066f2","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:2c066f2","result":"running","phase":"swap","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:2c066f2","result":"running","phase":"swap","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":1,"deployment":"main","how":"pause","checkpoint":"c:2c066f2","result":"ok","phase":"swap","by":"owner"}}
{"type":"status","component":"apps/fd-go","data":{"level":"warn","message":"watching the deploys","ts":1790538845}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":2,"deployment":"main","how":"reload-now","checkpoint":"c:fa4d675","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"work-tree","changed":2}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":2,"deployment":"main","how":"reload-now","checkpoint":"c:fa4d675","result":"failed","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":3,"deployment":"main","how":"deploy","checkpoint":"c:fa4d675","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":3,"deployment":"main","how":"deploy","checkpoint":"c:fa4d675","result":"failed","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":4,"deployment":"main","how":"rollback","checkpoint":"c:fa4d675","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":4,"deployment":"main","how":"rollback","checkpoint":"c:fa4d675","result":"failed","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":5,"deployment":"main","how":"reload-now","checkpoint":"c:fa4d675","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":5,"deployment":"main","how":"reload-now","checkpoint":"c:fa4d675","result":"failed","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":6,"deployment":"main","how":"reload-now","checkpoint":"c:8104834","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":6,"deployment":"main","how":"reload-now","checkpoint":"c:8104834","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":6,"deployment":"main","how":"reload-now","checkpoint":"c:8104834","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":7,"deployment":"main","how":"deploy","checkpoint":"c:8104834","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":7,"deployment":"main","how":"deploy","checkpoint":"c:8104834","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":7,"deployment":"main","how":"deploy","checkpoint":"c:8104834","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":8,"deployment":"main","how":"rollback","checkpoint":"c:8104834","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":8,"deployment":"main","how":"rollback","checkpoint":"c:8104834","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":8,"deployment":"main","how":"rollback","checkpoint":"c:8104834","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":8,"deployment":"main","how":"reload-now","checkpoint":"c:04d2d26","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":9,"deployment":"main","how":"deploy","checkpoint":"c:04d2d26","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":9,"deployment":"main","how":"deploy","checkpoint":"c:04d2d26","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":9,"deployment":"main","how":"reload-now","checkpoint":"c:1e83bee","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":9,"deployment":"main","how":"reload-now","checkpoint":"c:1e83bee","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":8,"deployment":"main","how":"reload-now","checkpoint":"c:8ae17df","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":9,"deployment":"main","how":"deploy","checkpoint":"c:8ae17df","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":9,"deployment":"main","how":"deploy","checkpoint":"c:8ae17df","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":9,"deployment":"main","how":"deploy","checkpoint":"c:04d2d26","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":10,"deployment":"main","how":"rollback","checkpoint":"c:04d2d26","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":10,"deployment":"main","how":"rollback","checkpoint":"c:04d2d26","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":9,"deployment":"main","how":"reload-now","checkpoint":"c:1e83bee","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":10,"deployment":"main","how":"deploy","checkpoint":"c:1e83bee","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":10,"deployment":"main","how":"deploy","checkpoint":"c:1e83bee","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":9,"deployment":"main","how":"deploy","checkpoint":"c:8ae17df","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":10,"deployment":"main","how":"rollback","checkpoint":"c:8ae17df","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":10,"deployment":"main","how":"rollback","checkpoint":"c:8ae17df","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-node","data":{"op":"deploy","id":10,"deployment":"main","how":"rollback","checkpoint":"c:04d2d26","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":10,"deployment":"main","how":"deploy","checkpoint":"c:1e83bee","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":11,"deployment":"main","how":"rollback","checkpoint":"c:1e83bee","result":"running","phase":"build","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":11,"deployment":"main","how":"rollback","checkpoint":"c:1e83bee","result":"running","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-go","data":{"op":"deploy","id":11,"deployment":"main","how":"rollback","checkpoint":"c:1e83bee","result":"failed","phase":"start","by":"owner"}}
{"type":"deployments","component":"apps/fd-python","data":{"op":"deploy","id":10,"deployment":"main","how":"rollback","checkpoint":"c:8ae17df","result":"failed","phase":"start","by":"owner"}}
`;
}
