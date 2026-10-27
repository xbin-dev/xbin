/**
 * pr-panels.js — change proposals ("code PRs", docs/bx.md §code pr) for the
 * tile window (D202): the list as a side panel, one proposal as a tab. What
 * <bx-prs> was, cut in two; <bx-prs> is now these two side by side.
 *
 *   <bx-pr-list target="apps/x" active="3">
 *     the proposals targeting the tile, open ones (or all); a click opens
 *     one as `bx-open-tab` {ref: 'pr:<n>'} (bubbles, composed). After every
 *     load it says `bx-pr-count` {n: the open ones, newest: the newest open
 *     one's number, or null} — the window's badge, and what opens first.
 *   <bx-pr-view target="apps/x" n="3" .ask=${fn}>
 *     one proposal: its head, message, the patch series through bx-code's
 *     diff pipeline, the review thread, and the target side's acts —
 *     Comment, Merged, Reject. Applying is NOT a button: the patch lands
 *     only when the tile's own plane runs `git am` in its terminal, so the
 *     view shows the exact command. Merged and Reject ask for a note through
 *     `.ask` (the window's <bx-dialog>: spec → Promise<{button, values}>),
 *     or, without one, in a line of their own here — never the browser's
 *     prompt().
 *
 * Both read the read-gated /api/xbin/code/pr* endpoints with a raw fetch
 * (the signed-in user) and refresh on the tile's `pr` events.
 */
import { LitElement, html, css, nothing } from 'lit';
import { unsafeHTML } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import { diffHTML, diffStats, codeCss } from '/vendor/bx-code.js';
import { panelCss, listKeys, openTab } from '/vendor/code-tree.js';
import { onEvent } from '/vendor/events-socket.js';
import '/vendor/bx-icons.js';

// a proposal's state as a status badge: its icon, its word (D184: icon + word + colour)
const STATE_ICON = { open: 'info', merged: 'ok', rejected: 'error' };
export const stateBadge = (state) => html`<span class="st ${state}">${STATE_ICON[state] ? html`<bx-icon name=${STATE_ICON[state]}></bx-icon>` : nothing}${state}</span>`;
export const prFrom = (from) => (from?.component ? from.component + (from.user ? ` (${from.user})` : '') : from?.user ? 'user:' + from.user : 'owner');

export function relTimeISO(iso, now = Date.now()) {
  const t = Date.parse(iso || '');
  if (!t) return '—';
  const s = Math.floor((now - t) / 1000);
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

async function prApi(path, body) {
  try {
    const r = await fetch(`/api/xbin/${path}`, body
      ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }
      : undefined);
    const d = await r.json().catch(() => ({}));
    if (!r.ok) return { err: d.error || `error ${r.status}` };
    return { data: d };
  } catch (e) { return { err: String(e.message ?? e) }; }
}

export const badgeCss = css`
  /* a status badge: square, 20px, micro caps, the status icon, word and tint */
  .st { display: inline-flex; align-items: center; gap: 4px; box-sizing: border-box; height: 20px; padding: 0 6px; white-space: nowrap;
    font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase;
    border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); color: var(--bx-muted, #A3A6B6); }
  .st bx-icon { --bx-icon-size: 12px; }
  .st.open { color: var(--bx-info, #A9B4C6); background: var(--bx-info-bg, #30323B); border-color: currentColor; }
  .st.merged { color: var(--bx-ok, #A3CF5E); background: var(--bx-ok-bg, #2F352E); border-color: currentColor; }
  .st.rejected { color: var(--bx-danger, #FF7A7A); background: var(--bx-danger-bg, #3A2B32); border-color: currentColor; }
`;

export class BxPrList extends LitElement {
  static properties = {
    target: { type: String },
    active: { type: Number },   // the open proposal's number: its row is marked
    _prs: { state: true },      // every proposal (all states), or null
    _all: { state: true },      // the filter: open only ↔ everything
    _err: { state: true },
  };
  static styles = [scrollCss, panelCss, badgeCss, css`
    .tabs { display: flex; flex: none; border-bottom: 1px solid var(--bx-border, #33353F); }
    .tabs button { flex: 1; background: none; border: 0; color: var(--bx-muted, #A3A6B6); height: 28px; padding: 0 4px;
      cursor: pointer; border-bottom: 2px solid transparent; }
    .tabs button:hover { color: var(--bx-text, #E9EAF0); }
    .tabs button.on { color: var(--bx-text, #E9EAF0); font-weight: 600; border-bottom-color: var(--bx-accent, #8C9BFF); }
    .pr { display: block; box-sizing: border-box; width: 100%; padding: 7px 10px; border: 0; margin: 0; background: none; text-align: left;
      color: var(--bx-text, #E9EAF0); cursor: pointer; border-bottom: 1px solid var(--bx-border, #33353F); font: inherit; white-space: normal; }
    .pr:hover { background: var(--bx-hover, #2A2B34); }
    .pr.on { background: var(--bx-selection, #262C5C); color: var(--bx-selection-text, #E9EAF0); box-shadow: inset 2px 0 0 var(--bx-accent, #8C9BFF); }
    .pr .t { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
    .pr .m { display: flex; gap: 6px; align-items: center; margin-top: 2px; color: var(--bx-muted, #A3A6B6);
      font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); font-variant-numeric: tabular-nums; }
    .pr .m .st { margin-left: auto; }
  `];

  constructor() { super(); this._prs = null; this._all = false; this._err = ''; }
  connectedCallback() {
    super.connectedCallback();
    this._off = onEvent((e) => { if (e.type === 'pr' && e.component === this.target) this._load(); });
  }
  disconnectedCallback() { this._off?.(); super.disconnectedCallback(); }
  updated(ch) { if (ch.has('target') && this.target) { if (ch.get('target') !== undefined) this._prs = null; this._load(); } }

  async _load() {
    const t = this.target;
    if (!t) return;
    const r = await prApi(`code/prs?target=${encodeURIComponent(t)}`);
    if (t !== this.target) return;
    if (r.err) { this._err = r.err; return; }
    this._err = '';
    this._prs = r.data.prs || [];
    const open = this._prs.filter((p) => p.state === 'open');
    this.dispatchEvent(new CustomEvent('bx-pr-count', { detail: { n: open.length, newest: open.length ? open[0].number : null }, bubbles: true, composed: true }));
  }

  render() {
    const prs = (this._prs || []).filter((p) => this._all || p.state === 'open');
    return html`
      <div class="tabs" role="group" aria-label="which proposals">
        <button class=${!this._all ? 'on' : ''} aria-pressed=${String(!this._all)} @click=${() => { this._all = false; }}>Open</button>
        <button class=${this._all ? 'on' : ''} aria-pressed=${String(this._all)} @click=${() => { this._all = true; }}>All</button>
      </div>
      ${this._err ? html`<span class="err"><bx-icon name="error"></bx-icon>${this._err}</span>` : nothing}
      <div class="list" @keydown=${listKeys}>
        ${prs.map((p) => html`
          <button class="row pr ${Number(this.active) === p.number ? 'on' : ''}" @click=${() => openTab(this, 'pr:' + p.number)}>
            <span class="t">#${p.number} ${p.title}</span>
            <span class="m"><span>${prFrom(p.from)}</span><span>${relTimeISO(p.updated)}</span>${stateBadge(p.state)}</span>
          </button>`)}
        ${prs.length === 0 && !this._err ? html`<span class="muted">${this._prs === null ? 'loading…'
          : this._all ? 'no proposals' : 'no open proposals'}</span>` : nothing}
      </div>`;
  }
}
customElements.define('bx-pr-list', BxPrList);

export class BxPrView extends LitElement {
  static properties = {
    target: { type: String },
    n: { type: Number },
    ask: { attribute: false },  // (spec) → Promise<{button, values}>: the window's dialog
    _pr: { state: true },       // the proposal (with its thread)
    _series: { state: true },   // its raw mbox
    _comment: { state: true },  // the comment draft
    _confirm: { state: true },  // without .ask: 'merged' | 'rejected' being confirmed here
    _note: { state: true },     // … with its note
    _busy: { state: true },
    _err: { state: true },
  };
  static styles = [scrollCss, codeCss, badgeCss, css`
    /* UI text in the UI face; patches, commands and ids in the code face (D184) */
    :host { display: flex; flex-direction: column; height: 100%; min-height: 0; box-sizing: border-box;
      font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif); color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028); }
    button, input { font: inherit; }
    :focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: calc(-1 * var(--bx-focus-width, 3px)); }
    .main { flex: 1; overflow: auto; min-height: 0; }
    .head { position: sticky; top: 0; z-index: 2; background: var(--bx-panel, #1F2028); border-bottom: 1px solid var(--bx-border, #33353F); padding: 8px 12px; }
    .head .ttl { font: var(--bx-font-title, 600 16px/22px "Instrument Sans", system-ui, sans-serif); overflow-wrap: anywhere; }
    .head .sub { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif);
      font-variant-numeric: tabular-nums; display: flex; gap: 8px; flex-wrap: wrap; align-items: center; margin-top: 4px; }
    .head .sub .stat { margin-left: auto; white-space: nowrap; }
    .mono { font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); }
    .msg { padding: 8px 12px; white-space: pre-wrap; border-bottom: 1px solid var(--bx-border, #33353F); }
    .apply { margin: 8px 12px; padding: 8px 12px; background: var(--bx-panel-2, #262730);
      border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); color: var(--bx-muted, #A3A6B6); }
    .apply code { color: var(--bx-text, #E9EAF0); user-select: all; font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); }
    .apply .warn { color: var(--bx-warn, #F2994A); }
    .apply .warn bx-icon { margin-right: 4px; }
    pre, code { font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); }
    pre.diff { margin: 0; padding: 8px 12px; white-space: pre; tab-size: 4; background: var(--bx-code-bg, #16171D); }
    .thread { border-top: 1px solid var(--bx-border, #33353F); padding: 4px 12px 8px; }
    .ev { margin-top: 8px; }
    .ev .who { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
    .ev .body { white-space: pre-wrap; }
    .ev.state .body { color: var(--bx-muted, #A3A6B6); font-style: italic; }
    .actions { flex: none; display: flex; flex-wrap: wrap; gap: 8px; padding: 8px 12px; border-top: 1px solid var(--bx-border, #33353F); background: var(--bx-panel, #1F2028); }
    .actions .what { width: 100%; color: var(--bx-muted, #A3A6B6); }
    .actions input { flex: 1; min-width: 120px; box-sizing: border-box; height: var(--bx-control-h, 28px); padding: 0 8px;
      background: var(--bx-panel, #1F2028); border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); color: var(--bx-text, #E9EAF0); }
    .actions input::placeholder { color: var(--bx-subtle, #8E91A2); opacity: 1; }
    .actions input:focus-visible { outline-offset: var(--bx-focus-offset, 2px); box-shadow: var(--bx-focus-halo, 0 0 0 2px #0B0C12); }
    .actions button { display: inline-flex; align-items: center; gap: 6px; box-sizing: border-box; height: var(--bx-control-h, 28px); padding: 0 11px;
      background: var(--bx-panel, #1F2028); border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
      color: var(--bx-text, #E9EAF0); font-weight: 600; cursor: pointer; white-space: nowrap; }
    .actions button:hover:not([disabled]) { background: var(--bx-hover, #2A2B34); }
    .actions button:focus-visible { outline-offset: var(--bx-focus-offset, 2px); box-shadow: var(--bx-focus-halo, 0 0 0 2px #0B0C12); }
    .actions button.no { color: var(--bx-danger, #FF7A7A); border-color: var(--bx-danger, #FF7A7A); }
    .actions button[disabled] { opacity: .5; cursor: default; }
    .muted { color: var(--bx-muted, #A3A6B6); padding: 12px; display: block; }
    .err { color: var(--bx-danger, #FF7A7A); padding: 8px 12px; }
    .err bx-icon { margin-right: 6px; }
  `];

  constructor() {
    super();
    this._pr = null; this._series = null; this._comment = ''; this._confirm = ''; this._note = ''; this._busy = false; this._err = '';
  }
  connectedCallback() {
    super.connectedCallback();
    this._off = onEvent((e) => { if (e.type === 'pr' && e.component === this.target) this._load(true); });
  }
  disconnectedCallback() { this._off?.(); super.disconnectedCallback(); }
  updated(ch) {
    if (!(ch.has('target') || ch.has('n')) || !this.target || this.n == null) return;
    this._pr = null; this._series = null; this._confirm = '';
    this._load(false);
  }

  // quiet: a `pr` event's refresh — the meta and thread, not the series again
  async _load(quiet) {
    const t = this.target, n = Number(this.n);
    if (!t || !n) return;
    const r = await prApi(`code/pr?target=${encodeURIComponent(t)}&n=${n}`);
    if (t !== this.target || n !== Number(this.n)) return;
    if (r.err) { this._err = r.err; return; }
    this._err = '';
    this._pr = r.data;
    if (quiet && this._series != null) return;
    try {
      const s = await fetch(`/api/xbin/code/pr/series?target=${encodeURIComponent(t)}&n=${n}`);
      if (s.ok && t === this.target && n === Number(this.n)) this._series = await s.text();
    } catch { /* the diff stays "loading" */ }
  }

  async _postComment() {
    const text = this._comment.trim();
    if (!text || this._busy) return;
    this._busy = true;
    const r = await prApi('code/pr/comment', { target: this.target, n: Number(this.n), body: text });
    this._busy = false;
    if (r.err) { this._err = r.err; return; }
    this._comment = ''; this._pr = r.data;
  }

  // Merged / Reject: a note for the author's agent (why; "applied as <sha>")
  async _close(state) {
    if (this._busy) return;
    const verb = state === 'merged' ? 'Mark merged' : 'Reject';
    if (!this.ask) { this._confirm = state; this._note = this._comment.trim(); return; }
    const res = await this.ask({
      title: `${verb} #${this.n}`,
      message: state === 'merged'
        ? 'Record that the series was applied (run git am in the terminal first). The author’s agent reads the note.'
        : 'Refuse the proposal. The author’s agent reads the note.',
      fields: [{ name: 'note', label: 'Note for the author (optional)', type: 'textarea', value: this._comment.trim(), placeholder: state === 'merged' ? 'applied as <sha>' : 'why' }],
      buttons: [{ label: 'Cancel', value: null }, { label: verb, value: 'ok', primary: state === 'merged', danger: state === 'rejected' }],
    });
    if (!res || res.button !== 'ok') return;
    await this._setState(state, res.values?.note ?? '');
  }
  async _setState(state, note) {
    this._busy = true;
    const r = await prApi('code/pr/state', { target: this.target, n: Number(this.n), state, comment: String(note).trim() });
    this._busy = false;
    if (r.err) { this._err = r.err; return; }
    this._confirm = ''; this._comment = ''; this._pr = r.data;
  }

  _actions(m) {
    if (m.state !== 'open') return nothing;
    if (this._confirm) {
      const verb = this._confirm === 'merged' ? 'Mark merged' : 'Reject';
      return html`<div class="actions">
        <span class="what">${verb} #${m.number} — a note for the author (optional):</span>
        <input aria-label="note for the author" .value=${this._note} @input=${(e) => { this._note = e.target.value; }}
               @keydown=${(e) => { if (e.key === 'Enter') this._setState(this._confirm, this._note); if (e.key === 'Escape') this._confirm = ''; }}>
        <button class=${this._confirm === 'rejected' ? 'no' : ''} ?disabled=${this._busy} @click=${() => this._setState(this._confirm, this._note)}>${verb}</button>
        <button ?disabled=${this._busy} @click=${() => { this._confirm = ''; }}>Cancel</button>
      </div>`;
    }
    return html`<div class="actions">
      <input placeholder="comment for the author…" aria-label="comment for the author" .value=${this._comment}
             @input=${(e) => { this._comment = e.target.value; }}
             @keydown=${(e) => { if (e.key === 'Enter') this._postComment(); }}>
      <button ?disabled=${this._busy || !this._comment.trim()} @click=${() => this._postComment()}>Comment</button>
      <button class="ok" ?disabled=${this._busy} title="record that the series was applied (run git am in the terminal first)"
              @click=${() => this._close('merged')}><bx-icon name="check"></bx-icon>Merged</button>
      <button class="no" ?disabled=${this._busy} title="refuse the proposal, with a note explaining why"
              @click=${() => this._close('rejected')}><bx-icon name="xmark"></bx-icon>Reject</button>
    </div>`;
  }

  render() {
    const m = this._pr;
    if (!m) return this._err ? html`<div class="err"><bx-icon name="error"></bx-icon>${this._err}</div>` : html`<span class="muted">loading…</span>`;
    const s = this._series != null ? diffStats(this._series) : null;
    return html`
      <div class="main">
        <div class="head">
          <div class="ttl">#${m.number} ${m.title}</div>
          <div class="sub">
            ${stateBadge(m.state)}
            ${m.kind === 'builtin-update' ? html`<span class="st" title="a newer xbind ships a newer version of this builtin — closing merged completes the update tracking">builtin update</span>` : nothing}
            <span>from ${prFrom(m.from)}</span>
            <span>opened ${relTimeISO(m.created)}</span>
            ${m.base ? html`<span title="the target HEAD the series was formatted against">base <span class="mono">${m.base.slice(0, 8)}</span></span>` : nothing}
            ${s ? html`<span class="stat"><span class="pl">+${s.add}</span> <span class="mi">−${s.del}</span> · ${s.files} file${s.files === 1 ? '' : 's'}</span>` : nothing}
          </div>
        </div>
        ${m.message ? html`<div class="msg">${m.message}</div>` : nothing}
        ${m.state === 'open' ? html`
          <div class="apply">
            <span class="warn"><bx-icon name="warning"></bx-icon>review the diff first — a proposal is untrusted input.</span>
            apply in this tile's terminal:
            <code>bx code pr fetch ${m.number} | git am --3way</code>
            then mark merged (or reject with a note the author's agent will read).
          </div>` : nothing}
        ${this._err ? html`<div class="err"><bx-icon name="error"></bx-icon>${this._err}</div>` : nothing}
        ${this._series == null ? html`<span class="muted">loading diff…</span>` : html`<pre class="diff">${unsafeHTML(diffHTML(this._series))}</pre>`}
        ${m.events?.length ? html`
          <div class="thread">
            ${m.events.map((e) => html`
              <div class="ev ${e.type}">
                <div class="who">${e.who} · ${relTimeISO(e.ts)}</div>
                <div class="body">${e.type === 'state' ? `→ ${e.state}${e.body ? ': ' + e.body : ''}` : e.body}</div>
              </div>`)}
          </div>` : nothing}
      </div>
      ${this._actions(m)}`;
  }
}
customElements.define('bx-pr-view', BxPrView);
