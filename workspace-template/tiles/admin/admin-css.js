// admin-css.js — the admin console's shared styles. `base` is what every tab
// element and the router share (filter bar, chips, tables, pills, buttons,
// inputs, links); each tab adds its own slice (`mapCss`, …). Tabs use
// `static styles = [base, <slice>]` — synchronous, no flash of unstyled
// content, never a <link> or a fetch.
import { css } from 'lit';

export const base = css`
    :host { display: block; }
    /* filter bar (scales list views to 1000s of tiles) */
    .filterbar { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin: 2px 0 10px; }
    .filterbar input.q { flex: 1; min-width: 12em; font: inherit; font-size: 12px; padding: 4px 9px;
      border: 1px solid var(--bx-border, #363c45); border-radius: 6px;
      background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0); }
    .chips { display: flex; gap: 4px; flex-wrap: wrap; }
    .chip { font-size: 11px; padding: 2px 9px; border-radius: 999px; cursor: pointer;
      border: 1px solid var(--bx-border, #363c45); background: var(--bx-panel, #23272e);
      color: var(--bx-muted, #868f9a); }
    .chip.on { background: var(--bx-accent, #f5a623); border-color: transparent; color: #fff; }
    .count-note { font-size: 11px; color: var(--bx-muted, #868f9a); white-space: nowrap; }
    /* standalone expand caret (component rows aren't inside .bk) */
    .caret { display: inline-block; color: var(--bx-muted, #868f9a); font-size: 10px;
             transition: transform .1s; }
    .caret.o { transform: rotate(90deg); }
    .body { padding: 12px 14px; }
    .err { color: var(--bx-red, #ef5350); font-size: 12px; margin: 4px 0; }
    .notice { color: var(--bx-green, #4caf50); font-size: 12px; margin: 4px 0; }
    .alertbar { display: flex; flex-direction: column; gap: 2px; margin: 0 0 10px; }
    .al { padding: 6px 10px; border-radius: 6px; font-size: 12px; color: #fff; }
    .al b { margin-right: 4px; }
    .al.warn { background: #b7791f; }
    .al.crit { background: #c53030; }
    .denied { padding: 20px 14px; }
    .denied code { background: var(--bx-panel-2); border: 1px solid var(--bx-border);
      border-radius: 4px; padding: 0 4px; font: 11.5px var(--bx-mono); }

    h4 { margin: 14px 0 6px; font-size: 10.5px; font-weight: 600; letter-spacing: .08em;
         text-transform: uppercase; color: var(--bx-muted, #868f9a); }
    h4:first-child { margin-top: 0; }
    .cards { display: flex; gap: 10px; flex-wrap: wrap; margin-bottom: 4px; }
    .stat { background: var(--bx-panel-2, #2b3038); border: 1px solid var(--bx-border, #363c45);
      border-radius: 6px; padding: 6px 12px; min-width: 74px; }
    .stat .n { font: 700 18px var(--bx-mono, monospace); color: var(--bx-accent, #f5a623); }
    .stat .l { font-size: 10px; text-transform: uppercase; letter-spacing: .06em;
               color: var(--bx-muted, #868f9a); }
    .stat.warn .n { color: var(--bx-amber, #f2a71b); }
    .vault-banner { border-radius: 6px; padding: 8px 12px; margin-bottom: 12px;
      font-size: 12.5px; font-weight: 600; }
    .vault-banner.sealed {
      background: var(--bx-red, #ef5350); color: #fff; cursor: pointer;
      font-size: 13.5px; letter-spacing: .02em;
      box-shadow: 0 0 0 1px color-mix(in srgb, var(--bx-red, #ef5350) 60%, #000),
                  0 2px 10px color-mix(in srgb, var(--bx-red, #ef5350) 50%, transparent);
      animation: vault-pulse 1.6s ease-in-out infinite;
    }
    @keyframes vault-pulse { 50% { filter: brightness(1.18); } }
    @media (prefers-reduced-motion: reduce) { .vault-banner.sealed { animation: none; } }
    .vault-banner.warn { background: color-mix(in srgb, var(--bx-amber, #f2a71b) 18%, transparent);
      color: var(--bx-amber, #f2a71b); cursor: pointer;
      border: 1px solid color-mix(in srgb, var(--bx-amber, #f2a71b) 45%, transparent); }
    .vault-banner.ok { background: none; border: 0; padding: 0 2px;
      color: var(--bx-green, #4caf50); font-weight: 500; font-size: 11px; }

    table { border-collapse: collapse; width: 100%; font-size: 12px; }
    th { text-align: left; font-size: 10px; text-transform: uppercase; letter-spacing: .06em;
         color: var(--bx-muted, #868f9a); font-weight: 600; padding: 3px 8px 3px 0; }
    td { padding: 3px 8px 3px 0; border-top: 1px solid var(--bx-border, #363c45);
         vertical-align: top; }
    .mono { font-family: var(--bx-mono, monospace); }
    .pill { display: inline-block; font-size: 11px; padding: 0 6px; border-radius: 999px;
      background: var(--bx-panel-2, #2b3038); border: 1px solid var(--bx-border, #363c45);
      margin: 1px 2px 1px 0; }
    .dot { display: inline-block; width: 7px; height: 7px; border-radius: 50%; margin-right: 5px; }
    .st-healthy { color: var(--bx-green, #4caf50); }
    .st-failed  { color: var(--bx-red, #ef5350); }
    .st-idle    { color: var(--bx-muted, #868f9a); }

    button.act { border: 1px solid var(--bx-border, #363c45); background: var(--bx-panel, #23272e);
      color: var(--bx-text, #d4d9e0); border-radius: 5px; font: inherit; font-size: 11px;
      padding: 1px 8px; cursor: pointer; }
    button.act:hover { background: var(--bx-panel-2, #2b3038); }
    button.go { background: var(--bx-green, #4caf50); color: #fff; border-color: transparent; }
    button.rm { color: var(--bx-red, #ef5350); border-color: color-mix(in srgb, var(--bx-red) 40%, transparent); }
    input, select { font: inherit; font-size: 12px; padding: 2px 6px;
      border: 1px solid var(--bx-border, #363c45); border-radius: 5px;
      background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0); }
    .secret { font-family: var(--bx-mono, monospace); }
    .muted { color: var(--bx-muted, #868f9a); }
    form.inline { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; margin-top: 8px; }
    a.link { color: var(--bx-accent, #f5a623); cursor: pointer; text-decoration: none; }
    a.link:hover { text-decoration: underline; }
    a.link.gated { color: var(--bx-muted, #868f9a); cursor: not-allowed; opacity: .55; }
    a.link.gated:hover { text-decoration: none; }
`;

export const mapCss = css`
    /* ---- access map (structure + effective-access matrix) ---- */
    .lv { display: inline-flex; align-items: center; justify-content: center;
      width: 16px; height: 16px; border-radius: 4px; font-size: 10px; font-weight: 700;
      font-family: var(--bx-mono, monospace); border: 1px solid transparent; }
    .lv-read { color: #3577c8; background: color-mix(in srgb, #3577c8 14%, transparent);
      border-color: color-mix(in srgb, #3577c8 40%, transparent); }
    .lv-write { color: var(--bx-green, #4caf50);
      background: color-mix(in srgb, var(--bx-green, #4caf50) 14%, transparent);
      border-color: color-mix(in srgb, var(--bx-green, #4caf50) 40%, transparent); }
    .lv-terminal { color: var(--bx-accent, #f5a623);
      background: color-mix(in srgb, var(--bx-accent, #f5a623) 16%, transparent);
      border-color: color-mix(in srgb, var(--bx-accent, #f5a623) 45%, transparent); }
    .lv-none { color: var(--bx-muted, #868f9a); opacity: .5; }
    .pill.crown { border-color: color-mix(in srgb, var(--bx-accent, #f5a623) 55%, transparent);
      color: var(--bx-accent, #f5a623); }
    .pill.pol { border-color: color-mix(in srgb, var(--bx-red, #ef5350) 45%, transparent);
      color: var(--bx-red, #ef5350); cursor: help; }
    .pill.lv-read, .pill.lv-write, .pill.lv-terminal { width: auto; height: auto; }
    .snode { border: 1px solid var(--bx-border, #363c45); border-left: 3px solid var(--bx-border, #363c45);
      border-radius: 6px; padding: 6px 10px; margin: 6px 0; }
    .snode .shead { font-weight: 600; font-size: 12px; margin-bottom: 3px;
      display: flex; align-items: baseline; gap: 6px; flex-wrap: wrap; }
    .snode.ws { border-left-color: var(--bx-muted, #868f9a); }
    .snode.org { border-left-color: var(--bx-accent, #f5a623); }
    .snode.team { border-left-color: var(--bx-green, #4caf50); margin-left: 18px; position: relative; }
    .snode.team::before { content: ''; position: absolute; left: -12px; top: 14px;
      width: 9px; border-top: 1px solid var(--bx-border, #363c45); }
    .matrix { border-collapse: collapse; font-size: 11px; }
    .matrix th { padding: 3px 6px; font-size: 10.5px; color: var(--bx-muted, #868f9a);
      font-weight: 600; text-align: center; }
    .matrix .mgrp { padding: 6px 4px 2px; font-size: 10px; font-weight: 700;
      letter-spacing: .07em; text-transform: uppercase; color: var(--bx-muted, #868f9a);
      border-bottom: 1px solid var(--bx-border, #363c45); }
    .matrix .mtile { padding: 2px 10px 2px 4px; font-size: 11px; white-space: nowrap; }
    .matrix .mcell { text-align: center; padding: 2px 5px; border-radius: 4px; }
    .matrix .mcell.has { cursor: pointer; }
    .matrix .mcell.has:hover { background: var(--bx-panel-2, #2b3038); }
    .matrix .mown { padding: 2px 10px 2px 4px; white-space: nowrap; }
    .maprow { display: flex; align-items: center; gap: 6px; padding: 3px 4px;
              border-bottom: 1px solid var(--bx-border, #363c45); font-size: 11.5px; }
    .maprow .mono { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
    .maprow:hover { background: var(--bx-panel-2, #2b3038); }
    .mapsub { margin: 0 0 6px 18px; font-size: 11px; }
    .mapsub td { padding: 1px 8px 1px 0; }
    .matrix .mcell.msel { outline: 2px solid color-mix(in srgb, var(--bx-accent, #f5a623) 55%, transparent);
      outline-offset: -2px; }
    .flow-deny { color: var(--bx-red, #ef5350); }
    .flow-allow { color: var(--bx-green, #4caf50); }
    .err-pill { color: var(--bx-red, #ef5350); font-size: 11px; }
`;
