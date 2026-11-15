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
 * What the chat kit draws (/vendor/chat/views.js) is its block model
 * (/vendor/chat/blocks.js): blockOf(b) says one of this fold's blocks in it
 * — the same key, the same version, a tool call's record the fold's own —
 * and transcriptOf(fold) the whole fold. The fold's own shape stays for who
 * reads it (the test API, the iOS port's parity corpus).
 *
 * Pure (no DOM, no lit): node-tested in hack/agent-fold.test.mjs.
 */
import { newTool, foldTool } from './agent-tools.js';
import { block, toolState, up } from './chat/blocks.js';
import { changeSet, isPlanApproval, planText, planPath, inputCommand } from './chat/tools.js';

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
    if (state.llm) this.st.llm = state.llm; // "connector" | "own" (D226)
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
    if (e.type !== 'thought.delta' && e.type !== 'status' && e.type !== 'files.changed' && e.type !== 'queue') this._done(this.cur, e.ts);
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
          // a message the running turn took mid-turn (D197) is a bubble of its own
          if (c && c.kind === 'msg' && c.role === role && c.mid === (d.messageId || '') && !c.files && !d.attachments && !c.steered && !d.steered) { c.text += d.text || ''; bump(c); }
          else { c = this._new({ kind: 'msg', role, mid: d.messageId || '', text: d.text || '', files: d.attachments, ...(d.steered ? { steered: true } : {}) }, into); list.push(c); }
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
          if (t) { t.files = f; if (t.rec) t.rec.changed = changeSet(d); bump(t); }
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
        // the call it is about waits on it: its card says so (and the request names the call's label)
        const t = p.tool.id ? this.byId.get(p.tool.id) : null;
        if (t) { p.label = t.label || ''; t.perm = d.pid; bump(t); }
        this.perms.set(d.pid, p); this.blocks.push(p);
        break;
      }
      case 'elicitation.request': {
        this.cur = null;
        // a form from its schema, or — mode url — a page to open (/vendor/chat/ask.js question())
        const q = this._new({ kind: 'ask', eid: d.eid, toolCallId: d.toolCallId || '', message: d.message || '', mode: d.mode === 'url' ? 'url' : 'form', url: d.url || '',
          schema: d.schema || null, action: null, by: null, content: null });
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
        if (p) {
          p.by = d.by; p.optionId = d.optionId; bump(p);
          const t = p.tool.id ? this.byId.get(p.tool.id) : null;
          if (t && t.perm === d.pid) { t.perm = ''; bump(t); }
        }
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
      case 'queue': // the held messages (D197): status, not a row
        foldStatus(this.st, e);
        break;
      case 'notice': // a line xbin says, not the agent (D175: the start moved the layer to a new base)
        foldStatus(this.st, e);
        this.blocks.push(this._new({ kind: 'notice', text: d.text || '' }));
        break;
    }
    return this;
  }
}

// llmHint(p): what a session of provider p would run on, in a few words —
// from the row's `llm` (GET /agent/providers, D226: only while the
// workspace's LLM connector is offered to agent sessions); '' otherwise.
export const llmHint = (p) => ({ connector: 'LLM by xbin', 'own-first': 'your sign-in, else LLM by xbin', own: 'your sign-in', signin: 'needs a sign-in', unavailable: "can't run here" })[p?.llm?.use] || '';

// the status digest: status/detail/currentMode/options/modes/commands each
// from the last status that carried it, last = the last status's data (its
// login), usage = the last turn.end's
export const newStatus = () => ({ status: '', detail: '', currentMode: '', options: null, modes: null, commands: null, last: null, usage: null, queue: [] });

// foldStatus(st, e): apply one event to a status digest (status, turn.end,
// queue — the messages held for the turn's end, D197 — and a notice naming
// who serves the session's model calls move it; anything else is ignored)
export function foldStatus(st, e) {
  const d = e.data || {};
  if (e.type === 'turn.end') { if (d.usage) st.usage = d.usage; return st; }
  if (e.type === 'queue') { st.queue = Array.isArray(d.items) ? d.items : []; return st; }
  if (e.type === 'notice') { if (d.llm) st.llm = d.llm; return st; } // who serves the session's model calls (D226)
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

// ---- the fold as the chat kit's blocks ---------------------------------------------------

// one of the fold's blocks in the kit's block model, field by field
function toBlock(b) {
  const k = b.key;
  switch (b.kind) {
    case 'msg':
      return b.role === 'user'
        ? block('user', k, { text: b.text, files: (b.files || []).map((f) => ({ name: f.name, mime: f.mime || '', size: f.size ?? null })), steered: !!b.steered })
        : block('assistant', k, { text: b.text, mid: b.mid });
    case 'thought':
      return block('thought', k, { text: b.text, live: !b.done, ms: Math.max(0, (b.t1 || 0) - (b.t0 || 0)), at: b.t0 || 0 });
    case 'tool': {
      // (a call made by an older agent-tools.js has no record: one made of what it holds)
      const rec = b.rec || { id: b.id, kind: b.tk || 'other', title: b.title || '', label: b.label || '', name: b.name || '', status: b.status || 'pending', rawInput: b.rawInput ?? null, output: b.output || '', exitCode: b.exitCode ?? null, diffs: [], locations: [], files: [], text: '' };
      const out = block('tool', k, { rec, state: toolState(rec, b.perm ? 'approval' : ''), children: null, at: b.t0 || 0 });
      if (b.children) out.children = b.children.map((c) => { const kid = blockOf(c); up(kid, out); kid.parent = rec.id; return kid; });
      return out;
    }
    case 'changes':
      return block('changes', k, { turn: b.turn, set: changeSet({ changes: b.changes, patch: b.patch }) });
    case 'plan':
      return block('plan', k, { entries: (b.entries || []).map((e) => ({ content: e.content, status: e.status, priority: e.priority || '' })) });
    case 'perm': {
      const tc = b.tool || {}, meta = b.meta || {};
      return block('perm', k, {
        pid: b.pid, callId: tc.id || '',
        tool: { title: tc.title || '', kind: tc.kind || '', name: tc.name || '', label: b.label || '', command: inputCommand(tc.rawInput), rawInput: tc.rawInput ?? null, content: tc.content ?? null },
        options: (b.options || []).map((o) => ({ optionId: o.optionId, name: o.name, kind: o.kind, explicit: !!o.explicit })),
        // xbind's "always" is its own session rule: null when the request can't be scoped to one
        rule: b.rule && b.rule.scoped !== false && (b.rule.kind || b.rule.title) ? { kind: b.rule.kind || '', title: b.rule.title || '' } : null,
        title: meta.title || '', description: meta.description || '', defaultToNo: !!meta.defaultToNo,
        plan: isPlanApproval(tc) ? { text: planText(tc), path: planPath(tc) } : null,
        by: b.by ?? null, optionId: b.optionId ?? null,
      });
    }
    case 'ask':
      return block('ask', k, { eid: b.eid, callId: b.toolCallId || '', message: b.message, mode: b.mode || 'form', schema: b.schema, url: b.url || '', action: b.action ?? null, by: b.by ?? null, content: b.content ?? null });
    case 'turn':
      return block('turn', k, { turn: b.turn, stopReason: b.stopReason || '', usage: b.usage || null, error: b.error || '' });
    case 'notice':
      return block('notice', k, { text: b.text });
    case 'gap':
      return block('gap', k, {});
  }
  return block(b.kind, k, {});
}

/**
 * blockOf(b): one of a Fold's blocks as the chat kit's block (the mapping
 * table of docs/frontend-kit.md → the chat kit): k is its key as text, v
 * its version, a tool call's `rec` the fold's own record, its children
 * mapped the same way. The result is kept on b until b's version moves, so
 * a row that did not change is the same object; its $memo is b's (what a
 * view memoized outlives the mapping).
 */
export function blockOf(b) {
  const had = b.$block;
  if (had && had.v === b.v) return had;
  const out = toBlock(b);
  out.v = b.v;
  Object.defineProperty(out, '$memo', { value: b.$memo || (b.$memo = Object.create(null)), writable: true, configurable: true, enumerable: false });
  Object.defineProperty(b, '$block', { value: out, writable: true, configurable: true, enumerable: false });
  return out;
}

/**
 * transcriptOf(fold) → the kit's Transcript of a Fold: its blocks; in `tail`
 * the sign-in the last status asks for (the tab draws it under the
 * transcript); the newest plan; the last turn's usage.
 */
export function transcriptOf(fold) {
  const blocks = fold.blocks.map(blockOf);
  const tail = [];
  const login = fold.st.last && fold.st.last.login;
  if (login && login.needed) tail.push(block('signin', 'signin', { provider: login.provider || '', command: login.command || '', methods: [], device: null }));
  return { blocks, tail, plan: blocks.findLast((x) => x.kind === 'plan') || null, usage: fold.st.usage || null };
}

// cached(b, slot, fn): fn()'s value, memoized on block b for its current
// version (the chat kit's, /vendor/chat/blocks.js).
export { cached } from './chat/blocks.js';
