// native/blocks.js — a conversation's rows the way the chat family draws
// them, from the model's blocks (model/fold.js): the model's thinking, its
// answer and the one being written, a tool call (its arguments, its result
// cut where a long one is, a coding harness subagent's blocks inside), a
// subagent's card with its own transcript, a folded notice. Both native
// views draw with these: the template's (native/chat.js, which adds the
// seams, the journal lines and its own messages) and a workspace agent's
// (native/ws-chat.js, which sets its cards among them).
//
// What a row needs of its host is in ctx (native/ui.js): ctx.app.session
// (what is open: its ui map; a subagent's child read), ctx.app.sel (the open
// run, for a call opened in full) and ctx.paint.
import { html, repeat, nothing, native } from '/vendor/xb-native.js';
import { ctx, push, secs, clip, cardState, FAMILY_ICON } from './ui.js';
import { argsShown } from '../model/tool-heads.js';

export const CUT = 1200; // a long result is cut here; the card's ↗ opens all of it

// isOpen/setOpen keep a card's open state where the web keeps it (the
// session's ui map), so a streamed token never folds what you opened.
export const isOpen = (id, dflt) => ctx.app.session.ui.isOpen(id, dflt);
export const setOpen = (id) => (e) => { ctx.app.session.open.set(id, !!e.open); ctx.paint(); };

// The engine's and the automations' notices fold like the web's (↵ head).
const NOTICE_ICON = { Scheduled: 'clock', 'Watcher check': 'eye', 'Learn a skill': 'doc', Triggered: 'bolt' };

// blockTpl: a block one of these rows holds inside (a subagent's steps), when
// the host gives no drawing of its own.
function blockTpl(b, depth = 0) {
  switch (b.k) {
    case 'think': return thinkTpl(b);
    case 'assistant': return assistantTpl(b);
    case 'draft': return draftTpl(b);
    case 'tool': return toolTpl(b, depth);
    case 'agent': return agentTpl(b, depth);
    case 'notice': return noticeTpl(b);
  }
  return nothing;
}

const copyRich = () => { try { return !!native.supports?.('button', 2); } catch { return false; } };

export const thinkTpl = (b) => html`<thinking text=${b.text} ?live=${b.live} seconds=${b.live ? nothing : b.ms ? secs(b.ms) : nothing}
  open=${isOpen(b.id, b.live)} @toggle=${setOpen(b.id)}/>`;
export const assistantTpl = (b) => html`<message role="assistant" markdown text=${b.text}>
  <actions><button icon="copy" copy=${b.text} copyFormat=${copyRich() ? 'markdown' : nothing}>Copy</button></actions></message>`;
export const draftTpl = (b) => html`<message role="assistant" markdown streaming text=${b.text}/>`;

export function noticeTpl(b) {
  const lines = b.text.split('\n');
  const head = lines[0].replace(/^\[|\]$/g, '').replace(/ — .*$/, '');
  const label = head.split(' · ')[0];
  return html`<toolcard title=${'↵ ' + head} icon=${NOTICE_ICON[label] || 'mail'} family="notice"
      open=${isOpen(b.id, false)} @toggle=${setOpen(b.id)}>
    <text selectable>${lines.slice(1).join('\n')}</text>
  </toolcard>`;
}

// argRows: short arguments as one "key: value" block, long ones each their
// own — parsed once per block object (the fold hands back the same block
// until its call changes; a call being written is a new one each time).
const argMemo = new WeakMap();
function argRows(b) {
  let t = argMemo.get(b);
  if (t) return t;
  const short = [], long = [];
  for (const [k, v] of Object.entries(argsShown(b.args))) {
    const s = typeof v === 'string' ? v : JSON.stringify(v, null, 2);
    (s.length > 80 || s.includes('\n') ? long : short).push([k, s]);
  }
  t = html`${short.length ? html`<text mono selectable>${short.map(([k, s]) => `${k}: ${s}`).join('\n')}</text>` : nothing}
    ${repeat(long, ([k]) => k, ([k, s]) => html`<text style="caption" tone="muted">${k}</text><code text=${clip(s, CUT * 4)}/>`)}`;
  argMemo.set(b, t);
  return t;
}

// A sandbox call's outcome (model/tool-heads.js outcome) as a chip's tone.
const OUTCOME_TONE = { ok: 'ok', bad: 'danger', run: 'accent' };

export function toolTpl(b, depth = 0, draw = blockTpl) {
  const { state, chip } = cardState(b.state);
  const long = b.result && b.result.length > CUT;
  const done = b.result && b.state !== 'running';
  // a sandbox call (▣) says what it came to on the card, and its command inside
  const oc = b.outcome ? [{ text: b.outcome.text, ...(OUTCOME_TONE[b.outcome.tone] ? { tone: OUTCOME_TONE[b.outcome.tone] } : {}) }] : [];
  const chips = [...oc, ...(chip ? [chip] : [])];
  return html`<toolcard title=${b.headline} icon=${FAMILY_ICON[b.fam] || 'wrench'} family=${b.fam} state=${state}
      chips=${chips.length ? chips : nothing} open=${isOpen(b.id, false)} @toggle=${setOpen(b.id)}
      @open=${long ? () => push({ kind: 'call', run: ctx.app.sel, id: b.id }) : nothing}>
    <text style="caption" tone="muted" mono>${b.name}</text>
    ${b.sub ? html`<text mono selectable>${b.sub}</text>` : nothing}
    ${argRows(b)}
    ${done ? html`<code text=${long ? b.result.slice(0, CUT) + '…' : b.result}/>` : nothing}
    ${done && long ? html`<text style="footnote" tone="muted">${`cut at ${CUT} of ${b.result.length} characters — ↗ shows all`}</text>` : nothing}
    ${b.kids ? html`<transcript>${repeat(b.kids, (x) => x.id, (x) => draw(x, depth + 1))}</transcript>` : nothing}
  </toolcard>`;
}

// A delivered child result starts with "--- #id title (outcome) ---".
const stripHead = (s) => String(s || '').replace(/^--- #\d+ .*? ---\n/, '');

export function agentTpl(b, depth = 0, draw = blockTpl, { approval = null, open: openIt = null } = {}) {
  const running = b.state === 'running' || b.state === 'approval';
  const open = isOpen(b.id, running);
  const child = b.child || {};
  const title = b.link && b.link.label ? b.link.label : b.headline;
  const phase = b.link && b.link.phase ? b.link.phase : child.status || '';
  const steps = child.llmCalls ? `${child.llmCalls} step${child.llmCalls === 1 ? '' : 's'}` : '';
  if (open && b.childId && !b.blocks) ctx.app.session.ui.act.loadChild(b.childId); // once: a failed read waits (model/session.js Failures)
  const readErr = open && b.childId && !b.blocks ? ctx.app.session.ui.readError(b.childId) : '';
  const chips = [
    ...(b.childId ? [{ text: '#' + b.childId }] : []),
    ...(b.state === 'done' ? [{ text: steps || 'done' }] : phase ? [{ text: phase, tone: b.state === 'approval' ? 'warn' : 'accent' }] : []),
    ...(b.state === 'error' ? [{ text: 'failed', tone: 'danger' }] : b.state === 'stopped' ? [{ text: 'stopped' }] : []),
  ];
  const state = { running: 'running', approval: 'running', error: 'error', stopped: 'canceled', done: 'ok' }[b.state] || 'running';
  const answer = !running && b.result && !b.result.startsWith('(') ? stripHead(b.result) : '';
  return html`<toolcard title=${title} icon="branch" family="agent" state=${state} chips=${chips}
      open=${open} @toggle=${(e) => { if (e.open && b.childId) ctx.app.session.failed.clear(b.childId); setOpen(b.id)(e); }}
      @open=${b.childId && openIt ? () => openIt(b.childId) : nothing}>
    ${b.task ? html`<text style="footnote" tone="muted" lines=${isOpen(b.id + ':task', false) ? nothing : 3}>${'task: ' + b.task}</text>` : nothing}
    <transcript>
      ${b.blocks ? repeat(b.blocks, (x) => x.id, (x) => draw(x, depth + 1))
        : readErr ? html`<notice tone="danger" text=${`Couldn't read its steps: ${readErr} — fold the card and open it again to retry.`}/>`
        : html`<progress label="loading…"/>`}
      ${b.pendingApproval && approval ? approval(b.pendingApproval, b.childId, 'The subagent wants to run') : nothing}
    </transcript>
    ${answer ? html`<text style="caption" tone="muted">answer</text><markdown source=${answer}/>` : nothing}
  </toolcard>`;
}

