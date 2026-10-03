/**
 * <bx-code src="apps/foo"> — a code browser + review panel for the terminal
 * pop-up (VS-Code-ish, buildless). A collapsible file tree + syntax-highlighted
 * viewer (with line numbers), a **Changes** tab showing the working-tree diff
 * and per-commit diffs (with change counts) for review, and an **Analysis** tab
 * charting commit activity over time (upstream too, when tracked). Read-only —
 * editing is the terminal's job. It live-refreshes when the component's files
 * change on disk (agent/terminal edits) via the shared /ws/events socket,
 * live reload paused or not.
 * Backed by the grant-gated /api/xbin/code/* + /git/* endpoints
 * (docs/protocol.md); uses a raw fetch so the caller is the signed-in user
 * (admin or a code:<tile> grant), matching the admin console's code viewer.
 */
import { LitElement, html, css, nothing } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import { unsafeHTML } from 'lit';
import hljs from '/vendor/highlight.min.js';
import { onEvent } from '/vendor/events-socket.js';
import '/vendor/bx-icons.js';

// codeCss: how code and diffs look (D184): highlight.js's classes on the
// --bx-syn-* tokens, and diffHTML's lines on the --bx-diff-* tokens — a
// changed line keeps its syntax colours on its tint, its +/- marker in the
// diff colour; file headers in the text colour, 600; unchanged lines in the
// context colour. Shared by every element that shows code or a diff
// (bx-prs, bx-deploy, the Agent tab's cards), so they read the same. Code is
// shown as typed: the code face's ligatures are off (=> stays two characters).
export const codeCss = css`
  pre, code, .hljs, .diff, .mono { font-variant-ligatures: none; font-feature-settings: "liga" 0, "calt" 0; }
  .hljs-comment, .hljs-quote { color: var(--bx-syn-comment, #9598A9); font-style: italic; }
  .hljs-keyword, .hljs-selector-tag, .hljs-section, .hljs-name, .hljs-doctag, .hljs-variable.language_ { color: var(--bx-syn-keyword, #FF8CC8); }
  .hljs-string, .hljs-regexp, .hljs-char.escape_, .hljs-meta .hljs-string { color: var(--bx-syn-string, #7BE0B0); }
  .hljs-number, .hljs-literal, .hljs-symbol, .hljs-bullet { color: var(--bx-syn-number, #FFD54A); }
  .hljs-title, .hljs-title.function_ { color: var(--bx-syn-function, #96A6FF); }
  .hljs-type, .hljs-title.class_, .hljs-class .hljs-title { color: var(--bx-syn-type, #5CCBE3); }
  .hljs-attr, .hljs-attribute, .hljs-property, .hljs-selector-attr, .hljs-selector-class, .hljs-selector-id, .hljs-selector-pseudo { color: var(--bx-syn-attr, #F5B07A); }
  .hljs-built_in, .hljs-meta, .hljs-template-variable, .hljs-variable { color: var(--bx-syn-builtin, #C6A8FF); }
  .hljs-deletion { color: var(--bx-syn-deletion, #FF8F8F); background: var(--bx-diff-del-bg, #342429); }
  .hljs-addition { color: var(--bx-syn-addition, #7BE0B0); background: var(--bx-diff-add-bg, #1D302D); }
  .hljs-params, .hljs-punctuation, .hljs-operator { color: inherit; }
  .hljs-subst { color: var(--bx-text, #E9EAF0); }
  .hljs-emphasis { font-style: italic; }
  .hljs-strong { font-weight: 600; }
  .hljs-link { color: var(--bx-link, #8C9BFF); }
  .diff .fh { display: block; color: var(--bx-text, #E9EAF0); font-weight: 600; }
  .diff .h { display: block; color: var(--bx-diff-hunk, #5CCBE3); background: var(--bx-diff-hunk-bg, #1C2830); }
  .diff .d { display: block; color: var(--bx-text, #E9EAF0); background: var(--bx-diff-add-bg, #1D302D); }
  .diff .a { display: block; color: var(--bx-text, #E9EAF0); background: var(--bx-diff-del-bg, #342429); }
  .diff .d > .mk { color: var(--bx-diff-add, #5EDBA5); }
  .diff .a > .mk { color: var(--bx-diff-del, #FF8F8F); }
  .diff .ctx { display: block; color: var(--bx-diff-context, #A3A6B6); background: var(--bx-diff-context-bg, transparent); }
  .pl, .mi { font-variant-numeric: tabular-nums; }
  .pl { color: var(--bx-diff-add, #5EDBA5); }
  .mi { color: var(--bx-diff-del, #FF8F8F); }
`;

const LANG_BY_EXT = {
  js: 'javascript', mjs: 'javascript', cjs: 'javascript', ts: 'typescript',
  jsx: 'javascript', json: 'json', jsonc: 'json', go: 'go', mod: 'go',
  py: 'python', rb: 'ruby', rs: 'rust', sh: 'bash', bash: 'bash', zsh: 'bash',
  css: 'css', scss: 'scss', less: 'less', html: 'xml', xml: 'xml', svg: 'xml',
  md: 'markdown', markdown: 'markdown', yml: 'yaml', yaml: 'yaml',
  toml: 'ini', ini: 'ini', sql: 'sql', java: 'java', c: 'c', h: 'c',
  cpp: 'cpp', cs: 'csharp', php: 'php', lua: 'lua', swift: 'swift', kt: 'kotlin',
};
export function langFor(path) {
  const base = path.split('/').pop();
  if (base === 'go.mod' || base === 'go.sum') return 'go';
  if (base === 'Makefile' || base === 'makefile') return 'makefile';
  if (base === 'Dockerfile') return 'dockerfile';
  const ext = base.includes('.') ? base.split('.').pop().toLowerCase() : '';
  return LANG_BY_EXT[ext] || '';
}
const escHTML = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
export function hl(code, lang) {
  try {
    if (lang && hljs.getLanguage(lang)) return hljs.highlight(code, { language: lang, ignoreIllegals: true }).value;
  } catch { /* fall through */ }
  return escHTML(code);
}
// Render a unified diff to highlighted HTML (per-file language, +/- coloring).
// Exported: bx-prs renders proposal series through the same pipeline.
export function diffHTML(diff) {
  if (!diff) return '<span class="muted">no changes</span>';
  const hdr = /^(--- |\+\+\+ )(a\/|b\/|\/dev\/null)/;
  let lang = '';
  const out = [];
  for (const raw of diff.split('\n')) {
    if (raw.startsWith('diff --git')) {
      const m = raw.match(/ b\/(\S+)/); if (m) lang = langFor(m[1]);
      out.push(`<span class="fh">${escHTML(raw)}</span>`); continue;
    }
    if (raw.startsWith('+++ ')) { const m = raw.match(/\+\+\+ b\/(.+)/); if (m) lang = langFor(m[1]); }
    if (hdr.test(raw) || raw.startsWith('index ') || raw.startsWith('new file')
      || raw.startsWith('deleted file') || raw.startsWith('similarity ') || raw.startsWith('rename ')) {
      out.push(`<span class="fh">${escHTML(raw)}</span>`); continue;
    }
    if (raw.startsWith('@@')) { out.push(`<span class="h">${escHTML(raw)}</span>`); continue; }
    const sign = raw[0];
    if (sign === '+') out.push(`<span class="d"><span class="mk">+</span>${hl(raw.slice(1), lang)}</span>`);
    else if (sign === '-') out.push(`<span class="a"><span class="mk">-</span>${hl(raw.slice(1), lang)}</span>`);
    else if (sign === ' ') out.push(`<span class="ctx"> ${hl(raw.slice(1), lang)}</span>`);
    else out.push(`<span class="fh">${escHTML(raw)}</span>`);
  }
  return out.join('');
}
// Totals for a change-count summary, parsed from a unified diff. Counts +/-
// content lines (not the +++/--- headers) and distinct files (diff --git).
export function diffStats(diff) {
  let add = 0, del = 0; const files = new Set();
  for (const line of (diff || '').split('\n')) {
    if (line.startsWith('diff --git')) { const m = line.match(/ b\/(\S+)/); if (m) files.add(m[1]); continue; }
    if (line.startsWith('+++') || line.startsWith('---')) continue;
    if (line[0] === '+') add++;
    else if (line[0] === '-') del++;
  }
  return { add, del, files: files.size };
}
// Build a nested {dirs,files} tree from flat "a/b/c.js" paths.
function buildTree(files) {
  const root = { dirs: {}, files: [] };
  for (const f of files) {
    const parts = f.path.split('/');
    let node = root;
    for (let i = 0; i < parts.length - 1; i++) node = (node.dirs[parts[i]] ??= { dirs: {}, files: [] });
    node.files.push({ name: parts[parts.length - 1], path: f.path });
  }
  return root;
}

const fmtDate = (ts) => (ts ? new Date(ts * 1000).toISOString().slice(0, 10) : '—');
function relTime(ts) {
  if (!ts) return '—';
  const s = Math.floor(Date.now() / 1000) - ts;
  if (s < 3600) return `${Math.max(1, Math.floor(s / 60))}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  if (s < 2592000) return `${Math.floor(s / 86400)}d ago`;
  if (s < 31536000) return `${Math.floor(s / 2592000)}mo ago`;
  return `${Math.floor(s / 31536000)}y ago`;
}
const card = (label, val) => html`<div class="cd"><div class="v">${val}</div><div class="l">${label}</div></div>`;

export class BxCode extends LitElement {
  static properties = {
    src: { type: String },
    _tab: { state: true },     // 'files' | 'changes' | 'analysis'
    _tree: { state: true },    // [{path,size}]
    _sel: { state: true },     // selected file path
    _file: { state: true },    // {content|binary|truncated}
    _log: { state: true },     // {commits:[{hash,short,subject,date,author,add,del,files}], remote}
    _rev: { state: true },     // selected commit hash ('' = working tree)
    _diff: { state: true },    // {diff, repo}
    _activity: { state: true },// {repo, remote, upstreamRef, local:[{t,a}], upstream:[{t,a}]|null}
    _collapsed: { state: true },
    _err: { state: true },
    _q: { state: true },       // file filter
  };

  static styles = [scrollCss, codeCss, css`
    /* UI text in the UI face; code, paths, ids and times in the code face (D184) */
    :host { display: flex; height: 100%; min-height: 0; font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif);
      color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028); }
    button, input { font: inherit; }
    :focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: calc(-1 * var(--bx-focus-width, 3px)); }
    .side { width: 230px; flex: none; display: flex; flex-direction: column; border-right: 1px solid var(--bx-border, #33353F); min-height: 0; }
    .tabs { display: flex; flex: none; border-bottom: 1px solid var(--bx-border, #33353F); }
    .tabs button { flex: 1; background: none; border: 0; color: var(--bx-muted, #A3A6B6); height: var(--bx-row, 28px); padding: 0 4px;
      cursor: pointer; border-bottom: 2px solid transparent; }
    .tabs button:hover { color: var(--bx-text, #E9EAF0); }
    .tabs button.on { color: var(--bx-text, #E9EAF0); font-weight: 600; border-bottom-color: var(--bx-accent, #8C9BFF); }
    .filter { flex: none; box-sizing: border-box; height: var(--bx-control-h, 28px); margin: 8px; padding: 0 8px;
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
      background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); }
    .filter::placeholder { color: var(--bx-subtle, #8E91A2); opacity: 1; }
    .filter:focus-visible { outline-offset: var(--bx-focus-offset, 2px); }
    .list { flex: 1; overflow: auto; padding: 0 0 8px; min-height: 0; }
    /* the file tree: 28px rows, the selected one in the selection colour with the accent rule */
    .row { display: flex; align-items: center; gap: 4px; min-height: var(--bx-row, 28px); padding: 0 8px 0 0; cursor: pointer; white-space: nowrap;
      font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); }
    .row:hover { background: var(--bx-hover, #2A2B34); }
    .row.on { background: var(--bx-selection, #262C5C); color: var(--bx-selection-text, #E9EAF0); box-shadow: inset 2px 0 0 var(--bx-accent, #8C9BFF); }
    .row .tw { display: inline-flex; width: 16px; flex: none; justify-content: center; color: var(--bx-muted, #A3A6B6); }
    .row .ic { display: inline-flex; flex: none; color: var(--bx-muted, #A3A6B6); }
    .commit { padding: 6px 12px; cursor: pointer; border-bottom: 1px solid var(--bx-border, #33353F); white-space: normal; }
    .commit:hover { background: var(--bx-hover, #2A2B34); }
    .commit.on { background: var(--bx-selection, #262C5C); color: var(--bx-selection-text, #E9EAF0); box-shadow: inset 2px 0 0 var(--bx-accent, #8C9BFF); }
    .commit .subj { color: inherit; }
    .commit .meta { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif);
      font-variant-numeric: tabular-nums; display: flex; align-items: baseline; gap: 6px; }
    .commit .meta .id { font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); }
    .commit .meta .cnt { margin-left: auto; white-space: nowrap; }
    .commit .wt { display: inline-block; width: 8px; height: 8px; margin-right: 6px; background: var(--bx-muted, #A3A6B6); vertical-align: 1px; }
    /* the code well: a pane one step below the panel */
    .main { flex: 1; overflow: auto; min-height: 0; padding: 0; background: var(--bx-code-bg, #16171D); }
    .path { position: sticky; top: 0; background: var(--bx-panel, #1F2028); border-bottom: 1px solid var(--bx-border, #33353F);
      padding: 5px 12px; color: var(--bx-text, #E9EAF0); z-index: 2; display: flex; align-items: baseline; gap: 10px;
      font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); }
    .path .stat { margin-left: auto; white-space: nowrap; color: var(--bx-muted, #A3A6B6); font-variant-numeric: tabular-nums; }
    pre, code { font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); }
    pre { margin: 0; padding: 8px 12px; white-space: pre; tab-size: 4; }
    pre.wrap { white-space: pre-wrap; word-break: break-word; }
    .muted { color: var(--bx-muted, #A3A6B6); padding: 12px; display: block; }
    .err { color: var(--bx-danger, #FF7A7A); padding: 12px; display: block; }
    .err bx-icon { margin-right: 6px; }
    /* file view with a line-number gutter */
    .fileview { display: flex; align-items: stretch; min-width: max-content; }
    .gutter { position: sticky; left: 0; padding: 8px 8px 8px 12px; text-align: right; z-index: 1;
      color: var(--bx-subtle, #8E91A2); font-variant-numeric: tabular-nums;
      background: var(--bx-code-bg, #16171D); border-right: 1px solid var(--bx-border, #33353F);
      user-select: none; -webkit-user-select: none; }
    .code { flex: 1; }
    /* analysis: series in the ANSI order (blue, then magenta: D184 §1.1) */
    .ahead { padding: 12px 12px 4px; color: var(--bx-muted, #A3A6B6);
      font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase; }
    .arow { position: relative; display: flex; align-items: center; gap: 6px; min-height: var(--bx-row, 28px); padding: 0 12px; }
    .arow .an { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .arow .ac { color: var(--bx-muted, #A3A6B6); font-variant-numeric: tabular-nums; }
    .arow .abar { position: absolute; left: 0; bottom: 0; height: 2px; background: var(--bx-term-blue, #6F86FF); }
    .cards { display: flex; flex-wrap: wrap; gap: 8px; padding: 12px; }
    .cd { flex: 1 1 120px; min-width: 108px; background: var(--bx-panel, #1F2028);
      border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); padding: 8px 12px; }
    .cd .v { font: var(--bx-font-title, 600 16px/22px "Instrument Sans", system-ui, sans-serif); font-variant-numeric: tabular-nums; color: var(--bx-text, #E9EAF0); }
    .cd .l { font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em);
      color: var(--bx-muted, #A3A6B6); margin-top: 2px; text-transform: uppercase; }
    .chart-title { padding: 8px 12px 0; color: var(--bx-muted, #A3A6B6); }
    .chart { display: flex; align-items: flex-end; gap: 1px; height: 120px; padding: 12px 12px 0; }
    .chart .col { position: relative; flex: 1; height: 100%; }
    .chart .b { position: absolute; bottom: 0; width: 100%; min-height: 0; }
    .chart .b.loc { background: var(--bx-term-blue, #6F86FF); }
    .chart .b.up { background: var(--bx-term-magenta, #FF5FB0); }
    .axis { display: flex; justify-content: space-between; padding: 4px 12px 0; color: var(--bx-muted, #A3A6B6);
      font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); font-variant-numeric: tabular-nums; }
    .legend { display: flex; gap: 16px; padding: 8px 12px; color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
    .legend .k { display: inline-flex; align-items: center; gap: 6px; }
    .legend .s { width: 8px; height: 8px; display: inline-block; }
    .legend .s.loc { background: var(--bx-term-blue, #6F86FF); } .legend .s.up { background: var(--bx-term-magenta, #FF5FB0); }
  `];

  constructor() {
    super();
    this._tab = 'files';
    this._tree = null; this._log = null; this._collapsed = new Set();
    this._rev = ''; this._q = ''; this._activity = null;
  }

  connectedCallback() {
    super.connectedCallback();
    // Live refresh: a component's files changing on disk publishes a `reload`
    // event (agent/terminal edits, commits, builds). Coalesce a burst.
    this._offEvents = onEvent((e) => this._ext(e));
    this._load();
  }
  disconnectedCallback() {
    this._offEvents?.(); clearTimeout(this._refreshT);
    super.disconnectedCallback();
  }
  updated(ch) {
    if (ch.has('src') && this.src) {
      this._sel = null; this._file = null; this._diff = null; this._activity = null;
      this._load();
    }
  }

  // Raw fetch (cookie principal): admin, or a code:<tile> grant. xbin.fetch is
  // avoided on purpose — its frame token would downgrade to a non-admin element
  // principal and 403 (same reason the admin console uses raw fetch here).
  async _api(path) {
    try {
      const r = await fetch(`/api/xbin/${path}`);
      if (r.status === 403) { this._err = 'no code access for this tile (needs admin or a code: grant)'; return null; }
      if (!r.ok) { this._err = `error ${r.status}`; return null; }
      this._err = '';
      return await r.json();
    } catch (e) { this._err = String(e.message ?? e); return null; }
  }

  async _load() {
    if (!this.src) return;
    const c = encodeURIComponent(this.src);
    const t = await this._api(`code/tree?component=${c}`);
    if (t) this._tree = t.files || [];
    const l = await this._api(`git/log?component=${c}&limit=50`);
    if (l) this._log = l;
    if (this._tab === 'changes' && !this._diff) this._loadDiff('');
    if (this._tab === 'analysis' && !this._activity) this._loadActivity();
  }

  async _openFile(path) {
    this._sel = path; this._file = null;
    const f = await this._api(`code/file?component=${encodeURIComponent(this.src)}&file=${encodeURIComponent(path)}`);
    if (f && this._sel === path) this._file = f;
  }

  async _loadDiff(rev) {
    this._rev = rev; this._diff = null;
    const q = rev ? `&rev=${encodeURIComponent(rev)}` : '';
    const d = await this._api(`git/diff?component=${encodeURIComponent(this.src)}${q}`);
    if (d && this._rev === rev) this._diff = d;
  }

  async _loadActivity() {
    const a = await this._api(`git/activity?component=${encodeURIComponent(this.src)}`);
    if (a) this._activity = a;
  }

  // --- live refresh on file changes -------------------------------------
  // A save reloads the tile (reload, build-ok) — or, while its live reload
  // is paused or attached to another tile deployment, only the tile's
  // `deployments` event says so: op work-tree (the changed count moved) or
  // that deployment's op reload and op build ok. The work tree changed all
  // the same.
  _ext(e) {
    const d = e.type === 'deployments' ? e.data || {} : null;
    const saved = e.type === 'reload' || e.type === 'build-ok' || d?.op === 'work-tree' || d?.op === 'reload' || (d?.op === 'build' && d.phase === 'ok');
    if (!saved || !e.component) return;
    if (!(e.component === this.src || e.component.startsWith(this.src + '/'))) return;
    clearTimeout(this._refreshT);
    this._refreshT = setTimeout(() => this._refresh(), 250);
  }
  async _refresh() {
    if (!this.src) return;
    const c = encodeURIComponent(this.src);
    const t = await this._api(`code/tree?component=${c}`);
    if (t) this._tree = t.files || [];
    const l = await this._api(`git/log?component=${c}&limit=50`);
    if (l) this._log = l;
    if (this._sel) await this._refreshFile();
    if (this._tab === 'changes') await this._refreshDiff();
    if (this._tab === 'analysis') await this._loadActivity();
  }
  // Refetch the open file without the loading flicker, preserving scroll, and
  // only re-render if the content actually changed.
  async _refreshFile() {
    const path = this._sel;
    const f = await this._api(`code/file?component=${encodeURIComponent(this.src)}&file=${encodeURIComponent(path)}`);
    if (!f || this._sel !== path) return;
    if (this._file && this._file.content === f.content
      && this._file.binary === f.binary && this._file.truncated === f.truncated) return;
    const main = this.renderRoot.querySelector('.main');
    const st = main?.scrollTop, sl = main?.scrollLeft;
    this._file = f;
    await this.updateComplete;
    if (main) { main.scrollTop = st ?? 0; main.scrollLeft = sl ?? 0; }
  }
  async _refreshDiff() {
    const rev = this._rev;
    const q = rev ? `&rev=${encodeURIComponent(rev)}` : '';
    const d = await this._api(`git/diff?component=${encodeURIComponent(this.src)}${q}`);
    if (d && this._rev === rev) this._diff = d; // no null flicker; swap in place
  }

  _setTab(t) {
    this._tab = t;
    if (t === 'changes' && !this._diff) this._loadDiff(this._rev || '');
    if (t === 'analysis' && !this._activity) this._loadActivity();
  }

  _toggleDir(path) {
    const c = new Set(this._collapsed);
    c.has(path) ? c.delete(path) : c.add(path);
    this._collapsed = c;
  }

  _renderDir(node, prefix, depth) {
    const dirs = Object.keys(node.dirs).sort();
    const files = node.files.slice().sort((a, b) => a.name.localeCompare(b.name));
    const q = this._q.toLowerCase();
    return html`
      ${dirs.map((name) => {
        const dpath = prefix ? `${prefix}/${name}` : name;
        const open = !this._collapsed.has(dpath);
        // When filtering, always expand so matches show.
        const show = open || q;
        return html`
          <div class="row" style="padding-left:${depth * 12 + 4}px" @click=${() => this._toggleDir(dpath)}>
            <span class="tw"><bx-icon name=${show ? 'caret-down' : 'caret-right'}></bx-icon></span><span class="ic"><bx-icon name="folder"></bx-icon></span>${name}
          </div>
          ${show ? this._renderDir(node.dirs[name], dpath, depth + 1) : nothing}`;
      })}
      ${files.filter((f) => !q || f.path.toLowerCase().includes(q)).map((f) => html`
        <div class="row ${this._sel === f.path ? 'on' : ''}" style="padding-left:${depth * 12 + 20}px"
             @click=${() => this._openFile(f.path)} title=${f.path}>
          <span class="ic"><bx-icon name="file"></bx-icon></span>${f.name}
        </div>`)}`;
  }

  _filesPane() {
    if (!this._tree) return html`<span class="muted">loading…</span>`;
    const tree = buildTree(this._tree);
    return html`
      <input class="filter" placeholder="filter files…" .value=${this._q}
             @input=${(e) => { this._q = e.target.value; }}>
      <div class="list">${this._renderDir(tree, '', 0)}</div>`;
  }

  _changesPane() {
    const commits = this._log?.commits ?? [];
    const wt = (this._rev === '' && this._diff) ? diffStats(this._diff.diff) : null;
    return html`
      <div class="list">
        <div class="commit ${this._rev === '' ? 'on' : ''}" @click=${() => this._loadDiff('')}>
          <div class="subj"><span class="wt" aria-hidden="true"></span>Working tree</div>
          <div class="meta">uncommitted changes${wt && (wt.add || wt.del) ? html`
            <span class="cnt"><span class="pl">+${wt.add}</span> <span class="mi">−${wt.del}</span></span>` : nothing}</div>
        </div>
        ${commits.map((c) => html`
          <div class="commit ${this._rev === (c.hash || c.rev) ? 'on' : ''}"
               @click=${() => this._loadDiff(c.hash || c.rev)}>
            <div class="subj">${c.subject ?? c.message ?? (c.hash || '').slice(0, 8)}</div>
            <div class="meta">
              <span class="id">${(c.short || c.hash || c.rev || '').slice(0, 8)}</span>
              <span class="cnt"><span class="pl">+${c.add ?? 0}</span> <span class="mi">−${c.del ?? 0}</span></span>
            </div>
            <div class="meta">${c.author ?? ''} · ${c.date ? relTime(Date.parse(c.date) / 1000) : ''}</div>
          </div>`)}
        ${commits.length === 0 ? html`<span class="muted">no commits yet</span>` : nothing}
      </div>`;
  }

  // Derive chart buckets + summary from the activity timeline.
  _analysis() {
    const a = this._activity;
    if (!a || a.repo === false) return null;
    const local = a.local || [];
    const up = a.upstream || null;
    const now = Math.floor(Date.now() / 1000);
    const WEEK = 7 * 86400, WEEKS = 52;
    // bucket[0] = oldest (WEEKS-1 ago), bucket[WEEKS-1] = current week.
    const bucket = (arr) => {
      const b = new Array(WEEKS).fill(0);
      for (const c of arr) { const wi = Math.floor((now - c.t) / WEEK); if (wi >= 0 && wi < WEEKS) b[WEEKS - 1 - wi]++; }
      return b;
    };
    const authors = {};
    for (const c of local) authors[c.a || '—'] = (authors[c.a || '—'] || 0) + 1;
    const topAuthors = Object.entries(authors).sort((x, y) => y[1] - x[1]).slice(0, 6);
    const first = local.reduce((m, c) => (c.t < m ? c.t : m), Infinity);
    const last = local.reduce((m, c) => (c.t > m ? c.t : m), 0);
    return {
      total: local.length, last30: local.filter((c) => c.t >= now - 30 * 86400).length,
      first: first === Infinity ? 0 : first, last, topAuthors,
      weeksLocal: bucket(local), weeksUp: up ? bucket(up) : null, WEEK, now,
      remote: a.remote || '', upstreamRef: a.upstreamRef || '',
      upstreamTotal: up ? up.length : 0,
      upstreamLast: up ? up.reduce((m, c) => (c.t > m ? c.t : m), 0) : 0,
    };
  }

  _chart(a) {
    const max = Math.max(1, ...a.weeksLocal, ...(a.weeksUp || [0]));
    const n = a.weeksLocal.length;
    return html`
      <div class="chart-title">commits / week · last 52 weeks</div>
      <div class="chart">
        ${a.weeksLocal.map((v, i) => {
          const up = a.weeksUp ? a.weeksUp[i] : 0;
          const wk = a.now - (n - 1 - i) * a.WEEK;
          return html`<div class="col" title="week of ${fmtDate(wk)} — ${v} local${a.weeksUp ? `, ${up} upstream` : ''}">
            ${a.weeksUp ? html`<span class="b up" style="height:${(up / max) * 100}%"></span>` : nothing}
            <span class="b loc" style="height:${(v / max) * 100}%"></span>
          </div>`;
        })}
      </div>
      <div class="axis"><span>${fmtDate(a.now - 51 * a.WEEK)}</span><span>${fmtDate(a.now - 26 * a.WEEK)}</span><span>now</span></div>
      <div class="legend">
        <span class="k"><i class="s loc"></i>local</span>
        ${a.weeksUp ? html`<span class="k"><i class="s up"></i>upstream · ${a.upstreamRef}</span>` : nothing}
      </div>`;
  }

  _analysisSidebar() {
    if (this._err) return html`<span class="err"><bx-icon name="error"></bx-icon>${this._err}</span>`;
    if (!this._activity) return html`<span class="muted">loading…</span>`;
    const a = this._analysis();
    if (!a) return html`<span class="muted">not a git repo</span>`;
    const max = a.topAuthors.length ? a.topAuthors[0][1] : 1;
    return html`
      <div class="list">
        <div class="ahead">top authors</div>
        ${a.topAuthors.map(([name, n]) => html`
          <div class="arow" title="${name}: ${n} commits">
            <span class="an">${name}</span><span class="ac">${n}</span>
            <span class="abar" style="width:${(n / max) * 100}%"></span>
          </div>`)}
        ${a.topAuthors.length === 0 ? html`<span class="muted">no commits</span>` : nothing}
      </div>`;
  }

  _analysisMain() {
    if (this._err) return html`<div class="err"><bx-icon name="error"></bx-icon>${this._err}</div>`;
    if (!this._activity) return html`<span class="muted">loading…</span>`;
    const a = this._analysis();
    if (!a) return html`<span class="muted">not a git repo</span>`;
    return html`
      <div class="path"><span>activity${a.remote ? ` · ${a.remote}` : ''}</span></div>
      <div class="cards">
        ${card('commits', a.total)}
        ${card('last 30 days', a.last30)}
        ${card('authors', a.topAuthors.length)}
        ${card('first commit', fmtDate(a.first))}
        ${card('latest', relTime(a.last))}
        ${a.weeksUp != null ? card('upstream', html`${a.upstreamTotal} · ${relTime(a.upstreamLast)}`) : nothing}
      </div>
      ${this._chart(a)}`;
  }

  _mainPane() {
    if (this._tab === 'analysis') return this._analysisMain();
    if (this._err) return html`<div class="err"><bx-icon name="error"></bx-icon>${this._err}</div>`;
    if (this._tab === 'changes') {
      if (!this._diff) return html`<span class="muted">loading…</span>`;
      if (this._diff.repo === false) return html`<span class="muted">not a git repo</span>`;
      const s = diffStats(this._diff.diff);
      return html`
        <div class="path">
          <span>${this._rev ? `commit ${this._rev.slice(0, 8)}` : 'working tree — uncommitted changes'}</span>
          ${this._diff.diff ? html`<span class="stat">
            <span class="pl">+${s.add}</span> <span class="mi">−${s.del}</span> · ${s.files} file${s.files === 1 ? '' : 's'}</span>` : nothing}
        </div>
        <pre class="diff hljs">${unsafeHTML(diffHTML(this._diff.diff))}</pre>`;
    }
    if (!this._sel) return html`<span class="muted">select a file</span>`;
    if (!this._file) return html`<span class="muted">loading…</span>`;
    if (this._file.binary) return html`<span class="muted">binary file (${this._file.size} bytes)</span>`;
    if (this._file.truncated) return html`<span class="muted">file too large to display (${this._file.size} bytes)</span>`;
    const content = this._file.content ?? '';
    let n = content.split('\n').length;
    if (content.endsWith('\n')) n--;   // a trailing newline isn't its own line
    if (n < 1) n = 1;
    const nums = Array.from({ length: n }, (_, i) => i + 1).join('\n');
    return html`
      <div class="path"><span>${this._sel}</span></div>
      <div class="fileview">
        <pre class="gutter" aria-hidden="true">${nums}</pre>
        <pre class="hljs code"><code>${unsafeHTML(hl(content, langFor(this._sel)))}</code></pre>
      </div>`;
  }

  render() {
    return html`
      <div class="side">
        <div class="tabs">
          <button class=${this._tab === 'files' ? 'on' : ''} @click=${() => this._setTab('files')}>Files</button>
          <button class=${this._tab === 'changes' ? 'on' : ''} @click=${() => this._setTab('changes')}>Changes</button>
          <button class=${this._tab === 'analysis' ? 'on' : ''} @click=${() => this._setTab('analysis')}>Analysis</button>
        </div>
        ${this._tab === 'files' ? this._filesPane()
          : this._tab === 'changes' ? this._changesPane()
          : this._analysisSidebar()}
      </div>
      <div class="main">${this._mainPane()}</div>`;
  }
}
customElements.define('bx-code', BxCode);
