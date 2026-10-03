// harness-cards.js — a coding harness's transcript on the web (D147
// §3.4, §4.3.2–§4.3.5, §8 U3), hooked on the web's seams (web-ext.js):
//
//   block  one card per ACP tool family — execute (the command, its output's
//          tail, the exit code), edit (a row per file, +a −d, unfolding to its
//          patch), read, search, fetch, delete / move, think, switch_mode,
//          other — and a harness-internal subagent (a Claude Task) with the
//          blocks that ran under it (the fold's kids) nested inside
//   top    the usage badge (context in use, cost), what the conversation
//          changed, and the 📋 plan pin — the harness's live plan, folded to
//          a line under the task pin (D133's pattern), unfolding to its entries
//
// A card keeps the built-in's frame (.tcard: .ic, .hl, .oc, .st; data-k,
// data-fam, data-tool), so the chat's window, its tests and its styles hold.
// The words are the model's (model/harness-heads.js, model/harness.js).
// Patches are highlighted by xbind's /vendor/bx-code.js (diffHTML), loaded on
// the first patch shown; without it (an older xbind, a workspace whose
// import map has no `lit`) they are drawn plain — same classes, one style.
import { html, nothing, unsafeHTML, classMap } from '/vendor/lit-all.min.js';
import { ext, ctx } from './web-ext.js';
import { blocksTpl } from './chat-cards.js';
import { ICON, argsShown, parseArgs } from './model/tool-heads.js';
import {
  isAcp, acpKind, acpChip, commandOf, outputTail, diffFiles, patchLines, placesOf,
  isSubagentCall, stepsWords, taskOf, resultText,
} from './model/harness-heads.js';
import { harnessOf, planOf, usageBadge, countsWords } from './model/harness.js';

const CLIP = 1200;   // a result's characters shown before "show all"
const TAIL = 20000;  // an output's last characters shown before "show all"
const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');
// the glyphs (D184): a family's, a chip's tone, a fold's caret
const icon = (name) => (name ? html`<bx-icon name=${name}></bx-icon>` : nothing);
const TONE_ICON = { ok: 'ok', bad: 'error', warn: 'warning' };
const caret = (open) => html`<span class="tw"><bx-icon name=${open ? 'caret-down' : 'caret-right'}></bx-icon></span>`;
// a plan entry's state, said by its square's title (the square's fill is the colour)
const PLAN_WORD = { pending: 'to do', in_progress: 'in progress', completed: 'done' };

ext.register({
  block: (b, ui, depth) => (b.k === 'tool' && (b.acp || isAcp(b.name)) ? cardTpl(b, ui, depth) : null),
  top: (v) => (v ? topTpl(v) : null),
});

// --- a call's card ------------------------------------------------------------------

const argMemo = new WeakMap(); // block → its parsed arguments
function argsOf(b) {
  let a = argMemo.get(b);
  if (!a) argMemo.set(b, (a = parseArgs(b.args)));
  return a;
}

function cardTpl(b, ui, depth) {
  const acp = b.acp || {};
  const kind = acpKind(b.name);
  const failed = b.state === 'error';
  const open = ui.isOpen(b.id, failed); // a failed call opens by itself
  const chip = acpChip(acp, b.state);
  const steps = isSubagentCall(b) ? stepsWords(b.kids) : '';
  const orphan = !depth && b.parent && !b.kids; // under a subagent call that isn't held here
  const busy = b.state === 'running' || b.state === 'writing';
  return html`<div class=${classMap({ tcard: true, hcard: true, on: open, [b.state]: true })} data-fam=${b.fam} data-tool=${b.name}
      data-kind=${kind} data-status=${acp.status || ''} data-k=${b.id}>
    <div class="tch" @click=${() => ui.toggle(b.id, failed)} title=${acp.title || b.name}>
      <span class="ic">${icon(ICON[b.fam])}</span>
      <span class="hl">${orphan ? html`<span class="hin" title="under a subagent's call further up">↳ </span>` : nothing}${b.headline}${b.sub ? html`<span class="sub">${b.sub}</span>` : nothing}</span>
      ${steps ? html`<span class="oc steps">${steps}</span>` : nothing}
      ${b.outcome ? html`<span class="oc ${b.outcome.tone}">${icon(TONE_ICON[b.outcome.tone])}${b.outcome.text}</span>` : nothing}
      ${busy ? html`<span class="spin"></span>` : nothing}
      ${chip ? html`<span class="st ${chip.tone}">${icon(TONE_ICON[chip.tone])}${chip.text}</span>` : nothing}
      ${caret(open)}
    </div>
    ${open ? html`<div class="tcb hcb">${metaTpl(b, acp)}${bodyTpl(b, acp, kind, ui, depth)}</div>` : nothing}
  </div>`;
}

// metaTpl: the call's name, the adapter's tool, and ACP's own title when the
// headline says something else.
function metaTpl(b, acp) {
  const title = acp.title && ![b.headline, acp.tool, commandOf(argsOf(b))].includes(acp.title) && !(b.sub && b.sub.endsWith(acp.title)) ? acp.title : '';
  return html`<div class="tname mono">${b.name}${acp.tool ? ` · ${acp.tool}` : ''}${title ? html` · <span class="htitle">${title}</span>` : nothing}</div>`;
}

function bodyTpl(b, acp, kind, ui, depth) {
  if (isSubagentCall(b)) return subagentTpl(b, acp, ui, depth);
  const a = argsOf(b);
  switch (kind) {
    case 'execute': return execTpl(b, acp, a, ui);
    case 'edit': return editTpl(b, acp, ui);
    case 'delete': if (diffFiles(acp).length) return editTpl(b, acp, ui); // falls through
    case 'read': case 'search': case 'move': return html`${placesTpl(acp)}${clipTpl(b, ui)}`;
    case 'fetch': return html`${urlTpl(a.url)}${clipTpl(b, ui)}`;
    case 'think': return html`${taskOf(a) ? html`<div class="hnote">${taskOf(a)}</div>` : nothing}${clipTpl(b, ui)}`;
    case 'switch_mode': return html`${typeof a.plan === 'string' && a.plan ? html`<pre class="hpre">${a.plan}</pre>` : nothing}${clipTpl(b, ui)}`;
  }
  return html`${rawTpl(b, ui)}${clipTpl(b, ui)}`;
}

// clipTpl: the call's result text, its first CLIP characters until "show all".
function clipTpl(b, ui, text = resultText(b)) {
  if (!text) return pendingTpl(b);
  const full = ui.isOpen(b.id + ':full', false);
  const long = text.length > CLIP;
  return html`<div class="res"><pre>${long && !full ? text.slice(0, CLIP) + '…' : text}</pre>
    ${long ? html`<button class="lnk" @click=${() => ui.toggle(b.id + ':full', false)}>${full ? 'show less' : `show all (${fmtN(text.length)} chars)`}</button>` : nothing}</div>`;
}

const pendingTpl = (b) => (b.state === 'approval' ? html`<div class="muted small">waiting for your approval</div>`
  : b.state === 'running' || b.state === 'writing' ? html`<div class="muted small">running…</div>` : nothing);

// placesTpl: the files and lines a call touched.
function placesTpl(acp) {
  const ps = placesOf(acp);
  return ps.length ? html`<div class="hplaces">${ps.map((p) => html`<span class="mono">${p}</span>`)}</div>` : nothing;
}

// urlTpl: a fetch's address, a link only when it is http(s).
function urlTpl(url) {
  if (typeof url !== 'string' || !url) return nothing;
  return /^https?:\/\//i.test(url) ? html`<div class="hplaces"><a class="mono" href=${url} target="_blank" rel="noopener noreferrer">${url}</a></div>`
    : html`<div class="hplaces"><span class="mono">${url}</span></div>`;
}

// rawTpl: an unknown call's raw input, behind a toggle.
function rawTpl(b, ui) {
  const a = argsShown(b.args);
  const keys = Object.keys(a);
  if (!keys.length) return nothing;
  const on = ui.isOpen(b.id + ':raw', false);
  return html`<button class="lnk" @click=${() => ui.toggle(b.id + ':raw', false)}>${icon(on ? 'caret-down' : 'caret-right')}raw input</button>
    ${on ? html`<pre class="hpre">${JSON.stringify(a, null, 2)}</pre>` : nothing}`;
}

// --- execute: the command, its output, the exit code ------------------------------------

function execTpl(b, acp, a, ui) {
  const cmd = commandOf(a) || acp.title || '';
  const hasOut = typeof acp.output === 'string';
  const full = ui.isOpen(b.id + ':full', false);
  const out = hasOut ? tailOf(acp, full) : null;
  const code = acp.exitCode;
  return html`${cmd ? html`<div class="hcmd"><pre class="mono">$ ${cmd}</pre>
      <button class="lnk" title="copy the command" @click=${(e) => copy(e, cmd)}>copy</button></div>` : nothing}
    ${hasOut ? html`<div class="res hout">
        ${out.dropped ? html`<div class="muted small">… ${fmtN(out.dropped)} bytes before this were not kept (the last 64 KiB are)</div>` : nothing}
        ${out.text ? html`<pre>${out.cut ? '…' : ''}${out.text}</pre>` : html`<div class="muted small">${b.state === 'running' ? 'no output yet' : 'no output'}</div>`}
        ${out.cut || full ? html`<button class="lnk" @click=${() => ui.toggle(b.id + ':full', false)}>${full ? 'show the end only' : `show all (${fmtN(out.cut + out.text.length)} chars)`}</button>` : nothing}
      </div>` : clipTpl(b, ui)}
    ${code != null ? html`<div class="hexit ${Number(code) === 0 ? 'ok' : 'bad'}">exit ${code}</div>` : nothing}`;
}

// tailOf: an output stripped once per tool row (a paint per streamed token
// must not strip 64 KiB again).
const outMemo = new WeakMap(); // acp → {true|false: outputTail}
function tailOf(acp, full) {
  let m = outMemo.get(acp);
  if (!m) outMemo.set(acp, (m = {}));
  return m[full] ?? (m[full] = outputTail(acp, full ? Infinity : TAIL));
}

function copy(e, text) {
  const btn = e.currentTarget;
  Promise.resolve().then(() => navigator.clipboard.writeText(text))
    .then(() => { btn.textContent = 'copied'; }, () => { btn.textContent = 'copy refused — select it'; })
    .finally(() => setTimeout(() => { btn.textContent = 'copy'; }, 1500));
}

// --- edit: a row per file, each unfolding to its patch ---------------------------------------

function editTpl(b, acp, ui) {
  const files = diffFiles(acp);
  if (!files.length) return html`${placesTpl(acp)}${clipTpl(b, ui)}`;
  return html`<div class="hfiles">${files.map((d, i) => {
    const key = `${b.id}:d:${d.path}`;
    const on = ui.isOpen(key, false);
    return html`<div class="hfile ${on ? 'on' : ''}" data-path=${d.path}>
      <div class="hfh" @click=${() => ui.toggle(key, false)} title=${on ? 'fold the patch' : 'show the patch'}>
        ${caret(on)}<span class="mono hpath">${d.path}</span>
        ${d.status !== 'modified' ? html`<span class="hst ${d.status}">${d.status}</span>` : nothing}
        <span class="hadd">+${d.add}</span><span class="hdel">−${d.del}</span>
      </div>
      ${on ? patchTpl(acp, i, d) : nothing}
    </div>`;
  })}</div>`;
}

// The highlighter: bx-code.js's diffHTML once imported (null: not asked
// yet; false: not there — patches stay plain).
let diffHTML = null;
let asked = false;
function highlighter() {
  if (!asked) {
    asked = true;
    import('/vendor/bx-code.js').then((m) => { diffHTML = typeof m.diffHTML === 'function' ? m.diffHTML : false; })
      .catch(() => { diffHTML = false; }).finally(() => ctx.paint());
  }
  return diffHTML || null;
}

// patchTpl: one file's patch, drawn once per diff and highlighter (the
// block's acp is the same object until its call changes).
const patchMemo = new WeakMap(); // acp → Map(index → {hl, tpl})
function patchTpl(acp, i, d) {
  if (!d.patch) return html`<div class="muted small hnopatch">${d.status === 'deleted' ? 'the file was deleted' : 'no patch to show'}</div>`;
  const hl = highlighter();
  let m = patchMemo.get(acp);
  if (!m) patchMemo.set(acp, (m = new Map()));
  const e = m.get(i);
  if (e && e.hl === hl) return e.tpl;
  let body;
  try { body = hl ? unsafeHTML(hl(d.patch)) : null; } catch { body = null; }
  const tpl = html`<pre class="hdiff ${hl ? 'hl hljs' : 'plain'}">${body || patchLines(d.patch).map((l) => html`<span class=${l.cls}>${l.text || ' '}</span>`)}</pre>
    ${d.truncated ? html`<div class="muted small">patch truncated — past 64 KiB it stops at a hunk</div>` : nothing}`;
  m.set(i, { hl, tpl });
  return tpl;
}

// --- a harness-internal subagent (Task): its steps inside it ---------------------------------

function subagentTpl(b, acp, ui, depth) {
  const task = taskOf(argsOf(b));
  const tkey = b.id + ':task';
  const answer = resultText(b);
  return html`${task ? html`<div class="task ${ui.isOpen(tkey, false) ? 'on' : ''}" @click=${() => ui.toggle(tkey, false)}><span class="k">task</span> ${task}</div>` : nothing}
    ${b.kids && b.kids.length ? html`<div class="acb hkids">${blocksTpl(b.kids, ui, depth + 1)}</div>`
      : html`<div class="muted small">${b.state === 'running' ? 'no steps yet' : 'its steps aren\'t held here'}</div>`}
    ${answer ? html`<div class="answer"><span class="k">answer</span>${clipTpl(b, ui, answer)}</div>` : pendingTpl(b)}`;
}

// --- the top bar: usage, what changed, the plan pin ---------------------------------------------

let planOpen = 0; // the run whose plan is unfolded

function topTpl(v) {
  const h = harnessOf(v.run);
  if (!h) return null;
  const u = usageBadge(h.usage);
  const c = countsWords(h.counts);
  const p = planOf(h);
  if (!u && !c && !p) return null;
  return html`${u ? html`<span class="badge husage ${u.tone}" title=${u.title}>${u.text}</span>` : nothing}
    ${c ? html`<span class="badge hcounts" title="what this conversation's coding agent did (across its restarts); each edit's patch is on its card">${c}</span>` : nothing}
    ${p ? planTpl(v.run.id, p) : nothing}`;
}

function planTpl(id, p) {
  const open = planOpen === id;
  const toggle = () => { planOpen = open ? 0 : id; ctx.paint(); };
  const next = p.entries.find((e) => e.status !== 'completed');
  const line = p.now ? `now: ${p.now}` : next ? `next: ${next.content}` : 'all done';
  return html`<div class="taskpin planpin ${open ? 'open' : ''}">
    <button class="tasktoggle" @click=${toggle} title=${open ? 'fold the plan' : 'the coding agent\'s plan, as it keeps it'}>${icon('clipboard')}Plan · ${p.done}/${p.total}${icon(open ? 'caret-down' : 'caret-right')}</button>
    ${open ? html`<ol class="planlist">${p.entries.map((e) => html`<li class="pe ${e.status || 'pending'}">
        <span class="pg" title=${PLAN_WORD[e.status] || PLAN_WORD.pending}></span><span class="pt">${e.content}</span>${e.priority === 'high' ? html`<span class="pp">high</span>` : nothing}</li>`)}</ol>`
      : html`<span class="taskline" title=${line}>${line}</span>`}
  </div>`;
}

// --- styles -----------------------------------------------------------------------------------

const CSS = `
.tch .st.warn { color: var(--bx-warn); }
.tch .st.bad { color: var(--bx-danger); }
.tch .st.run { color: var(--bx-text); }
.tch .hin { color: var(--bx-muted); }
.hcb .hnote { white-space: pre-wrap; color: var(--bx-muted); margin: 2px 0 4px; }
.hcb .hpre, .hcmd pre { margin: 0; font: var(--bx-font-code); white-space: pre-wrap; word-break: break-word;
  background: var(--bx-panel); border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 4px 8px; max-height: 320px; overflow: auto; }
.hcmd { display: flex; align-items: flex-start; gap: 8px; }
.hcmd pre { flex: 1; min-width: 0; }
.hout pre { max-height: 28em; }
.hexit { font: var(--bx-font-code); margin-top: 4px; }
.hexit.ok { color: var(--bx-ok); }
.hexit.bad { color: var(--bx-danger); }
.hplaces { display: flex; flex-wrap: wrap; gap: 4px 12px; font: var(--bx-font-meta); color: var(--bx-muted); margin: 2px 0 4px; }
.hplaces .mono, .hplaces a { font: var(--bx-font-code); }
.hplaces a { color: var(--bx-link); overflow-wrap: anywhere; }
.hfiles { display: grid; gap: 4px; margin-top: 4px; }
.hfile { border: 1px solid var(--bx-border); border-radius: var(--bx-radius); background: var(--bx-panel); min-width: 0; }
.hfh { display: flex; align-items: center; gap: 8px; padding: 2px 8px; cursor: pointer; min-width: 0; }
.hfh .hpath { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.hfh .hst { font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted); }
.hfh .hadd { color: var(--bx-diff-add); font: var(--bx-font-code); }
.hfh .hdel { color: var(--bx-diff-del); font: var(--bx-font-code); }
.hdiff { margin: 0; padding: 4px 0; border-top: 1px solid var(--bx-border); font: var(--bx-font-code);
  max-height: 480px; overflow: auto; white-space: pre; background: var(--bx-code-bg); }
.hdiff > span { display: block; padding: 0 8px; min-width: max-content; }
.hdiff .fh { color: var(--bx-muted); font-weight: 600; }
.hdiff .h { color: var(--bx-diff-hunk); background: var(--bx-diff-hunk-bg); }
.hdiff .d { background: var(--bx-diff-add-bg); }
.hdiff .a { background: var(--bx-diff-del-bg); }
.hdiff .hljs-comment, .hdiff .hljs-quote { color: var(--bx-syn-comment); font-style: italic; }
.hdiff .hljs-keyword, .hdiff .hljs-selector-tag { color: var(--bx-syn-keyword); }
.hdiff .hljs-built_in { color: var(--bx-syn-builtin); }
.hdiff .hljs-type, .hdiff .hljs-title.class_ { color: var(--bx-syn-type); }
.hdiff .hljs-string, .hdiff .hljs-regexp { color: var(--bx-syn-string); }
.hdiff .hljs-number, .hdiff .hljs-literal { color: var(--bx-syn-number); }
.hdiff .hljs-title, .hdiff .hljs-title.function_ { color: var(--bx-syn-function); }
.hdiff .hljs-attr, .hdiff .hljs-attribute, .hdiff .hljs-property { color: var(--bx-syn-attr); }
.hnopatch { padding: 2px 8px; border-top: 1px solid var(--bx-border); }
.hkids { margin-top: 4px; }
.hcb .answer .res { margin-top: 4px; }
.top .planpin { order: 1; }
.top .planpin .planlist { margin: 2px 0 0; padding: 0; list-style: none; display: grid; gap: 2px; max-height: 40vh; overflow: auto; }
.top .planpin .pe { display: flex; gap: 8px; align-items: baseline; }
/* a plan entry's square: hollow to do, the accent in progress, filled done */
.top .planpin .pg { flex: none; align-self: center; box-sizing: border-box; width: 8px; height: 8px; border: 1px solid var(--bx-muted); }
.top .planpin .pe.in_progress .pg { border-color: var(--bx-accent); background: var(--bx-accent); }
.top .planpin .pe.completed .pg { border-color: var(--bx-ok); background: var(--bx-ok); }
.top .planpin .pe.completed .pt { color: var(--bx-muted); }
.top .planpin .pp { font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted);
  border: 1px solid var(--bx-border-strong); border-radius: var(--bx-radius); padding: 0 4px; }
.badge.husage, .badge.hcounts { text-transform: none; letter-spacing: 0; font: var(--bx-font-code); }
.badge.husage.warn { color: var(--bx-warn); border-color: var(--bx-warn); }
.badge.husage.bad { color: var(--bx-danger); border-color: var(--bx-danger); }
`;
const style = document.createElement('style');
style.dataset.of = 'harness-cards';
style.textContent = CSS;
document.head.append(style);
