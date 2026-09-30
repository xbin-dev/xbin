/**
 * <bx-part-consent> — the shell's consent prompts for partitioned tiles
 * (docs/partitions.md §Calls between partitioned tiles; 06 §12.3). Only
 * while the workspace policy partitionConsent is on: when a partitioned
 * tile's call into the signed-in person's data in another partitioned tile
 * was refused for want of their consent, xbind asks them (a `partitions`
 * event, op consent-needed, to their own sockets), and this panel says so —
 * Allow, or Don't allow. It sits beside <bx-grants> in the shell's decision
 * strip and renders nothing when there is nothing to answer.
 *
 * The words and calls are partition-mode.js's (hack/partition-mode.test.mjs).
 * Every call is the person's own raw fetch — never the shell tile's
 * xbin.fetch, which xbind refuses here (PersonOnly). It reads the consents
 * only for a signed-in person who sees a partitioned tile (consentWatch),
 * or when a consent event arrives; an xbind without the consents API
 * answers 404, and the panel stays empty.
 *
 * Properties: components (/components rows: which tiles are partitioned,
 * and which still exist), who (/whoami).
 */
import { LitElement, html, css, nothing, repeat } from 'lit';
import { onEvent, onReconnect } from '/vendor/events-socket.js';
import { consentWatch, consentPrompts, keepDismissed, consentEventOp, consentCall, consentKey, consentAsk, consentWhy,
  consentAllowTitle, allowedText, declinedText, errorText, CONSENT_HEAD, CONSENT_DENY_TITLE } from './partition-mode.js';

// This browser's dismissed asks ({"from→to": at}): a per-viewer
// convenience — storage off, a dismissed ask shows again on the next load.
const DISMISSED_KEY = 'xbin-partition-consent-dismissed';
const readDismissed = () => {
  try { const v = JSON.parse(localStorage.getItem(DISMISSED_KEY) || '{}'); return v && typeof v === 'object' ? v : {}; } catch { return {}; }
};
const writeDismissed = (v) => {
  try {
    if (Object.keys(v).length) localStorage.setItem(DISMISSED_KEY, JSON.stringify(v)); else localStorage.removeItem(DISMISSED_KEY);
  } catch { /* storage off: this page only */ }
};
const DONE_MS = 15000; // how long an answer's words stay

export class BxPartConsent extends LitElement {
  static properties = {
    components: { attribute: false },
    who: { attribute: false },
    _view: { state: true },      // GET /partitions/consents' answer, null until read (or refused)
    _dismissed: { state: true }, // this browser's dismissed asks
    _rows: { state: true },      // key → {busy, err} of an ask being answered
    _done: { state: true },      // [{key, text}] answers still showing
  };

  static styles = css`
    :host { display: block; font: var(--bx-font, 13px/1.45 system-ui, sans-serif); color: var(--bx-text, #d4d9e0);
      --bx-part-c: var(--bx-part, #3fb5a3); }
    @supports (color: light-dark(#000, #fff)) {
      :host { --bx-part-c: var(--bx-part, light-dark(#1f8778, #3fb5a3)); }
    }
    .panel { background: var(--bx-panel, #23272e); border: 1px solid var(--bx-border, #363c45);
      border-left: 3px solid var(--bx-part-c); border-radius: var(--bx-radius, 6px);
      box-shadow: var(--bx-shadow, 0 1px 2px rgba(0, 0, 0, 0.35)); padding: 8px 12px; }
    h4 { margin: 0 0 4px; font-size: 10.5px; font-weight: 600; letter-spacing: .08em; text-transform: uppercase;
      color: var(--bx-muted, #868f9a); }
    .ask { padding: 4px 0; }
    .ask + .ask, .ask + .done, .done + .done { border-top: 1px solid var(--bx-border, #363c45); }
    .line { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
    .q { flex: 1; min-width: 12em; font-weight: 600; overflow-wrap: anywhere; }
    .why { margin: 2px 0 0; color: var(--bx-muted, #868f9a); font-size: 12px; }
    .err { margin: 4px 0 0; color: var(--bx-red, #ef5350); font-size: 12px; white-space: pre-wrap; }
    .done { display: flex; align-items: flex-start; gap: 8px; padding: 4px 0; font-size: 12px; color: var(--bx-muted, #868f9a); }
    .done span { flex: 1; overflow-wrap: anywhere; }
    button { font: inherit; font-size: 12px; font-weight: 600; border-radius: 5px; padding: 2px 10px; cursor: pointer;
      border: 1px solid var(--bx-border, #363c45); background: var(--bx-panel-2, #2b3038); color: var(--bx-text, #d4d9e0); }
    button.allow { color: var(--bx-part-c); border-color: color-mix(in srgb, var(--bx-part-c) 60%, transparent); }
    button:hover:not(:disabled) { border-color: var(--bx-muted, #868f9a); }
    button:disabled { opacity: .55; cursor: default; }
    button.x { padding: 0 6px; font-weight: 400; background: none; border: 0; color: var(--bx-muted, #868f9a); }
  `;

  constructor() {
    super();
    this.components = []; this.who = null;
    this._view = null; this._dismissed = readDismissed(); this._rows = {}; this._done = [];
    this._seq = 0;
  }

  connectedCallback() {
    super.connectedCallback();
    this._offEv = onEvent((e) => {
      if (consentEventOp(e)) this._load(); // asked, or answered elsewhere (bx, another tab)
      else if (e?.type === 'policies' && this._watching()) this._load();
    });
    this._offRe = onReconnect(() => { if (this._watching()) this._load(); });
    this._onStorage = (e) => { if (e.key === DISMISSED_KEY) this._dismissed = readDismissed(); };
    window.addEventListener('storage', this._onStorage);
  }
  disconnectedCallback() {
    super.disconnectedCallback();
    this._offEv?.(); this._offRe?.();
    window.removeEventListener('storage', this._onStorage);
    for (const d of this._done) clearTimeout(d.timer);
  }
  _watching() { return consentWatch(this.components, this.who) || !!this._view; }
  // The first read, once the viewer is known to be a person who sees a
  // partitioned tile; after it, only events read again (an xbind without
  // the API isn't asked at every /components change).
  updated(changed) {
    if ((changed.has('components') || changed.has('who')) && !this._read && consentWatch(this.components, this.who)) {
      this._read = true;
      this._load();
    }
  }

  // Read the person's consents view; the latest answer wins.
  async _load() {
    const seq = ++this._seq;
    const res = await consentCall((u, i) => fetch(u, i), 'GET');
    if (seq !== this._seq) return;
    this._view = res.ok ? res.body : null;
    if (res.ok) {
      const d = keepDismissed(this._dismissed, res.body);
      if (d !== this._dismissed) { this._dismissed = d; writeDismissed(d); }
    }
  }

  _say(key, text) {
    const d = { key, text };
    d.timer = setTimeout(() => this._drop(d), DONE_MS);
    this._done = [...this._done.filter((x) => x.key !== key), d];
  }
  _drop(d) { clearTimeout(d.timer); this._done = this._done.filter((x) => x !== d); }
  _setRow(key, st) { this._rows = { ...this._rows, [key]: st }; }

  async _allow(a) {
    this._setRow(a.key, { busy: true });
    const res = await consentCall((u, i) => fetch(u, i), 'POST', a);
    if (res.ok) {
      this._setRow(a.key, null);
      this._say(a.key, allowedText(a.from, a.to));
    } else {
      this._setRow(a.key, { err: errorText(res) });
    }
    this._load();
  }
  _decline(a) {
    this._dismissed = { ...this._dismissed, [a.key]: a.at };
    writeDismissed(this._dismissed);
    this._setRow(a.key, null);
    this._say(a.key, declinedText(a.from, a.to));
  }

  render() {
    const paths = Array.isArray(this.components) && this.components.length ? new Set(this.components.map((c) => c.path)) : null;
    const asks = consentPrompts(this._view, { dismissed: this._dismissed, paths }).filter((a) => !this._done.some((d) => d.key === a.key));
    if (!asks.length && !this._done.length) return nothing;
    return html`<div class="panel" role="region" aria-label=${CONSENT_HEAD} data-consents=${asks.length}>
      <h4>${CONSENT_HEAD}</h4>
      ${repeat(asks, (a) => a.key, (a) => {
        const st = this._rows[a.key] ?? {};
        return html`<div class="ask" data-consent=${consentKey(a)}>
          <div class="line">
            <span class="q">${consentAsk(a.from, a.to)}</span>
            <button class="deny" ?disabled=${!!st.busy} title=${CONSENT_DENY_TITLE} @click=${() => this._decline(a)}>Don't allow</button>
            <button class="allow" ?disabled=${!!st.busy} title=${consentAllowTitle(a.from, a.to)} @click=${() => this._allow(a)}>${st.busy ? 'Allowing…' : 'Allow'}</button>
          </div>
          <p class="why">${consentWhy(a.from, a.to)}</p>
          ${st.err ? html`<p class="err">${st.err}</p>` : nothing}
        </div>`;
      })}
      ${repeat(this._done, (d) => d.key, (d) => html`<div class="done" data-consent-done=${d.key} role="status">
        <span>${d.text}</span><button class="x" title="close" @click=${() => this._drop(d)}>✕</button></div>`)}
    </div>`;
  }
}

customElements.define('bx-part-consent', BxPartConsent);
