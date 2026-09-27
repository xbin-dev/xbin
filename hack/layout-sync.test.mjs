// workspace-template/shell/layout-sync.js — the shell follows a layout
// another client saved (a `prefs` event), skips its own writes and holds the
// reload while an edit is under way. The browser end is the UI harness's
// layoutSync pass.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { foreignWrite, editing, follow } from '../workspace-template/shell/layout-sync.js';

const ev = (data, component = 'root') => ({ type: 'prefs', component, data });

test('foreignWrite: the shell bucket\'s layout key, written by someone else', () => {
  assert.equal(foreignWrite(ev({ key: 'layout', writer: 'phone' }), 'layout', 'tab1'), true);
  assert.equal(foreignWrite(ev({ key: 'layout' }), 'layout', 'tab1'), true, 'a write without a writer id is foreign');
  assert.equal(foreignWrite(ev({ key: 'layout', writer: 'tab1' }), 'layout', 'tab1'), false, 'our own save');
  assert.equal(foreignWrite(ev({ key: 'mobile-screens' }), 'layout', 'tab1'), false, 'another key');
  assert.equal(foreignWrite(ev({ key: 'layout' }, 'apps/notes'), 'layout', 'tab1'), false, 'a tile\'s bucket');
  assert.equal(foreignWrite({ type: 'status', component: 'root', data: { key: 'layout' } }, 'layout', 'tab1'), false);
});

test('editing: drafts, dialogs, menus, pending saves and drags hold the reload', () => {
  const idle = { _layoutLoaded: true, _orgDrafts: {}, _folderDrafts: {}, _dialogs: [] };
  const doc = (shield) => ({ querySelector: (sel) => (shield && sel === '[data-drag-shield]' ? {} : null) });
  assert.equal(editing(idle, doc(false)), false);
  assert.equal(editing(idle, doc(true)), true, 'a drag');
  for (const busy of [{ _layoutLoaded: false }, { _saveTimer: 7 }, { _orgDrafts: { o1: {} } }, { _folderDrafts: { org: {} } },
    { _create: {} }, { _folderEdit: {} }, { _conflict: {} }, { _menu: {} }, { _dialogs: [{}] }, { _canvas: { _drag: {} } }]) {
    assert.equal(editing({ ...idle, ...busy }, doc(false)), true, JSON.stringify(busy));
  }
});

test('follow: reloads keeping the active screen; waits out an edit; ignores our own', async () => {
  let loads = 0;
  const s = { _layoutLoaded: true, _orgDrafts: {}, _folderDrafts: {}, _dialogs: [], _writer: 'tab1', _active: 'a', _orgScreens: [],
    _screens: [{ id: 'a' }],
    _loadLayout() { loads++; this._screens = [{ id: 'a' }, { id: 'phone' }]; this._active = 'phone'; return Promise.resolve(); } };
  await follow(s, ev({ key: 'layout', writer: 'tab1' }), 'layout');
  assert.equal(loads, 0);
  await follow(s, ev({ key: 'layout', writer: 'phone' }), 'layout');
  assert.equal(loads, 1);
  assert.equal(s._active, 'a', 'the tab stays on its screen');
  s._menu = {};
  const timers = [];
  const real = globalThis.setTimeout;
  globalThis.setTimeout = (fn) => { timers.push(fn); return timers.length; };
  try { follow(s, ev({ key: 'layout' }), 'layout'); } finally { globalThis.setTimeout = real; }
  assert.equal(loads, 1, 'held while the menu is open');
  assert.equal(timers.length, 1);
  s._menu = null;
  await timers[0]();
  assert.equal(loads, 2, 'reloaded once the edit ended');
});
