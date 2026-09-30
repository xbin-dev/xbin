// web/partitions-css.js — the partitions page's styles (/xbin/partitions;
// web/partitions-page.js). The workspace theme's tokens (/vendor/theme.css)
// with fallbacks, so the page renders on its own.
import { css } from '/vendor/lit-all.min.js';

export const pageCss = css`
  :host {
    display: block; color: var(--bx-text, #d4d9e0); font: var(--bx-font, 13px/1.45 system-ui, sans-serif);
    --pt-part: var(--bx-part, #3fb5a3); --pt-line: var(--bx-border, #363c45); --pt-muted: var(--bx-muted, #868f9a);
    --pt-panel: var(--bx-panel, #23272e); --pt-panel2: var(--bx-panel-2, #2b3038); --pt-red: var(--bx-red, #ef5350);
    --pt-amber: var(--bx-amber, #f2a71b); --pt-green: var(--bx-green, #4caf50); --pt-accent: var(--bx-accent, #f5a623);
  }
  main { max-width: 920px; margin: 0 auto; padding: 20px 16px 64px; }
  header { display: flex; align-items: baseline; gap: 12px; flex-wrap: wrap; margin-bottom: 6px; }
  header .grow { flex: 1; }
  h1 { font-size: 20px; margin: 0; display: flex; align-items: center; gap: 8px; }
  h2 { font-size: 14px; margin: 28px 0 8px; display: flex; align-items: baseline; gap: 8px; }
  h2 .n { color: var(--pt-muted); font-weight: 400; font-size: 12px; }
  h3 { font-size: 13px; margin: 0; font-family: var(--bx-mono, ui-monospace, monospace); font-weight: 600; overflow-wrap: anywhere; }
  p { margin: 6px 0; }
  a { color: var(--pt-accent); text-decoration: none; }
  a:hover { text-decoration: underline; }
  code, .mono { font-family: var(--bx-mono, ui-monospace, monospace); font-size: 12px; overflow-wrap: anywhere; }
  .muted { color: var(--pt-muted); }
  .small { font-size: 12px; }
  .who { color: var(--pt-muted); font-size: 12px; }
  .lead { color: var(--pt-muted); max-width: 70ch; }
  .mark { width: 14px; height: 14px; flex: none; }
  .banner { border: 1px solid var(--pt-line); border-left: 3px solid var(--pt-amber); border-radius: 6px; padding: 8px 12px; margin: 10px 0;
    background: var(--pt-panel); }
  .banner.err { border-left-color: var(--pt-red); }
  .card { border: 1px solid var(--pt-line); border-radius: 8px; background: var(--pt-panel); padding: 12px 14px; margin: 8px 0; }
  .card.attn { border-left: 3px solid var(--pt-amber); }
  .card.danger { border-left: 3px solid var(--pt-red); }
  .card.part { border-left: 3px solid var(--pt-part); }
  .head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .head .grow { flex: 1; }
  .chip { display: inline-block; font-size: 11px; line-height: 16px; padding: 0 7px; border-radius: 9px; border: 1px solid var(--pt-line);
    color: var(--pt-muted); white-space: nowrap; }
  .chip.part { color: var(--pt-part); border-color: color-mix(in srgb, var(--pt-part) 50%, transparent); }
  .chip.warn { color: var(--pt-amber); border-color: color-mix(in srgb, var(--pt-amber) 50%, transparent); }
  .chip.ok { color: var(--pt-green); border-color: color-mix(in srgb, var(--pt-green) 50%, transparent); }
  .chip.off { color: var(--pt-muted); }
  .facts { display: grid; grid-template-columns: max-content 1fr; gap: 3px 14px; margin: 8px 0 2px; font-size: 12px; }
  .facts dt { color: var(--pt-muted); }
  .facts dd { margin: 0; overflow-wrap: anywhere; }
  .acts { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; margin-top: 10px; }
  button { background: var(--pt-panel2); border: 1px solid var(--pt-line); border-radius: 6px; color: inherit; font: inherit; font-size: 12px;
    padding: 4px 11px; cursor: pointer; }
  button:hover:not(:disabled) { border-color: var(--pt-muted); }
  button:disabled { opacity: .5; cursor: default; }
  button.primary { border-color: color-mix(in srgb, var(--pt-part) 60%, transparent); color: var(--pt-part); }
  button.danger { border-color: color-mix(in srgb, var(--pt-red) 60%, transparent); color: var(--pt-red); }
  button.link { background: none; border: 0; padding: 0; color: var(--pt-accent); }
  input, select { background: var(--pt-panel2); border: 1px solid var(--pt-line); border-radius: 6px; color: inherit; font: inherit;
    font-size: 12px; padding: 4px 8px; min-width: 0; }
  input.mono { font-family: var(--bx-mono, ui-monospace, monospace); }
  input:focus, select:focus { outline: 2px solid color-mix(in srgb, var(--pt-part) 45%, transparent); }
  input[type=number] { width: 4.5em; }
  label { display: inline-flex; gap: 6px; align-items: center; }
  .confirm { margin-top: 10px; border-top: 1px dashed var(--pt-line); padding-top: 10px; }
  .confirm ul { margin: 4px 0 8px; padding-left: 18px; }
  .confirm li { margin: 2px 0; }
  .pre { white-space: pre-wrap; }
  .note { border-left: 2px solid var(--pt-line); padding-left: 10px; white-space: pre-wrap; overflow-wrap: anywhere; }
  .err { color: var(--pt-red); white-space: pre-wrap; }
  .done { color: var(--pt-green); white-space: pre-wrap; }
  .warn { color: var(--pt-amber); }
  details { margin-top: 8px; }
  summary { cursor: pointer; color: var(--pt-muted); font-size: 12px; }
  summary:hover { color: inherit; }
  table { border-collapse: collapse; width: 100%; font-size: 12px; margin-top: 6px; }
  th, td { text-align: left; padding: 3px 8px 3px 0; border-bottom: 1px solid var(--pt-line); vertical-align: top; overflow-wrap: anywhere; }
  th { color: var(--pt-muted); font-weight: 500; }
  td.n, th.n { text-align: right; white-space: nowrap; }
  .row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  .empty { color: var(--pt-muted); font-size: 12px; margin: 6px 0; }
  .toc { display: flex; gap: 12px; flex-wrap: wrap; font-size: 12px; margin: 8px 0 0; }
  @media (max-width: 600px) {
    main { padding: 14px 12px 48px; }
    .facts { grid-template-columns: 1fr; }
    .facts dt { margin-top: 4px; }
  }
`;
