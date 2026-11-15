// chat-cards.js — how the chat draws its blocks (model/fold.js): lit
// templates rendered into the timeline, keyed by block id so a streamed token
// or a settled result patches one card and leaves the rest — the selection,
// an open card, the scroll position — alone. Each block's root carries its
// id as data-k: the timeline's rows, which chat-window.js keeps in a window.
//
// Long conversations (D130): a block's markdown is parsed once — memoized on
// the block object, which the fold hands back unchanged until its message
// changes — and the answer being written re-parses only its last paragraph
// (mdInto), so a selection above it survives the stream.
//
// Seams (web-ext.js, as ui.ext): a feature module may draw a block its own
// way (ext.block, asked first) and add what follows the transcript (ext.end;
// a harness park is then its to draw — see sessionTpl).
import { html, nothing, repeat, unsafeHTML, classMap, directive, Directive, noChange } from '/vendor/lit-all.min.js';
import { md, mdInto, mdEpoch, onHighlighter } from './chat-md.js';
import { ctx } from './web-ext.js';
import { copyAnswer } from './chat-copy.js'; // copying with the formatting: an answer's Copy, the transcript's Ctrl+C (D199)
import { compactionWords } from './model/rules.js';
import { activityStill } from './model/harness.js';
import { docStepTpl } from './doc-cards.js'; // a document the agent wrote (D201)
import { kitRow, kitPark, KIT, ownViews } from './chat-kit.js'; // the kinds xbind's chat kit draws (D224), and what a run waits on a person for

// mdOf(b, slot, text): the HTML of a block's markdown, parsed once per block
// object (a changed message is a new block: parsed again).
// A memo is of one mdEpoch: once the code highlighter arrives (chat-md.js)
// the blocks are parsed again, and a paint is asked for.
const mdMemo = new WeakMap();
function mdOf(b, slot, text) {
  let m = mdMemo.get(b);
  if (!m || m.$epoch !== mdEpoch) mdMemo.set(b, (m = { $epoch: mdEpoch }));
  return m[slot] ?? (m[slot] = md(text));
}
onHighlighter(() => ctx.paint());
// mdLive(text): markdown written into the element it sits on, a top-level
// block at a time (chat-md.js mdInto) — the answer being streamed.
class MdLive extends Directive {
  render() { return noChange; }
  update(part, [text]) { mdInto(part.element, text); return noChange; }
}
const mdLive = directive(MdLive);

// the glyphs (D184): a line's own, a fold's caret
const icon = (name) => (name ? html`<bx-icon name=${name}></bx-icon>` : nothing);
const caret = (open) => html`<span class="tw"><bx-icon name=${open ? 'caret-down' : 'caret-right'}></bx-icon></span>`;

const secs = (ms) => {
  const s = Math.max(1, Math.round(ms / 1000));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
};

// ui: {isOpen(id, dflt), toggle(id, dflt), act: {...}, ext?}
export function blocksTpl(blocks, ui, depth = 0) {
  return repeat(blocks, (b) => b.id, (b) => blockTpl(b, ui, depth));
}

function blockTpl(b, ui, depth) {
  const x = ui.ext?.block(b, ui, depth);
  if (x) return x;
  if (KIT.has(b.k)) return kitRow(b, ui, depth);
  return ownTpl(b, ui, depth);
}

// the kinds still drawn here (also inside a card the kit draws: chat-kit.js asks)
function ownTpl(b, ui, depth) {
  switch (b.k) {
    case 'user': return userTpl(b, ui);
    case 'notice': return noticeTpl(b, ui);
    case 'think': return thinkTpl(b, ui);
    case 'assistant': return html`<div class="msg assistant" data-k=${b.id}><div class="md">${unsafeHTML(mdOf(b, 'text', b.text))}</div>
      <div class="acts"><button class="act" title="copy this answer (with its formatting)" aria-label="copy this answer"
        @click=${(e) => copyAnswer(e, b.text)}><bx-icon name="copy"></bx-icon></button></div></div>`;
    case 'draft': return html`<div class="msg assistant live" data-k=${b.id}><div class="md" ${mdLive(b.text)}></div><span class="cur"></span></div>`;
    case 'agent': return agentTpl(b, ui, depth);
    case 'step': return stepTpl(b, ui);
  }
  return nothing;
}

ownViews(ownTpl);

function userTpl(b, ui) {
  // someone else in a shared conversation: say who
  const other = b.sender && ui.me && b.sender !== ui.me();
  return html`<div class="msg user ${other ? 'other' : ''}" data-k=${b.id}>
    ${other ? html`<div class="who">${b.sender}</div>` : nothing}
    ${b.text ? html`<div class="txt">${b.text}</div>` : nothing}
    ${b.files && b.files.length ? html`<div class="afiles">${b.files.map((f) => {
      const st = ui.file(b.msgId, f);
      const kind = st.thumb ? '' : /^image\//.test(f.mime) ? 'photo' : /^text\/|^text$/.test(f.mime) ? 'file' : 'box';
      const pic = st.thumb ? html`<img src=${st.thumb} alt="">` : icon(kind);
      return html`<span class="afile ${st.linked ? '' : 'gone'}" data-afile=${st.linked ? f.path : nothing}
          title=${st.linked ? `${f.mime} — open in the Files tab` : 'deleted'} @click=${st.linked ? () => ui.act.openFile(f.path) : null}>
        <span class="ic">${pic}</span><span class="mono">${f.path}</span><span class="sz">${f.size}</span></span>`;
    })}</div>` : nothing}
  </div>`;
}

function noticeTpl(b, ui) {
  const open = ui.isOpen(b.id, false);
  const head = b.text.split('\n')[0].replace(/^\[|\]$/g, '').replace(/ — .*$/, '');
  return html`<div class="notice ${open ? 'on' : ''}" data-k=${b.id}>
    <div class="nh" @click=${() => ui.toggle(b.id, false)}>${caret(open)}${head}</div>
    ${open ? html`<div class="nb">${b.text.split('\n').slice(1).join('\n')}</div>` : nothing}
  </div>`;
}

function thinkTpl(b, ui) {
  const open = ui.isOpen(b.id, b.live);
  const label = b.live ? 'Thinking…' : b.ms ? `Thought for ${secs(b.ms)}` : 'Thought';
  return html`<div class="think ${b.live ? 'live' : ''} ${open ? 'on' : ''}" data-k=${b.id}>
    <div class="th" @click=${() => ui.toggle(b.id, b.live)}>${caret(open)}${label}</div>
    ${open ? html`<div class="tb">${b.text}</div>` : nothing}
  </div>`;
}

function agentTpl(b, ui, depth) {
  const running = b.state === 'running' || b.state === 'approval';
  const open = ui.isOpen(b.id, running);
  const child = b.child || {};
  const title = b.link && b.link.label ? b.link.label : b.headline;
  const phase = b.link && b.link.phase ? b.link.phase : child.status || '';
  const steps = child.llmCalls ? `${child.llmCalls} step${child.llmCalls === 1 ? '' : 's'}` : '';
  if (open && b.childId && !b.blocks) ui.act.loadChild(b.childId); // once: a failed read waits (model/session.js Failures)
  const readErr = open && b.childId && !b.blocks && ui.readError ? ui.readError(b.childId) : '';
  return html`<div class=${classMap({ acard: true, on: open, [b.state]: true })} data-k=${b.id}>
    <div class="ach" @click=${() => ui.toggle(b.id, running)}>
      <span class="ic">${icon('agent')}</span>
      <span class="hl">${title}</span>
      ${b.childId ? html`<span class="rid">#${b.childId}</span>` : nothing}
      ${running ? html`<span class="spin"></span>` : nothing}
      <span class="st">${b.state === 'done' ? (steps || 'done') : phase}</span>
      ${b.childId ? html`<button class="lnk" title="open this subagent's full session" @click=${(e) => { e.stopPropagation(); ui.act.select(b.childId); }}>open${icon('popout')}</button>` : nothing}
      ${caret(open)}
    </div>
    ${b.pendingApproval && b.child ? kitPark(b.child, ui) || nothing : nothing}
    ${approveNoteTpl(ui, b.childId)}
    ${open ? html`<div class="acb">
      ${b.task ? html`<div class="task ${ui.isOpen(b.id + ':task', false) ? 'on' : ''}" @click=${() => ui.toggle(b.id + ':task', false)}>
        <span class="k">task</span> ${b.task}</div>` : nothing}
      ${b.blocks ? blocksTpl(b.blocks, ui, depth + 1) : readErr ? html`<div class="small readfail" role="status"><span class="err">Couldn't read its steps: ${readErr}</span>
        <button class="lnk" data-act="retry" title="read it again" @click=${() => ui.act.retryRead(b.childId)}>Retry</button></div>`
      : html`<div class="muted small">loading…</div>`}
      ${!running && b.result && !b.result.startsWith('(') ? html`<div class="answer"><span class="k">answer</span>
        <div class="md">${unsafeHTML(mdOf(b, 'answer', stripHead(b.result)))}</div></div>` : nothing}
    </div>` : nothing}
  </div>`;
}

// A delivered child result starts with "--- #id title (outcome) ---".
const stripHead = (s) => String(s || '').replace(/^--- #\d+ .*? ---\n/, '');

// stepTpl: a journal line. finish's result is markdown (a model often puts
// its whole answer there); a render or a live page is a button that shows it
// again (ui.act.openPreview / openLive — agent.js).
function stepTpl(b, ui) {
  const d = b.detail || {};
  const act = (ui && ui.act) || {};
  // g: the line's glyph (a bx-icons name, D184); '' keeps a plain bullet
  let g = '', txt = '';
  switch (b.kind) {
    case 'error': g = 'warning'; txt = d.error || d.text || ''; break;
    case 'compaction': g = 'compact'; txt = compactionWords(d); break;
    case 'yield': g = 'pause'; txt = `slept ${d.seconds ?? ''}s`; break;
    case 'finish': if (!d.result) { g = 'check'; txt = 'finished'; break; }
      return html`<div class="step finish md-step" data-k=${b.id}><span class="g">${icon('check')}</span><div class="md">${unsafeHTML(mdOf(b, 'finish', d.result))}</div></div>`;
    case 'state_changed': g = 'info'; txt = `state changed${d.summary ? ': ' + d.summary : ''}`; break;
    case 'cancel': g = 'stop'; txt = `cancelled${d.reason ? ': ' + d.reason : ''}`; break;
    case 'ask': g = 'question'; txt = `asked: ${d.question || ''}`; break;
    case 'render': if (d.doc) return docStepTpl(b, act); g = 'photo'; txt = html`<button class="lnk steplnk" title="show it in the preview pane" @click=${() => act.openPreview?.(d.path, d.version, b.run, d.links)}>rendered ${d.path || ''} v${d.version || ''}</button>`; break;
    case 'live': g = 'signal'; txt = html`<button class="lnk steplnk" title="show it live in the preview pane" @click=${() => act.openLive?.(d, b.run)}>showing ${d.name || d.sandbox || 'the sandbox'}:${d.port || ''}${d.path || '/'} live</button>`; break;
    default: txt = d.text || '';
  }
  return html`<div class="step ${b.kind}" data-k=${b.id}><span class="g">${g ? icon(g) : '•'}</span> ${txt}</div>`;
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
