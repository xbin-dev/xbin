/**
 * agent-chrome-css.js — the one stylesheet of xbin's chrome for the
 * workspace agent (/vendor/agent-chrome-css.js, D232): the dock, the
 * approval sheet, the change-set dialog and the panels share it, each in
 * its own shadow root. Theme tokens only, with Night's values as fallbacks.
 */
import { css } from '/vendor/lit-all.min.js';

export const chromeCss = css`
  :host { color: var(--bx-text, #E9EAF0); font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif); }
  * { box-sizing: border-box; }
  h2, h3, p { margin: 0; }
  h2 { font: var(--bx-font-title, 600 16px/22px "Instrument Sans", system-ui, sans-serif); }
  h3 { font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em);
    text-transform: uppercase; color: var(--bx-muted, #A3A6B6); }
  .muted { color: var(--bx-muted, #A3A6B6); }
  .small { font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
  .mono { font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); overflow-wrap: anywhere; }
  .err { color: var(--bx-danger, #FF7A7A); }
  button { font: inherit; font-weight: 600; min-height: var(--bx-control-h, 28px); padding: 4px 11px; cursor: pointer; display: inline-flex; align-items: center; gap: 6px;
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); }
  button:hover:not(:disabled) { background: var(--bx-hover, #2A2B34); }
  button:disabled, button[aria-disabled="true"] { opacity: .5; cursor: default; }
  button:focus-visible, input:focus-visible, select:focus-visible, textarea:focus-visible, [tabindex]:focus-visible { outline: 3px solid var(--bx-focus, #3DD6F5); outline-offset: 2px; }
  button.primary { background: var(--bx-accent, #8C9BFF); border-color: var(--bx-accent, #8C9BFF); color: var(--bx-accent-ink, #0B0C12); }
  button.primary:hover:not(:disabled) { background: var(--bx-accent-hover, #A9B4FF); }
  button.danger { border-color: var(--bx-danger, #FF7A7A); color: var(--bx-danger, #FF7A7A); }
  button.danger:hover:not(:disabled) { background: var(--bx-danger-bg, #3A2B32); }
  button.quiet { border-color: transparent; background: none; }
  button.link { border: 0; background: none; padding: 0; min-height: 0; color: var(--bx-link, #8C9BFF); font-weight: 400; }
  button.link:hover:not(:disabled) { background: none; text-decoration: underline; }
  input, select, textarea { font: inherit; color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028); border: 1px solid var(--bx-border-strong, #666A7E);
    border-radius: var(--bx-radius, 2px); min-height: var(--bx-control-h, 28px); padding: 4px 8px; }
  input[type=radio], input[type=checkbox] { min-height: 0; padding: 0; accent-color: var(--bx-accent, #8C9BFF); }
  .tag { display: inline-flex; align-items: center; gap: 4px; padding: 0 6px; height: 18px; white-space: nowrap; border: 1px solid var(--bx-border-strong, #666A7E);
    border-radius: var(--bx-radius, 2px); font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase; }
  .tag.ok { color: var(--bx-ok, #A3CF5E); border-color: currentColor; background: var(--bx-ok-bg, #2F352E); }
  .tag.warn { color: var(--bx-warn, #F2994A); border-color: currentColor; background: var(--bx-warn-bg, #382F2C); }
  /* a start's trust line (D230): xbind's own words, warned, with the tick the person makes */
  .trustline { display: flex; flex-direction: column; gap: 4px; margin-top: 6px; padding: 6px 8px; border: 1px solid var(--bx-warn, #F2994A); border-radius: var(--bx-radius, 2px); }
  .trustline label.row { display: flex; gap: 8px; align-items: flex-start; cursor: pointer; }
  .trustline label.row input { margin-top: 3px; flex: none; }
  .tag.bad { color: var(--bx-danger, #FF7A7A); border-color: currentColor; background: var(--bx-danger-bg, #3A2B32); }
  .count { display: inline-flex; align-items: center; justify-content: center; min-width: 18px; height: 18px; padding: 0 4px; background: var(--bx-accent, #8C9BFF);
    color: var(--bx-accent-ink, #0B0C12); font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); font-variant-numeric: tabular-nums; }
  /* the agent's own words: always quoted, always labelled */
  .quote { border-left: 2px solid var(--bx-border-strong, #666A7E); padding: 2px 0 2px 12px; }
  .quote .ql { display: block; font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em);
    text-transform: uppercase; color: var(--bx-muted, #A3A6B6); margin-bottom: 2px; }
  .quote .qt { white-space: pre-wrap; overflow-wrap: anywhere; font-style: italic; }
  .banner { display: flex; gap: 8px; align-items: flex-start; padding: 8px 12px; border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); background: var(--bx-panel, #1F2028); }
  .banner.warn { border-color: var(--bx-warn, #F2994A); background: var(--bx-warn-bg, #382F2C); }
  .banner.bad { border-color: var(--bx-danger, #FF7A7A); background: var(--bx-danger-bg, #3A2B32); }
  .banner bx-icon { flex: none; margin-top: 1px; }
  /* a character of the agent's text that is never drawn as it is (bidi, zero-width, control): its escape, as a chip */
  .hid { display: inline-block; padding: 0 3px; margin: 0 1px; border: 1px solid var(--bx-warn, #F2994A); border-radius: var(--bx-radius, 2px);
    background: var(--bx-warn-bg, #382F2C); color: var(--bx-warn, #F2994A); font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif);
    font-style: normal; white-space: nowrap; unicode-bidi: isolate; }
  .hidnote { display: flex; gap: 4px; align-items: center; color: var(--bx-warn, #F2994A); margin-bottom: 2px; }
  /* in place of Allow / Apply while the agent is stopped: why, and that Deny / Reject still work */
  .banner.stopped { flex: 1 1 220px; min-width: 0; }
  .banner.warn bx-icon { color: var(--bx-warn, #F2994A); } .banner.bad bx-icon { color: var(--bx-danger, #FF7A7A); }
  /* xbin's own bar: inverted, so it reads as the product speaking and not the page under it */
  .xbar { display: flex; align-items: center; gap: 8px; padding: 4px 12px; background: var(--bx-text, #E9EAF0); color: var(--bx-bg, #0B0C12);
    font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase; }
  .xbar .who { margin-left: auto; text-transform: none; letter-spacing: 0; font-weight: 400; }
  .rows { display: grid; grid-template-columns: minmax(72px, max-content) 1fr; gap: 6px 12px; }
  .rows > dt { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); padding-top: 1px; }
  .rows > dd { margin: 0; min-width: 0; }
  pre.body { margin: 0; padding: 6px 8px; max-height: 180px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; background: var(--bx-code-bg, #16171D);
    border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); }
  .btns { display: flex; gap: 8px; flex-wrap: wrap; justify-content: flex-end; }
  .list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; }
  .list > li { display: flex; gap: 8px; align-items: flex-start; padding: 8px 12px; border-bottom: 1px solid var(--bx-border, #33353F); }
  .list > li .tx { flex: 1; min-width: 0; }
  .list > li .tx b { font-weight: 600; }
  .empty { padding: 16px 12px; color: var(--bx-muted, #A3A6B6); }
  .panel { display: flex; flex-direction: column; min-height: 0; flex: 1; }
  .panel > .ph { display: flex; align-items: center; gap: 8px; padding: 8px 12px; border-bottom: 1px solid var(--bx-border, #33353F); }
  .panel > .ph h2 { flex: 1; }
  .panel > .pb { flex: 1; min-height: 0; overflow: auto; }
  .panel > .pf { padding: 8px 12px; border-top: 1px solid var(--bx-border, #33353F); }
  .seg { display: inline-flex; }
  .seg button { border-radius: 0; margin-left: -1px; }
  .seg button[aria-pressed="true"] { background: var(--bx-selection, #262C5C); color: var(--bx-selection-text, #E9EAF0); border-color: var(--bx-accent, #8C9BFF); z-index: 1; }
`;
