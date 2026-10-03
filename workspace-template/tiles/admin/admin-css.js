// admin-css.js — the admin console's shared styles. `base` is what every tab
// element and the router share (filter bar, chips, tables, badges, buttons,
// inputs, links); each tab adds its own slice (`mapCss`, …). Tabs use
// `static styles = [base, <slice>]` — synchronous, no flash of unstyled
// content, never a <link> or a fetch.
//
// Base Two (D184, product-ui §9: the Inform volume — tables, plates,
// concrete and ink): theme.css's tokens only (the page links it and opts
// in, so every rule here is right in light and dark); 2px corners; the
// accent only for primary buttons, selection, the active tab's underline
// and links in prose; status as an icon, a word and its colour.
import { css } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';

export const base = [scrollCss, css`
    :host { display: block; }
    button, input, select, textarea { font: inherit; }
    ::placeholder { color: var(--bx-subtle); opacity: 1; }
    :focus-visible { outline: var(--bx-focus-outline); outline-offset: var(--bx-focus-offset); box-shadow: var(--bx-focus-halo); }
    [hidden] { display: none !important; }

    /* filter bar (scales list views to 1000s of tiles) */
    .filterbar { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin: 4px 0 12px; }
    .filterbar input.q { flex: 1; min-width: 12em; }
    .chips { display: flex; gap: 4px; flex-wrap: wrap; }
    /* a filter chip: a square toggle; on, it takes the selection colour and the accent rule */
    .chip { box-sizing: border-box; min-height: var(--bx-control-h); display: inline-flex; align-items: center; gap: 4px;
      padding: 0 10px; cursor: pointer; user-select: none; border: 1px solid var(--bx-border-strong);
      border-radius: var(--bx-radius); background: var(--bx-panel); color: var(--bx-muted); }
    .chip:hover { background: var(--bx-hover); color: var(--bx-text); }
    .chip.on { background: var(--bx-selection); border-color: var(--bx-accent); color: var(--bx-selection-text); }
    .count-note { font: var(--bx-font-meta); color: var(--bx-muted); white-space: nowrap; font-variant-numeric: tabular-nums; }
    /* a row's disclosure (component rows aren't inside .bk) */
    .caret { display: inline-flex; align-items: center; color: var(--bx-muted); cursor: pointer; }
    .caret:hover { color: var(--bx-text); }
    .body { padding: var(--bx-pad); }
    .err, .notice { display: flex; gap: 6px; align-items: baseline; margin: 4px 0; }
    .err { color: var(--bx-danger); }
    .notice { color: var(--bx-ok); }
    /* workspace alerts: a status tint, its icon, the words */
    .alertbar { display: flex; flex-direction: column; gap: 4px; margin: 0 0 12px; }
    .al { display: flex; gap: 8px; align-items: center; padding: 4px 12px; min-height: var(--bx-row); box-sizing: border-box;
      border: 1px solid var(--bx-border); border-radius: var(--bx-radius); color: var(--bx-text); }
    .al.warn { background: var(--bx-warn-bg); border-color: var(--bx-warn); }
    .al.warn > bx-icon { color: var(--bx-warn); }
    .al.crit { background: var(--bx-danger-bg); border-color: var(--bx-danger); }
    .al.crit > bx-icon { color: var(--bx-danger); }
    .al-x { margin-left: auto; }
    .denied { padding: 20px var(--bx-pad); }
    .denied code { background: var(--bx-code-bg); border: 1px solid var(--bx-border);
      border-radius: var(--bx-radius); padding: 0 4px; font: var(--bx-font-code); }

    h4 { margin: 16px 0 8px; font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro);
         text-transform: uppercase; color: var(--bx-muted); }
    h4:first-child { margin-top: 0; }
    h3 { margin: 16px 0 8px; font: var(--bx-font-title); }
    /* a micro caps label inside running text (a chart's name, a group's head) */
    .lbl { font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted); }
    /* an explanation (a tab's intro, a note under a control): running text,
       13px, set apart by the muted colour the markup gives it (R5: 12px is
       for times and meta) */
    .hint { font: var(--bx-font-ui); }
    /* the counts across the top: plates, the number in ink with tabular figures */
    .cards { display: flex; gap: 8px; flex-wrap: wrap; margin-bottom: 8px; }
    .stat { background: var(--bx-panel); border: 1px solid var(--bx-border);
      border-radius: var(--bx-radius); padding: 8px 12px; min-width: 88px; }
    .stat .n { display: flex; gap: 6px; align-items: center; font: var(--bx-font-title);
      font-variant-numeric: tabular-nums; color: var(--bx-text); }
    .stat .l { font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase;
               color: var(--bx-muted); }
    .stat.warn { border-color: var(--bx-warn); }
    .stat.warn .n { color: var(--bx-warn); }
    /* the vault's line: sealed or unconfigured is the danger outline, plaintext a warning, healthy one quiet line */
    .vault-banner { display: flex; gap: 8px; align-items: center; border-radius: var(--bx-radius);
      padding: 8px 12px; margin-bottom: 12px; font-weight: 600; }
    .vault-banner.sealed { color: var(--bx-danger); background: var(--bx-danger-bg);
      border: 1px solid var(--bx-danger); cursor: pointer; }
    .vault-banner.warn { color: var(--bx-warn); background: var(--bx-warn-bg); cursor: pointer;
      border: 1px solid var(--bx-warn); }
    .vault-banner.ok { background: none; border: 0; padding: 0 2px; color: var(--bx-ok); font-weight: 400; }

    /* tables (product-ui §6): 32px rows, tabular figures, a header ruled in
       ink that sticks under the console's nav, hairlines between rows */
    table { border-collapse: collapse; width: 100%; font-variant-numeric: tabular-nums; }
    th { position: sticky; top: var(--admin-nav-h, 0px); z-index: 1; background: var(--bx-panel);
         text-align: left; font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase;
         color: var(--bx-muted); padding: 8px 8px 4px 0; border-bottom: 2px solid var(--bx-text); white-space: nowrap; }
    td { box-sizing: border-box; height: 32px; padding: 6px 8px 6px 0; border-top: 1px solid var(--bx-border);
         vertical-align: top; }
    /* tables inside a row's detail or a card: no sticky header */
    td th, .detail th, .panel th, .card th, .editor th, details th { position: static; background: none; }
    .mono { font-family: var(--bx-mono); font-size: var(--bx-term-size); }
    /* a control inside a mono cell keeps the UI's type; so do words (a display name beside an id) */
    .mono button, .mono .sans { font-family: var(--bx-sans); font-size: var(--bx-text-size); }
    /* a tag (a role, a grant, a path): square, 20px, mono, a 1px border */
    .pill { box-sizing: border-box; display: inline-flex; align-items: center; gap: 4px; height: 20px; padding: 0 6px;
      margin: 1px 4px 1px 0; vertical-align: middle; font: var(--bx-font-code); white-space: nowrap;
      background: var(--bx-panel-2); border: 1px solid var(--bx-border); border-radius: var(--bx-radius); color: var(--bx-text); }
    /* a badge (a state, a mode): square, 20px, micro caps, a 1px border; a status badge adds its icon */
    .badge { box-sizing: border-box; display: inline-flex; align-items: center; gap: 4px; height: 20px; padding: 0 6px;
      vertical-align: middle; font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase;
      border: 1px solid var(--bx-border-strong); border-radius: var(--bx-radius); color: var(--bx-muted); white-space: nowrap; }
    .badge.ok { color: var(--bx-ok); border-color: var(--bx-ok); }
    .badge.warn { color: var(--bx-warn); border-color: var(--bx-warn); }
    .badge.danger { color: var(--bx-danger); border-color: var(--bx-danger); }
    .badge.info { color: var(--bx-info); border-color: var(--bx-info); }
    /* a state square: 8px, filled in the status colour */
    .dot { display: inline-block; box-sizing: border-box; width: 8px; height: 8px; margin-right: 6px;
      background: var(--bx-muted); vertical-align: baseline; }
    .st-healthy { color: var(--bx-ok); }
    .st-failed  { color: var(--bx-danger); }
    .st-idle    { color: var(--bx-muted); }
    .warn-ic    { color: var(--bx-warn); }

    /* buttons (product-ui §6): secondary by default; .go is the primary,
       .quiet text only (a table row's actions), .rm the danger outline */
    button.act { box-sizing: border-box; min-height: var(--bx-control-h); display: inline-flex; align-items: center;
      justify-content: center; gap: 6px; padding: 4px 11px; border: 1px solid var(--bx-border-strong);
      border-radius: var(--bx-radius); background: var(--bx-panel); color: var(--bx-text); font-weight: 600;
      cursor: pointer; white-space: nowrap; vertical-align: middle; }
    button.act:hover { background: var(--bx-hover); }
    button.act:disabled { opacity: .5; cursor: default; }
    button.go { background: var(--bx-accent); border-color: var(--bx-accent); color: var(--bx-accent-ink); }
    button.go:hover { background: var(--bx-accent-hover); border-color: var(--bx-accent-hover); }
    button.rm { color: var(--bx-danger); border-color: var(--bx-danger); }
    button.act.quiet { background: transparent; border-color: transparent; padding: 4px 6px; }
    button.act.quiet:hover { color: var(--bx-accent); }
    button.act.quiet:disabled:hover { color: inherit; }
    td button.act.quiet, .pill button.act.quiet { min-height: 20px; padding: 0 4px; font-weight: 400; }
    td.acts { white-space: nowrap; }
    /* a step not open yet (its title says why): it stays focusable, so the reason can be read */
    button.act.gated, button.act.gated:hover { color: var(--bx-muted); opacity: .55; cursor: not-allowed; }
    button.act.icon { padding: 0; min-width: var(--bx-control-h); }
    input, select, textarea { box-sizing: border-box; min-height: var(--bx-control-h); padding: 4px 8px;
      border: 1px solid var(--bx-border-strong); border-radius: var(--bx-radius);
      background: var(--bx-panel); color: var(--bx-text); }
    input[type=checkbox], input[type=radio] { min-height: 0; padding: 0; vertical-align: middle; }
    input:disabled, select:disabled, textarea:disabled { opacity: .6; }
    .secret { font-family: var(--bx-mono); }
    code { font: var(--bx-font-code); }
    .muted { color: var(--bx-muted); }
    form.inline { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-top: 8px; }
    /* links: prose and navigation in the link colour; an identifier that
       opens something (a component's code, an org) stays ink, in mono */
    a.link, a[href] { color: var(--bx-link); cursor: pointer; text-decoration: none; }
    a.link:hover, a[href]:hover { text-decoration: underline; }
    a.link.gated { color: var(--bx-muted); cursor: not-allowed; opacity: .55; }
    a.link.gated:hover { text-decoration: none; }
    a.path { color: var(--bx-text); cursor: pointer; text-decoration: none; font-family: var(--bx-mono); }
    a.path:hover { color: var(--bx-accent); text-decoration: underline; }
    /* shared tags + inline problem/flow colours (every tab) */
    .pill.crown { border-color: var(--bx-border-strong); }   /* an admin: the key glyph says so */
    .pill.pol { border-color: var(--bx-danger); color: var(--bx-danger); cursor: help; }
    .pill.hostnet { border-color: var(--bx-warn); color: var(--bx-warn); cursor: help; }
    .flow-deny { color: var(--bx-danger); }
    .flow-allow { color: var(--bx-ok); }
    .err-pill { display: inline-flex; gap: 4px; align-items: center; color: var(--bx-danger); }
    /* editors (typed rows, membership chips, warnings) — every tab */
    .warn-line { display: flex; gap: 6px; align-items: baseline; color: var(--bx-warn); margin-top: 4px; }
    td.warn-line { display: table-cell; }
    /* inline rule / membership chip (IdP-group rules, new-account org rows) */
    .rule { display: inline-flex; gap: 4px; align-items: center; border: 1px solid var(--bx-border);
      border-radius: var(--bx-radius); padding: 2px 6px; margin: 2px 4px 2px 0; }
    .editor { padding: 8px; background: var(--bx-panel-2); border: 1px solid var(--bx-border); border-radius: var(--bx-radius); }
    .editor .orow { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; padding: 2px 0; }
    /* permission-set creator (D57): stored entries in words, rows with an in-words preview */
    .allowlist { list-style: none; margin: 4px 0 0; padding: 0; }
    .allowlist li { margin: 4px 0; }
    .allow-desc { flex-basis: 100%; padding-left: 4px; font: var(--bx-font-meta); }
    .allow-desc .mono { opacity: .75; margin-left: 4px; }
    .seteditor select[name=kind] { min-width: 150px; }
`];

export const mapCss = css`
    /* ---- access map (structure + effective-access matrix) ---- */
    /* a level: a square letter; read, write and terminal in the first three
       chart colours (the ANSI order), so both themes keep their contrast */
    .lv { box-sizing: border-box; display: inline-flex; align-items: center; justify-content: center;
      min-width: 18px; height: 18px; border-radius: var(--bx-radius); font: var(--bx-font-code); font-weight: 700;
      border: 1px solid transparent; }
    .lv-read { color: var(--bx-term-blue); background: color-mix(in srgb, var(--bx-term-blue) 12%, transparent);
      border-color: var(--bx-term-blue); }
    .lv-write { color: var(--bx-term-green); background: color-mix(in srgb, var(--bx-term-green) 12%, transparent);
      border-color: var(--bx-term-green); }
    .lv-terminal { color: var(--bx-term-magenta); background: color-mix(in srgb, var(--bx-term-magenta) 12%, transparent);
      border-color: var(--bx-term-magenta); }
    .lv-none { color: var(--bx-muted); opacity: .5; }
    .pill.lv-read, .pill.lv-write, .pill.lv-terminal { width: auto; height: 20px; }
    .snode { border: 1px solid var(--bx-border); border-left: 2px solid var(--bx-border-strong);
      border-radius: var(--bx-radius); padding: 8px 12px; margin: 8px 0; }
    .snode .shead { font-weight: 600; margin-bottom: 4px;
      display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
    .snode.ws { border-left-color: var(--bx-muted); }
    .snode.org { border-left-color: var(--bx-text); }
    .snode.team { border-left-color: var(--bx-border-strong); margin-left: 18px; position: relative; }
    .snode.team::before { content: ''; position: absolute; left: -12px; top: 14px;
      width: 9px; border-top: 1px solid var(--bx-border); }
    .matrix { border-collapse: collapse; }
    .matrix th { position: static; padding: 4px 6px; text-align: center; border-bottom: 0; background: none; }
    .mgrp { padding: 8px 4px 2px; font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro);
      text-transform: uppercase; color: var(--bx-muted); border-bottom: 1px solid var(--bx-border); }
    .matrix td { height: auto; padding: 2px 4px; }
    .matrix .mtile { padding: 2px 12px 2px 4px; white-space: nowrap; }
    .matrix .mcell { text-align: center; padding: 2px 4px; border-radius: var(--bx-radius); }
    .matrix .mcell.has { cursor: pointer; }
    .matrix .mcell.has:hover { background: var(--bx-hover); }
    .matrix .mown { padding: 2px 12px 2px 4px; white-space: nowrap; }
    .maprow { display: flex; align-items: center; gap: 8px; min-height: var(--bx-row); padding: 2px 4px;
              border-bottom: 1px solid var(--bx-border); }
    .maprow .mono { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
    .maprow:hover { background: var(--bx-hover); }
    .mapsub { margin: 0 0 8px 18px; }
    .mapsub td { height: auto; padding: 2px 8px 2px 0; }
    /* the cell whose grants are open: the selection */
    .matrix .mcell.msel { background: var(--bx-selection); outline: 2px solid var(--bx-accent); outline-offset: -2px; }
`;

// the organisations tab: policy-row tables and synced-membership pills
export const orgsCss = css`
    .flowtab { width: 100%; }
    .flowtab td { height: auto; padding: 2px 8px 2px 0; }
    .pill.sync { border-style: dashed; }   /* membership / role synced from an IdP group */
    /* the org list → one org drill-in; titled panels instead of one flat run */
    .crumbs { margin: 0 0 8px; color: var(--bx-muted); }
    .crumbs a.link { font-weight: 600; }
    .orghead { display: flex; align-items: baseline; gap: 12px; flex-wrap: wrap; margin: 0 0 4px; }
    .orghead .id { font: var(--bx-font-title); }
    .orglist td.n { text-align: right; white-space: nowrap; }
    .orglist th.n { text-align: right; }
    .orglist tr.row:hover td { background: var(--bx-hover); }
    .orglist a.org { font-weight: 700; }
    .panel {
      border: 1px solid var(--bx-border); border-radius: var(--bx-radius);
      background: var(--bx-panel); padding: 8px 12px; margin: 12px 0;
    }
    .panel .ph { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; margin: 0 0 8px; }
    .panel .ph h3 { margin: 0; font: inherit; font-weight: 600; }
    .panel .ph .desc { font: var(--bx-font-meta); color: var(--bx-muted); }
    .panel .ph .sp { flex: 1; }
    .panel.danger { border-color: var(--bx-danger); }
    .panel .foot { font: var(--bx-font-meta); color: var(--bx-muted); margin-top: 8px; }
    .kv { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-top: 4px; }
    .kv > label { color: var(--bx-muted); }
    .empty { color: var(--bx-muted); }
`;

// the users tab: the per-row <details> menu and the accounts table
export const usersCss = css`
    /* per-row "more" menu: a native <details>, no JS state (users table) */
    details.menu { position: relative; display: inline-block; }
    details.menu > summary { list-style: none; box-sizing: border-box; min-height: 20px; display: inline-flex;
      align-items: center; gap: 4px; padding: 0 4px; border: 1px solid transparent; border-radius: var(--bx-radius);
      color: var(--bx-text); font-weight: 600; cursor: pointer; user-select: none; }
    details.menu > summary::-webkit-details-marker { display: none; }
    details.menu > summary:hover, details.menu[open] > summary { color: var(--bx-accent); }
    details.menu > .items { position: absolute; right: 0; top: calc(100% + 4px); z-index: 30; min-width: 14em;
      display: flex; flex-direction: column; padding: 4px; text-align: left; white-space: nowrap;
      background: var(--bx-panel); border: 1px solid var(--bx-border-strong); border-radius: var(--bx-radius);
      box-shadow: var(--bx-shadow-pop); }
    details.menu > .items button { box-sizing: border-box; min-height: var(--bx-row); border: 0; background: none;
      color: inherit; text-align: left; padding: 4px 8px; border-radius: var(--bx-radius); cursor: pointer; }
    details.menu > .items button:hover { background: var(--bx-hover); }
    details.menu > .items button.rm { color: var(--bx-danger); }
    details.menu > .items button:disabled { opacity: .45; cursor: not-allowed; }
    details.menu > .items hr { border: 0; border-top: 1px solid var(--bx-border); margin: 4px 0; }
    /* users table */
    td.user .sub { font: var(--bx-font-code); color: var(--bx-muted); }
    .pill.off { color: var(--bx-danger); border-color: var(--bx-danger); }
    .pill.sync { border-style: dashed; }   /* membership / role synced from an IdP group */
    .never { color: var(--bx-warn); font-weight: 600; }
    .chip .n { opacity: .7; margin-left: 4px; font-variant-numeric: tabular-nums; }
`;

// the runtime group: the code drill-in, the live-stats table, backend detail
export const runtimeCss = css`
    /* ---- code & history ---- */
    .code { display: grid; grid-template-columns: 240px 1fr; gap: 12px; align-items: start; }
    .code .side { min-width: 0; }
    .code .main { min-width: 0; }
    .code .files, .code .hist { border: 1px solid var(--bx-border); border-radius: var(--bx-radius);
      overflow: hidden; margin-bottom: 12px; }
    .code .files .row, .code .hist .row { box-sizing: border-box; min-height: var(--bx-row); padding: 4px 8px;
      cursor: pointer; border-top: 1px solid var(--bx-border); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .code .files .row { font: var(--bx-font-code); line-height: 20px; }
    .code .files .row:first-child, .code .hist .row:first-child { border-top: 0; }
    .code .files .row:hover, .code .hist .row:hover { background: var(--bx-hover); }
    /* the open file or commit: the selection and its accent rule */
    .code .files .row.on, .code .hist .row.on { background: var(--bx-selection); color: var(--bx-selection-text);
      box-shadow: inset 2px 0 0 var(--bx-accent); }
    .code .hist .row .s { font: var(--bx-font-code); color: var(--bx-muted); }
    .code .hist .row.on .s { color: inherit; }
    .code .hd { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
    .code .hd .path { font: var(--bx-font-code); font-weight: 600; }
    .code pre { margin: 0; padding: 8px 12px; background: var(--bx-code-bg);
      border: 1px solid var(--bx-border); border-radius: var(--bx-radius); overflow: auto; max-height: 70vh;
      font: var(--bx-font-code); white-space: pre; color: var(--bx-text); tab-size: 4; }
    /* diff: tint added/removed lines (a line is a direct-child span), keep the syntax colours */
    .code pre.diff > span { display: block; }
    .code pre.diff > .d { background: var(--bx-diff-add-bg); }
    .code pre.diff > .a { background: var(--bx-diff-del-bg); }
    .code pre.diff > .h { color: var(--bx-diff-hunk); background: var(--bx-diff-hunk-bg); }
    .code pre.diff > .fh { color: var(--bx-text); font-weight: 600; }
    .grouphd { font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase;
      color: var(--bx-muted); padding: 4px 8px; background: var(--bx-panel-2); }

    /* ---- live tile stats (resources tab) ---- */
    .strip { display: flex; gap: 8px; align-items: center; margin: 4px 0 8px; flex-wrap: wrap; }
    table.stats th.sortable { cursor: pointer; user-select: none; white-space: nowrap; }
    table.stats th.sortable:hover { color: var(--bx-text); }
    table.stats .strow { cursor: pointer; }
    table.stats .strow:hover td { background: var(--bx-hover); }
    table.stats .strow.on td { background: var(--bx-selection); }
    .stcell { display: flex; flex-direction: column; gap: 2px; min-width: 90px; }
    .stcell .stval { font: var(--bx-font-code); white-space: nowrap; }
    svg.spark { display: block; }
    td.stbig { padding: 8px 0 12px; }
    td.stbig > .stchart, .stbig > .stchart { display: inline-block; margin: 0 18px 4px 0; vertical-align: top; }
    .stchart b { font: var(--bx-font-code); font-weight: 700; color: var(--bx-text); text-transform: none; letter-spacing: 0; }
    table.stats .grouphd { padding: 4px 8px; }
    /* chart series: the six normal ANSI colours in the order blue, magenta,
       cyan, green, yellow, red — a tool palette with contrast in both themes */
    .s0 { stroke: var(--bx-term-blue); } .s1 { stroke: var(--bx-term-magenta); } .s2 { stroke: var(--bx-term-cyan); }
    .s3 { stroke: var(--bx-term-green); } .s4 { stroke: var(--bx-term-yellow); } .s5 { stroke: var(--bx-term-red); }
    .k0 { background: var(--bx-term-blue); } .k1 { background: var(--bx-term-magenta); } .k2 { background: var(--bx-term-cyan); }
    .k3 { background: var(--bx-term-green); } .k4 { background: var(--bx-term-yellow); } .k5 { background: var(--bx-term-red); }
    .key-sq { display: inline-block; width: 8px; height: 8px; margin-right: 4px; }

    /* highlight.js — the syntax tokens (D184), scoped to this shadow */
    .hljs-keyword, .hljs-selector-tag, .hljs-section, .hljs-name, .hljs-variable.language_, .hljs-doctag { color: var(--bx-syn-keyword); }
    .hljs-string, .hljs-regexp, .hljs-char.escape_, .hljs-meta .hljs-string { color: var(--bx-syn-string); }
    .hljs-number, .hljs-literal, .hljs-symbol, .hljs-bullet { color: var(--bx-syn-number); }
    .hljs-comment, .hljs-quote { color: var(--bx-syn-comment); font-style: italic; }
    .hljs-title, .hljs-title.function_ { color: var(--bx-syn-function); }
    .hljs-type, .hljs-title.class_, .hljs-class .hljs-title { color: var(--bx-syn-type); }
    .hljs-attr, .hljs-attribute, .hljs-property, .hljs-selector-attr, .hljs-selector-class,
    .hljs-selector-id, .hljs-selector-pseudo { color: var(--bx-syn-attr); }
    .hljs-built_in, .hljs-meta, .hljs-template-variable, .hljs-variable { color: var(--bx-syn-builtin); }
    .hljs-deletion { color: var(--bx-syn-deletion); }
    .hljs-addition { color: var(--bx-syn-addition); }
    .hljs-params, .hljs-subst, .hljs-punctuation, .hljs-operator { color: var(--bx-text); }
    .hljs-link { color: var(--bx-link); }
    .hljs-emphasis { font-style: italic; }
    .hljs-strong { font-weight: 600; }

    /* ---- runtime ---- */
    .hostcard { display: flex; flex-wrap: wrap; gap: 8px 18px; background: var(--bx-panel);
      border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 8px 12px; margin-bottom: 12px; }
    .hostcard .kv b { font: var(--bx-font-code); font-weight: 700; color: var(--bx-text); }
    .hostcard .kv span { color: var(--bx-muted); }
    .bk { border: 1px solid var(--bx-border); border-radius: var(--bx-radius); margin-bottom: 8px; overflow: hidden; }
    .bk .row { display: grid; grid-template-columns: 16px minmax(110px,1.3fr) 70px repeat(5, minmax(44px, .7fr)) 84px 1.1fr;
      gap: 8px; align-items: center; min-height: 32px; box-sizing: border-box; padding: 4px 12px; cursor: pointer; }
    .bk .row.rrow { grid-template-columns: minmax(150px, 2fr) 64px 78px 1fr; }
    .bk .row:hover { background: var(--bx-hover); }
    .bk .row .caret { color: var(--bx-muted); }
    .bk .p { font: var(--bx-font-code); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .bk .num { font: var(--bx-font-code); text-align: right; }
    .bk .hdr { font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase;
      color: var(--bx-muted); cursor: default; background: var(--bx-panel); border-bottom: 2px solid var(--bx-text); }
    .bk .hdr:hover { background: var(--bx-panel); }
    /* a backend's state: a status badge (its icon and word) */
    .state { box-sizing: border-box; display: inline-flex; align-items: center; gap: 4px; height: 20px; padding: 0 6px;
      font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; white-space: nowrap;
      border-radius: var(--bx-radius); border: 1px solid var(--bx-border-strong); color: var(--bx-muted); }
    .state.healthy { color: var(--bx-ok); border-color: var(--bx-ok); }
    .state.building { color: var(--bx-info); border-color: var(--bx-info); }
    .state.failed  { color: var(--bx-danger); border-color: var(--bx-danger); }
    .state.idle    { color: var(--bx-muted); }
    .lock { display: inline-flex; color: var(--bx-muted); }
    /* how a backend is isolated: a badge — a VM, the namespace sandbox, or the host (a warning) */
    .sbxmode { box-sizing: border-box; display: inline-flex; align-items: center; gap: 4px; height: 20px; padding: 0 6px;
      font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; white-space: nowrap;
      border-radius: var(--bx-radius); border: 1px solid var(--bx-border-strong); color: var(--bx-muted); }
    .sbxmode.vm { color: var(--bx-term-blue); border-color: var(--bx-term-blue); }
    .sbxmode.namespace { color: var(--bx-ok); border-color: var(--bx-ok); }
    .sbxmode.host { color: var(--bx-warn); border-color: var(--bx-warn); }
    .sbxmode.idle { opacity: .6; }
    .detail { border-top: 1px solid var(--bx-border); padding: 8px 12px; background: var(--bx-panel-2);
      display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 12px; }
    .detail h5 { margin: 0 0 4px; font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro);
      text-transform: uppercase; color: var(--bx-muted); }
    .detail .mono { font: var(--bx-font-code); }
    .detail td { height: auto; padding: 2px 8px 2px 0; }
    .nsrow .iso { color: var(--bx-ok); }
    .nsrow .shared { color: var(--bx-muted); }
    .flowtab { width: 100%; }
    .flowtab td { height: auto; padding: 2px 8px 2px 0; }
`;

// runtime → sandboxes (tabs/sandboxes.js, D112)
export const sandboxesCss = css`
    .sbx-assets { display: flex; flex-wrap: wrap; gap: 4px 8px; margin: 8px 0 12px; }
    .sbx-piece { box-sizing: border-box; display: inline-flex; align-items: center; gap: 4px; height: 20px; padding: 0 6px;
      font: var(--bx-font-code); border-radius: var(--bx-radius); border: 1px solid var(--bx-border); }
    .sbx-piece.ok { color: var(--bx-ok); border-color: var(--bx-ok); }
    .sbx-piece.no { color: var(--bx-danger); border-color: var(--bx-danger); }
    .sbx-why { flex-basis: 100%; }
    .sbx-budget { margin: 0 0 12px; }
    .sbx-budget .bar { display: flex; height: 8px; overflow: hidden;
      background: var(--bx-panel-2); border: 1px solid var(--bx-border); border-radius: var(--bx-radius); }
    .sbx-budget[data-over] .bar { border-color: var(--bx-danger); }
    .sbx-budget .lbl { margin-top: 4px; font: inherit; text-transform: none; letter-spacing: 0; color: var(--bx-text); }
    .sbx-budget .lbl b { font: var(--bx-font-code); font-weight: 700; }
    .sbx-by { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 4px; }
    .seg { height: 100%; }
    /* the budget's segments: the chart colours, the ANSI order */
    .c0 { background: var(--bx-term-blue); } .c1 { background: var(--bx-term-magenta); } .c2 { background: var(--bx-term-cyan); }
    .c3 { background: var(--bx-term-green); } .c4 { background: var(--bx-term-yellow); } .c5 { background: var(--bx-term-red); }
    .sbx-policy { margin: 0 0 12px; }
    .sbx-policy.editor { display: flex; flex-direction: column; gap: 8px; max-width: 720px; }
    .sbx-policy label { display: inline-flex; gap: 6px; align-items: center; }
    .sbx-fields { display: flex; flex-wrap: wrap; gap: 8px 16px; }
    .sbx-fields input { width: 90px; }
    .sbx-modes { margin: -4px 0 8px; }
    table.sbx .num { text-align: right; font: var(--bx-font-code); }
    tr.sbx-tile td { background: var(--bx-panel-2); padding-top: 4px; }
    .sbx-mode { box-sizing: border-box; display: inline-flex; align-items: center; gap: 4px; height: 20px; padding: 0 6px;
      font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; white-space: nowrap;
      border-radius: var(--bx-radius); border: 1px solid var(--bx-border-strong); color: var(--bx-muted); }
    .sbx-mode.vm { color: var(--bx-term-blue); border-color: var(--bx-term-blue); }
    .sbx-mode.namespace { color: var(--bx-ok); border-color: var(--bx-ok); }
    .sbx-mode.host { color: var(--bx-warn); border-color: var(--bx-warn); }
    .sbx-stage { box-sizing: border-box; display: inline-flex; align-items: center; gap: 4px; height: 20px; padding: 0 6px;
      font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; white-space: nowrap;
      border-radius: var(--bx-radius); border: 1px solid var(--bx-border-strong); color: var(--bx-muted); }
    .sbx-stage.refused { color: var(--bx-warn); border-color: var(--bx-warn); }
    .sbx-stage.start, .sbx-stage.exit, .sbx-stage.health { color: var(--bx-danger); border-color: var(--bx-danger); }
    .sbx-err { word-break: break-word; }
`;

// runtime → deployments (tabs/deployments.js; D127m, extended 2026-09-28)
export const deploymentsCss = css`
    .dep-note { margin: 0 0 12px; max-width: 820px; }
    .dcard { border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 8px 12px; margin-bottom: 8px;
      background: var(--bx-panel); }
    .dhead { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-bottom: 4px; }
    .dpath { font: var(--bx-font-code); font-weight: 700; margin-right: 4px; }
    .pill.prot { font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase;
      border-color: var(--bx-border-strong); background: none; }
    .dlast { font: var(--bx-font-meta); margin: 2px 0 4px; }
    table.dtab { margin: 4px 0 8px; }
    table.dtab th { position: static; }
    table.dtab td { padding: 4px 12px 4px 0; }
    .dsw { display: inline-flex; gap: 4px; align-items: center; margin-right: 12px; cursor: pointer; }
    .dsw input { margin: 0; }
    .dacts { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
    .dacts button[disabled] { opacity: .5; cursor: not-allowed; }
    .dwhy { font: var(--bx-font-meta); }
`;
