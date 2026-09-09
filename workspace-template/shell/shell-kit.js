// shell/shell-kit.js — what the shell and its child elements share: the
// grid module the layout is measured in, the runtime colour dots, the
// touch long-press gesture, the shell-text selection check and the ⇄
// change-proposal badge. Imported relatively by bx-shell.js and
// bx-canvas.js; nothing here touches element state.
import { html, nothing } from 'lit';

// Fixed snappable grid. Tiles are absolutely positioned + sized in multiples of
// GRID px, so resizing the browser window never reflows them, and a tile's own
// content can't stretch it (fixed size — the frame scrolls inside). GAP is the
// gutter drawn between neighbouring tiles. Tiles must be usable at MIN_W with no
// horizontal scroll (see AGENTS.md).
export const GRID = 48;
export const GAP = 8;
export const DEF_W = 12 * GRID; // default new-tile size: 576×384
export const DEF_H = 8 * GRID;
export const MIN_W = 4 * GRID; // resize floor: 192×144
export const MIN_H = 3 * GRID;
export const snap = (v) => Math.max(0, Math.round(v / GRID) * GRID);

// The dot before a tile's name: its runtime.
export const RUNTIME_COLOR = {
  '': 'var(--bx-muted, #868f9a)',
  static: 'var(--bx-muted, #868f9a)',
  go: 'var(--bx-accent, #f5a623)',
  node: 'var(--bx-green, #4caf50)',
  python: 'var(--bx-amber, #f2a71b)',
  cgi: 'var(--bx-red, #ef5350)',
};

// Long-press (touch/pen, phones only — the mouse keeps right-click): hold
// ~450 ms without moving 8px to open the menu the right-click would. A
// scroll cancels (pointercancel), and the menu's backdrop ignores the
// press's own pointerup/click.
export class LongPress {
  start(e, fire, mobile) {
    if (!mobile || e.pointerType === 'mouse' || e.button !== 0) return;
    this.cancel();
    const x = e.clientX, y = e.clientY;
    this._p = { x, y, t: setTimeout(() => { this._p = null; fire(); }, 450) };
  }
  move(e) { if (this._p && Math.hypot(e.clientX - this._p.x, e.clientY - this._p.y) > 8) this.cancel(); }
  cancel() { if (this._p) { clearTimeout(this._p.t); this._p = null; } }
}

// Selected text in the shell's own document — toString() of the document
// selection (Chromium and Firefox both read it through nested shadow
// roots for real, selectable text) or of Chromium's non-standard
// ShadowRoot.getSelection(). Not getComposedRanges: it also reports ranges
// over user-select:none chrome (card heads, sidebar rows — which no person
// can select) and retargets nested-shadow ranges to the host, so a caret
// left by a click read as "text selected" and swallowed every right-click.
export function selectedText(root) {
  for (const r of [root, document]) {
    const t = r?.getSelection?.()?.toString?.() ?? '';
    if (t.trim()) return t;
  }
  return '';
}

// The ⇄ badge: n open change proposals; with onOpen it is the button that
// opens the tile's proposals panel (card heads), without it a plain marker
// (sidebar rows).
export function prBadge(n, onOpen = null) {
  if (!n) return nothing;
  const title = `${n} open change proposal${n === 1 ? '' : 's'} — review in the tile's terminal window (⇄ tab)`;
  return onOpen
    ? html`<button class="prb" title=${title}
                   @pointerdown=${(e) => e.stopPropagation()}
                   @click=${(e) => { e.stopPropagation(); onOpen(); }}>⇄${n}</button>`
    : html`<span class="prb" title=${title}>⇄${n}</span>`;
}
