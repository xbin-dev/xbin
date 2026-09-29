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
import { md, mdInto } from './chat-md.js';
import { ICON, argsShown } from './model/tool-heads.js';
import { grantAsk, compactionWords } from './model/rules.js';

// mdOf(b, slot, text): the HTML of a block's markdown, parsed once per block
// object (a changed message is a new block: parsed again).
const mdMemo = new WeakMap();
function mdOf(b, slot, text) {
  let m = mdMemo.get(b);
  if (!m) mdMemo.set(b, (m = {}));
  return m[slot] ?? (m[slot] = md(text));
}
// the one question shown at a time (the run's result while it asks)
let askMemo = { text: null, html: '' };
const mdAsk = (text) => (askMemo.text === text ? askMemo.html : (askMemo = { text, html: md(text) }).html);

// mdLive(text): markdown written into the element it sits on, a top-level
// block at a time (chat-md.js mdInto) — the answer being streamed.
class MdLive extends Directive {
  render() { return noChange; }
  update(part, [text]) { mdInto(part.element, text); return noChange; }
}
const mdLive = directive(MdLive);

const STATE_LABEL = {
  running: 'running', writing: 'writing', waiting: 'waiting', approval: 'needs approval',
  error: 'failed', stopped: 'stopped', done: '',
};

const secs = (ms) => {
  const s = Math.max(1, Math.round(ms / 1000));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
};
const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');

// ui: {isOpen(id, dflt), toggle(id, dflt), act: {...}, ext?}
export function blocksTpl(blocks, ui, depth = 0) {
  return repeat(blocks, (b) => b.id, (b) => blockTpl(b, ui, depth));
}

function blockTpl(b, ui, depth) {
  const x = ui.ext?.block(b, ui, depth);
  if (x) return x;
  switch (b.k) {
    case 'user': return userTpl(b, ui);
    case 'notice': return noticeTpl(b, ui);
    case 'think': return thinkTpl(b, ui);
    case 'assistant': return html`<div class="msg assistant" data-k=${b.id}><div class="md">${unsafeHTML(mdOf(b, 'text', b.text))}</div></div>`;
    case 'draft': return html`<div class="msg assistant live" data-k=${b.id}><div class="md" ${mdLive(b.text)}></div><span class="cur"></span></div>`;
    case 'tool': return toolTpl(b, ui, depth);
    case 'agent': return agentTpl(b, ui, depth);
    case 'step': return stepTpl(b, ui);
  }
  return nothing;
}

function userTpl(b, ui) {
  // someone else in a shared conversation: say who
  const other = b.sender && ui.me && b.sender !== ui.me();
  return html`<div class="msg user ${other ? 'other' : ''}" data-k=${b.id}>
    ${other ? html`<div class="who">${b.sender}</div>` : nothing}
    ${b.text ? html`<div class="txt">${b.text}</div>` : nothing}
    ${b.files && b.files.length ? html`<div class="afiles">${b.files.map((f) => {
      const st = ui.file(b.msgId, f);
      const icon = st.thumb ? html`<img src=${st.thumb} alt="">` : /^image\//.test(f.mime) ? '🖼' : /^text\/|^text$/.test(f.mime) ? '📄' : '📦';
      return html`<span class="afile ${st.linked ? '' : 'gone'}" data-afile=${st.linked ? f.path : nothing}
          title=${st.linked ? `${f.mime} — open in the Files tab` : 'deleted'} @click=${st.linked ? () => ui.act.openFile(f.path) : null}>
        <span class="ic">${icon}</span><span class="mono">${f.path}</span><span class="sz">${f.size}</span></span>`;
    })}</div>` : nothing}
  </div>`;
}

function noticeTpl(b, ui) {
  const open = ui.isOpen(b.id, false);
  const head = b.text.split('\n')[0].replace(/^\[|\]$/g, '').replace(/ — .*$/, '');
  return html`<div class="notice ${open ? 'on' : ''}" data-k=${b.id}>
    <div class="nh" @click=${() => ui.toggle(b.id, false)}>↵ ${head}</div>
    ${open ? html`<div class="nb">${b.text.split('\n').slice(1).join('\n')}</div>` : nothing}
  </div>`;
}

function thinkTpl(b, ui) {
  const open = ui.isOpen(b.id, b.live);
  const label = b.live ? 'Thinking…' : b.ms ? `Thought for ${secs(b.ms)}` : 'Thought';
  return html`<div class="think ${b.live ? 'live' : ''} ${open ? 'on' : ''}" data-k=${b.id}>
    <div class="th" @click=${() => ui.toggle(b.id, b.live)}><span class="tw">${open ? '▾' : '▸'}</span> ${label}</div>
    ${open ? html`<div class="tb">${b.text}</div>` : nothing}
  </div>`;
}

// argsOf: a call's arguments as shown, parsed once per block object.
const argMemo = new WeakMap();
function argsOf(b) {
  let a = argMemo.get(b);
  if (!a) argMemo.set(b, (a = argsShown(b.args)));
  return a;
}

function argRows(b) {
  const a = argsOf(b);
  const keys = Object.keys(a);
  if (!keys.length) return nothing;
  return html`<div class="args">${keys.map((k) => {
    const v = a[k];
    const s = typeof v === 'string' ? v : JSON.stringify(v, null, 2);
    const long = s.length > 80 || s.includes('\n');
    return html`<div class="arg"><span class="k">${k}</span>${long ? html`<pre class="v">${s}</pre>` : html`<span class="v">${s}</span>`}</div>`;
  })}</div>`;
}

function toolTpl(b, ui, depth = 0) {
  const open = ui.isOpen(b.id, false);
  const st = b.state;
  return html`<div class=${classMap({ tcard: true, on: open, [st]: true })} data-fam=${b.fam} data-tool=${b.name} data-k=${b.id}>
    <div class="tch" @click=${() => ui.toggle(b.id, false)} title=${b.name}>
      <span class="ic">${ICON[b.fam] || '•'}</span>
      <span class="hl">${b.headline}${b.sub ? html`<span class="sub">${b.sub}</span>` : nothing}</span>
      ${b.outcome ? html`<span class="oc ${b.outcome.tone}">${b.outcome.text}</span>` : nothing}
      ${st === 'running' || st === 'writing' ? html`<span class="spin"></span>` : nothing}
      ${STATE_LABEL[st] ? html`<span class="st">${STATE_LABEL[st]}</span>` : nothing}
      <span class="tw">${open ? '▾' : '▸'}</span>
    </div>
    ${open ? html`<div class="tcb">
      <div class="tname mono">${b.name}</div>
      ${argRows(b)}
      ${b.result && st !== 'running' ? resultTpl(b, ui) : nothing}
      ${b.kids ? html`<div class="acb">${blocksTpl(b.kids, ui, depth + 1)}</div>` : nothing}
    </div>` : nothing}
  </div>`;
}

function resultTpl(b, ui) {
  const full = ui.isOpen(b.id + ':full', false);
  const long = b.result.length > 1200;
  const text = long && !full ? b.result.slice(0, 1200) + '…' : b.result;
  return html`<div class="res">
    <pre>${text}</pre>
    ${long ? html`<button class="lnk" @click=${() => ui.toggle(b.id + ':full', false)}>${full ? 'show less' : `show all (${fmtN(b.result.length)} chars)`}</button>` : nothing}
  </div>`;
}

function agentTpl(b, ui, depth) {
  const running = b.state === 'running' || b.state === 'approval';
  const open = ui.isOpen(b.id, running);
  const child = b.child || {};
  const title = b.link && b.link.label ? b.link.label : b.headline;
  const phase = b.link && b.link.phase ? b.link.phase : child.status || '';
  const steps = child.llmCalls ? `${child.llmCalls} step${child.llmCalls === 1 ? '' : 's'}` : '';
  if (open && b.childId && !b.blocks) ui.act.loadChild(b.childId);
  return html`<div class=${classMap({ acard: true, on: open, [b.state]: true })} data-k=${b.id}>
    <div class="ach" @click=${() => ui.toggle(b.id, running)}>
      <span class="ic">⑂</span>
      <span class="hl">${title}</span>
      ${b.childId ? html`<span class="rid">#${b.childId}</span>` : nothing}
      ${running ? html`<span class="spin"></span>` : nothing}
      <span class="st">${b.state === 'done' ? (steps || 'done') : phase}</span>
      ${b.childId ? html`<button class="lnk" title="open this subagent's full session" @click=${(e) => { e.stopPropagation(); ui.act.select(b.childId); }}>open ↗</button>` : nothing}
      <span class="tw">${open ? '▾' : '▸'}</span>
    </div>
    ${b.pendingApproval ? approvalTpl(b.pendingApproval, (yes) => ui.act.approve(b.childId, yes), 'The subagent wants to run') : nothing}
    ${approveNoteTpl(ui, b.childId)}
    ${open ? html`<div class="acb">
      ${b.task ? html`<div class="task ${ui.isOpen(b.id + ':task', false) ? 'on' : ''}" @click=${() => ui.toggle(b.id + ':task', false)}>
        <span class="k">task</span> ${b.task}</div>` : nothing}
      ${b.blocks ? blocksTpl(b.blocks, ui, depth + 1) : html`<div class="muted small">loading…</div>`}
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
  let g = '•', txt = '';
  switch (b.kind) {
    case 'error': g = '⚠'; txt = d.error || d.text || ''; break;
    case 'compaction': g = '🗜'; txt = compactionWords(d); break;
    case 'yield': g = '⏸'; txt = `slept ${d.seconds ?? ''}s`; break;
    case 'finish': if (!d.result) { g = '✓'; txt = 'finished'; break; }
      return html`<div class="step finish md-step" data-k=${b.id}><span class="g">✓</span><div class="md">${unsafeHTML(mdOf(b, 'finish', d.result))}</div></div>`;
    case 'state_changed': g = '✳'; txt = `state changed${d.summary ? ': ' + d.summary : ''}`; break;
    case 'cancel': g = '⏹'; txt = `cancelled${d.reason ? ': ' + d.reason : ''}`; break;
    case 'ask': g = '?'; txt = `asked: ${d.question || ''}`; break;
    case 'render': g = '🖼'; txt = html`<button class="lnk steplnk" title="show it in the preview pane" @click=${() => act.openPreview?.(d.path, d.version, b.run)}>rendered ${d.path || ''} v${d.version || ''}</button>`; break;
    case 'live': g = '📡'; txt = html`<button class="lnk steplnk" title="show it live in the preview pane" @click=${() => act.openLive?.(d, b.run)}>showing ${d.name || d.sandbox || 'the sandbox'}:${d.port || ''}${d.path || '/'} live</button>`; break;
    default: txt = d.text || '';
  }
  return html`<div class="step ${b.kind}" data-k=${b.id}><span class="g">${g}</span> ${txt}</div>`;
}

// approvalTpl: the calls a run wants to run, approve or deny. grant (rules
// grantAsk) is a capability only the conversation's owner may allow (D111):
// once, or here for an hour.
export function approvalTpl(calls, decide, lead = 'The agent wants to run', grant = null) {
  return html`<div class="ask approve ${grant ? 'grant' : ''}">
    <b>${grant ? grant.lead : lead}:</b>
    <ul>${(calls || []).map((c) => html`<li class="mono">${c.function ? c.function.name : c}</li>`)}</ul>
    ${grant ? html`<div class="muted small">${grant.note}</div>` : nothing}
    ${!grant ? html`<button class="btn btnsm" @click=${() => decide(true)}>Approve</button>`
      : grant.canAllow ? html`<button class="btn btnsm" @click=${() => decide(true, 'once')}>Allow once</button>
        <button class="btn btnsm" @click=${() => decide(true, 'hour')}>Allow here for 1 hour</button>` : nothing}
    <button class="btn ghost btnsm" @click=${() => decide(false)}>Deny</button>
  </div>`;
}

// approveNoteTpl says why a verdict on runId's ask was refused (its ask is
// gone — Session.noteApprove), for a few seconds.
function approveNoteTpl(ui, runId) {
  const note = runId != null && ui.approveNote ? ui.approveNote(runId) : '';
  return note ? html`<div class="anote muted small" role="status">⚠ ${note}</div>` : nothing;
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
    ${atEnd && parked && ps.kind === 'approval'
      ? approvalTpl(ps.toolCalls, (yes, how) => ui.act.approve(r.id, yes, how, ps.park), undefined, grantAsk(r, ui.who ? ui.who() : null)) : nothing}
    ${atEnd ? approveNoteTpl(ui, r.id) : nothing}
    ${atEnd && parked && ps.kind !== 'approval' && r.result
      ? html`<div class="ask"><b>The agent is asking:</b><div class="md">${unsafeHTML(mdAsk(r.result))}</div>
          <div class="muted small">answer below to continue</div></div>` : nothing}
    ${atEnd && s.activity ? html`<div class="activity"><span class="spin"></span> ${s.activity}</div>` : nothing}
    ${s.conn === 'reconnecting' ? html`<div class="activity warn">live updates lost — reconnecting…</div>` : nothing}
    ${win && win.pill ? html`<div class="jumpw"><button class="jump" @click=${() => win.latest()}>${win.pill}</button></div>` : nothing}
  `;
}

// queueTpl is the strip of queued messages above the composer.
export function queueTpl(queued, remove) {
  if (!queued || !queued.length) return nothing;
  return html`${queued.map((q) => html`<span class="qchip" title="queued — delivered at the agent's next step">
    <span class="ql">queued</span><span class="qt">${q.text || '(files)'}</span>
    <button title="take it back" @click=${() => remove(q.id)}>✕</button></span>`)}`;
}
