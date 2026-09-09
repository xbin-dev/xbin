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
    /* shared pills + inline problem/flow colours (every tab) */
    .pill.crown { border-color: color-mix(in srgb, var(--bx-accent, #f5a623) 55%, transparent);
      color: var(--bx-accent, #f5a623); }
    .pill.pol { border-color: color-mix(in srgb, var(--bx-red, #ef5350) 45%, transparent);
      color: var(--bx-red, #ef5350); cursor: help; }
    .flow-deny { color: var(--bx-red, #ef5350); }
    .flow-allow { color: var(--bx-green, #4caf50); }
    .err-pill { color: var(--bx-red, #ef5350); font-size: 11px; }
    /* editors (typed rows, membership chips, warnings) — every tab */
    .warn-line { color: var(--bx-amber, #f2a71b); font-size: 11px; margin-top: 4px; }
    /* inline rule / membership chip (IdP-group rules, new-account org rows) */
    .rule { display: inline-flex; gap: 4px; align-items: center; border: 1px solid var(--bx-border, #363c45);
      border-radius: 6px; padding: 2px 6px; margin: 2px 4px 2px 0; font-size: 12px; }
    .editor { padding: 6px 8px; background: var(--bx-panel-2, #2b3038); border-radius: 6px; font-size: 12px; }
    .editor .orow { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; padding: 2px 0; }
    /* permission-set creator (D57): stored entries in words, rows with an in-words preview */
    .allowlist { list-style: none; margin: 4px 0 0; padding: 0; font-size: 11.5px; }
    .allowlist li { margin: 2px 0; }
    .allow-desc { font-size: 11px; flex-basis: 100%; padding-left: 4px; }
    .allow-desc .mono { opacity: .75; margin-left: 4px; }
    .seteditor select[name=kind] { min-width: 150px; }
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
`;

// the organisations tab: policy-row tables and synced-membership pills
export const orgsCss = css`
    .flowtab { width: 100%; font-size: 11px; }
    .flowtab td { padding: 1px 6px 1px 0; }
    .pill.sync { border-style: dashed; }   /* membership / role synced from an IdP group */
`;

// the users tab: the per-row <details> menu and the accounts table
export const usersCss = css`
    /* per-row "more ▾" menu: a native <details>, no JS state (users table) */
    details.menu { position: relative; display: inline-block; }
    details.menu > summary { list-style: none; display: inline-block; border: 1px solid var(--bx-border, #363c45);
      background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0); border-radius: 5px;
      font-size: 11px; padding: 1px 8px; cursor: pointer; user-select: none; }
    details.menu > summary::-webkit-details-marker { display: none; }
    details.menu > summary:hover, details.menu[open] > summary { background: var(--bx-panel-2, #2b3038); }
    details.menu > .items { position: absolute; right: 0; top: calc(100% + 3px); z-index: 30; min-width: 14em;
      display: flex; flex-direction: column; padding: 3px; text-align: left; white-space: nowrap;
      background: var(--bx-panel, #23272e); border: 1px solid var(--bx-border, #363c45); border-radius: 6px;
      box-shadow: 0 8px 24px rgba(0, 0, 0, .18); }
    details.menu > .items button { border: 0; background: none; color: inherit; font: inherit; font-size: 12px;
      text-align: left; padding: 4px 8px; border-radius: 4px; cursor: pointer; }
    details.menu > .items button:hover { background: var(--bx-panel-2, #2b3038); }
    details.menu > .items button.rm { color: var(--bx-red, #ef5350); }
    details.menu > .items button:disabled { opacity: .45; cursor: not-allowed; }
    details.menu > .items hr { border: 0; border-top: 1px solid var(--bx-border, #363c45); margin: 3px 0; }
    /* users table */
    td.user .sub { font-size: 10.5px; color: var(--bx-muted, #868f9a); font-family: var(--bx-mono, ui-monospace, monospace); }
    .pill.off { color: var(--bx-red, #ef5350); border-color: var(--bx-red, #ef5350); }
    .pill.sync { border-style: dashed; }   /* membership / role synced from an IdP group */
    .never { color: var(--bx-amber, #f2a71b); font-weight: 600; }
    .chip .n { opacity: .7; margin-left: 3px; }
`;

// the runtime group: the code drill-in, the live-stats table, backend detail
export const runtimeCss = css`
    /* ---- code & history ---- */
    .code { display: grid; grid-template-columns: 240px 1fr; gap: 12px; align-items: start; }
    .code .side { min-width: 0; }
    .code .main { min-width: 0; }
    .code .files, .code .hist { border: 1px solid var(--bx-border, #363c45); border-radius: 6px;
      overflow: hidden; margin-bottom: 10px; }
    .code .files .row, .code .hist .row { padding: 3px 8px; cursor: pointer; font-size: 12px;
      border-top: 1px solid var(--bx-border, #363c45); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .code .files .row:first-child, .code .hist .row:first-child { border-top: 0; }
    .code .files .row.on, .code .hist .row.on { background: var(--bx-panel-2, #2b3038); }
    .code .files .row:hover, .code .hist .row:hover { background: var(--bx-panel-2, #2b3038); }
    .code .hist .row .s { font-family: var(--bx-mono, monospace); color: var(--bx-muted, #868f9a); font-size: 10.5px; }
    .code .hd { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; }
    .code .hd .path { font-family: var(--bx-mono, monospace); font-size: 12px; }
    .code pre { margin: 0; padding: 10px 12px; background: var(--bx-panel-2, #2b3038);
      border: 1px solid var(--bx-border, #363c45); border-radius: 6px; overflow: auto; max-height: 70vh;
      font: 11.5px/1.5 var(--bx-mono, monospace); white-space: pre;
      color: var(--bx-text, #d4d9e0); tab-size: 4; }
    /* diff: tint add/del lines (a line is a direct-child span), keep syntax colors */
    .code pre.diff > span { display: block; }
    .code pre.diff > .d { background: color-mix(in srgb, var(--bx-green, #4caf50) 14%, transparent); }
    .code pre.diff > .a { background: color-mix(in srgb, var(--bx-red, #ef5350) 14%, transparent); }
    .code pre.diff > .h { color: var(--bx-accent, #f5a623);
      background: color-mix(in srgb, var(--bx-accent, #f5a623) 8%, transparent); }
    .code pre.diff > .fh { color: var(--bx-muted, #868f9a); }
    .grouphd { font-size: 10px; text-transform: uppercase; letter-spacing: .06em;
      color: var(--bx-muted, #868f9a); padding: 4px 8px; background: var(--bx-panel-2, #2b3038); }

    /* ---- live tile stats (resources tab) ---- */
    .strip { display: flex; gap: 10px; align-items: center; margin: 4px 0 8px; flex-wrap: wrap; }
    table.stats th.sortable { cursor: pointer; user-select: none; white-space: nowrap; }
    table.stats th.sortable:hover { color: var(--bx-text, #d4d9e0); }
    table.stats .strow { cursor: pointer; }
    table.stats .strow:hover td, table.stats .strow.on td { background: var(--bx-panel-2, #2b3038); }
    .stcell { display: flex; flex-direction: column; gap: 1px; min-width: 90px; }
    .stcell .stval { font-size: 11px; font-variant-numeric: tabular-nums; white-space: nowrap; }
    svg.spark { display: block; opacity: .85; }
    td.stbig { padding: 8px 0 10px; }
    td.stbig > .stchart { display: inline-block; margin: 0 18px 4px 0; vertical-align: top; }
    .stchart b { font-variant-numeric: tabular-nums; font-weight: 600; }
    table.stats .grouphd { padding: 4px 8px; }

    /* highlight.js — dark palette (Atom-One-Dark-ish) scoped to this shadow */
    .hljs-comment, .hljs-quote { color: #7f8896; font-style: italic; }
    .hljs-keyword, .hljs-selector-tag, .hljs-doctag, .hljs-formula { color: #c678dd; }
    .hljs-name, .hljs-section, .hljs-tag, .hljs-deletion { color: #e06c75; }
    .hljs-string, .hljs-regexp, .hljs-addition, .hljs-meta .hljs-string { color: #98c379; }
    .hljs-number, .hljs-literal, .hljs-type, .hljs-attr, .hljs-attribute,
    .hljs-variable, .hljs-template-variable, .hljs-selector-attr,
    .hljs-selector-pseudo, .hljs-selector-class { color: #d19a66; }
    .hljs-title, .hljs-title.function_, .hljs-built_in, .hljs-title.class_ { color: #61afef; }
    .hljs-symbol, .hljs-bullet, .hljs-link, .hljs-meta, .hljs-selector-id { color: #56b6c2; }
    .hljs-emphasis { font-style: italic; }
    .hljs-strong { font-weight: 600; }

    /* ---- runtime ---- */
    .hostcard { display: flex; flex-wrap: wrap; gap: 8px 18px; background: var(--bx-panel-2, #2b3038);
      border: 1px solid var(--bx-border, #363c45); border-radius: 8px; padding: 10px 14px; margin-bottom: 12px; }
    .hostcard .kv { font-size: 12px; }
    .hostcard .kv b { font-family: var(--bx-mono, monospace); color: var(--bx-accent, #f5a623); }
    .hostcard .kv span { color: var(--bx-muted, #868f9a); }
    .bk { border: 1px solid var(--bx-border, #363c45); border-radius: 7px; margin-bottom: 7px; overflow: hidden; }
    .bk .row { display: grid; grid-template-columns: 16px minmax(110px,1.3fr) 70px repeat(5, minmax(44px, .7fr)) 84px 1.1fr;
      gap: 8px; align-items: center; padding: 6px 10px; cursor: pointer; font-size: 12px; }
    .bk .row.rrow { grid-template-columns: minmax(150px, 2fr) 64px 78px 1fr; }
    .bk .row:hover { background: var(--bx-panel-2, #2b3038); }
    .bk .row .caret { color: var(--bx-muted, #868f9a); transition: transform .1s; }
    .bk.open .row .caret { transform: rotate(90deg); }
    .bk .p { font-family: var(--bx-mono, monospace); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .bk .num { font-family: var(--bx-mono, monospace); text-align: right; }
    .bk .hdr { text-transform: uppercase; font-size: 9.5px; letter-spacing: .05em; color: var(--bx-muted, #868f9a);
      cursor: default; background: var(--bx-panel-2, #2b3038); }
    .bk .hdr:hover { background: var(--bx-panel-2, #2b3038); }
    .state { font-size: 10px; padding: 0 6px; border-radius: 999px; border: 1px solid var(--bx-border); text-align: center; }
    .state.healthy { color: var(--bx-green, #4caf50); border-color: color-mix(in srgb, var(--bx-green) 45%, var(--bx-border)); }
    .state.building { color: var(--bx-accent, #f5a623); }
    .state.failed  { color: var(--bx-red, #ef5350); }
    .state.idle    { color: var(--bx-muted, #868f9a); }
    .lock { font-size: 11px; }
    .detail { border-top: 1px solid var(--bx-border, #363c45); padding: 8px 12px; background: var(--bx-panel-2, #2b3038);
      display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 12px; }
    .detail h5 { margin: 0 0 4px; font-size: 10px; text-transform: uppercase; letter-spacing: .05em; color: var(--bx-muted); }
    .detail .mono { font-family: var(--bx-mono, monospace); font-size: 11px; }
    .nsrow { font-size: 11px; }
    .nsrow .iso { color: var(--bx-green, #4caf50); }
    .nsrow .shared { color: var(--bx-muted, #868f9a); }
    .flowtab { width: 100%; font-size: 11px; }
    .flowtab td { padding: 1px 6px 1px 0; }

`;
