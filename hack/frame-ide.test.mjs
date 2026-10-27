// hack/frame-ide.test.mjs — the tile window's IDE layout (web/frame-ide.js,
// D202), run by `make js-test`: tabs open in the focused pane (or show
// where they are), close, split, move by a drop (the middle moves, an edge
// splits), panes close into their neighbour, dividers clamp, the layout
// follows the sessions, the rects cover the area, and the window pref round
// trips — sessions by server id, never a Deployments tab or a session still
// starting — read from an older frame's fields and written for them.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { emptyIde, leaves, paneOf, allTabs, openTab, activate, focusPane, closeTab, split, drop, closePane, setRatio,
  setSide, setDock, sync, rects, toPref, fromPref, fromLegacy, legacyOf, isSession } from '../web/frame-ide.js';

const shape = (ide) => leaves(ide.tree).map((id) => `${id}${ide.focus === id ? '*' : ''}[${ide.panes[id].tabs.map((t) => (t === ide.panes[id].active ? `(${t})` : t)).join(' ')}]`).join(' ');
const two = () => sync(emptyIde(), ['s:a', 's:b']);

test('the layout follows the sessions: new ones open in the focused pane, gone ones lose their tab', () => {
  let ide = two();
  assert.equal(shape(ide), 'p1*[(s:a) s:b]', 'the first session becomes active, the second appends');
  assert.equal(shape(sync(emptyIde(), ['s:a'])), 'p1*[(s:a)]');
  ide = openTab(ide, 'f:main.go');
  ide = sync(ide, ['s:b']);
  assert.equal(shape(ide), 'p1*[(f:main.go) s:b]');
  assert.equal(sync(ide, ['s:b']), ide, 'nothing changed: the same layout');
  ide = sync(ide, ['s:b'], (t) => t !== 'f:main.go');
  assert.equal(shape(ide), 'p1*[(s:b)]', 'a tab keep refuses goes');
  assert.ok(isSession('s:x') && !isSession('f:s:x'));
});

test('a tab opens after the active one in the focused pane, or shows where it is', () => {
  let ide = activate(two(), 'p1', 's:a');
  ide = openTab(ide, 'f:a.go');
  assert.equal(shape(ide), 'p1*[s:a (f:a.go) s:b]');
  ide = split(ide, 'p1', 'row');
  assert.equal(shape(ide), 'p1[s:a (s:b)] p2*[(f:a.go)]', 'split moves the active tab into the new pane, which gets the focus');
  ide = focusPane(ide, 'p1');
  ide = openTab(ide, 'f:a.go');
  assert.equal(shape(ide), 'p1[s:a (s:b)] p2*[(f:a.go)]', 'already open: shown in its pane, which gets the focus');
  ide = openTab(focusPane(ide, 'p1'), 'pr:3');
  assert.equal(shape(ide), 'p1*[s:a s:b (pr:3)] p2[(f:a.go)]');
  assert.equal(paneOf(ide, 'pr:3'), 'p1');
  assert.equal(paneOf(ide, 'nope'), null);
});

test('closing tabs: the next one shows, an emptied pane goes, the last pane stays', () => {
  let ide = split(openTab(two(), 'f:x'), 'p1', 'col');
  assert.equal(shape(ide), 'p1[s:a (s:b)] p2*[(f:x)]');
  ide = closeTab(ide, 'f:x');
  assert.equal(shape(ide), 'p1*[s:a (s:b)]', 'p2 emptied: gone, the focus back on p1');
  ide = closeTab(closeTab(ide, 's:b'), 's:a');
  assert.equal(shape(ide), 'p1*[]', 'the last pane stays, empty');
  assert.equal(closeTab(ide, 'nope'), ide);
});

test('split: one tab or none leaves the new pane empty; the tree records the direction', () => {
  let ide = sync(emptyIde(), ['s:a']);
  ide = split(ide, 'p1', 'col');
  assert.equal(shape(ide), 'p1[(s:a)] p2*[]');
  assert.equal(ide.tree.dir, 'col');
  ide = split(ide, 'p2', 'row');
  assert.equal(shape(ide), 'p1[(s:a)] p2[] p3*[]');
  assert.equal(split(ide, 'p9', 'row'), ide, 'no such pane');
});

test('a drop: the middle moves the tab, an edge splits there; a pane\'s only tab on its own edge stays', () => {
  let ide = openTab(two(), 'f:x');
  ide = drop(ide, 's:a', 'p1', 'r');
  assert.equal(shape(ide), 'p1[(f:x) s:b] p2*[(s:a)]');
  ide = drop(ide, 'f:x', 'p2', 'c');
  assert.equal(shape(ide), 'p1[(s:b)] p2*[s:a (f:x)]');
  assert.equal(drop(ide, 's:b', 'p1', 'b'), ide, 'p1\'s only tab on its own edge');
  ide = drop(ide, 's:b', 'p2', 'b');
  assert.equal(shape(ide), 'p2[s:a (f:x)] p3*[(s:b)]', 'p1 emptied by the move: gone');
  assert.equal(ide.tree.dir, 'col');
  assert.deepEqual(drop(ide, 'f:x', 'p2', 'c'), activate(ide, 'p2', 'f:x'), 'the middle of its own pane: just shown');
});

test('closing a pane moves its tabs to the neighbour that takes its place', () => {
  let ide = split(split(openTab(two(), 'f:x'), 'p1', 'row'), 'p2', 'col');
  assert.equal(shape(ide), 'p1[s:a (s:b)] p2[(f:x)] p3*[]');
  ide = openTab(ide, 'w:y');
  ide = closePane(ide, 'p2');
  assert.equal(shape(ide), 'p1[s:a (s:b)] p3*[(w:y) f:x]');
  assert.equal(closePane(sync(emptyIde(), ['s:a']), 'p1').tree.pane, 'p1', 'the last pane can\'t close');
});

test('dividers clamp to 10–90 %; the rects cover the area', () => {
  let ide = split(split(openTab(two(), 'f:x'), 'p1', 'row'), 'p2', 'col');
  ide = setRatio(ide, '', 0.02);
  assert.equal(ide.tree.r, 0.1);
  ide = setRatio(ide, 'b', 0.7);
  assert.equal(ide.tree.b.r, 0.7);
  ide = setRatio(ide, '', 'nonsense');
  assert.equal(ide.tree.r, 0.5);
  const { panes, dividers } = rects(ide.tree);
  assert.deepEqual(panes.map((p) => p.id), ['p1', 'p2', 'p3']);
  const area = panes.reduce((s, p) => s + p.w * p.h, 0);
  assert.ok(Math.abs(area - 1) < 1e-9, `the panes cover the area (${area})`);
  assert.deepEqual(dividers.map((d) => [d.path, d.dir]), [['b', 'col'], ['', 'row']]);
  assert.ok(Math.abs(dividers[0].y - 0.7) < 1e-9);
});

test('the side panel and the dock', () => {
  let ide = setSide(emptyIde(), 'files');
  assert.equal(ide.side, 'files');
  assert.equal(setSide(ide, 'bogus').side, null);
  ide = setDock(ide, true, 20);
  assert.deepEqual(ide.dock, { open: true, h: 80 }, 'clamped');
  assert.equal(setDock(ide, true, 80), ide);
});

test('the pref: sessions by server id; no Deployments tab, no session still starting; garbage reads as nothing', () => {
  let ide = openTab(openTab(sync(emptyIde(), ['s:a', 's:b', 's:new']), 'deploy'), 'f:x.go');
  ide = drop(ide, 'f:x.go', 'p1', 'r');
  ide = setDock(setSide(ide, 'prs'), true, 240);
  const ids = { 's:a': 'S1', 's:b': 'S2' }; // s:new has no id yet
  const p = toPref(ide, (r) => ids[r] ?? null);
  assert.deepEqual(p.panes, { p1: { tabs: ['s#S1', 's#S2'], active: 's#S2' }, p2: { tabs: ['f:x.go'], active: 'f:x.go' } });
  assert.equal(p.side, 'prs');
  const back = fromPref(JSON.parse(JSON.stringify(p)), (id) => ({ S1: 'a', S2: 'b' }[id] ?? null));
  assert.equal(shape(back), 'p1[s:a (s:b)] p2*[(f:x.go)]');
  assert.deepEqual(back.dock, { open: true, h: 240 });
  // a session that ended meanwhile: its tab is gone; a pane left empty goes
  const gone = fromPref(p, (id) => (id === 'S1' ? 'a' : null));
  assert.equal(shape(gone), 'p1[(s:a)] p2*[(f:x.go)]');
  // what a pref may not hold is dropped, not trusted
  const evil = fromPref({ v: 1, tree: { dir: 'row', r: 9, a: { pane: 'p1' }, b: { pane: 'zz' } }, panes: { p1: { tabs: ['deploy', 'f:ok', 'x:bad', 7, 'f:ok'], active: 'deploy' } }, focus: 'p7', side: 'evil', dock: { open: 'yes', h: 1e9 } }, () => null);
  assert.equal(shape(evil), 'p1*[(f:ok)]');
  assert.equal(evil.side, null);
  assert.deepEqual(evil.dock, { open: true, h: 1200 });
  for (const bad of [null, {}, { v: 2 }, { v: 1, tree: 'x' }, { v: 1, tree: { pane: '../p1' } }]) assert.equal(fromPref(bad, () => null), null, JSON.stringify(bad));
  // a deep or wide tree is cut
  let deep = { pane: 'p0' };
  for (let i = 1; i < 40; i++) deep = { dir: 'row', r: 0.5, a: deep, b: { pane: `p${i}` } };
  const cut = fromPref({ v: 1, tree: deep, panes: {} }, () => null);
  assert.ok(cut && leaves(cut.tree).length <= 12, `bounded (${cut && leaves(cut.tree).length})`);
});

test('an older frame\'s fields: read into the layout, and written from it', () => {
  assert.equal(fromLegacy({ layout: 'split', paneW: 40 }).side, 'files');
  assert.equal(fromLegacy({ layout: 'code', beside: true }).side, 'files');
  assert.equal(fromLegacy({ layout: 'prs' }).side, 'prs');
  assert.equal(fromLegacy({ layout: 'logs' }).dock.open, true);
  assert.deepEqual(fromLegacy({ layout: 'deployments' }), emptyIde());
  assert.deepEqual(fromLegacy(null), emptyIde());
  assert.deepEqual(legacyOf(emptyIde()), { layout: 'term', beside: false, paneW: 55 });
  assert.deepEqual(legacyOf(setSide(emptyIde(), 'files')), { layout: 'code', beside: true, paneW: 55 });
  assert.deepEqual(legacyOf(setDock(emptyIde(), true)), { layout: 'logs', beside: true, paneW: 55 });
  assert.deepEqual(allTabs(two()), ['s:a', 's:b']);
});
