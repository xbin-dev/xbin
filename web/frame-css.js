/**
 * frame-css.js — <bx-frame>'s own styles (Base Two, D184; the titlebar,
 * launcher, deploy and panels slices are their modules' own). frameBaseCss
 * comes first in the element's list: the shadow root's controls in the UI
 * font, the focus ring and placeholders from the tokens (document rules
 * don't reach in here). frameCss comes last: the frame itself (the iframe
 * filling its host, the edit dot, the failed build's overlay) and the
 * floating terminal window's chrome (product-ui 3: the window edge and
 * shadows, the active window, the part tab, the phone's sheet). Split from
 * bx-frame.js (its size budget).
 */
import { css } from 'lit';

export const frameBaseCss = css`
  /* controls in the UI font, the focus ring, placeholders from the tokens
     (a shadow root's own: document rules don't reach in here) */
  button, input, select, textarea { font: inherit; color: inherit; }
  :focus-visible:not(iframe) {
    outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: var(--bx-focus-offset, 2px);
    box-shadow: var(--bx-focus-halo, 0 0 0 2px #0B0C12);
  }
  ::placeholder { color: var(--bx-subtle, #8E91A2); opacity: 1; }
  bx-icon { flex: none; }
`;

export const frameCss = css`
  :host { display: block; position: relative; }
  /* height:100% is what lets a fixed-height embedder (the shell grid tiles /
     floating windows pin the host with position:absolute; inset:0) flow a
     definite height down to the iframe: iframe height:100% resolves against
     .frame-wrap, so .frame-wrap must itself fill the host — otherwise it
     stays content-height (auto) and the iframe collapses. In auto-height mode
     the host is content-sized, so 100%-of-auto is just auto — no change. */
  .frame-wrap { position: relative; height: 100%; }
  iframe {
    display: block; width: 100%; border: 0;
    height: var(--bx-frame-height, 100%);
    background: transparent;
  }
  .edit {
    position: absolute; top: 2px; right: 2px;
    width: 7px; height: 7px; padding: 0; border: 0; border-radius: var(--bx-radius, 2px);
    background: var(--bx-accent, #8C9BFF);
    opacity: 0.35; cursor: pointer; z-index: 10;
  }
  .edit:hover { opacity: 1; }
  /* the build failed: its error glyph and word over the compiler output */
  .overlay {
    position: absolute; inset: 0; z-index: 9; overflow: auto;
    background: var(--bx-code-bg, #16171D);
    color: var(--bx-text, #E9EAF0);
    font: var(--bx-font-code, 12px/18px ui-monospace, monospace);
    padding: 10px 12px; margin: 0; white-space: pre-wrap;
    border-top: 2px solid var(--bx-danger, #FF7A7A);
  }
  .overlay b { display: inline-flex; align-items: center; gap: 6px; color: var(--bx-danger, #FF7A7A); font-weight: 700; }

  /* ---- floating terminal window: window chrome (product-ui 3) ---- */
  .pop {
    position: fixed; box-sizing: border-box;
    display: flex; flex-direction: column;
    font: var(--bx-font, 13px/18px system-ui, sans-serif);
    color: var(--bx-text, #E9EAF0);
    background: var(--bx-panel, #1F2028);
    border: 1px solid var(--bx-window-border, #666A7E);
    border-radius: var(--bx-radius, 2px);
    box-shadow: var(--bx-shadow-rest, 0 10px 28px rgba(0, 0, 0, 0.4));
    resize: both; overflow: hidden;
    min-width: 380px; min-height: 220px;
  }
  .pop.active { border-color: var(--bx-window-border-active, #9396A4); box-shadow: var(--bx-shadow-active, 0 16px 40px rgba(0, 0, 0, 0.55)); }
  /* the part tab: a 3 px strip across the top edge, green while a terminal
     tab is active, magenta while an agent session's is */
  .pop::before { content: ''; flex: none; height: var(--bx-part-tab-h, 3px); background: var(--bx-part-tab-terminal, #00A86B); }
  .pop.agent::before { background: var(--bx-part-tab-agent, #DB0072); }
  /* On phones the draggable pop-up becomes a full-screen sheet. */
  @media (max-width: 820px) {
    .pop { inset: 0 !important; width: auto !important; height: auto !important;
      resize: none !important; border-radius: 0; min-width: 0; min-height: 0; }
  }
`;
