// model/fold.js — a run as the chat shows it: a list of blocks. (It was
// chat-fold.js, which re-exports it.)
//
// Pure (no imports but tool-heads.js, no DOM): node-tested in
// hack/agent-template-chat.test.mjs. The input is a run's view as the tile
// holds it (GET /runs/{id}/view, kept current by the stream), the output is
// what the views draw (chat-cards.js on the web):
//
//   user · notice · think · assistant · tool · agent · step · draft
//
// A tool call and its result are ONE block (paired by call id), and a
// subagent call is an `agent` block carrying the child's own blocks — so a
// subagent's thinking and tools render inside its parent's session.
//
// With a FoldCache, fold() rebuilds only the blocks whose inputs changed —
// the view holds each message, step and link as an object that an update
// REPLACES (session.js upserts), so "changed" is identity — and hands back
// the very same block objects (and the same list, when nothing changed) for
// the rest: a streamed token re-folds a 1k-message conversation without
// re-deriving a thousand headlines, and a view (the native bridge) can skip
// what is identical. The output is exactly what fold() without a cache makes.
import { headline, family, isSpawn, parseArgs, resultState } from './tool-heads.js';

// Engine-written user messages that are not the owner speaking.
const NOTICE = /^\[(subagent results|results of the runs|message from your parent)/;
// Prompts an automation delivered into a conversation (messages.meta origin).
const ORIGIN_LABEL = { schedule: 'Scheduled', watch: 'Watcher check', learn: 'Learn a skill', trigger: 'Triggered' };
// Journal steps worth a line in the chat (the rest is in the transcript).
const SHOWN_STEPS = new Set(['note', 'error', 'compaction', 'yield', 'finish', 'render', 'state_changed', 'cancel', 'ask']);

const ATTACH_NOTE = /\n\n\[attached: ([^\]]*)\]$/;
const ATTACH_ITEM = /([A-Za-z0-9._/-]+) \(([^,()]+), ([^()]+)\)/g;

// splitAttachments peels the "[attached: …]" note the backend appends for
// the model off a user message.
export function splitAttachments(content) {
  const c = String(content ?? '');
  const hit = ATTACH_NOTE.exec(c);
  if (!hit) return { text: c, files: [] };
  const files = [...hit[1].matchAll(ATTACH_ITEM)].map(([, path, mime, size]) => ({ path, mime, size }));
  const text = c.slice(0, hit.index);
  return { text: text === '(see attached)' ? '' : text, files };
}

// childOfCall finds the subagent a spawn call started: its link (foreground
// spawns carry the call id) or the spawn step (background ones).
function childOfCall(v, callId, detail = (s) => parseJSON(s.detail)) {
  const link = (v.links || []).find((l) => l.toolCallId === callId);
  if (link) return { childId: link.childId, link };
  for (const s of v.steps || []) {
    if (s.kind !== 'spawn') continue;
    const d = detail(s);
    if (d.toolCallId === callId && d.runId) {
      const l = (v.links || []).filter((x) => x.childId === d.runId).pop();
      return { childId: d.runId, link: l || null };
    }
  }
  return null;
}

function parseJSON(s) {
  if (s && typeof s === 'object') return s;
  try { return JSON.parse(s || '{}') || {}; } catch { return {}; }
}

/**
 * FoldCache is what fold() remembers between calls for one run: each block
 * by id with the inputs it was made from, a cache per subagent shown inside
 * it, the steps' parsed details, and the list it last returned. Keep one per
 * run you fold (session.js does); `stats` counts blocks built and reused.
 */
export class FoldCache {
  constructor(stats = { built: 0, reused: 0 }) {
    this.blocks = new Map();       // block id → {deps, loose, block}
    this.kids = new Map();         // child run id → its FoldCache
    this.details = new WeakMap();  // step → its parsed detail
    this.out = null;               // the last list returned
    this.stats = stats;
  }

  kid(id) {
    let k = this.kids.get(id);
    if (!k) { k = new FoldCache(this.stats); this.kids.set(id, k); }
    return k;
  }

  detail(s) {
    let d = this.details.get(s);
    if (!d) { d = parseJSON(s.detail); this.details.set(s, d); }
    return d;
  }
}

// inOrder is list sorted by cmp — the list itself when it already is (the
// usual case: a transcript arrives in order), never sorted in place.
function inOrder(list, cmp) {
  for (let i = 1; i < list.length; i++) if (cmp(list[i - 1], list[i]) > 0) return [...list].sort(cmp);
  return list;
}

// shallowSame: the same object, or objects with the same own values.
function shallowSame(a, b) {
  if (a === b) return true;
  if (!a || !b || typeof a !== 'object' || typeof b !== 'object') return false;
  const ka = Object.keys(a);
  return ka.length === Object.keys(b).length && ka.every((k) => Object.is(a[k], b[k]));
}

// agentState is a subagent card's status word.
function agentState(link, child) {
  if (child && ['running', 'awaiting', 'sleeping'].includes(child.status)) return 'running';
  if (child && child.status === 'waiting_input') return 'approval';
  if (!link) return child ? (child.status === 'error' ? 'error' : 'done') : 'running';
  if (link.state === 'running') return 'running';
  if (link.state === 'error') return 'error';
  if (link.state === 'canceled') return 'stopped';
  return 'done';
}

/**
 * fold(v, childView, depth, cache) → blocks.
 *   v          the run's view: {run, messages, steps, links, draft?}
 *   childView  id → a loaded child view, or null (not loaded yet)
 *   depth      nesting depth (subagents inside subagents)
 *   cache      a FoldCache for this run (optional): rebuild only what changed
 */
export function fold(v, childView = () => null, depth = 0, cache = null) {
  const out = [];
  if (!v) return out;
  const detail = cache ? (s) => cache.detail(s) : (s) => parseJSON(s.detail);
  const seen = cache && new Set();
  const kids = cache && new Set();
  // memo is one block: the cached one while its inputs are the same objects
  // (deps, compared by identity; loose, compared value by value), else built.
  const memo = (id, deps, make, loose) => {
    if (!cache) return make();
    seen.add(id);
    const e = cache.blocks.get(id);
    if (e && e.deps.length === deps.length && e.deps.every((d, i) => Object.is(d, deps[i])) && shallowSame(e.loose, loose)) {
      cache.stats.reused++;
      return e.block;
    }
    const block = make();
    cache.blocks.set(id, { deps, loose, block });
    cache.stats.built++;
    return block;
  };
  const msgs = inOrder(v.messages || [], (a, b) => a.seq - b.seq || a.id - b.id);
  const results = new Map();
  for (const m of msgs) if (m.role === 'tool') results.set(m.toolCallId, m);

  // Steps interleave with messages by time; at the same second a message
  // comes first (the step is usually about it) — except the run's first
  // message, which a creation note ("started by schedule …") precedes.
  const steps = (v.steps || []).filter((s) => SHOWN_STEPS.has(s.kind) && !(s.kind === 'ask' && detail(s).kind === 'approval'));
  let si = 0;
  const flushSteps = (upto, inclusive) => {
    while (si < steps.length && (upto == null || steps[si].created < upto || (inclusive && steps[si].created === upto))) {
      const s = steps[si++];
      out.push(memo('s' + s.id, [s], () => ({ k: 'step', id: 's' + s.id, kind: s.kind, detail: detail(s), created: s.created })));
    }
  };
  steps.sort((a, b) => a.created - b.created || a.id - b.id); // a filtered copy: sorting it in place is fine

  // A subagent's first user message is its task (shown on its card). A paged
  // view with older pages holds neither the task nor the run's opening message.
  let skippedTask = depth === 0 || !!v.hasOlder;
  let first = !v.hasOlder;
  for (const m of msgs) {
    const opening = first && m.role === 'user' && !m.compacted;
    if (opening) first = false;
    flushSteps(m.created, opening);
    if (m.role === 'system' || m.role === 'tool' || m.compacted) continue;
    if (m.role === 'user') {
      if (!skippedTask) { skippedTask = true; continue; }
      out.push(memo('m' + m.id, [m], () => {
        const { text, files } = splitAttachments(m.content);
        if (NOTICE.test(text)) return { k: 'notice', id: 'm' + m.id, text };
        if (m.origin && ORIGIN_LABEL[m.origin]) {
          // what an automation delivered, not a person typing (D83)
          return { k: 'notice', id: 'm' + m.id, text: `[${ORIGIN_LABEL[m.origin]}${m.label ? ' · ' + m.label : ''}]\n${text}` };
        }
        return { k: 'user', id: 'm' + m.id, text, files, msgId: m.id, sender: m.sender || '' };
      }));
      continue;
    }
    // assistant
    if (m.reasoning) out.push(memo('r' + m.id, [m], () => ({ k: 'think', id: 'r' + m.id, text: m.reasoning, ms: m.reasoningMs || 0, live: false })));
    if (m.content) out.push(memo('m' + m.id, [m], () => ({ k: 'assistant', id: 'm' + m.id, text: m.content, model: m.model, usage: m.usage })));
    for (const c of m.toolCalls || []) {
      const name = c.function?.name || '';
      const raw = c.function?.arguments || '';
      const res = results.get(c.id);
      const base = () => {
        const content = res ? res.content : '';
        return {
          id: 'c' + c.id, callId: c.id, name, args: raw, headline: headline(name, raw), fam: family(name),
          state: resultState(content), result: content, resultId: res ? res.id : 0, created: m.created,
        };
      };
      if (isSpawn(name)) {
        const found = childOfCall(v, c.id, detail);
        const cv = found ? childView(found.childId) : null;
        const child = cv ? cv.run : found?.link?.child || null;
        if (cv && cache) kids.add(found.childId);
        const blocks = cv ? fold(cv, childView, depth + 1, cache ? cache.kid(found.childId) : null) : null;
        out.push(memo('c' + c.id, [m, res, found ? found.childId : 0, found ? found.link : null, blocks], () => ({
          ...base(), k: 'agent', childId: found ? found.childId : 0, link: found ? found.link : null,
          task: parseArgs(raw).task || '', child,
          state: agentState(found?.link, child),
          // The child's answer (the call's own result may be a receipt, a
          // progress digest, or the answer with a header).
          result: found && found.link && found.link.state !== 'running' ? found.link.result || '' : '',
          blocks,
          pendingApproval: cv && cv.run && cv.run.status === 'waiting_input' && cv.run.pendingState?.kind === 'approval'
            ? cv.run.pendingState.toolCalls || [] : null,
        }), child));
      } else {
        out.push(memo('c' + c.id, [m, res], () => ({ ...base(), k: 'tool' })));
      }
    }
  }
  flushSteps(null);

  // The call in flight.
  const d = v.draft;
  if (d) {
    if (d.thinking) out.push({ k: 'think', id: 'draft-think', text: d.thinking, ms: d.thinkEnd && d.thinkStart ? d.thinkEnd - d.thinkStart : 0, live: !d.thinkEnd, started: d.thinkStart });
    if (d.text) out.push({ k: 'draft', id: 'draft-text', text: d.text });
    for (const t of Object.values(d.tools || {}).sort((a, b) => a.index - b.index)) {
      out.push({ k: 'tool', id: 'draft-tool-' + t.index, callId: t.id, name: t.name || '', args: t.args || '',
        headline: headline(t.name, t.args), fam: family(t.name), state: 'writing', result: '' });
    }
  }
  if (!cache) return out;
  // forget what is gone; the same blocks in the same order are the same list
  for (const id of cache.blocks.keys()) if (!seen.has(id)) cache.blocks.delete(id);
  for (const id of cache.kids.keys()) if (!kids.has(id)) cache.kids.delete(id);
  if (cache.out && cache.out.length === out.length && cache.out.every((b, i) => b === out[i])) return cache.out;
  cache.out = out;
  return out;
}

// activity is the one line under the chat that says what the run is doing.
export function activity(v, blocks) {
  const r = v && v.run;
  if (!r) return '';
  const d = v.draft;
  switch (r.status) {
    case 'running': {
      if (d) {
        if (Object.keys(d.tools || {}).length) return 'Writing a tool call…';
        if (d.text) return 'Writing…';
        if (d.thinking && !d.thinkEnd) return 'Thinking…';
        return 'Waiting for the model…';
      }
      const run = [...(blocks || [])].reverse().find((b) => (b.k === 'tool' || b.k === 'agent') && b.state === 'running');
      return run ? `Running: ${run.headline}` : 'Working…';
    }
    case 'awaiting':
      return r.pendingState && r.pendingState.kind === 'deps' ? 'Waiting for the runs it was started after' : 'Waiting for subagents…';
    case 'sleeping':
      return r.wakeAt ? `Sleeping until ${new Date(r.wakeAt * 1000).toLocaleTimeString()}` : 'Sleeping';
    case 'waiting_input':
      return r.pendingState && r.pendingState.kind === 'approval' ? 'Waiting for your approval' : 'Waiting for your answer';
  }
  return '';
}

// busy reports statuses in which a message is queued rather than answered
// at once, and Stop makes sense.
export const busy = (status) => ['running', 'awaiting', 'sleeping'].includes(status);
