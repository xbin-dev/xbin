// hack/agent-template-native-render.test.mjs — the agent template's render
// preview in the app (D208; issues #41, #49, #46), out of
// agent-template-native.test.mjs (its size budget): on a phone a reader that
// fills the screen, zooms in place and shares a report's linked files; on a
// wide screen beside its conversation, half each; and what older apps get
// (canvas rev 1, split rev 2). Runs the template's native.js in the node
// runtime against test/native-stub.mjs, as the other native tests do.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { runNative } from './xbn/node.mjs';

const TPL = new URL('../builtin-templates/agent/', import.meta.url).pathname;
const NOW = Date.UTC(2026, 8, 21, 12);
const now = Math.floor(NOW / 1000);
const ME = { kind: 'user', user: 'admin', level: 'terminal', manager: true, halted: false, epochMs: 0 };

function all(root, m, out = [], inside = !m.in) {
  if (!root) return out;
  const hit = (n, q) => (!q.t || n.t === q.t) && Object.entries(q.p || {}).every(([k, v]) => JSON.stringify((n.p || {})[k]) === JSON.stringify(v))
    && (q.has == null || JSON.stringify(n.p || {}).includes(q.has));
  if (inside && hit(root, m)) out.push(root);
  const deeper = inside || hit(root, m.in);
  for (const c of root.c || []) all(c, m, out, deeper);
  return out;
}
const find = (tree, m) => all(tree.root || tree, m)[0] || null;
// one conversation, alone, in a state
const oneSeed = (view, extra = {}) => ({ me: ME, runs: [{ id: 9, title: view.run.title || 'run 9', status: view.run.status }],
  views: { 9: { access: 'owner', ...view, run: { id: 9, rootId: 9, parentId: 0, ...view.run } } }, ...extra });

const REPORT = '<h1>Q3</h1><p>See <a href="out/q3.xlsx">the figures</a>, <a href="./brief.pdf">the brief</a> and <a href="https://evil.example/x">this</a>.</p>';
const reportSeed = () => oneSeed({ run: { title: 'report', status: 'idle' }, steps: [{ id: 5, seq: 5, kind: 'render', created: now - 3600,
  detail: JSON.stringify({ path: 'r.html', version: 2, links: [{ href: 'out/q3.xlsx', path: 'out/q3.xlsx' }, { href: './brief.pdf', path: 'brief.pdf' }] }) }] },
{ routes: [['GET', '/runs/9/file\\?path=r.html$', { path: 'r.html', content: REPORT, version: 2 }]] });
const shares = (r) => r.messages.filter((m) => m.op === 'call' && m.what === 'share').map((m) => m.args.file);
const openReport = [{ event: [{ t: 'toolcard', p: { family: 'render' } }, 'open', {}] }, { wait: 30 }];
const reportRun = async (caps, steps) => {
  const r = await runNative({ caps, entry: TPL + 'native.js', state: { hash: 'c=9' },
    data: { now: NOW, self: 'apps/agent', setup: TPL + 'test/native-stub.mjs', seed: reportSeed() }, steps: [{ wait: 30 }, ...openReport, ...steps] });
  assert.equal(r.fatal, null);
  assert.deepEqual(r.errors, [], 'no runtime errors');
  assert.deepEqual(r.diagnostics.filter((d) => d.level !== 'info'), [], 'no diagnostics');
  assert.deepEqual(r.messages.filter((m) => m.op === 'unsupported'), [], 'nothing the app lacks: never the web page');
  return r;
};

test('D208: on a phone the preview is a reader — it fills the screen, − / + zoom it in place, a linked file opens the share sheet', async () => {
  const { FULL } = await import('./agent-template-native-caps.mjs');
  const r = await reportRun({ ...FULL, width: 'compact' }, [
    { snapshot: 'open' },
    { tap: { t: 'button', p: { label: 'Zoom in' } } }, { tap: { t: 'button', p: { label: 'Zoom in' } } }, { wait: 30 }, { snapshot: 'zoomed' },
    { event: [{ t: 'canvas' }, 'link', { href: 'brief.pdf' }] }, { wait: 30 },
    { event: [{ t: 'canvas' }, 'link', { href: 'https://evil.example/x' }] }, { wait: 30 },
    { tap: { t: 'button', p: { label: 'q3.xlsx' }, in: { t: 'menu', p: { label: 'Linked files' } } } }, { wait: 30 },
  ]);
  const c = find(r.snapshots.open, { t: 'canvas' });
  assert.equal(c.p.fill, true, 'the island fills the screen');
  assert.equal(c.p.zoom, 1);
  assert.ok(c.p.html.includes('href="out/q3.xlsx"') && c.p.html.includes('href="./brief.pdf"'), 'the run\'s files keep their links');
  assert.ok(!c.p.html.includes('evil.example'), 'every other off-page href is gone');
  assert.ok(find(r.snapshots.open, { t: 'toolbar', p: { place: 'bottom' } }), '− / + in a bottom toolbar');
  const z = find(r.snapshots.zoomed, { t: 'canvas' });
  assert.equal(z.p.zoom, 1.25, 'two steps up');
  assert.equal(z.p.html, c.p.html, 'zoomed in place: the page is not reloaded');
  assert.ok(find(r.snapshots.zoomed, { t: 'button', p: { label: '125%' } }), 'the percentage shows (and resets it)');
  const kept = (r.extra?.calls || []).filter((c) => c.method === 'PUT' && /\/api\/xbin\/prefs\/preview-zoom$/.test(c.url));
  assert.deepEqual(kept.map((c) => JSON.parse(c.body)), [1.1, 1.25], 'kept per person and tile, as the web keeps it (prefs/preview-zoom)');
  assert.deepEqual(shares(r), ['/api/apps/agent/runs/9/dl/brief.pdf', '/api/apps/agent/runs/9/dl/out/q3.xlsx'],
    'a tapped link (as the app resolves it) and the Linked files menu share the file under its own name; another link does nothing');
});

test('D208: an app before canvas rev 2 — the island at its height, − / + re-render it scaled, the linked files listed under it', async () => {
  const { FULL } = await import('./agent-template-native-caps.mjs');
  const r = await reportRun({ ...FULL, width: 'compact', prims: { ...FULL.prims, canvas: 1, split: 2 } }, [
    { snapshot: 'open' },
    { tap: { t: 'button', p: { label: 'Zoom out' } } }, { wait: 30 }, { snapshot: 'zoomed' },
    { tap: { t: 'row', p: { title: 'brief.pdf' } } }, { wait: 30 },
  ]);
  const c = find(r.snapshots.open, { t: 'canvas' });
  assert.equal(c.p.height, 'xl');
  assert.equal(c.p.fill, undefined);
  assert.deepEqual(c.e || [], [], 'no link event for an app that has none');
  const sec = find(r.snapshots.open, { t: 'section', p: { title: 'Files linked from this report' } });
  assert.deepEqual(all(sec, { t: 'row' }).map((x) => [x.p.title, x.p.subtitle ?? null]), [['q3.xlsx', 'out/q3.xlsx'], ['brief.pdf', null]]);
  assert.ok(find(r.snapshots.zoomed, { t: 'canvas' }).p.html.includes('html{zoom:0.9}'), 'scaled by CSS');
  assert.deepEqual(shares(r), ['/api/apps/agent/runs/9/dl/brief.pdf']);
});

test('D208: on a wide screen the preview and its conversation share it, half each; Close brings the list back and it stays closed', async () => {
  const { FULL } = await import('./agent-template-native-caps.mjs');
  const r = await reportRun({ ...FULL, width: 'regular' }, [
    { snapshot: 'halves' },
    { event: [{ t: 'split' }, 'columns', { value: 'detail' }] }, { wait: 30 }, { snapshot: 'wider' },
    { tap: { t: 'button', p: { label: 'Close' } } }, { wait: 30 }, { snapshot: 'closed' },
  ]);
  const split = find(r.snapshots.halves, { t: 'split' });
  assert.equal(split.p.halves, true);
  assert.deepEqual(split.c.map((s) => s.p.title), ['report', 'r.html'], 'the conversation, then the report');
  assert.ok(find(split.c[0], { t: 'transcript' }) && find(split.c[0], { t: 'composer' }), 'the conversation is live beside it');
  assert.ok(find(split.c[1], { t: 'toolbar', p: { place: 'leading' } }), 'the report has a Close of its own');
  assert.equal(find(r.snapshots.wider, { t: 'split' }).p.columns, 'detail', 'the sidebar button hides the conversation');
  const back = find(r.snapshots.closed, { t: 'split' });
  assert.equal(back.p.halves, undefined, 'closed: the list beside the conversation again');
  assert.deepEqual(find(back, { t: 'nav' }).c.map((s) => s.p.title), ['report']);
});

test('D208: an app with split rev 2 on a wide screen keeps the preview in the detail column', async () => {
  const { FULL } = await import('./agent-template-native-caps.mjs');
  const r = await reportRun({ ...FULL, width: 'regular', prims: { ...FULL.prims, split: 2 } }, [{ snapshot: 'open' }]);
  const split = find(r.snapshots.open, { t: 'split' });
  assert.equal(split.p.halves, undefined);
  assert.deepEqual(find(split, { t: 'nav' }).c.map((s) => s.p.title), ['report', 'r.html']);
});
