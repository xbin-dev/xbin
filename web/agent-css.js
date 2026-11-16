/**
 * agent-css.js — <bx-agent>'s own styles (the Agent tab, D74; Base Two,
 * D184): the element's frame around the transcript — the scroller, the foot
 * with the session's status as a square with its word, the hints. What the
 * transcript and the message box draw is the chat kit's one stylesheet now
 * (/vendor/chat/css.js, D224: the classes that start with `bxc-`), which
 * bx-agent.js adopts beside this one.
 */
import { css } from '/vendor/lit-all.min.js';

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
  .foot { flex: none; border-top: 1px solid var(--bx-border, #33353F); padding: 8px 12px; position: relative; }
  .status { font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); font-variant-numeric: tabular-nums; color: var(--bx-muted, #A3A6B6);
    display: flex; align-items: center; gap: 8px; margin-bottom: 6px; min-height: 16px; }
  /* the session's state: an 8px square beside its word (R2, R6) */
  .status .dot { width: 8px; height: 8px; background: var(--bx-muted, #A3A6B6); flex: none; }
  .status .dot.running, .status .dot.waiting_permission { background: var(--bx-warn, #F2994A); }
  .status .dot.idle { background: var(--bx-ok, #A3CF5E); }
  .status .dot.error, .status .dot.exited { background: var(--bx-danger, #FF7A7A); }
  .status .err { color: var(--bx-danger, #FF7A7A); }
  .status .signin-chip.warn { color: var(--bx-warn, #F2994A); }
  .status bx-agent-signin.signins { margin-left: auto; }
  .hint { color: var(--bx-muted, #A3A6B6); }
  /* signed out (the kit's card): Sign in the primary action */
  .bxc-signin button { box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 11px; font-weight: 600; cursor: pointer; white-space: nowrap;
    border: 1px solid var(--bx-border-strong, #666A7E); background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); border-radius: var(--bx-radius, 2px); }
  .bxc-signin button.bxc-primary { border-color: var(--bx-accent, #8C9BFF); background: var(--bx-accent, #8C9BFF); color: var(--bx-accent-ink, #0B0C12); }
  .status.signed { color: var(--bx-ok, #A3CF5E); }
  .status.ended button { margin-left: auto; box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 11px; cursor: pointer; font-weight: 600; white-space: nowrap;
    border: 1px solid var(--bx-accent, #8C9BFF); background: var(--bx-accent, #8C9BFF); color: var(--bx-accent-ink, #0B0C12); border-radius: var(--bx-radius, 2px); }
  .status.ended button:hover { background: var(--bx-accent-hover, #A9B4FF); border-color: var(--bx-accent-hover, #A9B4FF); }
`;
