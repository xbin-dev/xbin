// chat-fold.js — a run as the chat shows it: a list of blocks.
//
// Pure (no imports but tool-heads.js, no DOM): node-tested in
// hack/agent-template-chat.test.mjs. The input is a run's view as the tile
// holds it (GET /runs/{id}/view, kept current by the stream), the output is
// what chat-cards.js draws:
//
//   user · notice · think · assistant · tool · agent · step · draft
//
// A tool call and its result are ONE block (paired by call id), and a
// subagent call is an `agent` block carrying the child's own blocks — so a
// subagent's thinking and tools render inside its parent's session.
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
function childOfCall(v, callId) {
  const link = (v.links || []).find((l) => l.toolCallId === callId);
  if (link) return { childId: link.childId, link };
  for (const s of v.steps || []) {
    if (s.kind !== 'spawn') continue;
    const d = parseJSON(s.detail);
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
 * fold(v, childView, depth) → blocks.
 *   v          the run's view: {run, messages, steps, links, draft?}
 *   childView  id → a loaded child view, or null (not loaded yet)
 *   depth      nesting depth (subagents inside subagents)
 */
export function fold(v, childView = () => null, depth = 0) {
  const out = [];
  if (!v) return out;
  const msgs = [...(v.messages || [])].sort((a, b) => a.seq - b.seq || a.id - b.id);
  const results = new Map();
  for (const m of msgs) if (m.role === 'tool') results.set(m.toolCallId, m);

  // Steps interleave with messages by time; at the same second a message
  // comes first (the step is usually about it) — except the run's first
  // message, which a creation note ("started by schedule …") precedes.
  const steps = (v.steps || []).filter((s) => SHOWN_STEPS.has(s.kind) && !(s.kind === 'ask' && parseJSON(s.detail).kind === 'approval'));
  let si = 0;
  const flushSteps = (upto, inclusive) => {
    while (si < steps.length && (upto == null || steps[si].created < upto || (inclusive && steps[si].created === upto))) {
      const s = steps[si++];
      out.push({ k: 'step', id: 's' + s.id, kind: s.kind, detail: parseJSON(s.detail), created: s.created });
    }
  };
  steps.sort((a, b) => a.created - b.created || a.id - b.id);

  let skippedTask = depth === 0; // a subagent's first user message is its task (shown on its card)
  let first = true;
  for (const m of msgs) {
    const opening = first && m.role === 'user' && !m.compacted;
    if (opening) first = false;
    flushSteps(m.created, opening);
    if (m.role === 'system' || m.role === 'tool' || m.compacted) continue;
    if (m.role === 'user') {
      if (!skippedTask) { skippedTask = true; continue; }
      const { text, files } = splitAttachments(m.content);
      if (NOTICE.test(text)) out.push({ k: 'notice', id: 'm' + m.id, text });
      else if (m.origin && ORIGIN_LABEL[m.origin]) {
        // what an automation delivered, not a person typing (D83)
        out.push({ k: 'notice', id: 'm' + m.id, text: `[${ORIGIN_LABEL[m.origin]}${m.label ? ' · ' + m.label : ''}]\n${text}` });
      } else out.push({ k: 'user', id: 'm' + m.id, text, files, msgId: m.id, sender: m.sender || '' });
      continue;
    }
    // assistant
    if (m.reasoning) out.push({ k: 'think', id: 'r' + m.id, text: m.reasoning, ms: m.reasoningMs || 0, live: false });
    if (m.content) out.push({ k: 'assistant', id: 'm' + m.id, text: m.content, model: m.model, usage: m.usage });
    for (const c of m.toolCalls || []) {
      const name = c.function?.name || '';
      const raw = c.function?.arguments || '';
      const res = results.get(c.id);
      const content = res ? res.content : '';
      const base = {
        id: 'c' + c.id, callId: c.id, name, args: raw, headline: headline(name, raw), fam: family(name),
        state: resultState(content), result: content, resultId: res ? res.id : 0, created: m.created,
      };
      if (isSpawn(name)) {
        const found = childOfCall(v, c.id);
        const cv = found ? childView(found.childId) : null;
        const child = cv ? cv.run : found?.link?.child || null;
        out.push({
          ...base, k: 'agent', childId: found ? found.childId : 0, link: found ? found.link : null,
          task: parseArgs(raw).task || '', child,
          state: agentState(found?.link, child),
          // The child's answer (the call's own result may be a receipt, a
          // progress digest, or the answer with a header).
          result: found && found.link && found.link.state !== 'running' ? found.link.result || '' : '',
          blocks: cv ? fold(cv, childView, depth + 1) : null,
          pendingApproval: cv && cv.run && cv.run.status === 'waiting_input' && cv.run.pendingState?.kind === 'approval'
            ? cv.run.pendingState.toolCalls || [] : null,
        });
      } else {
        out.push({ ...base, k: 'tool' });
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
