/**
 * frame-titlebar.js — the tile window's title bar (D184 window chrome,
 * D202). `f` is the BxFrame; this reads its state and calls its handlers.
 *
 * Base Two: the window's 28 px title bar — the live square (the frame's
 * buildState), the tile's name and path — and the window's ✕. It drags the
 * window. Nothing else lives here any more (D202): the sessions are tabs in
 * the window's panes, the tools in the strip down its left side, and the
 * session's pickers in the status bar along its bottom (frame-ide-view.js,
 * frame-status.js). The bar is a grid whose last column is the ✕'s own, so
 * however long the path or whatever the window shows, the ✕ stays at the
 * window's top-right corner — before D202 a bar that ran out of room put it
 * wherever its tabs ended.
 *
 * The exports a page may still import from this URL (docs/compat.md) stay:
 * toolsRow draws nothing, fitBar says whether the window is narrow, barKey
 * is constant.
 */
import { html, css, nothing } from 'lit';
import '/vendor/bx-icons.js';

// The live square (product-ui 3): the tile's code is live (the accent),
// building (hollow) or its build failed (a danger outline and the word).
const LIVE = { live: 'live', building: 'building', failed: 'build failed' };
function liveSquare(state) {
  const t = LIVE[state] || LIVE.live;
  return html`<span class="lsq ${state}" role="img" aria-label=${t} title=${t}></span>${state === 'failed'
    ? html`<span class="lfail">failed</span>` : nothing}`;
}

export function titlebar(f) {
  const name = f.src.slice(f.src.lastIndexOf('/') + 1);
  return html`
    <div class="titlebar" @pointerdown=${(e) => { if (!e.target.closest('button')) f._dragStart(e); }}>
      <span class="tname">${liveSquare(f.buildState)}<span class="path" title=${f.src}><b class="nm">${name}</b><span class="dir">${f.src}</span></span></span>
      <button class="winx ib" title="close (sessions keep running)" aria-label="close the window (sessions keep running)"
              @click=${() => { f._termOpen = false; }}><bx-icon name="xmark"></bx-icon></button>
    </div>`;
}

// ---- kept for pages that import them (before D202 the bar degraded) ----
export const toolsRow = () => nothing;
export const fitBar = (f, pop, sheet) => !!sheet || (pop?.offsetWidth ?? 0) < 640;
export const barKey = () => '';

// The bar's styles (adopted by bx-frame alongside its own).
export const titlebarCss = css`
  .titlebar {
    display: grid; grid-template-columns: minmax(0, 1fr) 28px; align-items: center;
    height: var(--bx-titlebar-h, 28px); padding: 0 0 0 10px; box-sizing: border-box; flex: none;
    color: var(--bx-title-text-inactive, #8E91A2); background: var(--bx-titlebar, #1F2028);
    border-bottom: 1px solid var(--bx-border, #33353F);
    user-select: none; cursor: grab; touch-action: none; overflow: hidden;
  }
  .pop.active .titlebar { color: var(--bx-title-text, #E9EAF0); background: var(--bx-titlebar-active, #262730); }
  .titlebar:active { cursor: grabbing; }
  .titlebar .tname { display: flex; align-items: center; min-width: 0; overflow: hidden; }
  /* the live square (product-ui 3) */
  .lsq { flex: none; box-sizing: border-box; width: 8px; height: 8px; margin-right: 8px; background: var(--bx-accent, #8C9BFF); }
  .lsq.building { background: transparent; border: 1px solid var(--bx-border-strong, #666A7E); }
  .lsq.failed { background: transparent; border: 1px solid var(--bx-danger, #FF7A7A); }
  .lfail { flex: none; margin-right: 6px; font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); font-weight: 600; color: var(--bx-danger, #FF7A7A); }
  /* the name (UI 600) and the path (mono, muted): the path gives way first */
  .titlebar .path { display: flex; align-items: baseline; gap: 8px; min-width: 0; overflow: hidden; white-space: nowrap; }
  .titlebar .path .nm { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; font-weight: 600; }
  .titlebar .path .dir { flex: 0 100 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis;
    font: var(--bx-font-code, 12px/18px ui-monospace, monospace); color: var(--bx-muted, #A3A6B6); }
  .titlebar button.winx {
    display: inline-flex; align-items: center; justify-content: center; box-sizing: border-box;
    width: 28px; height: 28px; padding: 0; border: 0; border-radius: 0; cursor: pointer;
    background: transparent; color: var(--bx-muted, #A3A6B6);
  }
  .titlebar button.winx:hover { color: var(--bx-close-hover-ink, #0B0C12); background: var(--bx-close-hover, #FF7A7A); }
  .titlebar button.winx:focus-visible { outline-offset: calc(-1 * var(--bx-focus-width, 3px)); box-shadow: none; }
`;
