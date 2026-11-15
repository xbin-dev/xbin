// chat-cards.js — the chat of the open conversation: its blocks
// (model/fold.js) drawn by xbind's chat kit (D224; chat-kit.js: one renderer
// for a conversation whatever produced it), each a row of the timeline that
// carries its block's id as data-k — the rows chat-window.js keeps in a
// window — and what frames them: the crumbs of a subagent's session, the
// line that says there is more above, what the run waits on a person for,
// the activity line, the pill, and the queued messages over the composer.
//
// A streamed token or a settled result patches one row and leaves the rest
// — the selection, an open card, the scroll position — alone (the kit draws
// a row again only when its block's version moves; a block's markdown is
// parsed once and the answer being written re-parses its last paragraph).
//
// Seams (web-ext.js, as ui.ext): a feature module may draw a block its own
// way (ext.block, asked first) and add what follows the transcript (ext.end;
// a harness park is then its to draw — see sessionTpl).
import { html, nothing, repeat } from '/vendor/lit-all.min.js';
import { onHighlighter } from './chat-md.js';
import { ctx } from './web-ext.js';
import './chat-copy.js'; // copying with the formatting: the transcript's Ctrl+C (D199); an answer's Copy is chat-kit.js's
import { activityStill } from './model/harness.js';
import { kitRow, kitPark } from './chat-kit.js';

onHighlighter(() => ctx.paint()); // the code highlighter arrived: what was drawn plain is drawn again

const icon = (name) => (name ? html`<bx-icon name=${name}></bx-icon>` : nothing);

// ui: {isOpen(id, dflt), toggle(id, dflt), act: {...}, ext?}
export function blocksTpl(blocks, ui, depth = 0) {
  return repeat(blocks, (b) => b.id, (b) => blockTpl(b, ui, depth));
}

// one block: a feature module's own way first, else the kit's view of it
function blockTpl(b, ui, depth) {
  return ui.ext?.block(b, ui, depth) || kitRow(b, ui, depth);
}

// approveNoteTpl says why a verdict on runId's ask was refused (its ask is
// gone — Session.noteApprove), for a few seconds.
function approveNoteTpl(ui, runId) {
  const note = runId != null && ui.approveNote ? ui.approveNote(runId) : '';
  return note ? html`<div class="anote muted small" role="status">${icon('warning')}<span>${note}</span></div>` : nothing;
}

// sessionTpl is the chat of the selected run. win (chat-window.js) is the
// window of its blocks drawn — {start, end, pill, older(), latest()}; all of
// them without it. Above the window, a line says there is more; what closes
// the chat (the seams' end, an approval, a question, the activity line) shows
// only when the window reaches the end; the pill offers the latest while the
// reader is away from it. A harness park (pendingState.harness) is the end
// hooks' to draw while any answers; the built-in card is its fallback.
export function sessionTpl(s, ui, win) {
  const r = s.run || {};
  const n = s.blocks.length;
  const start = win ? win.start : 0, end = win ? win.end : n;
  const rows = start || end < n ? s.blocks.slice(start, end) : s.blocks;
  const atEnd = end >= n && !s.hasNewer;
  const ps = r.pendingState || {};
  const tail = atEnd ? ui.ext?.end(s, ui) : null;
  const parked = r.status === 'waiting_input' && !(tail && ps.harness); // the built-in card's to draw
  return html`
    ${s.chain && s.chain.length ? html`<div class="crumbs">${s.chain.map((c) => html`
      <a @click=${() => ui.act.select(c.id)}>${c.title || '#' + c.id}</a> ›`)} <b>${r.title || '#' + r.id}</b></div>` : nothing}
    ${start || s.hasOlder ? html`<div class="muted small center earlier">… earlier messages${win && win.older
      ? html` <button class="lnk" @click=${() => win.older()}>load earlier</button>` : nothing}</div>`
      : s.olderHidden ? html`<div class="muted small center">— earlier turns were compacted into the summary —</div>` : nothing}
    ${blocksTpl(rows, ui)}
    ${tail || nothing}
    ${atEnd && parked ? kitPark(r, ui, s.blocks) || nothing : nothing}
    ${atEnd ? approveNoteTpl(ui, r.id) : nothing}
    ${atEnd && s.activity ? (activityStill(r) ? html`<div class="activity still">${s.activity}</div>`
      : html`<div class="activity"><span class="spin"></span> ${s.activity}</div>`) : nothing}
    ${s.conn === 'reconnecting' ? html`<div class="activity warn">${icon('warning')}live updates lost — reconnecting…</div>` : nothing}
    ${win && win.pill ? html`<div class="jumpw"><button class="jump" @click=${() => win.latest()}>${win.pill}</button></div>` : nothing}
  `;
}

// queueTpl is the strip of queued messages above the composer; words
// ({label, title}) say what happens to them otherwise (a coding harness's:
// model/harness-ask.js steerWords).
export function queueTpl(queued, remove, words = null) {
  if (!queued || !queued.length) return nothing;
  return html`${queued.map((q) => html`<span class="qchip" title=${(words && words.title) || "queued — delivered at the agent's next step"}>
    <span class="ql">${(words && words.label) || 'queued'}</span><span class="qt">${q.text || '(files)'}</span>
    <button title="take it back" aria-label="take it back" @click=${() => remove(q.id)}>${icon('xmark')}</button></span>`)}`;
}
