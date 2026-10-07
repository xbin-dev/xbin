// hack/menus.test.mjs — unit tests for the shell's context-menu builders
// (workspace-template/shell/menus.js), run by `make js-test` (part of
// `make check`): node's built-in runner, no dependencies. The builders are
// pure over a state view + an actions object, so every branch the shell
// can show is a fixture here: org-screen draft lines, what "Open tile"
// lists/disables, the create-tile variants by owner count, the admin block
// per lifecycle state, which action each line fires, and the optional
// tile deployments line (absent in the zero state).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { canvasMenuItems, openTileItems, tileMenuItems, offloaded, hidden,
  useDeployLookup, deploySummary, deployHint, deployCheckpoint, deployFailed,
  deployNames, shownDeployment, deployMenu } from '../workspace-template/shell/menus.js';

const comps = [
  { path: 'root' },
  { path: 'apps/a', state: 'enabled' },
  { path: 'apps/b', state: 'enabled', owner: 'org:sales' },
  { path: 'apps/c', state: 'hidden' },
  { path: 'apps/d', state: 'offloaded' },
  { path: 'lib/t', template: true },
  { path: 'apps/e', state: 'enabled' },
];
const state = (over = {}) => ({
  orgScreen: null, draft: null, owners: [{ value: 'user:me', label: '— me (personal) —' }], components: comps,
  tiles: [{ path: 'apps/a' }], recent: ['apps/e', 'apps/a', 'apps/zzz'], showHidden: false, canMutate: true,
  prs: { 'apps/a': 2 }, canAdminTile: () => false, ...over,
});
// An actions object that records every call and answers true (confirm included).
const spy = (over = {}) => {
  const calls = [];
  const a = new Proxy({}, { get: (_, k) => over[k] ?? ((...args) => { calls.push([k, ...args]); return true; }) });
  return { a, calls };
};
const labels = (items) => items.map((it) => (it.kind ? `<${it.kind}>` : it.label));
const byLabel = (items, l) => items.find((it) => it.label === l);
// Label-based lookups for the tile menu, so a line or square added elsewhere
// in it doesn't shift what an assertion reads: the grid by kind, a square by
// its label, a header's section by the header's label.
const grid = (items) => items.find((it) => it.kind === 'grid');
const cell = (items, l) => grid(items)?.cells.find((c) => c.label === l);
const section = (items, header) => { // the lines under a header, up to the next separator or header; null without it
  const at = items.findIndex((it) => it.kind === 'header' && it.label === header);
  if (at < 0) return null;
  const end = items.findIndex((it, i) => i > at && (it.kind === 'sep' || it.kind === 'header'));
  return labels(items.slice(at + 1, end < 0 ? items.length : end));
};
// `want`'s labels all appear in `items`, in this order; other lines may sit between them
const assertInOrder = (items, want, msg) => {
  const got = labels(items);
  let at = -1;
  for (const l of want) {
    const i = got.indexOf(l, at + 1);
    assert.ok(i > at, `${msg}: "${l}" missing or out of order in ${JSON.stringify(got)}`);
    at = i;
  }
};

test('lifecycle predicates', () => {
  assert.equal(offloaded({ state: 'offloaded-full' }), true);
  assert.equal(offloaded({ state: 'enabled' }), false);
  assert.equal(hidden({ state: 'hidden' }), true);
  assert.equal(hidden(undefined), false);
});

test('canvas menu on a personal screen, one owner', () => {
  const { a, calls } = spy();
  const items = canvasMenuItems(state(), a);
  assert.deepEqual(labels(items), ['Open tile', 'Create a new tile…', 'New screen', '<sep>', 'Bring windows on-screen']);
  const create = byLabel(items, 'Create a new tile…');
  assert.equal(create.hint, 'me (personal)');
  create.action();
  byLabel(items, 'New screen').action();
  byLabel(items, 'Bring windows on-screen').action();
  assert.deepEqual(calls, [['newTileDialog', '', '', 'user:me', { fixed: true }], ['addScreen'], ['fitWindows', true]]);
  assert.ok(Array.isArray(byLabel(items, 'Open tile').items), 'Open tile carries a submenu');
});

test('create-tile variants by owner count', () => {
  const { a } = spy();
  const two = canvasMenuItems(state({ owners: [{ value: 'user:me', label: '— me (personal) —' }, { value: 'org:sales', label: 'org: Sales' }] }), a);
  const sub = byLabel(two, 'Create a new tile');
  assert.deepEqual(sub.items.map((i) => i.label), ['me (personal)', 'org: Sales']);
  const none = canvasMenuItems(state({ owners: [] }), a);
  const dis = byLabel(none, 'Create a new tile…');
  assert.equal(dis.disabled, true);
  assert.match(dis.hint, /org-only policy/);
  // the account's own switch (D88) says so instead
  const off = byLabel(canvasMenuItems(state({ owners: [], ownerHint: 'personal tiles are off for your account — ask an admin' }), a), 'Create a new tile…');
  assert.match(off.hint, /off for your account/);
});

test('canvas menu on an org screen: edit / draft lines', () => {
  const { a, calls } = spy();
  const os = { id: 'sales:1', canEdit: true };
  const view = canvasMenuItems(state({ orgScreen: os }), a);
  assert.deepEqual(labels(view).slice(0, 3), ['Edit this org screen', 'Copy to my screens', '<sep>']);
  byLabel(view, 'Edit this org screen').action();
  const clean = canvasMenuItems(state({ orgScreen: os, draft: { dirty: false } }), a);
  assert.deepEqual(labels(clean).slice(0, 4), ['Save and update for everyone', 'Discard draft', 'Copy to my screens', '<sep>']);
  assert.equal(byLabel(clean, 'Save and update for everyone').disabled, true, 'a clean draft has nothing to save');
  const dirty = canvasMenuItems(state({ orgScreen: os, draft: { dirty: true } }), a);
  assert.equal(byLabel(dirty, 'Save and update for everyone').disabled, false);
  byLabel(dirty, 'Save and update for everyone').action();
  byLabel(dirty, 'Discard draft').action();
  byLabel(dirty, 'Copy to my screens').action();
  const viewer = canvasMenuItems(state({ orgScreen: { id: 'x', canEdit: false } }), a);
  assert.deepEqual(labels(viewer).slice(0, 2), ['Copy to my screens', '<sep>'], 'no edit line without canEdit');
  assert.deepEqual(calls, [['enterEdit', 'sales:1'], ['saveOrgDraft', 'sales:1'], ['discardDraft', 'sales:1'], ['copyOrgScreen', 'sales:1']]);
});

test('open-tile submenu: find box, recents, the rest sorted, open ones disabled', () => {
  const { a, calls } = spy();
  const items = openTileItems(state(), a);
  assert.equal(items[0].kind, 'input');
  assert.deepEqual(labels(items).slice(1), ['<header>', 'e', 'a', 'b'], 'recent e (closed) first; a is open so not recent; root/template/hidden/offloaded gone');
  const open = byLabel(items, 'a');
  assert.equal(open.disabled, true);
  assert.equal(open.hint, 'open');
  assert.equal(byLabel(items, 'b').hint, 'apps', 'the dir is the hint');
  assert.equal(byLabel(items, 'b').keywords, 'apps/b', 'the full path is searchable');
  byLabel(items, 'e').action();
  assert.deepEqual(calls, [['openTile', 'apps/e']]);
  const shown = openTileItems(state({ showHidden: true }), a);
  assert.ok(byLabel(shown, 'c'), 'show-hidden lists hidden tiles');
  const many = openTileItems(state({ recent: ['apps/e', 'apps/b', 'apps/c'], tiles: [] }), a);
  assert.deepEqual(labels(many).slice(1), ['<header>', 'e', 'b', 'a'], 'recents keep their order; hidden c is not readable');
});

test('tile menu: panels, open/closed lines, view mode', () => {
  const { a, calls } = spy();
  const open = tileMenuItems('apps/a', state(), a);
  assert.equal(open[0].kind, 'grid', 'the squares lead the menu');
  // exactly four squares: the phone sheet's grid is four columns (web/bx-menu.js), so a fifth would wrap
  assert.deepEqual(grid(open).cells.map((c) => c.label), ['terminal', 'logs', 'source', 'proposals']);
  assert.equal(cell(open, 'proposals').badge, 2);
  assertInOrder(open, ['Close on this screen', 'Unpin into a window', 'Open full page'], 'an open tile\'s lines');
  assert.equal(section(open, 'admin'), null, 'no admin block without canAdminTile');
  assert.equal(byLabel(open, 'Disable'), undefined);
  cell(open, 'terminal').action();
  byLabel(open, 'Close on this screen').action();
  byLabel(open, 'Unpin into a window').action();
  byLabel(open, 'Open full page').action();
  assert.deepEqual(calls, [['frameOpen', 'apps/a', 'term'], ['toggle', 'apps/a'], ['togglePin', 'apps/a'], ['openFullPage', 'apps/a']]);
  const floating = tileMenuItems('apps/a', state({ tiles: [{ path: 'apps/a', float: { x: 1 } }] }), a);
  assert.ok(byLabel(floating, 'Pin to the grid'));
  const view = tileMenuItems('apps/a', state({ canMutate: false }), a);
  assert.equal(byLabel(view, 'Close on this screen').disabled, true);
  assert.equal(byLabel(view, 'Close on this screen').hint, 'view mode');
  assert.equal(byLabel(view, 'Unpin into a window').disabled, true);
  const closed = tileMenuItems('apps/b', state(), a);
  assert.ok(byLabel(closed, 'Open on this screen'));
  assert.ok(byLabel(tileMenuItems('apps/b', state({ orgScreen: { id: 'x', canEdit: false } }), a), 'Open on my screen'));
  assert.ok(byLabel(tileMenuItems('apps/b', state({ orgScreen: { id: 'x', canEdit: true } }), a), 'Open here (starts a draft)'));
  assert.ok(byLabel(tileMenuItems('apps/b', state({ orgScreen: { id: 'x', canEdit: true }, draft: { dirty: false } }), a), 'Open on this screen'));
});

test('tile menu: the admin block per lifecycle state', () => {
  const admin = { canAdminTile: () => true };
  const { a, calls } = spy();
  const enabled = tileMenuItems('apps/a', state(admin), a);
  assert.deepEqual(section(enabled, 'admin'), ['Disable', 'Hide',
    'Access…', 'Runtime…', 'Vault…', 'Roles & grants…', 'Interfaces…', 'Backup…', 'Cron…']);
  assertInOrder(enabled, ['Open full page', '<sep>', '<header>', 'Disable'], 'the admin block follows the tile\'s lines');
  assert.equal(byLabel(enabled, 'Disable').danger, true);
  byLabel(enabled, 'Disable').action();
  byLabel(enabled, 'Hide').action();
  byLabel(enabled, 'Interfaces…').action();
  assert.deepEqual(calls.filter((c) => c[0] !== 'confirm'), [['lifecycle', 'apps/a', 'disabled'], ['lifecycle', 'apps/a', 'hidden'], ['openAdminWin', 'apps/a', 'interfaces']]);
  assert.equal(calls.filter((c) => c[0] === 'confirm').length, 2, 'both destructive lines confirm first');
  const hiddenT = tileMenuItems('apps/c', state(admin), a);
  assert.ok(byLabel(hiddenT, 'Unhide'));
  assert.equal(byLabel(hiddenT, 'Hide'), undefined, 'a hidden tile is not offered Hide');
  const off = tileMenuItems('apps/d', state(admin), a);
  assert.ok(byLabel(off, 'Enable'));
  assert.equal(byLabel(off, 'Hide'), undefined, 'an offloaded tile is not offered Hide');
  const declined = spy({ confirm: () => false });
  byLabel(tileMenuItems('apps/a', state(admin), declined.a), 'Disable').action();
  assert.deepEqual(declined.calls, [], 'a declined confirm fires nothing');
});

// ---- tile deployments: the tile menu's optional ⇈ line ----
// A tile with a deployment record carries the primary summary on its
// /components row; the viewer's view comes from its deployments state
// (GET /api/xbin/deployments), which the state view's deployState answers.
const DEP = 'Deployments…';
const withSummary = (sum) => comps.map((c) => (c.path === 'apps/a' ? { ...c, deployments: sum } : c));
const pinnedSum = { primary: 'main', pinned: true, protected: false };
const liveSum = { primary: 'main', pinned: false, protected: false };
// a deployments state in the viewer's view (11-contract §1.1, §1.3)
const depState = ({ view = 'full', level = 'terminal', pinned = true, record = true, failed = false } = {}) => ({
  tile: 'apps/a', record, view, primary: 'main', liveReload: pinned ? '' : 'main', protectedPrimary: false,
  deployments: record ? [{ name: 'main', primary: true, liveReload: !pinned,
    checkpoint: pinned ? { id: 'c:3f2a1c9', hash: '3f2a1c9e' } : null,
    lastDeploy: { result: failed ? 'failed' : 'ok' } }] : [],
  caller: { level },
});

// covers D119c — a tile without a deployment record (no summary on its row)
// gets exactly today's tile menu, whatever the lookup answers: the
// Deployments line needs the summary an older xbind never sends.
test('tile menu: no deployments line in the zero state', () => {
  const { a } = spy();
  const today = labels(tileMenuItems('apps/a', state(), a));
  assert.ok(!today.includes(DEP));
  for (const deployState of [() => undefined, () => null, () => ({ tile: 'apps/a', record: false })]) {
    assert.deepEqual(labels(tileMenuItems('apps/a', state({ deployState }), a)), today, 'no summary: today\'s lines');
  }
  const admin = { canAdminTile: () => true };
  assert.deepEqual(labels(tileMenuItems('apps/a', state({ ...admin, deployState: () => undefined }), a)),
    labels(tileMenuItems('apps/a', state(admin), a)), 'the admin block is today\'s too');
});

// covers 10-ux §7 — the summary alone (the state not loaded yet) offers one
// line after "Open full page", never a fifth square, hinting at the
// primary's state; it opens the terminal window's Deployments layout, and
// the menu asks the lookup for the tile's state so it loads.
test('tile menu: the deployments line from the summary', () => {
  const { a, calls } = spy();
  const asked = [];
  const deployState = (p, c) => { asked.push([p, c?.deployments]); return undefined; };
  const items = tileMenuItems('apps/a', state({ components: withSummary(pinnedSum), deployState }), a);
  assert.deepEqual(asked, [['apps/a', pinnedSum]], 'the lookup gets the path and the row');
  assert.deepEqual(grid(items).cells.map((c) => c.label), ['terminal', 'logs', 'source', 'proposals'], 'still four squares');
  assertInOrder(items, ['Open full page', DEP], 'the line follows Open full page');
  const line = byLabel(items, DEP);
  assert.equal(line.icon, 'deploy');
  assert.equal(line.hint, 'main pinned');
  line.action();
  assert.deepEqual(calls, [['frameOpen', 'apps/a', 'deployments']]);
  const live = byLabel(tileMenuItems('apps/a', state({ components: withSummary(liveSum), deployState: () => undefined }), a), DEP);
  assert.equal(live.hint, 'main follows the work tree');
  const admin = tileMenuItems('apps/a', state({ components: withSummary(pinnedSum), deployState: () => undefined, canAdminTile: () => true }), a);
  assertInOrder(admin, ['Open full page', DEP, '<sep>', '<header>', 'Disable'], 'before the admin block');
  assert.deepEqual(section(admin, 'admin'), ['Disable', 'Hide', 'Access…', 'Runtime…', 'Vault…', 'Roles & grants…', 'Interfaces…', 'Backup…', 'Cron…']);
});

// covers 10-ux §7 — once the state is loaded it decides: the checkpoint in
// the hint, no line for a viewer with read access only (the reader view:
// nothing to operate), none for a tile that opted out since the row was
// fetched, and the row's summary again when this xbind couldn't answer.
test('tile menu: the deployments line from the state', () => {
  const { a } = spy();
  const menu = (st, sum = pinnedSum) => tileMenuItems('apps/a', state({ components: withSummary(sum), deployState: () => st }), a);
  assert.equal(byLabel(menu(depState()), DEP).hint, 'main pinned to c:3f2a1c9');
  assert.equal(byLabel(menu(depState({ level: 'write' })), DEP).hint, 'main pinned to c:3f2a1c9');
  assert.equal(byLabel(menu(depState({ pinned: true }), liveSum), DEP).hint, 'main pinned to c:3f2a1c9', 'the state is fresher than the row');
  assert.equal(byLabel(menu(depState({ pinned: false }), pinnedSum), DEP).hint, 'main follows the work tree');
  assert.equal(byLabel(menu(depState({ view: 'reader', level: 'read' })), DEP), undefined, 'a reader has nothing to operate');
  assert.equal(byLabel(menu(depState({ level: 'read' })), DEP), undefined);
  assert.equal(byLabel(menu(depState({ record: false })), DEP), undefined, 'opted out since: the zero state');
  assert.equal(byLabel(menu(null), DEP).hint, 'main pinned', 'no answer: the row decides');
});

// covers 10-ux §7 — without a deployState in the view (the shell's own
// _menuState has none) the lookup shell-kit.js installs answers.
test('tile menu: the installed deployments lookup', () => {
  const { a } = spy();
  try {
    useDeployLookup((p) => (p === 'apps/a' ? depState() : undefined));
    assert.equal(byLabel(tileMenuItems('apps/a', state({ components: withSummary(pinnedSum) }), a), DEP).hint, 'main pinned to c:3f2a1c9');
    useDeployLookup(() => depState({ view: 'reader', level: 'read' }));
    assert.equal(byLabel(tileMenuItems('apps/a', state({ components: withSummary(pinnedSum) }), a), DEP), undefined);
  } finally { useDeployLookup(() => undefined); }
});

// covers D119c — the summary helpers the ⇈ badges share: the row's summary
// until a state is loaded, none for a tile without a record, the primary's
// checkpoint and a failed last deploy onto it.
test('deployments summary helpers', () => {
  assert.equal(deploySummary({ path: 'apps/a' }, undefined), null);
  assert.equal(deploySummary({ path: 'apps/a' }, null), null);
  assert.deepEqual(deploySummary({ deployments: liveSum }, undefined), liveSum);
  assert.deepEqual(deploySummary({ deployments: liveSum }, depState()), pinnedSum);
  assert.equal(deploySummary({ deployments: pinnedSum }, depState({ record: false })), null);
  assert.deepEqual(deploySummary(null, { ...depState(), primary: 'dev', protectedPrimary: true,
    deployments: [{ name: 'dev', liveReload: false, checkpoint: { id: 'c:77aa01b' } }] }), { primary: 'dev', pinned: true, protected: true });
  assert.equal(deployCheckpoint(depState()), 'c:3f2a1c9');
  assert.equal(deployCheckpoint(depState({ pinned: false })), '');
  assert.equal(deployCheckpoint(undefined), '');
  assert.equal(deployFailed(depState({ failed: true })), true);
  assert.equal(deployFailed(depState()), false);
  assert.equal(deployHint({ primary: 'dev', pinned: true }, undefined), 'dev pinned');
});

// ---- the deployment a tile's window shows (the head's ⇈ menu, +name tag) ----
const withDev = (over = {}) => {
  const s = depState(over);
  return { ...s, liveReload: 'dev', deployments: [...s.deployments, { name: 'dev', liveReload: true, checkpoint: null }] };
};

// covers D127j — what a window may show: the primary first, then every
// deployment the viewer's state lists; nothing to switch to while unknown,
// without a record, or for a reader (whose state names the primary only).
test('window deployments: the names a window may show', () => {
  assert.deepEqual(deployNames(withDev()), ['main', 'dev']);
  assert.deepEqual(deployNames({ ...withDev(), primary: 'dev' }), ['dev', 'main']);
  assert.deepEqual(deployNames(depState()), ['main']);
  assert.deepEqual(deployNames(undefined), []);
  assert.deepEqual(deployNames(null), []);
  assert.deepEqual(deployNames(depState({ record: false })), []);
});

// covers D127j — the layout's pick shows only while it is a non-primary
// deployment the state lists; the primary's own name (its alias) and a
// removed one show the primary; the layout wins while the state is unknown.
test('window deployments: which one a window shows', () => {
  assert.equal(shownDeployment('dev', withDev()), 'dev');
  assert.equal(shownDeployment('main', withDev()), '');
  assert.equal(shownDeployment('dev', { ...withDev(), primary: 'dev' }), '');
  assert.equal(shownDeployment('gone', withDev()), '');
  assert.equal(shownDeployment('dev', depState()), '');
  assert.equal(shownDeployment('dev', undefined), 'dev');
  assert.equal(shownDeployment('', withDev()), '');
  assert.equal(shownDeployment('dev', null), '');
});

// covers D127j — the ⇈ menu: a pick per deployment (the one shown checked;
// picking the primary clears the pick), the full page of what's shown, and
// the Deployments panel for anyone who may operate.
test('window deployments: the head menu', () => {
  const { a, calls } = spy();
  let items = deployMenu('apps/a', { deployments: pinnedSum }, withDev(), '', a);
  assert.deepEqual(labels(items), ['<header>', 'main', 'dev', '<sep>', 'Deployments…']);
  assert.equal(byLabel(items, 'main').checked, true);
  assert.equal(byLabel(items, 'dev').checked, false);
  assert.equal(byLabel(items, 'main').hint, 'primary · pinned to c:3f2a1c9');
  assert.equal(byLabel(items, 'dev').hint, 'live reload');
  byLabel(items, 'dev').action();
  byLabel(items, 'Deployments…').action();
  assert.deepEqual(calls, [['show', 'dev'], ['openPanel']]);

  calls.length = 0;
  items = deployMenu('apps/a', { deployments: pinnedSum }, withDev(), 'dev', a);
  assert.deepEqual(labels(items), ['<header>', 'main', 'dev', 'Open apps/a+dev full page', '<sep>', 'Deployments…']);
  assert.equal(byLabel(items, 'dev').checked, true);
  byLabel(items, 'main').action();
  byLabel(items, 'Open apps/a+dev full page').action();
  assert.deepEqual(calls, [['show', ''], ['openPage', 'apps/a+dev']]);

  // a pinned primary alone: the status line, and the panel
  items = deployMenu('apps/a', { deployments: pinnedSum }, depState(), '', a);
  assert.deepEqual(labels(items), ['<header>', 'main', '<sep>', 'Deployments…']);
  // a reader: the primary only, no panel
  items = deployMenu('apps/a', { deployments: pinnedSum }, depState({ view: 'reader', level: 'read' }), '', a);
  assert.deepEqual(labels(items), ['<header>', 'main']);
});

test('Document mode (D187): the canvas menu Layout submenu and a tile Row submenu', () => {
  const layout = [{ kind: 'header', label: 'Layout' }, { label: 'Canvas', checked: false }, { label: 'Document', checked: true }];
  const cm = canvasMenuItems(state({ layoutItems: layout }), spy().a);
  assert.equal(byLabel(cm, 'Layout').hint, 'Document');
  assert.equal(byLabel(cm, 'Layout').items, layout);
  assert.equal(byLabel(canvasMenuItems(state(), spy().a), 'Layout'), undefined, 'no layout lines given: none shown');

  const tiles = [{ path: 'apps/a', x: 0, y: 0 }, { path: 'apps/b', x: 0, y: 400 }];
  assert.equal(byLabel(tileMenuItems('apps/a', state({ tiles }), spy().a), 'Row'), undefined, 'canvas mode: no Row lines');
  const { a, calls } = spy();
  const row = byLabel(tileMenuItems('apps/a', state({ tiles, docMode: true }), a), 'Row');
  assert.equal(row.hint, '1 column');
  assert.deepEqual(labels(row.items), ['1 column', '2 columns', '4 columns', '<sep>', 'Move up', 'Move down']);
  assert.ok(byLabel(row.items, '1 column').checked);
  assert.ok(byLabel(row.items, 'Move up').disabled, 'the first row alone cannot go up');
  assert.ok(!byLabel(row.items, 'Move down').disabled);
  byLabel(row.items, '2 columns').action();
  byLabel(row.items, 'Move down').action();
  assert.deepEqual(calls, [['docCols', 'apps/a', 2], ['docStep', 'apps/a', 1]]);
  // a fixed height offers fitting the content again; view mode disables the lot
  const fixed = [{ ...tiles[0], doc: { row: 0, col: 0, cols: 1, h: 300 } }, tiles[1]];
  assert.equal(byLabel(byLabel(tileMenuItems('apps/a', state({ tiles: fixed, docMode: true }), spy().a), 'Row').items, 'Fit height to content').hint, '300 px now');
  assert.ok(byLabel(tileMenuItems('apps/a', state({ tiles, docMode: true, canMutate: false }), spy().a), 'Row').disabled);
});
