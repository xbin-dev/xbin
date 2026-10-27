/**
 * code-tree.js — the tile window's Files and Changes side panels (D202), the
 * lists half of what <bx-code> was: a click opens a tab in the window instead
 * of a viewer beside the list (the window owns the tabs; code-view.js draws
 * a file or a diff in one).
 *
 *   <bx-code-tree component="apps/x" active="backend/main.go">
 *     the tile's files as a tree, with a filter; folders fold; a changed file
 *     carries its working-tree status (M, A, D, R). A file opens as
 *     `bx-open-tab` {ref: 'f:<path>'}; `active` (a path) marks the open one.
 *   <bx-code-changes component="apps/x" active="w:backend/main.go">
 *     what isn't committed yet, one row per file with its +/- counts
 *     ({ref: 'w:<path>'}), then the history ({ref: 'c:<rev>'}), then the
 *     commit activity (folded until asked for). `active` is the open tab's ref.
 *
 * `bx-open-tab` bubbles and is composed. Both refresh live when the tile's
 * work tree changes (code-shared.js savedEvent, the rule <bx-code> follows),
 * and read the grant-gated /api/xbin/code/* and /git/* endpoints with a raw
 * fetch: the signed-in user (admin or a code:<tile> grant), as <bx-code>.
 * Rows are buttons; ↑/↓ move between them.
 */
import { LitElement, html, css, nothing } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import { buildTree } from '/vendor/bx-code.js';
import { onEvent } from '/vendor/events-socket.js';
import { savedEvent, fileDiffs, statusMap, relTime, fmtDate, activitySummary } from '/vendor/code-shared.js';
import '/vendor/bx-icons.js';

// codeGet(path): GET /api/xbin/<path> as the signed-in user → {data} or {err}.
// A raw fetch on purpose: xbin.fetch's frame token would downgrade to an
// element principal and 403 (the admin console reads code the same way).
export async function codeGet(path) {
  try {
    const r = await fetch(`/api/xbin/${path}`);
    if (r.status === 403) return { err: 'no code access for this tile (needs admin or a code: grant)' };
    if (!r.ok) return { err: `error ${r.status}`, status: r.status };
    return { data: await r.json() };
  } catch (e) { return { err: String(e.message ?? e) }; }
}

// watchCode(src, cb): call cb (coalesced, 250 ms) whenever src()'s work tree
// changes; returns the unsubscribe.
export function watchCode(src, cb) {
  let t = 0;
  const off = onEvent((e) => {
    if (!savedEvent(e, src())) return;
    clearTimeout(t);
    t = setTimeout(cb, 250);
  });
  return () => { off(); clearTimeout(t); };
}

// openTab(el, ref): ask the window to open (or show) the tab named ref.
export const openTab = (el, ref) => el.dispatchEvent(new CustomEvent('bx-open-tab', { detail: { ref }, bubbles: true, composed: true }));

// listKeys(e): ↑/↓ (Home/End) move the focus between a list's row buttons.
export function listKeys(e) {
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(e.key)) return;
  const rows = [...e.currentTarget.querySelectorAll('button.row')];
  if (!rows.length) return;
  const i = rows.indexOf(e.target.closest?.('button.row'));
  const j = e.key === 'Home' ? 0 : e.key === 'End' ? rows.length - 1 : Math.max(0, Math.min(rows.length - 1, i + (e.key === 'ArrowDown' ? 1 : -1)));
  rows[j]?.focus();
  e.preventDefault();
}

// panelCss: a side panel's look (Base Two, D184): 24 px rows, names and
// paths in the code face, the open row in the selection colour with the
// accent rule; section heads in micro caps.
export const panelCss = css`
  :host { display: flex; flex-direction: column; height: 100%; min-height: 0; box-sizing: border-box;
    font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif); color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028); }
  button, input { font: inherit; }
  :focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: calc(-1 * var(--bx-focus-width, 3px)); }
  .list { flex: 1; overflow: auto; min-height: 0; padding: 2px 0 8px; }
  .row { display: flex; align-items: center; gap: 6px; box-sizing: border-box; width: 100%; min-height: 24px; padding: 0 8px; border: 0; margin: 0;
    background: none; color: var(--bx-text, #E9EAF0); text-align: left; cursor: pointer; white-space: nowrap;
    font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); }
  .row:hover { background: var(--bx-hover, #2A2B34); }
  .row.on { background: var(--bx-selection, #262C5C); color: var(--bx-selection-text, #E9EAF0); box-shadow: inset 2px 0 0 var(--bx-accent, #8C9BFF); }
  .row .tw { display: inline-flex; width: 16px; flex: none; justify-content: center; color: var(--bx-muted, #A3A6B6); }
  .row .ic { display: inline-flex; flex: none; color: var(--bx-muted, #A3A6B6); }
  .row .nm { overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .row .gst { margin-left: auto; padding-left: 8px; flex: none; font-weight: 600; }
  .gst.M, .gst.R { color: var(--bx-warn, #F2994A); }
  .gst.A { color: var(--bx-ok, #A3CF5E); }
  .gst.D { color: var(--bx-danger, #FF7A7A); }
  .row .cnt { margin-left: auto; padding-left: 8px; flex: none; font-variant-numeric: tabular-nums; }
  .row .cnt + .gst { margin-left: 0; }
  .pl { color: var(--bx-diff-add, #5EDBA5); }
  .mi { color: var(--bx-diff-del, #FF8F8F); }
  .sec { display: flex; align-items: center; gap: 6px; box-sizing: border-box; width: 100%; height: 26px; padding: 0 8px; margin: 6px 0 0;
    border: 0; background: none; color: var(--bx-muted, #A3A6B6); text-align: left;
    font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase; }
  button.sec { cursor: pointer; }
  button.sec:hover { color: var(--bx-text, #E9EAF0); }
  .filter { flex: none; box-sizing: border-box; height: var(--bx-control-h, 28px); margin: 8px; padding: 0 8px;
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); }
  .filter::placeholder { color: var(--bx-subtle, #8E91A2); opacity: 1; }
  .filter:focus-visible { outline-offset: var(--bx-focus-offset, 2px); }
  .muted { color: var(--bx-muted, #A3A6B6); padding: 6px 10px; display: block; }
  .err { color: var(--bx-danger, #FF7A7A); padding: 8px 10px; display: block; }
  .err bx-icon { margin-right: 6px; }
`;

export class BxCodeTree extends LitElement {
  static properties = {
    component: { type: String },
    active: { type: String },   // the open file's path: its row is marked
    _tree: { state: true },     // [{path, size}]
    _st: { state: true },       // path → working-tree status letter
    _collapsed: { state: true },
    _q: { state: true },        // the filter
    _err: { state: true },
  };
  static styles = [scrollCss, panelCss];

  constructor() {
    super();
    this._tree = null; this._st = {}; this._collapsed = new Set(); this._q = ''; this._err = '';
  }
  connectedCallback() {
    super.connectedCallback();
    this._off = watchCode(() => this.component, () => this._load());
  }
  disconnectedCallback() { this._off?.(); super.disconnectedCallback(); }
  willUpdate(ch) {
    if (ch.has('component') && ch.get('component') !== undefined) { this._tree = null; this._st = {}; this._collapsed = new Set(); }
  }
  updated(ch) { if (ch.has('component') && this.component) this._load(); }

  async _load() {
    const c = this.component;
    if (!c) return;
    const q = encodeURIComponent(c);
    const [t, d] = await Promise.all([codeGet(`code/tree?component=${q}`), codeGet(`git/diff?component=${q}`)]);
    if (c !== this.component) return;
    if (t.err) { this._err = t.err; return; }
    this._err = '';
    this._tree = t.data.files || [];
    this._st = d.data && d.data.repo !== false ? statusMap(d.data.diff) : {};
  }

  _toggle(path) {
    const c = new Set(this._collapsed);
    if (c.has(path)) c.delete(path); else c.add(path);
    this._collapsed = c;
  }

  _dir(node, prefix, depth) {
    const q = this._q.toLowerCase();
    const dirs = Object.keys(node.dirs).sort();
    const files = node.files.slice().sort((a, b) => a.name.localeCompare(b.name)).filter((f) => !q || f.path.toLowerCase().includes(q));
    return html`
      ${dirs.map((name) => {
        const path = prefix ? `${prefix}/${name}` : name;
        const open = !this._collapsed.has(path) || !!q; // a filter opens every folder, so matches show
        return html`
          <button class="row" style="padding-left:${depth * 12 + 4}px" aria-expanded=${open ? 'true' : 'false'} title=${path}
                  @click=${() => this._toggle(path)}>
            <span class="tw"><bx-icon name=${open ? 'caret-down' : 'caret-right'}></bx-icon></span><span class="ic"><bx-icon name="folder"></bx-icon></span><span class="nm">${name}</span>
          </button>
          ${open ? this._dir(node.dirs[name], path, depth + 1) : nothing}`;
      })}
      ${files.map((f) => {
        const st = this._st[f.path];
        return html`
          <button class="row ${this.active === f.path ? 'on' : ''}" style="padding-left:${depth * 12 + 20}px" title=${f.path}
                  aria-current=${this.active === f.path ? 'true' : 'false'} @click=${() => openTab(this, 'f:' + f.path)}>
            <span class="ic"><bx-icon name="file"></bx-icon></span><span class="nm">${f.name}</span>
            ${st ? html`<span class="gst ${st}" title=${{ M: 'changed', A: 'added', D: 'deleted', R: 'renamed' }[st]}>${st}</span>` : nothing}
          </button>`;
      })}`;
  }

  render() {
    return html`
      <input class="filter" placeholder="filter files…" aria-label="filter files" .value=${this._q}
             @input=${(e) => { this._q = e.target.value; }}>
      ${this._err ? html`<span class="err"><bx-icon name="error"></bx-icon>${this._err}</span>` : nothing}
      <div class="list" role="tree" aria-label="files" @keydown=${listKeys}>
        ${this._tree == null ? (this._err ? nothing : html`<span class="muted">loading…</span>`)
          : this._tree.length === 0 ? html`<span class="muted">no files</span>`
          : this._dir(buildTree(this._tree), '', 0)}
      </div>`;
  }
}
customElements.define('bx-code-tree', BxCodeTree);

export class BxCodeChanges extends LitElement {
  static properties = {
    component: { type: String },
    active: { type: String },   // the open tab's ref ('w:<path>' or 'c:<rev>')
    _files: { state: true },    // the uncommitted files (code-shared.js fileDiffs), or null
    _repo: { state: true },     // false: not a git repo
    _log: { state: true },      // git/log's {commits}
    _act: { state: true },      // git/activity, once the section opened
    _actOpen: { state: true },
    _err: { state: true },
  };
  static styles = [scrollCss, panelCss, css`
    .commit { display: block; box-sizing: border-box; width: 100%; padding: 6px 8px 6px 10px; border: 0; margin: 0; background: none; text-align: left;
      color: var(--bx-text, #E9EAF0); cursor: pointer; border-bottom: 1px solid var(--bx-border, #33353F); font: inherit; }
    .commit:hover { background: var(--bx-hover, #2A2B34); }
    .commit.on { background: var(--bx-selection, #262C5C); color: var(--bx-selection-text, #E9EAF0); box-shadow: inset 2px 0 0 var(--bx-accent, #8C9BFF); }
    .commit .subj { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .commit .meta { display: flex; align-items: baseline; gap: 6px; color: var(--bx-muted, #A3A6B6);
      font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); font-variant-numeric: tabular-nums; }
    .commit .meta .id { font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); }
    .commit .meta .cnt { margin-left: auto; white-space: nowrap; }
    .cards { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6px; padding: 4px 8px; }
    .cd { border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); padding: 6px 8px; }
    .cd .v { font: var(--bx-font-title, 600 16px/22px "Instrument Sans", system-ui, sans-serif); font-variant-numeric: tabular-nums; }
    .cd .l { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif);
      letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase; }
    .chart { display: flex; align-items: flex-end; gap: 1px; height: 56px; padding: 8px 8px 0; }
    .chart .col { position: relative; flex: 1; height: 100%; }
    .chart .b { position: absolute; bottom: 0; width: 100%; }
    .chart .b.loc { background: var(--bx-term-blue, #6F86FF); }
    .chart .b.up { background: var(--bx-term-magenta, #FF5FB0); }
    .axis { display: flex; justify-content: space-between; padding: 2px 8px 0; color: var(--bx-muted, #A3A6B6);
      font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); font-variant-numeric: tabular-nums; }
    .arow { display: flex; gap: 6px; padding: 2px 10px; }
    .arow .an { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .arow .ac { color: var(--bx-muted, #A3A6B6); font-variant-numeric: tabular-nums; }
  `];

  constructor() {
    super();
    this._files = null; this._repo = true; this._log = null; this._act = null; this._actOpen = false; this._err = '';
  }
  connectedCallback() {
    super.connectedCallback();
    this._off = watchCode(() => this.component, () => this._load());
  }
  disconnectedCallback() { this._off?.(); super.disconnectedCallback(); }
  willUpdate(ch) {
    if (ch.has('component') && ch.get('component') !== undefined) { this._files = null; this._log = null; this._act = null; }
  }
  updated(ch) { if (ch.has('component') && this.component) this._load(); }

  async _load() {
    const c = this.component;
    if (!c) return;
    const q = encodeURIComponent(c);
    const [d, l] = await Promise.all([codeGet(`git/diff?component=${q}`), codeGet(`git/log?component=${q}&limit=50`)]);
    if (c !== this.component) return;
    if (d.err) { this._err = d.err; return; }
    this._err = '';
    this._repo = d.data.repo !== false;
    this._files = this._repo ? fileDiffs(d.data.diff) : [];
    if (l.data) this._log = l.data;
    if (this._actOpen) this._loadActivity();
  }
  async _loadActivity() {
    const c = this.component;
    const a = await codeGet(`git/activity?component=${encodeURIComponent(c)}`);
    if (c === this.component && a.data) this._act = a.data;
  }
  _toggleActivity() {
    this._actOpen = !this._actOpen;
    if (this._actOpen && !this._act) this._loadActivity();
  }

  _activity() {
    if (!this._act) return html`<span class="muted">loading…</span>`;
    const a = activitySummary(this._act);
    if (!a) return html`<span class="muted">not a git repo</span>`;
    const max = Math.max(1, ...a.weeksLocal, ...(a.weeksUp || [0]));
    const n = a.weeksLocal.length;
    const card = (l, v) => html`<div class="cd"><div class="v">${v}</div><div class="l">${l}</div></div>`;
    return html`
      <div class="cards">${card('commits', a.total)}${card('last 30 days', a.last30)}${card('authors', a.topAuthors.length)}${card('latest', relTime(a.last))}</div>
      <div class="chart" role="img" aria-label="commits per week over the last 52 weeks">
        ${a.weeksLocal.map((v, i) => {
          const up = a.weeksUp ? a.weeksUp[i] : 0;
          return html`<div class="col" title="week of ${fmtDate(a.now - (n - 1 - i) * a.WEEK)} — ${v} local${a.weeksUp ? `, ${up} upstream` : ''}">
            ${a.weeksUp ? html`<span class="b up" style="height:${(up / max) * 100}%"></span>` : nothing}
            <span class="b loc" style="height:${(v / max) * 100}%"></span></div>`;
        })}
      </div>
      <div class="axis"><span>${fmtDate(a.now - 51 * a.WEEK)}</span><span>now</span></div>
      ${a.topAuthors.map(([name, k]) => html`<div class="arow"><span class="an">${name}</span><span class="ac">${k}</span></div>`)}`;
  }

  render() {
    if (this._err) return html`<span class="err"><bx-icon name="error"></bx-icon>${this._err}</span>`;
    if (this._files == null) return html`<span class="muted">loading…</span>`;
    if (!this._repo) return html`<span class="muted">not a git repo</span>`;
    const commits = this._log?.commits ?? [];
    return html`
      <div class="list" @keydown=${listKeys}>
        <div class="sec">Not committed · ${this._files.length}</div>
        ${this._files.map((f) => html`
          <button class="row ${this.active === 'w:' + f.path ? 'on' : ''}" title=${f.path} @click=${() => openTab(this, 'w:' + f.path)}>
            <span class="ic"><bx-icon name="file"></bx-icon></span><span class="nm">${f.path}</span>
            <span class="cnt"><span class="pl">+${f.add}</span> <span class="mi">−${f.del}</span></span>
            <span class="gst ${f.status}">${f.status}</span>
          </button>`)}
        ${this._files.length === 0 ? html`<span class="muted">nothing to commit</span>` : nothing}
        <div class="sec">History</div>
        ${commits.map((c) => {
          const rev = c.hash || c.rev || '';
          return html`
            <button class="row commit ${this.active === 'c:' + rev ? 'on' : ''}" @click=${() => openTab(this, 'c:' + rev)}>
              <span class="subj">${c.subject ?? c.message ?? rev.slice(0, 8)}</span>
              <span class="meta"><span class="id">${(c.short || rev).slice(0, 8)}</span><span>${c.author ?? ''}</span>
                <span class="cnt"><span class="pl">+${c.add ?? 0}</span> <span class="mi">−${c.del ?? 0}</span> · ${c.date ? relTime(Date.parse(c.date) / 1000) : ''}</span></span>
            </button>`;
        })}
        ${commits.length === 0 ? html`<span class="muted">no commits yet</span>` : nothing}
        <button class="sec" aria-expanded=${this._actOpen ? 'true' : 'false'} @click=${() => this._toggleActivity()}>
          <bx-icon name=${this._actOpen ? 'caret-down' : 'caret-right'}></bx-icon>Activity</button>
        ${this._actOpen ? this._activity() : nothing}
      </div>`;
  }
}
customElements.define('bx-code-changes', BxCodeChanges);
