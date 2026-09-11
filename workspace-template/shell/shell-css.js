// shell-css.js — the workspace shell's stylesheet, one `css` template the
// shell (bx-shell.js) sets as its static styles. Sections follow the
// element's parts (top bar, screen tabs, org bar, body/sidebar, admin
// popover, spawned windows, context menu, status dots + toasts, the grid
// canvas, floating windows, the phone layout). When a part becomes its own
// element (bx-side, bx-screens, bx-canvas, bx-toasts) its section moves
// with it; until then everything lives here, synchronous — never a <link>.
import { css } from 'lit';

export const shellCss = css`
    :host {
      display: flex; flex-direction: column; height: 100vh;
      background: var(--bx-bg, #1b1e24);
      color: var(--bx-text, #d4d9e0);
      font: var(--bx-font, 13px/1.45 -apple-system, "Segoe UI", system-ui, sans-serif);
    }

    /* ---- top bar ---- */
    .top {
      display: flex; align-items: center; gap: 10px;
      background: var(--bx-panel, #23272e);
      border-bottom: 1px solid var(--bx-border, #363c45);
      padding: 7px 12px; flex: none;
    }
    .logo { display: flex; align-items: center; gap: 8px; font-weight: 800; font-size: 14px; letter-spacing: .04em; }
    .logo .mark { flex: none; }
    .ws-chip {
      font-size: 11.5px; color: var(--bx-muted, #868f9a);
      background: var(--bx-panel-2, #2b3038); border: 1px solid var(--bx-border, #363c45);
      border-radius: 999px; padding: 1px 10px;
    }
    .top .spacer { flex: 1; }
    .top a.chip {
      display: inline-flex; align-items: center; gap: 6px; font-size: 12px;
      color: var(--bx-text, #d4d9e0); text-decoration: none;
      border: 1px solid var(--bx-border, #363c45); border-radius: 6px;
      padding: 3px 10px; background: var(--bx-panel, #23272e);
    }
    .top a.chip:hover { background: var(--bx-panel-2, #2b3038); }
    .top a.chip .c { width: 7px; height: 7px; border-radius: 2px; }

    /* ---- view-as banner: an admin reading the workspace as a user (D64) ---- */
    .viewas {
      display: flex; align-items: center; gap: 12px; flex: none; font-size: 12px;
      background: var(--bx-amber, #f2a71b); color: var(--bx-bg, #1b1e24);
      padding: 5px 12px;
    }
    .viewas b { font-weight: 700; }
    .viewas button.chip {
      margin-left: auto; flex: none; cursor: pointer; font: inherit; font-weight: 600;
      color: var(--bx-bg, #1b1e24); background: transparent;
      border: 1px solid var(--bx-bg, #1b1e24); border-radius: 6px; padding: 2px 10px;
    }

    /* ---- screen tabs ---- */
    .tabs {
      display: flex; align-items: stretch; gap: 2px; flex: none;
      background: var(--bx-panel-2, #2b3038);
      border-bottom: 1px solid var(--bx-border, #363c45);
      padding: 4px 8px 0; overflow-x: auto;
    }
    .tab {
      display: flex; align-items: center; gap: 6px; cursor: pointer;
      font-size: 12.5px; color: var(--bx-muted, #868f9a);
      background: transparent; border: 1px solid transparent; border-bottom: none;
      border-radius: 6px 6px 0 0; padding: 4px 10px; white-space: nowrap; user-select: none;
    }
    .tab.on {
      background: var(--bx-bg, #1b1e24); color: var(--bx-text, #d4d9e0);
      border-color: var(--bx-border, #363c45); margin-bottom: -1px;
    }
    .tab .x {
      border: 0; background: transparent; color: var(--bx-muted, #868f9a);
      cursor: pointer; font-size: 12px; line-height: 1; padding: 0 1px; opacity: .6;
    }
    .tab .x:hover { opacity: 1; color: var(--bx-red, #ef5350); }
    .tab.add { color: var(--bx-muted, #868f9a); font-weight: 600; }
    .tab .dirty { color: var(--bx-amber, #f2a71b); font-size: 10px; }

    /* ---- shared org screen bar (D55): view info, or the draft's save/discard ---- */
    .orgbar {
      position: sticky; top: 0; z-index: 8; display: flex; align-items: center; gap: 8px;
      margin: 0 0 6px; padding: 5px 10px; font-size: 11.5px; border-radius: 6px;
      color: var(--bx-muted, #868f9a); background: var(--bx-panel, #23272e);
      border: 1px solid var(--bx-border, #363c45);
    }
    .orgbar.editing { color: var(--bx-text, #d4d9e0);
      border-color: color-mix(in srgb, var(--bx-accent, #f5a623) 55%, transparent);
      background: color-mix(in srgb, var(--bx-accent, #f5a623) 8%, var(--bx-panel, #23272e)); }
    .orgbar .ico { flex: none; }
    .orgbar .txt { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .orgbar .spacer { flex: 1; }
    .orgbar .muted { font-size: 11px; opacity: .8; }
    .orgbar .newer { color: var(--bx-amber, #f2a71b); white-space: nowrap; }
    .orgbar .newer a { cursor: pointer; text-decoration: underline; }
    .orgbar button.act { font: inherit; font-size: 11.5px; border: 1px solid var(--bx-border, #363c45);
      background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0); border-radius: 5px;
      padding: 2px 9px; cursor: pointer; white-space: nowrap; }
    .orgbar button.act:hover { background: var(--bx-panel-2, #2b3038); }
    .orgbar button.act.go { background: var(--bx-accent, #f5a623); border-color: transparent; color: #23272e; font-weight: 600; }
    .orgbar button.act.go:disabled { opacity: .45; cursor: default; }

    /* ---- body ---- */
    .body { display: flex; flex: 1; min-height: 0; }
    bx-side { width: 224px; flex: none; }


    /* ---- per-tile admin popover (D56): wide, resizable, click-outside closes ---- */
    .admin-pop-backdrop { position: fixed; inset: 0; z-index: 2400; }
    .admin-pop {
      /* sized by its content up to max-height (inline); the user may still
         drag the corner to a fixed size */
      position: fixed; z-index: 2500; display: flex; flex-direction: column; box-sizing: border-box;
      min-width: 300px; min-height: 120px; overflow: hidden; resize: both;
      background: var(--bx-panel, #23272e); border: 1px solid var(--bx-border, #363c45);
      border-radius: 8px; box-shadow: 0 10px 32px rgba(0, 0, 0, .45);
    }
    .admin-pop .ahead {
      flex: none; display: flex; align-items: center; gap: 8px; padding: 6px 10px;
      border-bottom: 1px solid var(--bx-border, #363c45); background: var(--bx-panel-2, #2b3038);
      font: 600 12px var(--bx-mono, ui-monospace, monospace); user-select: none;
    }
    .admin-pop .ahead .t { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .admin-pop .ahead button { border: 0; background: transparent; color: var(--bx-muted, #868f9a);
      font-size: 14px; padding: 2px 6px; cursor: pointer; }
    .admin-pop > bx-tile-admin { flex: 1; min-height: 0; overflow: auto; }
    /* orgs & teams management popover (org admins + ws admins) — centered. */

    /* ---- tile-spawned pop-out windows ---- */
    .spawn {
      position: fixed; display: flex; flex-direction: column;
      border: 1px solid color-mix(in srgb, var(--bx-border, #363c45) 55%, var(--bx-muted, #868f9a));
      border-radius: var(--bx-radius, 6px); background: var(--bx-panel, #23272e);
      box-shadow: 0 0 0 1px rgba(0, 0, 0, .5), 3px 8px 18px rgba(0, 0, 0, .45), 8px 18px 44px rgba(0, 0, 0, .3);
      overflow: hidden; resize: both; min-width: 200px; min-height: 120px;
    }
    .spawn .shead {
      display: flex; align-items: center; gap: 8px; flex: none;
      padding: 5px 6px 5px 10px; cursor: grab; user-select: none; touch-action: none;
      background: var(--bx-panel-2, #2b3038); border-bottom: 1px solid var(--bx-border, #363c45);
    }
    .spawn .shead:active { cursor: grabbing; }
    .spawn .stitle { font-size: 12px; font-weight: 600; white-space: nowrap;
      overflow: hidden; text-overflow: ellipsis; }
    .spawn .sfrom { margin-left: auto; font: 10px var(--bx-mono, monospace); color: var(--bx-muted, #868f9a); }
    .spawn .shead button { border: 0; background: transparent; color: var(--bx-muted, #868f9a);
      cursor: pointer; font-size: 12px; padding: 0 2px; }
    .spawn .shead button:hover { color: var(--bx-red, #ef5350); }
    .spawn .sbody { flex: 1; min-height: 0; position: relative; }

    /* ---- background context menu ---- */
    .ctx-backdrop { position: fixed; inset: 0; z-index: 3000; }

    .alerts { position: sticky; top: 0; z-index: 3500; display: flex; flex-direction: column; }
    .alert { display: flex; align-items: center; gap: 8px; padding: 6px 14px; font-size: 12.5px;
      font-weight: 500; color: #fff; }
    .alert .ico { font-size: 14px; }
    .alert.warn { background: #b7791f; }
    .alert.crit { background: #c53030; }

    /* ---- component status: sidebar dots + tab tint + toasts (tiles → workspace) ---- */
    .tab.st-warn, .tab.st-error { color: var(--st); }
    .tab .stdot { margin-left: 2px; }
    .tab.on.st-warn, .tab.on.st-error { box-shadow: inset 0 -2px 0 var(--st); }
    @media (prefers-reduced-motion: no-preference) {
      .tab.st-warn .stdot, .tab.st-error .stdot { animation: bx-breathe 1.9s ease-in-out infinite; }
    }
    /* transient notifications (xbin.notify) — floating, auto-dismissed, click to close */
    .toasts { position: fixed; right: 14px; bottom: 14px; z-index: 4000;
      display: flex; flex-direction: column; gap: 8px; max-width: 340px; }
    .toast { display: flex; align-items: center; gap: 8px; cursor: pointer;
      padding: 9px 12px; border-radius: 8px; font-size: 12.5px; color: var(--bx-text, #d4d9e0);
      background: var(--bx-panel, #23272e); border: 1px solid var(--bx-border, #363c45);
      border-left: 3px solid var(--st, var(--bx-muted, #868f9a));
      box-shadow: 0 8px 24px rgba(0, 0, 0, .28); animation: bx-toast-in .18s ease-out; }
    .toast .tmsg { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .toast b { font-family: var(--bx-mono, monospace); font-weight: 700; }
    @keyframes bx-toast-in { from { opacity: 0; transform: translateY(8px); } to { opacity: 1; transform: none; } }
    .wsmenu {
      position: fixed; top: 42px; right: 12px; z-index: 3001; min-width: 220px; padding: 8px 10px;
      background: var(--bx-panel, #23272e); border: 1px solid var(--bx-border, #363c45);
      border-radius: 8px; box-shadow: 0 10px 30px rgba(0, 0, 0, .45); font-size: 12px;
    }
    .wsmenu .hd { font-size: 9.5px; letter-spacing: .08em; text-transform: uppercase;
      color: var(--bx-muted, #868f9a); font-weight: 600; margin-bottom: 6px; }
    .wsmenu .row { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
    .wsmenu .fs { display: flex; align-items: center; gap: 6px; }
    .wsmenu .fs b { min-width: 24px; text-align: center; font-variant-numeric: tabular-nums; }
    .wsmenu .step { width: 22px; height: 22px; border: 1px solid var(--bx-border, #363c45);
      border-radius: 5px; background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0);
      cursor: pointer; font: inherit; line-height: 1; }
    .wsmenu .step:hover { background: var(--bx-panel-2, #2b3038); }

    aside.collapsed {
      width: 22px; padding: 4px 0;
      border-right: 1px solid var(--bx-border, #363c45);
    }
    aside.collapsed .expand {
      width: 100%; border: 0; background: none; cursor: pointer;
      color: var(--bx-muted, #868f9a); font-size: 12px; padding: 6px 0;
    }
    aside.collapsed .expand:hover { color: var(--bx-accent, #f5a623); }
    .side-handle {
      flex: none; width: 5px; cursor: col-resize;
      background: var(--bx-border, #363c45); opacity: .55;
    }
    .side-handle:hover { opacity: 1; background: var(--bx-accent, #f5a623); }
    .tab[draggable="true"] { cursor: grab; }
    .tab .ob { font-size: 9px; text-transform: uppercase; letter-spacing: .05em;
      color: var(--bx-accent, #f5a623); border: 1px solid color-mix(in srgb, var(--bx-accent, #f5a623) 45%, transparent);
      border-radius: 999px; padding: 0 5px; }
    .tab .ro { font-size: 10px; color: var(--bx-muted, #868f9a); }
    .wsmenu input { font: inherit; font-size: 11.5px; padding: 3px 7px;
      border: 1px solid var(--bx-border, #363c45); border-radius: 5px;
      background: var(--bx-panel-2, #2b3038); color: var(--bx-text, #d4d9e0); }
    .wsmenu .menu-msg.ok { color: var(--bx-green, #4caf50); font-size: 11px; }
    .wsmenu .menu-msg.bad { color: var(--bx-red, #ef5350); font-size: 11px; }
    .wsmenu button.act { font: inherit; font-size: 11.5px; border: 1px solid var(--bx-border, #363c45);
      background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0); border-radius: 5px;
      padding: 3px 8px; cursor: pointer; }
    .wsmenu button.act:hover { background: var(--bx-panel-2, #2b3038); }


    main { flex: 1; min-width: 0; overflow: auto; padding: 14px; }
    .grants { margin-bottom: 12px; display: flex; flex-direction: column; gap: 8px; }

    /* ======================= mobile layout (≤ 820px) ======================= */
    /* The desktop shell is mouse-driven (fixed sidebar, absolute snap-grid,
       floating windows). On narrow screens we switch interaction models: the
       sidebar becomes an off-canvas drawer, tiles stack full-width (no drag or
       resize — content scrolls inside), and floating windows / terminals become
       full-screen sheets. */
    .ham {
      flex: none; border: 1px solid var(--bx-border, #363c45); background: var(--bx-panel, #23272e);
      color: var(--bx-text, #d4d9e0); font-size: 16px; line-height: 1; cursor: pointer;
      border-radius: 6px; padding: 5px 9px;
    }
    @media (max-width: 820px) {
      .top { gap: 8px; padding: 6px 10px; }
      .top .ws-chip { max-width: 34vw; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
      .tab { padding: 8px 13px; }               /* larger tap targets */
      main { padding: 8px; }
      .grants { margin-bottom: 8px; }

      /* sidebar → off-canvas drawer, slid in over a backdrop */
      .body.mobile bx-side.drawer {
        position: fixed; left: 0; top: 0; bottom: 0; z-index: 3600;
        width: min(290px, 84vw) !important;
        transform: translateX(-100%); transition: transform .2s ease;
        border-right: 1px solid var(--bx-border, #363c45);
        box-shadow: 6px 0 24px rgba(0, 0, 0, .4);
      }
      .body.mobile bx-side.drawer.open { transform: none; }
      .drawer-backdrop { position: fixed; inset: 0; z-index: 3550; background: rgba(0, 0, 0, .45); }

      main { -webkit-touch-callout: none; }

      /* the tile-admin popover → a sheet under the bars (tap above to close) */
      .admin-pop-backdrop { background: rgba(0, 0, 0, .45); }
      .admin-pop {
        left: 0 !important; right: 0; top: 48px !important; bottom: 0;
        width: auto !important; height: auto !important;
        resize: none; border-radius: 12px 12px 0 0; border: 0;
      }
    }
`;

// The ⇄ change-proposal badge: sidebar rows (the shell) and card heads (bx-canvas).
export const prbCss = css`
    /* ⇄ open change-proposal badge (sidebar rows + card headers) */
    .prb { flex: none; margin-left: 4px; padding: 0 4px; border-radius: 3px;
      font-size: 9.5px; line-height: 15px; letter-spacing: .02em;
      color: var(--bx-amber, #f2a71b);
      border: 1px solid color-mix(in srgb, var(--bx-amber, #f2a71b) 45%, transparent);
      background: color-mix(in srgb, var(--bx-amber, #f2a71b) 10%, transparent); }
    button.prb { cursor: pointer; font-family: inherit; }
    button.prb:hover { background: color-mix(in srgb, var(--bx-amber, #f2a71b) 22%, transparent); }
`;

// bx-canvas: the snappable grid, its cards, the floating windows — and their
// mobile shape (stacked cards, full-screen sheets).
export const canvasCss = css`
    :host { display: block; }
    /* view mode of a shared screen: no grab cursor, no resize handles */
    .canvas.ro .card .head { cursor: default; }
    .canvas.ro .rz { display: none; }
    /* ---- fixed snappable grid canvas ---- */
    /* Absolutely-positioned tiles on a GRID-px module; positions never reflow on
       window resize. Height grows to fit the lowest tile — floored to the
       viewport so the dot field always fills the pane (min size set inline). */
    /* Modern technical-slate surface: a crosshair marker at every 48px
       intersection, sitting on the real snap crossing. A tile sits on a 48k node
       but renders GAP (8px) narrower, so four tile corners meet in each gutter
       cross (~48k+44); mask-position 20px lands the SVG's centred crosshair
       there. Drawn on a ::before overlay via mask (not a background image) so the
       colour stays themeable — a data-URI SVG can't read CSS vars, so the SVG is
       only the mask shape and the ::before background supplies the var-driven
       colour. pointer-events:none keeps tile drag / canvas context-menu intact,
       and it paints behind the (later-in-DOM) absolutely-positioned tiles. */
    .canvas { position: relative; background-color: var(--bx-bg, #1b1e24); }
    .canvas::before {
      content: ''; position: absolute; inset: 0; pointer-events: none;
      background: color-mix(in srgb, var(--bx-muted, #868f9a) 25%, transparent);
      -webkit-mask: url("data:image/svg+xml,%3Csvg%20xmlns='http://www.w3.org/2000/svg'%20width='48'%20height='48'%3E%3Cpath%20d='M24%2020.5v7M20.5%2024h7'%20fill='none'%20stroke='white'%20stroke-width='1.3'%20stroke-linecap='round'/%3E%3C/svg%3E") 20px 20px / 48px 48px repeat;
      mask: url("data:image/svg+xml,%3Csvg%20xmlns='http://www.w3.org/2000/svg'%20width='48'%20height='48'%3E%3Cpath%20d='M24%2020.5v7M20.5%2024h7'%20fill='none'%20stroke='white'%20stroke-width='1.3'%20stroke-linecap='round'/%3E%3C/svg%3E") 20px 20px / 48px 48px repeat;
    }
    .gtile { position: absolute; display: flex; }
    .gtile.dragging { opacity: .85; z-index: 50; }
    .gtile.dragging .card { box-shadow: 0 8px 24px rgba(16,24,40,.22); }
    /* the resize corner */
    .gtile .rz {
      position: absolute; right: 0; bottom: 0; width: 16px; height: 16px;
      cursor: nwse-resize; z-index: 3; touch-action: none;
      background: linear-gradient(135deg, transparent 50%, var(--bx-border, #363c45) 50%);
      border-bottom-right-radius: var(--bx-radius, 6px); opacity: .6;
    }
    .gtile .rz:hover { opacity: 1; }
    .card {
      flex: 1; min-width: 0; min-height: 0; display: flex; flex-direction: column;
      background: var(--bx-panel, #23272e); border: 1px solid var(--bx-border, #363c45);
      border-radius: var(--bx-radius, 6px);
      box-shadow: var(--bx-shadow, 0 1px 2px rgba(0, 0, 0, 0.35));
      overflow: hidden;
    }
    .card .head {
      flex: none;
      display: flex; align-items: center; gap: 8px; padding: 6px 8px 6px 10px;
      border-bottom: 1px solid var(--bx-border, #363c45);
      background: var(--bx-panel-2, #2b3038);
      cursor: grab; user-select: none; touch-action: none;
    }
    .card .head:active { cursor: grabbing; }
    .card .head .c { width: 8px; height: 8px; border-radius: 3px; flex: none; }
    .card .head .t {
      font-size: 12px; font-weight: 600; font-family: var(--bx-mono, ui-monospace, monospace);
      overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    }
    .card .head .spacer { flex: 1; }
    .card .head button {
      border: 0; background: transparent; color: var(--bx-muted, #868f9a);
      cursor: pointer; font-size: 13px; padding: 0 4px; line-height: 1;
    }
    .card .head button:hover { color: var(--bx-text, #d4d9e0); }
    .card .head button.term { font-family: var(--bx-mono, monospace); font-size: 9.5px;
      font-weight: 700; letter-spacing: -.5px; }
    /* the tile body: fixed height, content scrolls inside — never stretches the card */
    .card .cbody { flex: 1; min-height: 0; overflow: hidden; position: relative; }
    .card .cbody > bx-frame { position: absolute; inset: 0; }
    .empty { color: var(--bx-muted, #868f9a); font-size: 12.5px; padding: 24px; text-align: center; }

    /* ---- floating (unpinned) tile windows ---- */
    .float {
      position: fixed; z-index: 100;
      display: flex; flex-direction: column;
      /* Dual edge so same-colored overlapping windows stay distinct: a border
         brighter than panel seams, ringed by a tight dark outline, over a
         deep ambient shadow. */
      border: 1px solid color-mix(in srgb, var(--bx-border, #363c45) 55%, var(--bx-muted, #868f9a));
      border-radius: var(--bx-radius, 6px);
      background: var(--bx-panel, #23272e);
      box-shadow: 0 0 0 1px rgba(0, 0, 0, 0.5),
                  3px 8px 18px rgba(0, 0, 0, 0.45),
                  8px 18px 44px rgba(0, 0, 0, 0.3);
      overflow: hidden; resize: both; min-width: 220px; min-height: 120px;
    }
    .float > .card { border: 0; border-radius: 0; box-shadow: none; }

    @media (max-width: 820px) {
      /* tiles → stacked full-width cards (keep each tile's own height inline) */
      .canvas {
        position: static !important; min-width: 0 !important; min-height: 0 !important;
        display: flex; flex-direction: column; gap: 10px; background: none;
      }
      .gtile {
        position: static !important; left: auto !important; top: auto !important;
        width: 100% !important; min-height: 260px; max-height: 82vh;
      }
      .gtile .rz { display: none; }             /* no resize on touch */
      .gtile .card .head { cursor: default; }   /* no drag on touch */
      .card .head button { font-size: 16px; padding: 0 8px; } /* tap targets */
      /* floating windows → full-screen sheets */
      .float {
        position: fixed !important; inset: 0 !important;
        width: auto !important; height: auto !important;
        resize: none !important; border-radius: 0; border: 0;
      }
    }
`;

// Status levels: the colour token, the dot and its breathe — sidebar rows
// (bx-side) and screen tabs (the shell) both carry them.
export const statusCss = css`
    /* level → colour, carried as --st so the dot + breathe ring inherit it */
    .st-ok    { --st: var(--bx-green, #4caf50); }
    .st-info  { --st: #61afef; }
    .st-warn  { --st: var(--bx-amber, #f2a71b); }
    .st-error { --st: var(--bx-red, #ef5350); }
    .stdot { flex: none; display: inline-block; width: 8px; height: 8px; border-radius: 50%;
      background: var(--st, var(--bx-muted, #868f9a)); }
    @keyframes bx-breathe {
      0%, 100% { box-shadow: 0 0 0 0 color-mix(in srgb, var(--st) 65%, transparent); opacity: 1; }
      55% { box-shadow: 0 0 0 4px transparent; opacity: .5; }
    }
`;

// bx-side: the sidebar's tree, filter, folders, footers — and its mobile rows.
export const sideCss = css`
    :host { display: flex; flex-direction: column; background: var(--bx-panel, #23272e); padding: 4px 0 0; overflow: hidden; }
    .root { display: flex; flex-direction: column; flex: 1; min-height: 0; }
    .side-scroll { flex: 1; min-height: 0; overflow-y: auto; overflow-x: hidden; padding-bottom: 12px; }
    .sysfoot {
      flex: none; border-top: 1px solid var(--bx-border, #363c45);
      padding: 8px 12px 10px; font-size: 10.5px;
    }
    .sysrow { display: flex; justify-content: space-between; align-items: baseline; margin-top: 5px; }
    .sysrow .l { text-transform: uppercase; letter-spacing: .07em; font-weight: 600;
      color: var(--bx-muted, #868f9a); font-size: 9.5px; }
    .sysrow .v { font-family: var(--bx-mono, monospace); font-size: 10px; color: var(--bx-text, #d4d9e0); }
    .sysrow .v.ok { color: var(--bx-green, #4caf50); }
    .sysrow .v.bad { color: var(--bx-red, #ef5350); font-weight: 700; }
    .sysbar { height: 4px; border-radius: 2px; background: var(--bx-panel-2, #2b3038);
      overflow: hidden; margin-top: 2px; }
    .sysbar .fill { height: 100%; border-radius: 2px;
      background: var(--bx-accent, #f5a623); transition: width .6s ease; }
    /* ---- xbind build commit (sidebar bottom) ---- */
    .buildfoot {
      flex: none; border-top: 1px solid var(--bx-border, #363c45);
      padding: 6px 12px 8px; display: flex; align-items: baseline; gap: 6px; font-size: 10px;
    }
    .buildfoot .glyph { color: var(--bx-muted, #868f9a); }
    .buildfoot .label { text-transform: uppercase; letter-spacing: .07em; font-weight: 600;
      color: var(--bx-muted, #868f9a); font-size: 9.5px; }
    .buildfoot .ver { margin-left: auto; font-family: var(--bx-mono, monospace);
      color: var(--bx-text, #d4d9e0); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .buildfoot .ver.dirty { color: var(--bx-amber, #f2a71b); }
    .orgbtn {
      display: flex; align-items: center; gap: 6px; width: 100%; margin-top: 8px;
      border: 1px solid var(--bx-border, #363c45); background: var(--bx-panel, #23272e);
      color: var(--bx-text, #d4d9e0); border-radius: 6px; padding: 4px 8px;
      font: inherit; font-size: 11px; cursor: pointer; text-align: left;
    }
    .orgbtn:hover { background: var(--bx-panel-2, #2b3038); }
    .orgbtn .n { margin-left: auto; font-family: var(--bx-mono, monospace);
      font-size: 10px; color: var(--bx-muted, #868f9a); }
    .item { position: relative; }
    .item .stdot { margin-left: 4px; }
    .item.st-warn, .item.st-error { background: color-mix(in srgb, var(--st) 9%, transparent); }
    .item.st-warn::before, .item.st-error::before {
      content: ''; position: absolute; left: 0; top: 3px; bottom: 3px; width: 2px;
      border-radius: 0 2px 2px 0; background: var(--st); }
    .group.folder .stdot { margin-left: 2px; }
    /* breathe for warn/error only; ok/info stay steady; motion-reduced = steady */
    @media (prefers-reduced-motion: no-preference) {
      .item.st-warn .stdot, .item.st-error .stdot,
      .group.folder.st-warn .stdot, .group.folder.st-error .stdot { animation: bx-breathe 1.9s ease-in-out infinite; }
    }
    .group.folder { cursor: grab; }
    .group.folder .ficon { flex: none; font-size: 12px; line-height: 1; margin-right: 1px; }
    .item.dropinto { box-shadow: inset 0 2px 0 var(--bx-accent, #f5a623); }
    .side-top { display: flex; align-items: center; padding: 2px 8px 4px; }
    .mini {
      border: 0; background: none; cursor: pointer; font: inherit;
      font-size: 10.5px; color: var(--bx-muted, #868f9a); padding: 2px 4px;
    }
    .mini:hover { color: var(--bx-accent, #f5a623); }
    /* sidebar filter */
    .side-search { display: flex; align-items: center; gap: 4px; padding: 0 8px 5px; }
    .side-q { flex: 1; min-width: 0; font: inherit; font-size: 11.5px; padding: 3px 7px;
      border: 1px solid var(--bx-border, #363c45); border-radius: 5px;
      background: var(--bx-panel-2, #2b3038); color: var(--bx-text, #d4d9e0); }
    .side-q::placeholder { color: var(--bx-muted, #868f9a); }
    .qx { flex: none; border: 0; background: none; color: var(--bx-muted, #868f9a);
      cursor: pointer; font-size: 11px; padding: 2px 4px; }
    .qx:hover { color: var(--bx-red, #ef5350); }
    /* a screen (tab) parked in the folder tree */
    .item.hid { opacity: .55; }
    .item .hidb { flex: none; font-size: 9px; text-transform: uppercase; letter-spacing: .05em;
      color: var(--bx-muted, #868f9a); border: 1px solid var(--bx-border, #363c45);
      border-radius: 3px; padding: 0 4px; }
    .hidtoggle { display: block; width: calc(100% - 16px); margin: 4px 8px; padding: 3px 6px;
      font-size: 10.5px; color: var(--bx-muted, #868f9a); background: none;
      border: 1px dashed var(--bx-border, #363c45); border-radius: 4px; cursor: pointer; }
    .hidtoggle:hover { color: var(--bx-text, #d4d9e0); }
    .item.screen { color: var(--bx-muted, #868f9a); }
    .item.screen .sic { flex: none; color: var(--bx-accent, #f5a623); font-size: 11px; }
    .item.screen .sname { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .item.screen .pk { flex: none; font-size: 9px; text-transform: uppercase; letter-spacing: .05em;
      color: var(--bx-muted, #868f9a); border: 1px solid var(--bx-border, #363c45); border-radius: 999px; padding: 0 5px; }
    .item.screen .xt { flex: none; border: 0; background: none; cursor: pointer;
      color: var(--bx-muted, #868f9a); font-size: 10px; opacity: 0; padding: 0 3px; }
    .item.screen:hover .xt { opacity: .7; }
    .item.screen .xt:hover { opacity: 1; color: var(--bx-red, #ef5350); }
    .group.folder { cursor: pointer; user-select: none; align-items: center; }
    .group.folder .tri { flex: none; font-size: 9px; width: 10px; }
    .group.folder .fname { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .group.folder .fx {
      margin-left: auto; border: 0; background: none; cursor: pointer;
      color: var(--bx-muted, #868f9a); font-size: 10px; opacity: 0; padding: 0 2px;
    }
    .group.folder:hover .fx { opacity: .7; }
    .group.folder .fx:hover { opacity: 1; color: var(--bx-red, #ef5350); }
    .group.folder.dropping {
      color: var(--bx-accent, #f5a623);
      background: color-mix(in srgb, var(--bx-accent, #f5a623) 12%, transparent);
    }
    .group.folder.ro { cursor: pointer; }
    .group.folder.ro .ficon { opacity: .8; }
    /* ✎ on an owner header: curate that section's shared folders (D55) */
    .group.owner .pen {
      margin-left: auto; border: 0; background: none; cursor: pointer; padding: 0 3px;
      color: var(--bx-muted, #868f9a); font-size: 11px; opacity: 0;
    }
    .group.owner:hover .pen { opacity: .8; }
    .group.owner .pen:hover { opacity: 1; color: var(--bx-accent, #f5a623); }
    .group.owner.editing { color: var(--bx-accent, #f5a623); }
    /* the section's edit bar while curating shared folders */
    .secbar {
      margin: 2px 8px 4px; padding: 5px 7px; border-radius: 6px; font-size: 10.5px;
      border: 1px solid color-mix(in srgb, var(--bx-accent, #f5a623) 55%, transparent);
      background: color-mix(in srgb, var(--bx-accent, #f5a623) 8%, var(--bx-panel, #23272e));
      color: var(--bx-text, #d4d9e0);
    }
    .secbar .l { margin-bottom: 3px; }
    .secbar .newer { color: var(--bx-amber, #f2a71b); margin-bottom: 3px; }
    .secbar .newer a { cursor: pointer; text-decoration: underline; }
    .secbar .r { display: flex; align-items: center; gap: 4px; }
    .secbar .mini.go { color: #23272e; background: var(--bx-accent, #f5a623); border-radius: 4px; padding: 1px 6px; font-weight: 600; }
    .secbar .mini.go:disabled { opacity: .45; cursor: default; }
    .group.owner { cursor: pointer; user-select: none; color: var(--bx-text, #d4d9e0);
      border-top: 1px solid color-mix(in srgb, var(--bx-border, #363c45) 60%, transparent); margin-top: 4px; }
    .group.owner .tri { flex: none; font-size: 9px; width: 10px; }
    .side-owner { display: flex; align-items: center; gap: 4px; padding: 0 8px 5px; }
    .side-owner select { flex: 1; min-width: 0; font: inherit; font-size: 11px; padding: 2px 5px;
      border: 1px solid var(--bx-border, #363c45); border-radius: 5px;
      background: var(--bx-panel-2, #2b3038); color: var(--bx-text, #d4d9e0); }
    .group {
      display: flex; align-items: baseline; gap: 6px; padding: 10px 12px 3px;
      font-size: 10.5px; font-weight: 600; letter-spacing: .08em;
      text-transform: uppercase; color: var(--bx-muted, #868f9a);
    }
    .group .n { font-weight: 500; color: var(--bx-accent, #f5a623); }
    .item {
      display: flex; align-items: center; gap: 8px; padding: 3px 12px 3px 16px;
      cursor: pointer; font-size: 12.5px; white-space: nowrap;
      overflow: hidden; text-overflow: ellipsis;
    }
    .item:hover { background: var(--bx-panel-2, #2b3038); }
    .item.open { color: var(--bx-accent, #f5a623); font-weight: 600; }
    .item .more { flex: none; border: 0; background: none; color: var(--bx-muted, #868f9a);
      font: inherit; font-size: 12px; line-height: 1; padding: 0 3px; opacity: 0; cursor: pointer; }
    .item:hover .more, .item:focus-within .more, :host(.drawer) .item .more { opacity: .7; }
    .item .more:hover { opacity: 1; color: var(--bx-accent, #f5a623); }
    .item .c { width: 7px; height: 7px; border-radius: 50%; flex: none; }
    .item .err { color: var(--bx-red, #ef5350); font-size: 10px; }
    .item .rt { margin-left: auto; font-size: 10px; color: var(--bx-muted, #868f9a); }
    .empty { color: var(--bx-muted, #868f9a); font-size: 12.5px; padding: 24px; text-align: center; }
    @media (max-width: 820px) {
      /* rows: tap-sized, long-press opens the tile menu (no callout/selection) */
      .item { padding: 7px 12px 7px 16px; -webkit-touch-callout: none; user-select: none; }
    }
`;
