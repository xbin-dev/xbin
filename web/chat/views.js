/**
 * chat/views.js — the chat kit's views (/vendor/chat/views.js): a transcript
 * of blocks (/vendor/chat/blocks.js) drawn as lit templates. Every view is a
 * function of (block, ctx): it reads its block and asks its host only
 * through ctx (blocks.js ChatCtx) — nothing here knows the Agent tab's
 * element or a tile's page, so the same functions draw both.
 *
 *   rowsView(blocks, ctx)   the rows of a transcript (or of the window of it
 *                           a host renders): one keyed element per block,
 *                           drawn again only when the block's version, what
 *                           the host keeps for it (ctx.rev) or a highlighter
 *                           changed
 *   blockView(block, ctx)   one block: the host is asked first (ctx.other),
 *                           then the kit's view of its kind — messages and
 *                           the small kinds here, cards in cards.js, what an
 *                           agent asks in asks.js
 *   activityView, usageView the line under a running transcript, the usage
 *                           badge
 *
 * Rows are template functions, not elements: a selection spans them, a host
 * measures them as the scroller's direct children (/vendor/chat/window.js),
 * and one stylesheet (css.js, `bxc-` classes) draws them in a shadow root or
 * in a document. All imports are by /vendor/ URL.
 */
import { html, nothing, repeat, guard, noChange, directive, Directive } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import { mdInto, mdEpoch } from '/vendor/chat/md.js';
import { usageBadge } from '/vendor/chat/blocks.js';
import { headline } from '/vendor/chat/tools.js';
import { toolView, agentView, changesView, planView, diffEpoch } from '/vendor/chat/cards.js';
import { permView, askView, signinView } from '/vendor/chat/asks.js';

/**
 * mdLive(text, block): markdown rendered into the element it sits on, a
 * top-level block at a time (md.js mdInto) — for text that streams: only
 * the last paragraph re-parses and the rest keep their DOM (a selection
 * survives). The parsed parts are memoized on the block ($memo).
 */
class MdLive extends Directive {
  render() { return noChange; }
  update(part, [text, b]) {
    const memo = b ? ((b.$memo ||= Object.create(null)).mdparts ||= {}) : null;
    mdInto(part.element, text, memo);
    return noChange;
  }
}
export const mdLive = directive(MdLive);

const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');

/**
 * What a host hangs on a block's element (ChatCtx.rowAttrs): hook classes
 * and data-* attributes of its own — for its tests, its instances' styles
 * — beside the kit's. Anything else it names is ignored; data-k and
 * data-kind stay the kit's.
 */
class HostAttrs extends Directive {
  render() { return noChange; }
  update(part, [attrs]) {
    const el = part.element, prev = this.prev || {}, next = attrs || {};
    const words = (v) => String(v || '').split(/\s+/).filter(Boolean);
    for (const c of words(prev.class)) if (!words(next.class).includes(c)) el.classList.remove(c);
    for (const c of words(next.class)) el.classList.add(c);
    const mine = (k) => k.startsWith('data-') && k !== 'data-k' && k !== 'data-kind';
    for (const k of Object.keys(prev)) if (mine(k) && (next[k] == null || next[k] === false)) el.removeAttribute(k);
    for (const k of Object.keys(next)) if (mine(k) && next[k] != null && next[k] !== false && prev[k] !== next[k]) el.setAttribute(k, String(next[k]));
    this.prev = next;
    return noChange;
  }
}
const hostAttrs = directive(HostAttrs);
const attrsOf = (ctx, b, depth) => hostAttrs(ctx.rowAttrs ? ctx.rowAttrs(b, depth) : null);

// ---- messages ---------------------------------------------------------------------------

// a file that came with a message: its picture when the host has one, else a glyph by its type; its name, its size
function fileChip(b, f, ctx) {
  const st = ctx.fileState ? ctx.fileState(b, f) : null;
  const size = f.size != null ? `${fmtN(f.size)} bytes` : f.sizeText || '';
  const title = [f.path && f.path !== f.name ? f.path : '', f.mime || '', size].filter(Boolean).join(' · ');
  const gone = st && st.linked === false;
  const pic = st && st.thumb ? html`<img src=${st.thumb} alt="">` : html`<bx-icon name=${/^image\//.test(f.mime || '') ? 'photo' : /^text(\/|$)/.test(f.mime || '') ? 'file' : 'box'}></bx-icon>`;
  const inside = html`${pic}<span class="bxc-mono">${f.path || f.name}</span>${f.sizeText ? html`<span class="bxc-sz">${f.sizeText}</span>` : nothing}`;
  return ctx.openFile && !gone
    ? html`<button class="bxc-att-file" data-path=${f.path || f.name} title=${title} @click=${() => ctx.openFile(b, f)}>${inside}</button>`
    : html`<span class="bxc-att-file ${gone ? 'bxc-gone' : ''}" title=${gone ? 'this file is gone' : title}>${inside}</span>`;
}

/**
 * userView(b, ctx): what a person sent — their words as typed, the files
 * that came with them — on the reader's side of the chat; another person's
 * (a shared conversation) on the other side, under their name.
 */
export function userView(b, ctx) {
  const other = !!b.sender && !!ctx.me && b.sender !== ctx.me;
  return html`<div class="bxc-msg bxc-user ${other ? 'bxc-other' : ''}">${other ? html`<div class="bxc-who">${b.sender}</div>` : nothing}<div class="bxc-bubble">${b.text ? html`<div class="bxc-text">${b.text}</div>` : nothing}${b.files?.length
    ? html`<div class="bxc-att ${b.text ? 'bxc-below' : ''}">${b.files.map((f) => fileChip(b, f, ctx))}</div>` : nothing}${b.steered ? html`<span class="bxc-steer">taken in mid-turn</span>` : nothing}</div></div>`;
}

/**
 * assistantView(b, ctx): a message of the agent's, markdown (it may still
 * be growing); where the host copies one (ChatCtx.copyMessage), a Copy that
 * shows with the pointer.
 */
export function assistantView(b, ctx) {
  return html`<div class="bxc-msg bxc-agent"><div class="bxc-bubble bxc-md" ${mdLive(b.text, b)}></div>${ctx.copyMessage ? html`<div class="bxc-acts">
    <button class="bxc-act" title="copy this answer (with its formatting)" aria-label="copy this answer" @click=${(e) => ctx.copyMessage(b, e)}><bx-icon name="copy"></bx-icon></button></div>` : nothing}</div>`;
}

/** draftView(b): the answer being written, before its block exists: a message with a still cursor at its end. */
export function draftView(b) {
  return html`<div class="bxc-msg bxc-agent bxc-draft"><div class="bxc-bubble bxc-md bxc-cursor" ${mdLive(b.text, b)}></div></div>`;
}

const took = (ms) => { const s = Math.max(1, Math.round(ms / 1000)); return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`; };
// thoughtTokens(n) → " · 4.3k tokens": a thought's reasoning tokens after its time ('' for none; D249)
export const thoughtTokens = (n) => !(n > 0) ? '' : n < 1000 ? ` · ${n} tokens` : ` · ${(n / 1000).toFixed(n < 10000 ? 1 : 0).replace(/\.0$/, '')}k tokens`;

/**
 * thoughtView(b, ctx): the agent thinking — open while it streams (the block
 * is live and a turn runs), then folded to how long it took.
 */
export function thoughtView(b, ctx) {
  const live = !!b.live && !!ctx.running && !ctx.ended;
  const timed = b.ms > 0 || !!b.at; // a reducer that times thoughts says when one began: under a second is still a second
  const open = ctx.isOpen(b, 'body', live);
  const toggle = (e) => { if (e.target.open !== ctx.isOpen(b, 'body', live)) ctx.setOpen(b, 'body', e.target.open, live); };
  return html`<div class="bxc-msg"><details class="bxc-thought" ?open=${open} @toggle=${toggle}>
    <summary><bx-icon class="bxc-cr" name="caret-right"></bx-icon><bx-icon class="bxc-cd" name="caret-down"></bx-icon>${live ? html`<span class="bxc-live">Thinking…</span>` : `${timed ? `Thought for ${took(b.ms || 0)}` : 'Thought'}${thoughtTokens(b.tokens)}`}</summary>
    ${open ? html`<div class="bxc-md" ${mdLive(b.text, b)}></div>` : nothing}</details></div>`;
}

/**
 * noticeView(b, ctx): a line that is neither the person's nor the agent's —
 * what an automation delivered, a journal line, a restart. Its first line
 * is its heading when more follow. A block may say more: `icon` (a glyph
 * before it; '' a bullet in the glyph's place), `tone` ('bad': something went wrong), `md` (its text is
 * markdown), `fold` (folded to its heading — `title`, else its first line —
 * until the reader opens it), `action` ({label, title}: the line is a button
 * that tells the host, ChatCtx.act).
 */
export function noticeView(b, ctx = {}) {
  const text = String(b.text || '');
  // (icon '': a line among lines with glyphs keeps their column, with a bullet)
  const glyph = b.icon ? html`<bx-icon class="bxc-ng" name=${b.icon}></bx-icon>` : b.icon === '' ? html`<span class="bxc-ng">•</span>` : nothing;
  const cls = `bxc-notice ${b.tone ? 'bxc-' + b.tone : ''} ${b.icon != null ? 'bxc-line' : ''}`;
  if (b.action && ctx.act) {
    return html`<div class=${cls}>${glyph}<button class="bxc-more" title=${b.action.title || nothing} @click=${() => ctx.act(b)}>${b.action.label || text}</button></div>`;
  }
  if (b.md) return html`<div class="${cls} bxc-nmd">${glyph}<div class="bxc-md" ${mdLive(text, b)}></div></div>`;
  const nl = text.indexOf('\n');
  if (b.fold) {
    const open = ctx.isOpen ? ctx.isOpen(b, 'body', false) : false;
    const toggle = (e) => { if (ctx.setOpen && e.target.open !== open) ctx.setOpen(b, 'body', e.target.open, false); };
    return html`<details class="${cls} bxc-nfold" ?open=${open} @toggle=${toggle}>
      <summary><bx-icon class="bxc-cr" name="caret-right"></bx-icon><bx-icon class="bxc-cd" name="caret-down"></bx-icon>${glyph}${b.title || (nl < 0 ? text : text.slice(0, nl))}</summary>
      ${open && nl >= 0 ? html`<div class="bxc-nbody">${text.slice(nl + 1)}</div>` : nothing}</details>`;
  }
  return nl < 0 ? html`<div class=${cls}>${glyph}<span>${text}</span></div>` : html`<div class=${cls}>${glyph}<span><b>${text.slice(0, nl)}</b>${text.slice(nl + 1)}</span></div>`;
}

/** turnView(b): a turn's end — its number, why it stopped, what went wrong. */
export function turnView(b) {
  return html`<div class="bxc-turn">turn ${b.turn ?? ''} · ${b.stopReason || 'done'}${b.error ? html` — <span class="bxc-err">${b.error}</span>` : nothing}</div>`;
}

/** gapView(): part of the transcript is not here. */
export const gapView = () => html`<div class="bxc-gap">… earlier events dropped (log limit)</div>`;

/** hostView(b, ctx): a host block — the kit's frame, the host's card inside it (ctx.host); its title alone when the host draws none. */
export function hostView(b, ctx) {
  const own = ctx.host ? ctx.host(b) : null;
  if (own == null && !b.title) return nothing;
  return html`<div class="bxc-host" data-type=${b.type || ''}>${own ?? html`<span class="bxc-muted">${b.title}</span>`}</div>`;
}

// ---- dispatch ---------------------------------------------------------------------------

/**
 * blockView(block, ctx, depth): one block drawn. The host is asked first
 * (ctx.other: a kind of its own, or a kit kind it draws its own way); null
 * or undefined leaves the block to the kit. A kind nobody draws is nothing.
 */
export function blockView(b, ctx, depth = 0) {
  if (!b) return nothing;
  if (ctx.other) { const own = ctx.other(b, depth); if (own != null) return own; }
  switch (b.kind) {
    case 'user': return userView(b, ctx);
    case 'assistant': return assistantView(b, ctx);
    case 'draft': return draftView(b, ctx);
    case 'thought': return thoughtView(b, ctx);
    case 'tool': return toolView(b, ctx, depth, innerView);
    case 'agent': return agentView(b, ctx, depth, innerView);
    case 'changes': return changesView(b, ctx);
    case 'plan': return planView(b, ctx);
    case 'perm': return permView(b, ctx);
    case 'ask': return askView(b, ctx);
    case 'signin': return signinView(b, ctx);
    case 'notice': return noticeView(b, ctx);
    case 'turn': return turnView(b, ctx);
    case 'gap': return gapView(b, ctx);
    case 'host': return hostView(b, ctx);
  }
  return nothing;
}

/**
 * innerView(block, ctx, depth): a block inside another's card (a subagent's
 * steps, a sub-conversation's blocks) — its view in an element that carries
 * its key, like a row: `<div class="bxc-in" data-k=…>`.
 */
export function innerView(b, ctx, depth = 1) {
  return b ? html`<div class="bxc-in" data-k=${b.k} data-kind=${b.kind} ${attrsOf(ctx, b, depth)}>${blockView(b, ctx, depth)}</div>` : nothing;
}

// a request that waits holds live form state (values, a busy answer): it is drawn every time
const waits = (b) => (b.kind === 'perm' && !b.by) || (b.kind === 'ask' && !b.action);

/** rowKey(b): what a row is drawn again for — everything outside the block's own version that its picture reads. */
const rowDeps = (b, ctx) => [b.v, ctx.rev ? ctx.rev(b) : 0, !!ctx.ended, !!ctx.readonly,
  (b.kind === 'thought' || b.kind === 'agent' || !!b.children) && !!ctx.running, mdEpoch, diffEpoch];

/**
 * rowView(block, ctx): one row of a transcript — `<div class="bxc-row"
 * data-k=…>` holding the block's view, which is computed again only when
 * the block's version, the host's revision for it (ctx.rev), the turn's
 * running (for the kinds that read it) or a highlighter's arrival changed.
 */
export function rowView(b, ctx) {
  return html`<div class="bxc-row" data-k=${b.k} data-kind=${b.kind} ${attrsOf(ctx, b, 0)}>${waits(b) ? blockView(b, ctx) : guard(rowDeps(b, ctx), () => blockView(b, ctx))}</div>`;
}

/** rowsView(blocks, ctx): the rows of blocks, keyed by k (the scroller's direct children: /vendor/chat/window.js measures them). */
export function rowsView(blocks, ctx) {
  return repeat(blocks || [], (b) => b.k, (b) => rowView(b, ctx));
}

// ---- beside the transcript --------------------------------------------------------------

/**
 * activityView(blocks, ctx): what the running turn is doing right now, for
 * under the transcript — the call in flight, else "Working…"; nothing when
 * no turn runs or an open thought already says it.
 */
export function activityView(blocks, ctx) {
  if (!ctx.running || ctx.ended) return nothing;
  const last = blocks[blocks.length - 1];
  if (last && last.kind === 'thought' && last.live) return nothing;
  let tool = null;
  for (let i = blocks.length - 1; i >= 0 && !tool && blocks[i].kind !== 'turn'; i--) if (blocks[i].kind === 'tool' && blocks[i].rec.status === 'in_progress') tool = blocks[i];
  return html`<div class="bxc-activity bxc-muted"><span class="bxc-live">${tool ? `Running ${headline(tool.rec)}…` : 'Working…'}</span></div>`;
}

/**
 * usageView(usage, opts): a Transcript's usage as a badge — the share of the
 * context in use (the tokens when its size is unknown), the cost when the
 * host has it, tinted as it fills (blocks.js usageBadge). opts.short: without
 * the cost.
 */
export function usageView(usage, opts = {}) {
  const u = usageBadge(usage);
  if (!u) return nothing;
  return html`<span class="bxc-usage ${u.tone ? 'bxc-' + u.tone : ''}" title=${u.title} data-pct=${u.pct ?? ''}>${opts.short ? u.head : u.text}</span>`;
}
