/**
 * agent-fold.js — the Agent tab's view model (D124): the session's typed event
 * log folded into ordered blocks, one event at a time.
 *
 * bx-agent used to re-fold the whole log (and re-run markdown, diffs and
 * highlighting for every block) on every event and keystroke, which made a
 * long or replayed conversation crawl. A Fold is incremental instead: push()
 * applies one event to the blocks it touches. Every block carries a stable
 * `key` (for lit's repeat) and a version `v`, bumped whenever the block — or a
 * block nested in it — changes, so the renderer can skip unchanged blocks and
 * memoize their expensive output (cached()). `st` is the digest of the latest
 * status fields, replacing a full scan per accessor.
 *
 * A block's key is the seq of the event that opened it (D130), so a block
 * folded again — its page unloaded and fetched back, a late event refolding
 * its page — keeps its key: lit's identity, open cards and memos follow it.
 * A fold may start mid-log, at a page the server cut where no block spans
 * the cut (GET …/events?before=&limit=): seed() applies the page's state
 * header first, then push() its events.
 *
 * Pure (no DOM, no lit): node-tested in hack/agent-fold.test.mjs.
 */
import { newTool, foldTool } from './agent-tools.js';

// bump b's version and its enclosing subagent cards' (their bodies render it)
const bump = (b) => { for (let x = b; x; x = x.up) x.v++; };

export class Fold {
  constructor(events) { this.reset(events); }

  // refold from scratch (an out-of-order event, a new session)
  reset(events = []) {
    this.blocks = [];
    this.tools = new Map(); // turn/id → record: an agent may reuse ids across turns
    this.byId = new Map(); // a call's latest record: files.changed may land after its turn ended
    this.perms = new Map();
    this.asks = new Map();
    this.plan = null;
    this.cur = null;
    this.turn = 0;
    this.n = 0;
    this.lastSeq = 0;
    this.seq = 0; // the event being pushed (its seq keys the block it opens)
    this.st = newStatus(); // the latest status fields (foldStatus)
    for (const e of events) this.push(e);
    return this;
  }

  // a block's key: the seq of the event that opens it (an event opens at
  // most one); an event without one (a test's, a stream's gap) counts down
  _new(b, up) { b.key = this.seq > 0 ? this.seq : -(++this.n); b.v = 0; if (up) { b.up = up; bump(up); } return b; }

  // seed(state): a page's state header (docs/protocol.md → Pages) — the
  // status digest and the last turn's usage and number before the page
  seed(state) {
    if (!state) return this;
    if (state.status) foldStatus(this.st, { type: 'status', data: state.status });
    if (state.usage) this.st.usage = state.usage;
    if (state.turn) this.turn = state.turn + 0.5;
    return this;
  }

  _done(t, ts) { // a thought ends when anything else arrives: that is its duration
    if (t && t.kind === 'thought' && !t.done) { if (ts) t.t1 = ts; t.done = true; bump(t); }
  }

  push(e) {
    this.seq = typeof e.seq === 'number' ? e.seq : 0;
    if (this.seq > this.lastSeq) this.lastSeq = this.seq;
    const d = e.data || {};
    if (e.type !== 'thought.delta' && e.type !== 'status' && e.type !== 'files.changed') this._done(this.cur, e.ts);
    switch (e.type) {
      case 'status':
        foldStatus(this.st, e);
        break;
      case 'message.delta': case 'thought.delta': {
        // a subagent's text goes into its call's card (Part of D77: nesting)
        const box = d.parent ? this.byId.get(d.parent) : null;
        const into = box && box.children ? box : null;
        const list = into ? into.children : this.blocks;
        let c = into ? into.cur : this.cur;
        if (c && c.kind === 'thought' && e.type !== 'thought.delta') this._done(c, e.ts);
        if (e.type === 'thought.delta') {
          if (c && c.kind === 'thought') { c.text += d.text || ''; c.t1 = e.ts || c.t1; bump(c); }
          else { c = this._new({ kind: 'thought', text: d.text || '', t0: e.ts || 0, t1: e.ts || 0, done: false }, into); list.push(c); }
        } else {
          const role = d.role || 'agent';
          // a prompt's attachments (names, types, sizes) ride its one user delta
          if (c && c.kind === 'msg' && c.role === role && c.mid === (d.messageId || '') && !c.files && !d.attachments) { c.text += d.text || ''; bump(c); }
          else { c = this._new({ kind: 'msg', role, mid: d.messageId || '', text: d.text || '', files: d.attachments }, into); list.push(c); }
        }
        if (into) into.cur = c; else this.cur = c;
        break;
      }
      case 'tool.call': case 'tool.update': {
        const tkey = this.turn + '/' + d.id;
        let t = this.tools.get(tkey);
        const box = d.parent ? this.byId.get(d.parent) : null;
        const into = box && box.children && box !== t ? box : null;
        if (into) { this._done(into.cur); into.cur = null; } else this.cur = null;
        if (!t) {
          t = this._new(newTool(d.id), into);
          t.t0 = e.ts || 0;
          this.tools.set(tkey, t);
          (into ? into.children : this.blocks).push(t);
        }
        foldTool(t, d);
        this.byId.set(d.id, t);
        if (t.children && t.status !== 'pending' && t.status !== 'in_progress') {
          for (const ch of t.children) this._done(ch); // a finished subagent thinks no more
        }
        bump(t);
        break;
      }
      case 'files.changed': { // a snapshot diff: of one call, or of a whole turn
        const f = { changes: d.changes || [], patch: d.patch || null };
        if (d.toolCallId) {
          const t = this.tools.get(this.turn + '/' + d.toolCallId) || this.byId.get(d.toolCallId);
          if (t) { t.files = f; bump(t); }
          break;
        }
        const blk = this._new({ kind: 'changes', turn: d.turn, ...f });
        const at = this.blocks.findLastIndex((b) => b.kind === 'turn' && b.turn === d.turn); // before its turn's end marker
        if (at >= 0) this.blocks.splice(at, 0, blk); else this.blocks.push(blk);
        break;
      }
      case 'plan':
        this.cur = null;
        if (!this.plan) { this.plan = this._new({ kind: 'plan', entries: [] }); this.blocks.push(this.plan); }
        this.plan.entries = d.entries || [];
        bump(this.plan);
        break;
      case 'permission.request': {
        this.cur = null;
        const p = this._new({ kind: 'perm', pid: d.pid, tool: d.toolCall || {}, options: d.options || [], rule: d.rule || null, meta: d.meta || null, by: null, optionId: null });
        this.perms.set(d.pid, p); this.blocks.push(p);
        break;
      }
      case 'elicitation.request': {
        this.cur = null;
        const q = this._new({ kind: 'ask', eid: d.eid, toolCallId: d.toolCallId || '', message: d.message || '', schema: d.schema || null, action: null, by: null, content: null });
        this.asks.set(d.eid, q); this.blocks.push(q);
        break;
      }
      case 'elicitation.resolved': {
        const q = this.asks.get(d.eid);
        if (q) { q.action = d.action; q.by = d.by; q.content = d.content || null; bump(q); }
        break;
      }
      case 'permission.resolved': {
        const p = this.perms.get(d.pid);
        if (p) { p.by = d.by; p.optionId = d.optionId; bump(p); }
        break;
      }
      case 'turn.end':
        this.cur = null;
        this.plan = null; // a new turn starts a fresh plan
        this.turn = (d.turn || this.turn) + 0.5; // tool ids in the next turn don't collide with this one's
        foldStatus(this.st, e);
        this.blocks.push(this._new({ kind: 'turn', turn: d.turn, stopReason: d.stopReason, usage: d.usage, error: d.error }));
        break;
      case 'gap':
        this.cur = null;
        this.blocks.push(this._new({ kind: 'gap' }));
        break;
    }
    return this;
  }
}

// the status digest: status/detail/currentMode/options/modes/commands each
// from the last status that carried it, last = the last status's data (its
// login), usage = the last turn.end's
export const newStatus = () => ({ status: '', detail: '', currentMode: '', options: null, modes: null, commands: null, last: null, usage: null });

// foldStatus(st, e): apply one event to a status digest (status and
// turn.end move it; anything else is ignored)
export function foldStatus(st, e) {
  const d = e.data || {};
  if (e.type === 'turn.end') { if (d.usage) st.usage = d.usage; return st; }
  if (e.type !== 'status') return st;
  if (d.status) st.status = d.status;
  if (d.detail) st.detail = d.detail;
  if (d.currentMode) st.currentMode = d.currentMode;
  if (Array.isArray(d.options)) st.options = d.options;
  if (Array.isArray(d.modes)) st.modes = d.modes;
  if (Array.isArray(d.commands)) st.commands = d.commands;
  st.last = d;
  return st;
}

// cached(b, slot, fn): fn()'s value, memoized on block b for its current
// version — markdown, diffs and highlighting run once per change, and survive
// the block leaving and re-entering the rendered window. A record without a
// version (a permission card's synthetic view) just computes.
export function cached(b, slot, fn) {
  if (!b || typeof b.v !== 'number') return fn();
  const memo = b.$memo || (b.$memo = Object.create(null));
  const m = memo[slot];
  if (m && m.v === b.v) return m.val;
  const val = fn();
  memo[slot] = { v: b.v, val };
  return val;
}
