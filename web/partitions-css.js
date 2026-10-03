// web/partitions-css.js — the partitions page's styles (/xbin/partitions;
// web/partitions-page.js). The workspace theme's tokens (/vendor/theme.css)
// with Night's values as fallbacks, so the page renders on its own; the page
// opts in to following the person's theme (D184: partitions.html,
// theme-boot.js). Square corners, status as glyph + word + colour, controls
// per product-ui §6.
import { css } from '/vendor/lit-all.min.js';

export const pageCss = css`
  :host {
    display: block; color: var(--bx-text, #E9EAF0); font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif);
    --pt-part: var(--bx-part, #3FB5A3); --pt-line: var(--bx-border, #33353F); --pt-muted: var(--bx-muted, #A3A6B6);
    --pt-panel: var(--bx-panel, #1F2028); --pt-panel2: var(--bx-panel-2, #262730); --pt-red: var(--bx-danger, #FF7A7A);
    --pt-amber: var(--bx-warn, #F2994A); --pt-green: var(--bx-ok, #A3CF5E); --pt-accent: var(--bx-accent, #8C9BFF);
  }
  :focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: var(--bx-focus-offset, 2px);
    box-shadow: var(--bx-focus-halo, 0 0 0 2px #0B0C12); }
  main { max-width: 920px; margin: 0 auto; padding: 24px 16px 64px; }
  header { display: flex; align-items: baseline; gap: 12px; flex-wrap: wrap; margin-bottom: 8px; }
  header .grow { flex: 1; }
  h1 { font: var(--bx-font-heading, 600 20px/26px "Bricolage Grotesque", "Arial Black", system-ui, sans-serif);
    letter-spacing: var(--bx-tracking-heading, -0.01em); margin: 0; display: flex; align-items: center; gap: 8px; }
  h2 { font: var(--bx-font-title, 600 16px/22px "Instrument Sans", system-ui, sans-serif); margin: 28px 0 8px; display: flex; align-items: baseline; gap: 8px; }
  h2 .n { color: var(--pt-muted); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); font-variant-numeric: tabular-nums; }
  h3 { font: inherit; font-weight: 600; font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); margin: 0; overflow-wrap: anywhere; }
  p { margin: 8px 0; }
  a { color: var(--bx-link, #8C9BFF); text-decoration: none; }
  a:hover { text-decoration: underline; }
  code, .mono { font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); font-size: 0.92em; overflow-wrap: anywhere;
    font-variant-ligatures: none; font-feature-settings: "liga" 0, "calt" 0; }
  .muted { color: var(--pt-muted); }
  .small, .who, .empty, .toc, summary { font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
  .who { color: var(--pt-muted); }
  .lead { color: var(--pt-muted); max-width: 70ch; }
  .mark { width: 16px; height: 16px; flex: none; }
  /* a notice: info, or an error (its glyph, its words, its tint) */
  .banner { border: 1px solid var(--pt-line); border-radius: var(--bx-radius, 2px); padding: 8px 12px; margin: 12px 0;
    background: var(--bx-info-bg, #30323B); }
  .banner.err { border-color: var(--pt-red); background: var(--bx-danger-bg, #3A2B32); }
  .banner bx-icon, .err bx-icon, .warn bx-icon, .done bx-icon { margin-right: 6px; }
  .banner bx-icon { color: var(--bx-info, #A9B4C6); }
  .banner.err bx-icon { color: var(--pt-red); }
  /* a card: a border, no shadow; a 3px rule on its left for its state */
  .card { border: 1px solid var(--pt-line); border-radius: var(--bx-radius, 2px); background: var(--pt-panel); padding: 12px; margin: 8px 0; }
  .card.attn { border-left: 3px solid var(--pt-amber); }
  .card.danger { border-left: 3px solid var(--pt-red); }
  .card.part { border-left: 3px solid var(--pt-part); }
  .head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .head .grow { flex: 1; }
  /* a badge: square, 20px, micro caps, 1px border */
  .chip { display: inline-flex; align-items: center; box-sizing: border-box; height: 20px; padding: 0 6px; white-space: nowrap;
    font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase;
    border: 1px solid var(--pt-line); border-radius: var(--bx-radius, 2px); color: var(--pt-muted); }
  .chip.part { color: var(--pt-part); border-color: currentColor; }
  .chip.warn { color: var(--pt-amber); border-color: currentColor; background: var(--bx-warn-bg, #382F2C); }
  .chip.ok { color: var(--pt-green); border-color: currentColor; background: var(--bx-ok-bg, #2F352E); }
  .chip.off { color: var(--pt-muted); }
  .facts { display: grid; grid-template-columns: max-content 1fr; gap: 4px 16px; margin: 8px 0 2px; }
  .facts dt { color: var(--pt-muted); }
  .facts dd { margin: 0; overflow-wrap: anywhere; }
  .acts { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; margin-top: 12px; }
  /* buttons (product-ui §6): secondary by default; .primary the accent with
     its ink; .danger the destructive outline; .link a quiet text button */
  button { box-sizing: border-box; min-height: var(--bx-control-h, 28px); background: var(--pt-panel); border: 1px solid var(--bx-border-strong, #666A7E);
    border-radius: var(--bx-radius, 2px); color: inherit; font: inherit; font-weight: 600; padding: 0 11px; cursor: pointer; }
  button:hover:not(:disabled) { background: var(--bx-hover, #2A2B34); }
  button:disabled { opacity: .5; cursor: default; }
  button.primary { background: var(--pt-accent); border-color: var(--pt-accent); color: var(--bx-accent-ink, #0B0C12); }
  button.primary:hover:not(:disabled) { background: var(--bx-accent-hover, #A9B4FF); border-color: var(--bx-accent-hover, #A9B4FF); }
  button.danger { border-color: var(--pt-red); color: var(--pt-red); }
  button.link { min-height: 0; background: none; border: 0; padding: 0; font-weight: 400; color: var(--bx-link, #8C9BFF); }
  button.link:hover:not(:disabled) { background: none; text-decoration: underline; }
  input, select { box-sizing: border-box; min-height: var(--bx-control-h, 28px); background: var(--pt-panel); border: 1px solid var(--bx-border-strong, #666A7E);
    border-radius: var(--bx-radius, 2px); color: inherit; font: inherit; padding: 0 8px; min-width: 0; }
  input::placeholder { color: var(--bx-subtle, #8E91A2); opacity: 1; }
  input.mono { font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); }
  input[type=checkbox] { min-height: 0; accent-color: var(--pt-accent); }
  input[type=number] { width: 4.5em; font-variant-numeric: tabular-nums; }
  label { display: inline-flex; gap: 6px; align-items: center; }
  .confirm { margin-top: 12px; border-top: 1px solid var(--pt-line); padding-top: 12px; }
  .confirm ul { margin: 4px 0 8px; padding-left: 18px; }
  .confirm li { margin: 2px 0; }
  .pre { white-space: pre-wrap; }
  .note { border-left: 2px solid var(--pt-line); padding-left: 12px; white-space: pre-wrap; overflow-wrap: anywhere; }
  .err { color: var(--pt-red); white-space: pre-wrap; }
  .done { color: var(--pt-green); white-space: pre-wrap; }
  .warn { color: var(--pt-amber); }
  details { margin-top: 8px; }
  summary { cursor: pointer; color: var(--pt-muted); }
  summary:hover { color: inherit; }
  table { border-collapse: collapse; width: 100%; margin-top: 8px; font-variant-numeric: tabular-nums; }
  th, td { text-align: left; padding: 4px 8px 4px 0; border-bottom: 1px solid var(--pt-line); vertical-align: top; overflow-wrap: anywhere; }
  th { color: var(--pt-muted); font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em);
    text-transform: uppercase; border-bottom: 2px solid var(--bx-text, #E9EAF0); }
  td.n, th.n { text-align: right; white-space: nowrap; }
  .row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  .empty { color: var(--pt-muted); margin: 8px 0; }
  .toc { display: flex; gap: 12px; flex-wrap: wrap; margin: 8px 0 0; }
  @media (max-width: 600px) {
    main { padding: 16px 12px 48px; }
    .facts { grid-template-columns: 1fr; }
    .facts dt { margin-top: 4px; }
  }
`;
