/**
 * <bx-code src="apps/foo"> — a code browser + review panel for the terminal
 * pop-up (VS-Code-ish, buildless). A collapsible file tree + syntax-highlighted
 * viewer, and a **Changes** tab showing the working-tree diff and per-commit
 * diffs for review. Read-only — editing is the terminal's job. Backed by the
 * grant-gated /api/xbin/code/* + /git/* endpoints (docs/protocol.md); uses a
 * raw fetch so the caller is the signed-in user (admin or a code:<tile> grant),
 * matching the admin console's code viewer.
 */
import { LitElement, html, css, nothing } from 'lit';
import { unsafeHTML } from 'lit';
import hljs from '/vendor/highlight.min.js';

const LANG_BY_EXT = {
  js: 'javascript', mjs: 'javascript', cjs: 'javascript', ts: 'typescript',
  jsx: 'javascript', json: 'json', jsonc: 'json', go: 'go', mod: 'go',
  py: 'python', rb: 'ruby', rs: 'rust', sh: 'bash', bash: 'bash', zsh: 'bash',
  css: 'css', scss: 'scss', less: 'less', html: 'xml', xml: 'xml', svg: 'xml',
  md: 'markdown', markdown: 'markdown', yml: 'yaml', yaml: 'yaml',
  toml: 'ini', ini: 'ini', sql: 'sql', java: 'java', c: 'c', h: 'c',
  cpp: 'cpp', cs: 'csharp', php: 'php', lua: 'lua', swift: 'swift', kt: 'kotlin',
};
function langFor(path) {
  const base = path.split('/').pop();
  if (base === 'go.mod' || base === 'go.sum') return 'go';
  if (base === 'Makefile' || base === 'makefile') return 'makefile';
  if (base === 'Dockerfile') return 'dockerfile';
  const ext = base.includes('.') ? base.split('.').pop().toLowerCase() : '';
  return LANG_BY_EXT[ext] || '';
}
const escHTML = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
function hl(code, lang) {
  try {
    if (lang && hljs.getLanguage(lang)) return hljs.highlight(code, { language: lang, ignoreIllegals: true }).value;
  } catch { /* fall through */ }
  return escHTML(code);
}
// Render a unified diff to highlighted HTML (per-file language, +/- coloring).
function diffHTML(diff) {
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
    if (sign === '+') out.push(`<span class="d">+${hl(raw.slice(1), lang)}</span>`);
    else if (sign === '-') out.push(`<span class="a">-${hl(raw.slice(1), lang)}</span>`);
    else if (sign === ' ') out.push(`<span class="ctx"> ${hl(raw.slice(1), lang)}</span>`);
    else out.push(`<span class="fh">${escHTML(raw)}</span>`);
  }
  return out.join('');
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

export class BxCode extends LitElement {
  static properties = {
    src: { type: String },
    _tab: { state: true },   // 'files' | 'changes'
    _tree: { state: true },  // [{path,size}]
    _sel: { state: true },   // selected file path
    _file: { state: true },  // {content|binary|truncated}
    _log: { state: true },   // {commits:[{hash,subject,date,author}], repo}
    _rev: { state: true },   // selected commit hash ('' = working tree)
    _diff: { state: true },  // {diff, repo}
    _collapsed: { state: true },
    _err: { state: true },
    _q: { state: true },     // file filter
  };

  static styles = css`
    :host { display: flex; height: 100%; min-height: 0; font: 12px/1.5 var(--bx-mono, ui-monospace, monospace);
      color: var(--bx-text, #d7dce5); background: var(--bx-panel, #23272e); }
    .side { width: 210px; flex: none; display: flex; flex-direction: column; border-right: 1px solid var(--bx-border, #39414d); min-height: 0; }
    .tabs { display: flex; flex: none; border-bottom: 1px solid var(--bx-border, #39414d); }
    .tabs button { flex: 1; background: none; border: 0; color: var(--bx-muted, #8794a1); padding: 6px 4px;
      font: inherit; cursor: pointer; border-bottom: 2px solid transparent; }
    .tabs button.on { color: var(--bx-text, #d7dce5); border-bottom-color: var(--bx-accent, #f2a71b); }
    .filter { flex: none; margin: 5px; padding: 3px 6px; border: 1px solid var(--bx-border, #39414d); border-radius: 4px;
      background: var(--bx-panel-2, #1c2026); color: inherit; font: inherit; }
    .list { flex: 1; overflow: auto; padding: 2px 0 8px; min-height: 0; }
    .row { display: flex; align-items: center; gap: 3px; padding: 1px 8px 1px 0; cursor: pointer; white-space: nowrap; }
    .row:hover { background: var(--bx-panel-2, #1c2026); }
    .row.on { background: color-mix(in srgb, var(--bx-accent, #f2a71b) 22%, transparent); color: #fff; }
    .row .tw { display: inline-block; width: 12px; text-align: center; color: var(--bx-muted, #8794a1); }
    .row .ic { opacity: .6; }
    .commit { padding: 4px 8px; cursor: pointer; border-bottom: 1px solid color-mix(in srgb, var(--bx-border,#39414d) 40%, transparent); white-space: normal; }
    .commit:hover { background: var(--bx-panel-2, #1c2026); }
    .commit.on { background: color-mix(in srgb, var(--bx-accent, #f2a71b) 22%, transparent); }
    .commit .subj { color: var(--bx-text, #d7dce5); }
    .commit .meta { color: var(--bx-muted, #8794a1); font-size: 10.5px; }
    .main { flex: 1; overflow: auto; min-height: 0; padding: 0; }
    .path { position: sticky; top: 0; background: var(--bx-panel, #23272e); border-bottom: 1px solid var(--bx-border, #39414d);
      padding: 5px 10px; color: var(--bx-muted, #8794a1); z-index: 1; }
    pre { margin: 0; padding: 8px 12px; white-space: pre; tab-size: 4; }
    pre.wrap { white-space: pre-wrap; word-break: break-word; }
    .muted { color: var(--bx-muted, #8794a1); padding: 12px; display: block; }
    .err { color: var(--bx-red, #e5484d); padding: 12px; }
    /* diff line colors */
    .diff .fh { color: var(--bx-muted, #8794a1); display: block; }
    .diff .h { color: #61afef; display: block; }
    .diff .d { color: #98c379; display: block; background: color-mix(in srgb, #98c379 10%, transparent); }
    .diff .a { color: #e06c75; display: block; background: color-mix(in srgb, #e06c75 10%, transparent); }
    .diff .ctx { display: block; color: #abb2bf; }
    /* highlight.js — Atom-One-Dark-ish, scoped */
    .hljs-comment, .hljs-quote { color: #7f8896; font-style: italic; }
    .hljs-keyword, .hljs-selector-tag, .hljs-doctag, .hljs-formula { color: #c678dd; }
    .hljs-name, .hljs-section, .hljs-tag, .hljs-deletion { color: #e06c75; }
    .hljs-string, .hljs-regexp, .hljs-addition, .hljs-meta .hljs-string { color: #98c379; }
    .hljs-number, .hljs-literal, .hljs-type, .hljs-params, .hljs-template-variable, .hljs-variable { color: #d19a66; }
    .hljs-title, .hljs-class .hljs-title, .hljs-function .hljs-title { color: #61afef; }
    .hljs-attr, .hljs-attribute, .hljs-symbol, .hljs-bullet, .hljs-meta { color: #56b6c2; }
    .hljs-built_in, .hljs-selector-class, .hljs-selector-id { color: #e5c07b; }
    .hljs-emphasis { font-style: italic; } .hljs-strong { font-weight: 700; }
  `;

  constructor() {
    super();
    this._tab = 'files';
    this._tree = null; this._log = null; this._collapsed = new Set();
    this._rev = ''; this._q = '';
  }

  connectedCallback() { super.connectedCallback(); this._load(); }
  updated(ch) { if (ch.has('src') && this.src) { this._sel = null; this._file = null; this._diff = null; this._load(); } }

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
  }

  async _openFile(path) {
    this._sel = path; this._file = null;
    const f = await this._api(`code/file?component=${encodeURIComponent(this.src)}&file=${encodeURIComponent(path)}`);
    if (f) this._file = f;
  }

  async _loadDiff(rev) {
    this._rev = rev; this._diff = null;
    const q = rev ? `&rev=${encodeURIComponent(rev)}` : '';
    const d = await this._api(`git/diff?component=${encodeURIComponent(this.src)}${q}`);
    if (d) this._diff = d;
  }

  _setTab(t) {
    this._tab = t;
    if (t === 'changes' && !this._diff) this._loadDiff(this._rev || '');
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
            <span class="tw">${show ? '▾' : '▸'}</span><span class="ic">📁</span>${name}
          </div>
          ${show ? this._renderDir(node.dirs[name], dpath, depth + 1) : nothing}`;
      })}
      ${files.filter((f) => !q || f.path.toLowerCase().includes(q)).map((f) => html`
        <div class="row ${this._sel === f.path ? 'on' : ''}" style="padding-left:${depth * 12 + 16}px"
             @click=${() => this._openFile(f.path)} title=${f.path}>
          <span class="ic">📄</span>${f.name}
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
    return html`
      <div class="list">
        <div class="commit ${this._rev === '' ? 'on' : ''}" @click=${() => this._loadDiff('')}>
          <div class="subj">● Working tree</div>
          <div class="meta">uncommitted changes</div>
        </div>
        ${commits.map((c) => html`
          <div class="commit ${this._rev === (c.hash || c.rev) ? 'on' : ''}"
               @click=${() => this._loadDiff(c.hash || c.rev)}>
            <div class="subj">${c.subject ?? c.message ?? (c.hash || '').slice(0, 8)}</div>
            <div class="meta">${(c.hash || c.rev || '').slice(0, 8)} · ${c.author ?? ''} ${c.date ?? ''}</div>
          </div>`)}
        ${commits.length === 0 ? html`<span class="muted">no commits yet</span>` : nothing}
      </div>`;
  }

  _mainPane() {
    if (this._err) return html`<div class="err">${this._err}</div>`;
    if (this._tab === 'changes') {
      if (!this._diff) return html`<span class="muted">loading…</span>`;
      if (this._diff.repo === false) return html`<span class="muted">not a git repo</span>`;
      return html`
        <div class="path">${this._rev ? `commit ${this._rev.slice(0, 8)}` : 'working tree — uncommitted changes'}</div>
        <pre class="diff hljs">${unsafeHTML(diffHTML(this._diff.diff))}</pre>`;
    }
    if (!this._sel) return html`<span class="muted">select a file</span>`;
    if (!this._file) return html`<span class="muted">loading…</span>`;
    if (this._file.binary) return html`<span class="muted">binary file (${this._file.size} bytes)</span>`;
    if (this._file.truncated) return html`<span class="muted">file too large to display (${this._file.size} bytes)</span>`;
    return html`
      <div class="path">${this._sel}</div>
      <pre class="hljs"><code>${unsafeHTML(hl(this._file.content ?? '', langFor(this._sel)))}</code></pre>`;
  }

  render() {
    return html`
      <div class="side">
        <div class="tabs">
          <button class=${this._tab === 'files' ? 'on' : ''} @click=${() => this._setTab('files')}>Files</button>
          <button class=${this._tab === 'changes' ? 'on' : ''} @click=${() => this._setTab('changes')}>Changes</button>
        </div>
        ${this._tab === 'files' ? this._filesPane() : this._changesPane()}
      </div>
      <div class="main">${this._mainPane()}</div>`;
  }
}
customElements.define('bx-code', BxCode);
