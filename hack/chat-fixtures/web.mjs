// hack/chat-fixtures/web.mjs — the Agent tab's fold as the chat kit's block
// model (/vendor/chat/blocks.js): what web/agent-fold.js holds today,
// field by field, with nothing added — where today's fold has dropped
// something the model asks for, the block says its zero value and
// hack/chat-fixtures/expect.mjs names the gap. The spec's "Agent tab"
// mapping table is this file; when the fold makes blocks itself, this goes.
// A call's record is the fold's own (the kit's ToolRecord, kept by
// /vendor/chat/tools.js mergeTool beside the tab's old record).
import { block, toolState, up } from '../../web/chat/blocks.js';
import { isPlanApproval, planText, planPath, inputCommand, changeSet } from '../../web/chat/tools.js';

function webBlock(b, parent) {
  const k = b.key;
  let out;
  switch (b.kind) {
    case 'msg':
      out = b.role === 'user'
        ? block('user', k, { text: b.text, files: (b.files || []).map((f) => ({ name: f.name, mime: f.mime || '', size: f.size ?? null })), steered: !!b.steered })
        : block('assistant', k, { text: b.text, mid: b.mid });
      break;
    case 'thought':
      out = block('thought', k, { text: b.text, live: !b.done, ms: Math.max(0, (b.t1 || 0) - (b.t0 || 0)), at: b.t0 || 0 });
      break;
    case 'tool': {
      const rec = b.rec;
      out = block('tool', k, { rec, state: toolState(rec), children: null, at: b.t0 || 0 });
      if (b.children) out.children = b.children.map((c) => webBlock(c, out));
      break;
    }
    case 'changes':
      out = block('changes', k, { turn: b.turn, set: changeSet({ changes: b.changes, patch: b.patch }) });
      break;
    case 'plan':
      out = block('plan', k, { entries: (b.entries || []).map((e) => ({ content: e.content, status: e.status, priority: e.priority || '' })) });
      break;
    case 'perm': {
      const tc = b.tool || {};
      const meta = b.meta || {};
      const plan = isPlanApproval(tc);
      out = block('perm', k, {
        pid: b.pid, callId: tc.id || '',
        tool: { title: tc.title || '', kind: tc.kind || '', name: tc.name || '', label: '', command: inputCommand(tc.rawInput), rawInput: tc.rawInput ?? null, content: tc.content ?? null },
        options: (b.options || []).map((o) => ({ optionId: o.optionId, name: o.name, kind: o.kind, explicit: false })),
        rule: b.rule && b.rule.scoped !== false && (b.rule.kind || b.rule.title) ? { kind: b.rule.kind || '', title: b.rule.title || '' } : null,
        title: meta.title || '', description: meta.description || '', defaultToNo: !!meta.defaultToNo,
        plan: plan ? { text: planText(tc), path: planPath(tc) } : null,
        by: b.by ?? null, optionId: b.optionId ?? null,
      });
      break;
    }
    case 'ask':
      out = block('ask', k, { eid: b.eid, callId: b.toolCallId || '', message: b.message, mode: b.mode, schema: b.schema, url: b.url, action: b.action ?? null, by: b.by ?? null, content: b.content ?? null });
      break;
    case 'turn':
      out = block('turn', k, { turn: b.turn, stopReason: b.stopReason || '', usage: b.usage || null, error: b.error || '' });
      break;
    case 'notice':
      out = block('notice', k, { text: b.text });
      break;
    case 'gap':
      out = block('gap', k, {});
      break;
    default:
      out = block(b.kind, k, {});
  }
  out.v = b.v;
  if (parent) { up(out, parent); out.parent = parent.rec.id; }
  return out;
}

/**
 * webTranscript(fold) → the Transcript of a Fold (web/agent-fold.js): its
 * blocks; in `tail` the sign-in the last status asks for (the tab draws it
 * as a banner under the transcript); the newest plan; the last turn's usage.
 */
export function webTranscript(fold) {
  const blocks = fold.blocks.map((b) => webBlock(b, null));
  const tail = [];
  const login = fold.st.last && fold.st.last.login;
  if (login && login.needed) tail.push(block('signin', 'signin', { provider: login.provider || '', command: login.command || '', methods: [], device: null }));
  const plan = blocks.findLast((b) => b.kind === 'plan') || null;
  return { blocks, tail, plan, usage: fold.st.usage || null };
}
