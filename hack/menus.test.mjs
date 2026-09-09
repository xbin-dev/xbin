// hack/menus.test.mjs — unit tests for the shell's context-menu builders
// (workspace-template/shell/menus.js), run by `make js-test` (part of
// `make check`): node's built-in runner, no dependencies. The builders are
// pure over a state view + an actions object, so every branch the shell
// can show is a fixture here: org-screen draft lines, what "Open tile"
// lists/disables, the create-tile variants by owner count, the admin block
// per lifecycle state, and which action each line fires.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { canvasMenuItems, openTileItems, tileMenuItems, offloaded, hidden } from '../workspace-template/shell/menus.js';

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
  assert.equal(open[0].kind, 'grid');
  assert.deepEqual(open[0].cells.map((c) => c.label), ['terminal', 'logs', 'source', 'proposals']);
  assert.equal(open[0].cells[3].badge, 2);
  assert.deepEqual(labels(open).slice(1), ['<sep>', 'Close on this screen', 'Unpin into a window', 'Open full page'], 'no admin block without canAdminTile');
  open[0].cells[0].action();
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
  assert.deepEqual(labels(enabled).slice(5), ['<sep>', '<header>', 'Disable', 'Hide',
    'Access…', 'Runtime…', 'Vault…', 'Roles & grants…', 'Interfaces…', 'Backup…', 'Cron…']);
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
