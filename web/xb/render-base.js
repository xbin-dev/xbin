/**
 * xb/render-base.js — helpers shared by the reference renderer's modules:
 * icons, class lists, tones, the spinner, and prop access.
 *
 * A primitive renderer is `(n, cx) => TemplateResult`, where `n` is a tree
 * node {k, t, p?, e?, c?} (native/spec/tree.md) and `cx` the render context
 * (xb/render.js):
 *   cx.place            where the node sits: 'screen' (a list/form screen's
 *                       body), 'group' (a cell of an inset group), 'free' (a
 *                       scroll body, sheet, stack…), 'toolbar', 'actions', 'menu'
 *   cx.in(place, extra) a context for children in another place
 *   cx.node(n)          render one node here
 *   cx.kids(n, place?)  render n's children (keyed by k)
 *   cx.val(n, prop, d)  a controlled prop as the renderer shows it: the
 *                       tile's value when bound, else the renderer's own
 *   cx.emit(n, type, payload)  the user acted (see XbView#emit)
 *   cx.on(n, type)      whether the tile listens to `type` on n
 *   cx.ui(k)            renderer-owned per-node state (dropped with the node)
 *   cx.update()         re-render after changing ui state
 *   cx.v                the <xb-view> element (copy, confirm, images, menus)
 */
import { html, svg, nothing, noChange, live, unsafeSVG } from '/vendor/lit-all.min.js';
import { ICON_SVG } from '/vendor/xb/render-icons.js';
import { ICONS } from '/vendor/xb/vocab.js';

export const own = (o, k) => o != null && Object.prototype.hasOwnProperty.call(o, k);
export const P = (n) => n.p || {};
export const kids = (n) => n.c || [];

// cls('a', cond && 'b', …) → "a b"
export const cls = (...xs) => xs.filter(Boolean).join(' ');

const TONES = new Set(['muted', 'accent', 'ok', 'warn', 'danger']);
export const tone = (t) => (TONES.has(t) ? `tone-${t}` : '');
// the colour a tone paints with (accent → the text-safe accent)
export const toneVar = (t) => (t === 'accent' ? 'var(--xb-accent-text)' : TONES.has(t) ? `var(--xb-${t})` : 'var(--xb-muted)');

const known = new Set(Object.keys(ICONS));
// icon(name, cls): an inline SVG. A name outside the vocabulary draws a
// neutral dashed circle (the runtime has already sent a diagnostic).
export function icon(name, c = '') {
  const body = (known.has(name) || String(name).startsWith('ui-')) ? ICON_SVG[name] : undefined;
  return html`<svg class=${cls('ic', c, body === undefined && 'ic-unknown')} viewBox="0 0 24 24" fill="none"
    stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"
    >${unsafeSVG(body ?? ICON_SVG['ui-unknown'])}</svg>`;
}

// The iOS activity indicator: twelve fading spokes.
const SPOKES = Array.from({ length: 12 }, (_, i) => i);
export const spinner = (c = '') => html`<svg class=${cls('spin', c)} viewBox="0 0 24 24" aria-label="loading">${
  SPOKES.map((i) => svg`<rect x="11" y="2" width="2" height="6" rx="1" fill="currentColor"
    opacity=${(0.25 + (0.75 * ((i + 1) / 12))).toFixed(2)} transform=${`rotate(${i * 30} 12 12)`}/>`)}</svg>`;

// str(v): a prop as display text ('' for null/undefined)
export const str = (v) => (v == null ? '' : String(v));

// composing(n, cx): a text control's input-method rules (native/spec/tree.md
// §6, the app's TextInputGate): no `input` while a composition is in
// progress (the reading before its kanji are chosen), one when it commits;
// a value the tile sets meanwhile is never drawn over the text being
// composed — it replaces it when the composition ends (and the composed
// text is not reported).
//   .value(v)            bind the control's value: live(v), or noChange while composing
//   .input, .start, .end the input / compositionstart / compositionend handlers
// Browsers disagree on the order at the end (Chromium: the last input, then
// compositionend; WebKit: compositionend, then an input that is no longer
// composing), so the commit is reported once either way.
export function composing(n, cx) {
  const u = cx.ui(n.k);
  const shown = () => str(own(n.p, 'value') ? n.p.value : u.value);
  return {
    value: (v) => (u.composing ? noChange : live(v)),
    input: (e) => {
      if (e.isComposing || u.composing) return;
      if (u.committed != null && u.committed === e.target.value) { u.committed = null; return; }
      u.committed = null;
      cx.emit(n, 'input', { value: e.target.value });
    },
    start: () => { u.composing = true; u.base = shown(); u.committed = null; },
    end: (e) => {
      u.composing = false;
      const tile = shown();
      if (tile !== u.base) {
        e.target.value = tile; // the tile set it meanwhile: its value now
        u.committed = tile;
        cx.update();
        return;
      }
      u.committed = e.target.value;
      cx.emit(n, 'input', { value: e.target.value });
    },
  };
}

export { html, svg, nothing };
