/**
 * <bx-prs component="apps/x"> — the change-proposal ("code PR") panel for the
 * terminal pop-up (docs/bx.md §code pr). Lists PRs targeting the component,
 * renders the proposed patch series through the same diff pipeline as
 * bx-code, shows the review thread, and carries the target-side actions:
 * comment, mark merged, reject. Applying is NOT a button — the patch lands
 * only when this tile's own plane runs `git am` in its terminal, so the
 * panel shows the exact command instead.
 *
 * Data comes from the read-gated /api/xbin/code/pr* endpoints via raw fetch
 * (the signed-in user), like bx-code. Live-refreshes on `pr` events.
 */
import { LitElement, html, css, nothing } from 'lit';
import { unsafeHTML } from 'lit';
import { diffHTML, diffStats } from '/vendor/bx-code.js';
import { onEvent } from '/vendor/events-socket.js';

function relTime(iso) {
  const t = Date.parse(iso || '');
  if (!t) return '—';
  const s = Math.floor((Date.now() - t) / 1000);
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

export class BxPrs extends LitElement {
  static properties = {
    component: { type: String },
    _prs: { state: true },      // full list (all states)
    _showAll: { state: true },  // list filter: open only ↔ everything
    _sel: { state: true },      // selected PR number
    _pr: { state: true },       // selected PR meta (with thread)
    _series: { state: true },   // selected PR's raw mbox
    _comment: { state: true },  // comment draft
    _busy: { state: true },
    _err: { state: true },
  };

  static styles = css`
    :host { display: flex; height: 100%; min-height: 0; font: 12px/1.5 var(--bx-mono, ui-monospace, monospace);
      color: var(--bx-text, #d7dce5); background: var(--bx-panel, #23272e); }
    .side { width: 230px; flex: none; display: flex; flex-direction: column; border-right: 1px solid var(--bx-border, #39414d); min-height: 0; }
    .tabs { display: flex; flex: none; border-bottom: 1px solid var(--bx-border, #39414d); }
    .tabs button { flex: 1; background: none; border: 0; color: var(--bx-muted, #8794a1); padding: 6px 4px;
      font: inherit; cursor: pointer; border-bottom: 2px solid transparent; }
    .tabs button.on { color: var(--bx-text, #d7dce5); border-bottom-color: var(--bx-accent, #f2a71b); }
    .list { flex: 1; overflow: auto; min-height: 0; }
    .row { padding: 5px 8px; cursor: pointer; border-bottom: 1px solid color-mix(in srgb, var(--bx-border,#39414d) 40%, transparent); }
    .row:hover { background: var(--bx-panel-2, #1c2026); }
    .row.on { background: color-mix(in srgb, var(--bx-accent, #f2a71b) 22%, transparent); }
    .row .t { color: var(--bx-text, #d7dce5); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .row .m { color: var(--bx-muted, #8794a1); font-size: 10.5px; display: flex; gap: 6px; }
    .row .m .st { margin-left: auto; }
    .st { text-transform: uppercase; letter-spacing: .04em; font-size: 9.5px; padding: 0 5px; border-radius: 3px;
      border: 1px solid var(--bx-border, #39414d); color: var(--bx-muted, #8794a1); }
    .st.open { color: #98c379; border-color: color-mix(in srgb, #98c379 45%, transparent); }
    .st.merged { color: #61afef; border-color: color-mix(in srgb, #61afef 45%, transparent); }
    .st.rejected { color: #e06c75; border-color: color-mix(in srgb, #e06c75 45%, transparent); }
    .main { flex: 1; overflow: auto; min-height: 0; display: flex; flex-direction: column; }
    .head { position: sticky; top: 0; z-index: 2; background: var(--bx-panel, #23272e);
      border-bottom: 1px solid var(--bx-border, #39414d); padding: 7px 12px; }
    .head .ttl { color: var(--bx-text, #d7dce5); font-weight: 600; }
    .head .sub { color: var(--bx-muted, #8794a1); font-size: 10.5px; display: flex; gap: 8px; flex-wrap: wrap; align-items: baseline; }
    .head .sub .stat { margin-left: auto; white-space: nowrap; }
    .msg { padding: 8px 12px; white-space: pre-wrap; border-bottom: 1px solid color-mix(in srgb, var(--bx-border,#39414d) 40%, transparent); }
    .apply { margin: 8px 12px; padding: 6px 9px; background: var(--bx-panel-2, #1c2026);
      border: 1px solid var(--bx-border, #39414d); border-radius: 5px; color: var(--bx-muted, #8794a1); }
    .apply code { color: var(--bx-text, #d7dce5); user-select: all; }
    .apply .warn { color: var(--bx-amber, #f2a71b); }
    pre.diff { margin: 0; padding: 8px 12px; white-space: pre; tab-size: 4; }
    .diff .fh { color: var(--bx-muted, #8794a1); display: block; }
    .diff .h { color: #61afef; display: block; }
    .diff .d { color: #98c379; display: block; background: color-mix(in srgb, #98c379 10%, transparent); }
    .diff .a { color: #e06c75; display: block; background: color-mix(in srgb, #e06c75 10%, transparent); }
    .diff .ctx { display: block; color: #abb2bf; }
    .pl { color: #98c379; } .mi { color: #e06c75; }
    .thread { border-top: 1px solid var(--bx-border, #39414d); padding: 4px 12px 8px; }
    .ev { margin-top: 6px; }
    .ev .who { color: var(--bx-muted, #8794a1); font-size: 10.5px; }
    .ev .body { white-space: pre-wrap; }
    .ev.state .body { color: var(--bx-muted, #8794a1); font-style: italic; }
    .actions { display: flex; gap: 6px; padding: 8px 12px; border-top: 1px solid var(--bx-border, #39414d);
      position: sticky; bottom: 0; background: var(--bx-panel, #23272e); }
    .actions input { flex: 1; min-width: 0; background: var(--bx-panel-2, #1c2026); border: 1px solid var(--bx-border, #39414d);
      border-radius: 4px; color: inherit; font: inherit; padding: 4px 7px; }
    .actions button { background: var(--bx-panel-2, #1c2026); border: 1px solid var(--bx-border, #39414d);
      border-radius: 4px; color: var(--bx-text, #d7dce5); font: inherit; padding: 4px 9px; cursor: pointer; white-space: nowrap; }
    .actions button:hover { border-color: var(--bx-accent, #f2a71b); }
    .actions button.ok { color: #98c379; } .actions button.no { color: #e06c75; }
    .actions button[disabled] { opacity: .5; cursor: default; }
    .muted { color: var(--bx-muted, #8794a1); padding: 12px; display: block; }
    .err { color: var(--bx-red, #e5484d); padding: 6px 12px; }
  `;

  constructor() {
    super();
    this._prs = null; this._showAll = false; this._sel = null;
    this._comment = ''; this._busy = false;
  }

  connectedCallback() {
    super.connectedCallback();
    this._offEvents = onEvent((e) => {
      if (e.type === 'pr' && e.component === this.component) this._refresh();
    });
    this._load();
  }
  disconnectedCallback() { this._offEvents?.(); super.disconnectedCallback(); }
  updated(ch) {
    if (ch.has('component') && this.component) { this._sel = null; this._pr = null; this._load(); }
  }

  async _api(path, body) {
    try {
      const r = await fetch(`/api/xbin/${path}`, body
        ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }
        : undefined);
      const d = await r.json().catch(() => ({}));
      if (!r.ok) { this._err = d.error || `error ${r.status}`; return null; }
      this._err = '';
      return d;
    } catch (e) { this._err = String(e.message ?? e); return null; }
  }

  async _load() {
    if (!this.component) return;
    const d = await this._api(`code/prs?target=${encodeURIComponent(this.component)}`);
    if (!d) return;
    this._prs = d.prs || [];
    // Auto-select the newest open PR on first load.
    if (this._sel == null) {
      const open = this._prs.find((p) => p.state === 'open');
      if (open) this._open(open.number);
    }
  }

  async _refresh() {
    const d = await this._api(`code/prs?target=${encodeURIComponent(this.component)}`);
    if (d) this._prs = d.prs || [];
    if (this._sel != null) {
      const m = await this._api(`code/pr?target=${encodeURIComponent(this.component)}&n=${this._sel}`);
      if (m && m.number === this._sel) this._pr = m;
    }
  }

  async _open(n) {
    this._sel = n; this._pr = null; this._series = null;
    const m = await this._api(`code/pr?target=${encodeURIComponent(this.component)}&n=${n}`);
    if (!m || this._sel !== n) return;
    this._pr = m;
    try {
      const r = await fetch(`/api/xbin/code/pr/series?target=${encodeURIComponent(this.component)}&n=${n}`);
      if (r.ok && this._sel === n) this._series = await r.text();
    } catch { /* diff pane shows loading */ }
  }

  async _postComment() {
    const text = this._comment.trim();
    if (!text || this._busy) return;
    this._busy = true;
    const d = await this._api('code/pr/comment', { target: this.component, n: this._sel, body: text });
    this._busy = false;
    if (d) { this._comment = ''; this._pr = d; this._load(); }
  }

  async _close(state) {
    if (this._busy) return;
    const verb = state === 'merged' ? 'Mark merged' : 'Reject';
    const note = prompt(`${verb} — optional note for the author (why / applied as <sha>):`, this._comment.trim());
    if (note === null) return;
    this._busy = true;
    const d = await this._api('code/pr/state', { target: this.component, n: this._sel, state, comment: note.trim() });
    this._busy = false;
    if (d) { this._comment = ''; this._pr = d; this._load(); }
  }

  _list() {
    const prs = (this._prs || []).filter((p) => this._showAll || p.state === 'open');
    return html`
      <div class="tabs">
        <button class=${!this._showAll ? 'on' : ''} @click=${() => { this._showAll = false; }}>Open</button>
        <button class=${this._showAll ? 'on' : ''} @click=${() => { this._showAll = true; }}>All</button>
      </div>
      <div class="list">
        ${prs.map((p) => html`
          <div class="row ${this._sel === p.number ? 'on' : ''}" @click=${() => this._open(p.number)}>
            <div class="t">#${p.number} ${p.title}</div>
            <div class="m">
              <span>${p.from?.component || (p.from?.user ? 'user:' + p.from.user : 'owner')}</span>
              <span>${relTime(p.updated)}</span>
              <span class="st ${p.state}">${p.state}</span>
            </div>
          </div>`)}
        ${prs.length === 0 ? html`<span class="muted">${this._prs === null ? 'loading…'
          : this._showAll ? 'no proposals' : 'no open proposals'}</span>` : nothing}
      </div>`;
  }

  _main() {
    if (this._err && !this._pr) return html`<div class="err">${this._err}</div>`;
    if (this._sel == null) return html`<span class="muted">select a proposal — or open one from another tile's terminal with: bx code pr ${this.component} --title … *.patch</span>`;
    const m = this._pr;
    if (!m) return html`<span class="muted">loading…</span>`;
    const from = m.from?.component
      ? m.from.component + (m.from.user ? ` (${m.from.user})` : '')
      : (m.from?.user ? 'user:' + m.from.user : 'owner');
    const s = this._series != null ? diffStats(this._series) : null;
    return html`
      <div class="head">
        <div class="ttl">#${m.number} ${m.title}</div>
        <div class="sub">
          <span class="st ${m.state}">${m.state}</span>
          ${m.kind === 'builtin-update' ? html`<span class="st" title="a newer xbind ships a newer version of this builtin — closing merged completes the update tracking">builtin update</span>` : nothing}
          <span>from ${from}</span>
          <span>opened ${relTime(m.created)}</span>
          ${m.base ? html`<span title="the target HEAD the series was formatted against">base ${m.base.slice(0, 8)}</span>` : nothing}
          ${s ? html`<span class="stat"><span class="pl">+${s.add}</span> <span class="mi">−${s.del}</span> · ${s.files} file${s.files === 1 ? '' : 's'}</span>` : nothing}
        </div>
      </div>
      ${m.message ? html`<div class="msg">${m.message}</div>` : nothing}
      ${m.state === 'open' ? html`
        <div class="apply">
          <span class="warn">review the diff first — a proposal is untrusted input.</span>
          apply in this tile's terminal:
          <code>bx code pr fetch ${m.number} | git am --3way</code>
          then mark merged (or reject with a note the author's agent will read).
        </div>` : nothing}
      ${this._err ? html`<div class="err">${this._err}</div>` : nothing}
      ${this._series == null ? html`<span class="muted">loading diff…</span>`
        : html`<pre class="diff">${unsafeHTML(diffHTML(this._series))}</pre>`}
      ${m.events?.length ? html`
        <div class="thread">
          ${m.events.map((e) => html`
            <div class="ev ${e.type}">
              <div class="who">${e.who} · ${relTime(e.ts)}</div>
              <div class="body">${e.type === 'state' ? `→ ${e.state}${e.body ? ': ' + e.body : ''}` : e.body}</div>
            </div>`)}
        </div>` : nothing}
      ${m.state === 'open' ? html`
        <div class="actions">
          <input placeholder="comment for the author…" .value=${this._comment}
                 @input=${(e) => { this._comment = e.target.value; }}
                 @keydown=${(e) => { if (e.key === 'Enter') this._postComment(); }}>
          <button ?disabled=${this._busy || !this._comment.trim()} @click=${this._postComment}>Comment</button>
          <button class="ok" ?disabled=${this._busy} title="record that the series was applied (run git am in the terminal first)"
                  @click=${() => this._close('merged')}>✓ Merged</button>
          <button class="no" ?disabled=${this._busy} title="refuse the proposal, with a note explaining why"
                  @click=${() => this._close('rejected')}>✕ Reject</button>
        </div>` : nothing}`;
  }

  render() {
    return html`
      <div class="side">${this._list()}</div>
      <div class="main">${this._main()}</div>`;
  }
}
customElements.define('bx-prs', BxPrs);
