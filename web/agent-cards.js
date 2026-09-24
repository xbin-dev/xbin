/**
 * agent-cards.js — the Agent tab's tool, permission and plan cards (D77),
 * extracted from bx-agent (the frame-titlebar pattern: functions of the
 * element `a`, which owns the state and the actions). What they render comes
 * from the normalized tool record (agent-tools.js), following what mature
 * ACP clients converged on: a card per call titled by the harness's own
 * description (the command collapsed beneath it), text content rendered as
 * markdown, raw JSON only behind a toggle, terminal output as output, and a
 * plan approval as the plan itself with the agent's choices — never a
 * generic "Permission" card with "allow for the session" semantics.
 */
import { html, css, nothing } from 'lit';
import { md } from '/vendor/bx-md.js';
import { diffHTML, diffStats } from '/vendor/bx-code.js';
import { headline, commandOf, isPlanApproval, planText, stripAnsi, rawText, unifiedDiff, filesStat } from '/vendor/agent-tools.js';

export const KIND_ICON = { read: '📖', edit: '✏️', delete: '🗑️', move: '↪', search: '🔎', execute: '⚙', think: '💭', fetch: '🌐', switch_mode: '⇄', other: '•' };

const OUT_CAP = 20000; // chars of command output shown before "show all"
const running = (t) => t.status === 'pending' || t.status === 'in_progress';

// text content is markdown when it looks like it (the adapters fence command
// output and file reads); plain prose/output stays verbatim
const looksMarkdown = (s) => /```|^#{1,6}\s|^\s*[-*]\s|\*\*|\[[^\]]+\]\([^)]+\)/m.test(s);
function textBlock(s) {
  const t = String(s ?? '');
  return looksMarkdown(t) ? html`<div class="md" .innerHTML=${md(t)}></div>` : html`<pre>${t}</pre>`;
}

// +added/-removed across a tool's diff content and its snapshot diff
export function diffStat(t) {
  let add = 0, del = 0;
  for (const it of Array.isArray(t.content) ? t.content : []) {
    if (it.type === 'diff') { const st = diffStats(unifiedDiff(it.path, it.oldText, it.newText)); add += st.add; del += st.del; }
  }
  if (t.files) { const st = filesStat(t.files); add += st.add; del += st.del; }
  return add || del ? html` +${add}/-${del}` : nothing;
}

const FSTATUS = { added: 'A', modified: 'M', deleted: 'D', renamed: 'R', typechange: 'T' };

// a snapshot diff (files.changed: what a call or a turn changed on disk,
// from git trees — so a shell write shows as the real change): the files,
// then git's own patch
export function filesBlock(f, label) {
  const st = filesStat(f);
  return html`<details class="files">
    <summary>${label || 'Changed'} ${st.n} file${st.n === 1 ? '' : 's'} <span class="fadd">+${st.add}</span> <span class="fdel">−${st.del}</span></summary>
    <ul class="flist">${(f.changes || []).map((c) => html`<li><span class="fs ${c.status}" title=${c.status}>${FSTATUS[c.status] || '?'}</span>
      <span class="fp">${c.oldPath ? `${c.oldPath} → ` : ''}${c.path}</span>${c.binary ? html` <span class="muted">binary</span>` : html` <span class="fadd">+${c.add}</span> <span class="fdel">−${c.del}</span>`}</li>`)}</ul>
    ${f.patch && f.patch.text ? html`<pre class="diff" .innerHTML=${diffHTML(f.patch.text)}></pre>` : nothing}
    ${f.patch && f.patch.truncated ? html`<div class="muted">… the rest of the patch is too large to show</div>` : nothing}
  </details>`;
}

// what a whole turn changed, after its last card
export function changesCard(b) {
  return html`<div class="turn-changes">${filesBlock(b, 'This turn changed')}</div>`;
}

const hasContent = (t) => Array.isArray(t.content) && t.content.length > 0;

function contentItems(t, skipText) {
  const items = Array.isArray(t.content) ? t.content : [];
  return items.map((it) => {
    if (it.type === 'diff') return html`<pre class="diff" .innerHTML=${diffHTML(unifiedDiff(it.path, it.oldText, it.newText))}></pre>`;
    if (it.type === 'terminal') return t.output ? nothing : html`<div class="muted">${running(t) ? 'running…' : '(no output)'}</div>`;
    const text = it.content?.text ?? it.text ?? '';
    if (!String(text).trim() || text === skipText) return nothing;
    return textBlock(text);
  });
}

// a shell command: its first lines, the rest one click away, and a copy button
function commandBlock(cmd) {
  const lines = String(cmd).split('\n');
  const copy = html`<button class="copy" title="copy the command" @click=${(e) => { e.preventDefault(); navigator.clipboard?.writeText(cmd); }}>copy</button>`;
  if (lines.length <= 3) return html`<div class="cmdwrap"><pre class="cmd">${cmd}</pre>${copy}</div>`;
  return html`<details class="cmdx"><summary><pre class="cmd preview">${lines.slice(0, 3).join('\n')}</pre>
      <span class="more">show all ${lines.length} lines</span></summary>
    <div class="cmdwrap"><pre class="cmd">${cmd}</pre>${copy}</div></details>`;
}

function outputBlock(out) {
  const s = stripAnsi(out);
  if (!s.trim()) return nothing;
  if (s.length <= OUT_CAP) return html`<pre class="out">${s}</pre>`;
  return html`<details class="outx"><summary class="muted">… ${s.length - OUT_CAP} earlier characters — show all</summary><pre class="out">${s}</pre></details>
    <pre class="out">${s.slice(-OUT_CAP)}</pre>`;
}

function rawInputBlock(t) {
  if (t.rawInput == null || t.tk === 'execute' || t.tk === 'edit' || isPlanApproval(t)) return nothing;
  const txt = rawText(t.rawInput);
  return txt ? html`<details class="raw"><summary>raw input</summary><pre>${txt}</pre></details>` : nothing;
}

// the card for one tool call
export function toolCard(a, t) {
  const exec = t.tk === 'execute';
  const plan = isPlanApproval(t);
  const title = plan ? (t.title || 'Plan') : headline(t);
  const body = exec
    ? [commandOf(t) ? commandBlock(commandOf(t)) : nothing, t.output ? outputBlock(t.output) : hasContent(t) ? contentItems(t, t.label) : nothing, t.files ? filesBlock(t.files) : nothing]
    : plan
      ? [planText(t) ? html`<div class="md plan-md" .innerHTML=${md(planText(t))}></div>` : nothing]
      : [hasContent(t) ? contentItems(t) : nothing, t.output ? outputBlock(t.output) : nothing, t.files ? filesBlock(t.files) : nothing, rawInputBlock(t)];
  if (t.children) return subagentCard(a, t);
  return html`<details class="tool ${exec ? 'exec' : ''}" ?open=${t.status === 'failed'}>
    <summary><span class="ic">${KIND_ICON[t.tk] || KIND_ICON.other}</span>
      <span class="title" title=${exec ? commandOf(t) : t.title || ''}>${title}</span>
      ${t.exitCode != null && t.exitCode !== 0 ? html`<span class="chip failed">exit ${t.exitCode}</span>` : nothing}
      <span class="chip ${t.status}">${String(t.status).replace('_', ' ')}${diffStat(t)}</span></summary>
    ${body.some((x) => x !== nothing) ? html`<div class="body">${body}</div>` : nothing}
  </details>`;
}

// a subagent (Claude's Task/Agent call): its description, its prompt
// collapsed, and everything it did nested beneath — open while it works,
// folded to its answer when done
function subagentCard(a, t) {
  const live = running(t);
  const prompt = t.rawInput && typeof t.rawInput.prompt === 'string' ? t.rawInput.prompt : '';
  const kind = t.rawInput && typeof t.rawInput.subagent_type === 'string' ? t.rawInput.subagent_type : '';
  const steps = t.children.filter((c) => c.kind === 'tool').length;
  return html`<details class="tool sub" ?open=${live || t.status === 'failed'}>
    <summary><span class="ic">⧉</span>
      <span class="title" title=${prompt}>${kind ? html`<span class="muted">${kind}</span> ` : nothing}${headline(t)}</span>
      ${steps ? html`<span class="chip">${steps} step${steps === 1 ? '' : 's'}</span>` : nothing}
      <span class="chip ${t.status}">${String(t.status).replace('_', ' ')}</span></summary>
    <div class="body">
      ${prompt ? html`<details class="raw"><summary>prompt</summary><div class="md" .innerHTML=${md(prompt)}></div></details>` : nothing}
      <div class="children">${t.children.map((c) => a._block(c))}</div>
      ${!live && hasContent(t) ? html`<div class="answer">${contentItems(t)}</div>` : nothing}
    </div>
  </details>`;
}

const whoOf = (by) => (by === 'auto' ? 'a session rule' : by === 'cancel' ? 'cancel' : String(by || '').replace('user:', ''));

// a permission request: the plan card for a plan approval, else the call's
// headline, what exactly would run, and the agent's own options
export function permCard(a, b) {
  const tc = b.tool || {};
  if (isPlanApproval(tc)) return planCard(a, b);
  const view = { ...tc, tk: tc.kind, label: '' };
  const cmd = commandOf(view);
  const mt = b.meta && b.meta.title;
  const heading = mt && mt.trim() !== cmd.trim() ? mt : headline(view); // claude titles a Bash ask with the command itself
  if (b.by) {
    const opt = (b.options || []).find((o) => o.optionId === b.optionId);
    const verb = b.by === 'cancel' ? 'cancelled' : opt && /reject/.test(opt.kind || '') ? 'denied' : 'allowed';
    return html`<div class="perm settled-card"><div class="q">${heading}</div><div class="settled">${verb} by ${whoOf(b.by)}</div></div>`;
  }
  const opts = (b.options || []).slice();
  if (b.meta && b.meta.defaultToNo) opts.sort((x, y) => (/reject/.test(x.kind || '') ? -1 : 0) - (/reject/.test(y.kind || '') ? -1 : 0));
  const scoped = b.rule ? b.rule.scoped : true; // hide "for the session" when it can't be scoped
  const desc = (b.meta && b.meta.description) || '';
  return html`<div class="perm">
    <div class="q"><b>Permission</b> — ${heading}${desc ? html`<div class="desc">${desc}</div>` : nothing}</div>
    ${cmd ? commandBlock(cmd) : tc.rawInput != null ? html`<pre class="cmd">${rawText(tc.rawInput)}</pre>` : nothing}
    <div class="pbody">${contentItems({ ...view, status: 'pending' }, heading)}</div>
    ${scoped && b.rule && (b.rule.kind || b.rule.title) ? html`<div class="rulenote">“Allow for the session” auto-approves later ${b.rule.kind || ''} calls${b.rule.title ? html` titled “${b.rule.title}”` : ''}.</div>` : nothing}
    <div class="btns">
      ${opts.length
        ? opts.filter((o) => scoped || o.kind !== 'allow_always').map((o) => html`<button class="${/reject/.test(o.kind || '') ? 'deny' : 'allow'}" @click=${() => a._permit(b.pid, null, o.optionId)}>${o.name || o.optionId}</button>`)
        : html`<button class="allow" @click=${() => a._permit(b.pid, 'allow_once')}>Allow once</button>
           ${scoped ? html`<button class="allow" @click=${() => a._permit(b.pid, 'allow_always')}>Allow for the session</button>` : nothing}
           <button class="deny" @click=${() => a._permit(b.pid, 'reject_once')}>Deny</button>`}
    </div>
  </div>`;
}

// the plan approval (Claude's ExitPlanMode "Ready to code?", Codex's
// "Implement this plan?"): the plan as markdown, every choice the agent
// offers with its full label (they are modes — "clear context (37% used)",
// "bypass permissions" — not "remember this"), and, beside "keep planning",
// a box whose text goes in as the next message (Claude's TUI does the same)
function planCard(a, b) {
  const tc = b.tool || {};
  const text = planText(tc);
  const path = tc.rawInput && typeof tc.rawInput.planFilePath === 'string' ? tc.rawInput.planFilePath : '';
  const heading = (b.meta && b.meta.title) || tc.title || 'Approve the plan?';
  const opts = b.options || [];
  const planMd = html`<div class="md plan-md" .innerHTML=${md(text || '*(the agent sent no plan text)*')}></div>`;
  if (b.by) {
    const opt = opts.find((o) => o.optionId === b.optionId);
    const kept = b.by === 'cancel' || !opt || /reject/.test(opt.kind || '');
    return html`<div class="perm plan-card settled-card">
      <div class="q"><b>${kept ? 'Kept planning' : 'Plan approved'}</b>${opt && !kept ? html` — ${opt.name}` : nothing} <span class="settled">by ${whoOf(b.by)}</span></div>
      <details><summary class="muted">the plan</summary>${planMd}</details></div>`;
  }
  const allows = opts.filter((o) => !/reject/.test(o.kind || ''));
  const rejects = opts.filter((o) => /reject/.test(o.kind || ''));
  return html`<div class="perm plan-card">
    <div class="q"><b>${heading}</b>${path ? html`<div class="desc mono">${path}</div>` : nothing}</div>
    ${planMd}
    <div class="btns col">${allows.map((o, i) => html`<button class="allow ${i === 0 ? 'primary' : ''}" @click=${() => a._permit(b.pid, null, o.optionId)}>${o.name || o.optionId}</button>`)}</div>
    ${rejects.length ? html`<div class="reject-row">
      <textarea rows="2" placeholder="Tell the agent what to change (optional — sent as your next message)"
        .value=${a._planFeedback[b.pid] || ''} @input=${(e) => { a._planFeedback[b.pid] = e.target.value; }}></textarea>
      ${rejects.map((o) => html`<button class="deny" @click=${() => a._rejectPlan(b.pid, o.optionId)}>${o.name || o.optionId}</button>`)}
    </div>` : nothing}
  </div>`;
}

export const cardsCss = css`
  .muted { color: var(--bx-muted, #868f9a); font-size: 11.5px; }
  .mono { font-family: var(--bx-mono, ui-monospace, monospace); }
  .md > :first-child { margin-top: 0; } .md > :last-child { margin-bottom: 0; }
  .md pre { background: var(--bx-term-bg, #262c36); padding: 6px 8px; border-radius: 5px; overflow-x: auto; font: 11px var(--bx-mono, ui-monospace, monospace); }
  .md :not(pre) > code { background: var(--bx-term-bg, #262c36); padding: .1em .3em; border-radius: 3px; font-family: var(--bx-mono, ui-monospace, monospace); }
  .tool { border: 1px solid var(--bx-border, #363c45); border-radius: 6px; margin: 0 0 8px; overflow: hidden; }
  .tool > summary { list-style: none; cursor: pointer; padding: 5px 9px; display: flex; align-items: center; gap: 6px;
    font: 11px var(--bx-mono, ui-monospace, monospace); }
  .tool > summary::-webkit-details-marker { display: none; }
  .tool .title { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .chip { font: 9.5px var(--bx-mono, ui-monospace, monospace); text-transform: uppercase; letter-spacing: .03em;
    padding: 1px 5px; border-radius: 3px; background: var(--bx-panel-2, #2b3038); color: var(--bx-muted, #868f9a); white-space: nowrap; }
  .chip.completed { color: var(--bx-green, #4caf50); }
  .chip.failed, .chip.cancelled { color: var(--bx-red, #ef5350); }
  .chip.in_progress, .chip.pending { color: var(--bx-amber, #f2a71b); }
  .tool .body { padding: 6px 9px; border-top: 1px solid var(--bx-border, #363c45); display: flex; flex-direction: column; gap: 6px; }
  .tool .body:empty { display: none; }
  .tool pre, .perm pre { margin: 0; font: 11px var(--bx-mono, ui-monospace, monospace); white-space: pre-wrap; overflow-x: auto; }
  .cmdwrap { position: relative; }
  pre.cmd { background: var(--bx-term-bg, #262c36); border-radius: 5px; padding: 6px 8px; max-height: 320px; overflow: auto; }
  .copy { position: absolute; top: 3px; right: 3px; font: 10px var(--bx-mono, ui-monospace, monospace); border: 1px solid var(--bx-border, #363c45);
    background: var(--bx-panel, #23272e); color: var(--bx-muted, #868f9a); border-radius: 4px; padding: 0 5px; cursor: pointer; opacity: .6; }
  .copy:hover { opacity: 1; }
  .cmdx > summary { list-style: none; cursor: pointer; } .cmdx > summary::-webkit-details-marker { display: none; }
  .cmdx[open] > summary .preview { display: none; }
  .cmdx .more { font: 10.5px var(--bx-mono, ui-monospace, monospace); color: var(--bx-accent, #f5a623); }
  .cmdx[open] .more::after { content: ' (hide)'; }
  pre.out { color: var(--bx-text, #d4d9e0); border-left: 2px solid var(--bx-border, #363c45); padding-left: 8px; max-height: 360px; overflow: auto; }
  .raw > summary, .outx > summary { cursor: pointer; font: 10.5px var(--bx-mono, ui-monospace, monospace); color: var(--bx-muted, #868f9a); }
  .diff { font: 11px var(--bx-mono, ui-monospace, monospace); }
  .diff .fh { color: var(--bx-muted, #868f9a); display: block; }
  .diff .h { color: var(--bx-accent, #f5a623); display: block; }
  .diff .d { color: var(--bx-green, #4caf50); display: block; background: color-mix(in srgb, var(--bx-green, #4caf50) 12%, transparent); }
  .diff .a { color: var(--bx-red, #ef5350); display: block; background: color-mix(in srgb, var(--bx-red, #ef5350) 12%, transparent); }
  .diff .ctx { display: block; color: var(--bx-text, #d4d9e0); }
  .files > summary { cursor: pointer; font: 11px var(--bx-mono, ui-monospace, monospace); color: var(--bx-text, #d4d9e0); }
  .fadd { color: var(--bx-green, #4caf50); } .fdel { color: var(--bx-red, #ef5350); }
  .flist { list-style: none; margin: 4px 0; padding: 0; font: 11px var(--bx-mono, ui-monospace, monospace); }
  .flist li { display: flex; gap: 6px; align-items: baseline; }
  .flist .fp { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .fs { width: 1.2em; text-align: center; border-radius: 3px; font-weight: 700; color: var(--bx-muted, #868f9a); }
  .fs.added { color: var(--bx-green, #4caf50); } .fs.deleted { color: var(--bx-red, #ef5350); } .fs.modified, .fs.renamed { color: var(--bx-amber, #f2a71b); }
  .files pre.diff { max-height: 420px; overflow: auto; margin-top: 4px; }
  .turn-changes { border: 1px solid var(--bx-border, #363c45); border-radius: 6px; padding: 5px 9px; margin: 0 0 8px; }
  .tool.sub { border-color: color-mix(in srgb, var(--bx-accent, #f5a623) 45%, var(--bx-border, #363c45)); }
  .tool.sub .children { border-left: 2px solid color-mix(in srgb, var(--bx-accent, #f5a623) 45%, transparent); padding-left: 8px; }
  .tool.sub .children:empty { display: none; }
  .tool.sub .children .row { margin-bottom: 6px; }
  .tool.sub .answer { border-top: 1px dashed var(--bx-border, #363c45); padding-top: 6px; }
  .perm { border: 1px solid var(--bx-amber, #f2a71b); border-radius: 6px; padding: 8px 10px; margin: 0 0 10px;
    background: color-mix(in srgb, var(--bx-amber, #f2a71b) 8%, var(--bx-panel, #23272e)); display: flex; flex-direction: column; gap: 6px; }
  .perm .desc { color: var(--bx-muted, #868f9a); font-size: 12px; margin-top: 2px; }
  .perm .pbody:empty { display: none; }
  .perm .rulenote { color: var(--bx-muted, #868f9a); font-size: 11.5px; }
  .perm.settled-card { opacity: .8; }
  .perm .btns { display: flex; gap: 6px; flex-wrap: wrap; }
  .perm .btns.col { flex-direction: column; align-items: stretch; }
  .perm button { border: 1px solid var(--bx-border, #363c45); background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0);
    border-radius: 5px; padding: 4px 10px; cursor: pointer; font: 12px var(--bx-sans, system-ui); text-align: left; }
  .perm button.allow { border-color: var(--bx-green, #4caf50); }
  .perm button.allow.primary { background: color-mix(in srgb, var(--bx-green, #4caf50) 22%, var(--bx-panel, #23272e)); font-weight: 600; }
  .perm button.deny { border-color: var(--bx-red, #ef5350); }
  .perm .settled { color: var(--bx-muted, #868f9a); font: 11px var(--bx-mono, ui-monospace, monospace); }
  .plan-card { border-color: var(--bx-accent, #f5a623); background: color-mix(in srgb, var(--bx-accent, #f5a623) 6%, var(--bx-panel, #23272e)); }
  .plan-md { max-height: 50vh; overflow: auto; background: var(--bx-panel, #23272e); border: 1px solid var(--bx-border, #363c45);
    border-radius: 5px; padding: 8px 12px; font-size: 13px; }
  .reject-row { display: flex; gap: 6px; align-items: flex-end; }
  .reject-row textarea { flex: 1; resize: vertical; background: var(--bx-term-bg, #262c36); color: var(--bx-text, #d4d9e0);
    border: 1px solid var(--bx-border, #363c45); border-radius: 5px; padding: 5px 7px; font: 12px var(--bx-sans, system-ui); }
`;
