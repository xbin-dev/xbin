// chat-cards.js — how the chat draws its blocks (chat-fold.js): lit
// templates rendered into the timeline, keyed by block id so a streamed token
// or a settled result patches one card and leaves the rest — the selection,
// an open card, the scroll position — alone.
import { html, nothing, repeat, unsafeHTML, classMap } from '/vendor/lit-all.min.js';
import { md } from './chat-md.js';
import { ICON, argsShown } from './tool-heads.js';

const STATE_LABEL = {
  running: 'running', writing: 'writing', waiting: 'waiting', approval: 'needs approval',
  error: 'failed', stopped: 'stopped', done: '',
};

const secs = (ms) => {
  const s = Math.max(1, Math.round(ms / 1000));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
};
const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');

// ui: {isOpen(id, dflt), toggle(id, dflt), act: {...}}
export function blocksTpl(blocks, ui, depth = 0) {
  return repeat(blocks, (b) => b.id, (b) => blockTpl(b, ui, depth));
}

function blockTpl(b, ui, depth) {
  switch (b.k) {
    case 'user': return userTpl(b, ui);
    case 'notice': return noticeTpl(b, ui);
    case 'think': return thinkTpl(b, ui);
    case 'assistant': return html`<div class="msg assistant"><div class="md">${unsafeHTML(md(b.text))}</div></div>`;
    case 'draft': return html`<div class="msg assistant live"><div class="md">${unsafeHTML(md(b.text))}<span class="cur"></span></div></div>`;
    case 'tool': return toolTpl(b, ui);
    case 'agent': return agentTpl(b, ui, depth);
    case 'step': return stepTpl(b, ui);
  }
  return nothing;
}

function userTpl(b, ui) {
  // someone else in a shared conversation: say who
  const other = b.sender && ui.me && b.sender !== ui.me();
  return html`<div class="msg user ${other ? 'other' : ''}">
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
  return html`<div class="notice ${open ? 'on' : ''}">
    <div class="nh" @click=${() => ui.toggle(b.id, false)}>↵ ${head}</div>
    ${open ? html`<div class="nb">${b.text.split('\n').slice(1).join('\n')}</div>` : nothing}
  </div>`;
}

function thinkTpl(b, ui) {
  const open = ui.isOpen(b.id, b.live);
  const label = b.live ? 'Thinking…' : b.ms ? `Thought for ${secs(b.ms)}` : 'Thought';
  return html`<div class="think ${b.live ? 'live' : ''} ${open ? 'on' : ''}">
    <div class="th" @click=${() => ui.toggle(b.id, b.live)}><span class="tw">${open ? '▾' : '▸'}</span> ${label}</div>
    ${open ? html`<div class="tb">${b.text}</div>` : nothing}
  </div>`;
}

function argRows(raw) {
  const a = argsShown(raw);
  const keys = Object.keys(a);
  if (!keys.length) return nothing;
  return html`<div class="args">${keys.map((k) => {
    const v = a[k];
    const s = typeof v === 'string' ? v : JSON.stringify(v, null, 2);
    const long = s.length > 80 || s.includes('\n');
    return html`<div class="arg"><span class="k">${k}</span>${long ? html`<pre class="v">${s}</pre>` : html`<span class="v">${s}</span>`}</div>`;
  })}</div>`;
}

function toolTpl(b, ui) {
  const open = ui.isOpen(b.id, false);
  const st = b.state;
  return html`<div class=${classMap({ tcard: true, on: open, [st]: true })} data-fam=${b.fam} data-tool=${b.name}>
    <div class="tch" @click=${() => ui.toggle(b.id, false)} title=${b.name}>
      <span class="ic">${ICON[b.fam] || '•'}</span>
      <span class="hl">${b.headline}</span>
      ${st === 'running' || st === 'writing' ? html`<span class="spin"></span>` : nothing}
      ${STATE_LABEL[st] ? html`<span class="st">${STATE_LABEL[st]}</span>` : nothing}
      <span class="tw">${open ? '▾' : '▸'}</span>
    </div>
    ${open ? html`<div class="tcb">
      <div class="tname mono">${b.name}</div>
      ${argRows(b.args)}
      ${b.result && st !== 'running' ? resultTpl(b, ui) : nothing}
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
  return html`<div class=${classMap({ acard: true, on: open, [b.state]: true })}>
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
    ${open ? html`<div class="acb">
      ${b.task ? html`<div class="task ${ui.isOpen(b.id + ':task', false) ? 'on' : ''}" @click=${() => ui.toggle(b.id + ':task', false)}>
        <span class="k">task</span> ${b.task}</div>` : nothing}
      ${b.blocks ? blocksTpl(b.blocks, ui, depth + 1) : html`<div class="muted small">loading…</div>`}
      ${!running && b.result && !b.result.startsWith('(') ? html`<div class="answer"><span class="k">answer</span>
        <div class="md">${unsafeHTML(md(stripHead(b.result)))}</div></div>` : nothing}
    </div>` : nothing}
  </div>`;
}

// A delivered child result starts with "--- #id title (outcome) ---".
const stripHead = (s) => String(s || '').replace(/^--- #\d+ .*? ---\n/, '');

function stepTpl(b) {
  const d = b.detail || {};
  let g = '•', txt = '';
  switch (b.kind) {
    case 'error': g = '⚠'; txt = d.error || d.text || ''; break;
    case 'compaction': g = '🗜'; txt = `compacted ${d.messages || 0} message(s) into the summary`; break;
    case 'yield': g = '⏸'; txt = `slept ${d.seconds ?? ''}s`; break;
    case 'finish': g = '✓'; txt = d.result ? `finished: ${d.result}` : 'finished'; break;
    case 'state_changed': g = '✳'; txt = `state changed${d.summary ? ': ' + d.summary : ''}`; break;
    case 'cancel': g = '⏹'; txt = `cancelled${d.reason ? ': ' + d.reason : ''}`; break;
    case 'ask': g = '?'; txt = `asked: ${d.question || ''}`; break;
    case 'render': g = '🖼'; txt = `rendered ${d.path || ''} v${d.version || ''}`; break;
    default: txt = d.text || '';
  }
  return html`<div class="step ${b.kind}"><span class="g">${g}</span> ${txt}</div>`;
}

export function approvalTpl(calls, decide, lead = 'The agent wants to run') {
  return html`<div class="ask approve">
    <b>${lead}:</b>
    <ul>${(calls || []).map((c) => html`<li class="mono">${c.function ? c.function.name : c}</li>`)}</ul>
    <button class="btn btnsm" @click=${() => decide(true)}>Approve</button>
    <button class="btn ghost btnsm" @click=${() => decide(false)}>Deny</button>
  </div>`;
}

// sessionTpl is the whole chat of the selected run.
export function sessionTpl(s, ui) {
  const r = s.run || {};
  const ps = r.pendingState || {};
  return html`
    ${s.chain && s.chain.length ? html`<div class="crumbs">${s.chain.map((c) => html`
      <a @click=${() => ui.act.select(c.id)}>${c.title || '#' + c.id}</a> ›`)} <b>${r.title || '#' + r.id}</b></div>` : nothing}
    ${s.olderHidden ? html`<div class="muted small center">— earlier turns were compacted into the summary —</div>` : nothing}
    ${blocksTpl(s.blocks, ui)}
    ${r.status === 'waiting_input' && ps.kind === 'approval'
      ? approvalTpl(ps.toolCalls, (yes) => ui.act.approve(r.id, yes)) : nothing}
    ${r.status === 'waiting_input' && ps.kind !== 'approval' && r.result
      ? html`<div class="ask"><b>The agent is asking:</b><div class="md">${unsafeHTML(md(r.result))}</div>
          <div class="muted small">answer below to continue</div></div>` : nothing}
    ${s.activity ? html`<div class="activity"><span class="spin"></span> ${s.activity}</div>` : nothing}
    ${s.conn === 'reconnecting' ? html`<div class="activity warn">live updates lost — reconnecting…</div>` : nothing}
  `;
}

// queueTpl is the strip of queued messages above the composer.
export function queueTpl(queued, remove) {
  if (!queued || !queued.length) return nothing;
  return html`${queued.map((q) => html`<span class="qchip" title="queued — delivered at the agent's next step">
    <span class="ql">queued</span><span class="qt">${q.text || '(files)'}</span>
    <button title="take it back" @click=${() => remove(q.id)}>✕</button></span>`)}`;
}
