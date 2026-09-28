// hack/deploy-panel.test.mjs — unit tests for the Deployments panel's view
// (web/deploy-panel.js) and its confirmations and results (web/deploy-state.js),
// run by `make js-test`: node's built-in runner, no dependencies. Split out
// of hack/deploy-state.test.mjs; the states come from hack/deploy-fixtures.mjs.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as dsState from '../web/deploy-state.js';
import * as dsPanel from '../web/deploy-panel.js';
import {
  T, NOW, at, opts, yes, no, PROTECTED, ISOLATE, NEEDS_TERMINAL, TODAY, terminalCaller, depCan, mainLive, mainPinned, devLive, zero, paused, attachedMain, onDev, reader, labels, byLabel, summary, CODE,
} from './deploy-fixtures.mjs';

const ds = { ...dsState, ...dsPanel };
const {
  who, ago, entry, confirmation, result, notice,
  REASON, PANEL_OPS, panelRows, panelHeader, overview, panelActions, zeroPanel, edgeRows, widens, registrationRows, registrationsNote, asksToRun,
  wouldNotifyRows, logRows, diffLine, addDialog,
} = ds;

// ---- M2: the Deployments panel (web/bx-deploy.js draws these) ----

const MANAGER = 'tile managers only';
const mgr = (over = {}) => terminalCaller({ manager: true, can: { pause: yes, resume: yes, reloadNow: yes, add: yes, edges: yes, protect: yes }, ...over });
const EDGES = [
  { id: 'slot:llm', kind: 'http', to: 'apps/llm-gw', role: 'writer', policy: 'read', default: 'read', values: ['read', 'block'], set: false, refused: 0, clamped: 12 },
  { id: 'grant:apps/leads', kind: 'grant', to: 'apps/leads', role: 'reader', policy: 'block', default: 'read', values: ['read', 'block'], set: true, refused: 14, clamped: 3 },
  { id: 'slot:sandboxes', kind: 'http', to: 'apps/agent', role: 'consumer', policy: 'block', default: 'block', values: ['block'], set: false, why: 'the custom role consumer on apps/agent implies no reader', refused: 2, clamped: 0 },
  { id: 'slot:net', kind: 'net', to: '', policy: 'inherit', default: 'inherit', values: ['inherit', 'block'], set: false, effective: 'block', why: 'this tile\'s network shares the host\'s: non-primary deployments get no egress', refused: 3, clamped: 0 },
];
const devM2 = (over = {}) => devLive({
  status: { state: 'healthy', gen: 3, serving: 'work-tree' }, data: { state: 'seeded', from: 'main', at: at(60 * 48), by: 'user:ana' }, vault: { keys: 4, placeholders: 1 },
  limits: { memMiB: 256, pids: 512, diskGiB: 50, overrides: ['memMiB'] }, deliveries: true, alwaysOn: false, alwaysOnDeclared: true,
  wouldNotify: [{ at: at(3), to: 'user:ana', title: 'Deploy v2.3?' }], registrations: [{ kind: 'cron', name: 'nightly', schedule: '0 3 * * *', path: '/tick', dormant: false },
    { kind: 'bus', name: 'trig-12', resource: 'res:apps/crm/events', prefix: 'orders/', dormant: false }, { kind: 'ingress-host', name: 'crm.example.com', dormant: true }],
  can: depCan({ remove: yes, reset: yes, runNow: yes, seed: no(MANAGER), vaultCopy: no(MANAGER), deliveries: no(MANAGER), alwaysOn: no(MANAGER), limits: no(MANAGER), primary: no(MANAGER) }), ...over,
});
const mainM2 = (over = {}) => mainPinned({ data: { state: 'original' }, limits: { memMiB: 512, pids: 512, diskGiB: 50 }, deliveries: true,
  can: depCan({ remove: no('main can\'t be removed', 'state'), limits: no(MANAGER), primary: no('main is the primary of apps/crm', 'state') }), ...over });
const m2 = (over = {}) => onDev({ edges: EDGES, caps: { tile: 3, tileUsed: 1, workspace: 24, workspaceUsed: 7 }, deployments: [mainM2(), devM2()], ...over });
const ids = (list) => list.map((a) => `${a.id}${a.enabled ? '' : '✗'}`);

// covers P2 P9 P13 P14 P24 — the side list, the header and the overview of a
// tile whose live reload is on dev: code pointers, status, data, limits,
// vault, deliveries, the git line of a pinned deployment; Undo after a roll back.
test('the panel: rows, header and overview', () => {
  const s = m2();
  assert.deepEqual(panelRows(s, { ...opts, target: 'dev' }).map((r) => [r.name, r.primary, r.code, r.status, r.data, r.deliveries, r.target]), [
    ['main', true, '📌 c:3f2a1c9', 'static', 'original', 'deliveries: active', false], ['dev', false, '● work tree', 'healthy', 'seeded from main · 2d ago', 'deliveries: on', true]]);
  const h = panelHeader(s, opts);
  assert.equal(h.text, 'Live reload: dev — saves reach apps/crm+dev. The primary, main, is pinned to c:3f2a1c9.');
  assert.deepEqual([h.actions.map((a) => a.id), h.actions[1].items.map((i) => i.id)], [['pause', 'attach'], ['attach/main']]);
  assert.deepEqual(overview(s, 'dev', opts).lines, [['code', '● work tree'], ['status', 'healthy'], ['data', 'seeded from main · 2d ago'],
    ['limits', 'memory: 256 MiB (set by a tile manager) · disk: the tile\'s default (50 GiB)'], ['vault', '4 secrets · 1 is a placeholder'], ['deliveries', 'deliveries: on'], ['alwaysOn', 'alwaysOn: off']]);
  const mo = overview(s, 'main', { ...opts, entry: { deployment: 'main', how: 'promote', from: 'dev', by: 'user:ana', finishedAt: at(120), result: 'ok' } });
  assert.deepEqual(mo.lines.slice(0, 2), [['primary', 'primary — everything from outside reaches it'], ['code', '📌 c:3f2a1c9 · promoted from dev by ana, 2h ago']]);
  assert.deepEqual([mo.gitLine, mo.url, overview(s, 'dev', opts).gitLine], ['git: deploy/main — git fetch xbin-deploy', '/c/apps/crm/', null]);
  const rb = paused({ deployments: [mainPinned({ checkpoint: { id: 'c:1e9d0aa', hash: '1e9d0aa0' }, lastDeploy: { id: 5, how: 'rollback', at: at(5), by: 'user:ana', result: 'ok' } })] });
  const hu = panelHeader(rb, { ...opts, undo: { id: 5, deployment: 'main', how: 'rollback', previous: 'c:3f2a1c9', result: 'ok' } });
  assert.deepEqual([hu.actions.map((a) => a.id), hu.actions[2].label], [['reloadNow', 'resume', 'undo'], 'Undo: roll main back to c:3f2a1c9']);
  assert.match(hu.text, /main was rolled back to c:1e9d0aa\. The work tree still holds the code you rolled back from: resuming ships it again\.$/);
  assert.deepEqual([panelHeader(paused(), opts).text, panelHeader(attachedMain(), opts).text],
    ['Live reload paused by ana 12m ago — 3 files changed since c:3f2a1c9, the checkpoint main runs.', 'Live reload: main (primary) — every save reaches everyone using apps/crm.']);
  assert.equal(panelHeader(paused({ deployments: [mainPinned({ lastDeploy: { how: 'reload-now', result: 'failed' } })] }), opts).text,
    'Live reload paused by ana 12m ago — 3 files changed since c:3f2a1c9, the checkpoint main runs — the last deploy to main failed; main keeps running c:3f2a1c9.');
  assert.deepEqual([panelHeader(zero(), opts).text, panelHeader(zero(), opts).actions.map((a) => a.id)], ['Live reload: main — every save reaches everyone using apps/crm.', ['pause']]);
});

// covers P4 P21 P22 — actions follow the server's can: a terminal-level
// caller, a tile manager, an unhealthy reassignment target, a protected primary.
test('the panel: actions follow the server\'s can, a protected primary included', () => {
  const s = m2();
  assert.deepEqual(ids(panelActions(s, 'dev', opts)), ['deploy', 'promote', 'remove', 'seed✗', 'reset', 'vaultCopy✗', 'deliveries✗', 'alwaysOn✗', 'limits✗', 'open']);
  assert.deepEqual([panelActions(s, 'dev', opts)[1].label, panelActions(s, 'dev', opts)[3].why], ['Promote dev → main…', MANAGER]);
  assert.deepEqual([ids(panelActions(s, 'main', opts)), panelActions(s, 'main', opts)[1].label], [['deploy', 'promote', 'remove✗', 'limits✗', 'open'], 'Promote main → dev…']);
  assert.deepEqual(ids(panelActions(s, '', opts)), ['reassign✗', 'protect✗', 'blockEdges✗']);
  assert.deepEqual(ids(panelActions(m2({ caller: mgr(), deployments: [mainM2(), devM2({ can: depCan({ primary: yes }) })] }), '', opts)), ['reassign', 'protect', 'blockEdges']);
  const sick = m2({ caller: mgr(), deployments: [mainM2(), devM2({ status: { state: 'failed' }, can: depCan({ primary: yes }) })] });
  assert.equal(panelActions(sick, '', opts)[0].why, 'dev isn\'t healthy — deploy working code to it first.');
  const prot = m2({ protectedPrimary: true, deployments: [mainM2({ can: depCan({ deploy: no(PROTECTED), promoteTo: no(PROTECTED) }) }), devM2()] });
  const pa = panelActions(prot, 'dev', opts);
  assert.deepEqual([pa[1].why, pa[0].enabled], [PROTECTED, true], 'promote onto main refused; deploy to dev stays');
  assert.deepEqual([panelActions(prot, '', opts)[1].label, panelRows(prot)[0].protected, overview(prot, 'main', opts).lines[0][1], panelActions(zero(), 'main', opts)],
    ['Unprotect the primary', true, 'primary — everything from outside reaches it · 🛡 protected', []]);
});

// covers P5 P4 D64 — a reader's filtered view names and counts no other
// deployment; a write-level caller operates nothing; view-as disables all.
test('the panel: a reader sees the primary only; write and view-as operate nothing', () => {
  const r = reader();
  const all = [panelRows(r), panelHeader(r, opts), panelActions(r, 'main', opts), panelActions(r, '', opts), overview(r, 'main', opts), edgeRows(r), registrationRows(r, 'main'), wouldNotifyRows(r, 'main')];
  assert.doesNotMatch(JSON.stringify(all), /\bdev\b|\bfiles?\b/);
  assert.deepEqual([panelRows(r).map((x) => x.name), panelHeader(r, opts).text, panelHeader(r, opts).actions], [['main'], 'main is pinned to c:3f2a1c9.', []]);
  const W = 'Needs write access to apps/crm.';
  assert.deepEqual(panelActions(r, 'main', opts).map((a) => [a.id, a.enabled, a.why]), [['deploy', false, W], ['remove', false, W], ['limits', false, W], ['open', true, '']]);
  assert.equal(overview(r, 'main', opts).gitLine, null, 'the fetch remote is for writers');
  const writeCan = { open: yes, ...Object.fromEntries(['deploy', 'promoteTo', 'remove', 'reset', 'limits'].map((k) => [k, no(NEEDS_TERMINAL(k))])) };
  const w = m2({ caller: { level: 'write', can: {} }, deployments: [mainM2({ can: writeCan }), devM2({ can: writeCan })] });
  assert.deepEqual(ids(panelActions(w, 'dev', opts)), ['deploy✗', 'promote✗', 'remove✗', 'seed✗', 'reset✗', 'vaultCopy✗', 'deliveries✗', 'alwaysOn✗', 'limits✗', 'open']);
  const va = panelActions(m2({ caller: mgr({ readOnly: true }) }), 'main', { ...opts, viewing: 'dev1' });
  assert.deepEqual([va.every((a) => !a.enabled), va[0].why], [true, 'dev1 may do this — you are viewing as dev1 (read-only).']);
});

// covers P3 P23 P27 — the outbound-edges table: the read clamp, a stored
// override, an edge fixed at block with its reason, host networking; only
// widening confirms.
test('the panel: the outbound-edges table', () => {
  const s = m2({ caller: mgr() });
  assert.deepEqual(edgeRows(s).map((r) => [r.label, r.primary, r.values.map((v) => v.label), r.text, r.refused, r.enabled]), [
    ['llm → apps/llm-gw', 'writer', ['read — its primary, as reader (clamped from writer)', 'block'], 'read — its primary, as reader (clamped from writer)', 'refused 0 · clamped 12', true],
    ['apps/leads', 'reader', ['read — its primary, as reader', 'block', 'default (read)'], 'block', 'refused 14 · clamped 3', true],
    ['sandboxes → apps/agent', 'consumer', [], 'blocked — the custom role consumer on apps/agent implies no reader', 'refused 2 · clamped 0', false],
    ['net', 'as today', [], 'blocked — this tile\'s network shares the host\'s: non-primary deployments get no egress', 'refused 3 · clamped 0', false]]);
  assert.deepEqual([edgeRows(m2()).slice(0, 2).map((r) => [r.enabled, r.why]), edgeRows(m2({ caller: terminalCaller({ can: {} }) }))[0].why], [[[false, MANAGER], [false, MANAGER]], REASON.manager]);
  assert.deepEqual([widens(s, 'grant:apps/leads', 'read'), widens(s, 'grant:apps/leads', 'default'), widens(s, 'slot:llm', 'block')], [true, true, false]);
});

// covers P13 P9 — registrations and their pills (a non-primary's cron jobs
// and bus subscriptions active for it, dormant only with deliveries off; its
// ingress hosts dormant), the tab's note, Run now (and when it asks),
// "would notify", the deploy log with its running entry and Roll back.
test('the panel: registrations, Run now, would notify and the deploy log', () => {
  const s = m2();
  assert.deepEqual(registrationRows(s, 'dev').map((r) => [r.label, r.pill, !!r.runNow?.enabled]), [['nightly · 0 3 * * *', 'active', true],
    ['trig-12 · res:apps/crm/events orders/', 'active', false], ['crm.example.com', 'dormant — routes reach the primary only', false]]);
  const off = m2({ deployments: [mainM2(), devM2({ deliveries: false, registrations: devM2().registrations.map((r) => ({ ...r, dormant: true })) })] });
  assert.deepEqual(registrationRows(off, 'dev').map((r) => r.pill), ['dormant — deliveries off', 'dormant — deliveries off', 'dormant — routes reach the primary only']);
  assert.deepEqual([panelRows(off, opts)[1].deliveries, panelActions(off, 'dev', opts).find((a) => a.id === 'deliveries').on, panelActions(s, 'dev', opts).find((a) => a.id === 'deliveries').on],
    ['deliveries: off', false, true]);
  assert.deepEqual([registrationsNote(s, 'dev'), registrationsNote(off, 'dev'), registrationsNote(s, 'main')], [
    'dev\'s cron jobs and bus subscriptions fire for dev, with its own data. Its interface instances and ingress hosts stay dormant: routes reach the primary, main, only.',
    'dev\'s cron jobs and bus subscriptions are silenced: a tile manager switched its deliveries off. Its interface instances and ingress hosts stay dormant: routes reach the primary, main, only.', '']);
  assert.deepEqual([asksToRun(s, 'dev'), asksToRun(m2({ deployments: [mainM2(), devM2({ data: { state: 'empty' } })] }), 'dev'), wouldNotifyRows(s, 'dev', opts)],
    [true, false, ['would notify ana · "Deploy v2.3?" · 3m ago']]);
  const log = logRows(s, 'main', [
    { id: 9, how: 'deploy', checkpoint: 'c:9d1e3b4', by: 'user:ana', requestedAt: at(1), result: 'failed', error: 'exit status 1' },
    { id: 8, how: 'promote', from: 'dev', checkpoint: 'c:3f2a1c9', by: 'user:ana', agent: true, finishedAt: at(120), result: 'ok' },
    { id: 7, how: 'rollback', checkpoint: 'c:1e9d0aa', by: 'user:bob', finishedAt: at(60 * 48), result: 'ok' },
    { id: 6, how: 'resume', followsWorkTree: true, by: 'user:ana', finishedAt: at(60 * 72), result: 'ok' }], opts);
  assert.deepEqual(log.map((r) => [r.code, r.how, r.result, r.who, r.state, r.rollback?.label ?? null]), [
    ['📌 c:9d1e3b4', 'deploy', 'failed — exit status 1', 'ana · 1m ago', '', null], ['📌 c:3f2a1c9', 'promote (from dev)', 'ok', 'ana (agent) · 2h ago', 'running', null],
    ['📌 c:1e9d0aa', 'roll back', 'ok', 'bob · 2d ago', '', 'Roll back to c:1e9d0aa'], ['● work tree', 'resume live reload', 'ok', 'ana · 3d ago', '', null]]);
  assert.equal(diffLine('c:3f2a1c9', 'c:7b19e02', { files: 3, add: 40, del: 12 }), 'c:3f2a1c9 → c:7b19e02 · 3 files, +40 −12');
});

// covers P5 P18 — the zero state's one entry point, and the add form.
test('the panel: the zero state\'s entry point, and the add form', () => {
  const z = zeroPanel(zero(), opts);
  assert.deepEqual([...z.map((p) => [p.id, p.lead, p.enabled]), z[2].text], [[undefined, 'Live reload: main', undefined], ['pause', 'Pause live reload', true], ['add', 'Add deployment…', true],
    'a second runtime of apps/crm, for example dev, with its own data, at /c/apps/crm+dev/. Saves can go there while main stays put.']);
  const iso = zeroPanel(zero({ allowed: { pause: no(ISOLATE, 'policy'), deployments: no(ISOLATE, 'policy') } }), opts);
  assert.deepEqual(iso.slice(1).map((p) => [p.enabled, p.why]), [[false, ISOLATE], [false, ISOLATE]]);
  const f = addDialog(m2(), 'apps/crm already has a deployment "dev"', ['c:1e9d0aa']);
  assert.deepEqual([f.error, f.message, f.fields.map((x) => x.name)], ['apps/crm already has a deployment "dev"', 'Seeding needs a tile manager.', ['name', 'from', 'data', 'attach']]);
  assert.deepEqual(f.fields[1].options.map((o) => o.label), ['the work tree now (a fresh checkpoint)', 'main\'s code (c:3f2a1c9)', 'c:1e9d0aa']);
  assert.deepEqual([f.fields[2].options.map((o) => o.value), addDialog(m2({ caller: mgr() })).fields[2].options.map((o) => o.value)], [['empty'], ['empty', 'seed']]);
});

// covers P5 P10 P14 P21 P22 — the panel's confirmations render the dry run
// (a fact it lacks is left out) and send what they showed: the reviewed
// checkpoint, the confirm token of a required box, the chosen fields.
test('the panel\'s confirmations render the dry run and send what it showed', () => {
  const s = m2(), C = (op, ctx) => confirmation(op, { state: s, ...ctx }, opts), line = (c, i) => c.message.split('\n')[i];
  const add = C('add', { deployment: 'qa', add: { from: 'work-tree', attach: true }, impact: { code: { to: 'c:7b19e02' }, affects: 'nobody' } });
  assert.deepEqual([add.title, line(add, 0), line(add, 3), add.send({}), add.required], ['Add deployment qa to apps/crm?', 'Code: qa runs a fresh checkpoint of the work tree, c:7b19e02. Live reload moves to qa; dev is pinned to c:7b19e02.',
    'Affects: nobody now. It is reachable at /c/apps/crm+qa/ by people with write on apps/crm and by this tile\'s terminals. Its cron jobs and bus subscriptions fire for qa, with qa\'s data: anything they send is real. Its interface instances and ingress hosts stay with the primary, and alwaysOn stays off.', {}, []]);
  const seed = C('add', { deployment: 'qa', add: { from: 'primary', data: 'seed' }, impact: {} });
  assert.deepEqual([line(seed, 0), seed.spec.fields.map((f) => f.name), seed.send({ ok: true })], ['Code: qa runs main\'s code.', ['ok'], { confirm: 'copy-data' }]);
  assert.match(C('add', { deployment: 'dev', add: {}, impact: { joins: { scope: 'apps/shop', state: 'seeded', by: 'user:ana', at: at(2880) } } }).message, /Data: joins apps\/shop's "dev" data \(seeded by ana 2d ago\); secrets/);
  const d = C('deploy', { deployment: 'dev', impact: { code: CODE, affects: 'deployment', pausesLiveReload: true } });
  assert.deepEqual([d.title, d.message, d.send({})], ['Deploy the work tree to dev?', ['Code: dev runs a fresh checkpoint of the work tree, c:7b19e02 (3 files, +40 −12 against c:3f2a1c9). If it fails, dev keeps its current code.',
    'Data: dev keeps its data.', 'Pauses: live reload — later saves won\'t reach dev until you resume.', 'Affects: only people using apps/crm+dev.'].join('\n'), { checkpoint: 'c:7b19e02' }]);
  const p = C('promote', { from: 'dev', to: 'main', reviewed: 'c:7b19e02', impact: { code: { ...CODE, workTreeAt: undefined }, affects: 'everyone' } });
  assert.deepEqual([line(p, 0), line(p, 2), p.send({})], ['Code: main runs dev\'s code, c:7b19e02 (3 files, +40 −12 against main\'s c:3f2a1c9).', 'Affects: everyone using apps/crm: frames reload once.', { expect: 'c:7b19e02' }]);
  const rb = C('rollback', { deployment: 'main', checkpoint: 'c:1e9d0aa', entry: { by: 'user:ana', finishedAt: at(2880) }, impact: { code: { from: 'c:3f2a1c9', to: 'c:1e9d0aa', files: 2, added: 5, removed: 9 } } });
  const rm = C('remove', { deployment: 'dev', impact: {} });
  assert.deepEqual([line(rb, 0), rb.send({}), rm.spec.buttons[1], rm.required, rm.send({ ok: true }), line(rm, 1)], ['Code: main runs c:1e9d0aa again (deployed 2d ago by ana; 2 files, +5 −9 against c:3f2a1c9).',
    { checkpoint: 'c:1e9d0aa' }, { label: 'Remove dev', value: 'ok', danger: true }, ['ok'], { confirm: 'erase' }, 'Pauses: live reload was on dev; Reload now and Resume live reload then go to main, which keeps running c:3f2a1c9.']);
  const pr = C('primary', { deployment: 'dev', impact: { placeholders: ['STRIPE_KEY', 'SMTP_PASS'] } });
  assert.match(pr.message, /\nSecrets: dev has no value for 2 secrets main uses \(STRIPE_KEY, SMTP_PASS\): copy or set them first\.\nLive reload is attached to dev: from now on every save reaches everyone/);
  assert.deepEqual([pr.required, pr.send({}), confirmation('primary', { state: m2({ protectedPrimary: true }), deployment: 'dev', impact: { code: CODE } }).send({})],
    [['ok', 'secrets'], { confirm: 'data-stays' }, { confirm: 'data-stays', expect: 'c:7b19e02' }]);
  const pz = confirmation('protect', { state: zero(), impact: { code: null } });
  assert.deepEqual([/\nTerminals: terminal and agent sessions that call main restart now, calling dev;/.test(C('protect', { impact: { code: CODE } }).message), pz.send({}), C('unprotect', {}).title,
    /\nPauses: live reload leaves main, which is pinned to a checkpoint of the work tree taken when you confirm\.\nTerminals: .* with the tile API off: nothing else can be offered;/.test(pz.message)], [true, {}, 'Unprotect main?', true]);
  const sd = C('seed', { deployment: 'dev', impact: { stops: ['dev', 'apps/shop-admin+dev'] } });
  assert.match(line(sd, 0), /dev and apps\/shop-admin\+dev stop during the copy and restart\.$/);
  assert.deepEqual([sd.send({ ok: true, stop: true }), sd.danger, C('reset', { deployment: 'dev', impact: {} }).send({ ok: true, vault: true })], [{ confirm: 'copy-data', stop: true }, true, { confirm: 'erase-data', vault: true }]);
  assert.match(confirmation('reset', { state: m2({ primary: 'dev' }), deployment: 'main', impact: {} }).message, /main is main and not the primary: this deletes main's data — what apps\/crm served until dev became the primary\./);
  const vc = C('vaultCopy', { deployment: 'dev', keys: ['STRIPE_KEY', 'SMTP_PASS'] });
  assert.deepEqual([vc.spec.fields.map((f) => f.label), vc.send({ 'key:SMTP_PASS': true }), vc.send({}), C('vaultCopy', { deployment: 'dev' }).send({ all: true })],
    [['STRIPE_KEY', 'SMTP_PASS'], { keys: ['SMTP_PASS'] }, null, { all: true }]);
  assert.deepEqual([C('deliveries', { deployment: 'dev' }).title, /^dev's cron jobs \(1\) and bus subscriptions \(1\) fire for dev again, alongside main's\./.test(C('deliveries', { deployment: 'dev' }).message)],
    ['Turn dev\'s deliveries back on?', true]);
  assert.match(pr.message, /grants, ingress, interface instances, notifications and the app\. Cron jobs and bus subscriptions stay with the deployment that registered them\.\nData: .*its cron jobs and subscriptions keep firing for main; its ingress hosts and interface instances become dormant\./);
  assert.deepEqual([C('alwaysOn', { deployment: 'dev' }).title, C('edge', { edge: 'slot:net', policy: 'inherit' }).title, C('edge', { edge: 'grant:apps/leads', policy: 'read' }).message],
    ['Keep dev running?', 'Let non-primary deployments use apps/crm\'s network?', 'apps/crm\'s non-primary deployments (dev) may call apps/leads\'s primary, as reader. They never write to it.']);
  assert.equal(C('runNow', { deployment: 'dev', job: 'nightly' }).message, 'Runs nightly once on dev, with dev\'s data seeded from main: real people\'s data and apps/crm\'s network: anything it sends (email, webhooks) is real.');
  const lm = C('limits', { deployment: 'dev' });
  assert.deepEqual(lm.spec.fields.map((f) => [f.name, f.value, f.placeholder]), [['memMiB', '256', 'the tile\'s default'], ['diskGiB', '', 'the tile\'s default (50 GiB)']]);
  assert.deepEqual([lm.send({ memMiB: '', diskGiB: '20' }), lm.send({ memMiB: '128', diskGiB: '' })], [{ limits: { memMiB: null, diskGiB: 20 } }, { limits: { memMiB: 128 } }]);
  assert.match(lm.message, /^apps\/crm's own limits \(512 MiB, 50 GiB\) are the ceiling/);
  assert.deepEqual([C('target', { deployment: 'dev' }).title, C('target', { deployment: 'off' }).message, PANEL_OPS.includes('undo'), PANEL_OPS.includes('pause')],
    ['Restart this terminal calling dev?', 'Its shell and anything running in it end, and the scrollback is lost.', true, false]);
});

// covers P2 P13 P21 — the panel's result lines, and the terminal lines M2
// adds: a reassigned primary, protection, the tab's target removed; and the
// glossary's words over every string the panel shows.
test('the panel\'s results, M2 terminal lines, and its words', () => {
  const s = m2(), R = (op, a = {}, d = 'dev') => result(op, { state: s, ...a }, { deployment: d }), rec = (what) => ({ op: 'record', by: 'user:ana', what });
  assert.deepEqual([R('add', { deploy: { result: 'queued' } }), R('remove'), R('seed'), R('reset'), R('deliveries'), R('limits'), R('vaultCopy', { copied: ['A', 'B'] }), R('runNow', { delivery: { status: 200, ms: 812 } })],
    ['Added dev at /c/apps/crm+dev/.', 'Removed dev.', 'dev seeded from main.', 'dev\'s data was reset.', 'Deliveries on for dev.', 'dev\'s limits set: 256 MiB, 50 GiB.', 'Copied 2 secrets to dev.', 'delivered · 200 · 812 ms']);
  assert.deepEqual([result('primary', { state: m2({ primary: 'dev' }) }), R('protect'), R('deploy', { unchanged: true })], ['dev is now the primary — it serves dev\'s data.', 'main is protected.', 'dev already runs this code.']);
  assert.deepEqual([notice(s, m2({ primary: 'dev' }), rec(['primary'])), notice(s, m2({ protectedPrimary: true }), rec(['protectedPrimary', 'liveReload'])),
    notice(s, attachedMain(), rec(['deployments']), { target: 'dev' }), notice(s, attachedMain(), rec(['deployments']), { target: 'primary' })],
  ['dev is now the primary of apps/crm (by ana) — it serves dev\'s data', 'main is protected by ana — only tile managers change its code',
    'deployment dev was removed by ana — this terminal\'s API calls fail until you switch it in the tile API select', null]);
  const out = [], walk = (v) => (typeof v === 'string' ? out.push(v) : v && typeof v === 'object' && Object.values(v).forEach(walk)), m = m2({ caller: mgr() });
  walk([panelRows(m, opts), panelHeader(m, opts), ...['dev', 'main', ''].map((n) => panelActions(m, n, opts)), overview(m, 'main', opts), overview(m, 'dev', opts), edgeRows(m, opts),
    registrationRows(m, 'dev', opts).map((r) => r.label + r.pill), registrationsNote(m, 'dev'), wouldNotifyRows(m, 'dev', opts), zeroPanel(zero(), opts), addDialog(m), Object.values(REASON).map((r) => (typeof r === 'function' ? r('apps/crm') : r))]);
  for (const op of PANEL_OPS) walk(confirmation(op, { state: m, deployment: 'dev', from: 'dev', to: 'main', checkpoint: 'c:1e9d0aa', edge: 'grant:apps/leads', policy: 'read', job: 'nightly', add: { data: 'seed', attach: true }, keys: ['K'], impact: { code: CODE, affects: 'everyone', pausesLiveReload: true, placeholders: ['K'] } }, opts).spec);
  for (const t of out.filter((x) => /\s/.test(x))) { // prose, not ids
    assert.doesNotMatch(t, /\b(identity|principal|environments?|env|stag(e|ing)|versions?|revisions?|releases?|snapshots?|generations?|layers?|previews?|channels?|lanes?|freeze|frozen|source|fork|clone|publish|prod|production|origins?|automations?)\b|[⏸⟲⟳🔒▶▣⧉]/iu, t);
    for (const x of t.matchAll(/\blive\b/gi)) assert.match(t.slice(x.index, x.index + 11), /^live reload$/i, t);
    for (const x of t.matchAll(/\bpaus\w*\b(?!:)/gi)) assert.match(t.slice(Math.max(0, x.index - 16), x.index + x[0].length + 16), /live reload/i, t);
  }
});

// The panel module's exports are the contract web/bx-deploy.js builds on:
// adding one is fine, renaming or dropping one breaks the panel.
test('the panel module\'s exports', () => {
  assert.deepEqual(Object.keys(dsPanel).sort(), [
    'addDialog', 'asksToRun', 'diffLine', 'edgeRows', 'logRows', 'overview', 'panelActions', 'panelHeader', 'panelRows',
    'registrationRows', 'registrationsNote', 'widens', 'wouldNotifyRows', 'zeroPanel',
  ]);
});
