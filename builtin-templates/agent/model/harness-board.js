// model/harness-board.js — the Coding agents board (D-harness §4.3.6,
// §8 U7) for both views, as app.board: the coding agents (harness runs) in
// the open conversation's tree — or, at home, every one of yours that runs
// or needs you: your coding-agent conversations and the coding agents started
// below your conversations — as rows in a stable order (by id, which is
// creation order: a row never moves when one changes), each a child card
// (model/harness-child.js childCard) in its section (needs you · running ·
// done); the top bar's chip; the pinned task's Delegated section.
//
// Where a row comes from, each source on top of the last:
//   - the tree (GET /runs/{root}/tree, §4.3.6): which runs there are, and
//     their compact summary — read when first wanted, and again when the
//     stream names a run or link it lacks (one request in flight per root,
//     like the workflow tree's treeDirty); at home, whose stream follows the
//     run list only, also when its root's row changes;
//   - the link a held view carries (its child has the full pendingState, so
//     a park is answered in place) and the session's run summaries — unless
//     the tree is newer (`updated`);
//   - what the stream said since the board began watching the root (run,
//     link and harness events).
// A conversation's tree is read only once something says a coding agent is
// in it (its row's kids, a held link, a run summary); at home only the trees
// of rows with coding agents at work (§4.3.8 kids.harness), at most HOME_TREES.
//
// No lit, no DOM: node-tested in hack/agent-template-harness-board.test.mjs.
import { isHarness } from './harness.js';
import { childCard } from './harness-child.js';
import { parseArgs } from './tool-heads.js';

export const SECTIONS = [
  { key: 'needs', title: 'Needs you' },
  { key: 'running', title: 'Running' },
  { key: 'done', title: 'Done' },
];
const NEEDS = new Set(['approval', 'question', 'login']);
const RUNNING = new Set(['starting', 'working', 'lost']);
// sectionOf: a card's state (childCard state.key) → needs | running | done
export const sectionOf = (key) => (NEEDS.has(key) ? 'needs' : RUNNING.has(key) ? 'running' : 'done');
export const HOME_TREES = 12; // the most trees home reads (the rows first in the list)

// nodeRun: a tree node as a run summary (a node's `status` is a word; rawStatus the run's)
function nodeRun(n, root) {
  const out = { id: n.id, parentId: n.parentId || 0, rootId: root, title: n.title || '', status: n.rawStatus || n.status || '',
    engine: n.engine || '', created: n.created, updated: n.updated, result: n.result || '' };
  if (n.harness) out.harness = n.harness;
  return out;
}
// nodeLink: a node's own link (§4.3.6 carries its state) with what the card reads of a link
const nodeLink = (n) => (n.link ? { ...n.link, parentId: n.parentId, created: n.created, settled: n.settledAt, result: n.result || '' } : null);

const firstLine = (s, n = 140) => {
  const t = String(s || '').split('\n').map((x) => x.trim()).find(Boolean) || '';
  return t.length > n ? t.slice(0, n - 1) + '…' : t;
};

/**
 * chipWords: the top bar's chip — {text: "⌨ 3 coding agents · 1 needs you",
 * n, needs, running, done, tone: warn | run | '', title} — or null (no rows).
 */
export function chipWords(rows) {
  if (!rows || !rows.length) return null;
  const by = { needs: 0, running: 0, done: 0 };
  for (const r of rows) by[r.section]++;
  const n = rows.length;
  const need = by.needs ? ` · ${by.needs} need${by.needs === 1 ? 's' : ''} you` : '';
  const parts = [by.needs ? `${by.needs} waiting for you` : '', by.running ? `${by.running} running` : '', by.done ? `${by.done} done` : ''].filter(Boolean);
  return {
    text: `⌨ ${n} coding agent${n === 1 ? '' : 's'}${need}`, n, ...by,
    tone: by.needs ? 'warn' : by.running ? 'run' : '',
    title: `${parts.join(' · ')} — open the Coding agents board`,
  };
}

// filterWords: the "needs you" filter's chip — {text, on, n} — null when nothing needs you.
export function filterWords(rows, on) {
  const n = (rows || []).filter((r) => r.section === 'needs').length;
  if (!n && !on) return null;
  return { n, on: !!on, text: n ? `${n} need${n === 1 ? 's' : ''} you` : 'needs you', title: on ? 'show every coding agent again' : 'show only the ones waiting for you' };
}

// sectioned: rows by section, in SECTIONS' order (each keeps the rows' order); empty ones left out.
export const sectioned = (rows) => SECTIONS.map((s) => ({ ...s, rows: rows.filter((r) => r.section === s.key) })).filter((s) => s.rows.length);

// emptyWords: what an empty board says.
export const emptyWords = (home, filtered) => (filtered ? 'none of them needs you now'
  : home ? 'none of your coding agents is running or needs you' : 'no coding agents in this conversation');

// delegatedWords: a row in the pinned task's Delegated section — {id, mono,
// name, title, task (its first line), state, status}.
export const delegatedWords = (r) => ({ id: r.id, mono: r.card.mono, name: r.card.name, title: r.card.title,
  task: firstLine(r.card.task), state: r.card.state, status: r.card.status });

/**
 * createBoard(app) → app.board:
 *   rows(root)    the rows of a conversation's tree (root: its root run), or at
 *                 home (null) yours that run or need you — [{id, parentId, root,
 *                 b (the card's agent block), run, card, section, view (whose
 *                 access answers: the open view, at home its root's row)}]
 *   chip(root)    chipWords of those
 *   delegated(v)  the rows below the open run v (its pinned task's Delegated)
 *   take(ev)      every stream event (model/app.js); reset() after the stream's reset
 */
export function createBoard(app, { now = () => Date.now() } = {}) {
  const trees = new Map(); // root → {tree, busy, again, err}
  const seen = new Map();  // run id → what the stream said of it since the board watched its root
  const linked = new Map(); // child id → its link, from link events
  let scope; // the root the board holds (a conversation's), or 'home'
  const S = () => app.session;

  const read = (root) => {
    let t = trees.get(root);
    if (!t) trees.set(root, (t = { tree: null, busy: false, again: false, err: '' }));
    if (t.busy) { t.again = true; return; }
    t.busy = true;
    let p;
    try { p = Promise.resolve(app.actions.tree(root)); } catch (e) { p = Promise.reject(e); }
    p.then((x) => { t.tree = x; t.err = ''; }, (e) => { t.err = (e && e.message) || String(e); })
      .finally(() => {
        t.busy = false;
        if (trees.get(root) !== t) return; // let go meanwhile
        if (t.again) { t.again = false; read(root); }
        S().changed();
      });
  };
  const want = (root) => { if (!trees.has(root)) read(root); };
  const inTree = (root, id) => !!((trees.get(root) || {}).tree?.nodes || []).some((n) => n.id === id);

  // the latest link to a child: a held view's (kept current by the session), else one an event brought
  function linkOf(id) {
    let best = linked.get(id) || null;
    for (const v of S().views.values()) {
      for (const l of v.links || []) if (l.childId === id && (!best || (l.id || 0) >= (best.id || 0))) best = l;
    }
    return best;
  }
  // the task a spawn gave its child: the call's arguments in the parent's held messages
  const taskMemo = new WeakMap();
  function spawnTask(l) {
    if (!l || !l.toolCallId) return '';
    if (taskMemo.has(l)) return taskMemo.get(l);
    let task = '';
    for (const m of (S().views.get(l.parentId) || {}).messages || []) {
      const c = (m.toolCalls || []).find((x) => x.id === l.toolCallId);
      if (c) { task = String(parseArgs(c.function && c.function.arguments).task || ''); break; }
    }
    if (task) taskMemo.set(l, task);
    return task;
  }
  const taskOf = (run, link) => spawnTask(link) || (run.task && run.task.first && run.task.first.text) || '';

  // rowOf: a harness run as a board row — base: its tree node as a run, or
  // (a coding-agent conversation at home) its list row; node: the tree's
  function rowOf(id, base, root, view, node = null) {
    const link = linkOf(id);
    const held = S().runs.get(id);
    const newer = (x) => x && !(Number(base.updated) > (Number(x.updated) || 0)); // not older than what the base says
    const run = { ...base, ...(newer(link && link.child) ? link.child : {}), ...(newer(held) ? held : {}), ...(seen.get(id) || {}) };
    run.id = id;
    run.engine = 'harness';
    const b = { k: 'agent', id: 'hb' + id, childId: id, child: run, harness: run.harness || {}, args: '',
      link: link || (node ? nodeLink(node) : null), task: taskOf(run, link),
      blocks: S().views.has(id) ? S().blocks(id) : null };
    const card = childCard(b, run, now());
    return { id, parentId: run.parentId || 0, root, b, run, card, section: sectionOf(card.state.key), view };
  }

  // something says a coding agent is in this conversation (the tree is then read)
  function evidence(root) {
    const row = app.convs && app.convs.find(root);
    if (row && row.kids && row.kids.harness > 0) return true;
    for (const v of S().views.values()) {
      if ((v.run && (v.run.rootId || v.run.id)) !== root) continue;
      if (isHarness(v.run) && v.run.parentId) return true;
      if ((v.links || []).some((l) => isHarness(l.child))) return true;
    }
    for (const x of seen.values()) if (isHarness(x) && x.parentId) return true;
    return false;
  }

  // the coding agents below a root: its tree's harness nodes, and any the stream named since
  function treeRows(root, view) {
    const t = trees.get(root);
    const nodes = (t && t.tree && t.tree.nodes) || [];
    const ids = new Map();
    for (const n of nodes) if (n.engine === 'harness' && n.id !== root && n.parentId) ids.set(n.id, n);
    const more = (x) => { if (x && isHarness(x) && x.parentId && (x.rootId || 0) === root && !ids.has(x.id)) ids.set(x.id, null); };
    for (const [id, x] of seen) more({ ...x, id });
    for (const l of linked.values()) more(l.child);
    for (const v of S().views.values()) for (const l of v.links || []) more(l.child);
    return [...ids].map(([id, n]) => rowOf(id, n ? nodeRun(n, root) : { id, rootId: root }, root, view, n));
  }

  function convRows(root) {
    if (!trees.has(root) && !evidence(root)) return [];
    want(root);
    return treeRows(root, S().current() || { access: 'owner' });
  }

  // home: your coding-agent conversations that run or wait, and the coding
  // agents at work below your conversations (and below what needs you)
  function homeRows() {
    const roots = new Map();
    for (const x of (app.convs ? app.convs.all() : []).filter((r) => r.mine !== false)) roots.set(x.id, x);
    for (const n of app.needs || []) if (n.run && !roots.has(n.run.id)) roots.set(n.run.id, n.run);
    const out = [];
    let reads = 0;
    for (const x of roots.values()) {
      const view = { access: x.access || 'owner', run: x };
      if (isHarness(x)) out.push(rowOf(x.id, x, x.id, view));
      if (x.kids && x.kids.harness > 0 && reads++ < HOME_TREES) { want(x.id); out.push(...treeRows(x.id, view)); }
    }
    return out.filter((r) => r.section !== 'done');
  }

  const board = {
    rows(root = null) {
      // what the stream follows now: the open conversation's tree, or the run list at home —
      // trees read while it followed something else are stale
      const s = app.sel == null ? 'home' : app.root ?? root;
      if (s !== scope) { scope = s; trees.clear(); seen.clear(); linked.clear(); }
      const rows = root == null ? homeRows() : convRows(root);
      return rows.sort((a, b) => a.id - b.id); // creation order: never re-sorted by state
    },
    chip(root = null) { return chipWords(board.rows(root)); },
    // delegated: the coding agents below run v (its pinned task's Delegated section)
    delegated(v) {
      if (!v || !v.run) return [];
      const root = v.run.rootId || v.run.id;
      const rows = board.rows(root);
      if (v.run.id === root) return rows;
      const parent = new Map();
      for (const n of ((trees.get(root) || {}).tree || {}).nodes || []) parent.set(n.id, n.parentId || 0);
      for (const r of rows) parent.set(r.id, r.parentId);
      const under = (id) => { for (let p = parent.get(id), k = 0; p && k < 64; p = parent.get(p), k++) if (p === v.run.id) return true; return false; };
      return rows.filter((r) => under(r.id));
    },
    // loading: a tree it waits for (the board says loading… before its first rows)
    loading(root = null) {
      if (root != null) return !!(trees.get(root) && !trees.get(root).tree && !trees.get(root).err);
      return [...trees.values()].some((t) => !t.tree && !t.err);
    },
    take(ev) {
      const d = ev.data || {};
      if (!trees.has(ev.root)) return; // a root the board doesn't watch
      const merge = (id, x) => seen.set(id, { ...(seen.get(id) || {}), ...x });
      switch (ev.type) {
        case 'run':
          if (d.deleted) { seen.delete(ev.run); read(ev.root); break; }
          merge(ev.run, d);
          // a run the tree lacks; at home (no child events) any change of its root
          if (!inTree(ev.root, ev.run) || (scope === 'home' && ev.run === ev.root)) read(ev.root);
          break;
        case 'harness':
          merge(ev.run, { harness: d });
          break;
        case 'link':
          if (d.childId) linked.set(d.childId, d);
          if (d.child) merge(d.childId, d.child);
          if (d.childId && !inTree(ev.root, d.childId)) read(ev.root);
          break;
      }
    },
    reset() { seen.clear(); for (const root of [...trees.keys()]) read(root); },
  };
  return board;
}
