// hack/deploy-branch.test.mjs — unit tests for branch-assigned deployments in
// the terminal window (D131): web/deploy-branch.js, and what
// web/deploy-state.js and web/deploy-panel.js draw from it — the chip, its
// menu's offers, the panel's header offers, the Branch row and its actions,
// the add form's Branch control, the confirmations' Branch line, a
// mismatch's question and the grey lines. `make js-test`; the states come
// from hack/deploy-fixtures.mjs.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as branch from '../web/deploy-branch.js';
import * as dsState from '../web/deploy-state.js';
import * as dsPanel from '../web/deploy-panel.js';
import { T, at, opts, yes, no, terminalCaller, depCan, mainPinned, devLive, onDev, paused } from './deploy-fixtures.mjs';

const ds = { ...dsState, ...dsPanel };
const FEATURES = ['live-reload/1', 'deployments/1', 'branches/1'];
const pinned = (name, id, over = {}) => ({ ...devLive({ name, url: `/c/${T}+${name}/` }), liveReload: false,
  checkpoint: { id, hash: id.slice(2), feed: 'work-tree', at: at(5), by: 'user:ana' }, status: { state: 'static', gen: 2, serving: id }, ...over });
// dev requires feature, qa requires release; live reload on dev
const attached = (wt, over = {}) => onDev({ features: FEATURES, workTree: { branch: wt },
  deployments: [mainPinned(), devLive({ branch: 'feature' }), pinned('qa', 'c:9a9a9a9', { branch: 'release' })], ...over });
// xbind paused live reload on dev when the work tree left feature
const switched = (wt, over = {}) => paused({ features: FEATURES, lastLiveReload: 'dev', liveReloadSince: { at: at(1), by: 'xbind' },
  workTree: { changed: 2, since: 'c:5e5e5e5', branch: wt },
  deployments: [mainPinned(), pinned('dev', 'c:5e5e5e5', { branch: 'feature', lastDeploy: { id: 7, how: 'pause', at: at(1), by: 'xbind', result: 'ok' } }),
    pinned('qa', 'c:9a9a9a9', { branch: 'release' })], ...over });
const labels = (items) => items.map((it) => it.label);

test('the feature and the facts', () => {
  assert.equal(branch.speaks(attached('feature')), true);
  assert.equal(branch.speaks(onDev()), false);
  const base = { zero: false, reader: false, attached: 'dev', last: 'dev' };
  assert.deepEqual(branch.facts(attached('feature'), base), { need: 'feature', other: '', wtb: 'feature', off: false, related: '', switched: false });
  assert.deepEqual(branch.facts(attached('release'), base), { need: 'feature', other: '', wtb: 'release', off: true, related: 'qa', switched: false });
  // an override for the work tree's branch: not off, and nothing offered (the user chose it)
  const s = attached('hotfix', { deployments: [mainPinned(), devLive({ branch: 'feature', branchOverride: 'hotfix' }), pinned('qa', 'c:9a9a9a9', { branch: 'hotfix' })] });
  assert.equal(branch.facts(s, base).off, false);
  assert.deepEqual(ds.branchOffers(s, opts), []);
  // no workTree.branch (an older xbind): nothing known
  assert.equal(branch.facts(onDev({ deployments: [mainPinned(), devLive({ branch: 'feature' })] }), base).wtb, null);
});

test('a branch switch on the chip, the menu and the launcher', () => {
  const s = switched('hotfix');
  const c = ds.chip(s, opts);
  assert.equal(c.text, 'Live reload paused · branch hotfix');
  assert.equal(c.compact, 'branch');
  assert.equal(c.title, 'Live reload paused — the work tree is on hotfix, and dev requires feature: dev keeps running c:5e5e5e5.');
  const items = ds.chipItems(s, opts);
  assert.deepEqual(labels(items).slice(0, 5), ['Live reload', c.title, 'Keep dev on hotfix this time', 'Add a deployment for hotfix…', undefined]);
  const keep = items.find((it) => it.label === 'Keep dev on hotfix this time');
  assert.deepEqual({ op: keep.op, deployment: keep.deployment, other: keep.other, enabled: keep.enabled }, { op: 'resume', deployment: 'dev', other: true, enabled: true });
  assert.equal(items.find((it) => it.op === 'addFor').branch, 'hotfix');
  assert.equal(ds.launcher(s, opts).banner.text, 'Live reload is paused: the work tree is on hotfix, and dev requires feature.');
  // the branch of another deployment: follow it, nothing else
  const r = ds.chipItems(switched('release'), opts).filter((it) => it.offer);
  assert.deepEqual(r.map((it) => [it.label, it.op, it.deployment]), [['Resume live reload on qa (release)', 'resume', 'qa']]);
  // back on dev's branch: resume it
  const back = switched('feature');
  assert.equal(ds.chip(back, opts).text, 'Live reload paused · 2');
  assert.equal(ds.chip(back, opts).title, 'Live reload paused when the work tree left feature; it is on feature again — resume live reload on dev to follow saves.');
  assert.deepEqual(ds.chipItems(back, opts).filter((it) => it.offer).map((it) => [it.label, it.op, it.deployment]), [['Resume live reload on dev', 'resume', 'dev']]);
  // attached: a checkout that changed no file yet — follow qa, and the tooltip says what the next save does
  const a = attached('release');
  assert.match(ds.chip(a, opts).title, /The work tree is on release, and dev requires feature: the next save pauses live reload\.$/);
  assert.deepEqual(ds.chipItems(a, opts).filter((it) => it.offer).map((it) => [it.label, it.op, it.deployment]), [['Attach live reload to qa (release)', 'attach', 'qa']]);
  // an unguarded target (main), the work tree on dev's branch: attach to dev
  const m = paused({ features: FEATURES, liveReload: 'main', workTree: { branch: 'feature' }, deployments: [devLive({ branch: 'feature', liveReload: false, checkpoint: { id: 'c:1' } }), { ...mainPinned(), liveReload: true, checkpoint: null }] });
  assert.deepEqual(ds.chipItems(m, opts).filter((it) => it.offer).map((it) => it.label), ['Attach live reload to dev (feature)']);
  // no offers without the feature, for a reader, or on dev's own branch
  assert.equal(ds.chipItems(switched('hotfix', { features: ['live-reload/1'] }), opts).some((it) => it.offer), false);
  assert.equal(ds.chipItems(attached('feature'), opts).some((it) => it.offer), false);
  // the offers take the permission of what they start
  const denied = switched('hotfix', { caller: terminalCaller({ can: { pause: yes, resume: no('resuming live reload needs terminal-level access on apps/crm'), reloadNow: yes, add: no('nope'), edges: yes, protect: yes } }) });
  assert.deepEqual(ds.chipItems(denied, opts).filter((it) => it.offer).map((it) => [it.enabled, it.hint]),
    [[false, 'resuming live reload needs terminal-level access on apps/crm'], [false, 'nope']]);
  // a menu item hands its whole item to run (other, branch)
  const calls = [];
  ds.toMenu(items, (op, dep, it) => calls.push([op, dep, it.other, it.branch])).filter((x) => x.label?.startsWith('Keep') || x.label?.startsWith('Add a')).forEach((x) => x.action());
  assert.deepEqual(calls, [['resume', 'dev', true, undefined], ['addFor', '', undefined, 'hotfix']]);
});

test('the panel: header offers, the Branch row, its actions, the side list and the log', () => {
  const h = ds.panelHeader(switched('hotfix'), opts);
  assert.deepEqual(h.offers.map((o) => o.id), ['keep/dev', 'addFor/hotfix']);
  assert.ok(!h.actions.some((a) => a.id.startsWith('keep') || a.id.startsWith('addFor')));
  assert.deepEqual(ds.panelHeader(switched('release'), opts).offers.map((o) => o.id), ['follow/qa']);
  const s = attached('feature', { deployments: [mainPinned(), devLive({ branch: 'feature', branchOverride: 'hotfix', can: depCan({ remove: yes, branch: yes }) })] });
  const ov = ds.overview(s, 'dev', opts).lines;
  assert.deepEqual(ov.find(([k]) => k === 'branch'), ['branch', 'feature · takes hotfix this time']);
  assert.equal(ds.overview(s, 'main', opts).lines.some(([k]) => k === 'branch'), false);
  assert.deepEqual(ds.overview(attached('x', { deployments: [mainPinned(), devLive()] }), 'dev', opts).lines.find(([k]) => k === 'branch'),
    ['branch', 'none — the work tree feeds it on any branch']);
  const acts = ds.panelActions(s, 'dev', opts).filter((a) => a.id === 'branch' || a.id === 'clearBranch');
  assert.deepEqual(acts.map((a) => [a.id, a.label, a.enabled]), [['branch', 'Branch: feature…', true], ['clearBranch', 'Clear branch', true]]);
  assert.equal(ds.panelActions(onDev({ deployments: [mainPinned(), devLive({ branch: 'feature' })] }), 'dev', opts).some((a) => a.id === 'branch'), false, 'no branch actions without branches/1');
  assert.equal(ds.panelRows(s, opts).find((r) => r.name === 'dev').branch, 'feature');
  const log = ds.logRows(s, 'dev', [{ id: 3, how: 'attach', checkpoint: 'c:5e5e5e5', followsWorkTree: true, result: 'ok', by: 'user:ana', requestedAt: at(3), branch: 'feature' }], opts);
  assert.equal(log[0].branch, 'feature');
});

test('the add form and the Set branch… form', () => {
  const s = attached('feature');
  const f = ds.addDialog(s, '', []).fields;
  const b = f.find((x) => x.name === 'branch');
  assert.deepEqual(b.options.map((o) => o.value), ['none', 'current', 'new']);
  assert.equal(b.options[1].label, 'current (feature) — it requires feature');
  assert.equal(b.value, 'none');
  assert.deepEqual(f.map((x) => x.name), ['name', 'from', 'data', 'branch', 'newBranch', 'attach']);
  assert.equal(ds.addDialog(onDev(), '', []).fields.some((x) => x.name === 'branch'), false, 'no Branch control without branches/1');
  const pre = ds.addDialog(s, '', [], { name: 'hotfix', attach: true, branch: 'feature' }).fields;
  assert.deepEqual([pre[0].value, pre.find((x) => x.name === 'branch').value, pre.find((x) => x.name === 'attach').value], ['hotfix', 'current', true]);
  assert.deepEqual(branch.readAdd({ branch: 'current' }, s), { branch: 'feature' });
  assert.deepEqual(branch.readAdd({ branch: 'new', newBranch: ' feat/x ' }, s), { newBranch: 'feat/x' });
  assert.deepEqual(branch.readAdd({ branch: 'new', newBranch: '' }, s), { error: 'Name the new branch.' });
  assert.deepEqual(branch.readAdd({ branch: 'new', newBranch: '-x' }, s), { error: branch.BAD_NAME });
  assert.deepEqual(branch.readAdd({ branch: 'none' }, s), {});
  for (const bad of ['-x', '.x', 'a..b', 'a//b', 'a/', 'x.lock', 'HEAD', 'a b', '']) assert.equal(branch.nameOK(bad), false, bad);
  for (const good of ['feature', 'feat/x', 'release-1.2+x', 'A_b']) assert.equal(branch.nameOK(good), true, good);
  assert.deepEqual(['feature/Add-Login', 'release-1.2', '123', 'main', 'x'.repeat(40)].map(branch.suggestName), ['add-login', 'release-1-2', '', '', 'x'.repeat(24)]);
  const d = branch.dialog(s, 'dev', '');
  assert.equal(d.fields[0].value, 'feature');
});

test('confirmations and results', () => {
  const s = switched('hotfix');
  const c = ds.confirmation('resume', { state: s, impact: { code: null, data: 'none', affects: 'deployment', reloads: ['dev'],
    branch: { deployment: 'dev', assigned: 'feature', workTree: 'hotfix', other: true } }, deployment: 'dev', other: true }, opts);
  assert.equal(c.message.split('\n')[1], "Branch: dev requires feature; it takes the work tree's hotfix this time, until live reload moves or the work tree's branch changes again.");
  const same = ds.confirmation('reloadNow', { state: s, impact: { code: null, branch: { deployment: 'dev', assigned: 'feature', workTree: 'feature' } } }, opts);
  assert.equal(same.message.split('\n')[1], 'Branch: dev requires feature — the work tree is on it.');
  const add = ds.confirmation('add', { state: s, impact: { code: null, affects: 'nobody', branch: { deployment: 'qa2', assigned: 'x', workTree: 'x' } }, deployment: 'qa2', add: { attach: true, newBranch: 'x' } }, opts);
  assert.match(add.message, /^Branch: x is created at the work tree's HEAD and checked out \(no file changes\); qa2 requires it\.$/m);
  assert.equal(add.message.includes('Branch: qa2 requires'), false);
  const set = ds.confirmation('branch', { state: s, impact: { branch: { deployment: 'qa', assigned: 'release', workTree: 'hotfix' }, pausesLiveReload: false }, deployment: 'qa', branch: 'release' }, opts);
  assert.equal(set.title, 'Assign qa branch release?');
  assert.match(set.message, /The work tree is on hotfix now\./);
  assert.deepEqual(set.send({}), {});
  assert.equal(ds.confirmation('branch', { state: s, deployment: 'qa', branch: null }, opts).title, "Clear qa's branch?");
  assert.ok(ds.PANEL_OPS.includes('branch'));
  assert.equal(ds.result('branch', { state: s }, { deployment: 'qa' }), 'qa requires release.');
});

test("a mismatch's question, from the server's text", () => {
  const err = 'dev is assigned branch feature, and the work tree is on main: check out feature, or send confirm:"other-branch" to use main this time';
  assert.deepEqual(branch.mismatch(err), { deployment: 'dev', assigned: 'feature', workTree: 'main' });
  assert.equal(branch.mismatch("dev is assigned branch feature, and the work tree isn't on a branch (a detached HEAD, or no repository xbind can read): check out feature"), null);
  assert.equal(branch.mismatch('live reload is paused: resume it onto dev instead'), null);
  const d = branch.mismatchDialog(branch.mismatch(err), err);
  assert.equal(d.title, 'dev requires branch feature');
  assert.deepEqual(d.buttons.map((b) => b.label), ['Cancel', 'Use main this time']);
});

test('the grey lines of op branch, and the state refetched', () => {
  const pin = () => 'c:5e5e5e5';
  const ev = { op: 'branch', deployment: 'dev', assigned: 'feature', workTree: 'hotfix', related: '', paused: true };
  assert.equal(branch.notice(ev, switched('hotfix'), pin), 'live reload paused — the work tree is on hotfix, and dev requires feature; dev keeps running c:5e5e5e5 — keep dev on hotfix this time, or add a deployment for it, from the live reload chip');
  assert.equal(branch.notice({ ...ev, workTree: 'release', related: 'qa' }, switched('release'), pin),
    'live reload paused — the work tree is on release, and dev requires feature; dev keeps running c:5e5e5e5 — qa requires release: follow it from the live reload chip');
  assert.equal(branch.notice({ ...ev, workTree: 'feature', paused: false }, switched('feature'), pin), "the work tree is on feature again, dev's branch — resume live reload on dev from the live reload chip");
  assert.equal(branch.notice({ op: 'branch', deployment: 'main', assigned: '', workTree: 'feature', related: 'dev', paused: false }, attached('feature', { liveReload: 'main' }), pin),
    'the work tree is on feature, which dev requires — attach live reload on dev from the live reload chip');
  assert.equal(branch.notice({ op: 'branch', deployment: 'main', assigned: '', workTree: 'x', related: '', paused: false }, attached('x'), pin), null);
  assert.equal(ds.applyEvent(switched('hotfix'), { op: 'branch' }).refetch, true);
});
