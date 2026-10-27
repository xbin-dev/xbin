/**
 * code-view.js — what a code tab shows in the tile window (D202): a file, or
 * a diff, the viewer half of what <bx-code> was (code-tree.js has the lists).
 *
 *   <bx-code-view component="apps/x" path="backend/main.go">
 *     the file, read-only (editing is the terminal's and the agent's job):
 *     a breadcrumb, a line-number gutter and the code highlighted for its
 *     language. It follows the file live, keeping its scroll, when the
 *     tile's work tree changes.
 *   <bx-code-diff component="apps/x" rev="" path="backend/main.go">
 *     rev '' — the uncommitted change (followed live), else a commit; path
 *     narrows it to one file's part. Its head says what it is: the file and
 *     "not committed", or the commit's subject, id, author and time; and
 *     the +/- counts.
 *
 * Both scroll themselves (they fill the tab), draw code and diffs through
 * bx-code's pipeline (codeCss, hl, diffHTML) so every code surface reads
 * the same, and read the endpoints as the signed-in user (codeGet).
 */
import { LitElement, html, css, nothing } from 'lit';
import { unsafeHTML } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import { codeCss, hl, langFor, diffHTML } from '/vendor/bx-code.js';
import { codeGet, watchCode } from '/vendor/code-tree.js';
import { fileDiffs, relTime, commitOf } from '/vendor/code-shared.js';
import '/vendor/bx-icons.js';

const viewCss = css`
  /* UI text in the UI face; code, paths and ids in the code face (D184) */
  :host { display: flex; flex-direction: column; height: 100%; min-height: 0; box-sizing: border-box;
    font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif); color: var(--bx-text, #E9EAF0); background: var(--bx-code-bg, #16171D); }
  .head { flex: none; display: flex; align-items: center; gap: 8px; min-height: 26px; padding: 0 12px; box-sizing: border-box;
    border-bottom: 1px solid var(--bx-border, #33353F); background: var(--bx-panel, #1F2028); color: var(--bx-muted, #A3A6B6);
    font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); white-space: nowrap; overflow: hidden; }
  .head .crumb { overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .head .crumb b { color: var(--bx-text, #E9EAF0); font-weight: 400; }
  .head .stat { margin-left: auto; flex: none; font-variant-numeric: tabular-nums; }
  .dhead { flex: none; display: flex; flex-direction: column; gap: 2px; padding: 8px 12px; border-bottom: 1px solid var(--bx-border, #33353F);
    background: var(--bx-panel, #1F2028); }
  .dhead .t { font: var(--bx-font-title, 600 16px/22px "Instrument Sans", system-ui, sans-serif); overflow-wrap: anywhere; }
  .dhead .m { display: flex; flex-wrap: wrap; align-items: baseline; gap: 8px; color: var(--bx-muted, #A3A6B6);
    font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); font-variant-numeric: tabular-nums; }
  .dhead .m .id { font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); }
  .main { flex: 1; overflow: auto; min-height: 0; }
  pre, code { font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); }
  pre { margin: 0; padding: 8px 12px; white-space: pre; tab-size: 4; }
  .fileview { display: flex; align-items: stretch; min-width: max-content; }
  .gutter { position: sticky; left: 0; padding: 8px 8px 8px 12px; text-align: right; z-index: 1;
    color: var(--bx-subtle, #8E91A2); font-variant-numeric: tabular-nums; background: var(--bx-code-bg, #16171D);
    border-right: 1px solid var(--bx-border, #33353F); user-select: none; -webkit-user-select: none; }
  .code { flex: 1; }
  .pl { color: var(--bx-diff-add, #5EDBA5); }
  .mi { color: var(--bx-diff-del, #FF8F8F); }
  .muted { color: var(--bx-muted, #A3A6B6); padding: 12px; display: block; }
  .err { color: var(--bx-danger, #FF7A7A); padding: 12px; display: block; }
  .err bx-icon { margin-right: 6px; }
`;

export class BxCodeView extends LitElement {
  static properties = {
    component: { type: String },
    path: { type: String },
    _file: { state: true },   // {content|binary|truncated|size}
    _err: { state: true },
  };
  static styles = [scrollCss, codeCss, viewCss];

  constructor() { super(); this._file = null; this._err = ''; }
  connectedCallback() {
    super.connectedCallback();
    this._off = watchCode(() => this.component, () => this._load(true));
  }
  disconnectedCallback() { this._off?.(); super.disconnectedCallback(); }
  willUpdate(ch) { if ((ch.has('component') || ch.has('path')) && (ch.get('component') !== undefined || ch.get('path') !== undefined)) this._file = null; }
  updated(ch) { if ((ch.has('component') || ch.has('path')) && this.component && this.path) this._load(false); }

  // quiet: a live refresh — no loading flicker, the scroll kept, a render only when the file changed
  async _load(quiet) {
    const c = this.component, p = this.path;
    if (!c || !p) return;
    const r = await codeGet(`code/file?component=${encodeURIComponent(c)}&file=${encodeURIComponent(p)}`);
    if (c !== this.component || p !== this.path) return;
    if (r.err) { if (!quiet) this._err = r.err; return; }
    this._err = '';
    const f = r.data, was = this._file;
    if (quiet && was && was.content === f.content && was.binary === f.binary && was.truncated === f.truncated) return;
    const main = this.renderRoot.querySelector('.main');
    const st = main?.scrollTop, sl = main?.scrollLeft;
    this._file = f;
    if (!quiet || !main) return;
    await this.updateComplete;
    main.scrollTop = st ?? 0; main.scrollLeft = sl ?? 0;
  }

  _body() {
    if (this._err) return html`<span class="err"><bx-icon name="error"></bx-icon>${this._err}</span>`;
    const f = this._file;
    if (!f) return html`<span class="muted">loading…</span>`;
    if (f.binary) return html`<span class="muted">binary file (${f.size} bytes)</span>`;
    if (f.truncated) return html`<span class="muted">file too large to display (${f.size} bytes)</span>`;
    const content = f.content ?? '';
    let n = content.split('\n').length;
    if (content.endsWith('\n')) n--; // a trailing newline isn't its own line
    if (n < 1) n = 1;
    const nums = Array.from({ length: n }, (_, i) => i + 1).join('\n');
    return html`
      <div class="fileview">
        <pre class="gutter" aria-hidden="true">${nums}</pre>
        <pre class="hljs code"><code>${unsafeHTML(hl(content, langFor(this.path)))}</code></pre>
      </div>`;
  }

  render() {
    const parts = (this.path || '').split('/');
    const name = parts.pop();
    return html`
      <div class="head" title=${`${this.component} / ${this.path}`}>
        <bx-icon name="file"></bx-icon>
        <span class="crumb">${parts.length ? html`${parts.join(' / ')} / ` : nothing}<b>${name}</b></span>
        <span class="stat">read-only</span>
      </div>
      <div class="main">${this._body()}</div>`;
  }
}
customElements.define('bx-code-view', BxCodeView);

export class BxCodeDiff extends LitElement {
  static properties = {
    component: { type: String },
    rev: { type: String },    // '' — the uncommitted change; else a commit
    path: { type: String },   // optional: one file's part of it
    _diff: { state: true },   // git/diff's {diff, repo}
    _commit: { state: true }, // the commit's git/log row, when rev is set
    _err: { state: true },
  };
  static styles = [scrollCss, codeCss, viewCss];

  constructor() { super(); this.rev = ''; this.path = ''; this._diff = null; this._commit = null; this._err = ''; }
  connectedCallback() {
    super.connectedCallback();
    // a commit never changes; the uncommitted change follows the work tree
    this._off = watchCode(() => (this.rev ? '' : this.component), () => this._load(true));
  }
  disconnectedCallback() { this._off?.(); super.disconnectedCallback(); }
  updated(ch) {
    if (!(ch.has('component') || ch.has('rev') || ch.has('path')) || !this.component) return;
    if (ch.has('component') || ch.has('rev')) { this._diff = null; this._commit = null; }
    this._load(false);
  }

  async _load(quiet) {
    const c = this.component, rev = this.rev || '';
    if (!c) return;
    const q = encodeURIComponent(c);
    const [d, l] = await Promise.all([
      codeGet(`git/diff?component=${q}${rev ? `&rev=${encodeURIComponent(rev)}` : ''}`),
      rev && !this._commit ? codeGet(`git/log?component=${q}&limit=50`) : Promise.resolve(null),
    ]);
    if (c !== this.component || rev !== (this.rev || '')) return;
    if (d.err) { if (!quiet) this._err = d.err; return; }
    this._err = '';
    this._diff = d.data;
    if (l?.data) this._commit = commitOf(l.data, rev);
  }

  // the part of the diff this tab shows, and its counts
  _part() {
    const all = this._diff?.diff || '';
    const files = fileDiffs(all);
    const shown = this.path ? files.filter((f) => f.path === this.path || f.from === this.path) : files;
    return {
      text: this.path ? shown.map((f) => f.text).join('') : all,
      add: shown.reduce((n, f) => n + f.add, 0), del: shown.reduce((n, f) => n + f.del, 0), files: shown.length,
    };
  }

  _head(p) {
    const stat = p.files ? html`<span><span class="pl">+${p.add}</span> <span class="mi">−${p.del}</span>${this.path ? nothing : html` · ${p.files} file${p.files === 1 ? '' : 's'}`}</span>` : nothing;
    if (!this.rev) {
      return html`<div class="dhead">
        <span class="t">${this.path || 'Every file'}</span>
        <span class="m"><span>not committed · against the last commit</span>${stat}</span></div>`;
    }
    const c = this._commit;
    return html`<div class="dhead">
      <span class="t">${c ? (c.subject ?? c.message) : `commit ${this.rev.slice(0, 8)}`}${this.path ? html` — ${this.path}` : nothing}</span>
      <span class="m"><span class="id">${(c?.short || this.rev).slice(0, 8)}</span>
        ${c?.author ? html`<span>${c.author}</span>` : nothing}
        ${c?.date ? html`<span title=${c.date}>${relTime(Date.parse(c.date) / 1000)}</span>` : nothing}
        ${stat}</span></div>`;
  }

  render() {
    if (this._err) return html`<span class="err"><bx-icon name="error"></bx-icon>${this._err}</span>`;
    if (!this._diff) return html`<span class="muted">loading…</span>`;
    if (this._diff.repo === false) return html`<span class="muted">not a git repo</span>`;
    const p = this._part();
    return html`
      ${this._head(p)}
      <div class="main">
        ${p.text ? html`<pre class="diff hljs">${unsafeHTML(diffHTML(p.text))}</pre>`
          : html`<span class="muted">${this.rev ? 'no changes in this commit' : this.path ? 'this file has no uncommitted changes now' : 'nothing to commit'}</span>`}
      </div>`;
  }
}
customElements.define('bx-code-diff', BxCodeDiff);
