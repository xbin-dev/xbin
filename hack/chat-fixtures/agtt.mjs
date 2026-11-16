// hack/chat-fixtures/agtt.mjs — the agent template's fold as the chat kit's
// block model (/vendor/chat/blocks.js): a run's view (GET /runs/{id}/view,
// as the tile holds it) folded by the template's own model/fold.js and said
// in the model's words by the template's own adapter (model/fold.js blockOf:
// the spec's "agent template" mapping table; a call and a request by
// model/harness-heads.js acpRecord and model/harness-ask.js parkBlock) —
// what its page hands the kit's views. Nothing is added: what the view
// doesn't carry is the zero value and a gap in hack/chat-fixtures/expect.mjs.
// The template's models import the kit by its served URL: load this module
// after hack/xbn/hooks.mjs installHooks().
import { block } from '../../web/chat/blocks.js';
import { fold, blockOf, builtinRecord } from '../../builtin-templates/agent/model/fold.js';
import { callIdOf } from '../../builtin-templates/agent/model/harness-heads.js';
import { parkBlock } from '../../builtin-templates/agent/model/harness-ask.js';

export { callIdOf, builtinRecord };

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
  const blocks = fold(view, childView).map((b) => blockOf(b));
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
