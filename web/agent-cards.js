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
 *
 * Long transcripts (D124): what a card derives — markdown, diffs, stripped
 * output — is memoized on its block (cached(), per version), and a folded
 * card's body renders only once the reader opens it (a._isOpen/_toggled).
 */
import { html, css, nothing, noChange, directive, Directive, unsafeCSS } from 'lit';
import { md, mdInto, mdCssText } from '/vendor/bx-md.js';
import { diffHTML, diffStats, codeCss } from '/vendor/bx-code.js';
import '/vendor/bx-icons.js';
import { headline, commandOf, isPlanApproval, planText, stripAnsi, rawText, unifiedDiff, filesStat, formFields, formContent } from '/vendor/agent-tools.js';
import { cached } from '/vendor/agent-fold.js';

// mdLive(text, block): markdown rendered into the element it sits on, a
// top-level block at a time (bx-md.js mdInto) — for text that streams: only
// the last paragraph re-parses and the rest keep their DOM (a selection
// survives). The parsed blocks are memoized on the block (its $memo, which
// the window drops far from the view).
class MdLive extends Directive {
  render() { return noChange; }
  update(part, [text, b]) {
    const memo = b ? ((b.$memo ||= Object.create(null)).mdparts ||= {}) : null;
    mdInto(part.element, text, memo);
    return noChange;
  }
}
export const mdLive = directive(MdLive);

// a tool call's kind → its glyph (/vendor/bx-icons.js, D184); 'other' has none
export const KIND_ICON = Object.freeze({ read: 'doc', edit: 'pencil', delete: 'trash', move: 'arrow-right', search: 'search', execute: 'terminal', think: 'thought', fetch: 'globe', switch_mode: 'refresh', other: '' });
const kindIcon = (tk) => (KIND_ICON[tk] ? html`<bx-icon class="ic" name=${KIND_ICON[tk]}></bx-icon>` : html`<span class="ic"></span>`);
// a status badge: its glyph, its word, its colour (R2)
const STATUS_ICON = { completed: 'ok', failed: 'error', cancelled: 'error', pending: 'wait', in_progress: 'wait' };
const statusIcon = (st) => (STATUS_ICON[st] ? html`<bx-icon name=${STATUS_ICON[st]}></bx-icon>` : nothing);

const OUT_CAP = 20000; // chars of command output shown before "show all"
const running = (t) => t.status === 'pending' || t.status === 'in_progress';

// text content is markdown when it looks like it (the adapters fence command
// output and file reads); plain prose/output stays verbatim
const looksMarkdown = (s) => /```|^#{1,6}\s|^\s*[-*]\s|\*\*|\[[^\]]+\]\([^)]+\)/m.test(s);
function textBlock(s, owner, slot) {
  const t = String(s ?? '');
  return looksMarkdown(t) ? html`<div class="md" .innerHTML=${cached(owner, slot, () => md(t))}></div>` : html`<pre>${t}</pre>`;
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
// then git's own patch (highlighted once opened); owner is the block it hangs on
export function filesBlock(a, owner, f, label) {
  const st = filesStat(f);
  const open = a._isOpen(owner, 'files');
  return html`<details class="files" @toggle=${(e) => a._toggled(e, owner, 'files', false)}>
    <summary>${label || 'Changed'} ${st.n} file${st.n === 1 ? '' : 's'} <span class="fadd">+${st.add}</span> <span class="fdel">−${st.del}</span></summary>
    <ul class="flist">${(f.changes || []).map((c) => html`<li><span class="fs ${c.status}" title=${c.status}>${FSTATUS[c.status] || '?'}</span>
      <span class="fp">${c.oldPath ? `${c.oldPath} → ` : ''}${c.path}</span>${c.binary ? html` <span class="muted">binary</span>` : html` <span class="fadd">+${c.add}</span> <span class="fdel">−${c.del}</span>`}</li>`)}</ul>
    ${open && f.patch && f.patch.text ? html`<pre class="diff" .innerHTML=${cached(owner, 'patch', () => diffHTML(f.patch.text))}></pre>` : nothing}
    ${f.patch && f.patch.truncated ? html`<div class="muted">… the rest of the patch is too large to show</div>` : nothing}
  </details>`;
}

// what a whole turn changed, after its last card
export function changesCard(a, b) {
  return html`<div class="turn-changes">${filesBlock(a, b, b, 'This turn changed')}</div>`;
}

const hasContent = (t) => Array.isArray(t.content) && t.content.length > 0;

function contentItems(t, skipText) {
  const items = Array.isArray(t.content) ? t.content : [];
  return items.map((it, i) => {
    if (it.type === 'diff') return html`<pre class="diff" .innerHTML=${cached(t, 'diff' + i, () => diffHTML(unifiedDiff(it.path, it.oldText, it.newText)))}></pre>`;
    if (it.type === 'terminal') return t.output ? nothing : html`<div class="muted">${running(t) ? 'running…' : '(no output)'}</div>`;
    const text = it.content?.text ?? it.text ?? '';
    if (!String(text).trim() || text === skipText) return nothing;
    return textBlock(text, t, 'txt' + i);
  });
}

// a shell command: its first lines, the rest one click away, and a copy button
function commandBlock(cmd) {
  const lines = String(cmd).split('\n');
  const copy = html`<button class="copy" title="copy the command" @click=${(e) => { e.preventDefault(); navigator.clipboard?.writeText(cmd); }}><bx-icon name="copy"></bx-icon>copy</button>`;
  if (lines.length <= 3) return html`<div class="cmdwrap"><pre class="cmd">${cmd}</pre>${copy}</div>`;
  return html`<details class="cmdx"><summary><pre class="cmd preview">${lines.slice(0, 3).join('\n')}</pre>
      <span class="more">show all ${lines.length} lines</span></summary>
    <div class="cmdwrap"><pre class="cmd">${cmd}</pre>${copy}</div></details>`;
}

function outputBlock(t) {
  const s = cached(t, 'out', () => stripAnsi(t.output));
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

// the card for one tool call (its body renders once opened)
export function toolCard(a, t) {
  if (t.children) return subagentCard(a, t);
  const exec = t.tk === 'execute';
  const plan = isPlanApproval(t);
  const title = plan ? (t.title || 'Plan') : headline(t);
  const dflt = t.status === 'failed';
  const body = !(dflt || a._isOpen(t, 'body')) ? []
    : exec
      ? [commandOf(t) ? commandBlock(commandOf(t)) : nothing, t.output ? outputBlock(t) : hasContent(t) ? contentItems(t, t.label) : nothing, t.files ? filesBlock(a, t, t.files) : nothing]
      : plan
        ? [planText(t) ? html`<div class="md plan-md" .innerHTML=${cached(t, 'plan', () => md(planText(t)))}></div>` : nothing]
        : [hasContent(t) ? contentItems(t) : nothing, t.output ? outputBlock(t) : nothing, t.files ? filesBlock(a, t, t.files) : nothing, rawInputBlock(t)];
  // a finished command shows its exit status in place of the generic word (product-ui §8)
  const exited = t.exitCode != null && !running(t);
  return html`<details class="tool ${exec ? 'exec' : ''}" ?open=${dflt} @toggle=${(e) => a._toggled(e, t, 'body', dflt)}>
    <summary>${kindIcon(t.tk)}
      <span class="title" title=${exec ? commandOf(t) : t.title || ''}>${title}</span>
      ${exited ? html`<span class="chip ${t.exitCode === 0 ? 'completed' : 'failed'}">${statusIcon(t.exitCode === 0 ? 'completed' : 'failed')}exit ${t.exitCode}${cached(t, 'stat', () => diffStat(t))}</span>`
        : html`<span class="chip ${t.status}">${statusIcon(t.status)}${String(t.status).replace('_', ' ')}${cached(t, 'stat', () => diffStat(t))}</span>`}</summary>
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
  const dflt = live || t.status === 'failed';
  return html`<details class="tool sub" ?open=${dflt} @toggle=${(e) => a._toggled(e, t, 'body', dflt)}>
    <summary><bx-icon class="ic" name="agent"></bx-icon>
      <span class="title" title=${prompt}>${kind ? html`<span class="muted">${kind}</span> ` : nothing}${headline(t)}</span>
      ${steps ? html`<span class="chip">${steps} step${steps === 1 ? '' : 's'}</span>` : nothing}
      <span class="chip ${t.status}">${statusIcon(t.status)}${String(t.status).replace('_', ' ')}</span></summary>
    ${dflt || a._isOpen(t, 'body') ? html`<div class="body">
      ${prompt ? html`<details class="raw"><summary>prompt</summary><div class="md" .innerHTML=${cached(t, 'prompt', () => md(prompt))}></div></details>` : nothing}
      <div class="children">${t.children.map((c) => a._block(c))}</div>
      ${!live && hasContent(t) ? html`<div class="answer">${contentItems(t)}</div>` : nothing}
    </div>` : nothing}
  </details>`;
}

// a question the agent asks (an elicitation: Claude's AskUserQuestion, an
// MCP server's form): each question with its choices — radios or checkboxes,
// an option's description beside it — and its "Other" box; Submit answers,
// Skip declines (the agent hears the user skipped). Settled, it shows what
// was answered.
export function askCard(a, b) {
  const fields = formFields(b.schema);
  const who = whoOf(b.by);
  if (b.action) {
    const c = b.content || {};
    const verb = b.action === 'accept' ? 'answered' : b.action === 'decline' ? 'skipped' : 'cancelled';
    return html`<div class="perm ask settled-card"><div class="q md" .innerHTML=${cached(b, 'q', () => md(b.message || 'A question'))}></div>
      ${b.action === 'accept' ? html`<ul class="answers">${fields.map((f) => answerLine(f, c))}</ul>` : nothing}
      <div class="settled">${verb} by ${who}</div></div>`;
  }
  const vals = a._askVals[b.eid] || (a._askVals[b.eid] = {});
  const set = (k, v) => { vals[k] = v; a.requestUpdate(); };
  return html`<div class="perm ask">
    <div class="q md" .innerHTML=${md(b.message || 'The agent asks')}></div>
    ${fields.map((f) => fieldRow(f, vals, set, b.eid))}
    <div class="btns">
      <button class="allow primary" @click=${() => a._answer(b.eid, 'accept', formContent(fields, vals), fields)}>Submit</button>
      <button class="deny" @click=${() => a._answer(b.eid, 'decline')}>Skip</button>
    </div>
  </div>`;
}

function fieldRow(f, vals, set, eid) {
  const opt = (o, input) => html`<label class="opt">${input}<span><b>${o.title}</b>${o.description ? html` <span class="od">${o.description}</span>` : nothing}</span></label>`;
  let body;
  if (f.kind === 'radio') {
    body = f.options.map((o) => opt(o, html`<input type="radio" name=${eid + '-' + f.key} .checked=${vals[f.key] === o.value} @change=${() => set(f.key, o.value)}>`));
  } else if (f.kind === 'check') {
    const cur = Array.isArray(vals[f.key]) ? vals[f.key] : [];
    body = f.options.map((o) => opt(o, html`<input type="checkbox" .checked=${cur.includes(o.value)}
      @change=${(e) => set(f.key, e.target.checked ? [...cur.filter((x) => x !== o.value), o.value] : cur.filter((x) => x !== o.value))}>`));
  } else if (f.kind === 'bool') {
    body = html`<label class="opt"><input type="checkbox" .checked=${vals[f.key] === true} @change=${(e) => set(f.key, e.target.checked)}><span>yes</span></label>`;
  } else {
    body = html`<input class="txt" type=${f.kind === 'number' ? 'number' : 'text'} .value=${vals[f.key] ?? ''} @input=${(e) => set(f.key, e.target.value)}>`;
  }
  return html`<div class="field">
    ${f.title ? html`<div class="fh">${f.title}${f.required ? ' *' : ''}</div>` : nothing}
    ${f.description ? html`<div class="fd">${f.description}</div>` : nothing}
    <div class="opts">${body}</div>
    ${f.other ? html`<input class="txt other" type="text" placeholder=${f.otherHint || 'Other…'} .value=${vals[f.other] ?? ''} @input=${(e) => set(f.other, e.target.value)}>` : nothing}
  </div>`;
}

function answerLine(f, c) {
  const v = c[f.key];
  const o = f.other ? c[f.other] : undefined;
  const shown = [Array.isArray(v) ? v.join(', ') : v, o].filter((x) => x != null && x !== '').join(' — ');
  return shown ? html`<li><span class="muted">${f.title || f.description || f.key}:</span> ${shown}</li>` : nothing;
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
  // the agent's first allow option is the primary button; the rest secondary (product-ui §8)
  const first = opts.find((o) => !/reject/.test(o.kind || ''));
  return html`<div class="perm">
    <div class="q"><b><bx-icon name="warning"></bx-icon>Permission</b> — ${heading}${desc ? html`<div class="desc">${desc}</div>` : nothing}</div>
    ${cmd ? commandBlock(cmd) : tc.rawInput != null ? html`<pre class="cmd">${rawText(tc.rawInput)}</pre>` : nothing}
    <div class="pbody">${contentItems({ ...view, status: 'pending' }, heading)}</div>
    ${scoped && b.rule && (b.rule.kind || b.rule.title) ? html`<div class="rulenote">“Allow for the session” auto-approves later ${b.rule.kind || ''} calls${b.rule.title ? html` titled “${b.rule.title}”` : ''}.</div>` : nothing}
    <div class="btns">
      ${opts.length
        ? opts.filter((o) => scoped || o.kind !== 'allow_always').map((o) => html`<button class="${/reject/.test(o.kind || '') ? 'deny' : o === first ? 'allow primary' : 'allow'}" @click=${() => a._permit(b.pid, null, o.optionId)}>${o.name || o.optionId}</button>`)
        : html`<button class="allow primary" @click=${() => a._permit(b.pid, 'allow_once')}>Allow once</button>
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
  const planMd = html`<div class="md plan-md" .innerHTML=${cached(b, 'plan', () => md(text || '*(the agent sent no plan text)*'))}></div>`;
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

// The cards' styles (D184): code and diffs on codeCss (bx-code.js), rendered
// markdown on mdCssText (bx-md.js), everything else on the tokens — tool
// calls as code-face blocks, status as square badges with their glyph and
// word, a permission request in the warn tint, buttons per product-ui §6.
export const cardsCss = [codeCss, css`
  .muted { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
  .mono { font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); }
  .md > :first-child { margin-top: 0; } .md > :last-child { margin-bottom: 0; }
  ${unsafeCSS(mdCssText('.md'))}
  .tool { border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); margin: 0 0 8px; overflow: hidden; background: var(--bx-panel, #1F2028); }
  .tool > summary { list-style: none; cursor: pointer; min-height: var(--bx-row, 28px); box-sizing: border-box; padding: 4px 8px;
    display: flex; align-items: center; gap: 8px; font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); }
  .tool > summary:hover { background: var(--bx-hover, #2A2B34); }
  .tool > summary::-webkit-details-marker { display: none; }
  .tool .ic { flex: none; width: 16px; color: var(--bx-muted, #A3A6B6); }
  .tool .title { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  /* a status badge: square, 20px, micro caps, glyph + word + colour */
  .chip { display: inline-flex; align-items: center; gap: 4px; box-sizing: border-box; height: 20px; padding: 0 6px; white-space: nowrap; flex: none;
    font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase;
    font-variant-numeric: tabular-nums; border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); color: var(--bx-muted, #A3A6B6); }
  .chip bx-icon { --bx-icon-size: 12px; }
  .chip.completed { color: var(--bx-ok, #A3CF5E); background: var(--bx-ok-bg, #2F352E); border-color: currentColor; }
  .chip.failed, .chip.cancelled { color: var(--bx-danger, #FF7A7A); background: var(--bx-danger-bg, #3A2B32); border-color: currentColor; }
  .chip.in_progress, .chip.pending { color: var(--bx-warn, #F2994A); background: var(--bx-warn-bg, #382F2C); border-color: currentColor; }
  .tool .body { padding: 8px; border-top: 1px solid var(--bx-border, #33353F); display: flex; flex-direction: column; gap: 8px; }
  .tool .body:empty { display: none; }
  .tool pre, .perm pre { margin: 0; font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); white-space: pre-wrap; overflow-x: auto; }
  .cmdwrap { position: relative; }
  pre.cmd { background: var(--bx-code-bg, #16171D); border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); padding: 8px 12px; max-height: 320px; overflow: auto; }
  .copy { position: absolute; top: 4px; right: 4px; display: inline-flex; align-items: center; gap: 4px; padding: 0 6px; cursor: pointer;
    font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); border: 1px solid var(--bx-border, #33353F);
    background: var(--bx-panel, #1F2028); color: var(--bx-muted, #A3A6B6); border-radius: var(--bx-radius, 2px); }
  .copy:hover { color: var(--bx-text, #E9EAF0); border-color: var(--bx-border-strong, #666A7E); }
  .cmdx > summary { list-style: none; cursor: pointer; } .cmdx > summary::-webkit-details-marker { display: none; }
  .cmdx[open] > summary .preview { display: none; }
  .cmdx .more { font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); color: var(--bx-link, #8C9BFF); }
  .cmdx[open] .more::after { content: ' (hide)'; }
  pre.out { color: var(--bx-text, #E9EAF0); border-left: 2px solid var(--bx-border, #33353F); padding-left: 8px; max-height: 360px; overflow: auto; }
  .raw > summary, .outx > summary { cursor: pointer; font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); color: var(--bx-muted, #A3A6B6); }
  .diff { font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); background: var(--bx-code-bg, #16171D); }
  .files > summary { cursor: pointer; font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); color: var(--bx-text, #E9EAF0); }
  .fadd { color: var(--bx-diff-add, #5EDBA5); } .fdel { color: var(--bx-diff-del, #FF8F8F); }
  .fadd, .fdel { font-variant-numeric: tabular-nums; }
  .flist { list-style: none; margin: 4px 0; padding: 0; font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); }
  .flist li { display: flex; gap: 6px; align-items: baseline; }
  .flist .fp { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .fs { width: 1.2em; text-align: center; font-weight: 700; color: var(--bx-muted, #A3A6B6); }
  .fs.added { color: var(--bx-diff-add, #5EDBA5); } .fs.deleted { color: var(--bx-diff-del, #FF8F8F); } .fs.modified, .fs.renamed { color: var(--bx-warn, #F2994A); }
  .files pre.diff { max-height: 420px; overflow: auto; margin-top: 4px; }
  .turn-changes { border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); padding: 4px 8px; margin: 0 0 8px; }
  .tool.sub { border-color: var(--bx-border-strong, #666A7E); }
  .tool.sub .children { border-left: 2px solid var(--bx-border-strong, #666A7E); padding-left: 8px; }
  .tool.sub .children:empty { display: none; }
  .tool.sub .children .row { margin-bottom: 6px; }
  .tool.sub .answer { border-top: 1px solid var(--bx-border, #33353F); padding-top: 8px; }
  /* a permission request: attention, in the warn tint, its glyph and word */
  .perm { border: 1px solid var(--bx-warn, #F2994A); border-radius: var(--bx-radius, 2px); padding: 8px 12px; margin: 0 0 12px;
    background: var(--bx-warn-bg, #382F2C); display: flex; flex-direction: column; gap: 8px; }
  .perm .q > b bx-icon { color: var(--bx-warn, #F2994A); margin-right: 6px; }
  .perm .desc { color: var(--bx-muted, #A3A6B6); margin-top: 2px; }
  .perm .pbody:empty { display: none; }
  .perm .rulenote { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
  /* answered: nothing waits on it any more — a plain card */
  .perm.settled-card { border-color: var(--bx-border, #33353F); background: var(--bx-panel, #1F2028); }
  .perm .btns { display: flex; gap: 8px; flex-wrap: wrap; }
  .perm .btns.col { flex-direction: column; align-items: stretch; }
  .perm button { box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 4px 11px; cursor: pointer; text-align: left;
    border: 1px solid var(--bx-border-strong, #666A7E); background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
    border-radius: var(--bx-radius, 2px); font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif); font-weight: 600; }
  .perm button:hover { background: var(--bx-hover, #2A2B34); }
  .perm button.primary { background: var(--bx-accent, #8C9BFF); border-color: var(--bx-accent, #8C9BFF); color: var(--bx-accent-ink, #0B0C12); }
  .perm button.primary:hover { background: var(--bx-accent-hover, #A9B4FF); border-color: var(--bx-accent-hover, #A9B4FF); }
  .perm .settled { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
  .ask .field { border-top: 1px solid var(--bx-border, #33353F); padding-top: 8px; display: flex; flex-direction: column; gap: 4px; }
  .ask .fh { font-weight: 600; }
  .ask .fd { color: var(--bx-text, #E9EAF0); }
  .ask .opts { display: flex; flex-direction: column; gap: 2px; }
  .ask .opt { display: flex; gap: 6px; align-items: baseline; cursor: pointer; }
  .ask .opt input { accent-color: var(--bx-accent, #8C9BFF); }
  .ask .opt .od { color: var(--bx-muted, #A3A6B6); }
  .ask .txt { box-sizing: border-box; min-height: var(--bx-control-h, 28px); background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); padding: 4px 8px; font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif); }
  .ask .txt::placeholder, .reject-row textarea::placeholder { color: var(--bx-subtle, #8E91A2); opacity: 1; }
  .ask .answers { margin: 0; padding-left: 16px; }
  .plan-card { border-color: var(--bx-border-strong, #666A7E); background: var(--bx-panel, #1F2028); }
  .plan-md { max-height: 50vh; overflow: auto; background: var(--bx-panel-2, #262730); border: 1px solid var(--bx-border, #33353F);
    border-radius: var(--bx-radius, 2px); padding: 8px 12px; font: var(--bx-font-body, 400 14px/20px "Instrument Sans", system-ui, sans-serif); }
  .reject-row { display: flex; gap: 8px; align-items: flex-end; }
  .reject-row textarea { flex: 1; resize: vertical; box-sizing: border-box; background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); padding: 4px 8px; font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif); }
`];
