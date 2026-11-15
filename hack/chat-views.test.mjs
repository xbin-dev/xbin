// hack/chat-views.test.mjs — the chat kit's views (web/chat/views.js,
// cards.js, asks.js, composer.js, window.js, css.js; D224), as far as node
// reaches without a DOM, run by `make js-test`:
//   - the contracts a host relies on: every class the views write starts
//     with `bxc-` and has its rule in the one stylesheet (or is a named
//     hook), the stylesheet styles nothing outside its prefix, a view
//     imports by /vendor/ URL only, and no view reaches into a host;
//   - the Agent tab's fold said in the kit's block model (agent-fold.js
//     blockOf / transcriptOf): stable objects per version, a memo that
//     outlives the mapping, a call that waits on its request;
//   - ChatWindow's bookkeeping (window.js) against a stand-in scroller;
//   - the pure helpers the views draw from (usageBadge, cached, stageFiles);
//   - the old modules' names (agent-cards.js, agent-css.js).
// What the views draw is driven in a browser: hack/ui-harness agentTab
// (the Agent tab, and the same functions in a document with a plain
// ChatCtx), agentReview, agentLong.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { Fold, blockOf, transcriptOf, cached as foldCached } from '../web/agent-fold.js';
import * as model from '../web/chat/blocks.js';
import { stageFiles, fmtSize, ATTACH_LIMITS } from '../web/chat/ask.js';
import { KIT } from '../web/chat/rev.js';
import { installHooks } from './xbn/hooks.mjs';

installHooks(); // /vendor/<name> as xbind serves it
const { chatCssText } = await import('../web/chat/css.js');
const { ChatWindow } = await import('../web/chat/window.js');

const src = (f) => readFileSync(new URL(`../web/${f}`, import.meta.url), 'utf8');
const VIEWS = ['chat/views.js', 'chat/cards.js', 'chat/asks.js', 'chat/composer.js'];
const code = (f) => src(f).replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, ''); // without comments

// ---- contracts ----------------------------------------------------------------------------

test('the views write only `bxc-` classes, and each has its rule in the stylesheet (or is a named hook)', () => {
  // hooks: classes a host or a test selects by, that need no rule of their own
  const HOOKS = new Set(['bxc-row', 'bxc-send', 'bxc-lbl', 'bxc-exec', 'bxc-agent', 'bxc-draft', 'bxc-other', 'bxc-deny', 'bxc-hint', 'bxc-steps', 'bxc-in']);
  const used = new Set();
  for (const f of VIEWS) {
    const text = code(f);
    for (const m of text.matchAll(/class="([^"]*)"/g)) {
      for (const tok of m[1].replace(/\$\{[^}]*\}/g, ' ').split(/\s+/).filter(Boolean)) {
        assert.ok(tok.startsWith('bxc-') || tok === 'hljs' || tok === 'mk', `${f}: class "${tok}" is not the kit's (in class="${m[1]}")`);
        if (tok.startsWith('bxc-')) used.add(tok);
      }
    }
    for (const m of text.matchAll(/'(bxc-[a-z-]*[a-z])'/g)) used.add(m[1]); // classes chosen in an expression
    used.delete('bxc-'); // (a prefix an expression completes: bxc-${status})
  }
  assert.ok(used.size > 80, `${used.size} classes`);
  const missing = [...used].filter((c) => !HOOKS.has(c) && !new RegExp(`\\.${c}(?![a-z-])`).test(chatCssText));
  assert.deepEqual(missing, [], 'classes the views write that css.js has no rule for');
  for (const h of HOOKS) assert.ok(used.has(h), `${h} is listed as a hook but no view writes it`);
});

test('the stylesheet styles nothing outside its prefix: every rule starts at a bxc- class or a kit element', () => {
  const css = chatCssText.replace(/\/\*[\s\S]*?\*\//g, '');
  let n = 0;
  for (const m of css.matchAll(/(^|\})\s*([^{}]+)\{/g)) {
    for (const sel of m[2].split(',').map((x) => x.trim()).filter(Boolean)) {
      n++;
      assert.match(sel, /^(?:[a-z]+)?\.bxc-|^bx-chat-(?:form|composer)\b/, `selector "${sel}" would reach a host's own markup`);
    }
  }
  assert.ok(n > 200, `${n} selectors`);
  assert.ok(!/:host|(?:^|[\s,}])(?:body|html)\b|\*\s*\{/.test(css), 'no :host, body or universal rule: it sits in a shadow root and in a document alike');
  // every colour comes from the theme, with Night's value beside it (make theme-check holds the values)
  assert.equal(css.replace(/var\(--bx-[a-z0-9-]+,[^()]*(?:\([^()]*\)[^()]*)*\)/g, '').match(/#[0-9a-fA-F]{3,8}\b/g), null, 'a colour outside a var(--bx-…, fallback)');
});

test('a view imports by /vendor/ URL only, and reaches no host', () => {
  for (const f of [...VIEWS, 'chat/window.js']) {
    const text = code(f);
    const specs = [...text.matchAll(/^import\s[^'"]*['"]([^'"]+)['"]/gm), ...text.matchAll(/\bimport\(\s*['"]([^'"]+)['"]\s*\)/g)].map((m) => m[1]);
    assert.ok(specs.length > 0, f);
    for (const s of specs) assert.match(s, /^\/vendor\//, `${f} imports ${s}: a served URL, never a bare specifier or a host's path`);
    assert.ok(!/\/vendor\/(agent-|bx-agent|bx-frame|frame-)/.test(specs.join(' ')), `${f} imports a host's module`);
    // a host is reached through ctx only: no private member of an element, no session API
    assert.equal(text.match(/\b(?:a|host|el)\._[a-zA-Z]/g), null, `${f} reaches into a host element`);
    assert.ok(!/\/api\/xbin\//.test(text), `${f} calls xbind's API: the host does the sending`);
  }
  for (const f of ['chat/css.js', 'chat/blocks.js', 'chat/tools.js', 'chat/ask.js']) assert.ok(!/lit-all|from 'lit'/.test(code(f)), `${f} stays pure`);
});

test('every kind of the model has a view, and the kit says its revision', () => {
  const views = code('chat/views.js');
  for (const kind of model.KINDS) assert.match(views, new RegExp(`case '${kind}':`), `blockView draws ${kind}`);
  assert.equal(KIT, 4);
  assert.match(src('chat/rev.js'), /\n \* {3}3 {2}the views/);
});

test('/vendor/agent-cards.js and /vendor/agent-css.js keep their names', () => {
  const names = (f) => [...src(f).matchAll(/^export\s+(?:const|(?:async\s+)?function)\s+(\w+)|^export\s*\{([^}]*)\}/gm)].flatMap((m) => (m[1] ? [m[1]] : m[2].split(',').map((x) => x.trim().split(/\s+as\s+/).pop()))).sort();
  assert.deepEqual(names('agent-cards.js'), ['KIND_ICON', 'askCard', 'cardsCss', 'changesCard', 'diffStat', 'filesBlock', 'mdLive', 'permCard', 'toolCard']);
  assert.deepEqual(names('agent-css.js'), ['agentCss']);
  assert.deepEqual(names('agent-queue.js'), ['fitComposer', 'queueChips', 'queueURL', 'stop', 'takeBack']);
});

// ---- the Agent tab's fold in the kit's block model ----------------------------------------

const ev = (seq, type, data) => ({ seq, ts: 1000 * seq, type, data });

test('blockOf: the same block while its version holds, a new one when it moves, one memo throughout', () => {
  const fold = new Fold([ev(1, 'message.delta', { role: 'agent', messageId: 'm1', text: 'Hel' })]);
  const b = fold.blocks[0];
  const first = blockOf(b);
  assert.deepEqual(model.plain(first), { k: '1', v: 0, kind: 'assistant', text: 'Hel', mid: 'm1' });
  assert.equal(blockOf(b), first, 'unchanged: the same object (a row\'s guard sees no change)');
  model.cached(first, 'x', () => 'memoized');
  (first.$memo.mdparts ||= {}).parts = ['kept'];
  fold.push(ev(2, 'message.delta', { role: 'agent', messageId: 'm1', text: 'lo' }));
  const second = blockOf(b);
  assert.notEqual(second, first);
  assert.deepEqual([second.k, second.v, second.text], ['1', 1, 'Hello']);
  assert.equal(second.$memo, first.$memo, 'what a view memoized (a streaming message\'s parsed parts) outlives the mapping');
  assert.deepEqual(second.$memo.mdparts.parts, ['kept']);
  assert.equal(JSON.stringify(second).includes('$'), false, '$memo is not data');
  assert.equal(foldCached, model.cached, '/vendor/agent-fold.js cached is the kit\'s');
});

test('blockOf: a subagent\'s blocks nest under its call; a call that waits on its request says so', () => {
  const fold = new Fold([
    ev(1, 'tool.call', { id: 'task', kind: 'think', title: 'Explore', status: 'in_progress', subagent: true, label: 'Explore the repo' }),
    ev(2, 'thought.delta', { parent: 'task', text: 'hm' }),
    ev(3, 'tool.call', { id: 'r1', parent: 'task', kind: 'read', title: 'Read a.go', status: 'pending' }),
    ev(4, 'permission.request', { pid: 'p1', toolCall: { id: 'r1', kind: 'read', title: 'Read a.go' }, options: [{ optionId: 'ok', name: 'Allow', kind: 'allow_once' }, { optionId: 'yolo', name: 'Bypass', kind: 'allow_always', explicit: true }],
      rule: { kind: 'read', title: 'Read a.go', scoped: true } }),
  ]);
  const sub = blockOf(fold.blocks[0]);
  assert.deepEqual(sub.children.map((c) => [c.k, c.kind, c.parent, c.$up === sub]), [['2', 'thought', 'task', true], ['3', 'tool', 'task', true]]);
  assert.equal(sub.children[1].state, 'approval', 'the request names the call: its card waits');
  assert.equal(sub.children[1].rec, fold.blocks[0].children[1].rec, 'the record is the fold\'s own');
  const perm = blockOf(fold.blocks[1]);
  assert.deepEqual([perm.kind, perm.callId, perm.rule, perm.options.map((o) => !!o.explicit)], ['perm', 'r1', { kind: 'read', title: 'Read a.go' }, [false, true]]);
  const v0 = sub.v;
  fold.push(ev(5, 'permission.resolved', { pid: 'p1', by: 'user:alice', optionId: 'ok' }));
  const after = blockOf(fold.blocks[0]);
  assert.ok(after.v > v0, 'the answer moved the call\'s row (and the card it is nested in)');
  assert.equal(after.children[1].state, 'pending');
  assert.equal(after.children[0], sub.children[0], 'a nested block that did not change is the same object');
  assert.equal(after.children[0].$up, after, '…under its parent as it is now');
  // a request that can't be scoped to a session rule says so (xbind's "always" is its own rule)
  const unscoped = blockOf(new Fold([ev(1, 'permission.request', { pid: 'p2', toolCall: { id: 'x', kind: 'execute', title: 'ls', rawInput: { command: 'ls' } }, options: [], rule: { kind: 'execute', title: 'ls', scoped: false } })]).blocks[0]);
  assert.deepEqual([unscoped.rule, unscoped.tool.command], [null, 'ls']);
});

test('transcriptOf: blocks, the sign-in that waits, the newest plan, the usage', () => {
  const fold = new Fold([
    ev(1, 'plan', { entries: [{ content: 'a', status: 'pending' }] }),
    ev(2, 'turn.end', { turn: 1, stopReason: 'end_turn', usage: { used: 42, size: 1000 } }),
    ev(3, 'status', { status: 'error', login: { needed: true, provider: 'Codex', command: 'codex login' } }),
  ]);
  const tr = transcriptOf(fold);
  assert.deepEqual(tr.blocks.map((b) => b.kind), ['plan', 'turn']);
  assert.equal(tr.plan, tr.blocks[0]);
  assert.deepEqual(model.plain(tr.tail), [{ k: 'signin', v: 0, kind: 'signin', provider: 'Codex', command: 'codex login', methods: [], device: null }]);
  assert.deepEqual(tr.usage, { used: 42, size: 1000 });
  assert.deepEqual(model.usageBadge(tr.usage), { text: 'ctx 4%', head: 'ctx 4%', pct: 4, tone: '', title: '42 of 1 000 tokens of context' });
});

// ---- the window's bookkeeping -------------------------------------------------------------

// a stand-in for ScrollWindow: the view's answers are set by the test
function fakeWindow(opts = {}) {
  const src = { version: 1, hasOlder: false, hasNewer: false, fresh: 0, calls: [],
    loadOlder: async () => { src.calls.push('older'); return true; }, loadNewer: async () => { src.calls.push('newer'); return true; },
    latest: async () => { src.calls.push('latest'); }, keep: (lo, hi, drop) => { src.calls.push(['keep', lo, hi, drop]); return false; }, follow: (at) => { src.calls.push(['follow', at]); } };
  let renders = 0;
  const win = new ChatWindow({ source: src, changed: () => { renders++; }, keyOf: (b) => b.key, ...opts });
  const sw = { atBottom: true, keepView: false, sc: {}, above: false, below: false, ta: null, tb: null, rows: 10,
    before() {}, after() {}, attach() {}, detach() {}, toBottom() { sw.atBottom = true; },
    trimAbove: () => sw.ta, trimBelow: () => sw.tb, wantsAbove: () => sw.above, wantsBelow: () => sw.below, rowsPerView: () => sw.rows };
  win.sw = sw;
  return { win, sw, src, renders: () => renders };
}
const rows = (n, from = 1) => Array.from({ length: n }, (_, i) => ({ key: from + i }));

test('ChatWindow: follows the bottom with the last page; grows by a page where the reader heads; loads when it has no more rows', async () => {
  const { win, sw, src, renders } = fakeWindow();
  const blocks = rows(100);
  assert.deepEqual(win.place(blocks), { start: 70, end: 100 }, 'the last page (30 rows)');
  assert.equal(win.pill(), '', 'at the bottom: no pill');
  // new rows while following: the window reaches the end
  src.version = 2;
  assert.deepEqual(win.place(rows(105)), { start: 70, end: 105 });
  // the reader nears the top: a page of rows already held, by key
  sw.atBottom = false; sw.above = true;
  win.fill();
  assert.equal(renders(), 1);
  assert.deepEqual(win.place(rows(105)), { start: 40, end: 105 });
  assert.equal(win.pill(), '', 'nothing is below what is rendered: no pill yet');
  // …a row arrives while they are away: the window still reaches the end of what is held, and the pill says what is new
  src.version = 3; src.fresh = 1;
  assert.deepEqual(win.place(rows(106)), { start: 40, end: 106 });
  assert.equal(win.pill(), '1 new — jump to latest');
  // …once the window's end is a row (it was trimmed below), later rows stay out until the reader heads there
  win.toKey = 100;
  assert.deepEqual(win.place(rows(110)), { start: 40, end: 100 });
  assert.equal(win.pill(), '1 new — jump to latest');
  sw.above = false; sw.below = true;
  win.fill();
  assert.deepEqual(win.place(rows(110)), { start: 40, end: 110 }, 'a page of rows below');
  sw.below = false; sw.above = true;
  // at the first held row: older pages are asked of the source, one load at a time
  win.fromKey = 1; src.hasOlder = true;
  win.place(rows(110));
  win.fill(); win.fill();
  await win.busy;
  assert.deepEqual(src.calls.filter((c) => c === 'older'), ['older']);
  // jump to latest: the tail again, followed
  src.hasNewer = true;
  await win.latest();
  assert.ok(src.calls.includes('latest') && src.calls.some((c) => Array.isArray(c) && c[0] === 'follow' && c[1] === true));
  assert.deepEqual(win.place(rows(110)), { start: 80, end: 110 });
});

test('ChatWindow: a long window is trimmed on the far side; load all renders everything; far pages are let go', async () => {
  const { win, sw, src } = fakeWindow();
  const blocks = rows(400);
  win.fromKey = 100; // grown far up while following
  sw.ta = '250'; // ScrollWindow: rows above key 250 are far above the view (a row's data-k is text)
  assert.deepEqual(win.place(blocks), { start: 249, end: 400 }, 'trimmed above, by key');
  sw.atBottom = false; sw.ta = null; sw.tb = '300';
  win.fromKey = 100; win.toKey = 390;
  assert.deepEqual(win.place(blocks), { start: 99, end: 300 }, 'away from the bottom: trimmed below');
  // unload: pages about three views beyond the rendered rows
  win.fill();
  assert.deepEqual(src.calls.find((c) => Array.isArray(c) && c[0] === 'keep'), ['keep', 99 - 30, 300 + 30, true]);
  const unloaded = [];
  const b = fakeWindow({ minMargin: 80, onUnload: (lo, hi) => unloaded.push([lo, hi]) });
  b.win.place(rows(100)); b.win.fill();
  assert.deepEqual(unloaded, [[70 - 80, 100 + 80]], 'a host\'s floor under the margin (the agent template\'s)');
  // load all: every row, nothing unloads
  src.calls.length = 0; src.hasOlder = false; src.hasNewer = false;
  await win.loadAll();
  assert.deepEqual(win.place(blocks), { start: 0, end: 400 });
  win.fill();
  assert.equal(src.calls.filter((c) => Array.isArray(c) && c[0] === 'keep').length, 0);
  assert.equal(win.pill(), '', 'load all has no pill');
  // a paused host (a hidden tab) fills nothing
  const p = fakeWindow({ paused: () => true });
  p.win.place(rows(50)); p.sw.atBottom = false; p.sw.above = true; p.win.fill();
  assert.equal(p.renders(), 0);
  // text keys (a tile's row ids)
  const t = fakeWindow({ keyOf: (x) => x.k });
  t.win.fromKey = 'm3';
  assert.deepEqual(t.win.place(['m1', 'm2', 'm3', 'm4'].map((k) => ({ k }))), { start: 2, end: 4 });
});

// ---- pure helpers -------------------------------------------------------------------------

test('usageBadge, cached, stageFiles', () => {
  assert.equal(model.usageBadge(null), null);
  assert.equal(model.usageBadge({}), null);
  assert.deepEqual(model.usageBadge({ used: 160000, size: 200000 }).tone, 'warn');
  assert.deepEqual(model.usageBadge({ used: 190000, size: 200000 }).tone, 'bad');
  assert.equal(model.usageBadge({ cost: { amount: 1.5, currency: 'EUR' } }).text, 'EUR 1.50');
  const b = { k: 'x', v: 1 };
  let n = 0;
  assert.deepEqual([model.cached(b, 's', () => ++n), model.cached(b, 's', () => ++n)], [1, 1]);
  b.v = 2;
  assert.equal(model.cached(b, 's', () => ++n), 2);
  assert.equal(model.cached({ k: 'no-version' }, 's', () => 'computed'), 'computed');
  assert.deepEqual(ATTACH_LIMITS, { files: 10, bytes: 10 << 20, total: 20 << 20 }, 'the session API\'s limits');
  const r = stageFiles([{ name: 'a', size: 5 }], [{ name: 'big', size: 40 }, { name: 'b', size: 10 }, { name: 'c', size: 10 }, { name: 'd', size: 1 }], { files: 3, bytes: 16, total: 20 });
  assert.deepEqual(r.files.map((f) => f.name), ['a', 'b', 'd']);
  assert.deepEqual(r.refused, ['big is 40 B: a file may be 16 B at most', 'c: the files of a message may be 20 B together']);
  assert.equal(stageFiles([], [{ name: 'x', size: 1 }, { name: 'y', size: 1 }], { files: 1, bytes: 9, total: 9 }).refused[0], 'y: at most 1 files go with a message');
  assert.deepEqual([fmtSize(12), fmtSize(2048), fmtSize(3 << 20)], ['12 B', '2 KiB', '3.0 MiB']);
});
