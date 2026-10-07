// hack/agent-template-native-parity.test.mjs — the agent template's native
// view where it reached the web's features (D190 B3/B4): the iPad and Duo
// split (and the phone stack an app of rev 1 keeps), per-conversation
// composer drafts, jump to latest on the transcript's anchors, the steering
// of a coding agent's card, the Terminals tabs and Ports, and a person's
// partition forms (share a copy, copy to my own space, host, copy in, a
// shared new chat). Rendered in node like hack/agent-template-native.test.mjs
// (the web tests' fake backend through test/native-stub.mjs). Run by
// `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { runNative } from './xbn/node.mjs';
import { FULL, PHONE, OLD } from './agent-template-native-caps.mjs';

const TPL = new URL('../builtin-templates/agent/', import.meta.url).pathname;
const NOW = Date.UTC(2026, 8, 21, 12);
const now = Math.floor(NOW / 1000);
const ME = { kind: 'user', user: 'admin', level: 'terminal', manager: true, halted: false, epochMs: 0 };

async function run(seed, steps = [], { state = null, caps = PHONE, setup = 'test/native-stub.mjs' } = {}) {
  const r = await runNative({ caps, entry: TPL + 'native.js', data: { now: NOW, self: 'apps/agent', setup: TPL + setup, seed }, steps, state });
  assert.equal(r.fatal, null);
  assert.deepEqual(r.errors, [], 'no runtime errors');
  assert.deepEqual(r.diagnostics.filter((d) => d.level !== 'info'), [], 'no diagnostics');
  r.calls = (r.extra && r.extra.calls) || [];
  return r;
}

function all(root, m, out = [], inside = !m.in) {
  if (!root) return out;
  const hit = (n, q) => (!q.t || n.t === q.t) && Object.entries(q.p || {}).every(([k, v]) => JSON.stringify((n.p || {})[k]) === JSON.stringify(v))
    && (q.has == null || JSON.stringify(n.p || {}).includes(q.has));
  if (inside && hit(root, m)) out.push(root);
  const deeper = inside || hit(root, m.in);
  for (const c of root.c || []) all(c, m, out, deeper);
  return out;
}
const find = (tree, m) => all(tree.root || tree, m)[0] || null;
const called = (r, method, re) => r.calls.filter((c) => c.method === method && re.test(c.url));
const bodyOf = (c) => (typeof c.body === 'string' ? JSON.parse(c.body) : c.body);
const topScreen = (tree) => { const nav = find(tree, { t: 'nav' }); return nav.c[nav.c.length - 1]; };
const titles = (tree) => find(tree, { t: 'nav' }).c.map((s) => s.p.title);
const msg = (id, role, content, extra = {}) => ({ id, runId: extra.runId || 1, seq: id, role, content, created: now - 600 + id, ...extra });
const menuOf = (tree) => all(topScreen(tree), { t: 'button', in: { t: 'menu', p: { icon: 'ellipsis' } } }).map((b) => b.p.label);

const twoSeed = () => ({
  me: ME,
  runs: [{ id: 1, title: 'plan the quarter', status: 'idle', activityMs: NOW - 1000 }, { id: 3, title: 'send the invoices', status: 'idle', activityMs: NOW - 2000 }],
  views: {
    1: { access: 'owner', run: { id: 1, title: 'plan the quarter', status: 'idle', rootId: 1 }, messages: [msg(1, 'user', 'plan it'), msg(2, 'assistant', 'On it.')] },
    3: { access: 'owner', run: { id: 3, title: 'send the invoices', status: 'idle', rootId: 3 }, messages: [msg(1, 'user', 'send them', { runId: 3 })] },
  },
});

// --- the split (D190 B3) -------------------------------------------------------------------------

test('the split: the list beside the open place — the new chat screen while nothing is open, a row pushes into the detail column, close and columns', async () => {
  const r = await run(twoSeed(), [
    { snapshot: 'root' },
    { tap: { t: 'row', p: { title: 'plan the quarter' } } },
    { wait: 20 },
    { snapshot: 'chat' },
    { tap: { t: 'button', p: { label: 'Files (0)' } } },
    { snapshot: 'files' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { snapshot: 'popped' },
    { event: [{ t: 'split' }, 'columns', { value: 'detail' }] },
    { snapshot: 'wide' },
    { event: [{ t: 'split' }, 'close', {}] },
    { snapshot: 'closed' },
  ], { caps: FULL });
  const sp = (snap) => find(r.snapshots[snap], { t: 'split' });
  const root = sp('root');
  assert.equal(root.p.detail, false, 'nothing open: the detail column is not pushed on a phone');
  assert.equal(root.c[0].t, 'screen');
  assert.equal(root.c[0].p.title, 'Agent', 'the list is the primary column');
  assert.deepEqual(root.c[1].c.map((s) => s.p.title), ['New chat'], 'the detail column: the new chat screen (the web\'s home pane)');
  assert.ok(find(root.c[1], { t: 'composer' }), '…with its composer');
  const chat = sp('chat');
  assert.equal(chat.p.detail, true, 'a row pushes its conversation into the detail column');
  assert.deepEqual(chat.c[1].c.map((s) => s.p.title), ['plan the quarter']);
  assert.equal(find(chat.c[0], { t: 'row', p: { title: 'plan the quarter' } }).p.selected, true, 'its row is selected in the list');
  assert.deepEqual(sp('files').c[1].c.map((s) => s.p.title), ['plan the quarter', 'Files'], 'a tool screen opens in the detail column');
  assert.deepEqual(sp('popped').c[1].c.map((s) => s.p.title), ['plan the quarter'], 'its nav\'s back pops to the conversation');
  assert.equal(sp('wide').p.columns, 'detail', 'the sidebar button\'s toggle is kept');
  assert.equal(sp('closed').p.detail, false, 'close (Back from the first detail screen): the stack is gone…');
  assert.deepEqual(sp('closed').c[1].c.map((s) => s.p.title), ['New chat'], '…and the model is home');
  assert.equal(r.messages.filter((m) => m.op === 'state').pop().state.hash, '');
});

test('the split: a deep link opens its conversation in the detail column; an app of rev 1 gets the one nav', async () => {
  const r = await run(twoSeed(), [{ wait: 20 }, { snapshot: 's' }], { caps: FULL, state: { hash: 'c=3' } });
  const sp = find(r.snapshots.s, { t: 'split' });
  assert.equal(sp.p.detail, true);
  assert.deepEqual(sp.c[1].c.map((s) => s.p.title), ['send the invoices']);
  for (const caps of [OLD, PHONE]) {
    const o = await run(twoSeed(), [{ wait: 20 }, { snapshot: 's' }], { caps, state: { hash: 'c=3' } });
    assert.equal(find(o.snapshots.s, { t: 'split' }), null, 'no split from an app without its rev 2');
    assert.deepEqual(titles(o.snapshots.s), ['Agent', 'send the invoices'], 'the list, then the stack, in one nav');
  }
});

// --- composer drafts per place --------------------------------------------------------------------

test('drafts: each conversation keeps its own, the new chat screen too; a send clears only its own', async () => {
  const comp = (snap) => find(topScreen(r.snapshots[snap]), { t: 'composer' }).p.value;
  const r = await run(twoSeed(), [
    { tap: { t: 'row', p: { title: 'plan the quarter' } } }, { wait: 20 },
    { event: [{ t: 'composer' }, 'input', { value: 'half-written for one' }] },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { tap: { t: 'row', p: { title: 'send the invoices' } } }, { wait: 20 },
    { snapshot: 'three' },
    { event: [{ t: 'composer' }, 'input', { value: 'for three' }] },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { tap: { t: 'button', p: { label: 'New chat' } } },
    { snapshot: 'new' },
    { event: [{ t: 'composer' }, 'input', { value: 'a new ask' }] },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { tap: { t: 'row', p: { title: 'plan the quarter' } } }, { wait: 20 },
    { snapshot: 'one' },
    { event: [{ t: 'composer' }, 'send', { value: 'half-written for one' }] }, { wait: 30 },
    { snapshot: 'sent' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { tap: { t: 'row', p: { title: 'send the invoices' } } }, { wait: 20 },
    { snapshot: 'threeAgain' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { tap: { t: 'button', p: { label: 'New chat' } } },
    { snapshot: 'newAgain' },
  ]);
  assert.equal(comp('three'), '', 'another conversation starts empty');
  assert.equal(comp('new'), '', 'the new chat screen has its own');
  assert.equal(comp('one'), 'half-written for one', 'back to it: its draft');
  assert.equal(comp('sent'), '', 'sent: its draft goes');
  assert.equal(comp('threeAgain'), 'for three', 'the others stay');
  assert.equal(comp('newAgain'), 'a new ask');
});

// --- jump to latest (rev 2 anchors) ---------------------------------------------------------------

const TRANSCRIPT = { t: 'transcript', p: { follow: true } };
const longSeed = () => {
  const msgs = [];
  for (let i = 1; i <= 300; i++) msgs.push(msg(i, i % 2 ? 'user' : 'assistant', `m${i}`, { runId: 9 }));
  return { me: ME, runs: [{ id: 9, title: 'long', status: 'running' }], pages: { 9: { hasOlder: false } },
    views: { 9: { access: 'owner', run: { id: 9, rootId: 9, parentId: 0, title: 'long', status: 'running' }, messages: msgs } } };
};
const live = (id, text) => ({ call: ['push', { type: 'message', run: 9, root: 9, data: msg(id, 'assistant', text, { runId: 9 }) }] });

test('jump to latest: away from the end what arrives is counted, the window grown far up is cut below its anchor, the pill reads the newest again', async () => {
  const r = await run(longSeed(), [
    { wait: 100 },
    { snapshot: 'open' },
    { event: [TRANSCRIPT, 'edge', { edge: 'end', at: false }] },
    live(301, 'm301'), { wait: 30 },
    { snapshot: 'away' },
    { event: [TRANSCRIPT, 'more', {}] },
    { snapshot: 'grew' },
    { event: [TRANSCRIPT, 'more', {}] },
    { event: [TRANSCRIPT, 'more', {}] },
    { event: [TRANSCRIPT, 'more', {}] },
    { wait: 50 },
    { snapshot: 'far' },
    { tap: { t: 'button', has: 'jump to latest', in: { t: 'composer' } } },
    { wait: 50 },
    { snapshot: 'latest' },
  ], { state: { hash: 'c=9' } });
  const tr = (snap) => find(r.snapshots[snap], TRANSCRIPT);
  const rows = (snap) => tr(snap).c.filter((n) => n.t === 'message').map((n) => +n.p.text.slice(1));
  const pill = (snap) => (find(r.snapshots[snap], { t: 'button', has: 'jump to latest', in: { t: 'composer' } }) || { p: {} }).p.label || '';
  const open = tr('open');
  assert.deepEqual([open.p.scrollTo, open.p.anchor], ['end#0', undefined], 'it opens at the end, following, no row anchored');
  assert.ok(open.e.includes('edge') && !open.e.includes('scrolled'), 'rev 2: it hears the end\'s edge');
  assert.equal(pill('open'), '', 'at the end: no pill');
  assert.equal(pill('away'), '↓ 1 new — jump to latest', 'away: what arrives is counted');
  assert.equal(rows('away').at(-1), 301, `…and drawn below: ${rows('away').slice(-3)}`);
  assert.equal(tr('grew').p.anchor, `${tr('open').c.find((n) => n.t === 'message').k.split(':').pop()}`, 'grown up: the row it grew from is anchored');
  const far = rows('far');
  assert.ok(far.at(-1) < 301, `far up, the window is cut below its anchor (ends at m${far.at(-1)})`);
  assert.ok(called(r, 'GET', /\/runs\/9\/view\?limit=50/).length >= 1);
  assert.match(pill('far'), /jump to latest$/, 'the pill while what is below was let go');
  const latest = tr('latest');
  assert.deepEqual([latest.p.scrollTo, latest.p.anchor], ['end#1', undefined], 'the pill jumps to the end and follows again');
  assert.equal(called(r, 'GET', /\/runs\/9\/view\?limit=50$/).length, 2, 'the newest page read again (once to open, once to jump)');
  assert.equal(rows('latest').at(-1), 300, '…as the backend has it (the stub never stored the live one)');
  assert.equal(pill('latest'), '');
});

test('jump to latest: an app of rev 1 never lets the end go — no anchor, no pill', async () => {
  const { CHAT1 } = await import('./agent-template-native-caps.mjs');
  const r = await run(longSeed(), [
    { wait: 100 },
    { event: [TRANSCRIPT, 'scrolled', { atBottom: false }] },
    live(301, 'm301'), { wait: 30 },
    { snapshot: 'away' },
  ], { state: { hash: 'c=9' }, caps: CHAT1 });
  const tr = find(r.snapshots.away, TRANSCRIPT);
  assert.deepEqual([tr.p.anchor, tr.p.scrollTo], [undefined, undefined]);
  assert.equal(find(r.snapshots.away, { t: 'button', has: 'jump to latest' }), null);
});

// --- a coding agent's card: steering it (chat.harnessChild.steer) -----------------------------------

test('a coding agent\'s card: Send, Send now, Stop and Cancel task (confirmed) from the card, on the child\'s run', async () => {
  const { kidsSeed } = await import(TPL + 'test/harness-fixtures.mjs');
  const CARD = { t: 'toolcard', p: { title: 'Split the router' } };
  const STEER = { t: 'approval', has: 'Steer' };
  const s = kidsSeed();
  s.routes = [['POST', '/runs/26/(message|interrupt|cancel)$', { ok: true }]];
  const r = await run(s, [
    { event: [CARD, 'toggle', { open: true }] }, { wait: 50 },
    { snapshot: 'open' },
    { event: [STEER, 'choose', { id: 'send', feedback: '' }] }, { wait: 20 },
    { snapshot: 'empty' },
    { event: [STEER, 'choose', { id: 'send', feedback: 'use chi for the router' }] }, { wait: 20 },
    { snapshot: 'sent' },
    { event: [STEER, 'choose', { id: 'send-now', feedback: 'stop and run the tests' }] }, { wait: 20 },
    { event: [STEER, 'choose', { id: 'stop', feedback: '' }] }, { wait: 20 },
    { event: [STEER, 'choose', { id: 'cancel', feedback: '' }] }, { wait: 20 },
    { snapshot: 'confirm' },
    { event: [{ t: 'approval', p: { title: 'Cancel task' } }, 'choose', { id: 'cancel-yes' }] }, { wait: 20 },
  ], { state: { hash: 'c=25' } });
  const card = find(r.snapshots.open, CARD);
  const steer = find(card, STEER);
  assert.ok(steer, 'the open card holds its steering');
  assert.equal(steer.p.title, 'Steer Claude Code');
  assert.equal(steer.p.feedback, true, 'with a message field');
  assert.deepEqual(steer.p.options.map((o) => o.label), ['Send', 'Send now', 'Stop', 'Cancel task…']);
  assert.match(steer.p.text, /^Message Claude Code directly — the agent is told\nStop Claude Code's turn/);
  assert.equal(find(r.snapshots.empty, STEER).p.text.split('\n')[0], 'Write the message first.');
  const sent = called(r, 'POST', /\/runs\/26\/message$/).map(bodyOf);
  assert.deepEqual(sent.map((b) => [b.text, !!b.interrupt]), [['use chi for the router', false], ['stop and run the tests', true]], 'straight to the child, Send now interrupting');
  assert.match(find(r.snapshots.sent, STEER).p.text, /^Sent to Claude Code — #25's agent is told\./);
  assert.equal(called(r, 'POST', /\/runs\/26\/interrupt$/).length, 1, 'Stop: its turn');
  const conf = find(r.snapshots.confirm, { t: 'approval', p: { title: 'Cancel task' } });
  assert.match(conf.p.text, /^Cancel Claude Code's task \(#26\)\? It stops for good/);
  assert.equal(called(r, 'POST', /\/runs\/26\/cancel$/).length, 1, 'Cancel task once confirmed');
});

// --- Terminals as tabs, and Ports --------------------------------------------------------------------

test('Terminals: shells as tabs of one screen — New shell, switching, Back leaves them running, Terminals (N) brings them back, Close tab ends one', async () => {
  const { harnessSeed, SBX } = await import(TPL + 'test/harness-fixtures.mjs');
  const s = harnessSeed();
  s.sbxManagers = [{ provider: SBX, title: 'Coding sandboxes', ok: true, caps: ['exec', 'files', 'tar', 'tty'], egress: ['none'], images: [], sizes: [], limits: {} }];
  s.routes = [['DELETE', '/terminals/t[a-z0-9]+$', { ended: 1 }]];
  const TABS = { t: 'tabs' };
  const r = await run(s, [
    { wait: 50 },
    { tap: { t: 'button', p: { label: 'Terminal' }, in: { t: 'menu' } } },
    { snapshot: 'one' },
    { tap: { t: 'button', p: { label: 'New shell' } } },
    { snapshot: 'two' },
    { event: [TABS, 'change', { key: '1' }] },
    { snapshot: 'first' },
    { event: [{ t: 'nav' }, 'pop', { depth: 2 }] },
    { snapshot: 'left' },
    { tap: { t: 'button', p: { label: 'Terminals (2)' } } },
    { snapshot: 'back' },
    { tap: { t: 'button', p: { label: 'Close tab' } } }, { wait: 20 },
    { snapshot: 'closed' },
  ], { state: { hash: 'c=21' } });
  const tabs = (snap) => find(topScreen(r.snapshots[snap]), TABS);
  const one = tabs('one');
  assert.equal(topScreen(r.snapshots.one).p.title, 'Terminals');
  assert.equal(one.p.style, 'bar');
  assert.deepEqual(one.c.map((t) => t.p.title), ['api-dev']);
  // only the shown tab is materialized (tabs: lazy) — the others' sockets close, their shells run on at the relay
  const shown = (snap) => all(tabs(snap), { t: 'terminal' }).map((x) => x.p.src);
  assert.equal(shown('one').length, 1);
  assert.match(shown('one')[0], /^runs\/21\/harness\/terminal\?tab=t[a-z0-9]+$/, 'its relay, with the tab\'s name');
  const two = tabs('two');
  assert.deepEqual(two.c.map((t) => t.p.title), ['api-dev', 'api-dev 2'], 'another shell beside it');
  assert.equal(two.p.selected, '2', 'the new one shown');
  assert.notEqual(shown('two')[0], shown('one')[0], 'each tab its own name');
  assert.equal(tabs('first').p.selected, '1');
  assert.deepEqual(shown('first'), shown('one'), 'switching back dials the same tab: the relay attaches to its shell again');
  assert.equal(titles(r.snapshots.left).length, 2, 'Back leaves the screen…');
  assert.ok(menuOf(r.snapshots.left).includes('Terminals (2)'), '…the shells run on: Terminals (2) in ⋯');
  assert.deepEqual(tabs('back').c.map((t) => t.p.title), ['api-dev', 'api-dev 2'], 'back to them');
  const del = called(r, 'DELETE', /\/terminals\/t[a-z0-9]+$/);
  assert.equal(del.length, 1, 'Close tab ends its shell');
  assert.ok(shown('one')[0].endsWith('tab=' + del[0].url.split('/').pop()), 'the shown one (the first, picked)');
  assert.deepEqual(tabs('closed').c.map((t) => t.p.title), ['api-dev'], 'one left');
  assert.deepEqual(shown('closed'), shown('two'), '…the other');
});

test('Ports: the conversation\'s live previews, probed, with Open; a probe of any port of its sandbox', async () => {
  const { harnessSeed } = await import(TPL + 'test/harness-fixtures.mjs');
  const s = harnessSeed();
  s.routes = [
    ['GET', '/runs/21/ports$', { previews: [{ sandbox: 'sb-7f3a', name: 'api-dev', port: 8080, path: '/', ok: true, status: 200, contentType: 'text/html', ms: 12 }] }],
    ['GET', '/runs/21/ports/sb-7f3a/3000\\?path=%2Fhealth$', { ok: false, refusal: 'not-listening', error: 'connection refused' }],
  ];
  const r = await run(s, [
    { wait: 50 },
    { tap: { t: 'button', has: 'Sandbox: api-dev', in: { t: 'menu' } } }, { wait: 20 },
    { tap: { t: 'row', p: { title: 'Ports' } } }, { wait: 30 },
    { snapshot: 'ports' },
    { event: [{ t: 'field', p: { label: 'Port' } }, 'input', { value: '3000' }] },
    { event: [{ t: 'field', p: { label: 'Path' } }, 'input', { value: '/health' }] },
    { tap: { t: 'button', p: { label: 'Probe' } } }, { wait: 30 },
    { snapshot: 'probed' },
    { tap: { t: 'row', has: 'api-dev:8080/' } },
    { snapshot: 'live' },
  ], { state: { hash: 'c=21' } });
  const scr = topScreen(r.snapshots.ports);
  assert.equal(scr.p.title, 'Ports');
  const row = find(scr, { t: 'row', has: 'api-dev:8080/' });
  assert.deepEqual([row.p.title, row.p.subtitle, row.p.tone], ['api-dev:8080/', 'HTTP 200 · text/html · 12 ms', 'ok']);
  assert.ok(find(row, { t: 'button', p: { label: 'Open' } }), 'Open beside it');
  const probed = topScreen(r.snapshots.probed);
  const said = all(find(probed, { t: 'section', p: { title: 'Probe a port' } }), { t: 'text' }).map((x) => x.p.text);
  assert.equal(said[0], 'not-listening — connection refused');
  assert.match(said[1], /^nothing listens on that port in the sandbox/);
  assert.equal(topScreen(r.snapshots.live).p.title, 'Live preview', 'a preview row opens it');
});

// --- a person's partition: the share sheet's forms, a shared new chat -------------------------------

const P = 2 ** 40 + 5;
const partSeed = () => ({
  me: { ...ME, partition: 'user:admin' }, partition: 'user:admin',
  runs: [{ id: P, title: 'my notes', status: 'idle', activityMs: NOW - 1000 }, { id: 7, title: 'team plan', status: 'idle', activityMs: NOW - 2000, owner: 'bob', access: 'participant' }],
  views: {
    [P]: { access: 'owner', run: { id: P, title: 'my notes', status: 'idle', rootId: P }, messages: [msg(1, 'user', 'notes', { runId: P })] },
    7: { access: 'participant', run: { id: 7, title: 'team plan', status: 'idle', rootId: 7, owner: 'bob' }, messages: [msg(1, 'user', 'plan', { runId: 7 })] },
  },
  routes: [
    ['POST', `/runs/${P}/publish$`, { run: { id: 7 } }],
    ['GET', '/runs/7/members$', { owner: 'bob', visibility: 'private', teamRole: 'viewer', members: [{ user: 'admin', role: 'participant' }], links: [] }],
    ['POST', '/copy$', { id: P + 1 }],
    ['POST', '/hosting$', { conversation: 2 ** 39 + 3 }],
    ['GET', '/conversations\\?scope=mine&limit=50$', { items: [{ id: P, title: 'my notes' }, { id: 7, title: 'team plan' }] }],
    ['GET', `/runs/${P}/files$`, [{ path: 'notes.md' }, { path: 'data.csv' }]],
    ['POST', '/copyin$', { files: ['notes.md'] }],
  ],
});

test('partitions: Share a copy of one\'s own conversation — who sees it, its files, the original kept — and the copy opens', async () => {
  const r = await run(partSeed(), [
    { tap: { t: 'button', p: { label: 'Share a copy…' }, in: { t: 'row', p: { title: 'my notes' } } } },
    { snapshot: 'sheet' },
    { event: [{ t: 'picker', p: { style: 'inline' } }, 'change', { value: 'people' }] },
    { tap: { t: 'button', p: { label: 'Share a copy' }, in: { t: 'toolbar' } } }, { wait: 20 },
    { snapshot: 'nobody' },
    { event: [{ t: 'field', p: { label: 'People' } }, 'input', { value: 'Bob, carol' }] },
    { event: [{ t: 'toggle', has: 'session files' }, 'change', { value: true }] },
    { tap: { t: 'button', p: { label: 'Share a copy' }, in: { t: 'toolbar' } } }, { wait: 30 },
    { snapshot: 'done' },
  ]);
  const sheet = find(r.snapshots.sheet, { t: 'sheet' });
  assert.equal(sheet.p.title, 'Share a copy of “my notes”');
  assert.match(find(sheet, { t: 'notice', p: { tone: 'info' } }).p.text, /^This conversation is in your own space, which only you can open\./);
  assert.equal(find(sheet, { t: 'picker' }).p.value, 'team-participant');
  assert.equal(find(r.snapshots.nobody, { t: 'notice', p: { tone: 'danger' } }).p.text, 'Choose who can see the copy: the team, or people.');
  const pub = called(r, 'POST', new RegExp(`/runs/${P}/publish$`)).map(bodyOf);
  assert.deepEqual(pub, [{ share: { members: [{ user: 'bob', role: 'participant' }, { user: 'carol', role: 'participant' }] }, files: true, keep: true }]);
  assert.equal(find(r.snapshots.done, { t: 'sheet' }), null, 'the sheet closes…');
  assert.deepEqual(titles(r.snapshots.done), ['Agent', 'team plan'], '…and the copy opens');
});

test('partitions: a shared conversation\'s share sheet — Copy to my own space, Use my private resources (the warning), Add a copy of my files', async () => {
  const open = [{ wait: 30 }, { tap: { t: 'button', p: { label: 'Shared' }, in: { t: 'menu' } } }, { wait: 30 }];
  const r = await run(partSeed(), [...open,
    { snapshot: 'share' },
    { tap: { t: 'button', p: { label: 'Copy to my own space' } } }, { wait: 30 },
    { snapshot: 'copied' },
  ], { state: { hash: 'c=7' } });
  const share = find(r.snapshots.share, { t: 'sheet' });
  assert.ok(find(share, { t: 'section', p: { title: 'Your own copy' } }));
  assert.ok(find(share, { t: 'button', p: { label: 'Use my private resources…' } }));
  assert.ok(find(share, { t: 'button', p: { label: 'Add a copy of my files…' } }));
  assert.deepEqual(called(r, 'POST', /\/copy$/).map(bodyOf), [{ from: 7, files: false }]);
  assert.equal(titles(r.snapshots.copied).length, 3, 'the copy opens over it');
  assert.equal(find(r.snapshots.copied, { t: 'sheet' }), null, 'the sheet closed');

  const h = await run(partSeed(), [...open,
    { tap: { t: 'button', p: { label: 'Use my private resources…' } } },
    { snapshot: 'warn' },
    { tap: { t: 'button', p: { label: 'Use my private resources' }, in: { t: 'toolbar' } } }, { wait: 30 },
  ], { state: { hash: 'c=7' } });
  const warn = find(h.snapshots.warn, { t: 'sheet' });
  assert.equal(warn.p.title, '“team plan” is not private');
  assert.match(find(warn, { t: 'notice', p: { tone: 'warn' } }).p.text, /^Letting the agent use your private sandboxes/);
  assert.deepEqual(all(find(warn, { t: 'section', p: { title: 'Who can read it' } }), { t: 'row' }).map((x) => x.p.title)[0], 'its members: bob, admin');
  assert.deepEqual(called(h, 'POST', /\/hosting$/).map(bodyOf),
    [{ conversation: 7, seen: { owner: 'bob', visibility: 'private', teamRole: 'viewer', members: { admin: 'participant' } } }]);

  const c = await run(partSeed(), [...open,
    { tap: { t: 'button', p: { label: 'Add a copy of my files…' } } }, { wait: 30 },
    { snapshot: 'pick' },
    { event: [{ t: 'picker', p: { label: 'Conversation' } }, 'change', { value: P }] }, { wait: 30 },
    { tap: { t: 'button', p: { label: 'Add the copies' } } }, { wait: 10 },
    { snapshot: 'none' },
    { event: [{ t: 'toggle', p: { label: 'notes.md' } }, 'change', { value: true }] },
    { tap: { t: 'button', p: { label: 'Add the copies' } } }, { wait: 30 },
    { snapshot: 'done' },
  ], { state: { hash: 'c=7' } });
  const pick = find(c.snapshots.pick, { t: 'picker', p: { label: 'Conversation' } });
  assert.deepEqual(pick.p.options.map((o) => o.label), ['choose one of your own conversations…', 'my notes'], 'only their own (from 2^40)');
  assert.equal(find(c.snapshots.none, { t: 'notice', p: { tone: 'danger' } }).p.text, 'Choose the files to copy.');
  assert.deepEqual(c.calls.filter((x) => x.method === 'POST' && /\/copyin$/.test(x.url)).map(bodyOf), [{ conversation: 7, files: [{ run: P, path: 'notes.md' }] }]);
  assert.equal(find(c.snapshots.done, { t: 'notice', p: { tone: 'ok' } }).p.text, 'Copied into the conversation: notes.md');
});

test('partitions: New chat with options asks who can see it; people chosen and none named stays; unpartitioned, no word of it', async () => {
  const steps = [
    { tap: { t: 'button', p: { label: 'New chat' } } },
    { tap: { t: 'button', p: { label: 'New chat with options…' } } },
    { event: [{ t: 'field', p: { kind: 'multiline', placeholder: 'what should it do?' } }, 'input', { value: 'draft the plan' }] },
    { snapshot: 'sheet' },
  ];
  const r = await run(partSeed(), [...steps,
    { event: [{ t: 'picker', p: { label: 'Who can see it' } }, 'change', { value: 'people' }] },
    { tap: { t: 'button', p: { label: 'Start' } } }, { wait: 20 },
    { snapshot: 'nobody' },
    { event: [{ t: 'field', p: { label: 'People' } }, 'input', { value: 'bob' }] },
    { tap: { t: 'button', p: { label: 'Start' } } }, { wait: 30 },
  ]);
  const who = find(r.snapshots.sheet, { t: 'picker', p: { label: 'Who can see it' } });
  assert.equal(who.p.value, 'mine');
  assert.equal(who.p.options[0].label, 'Only you — in your own space');
  assert.equal(find(r.snapshots.nobody, { t: 'section', p: { title: 'Who can see it' } }).p.footer, 'Name the people who can see it (their user ids).');
  const asks = called(r, 'POST', /\/ask$/).map(bodyOf);
  assert.equal(asks.length, 1, 'once named, it starts');
  assert.deepEqual(asks[0].share, { members: [{ user: 'bob', role: 'participant' }] });
  const u = await run({ ...twoSeed() }, steps);
  assert.equal(find(u.snapshots.sheet, { t: 'picker', p: { label: 'Who can see it' } }), null);
});
