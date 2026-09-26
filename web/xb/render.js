/**
 * xb/render.js — the Lit reference renderer of the native vocabulary.
 *
 * Draws a native tree (native/spec/tree.md; the vocabulary is xb/vocab.js)
 * as a phone UI in the browser, the way the xbin app draws it with SwiftUI —
 * for previews (`bx preview --native`, the runtime document with
 * `?preview=1`), fixture screenshots and tests. It is never used by the web
 * shell, and a tile never imports it.
 *
 *   import '/vendor/xb/render.js';           // defines <xb-view> and xb-*
 *   const view = document.createElement('xb-view');
 *   view.theme = 'dark';                      // '' follows prefers-color-scheme
 *   view.text = 'large';                      // iOS xxxLarge type scale
 *   view.onevent = (k, type, payload, n) => xbn.event(k, type, payload, n);
 *   view.apply(msg);                          // {op:'mount'|'patch'} from the runtime
 *   view.setTree({v: 1, root});               // or a whole tree (fixtures)
 *
 * The view keeps its own copy of the tree ("what the app shows"): a user
 * change to a controlled prop (a field's value, a toggle, a sheet's open…)
 * is written into that copy and reported with the event that carries it
 * (tree.md §6), so an unchanged re-render needs no patch. Renderer-owned
 * state (an unbound disclosure's open, an uncontrolled tab) lives beside it,
 * per node key. Every node is drawn as an xb-<primitive> element carrying
 * data-k=<key>, so tests can find and drive it.
 *
 * Other hooks: view.oncopy(text) (default: the clipboard), view.loadImage(src)
 * → URL (default: src resolved against the document), view.onupload(node,
 * file) (composer attachments; default: none).
 */
import { LitElement, html, css, nothing, repeat } from '/vendor/lit-all.min.js';
import { applyOps, cloneJSON } from '/vendor/xb/rt-diff.js';
import { VOCAB } from '/vendor/xb/vocab.js';
import { TOKENS, TYPE_CLASSES } from '/vendor/xb/render-theme.js';
import { own, cls, icon, str } from '/vendor/xb/render-base.js';
import { STRUCTURE, STRUCTURE_CSS } from '/vendor/xb/render-structure.js';
import { CONTENT, CONTENT_CSS } from '/vendor/xb/render-content.js';
import { CONTROLS, CONTROLS_CSS, overlays, OVERLAY_CSS } from '/vendor/xb/render-controls.js';
import { CHAT, CHAT_CSS } from '/vendor/xb/render-chat.js';
import '/vendor/xb/render-chart.js';

const R = { ...STRUCTURE, ...CONTENT, ...CONTROLS, ...CHAT };

// Every primitive is an xb-<name> element (a plain custom element — the
// view's shadow root styles them all), so the DOM reads like the tree.
for (const name of [...Object.keys(VOCAB.prims), 'unknown']) {
  const tag = `xb-${name}`;
  if (!customElements.get(tag) && name !== 'chart') customElements.define(tag, class extends HTMLElement {});
}

// xb-more: fires `more` on its host when it scrolls into view (list/transcript paging).
if (!customElements.get('xb-more')) {
  customElements.define('xb-more', class extends HTMLElement {
    connectedCallback() {
      this.io = new IntersectionObserver((es) => {
        if (es.some((e) => e.isIntersecting)) this.dispatchEvent(new CustomEvent('more'));
      });
      this.io.observe(this);
    }
    disconnectedCallback() { this.io?.disconnect(); }
  });
}

class Cx {
  constructor(v, place, extra) { this.v = v; this.place = place; this.x = extra || {}; }
  in(place, extra) { return new Cx(this.v, place ?? this.place, extra ? { ...this.x, ...extra } : this.x); }
  node(n) { return drawNode(n, this); }
  kids(n, place) {
    const c = place ? this.in(place) : this;
    return repeat(n.c || [], (x) => x.k, (x) => drawNode(x, c));
  }
  val(n, prop, d) {
    if (own(n.p, prop)) return n.p[prop];
    const u = this.v.uiOf(n.k);
    return own(u, prop) ? u[prop] : d;
  }
  on(n, type) { return Array.isArray(n.e) && n.e.includes(type); }
  emit(n, type, payload) { this.v.emit(n, type, payload); }
  ui(k) { return this.v.uiOf(k); }
  update() { this.v.requestUpdate(); }
}

function drawNode(n, cx) {
  if (!n || typeof n !== 'object') return nothing;
  const f = R[n.t];
  if (f) return f(n, cx);
  return html`<xb-unknown data-k=${n.k} class=${cls(cx.place === 'group' && 'cell')}>
    ${icon('ui-unknown')}<span>${str(n.t)}</span></xb-unknown>`;
}

// Roots that lay themselves out over the whole view; anything else gets a
// scrolling body with margins.
const FULL = new Set(['screen', 'nav', 'fragment', 'split', 'tabs', 'sheet', 'transcript']);

const BASE_CSS = css`
  :host {
    display: block; position: relative; height: 100%; overflow: hidden;
    background: var(--xb-bg); color: var(--xb-text);
    font: var(--xb-font-body); letter-spacing: -0.2px;
    -webkit-font-smoothing: antialiased; -webkit-tap-highlight-color: transparent;
    text-size-adjust: 100%;
  }
  * { box-sizing: border-box; }
  .root { position: absolute; inset: 0; contain: layout paint; display: flex; flex-direction: column; }
  .root > * { flex: 1 1 auto; min-height: 0; }
  .loose { overflow: auto; padding: 16px var(--xb-margin) 32px; display: flex; flex-direction: column; gap: 12px; }
  .loose > * { flex-shrink: 0; }
  pre { tab-size: 4; }
  button { font: inherit; color: inherit; letter-spacing: inherit; background: none; border: 0; padding: 0; margin: 0; cursor: pointer; }
  button:disabled { cursor: default; }
  button:focus-visible, input:focus-visible, textarea:focus-visible, select:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--xb-accent) 70%, transparent); outline-offset: 1px;
  }
  input, textarea, select { font: inherit; color: inherit; letter-spacing: inherit; }
  .ic { width: var(--xb-icon); height: var(--xb-icon); flex: none; display: block; }
  .ic-unknown { opacity: 0.5; }
  .spin { width: 20px; height: 20px; flex: none; animation: xb-spin 1s steps(12) infinite; color: var(--xb-muted); }
  @keyframes xb-spin { to { transform: rotate(360deg); } }
  xb-unknown { display: flex; align-items: center; gap: 8px; padding: 10px 12px; color: var(--xb-muted);
    border: 1px dashed var(--xb-border); border-radius: 8px; font: var(--xb-font-footnote); }
  xb-unknown.cell { border: 0; border-radius: 0; padding: 11px 16px; }
  xb-fragment { display: contents; }
  [hidden] { display: none !important; }
`;

export class XbView extends LitElement {
  static properties = {
    theme: { reflect: true },
    text: { reflect: true },
  };

  static styles = [TOKENS, TYPE_CLASSES, BASE_CSS, STRUCTURE_CSS, CONTENT_CSS, CONTROLS_CSS, CHAT_CSS, OVERLAY_CSS];

  constructor() {
    super();
    this.theme = '';
    this.text = '';
    this.root = null; // the tree the view shows (its own copy)
    this.n = undefined; // n of the last applied mount/patch
    this.onevent = null;
    this.oncopy = null;
    this.onupload = null;
    this.loadImage = null;
    this.events = []; // every event the view sent: [k, type, payload, n]
    this._ui = new Map();
    this._images = new Map();
    this._overlay = null; // {kind: 'confirm'|'menu'|'image', …}
    this._shown = new Set();
  }

  // ── input ───────────────────────────────────────────────────────────────
  setTree(tree) {
    const root = tree && tree.root ? tree.root : tree;
    this.root = root ? cloneJSON(root) : null;
    this.n = undefined;
    this._changed();
  }

  // apply(msg): a runtime message. mount/patch change the tree (true);
  // everything else is the host's business (false).
  apply(msg) {
    if (!msg || typeof msg !== 'object') return false;
    if (msg.op === 'mount') {
      this.root = cloneJSON(msg.root);
    } else if (msg.op === 'patch') {
      if (!this.root) return false;
      try { this.root = applyOps(this.root, cloneJSON(msg.ops)); } catch (e) {
        console.error('[xb-view] patch failed; asking for a remount', e);
        this.dispatchEvent(new CustomEvent('xb-remount'));
        return false;
      }
    } else return false;
    if (typeof msg.n === 'number') this.n = msg.n;
    this._changed();
    return true;
  }

  get tree() { return this.root ? { v: 1, root: cloneJSON(this.root) } : null; }

  _changed() {
    const live = new Set();
    const walk = (x) => { live.add(x.k); for (const c of x.c || []) walk(c); };
    if (this.root) walk(this.root);
    for (const k of this._ui.keys()) if (!live.has(k)) this._ui.delete(k);
    this.requestUpdate();
  }

  // ── state and events ────────────────────────────────────────────────────
  uiOf(k) {
    let u = this._ui.get(k);
    if (!u) { u = {}; this._ui.set(k, u); }
    return u;
  }

  // emit(n, type, payload): the user acted on node n. When the event reports
  // a prop (vocab `reports`), the view first shows the new value: bound props
  // are updated in the tree copy (and the event always goes out, so the
  // runtime's shadow stays true), unbound ones are the renderer's own. Other
  // events go out only when the tile listens.
  emit(n, type, payload = {}) {
    const rep = VOCAB.prims[n.t]?.events?.[type]?.reports;
    let bound = false;
    if (rep) {
      const v = own(rep, 'value') ? rep.value : payload[rep.from || rep.prop];
      if (v !== undefined) {
        if (own(n.p, rep.prop)) { n.p[rep.prop] = cloneJSON(v); bound = true; }
        else this.uiOf(n.k)[rep.prop] = cloneJSON(v);
      }
    }
    this.requestUpdate();
    if (!bound && !(Array.isArray(n.e) && n.e.includes(type))) return false;
    const pl = cloneJSON(payload);
    this.events.push([n.k, type, pl, this.n]);
    if (this.events.length > 1000) this.events.shift();
    if (typeof this.onevent === 'function') this.onevent(n.k, type, pl, this.n);
    this.dispatchEvent(new CustomEvent('xb-event', { detail: { k: n.k, type, payload: pl, n: this.n } }));
    return true;
  }

  // ── services for primitives ─────────────────────────────────────────────
  async copy(text) {
    if (typeof this.oncopy === 'function') return this.oncopy(String(text));
    try { await navigator.clipboard.writeText(String(text)); return true; } catch { return false; }
  }

  // confirm({title, message, label, destructive}) → Promise<bool>: the
  // confirmation dialog a button's `confirm` shows before its tap.
  confirm(opts) {
    return new Promise((resolve) => {
      this._overlay = { kind: 'confirm', opts: opts || {}, resolve };
      this.requestUpdate();
    });
  }

  openMenu(n, anchor) {
    const r = anchor.getBoundingClientRect();
    const me = this.getBoundingClientRect();
    this._overlay = { kind: 'menu', n, at: { top: r.bottom - me.top + 6, left: r.left - me.left, right: me.right - r.right } };
    this.requestUpdate();
  }

  closeOverlay(result) {
    const o = this._overlay;
    this._overlay = null;
    if (o?.resolve) o.resolve(result);
    this.requestUpdate();
  }

  // image(src) → a URL to draw, or '' while it loads.
  image(src) {
    src = str(src);
    if (!src) return '';
    if (src.startsWith('data:')) return src;
    const have = this._images.get(src);
    if (typeof have === 'string') return have;
    if (!have) {
      if (typeof this.loadImage !== 'function') {
        try { return new URL(src, document.baseURI).href; } catch { return ''; }
      }
      const p = Promise.resolve(this.loadImage(src)).then((u) => {
        this._images.set(src, u || '');
        this.requestUpdate();
      }, () => this._images.set(src, ''));
      this._images.set(src, p);
    }
    return '';
  }

  showImage(url, alt) { this._overlay = { kind: 'image', url, alt }; this.requestUpdate(); }

  // screens report `appear` once each time they come on screen
  shown(n, cx) { if (cx.on(n, 'appear')) this._shownNow.add(n); }

  // ── drawing ─────────────────────────────────────────────────────────────
  render() {
    this._shownNow = new Set();
    const r = this.root;
    const cx = new Cx(this, 'free', {});
    const body = !r ? nothing : FULL.has(r.t) ? cx.node(r) : html`<div class="loose">${cx.node(r)}</div>`;
    return html`<div class="root" part="root">${body}</div>${overlays(this)}`;
  }

  updated() {
    // transcripts with `follow` stay at the bottom while the user is there
    for (const el of this.renderRoot.querySelectorAll('xb-transcript.follow')) {
      if (this.uiOf(el.dataset.k).atBottom !== false) el.scrollTop = el.scrollHeight;
    }
    const now = new Set([...this._shownNow].map((n) => n.k));
    for (const n of this._shownNow) if (!this._shown.has(n.k)) this.emit(n, 'appear', {});
    this._shown = now;
  }

  // find(k): the node with key k in the view's tree, or null.
  find(k) {
    const walk = (x) => { if (x.k === k) return x; for (const c of x.c || []) { const f = walk(c); if (f) return f; } return null; };
    return this.root ? walk(this.root) : null;
  }

  cx(place = 'free') { return new Cx(this, place, {}); }
}

if (!customElements.get('xb-view')) customElements.define('xb-view', XbView);


