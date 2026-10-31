/**
 * win-resize.js — a floating window's resize corner (D217): the window's
 * bottom-right grip as a pointer drag through the kit's dragResize — the
 * shield over the tiles' iframes, the pointer captured — instead of the CSS
 * resize handle, whose drag an iframe under a fast cursor took (issue #54:
 * the corner "fell off" the window over another tile). <bx-frame>'s window,
 * the shell's floating tiles and its pop-out windows carry it; the window
 * drops `resize: both`. The grip is the window element's direct child.
 *
 *   ${resizeCorner({ onMove: (w, h) => …, onUp: (w, h) => … })}   in the window's template
 *   static styles = [..., resizeCss]
 */
import { html, css } from 'lit';
import { dragResize } from '/vendor/bx-kit.js';

// resizeCorner(opts): the grip; opts as dragResize's (maxW, maxH, onMove, onUp)
export const resizeCorner = (opts = {}) => html`<div class="rsz" title="drag to resize" aria-hidden="true"
  @pointerdown=${(e) => dragResize(e, e.currentTarget.parentElement, 'se', opts)}></div>`;

// the grid tile's corner (shell-css .gtile .rz): a 6 px mark in the grip, over
// whatever fills the window (a frame's overlays, the status bar); no grip
// where windows are full-screen sheets (phones)
export const resizeCss = css`
  .rsz {
    position: absolute; right: 0; bottom: 0; width: 14px; height: 14px;
    cursor: nwse-resize; z-index: 40; touch-action: none;
  }
  .rsz::after {
    content: ''; position: absolute; right: 2px; bottom: 2px; width: 6px; height: 6px; box-sizing: border-box;
    border-right: 2px solid var(--bx-border-strong, #666A7E); border-bottom: 2px solid var(--bx-border-strong, #666A7E);
  }
  .rsz:hover::after { border-color: var(--bx-text, #E9EAF0); }
  @media (max-width: 820px) { .rsz { display: none; } }
`;
