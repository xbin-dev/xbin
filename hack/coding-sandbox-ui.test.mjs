// hack/coding-sandbox-ui.test.mjs — the coding-sandbox template's page
// (builtin-templates/coding-sandbox): its shared model (model/) in node with
// no DOM and no lit, the feature parity of its two views (model/features.js,
// D96's mechanism), and the native view's trees rendered with hack/xbn
// against the web tests' fake backend (test/stub.mjs, test/seed.mjs). The
// web view's own browser test is the template's test/web.mjs. Run by
// `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { runNative } from './xbn/node.mjs';

const TPL = new URL('../builtin-templates/coding-sandbox/', import.meta.url).pathname;
const F = await import(TPL + 'model/format.js');
const O = await import(TPL + 'model/ops.js');
const M = await import(TPL + 'model/mine.js');
const { createApp } = await import(TPL + 'model/app.js');
const { AREAS, FEATURES, DIFFERENCES, gaps } = await import(TPL + 'model/features.js');
const { IMPLEMENTS: WEB } = await import(TPL + 'web-features.js');
const { IMPLEMENTS: NATIVE } = await import(TPL + 'native-features.js');
const { STUB } = await import(TPL + 'test/stub.mjs');
const { SEED, READER, NOW } = await import(TPL + 'test/seed.mjs');

// --- the model ----------------------------------------------------------------------

test('format: states, networks, sizes, bytes, times, paths', () => {
  assert.equal(F.stateTone('running'), 'ok');
  assert.equal(F.stateTone('creating'), 'warn');
  assert.equal(F.egressText({ egress: 'internet', egressNext: 'none' }), 'internet → no network at the next start');
  assert.equal(F.sizeText({ memMiB: 3072, vcpus: 2, diskGiB: 20 }), '3 GiB · 2 vCPU · 20 GiB disk');
  assert.equal(F.bytes(734003200), '700 MiB');
  assert.equal(F.ago(NOW - 3 * 3600e3, NOW), '3 h ago');
  assert.equal(F.ownerText({ user: 'alice', asserted: true }), 'alice (asserted)');
  assert.deepEqual(F.parseUsers('alice, bob alice'), ['alice', 'bob']);
  assert.equal(F.parseUsers(' '), '*');
  assert.equal(F.cleanPath('work/../etc//x/.'), '/etc/x');
  assert.equal(F.parentPath('/work/src/'), '/work');
  assert.deepEqual(F.crumbs('/a/b').map((c) => c.path), ['/', '/a', '/a/b']);
  assert.ok(F.looksBinary('PNG\u0000x') && !F.looksBinary('hello\n'));
});

test('ops: rows, usage, the substrate, the mode, images', () => {
  const rows = O.sandboxRows(SEED.ops, NOW);
  assert.deepEqual(rows.map((r) => r.id), ['sb-node', 'sb-api', 'sb-term', 'sb-own', 'sb-rusty', 'sb-web']);
  const act = (id) => rows.find((r) => r.id === id).actions.map((a) => a.id);
  assert.deepEqual(act('sb-api'), ['stop', 'snapshots', 'shares', 'delete']);
  assert.deepEqual(act('sb-web'), ['start', 'snapshots', 'shares', 'delete']);
  assert.deepEqual(act('sb-node'), ['shares', 'delete'], 'a creating sandbox: no lifecycle, no snapshots');
  const u = O.usageRows(SEED.ops).find((x) => x.who === 'apps/agent');
  assert.ok(u.override && u.cells[0].text === '4 / 6' && !u.full);
  const b = O.backendInfo(SEED.ops);
  assert.deepEqual(b.classes.map((c) => [c.slot, c.offered]), [['internet', true], ['open', false]]);
  assert.match(b.classes[1].bind, /^bx bind apps\/coding-sandbox open=/);
  assert.equal(O.modeInfo(SEED.ops).now, 'vm');
  const noVM = { ...SEED.ops, config: { ...SEED.ops.config, mode: 'vm' }, runtime: { ...SEED.ops.runtime, modes: [{ mode: 'namespace' }], unavailable: [{ mode: 'vm', reason: 'no KVM' }] } };
  const mi = O.modeInfo(noVM);
  assert.ok(mi.now === '' && mi.blocked === 'no KVM', 'a chosen mode the substrate lacks: none, never another');
  const imgs = O.imageRows(SEED.ops);
  assert.deepEqual(imgs.map((i) => [i.id, i.tone, i.canBuild]), [['base', 'muted', false], ['node', 'ok', true], ['rust', 'danger', true]]);
});

test('ops: the editors — images, sizes, quotas, shares, mounts', () => {
  const images = SEED.ops.config.images;
  assert.match(O.applyImage(images, { ...O.imageForm(), id: 'node' }).error, /already/);
  const r = O.applyImage(images, { ...O.imageForm(images[1]), title: 'Node 24', default: true });
  assert.equal(r.images.find((i) => i.id === 'node').title, 'Node 24');
  assert.deepEqual(r.images.filter((i) => i.default).map((i) => i.id), ['node']);
  assert.ok(O.removeImage([images[0]], 'base').error);
  assert.match(O.applySizes([{ id: 'x', memMiB: 64, vcpus: 1, diskGiB: 1 }]).error, /128/);
  const q = O.setQuota(SEED.ops.config.quotas, 'person', 'alice', { running: '5', sandboxes: 0 });
  assert.deepEqual(q.people, { alice: { running: 5 } });
  assert.deepEqual(O.setQuota(q, 'person', 'alice', null).people, {});
  assert.deepEqual(O.shareWith([{ consumer: 'apps/a', users: '*' }], 'apps/a', 'bob').shares, [{ consumer: 'apps/a', users: ['bob'] }]);
  assert.deepEqual(O.parseMount('res:apps/cs/cache:go /cache ro').mount, { res: 'res:apps/cs/cache', at: '/cache', path: 'go', ro: true });
  assert.ok(O.parseMount('apps/cs/cache /cache').error);
});

test('mine: rows, the create form, terminals, files', () => {
  const rows = M.myRows(SEED.mine, SEED.me, NOW);
  assert.deepEqual(rows.map((r) => [r.id, r.mine, r.actions.map((a) => a.id).join()]), [['sb-own', true, 'stop,delete'], ['sb-team', false, 'start']]);
  const f = M.createForm(SEED.hello, { name: '' });
  assert.deepEqual([f.f.image, f.f.size, f.f.egress, f.error], ['base', 'small', 'none', 'name it']);
  assert.equal(M.createForm({ ...SEED.hello, caps: ['files'] }, {}).cant, 'the substrate runs no commands yet');
  assert.equal(M.terminalSrc('apps/cs', 'sb-1', '/work'), '/api/apps/cs/sbx/sandboxes/sb-1/tty?cwd=%2Fwork');
  assert.equal(M.attachSrc('sb-1', 'e9'), 'sbx/sandboxes/sb-1/execs/e9/tty');
  assert.deepEqual(M.fileRows(SEED.files['/work'], NOW).map((e) => e.name), ['src', 'logo.bin', 'README.md']);
});

test('the app: reads, acts, files — against the fake backend', async () => {
  STUB(SEED);
  const app = createApp();
  let changes = 0;
  app.on(() => changes++);
  await app.load();
  assert.ok(app.operator && app.ops && app.hello && app.mine.length === 2 && changes > 0);
  await app.opAct('sb-api', 'stop');
  assert.equal(app.opSandbox('sb-api').state, 'stopped');
  await app.opSnapshot('sb-api', 'first');
  assert.equal(app.snaps.list[0].name, 'first');
  await app.saveConfig({ mode: 'namespace' });
  assert.equal(app.ops.config.mode, 'namespace');
  const s = await app.create({ name: 'scratch', image: 'node', size: 'small', egress: 'internet', visibility: 'team' });
  assert.ok(app.mySandbox(s.id) && s.image.id === 'node');
  await app.browse('sb-own', '/work');
  assert.equal(app.files.listing.entries.length, 3);
  await app.readFile('sb-own', '/work/README.md');
  assert.match(app.files.file.text, /run `make`/);
  await app.upload('sb-own', '/work', 'n.txt', 'hi');
  assert.ok(app.files.listing.entries.some((e) => e.name === 'n.txt'));
  const sh = await app.startShell('sb-own', '/work');
  assert.match(sh.src, /^sbx\/sandboxes\/sb-own\/execs\/e\d+\/tty$/);
  const calls = globalThis.__calls;
  const exec = calls.find((c) => c.method === 'POST' && /\/execs$/.test(c.url));
  assert.deepEqual(JSON.parse(exec.body), { argv: ['/bin/bash', '-l'], cwd: '/work', tty: true, label: 'terminal' });
  await assert.rejects(app.opAct('sb-nope', 'start'), /no such sandbox/);
});

// --- the views are level --------------------------------------------------------------

test('feature keys are <area>.<feature>[.<detail>] in a known area, each described', () => {
  for (const [k, what] of Object.entries(FEATURES)) {
    assert.match(k, /^[a-z]+(\.[a-zA-Z]+){1,2}$/, `malformed key ${k}`);
    assert.ok(k.split('.')[0] in AREAS, `${k}: unknown area`);
    assert.ok(typeof what === 'string' && what.length > 3, `${k}: describe it`);
  }
});

for (const [view, implemented] of Object.entries({ web: WEB, native: NATIVE })) {
  test(`the ${view} view implements every feature, or says why not — in files that exist`, () => {
    const g = gaps(view, implemented);
    assert.deepEqual(g.missing, [], `${view} misses features (implement them, or list them in DIFFERENCES.${view} with the reason)`);
    assert.deepEqual(g.unknown, [], `${view} implements keys model/features.js does not have`);
    assert.deepEqual(g.stale, [], `DIFFERENCES.${view} lists keys that are implemented or gone`);
    for (const [k, where] of Object.entries(implemented)) {
      const files = String(where).match(/[\w/-]+\.(?:js|html)\b/g) || [];
      assert.ok(files.length, `${k}: name the file that implements it`);
      for (const f of files) assert.ok(existsSync(TPL + f), `${k}: ${f} does not exist in the template`);
    }
    for (const [k, why] of Object.entries(DIFFERENCES[view])) assert.ok(k in FEATURES && why.length > 10, `DIFFERENCES.${view}.${k}: say why`);
  });
}

test('the model has no lit and no DOM; the native view imports the model and the runtime, not the web view', () => {
  const strip = (src) => src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:'"`\\])\/\/.*$/gm, '$1');
  for (const f of readdirSync(TPL + 'model')) {
    const src = strip(readFileSync(TPL + 'model/' + f, 'utf8'));
    assert.doesNotMatch(src, /lit-all|\bdocument\.|\bwindow\.|\b(alert|confirm|prompt)\(/, `model/${f}: no lit, no DOM, no dialogs`);
  }
  const files = ['native.js', 'native-features.js', ...readdirSync(TPL + 'native').map((f) => 'native/' + f)];
  const allowed = (spec) => spec === '/vendor/xb-native.js' || /^(model|native)\//.test(spec);
  for (const f of files) {
    const src = strip(readFileSync(TPL + f, 'utf8'));
    assert.doesNotMatch(src, /lit-all|\b(document|window)\.|\b(alert|confirm|prompt)\(|innerHTML|querySelector/, `${f}: no lit, no DOM`);
    for (const [, spec] of src.matchAll(/from '([^']+)'/g)) {
      const rel = spec.startsWith('.') ? new URL(spec, 'file:///t/' + f).pathname.slice(3) : spec;
      assert.ok(allowed(rel), `${f} imports ${spec}: the native view shares the model, not the web's views`);
    }
  }
});

// --- the native view's trees ---------------------------------------------------------------

async function run(seed, steps = []) {
  const r = await runNative({ entry: TPL + 'native.js', data: { now: NOW, self: 'apps/coding-sandbox', setup: TPL + 'test/native-stub.mjs', seed }, steps });
  assert.equal(r.fatal, null);
  assert.deepEqual(r.errors, [], 'no runtime errors');
  assert.deepEqual(r.diagnostics.filter((d) => d.level !== 'info'), [], 'no diagnostics');
  r.calls = (r.extra && r.extra.calls) || [];
  return r;
}
function all(root, m, out = [], inside = !m.in) {
  if (!root) return out;
  const hit = (n, q) => (!q.t || n.t === q.t) && Object.entries(q.p || {}).every(([k, v]) => JSON.stringify((n.p || {})[k]) === JSON.stringify(v));
  if (inside && hit(root, m)) out.push(root);
  const deeper = inside || hit(root, m.in);
  for (const c of root.c || []) all(c, m, out, deeper);
  return out;
}
const find = (tree, m) => all(tree.root || tree, m)[0] || null;
const topScreen = (tree) => { const nav = find(tree, { t: 'nav' }); return nav.c[nav.c.length - 1]; };
const called = (r, method, re) => r.calls.filter((c) => c.method === method && re.test(c.url));
const show = (tab) => ({ event: [{ t: 'picker', p: { label: 'Show' } }, 'change', { value: tab }] });

test('native: an operator\'s Sandboxes — the rows, one sandbox\'s screen, its lifecycle and snapshots', async () => {
  const r = await run(SEED, [
    { tap: { t: 'row', p: { title: 'api-dev' } } },
    { snapshot: 'op' },
    { tap: { t: 'button', p: { label: 'Stop' }, in: { t: 'screen', p: { title: 'api-dev' } } } },
    { input: [{ t: 'field', p: { label: 'Name' } }, 'nightly'] },
    { tap: { t: 'button', p: { label: 'Take a snapshot' } } },
  ]);
  const root = find(r.snapshots.op, { t: 'screen', p: { title: 'Coding sandboxes' } });
  assert.deepEqual(find(root, { t: 'picker', p: { label: 'Show' } }).p.options.map((o) => o.value), ['ops', 'images', 'settings', 'mine']);
  assert.deepEqual(all(root, { t: 'row', in: { t: 'section', p: { title: 'Sandboxes' } } }).map((n) => n.p.title),
    ['frontend', 'api-dev', 'shell box', 'mine', 'rusty', 'web']);
  const op = topScreen(r.snapshots.op);
  assert.equal(op.p.title, 'api-dev');
  assert.equal(find(op, { t: 'row', p: { title: 'Consumer' } }).p.detail, 'apps/agent');
  assert.equal(find(op, { t: 'row', p: { title: 'Isolation' } }).p.detail, 'VM');
  assert.ok(find(op, { t: 'button', p: { label: 'Delete' } }).p.confirm.destructive, 'delete is confirmed');
  assert.equal(called(r, 'POST', /\/ops\/sandboxes\/sb-api\/stop\?wait=30$/).length, 1);
  const snap = called(r, 'POST', /\/ops\/sandboxes\/sb-api\/snapshots$/);
  assert.equal(JSON.parse(snap[0].body).name, 'nightly');
  assert.ok(find(topScreen(r.tree), { t: 'row', p: { title: 'nightly' } }), 'the snapshot is listed');
});

test('native: Images and Settings — a rebuild, the mode, a person\'s quota', async () => {
  const r = await run(SEED, [
    show('images'),
    { tap: { t: 'row', p: { title: 'Rust' } } },
    { tap: { t: 'button', p: { label: 'Rebuild' } } },
    { tap: { t: 'button', p: { label: 'Edit' } } },
    { input: [{ t: 'field', p: { label: 'Title' } }, 'Rust nightly'] },
    { tap: { t: 'button', p: { label: 'Save' } } },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    show('settings'),
    { tap: { t: 'row', p: { title: 'Medium' } } },
    { snapshot: 'size' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { tap: { t: 'row', p: { title: 'Layout, idle stop, mounts' } } },
    { input: [{ t: 'field', p: { label: 'A mount' } }, 'res:apps/coding-sandbox/cache /cache'] },
    { tap: { t: 'button', p: { label: 'Add the mount' } } },
    { snapshot: 'advanced' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { snapshot: 'settings' },
    { event: [{ t: 'picker', p: { label: 'Mode' } }, 'change', { value: 'namespace' }] },
    { tap: { t: 'button', p: { label: 'Save the mode' } } },
    { tap: { t: 'button', p: { label: 'A consumer\'s or a person\'s own quota' } } },
    { event: [{ t: 'picker', p: { label: 'For' } }, 'change', { value: 'person' }] },
    { input: [{ t: 'field', p: { label: 'User id' } }, 'alice'] },
    { input: [{ t: 'field', p: { label: 'running' } }, '5'] },
    { tap: { t: 'button', p: { label: 'Save' } } },
  ]);
  assert.equal(called(r, 'POST', /\/ops\/images\/rust\/build$/).length, 1);
  assert.equal(find(topScreen(r.snapshots.size), { t: 'field', p: { label: 'Memory, MiB' } }).p.value, '4096');
  assert.ok(find(topScreen(r.snapshots.advanced), { t: 'row', p: { title: 'res:apps/coding-sandbox/cache → /cache' } }));
  const st = r.snapshots.settings;
  assert.ok(find(st, { t: 'row', p: { title: 'open', detail: 'not offered' } }), 'an unbound class');
  const puts = called(r, 'PUT', /\/ops\/config$/).map((c) => JSON.parse(c.body));
  assert.equal(puts[0].images.find((i) => i.id === 'rust').title, 'Rust nightly');
  assert.deepEqual(puts[1], { mode: 'namespace' });
  assert.deepEqual(puts[2].quotas.people, { alice: { sandboxes: 4, running: 5 } });
});

test('native: yours — create, files (a directory, a file, a download), a terminal and its end', async () => {
  const r = await run(SEED, [
    show('mine'),
    { tap: { t: 'button', p: { label: 'New sandbox' } } },
    { input: [{ t: 'field', p: { label: 'Name' }, in: { t: 'sheet' } }, 'scratch'] },
    { event: [{ t: 'picker', p: { label: 'Network' } }, 'change', { value: 'internet' }] },
    { tap: { t: 'button', p: { label: 'Create' } } },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { tap: { t: 'row', p: { title: 'mine' } } },
    { tap: { t: 'row', p: { title: 'Files' } } },
    { snapshot: 'files' },
    { tap: { t: 'row', p: { title: 'README.md' } } },
    { snapshot: 'file' },
    { tap: { t: 'button', p: { label: 'Download' }, in: { t: 'screen', p: { title: 'README.md' } } } },
    { event: [{ t: 'nav' }, 'pop', { depth: 2 }] },
    { tap: { t: 'row', p: { title: 'Terminal' } } },
    { snapshot: 'term' },
    { tap: { t: 'button', p: { label: 'End' } } },
  ]);
  const made = JSON.parse(called(r, 'POST', /\/sbx\/sandboxes\?wait=60$/)[0].body);
  assert.deepEqual(made, { name: 'scratch', image: 'base', size: 'small', egress: 'internet', visibility: 'private' });
  const files = topScreen(r.snapshots.files);
  assert.equal(files.p.subtitle, '/work');
  assert.deepEqual(all(files, { t: 'row' }).map((n) => n.p.title), ['src', 'logo.bin', 'README.md']);
  assert.match(find(topScreen(r.snapshots.file), { t: 'code' }).p.text, /run `make` to build/);
  const share = r.messages.find((m) => m.op === 'call' && m.what === 'share');
  assert.ok(share && JSON.stringify(share).includes('/api/apps/coding-sandbox/sbx/sandboxes/sb-own/files/content?path=%2Fwork%2FREADME.md'), JSON.stringify(share));
  const term = find(topScreen(r.snapshots.term), { t: 'terminal' });
  assert.match(term.p.src, /^sbx\/sandboxes\/sb-own\/execs\/e\d+\/tty$/, 'the terminal attaches to its own shell, on this tile\'s route');
  const eid = term.p.src.split('/')[4];
  assert.equal(called(r, 'DELETE', new RegExp(`/sbx/sandboxes/sb-own/execs/${eid}$`)).length, 1, 'End ends the shell');
});

test('native: someone who isn\'t an operator sees only theirs', async () => {
  const r = await run(READER);
  assert.equal(find(r.tree, { t: 'picker', p: { label: 'Show' } }), null);
  assert.ok(find(r.tree, { t: 'section', p: { title: 'Your sandboxes' } }));
  assert.equal(called(r, 'GET', /\/ops\//).length, 0);
});
