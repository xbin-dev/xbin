// hack/agent-template-harness-start.test.mjs — starting a conversation with a
// coding agent in the agent template (D-harness §8 U2): "Who
// answers" (model/harness-start.js agentPicker), the sandbox a coding agent
// starts in (the ones it fits, the one last used with it, the create form
// filled in for it), the setup card, a row's kind and the top bar's chip —
// and the native view over the fixtures (test/harness-fixtures.mjs): the
// home toolbar's picker, the class and model pickers it hides, the ask it
// sends, the new-chat sheet's section, the setup notice, a conversation's
// badge and the drawer's rows. Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';
import { runNative } from './xbn/node.mjs';

const TPL = new URL('../builtin-templates/agent/', import.meta.url);
const KIT = new URL('../web/bx-kit.js', import.meta.url).href;
registerHooks({ resolve: (spec, ctx, next) => (spec === '/vendor/bx-kit.js' ? { url: KIT, shortCircuit: true } : next(spec, ctx)) });

const HS = await import(new URL('model/harness-start.js', TPL));
const { catalogOf } = await import(new URL('model/harness.js', TPL));
const { listOf } = await import(new URL('model/sandboxes.js', TPL));
const C = await import(new URL('model/classes.js', TPL));
const { harnessSeed, API_DEV, SBX } = await import(new URL('test/harness-fixtures.mjs', TPL));

const seed = harnessSeed();
const cat = catalogOf({ harnesses: seed.harnesses });
const list = listOf({ sandboxes: seed.sandboxes, managers: [{ provider: SBX, title: 'Coding sandboxes', ok: true, egress: ['none', 'internet', 'open'] }] });
const classes = C.listOf({ classes: [{ id: 'internal', name: 'Internal', icon: '🔒', toolsets: ['internal'] }, seed.classes.classes[0]], default: 'internal' });
const CODING = seed.classes.classes[0];

test('who answers: the built-in agent, then each coding agent — monogram, class, why not', () => {
  const p = HS.agentPicker(cat, 'agent', { classes, classId: 'internal', remembered: { claude: API_DEV, codex: API_DEV }, list, manager: true });
  assert.equal(p.shown, true);
  assert.equal(p.value, 'agent');
  assert.equal(p.harness, null);
  assert.deepEqual(p.rows.map((r) => [r.value, r.mono, r.disabled]), [['agent', '✦', false], ['claude', 'CC', false], ['codex', 'CX', false], ['gemini', 'GM', true], ['opencode', 'OC', true]]);
  assert.equal(p.rows[0].name, 'Agent (built in)');
  assert.equal(p.rows[1].detail, 'in ▣ Coding · signed in on api-dev', 'the class it resolves to, and its sign-in where you last used it');
  assert.equal(p.rows[2].detail, 'in ▣ Coding · not signed in on api-dev');
  assert.match(p.rows[3].why, /^needs internet access/);
  assert.equal(p.rows[4].why, "no bound sandbox manager's image has it");
  assert.equal(p.empty, '');
  const c = HS.agentPicker(cat, 'claude', { classes, classId: 'internal' });
  assert.deepEqual([c.value, c.mono, c.label, c.rows[1].on], ['claude', 'CC', 'Claude Code', true]);
  assert.match(c.title, /in ▣ Coding, in a coding sandbox; fixed once the chat starts/);
  const g = HS.agentPicker(cat, 'gemini', {});
  assert.equal(g.value, 'agent', 'a pick that isn\'t available: the built-in agent answers');
  const none = catalogOf({ harnesses: seed.harnesses.map((h) => ({ ...h, available: false, reason: 'no-class' })) });
  assert.equal(HS.agentPicker(none, 'agent', {}).shown, false, 'nothing to choose: no picker');
  assert.match(HS.agentPicker(none, 'agent', { manager: true }).empty, /^Bind a sandbox manager/);
  assert.match(HS.agentPicker(none, 'agent', {}).empty, /^Ask a manager/);
  assert.equal(HS.agentPicker(none, 'claude', {}).shown, true, 'still shown while a stale pick is there');
});

test('its sandbox: the ones it fits first; the one last used with it; the create form filled in', () => {
  const claude = cat.harnesses[0];
  const opts = HS.sandboxOptions(claude, list);
  assert.deepEqual(opts.map((o) => [o.name, o.disabled]), [['api-dev', false], ['scratch', true], ['go-dev', true]]);
  assert.equal(opts[0].label, 'api-dev · running · signed in');
  assert.match(opts[1].why, /egress is none/);
  assert.match(opts[2].why, /image doesn't have Claude Code/);
  assert.equal(HS.preferredSandbox(claude, list, '', '').value, API_DEV);
  assert.equal(HS.preferredSandbox(claude, list, `${SBX}|sb-9c1d`, '').value, API_DEV, 'a remembered one it no longer fits is passed over');
  const empty = listOf({ sandboxes: seed.sandboxes.filter((s) => s.ref !== API_DEV), managers: list.managers });
  assert.equal(HS.preferredSandbox(claude, empty, API_DEV, ''), null);
  assert.deepEqual(HS.createPrefill(claude, empty, CODING), { provider: SBX, image: 'base', egress: 'internet', name: 'claude-dev' });
  const taken = listOf({ sandboxes: [...empty.sandboxes, { ref: `${SBX}|x`, name: 'claude-dev' }], managers: list.managers });
  assert.equal(HS.createPrefill(claude, taken, CODING).name, 'claude-dev-2');
  assert.equal(HS.createPrefill(claude, listOf({ sandboxes: [], managers: [] }), CODING), null, 'no manager: nothing to make');
  const noNet = { ...CODING, sandboxEgress: ['none'] };
  assert.equal(HS.createPrefill(claude, empty, noNet), null, 'a class that allows no egress: nothing it could start in');
});

test('its sandbox: one the class it starts in may not use is passed over — a refused ask doesn\'t pick it again', () => {
  const claude = cat.harnesses[0];
  const held = { ...seed.sandboxes[0], labels: { 'xbin.agent/internal': 'true' } }; // held internal data: the coding class reaches out
  const other = { ...seed.sandboxes[0], ref: `${SBX}|sb-other`, id: 'sb-other', name: 'other', visibility: 'private', labels: {} };
  const two = listOf({ sandboxes: [held, other, ...seed.sandboxes.slice(1)], managers: list.managers });
  assert.equal(HS.preferredSandbox(claude, two, API_DEV, '').value, API_DEV, 'the harness alone fits it');
  const opts = HS.sandboxOptions(claude, two, CODING);
  assert.match(opts.find((o) => o.value === API_DEV).why, /held data from an internal-reach conversation/);
  assert.equal(HS.preferredSandbox(claude, two, API_DEV, API_DEV, CODING).value, `${SBX}|sb-other`, 'its class may not use it: the next that fits');
  const only = { ...CODING, managers: ['apps/other-sandboxes'] };
  assert.equal(HS.preferredSandbox(claude, list, API_DEV, '', only), null, 'a class that allows none of its managers: none');
  assert.equal(HS.setupOf(claude, list, API_DEV, only).kind, 'create', '…and the setup card says so');
  // keepSandbox: the pick a refused ask dropped (sbx.refused) isn't picked again
  const chosen = [];
  const app = { sel: null, classId: 'internal', classes: C.listOf({ classes: [CODING], default: 'internal' }),
    harness: { picked: () => claude, sandboxes: { claude: API_DEV }, rememberSandbox(p, ref) { this.sandboxes = { ...this.sandboxes, [p]: ref }; }, load: async () => {} },
    sbx: { list: two, pick: null, ensure() {}, choose: async (ref) => { chosen.push(ref); app.sbx.pick = { ref }; } } };
  HS.keepSandbox(app);
  assert.deepEqual(chosen, [`${SBX}|sb-other`]);
  assert.equal(app.harness.sandboxes.claude, `${SBX}|sb-other`, 'and remembered for it');
});

test('the setup card: no sandbox fits → Create; not signed in there → say so, and where it is', () => {
  const [claude, codex] = cat.harnesses;
  assert.equal(HS.setupOf(claude, listOf(null), '', CODING), null, 'the list not read yet');
  assert.equal(HS.setupOf(claude, list, API_DEV, CODING), null, 'signed in: nothing to set up');
  const empty = listOf({ sandboxes: seed.sandboxes.filter((s) => s.ref !== API_DEV), managers: list.managers });
  const c = HS.setupOf(claude, empty, '', CODING);
  assert.equal(c.kind, 'create');
  assert.equal(c.text, 'Claude Code needs a coding sandbox with internet access. Its sign-in is kept in that sandbox — reuse one to stay signed in.');
  assert.deepEqual(c.create, { label: 'Create claude-dev', form: { provider: SBX, image: 'base', egress: 'internet', name: 'claude-dev' } });
  const s = HS.setupOf(codex, list, API_DEV, CODING);
  assert.equal(s.kind, 'signin');
  assert.equal(s.title, "Codex isn't signed in on api-dev");
  assert.match(s.text, /everyone who may use it acts as you with Codex/, 'a team sandbox: its co-users act as you');
  assert.equal(s.use, null);
  const two = listOf({ sandboxes: [...seed.sandboxes, { ...seed.sandboxes[0], ref: `${SBX}|sb-other`, name: 'other', visibility: 'private' }], managers: list.managers });
  const cx = { ...codex, sandboxes: { ...codex.sandboxes, [`${SBX}|sb-other`]: { installed: true, signedIn: true } } };
  assert.deepEqual(HS.setupOf(cx, two, API_DEV, CODING).use, { label: 'Use other (signed in)', ref: `${SBX}|sb-other` });
});

test('a row\'s kind, the top bar\'s chip', () => {
  const rows = Object.fromEntries(seed.runs.map((r) => [r.id, r]));
  assert.deepEqual(HS.kindOf(rows[21]), { provider: 'claude', mono: 'CC', name: 'Claude Code', title: 'Claude Code answers here — in ▣ api-dev' });
  assert.equal(HS.kindOf(rows[24]).mono, 'CX');
  assert.equal(HS.kindOf(rows[25]), null, 'the built-in agent\'s: none');
  const t = HS.topChip(seed.views[21]);
  assert.deepEqual([t.mono, t.label, t.tone], ['CC', 'Claude Code · ready', 'ok']);
  assert.match(t.title, /^Claude Code answers this conversation in ▣ api-dev at \/work\/api — fixed for its life/);
  assert.equal(t.shared, 'api-dev is shared — the people who may use it can read what Claude Code does here');
  assert.equal(HS.topChip(seed.views[25]), null);
});

test('the new-chat dialog\'s ask part: a coding agent\'s, or the built-in agent\'s', () => {
  const app = { classId: 'internal', model: 'apps/llm|big',
    harness: { find: (id) => cat.harnesses.find((h) => h.id === id) || null, options: { claude: { model: 'sonnet' } } } };
  assert.deepEqual(HS.newChatPick(app, 'claude', API_DEV, '/work/api'), { harness: { provider: 'claude', options: { model: 'sonnet' } }, class: 'coding',
    sandbox: { ref: API_DEV, cwd: '/work/api' }, system: undefined, model: undefined });
  assert.deepEqual(JSON.parse(JSON.stringify(HS.newChatPick(app, 'codex', ''))), { harness: { provider: 'codex' }, class: 'coding' }, 'no sandbox: none sent (the backend says why)');
  assert.deepEqual(HS.newChatPick(app, 'agent', ''), { harness: undefined, model: 'apps/llm|big' });
  assert.deepEqual(HS.newChatPick(app, 'gemini', API_DEV), { harness: undefined, model: 'apps/llm|big' }, 'not available: the built-in agent');
});

// --- the native view -----------------------------------------------------------------------------

async function run(steps, { hash = '', mut = (s) => s } = {}) {
  const r = await runNative({ entry: new URL('native.js', TPL).pathname,
    data: { now: Date.UTC(2026, 8, 30, 12), self: 'apps/agent', setup: new URL('test/native-stub.mjs', TPL).pathname, seed: mut(harnessSeed()) },
    steps, state: hash ? { hash } : null });
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
const asks = (r) => r.calls.filter((c) => c.method === 'POST' && /\/ask$/.test(c.url)).map((c) => JSON.parse(c.body));
const WHO = { t: 'picker', p: { label: 'Who answers' } };

test('native: "Who answers" in the home toolbar; picking a coding agent hides the class and model, narrows the sandbox; the ask', async () => {
  const r = await run([
    { wait: 50 }, { snapshot: 'home' },
    { event: [WHO, 'change', { value: 'gemini' }] }, { snapshot: 'refused' },
    { event: [WHO, 'change', { value: 'claude' }] }, { wait: 50 }, { snapshot: 'claude' },
    { event: [{ t: 'composer' }, 'send', { value: 'fix the flaky test' }] }, { wait: 50 }, { snapshot: 'chat' },
  ]);
  const home = r.snapshots.home;
  const who = find(home, { t: 'picker', p: { label: 'Who answers' }, in: { t: 'toolbar' } });
  assert.ok(who, 'in the home toolbar');
  assert.equal(who.p.value, 'agent');
  assert.deepEqual(who.p.options.map((o) => o.label), ['Agent (built in)', 'CC · Claude Code', 'CX · Codex', 'GM · Gemini CLI — unavailable', 'OC · opencode — unavailable']);
  assert.ok(find(home, { t: 'picker', p: { label: 'Class' } }), 'the class picker, while the built-in agent answers');
  assert.match(JSON.stringify(r.snapshots.refused), /Gemini CLI: needs internet access/, 'an unavailable one says why');
  const c = r.snapshots.claude;
  assert.equal(find(c, WHO).p.value, 'claude');
  assert.equal(find(c, { t: 'picker', p: { label: 'Class' } }), null, 'the class resolves');
  assert.equal(find(c, { t: 'picker', p: { label: 'Model' } }), null, 'a coding agent\'s model is its own');
  assert.equal(find(c, { t: 'picker', p: { label: 'Sandbox' } }).p.value, API_DEV, 'its sandbox, preselected');
  assert.match(JSON.stringify(find(c, { t: 'picker', p: { label: 'Sandbox' } }).p.options), /scratch · egress none — unavailable|scratch — unavailable/);
  assert.equal(find(c, { t: 'composer' }).p.placeholder, 'ask Claude Code — it works in api-dev…');
  const [ask] = asks(r);
  assert.deepEqual([ask.harness, ask.class, ask.sandbox, 'model' in ask], [{ provider: 'claude' }, 'coding', { ref: API_DEV }, false], JSON.stringify(ask));
  const badge = find(r.snapshots.chat, { t: 'badge', in: { t: 'toolbar' } });
  assert.equal(badge.p.text, 'CC starting 👥', 'short, so More stays on the bar');
  assert.equal(badge.p.tone, 'accent');
  assert.ok(r.calls.some((c) => c.method === 'PUT' && /prefs\/agent$/.test(c.url) && c.body === '"claude"'), 'remembered (prefs/agent)');
  assert.ok(r.calls.some((c) => c.method === 'PUT' && /prefs\/harness-sandbox$/.test(c.url)), '…and its sandbox (prefs/harness-sandbox)');
});

test('native: the new-chat sheet — who answers, a coding agent\'s sandbox, no class; the drawer\'s rows; a conversation\'s badge', async () => {
  const sheet = { t: 'sheet', p: { title: 'New chat' } };
  const r = await run([
    { wait: 50 },
    { tap: { t: 'button', p: { label: 'Conversations' } } }, { wait: 50 },
    { snapshot: 'drawer' },
    { tap: { t: 'row', p: { title: 'New chat with options…' } } },
    { snapshot: 'sheet' },
    { event: [{ ...WHO, in: sheet }, 'change', { value: 'codex' }] },
    { event: [{ t: 'field', p: { placeholder: 'what should it do?' } }, 'input', { value: 'port the CLI' }] },
    { snapshot: 'codex' },
    { tap: { t: 'button', p: { label: 'Start' }, in: sheet } }, { wait: 50 },
  ]);
  const rows = all(r.snapshots.drawer.root, { t: 'row', in: { t: 'sheet' } }).filter((x) => /Fix the flaky test|Port the CLI|Refactor the API/.test(x.p.title));
  assert.deepEqual(rows.map((x) => [x.p.title, x.p.subtitle ?? '']).sort(), [['Fix the flaky test', 'Claude Code'], ['Port the CLI', 'Codex'], ['Refactor the API', '']]);
  const s0 = find(r.snapshots.sheet, sheet);
  assert.equal(find(s0, WHO).p.value, 'agent');
  assert.ok(find(s0, { t: 'section', p: { title: 'Class' } }), 'the built-in agent: its class');
  assert.ok(find(s0, { t: 'field', p: { label: 'Instructions' } }), '…and its instructions');
  const s1 = find(r.snapshots.codex, sheet);
  assert.equal(find(s1, { t: 'section', p: { title: 'Class' } }), null, 'a coding agent: its class resolves');
  assert.equal(find(s1, { t: 'field', p: { label: 'Instructions' } }), null, '…and it keeps its own instructions (none typed there to be dropped)');
  assert.equal(find(s1, { t: 'picker', p: { label: 'Sandbox' } }).p.value, API_DEV);
  assert.match(find(s1, { t: 'section', p: { title: 'Who answers' } }).p.footer, /^In ▣ Coding · not signed in on api-dev\. A coding agent keeps its own instructions/);
  const [ask] = asks(r);
  assert.deepEqual([ask.harness, ask.class, ask.sandbox, ask.text], [{ provider: 'codex' }, 'coding', { ref: API_DEV }, 'port the CLI'], JSON.stringify(ask));
});

test('native: no sandbox fits — the setup notice, Create filled in; a harness conversation\'s badge', async () => {
  const noDev = (s) => { s.sandboxes = s.sandboxes.filter((x) => x.ref !== API_DEV); return s; };
  const r = await run([
    { wait: 50 },
    { event: [WHO, 'change', { value: 'claude' }] }, { wait: 50 }, { snapshot: 'setup' },
    { tap: { t: 'button', p: { label: 'Create claude-dev' } } }, { wait: 20 }, { snapshot: 'form' },
  ], { mut: noDev });
  const sec = find(r.snapshots.setup, { t: 'section', p: { title: 'Claude Code needs a coding sandbox' } });
  assert.ok(sec, 'the setup notice');
  assert.match(JSON.stringify(sec), /Its sign-in is kept in that sandbox/);
  const form = JSON.stringify(r.snapshots.form);
  assert.match(form, /"value":"claude-dev"/, 'the create form, its name filled in');
  assert.match(form, /"value":"internet"/, '…internet');
  const c = await run([{ wait: 50 }, { snapshot: 'chat' }], { hash: 'c=24' });
  assert.equal(find(c.snapshots.chat, { t: 'badge', in: { t: 'toolbar' } }).p.text, 'CX sign-in 👥');
});
