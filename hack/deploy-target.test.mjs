// hack/deploy-target.test.mjs — unit tests for the terminal window's session
// targets and deployment frames (web/deploy-state.js), run by `make js-test`:
// node's built-in runner, no dependencies. What the tile API select shows
// and restarts (a session's target, P24), how a session listing keeps each
// tab's echoed target, and what a frame of a deployment
// (<bx-frame src="<tile>+<name>">) does for a `deployments` event. The
// states come from hack/deploy-fixtures.mjs.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  apiOptions, apiTitle, sessionTarget, targetChange, noTarget, keepTargets, deploymentFrame, confirmation,
} from '../web/deploy-state.js';
import { T, TODAY, depCan, no, mainPinned, devLive, zero, paused, onDev } from './deploy-fixtures.mjs';

const RESTARTS = 'switching restarts the terminal';

// covers P24 PO-10 — a tab's target is its session's echo, never what it
// asked for: no API, a named deployment, or the primary it follows.
test('a tab\'s target is its echo', () => {
  assert.equal(sessionTarget(null), null);
  assert.equal(sessionTarget({ id: 's1' }), 'primary', 'no deployment echoed: the session follows the primary');
  assert.equal(sessionTarget({ api: true, deployment: '' }), 'primary');
  assert.equal(sessionTarget({ api: true, deployment: 'dev' }), 'dev');
  assert.equal(sessionTarget({ api: false, deployment: 'dev' }), 'off', 'no API wins');
});

// covers P24 PO-10 — the select marks the echoed target, and a named target
// the list lacks (removed, or no longer the viewer's to open) is still shown
// rather than guessed; the zero state keeps today's two options.
test('the tile API select shows the echo', () => {
  const s = onDev();
  assert.deepEqual(apiOptions(s, { api: true, deployment: 'dev' }).value, 'dev');
  const gone = apiOptions(s, { api: true, deployment: 'old' });
  assert.deepEqual(gone.options.map((o) => o.value), ['primary', 'dev', 'old', 'off'], 'the removed target, before "no API"');
  assert.equal(gone.options[2].label, '🔌 target: old');
  assert.equal(gone.value, 'old');
  const hidden = onDev({ deployments: [mainPinned(), devLive({ can: depCan({ open: no('deployments of apps/crm need write access') }) })] });
  assert.deepEqual(apiOptions(hidden, { deployment: 'dev' }).options.map((o) => o.value), ['primary', 'dev', 'off']);
  assert.deepEqual(apiOptions(hidden).options.map((o) => o.value), ['primary', 'off'], 'a new tab lists only what the viewer may reach');
  assert.deepEqual(apiOptions(onDev({ protectedPrimary: true }), {}).options.map((o) => o.value), ['dev', 'off'],
    'a session still following a protected primary is restarted by the server: never listed');
  assert.deepEqual(apiOptions(zero(), { api: true, deployment: '' }).options, TODAY);
  assert.deepEqual(apiOptions(paused(), { api: false }).options, TODAY, 'main alone, unprotected: today\'s two');
});

// covers P24 — the select's tooltip says what the entry decides and what
// "off" stops, then the notes: a protected primary, saves and calls apart.
test('the tile API select\'s tooltip', () => {
  const a = apiOptions(onDev(), { api: true, deployment: '' });
  assert.equal(apiTitle(a, 'shell', RESTARTS), [
    `the tile API this shell calls, bx included — off = the shell can read and edit code, but every API call, bx included, is unauthorized (${RESTARTS})`,
    'Saves reach dev; this terminal calls main.'].join('\n'));
  const p = apiOptions(onDev({ protectedPrimary: true }), { api: true, deployment: 'dev' });
  assert.equal(apiTitle(p, 'agent', 'switching restarts the agent (its conversation resumes)'), [
    'the tile API this agent calls, bx included — off = the agent can read and edit code, but every API call, bx included, is unauthorized (switching restarts the agent (its conversation resumes))',
    'main is protected: terminals and agents can\'t call it.'].join('\n'));
});

// covers P24 PO-10 — choosing an entry restarts the session onto it: the
// primary's entry requests no deployment (the session follows the
// primary), a deployment's names it, "off" drops the API; the confirmation
// is §5.2 row 18; today's two entries restart exactly as before.
test('switching the target', () => {
  const s = onDev();
  const onPrimary = { key: 'k', id: 's1', api: true, deployment: '' };
  assert.equal(targetChange(s, onPrimary, 'primary'), null, 'already there: nothing restarts');
  assert.equal(targetChange(s, null, 'dev'), null);
  assert.equal(targetChange(s, onPrimary, ''), null);

  const toDev = targetChange(s, onPrimary, 'dev');
  assert.deepEqual(toDev.patch, { api: true, deployment: 'dev' });
  assert.equal(toDev.what, 'calling dev');
  const row = confirmation('target', { state: s, deployment: 'dev' });
  assert.equal(row.title, `Restart this terminal ${toDev.what}?`, 'the frame\'s confirm title is row 18\'s');
  assert.equal(toDev.message, 'Its shell and anything running in it end, and the scrollback is lost. Its API calls and bx commands then reach apps/crm+dev.');

  const back = targetChange(s, { ...onPrimary, deployment: 'dev' }, 'primary');
  assert.deepEqual(back.patch, { api: true, deployment: '' }, 'the primary\'s entry requests no deployment');
  assert.equal(back.what, 'calling main');
  assert.equal(back.message, 'Its shell and anything running in it end, and the scrollback is lost. Its API calls and bx commands then reach the primary.');

  const off = targetChange(s, { ...onPrimary, deployment: 'dev' }, 'off');
  assert.deepEqual(off, { patch: { api: false, deployment: '' }, what: 'without API access' });
  assert.equal(confirmation('target', { state: s, deployment: 'off' }).title, `Restart this terminal ${off.what}?`);
  assert.equal(targetChange(s, { api: false }, 'off'), null);

  // today's select: 'on' and 'off', the same restarts as before tile deployments
  assert.deepEqual(targetChange(zero(), { api: false }, 'on'), { patch: { api: true, deployment: '' }, what: 'with tile API access' });
  assert.deepEqual(targetChange(zero(), { api: true }, 'off'), { patch: { api: false, deployment: '' }, what: 'without API access' });
  assert.equal(targetChange(zero(), { api: true }, 'on'), null);
  assert.equal(targetChange(null, { api: true }, 'on'), null, 'an xbind without tile deployments');
});

// covers P24 PO-10 — a listing (tabsFrom) carries no target: a shell keeps
// what its own session frame echoed, since a changed attribute would restart
// it; an agent and a tab first seen take the directory's row; a spawning tab
// keeps its own; a shell ended for want of an echo stays until dismissed.
test('a listing keeps each tab\'s target', () => {
  const prev = [
    { key: 'a', id: 's1', kind: 'shell', deployment: 'dev' },
    { key: 'b', id: 's2', kind: 'agent', deployment: 'dev' },
    { key: 'c', id: null, kind: 'shell', deployment: 'dev' },
    { key: 'd', id: 's4', kind: 'shell', ended: true, refused: true, deployment: '' },
    { key: 'e', id: 's5', kind: 'agent' },
  ];
  const rows = [{ id: 's1', deployment: '' }, { id: 's2', deployment: '' }, { id: 's5', deployment: 'dev' }, { id: 's6', deployment: 'dev' }];
  const listed = [
    { key: 'a', id: 's1', kind: 'shell' }, // tabsFrom rebuilt it, without the target
    { key: 'b', id: 's2', kind: 'agent' },
    { key: 'e', id: 's5', kind: 'agent' },
    { key: 'f', id: 's6', kind: 'shell' }, // first seen
    prev[2], // still spawning: tabsFrom keeps the same object
  ];
  const out = keepTargets(listed, prev, rows);
  assert.deepEqual(out.map((t) => [t.key, t.deployment]), [['a', 'dev'], ['b', ''], ['e', 'dev'], ['f', 'dev'], ['c', 'dev'], ['d', '']]);
  assert.equal(out[4], prev[2], 'an unchanged tab is the same object');
  assert.ok(out[5].refused && out[5].ended, 'the ended shell is kept, with its line');
  assert.deepEqual(keepTargets([{ key: 'a', id: 's1', kind: 'shell' }], [], []).map((t) => t.deployment), [undefined], 'nothing known: nothing set');
  assert.deepEqual(keepTargets([], prev, rows).map((t) => t.key), ['d'], 'only the ended shell outlives its row');
  const relisted = keepTargets([{ key: 'd', id: 's4', kind: 'shell', net: 'org' }], prev, [{ id: 's4' }]);
  assert.deepEqual(relisted.map((t) => [t.key, !!t.ended, !!t.refused]), [['d', true, true]], 'still listed (its end not yet in): still ended');
  // a shell whose echo is being checked: a listing fetched before its
  // session opened would drop it (tabsFrom), a later one rebuilds it
  const checking = [{ key: 'g', id: 's7', kind: 'shell', deployment: '', refusing: true }];
  assert.deepEqual(keepTargets([], checking, []).map((t) => [t.key, t.refusing]), [['g', true]], 'kept while it is checked');
  assert.deepEqual(keepTargets([{ key: 'g', id: 's7', kind: 'shell' }], checking, [{ id: 's7' }]).map((t) => [t.key, t.refusing, t.deployment]), [['g', true, '']]);
  assert.deepEqual(keepTargets([], [{ ...checking[0], refusing: false }], []), [], 'checked and fine: a vanished shell goes, as today');
  assert.match(noTarget('dev'), /^this xbind can't target deployments: the session that asked for dev was ended/);
});

// covers C2 P24 PO-10 — a frame of a deployment hears its deployment's
// reloads and builds on the tile's `deployments` events, matched exactly: a
// frame of the primary or of an ancestor tile never reacts, and today's
// event types never speak of a non-primary deployment.
test('a frame of a deployment', () => {
  const ev = (data, component = T) => ({ type: 'deployments', component, data });
  assert.deepEqual(deploymentFrame(`${T}+dev`, ev({ op: 'reload', deployment: 'dev' })), { reload: true });
  assert.deepEqual(deploymentFrame(`${T}+dev`, ev({ op: 'build', deployment: 'dev', phase: 'error', text: 'main.go:3: undefined: x' })), { error: 'main.go:3: undefined: x' });
  assert.deepEqual(deploymentFrame(`${T}+dev`, ev({ op: 'build', deployment: 'dev', phase: 'error' })), { error: 'the logs tab has the output' },
    'no compiler output for this viewer: the overlay points at the logs');
  assert.deepEqual(deploymentFrame(`${T}+dev`, ev({ op: 'build', deployment: 'dev', phase: 'ok' })), { error: null });
  assert.equal(deploymentFrame(`${T}+dev`, ev({ op: 'build', deployment: 'dev', phase: 'start' })), null);
  assert.equal(deploymentFrame(`${T}+dev`, ev({ op: 'reload', deployment: 'qa' })), null, 'another deployment');
  assert.equal(deploymentFrame(`${T}+dev`, ev({ op: 'deploy', deployment: 'dev', result: 'ok' })), null, 'a deploy reloads through op reload');
  assert.equal(deploymentFrame(T, ev({ op: 'reload', deployment: 'dev' })), null, 'the primary\'s frame');
  assert.equal(deploymentFrame('apps', ev({ op: 'reload', deployment: 'dev' })), null, 'an ancestor tile\'s frame');
  assert.equal(deploymentFrame('apps/shop+dev', ev({ op: 'reload', deployment: 'dev' }, 'apps/shop/admin')), null, 'a nested tile\'s deployment');
  assert.equal(deploymentFrame(`${T}+dev`, { type: 'reload', component: `${T}+dev` }), null, 'today\'s types never name a deployment');
  assert.equal(deploymentFrame(`${T}+dev`, ev({ op: 'work-tree', changed: 2 })), null);
  assert.equal(deploymentFrame(`${T}+dev`, null), null);
});

// covers P2 — the new strings use the glossary's words.
test('target strings use the glossary\'s words', () => {
  const s = onDev();
  const strings = [
    apiTitle(apiOptions(s, {}), 'shell', RESTARTS), noTarget('dev'), deploymentFrame(`${T}+dev`, { type: 'deployments', component: T, data: { op: 'build', deployment: 'dev', phase: 'error' } }).error,
    targetChange(s, { api: true }, 'dev').message, targetChange(s, { api: true }, 'dev').what,
  ];
  const banned = /\b(identity|principal|instances?|environments?|env|stag(e|ing)|slots?|versions?|revisions?|releases?|snapshots?|generations?|layers?|previews?|channels?|lanes?|freeze|frozen|source|copy|fork|clone|publish|prod|production|origins?|automations?)\b/i;
  for (const x of strings) assert.doesNotMatch(x, banned, `a word the feature must not use: ${JSON.stringify(x)}`);
});
