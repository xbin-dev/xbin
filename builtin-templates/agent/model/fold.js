// model/fold.js — a run as the chat shows it: a list of blocks. (It was
// chat-fold.js, which re-exports it.)
//
// Pure (no imports but the heads and harness.js, no DOM): node-tested in
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
// A coding harness's call (`acp:<kind>`, D147 §4.3.5) is a
// tool block that also carries its tool row's `acp`; the blocks of a
// harness-internal subagent (a Claude Task: `acp.parent`, on its calls and
// on its text's assistant rows as `parent`) go inside that call's block as
// `kids` when the call is held here — an orphan stays flat. An `agent`
// block whose child a harness drives carries the child's `harness` summary.
//
// blockOf() says a block in xbind's chat kit's block model
// (/vendor/chat/blocks.js, D224) — what the web view hands the kit's views
// (chat-kit.js); the fold's own blocks stay what the native view and the
// models read.
//
// With a FoldCache, fold() rebuilds only the blocks whose inputs changed —
// the view holds each message, step and link as an object that an update
// REPLACES (session.js upserts), so "changed" is identity — and hands back
// the very same block objects (and the same list, when nothing changed) for
// the rest: a streamed token re-folds a 1k-message conversation without
// re-deriving a thousand headlines, and a view (the native bridge) can skip
// what is identical. The output is exactly what fold() without a cache makes.
import { headline, family, isSpawn, parseArgs, resultState, subline, outcome, argsShown, ICON } from './tool-heads.js';
import { acpState, acpWords, isAcp, ACP_FAMILY, acpRecord, callIdOf, rawInputOf } from './harness-heads.js';
import { activityLine, nameOf } from './harness.js';
import { block, newToolRecord, toolState, toolDone, up } from '/vendor/chat/blocks.js';

// Engine-written user messages that are not the owner speaking — a person's
// message to a harness child, told its parent (§4.3.13), among them.
const NOTICE = /^\[(subagent results|results of the runs|message from your parent|direct message to #)/;
// Prompts an automation (or a new project's starter) delivered into a
// conversation (messages.meta origin).
const ORIGIN_LABEL = { schedule: 'Scheduled', watch: 'Watcher check', learn: 'Learn a skill', trigger: 'Triggered', starter: 'Project starter' };
// Journal steps worth a line in the chat (the rest is in the transcript).
// `off`: a workspace agent's switch met a message or a turn, answered
// without the model (API.md §Workspace mode, "When the agent's switch is off").
const SHOWN_STEPS = new Set(['note', 'error', 'compaction', 'yield', 'finish', 'render', 'live', 'state_changed', 'cancel', 'ask', 'off']);

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
  // message, which a creation note ("started by schedule …") precedes. A
  // coding agent's conversation has none (automations never start one,
  // D147 §4.2.3): its first prompt's user row is written as it is
  // delivered, and a note that second (a sign-in park's) is about it.
  const creationNote = !(v.run && v.run.engine === 'harness');
  const steps = (v.steps || []).filter((s) => SHOWN_STEPS.has(s.kind) && !(s.kind === 'ask' && detail(s).kind === 'approval'));
  let si = 0;
  const flushSteps = (upto, inclusive) => {
    while (si < steps.length && (upto == null || steps[si].created < upto || (inclusive && steps[si].created === upto))) {
      const s = steps[si++];
      out.push(memo('s' + s.id, [s], () => ({ k: 'step', id: 's' + s.id, kind: s.kind, detail: detail(s), created: s.created, run: s.runId })));
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
    flushSteps(m.created, opening && creationNote);
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
        // a copy brought it from elsewhere (API.md "Shared conversations"): who wrote it there, never a sender here
        const sender = m.origin === 'copy' ? `copied${m.label ? ' · ' + m.label : ''}` : m.sender || '';
        return { k: 'user', id: 'm' + m.id, text, files, msgId: m.id, sender };
      }));
      continue;
    }
    // assistant (a harness-internal subagent's text: under its call)
    const under = m.acp && m.acp.parent ? { parent: m.acp.parent } : null;
    // (with the reasoning tokens its answer's usage reports, when it does: "Thought for 21s · 4.3k tokens", D249)
    if (m.reasoning) out.push(memo('r' + m.id, [m], () => ({ k: 'think', id: 'r' + m.id, text: m.reasoning, ms: m.reasoningMs || 0, live: false,
      ...(m.usage && m.usage.reasoning_tokens > 0 ? { tokens: m.usage.reasoning_tokens } : {}), ...under })));
    if (m.content) out.push(memo('m' + m.id, [m], () => ({ k: 'assistant', id: 'm' + m.id, text: m.content, model: m.model, usage: m.usage, ...under })));
    for (const c of m.toolCalls || []) {
      const name = c.function?.name || '';
      const raw = c.function?.arguments || '';
      const res = results.get(c.id);
      const base = () => {
        const content = res ? res.content : '';
        const acp = res && res.acp ? res.acp : null;
        const parent = (acp && acp.parent) || (under && under.parent) || '';
        // a harness call's words: from its tool row's acp (model/harness-heads.js — xbind's chat kit's headline)
        const words = acp && isAcp(name) ? acpWords(name, parseArgs(raw), acp) : { headline: headline(name, raw), sub: subline(name, raw) };
        return {
          id: 'c' + c.id, callId: c.id, name, args: raw, headline: words.headline, fam: family(name),
          state: acp ? acpState(acp, resultState(content)) : resultState(content), result: content, resultId: res ? res.id : 0, created: m.created,
          // a sandbox call (D115): its command under a summary, and what it came to
          sub: words.sub, outcome: outcome(name, content, acp),
          ...(acp ? { acp } : {}), ...(parent ? { parent } : {}),
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
          task: parseArgs(raw).task || '', child, ...(child && child.engine === 'harness' ? { harness: child.harness || {} } : {}),
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
  const flat = nest(out, memo);

  // The call in flight.
  const d = v.draft;
  if (d) {
    if (d.thinking) flat.push({ k: 'think', id: 'draft-think', text: d.thinking, ms: d.thinkEnd && d.thinkStart ? d.thinkEnd - d.thinkStart : 0, live: !d.thinkEnd, started: d.thinkStart });
    if (d.text) flat.push({ k: 'draft', id: 'draft-text', text: d.text });
    for (const t of Object.values(d.tools || {}).sort((a, b) => a.index - b.index)) {
      flat.push({ k: 'tool', id: 'draft-tool-' + t.index, callId: t.id, name: t.name || '', args: t.args || '',
        headline: headline(t.name, t.args), fam: family(t.name), state: 'writing', result: '', sub: subline(t.name, t.args), outcome: null });
    }
  }
  if (!cache) return flat;
  // forget what is gone; the same blocks in the same order are the same list
  for (const id of cache.blocks.keys()) if (!seen.has(id)) cache.blocks.delete(id);
  for (const id of cache.kids.keys()) if (!kids.has(id)) cache.kids.delete(id);
  if (cache.out && cache.out.length === flat.length && cache.out.every((b, i) => b === flat[i])) return cache.out;
  cache.out = flat;
  return flat;
}

// nest puts the blocks of a harness-internal subagent (b.parent: the call it
// runs under) inside that call's block, as its kids — a new block (memoized
// on the call's block and its kids) with the call's id. A block whose parent
// isn't held here (paged out) stays where it is.
function nest(out, memo) {
  if (!out.some((b) => b.parent)) return out;
  const calls = new Map();
  for (const b of out) if (b.k === 'tool' && b.callId) calls.set(b.callId, b);
  const kids = new Map(); // a call's block → the blocks under it, in order
  const top = [];
  for (const b of out) {
    const p = b.parent && b.parent !== b.callId ? calls.get(b.parent) : null;
    if (p) { if (!kids.has(p)) kids.set(p, []); kids.get(p).push(b); } else top.push(b);
  }
  const placed = new Set();
  const withKids = (b) => {
    placed.add(b);
    const ks = (kids.get(b) || []).filter((k) => !placed.has(k));
    if (!ks.length) return b;
    const inner = ks.map(withKids);
    return memo('n' + b.id, [b, ...inner], () => ({ ...b, kids: inner }));
  };
  const flat = top.map(withKids);
  // a parent loop (never written by the backend) leaves its blocks flat, not lost
  for (const b of out) if (!placed.has(b)) flat.push(b);
  return flat;
}

// activity is the one line under the chat that says what the run is doing.
export function activity(v, blocks) {
  const r = v && v.run;
  if (!r) return '';
  const h = activityLine(r); // a coding harness's run says what its adapter does
  if (h != null) return h;
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

// ---- the blocks as the chat kit's block model ------------------------------------------------
//
//   user · notice · assistant · draft     the same kinds
//   think                                 thought
//   tool                                  tool — its ToolRecord: a coding agent's call from its tool row's acp
//                                         (model/harness-heads.js acpRecord), a tool of the template's own from
//                                         what the fold says of it (builtinRecord), with the fold's words as the
//                                         block's own (its glyph, its command under a summary, what it came to,
//                                         its arguments as rows, its result)
//   agent                                 agent — the child's blocks converted the same way; an approval the
//                                         child waits on is its `tail` (a perm block)
//   step                                  step: the template's own kind (a journal line), drawn by its own view
//
// A converted block is kept with the fold's block it was made of — which the
// fold hands back unchanged until its inputs change — so its version (`v`)
// moves exactly when the block does; the blocks of the call in flight (made
// anew at every fold) take over what the views memoized on the ones before.

// a call of the template's own tools (bash, read_file, …): no ACP record
// exists, so one is made of what the fold says of it
const KIND_OF_FAMILY = Object.fromEntries(Object.entries(ACP_FAMILY).map(([kind, fam]) => [fam, kind]));
const STATUS_OF_STATE = { writing: 'pending', running: 'in_progress', approval: 'pending', waiting: 'in_progress', done: 'completed', error: 'failed', stopped: 'cancelled' };
export function builtinRecord(b) {
  const rec = newToolRecord(b.callId || '');
  rec.kind = KIND_OF_FAMILY[b.fam] || 'other';
  rec.name = b.name || '';
  rec.title = b.headline || '';
  rec.label = b.headline || ''; // the fold's headline is what the card is called (model/tool-heads.js)
  rec.status = STATUS_OF_STATE[b.state] || 'pending';
  rec.rawInput = rawInputOf(parseArgs(b.args));
  rec.text = toolDone(rec.status) ? String(b.result || '') : '';
  return rec;
}

// the template's own approval (its engine asks once for the calls it wants
// to run: Approve / Deny) as a perm block; `park` names the run's park
const OWN_OPTIONS = [{ optionId: 'approve', name: 'Approve', kind: 'allow_once', explicit: false }, { optionId: 'deny', name: 'Deny', kind: 'reject_once', explicit: false }];
export function ownApproval(k, park, calls) {
  const names = (calls || []).map((c) => (c.function ? c.function.name : String(c)));
  return block('perm', k, { pid: '', park, callId: '', tool: { title: names.join(', '), kind: 'other' }, calls: names, options: OWN_OPTIONS.map((o) => ({ ...o })),
    rule: null, plan: null, by: null, optionId: null });
}

let serial = 0;                // a converted block's version: no two alike
const converted = new WeakMap(); // a fold block → its kit block
const lists = new WeakMap();   // a list of fold blocks → the list of theirs
const inFlight = new Map();    // scope + id of a block of the call in flight → its last kit block

function convert(b, scope) {
  const under = b.parent ? { parent: callIdOf(b.parent) } : {};
  switch (b.k) {
    case 'user':
      return block('user', b.id, { text: b.text, sender: b.sender || '', ref: b.msgId,
        files: (b.files || []).map((f) => ({ name: String(f.path).split('/').pop(), path: f.path, mime: f.mime || '', size: null, sizeText: f.size || '' })) });
    // (an engine-written message, an automation's prompt: folded to its heading — the first line without its brackets)
    case 'notice': return block('notice', b.id, { text: b.text, fold: true, title: String(b.text || '').split('\n')[0].replace(/^\[|\]$/g, '').replace(/ — .*$/, '') });
    case 'think': return block('thought', b.id, { text: b.text, live: !!b.live, ms: b.ms || 0, at: b.started || 0, ...(b.tokens ? { tokens: b.tokens } : {}), ...under });
    case 'assistant': return block('assistant', b.id, { text: b.text, model: b.model || '', usage: b.usage || null, ...under });
    case 'draft': return block('draft', b.id, { text: b.text });
    case 'tool': {
      const harness = !!b.acp || isAcp(b.name);
      const rec = harness ? acpRecord(b, parseArgs(b.args)) : builtinRecord(b);
      // the row's placeholder says running, parked or settled some other way; ACP's status says the rest
      const state = toolDone(rec.status) ? toolState(rec) : b.state === 'running' ? toolState(rec) : b.state;
      const out = block('tool', b.id, { rec, state, children: rec.subagent ? [] : null, result: harness ? '' : b.result || '', at: (b.created || 0) * 1000, ...under,
        // a tool of the template's own: its family's glyph, the fold's words, its arguments a row each
        ...(harness ? {} : { icon: ICON[b.fam] || '', sub: b.sub || '', outcome: b.outcome || null, args: argsShown(b.args) }) });
      if (b.kids) out.children = b.kids.map((c) => up(blockOf(c, scope + b.id + '/'), out));
      return out;
    }
    case 'agent': {
      const child = b.child || {};
      return block('agent', b.id, {
        title: (b.link && b.link.label) || b.headline || '', ref: b.childId || 0, state: b.state, phase: (b.link && b.link.phase) || child.status || '',
        steps: child.llmCalls || 0, task: b.task || '', answer: String(b.result || '').replace(/^--- #\d+ .*? ---\n/, ''),
        blocks: b.blocks ? blocksOf(b.blocks, scope + b.id + '/') : null,
        // (the child's run rides along: a host answers the request on it)
        tail: b.pendingApproval ? [Object.defineProperty(ownApproval(b.id + ':park', '', b.pendingApproval), '$run', { value: b.child || null })] : [],
        provider: b.harness ? nameOf(b.harness) : '',
      });
    }
  }
  // step (a journal line): the template's own kind
  return block(b.k, b.id, { step: b.kind, detail: b.detail, run: b.run });
}

/**
 * blockOf(b, scope) → the kit's block for a fold block (which it keeps as
 * `$src`): the same object while the fold hands back the same block. scope: where a nested list sits
 * (a child's blocks under its card), for the blocks of a call in flight.
 */
export function blockOf(b, scope = '') {
  if (!b) return null;
  let out = converted.get(b);
  if (out) return out;
  out = convert(b, scope);
  out.v = ++serial;
  Object.defineProperty(out, '$src', { value: b }); // the fold's block it says (a host's view may need its own fields)
  if (String(b.id).startsWith('draft-')) {
    const prev = inFlight.get(scope + b.id);
    if (prev && prev.$memo) out.$memo = prev.$memo; // the paragraphs already parsed
    inFlight.set(scope + b.id, out);
  }
  converted.set(b, out);
  return out;
}

/** blocksOf(list, scope) → the kit's blocks of a fold's list (the same list while the fold's is). */
export function blocksOf(list, scope = '') {
  if (!list) return [];
  let out = lists.get(list);
  if (!out) lists.set(list, (out = list.map((b) => blockOf(b, scope))));
  return out;
}
