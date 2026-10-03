/**
 * agent-css.js — <bx-agent>'s styles (the Agent tab, D74; Base Two, D184):
 * the agent's output on the panel, a person's turns on panel-2 with their
 * name; tool calls as code-face blocks with their exit status (their own
 * styles are agent-cards.js cardsCss); no shimmer, a static "Working…"
 * label; the session's status as a square with its word; the composer's
 * controls 28px with the focus ring, Send the primary action. Split from
 * bx-agent.js (its size budget).
 */
import { css, unsafeCSS } from 'lit';
import { mdCssText } from '/vendor/bx-md.js';

export const agentCss = css`
  :host { display: flex; flex-direction: column; height: 100%; min-height: 0;
    background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
    font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif); }
  button, select, textarea { font: inherit; }
  :focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: var(--bx-focus-offset, 2px);
    box-shadow: var(--bx-focus-halo, 0 0 0 2px #0B0C12); }
  /* overflow-anchor: none — the element anchors itself, the same on every
     engine (Safari has no native scroll anchoring) */
  .scroll { flex: 1; min-height: 0; overflow-y: auto; padding: 12px; overflow-anchor: none; }
  .earlier { display: flex; gap: 8px; justify-content: center; align-items: baseline; }
  .earlier button { border: 0; background: none; padding: 0; cursor: pointer; color: var(--bx-link, #8C9BFF); }
  .row { margin: 0 0 12px; }
  .who { font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); text-transform: uppercase;
    letter-spacing: var(--bx-tracking-micro, 0.06em); color: var(--bx-muted, #A3A6B6); margin-bottom: 4px; }
  .bubble { font: var(--bx-font-body, 400 14px/20px "Instrument Sans", system-ui, sans-serif); }
  .user .bubble { background: var(--bx-panel-2, #262730); border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px);
    padding: 8px 12px; white-space: pre-wrap; }
  .user .files { display: flex; flex-wrap: wrap; gap: 4px; white-space: normal; }
  .user .files.below { margin-top: 4px; }
  .user .file { border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); padding: 0 6px;
    font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
  .md-b { display: contents; }
  .agent .bubble > :first-child, .agent .bubble > .md-b:first-child > :first-child { margin-top: 0; }
  .agent .bubble > :last-child, .agent .bubble > .md-b:last-child > :last-child { margin-bottom: 0; }
  ${unsafeCSS(mdCssText('.bubble'))}
  .thought { color: var(--bx-muted, #A3A6B6); border-left: 2px solid var(--bx-border, #33353F); padding-left: 8px; }
  .thought > summary { list-style: none; cursor: pointer; display: inline-flex; align-items: center; gap: 4px;
    font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
  .thought > summary::-webkit-details-marker { display: none; }
  .thought:not([open]) > summary .cd, .thought[open] > summary .cr { display: none; }
  .activity { font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); margin: 2px 0 8px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  /* progress is a static label (product-ui §8: no shimmer; R14: nothing loops) */
  .shimmer { color: var(--bx-muted, #A3A6B6); }
  .thought .md { font-style: italic; }
  .thought .md > .md-b:first-child > :first-child { margin-top: 4px; } .thought .md > .md-b:last-child > :last-child { margin-bottom: 0; }
  .plan { border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); padding: 8px 12px; margin: 0 0 8px; }
  .plan .h { font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em);
    text-transform: uppercase; color: var(--bx-muted, #A3A6B6); margin-bottom: 4px; }
  .plan ul { margin: 0; padding: 0; }
  .plan li { list-style: none; margin: 2px 0; display: flex; gap: 6px; align-items: baseline; }
  .plan li bx-icon, .plan li .todo { flex: none; align-self: center; }
  .plan li .todo { width: 8px; height: 8px; margin: 0 4px; box-sizing: border-box; border: 1px solid var(--bx-border-strong, #666A7E); }
  .plan .done { color: var(--bx-ok, #A3CF5E); text-decoration: line-through; }
  .gap { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); text-align: center; margin: 4px 0; }
  .notice { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); margin: 4px 0; white-space: pre-wrap; }
  .turn { border-top: 1px solid var(--bx-border, #33353F); margin: 12px 0; padding-top: 4px; text-align: center;
    font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); font-variant-numeric: tabular-nums; color: var(--bx-muted, #A3A6B6); }
  .foot { flex: none; border-top: 1px solid var(--bx-border, #33353F); padding: 8px 12px; position: relative; }
  .pill { position: absolute; bottom: calc(100% + 8px); left: 50%; transform: translateX(-50%); z-index: 1; white-space: nowrap;
    display: inline-flex; align-items: center; gap: 6px; box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 11px; cursor: pointer;
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); font-weight: 600; box-shadow: var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6)); }
  .pill:hover { background: var(--bx-hover, #2A2B34); }
  .status { font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); font-variant-numeric: tabular-nums; color: var(--bx-muted, #A3A6B6);
    display: flex; align-items: center; gap: 8px; margin-bottom: 6px; min-height: 16px; }
  /* the session's state: an 8px square beside its word (R2, R6) */
  .status .dot { width: 8px; height: 8px; background: var(--bx-muted, #A3A6B6); flex: none; }
  .status .dot.running, .status .dot.waiting_permission { background: var(--bx-warn, #F2994A); }
  .status .dot.idle { background: var(--bx-ok, #A3CF5E); }
  .status .dot.error, .status .dot.exited { background: var(--bx-danger, #FF7A7A); }
  .status .err { color: var(--bx-danger, #FF7A7A); }
  .compose { display: flex; gap: 8px; align-items: flex-end; }
  .slash { border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); background: var(--bx-panel-2, #262730);
    margin-bottom: 6px; max-height: 220px; overflow-y: auto; }
  .slash .sc { display: flex; gap: 8px; align-items: center; min-height: var(--bx-row, 28px); padding: 0 8px; cursor: pointer; }
  .slash .sc.on { background: var(--bx-selection, #262C5C); color: var(--bx-selection-text, #E9EAF0); box-shadow: inset 2px 0 0 var(--bx-accent, #8C9BFF); }
  .slash .sc b { font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); font-weight: 600; white-space: nowrap; }
  .slash .sc .d { color: var(--bx-muted, #A3A6B6); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; }
  .slash .sc .h { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); white-space: nowrap; }
  .slash-hint { font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); color: var(--bx-muted, #A3A6B6); margin-bottom: 4px; }
  .compose textarea { flex: 1; resize: none; box-sizing: border-box; min-height: var(--bx-control-h, 28px);
    background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); padding: 4px 8px; max-height: 40vh; }
  .compose textarea::placeholder { color: var(--bx-subtle, #8E91A2); opacity: 1; }
  /* Send (and Start) is the primary action; Stop the destructive outline */
  .compose button { box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 11px; cursor: pointer; font-weight: 600;
    border: 1px solid var(--bx-accent, #8C9BFF); background: var(--bx-accent, #8C9BFF); color: var(--bx-accent-ink, #0B0C12); border-radius: var(--bx-radius, 2px); }
  .compose button:hover:not(:disabled) { background: var(--bx-accent-hover, #A9B4FF); border-color: var(--bx-accent-hover, #A9B4FF); }
  .compose button:disabled { opacity: .5; cursor: default; }
  .compose button.cancel { background: var(--bx-panel, #1F2028); color: var(--bx-danger, #FF7A7A); border-color: var(--bx-danger, #FF7A7A); }
  .compose button.cancel:hover { background: var(--bx-hover, #2A2B34); border-color: var(--bx-danger, #FF7A7A); }
  .chooser { display: flex; gap: 8px; margin-bottom: 8px; flex-wrap: wrap; }
  .chooser label { display: inline-flex; align-items: center; gap: 6px; color: var(--bx-muted, #A3A6B6);
    font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase; }
  .chooser select { box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 6px;
    background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif); letter-spacing: 0; text-transform: none; }
  .hint { color: var(--bx-muted, #A3A6B6); }
  /* signed out: a warning (its glyph, its words, its tint), Sign in the primary action */
  .signin { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 8px; padding: 8px 12px;
    border: 1px solid var(--bx-warn, #F2994A); border-radius: var(--bx-radius, 2px); background: var(--bx-warn-bg, #382F2C); color: var(--bx-text, #E9EAF0); }
  .signin .msg { flex: 1; }
  .signin .msg bx-icon { color: var(--bx-warn, #F2994A); margin-right: 6px; }
  .signin button { box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 11px; font-weight: 600; cursor: pointer; white-space: nowrap;
    border: 1px solid var(--bx-border-strong, #666A7E); background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); border-radius: var(--bx-radius, 2px); }
  .signin button.go { border-color: var(--bx-accent, #8C9BFF); background: var(--bx-accent, #8C9BFF); color: var(--bx-accent-ink, #0B0C12); }
  .status.signed { color: var(--bx-ok, #A3CF5E); }
  .status.ended button { margin-left: auto; box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 11px; cursor: pointer; font-weight: 600; white-space: nowrap;
    border: 1px solid var(--bx-accent, #8C9BFF); background: var(--bx-accent, #8C9BFF); color: var(--bx-accent-ink, #0B0C12); border-radius: var(--bx-radius, 2px); }
  .status.ended button:hover { background: var(--bx-accent-hover, #A9B4FF); border-color: var(--bx-accent-hover, #A9B4FF); }
`;
