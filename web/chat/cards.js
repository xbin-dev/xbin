/**
 * chat/cards.js — the chat kit's cards (/vendor/chat/cards.js): a tool call
 * (its command, its terminal output, its diffs, what it changed on disk), a
 * subagent's call with everything that ran under it, a sub-conversation, a
 * turn's change set, a plan's entries. Each is a lit template function of
 * (block, ctx) — the block model's blocks (/vendor/chat/blocks.js), the
 * host through ChatCtx and nothing else — and the words are
 * /vendor/chat/tools.js's, read from the block's ToolRecord.
 *
 * A call's card is one line — its glyph, what it is called (the command
 * under a description, muted), what it came to (`exit 1`, `+3 −1`,
 * `12 matches`), a mark while it runs, its state only while that needs a
 * look (needs approval, failed, cancelled) — that unfolds to the call:
 * its name, what it ran, its output, its diffs, the places it touched.
 *
 * What a card derives — markdown, a diff's highlighted HTML, stripped output
 * — is memoized on its block (cached(), per version), and a folded card's
 * body is drawn only once the reader opens it (ctx.isOpen / ctx.setOpen).
 *
 * Diffs are drawn by /vendor/bx-code.js diffHTML — the highlighter the code
 * panels use — and that module is optional: it is imported the first time a
 * diff is drawn (a host that has it hands it over with useDiffHighlighter),
 * and until it arrives, or where it can't load (a page whose import map has
 * no `lit`), a patch is drawn plain with the same line classes
 * (tools.js patchLines) under the same styles (css.js .bxc-diff).
 */
import { html, nothing } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import { md } from '/vendor/chat/md.js';
import { cached } from '/vendor/chat/blocks.js';
import {
  headline, subline, outcome, stateChip, commandOf, isPlanApproval, planText, stripAnsi, rawText, filesStat, unifiedDiff, diffPatch, patchLines,
  placesOf, taskOf, OUTPUT_TAIL, DIFF_LINES, lineCount, headLines, diffBody,
} from '/vendor/chat/tools.js';

// ---- the diff highlighter (optional) ----------------------------------------------------

let hl = null; // bx-code.js diffHTML, once here
let langOf = null; // …and its langFor
let asked = false;
const waiting = new Set(); // repaints owed once it arrives

/** diffEpoch moves when the highlighter arrives: what was drawn plain is drawn again (a row's guard reads it). */
export let diffEpoch = 0;

/** useDiffHighlighter(diffHTML, langFor): hand over /vendor/bx-code.js's functions (a host that already imported it). */
export function useDiffHighlighter(diffHTML, langFor) {
  if (typeof diffHTML !== 'function' || hl === diffHTML) return;
  hl = diffHTML;
  langOf = typeof langFor === 'function' ? langFor : null;
  diffEpoch++;
  for (const fn of waiting) { try { fn(); } catch { /* a host that is gone */ } }
  waiting.clear();
}

function highlighter(ctx) {
  if (!hl && !asked) {
    asked = true;
    import('/vendor/bx-code.js').then((m) => useDiffHighlighter(m.diffHTML, m.langFor), () => { waiting.clear(); });
  }
  if (!hl && asked && ctx && typeof ctx.repaint === 'function') waiting.add(ctx.repaint);
  return hl;
}

/** diffView(text, ctx, lang): a unified diff as a <pre>, highlighted when the highlighter is here, else plain. */
export function diffView(text, ctx, lang = '') {
  const h = highlighter(ctx);
  const t = String(text ?? '').replace(/\n$/, ''); // (no empty row for the final newline)
  if (h) { try { return html`<pre class="bxc-diff hljs" .innerHTML=${h(t, lang)}></pre>`; } catch { /* plain */ } }
  return html`<pre class="bxc-diff">${patchLines(t).map((l) => html`<span class=${l.cls}>${l.cls === 'd' || l.cls === 'a' ? html`<span class="mk">${l.text[0]}</span>${l.text.slice(1)}` : l.text || ' '}</span>`)}</pre>`;
}

// a diff's first DIFF_LINES lines, then "Show all N lines" (slot + ':all'
// remembers the choice); what it draws memoized on memo, per highlighter
function capped(b, ctx, slot, memo, textOf, lang = '') {
  const all = ctx.isOpen(b, slot + ':all', false);
  highlighter(ctx);
  let h = memo.$view;
  if (!h || h.epoch !== diffEpoch) h = memo.$view = { epoch: diffEpoch, n: lineCount(textOf()) };
  const cut = !all && h.n > DIFF_LINES;
  const k = cut ? 'head' : 'all';
  h[k] ??= diffView(cut ? headLines(textOf(), DIFF_LINES) : textOf(), ctx, lang);
  return html`${h[k]}${cut ? html`<button class="bxc-more" @click=${() => ctx.setOpen(b, slot + ':all', true, false)}>Show all ${h.n} lines</button>` : nothing}`;
}

// ---- small parts ------------------------------------------------------------------------

/** A tool call's kind → its glyph (/vendor/bx-icons.js); 'other' has none. */
export const KIND_ICON = Object.freeze({ read: 'doc', edit: 'pencil', delete: 'trash', move: 'arrow-right', search: 'search', execute: 'terminal', think: 'thought', fetch: 'globe', switch_mode: 'refresh', other: '' });
// (a block's own `icon` — its reducer's glyph for a tool of its own — comes first)
const callIcon = (b) => { const g = b.icon ?? KIND_ICON[b.rec.kind]; return g ? html`<bx-icon class="bxc-ic" name=${g}></bx-icon>` : html`<span class="bxc-ic"></span>`; };

// a tone as its glyph and its class: an outcome's, a state's
const TONE_ICON = { ok: 'ok', bad: 'error', warn: 'warning' };
const toneIcon = (t) => (TONE_ICON[t] ? html`<bx-icon name=${TONE_ICON[t]}></bx-icon>` : nothing);
const toneClass = (t) => (t ? 'bxc-' + t : '');
const carets = html`<bx-icon class="bxc-cr" name="caret-right"></bx-icon><bx-icon class="bxc-cd" name="caret-down"></bx-icon>`;

/**
 * What a call's line ends with: what it came to (the block's own `outcome`
 * when its reducer words one, else tools.js outcome; a change set the host
 * observed beside it), the mark of a call at work, its state while that
 * needs saying (tools.js stateChip: nothing once it completed), the fold's
 * caret. `more`: chips before them (a subagent's steps).
 */
function lineEnd(b, more = nothing) {
  const rec = b.rec;
  const oc = b.outcome !== undefined ? b.outcome : outcome(rec);
  const chip = stateChip(rec, b.state);
  const set = rec.changed && !(oc && rec.diffs && rec.diffs.length) ? cached(b, 'stat', () => filesStat(rec.changed)) : null;
  const busy = live(rec) || b.state === 'writing' || b.state === 'running';
  return html`${more}${oc ? html`<span class="bxc-oc ${toneClass(oc.tone)}">${toneIcon(oc.tone)}${oc.text}</span>` : nothing}
    ${set && (set.add || set.del) ? html`<span class="bxc-oc"><span class="bxc-add">+${set.add}</span> <span class="bxc-del">−${set.del}</span></span>` : nothing}
    ${busy && b.state !== 'approval' ? html`<span class="bxc-spin"></span>` : nothing}
    ${chip ? html`<span class="bxc-st ${toneClass(chip.tone)}">${toneIcon(chip.tone)}${chip.text}</span>` : nothing}${carets}`;
}

const live = (rec) => rec.status === 'pending' || rec.status === 'in_progress';

// the fold of a <details>: it is drawn as the host holds it (ctx.isOpen, a
// default when the host holds nothing), and the host hears when the reader
// moves it — not when lit applies what the host already said
function toggled(e, b, ctx, slot, dflt) {
  const open = e.target.open;
  if (open === ctx.isOpen(b, slot, !!dflt)) return;
  ctx.setOpen(b, slot, open, !!dflt);
}

// text is markdown when it looks like it (the adapters fence command output
// and file reads); plain prose and output stay verbatim
const looksMarkdown = (s) => /```|^#{1,6}\s|^\s*[-*]\s|\*\*|\[[^\]]+\]\([^)]+\)/m.test(s);
export function textView(s, b, slot) {
  const t = String(s ?? '');
  return looksMarkdown(t) ? html`<div class="bxc-md" .innerHTML=${cached(b, slot, () => md(t))}></div>` : html`<pre class="bxc-res">${t}</pre>`;
}

const copyText = (ctx, text) => (ctx.copy ? ctx.copy(text) : navigator.clipboard?.writeText(text));

/** commandView(cmd, ctx): a shell command — its first lines, the rest one click away, and a copy button. */
export function commandView(cmd, ctx) {
  const lines = String(cmd).split('\n');
  const copy = html`<button class="bxc-copy" title="copy the command" @click=${(e) => { e.preventDefault(); copyText(ctx, cmd); }}><bx-icon name="copy"></bx-icon>copy</button>`;
  if (lines.length <= 3) return html`<div class="bxc-cmdwrap"><pre class="bxc-cmd">${cmd}</pre>${copy}</div>`;
  return html`<details class="bxc-cmdx"><summary><pre class="bxc-cmd bxc-preview">${lines.slice(0, 3).join('\n')}</pre>
      <span class="bxc-more">show all ${lines.length} lines</span></summary>
    <div class="bxc-cmdwrap"><pre class="bxc-cmd">${cmd}</pre>${copy}</div></details>`;
}

// a call's terminal output: escapes stripped, its tail; all of it one click away
function outputView(b) {
  const s = cached(b, 'out', () => stripAnsi(b.rec.output));
  if (!s.trim()) return nothing;
  const dropped = Number(b.rec.outputTruncated) || 0;
  const lost = dropped ? html`<div class="bxc-muted">… ${fmtN(dropped)} bytes before this were not kept</div>` : nothing;
  if (s.length <= OUTPUT_TAIL) return html`${lost}<pre class="bxc-out">${s}</pre>`;
  return html`${lost}<details class="bxc-outx"><summary class="bxc-muted">… ${s.length - OUTPUT_TAIL} earlier characters — show all</summary><pre class="bxc-out">${s}</pre></details>
    <pre class="bxc-out">${s.slice(-OUTPUT_TAIL)}</pre>`;
}

function rawInputView(rec) {
  if (rec.rawInput == null || rec.kind === 'execute' || rec.kind === 'edit' || isPlanApproval(rec)) return nothing;
  const txt = rawText(rec.rawInput);
  return txt ? html`<details class="bxc-raw"><summary>raw input</summary><pre>${txt}</pre></details>` : nothing;
}

/** argsView(args): a call's arguments a row each — a name, its value (a long or structured one as a block). */
export function argsView(args) {
  const keys = args && typeof args === 'object' ? Object.keys(args) : [];
  if (!keys.length) return nothing;
  return html`<div class="bxc-args">${keys.map((k) => {
    const v = args[k];
    const t = typeof v === 'string' ? v : JSON.stringify(v, null, 2);
    return html`<div class="bxc-arg"><span class="bxc-ak">${k}</span>${t.length > 80 || t.includes('\n') ? html`<pre class="bxc-av">${t}</pre>` : html`<span class="bxc-av">${t}</span>`}</div>`;
  })}</div>`;
}

const CLIP = 1200; // a plain result's characters shown before "show all"
const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');

/** clipView(text, b, ctx, slot): a plain text — its first CLIP characters, all of it one click away (the host remembers: slot). */
export function clipView(text, b, ctx, slot = 'full') {
  const t = String(text ?? '');
  if (t.length <= CLIP) return html`<pre class="bxc-res">${t}</pre>`;
  const full = ctx.isOpen(b, slot, false);
  return html`<div class="bxc-clip"><pre class="bxc-res">${full ? t : t.slice(0, CLIP) + '…'}</pre>
    <button class="bxc-more" @click=${() => ctx.setOpen(b, slot, !full, false)}>${full ? 'show less' : `show all (${fmtN(t.length)} chars)`}</button></div>`;
}

// the call's own name, and the agent's title when the card's line says something else
function metaView(b, title, sub, cmd) {
  const rec = b.rec;
  const t = rec.title && ![title, rec.name, cmd].includes(rec.title) && !(sub && sub.endsWith(rec.title)) ? rec.title : '';
  const name = b.name ?? rec.name;
  return name || t ? html`<div class="bxc-meta">${[name, t].filter(Boolean).join(' · ')}</div>` : nothing;
}

// the files and lines a call touched; a fetch's address (a link only when it is http(s))
function placesView(rec) {
  const ps = placesOf(rec);
  return ps.length ? html`<div class="bxc-places">${ps.map((p) => html`<span>${p}</span>`)}</div>` : nothing;
}
function urlView(rec) {
  const url = rec.rawInput && typeof rec.rawInput.url === 'string' ? rec.rawInput.url : '';
  if (!url) return nothing;
  return html`<div class="bxc-places">${/^https?:\/\//i.test(url) ? html`<a href=${url} target="_blank" rel="noopener noreferrer">${url}</a>` : html`<span>${url}</span>`}</div>`;
}

// nothing came of a call yet: say that it waits, or runs
const pendingView = (b) => (b.state === 'approval' ? html`<div class="bxc-muted">waiting for your approval</div>`
  : live(b.rec) || b.state === 'writing' ? html`<div class="bxc-muted">running…</div>` : nothing);

/**
 * contentView(items, b, ctx, skipText): an ACP content list (a request's
 * tool.content) as a card draws it — a diff item capped like any diff, a
 * text item as markdown when it looks like it; skipText: a text the card
 * already said.
 */
export function contentView(items, b, ctx, skipText = '') {
  return (Array.isArray(items) ? items : []).map((it, i) => {
    if (!it || typeof it !== 'object') return nothing;
    if (it.type === 'diff') return capped(b, ctx, 'diff' + i, cached(b, 'diff' + i, () => ({})), () => cached(b, 'udiff' + i, () => unifiedDiff(it.path, it.oldText, it.newText)));
    if (it.type === 'terminal') return nothing;
    const text = it.content?.text ?? it.text ?? '';
    if (!String(text).trim() || text === skipText) return nothing;
    return textView(text, b, 'txt' + i);
  });
}

/** diffsView(b, diffs, ctx): a call's diffs (ToolDiff[]) as a card draws them — each capped like any diff, one a patch can't be made for by name. */
export function diffsView(b, diffs, ctx) {
  return (diffs || []).map((d, i) => (cached(b, 'udiff' + i, () => diffPatch(d))
    ? [capped(b, ctx, 'diff' + i, cached(b, 'diff' + i, () => ({})), () => cached(b, 'udiff' + i, () => diffPatch(d))),
      d.truncated ? html`<div class="bxc-muted">… the rest of this patch is too large to keep</div>` : nothing]
    // nothing to draw of it: say which file, and why
    : html`<div class="bxc-nopatch"><span class="bxc-mono">${d.path}</span> <span class="bxc-muted">${d.status === 'deleted' ? 'the file was deleted' : 'no patch to show'}</span></div>`));
}

// what a record carries of its content: its text, its diffs, a terminal that said nothing yet
function recordContent(b, ctx, skipText = '') {
  const rec = b.rec;
  const text = String(rec.text || '');
  return [
    text.trim() && text !== skipText ? textView(text, b, 'txt') : nothing,
    ...diffsView(b, rec.diffs, ctx),
    rec.terminal && !rec.output ? html`<div class="bxc-muted">${live(rec) ? 'running…' : '(no output)'}</div>` : nothing,
  ];
}
const hasContent = (rec) => !!(String(rec.text || '').trim() || (rec.diffs && rec.diffs.length) || rec.terminal);

// ---- a change set, a file at a time -----------------------------------------------------

const FSTATUS = { added: 'A', modified: 'M', deleted: 'D', renamed: 'R', typechange: 'T' };

/**
 * filesView(b, set, ctx, label): what a call or a turn changed on disk (a
 * ChangeSet), reviewed a file at a time: folded, its counts; open, its
 * files; a file's row opens that file's diff (ctx.fileDiff), its first
 * DIFF_LINES lines until "Show all" — never the whole patch at once.
 */
export function filesView(b, set, ctx, label = '') {
  const st = filesStat(set);
  const open = ctx.isOpen(b, 'files', false);
  return html`<details class="bxc-files" @toggle=${(e) => toggled(e, b, ctx, 'files', false)}>
    <summary>${label || 'Changed'} ${st.n} file${st.n === 1 ? '' : 's'} <span class="bxc-add">+${st.add}</span> <span class="bxc-del">−${st.del}</span></summary>
    ${open ? html`<ul class="bxc-flist">${(set.files || []).map((c) => fileRow(b, set, c, ctx))}</ul>` : nothing}
  </details>`;
}

function fileRow(b, set, c, ctx) {
  const head = html`<span class="bxc-fs bxc-${c.status}" title=${c.status}>${FSTATUS[c.status] || '?'}</span>
    <span class="bxc-fp" title=${c.path}>${c.oldPath ? `${c.oldPath} → ` : ''}${c.path}</span>${c.binary ? html` <span class="bxc-muted">binary</span>` : html` <span class="bxc-add">+${c.add}</span> <span class="bxc-del">−${c.del}</span>`}
    ${ctx.openDiff ? html`<button class="bxc-more" title="open this file's diff" @click=${(e) => { e.preventDefault(); ctx.openDiff(b, set, c); }}>open</button>` : nothing}`;
  if (c.binary || !ctx.fileDiff) return html`<li><div class="bxc-frow-static">${head}</div></li>`;
  const slot = 'f:' + c.path;
  return html`<li><details class="bxc-frow" @toggle=${(e) => toggled(e, b, ctx, slot, false)}>
    <summary><bx-icon class="bxc-cr" name="caret-right"></bx-icon>${head}</summary>
    ${ctx.isOpen(b, slot, false) ? fileBody(b, ctx.fileDiff(b, set, c), ctx, slot, c.path) : nothing}
  </details></li>`;
}

// one file's diff (its hunks: the row names the file), or why there is none
function fileBody(b, d, ctx, slot, path) {
  if (!d || d.state === 'loading') return html`<div class="bxc-fbody bxc-muted">loading the diff…</div>`;
  if (d.state !== 'ok') return html`<div class="bxc-fbody bxc-muted">${d.note}</div>`;
  const body = (d.body ??= diffBody(d.text));
  if (!body.trim()) return html`<div class="bxc-fbody bxc-muted">no line changes</div>`;
  return html`<div class="bxc-fbody">${capped(b, ctx, slot, d, () => body, langOf ? langOf(path) : '')}
    ${d.cut ? html`<div class="bxc-muted">… the rest of this file's diff is too large to keep</div>` : nothing}</div>`;
}

/** changesView(b, ctx): a changes block — what a whole turn changed, after its last card. */
export function changesView(b, ctx) {
  return html`<div class="bxc-changes">${filesView(b, b.set || { files: [], patch: null }, ctx, 'This turn changed')}</div>`;
}

// ---- a tool call ------------------------------------------------------------------------

/**
 * toolView(b, ctx, depth, child): a tool block's card. Its body is drawn
 * once opened; a failed call opens by itself. child(block, ctx, depth)
 * draws a nested block (a subagent's): the dispatcher of views.js.
 */
export function toolView(b, ctx, depth = 0, child = () => nothing) {
  const rec = b.rec;
  if (b.children) return subagentView(b, ctx, depth, child);
  const exec = rec.kind === 'execute';
  const plan = isPlanApproval(rec);
  const title = plan ? (rec.title || 'Plan') : headline(rec);
  const sub = plan ? '' : b.sub !== undefined ? b.sub : subline(rec, title);
  const dflt = rec.status === 'failed' || b.state === 'error';
  const cmd = exec ? commandOf(rec) : '';
  const open = ctx.isOpen(b, 'body', dflt);
  const changed = () => (rec.changed ? filesView(b, rec.changed, ctx) : nothing);
  // what the call answered in its host's words (a tool of the host's own) is plain text; the record's text is not said twice
  const result = () => (b.result ? clipView(b.result, b, ctx) : nothing);
  const said = () => (hasContent(rec) ? recordContent(b, ctx, b.result || '') : nothing);
  const body = !open ? []
    : exec
      ? [cmd ? commandView(cmd, ctx) : nothing, rec.output ? outputView(b) : hasContent(rec) ? recordContent(b, ctx, rec.label) : result(),
        rec.exitCode != null && !live(rec) ? html`<div class="bxc-exit ${Number(rec.exitCode) === 0 ? 'bxc-ok' : 'bxc-bad'}">exit ${rec.exitCode}</div>` : nothing, changed()]
      : plan
        ? [planText(rec) ? html`<div class="bxc-md bxc-plan-md" .innerHTML=${cached(b, 'plan', () => md(planText(rec)))}></div>` : nothing]
        : [rec.kind === 'fetch' ? urlView(rec) : rec.diffs && rec.diffs.length ? nothing : placesView(rec),
          rec.kind === 'think' && taskOf(rec.rawInput) ? html`<div class="bxc-task">${taskOf(rec.rawInput)}</div>` : nothing,
          b.args ? argsView(b.args) : nothing, said(), rec.output ? outputView(b) : nothing, changed(), b.args ? nothing : rawInputView(rec), result()];
  const drawn = body.flat().some((x) => x !== nothing);
  return html`<details class="bxc-tool ${exec ? 'bxc-exec' : ''}" data-state=${b.state || ''} ?open=${open} @toggle=${(e) => toggled(e, b, ctx, 'body', dflt)}>
    <summary title=${exec ? cmd : rec.title || ''}>${callIcon(b)}
      <span class="bxc-title">${depth === 0 && rec.parent && !b.$up ? html`<span class="bxc-under" title="under a subagent's call further up">↳ </span>` : nothing}${title}${sub ? html`<span class="bxc-sub">${sub}</span>` : nothing}</span>
      ${lineEnd(b)}</summary>
    ${open ? html`<div class="bxc-body">${metaView(b, title, sub, cmd)}${drawn ? body : pendingView(b)}</div>` : nothing}
  </details>`;
}

// a subagent (Claude's Task/Agent call): its description, its prompt
// collapsed, and everything it did nested beneath — open while it works,
// folded to its answer when done
function subagentView(b, ctx, depth, child) {
  const rec = b.rec;
  const running = live(rec);
  const prompt = rec.rawInput && typeof rec.rawInput.prompt === 'string' ? rec.rawInput.prompt : '';
  const kind = rec.rawInput && typeof rec.rawInput.subagent_type === 'string' ? rec.rawInput.subagent_type : '';
  const steps = b.children.filter((c) => c.kind === 'tool').length;
  const dflt = running || rec.status === 'failed';
  const open = ctx.isOpen(b, 'body', dflt);
  return html`<details class="bxc-tool bxc-sub" data-state=${b.state || ''} ?open=${open} @toggle=${(e) => toggled(e, b, ctx, 'body', dflt)}>
    <summary><bx-icon class="bxc-ic" name="agent"></bx-icon>
      <span class="bxc-title" title=${prompt}>${kind ? html`<span class="bxc-muted">${kind}</span> ` : nothing}${headline(rec)}</span>
      ${lineEnd(b, steps ? html`<span class="bxc-oc bxc-steps">${steps} step${steps === 1 ? '' : 's'}</span>` : nothing)}</summary>
    ${open ? html`<div class="bxc-body">
      ${prompt ? html`<details class="bxc-raw"><summary>prompt</summary><div class="bxc-md" .innerHTML=${cached(b, 'prompt', () => md(prompt))}></div></details>` : nothing}
      <div class="bxc-children">${b.children.map((c) => child(c, ctx, depth + 1))}</div>
      ${!running && hasContent(rec) ? html`<div class="bxc-answer">${recordContent(b, ctx)}</div>` : nothing}
    </div>` : nothing}
  </details>`;
}

// ---- a sub-conversation -----------------------------------------------------------------

// a sub-conversation's state in a call's words (lineEnd reads them like a call's)
const AGENT_STATUS = { running: 'in_progress', approval: 'pending', done: 'completed', error: 'failed', stopped: 'cancelled' };

/**
 * agentView(b, ctx, depth, child): an agent block — another agent's run
 * started from this one — as a card that unfolds to that run's own blocks
 * (asked of the host once opened: ctx.loadAgent) and its answer. Its line
 * reads like a call's: what it is called, where it is while it works, how
 * many steps it took. What it waits on a person for (`tail`) is drawn
 * under the card, folded or not — a request is never hidden.
 */
export function agentView(b, ctx, depth = 0, child = () => nothing) {
  const working = b.state === 'running' || b.state === 'approval';
  const dflt = working || b.state === 'error';
  const open = ctx.isOpen(b, 'body', dflt);
  const err = open && ctx.agentError ? ctx.agentError(b) : '';
  if (open && b.blocks == null && !err && ctx.loadAgent) ctx.loadAgent(b);
  const chip = stateChip({ status: AGENT_STATUS[b.state] || 'completed' }, b.state === 'approval' ? 'approval' : '');
  const steps = b.steps ? `${b.steps} step${b.steps === 1 ? '' : 's'}` : '';
  return html`<details class="bxc-tool bxc-sub bxc-agent" data-state=${b.state || ''} ?open=${open} @toggle=${(e) => toggled(e, b, ctx, 'body', dflt)}>
    <summary title=${b.task || ''}><bx-icon class="bxc-ic" name="agent"></bx-icon>
      <span class="bxc-title">${b.provider ? html`<span class="bxc-muted">${b.provider}</span> ` : nothing}${b.title || 'A sub-conversation'}</span>
      ${b.ref ? html`<span class="bxc-ref">#${b.ref}</span>` : nothing}
      ${b.state === 'running' ? html`<span class="bxc-spin"></span>` : nothing}
      ${b.state === 'running' && b.phase ? html`<span class="bxc-st">${b.phase}</span>` : b.state === 'done' ? html`<span class="bxc-st">${steps || 'done'}</span>`
        : chip && b.state !== 'running' ? html`<span class="bxc-st ${toneClass(chip.tone)}">${toneIcon(chip.tone)}${chip.text}</span>` : nothing}
      ${ctx.openAgent && b.ref ? html`<button class="bxc-more bxc-open" title="open this conversation on its own" @click=${(e) => { e.preventDefault(); ctx.openAgent(b); }}>open<bx-icon name="popout"></bx-icon></button>` : nothing}
      ${carets}</summary>
    ${open ? html`<div class="bxc-body">
      ${b.task ? html`<div class="bxc-task bxc-clamp ${ctx.isOpen(b, 'task', false) ? 'bxc-all' : ''}" title="the task it was given" @click=${() => ctx.setOpen(b, 'task', !ctx.isOpen(b, 'task', false), false)}><span class="bxc-k">task</span> ${b.task}</div>` : nothing}
      ${err ? html`<div class="bxc-muted bxc-err"><span>Couldn't read its steps: ${err}</span>${ctx.loadAgent ? html` <button class="bxc-more" data-act="retry" title="read it again" @click=${() => ctx.loadAgent(b, true)}>Retry</button>` : nothing}</div>`
        : b.blocks == null ? html`<div class="bxc-muted">${ctx.loadAgent ? 'loading…' : 'its steps are not held here'}</div>`
          : html`<div class="bxc-children">${b.blocks.map((c) => child(c, ctx, depth + 1))}</div>`}
      ${b.answer && !working ? html`<div class="bxc-answer"><span class="bxc-k">answer</span><div class="bxc-md" .innerHTML=${cached(b, 'answer', () => md(b.answer))}></div></div>` : nothing}
    </div>` : nothing}
  </details>${(b.tail || []).map((c) => child(c, ctx, depth + 1))}`;
}

// ---- a plan's entries -------------------------------------------------------------------

/** planView(b): a plan block — the agent's plan as it stands, an entry a line. */
export function planView(b) {
  return html`<div class="bxc-plan"><div class="bxc-h">plan</div><ul>
    ${(b.entries || []).map((en) => html`<li class="${en.status === 'completed' ? 'bxc-done' : ''}">${en.status === 'completed' ? html`<bx-icon name="check" label="done"></bx-icon>`
      : en.status === 'in_progress' ? html`<bx-icon name="caret-right" label="in progress"></bx-icon>` : html`<span class="bxc-todo" role="img" aria-label="to do"></span>`}<span>${en.content}</span></li>`)}
  </ul></div>`;
}
