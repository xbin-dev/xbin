// hack/chat-fixtures/agtt.mjs — the agent template's fold as the chat kit's
// block model (/vendor/chat/blocks.js): a run's view (GET /runs/{id}/view,
// as the tile holds it) folded by the template's own model/fold.js, then
// each of its blocks said in the model's words. The spec's "agent template"
// mapping table is this file; it is what model/fold.js's adapter to the
// block model starts from. Nothing is added: what the view doesn't carry is
// the zero value here and a gap in hack/chat-fixtures/gaps.mjs.
import { block, newToolRecord, toolState, toolDone, up } from '../../web/chat/blocks.js';
import { fold } from '../../builtin-templates/agent/model/fold.js';
import { parseArgs } from '../../builtin-templates/agent/model/tool-heads.js';
import { isAcp, ACP_FAMILY } from '../../builtin-templates/agent/model/harness-heads.js';
import { nameOf } from '../../builtin-templates/agent/model/harness.js';

// the agent's own id of a stored call id: "h<gen>:<id>", "h<gen>:<id>#<n>"
// for its n-th use (harness_map.go sid) — an id that itself ends in #<n>
// can't be told from a reuse
export const callIdOf = (stored) => String(stored || '').replace(/^h\d+:/, '').replace(/#\d+$/, '');

// rawInputOf: a call's arguments back as the agent's rawInput (harness_map.go
// callsJSON: its fields plus `summary`; a rawInput `summary` is rawSummary;
// one that isn't an object is {summary, rawInput}) — no arguments and an
// empty object are both null
export function rawInputOf(args) {
  const { summary: _s, rawSummary, ...rest } = parseArgs(args);
  if ('rawInput' in rest && Object.keys(rest).length === 1 && (rest.rawInput == null || typeof rest.rawInput !== 'object' || Array.isArray(rest.rawInput))) return rest.rawInput;
  if (rawSummary !== undefined) rest.summary = rawSummary;
  return Object.keys(rest).length ? rest : null;
}

// textOf: a harness call's text content, read back from its tool row — the
// row holds it only when it has nothing better to say (harness_map.go
// rowText: an edit's row is "edited …", a command's its output; a failed
// call's is "error: …") and the lift (`acp`) leaves it out
function textOf(b, acp) {
  const c = String(b.result || '');
  if (!toolDone(acp.status) || acp.status === 'cancelled') return '';
  if (acp.status === 'failed') {
    const m = c.startsWith('error: ') ? c.slice(7) : '';
    return m === 'the call failed' || (acp.output && acp.output.endsWith(m)) ? '' : m;
  }
  if ((acp.diffs && acp.diffs.length) || acp.output || acp.exitCode != null) return '';
  return c === (acp.title || 'done') ? '' : c;
}

// agttRecord: a harness call's block (its tool row's `acp`, its arguments)
// as a ToolRecord
export function agttRecord(b) {
  const acp = b.acp || {};
  const rec = newToolRecord(callIdOf(b.callId));
  rec.kind = acp.kind || 'other';
  rec.title = acp.title || '';
  rec.label = acp.label || '';
  rec.name = acp.tool || '';
  rec.status = acp.status || 'pending';
  rec.parent = callIdOf(acp.parent);
  rec.subagent = !!acp.subagent;
  rec.planReview = !!acp.planReview;
  rec.rawInput = rawInputOf(b.args);
  rec.locations = (acp.locations || []).map((l) => (l.line != null ? { path: l.path, line: l.line } : { path: l.path }));
  rec.files = [...(acp.files || [])];
  rec.text = textOf(b, acp);
  rec.terminal = typeof acp.output === 'string'; // (all it knows of one: output or an exit code came)
  rec.output = acp.output || '';
  rec.outputTruncated = Number(acp.outputTruncated) || 0;
  rec.exitCode = acp.exitCode ?? null;
  rec.diffs = (acp.diffs || []).map((d) => ({ path: d.path, status: d.status || 'modified', add: d.add || 0, del: d.del || 0, patch: d.patch || '', truncated: !!d.truncated }));
  return rec;
}

// a call of the template's own tools (bash, read_file, …): no ACP record
// exists, so one is made of what the fold says of it
const KIND_OF_FAMILY = Object.fromEntries(Object.entries(ACP_FAMILY).map(([kind, fam]) => [fam, kind]));
const STATUS_OF_STATE = { writing: 'pending', running: 'in_progress', approval: 'pending', waiting: 'in_progress', done: 'completed', error: 'failed', stopped: 'cancelled' };
export function builtinRecord(b) {
  const rec = newToolRecord(b.callId || '');
  rec.kind = KIND_OF_FAMILY[b.fam] || 'other';
  rec.name = b.name || '';
  rec.title = b.headline || '';
  rec.status = STATUS_OF_STATE[b.state] || 'pending';
  rec.rawInput = rawInputOf(b.args);
  rec.text = toolDone(rec.status) ? String(b.result || '') : '';
  return rec;
}

function agttBlock(b, parent) {
  let out;
  const under = b.parent ? { parent: callIdOf(b.parent) } : {};
  switch (b.k) {
    case 'user':
      out = block('user', b.id, { text: b.text, sender: b.sender || '', ref: b.msgId,
        files: (b.files || []).map((f) => ({ name: String(f.path).split('/').pop(), path: f.path, mime: f.mime || '', size: null, sizeText: f.size || '' })) });
      break;
    case 'notice':
      out = block('notice', b.id, { text: b.text });
      break;
    case 'think':
      out = block('thought', b.id, { text: b.text, live: !!b.live, ms: b.ms || 0, at: b.started || 0, ...under });
      break;
    case 'assistant':
      out = block('assistant', b.id, { text: b.text, model: b.model || '', usage: b.usage || null, ...under });
      break;
    case 'draft':
      out = block('draft', b.id, { text: b.text });
      break;
    case 'tool': {
      const harness = !!b.acp || isAcp(b.name);
      const rec = harness ? agttRecord(b) : builtinRecord(b);
      // the row's placeholder says running, parked or settled some other way; ACP's status says the rest
      const state = toolDone(rec.status) ? toolState(rec) : b.state === 'running' ? toolState(rec) : b.state;
      out = block('tool', b.id, { rec, state, children: rec.subagent ? [] : null, result: harness ? '' : b.result || '', at: (b.created || 0) * 1000, ...under });
      if (b.kids) out.children = b.kids.map((c) => agttBlock(c, out));
      break;
    }
    case 'agent': {
      const child = b.child || {};
      out = block('agent', b.id, {
        title: (b.link && b.link.label) || b.headline || '', ref: b.childId || 0, state: b.state, phase: (b.link && b.link.phase) || child.status || '',
        steps: child.llmCalls || 0, task: b.task || '', answer: String(b.result || '').replace(/^--- #\d+ .*? ---\n/, ''),
        blocks: b.blocks ? b.blocks.map((c) => agttBlock(c, null)) : null,
        tail: b.pendingApproval ? [ownApproval(b.id + ':park', '', b.pendingApproval)] : [],
        provider: b.harness ? nameOf(b.harness) : '',
      });
      break;
    }
    default: // step (a journal line): the template's own kind, drawn by its own view
      out = block(b.k, b.id, { step: b.kind, detail: b.detail, run: b.run });
  }
  if (parent) up(out, parent);
  return out;
}

// the template's own approval (its engine asks once for the calls it wants
// to run: Approve / Deny, chat-cards.js approvalTpl)
const OWN_OPTIONS = [{ optionId: 'approve', name: 'Approve', kind: 'allow_once', explicit: false }, { optionId: 'deny', name: 'Deny', kind: 'reject_once', explicit: false }];
function ownApproval(k, park, calls) {
  const names = (calls || []).map((c) => (c.function ? c.function.name : String(c)));
  return block('perm', k, { pid: '', park, callId: '', tool: { title: names.join(', '), kind: 'other' }, calls: names, options: OWN_OPTIONS.map((o) => ({ ...o })),
    rule: null, plan: null, by: null, optionId: null });
}

// what a view's run is parked on, as the block that asks: a coding agent's
// request (pendingState.harness, D147 §4.3.4), else the template's own
// approval or question
function parkBlock(run) {
  const ps = run.status === 'waiting_input' ? run.pendingState : null;
  if (!ps) return null;
  const k = 'park:' + (ps.park || '');
  const hs = ps.harness;
  if (!hs) {
    if (ps.kind === 'approval') return ownApproval(k, ps.park || '', ps.toolCalls);
    return run.result ? block('ask', k, { eid: '', park: ps.park || '', callId: '', message: run.result, mode: 'text', schema: null, action: null, by: null, content: null }) : null;
  }
  if (ps.kind === 'approval') {
    const t = hs.tool || {};
    return block('perm', k, {
      pid: hs.pid || '', park: ps.park || '', callId: callIdOf(hs.callId),
      tool: { title: t.title || '', kind: t.kind || '', name: t.name || '', label: t.label || '', command: t.command || '', rawInput: t.rawInput ?? null, content: t.content ?? null },
      options: (hs.options || []).map((o) => ({ optionId: o.optionId, name: o.name, kind: o.kind, explicit: !!o.explicit })),
      rule: hs.rule ? { kind: hs.rule.kind || '', title: hs.rule.title || '' } : null,
      title: '', description: hs.description || '', defaultToNo: !!hs.defaultToNo,
      plan: hs.planApproval ? { text: hs.plan || '', path: t.rawInput && typeof t.rawInput.planFilePath === 'string' ? t.rawInput.planFilePath : '' } : null,
      by: null, optionId: null,
    });
  }
  if (ps.kind === 'question') {
    const url = hs.url || '';
    return block('ask', k, { eid: hs.eid || '', park: ps.park || '', callId: callIdOf(hs.callId), message: hs.message || '', mode: url || hs.mode === 'url' ? 'url' : 'form',
      schema: hs.schema ?? null, url, action: null, by: null, content: null });
  }
  if (ps.kind === 'login') {
    const l = hs.login || {};
    return block('signin', k, { park: ps.park || '', provider: nameOf(run.harness), command: l.command || '', methods: l.methods || [], device: l.device || null });
  }
  return null;
}

// the tile's view of what GET /runs/{id}/view answers: its run's draft (model/session.js draftOf)
export function tileView(served) {
  const d = (served.drafts || []).find((x) => x.run === served.run.id);
  const tools = {};
  for (const t of (d && d.tools) || []) tools[t.index] = t;
  return { ...served, draft: d ? { text: d.text || '', thinking: d.thinking || '', thinkStart: d.thinkStart || 0, thinkEnd: d.thinkEnd || 0, tools } : null };
}

/**
 * agttTranscript(view, childView) → the Transcript of a run's view: its
 * fold's blocks; in `tail` what the run is parked on (the template draws
 * that under the transcript, and nothing once it is answered); the plan and
 * the usage of its coding agent's summary (the template pins them in its
 * top bar).
 */
export function agttTranscript(view, childView = () => null) {
  const blocks = fold(view, childView).map((b) => agttBlock(b, null));
  const run = view.run || {};
  const h = run.harness || {};
  const park = parkBlock(run);
  const entries = h.plan && Array.isArray(h.plan.entries) ? h.plan.entries : null;
  return {
    blocks, tail: park ? [park] : [],
    plan: entries ? block('plan', 'plan', { entries: entries.map((e) => ({ content: e.content, status: e.status, priority: e.priority || '' })) }) : null,
    usage: h.usage || null,
  };
}
