// hack/agent-template-classes.test.mjs — agent classes (D116) in the agent
// template's shared model (builtin-templates/agent/model/classes.js): the
// composer's picker and your pick, the conversation's badge, the managers'
// editor (a class as a form, its checks, what a save sends), and the app's
// wiring (the pick is remembered; a save refreshes the picker). Both views
// draw from these; their own tests (the native view's node test, the
// browser tests) check the drawing. Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';

const TPL = new URL('../builtin-templates/agent/', import.meta.url);
const KIT = new URL('../web/bx-kit.js', import.meta.url).href;
registerHooks({ resolve: (spec, ctx, next) => (spec === '/vendor/bx-kit.js' ? { url: KIT, shortCircuit: true } : next(spec, ctx)) });

const C = await import(new URL('model/classes.js', TPL).href);
const ch = await import(new URL('model/auto-channels.js', TPL).href);
const trig = await import(new URL('model/auto-triggers.js', TPL).href);
const auto = await import(new URL('model/auto.js', TPL).href);

// GET /classes as a manager sees it (the backend's classView + stored).
const listed = () => ({
  default: 'internal',
  classes: [
    { id: 'internal', name: 'Internal', icon: '🔒', description: 'systems', toolsets: ['files', 'internal'], mcp: 'all', managers: [], sandboxEgress: [],
      builtin: true, stored: false, lane: 'private', mixed: false, who: 'everyone' },
    { id: 'web', name: 'Web', icon: '🌐', description: 'the web', toolsets: ['files', 'web'], mcp: [], managers: [], sandboxEgress: [],
      builtin: true, stored: false, lane: 'web', egress: true, mixed: false, who: 'everyone' },
    { id: 'coding', name: 'Coding', icon: '▣', description: 'a sandbox', toolsets: ['sandbox', 'web', 'files'], mcp: [], managers: 'all',
      sandboxEgress: ['none', 'internet'], builtin: true, stored: true, lane: 'web', egress: true, mixed: false, who: 'everyone' },
    { id: 'bridge', name: 'Bridge', icon: '🌉', description: 'both', toolsets: ['internal', 'web'], mcp: ['apps/pg'], managers: [], sandboxEgress: [],
      builtin: false, stored: true, lane: 'private', egress: true, mixed: true, who: 'everyone' },
    { id: 'ops', name: 'Ops', icon: '', description: '', toolsets: ['internal'], mcp: 'all', managers: [], sandboxEgress: [],
      builtin: false, stored: true, lane: 'private', mixed: false, who: 'managers', model: 'apps/llm|big', system: 'be careful' },
  ],
});

test('GET /classes as the views keep it; the lanes when a backend lists none', () => {
  const st = C.listOf(listed());
  assert.equal(st.default, 'internal');
  assert.equal(st.classes.length, 5);
  assert.equal(C.listOf({ classes: [{ id: 'a' }], default: 'gone' }).default, 'a', 'a default you may not use: the first you may');
  const fb = C.listOf({});
  assert.deepEqual(fb.classes.map((c) => [c.id, C.laneOf(c)]), [['internal', 'private'], ['web', 'web']]);
  assert.ok(fb.fallback);
  assert.deepEqual(C.listOf(null).classes.map((c) => c.id), ['internal', 'web']);
});

test('your class for new chats: your pick, else the lane you picked before, else the default', () => {
  const st = C.listOf(listed());
  assert.equal(C.resolvePick(st, 'coding', 'web'), 'coding', 'your pick wins');
  assert.equal(C.resolvePick(st, 'gone', 'web'), 'internal', 'a pick you may no longer use: the default (not the old lane)');
  assert.equal(C.resolvePick(st, '', 'web'), 'web', 'no pick: the old lane — web');
  assert.equal(C.resolvePick(st, '', 'private'), 'internal', '…or private');
  assert.equal(C.resolvePick(st, '', ''), 'internal', 'nothing: the tile\'s default');
  const noWeb = { classes: st.classes.filter((c) => c.id !== 'web'), default: 'coding' };
  assert.equal(C.resolvePick(noWeb, '', 'web'), 'coding', 'an old lane whose class you may not use: the default');
});

test('reach: egress, internal reach, mixed, the lane — as the backend says them', () => {
  const r = (toolsets, sandboxEgress) => C.reach({ toolsets, sandboxEgress });
  assert.deepEqual(r(['internal', 'files']), { internal: true, egress: false, mixed: false, lane: 'private' });
  assert.deepEqual(r(['web']), { internal: false, egress: true, mixed: false, lane: 'web' });
  assert.equal(r(['internal', 'web']).mixed, true);
  assert.equal(r(['internal', 'sandbox'], ['none']).mixed, false, 'a sandbox with no network is not egress');
  assert.equal(r(['internal', 'sandbox'], ['none', 'internet']).mixed, true);
  assert.equal(r(['sandbox'], ['none']).lane, 'private', 'no egress, no internal reach: the private lane');
  assert.equal(C.laneOf(null), 'private');
});

test('the composer\'s picker: at home, icon + name, descriptions and warnings in the menu', () => {
  const st = C.listOf(listed());
  const p = C.classPicker(null, st, 'bridge');
  assert.equal(p.shown, true);
  assert.deepEqual([p.value, p.label, p.mixed], ['bridge', '🌉 Bridge', true]);
  assert.match(p.title, /^Class for your next new chat: Bridge — both \(⚠ can move internal data out\)$/);
  assert.deepEqual(p.rows.map((x) => [x.value, x.on, x.mixed, x.managers, x.nativeIcon]), [
    ['internal', false, false, false, 'lock'], ['web', false, false, false, 'globe'], ['coding', false, false, false, 'terminal'],
    ['bridge', true, true, false, 'lock'], ['ops', false, false, true, 'lock']]);
  assert.equal(C.classPicker({ run: { id: 1 } }, st, 'bridge').shown, false, 'an open conversation\'s class is fixed');
  assert.equal(C.classPicker(null, null, '').shown, false, 'nothing before the classes are read');
  assert.equal(C.classPicker(null, st, 'gone').value, 'internal', 'a pick not listed shows the default');
});

test('the badge: the conversation\'s class, the warning of a mixed one, the lane of an older view', () => {
  const b = C.badge({ class: { id: 'bridge', name: 'Bridge', icon: '🌉', description: 'both', mixed: true } });
  assert.deepEqual([b.label, b.warn, b.mixed], ['🌉 Bridge', '⚠ can move internal data out', true]);
  assert.match(b.title, /^Bridge: both — fixed for this conversation$/);
  assert.match(b.warnTitle, /steer the agent into sending internal data outside/);
  assert.equal(C.badge({ class: { id: 'web', name: 'Web', icon: '🌐' } }).warn, '');
  assert.equal(C.badge({ config: { toolset: 'web' } }).label, '🌐 web', 'no class in the view: its lane');
  assert.equal(C.badge({ config: {} }).label, '🔒 internal');
  assert.equal(C.nativeIcon({ icon: '⚙️' }), 'gear', 'an emoji with its variation selector');
  assert.equal(C.nativeIcon({ icon: '🦄', toolsets: ['web'] }), 'globe', 'an icon the app lacks: by what the class reaches');
});

test('the editor: a class as a form and back', () => {
  const st = C.listOf(listed());
  const find = (id) => st.classes.find((c) => c.id === id);
  const coding = C.formOf(find('coding'));
  assert.deepEqual([coding.managersMode, coding.egress, coding.mcpMode], ['all', ['none', 'internet'], 'all']);
  assert.deepEqual(C.classOf(coding), { id: 'coding', name: 'Coding', icon: '▣', description: 'a sandbox', toolsets: ['files', 'web', 'sandbox'],
    model: '', system: '', who: 'everyone', managers: 'all', sandboxEgress: ['none', 'internet'] }, 'no mcp without internal reach');
  const bridge = C.formOf(find('bridge'));
  assert.deepEqual([bridge.mcpMode, bridge.mcp], ['only', 'apps/pg']);
  assert.deepEqual(C.classOf(bridge).mcp, ['apps/pg']);
  const web = C.formOf(find('web'));
  assert.equal(web.mcpMode, 'all', 'a class with no internal reach starts at "all" should it get some');
  web.toolsets = C.toggle(web.toolsets, 'internal', true);
  assert.equal(C.classOf(web).mcp, 'all');
  web.toolsets = C.toggle(web.toolsets, 'sandbox', true);
  web.egress = [];
  assert.deepEqual(C.classOf(web).sandboxEgress, ['none'], 'a sandbox with no egress named: none');
  const ops = C.formOf(find('ops'));
  assert.deepEqual([ops.who, ops.model, ops.system], ['managers', 'apps/llm|big', 'be careful']);
  const odd = C.formOf({ id: 'x', toolsets: ['files', 'future'] });
  assert.deepEqual(C.classOf(odd).toolsets, ['files', 'future'], 'a toolset a newer backend knows stays');
  // names: the bound ones and those listed; toggling one
  assert.deepEqual(C.names('a, b', ['b', 'c']), [{ name: 'b', on: true }, { name: 'c', on: false }, { name: 'a', on: true }]);
  assert.equal(C.toggleName('a, b', 'a'), 'b');
  assert.equal(C.toggleName('a', 'c'), 'a, c');
  assert.deepEqual(C.ifaceNames({ endpoints: [{ provider: 'apps/pg' }, { provider: 'apps/mail', instance: 'work' }, {}] }), ['apps/pg', 'apps/mail#work']);
});

test('the editor\'s checks', () => {
  const st = C.listOf(listed());
  const f = (over) => ({ ...C.blankForm(), id: 'research', name: 'Research', ...over });
  assert.equal(C.check(f(), st), '');
  assert.match(C.check(f({ id: 'Research' }), st), /^The id is a–z/);
  assert.match(C.check(f({ id: '9x' }), st), /^The id is a–z/);
  assert.match(C.check(f({ id: 'web' }), st), /There is a class “web” already/);
  assert.equal(C.check({ ...C.formOf(st.classes[1]) }, st), '', 'editing a class keeps its id');
  assert.match(C.check(f({ name: 'x'.repeat(61) }), st), /name is up to 60/);
  assert.match(C.check(f({ icon: '🌉🌉🌉🌉🌉🌉🌉🌉🌉' }), st), /icon is a short symbol/, '32 bytes, as the backend counts');
  assert.match(C.check(f({ description: 'x'.repeat(401) }), st), /description is up to 400/);
});

test('what a save sends: the stored classes, this one replaced or added, mixed ones confirmed', () => {
  const st = C.listOf(listed());
  // a built-in at its default, edited: it joins the stored ones, in the list's order
  const web = C.formOf(st.classes[1]);
  web.description = 'the whole web';
  let plan = C.savePlan(st, web);
  assert.deepEqual(plan.body.classes.map((c) => c.id), ['web', 'coding', 'bridge', 'ops']);
  assert.equal(plan.body.classes[0].description, 'the whole web');
  assert.equal(plan.mixed, false);
  assert.deepEqual(plan.others, ['bridge']);
  assert.equal(plan.body.confirmMixed, true, 'bridge was confirmed when it was saved');
  assert.equal(plan.body.default, 'internal');
  // a new class that mixes: the person confirms it (the view sets confirmMixed)
  const noMixed = { ...st, classes: st.classes.filter((c) => c.id !== 'bridge') };
  const nu = { ...C.blankForm(), id: 'leaky', name: 'Leaky', toolsets: ['internal', 'sandbox'], egress: ['internet'] };
  plan = C.savePlan(noMixed, nu);
  assert.deepEqual([plan.mixed, plan.body.confirmMixed], [true, undefined]);
  assert.deepEqual(plan.body.classes.map((c) => c.id), ['coding', 'ops', 'leaky']);
  assert.match(C.confirmWords(nu), /^Save “Leaky”\? It can move internal data out: /);
  // a class written back is the class it was (the stored ones ride along)
  const ops = plan.body.classes[1];
  assert.deepEqual(ops, { id: 'ops', name: 'Ops', icon: '', description: '', toolsets: ['internal'], model: 'apps/llm|big', system: 'be careful',
    who: 'managers', mcp: 'all' });
  // a backend that does not say what is stored: everything is
  const old = { ...st, classes: st.classes.map(({ stored, ...c }) => c) };
  assert.equal(C.savePlan(old, C.formOf(old.classes[0])).body.classes.length, 5);
});

test('delete, reset, the default', () => {
  const st = C.listOf({ ...listed(), default: 'bridge' });
  let body = C.removePlan(st, 'bridge').body;
  assert.deepEqual(body.classes.map((c) => c.id), ['coding', 'ops']);
  assert.equal(body.default, '', 'a deleted default hands over to the backend\'s');
  assert.equal(body.confirmMixed, undefined, 'nothing mixed stays');
  body = C.removePlan(st, 'coding').body;
  assert.deepEqual([body.classes.map((c) => c.id), body.default, body.confirmMixed], [['bridge', 'ops'], 'bridge', true]);
  body = C.defaultPlan(st, 'ops').body;
  assert.deepEqual([body.classes.map((c) => c.id), body.default], [['coding', 'bridge', 'ops'], 'ops']);
  const rows = C.editorRows(st);
  assert.deepEqual(rows.map((r) => [r.id, r.tags.join(' · '), r.del && r.del.label]), [
    ['internal', 'built-in', null], ['web', 'built-in', null], ['coding', 'built-in', 'Reset to default'],
    ['bridge', 'default · ⚠ can move internal data out', 'Delete'], ['ops', 'managers only', 'Delete']]);
  assert.equal(rows[3].del.confirm, 'Delete the Bridge class? Its conversations go on as Internal.');
  assert.equal(rows[4].toolsets, 'internal');
});

test('the channel rules form keeps the classes it does not show (D116)', () => {
  const d = ch.draftOf({ name: 'c', config: { policy: { privateLane: true, privateClass: 'ops', webClass: 'web' } } });
  const p = ch.policyOf(d);
  assert.deepEqual([p.privateClass, p.webClass], ['ops', 'web']);
  assert.equal('privateClass' in ch.policyOf(ch.draftOf({ name: 'c', config: { policy: {} } })), false);
});

test('the channel rules form sends the whole policy back: what it does not show, what it cleared', () => {
  const pol = { dm: { policy: 'pairing', scope: 'main' }, groups: { policy: 'allowlist', allow: ['C1'], scope: 'per-user', linkedOnly: true },
    privateLane: true, trustedGroups: ['G1'], webClass: 'coding', reset: 'idle:3600', future: { x: 1 } };
  const d = ch.draftOf({ name: 'c', config: { policy: pol } });
  let p = ch.policyOf(d);
  assert.deepEqual([p.groups.scope, p.future, p.webClass, p.dm.scope, p.groups.linkedOnly], ['per-user', { x: 1 }, 'coding', 'main', true],
    'fields the form does not show ride along');
  Object.assign(d, { privateLane: false, dmScope: '', linkedOnly: false, reset: '', webClass: '', privateClass: 'bridge' });
  p = ch.policyOf(d);
  for (const k of ['privateLane', 'trustedGroups', 'reset', 'webClass']) assert.equal(k in p, false, `${k} cleared`);
  assert.equal('scope' in p.dm, false);
  assert.equal('linkedOnly' in p.groups, false);
  assert.equal(p.privateClass, 'bridge', 'a trusted class is kept even while the private lane is shut');
  assert.equal(pol.privateLane, true, 'the policy it came from is untouched');
});

// --- automations: the class they run in ------------------------------------------------

test('an automation\'s class: the forms\' choices, what cards say, the lane sent beside it', () => {
  const st = C.listOf(listed());
  assert.deepEqual(C.choices(st, 'web').map((r) => [r.value, r.lane, r.on]),
    [['internal', 'private', false], ['web', 'web', true], ['coding', 'web', false], ['bridge', 'private', false], ['ops', 'private', false]]);
  assert.match(C.choices(st, 'bridge').find((r) => r.on).label, /⚠ can move internal data out$/);
  const gone = C.choices(st, 'old-one');
  assert.deepEqual([gone.length, gone.at(-1).value, gone.at(-1).gone, gone.at(-1).on], [6, 'old-one', true, true], 'the one it has stays listed');
  assert.deepEqual(C.choices(st, 'web', 'web').map((r) => r.value), ['web', 'coding'], 'one lane only');
  assert.deepEqual([C.laneFor(st, 'coding'), C.laneFor(st, 'bridge'), C.laneFor(st, 'gone', 'web'), C.laneFor(st, 'gone')], ['web', 'private', 'web', 'private']);
  let a = C.ofAutomation(st, { class: 'bridge', toolset: 'private' });
  assert.deepEqual([a.label, a.lane, a.mixed, a.warn], ['🌉 Bridge', 'private', true, '⚠ can move internal data out']);
  a = C.ofAutomation(st, { toolset: 'web' });
  assert.deepEqual([a.id, a.label, a.nativeIcon], ['web', '🌐 Web', 'globe'], 'from before classes: its lane\'s built-in');
  a = C.ofAutomation(st, { class: 'secret', toolset: 'web' });
  assert.deepEqual([a.known, a.label, a.lane], [false, 'secret', 'web'], 'one you may not see: its id, its lane');
  const cc = C.channelClasses(st, { privateClass: '', webClass: '' });
  assert.deepEqual([cc.web.value, cc.web.rows.map((r) => r.value), cc.private.value, cc.private.rows.length], ['web', ['web', 'coding'], 'internal', 5]);
  assert.equal(C.channelClasses(st, { webClass: 'bridge' }).web.rows.at(-1).gone, true, 'a stored webClass that no longer fits still shows');
});

test('the schedule form: your default class, fixed on edit, the class and its lane sent', async () => {
  const calls = [];
  const fake = async (url, opt = {}) => {
    calls.push({ url: String(url), method: opt.method || 'GET', body: opt.body });
    return new Response(JSON.stringify(String(url).endsWith('/schedules') ? { id: 9 } : { items: [] }), { headers: { 'Content-Type': 'application/json' } });
  };
  globalThis.window = globalThis;
  globalThis.xbin = { self: 'apps/agent', fetch: fake };
  globalThis.fetch = fake;
  const st = C.listOf({ ...listed(), default: 'coding' });
  const page = new auto.AutoPage({ classes: () => st });
  page.newSchedule();
  assert.deepEqual([page.form.class, page.form.toolset], ['coding', 'web'], 'a new one: the class a new one of yours gets');
  Object.assign(page.form, { goal: 'look', class: 'bridge' });
  await page.save();
  const body = JSON.parse(calls.find((c) => c.method === 'POST').body);
  assert.deepEqual([body.class, body.toolset], ['bridge', 'private']);
  page.editSchedule({ id: 4, kind: 'schedule', name: 'old', config: { cron: '@daily', goal: 'g', toolset: 'web' } });
  assert.deepEqual([page.form.class, page.form.toolset], ['web', 'web'], 'from before classes: its lane\'s built-in');
  assert.deepEqual(new auto.AutoPage({}).classes().classes.map((c) => c.id), ['internal', 'web'], 'before GET /classes: the lanes');
});

test('the trigger form: a class; the firewall holds private data outward and public data from a mixed class', () => {
  const st = C.listOf(listed());
  const page = { classes: () => st, changed() {}, items: [] };
  trig.startForm(page, null);
  assert.deepEqual([trig.st.form.class, trig.st.form.toolset], ['internal', 'private'], 'a new one: internal, as the backend gives');
  const f = trig.st.form;
  assert.deepEqual(trig.firewall(f, st), { clash: false, why: '' });
  f.class = 'coding';
  assert.equal(trig.firewall(f, st).why, trig.OUTWARD, 'a web-lane class takes public data only');
  f.dataClass = 'public';
  assert.equal(trig.firewall(f, st).clash, false);
  f.class = 'bridge';
  assert.match(trig.firewall(f, st).why, /^The Bridge class can move internal data out/);
  f.class = 'internal';
  f.deliver = 'chan:8:dm:U2';
  f.dataClass = 'private';
  assert.equal(trig.firewall(f, st).clash, true, 'announcing to a chat is outward too');
  assert.equal(trig.firewall({ toolset: 'web', dataClass: 'private' }).clash, true, 'no classes: the legacy toolset says the lane');
  trig.startForm(page, { id: 3, config: { name: 't', toolset: 'web', dataClass: 'public' } });
  assert.deepEqual([trig.st.form.class, trig.st.form.toolset], ['web', 'web'], 'from before classes');
  trig.closeForm(page);
});

// --- the app: your pick is remembered, a save refreshes the picker -----------------

test('the app: pick, remember, save, ask', async () => {
  const calls = [];
  const prefs = { class: 'coding' };
  const json = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
  const view = () => listed();
  const fake = async (url, opt = {}) => {
    const method = opt.method || 'GET';
    calls.push({ method, url: String(url), body: opt.body });
    const u = String(url);
    if (u.endsWith('/prefs/class')) {
      if (method === 'PUT') { prefs.class = JSON.parse(opt.body); return json({}); }
      return prefs.class ? json(prefs.class) : json({}, 404);
    }
    if (u.endsWith('/prefs/toolset')) return json({}, 404);
    if (u.endsWith('/classes') && method === 'GET') return json(view());
    if (u.endsWith('/classes') && method === 'PUT') {
      const b = JSON.parse(opt.body);
      if (b.classes.some((c) => c.id === 'leaky') && !b.confirmMixed) return json({ error: 'confirm it', mixed: ['leaky'] }, 409);
      const v = view();
      return json({ ...v, classes: v.classes.filter((c) => c.id !== 'coding') }); // a class gone: the pick falls back
    }
    if (u.endsWith('/ask')) return json({ id: 7, title: 'x', status: 'running', rootId: 7 });
    if (u.includes('/stream')) return new Response(new ReadableStream({ start() {} }), { headers: { 'Content-Type': 'text/event-stream' } });
    return json({});
  };
  globalThis.window = globalThis;
  globalThis.xbin = { self: 'apps/agent', fetch: fake };
  globalThis.fetch = fake;
  const { createApp } = await import(new URL('model/app.js', TPL).href);
  const app = createApp({ frame: (fn) => setTimeout(fn, 0) });
  const heard = [];
  app.on('class', () => heard.push('class'));
  await app.loadClasses();
  assert.deepEqual([app.classId, app.toolset], ['coding', 'web'], 'your pick; its lane');
  assert.equal(calls.some((c) => c.url.endsWith('/prefs/toolset')), false, 'a pick: the old lane is not read');
  app.pickClass('bridge');
  assert.deepEqual([app.classId, app.toolset, prefs.class], ['bridge', 'private', 'bridge']);
  app.pickClass('nope');
  assert.equal(app.classId, 'bridge', 'a class not listed is not picked');
  assert.ok(heard.length >= 2);
  // asking sends the class, and its lane as the legacy toolset
  await app.ask({ text: 'hi', title: '', system: '', class: 'web' });
  let ask = JSON.parse(calls.filter((c) => c.url.endsWith('/ask')).pop().body);
  assert.deepEqual([ask.class, ask.toolset], ['web', 'web'], 'the form\'s class');
  await app.ask({ text: 'old', toolset: 'web' });
  ask = JSON.parse(calls.filter((c) => c.url.endsWith('/ask')).pop().body);
  assert.deepEqual([ask.class, ask.toolset], [undefined, 'web'], 'a legacy {toolset} alone still names its lane');
  // saving: a 409 carries what to confirm; a save refreshes the picker
  app.pickClass('coding');
  await assert.rejects(app.saveClasses({ classes: [{ id: 'leaky' }] }), (e) => e.status === 409 && e.mixed[0] === 'leaky');
  await app.saveClasses({ classes: [{ id: 'leaky' }], confirmMixed: true });
  assert.equal(app.classId, 'internal', 'your pick is gone: the default');
  app.toggleToolset();
  assert.equal(app.classId, 'web', 'the old toggle picks a lane\'s class');
});
