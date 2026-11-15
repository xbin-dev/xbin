/**
 * chat/asks.js — what an agent asks a person, drawn (/vendor/chat/asks.js):
 * a permission request with the agent's own options, a plan's approval, a
 * question as a form, as a page to open, or as words the next message
 * answers, and the card that says the agent is signed out (the host fills
 * it with its sign-in). Template functions of (block, ctx); the words and
 * the rules are /vendor/chat/ask.js's.
 *
 * Who may answer is the host's to say (ChatCtx.can, .role, .readonly): a
 * reader who may not is shown the request without its buttons. An option
 * that raises what the agent does without asking (`explicit`) is only
 * offered to who may raise, drawn as a caution, and confirmed before it is
 * sent (ChatCtx.confirm). What a person types into a card — a form's
 * values, a word with a rejection — is kept in the host's bag for the block
 * (ChatCtx.state), so it outlives the row leaving a window.
 *
 * The question's form is an element, <bx-chat-form> (light DOM: it wears
 * the stylesheet of the root it sits in, css.js), so typing into it draws
 * the form again and nothing else.
 */
import { html, nothing, LitElement } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import { md } from '/vendor/chat/md.js';
import { cached } from '/vendor/chat/blocks.js';
import { permission, decisions, permAnswer, optionConfirm, question, formContent, missingRequired } from '/vendor/chat/ask.js';
import { headline, commandOf, rawText } from '/vendor/chat/tools.js';
import { commandView, contentView } from '/vendor/chat/cards.js';

// ---- who, and what they typed ------------------------------------------------------------

/** canOf(ctx): what the reader may do with a request — {answer, raise} — from ctx.can, else its role; nothing once it is read-only. */
export function canOf(ctx) {
  if (!ctx || ctx.readonly || ctx.ended) return { answer: false, raise: false };
  const role = ctx.role || 'owner';
  const can = ctx.can || {};
  return { answer: can.answer ?? role !== 'viewer', raise: can.raise ?? role === 'owner' };
}

/** bagOf(ctx, block): the bag the host keeps for a block's widgets (ChatCtx.state), else one the kit keeps on ctx by key. */
export function bagOf(ctx, b) {
  if (ctx.state) return ctx.state(b);
  const bags = ctx.$bags || (ctx.$bags = new Map());
  let bag = bags.get(b.k);
  if (!bag) {
    if (bags.size > 64) bags.delete(bags.keys().next().value);
    bags.set(b.k, (bag = {}));
  }
  return bag;
}

const whoOf = (by) => (by === 'auto' ? 'a session rule' : by === 'cancel' ? 'cancel' : String(by || '').replace('user:', ''));
const nameOf = (ctx) => ctx.agentName || 'the agent';
// a reader who may not answer is told so — unless nobody can any more (the session is over)
const viewOnly = (ctx) => (ctx.ended ? nothing : html`<div class="bxc-note">view only — someone who may answer here does</div>`);

// an answer on its way: the bag says so until the host's promise settles (the buttons wait)
async function send(b, ctx, fn) {
  const bag = bagOf(ctx, b);
  if (bag.busy) return;
  bag.busy = true;
  ctx.repaint();
  try { await fn(); } finally { bag.busy = false; ctx.repaint(); }
}

// choose an option: an explicit one is confirmed first
async function choose(b, ctx, o, feedback = '') {
  if (o.explicit) {
    const ask = ctx.confirm || ((text) => window.confirm(text));
    if (!(await ask(optionConfirm(nameOf(ctx), o)))) return;
  }
  await send(b, ctx, () => ctx.permit(b, permAnswer(o, feedback)));
}

const optionButton = (b, ctx, o, primary, busy, feedback) => html`<button class="${o.reject ? 'bxc-deny' : o.explicit ? 'bxc-explicit' : primary ? 'bxc-primary' : ''}"
  data-option=${o.id || o.decision || ''} data-kind=${o.kind || o.decision || ''} title=${o.title || nothing} ?disabled=${busy}
  @click=${() => choose(b, ctx, o, feedback ? feedback() : '')}>${o.explicit ? html`<bx-icon name="warning"></bx-icon>` : nothing}${o.name}</button>`;

const noteOf = (b, ctx) => { const n = ctx.note ? ctx.note(b) : ''; return n ? html`<div class="bxc-note bxc-err">${n}</div>` : nothing; };
const hiddenNote = (card, ctx) => (card.hidden ? html`<div class="bxc-note">${card.hidden === 1 ? 'One option' : `${card.hidden} options`} that would stop ${nameOf(ctx)} asking — only the owner may pick ${card.hidden === 1 ? 'it' : 'them'}.</div>` : nothing);

// ---- a permission request -----------------------------------------------------------------

/**
 * permView(b, ctx): a perm block — the plan's card for a plan approval,
 * else the call's headline, what exactly would run, and the agent's own
 * options; once answered, who answered and how.
 */
export function permView(b, ctx) {
  if (b.plan) return planApprovalView(b, ctx);
  const tool = b.tool || {};
  const call = { kind: tool.kind, title: tool.title, name: tool.name, rawInput: tool.rawInput, id: b.callId };
  const cmd = tool.command || commandOf(call);
  // an agent may title its request with the command itself: then the call's own headline says it
  const heading = b.title && b.title.trim() !== cmd.trim() ? b.title : headline(call, { reading: false }) || (b.calls || []).join(', ');
  if (b.by) {
    const opt = (b.options || []).find((o) => o.optionId === b.optionId);
    const verb = b.by === 'cancel' ? 'cancelled' : opt && /reject/.test(opt.kind || '') ? 'denied' : 'allowed';
    return html`<div class="bxc-perm bxc-settled" data-pid=${b.pid || ''}><div class="bxc-q">${heading}</div><div class="bxc-settled-by">${verb} by ${whoOf(b.by)}</div></div>`;
  }
  const can = canOf(ctx);
  const card = permission(b, { name: nameOf(ctx), can, scopedOnly: !!ctx.sessionRules, where: ctx.sessionRules ? 'in this session' : 'in this conversation' });
  const opts = card.options.length ? card.options : (b.options || []).length ? [] : decisions(b);
  const first = opts.find((o) => !o.reject && !o.explicit);
  const busy = !!bagOf(ctx, b).busy;
  return html`<div class="bxc-perm" data-pid=${b.pid || ''}>
    <div class="bxc-q"><b><bx-icon name="warning"></bx-icon>Permission</b> — ${heading}${card.description ? html`<div class="bxc-desc">${card.description}</div>` : nothing}</div>
    ${cmd ? commandView(cmd, ctx) : tool.rawInput != null ? html`<pre class="bxc-cmd">${rawText(tool.rawInput)}</pre>` : nothing}
    <div class="bxc-pbody">${contentView(tool.content, b, ctx, heading)}</div>
    ${can.answer ? html`${card.rule ? html`<div class="bxc-rulenote">${card.rule}.</div>` : nothing}
      <div class="bxc-btns">${opts.map((o) => optionButton(b, ctx, o, o === first, busy))}</div>
      ${hiddenNote(card, ctx)}${noteOf(b, ctx)}` : viewOnly(ctx)}
  </div>`;
}

// the plan approval (Claude's ExitPlanMode "Ready to code?", Codex's
// "Implement this plan?"): the plan as markdown, every choice the agent
// offers with its full label (they are modes — "clear context (37% used)",
// "bypass permissions" — not "remember this"), and, beside "keep planning",
// a box for what should change (the host sends it the way its agent takes it)
function planApprovalView(b, ctx) {
  const tool = b.tool || {};
  const heading = b.title || tool.title || 'Approve the plan?';
  const planMd = html`<div class="bxc-md bxc-plan-md" .innerHTML=${cached(b, 'plan', () => md(b.plan.text || '*(the agent sent no plan text)*'))}></div>`;
  if (b.by) {
    const opt = (b.options || []).find((o) => o.optionId === b.optionId);
    const kept = b.by === 'cancel' || !opt || /reject/.test(opt.kind || '');
    return html`<div class="bxc-perm bxc-plan-card bxc-settled" data-pid=${b.pid || ''}>
      <div class="bxc-q"><b>${kept ? 'Kept planning' : 'Plan approved'}</b>${opt && !kept ? html` — ${opt.name}` : nothing} <span class="bxc-settled-by">by ${whoOf(b.by)}</span></div>
      <details><summary class="bxc-muted">the plan</summary>${planMd}</details></div>`;
  }
  const can = canOf(ctx);
  const card = permission(b, { name: nameOf(ctx), can });
  const allows = card.options.filter((o) => !o.reject), rejects = card.options.filter((o) => o.reject);
  const first = allows.find((o) => !o.explicit);
  const bag = bagOf(ctx, b);
  return html`<div class="bxc-perm bxc-plan-card" data-pid=${b.pid || ''}>
    <div class="bxc-q"><b>${heading}</b>${card.planPath ? html`<div class="bxc-desc bxc-mono">${card.planPath}</div>` : nothing}</div>
    ${planMd}
    ${can.answer ? html`<div class="bxc-btns bxc-col">${allows.map((o) => optionButton(b, ctx, o, o === first, !!bag.busy))}</div>
      ${rejects.length ? html`<div class="bxc-reject">
        <textarea rows="2" placeholder=${ctx.feedbackHint || `Tell ${nameOf(ctx)} what to change (optional)`}
          .value=${bag.feedback || ''} @input=${(e) => { bag.feedback = e.target.value; }}></textarea>
        ${rejects.map((o) => optionButton(b, ctx, o, false, !!bag.busy, () => { const fb = bag.feedback || ''; bag.feedback = ''; return fb; }))}
      </div>` : nothing}
      ${hiddenNote(card, ctx)}${noteOf(b, ctx)}` : viewOnly(ctx)}
  </div>`;
}

// ---- a question ---------------------------------------------------------------------------

/**
 * askView(b, ctx): an ask block — a form (<bx-chat-form>), a page to open
 * with Done / Skip, or only its message (the next message answers it);
 * once answered, what was said and by whom.
 */
export function askView(b, ctx) {
  const q = question(b);
  const can = canOf(ctx);
  const message = (dflt) => html`<div class="bxc-q bxc-md" .innerHTML=${cached(b, 'q', () => md(b.message || dflt))}></div>`;
  if (q.mode === 'url') {
    if (b.action) {
      const verb = b.action === 'accept' ? 'opened' : b.action === 'complete' ? 'done' : b.action === 'decline' ? 'skipped' : 'cancelled';
      return html`<div class="bxc-perm bxc-ask bxc-settled" data-eid=${b.eid || ''}>${message('A page to open')}<div class="bxc-settled-by">${verb} by ${whoOf(b.by)}</div></div>`;
    }
    const busy = !!bagOf(ctx, b).busy;
    return html`<div class="bxc-perm bxc-ask" data-eid=${b.eid || ''} data-mode="url">
      ${message(`${nameOf(ctx)} asks you to open a page`)}
      <div class="bxc-field">${q.url ? html`<a class="bxc-mono" href=${q.url} target="_blank" rel="noopener noreferrer">${q.url}</a>` : html`<span class="bxc-mono">${q.text}</span>`}</div>
      ${can.answer ? html`<div class="bxc-btns">
        <button class="bxc-primary" ?disabled=${busy} @click=${() => send(b, ctx, () => ctx.answer(b, { action: 'accept', content: {} }))}>Done</button>
        <button class="bxc-deny" ?disabled=${busy} @click=${() => send(b, ctx, () => ctx.answer(b, { action: 'decline' }))}>Skip</button>
      </div>${noteOf(b, ctx)}` : viewOnly(ctx)}
    </div>`;
  }
  if (q.mode === 'text') {
    return html`<div class="bxc-perm bxc-ask ${b.action ? 'bxc-settled' : ''}" data-eid=${b.eid || ''} data-mode="text">${message(`${nameOf(ctx)} asks`)}
      ${b.action ? html`<div class="bxc-settled-by">answered by ${whoOf(b.by)}</div>` : can.answer ? html`<div class="bxc-note">Your next message answers it.</div>` : viewOnly(ctx)}</div>`;
  }
  if (b.action) {
    const c = b.content || {};
    const verb = b.action === 'accept' ? 'answered' : b.action === 'decline' ? 'skipped' : 'cancelled';
    return html`<div class="bxc-perm bxc-ask bxc-settled" data-eid=${b.eid || ''}>${message('A question')}
      ${b.action === 'accept' ? html`<ul class="bxc-answers">${q.fields.map((f) => answerLine(f, c))}</ul>` : nothing}
      <div class="bxc-settled-by">${verb} by ${whoOf(b.by)}</div></div>`;
  }
  return html`<div class="bxc-perm bxc-ask" data-eid=${b.eid || ''} data-mode="form">
    ${message(`${nameOf(ctx)} asks`)}
    <bx-chat-form .block=${b} .ctx=${ctx} .fields=${q.fields} .readonly=${!can.answer}></bx-chat-form>
    ${noteOf(b, ctx)}
  </div>`;
}

function answerLine(f, c) {
  const v = c[f.key];
  const o = f.other ? c[f.other] : undefined;
  const shown = [Array.isArray(v) ? v.join(', ') : v, o].filter((x) => x != null && x !== '').join(' — ');
  return shown ? html`<li><span class="bxc-muted">${f.title || f.description || f.key}:</span> ${shown}</li>` : nothing;
}

/**
 * <bx-chat-form>: a question's form — each field with its choices (radios
 * or checkboxes, an option's description beside it), its "Other" box, a
 * field starting at its schema's default; Submit answers (the required
 * ones first), Skip declines. Properties: block (the ask block), ctx,
 * fields (ask.js formFields), readonly. Its values live in the host's bag
 * for the block.
 */
export class BxChatForm extends LitElement {
  static properties = { block: { attribute: false }, ctx: { attribute: false }, fields: { attribute: false }, readonly: { type: Boolean }, _err: { state: true } };

  constructor() { super(); this.block = null; this.ctx = null; this.fields = []; this.readonly = false; this._err = ''; }

  createRenderRoot() { return this; } // light DOM: the stylesheet of the root it sits in

  get _bag() { const bag = bagOf(this.ctx, this.block); bag.vals ||= {}; return bag; }

  _set(key, value) { this._bag.vals[key] = value; this._err = ''; this.requestUpdate(); }

  /** values(): what the form would answer now. */
  values() { return formContent(this.fields || [], this._bag.vals); }

  async submit() {
    const content = this.values();
    const miss = missingRequired(this.fields || [], content);
    if (miss.length) { this._err = `Answer ${miss.join(', ')} first.`; return false; }
    return this._send({ action: 'accept', content });
  }

  skip() { return this._send({ action: 'decline' }); }

  async _send(answer) {
    const bag = this._bag;
    if (bag.busy) return false;
    bag.busy = true; this._err = ''; this.requestUpdate();
    try { return await this.ctx.answer(this.block, answer); } finally { bag.busy = false; this.requestUpdate(); }
  }

  render() {
    if (!this.block || !this.ctx) return nothing;
    const bag = this._bag, off = this.readonly || !!bag.busy;
    return html`${(this.fields || []).map((f) => this._field(f, bag.vals, off))}
      ${this._err ? html`<div class="bxc-note bxc-err">${this._err}</div>` : nothing}
      ${this.readonly ? viewOnly(this.ctx) : html`<div class="bxc-btns">
        <button class="bxc-primary" ?disabled=${off} @click=${() => this.submit()}>Submit</button>
        <button class="bxc-deny" ?disabled=${off} @click=${() => this.skip()}>Skip</button>
      </div>`}`;
  }

  _field(f, vals, off) {
    const opt = (o, input) => html`<label class="bxc-opt">${input}<span><b>${o.title}</b>${o.description ? html` <span class="bxc-od">${o.description}</span>` : nothing}</span></label>`;
    const now = vals[f.key] ?? f.dflt; // untouched, a field shows its schema's default (formContent answers it)
    const name = `${this.block.k}-${f.key}`;
    let body;
    if (f.kind === 'radio') {
      body = f.options.map((o) => opt(o, html`<input type="radio" name=${name} ?disabled=${off} .checked=${now === o.value} @change=${() => this._set(f.key, o.value)}>`));
    } else if (f.kind === 'check') {
      const cur = Array.isArray(now) ? now : [];
      body = f.options.map((o) => opt(o, html`<input type="checkbox" ?disabled=${off} .checked=${cur.includes(o.value)}
        @change=${(e) => this._set(f.key, e.target.checked ? [...cur.filter((x) => x !== o.value), o.value] : cur.filter((x) => x !== o.value))}>`));
    } else if (f.kind === 'bool') {
      body = html`<label class="bxc-opt"><input type="checkbox" ?disabled=${off} .checked=${now === true} @change=${(e) => this._set(f.key, e.target.checked)}><span>yes</span></label>`;
    } else {
      body = html`<input class="bxc-txt" type=${f.kind === 'number' ? 'number' : 'text'} ?disabled=${off} .value=${now ?? ''} @input=${(e) => this._set(f.key, e.target.value)}>`;
    }
    return html`<div class="bxc-field" data-field=${f.key}>
      ${f.title ? html`<div class="bxc-fh">${f.title}${f.required ? ' *' : ''}</div>` : nothing}
      ${f.description ? html`<div class="bxc-fd">${f.description}</div>` : nothing}
      <div class="bxc-opts">${body}</div>
      ${f.other ? html`<input class="bxc-txt bxc-other" type="text" ?disabled=${off} placeholder=${f.otherHint || 'Other…'} .value=${vals[f.other] ?? ''} @input=${(e) => this._set(f.other, e.target.value)}>` : nothing}
    </div>`;
  }
}
if (!customElements.get('bx-chat-form')) customElements.define('bx-chat-form', BxChatForm);

// ---- signed out ---------------------------------------------------------------------------

/**
 * signinView(b, ctx): a signin block — the agent is signed out and a turn
 * waits for that. The kit says so; how to sign in is the host's
 * (ChatCtx.signin: its guided flow, its buttons), drawn inside the card.
 */
export function signinView(b, ctx) {
  const own = ctx.signin ? ctx.signin(b) : null;
  return html`<div class="bxc-signin" data-provider=${b.provider || ''}>
    <span class="bxc-q"><bx-icon name="warning"></bx-icon>Not signed in to ${b.provider || nameOf(ctx)}.</span>
    ${own ?? (b.command ? html`<span class="bxc-muted bxc-mono">${b.command}</span>` : nothing)}
  </div>`;
}
