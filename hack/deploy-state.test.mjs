// hack/deploy-state.test.mjs — unit tests for the terminal window's live
// reload view (web/deploy-state.js), run by `make js-test`: node's built-in
// runner, no dependencies. The module is pure over the deployments state
// (GET /api/xbin/deployments), so every state the window can draw is a
// fixture here — the zero state first, then paused (with and without
// changes, by a code move, by protection, after a failed deploy), attached,
// a reader's primary-only view, a write-level caller, view-as — and every
// string it shows is pinned: the chip, the offer, the menu, the launcher,
// the frame chip, the tile API select, confirmations, results, refusals and
// terminal lines.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as ds from '../web/deploy-state.js';

const {
  GLYPH, LABEL, who, ago, control, chip, offer, chipItems, toMenu, entry, defaultTarget, apiOptions,
  launcher, frameChip, confirmation, refusal, conflict, deployText, result, notice, applyEvent, viewModel,
} = ds;

import {
  T, NOW, at, opts, yes, no, PROTECTED, ISOLATE, NEEDS_TERMINAL, TODAY, terminalCaller, depCan, mainLive, mainPinned, devLive, zero, paused, attachedMain, onDev, reader, labels, byLabel, summary, CODE,
} from './deploy-fixtures.mjs';

// covers P5 PO-10 — the zero-state row: no chip, no offer, no launcher line,
// no frame chip, today's two tile API options byte for byte, and exactly one
// entry point (⇈), whose menu offers Pause live reload; an xbind without tile
// deployments (state null) draws nothing at all.
test('the zero state: one entry point, today\'s API select, nothing else', () => {
  const s = zero();
  const vm = viewModel(s, opts);
  assert.equal(vm.feature, true);
  assert.equal(vm.zero, true);
  assert.equal(vm.paused, false);
  assert.equal(vm.attached, 'main');
  assert.equal(vm.changed, null, 'no pending count');
  assert.equal(vm.chip, null, 'no chip');
  assert.equal(vm.offer, null, 'no Reload now offer');
  assert.equal(vm.launcher, null, 'no launcher banner, no card subtitles');
  assert.equal(frameChip(undefined, s), null, 'no /components summary: no frame chip');
  assert.equal(frameChip(summary({ pinned: false }), s), null);
  assert.deepEqual(vm.api.options, TODAY, 'exactly today\'s two options');
  assert.equal(vm.api.value, 'on');
  assert.deepEqual(vm.api.notes, []);
  assert.equal(apiOptions(s, { api: false }).value, 'off', 'the echo, not a guess');
  assert.deepEqual(vm.entry, { text: '⇈', title: 'deployments — live reload, deploy, promote, roll back', count: 0 });
  const shown = [vm.entry, vm.chip, vm.offer, vm.launcher, frameChip(undefined, s)].filter(Boolean);
  assert.equal(shown.length, 1, 'at most one entry point');
  assert.deepEqual(labels(vm.items), ['<header>', 'Live reload: main — every save reaches everyone using apps/crm.', 'Pause live reload']);
  const p = byLabel(vm.items, 'Pause live reload');
  assert.equal(p.enabled, true);
  assert.equal(p.op, 'pause');
  assert.equal(p.title, 'Keep main on the code it runs now; saves stop reaching it until Reload now or Resume live reload.');
  assert.equal(vm.barKey, '|0||0|0|0');

  const none = viewModel(null, opts);
  assert.equal(none.feature, false);
  assert.equal(none.entry, null, 'an older xbind: no entry point');
  assert.equal(none.chip, null);
  assert.deepEqual(none.items, []);
  assert.deepEqual(none.api.options, TODAY);
  assert.deepEqual(apiOptions(undefined).options, TODAY);

  // the zero-state answer is the same for every view
  assert.equal(chip(zero({ view: 'reader', caller: reader().caller })), null);
});

// covers P5 PO-10 — on a zero-state tile a dry run captures nothing, so the
// pause confirmation names no checkpoint id, and says so.
test('the zero state: pausing confirms from a dry run that captured nothing', () => {
  const c = confirmation('pause', { state: zero(), impact: { code: null, data: 'none', pausesLiveReload: true, stops: [], affects: 'nobody', reloads: [] } });
  assert.equal(c.title, 'Pause live reload on apps/crm?');
  assert.equal(c.message, [
    'Code: main keeps running the code it runs now, pinned to a checkpoint of the work tree taken when you confirm.',
    'Data: nothing moves.',
    'Pauses: live reload — saves stop reaching main until Reload now or Resume live reload.',
    'Affects: nobody now.',
  ].join('\n'));
  assert.doesNotMatch(c.message, /c:[0-9a-f]/, 'no checkpoint id');
  assert.equal(c.expect, undefined, 'pausing takes no expect');
});

// covers P18 — a tile that may not pause (a backend without isolation)
// keeps the control, disabled, with the state's allowed.why.
test('the zero state: a tile that can\'t pause shows why', () => {
  const s = zero({ allowed: { pause: no(ISOLATE, 'policy'), deployments: no(ISOLATE, 'policy') } });
  const p = byLabel(chipItems(s, opts), 'Pause live reload');
  assert.equal(p.enabled, false);
  assert.equal(p.hint, ISOLATE);
  assert.match(p.hint, /--isolate/);
  assert.deepEqual(control(s, 'add'), { enabled: false, why: ISOLATE, kind: 'policy' });
});

// covers P2 — live reload paused with files changed: the chip and its
// count, the offer, the menu (Reload now first, Resume on ▸ with the last
// target), the launcher's amber banner.
test('paused, 3 files changed: chip, pending count, offer, menu, launcher', () => {
  const s = paused();
  const vm = viewModel(s, opts);
  assert.equal(vm.paused, true);
  assert.equal(vm.changed, 3);
  assert.deepEqual(vm.chip, {
    text: '📌 Live reload paused · 3', compact: '📌 3', base: '📌 Live reload paused · 3', baseCompact: '📌 3',
    title: 'Live reload paused by ana 12m ago — 3 files changed since c:3f2a1c9, which main runs. Reload now ships them to main once.',
    failed: false, count: 3,
  });
  assert.deepEqual(vm.offer, { label: '⇡ Reload now · 3', title: 'Ship the work tree to main once (3 files); main stays pinned.' });
  assert.deepEqual(labels(vm.items), ['<header>', vm.chip.title, '⇡ Reload now · 3', 'Resume live reload on ▸']);
  assert.equal(vm.items[1].enabled, false, 'the sentence is a disabled item');
  const rn = byLabel(vm.items, '⇡ Reload now · 3');
  assert.equal(rn.enabled, true);
  assert.equal(rn.op, 'reloadNow');
  const rs = byLabel(vm.items, 'Resume live reload on ▸');
  assert.equal(rs.enabled, true);
  assert.deepEqual(rs.items.map((it) => [it.label, it.enabled, it.op, it.deployment]), [['main', true, 'resume', 'main']]);
  assert.equal(rs.items[0].title, 'main follows the work tree again; the 3 changed files ship now.');
  assert.equal(byLabel(vm.items, 'Pause live reload'), undefined, 'no Pause while paused');
  assert.deepEqual(vm.launcher, {
    banner: { text: 'Live reload is paused: 3 files changed since c:3f2a1c9, the checkpoint main runs.', tone: 'paused', reloadNow: true },
    subtitle: '· target: main', note: null,
  });
  assert.deepEqual(vm.api.options, TODAY, 'main-only: today\'s two options');
  assert.deepEqual(vm.entry, { text: '⇈', title: 'deployments — live reload, deploy, promote, roll back', count: 0 });
  assert.equal(vm.barKey, '|1|3|1|1|0');

  const one = paused({ workTree: { changed: 1, since: 'c:3f2a1c9' } });
  assert.match(chip(one, opts).title, /— 1 file changed since c:3f2a1c9, which main runs\./);
  assert.equal(offer(one, opts).title, 'Ship the work tree to main once (1 file); main stays pinned.');
  assert.equal(byLabel(chipItems(one, opts), 'Resume live reload on ▸').items[0].title, 'main follows the work tree again; the 1 changed file ships now.');
  assert.match(launcher(one, opts).banner.text, /^Live reload is paused: 1 file changed since/);

  const agent = paused({ liveReloadSince: { at: at(3 * 60 + 5), by: 'user:ana', agent: true } });
  assert.match(chip(agent, opts).title, /^Live reload paused by ana \(agent\) 3h ago — /);
});

// covers P2 — paused with nothing changed: no count, no offer, and Reload
// now disabled with the one reason that isn't a refusal.
test('paused, nothing changed: no count, no offer, Reload now says why', () => {
  const s = paused({ workTree: { changed: 0, since: 'c:3f2a1c9' } });
  const c = chip(s, opts);
  assert.equal(c.text, '📌 Live reload paused');
  assert.equal(c.compact, '📌');
  assert.equal(c.title, 'Live reload paused by ana 12m ago — no changes since c:3f2a1c9.');
  assert.equal(offer(s, opts), null);
  const rn = byLabel(chipItems(s, opts), '⇡ Reload now');
  assert.equal(rn.enabled, false);
  assert.equal(rn.hint, 'No changes since c:3f2a1c9.');
  assert.deepEqual(launcher(s, opts).banner, { text: 'Live reload is paused: no changes since c:3f2a1c9.', tone: 'paused', reloadNow: false });
});

// covers P2 P9 — a failed deploy: the chip adds "deploy failed" (and ! on
// the degraded bar), the tooltip names the deployment and what it keeps
// running, and Reload now retries even with nothing changed.
test('a failed deploy: the chip says so, and Reload now retries', () => {
  const s = paused({
    workTree: { changed: 0, since: 'c:3f2a1c9' },
    deployments: [mainPinned({ lastDeploy: { id: 3, how: 'reload-now', at: at(2), by: 'user:ana', result: 'failed' } })],
  });
  const c = chip(s, opts);
  assert.equal(c.failed, true);
  assert.equal(c.text, '📌 Live reload paused · deploy failed');
  assert.equal(c.compact, '📌!');
  assert.equal(c.base, '📌 Live reload paused');
  assert.equal(c.title, 'Live reload paused by ana 12m ago — no changes since c:3f2a1c9. The last deploy to main failed; main keeps running c:3f2a1c9. Reload now retries.');
  const rn = byLabel(chipItems(s, opts), '⇡ Reload now');
  assert.equal(rn.enabled, true, 'a failed last target retries');
  assert.equal(launcher(s, opts).banner.text, 'Live reload is paused: no changes since c:3f2a1c9, and the last deploy to main failed.');
  assert.equal(launcher(s, opts).banner.reloadNow, true);
  assert.equal(frameChip(summary(), s).title, 'main pinned to c:3f2a1c9 · deploy failed');
  assert.equal(viewModel(s, opts).barKey, '|1|0|1|0|1');

  // a pause whose build failed: still serving the work tree's last build
  const f = paused({ deployments: [mainPinned({ status: { state: 'failed', gen: 1, serving: 'work-tree' }, lastDeploy: { id: 1, how: 'pause', at: at(1), by: 'user:ana', result: 'failed' } })] });
  assert.match(chip(f, opts).title, /The last deploy to main failed; main keeps running its current code\. Reload now retries\.$/);
});

// covers P2 P21 — why live reload is paused: by protection (the manager
// reason on Reload now, the protected primary never offered to resume onto)
// and by a code move (the header of that move).
test('paused by protection or by a code move', () => {
  const prot = paused({
    protectedPrimary: true,
    deployments: [mainPinned({ lastDeploy: { id: 4, how: 'protect', at: at(5), by: 'user:sales1', result: 'ok' } })],
    caller: terminalCaller({ can: { pause: yes, resume: no(PROTECTED, 'state'), reloadNow: no(PROTECTED), add: yes } }),
  });
  const c = chip(prot, opts);
  assert.equal(c.text, '📌 Live reload paused', 'no count when protection paused it');
  assert.equal(c.compact, '📌');
  assert.equal(c.title, 'Live reload paused when sales1 protected main — main is pinned to c:3f2a1c9.');
  const items = chipItems(prot, opts);
  const rn = byLabel(items, '⇡ Reload now · 3');
  assert.equal(rn.enabled, false);
  assert.equal(rn.hint, PROTECTED, 'the server\'s protected reason');
  const rs = byLabel(items, 'Resume live reload on ▸');
  assert.deepEqual(rs.items, [], 'never resume onto a protected primary');
  assert.equal(rs.enabled, false);
  assert.equal(rs.hint, PROTECTED);
  assert.equal(offer(prot, opts), null);
  assert.equal(launcher(prot, opts).banner.text, 'Live reload is paused: main is protected.');

  const rb = paused({ deployments: [mainPinned({ checkpoint: { id: 'c:1e9d0aa', hash: '1e9d0aa0' }, lastDeploy: { id: 5, how: 'rollback', at: at(5), by: 'user:ana', result: 'ok' } })] });
  assert.equal(chip(rb, opts).title, 'Live reload paused — main was rolled back to c:1e9d0aa. The work tree still holds the code you rolled back from: resuming ships it again.');
  assert.equal(chip(rb, opts).text, '📌 Live reload paused · 3', 'the count stays');

  const pr = paused({ deployments: [mainPinned({ checkpoint: { id: 'c:7b19e02', hash: '7b19e020' }, lastDeploy: { id: 6, how: 'promote', from: 'dev', at: at(5), by: 'user:ana', result: 'ok' } })] });
  assert.equal(chip(pr, opts).title, 'Live reload paused — main received c:7b19e02 from dev. Resuming ships the work tree to main.');
  const dp = paused({ deployments: [mainPinned({ checkpoint: { id: 'c:7b19e02', hash: '7b19e020' }, lastDeploy: { id: 6, how: 'deploy', at: at(5), by: 'user:ana', result: 'ok' } })] });
  assert.equal(chip(dp, opts).title, 'Live reload paused — main received c:7b19e02. Resuming ships the work tree to main.');
  const now = paused({ deployments: [mainPinned({ lastDeploy: { id: 7, how: 'reload-now', at: at(1), by: 'user:bob', result: 'ok' } })] });
  assert.match(chip(now, opts).title, /^Live reload paused by ana 12m ago — /, 'a later Reload now doesn\'t change the cause');
});

// covers P2 — attached to the primary with a record: the chip names it;
// Pause is the menu's action; no banner.
test('attached to the primary, with a record', () => {
  const s = attachedMain();
  const vm = viewModel(s, opts);
  assert.equal(vm.paused, false);
  assert.equal(vm.changed, null);
  assert.equal(vm.chip.text, '● Live reload: main');
  assert.equal(vm.chip.compact, '● main');
  assert.equal(vm.chip.title, 'Live reload: main — every save reaches everyone using apps/crm.');
  assert.equal(vm.offer, null);
  assert.deepEqual(labels(vm.items), ['<header>', vm.chip.title, 'Pause live reload']);
  assert.deepEqual(vm.launcher, { banner: null, subtitle: '· target: main', note: null });
  assert.equal(frameChip(summary({ pinned: false }), s), null);
  assert.equal(vm.barKey, 'main|0||1|0|0');
});

// covers P2 P24 — attached to a non-primary deployment: the chip, the
// launcher's plain line, Attach ▸, and the tile API select's target entries
// with P24's default (the primary) and the saves/calls note.
test('attached to a non-primary deployment: chip, launcher, targets', () => {
  const s = onDev();
  const vm = viewModel(s, opts);
  assert.equal(vm.chip.text, '● Live reload: dev');
  assert.equal(vm.chip.compact, '● dev');
  assert.equal(vm.chip.title, 'Live reload: dev — saves reach apps/crm+dev. The primary, main, is pinned to c:3f2a1c9.');
  assert.deepEqual(labels(vm.items), ['<header>', vm.chip.title, 'Pause live reload', 'Attach live reload to ▸']);
  const att = byLabel(vm.items, 'Attach live reload to ▸');
  assert.deepEqual(att.items.map((it) => [it.label, it.enabled, it.op, it.deployment]), [['main', true, 'attach', 'main']]);
  assert.equal(att.items[0].title, 'main follows every save; dev is pinned to its current code.');
  assert.equal(att.hint, '', 'a usable submenu carries no reason');
  const noAttach = onDev({ deployments: [mainPinned({ can: depCan({ attach: no(NEEDS_TERMINAL('Attach live reload')) }) }), devLive()] });
  const na = byLabel(chipItems(noAttach, opts), 'Attach live reload to ▸');
  assert.equal(na.enabled, false);
  assert.equal(na.hint, NEEDS_TERMINAL('Attach live reload'), 'the per-deployment reason');
  assert.equal(byLabel(vm.items, 'Pause live reload').title, 'Keep dev on the code it runs now; saves stop reaching it until Reload now or Resume live reload.');
  assert.deepEqual(vm.launcher, {
    banner: { text: 'Live reload: dev — saves reach apps/crm+dev; main is pinned to c:3f2a1c9.', tone: 'plain', reloadNow: false },
    subtitle: '· target: main',
    note: 'Saves reach dev; new sessions call main. Switch in the tile API select after starting.',
  });
  assert.deepEqual(vm.api.options, [
    { value: 'primary', label: '🔌 target: main (primary)' }, { value: 'dev', label: '🔌 target: dev' }, { value: 'off', label: '⛔ no API' }]);
  assert.equal(vm.api.value, 'primary');
  assert.equal(vm.api.def, 'primary');
  assert.deepEqual(vm.api.notes, ['Saves reach dev; this terminal calls main.']);
  assert.deepEqual(apiOptions(s, { deployment: 'dev' }).notes, [], 'saves and calls agree');
  assert.equal(apiOptions(s, { api: false }).value, 'off');
  assert.deepEqual(vm.entry, { text: '⇈ 2', title: 'deployments — live reload, deploy, promote, roll back', count: 2 });
  assert.equal(frameChip(summary(), s).title, 'main pinned to c:3f2a1c9 · saves reach dev');
  assert.equal(vm.barKey, 'dev|0||2|0|0');

  const long = onDev({ liveReload: 'feature-login-flow', lastLiveReload: 'feature-login-flow', deployments: [mainPinned(), devLive({ name: 'feature-login-flow' })] });
  assert.equal(chip(long, opts).compact, '● feature-l…', 'the degraded bar cuts long names');
  assert.equal(chip(long, opts).text, '● Live reload: feature-login-flow');

  // a deployment the viewer may not open is not a target entry
  const hidden = onDev({ deployments: [mainPinned(), devLive({ can: depCan({ open: no('deployments of apps/crm need write access') }) })] });
  assert.deepEqual(apiOptions(hidden).options.map((o) => o.value), ['primary', 'off']);
});

// covers P21 P24 — a protected primary is never a target: the default
// falls to the live reload target, then to "API off".
test('a protected primary is never a target', () => {
  const s = onDev({ protectedPrimary: true });
  const a = apiOptions(s, { deployment: 'dev' });
  assert.deepEqual(a.options, [{ value: 'dev', label: '🔌 target: dev' }, { value: 'off', label: '⛔ no API' }]);
  assert.equal(a.def, 'dev');
  assert.deepEqual(a.notes, ['main is protected: terminals and agents can\'t call it.']);
  assert.equal(launcher(s, opts).subtitle, '· target: dev (main is protected)');
  assert.equal(launcher(s, opts).note, null, 'new sessions call where saves go');
  assert.deepEqual(chipItems(s, opts).find((it) => it.label === 'Attach live reload to ▸'), undefined, 'nothing to attach to but the protected primary');

  const both = paused({ protectedPrimary: true });
  const b = apiOptions(both);
  assert.deepEqual(b.options, [{ value: 'off', label: '⛔ no API' }], 'protected and paused: API off only');
  assert.equal(b.def, 'off');
  assert.equal(defaultTarget(both), 'off');
  assert.equal(launcher(both, opts).subtitle, '· tile API off (main is protected and live reload is paused)');
  assert.equal(defaultTarget(zero()), 'primary');
  assert.equal(defaultTarget(onDev()), 'primary');
});

// covers P5 P24 — the reader's primary-only state: the reader row, the
// menu's header and sentence only, no launcher, no frame chip, no offer,
// today's API options, nothing that names or counts another deployment.
test('a reader sees the primary only', () => {
  const s = reader();
  const vm = viewModel(s, opts);
  assert.equal(vm.reader, true);
  assert.equal(vm.paused, false, 'a reader can\'t tell paused from attached elsewhere');
  assert.equal(vm.changed, null);
  assert.equal(vm.chip.text, '📌 main pinned to c:3f2a1c9');
  assert.equal(vm.chip.compact, '📌 main');
  assert.equal(vm.chip.title, 'main is pinned to c:3f2a1c9: saves in the work tree don\'t reach it.');
  assert.equal(vm.offer, null);
  assert.deepEqual(labels(vm.items), ['<header>', vm.chip.title]);
  assert.equal(vm.launcher, null);
  assert.equal(frameChip(summary(), s), null, 'read level: no frame chip');
  assert.deepEqual(vm.api.options, TODAY);
  assert.deepEqual(vm.entry.count, 0, 'no count');
  assert.doesNotMatch(JSON.stringify(vm), /\bdev\b|\bfiles?\b/, 'no other deployment, no file count');

  assert.equal(chip(reader({ liveReload: 'main' }), opts).text, '● Live reload: main');
  const bad = reader();
  bad.deployments[0].lastDeploy.result = 'failed';
  assert.equal(chip(bad, opts).text, '📌 main pinned to c:3f2a1c9 · deploy failed');
  assert.equal(chip(bad, opts).title, 'main is pinned to c:3f2a1c9: saves in the work tree don\'t reach it. The last deploy to main failed; main keeps running c:3f2a1c9.');
});

// covers P4 — a write-level caller (a noTerminal account): every operation
// disabled with the server's reason, no offer, no Reload now in the
// launcher, no frame chip.
test('a write-level caller may operate nothing', () => {
  const s = paused({ caller: { level: 'write', manager: false, humanSession: true, readOnly: false,
    can: { pause: no(NEEDS_TERMINAL('Pause live reload')), resume: no(NEEDS_TERMINAL('Resume live reload')), reloadNow: no(NEEDS_TERMINAL('Reload now')), add: no(NEEDS_TERMINAL('Add deployment')) } } });
  const items = chipItems(s, opts);
  const rn = byLabel(items, '⇡ Reload now · 3');
  assert.equal(rn.enabled, false);
  assert.equal(rn.hint, NEEDS_TERMINAL('Reload now'));
  const rs = byLabel(items, 'Resume live reload on ▸');
  assert.equal(rs.enabled, false);
  assert.equal(rs.items[0].hint, NEEDS_TERMINAL('Resume live reload'));
  assert.equal(offer(s, opts), null);
  assert.equal(launcher(s, opts).banner.reloadNow, false);
  assert.equal(frameChip(summary(), s), null, 'write level: no frame chip');
  assert.equal(frameChip(summary(), paused({ caller: terminalCaller({ level: 'admin' }) })).text, '📌 pinned');
  assert.equal(frameChip(summary(), paused({ caller: { level: 'tile' } })), null, 'a tile credential: no chip');
  // a missing permission is a no, never a yes
  assert.deepEqual(control(paused({ caller: { level: 'terminal', can: {} } }), 'reloadNow'), { enabled: false, why: '', kind: '' });
});

// covers D64 — view-as: every action disabled; where the viewed user may,
// the reason says so; elsewhere the ordinary reason.
test('view-as renders every action disabled', () => {
  const s = paused({ caller: terminalCaller({ readOnly: true, can: { pause: yes, resume: yes, reloadNow: yes } }) });
  const items = chipItems(s, { ...opts, viewing: 'dev1' });
  const rn = byLabel(items, '⇡ Reload now · 3');
  assert.equal(rn.enabled, false);
  assert.equal(rn.hint, 'dev1 may do this — you are viewing as dev1 (read-only).');
  assert.equal(byLabel(items, 'Resume live reload on ▸').items[0].hint, 'dev1 may do this — you are viewing as dev1 (read-only).');
  assert.equal(offer(s, { ...opts, viewing: 'dev1' }), null);
  assert.equal(launcher(s, opts).banner.reloadNow, false);
  assert.equal(control(s, 'reloadNow').why, 'this user may do this — you are viewing as another user (read-only).');
  const denied = paused({ caller: { level: 'write', readOnly: true, can: { reloadNow: no(NEEDS_TERMINAL('Reload now')) } } });
  assert.equal(control(denied, 'reloadNow', null, { viewing: 'dev1' }).why, NEEDS_TERMINAL('Reload now'), 'the ordinary reason');
  const menu = toMenu(items, () => assert.fail('nothing runs in view-as'));
  assert.ok(menu.every((m) => m.kind || m.disabled));
});

// covers P2 — the frame chip: only while the primary is pinned, only for
// terminal level, "📌 pinned" at rest, the sentence on hover.
test('the frame chip', () => {
  assert.deepEqual(frameChip(summary(), paused()), { text: '📌 pinned', title: 'main pinned to c:3f2a1c9 · live reload paused' });
  assert.equal(frameChip(summary({ pinned: false }), paused()), null);
  assert.equal(frameChip(null, paused()), null);
  assert.equal(frameChip(summary(), null), null);
  assert.equal(frameChip(summary(), zero()), null);
  assert.equal(frameChip(summary(), attachedMain()), null, 'never while live reload is on the primary');
});

// covers P2 — toMenu hands <bx-menu> its items: actions only on enabled
// lines, reasons as hints, submenus converted.
test('toMenu builds bx-menu items', () => {
  const calls = [];
  const menu = toMenu(chipItems(onDev(), { ...opts, panel: true }), (op, d) => calls.push([op, d]));
  assert.deepEqual(menu.map((m) => m.kind ? `<${m.kind}>` : m.label).slice(2), ['Pause live reload', 'Attach live reload to ▸', '<sep>', '⇈ Deployments…']);
  assert.equal(menu[1].disabled, true, 'the sentence');
  assert.equal(menu[1].action, undefined);
  menu[2].action();
  menu[3].items[0].action();
  menu[5].action();
  assert.deepEqual(calls, [['pause', undefined], ['attach', 'main'], ['deployments', undefined]]);
  const off = toMenu(chipItems(paused({ workTree: { changed: 0, since: 'c:3f2a1c9' } }), opts), () => {});
  const rn = off.find((m) => m.label === '⇡ Reload now');
  assert.equal(rn.disabled, true);
  assert.equal(rn.hint, 'No changes since c:3f2a1c9.');
  assert.equal(rn.action, undefined);
  assert.equal(chipItems(paused(), opts).some((it) => it.label === LABEL.deployments), false, 'no Deployments line without the layout');
});


// covers P2 P5 — the confirmations of pause, resume, Reload now and attach,
// rendered from the dry run: exact titles, lines and verbs; the reviewed
// checkpoint Reload now then sends as expect.
test('confirmations render the dry run', () => {
  const pause = confirmation('pause', { state: attachedMain(), impact: { code: { deployment: 'main', to: 'c:7b19e02', files: 0 }, data: 'none', affects: 'everyone', reloads: ['main'] } });
  assert.equal(pause.title, 'Pause live reload on apps/crm?');
  assert.equal(pause.ok, 'Pause live reload');
  assert.equal(pause.message, [
    'Code: main keeps running the code it runs now, pinned to c:7b19e02.',
    'Data: nothing moves.',
    'Pauses: live reload — saves stop reaching main until Reload now or Resume live reload.',
    'Affects: everyone using apps/crm: frames reload once.',
  ].join('\n'));
  assert.deepEqual(pause.spec, { title: pause.title, message: pause.message, buttons: [{ label: 'Cancel', value: null }, { label: 'Pause live reload', value: 'ok', primary: true }] });
  const moved = confirmation('pause', { state: attachedMain(), impact: { code: { deployment: 'main', to: 'c:7b19e02', files: 2 }, affects: 'everyone' } });
  assert.match(moved.message, /^Code: main keeps running the code it runs now, pinned to c:7b19e02\. The work tree changed since main's last build \(2 files\): pausing live reload ships those changes to main once, now\.\n/);

  const resume = confirmation('resume', { state: paused(), impact: { code: CODE, data: 'none', affects: 'everyone', reloads: ['main'] } });
  assert.equal(resume.title, 'Resume live reload on main?');
  assert.equal(resume.ok, 'Resume live reload');
  assert.equal(resume.message, [
    'Code: main switches to the work tree now: 3 files (+40 −12) changed since main\'s c:3f2a1c9 ship at once, then every save reaches main.',
    'Data: nothing moves.',
    'Affects: everyone using apps/crm: frames reload.',
  ].join('\n'));
  assert.equal(resume.expect, undefined);
  const afterRollback = paused({ deployments: [mainPinned({ lastDeploy: { id: 5, how: 'rollback', at: at(5), by: 'user:ana', result: 'ok' } })] });
  assert.match(confirmation('resume', { state: afterRollback, impact: { code: CODE, affects: 'everyone' } }, opts).message,
    /then every save reaches main\. This includes the change rolled back 5m ago\.\n/);
  assert.match(confirmation('resume', { state: paused(), impact: { code: { ...CODE, files: 0 }, affects: 'everyone' } }).message,
    /^Code: main switches to the work tree now, then every save reaches main\.\n/);
  const onto = confirmation('resume', { state: onDev({ liveReload: '', lastLiveReload: 'dev' }), impact: { code: { ...CODE, deployment: 'dev', files: 1 }, affects: 'deployment' }, deployment: 'dev' });
  assert.equal(onto.title, 'Resume live reload on dev?');
  assert.match(onto.message, /1 file \(\+40 −12\) changed since dev's c:3f2a1c9 ships at once/);
  assert.match(onto.message, /\nAffects: people using apps\/crm\+dev: frames reload\.$/);

  const now = confirmation('reloadNow', { state: paused(), impact: { code: CODE, data: 'none', affects: 'everyone', reloads: ['main'] } });
  assert.equal(now.title, 'Reload main now?');
  assert.equal(now.ok, 'Reload now');
  assert.equal(now.expect, 'c:7b19e02', 'what the dialog showed is what ships');
  assert.equal(now.message, [
    'Code: ships the work tree to main once, as c:7b19e02 (3 files, +40 −12 against c:3f2a1c9). main stays pinned; later saves wait for the next Reload now.',
    'Data: nothing moves.',
    'Affects: everyone using apps/crm: frames reload once.',
  ].join('\n'), 'a static tile: no build, no drain');
  const backend = paused({ deployments: [mainPinned({ status: { state: 'healthy', gen: 4, serving: 'c:3f2a1c9' }, api: `/api/${T}/` })] });
  assert.equal(confirmation('reloadNow', { state: backend, impact: { code: CODE, affects: 'everyone' } }, { panel: true }).message, [
    'Code: ships the work tree to main once, as c:7b19e02 (3 files, +40 −12 against c:3f2a1c9): build, health check, swap. main stays pinned; later saves wait for the next Reload now. The Deployments panel shows the diff.',
    'Data: nothing moves.',
    'Affects: everyone using apps/crm: frames reload once; open WebSocket and SSE connections drop at the 30 s drain.',
  ].join('\n'));

  const attach = confirmation('attach', { state: attachedMain({ deployments: [mainLive(), devLive({ liveReload: false, checkpoint: { id: 'c:1e9d0aa', hash: '1e9d0aa0' } })] }),
    impact: { code: { deployment: 'dev', from: 'c:1e9d0aa', to: 'c:7b19e02', files: 3, added: 40, removed: 12 }, affects: 'deployment', reloads: ['dev'] }, deployment: 'dev' });
  assert.equal(attach.title, 'Attach live reload to dev?');
  assert.equal(attach.ok, 'Attach to dev');
  assert.equal(attach.message, [
    'Code: main is pinned to a fresh checkpoint of the work tree, c:7b19e02, and stops following saves. dev switches to the work tree now: 3 files (+40 −12) against dev\'s c:1e9d0aa ship at once, and dev follows every save.',
    'Data: nothing moves.',
    'Affects: people using apps/crm+dev: frames reload. Nobody using main sees a change.',
  ].join('\n'));

  const bare = confirmation('pause', { state: attachedMain(), impact: {} });
  assert.equal(bare.message.split('\n').length, 3, 'no affects fact, no Affects line');
  assert.throws(() => confirmation('constructor', { state: paused(), impact: {} }), /no confirmation for constructor/);
});

// covers P2 — a refused bar action shows the server's text verbatim; a 409
// says whether the record or the reviewed code moved.
test('refusals and conflicts', () => {
  assert.deepEqual(refusal('reloadNow', PROTECTED), { title: 'Reload now was refused', message: PROTECTED, ok: 'OK' });
  assert.equal(refusal('pause', 'x').title, 'Pause live reload was refused');
  assert.equal(refusal('resume', 'x').title, 'Resume live reload was refused');
  assert.equal(conflict(409, 'the deployments of apps/crm changed (seq 4); reload and retry'), 'seq');
  assert.equal(conflict(409, 'the code changed since you reviewed c:7b19e02 (now c:9d1e3b4)'), 'expect');
  assert.equal(conflict(409, 'live reload is paused: resume it onto main instead'), null);
  assert.equal(conflict(403, 'the deployments of apps/crm changed (seq 4); reload and retry'), null);
});

// covers P2 P9 — result lines, from an operation's answer or a deploy entry.
test('results', () => {
  assert.equal(result('pause', { state: paused(), deploy: { id: 2, deployment: 'main', how: 'pause', result: 'running' } }), 'Live reload paused — main is pinned to c:3f2a1c9.');
  assert.equal(result('resume', { state: zero() }), 'Live reload: main — saves reach main again.');
  assert.equal(result('resume', { state: onDev() }), 'Live reload: dev — saves reach dev again.');
  assert.equal(result('attach', { state: onDev() }, { prev: attachedMain() }), 'Live reload: dev — main is pinned to c:3f2a1c9.');
  assert.equal(result('reloadNow', { state: paused(), unchanged: true }), 'No changes since c:3f2a1c9.');
  assert.equal(result('reloadNow', { state: paused(), deploy: { deployment: 'main', how: 'reload-now', result: 'running' } }), null, 'the deploy runs: its events tell the rest');
  assert.equal(result('reloadNow', { state: paused(), deploy: { deployment: 'main', how: 'reload-now', result: 'queued' } }), 'Waiting for the deploy in progress on main…');
  assert.equal(result('reloadNow', { state: paused(), deploy: { deployment: 'main', how: 'reload-now', checkpoint: 'c:7b19e02', result: 'ok' } }), 'main now runs c:7b19e02 (Reload now).');
  assert.equal(deployText({ deployment: 'main', how: 'promote', from: 'dev', checkpoint: 'c:7b19e02', result: 'ok' }), 'main now runs c:7b19e02 (promoted from dev).');
  assert.equal(deployText({ deployment: 'main', how: 'rollback', checkpoint: 'c:1e9d0aa', result: 'ok' }), 'main now runs c:1e9d0aa (rolled back).');
  assert.equal(deployText({ deployment: 'main', how: 'deploy', previous: 'c:3f2a1c9', result: 'failed', error: 'exit status 1' }), 'Deploy to main failed — main keeps running c:3f2a1c9. exit status 1');
  assert.equal(deployText({ deployment: 'main', how: 'pause', result: 'failed' }, paused()), 'Deploy to main failed — main keeps running c:3f2a1c9.');
  assert.equal(deployText({ deployment: 'main', how: 'reload-now', result: 'cancelled' }, paused()), 'Cancelled: main was removed or apps/crm was disabled.');
  assert.equal(deployText({ deployment: 'main', how: 'resume', result: 'ok' }), null, 'resume is told by the record');
  assert.equal(deployText(null), null);
});

// covers P2 — the grey terminal lines of §9, M1's rows: paused (and the
// first record's remote), resumed, attached elsewhere, code moved, failed;
// op work-tree and the rest print nothing.
test('terminal lines', () => {
  const rec = { op: 'record', seq: 1, by: 'user:ana', session: 's1', what: ['liveReload'] };
  assert.equal(notice(zero(), paused(), rec), 'live reload paused by ana — main is pinned to c:3f2a1c9; saves no longer reach it — new terminals get the xbin-deploy remote');
  assert.equal(notice(attachedMain(), paused(), rec), 'live reload paused by ana — main is pinned to c:3f2a1c9; saves no longer reach it');
  assert.equal(notice(paused(), zero(), rec), 'live reload resumed on main by ana — saves reach main again');
  assert.equal(notice(attachedMain(), onDev(), rec), 'live reload attached to dev by ana — saves reach apps/crm+dev; main is pinned to c:3f2a1c9 — this terminal still calls main');
  assert.equal(notice(attachedMain(), onDev(), rec, { target: 'dev' }), 'live reload attached to dev by ana — saves reach apps/crm+dev; main is pinned to c:3f2a1c9');
  assert.equal(notice(attachedMain(), onDev(), rec, { target: 'off' }), 'live reload attached to dev by ana — saves reach apps/crm+dev; main is pinned to c:3f2a1c9');
  assert.equal(notice(paused(), paused(), rec), null, 'nothing moved');
  assert.equal(notice(paused(), paused(), { ...rec, what: ['edges'] }), null);
  assert.equal(notice(paused(), paused(), { op: 'work-tree', changed: 4 }), null, 'saves never print');
  assert.equal(notice(zero(), paused(), { ...rec, by: undefined }), 'live reload paused by ana — main is pinned to c:3f2a1c9; saves no longer reach it — new terminals get the xbin-deploy remote', 'by falls back to liveReloadSince');

  const dep = { op: 'deploy', id: 4, deployment: 'main', checkpoint: 'c:7b19e02', by: 'user:ana', session: 's1' };
  const next = paused({ deployments: [mainPinned({ checkpoint: { id: 'c:7b19e02', hash: '7b19e020' } })] });
  assert.equal(notice(paused(), next, { ...dep, how: 'reload-now', result: 'ok' }), 'main now runs c:7b19e02 — Reload now by ana');
  assert.equal(notice(paused(), next, { ...dep, how: 'promote', from: 'dev', result: 'ok' }), 'main now runs c:7b19e02 — promoted from dev by ana');
  assert.equal(notice(paused(), next, { ...dep, how: 'rollback', result: 'ok' }), 'main now runs c:7b19e02 — rolled back by ana');
  assert.equal(notice(paused(), paused(), { ...dep, how: 'reload-now', result: 'failed' }), 'deploy to main failed — main keeps running c:3f2a1c9; bx logs has the output');
  assert.equal(notice(paused(), paused(), { ...dep, how: 'reload-now', result: 'failed' }, { panel: true }), 'deploy to main failed — main keeps running c:3f2a1c9; the Deployments panel has the output');
  assert.equal(notice(paused(), paused(), { ...dep, how: 'reload-now', result: 'running', phase: 'build' }), null, 'phases print nothing');
  assert.equal(notice(zero(), paused(), { ...dep, how: 'pause', result: 'ok' }), null, 'the record\'s line tells a pause');
  assert.equal(notice(paused(), null, rec), null);
  assert.equal(notice(paused(), paused(), null), null);
});

// covers P2 — op work-tree moves the pending count in place (no request
// per save); record, deploy and data refetch.
test('applyEvent', () => {
  const s = paused();
  const r = applyEvent(s, { op: 'work-tree', changed: 5 });
  assert.equal(r.refetch, false);
  assert.equal(r.state.workTree.changed, 5);
  assert.equal(r.state.workTree.since, 'c:3f2a1c9');
  assert.equal(s.workTree.changed, 3, 'the input is not mutated');
  assert.equal(chip(r.state, opts).text, '📌 Live reload paused · 5');
  assert.equal(applyEvent(zero(), { op: 'work-tree', changed: 5 }).state.workTree, undefined, 'no count outside a pause');
  assert.equal(applyEvent(attachedMain(), { op: 'work-tree', changed: 5 }).state.workTree, undefined);
  assert.equal(applyEvent(s, { op: 'record', what: ['liveReload'] }).refetch, true);
  assert.equal(applyEvent(s, { op: 'deploy', result: 'ok' }).refetch, true);
  assert.equal(applyEvent(s, { op: 'data', deployment: 'dev' }).refetch, true);
  assert.equal(applyEvent(s, { op: 'reload', deployment: 'dev' }).refetch, false);
  assert.deepEqual(applyEvent(null, { op: 'record' }), { state: null, refetch: false });
});

// covers P2 — attribution and times read as the wording table spells them.
test('who and ago', () => {
  assert.equal(who('user:ana'), 'ana');
  assert.equal(who('owner'), 'the owner');
  assert.equal(who(''), '');
  assert.equal(ago(at(0.5), NOW), 'just now');
  assert.equal(ago(at(12), NOW), '12m ago');
  assert.equal(ago(at(60 * 3), NOW), '3h ago');
  assert.equal(ago(at(60 * 24 * 2 + 5), NOW), '2d ago');
  assert.equal(ago('soon', NOW), '');
  assert.deepEqual(GLYPH, { attached: '●', pinned: '📌', reloadNow: '⇡', layout: '⇈' });
});

// Every string the module can produce for the sample states.
function everyString() {
  const out = [];
  const walk = (v) => {
    if (typeof v === 'string') out.push(v);
    else if (Array.isArray(v)) v.forEach(walk);
    else if (v && typeof v === 'object') Object.values(v).forEach(walk);
  };
  const states = [zero(), paused(), paused({ workTree: { changed: 0, since: 'c:3f2a1c9' } }), attachedMain(), onDev(), onDev({ protectedPrimary: true }),
    paused({ protectedPrimary: true, deployments: [mainPinned({ lastDeploy: { how: 'protect', by: 'user:sales1', result: 'ok' } })] }),
    paused({ deployments: [mainPinned({ lastDeploy: { how: 'rollback', at: at(5), result: 'ok' } })] }),
    paused({ deployments: [mainPinned({ lastDeploy: { how: 'reload-now', result: 'failed' } })] }), reader(),
    paused({ caller: terminalCaller({ readOnly: true }) })];
  for (const s of states) {
    walk(viewModel(s, { ...opts, viewing: 'dev1', panel: true }));
    walk(frameChip(summary(), s));
    for (const op of ['pause', 'resume', 'reloadNow']) {
      walk(confirmation(op, { state: s, impact: { code: CODE, affects: 'everyone', reloads: ['main'] } }, { panel: true }));
      walk(confirmation(op, { state: s, impact: { code: null, affects: 'nobody' } }));
      walk(refusal(op, ''));
    }
    walk(result('pause', { state: s }));
    walk(result('resume', { state: s }));
    walk(notice(attachedMain(), s, { op: 'record', by: 'user:ana', what: ['liveReload'] }));
  }
  walk(confirmation('attach', { state: onDev(), impact: { code: CODE, affects: 'deployment', reloads: ['main'] }, deployment: 'main' }));
  for (const how of ['deploy', 'promote', 'rollback', 'reload-now']) {
    for (const res of ['ok', 'failed', 'queued', 'cancelled']) walk(deployText({ deployment: 'main', how, from: 'dev', checkpoint: 'c:7b19e02', result: res }, paused()));
    walk(notice(paused(), paused(), { op: 'deploy', deployment: 'main', how, from: 'dev', checkpoint: 'c:7b19e02', result: 'ok', by: 'user:ana' }));
  }
  walk(notice(paused(), paused(), { op: 'deploy', deployment: 'main', how: 'deploy', result: 'failed' }, { panel: true }));
  walk(notice(paused(), paused(), { op: 'deploy', deployment: 'main', how: 'deploy', result: 'failed' }));
  walk(LABEL);
  return out.filter((s) => !/^(user:|c:|\/c\/|apps\/)/.test(s) && !['ok', 'reload-now', 'rollback', 'promote', 'deploy', 'pause', 'resume', 'reloadNow', 'attach', 'deployments', 'primary', 'off', 'on', 'paused', 'plain', 'state', 'authority', 'policy', 'static', 'healthy', 'failed', 'work-tree', 'main', 'dev', 'header', 'sep'].includes(s));
}

// covers P2 — the glossary's spellings: no word the feature must not use,
// "live" only in "live reload", "pause" only about live reload, none of
// the glyphs other features own.
test('strings use the glossary\'s words', () => {
  const strings = everyString();
  assert.ok(strings.length > 80, `sampled ${strings.length} strings`);
  const banned = /\b(identity|principal|instances?|environments?|env|stag(e|ing)|slots?|versions?|revisions?|releases?|snapshots?|generations?|layers?|previews?|channels?|lanes?|freeze|frozen|source|copy|fork|clone|publish|prod|production|origins?|automations?)\b/i;
  for (const s of strings) {
    assert.doesNotMatch(s, banned, `a word the feature must not use: ${JSON.stringify(s)}`);
    assert.doesNotMatch(s, /[⏸⟲⟳🔒▶▣⧉]/u, `a glyph another feature owns: ${JSON.stringify(s)}`);
    for (const m of s.matchAll(/\blive\b/gi)) {
      assert.match(s.slice(m.index, m.index + 11), /^live reload$/i, `"live" as a noun: ${JSON.stringify(s)}`);
    }
    for (const m of s.matchAll(/\bpaus\w*/gi)) {
      const around = s.slice(Math.max(0, m.index - 16), m.index + m[0].length + 16);
      assert.match(around, /live reload/i, `an unqualified pause: ${JSON.stringify(s)}`);
    }
    assert.doesNotMatch(s, /pause the tile/i);
  }
  // "pinned" says to what, except the frame chip at rest (📌 pinned) and
  // "stays pinned" in the terminal window
  for (const s of strings) {
    if (s === '📌 pinned') continue;
    for (const m of s.matchAll(/\bpinned\b/g)) {
      const rest = s.slice(m.index + 6);
      const before = s.slice(Math.max(0, m.index - 6), m.index);
      assert.ok(/^ to /.test(rest) || before === 'stays ', `a bare "pinned": ${JSON.stringify(s)}`);
    }
  }
});

// The module's exports are the contract the terminal window builds on:
// adding one is fine, renaming or dropping one breaks web/frame-deploy.js.
test('the exports', () => {
  assert.deepEqual(Object.keys(ds).sort(), [
    'GLYPH', 'LABEL', 'PANEL_OPS', 'REASON', 'ago', 'apiOptions', 'apiTitle', 'applyEvent', 'chip', 'chipItems', 'confirmation',
    'conflict', 'control', 'defaultTarget', 'deployText', 'deploymentFrame', 'entry', 'frameChip', 'keepTargets', 'launcher',
    'noTarget', 'notice', 'offer', 'refusal', 'result', 'sessionTarget', 'shared', 'targetChange', 'toMenu', 'viewModel', 'who',
  ]);
  assert.equal(entry(null), null);
});
