// hack/partition-mode.test.mjs — unit tests for the shell's partitioned-tile
// words and decisions (workspace-template/shell/partition-mode.js), run by
// `make js-test`: what a /components row's `partition` reads as, the
// marker's tooltip, the pending card's text, the switch's words (in step
// with xbind's registry.SwitchDeletes), the POST /partitions/mode bodies,
// the typed confirmation's spec and answers, what the card says after a
// decision, how long a card's decision state lives, the partition chip a
// window's head carries, and the consent prompts (what they show, when the
// shell reads them, the calls). A row without `partition` (an older xbind,
// a tile that never asked) reads as nothing at all.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { partitionView, markTitle, MARK_TITLE, modeName, switchDeletes, switchLabel, pendingText, requestKey,
  modeBody, postMode, errorText, bytesText, wipedText, switchSpec, switchResolve, DELETES_ALL,
  pruneDecisions, whoDecides, noteText, staleRefusal, deletesNothing, keptText, switchedText, DEP_SHARED,
  partitionChip, CHIP_YOURS, CHIP_GLOBAL, CHIP_NONE_ROOT, framedTile, consentPerson, consentWatch, consentPrompts, keepDismissed,
  consentEventOp, consentCall, consentKey, consentAsk, consentWhy, allowedText, declinedText, consentStoreKey, coverPoints,
  CONSENTS_API, PARTITIONS_PAGE } from '../workspace-template/shell/partition-mode.js';

const row = (partition) => ({ path: 'apps/p', partition });

test('rows without partition read as nothing: no marker, no overlay', () => {
  for (const c of [undefined, null, {}, { path: 'apps/a' }, { path: 'apps/a', partition: null }, { path: 'apps/a', partition: 'user' }]) {
    assert.equal(partitionView(c), null);
    assert.equal(markTitle(partitionView(c)), '');
  }
});

test('the marker follows the recorded mode (user partitions), not the request', () => {
  const user = partitionView(row({ state: 'partitioned', user: true, global: false }));
  assert.equal(markTitle(user), MARK_TITLE);
  assert.equal(MARK_TITLE, 'Partitioned: each person here has their own data'); // 06 §12.2's words
  const both = partitionView(row({ state: 'partitioned', user: true, global: true }));
  assert.ok(markTitle(both).startsWith(MARK_TITLE + '; ') && /global instance/.test(markTitle(both)));
  // pending into user partitions: R is unpartitioned, so no marker yet
  const into = partitionView(row({ state: 'pending', user: false, global: false, request: { user: true, global: false, declined: false } }));
  assert.equal(markTitle(into), '');
  assert.equal(into.pending, true);
  // pending out of them: R still has user partitions, so the marker stays
  const out = partitionView(row({ state: 'pending', user: true, global: false, request: { user: false, global: false, declined: false } }));
  assert.equal(markTitle(out), MARK_TITLE);
  assert.deepEqual([out.from, out.to], [{ user: true, global: false }, { user: false, global: false }]);
  // declined: runs R, nothing pending
  const dec = partitionView(row({ state: 'partitioned', user: true, global: false, request: { user: false, global: false, declined: true } }));
  assert.equal(dec.pending, false);
  assert.equal(dec.declined, true);
  // a pending state without a request (never sent, but) isn't an overlay
  assert.equal(partitionView(row({ state: 'pending', user: true })).pending, false);
});

test('modes and what a switch deletes match xbind\'s words (H1)', () => {
  const U = { user: true, global: false }, UG = { user: true, global: true }, N = { user: false, global: false };
  assert.deepEqual([modeName(N), modeName(U), modeName(UG), modeName({ global: true }), modeName(null)],
    ['unpartitioned', 'user', 'user + global', 'global', 'unpartitioned']);
  assert.equal(switchDeletes(N, U), DELETES_ALL);
  assert.equal(switchDeletes(U, N), DELETES_ALL);
  assert.equal(switchDeletes(UG, U), 'the global instance\'s data and the tile\'s shared resources (people\'s partitions stay)');
  assert.equal(switchDeletes(U, UG), 'nothing (the global instance starts empty)');
  assert.equal(switchLabel(N, U), 'Switch and delete all data');
  assert.equal(switchLabel(U, UG), 'Switch');
  assert.notEqual(requestKey(partitionView(row({ state: 'pending', user: false, request: { user: true } }))),
    requestKey(partitionView(row({ state: 'pending', user: false, request: { user: true, global: true } }))));
});

test('the pending card says the alert\'s words, else the row\'s', () => {
  const v = partitionView(row({ state: 'pending', user: false, global: false, request: { user: true, global: false } }));
  const alerts = [{ kind: 'disk', tile: 'apps/p', message: 'disk (unpartitioned → user)' },
    { kind: 'partition-switch', tile: 'apps/p', message: 'xbind says so (unpartitioned → user)' }];
  assert.equal(pendingText('apps/p', v, alerts), 'xbind says so (unpartitioned → user)');
  const own = pendingText('apps/p', v, [{ kind: 'partition-switch', tile: 'apps/q', message: 'another tile' }]);
  assert.match(own, /^A partition mode switch is requested for apps\/p \(unpartitioned → user\): switching deletes all data in this tile\./);
  assert.equal(pendingText('apps/p', v, undefined), own);
});

test('decision bodies carry the request as the row showed it', () => {
  const v = partitionView(row({ state: 'pending', user: true, global: true, request: { user: false, global: false } }));
  assert.deepEqual(modeBody('apps/p', 'keep', v), { tile: 'apps/p', act: 'keep', from: { user: true, global: true }, to: null });
  assert.deepEqual(modeBody('apps/p', 'switch', v, { dryRun: true }),
    { tile: 'apps/p', act: 'switch', from: { user: true, global: true }, to: null, dryRun: true });
});

test('postMode posts JSON as the person and reads refusals', async () => {
  const calls = [];
  const ok = await postMode(async (u, i) => { calls.push([u, i]); return { ok: true, status: 200, json: async () => ({ ok: true }) }; }, { tile: 'apps/p' });
  assert.deepEqual(ok, { ok: true, status: 200, body: { ok: true } });
  assert.equal(calls[0][0], '/api/xbin/partitions/mode');
  assert.equal(calls[0][1].method, 'POST');
  assert.equal(calls[0][1].body, '{"tile":"apps/p"}');
  const no = await postMode(async () => ({ ok: false, status: 409, json: async () => ({ error: 'look again' }) }), {});
  assert.equal(errorText(no), 'look again');
  const bad = await postMode(async () => ({ ok: false, status: 502, json: async () => { throw new Error('html'); } }), {});
  assert.equal(errorText(bad), 'failed (502)');
  const off = await postMode(async () => { throw new Error('offline'); }, {});
  assert.equal(off.status, 0);
  assert.match(errorText(off), /can't be reached \(offline\)/);
});

test('the typed confirmation shows the dry run and asks for the path', () => {
  assert.equal(bytesText(0), '0 bytes');
  assert.equal(bytesText(1), '1 byte');
  assert.equal(bytesText(1536), '1.5 KB');
  assert.equal(bytesText(5 * 1024 * 1024), '5.0 MB');
  assert.match(wipedText({ namespaces: 1, partitions: 2, vaultKeys: 1, registrations: 0, bytes: 2048, subkeys: 3 }),
    /^1 data namespace, 2 people's partitions, 1 vault key, 0 registrations \(.*\), 2\.0 KB; 3 backup keys erased/);
  const v = partitionView(row({ state: 'pending', user: true, global: false, request: { user: false, global: false } }));
  const dry = { deletes: DELETES_ALL, wiped: { namespaces: 1, vaultKeys: 1 }, keeps: ['the code'], people: 2 };
  const s = switchSpec('apps/p', v, dry);
  assert.match(s.message, /^Switching apps\/p from user to unpartitioned deletes all data in this tile\./);
  assert.match(s.message, /It deletes: 1 data namespace, 0 people's partitions, 1 vault key/);
  assert.match(s.message, /2 people whose partition is deleted will be told\./);
  assert.match(s.message, /It keeps:\n• the code/);
  assert.deepEqual(s.fields, [{ name: 'confirm', label: 'Type apps/p to confirm', placeholder: 'apps/p', value: '' }]);
  assert.deepEqual(s.buttons.map((b) => [b.value, !!b.danger]), [[null, false], ['switch', true]]);
  assert.equal(s.buttons[1].label, 'Switch and delete all data');
  assert.equal(s.error, undefined);
  // sandbox managers that lack "partitions" (C12): named, and a Switch anyway box
  const m = switchSpec('apps/p', v, { ...dry, managers: ['sbx/a'] }, { typed: 'apps/p', error: 'nope' });
  assert.match(m.message, /sbx\/a/);
  assert.equal(m.error, 'nope');
  assert.deepEqual(m.fields.map((f) => [f.name, f.value]), [['confirm', 'apps/p'], ['yes', false]]);
});

test('the confirmation\'s answers: cancel, again, or the switch body', () => {
  assert.deepEqual(switchResolve('apps/p', { button: null, values: { confirm: 'apps/p' } }), { cancel: true });
  const wrong = switchResolve('apps/p', { button: 'switch', values: { confirm: 'apps/q' } });
  assert.match(wrong.again, /Type the tile's path exactly \(apps\/p\)/);
  assert.equal(wrong.typed, 'apps/q');
  assert.deepEqual(switchResolve('apps/p', { button: 'switch', values: { confirm: ' apps/p ' } }).extra, { confirm: 'apps/p' });
  const noYes = switchResolve('apps/p', { button: 'switch', values: { confirm: 'apps/p', yes: false } }, { managers: ['sbx/a'] });
  assert.match(noYes.again, /Switch anyway/);
  assert.deepEqual(switchResolve('apps/p', { button: 'switch', values: { confirm: 'apps/p', yes: true } }, { managers: ['sbx/a'] }).extra,
    { confirm: 'apps/p', yes: true });
});

test('a card\'s decision state lives only while its request is pending', () => {
  const pend = (global) => ({ path: 'apps/p', partition: { state: 'pending', user: false, global: false, request: { user: true, global: !!global } } });
  const key = requestKey(partitionView(pend()));
  const part = { 'apps/p': { key, done: 'Kept unpartitioned: …' }, 'apps/q': { key, err: 'x' } };
  // still pending, the same request: kept, and the same object (no re-render)
  const rows = { 'apps/p': pend(), 'apps/q': pend() };
  assert.equal(pruneDecisions(part, (p) => rows[p]), part);
  // kept (declined), withdrawn (no partition), gone, or another request: dropped
  const kept = { path: 'apps/p', partition: { state: 'unpartitioned', user: false, global: false, request: { user: true, declined: true } } };
  for (const r of [kept, { path: 'apps/p' }, undefined, pend(true)]) {
    const out = pruneDecisions(part, (p) => (p === 'apps/p' ? r : rows[p]));
    assert.deepEqual(Object.keys(out), ['apps/q']);
    assert.notEqual(out, part);
  }
  // keep, withdraw, then the same R → Q again: the reopened request starts over
  let st = pruneDecisions(part, (p) => (p === 'apps/p' ? kept : rows[p]));
  st = pruneDecisions(st, (p) => (p === 'apps/p' ? { path: 'apps/p' } : rows[p]));
  st = pruneDecisions(st, (p) => rows[p]);
  assert.equal(st['apps/p'], undefined);
  assert.equal(pruneDecisions(undefined, () => null), undefined);
});

test('who decides, the tile\'s note, and a deployment\'s window', () => {
  assert.equal(whoDecides('org:devs'), 'an admin of org:devs, which owns it, or a workspace admin');
  assert.equal(whoDecides('user:dev1'), 'its owner, user:dev1, or a workspace admin');
  assert.equal(whoDecides(''), 'a workspace admin');
  assert.equal(whoDecides(undefined), 'a workspace admin');
  const v = partitionView(row({ state: 'pending', user: false, request: { user: true }, note: '  Your notes live here.  ' }));
  assert.equal(v.note, 'Your notes live here.');
  assert.equal(noteText('apps/p', v), 'apps/p says: Your notes live here.');
  assert.equal(noteText('apps/p', partitionView(row({ state: 'pending', request: { user: true }, note: 7 }))), '');
  // a deployment of a partitioned tile is its writers' shared instance
  assert.match(DEP_SHARED, /^Not partitioned: .*shares.*not each person's own partition$/);
});

test('the card takes the alert of this request, without the CLI hint', () => {
  const v = partitionView(row({ state: 'pending', user: false, global: false, request: { user: true, global: false } }));
  const msg = 'A partition mode switch is requested for apps/p (unpartitioned → user): switching deletes all data in this tile. '
    + 'Until a manager of apps/p switches or keeps the current mode (bx partition switch|keep apps/p), it doesn\'t run.';
  assert.equal(pendingText('apps/p', v, [{ kind: 'partition-switch', tile: 'apps/p', message: msg }]),
    'A partition mode switch is requested for apps/p (unpartitioned → user): switching deletes all data in this tile. '
    + 'Until a manager of apps/p switches or keeps the current mode, it doesn\'t run.');
  // xbind's hint names the partitions page too: the whole parenthesis goes
  const paged = msg.replace('(bx partition switch|keep apps/p)', '(bx partition switch|keep apps/p, or on /xbin/partitions)');
  assert.equal(pendingText('apps/p', v, [{ kind: 'partition-switch', tile: 'apps/p', message: paged }]),
    pendingText('apps/p', v, [{ kind: 'partition-switch', tile: 'apps/p', message: msg }]));
  // an alert of an older request (alerts and rows load apart): the row's words
  const old = msg.replace('(unpartitioned → user)', '(unpartitioned → user + global)');
  assert.equal(pendingText('apps/p', v, [{ kind: 'partition-switch', tile: 'apps/p', message: old }]), pendingText('apps/p', v, []));
});

test('a switch that deletes nothing says so; the answers after a decision', () => {
  const U = { user: true, global: false }, UG = { user: true, global: true };
  assert.equal(deletesNothing(U, UG), true);
  assert.equal(deletesNothing(UG, U), false);
  assert.equal(deletesNothing({}, U), false);
  const v = partitionView(row({ state: 'pending', user: true, global: false, request: { user: true, global: true }, note: 'shared lists' }));
  const s = switchSpec('apps/p', v, { deletes: 'nothing (the global instance starts empty)', wiped: {}, keeps: ['the code'] });
  assert.doesNotMatch(s.message, /It deletes:|can't be undone/);
  assert.match(s.message, /apps\/p says: shared lists/);
  assert.match(s.message, /Nothing is deleted: the global instance starts empty\.$/);
  assert.deepEqual(s.buttons.map((b) => [b.label, !!b.danger]), [['Cancel', false], ['Switch', false]]);
  assert.equal(switchedText('apps/p', v, { deletes: 'nothing (the global instance starts empty)' }),
    'Switched apps/p to user + global: nothing was deleted (the global instance starts empty).');
  const all = partitionView(row({ state: 'pending', user: true, global: false, request: { user: false, global: false } }));
  assert.equal(switchedText('apps/p', all, { deletes: DELETES_ALL }), 'Switched apps/p to unpartitioned: all data in this tile deleted.');
  assert.equal(switchedText('apps/p', all, { deletes: DELETES_ALL, eraseError: 'key file busy', archiver: 'gc runs tonight' }),
    'Switched apps/p to unpartitioned: all data in this tile deleted.\n'
    + 'The backup keys are erased, but not every key file is removed yet: key file busy\nArchiver: gc runs tonight');
  assert.equal(keptText('apps/p', all), 'Kept user: apps/p runs again, and nothing was deleted.');
  // a refusal the dialog can't fix closes it; the managers' 409 and a 500 re-open it
  assert.equal(staleRefusal({ status: 409, body: { error: 'changed', partition: { state: 'pending' } } }), true);
  assert.equal(staleRefusal({ status: 409, body: { error: 'already running' } }), true);
  assert.equal(staleRefusal({ status: 409, body: { error: 'managers', managers: ['sbx/a'] } }), false);
  assert.equal(staleRefusal({ status: 500, body: { error: 'part-way' } }), false);
});

test('the partition chip says whose partition a window shows (I3)', () => {
  const u = partitionView(row({ state: 'partitioned', user: true, global: false }));
  const ug = partitionView(row({ state: 'partitioned', user: true, global: true }));
  const person = { kind: 'user', id: 'alice' }, root = { kind: 'root', id: 'root' };
  const viewAs = { kind: 'user', id: 'alice', impersonatedBy: 'owner', readOnly: true };
  // not partitioned (recorded mode), or an older xbind's row: no chip, whoever looks
  for (const v of [null, partitionView(row({ state: 'unpartitioned', user: false, global: true })),
    partitionView(row({ state: 'pending', user: false, global: false, request: { user: true, global: false } }))]) {
    for (const who of [person, root, viewAs, null]) assert.equal(partitionChip(v, { who }), null);
  }
  assert.deepEqual(partitionChip(u, { who: person }), { kind: 'yours', text: 'yours', title: CHIP_YOURS });
  assert.deepEqual(partitionChip(ug, { who: person }), { kind: 'yours', text: 'yours', title: CHIP_YOURS });
  // a pending switch out of partitions: still the person's own partition (R)
  const out = partitionView(row({ state: 'pending', user: true, global: false, request: { user: false, global: false } }));
  assert.equal(partitionChip(out, { who: person }).kind, 'yours');
  // a deployment's one instance: shared, in F14's words
  for (const who of [person, root, null]) assert.deepEqual(partitionChip(ug, { shown: 'dev', who }), { kind: 'shared', text: 'shared', title: DEP_SHARED });
  // the workspace token: the global instance, or none without one
  assert.deepEqual(partitionChip(ug, { who: root }), { kind: 'global', text: 'global', title: CHIP_GLOBAL });
  assert.deepEqual(partitionChip(u, { who: root }), { kind: 'none', text: 'no partition', title: CHIP_NONE_ROOT });
  // view-as never opens a person's partition — on a deployment neither
  for (const shown of ['', 'dev']) {
    const c = partitionChip(ug, { shown, who: viewAs });
    assert.equal(c.kind, 'none');
    assert.match(c.title, /^No partition: viewing the workspace as alice never opens their partition/);
  }
  // who unknown (whoami not read yet, or an element): no guess on the primary
  assert.equal(partitionChip(ug, { who: null }), null);
  assert.equal(partitionChip(ug, { who: { kind: 'element' } }), null);
  assert.match(CHIP_YOURS, /^Your partition: /);
  assert.match(CHIP_GLOBAL, /^The global instance: /);
});

test('a pop-out window is a window of the tile it frames', () => {
  const comps = [{ path: 'apps/x', partition: { state: 'partitioned', user: true } }, { path: 'apps/x/y' }, { path: 'apps/xy' }, { path: 'apps/z' }];
  const at = (src) => { const f = framedTile(src, comps); return [f.row?.path ?? null, f.shown]; };
  assert.deepEqual(at('apps/x'), ['apps/x', '']); // spec.src: the tile itself
  assert.deepEqual(at('apps/x/compose'), ['apps/x', '']); // a sub-path: the tile's own page
  assert.deepEqual(at('apps/x/y'), ['apps/x/y', '']); // a listed tile under it: that one (the longest)
  assert.deepEqual(at('apps/x/y/z'), ['apps/x/y', '']);
  assert.deepEqual(at('apps/xy/a'), ['apps/xy', '']); // not a prefix on a segment boundary
  assert.deepEqual(at('apps/x+dev'), ['apps/x', 'dev']); // a deployment
  assert.deepEqual(at('apps/x+dev/compose'), ['apps/x', 'dev']);
  assert.deepEqual(at('apps/q'), [null, '']);
  assert.deepEqual(at(''), [null, '']);
  assert.deepEqual(framedTile(undefined, null), { row: null, shown: '' });
  // its chip is the card's: the person's own partition on a sub-path, shared on a deployment
  const person = { kind: 'user', id: 'alice' };
  const chip = (src) => { const f = framedTile(src, comps); return partitionChip(partitionView(f.row), { shown: f.shown, who: person })?.kind ?? null; };
  assert.deepEqual(['apps/x/compose', 'apps/x+dev', 'apps/x/y', 'apps/q'].map(chip), ['yours', 'shared', null, null]);
});

test('the shell reads consents only for a person who sees a partitioned tile', () => {
  const comps = [{ path: 'apps/a' }, { path: 'apps/p', partition: { state: 'partitioned', user: true, global: false } }];
  assert.equal(consentWatch(comps, { kind: 'user', id: 'alice' }), true);
  assert.equal(consentWatch([{ path: 'apps/a' }], { kind: 'user', id: 'alice' }), false);
  assert.equal(consentWatch([{ path: 'apps/g', partition: { state: 'partitioned', user: false, global: true } }], { kind: 'user' }), false);
  assert.equal(consentWatch(comps, { kind: 'root', id: 'root' }), false); // the workspace token has no consents (PersonOnly)
  assert.equal(consentWatch(comps, { kind: 'user', id: 'alice', impersonatedBy: 'owner' }), false); // view-as
  assert.equal(consentWatch(comps, null), false);
  assert.equal(consentWatch(null, { kind: 'user', id: 'alice' }), false);
  assert.equal(consentPerson({ kind: 'user', id: 'alice' }), true);
  for (const who of [null, { kind: 'root', id: 'root' }, { kind: 'user', id: 'alice', impersonatedBy: 'owner' }, { kind: 'user' }, { kind: 'element', id: 'x' }]) {
    assert.equal(consentPerson(who), false);
  }
});

test('dismissals are kept per person in this browser', () => {
  assert.equal(consentStoreKey({ kind: 'user', id: 'alice' }), 'xbin-partition-consent-dismissed:alice');
  assert.notEqual(consentStoreKey({ kind: 'user', id: 'bob' }), consentStoreKey({ kind: 'user', id: 'alice' }));
  for (const who of [null, { kind: 'root', id: 'root' }, { kind: 'user', id: 'alice', impersonatedBy: 'owner' }]) assert.equal(consentStoreKey(who), '');
});

test('an ask is hit-tested edge to edge before Allow counts', () => {
  const pts = coverPoints({ left: 10, top: 20, right: 70, bottom: 40 }, 24);
  const xs = [...new Set(pts.map((p) => p[0]))], ys = [...new Set(pts.map((p) => p[1]))];
  assert.deepEqual(xs, [11, 35, 59, 69]);
  assert.deepEqual(ys, [21, 39]);
  assert.equal(pts.length, 8);
  // any window at least a step wide and tall overlapping the ask hits a point
  const hit = (w) => pts.some(([x, y]) => x >= w.left && x < w.right && y >= w.top && y < w.bottom);
  for (const w of [{ left: 36, top: 0, right: 60, bottom: 30 }, { left: 0, top: 38, right: 12, bottom: 400 }, { left: 60, top: 22, right: 300, bottom: 46 }]) {
    assert.equal(hit(w), true, JSON.stringify(w));
  }
  assert.deepEqual(coverPoints({ left: 0, top: 0, right: 1, bottom: 1 }), [[1, 1]]);
});

test('consent prompts: the asks, only with the policy on', () => {
  const asked = [{ from: 'apps/z', to: 'apps/x', at: '2026-09-30T10:00:00Z' }, { from: 'apps/w', to: 'apps/x', at: '2026-09-30T11:00:00Z' }];
  const on = { policy: { partitionConsent: true }, consents: [], asked };
  // policy off (or an older xbind, or not read): nothing, even with asks listed
  for (const v of [null, undefined, {}, { policy: { partitionConsent: false }, asked }, { asked }]) assert.deepEqual(consentPrompts(v), []);
  assert.deepEqual(consentPrompts(on), [
    { key: 'apps/z→apps/x', from: 'apps/z', to: 'apps/x', at: '2026-09-30T10:00:00Z' },
    { key: 'apps/w→apps/x', from: 'apps/w', to: 'apps/x', at: '2026-09-30T11:00:00Z' }]);
  assert.equal(consentKey(asked[0]), 'apps/z→apps/x');
  // dismissed in this browser: that very ask stays hidden; a later ask (another day) shows again
  assert.deepEqual(consentPrompts(on, { dismissed: { 'apps/z→apps/x': '2026-09-30T10:00:00Z' } }).map((a) => a.from), ['apps/w']);
  assert.deepEqual(consentPrompts(on, { dismissed: { 'apps/z→apps/x': '2026-09-29T09:00:00Z' } }).map((a) => a.from), ['apps/z', 'apps/w']);
  // a tile that went (the ask outlives it for a day): not shown when the shell knows the tiles
  assert.deepEqual(consentPrompts(on, { paths: new Set(['apps/z', 'apps/x']) }).map((a) => a.from), ['apps/z']);
  // malformed and repeated rows
  const odd = { policy: { partitionConsent: true }, asked: [null, { from: 'apps/z' }, { from: 1, to: 'apps/x' }, { from: '', to: 'apps/x' }, asked[0], asked[0]] };
  assert.deepEqual(consentPrompts(odd).map((a) => a.key), ['apps/z→apps/x']);
  assert.deepEqual(consentPrompts({ policy: { partitionConsent: true }, asked: 'nope' }), []);
});

test('dismissals are kept only while their ask is listed', () => {
  const view = { policy: { partitionConsent: true }, asked: [{ from: 'apps/z', to: 'apps/x', at: 't1' }] };
  const d = { 'apps/z→apps/x': 't1' };
  assert.equal(keepDismissed(d, view), d);
  assert.deepEqual(keepDismissed({ ...d, 'apps/w→apps/x': 't0' }, view), d);
  assert.deepEqual(keepDismissed({ 'apps/z→apps/x': 't0' }, view), {}); // an older ask of the same edge
  assert.deepEqual(keepDismissed(d, { asked: [] }), {});
  const empty = {};
  assert.equal(keepDismissed(empty, view), empty);
});

test('consent events: consent-needed and consent, nothing else', () => {
  assert.equal(consentEventOp({ type: 'partitions', component: 'apps/x', partition: 'user:alice', data: { op: 'consent-needed', from: 'apps/z', to: 'apps/x' } }), 'consent-needed');
  assert.equal(consentEventOp({ type: 'partitions', data: { op: 'consent', from: 'apps/z', to: 'apps/x', allowed: true } }), 'consent');
  for (const e of [null, {}, { type: 'partitions' }, { type: 'partitions', data: { op: 'mode' } }, { type: 'partitions', data: { op: 'notice' } },
    { type: 'policies' }, { type: 'grants', data: { op: 'consent' } }]) assert.equal(consentEventOp(e), '');
});

test('consent calls are the person\'s own: GET the view, POST allows, DELETE takes back', async () => {
  const calls = [];
  const f = async (u, i) => { calls.push([u, i]); return { ok: true, status: 200, json: async () => ({ policy: { partitionConsent: true }, asked: [] }) }; };
  const g = await consentCall(f);
  assert.deepEqual(g, { ok: true, status: 200, body: { policy: { partitionConsent: true }, asked: [] } });
  assert.deepEqual(calls[0], ['/api/xbin/partitions/consents', { method: 'GET' }]);
  assert.equal(CONSENTS_API, '/api/xbin/partitions/consents');
  await consentCall(f, 'POST', { from: 'apps/z', to: 'apps/x', at: 't1', key: 'apps/z→apps/x' });
  assert.equal(calls[1][1].method, 'POST');
  assert.equal(calls[1][1].body, '{"from":"apps/z","to":"apps/x"}');
  assert.equal(calls[1][1].headers['Content-Type'], 'application/json');
  await consentCall(f, 'DELETE', { from: 'apps/z', to: 'apps/x' });
  assert.equal(calls[2][1].method, 'DELETE');
  const no = await consentCall(async () => ({ ok: false, status: 409, json: async () => ({ error: 'the workspace policy partitionConsent is off' }) }), 'POST', { from: 'a', to: 'b' });
  assert.equal(no.ok, false);
  assert.equal(errorText(no), 'the workspace policy partitionConsent is off');
  const old = await consentCall(async () => ({ ok: false, status: 404, json: async () => { throw new Error('html'); } }));
  assert.deepEqual(old, { ok: false, status: 404, body: {} });
  assert.equal((await consentCall(async () => { throw new Error('offline'); })).status, 0);
});

test('the consent prompt\'s words name both tiles and what answering does', () => {
  assert.equal(consentAsk('apps/z', 'apps/x'), 'apps/z asks to use your data in apps/x');
  assert.match(consentWhy('apps/z', 'apps/x'), /^Your workspace asks you first\. Allowing lets apps\/z's code — and everyone who can change it — reach your apps\/x data/);
  assert.equal(PARTITIONS_PAGE, '/xbin/partitions'); // xbind serves the person's page (F11): the words name it
  assert.equal(allowedText('apps/z', 'apps/x'),
    'Allowed: apps/z can use your apps/x data from its next call. Take it back on your partitions page (/xbin/partitions).');
  assert.match(declinedText('apps/z', 'apps/x'), /^Not allowed: apps\/z's calls into your apps\/x data stay refused\./);
});
