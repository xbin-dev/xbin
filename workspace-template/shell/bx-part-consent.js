/**
 * <bx-part-consent> — the shell's consent prompts for partitioned tiles
 * (docs/partitions.md §Calls between partitioned tiles; 06 §12.3). Only
 * while the workspace policy partitionConsent is on: when a partitioned
 * tile's call into the signed-in person's data in another partitioned tile
 * was refused for want of their consent, xbind asks them (a `partitions`
 * event, op consent-needed, to their own sockets), and this panel says so —
 * Allow, or Don't allow. It sits beside <bx-grants> in the shell's decision
 * strip and hides (no box, no gap) when there is nothing to answer.
 *
 * The words and calls are partition-mode.js's (hack/partition-mode.test.mjs).
 * Every call is the person's own raw fetch — never the shell tile's
 * xbin.fetch, which xbind refuses here (PersonOnly). It reads the consents
 * only for a signed-in person (never view-as, never the workspace token)
 * who sees a partitioned tile (consentWatch), or when a consent event
 * arrives for them; an xbind without the consents API answers 404, and the
 * panel stays empty. Allow is live only while nothing covers the ask (a
 * tile's pop-out window is placed where the tile says, above the strip).
 *
 * Properties: components (/components rows: which tiles are partitioned,
 * and which still exist), who (/whoami), tile (only the asks that tile
 * made, `from` — the shell puts one on each tile's window, D195).
 *
 * The consents view and this browser's dismissals are the page's, one
 * read for every instance (consentContext / consentAsks / onConsents): the
 * shell sets the context and counts each tile's asks for the sidebar's
 * flags even when no instance is drawn.
 */
import { LitElement, html, css, nothing, repeat } from 'lit';
import { onEvent, onReconnect } from '/vendor/events-socket.js';
import '/vendor/bx-icons.js';
import { baseCss } from './shell-css.js';
import { consentPerson, consentWatch, consentPrompts, keepDismissed, consentEventOp, consentCall, consentKey, consentAsk, consentWhy,
  consentAllowTitle, allowedText, declinedText, errorText, consentStoreKey, coverPoints, CONSENT_HEAD, CONSENT_REGION, CONSENT_DENY_TITLE,
  CONSENT_COVERED, PARTITIONS_PAGE } from './partition-mode.js';

// This browser's dismissed asks of the signed-in person ({"from→to": at},
// under consentStoreKey(who) — one key per person): a per-viewer
// convenience — storage off, a dismissed ask shows again on the next load.
const readDismissed = (key) => {
  if (!key) return {};
  try { const v = JSON.parse(localStorage.getItem(key) || '{}'); return v && typeof v === 'object' ? v : {}; } catch { return {}; }
};
const writeDismissed = (key, v) => {
  if (!key) return;
  try {
    if (Object.keys(v).length) localStorage.setItem(key, JSON.stringify(v)); else localStorage.removeItem(key);
  } catch { /* storage off: this page only */ }
};
const DONE_MS = 15000; // how long an answer's words stay

// ---- the page's consents (one read for every instance and the shell) ----
const st = { view: null, seq: 0, read: false, started: false, who: null, components: [], key: '', dismissed: {}, subs: new Set() };
const emit = () => { for (const f of st.subs) f(); };
const watching = () => consentWatch(st.components, st.who) || !!st.view;
async function loadConsents() {
  const seq = ++st.seq;
  const res = await consentCall((u, i) => fetch(u, i), 'GET');
  if (seq !== st.seq) return;
  st.view = res.ok ? res.body : null;
  if (res.ok) {
    const d = keepDismissed(st.dismissed, res.body);
    if (d !== st.dismissed) { st.dismissed = d; writeDismissed(st.key, d); }
  }
  emit();
}
function startConsents() {
  if (st.started) return;
  st.started = true;
  onEvent((e) => {
    if (!consentPerson(st.who)) return; // view-as and the workspace token have no consents: never asked
    if (consentEventOp(e)) loadConsents(); // asked, or answered elsewhere (bx, another tab)
    else if (e?.type === 'policies' && watching()) loadConsents();
  });
  onReconnect(() => { if (watching()) loadConsents(); });
  window.addEventListener('storage', (e) => { if (st.key && e.key === st.key) { st.dismissed = readDismissed(st.key); emit(); } });
}
// consentContext(components, who): what the page knows — the first read
// once the viewer is a person who sees a partitioned tile; after it only
// events read again (an xbind without the API isn't asked at every
// /components change).
export function consentContext(components, who) {
  startConsents();
  st.components = Array.isArray(components) ? components : [];
  if (who !== st.who) {
    st.who = who;
    if (consentStoreKey(who) !== st.key) { st.key = consentStoreKey(who); st.dismissed = readDismissed(st.key); emit(); }
  }
  if (!st.read && consentWatch(st.components, st.who)) { st.read = true; loadConsents(); }
}
// consentAsks() → the asks still to answer (not dismissed here, their tiles still there).
export function consentAsks() {
  const paths = st.components.length ? new Set(st.components.map((c) => c.path)) : null;
  return consentPrompts(st.view, { dismissed: st.dismissed, paths });
}
// onConsents(f): f() whenever the asks may have changed; → unsubscribe
export function onConsents(f) { st.subs.add(f); return () => st.subs.delete(f); }
// Allow counts only once nothing has covered the asks for ARM_MS: the
// panel is hit-tested every COVER_MS while it asks, and at the click — a
// tile's pop-out window (tile-placed, above the strip) could hide the
// question, or close the instant the pointer leaves it for Allow.
const ARM_MS = 600, COVER_MS = 200;

export class BxPartConsent extends LitElement {
  static properties = {
    components: { attribute: false },
    who: { attribute: false },
    tile: { type: String, reflect: true }, // only this tile's asks (D195)
    _rows: { state: true },      // key → {busy, err} of an ask being answered
    _done: { state: true },      // [{key, text}] answers still showing
    _armed: { state: true },     // nothing has covered the asks for ARM_MS: Allow is live
  };

  static styles = [baseCss, css`
    :host { display: block; font: var(--bx-font, 13px/18px system-ui, sans-serif); color: var(--bx-text, #E9EAF0);
      --bx-part-c: var(--bx-part, #3FB5A3); }
    :host([hidden]) { display: none; } /* nothing to answer: no box, so the strip's gap doesn't move the canvas */
    /* the partition marker's hue as the panel's 3 px rule: what the asks are about */
    .panel { background: var(--bx-panel, #1F2028); border: 1px solid var(--bx-border, #33353F);
      border-left: 3px solid var(--bx-part-c); border-radius: var(--bx-radius, 2px); padding: 8px 12px; }
    :host([tile]) .panel { background: transparent; border: 0; border-left: 3px solid var(--bx-part-c); padding: 0 0 0 10px; }
    h4 { margin: 0 0 4px; font: var(--bx-font-micro, 600 11px/14px system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase;
      color: var(--bx-muted, #A3A6B6); }
    .ask { padding: 4px 0; }
    .ask + .ask, .ask + .done, .done + .done { border-top: 1px solid var(--bx-border, #33353F); }
    .line { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
    .q { flex: 1; min-width: 12em; font-weight: 600; overflow-wrap: anywhere; }
    .why { margin: 2px 0 0; color: var(--bx-muted, #A3A6B6); }
    .err { display: flex; align-items: flex-start; gap: 6px; margin: 4px 0 0; color: var(--bx-danger, #FF7A7A); white-space: pre-wrap; }
    .done { display: flex; align-items: flex-start; gap: 8px; padding: 4px 0; color: var(--bx-muted, #A3A6B6); }
    .done span { flex: 1; overflow-wrap: anywhere; }
    .done a { color: var(--bx-link, #8C9BFF); }
    button { box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 12px; cursor: pointer; font-weight: 600;
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
      background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); }
    button.allow { color: var(--bx-part-c); border-color: var(--bx-part-c); }
    button:hover:not(:disabled) { background: var(--bx-hover, #2A2B34); }
    button:disabled { opacity: 0.5; cursor: default; }
    button.x { display: inline-flex; align-items: center; justify-content: center; width: 28px; padding: 0; background: none; border: 0; color: var(--bx-muted, #A3A6B6); }
    button.x:hover:not(:disabled) { background: var(--bx-close-hover, #FF7A7A); color: var(--bx-close-hover-ink, #0B0C12); }
  `];

  constructor() {
    super();
    this.components = []; this.who = null; this.tile = '';
    this._rows = {}; this._done = [];
    this._coveredAt = 0; this._armed = false;
  }

  connectedCallback() {
    super.connectedCallback();
    this.hidden = true; // until there is something to answer
    this._offSt = onConsents(() => this.requestUpdate());
  }
  disconnectedCallback() {
    super.disconnectedCallback();
    this._offSt?.();
    for (const d of this._done) clearTimeout(d.timer);
    this._done = [];
    this._watchCover(false);
  }
  willUpdate(changed) {
    if (changed.has('components') || changed.has('who')) consentContext(this.components, this.who);
  }
  // The host hides while there is nothing to answer, and the asks are
  // watched for cover.
  updated() {
    const asks = this.renderRoot.querySelectorAll('.ask').length;
    this.hidden = !asks && !this._done.length;
    this._watchCover(asks > 0);
  }

  // _obscured() → whether anything is drawn over part of an ask — its
  // question, why, or buttons — or part of it is out of view: every
  // coverPoints point must hit this element (a hit inside the panel is
  // retargeted to it; a pop-out window, a float tile or a menu isn't).
  _obscured() {
    const root = this.getRootNode();
    if (typeof root?.elementFromPoint !== 'function') return false;
    const W = document.documentElement.clientWidth, H = document.documentElement.clientHeight;
    for (const el of this.renderRoot.querySelectorAll('.ask')) {
      const r = el.getBoundingClientRect();
      if (!r.width || !r.height || r.left < 0 || r.top < 0 || r.right > W || r.bottom > H) return true;
      if (coverPoints(r).some(([x, y]) => root.elementFromPoint(x, y) !== this)) return true;
    }
    return false;
  }
  // While asks show, a timer hit-tests them: Allow is disabled until
  // nothing has covered them for ARM_MS (a new panel too: an ask that
  // appears under a click meant for something else doesn't take it).
  _watchCover(on) {
    if (!on) {
      clearInterval(this._coverTimer);
      this._coverTimer = 0;
      if (this._armed) this._armed = false;
      return;
    }
    if (this._coverTimer) return;
    this._coveredAt = Date.now();
    this._coverTimer = setInterval(() => this._checkCover(), COVER_MS);
  }
  // _checkCover() → whether Allow is live now (and says so to render).
  _checkCover() {
    const now = Date.now();
    if (this._obscured()) this._coveredAt = now;
    const armed = now - this._coveredAt >= ARM_MS;
    if (armed !== this._armed) this._armed = armed;
    return armed;
  }

  _say(key, text, link = '') {
    const d = { key, text, link };
    d.timer = setTimeout(() => this._drop(d), DONE_MS);
    this._done = [...this._done.filter((x) => x.key !== key), d];
  }
  _drop(d) { clearTimeout(d.timer); this._done = this._done.filter((x) => x !== d); }
  _setRow(key, st) { this._rows = { ...this._rows, [key]: st }; }

  async _allow(a) {
    // nothing may cover the question now, nor have covered it a moment
    // ago (the timer's last look may be up to COVER_MS old)
    if (!this._checkCover()) {
      this._setRow(a.key, { err: CONSENT_COVERED });
      return;
    }
    this._setRow(a.key, { busy: true });
    const res = await consentCall((u, i) => fetch(u, i), 'POST', a);
    if (res.ok) {
      this._setRow(a.key, null);
      this._say(a.key, allowedText(a.from, a.to), PARTITIONS_PAGE);
    } else {
      this._setRow(a.key, { err: errorText(res) });
    }
    loadConsents();
  }
  _decline(a) {
    st.dismissed = { ...st.dismissed, [a.key]: a.at };
    writeDismissed(st.key, st.dismissed);
    emit();
    this._setRow(a.key, null);
    this._say(a.key, declinedText(a.from, a.to));
  }

  render() {
    const asks = consentAsks().filter((a) => (!this.tile || a.from === this.tile) && !this._done.some((d) => d.key === a.key));
    if (!asks.length && !this._done.length) return nothing;
    return html`<div class="panel" role="region" aria-label=${CONSENT_REGION} data-consents=${asks.length}>
      ${asks.length ? html`<h4>${CONSENT_HEAD}</h4>` : nothing}
      ${repeat(asks, (a) => a.key, (a) => {
        const st = this._rows[a.key] ?? {};
        return html`<div class="ask" data-consent=${consentKey(a)}>
          <div class="line">
            <span class="q">${consentAsk(a.from, a.to)}</span>
            <button class="deny" ?disabled=${!!st.busy} title=${CONSENT_DENY_TITLE} @click=${() => this._decline(a)}>Don't allow</button>
            <button class="allow" ?disabled=${!!st.busy || !this._armed} title=${consentAllowTitle(a.from, a.to)} @click=${() => this._allow(a)}>${st.busy ? 'Allowing…' : 'Allow'}</button>
          </div>
          <p class="why">${consentWhy(a.from, a.to)}</p>
          ${st.err ? html`<p class="err"><bx-icon name="error" label="Error"></bx-icon><span>${st.err}</span></p>` : nothing}
        </div>`;
      })}
      ${repeat(this._done, (d) => d.key, (d) => html`<div class="done" data-consent-done=${d.key} role="status">
        <span>${d.text}${d.link ? html` <a href=${d.link} target="_blank" rel="noopener">open it</a>` : nothing}</span><button class="x" title="close" aria-label="close" @click=${() => this._drop(d)}><bx-icon name="xmark"></bx-icon></button></div>`)}
    </div>`;
  }
}

customElements.define('bx-part-consent', BxPartConsent);
