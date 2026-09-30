// hack/agent-template-harness-manage.test.mjs — coding agents for the agent
// template's managers (D147 §8 U8): the class editor's Coding
// agents toolset and `harnesses` field (model/classes.js: a form and back,
// the checklist, what the toolset needs), the catalog as the managers' view
// says it (model/harness-manage.js), and the native view's class form and
// Settings → Coding agents over the fixtures (test/harness-fixtures.mjs).
// Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';
import { runNative } from './xbn/node.mjs';

const TPL = new URL('../builtin-templates/agent/', import.meta.url);
const KIT = new URL('../web/bx-kit.js', import.meta.url).href;
registerHooks({ resolve: (spec, ctx, next) => (spec === '/vendor/bx-kit.js' ? { url: KIT, shortCircuit: true } : next(spec, ctx)) });

const C = await import(new URL('model/classes.js', TPL));
const M = await import(new URL('model/harness-manage.js', TPL));
const { catalogOf } = await import(new URL('model/harness.js', TPL));
const { listOf } = await import(new URL('model/sandboxes.js', TPL));
const { harnessSeed, NOW, API_DEV, SBX } = await import(new URL('test/harness-fixtures.mjs', TPL));

const seed = harnessSeed();
const cat = catalogOf({ harnesses: seed.harnesses });
const CODING = seed.classes.classes[0];

test('the class editor: the Coding agents toolset and which of them — a form and back', () => {
  assert.ok(C.TOOLSETS.some((t) => t.id === 'harness'), 'a toolset of its own');
  const f = C.formOf(CODING);
  assert.deepEqual([f.harnessesMode, f.harnesses], ['all', '']);
  assert.equal(C.classOf(f).harnesses, 'all');
  assert.deepEqual(C.classOf(f).toolsets, ['files', 'web', 'sandbox', 'harness', 'subagents', 'skills'], 'in the editor\'s order');
  f.harnessesMode = 'only';
  f.harnesses = C.toggleName(C.toggleName('', 'claude'), 'codex');
  assert.deepEqual(C.classOf(f).harnesses, ['claude', 'codex']);
  const back = C.formOf({ ...CODING, harnesses: ['claude'] });
  assert.deepEqual([back.harnessesMode, back.harnesses], ['only', 'claude']);
  f.toolsets = C.toggle(f.toolsets, 'harness', false);
  assert.equal('harnesses' in C.classOf(f), false, 'without the toolset: not sent (the backend keeps what it has)');
  assert.deepEqual([C.blankForm().harnessesMode, 'harnesses' in C.classOf({ ...C.blankForm(), id: 'x' })], ['all', false]);
  // the checklist: the catalog's, then what the class lists beyond it
  assert.deepEqual(C.harnessNames('codex, mine', cat).map((n) => [n.id, n.name, n.on]),
    [['claude', 'Claude Code', false], ['codex', 'Codex', true], ['gemini', 'Gemini CLI', false], ['opencode', 'opencode', false], ['mine', 'mine', true]]);
});

test('the class editor: what the toolset needs, in the backend\'s words', () => {
  const f = C.formOf(CODING);
  assert.equal(C.harnessWhy(f), '');
  assert.equal(C.harnessWhy({ ...f, toolsets: C.toggle(f.toolsets, 'sandbox', false) }), C.HARNESS_NEEDS);
  assert.equal(C.harnessWhy({ ...f, egress: ['none'] }), C.HARNESS_NEEDS, 'a sandbox that reaches nothing');
  assert.equal(C.harnessWhy({ ...f, egress: ['none'], toolsets: C.toggle(f.toolsets, 'harness', false) }), '', 'no toolset: nothing to say');
  assert.equal(C.HARNESS_NEEDS, 'the harness toolset needs sandbox and an egress other than none — a coding agent must reach its provider');
});

test('the catalog for managers: availability, images, sandboxes, classes, modes, sign-in', () => {
  const list = listOf({ sandboxes: seed.sandboxes, managers: [] });
  const state = C.listOf({ classes: [CODING], default: 'coding' });
  const rows = M.catalogRows(cat, list, state, NOW);
  assert.deepEqual(rows.map((r) => [r.id, r.mono, r.available]), [['claude', 'CC', true], ['codex', 'CX', true], ['gemini', 'GM', false], ['opencode', 'OC', false]]);
  const [claude, codex, gemini, opencode] = rows;
  assert.deepEqual(claude.images, [{ advertised: true, label: 'Coding sandboxes · base — internet, open network' }]);
  assert.deepEqual(claude.sandboxes, [{ ref: API_DEV, name: 'api-dev', label: 'installed · signed in · 1 h ago', tone: 'ok' }]);
  assert.equal(codex.sandboxes[0].label, 'installed · not signed in · 10 min ago');
  assert.equal(codex.sandboxes[0].tone, 'warn');
  assert.deepEqual(claude.classes, ['▣ Coding']);
  assert.equal(M.modesWords(claude), 'default Ask before acting · Auto: Accept edits · plan: Plan · the owner only: Bypass permissions');
  assert.equal(M.modesWords(codex), 'default Read only · Auto: Agent · the owner only: Full access', 'plan the same as the default: not said again');
  assert.equal(claude.login, 'CLAUDE_CODE_REMOTE=1 claude /login');
  assert.deepEqual(claude.options, ['Model', 'Reasoning effort']);
  assert.match(gemini.why, /^needs internet access/);
  assert.equal(gemini.images[0].label, 'Coding sandboxes · base — no network (egress none only)');
  assert.equal(M.modesWords(opencode), 'no auto mode');
  const pre = catalogOf({ harnesses: [{ ...seed.harnesses[0], images: [{ provider: SBX, manager: 'Coding sandboxes', image: 'base', advertised: false, egress: ['internet'] }],
    sandboxes: { [`${SBX}|gone`]: { installed: false } } }] });
  const [r] = M.catalogRows(pre, list, state, NOW);
  assert.match(r.images[0].label, /its manager doesn't say — a check decides/);
  assert.deepEqual(r.sandboxes[0], { ref: `${SBX}|gone`, name: 'gone', label: 'missing', tone: 'bad' }, 'one the list doesn\'t have: its id');
  assert.deepEqual(M.probeTargets(list).map((t) => t.name), ['api-dev', 'scratch'], 'running ones you may use');
});

// --- the native view -------------------------------------------------------------------------

async function run(steps, mut = (s) => s) {
  const r = await runNative({ entry: new URL('native.js', TPL).pathname,
    data: { now: NOW, self: 'apps/agent', setup: new URL('test/native-stub.mjs', TPL).pathname, seed: mut(harnessSeed()) }, steps });
  assert.equal(r.fatal, null);
  assert.deepEqual(r.errors, [], 'no runtime errors');
  assert.deepEqual(r.diagnostics.filter((d) => d.level !== 'info'), [], 'no diagnostics');
  r.calls = (r.extra && r.extra.calls) || [];
  return r;
}
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
const topScreen = (tree) => { const nav = find(tree, { t: 'nav' }); return nav.c[nav.c.length - 1]; };

test('native: a class\'s Coding agents — the toggle, the checklist, what it needs, the refusal said', async () => {
  const toggle = (label) => ({ t: 'toggle', has: label });
  const r = await run([
    { wait: 50 },
    { tap: { t: 'button', p: { label: 'Settings' } } },
    { tap: { t: 'row', p: { title: 'Classes' } } }, { wait: 50 },
    { tap: { t: 'row', has: 'Coding', in: { t: 'screen', p: { title: 'Classes' } } } }, { wait: 20 },
    { snapshot: 'form' },
    { event: [{ t: 'picker', p: { label: 'Coding agents' } }, 'change', { value: 'only' }] },
    { event: [{ t: 'toggle', p: { label: 'Claude Code' } }, 'change', { value: true }] },
    { snapshot: 'only' },
    { event: [toggle('Coding sandbox —'), 'change', { value: false }] },
    { snapshot: 'lame' },
    { tap: { t: 'button', p: { label: 'Save' } } }, { wait: 50 },
    { snapshot: 'refused' },
  ]);
  const form = topScreen(r.snapshots.form);
  assert.equal(find(form, { t: 'toggle', has: 'Coding agents —' }).p.value, true, 'the toolset, on for the built-in coding class');
  assert.equal(find(form, { t: 'picker', p: { label: 'Coding agents' } }).p.value, 'all');
  const only = topScreen(r.snapshots.only);
  assert.deepEqual(all(only, { t: 'toggle', in: { t: 'section', p: { title: 'Coding agents it may start or spawn' } } }).map((t) => [t.p.label, t.p.value]),
    [['Claude Code', true], ['Codex', false], ['Gemini CLI', false], ['opencode', false]]);
  assert.match(find(topScreen(r.snapshots.lame), { t: 'section', p: { title: 'Coding agents it may start or spawn' } }).p.footer, /^⚠ the harness toolset needs sandbox/);
  assert.match(JSON.stringify(topScreen(r.snapshots.refused)), /class coding: the harness toolset needs sandbox and an egress other than none/, 'the backend\'s refusal, said');
});

test('native: Settings → Coding agents — each one, and checking a running sandbox', async () => {
  const r = await run([
    { wait: 50 },
    { tap: { t: 'button', p: { label: 'Settings' } } },
    { tap: { t: 'row', p: { title: 'Coding agents' } } }, { wait: 50 },
    { snapshot: 'cat' },
    { tap: { t: 'row', p: { title: 'scratch' }, in: { t: 'section', p: { title: 'Check a running sandbox now' } } } }, { wait: 50 },
    { snapshot: 'checked' },
  ]);
  const s = topScreen(r.snapshots.cat);
  assert.equal(s.p.title, 'Coding agents');
  const secs = all(s, { t: 'section' }).map((x) => x.p.title).filter(Boolean);
  assert.deepEqual(secs, ['CC · Claude Code', 'CX · Codex', 'GM · Gemini CLI', 'OC · opencode', 'Check a running sandbox now']);
  const claude = find(s, { t: 'section', p: { title: 'CC · Claude Code' } });
  assert.equal(claude.p.footer, 'default Ask before acting · Auto: Accept edits · plan: Plan · the owner only: Bypass permissions');
  assert.deepEqual(all(claude, { t: 'row' }).map((x) => x.p.title), ['Available', 'Coding sandboxes · base — internet, open network', 'api-dev', 'Classes', 'Sign-in']);
  assert.match(JSON.stringify(find(s, { t: 'section', p: { title: 'GM · Gemini CLI' } })), /needs internet access/);
  assert.ok(r.calls.some((c) => c.url.endsWith('/harnesses?probe=' + encodeURIComponent(`${SBX}|sb-9c1d`))), 'checked now');
  const after = topScreen(r.snapshots.checked);
  assert.equal(find(after, { t: 'section', p: { title: 'Check a running sandbox now' } }).p.footer, 'checked scratch ✓');
  assert.ok(find(after, { t: 'row', p: { title: 'scratch' }, in: { t: 'section', p: { title: 'CC · Claude Code' } } }), 'what it found is listed');
});
