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
