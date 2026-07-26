/**
 * <bx-shell> — the workspace shell: top bar, screen tabs, component sidebar,
 * and a dense, draggable card canvas. Lives in YOUR workspace (component
 * `shell/`), not in xbin's core — open a terminal here and restyle it live.
 *
 * Usage (see root/index.html):
 *
 *   <script type="module" src="/c/shell/bx-shell.js"></script>
 *   <bx-shell name="my workspace">
 *     <bx-frame src="apps/welcome"></bx-frame>   <!-- seeds the first screen -->
 *   </bx-shell>
 *
 * Layout is **persisted per user** via the prefs API (server-side, so it
 * follows you across browsers/devices). Organise work into named **screens**
 * (the tabs at the top) — each screen holds its own set of tiles on a **fixed
 * snappable grid** (48px). Drag a card by its title bar to move it (it snaps to
 * the grid); drag the bottom-right corner to resize it. Positions are absolute,
 * so resizing the browser window never rearranges anything, and a tile is a
 * fixed size — its content scrolls inside instead of stretching the card. Open
 * tiles from the sidebar; close with ✕.
 * The sidebar is collapsible («/»), resizable (drag its right edge), and
 * supports view-only **folders**: ＋ folder, then drag components in to
 * organise them — purely visual grouping, nothing moves on disk; drop a
 * component on empty sidebar space to unfile it. All persisted per user.
 * Unpin a card (⧉) to pop it out as a floating, draggable + resizable window
 * (pin back with ▣); its position/size is saved in the layout like everything
 * else. Floating windows are per-screen.
 * The <bx-frame> children of <bx-shell> seed the first screen on first run;
 * after that your saved layout is the source of truth. Theme tokens come from
 * /vendor/theme.css and can be overridden here.
 */
import { LitElement, html, css, nothing, repeat } from 'lit';
import '/vendor/bx-frame.js';
import '/vendor/bx-grants.js';
import '/vendor/bx-bindings.js';
import './bx-tile-admin.js';
import './bx-org-admin.js';
import '/vendor/bx-dialog.js';

// Fixed snappable grid. Tiles are absolutely positioned + sized in multiples of
// GRID px, so resizing the browser window never reflows them, and a tile's own
// content can't stretch it (fixed size — the frame scrolls inside). GAP is the
// gutter drawn between neighbouring tiles. Tiles must be usable at MIN_W with no
// horizontal scroll (see AGENTS.md).
const GRID = 48;
const GAP = 8;
const DEF_W = 12 * GRID; // default new-tile size: 576×384
const DEF_H = 8 * GRID;
const MIN_W = 4 * GRID; // resize floor: 192×144
const MIN_H = 3 * GRID;
const snap = (v) => Math.max(0, Math.round(v / GRID) * GRID);
const LAYOUT_PREF = 'layout';
const SETTINGS_PREF = 'settings'; // per-user workspace settings (font size, …)

// dragShield lays a transparent full-viewport layer over the page for the
// duration of a pointer drag, so tile <iframe>s can't swallow the pointer when
// the cursor races over them (which otherwise stalls window pointermove until
// the cursor leaves the iframe). Returns a cleanup fn.
function dragShield(cursor = 'grabbing') {
  const el = document.createElement('div');
  el.style.cssText = `position:fixed; inset:0; z-index:2147483647; cursor:${cursor};`;
  document.body.appendChild(el);
  return () => el.remove();
}

const RUNTIME_COLOR = {
  '': 'var(--bx-muted, #8794a1)',
  static: 'var(--bx-muted, #8794a1)',
  go: 'var(--bx-accent, #f5a623)',
  node: 'var(--bx-green, #43a047)',
  python: 'var(--bx-amber, #f2a71b)',
  cgi: 'var(--bx-red, #e5484d)',
};

const uid = () => Math.random().toString(36).slice(2, 9);

// Shared z-order for floating (unpinned) tile windows. Kept below bx-frame's
// terminal pop-ups (which start at 2000) so a terminal always sits on top.
let zTop = 100;

// Convert a legacy column-based tile ({col, height}) to a fixed-grid tile
// ({x,y,w,h}); tiles already in grid form pass through. Old columns become grid
// columns of DEF_W width, their tiles stacked top-to-bottom. Floating tiles get
// a grid home too (used when pinned back).
function gridMigrate(tiles) {
  const nextY = {}; // col → next free y
  return (tiles ?? []).map((o) => {
    if (o.x != null && o.w != null) return o; // already grid
    const { col = 0, height, pinned, ...rest } = o;
    if (o.float) return { x: 0, y: 0, w: DEF_W, h: DEF_H, ...rest };
    const x = col * DEF_W;
    const y = nextY[col] ?? 0;
    const h = snap(Math.max(MIN_H, Math.min(height ?? DEF_H, 16 * GRID)));
    nextY[col] = y + h;
    return { x, y, w: DEF_W, h, ...rest };
  });
}

export class BxShell extends LitElement {
  static properties = {
    name: { type: String },
    _components: { state: true },
    _screens: { state: true }, // [{id, name, tiles: [{path, x, y, w, h, float?:{x,y,w,h,z}}]}]
    _active: { state: true },  // active screen id
    _side: { state: true },    // sidebar: {width, collapsed, folders:[{id,name,open,items}]}
    _dropFolder: { state: true }, // folder id highlighted as a drag target
    _sys: { state: true },        // status footer data (admin-only; null = hidden)
    _isAdmin: { state: true },    // shows the per-tile ⚙ mini-admin (probed via /whoami)
    _adminOrgs: { state: true },  // orgs this human administers (⚙ on their org's tiles)
    _orgsOpen: { state: true },   // the orgs & teams management popover
    _adminFor: { state: true },   // tile path whose mini-admin popover is open
    _dialogs: { state: true },    // shell-rendered dialogs a tile asked for
    _spawnWins: { state: true },  // pop-out windows a tile asked for
    _ctx: { state: true },        // {x,y} background context menu (null = closed)
    _create: { state: true },     // new-tile dialog spec (null = closed)
    _folderEdit: { state: true }, // folder name/icon dialog (null = closed)
    _settings: { state: true },     // per-user workspace settings {fontSize}
    _settingsOpen: { state: true }, // the 🔧 settings dropdown
    _alerts: { state: true },       // workspace health banners (/api/xbin/alerts)
    _dropBefore: { state: true }, // sidebar item being hovered as a drop target
  };

  static styles = css`
    :host {
      display: flex; flex-direction: column; height: 100vh;
      background: var(--bx-bg, #f0f2f5);
      color: var(--bx-text, #33414e);
      font: var(--bx-font, 13px/1.45 -apple-system, "Segoe UI", system-ui, sans-serif);
    }

    /* ---- top bar ---- */
    .top {
      display: flex; align-items: center; gap: 10px;
      background: var(--bx-panel, #fff);
      border-bottom: 1px solid var(--bx-border, #e4e8ed);
      padding: 7px 12px; flex: none;
    }
    .logo { display: flex; align-items: center; gap: 8px; font-weight: 800; font-size: 14px; letter-spacing: .04em; }
    .logo .mark { flex: none; }
    .ws-chip {
      font-size: 11.5px; color: var(--bx-muted, #8794a1);
      background: var(--bx-panel-2, #f7f8fa); border: 1px solid var(--bx-border, #e4e8ed);
      border-radius: 999px; padding: 1px 10px;
    }
    .top .spacer { flex: 1; }
    .top a.chip {
      display: inline-flex; align-items: center; gap: 6px; font-size: 12px;
      color: var(--bx-text, #33414e); text-decoration: none;
      border: 1px solid var(--bx-border, #e4e8ed); border-radius: 6px;
      padding: 3px 10px; background: var(--bx-panel, #fff);
    }
    .top a.chip:hover { background: var(--bx-panel-2, #f7f8fa); }
    .top a.chip .c { width: 7px; height: 7px; border-radius: 2px; }

    /* ---- screen tabs ---- */
    .tabs {
      display: flex; align-items: stretch; gap: 2px; flex: none;
      background: var(--bx-panel-2, #f7f8fa);
      border-bottom: 1px solid var(--bx-border, #e4e8ed);
      padding: 4px 8px 0; overflow-x: auto;
    }
    .tab {
      display: flex; align-items: center; gap: 6px; cursor: pointer;
      font-size: 12.5px; color: var(--bx-muted, #8794a1);
      background: transparent; border: 1px solid transparent; border-bottom: none;
      border-radius: 6px 6px 0 0; padding: 4px 10px; white-space: nowrap; user-select: none;
    }
    .tab.on {
      background: var(--bx-bg, #f0f2f5); color: var(--bx-text, #33414e);
      border-color: var(--bx-border, #e4e8ed); margin-bottom: -1px;
    }
    .tab .x {
      border: 0; background: transparent; color: var(--bx-muted, #8794a1);
      cursor: pointer; font-size: 12px; line-height: 1; padding: 0 1px; opacity: .6;
    }
    .tab .x:hover { opacity: 1; color: var(--bx-red, #e5484d); }
    .tab.add { color: var(--bx-muted, #8794a1); font-weight: 600; }

    /* ---- body ---- */
    .body { display: flex; flex: 1; min-height: 0; }
    aside {
      width: 224px; flex: none; display: flex; flex-direction: column;
      background: var(--bx-panel, #fff);
      padding: 4px 0 0; overflow: hidden;
    }
    .side-scroll { flex: 1; min-height: 0; overflow-y: auto; overflow-x: hidden; padding-bottom: 12px; }
    .sysfoot {
      flex: none; border-top: 1px solid var(--bx-border, #e4e8ed);
      padding: 8px 12px 10px; font-size: 10.5px;
    }
    .sysrow { display: flex; justify-content: space-between; align-items: baseline; margin-top: 5px; }
    .sysrow .l { text-transform: uppercase; letter-spacing: .07em; font-weight: 600;
      color: var(--bx-muted, #8794a1); font-size: 9.5px; }
    .sysrow .v { font-family: var(--bx-mono, monospace); font-size: 10px; color: var(--bx-text, #33414e); }
    .sysrow .v.ok { color: var(--bx-green, #43a047); }
    .sysrow .v.bad { color: var(--bx-red, #e5484d); font-weight: 700; }
    .sysbar { height: 4px; border-radius: 2px; background: var(--bx-panel-2, #f7f8fa);
      overflow: hidden; margin-top: 2px; }
    .sysbar .fill { height: 100%; border-radius: 2px;
      background: var(--bx-accent, #f5a623); transition: width .6s ease; }

    /* ---- xbind build commit (sidebar bottom) ---- */
    .buildfoot {
      flex: none; border-top: 1px solid var(--bx-border, #e4e8ed);
      padding: 6px 12px 8px; display: flex; align-items: baseline; gap: 6px; font-size: 10px;
    }
    .buildfoot .glyph { color: var(--bx-muted, #8794a1); }
    .buildfoot .label { text-transform: uppercase; letter-spacing: .07em; font-weight: 600;
      color: var(--bx-muted, #8794a1); font-size: 9.5px; }
    .buildfoot .ver { margin-left: auto; font-family: var(--bx-mono, monospace);
      color: var(--bx-text, #33414e); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .buildfoot .ver.dirty { color: var(--bx-amber, #f2a71b); }

    /* ---- per-tile mini-admin popover ---- */
    .admin-pop-backdrop { position: fixed; inset: 0; z-index: 2400; }
    .admin-pop {
      position: fixed; z-index: 2500; width: 340px; max-height: 72vh; overflow-y: auto;
      background: var(--bx-panel, #fff); border: 1px solid var(--bx-border, #e4e8ed);
      border-radius: 8px; box-shadow: 0 10px 32px rgba(0, 0, 0, .45);
    }
    /* orgs & teams management popover (org admins + ws admins) — centered. */
    .admin-pop.orgs-pop { left: 50%; top: 9vh; transform: translateX(-50%);
      width: min(560px, 92vw); max-height: 80vh; }
    .orgbtn {
      display: flex; align-items: center; gap: 6px; width: 100%; margin-top: 8px;
      border: 1px solid var(--bx-border, #e4e8ed); background: var(--bx-panel, #fff);
      color: var(--bx-text, #33414e); border-radius: 6px; padding: 4px 8px;
      font: inherit; font-size: 11px; cursor: pointer; text-align: left;
    }
    .orgbtn:hover { background: var(--bx-panel-2, #f7f8fa); }
    .orgbtn .n { margin-left: auto; font-family: var(--bx-mono, monospace);
      font-size: 10px; color: var(--bx-muted, #8794a1); }

    /* ---- tile-spawned pop-out windows ---- */
    .spawn {
      position: fixed; display: flex; flex-direction: column;
      border: 1px solid color-mix(in srgb, var(--bx-border, #e4e8ed) 55%, var(--bx-muted, #8794a1));
      border-radius: var(--bx-radius, 6px); background: var(--bx-panel, #fff);
      box-shadow: 0 0 0 1px rgba(0, 0, 0, .5), 3px 8px 18px rgba(0, 0, 0, .45), 8px 18px 44px rgba(0, 0, 0, .3);
      overflow: hidden; resize: both; min-width: 200px; min-height: 120px;
    }
    .spawn .shead {
      display: flex; align-items: center; gap: 8px; flex: none;
      padding: 5px 6px 5px 10px; cursor: grab; user-select: none; touch-action: none;
      background: var(--bx-panel-2, #f7f8fa); border-bottom: 1px solid var(--bx-border, #e4e8ed);
    }
    .spawn .shead:active { cursor: grabbing; }
    .spawn .stitle { font-size: 12px; font-weight: 600; white-space: nowrap;
      overflow: hidden; text-overflow: ellipsis; }
    .spawn .sfrom { margin-left: auto; font: 10px var(--bx-mono, monospace); color: var(--bx-muted, #8794a1); }
    .spawn .shead button { border: 0; background: transparent; color: var(--bx-muted, #8794a1);
      cursor: pointer; font-size: 12px; padding: 0 2px; }
    .spawn .shead button:hover { color: var(--bx-red, #e5484d); }
    .spawn .sbody { flex: 1; min-height: 0; position: relative; }

    /* ---- background context menu ---- */
    .ctx-backdrop { position: fixed; inset: 0; z-index: 3000; }
    .ctxmenu {
      position: fixed; z-index: 3001; min-width: 190px; padding: 4px;
      background: var(--bx-panel, #fff); border: 1px solid var(--bx-border, #e4e8ed);
      border-radius: 8px; box-shadow: 0 10px 30px rgba(0, 0, 0, .45);
    }
    .ctxmenu button {
      display: block; width: 100%; text-align: left; border: 0; background: transparent;
      color: var(--bx-text, #33414e); font: inherit; font-size: 12.5px;
      padding: 6px 10px; border-radius: 5px; cursor: pointer; white-space: nowrap;
    }
    .ctxmenu button:hover { background: var(--bx-panel-2, #f7f8fa); color: var(--bx-accent, #f5a623); }

    .alerts { position: sticky; top: 0; z-index: 3500; display: flex; flex-direction: column; }
    .alert { display: flex; align-items: center; gap: 8px; padding: 6px 14px; font-size: 12.5px;
      font-weight: 500; color: #fff; }
    .alert .ico { font-size: 14px; }
    .alert.warn { background: #b7791f; }
    .alert.crit { background: #c53030; }
    .wsmenu {
      position: fixed; top: 42px; right: 12px; z-index: 3001; min-width: 220px; padding: 8px 10px;
      background: var(--bx-panel, #fff); border: 1px solid var(--bx-border, #e4e8ed);
      border-radius: 8px; box-shadow: 0 10px 30px rgba(0, 0, 0, .45); font-size: 12px;
    }
    .wsmenu .hd { font-size: 9.5px; letter-spacing: .08em; text-transform: uppercase;
      color: var(--bx-muted, #8794a1); font-weight: 600; margin-bottom: 6px; }
    .wsmenu .row { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
    .wsmenu .fs { display: flex; align-items: center; gap: 6px; }
    .wsmenu .fs b { min-width: 24px; text-align: center; font-variant-numeric: tabular-nums; }
    .wsmenu .step { width: 22px; height: 22px; border: 1px solid var(--bx-border, #e4e8ed);
      border-radius: 5px; background: var(--bx-panel, #fff); color: var(--bx-text, #33414e);
      cursor: pointer; font: inherit; line-height: 1; }
    .wsmenu .step:hover { background: var(--bx-panel-2, #f7f8fa); }

    .group.folder { cursor: grab; }
    .group.folder .ficon { flex: none; font-size: 12px; line-height: 1; margin-right: 1px; }
    .item.dropinto { box-shadow: inset 0 2px 0 var(--bx-accent, #f5a623); }
    aside.collapsed {
      width: 22px; padding: 4px 0;
      border-right: 1px solid var(--bx-border, #e4e8ed);
    }
    aside.collapsed .expand {
      width: 100%; border: 0; background: none; cursor: pointer;
      color: var(--bx-muted, #8794a1); font-size: 12px; padding: 6px 0;
    }
    aside.collapsed .expand:hover { color: var(--bx-accent, #f5a623); }
    .side-handle {
      flex: none; width: 5px; cursor: col-resize;
      background: var(--bx-border, #e4e8ed); opacity: .55;
    }
    .side-handle:hover { opacity: 1; background: var(--bx-accent, #f5a623); }
    .side-top { display: flex; align-items: center; padding: 2px 8px 4px; }
    .mini {
      border: 0; background: none; cursor: pointer; font: inherit;
      font-size: 10.5px; color: var(--bx-muted, #8794a1); padding: 2px 4px;
    }
    .mini:hover { color: var(--bx-accent, #f5a623); }
    .group.folder { cursor: pointer; user-select: none; align-items: center; }
    .group.folder .tri { flex: none; font-size: 9px; width: 10px; }
    .group.folder .fname { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .group.folder .fx {
      margin-left: auto; border: 0; background: none; cursor: pointer;
      color: var(--bx-muted, #8794a1); font-size: 10px; opacity: 0; padding: 0 2px;
    }
    .group.folder:hover .fx { opacity: .7; }
    .group.folder .fx:hover { opacity: 1; color: var(--bx-red, #e5484d); }
    .group.folder.dropping {
      color: var(--bx-accent, #f5a623);
      background: color-mix(in srgb, var(--bx-accent, #f5a623) 12%, transparent);
    }
    .group {
      display: flex; align-items: baseline; gap: 6px; padding: 10px 12px 3px;
      font-size: 10.5px; font-weight: 600; letter-spacing: .08em;
      text-transform: uppercase; color: var(--bx-muted, #8794a1);
    }
    .group .n { font-weight: 500; color: var(--bx-accent, #f5a623); }
    .item {
      display: flex; align-items: center; gap: 8px; padding: 3px 12px 3px 16px;
      cursor: pointer; font-size: 12.5px; white-space: nowrap;
      overflow: hidden; text-overflow: ellipsis;
    }
    .item:hover { background: var(--bx-panel-2, #f7f8fa); }
    .item.open { color: var(--bx-accent, #f5a623); font-weight: 600; }
    .item .c { width: 7px; height: 7px; border-radius: 50%; flex: none; }
    .item .err { color: var(--bx-red, #e5484d); font-size: 10px; }
    .item .rt { margin-left: auto; font-size: 10px; color: var(--bx-muted, #8794a1); }

    main { flex: 1; min-width: 0; overflow: auto; padding: 14px; }
    .grants { margin-bottom: 12px; display: flex; flex-direction: column; gap: 8px; }

    /* ---- fixed snappable grid canvas ---- */
    /* Absolutely-positioned tiles on a GRID-px module; positions never reflow on
       window resize. Height grows to fit the lowest tile — floored to the
       viewport so the dot field always fills the pane (min size set inline). */
    /* Modern technical-slate surface: a fine dot at every 48px grid node (so a
       tile's snapped corner sits on a dot) with a brighter dot every 4th node
       (192px = MIN_W) for a blueprint 'major grid' read. Each layer's dots are
       shifted half a cell so they land on the nodes, not the cell centres. */
    .canvas {
      position: relative;
      background-color: var(--bx-bg, #1b1e24);
      background-image:
        radial-gradient(color-mix(in srgb, var(--bx-muted, #868f9a) 30%, transparent) 1.5px, transparent 1.8px),
        radial-gradient(color-mix(in srgb, var(--bx-muted, #868f9a) 15%, transparent) 1px, transparent 1.4px);
      background-size: 192px 192px, 48px 48px;
      background-position: 96px 96px, 24px 24px;
    }
    .gtile { position: absolute; display: flex; }
    .gtile.dragging { opacity: .85; z-index: 50; }
    .gtile.dragging .card { box-shadow: 0 8px 24px rgba(16,24,40,.22); }
    /* the resize corner */
    .gtile .rz {
      position: absolute; right: 0; bottom: 0; width: 16px; height: 16px;
      cursor: nwse-resize; z-index: 3; touch-action: none;
      background: linear-gradient(135deg, transparent 50%, var(--bx-border, #c7cdd4) 50%);
      border-bottom-right-radius: var(--bx-radius, 6px); opacity: .6;
    }
    .gtile .rz:hover { opacity: 1; }
    .card {
      flex: 1; min-width: 0; min-height: 0; display: flex; flex-direction: column;
      background: var(--bx-panel, #fff); border: 1px solid var(--bx-border, #e4e8ed);
      border-radius: var(--bx-radius, 6px);
      box-shadow: var(--bx-shadow, 0 1px 2px rgba(16,24,40,.05));
      overflow: hidden;
    }
    .card .head {
      flex: none;
      display: flex; align-items: center; gap: 8px; padding: 6px 8px 6px 10px;
      border-bottom: 1px solid var(--bx-border, #e4e8ed);
      background: var(--bx-panel-2, #f7f8fa);
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
      border: 0; background: transparent; color: var(--bx-muted, #8794a1);
      cursor: pointer; font-size: 13px; padding: 0 4px; line-height: 1;
    }
    .card .head button:hover { color: var(--bx-text, #33414e); }
    .card .head button.term { font-family: var(--bx-mono, monospace); font-size: 9.5px;
      font-weight: 700; letter-spacing: -.5px; }
    /* the tile body: fixed height, content scrolls inside — never stretches the card */
    .card .cbody { flex: 1; min-height: 0; overflow: hidden; position: relative; }
    .card .cbody > bx-frame { position: absolute; inset: 0; }
    .empty { color: var(--bx-muted, #8794a1); font-size: 12.5px; padding: 24px; text-align: center; }

    /* ---- floating (unpinned) tile windows ---- */
    .float {
      position: fixed; z-index: 100;
      display: flex; flex-direction: column;
      /* Dual edge so same-colored overlapping windows stay distinct: a border
         brighter than panel seams, ringed by a tight dark outline, over a
         deep ambient shadow. */
      border: 1px solid color-mix(in srgb, var(--bx-border, #e4e8ed) 55%, var(--bx-muted, #8794a1));
      border-radius: var(--bx-radius, 6px);
      background: var(--bx-panel, #fff);
      box-shadow: 0 0 0 1px rgba(0, 0, 0, 0.5),
                  3px 8px 18px rgba(0, 0, 0, 0.45),
                  8px 18px 44px rgba(0, 0, 0, 0.3);
      overflow: hidden; resize: both; min-width: 220px; min-height: 120px;
    }
    .float > .card { border: 0; border-radius: 0; box-shadow: none; }
  `;

  constructor() {
    super();
    this.name = 'workspace';
    this._components = [];
    this._screens = [];
    this._active = '';
    this._side = { width: 224, collapsed: false, folders: [] }; // persisted with the layout
    this._dropFolder = null;
    this._sys = null;
    this._sysPrev = null; // previous traffic sample for req/s + MB/s deltas
    this._isAdmin = false;
    this._adminOrgs = new Set();
    this._orgsOpen = false;
    this._adminFor = null;
    this._adminPos = { x: 0, y: 0 };
    this._dialogs = [];
    this._spawnWins = [];
    this._ctx = null;
    this._create = null;
    this._folderEdit = null;
    this._settings = { fontSize: 13 };
    this._alerts = [];
    this._settingsOpen = false;
    this._dropBefore = null;
    // Tile → shell requests (dialog / pop-out window). Composed events reach
    // window; the detail carries the VERIFIED component + a reply closure.
    this._onSpawn = (e) => this._spawn(e.detail);
    this._onSpawnClose = (e) => this._closeSpawn(e.detail.id);
    this._seeds = [];        // {path, height} from slotted <bx-frame> children
    this._layoutLoaded = false;
    this._saveTimer = null;
    this._onBlur = () => this._raiseFocusedFloat();
  }

  connectedCallback() {
    super.connectedCallback();
    this._load();
    this._loadLayout();
    this._loadSettings();
    this._off = window.xbin?.events.on((e) => {
      if (e.type === 'reload' || e.type === 'grants') this._load();
      if (e.type === 'users') { this._load(); this._probeAdmin(); } // org/team/access changes
    });
    window.addEventListener('blur', this._onBlur);
    this._probeAdmin();
    window.addEventListener('bx-spawn', this._onSpawn);
    window.addEventListener('bx-spawn-close', this._onSpawnClose);
    this._loadSys();
    this._sysTimer = setInterval(() => this._loadSys(), 5000);
    this._loadAlerts();
    this._alertTimer = setInterval(() => this._loadAlerts(), 20000);
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._off?.();
    window.removeEventListener('bx-spawn', this._onSpawn);
    window.removeEventListener('bx-spawn-close', this._onSpawnClose);
    clearInterval(this._sysTimer);
    clearInterval(this._alertTimer);
    window.removeEventListener('blur', this._onBlur);
  }

  firstUpdated() {
    // The grid is absolute-positioned in fixed px, so window resize never
    // reflows it — no ResizeObserver on the canvas.
    const slot = this.renderRoot.querySelector('slot');
    const adopt = () => {
      for (const f of slot.assignedElements()) {
        if (f.tagName !== 'BX-FRAME' || !f.getAttribute('src')) continue;
        const path = f.getAttribute('src');
        if (!this._seeds.some((s) => s.path === path)) {
          this._seeds.push({ path, height: f.getAttribute('height') ?? undefined });
        }
        f.remove();
      }
      this._ensureScreen();
    };
    slot.addEventListener('slotchange', adopt);
    adopt();
  }

  // ---- persistence ----
  async _loadLayout() {
    try {
      const r = await window.xbin?.fetch(`/api/xbin/prefs/${LAYOUT_PREF}`);
      if (r?.ok) {
        const l = await r.json();
        if (Array.isArray(l?.screens) && l.screens.length) {
          // Migrate any old column-based layout to the fixed grid on load.
          this._screens = l.screens.map((s) => ({ ...s, tiles: gridMigrate(s.tiles ?? []) }));
          this._active = l.screens.some((s) => s.id === l.active) ? l.active : l.screens[0].id;
        }
        if (l?.side && typeof l.side === 'object') {
          this._side = { width: 224, collapsed: false, folders: [], ...l.side };
        }
      }
    } catch { /* offline / restarting — fall through to seed */ }
    this._layoutLoaded = true;
    this._ensureScreen();
  }

  // Seed a default screen from the slotted <bx-frame> pins, but only once the
  // saved layout has been consulted and found empty. Lay them out two per row.
  _ensureScreen() {
    if (!this._layoutLoaded || this._screens.length) return;
    const perRow = 2;
    const tiles = this._seeds.map((s, i) => ({
      path: s.path,
      x: (i % perRow) * DEF_W, y: Math.floor(i / perRow) * DEF_H,
      w: DEF_W, h: DEF_H,
    }));
    this._screens = [{ id: uid(), name: 'Home', tiles }];
    this._active = this._screens[0].id;
    this._save();
  }

  _save() {
    clearTimeout(this._saveTimer);
    this._saveTimer = setTimeout(() => {
      window.xbin?.fetch(`/api/xbin/prefs/${LAYOUT_PREF}`, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ screens: this._screens, active: this._active, side: this._side }),
      }).catch(() => { /* best-effort; retried on next change */ });
    }, 400);
  }

  // ---- active screen helpers ----
  get _screen() { return this._screens.find((s) => s.id === this._active); }
  get _tiles() { return this._screen?.tiles ?? []; }

  // Replace the active screen's tiles via fn(copy) → new array, then persist
  // (debounced, so rapid changes like drag/resize coalesce).
  _mutateTiles(fn) {
    if (!this._screen) return;
    const tiles = fn(this._tiles.map((t) => ({ ...t })));
    this._screens = this._screens.map((s) => s.id === this._active ? { ...s, tiles } : s);
    this._save();
  }

  async _load() {
    try {
      const r = await (window.xbin?.fetch ?? fetch)('/api/xbin/components');
      if (r.ok) {
        this._components = await r.json();
        this._pruneOffloaded(); // an offloaded tile disappears from open screens too
      }
    } catch { /* xbind restarting; next event retries */ }
  }

  // Offloaded tiles are archived — not openable; hidden from the sidebar and
  // closed if open. (offloaded / offloaded-full.)
  _offloaded(c) { return c?.state === 'offloaded' || c?.state === 'offloaded-full'; }

  _pruneOffloaded() {
    const off = new Set(this._components.filter((c) => this._offloaded(c)).map((c) => c.path));
    if (!off.size) return;
    let changed = false;
    const screens = this._screens.map((s) => {
      const tiles = s.tiles.filter((t) => !off.has(t.path));
      if (tiles.length !== s.tiles.length) changed = true;
      return { ...s, tiles };
    });
    if (changed) { this._screens = screens; this._save(); }
  }

  get _groups() {
    const filed = new Set((this._side.folders ?? []).flatMap((f) => f.items));
    const g = new Map();
    for (const c of this._components) {
      if (c.path === 'root') continue; // framing root inside root recurses
      if (c.template) continue; // blueprints aren't openable tiles (instantiate via Tile Manager)
      if (this._offloaded(c)) continue; // archived — restore from the admin console
      if (filed.has(c.path)) continue; // shown under its folder instead
      // Org tiles group under their org (o/<org>), wherever the marker sits
      // in the path; everything else under its top-level dir as before.
      const org = this._orgOf(c.path);
      const top = org ? `o/${org}` : (c.path.includes('/') ? c.path.split('/')[0] : 'workspace');
      if (!g.has(top)) g.set(top, []);
      g.get(top).push(c);
    }
    return [...g.entries()].sort(([a], [b]) => a.localeCompare(b));
  }

  // _sideLabel shortens an item's label for its group: org tiles drop
  // everything up to the o/<org>/ marker (the group header carries it), others
  // drop their top-level dir as before. The tooltip keeps the full path.
  _sideLabel(top, path) {
    if (top.startsWith('o/')) {
      const i = path.indexOf(top + '/');
      if (i >= 0) return path.slice(i + top.length + 1);
    }
    return path.includes('/') ? path.slice(path.indexOf('/') + 1) : path;
  }

  // ---- tile-spawned dialogs & pop-out windows (docs/elements.md) ----
  _spawn(d) {
    if (d.kind === 'dialog') {
      // One dialog per tile at a time: a rogue tile can't carpet-bomb modals,
      // and a stack of same-tile modals can't obscure the attribution. The
      // dropped request resolves as a dismiss so the caller's await unblocks.
      if (this._dialogs.some((x) => x.from === d.from)) { d.reply({ button: null, values: {} }); return; }
      this._dialogs = [...this._dialogs, { id: d.id, from: d.from, spec: d.spec, reply: d.reply }];
      return;
    }
    // Cap pop-out windows per tile too (its `closed` resolves immediately if
    // over the cap).
    if (this._spawnWins.filter((x) => x.from === d.from).length >= 6) { d.reply(); return; }
    // window: frame a sub-path of the caller (default) or an explicit component
    // path. Both go through <bx-frame>, so RBAC (frame-token/CanUseTile) still
    // applies; strip any traversal from a caller-supplied sub-path.
    const safe = (p) => String(p || '').replace(/\.\.(\/|$)/g, '').replace(/^\/+|\/+$/g, '');
    const src = d.spec.src ? safe(d.spec.src) : [d.from, safe(d.spec.path)].filter(Boolean).join('/');
    const w = Math.max(200, Math.min(d.spec.width || 480, window.innerWidth - 40));
    const h = Math.max(140, Math.min(d.spec.height || 360, window.innerHeight - 40));
    const win = {
      id: d.id, from: d.from, src, reply: d.reply,
      title: d.spec.title || src,
      x: d.spec.x ?? Math.round((window.innerWidth - w) / 2),
      y: d.spec.y ?? Math.round((window.innerHeight - h) / 2.4),
      w, h, z: ++zTop,
    };
    this._spawnWins = [...this._spawnWins, win];
  }

  _resolveDialog(id, detail) {
    const d = this._dialogs.find((x) => x.id === id);
    if (d) d.reply(detail);
    this._dialogs = this._dialogs.filter((x) => x.id !== id);
  }

  // Close a spawned window (by the tile, its ✕, or the tile unmounting): tell
  // the caller its window closed (resolves handle.closed) and drop it.
  _closeSpawn(id) {
    const w = this._spawnWins.find((x) => x.id === id);
    if (!w) return;
    w.reply();
    this._spawnWins = this._spawnWins.filter((x) => x.id !== id);
  }

  _spawnFront(id) {
    const w = this._spawnWins.find((x) => x.id === id);
    if (w) { w.z = ++zTop; this.requestUpdate(); }
  }

  _spawnDragStart(e, id) {
    if (e.button !== 0 || e.target.closest('button')) return;
    e.preventDefault();
    this._spawnFront(id);
    const w = this._spawnWins.find((x) => x.id === id);
    if (!w) return;
    const ox = e.clientX - w.x, oy = e.clientY - w.y;
    const unshield = dragShield();
    const move = (ev) => {
      w.x = Math.max(-w.w + 60, Math.min(ev.clientX - ox, window.innerWidth - 40));
      w.y = Math.max(0, Math.min(ev.clientY - oy, window.innerHeight - 24));
      this.requestUpdate();
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      unshield();
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  }

  _spawnTemplate(w) {
    return html`
      <div class="spawn" style="left:${w.x}px; top:${w.y}px; width:${w.w}px; height:${w.h}px; z-index:${w.z}"
           @pointerdown=${() => this._spawnFront(w.id)}>
        <div class="shead" @pointerdown=${(e) => this._spawnDragStart(e, w.id)}>
          <span class="stitle">${w.title}</span>
          <span class="sfrom">${w.from}</span>
          <button title="close" @click=${() => this._closeSpawn(w.id)}>✕</button>
        </div>
        <div class="sbody">
          <bx-frame src=${w.src} height="100%" no-edit style="position:absolute; inset:0"></bx-frame>
        </div>
      </div>`;
  }

  // ---- background context menu ----
  _openCtx(e) {
    // Only for the shell's own background — right-clicks inside a tile iframe
    // never reach here, and we skip our own cards/controls so their native
    // menu still works.
    if (e.target.closest('.card, button, input, select, a, bx-frame')) return;
    e.preventDefault();
    this._ctx = { x: Math.min(e.clientX, window.innerWidth - 200), y: Math.min(e.clientY, window.innerHeight - 160) };
  }
  _ctxDo(fn) { this._ctx = null; fn(); }

  // New-tile dialog: names a static tile under apps/, creates it, opens it on
  // the current screen. Re-opens with an inline error on failure.
  _newTileDialog(name = '', message = 'Creates a static tile under apps/ and opens it here.') {
    this._create = {
      title: 'Create a new tile', message,
      fields: [{ name: 'name', label: 'Tile name', value: name, placeholder: 'My Tile' }],
      buttons: [{ label: 'Cancel', value: null }, { label: 'Create', value: 'create', primary: true }],
    };
  }
  async _onNewTile({ button, values }) {
    this._create = null;
    if (button !== 'create') return;
    const name = (values.name || '').trim();
    const slug = name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
    if (!slug) { this._newTileDialog(name, 'Enter a name with letters or digits.'); return; }
    const path = 'apps/' + slug;
    try {
      // Chrome runs as the owner cookie — raw fetch (xbin.fetch would attach a
      // frame token and downgrade). Needs owner/xbin:writer, which the owner is.
      const r = await fetch('/api/xbin/create', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ path, title: name }),
      });
      const d = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error(d.error ?? r.status);
      await this._load();                    // pick up the new component
      if (!this._isOpen(path)) this._toggle(path); // place it on this screen
    } catch (e) {
      this._newTileDialog(name, String(e.message ?? e));
    }
  }

  _isOpen(path) { return this._tiles.some((o) => o.path === path); }

  // ---- sidebar: folders (view-only grouping), collapse, resize ----
  _saveSide(patch) { this._side = { ...this._side, ...patch }; this._save(); }

  // ---- per-user workspace settings (font size, …) ----
  async _loadSettings() {
    try {
      const r = await window.xbin?.fetch(`/api/xbin/prefs/${SETTINGS_PREF}`);
      if (r?.ok) {
        const s = await r.json();
        if (s && typeof s === 'object') this._settings = { fontSize: 13, ...s };
      }
    } catch { /* defaults */ }
    this._applySettings();
  }

  _saveSettings(patch) {
    this._settings = { ...this._settings, ...patch };
    this._applySettings();
    try {
      window.xbin?.fetch(`/api/xbin/prefs/${SETTINGS_PREF}`, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(this._settings),
      });
    } catch { /* transient */ }
  }

  // The whole workspace scales via zoom (13px = 100%). Height/width are
  // compensated so the zoomed shell still fits the viewport exactly.
  //
  // Terminals are the exception: xterm measures its cell size on a canvas
  // (which ignores an ancestor's CSS zoom) but reads pointer coords that DO
  // honor it, so any ambient zoom drifts selection/right-click by that factor,
  // worse the further from the terminal's top-left. bx-terminal detects the
  // ambient zoom itself and counters it (re-scaling through its own font size),
  // so it needs no cooperation here — this event just lets it react instantly
  // instead of waiting for its ResizeObserver to notice the reflow.
  _applySettings() {
    const fs = Math.max(9, Math.min(20, this._settings?.fontSize || 13));
    const z = fs / 13;
    if (Math.abs(z - 1) < 0.01) {
      this.style.zoom = ''; this.style.height = ''; this.style.width = '';
    } else {
      this.style.zoom = String(z);
      this.style.height = `calc(100vh / ${z})`;
      this.style.width = `calc(100vw / ${z})`;
    }
    window.dispatchEvent(new CustomEvent('bx-ambient-zoom', { detail: { zoom: z } }));
  }

  _addFolder() {
    const name = prompt('Folder name');
    if (!name?.trim()) return;
    this._saveSide({ folders: [...this._side.folders, { id: uid(), name: name.trim(), open: true, items: [] }] });
  }
  _folderDialog(f) {
    this._folderEdit = { id: f.id, spec: {
      title: 'Folder', message: 'Name and an optional emoji icon.',
      fields: [
        { name: 'name', label: 'Name', value: f.name },
        { name: 'icon', label: 'Icon (emoji)', value: f.icon || '', placeholder: '📁' },
      ],
      buttons: [{ label: 'Cancel', value: null }, { label: 'Save', value: 'save', primary: true }],
    } };
  }
  _onFolderEdit({ button, values }) {
    const edit = this._folderEdit;
    this._folderEdit = null;
    if (!edit || button !== 'save') return;
    const name = (values.name || '').trim();
    const icon = (values.icon || '').trim().slice(0, 4); // 1–2 emoji
    this._saveSide({ folders: this._side.folders.map((f) =>
      f.id === edit.id ? { ...f, name: name || f.name, icon } : f) });
  }

  // Move a folder before another (drag-reorder in the sidebar).
  _moveFolder(dragId, beforeId) {
    if (dragId === beforeId) return;
    const drag = this._side.folders.find((f) => f.id === dragId);
    if (!drag) return;
    const rest = this._side.folders.filter((f) => f.id !== dragId);
    const i = rest.findIndex((f) => f.id === beforeId);
    const at = i < 0 ? rest.length : i;
    this._saveSide({ folders: [...rest.slice(0, at), drag, ...rest.slice(at)] });
  }

  // Move a component into folderId, positioned before beforePath (or at the end).
  _moveInto(folderId, path, beforePath) {
    this._saveSide({ folders: this._side.folders.map((f) => {
      let items = f.items.filter((p) => p !== path);
      if (f.id === folderId) {
        const i = beforePath ? items.indexOf(beforePath) : -1;
        const at = i < 0 ? items.length : i;
        items = [...items.slice(0, at), path, ...items.slice(at)];
      }
      return { ...f, items };
    }) });
  }

  _dropOnItem(e, targetPath, folderId) {
    e.preventDefault(); e.stopPropagation();
    this._dropBefore = null;
    const path = e.dataTransfer.getData('application/bx-comp');
    if (!path || path === targetPath) return;
    if (folderId) this._moveInto(folderId, path, targetPath);
    else this._fileInto('', path); // dropped on an ungrouped item → unfile
  }
  _deleteFolder(f) { // items just return to their prefix groups
    this._saveSide({ folders: this._side.folders.filter((x) => x.id !== f.id) });
  }
  _toggleFolder(f) {
    this._saveSide({ folders: this._side.folders.map((x) => x.id === f.id ? { ...x, open: !x.open } : x) });
  }
  // File path into folder id ('' = unfile). A component lives in one folder max.
  _fileInto(folderId, path) {
    this._saveSide({
      folders: this._side.folders.map((f) => {
        const items = f.items.filter((p) => p !== path);
        if (f.id === folderId) items.push(path);
        return { ...f, items };
      }),
    });
  }
  _dropOnFolder(e, f) {
    e.preventDefault(); e.stopPropagation();
    this._dropFolder = null;
    const fid = e.dataTransfer.getData('application/bx-folder');
    if (fid) { this._moveFolder(fid, f.id); return; }
    const path = e.dataTransfer.getData('application/bx-comp') || e.dataTransfer.getData('text/plain');
    if (path) this._fileInto(f.id, path);
  }

  // ---- sidebar: system status footer (admin-only; polls /status every 5s) ----
  async _loadAlerts() {
    if (document.hidden) return;
    try {
      const r = await window.xbin?.fetch('/api/xbin/alerts');
      if (r?.ok) this._alerts = (await r.json()).alerts || [];
    } catch { /* transient */ }
  }

  async _loadSys() {
    if (this._side.collapsed || document.hidden) return;
    try {
      // RAW fetch on purpose: the cookie principal is the signed-in admin;
      // xbin.fetch would attach the chrome frame token and downgrade to a
      // non-admin element principal (403 on these endpoints).
      const [st, be, vs] = await Promise.all([
        fetch('/api/xbin/status'),
        fetch('/api/xbin/backends'),
        fetch('/api/xbin/vault-status'),
      ]);
      if (!st.ok) { this._sys = null; return; } // not admin — footer hidden
      const status = await st.json();
      const backends = be.ok ? await be.json() : {};
      const vault = vs.ok ? await vs.json() : null;

      // Rates from cumulative counters (delta between polls).
      let reqRate = 0, mbRate = 0;
      const t = status.traffic ?? {};
      const now = performance.now();
      if (this._sysPrev && now > this._sysPrev.at) {
        const dt = (now - this._sysPrev.at) / 1000;
        reqRate = Math.max(0, (t.reqs - this._sysPrev.reqs) / dt);
        mbRate = Math.max(0, (t.bytesOut - this._sysPrev.bytes) / dt / 1048576);
      }
      // CPU% from jiffy deltas.
      let cpu = null;
      const h = status.host ?? {};
      if (this._sysPrev && h.cpuTotal > this._sysPrev.cpuTotal) {
        cpu = (h.cpuBusy - this._sysPrev.cpuBusy) / (h.cpuTotal - this._sysPrev.cpuTotal);
      }
      this._sysPrev = { at: now, reqs: t.reqs ?? 0, bytes: t.bytesOut ?? 0,
                        cpuBusy: h.cpuBusy ?? 0, cpuTotal: h.cpuTotal ?? 0 };

      const states = Object.values(backends ?? {});
      this._sys = {
        cpu,
        mem: h.memTotal ? 1 - (h.memAvail ?? 0) / h.memTotal : null,
        disk: h.diskTotal ? 1 - (h.diskFree ?? 0) / h.diskTotal : null,
        diskFree: h.diskFree, diskTotal: h.diskTotal,
        services: states.length,
        running: states.filter((b) => b.state === 'healthy').length,
        components: status.components ?? 0,
        vault: vault?.mode ?? null,
        version: status.version || null, // running xbind build commit
        reqRate, mbRate,
      };
    } catch { /* xbind restarting; next tick */ }
  }

  // Probe once whether the signed-in human is an admin (raw fetch → cookie
  // principal). Gates the per-tile ⚙ mini-admin; its APIs 403 otherwise anyway.
  // Org admins (docs/auth.md, orgs & teams) get the ⚙ on THEIR org's tiles:
  // the access + lifecycle sections work for them, admin-only sections just
  // show their 403s.
  async _probeAdmin() {
    try {
      const r = await fetch('/api/xbin/whoami');
      if (r.ok) {
        const d = await r.json();
        this._isAdmin = !!d.admin;
        this._adminOrgs = new Set((d.orgs ?? []).filter((o) => o.admin).map((o) => o.id));
      }
    } catch { /* xbind restarting */ }
  }

  // _orgOf mirrors the server's positional org binding (plans/orgs.md):
  // o/<org>/… or <seg>/o/<org>/….
  _orgOf(path) {
    const s = String(path).split('/');
    if (s.length >= 2 && s[0] === 'o' && s[1]) return s[1];
    if (s.length >= 3 && s[1] === 'o' && s[2]) return s[2];
    return null;
  }

  _canAdminTile(path) {
    if (this._isAdmin) return true;
    const org = this._orgOf(path);
    return !!(org && this._adminOrgs?.has(org));
  }

  _openAdmin(e, path) {
    e.stopPropagation();
    if (this._adminFor === path) { this._adminFor = null; return; }
    const r = e.currentTarget.getBoundingClientRect();
    this._adminPos = {
      x: Math.max(8, Math.min(r.right - 340, window.innerWidth - 356)),
      y: Math.max(8, Math.min(r.bottom + 6, window.innerHeight - 120)),
    };
    this._adminFor = path;
  }

  _bar(label, frac, detail) {
    const pct = frac == null ? null : Math.max(0, Math.min(1, frac));
    return html`
      <div class="sysrow" title=${detail ?? ''}>
        <span class="l">${label}</span>
        <span class="v">${detail ?? (pct == null ? '—' : Math.round(pct * 100) + '%')}</span>
      </div>
      <div class="sysbar"><div class="fill" style="width:${(pct ?? 0) * 100}%"></div></div>`;
  }

  _statusFooter() {
    const s = this._sys;
    if (!s) return nothing;
    const gb = (b) => (b / 1073741824).toFixed(b > 100 * 1073741824 ? 0 : 1);
    const vaultCls = s.vault === 'unsealed' ? 'ok' : s.vault ? 'bad' : '';
    return html`
      <div class="sysfoot">
        ${this._bar('cpu', s.cpu)}
        ${this._bar('memory', s.mem)}
        ${this._bar('disk', s.disk, s.diskTotal ? `${gb(s.diskTotal - s.diskFree)} / ${gb(s.diskTotal)} GB` : null)}
        ${this._bar('services', s.services ? s.running / s.services : 0, `${s.running} / ${s.services} running`)}
        <div class="sysrow"><span class="l">components</span><span class="v">${s.components}</span></div>
        <div class="sysrow"><span class="l">vault</span><span class="v ${vaultCls}">${s.vault ?? '—'}</span></div>
        <div class="sysrow"><span class="l">http</span>
          <span class="v">${s.reqRate.toFixed(s.reqRate < 10 ? 1 : 0)} req/s · ${s.mbRate.toFixed(2)} MB/s</span></div>
      </div>`;
  }

  // ---- xbind build commit (bottom of the sidebar; admin-only) ----
  _buildFoot() {
    const v = this._sys?.version;
    if (!v) return nothing;
    const dirty = v.endsWith('-dirty');
    return html`
      <div class="buildfoot" title="the running xbind daemon's build commit">
        <span class="glyph">⬡</span>
        <span class="label">xbind</span>
        <span class="ver ${dirty ? 'dirty' : ''}">${v}</span>
      </div>`;
  }

  _sideResizeStart(e) {
    if (e.button !== 0) return;
    e.preventDefault();
    const startX = e.clientX, startW = this._side.width || 224;
    const unshield = dragShield('col-resize');
    const move = (ev) => {
      this._side = { ...this._side, width: Math.min(480, Math.max(140, startW + ev.clientX - startX)) };
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      unshield(); this._save();
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  }

  // One sidebar row for a component — used by folders and prefix groups alike.
  _itemTemplate(c, folderId = null, label = null) {
    return html`
      <div class="item ${this._isOpen(c.path) ? 'open' : ''} ${this._dropBefore === c.path ? 'dropinto' : ''}"
           draggable="true"
           title=${c.manifestError ? `${c.path} — manifest error: ${c.manifestError}` : c.path}
           @dragstart=${(e) => { e.dataTransfer.setData('application/bx-comp', c.path);
             e.dataTransfer.setData('text/plain', c.path); e.dataTransfer.effectAllowed = 'move'; }}
           @dragover=${(e) => { if (e.dataTransfer.types.includes('application/bx-comp')) {
             e.preventDefault(); e.stopPropagation(); this._dropBefore = c.path; } }}
           @dragleave=${() => { if (this._dropBefore === c.path) this._dropBefore = null; }}
           @drop=${(e) => this._dropOnItem(e, c.path, folderId)}
           @click=${() => this._toggle(c.path)}>
        <span class="c" style="background:${RUNTIME_COLOR[c.runtime ?? ''] ?? RUNTIME_COLOR['']}"></span>
        <span>${label ?? (c.path.includes('/') ? c.path.slice(c.path.indexOf('/') + 1) : c.path)}</span>
        ${c.manifestError ? html`<span class="err">⚠</span>` : nothing}
        <span class="rt">${c.runtime || ''}</span>
      </div>`;
  }

  _folderTemplate(f) {
    const comps = f.items.map((p) => this._components.find((c) => c.path === p))
      .filter((c) => c && !this._offloaded(c)); // hide archived tiles in folders too
    return html`
      <div class="group folder ${this._dropFolder === f.id ? 'dropping' : ''}" draggable="true"
           title="click to fold · double-click to rename/icon · drag to reorder or drop components in"
           @click=${() => this._toggleFolder(f)}
           @dblclick=${() => this._folderDialog(f)}
           @dragstart=${(e) => { e.dataTransfer.setData('application/bx-folder', f.id);
             e.dataTransfer.effectAllowed = 'move'; e.stopPropagation(); }}
           @dragover=${(e) => { e.preventDefault(); this._dropFolder = f.id; }}
           @dragleave=${() => { if (this._dropFolder === f.id) this._dropFolder = null; }}
           @drop=${(e) => this._dropOnFolder(e, f)}>
        <span class="tri">${f.open ? '▾' : '▸'}</span>
        <span class="ficon">${f.icon || '📁'}</span>
        <span class="fname">${f.name}</span> <span class="n">${comps.length}</span>
        <button class="fx" title="delete folder (components return to their groups)"
                @click=${(e) => { e.stopPropagation(); this._deleteFolder(f); }}>✕</button>
      </div>
      ${f.open ? comps.map((c) => this._itemTemplate(c, f.id)) : nothing}`;
  }

  // Find a free-ish grid spot for a new tile: scan the top row left→right for a
  // gap wide enough for a default tile that overlaps nothing, else drop into a
  // new row below everything. Cheap and deterministic; the user rearranges.
  _freeSpot() {
    const placed = this._tiles.filter((o) => !o.float);
    const overlaps = (x, y) => placed.some((o) =>
      x < o.x + o.w && x + DEF_W > o.x && y < o.y + o.h && y + DEF_H > o.y);
    const viewW = this.renderRoot?.querySelector('main')?.clientWidth || 1200;
    const cols = Math.max(1, Math.floor(viewW / DEF_W));
    for (let row = 0; row < 100; row++) {
      for (let c = 0; c < cols; c++) {
        const x = c * DEF_W, y = row * DEF_H;
        if (!overlaps(x, y)) return { x, y };
      }
    }
    const maxY = placed.reduce((m, o) => Math.max(m, o.y + o.h), 0);
    return { x: 0, y: snap(maxY) };
  }

  _toggle(path) {
    if (this._isOpen(path)) {
      this._mutateTiles((tiles) => tiles.filter((o) => o.path !== path));
      return;
    }
    const { x, y } = this._freeSpot();
    this._mutateTiles((tiles) => [...tiles, { path, x, y, w: DEF_W, h: DEF_H }]);
  }

  _runtimeOf(path) {
    return this._components.find((c) => c.path === path)?.runtime ?? '';
  }

  // ---- screens ----
  _switchScreen(id) { this._active = id; this._save(); }
  _addScreen() {
    const s = { id: uid(), name: `Screen ${this._screens.length + 1}`, tiles: [] };
    this._screens = [...this._screens, s];
    this._active = s.id;
    this._save();
  }
  _renameScreen(id) {
    const s = this._screens.find((x) => x.id === id);
    const name = prompt('Screen name:', s?.name ?? '');
    if (name == null || !name.trim()) return;
    this._screens = this._screens.map((x) => x.id === id ? { ...x, name: name.trim() } : x);
    this._save();
  }
  _closeScreen(id, ev) {
    ev.stopPropagation();
    if (this._screens.length <= 1) return; // keep at least one
    const s = this._screens.find((x) => x.id === id);
    if (s.tiles.length && !confirm(`Close screen "${s.name}" and its ${s.tiles.length} tile(s)?`)) return;
    const remaining = this._screens.filter((x) => x.id !== id);
    this._screens = remaining;
    if (this._active === id) this._active = remaining[0].id;
    this._save();
  }

  // ---- grid drag + resize ----
  // Both manipulate the tile's DOM directly during the gesture (so its
  // <bx-frame> isn't re-rendered/reloaded mid-move) and commit snapped geometry
  // on release. The tile is a `.gtile` at (x, y) sized (w−GAP)×(h−GAP) — the GAP
  // is the gutter — so committed w/h add GAP back.
  _gtile(path) { return this.renderRoot.querySelector(`.gtile[data-path="${path}"]`); }

  _gridDragStart(ev, path) {
    if (ev.button !== 0 || ev.target.closest('button, select, .rz')) return;
    ev.preventDefault();
    const el = this._gtile(path);
    if (!el) return;
    el.classList.add('dragging');
    const dx = ev.clientX - el.offsetLeft, dy = ev.clientY - el.offsetTop;
    const shield = dragShield('grabbing');
    const move = (e) => {
      el.style.left = snap(Math.max(0, e.clientX - dx)) + 'px';
      el.style.top = snap(Math.max(0, e.clientY - dy)) + 'px';
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      shield();
      el.classList.remove('dragging');
      this._setGeom(path, { x: el.offsetLeft, y: el.offsetTop });
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  }

  _gridResizeStart(ev, path) {
    if (ev.button !== 0) return;
    ev.preventDefault(); ev.stopPropagation();
    const el = this._gtile(path);
    if (!el) return;
    const sx = ev.clientX, sy = ev.clientY;
    const w0 = el.offsetWidth + GAP, h0 = el.offsetHeight + GAP; // full cell size
    const shield = dragShield('nwse-resize');
    const move = (e) => {
      el.style.width = (snap(Math.max(MIN_W, w0 + (e.clientX - sx))) - GAP) + 'px';
      el.style.height = (snap(Math.max(MIN_H, h0 + (e.clientY - sy))) - GAP) + 'px';
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      shield();
      this._setGeom(path, { w: el.offsetWidth + GAP, h: el.offsetHeight + GAP });
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  }

  _setGeom(path, patch) {
    this._mutateTiles((tiles) => tiles.map((o) =>
      o.path === path && !o.float ? { ...o, ...patch } : o));
  }

  _gridCard(o) {
    return html`
      <div class="gtile" data-path=${o.path}
           style="left:${o.x}px; top:${o.y}px; width:${o.w - GAP}px; height:${o.h - GAP}px;">
        ${this._cardTemplate(o, 'grid')}
        <div class="rz" title="drag to resize" @pointerdown=${(e) => this._gridResizeStart(e, o.path)}></div>
      </div>`;
  }

  // Content bounds so the (absolute-positioned) canvas scrolls to fit its tiles,
  // floored to the visible pane so the dot field fills it even on a near-empty
  // screen. main's padding (14px) and the grants bar sit above the canvas.
  _gridExtent() {
    const g = this._tiles.filter((o) => !o.float);
    const main = this.renderRoot?.querySelector('main');
    const grants = this.renderRoot?.querySelector('.grants');
    const vw = main ? main.clientWidth - 28 : 0;
    const vh = main ? main.clientHeight - 28 - (grants?.offsetHeight ?? 0) : 0;
    return {
      w: Math.max(vw, g.reduce((m, o) => Math.max(m, o.x + o.w), 0) + GRID),
      h: Math.max(vh, g.reduce((m, o) => Math.max(m, o.y + o.h), 0) + GRID),
    };
  }

  // Open/close the terminal of the card's own frame (the header >_ button —
  // integrated here so tiles don't need the tiny corner button).
  _cardTerm(e) {
    e.stopPropagation();
    e.currentTarget.closest('.card')?.querySelector('bx-frame')?.toggleTerminal?.();
  }

  // kind: 'grid' (on the snappable grid) | 'float' (a free-floating window).
  // Both are fixed-size: the frame fills a fixed body and scrolls inside.
  _cardTemplate(o, kind = 'grid') {
    const floating = kind === 'float';
    const frame = html`<bx-frame src=${o.path} no-edit height="100%"></bx-frame>`;
    return html`
      <div class="card" data-path=${o.path}>
        <div class="head" @pointerdown=${(e) => (floating ? this._floatDragStart(e, o.path) : this._gridDragStart(e, o.path))}>
          <span class="c" style="background:${RUNTIME_COLOR[this._runtimeOf(o.path)] ?? RUNTIME_COLOR['']}"></span>
          <span class="t">${o.path}</span>
          <span class="spacer"></span>
          <button class="term" title="terminal on ${o.path}"
                  @pointerdown=${(e) => e.stopPropagation()}
                  @click=${(e) => this._cardTerm(e)}>&gt;_</button>
          ${this._canAdminTile(o.path) ? html`<button title="tile admin (access · lifecycle · runtime · vault · grants · interfaces · backup · cron)"
                  @pointerdown=${(e) => e.stopPropagation()}
                  @click=${(e) => this._openAdmin(e, o.path)}>⚙</button>` : nothing}
          <button title=${floating ? 'pin back onto the grid' : 'unpin into a floating window'}
                  @click=${() => this._togglePin(o.path)}>${floating ? '▣' : '⧉'}</button>
          <button title="open full page" @click=${() => window.open(`/c/${o.path}/`, '_blank')}>⤢</button>
          <button title="close" @click=${() => this._toggle(o.path)}>✕</button>
        </div>
        <div class="cbody">${frame}</div>
      </div>`;
  }

  // ---- floating (unpinned) windows ----
  // A tile with a `float:{x,y,w,h,z}` is rendered as a viewport-fixed window
  // instead of on the grid; the geometry is part of the tile, so it persists in
  // the saved layout. Pinning/unpinning re-creates the tile's <bx-frame> (moving
  // between two DOM containers) — a brief reload, but any open terminal on it
  // reattaches via bx-frame's session persistence.
  _floatTemplate(o) {
    const f = o.float;
    return html`
      <div class="float" data-path=${o.path}
           style="left:${f.x}px; top:${f.y}px; width:${f.w}px; height:${f.h}px; z-index:${f.z ?? 100};"
           @pointerdown=${() => this._floatFront(o.path)}
           @pointerup=${(e) => this._floatCommit(e, o.path)}>
        ${this._cardTemplate(o, 'float')}
      </div>`;
  }

  _togglePin(path) {
    // Read the current on-screen rect before the mutation re-renders.
    const init = this._initialFloat(path);
    this._mutateTiles((tiles) => tiles.map((o) => {
      if (o.path !== path) return o;
      if (o.float) { const { float, ...rest } = o; return rest; } // pin back to its column
      return { ...o, float: init };                                // unpin → floating window
    }));
  }

  _initialFloat(path) {
    const el = this.renderRoot.querySelector(`.card[data-path="${path}"]`);
    const r = el?.getBoundingClientRect();
    const w = Math.round(Math.min(r?.width || 480, window.innerWidth - 16));
    const h = Math.round(Math.min(r?.height || 340, 520, window.innerHeight - 16));
    const x = Math.max(8, Math.min(Math.round((r?.left ?? 120) + 28), window.innerWidth - w - 8));
    const y = Math.max(8, Math.min(Math.round((r?.top ?? 90) + 20), window.innerHeight - h - 8));
    return { x, y, w, h, z: ++zTop };
  }

  _floatWin(path) { return this.renderRoot.querySelector(`.float[data-path="${path}"]`); }

  // Raise a floating window to the top — but only if it isn't already there, so
  // repeatedly clicking the front window doesn't churn the layout. z is part of
  // the tile, so the stacking order persists.
  _floatFront(path) {
    const floats = this._tiles.filter((o) => o.float);
    if (floats.length < 2) return;
    const o = floats.find((t) => t.path === path);
    if (!o) return;
    const maxZ = Math.max(...floats.map((t) => t.float.z ?? 100));
    if ((o.float.z ?? 100) >= maxZ) return; // already on top
    zTop = maxZ + 1;
    this._setFloat(path, { z: zTop });
  }

  // A click inside a tile's <iframe> focuses it and blurs the top window (the
  // iframe swallows the pointerdown, so .float's own handler can't fire). Walk
  // the shadow roots to the focused iframe and raise its floating window, so
  // clicking anywhere in a window — not just its title bar — brings it forward.
  _raiseFocusedFloat() {
    setTimeout(() => {
      let el = document.activeElement;
      while (el?.shadowRoot?.activeElement) el = el.shadowRoot.activeElement;
      if (el?.tagName !== 'IFRAME') return;
      const win = el.getRootNode()?.host?.closest?.('.float');
      if (win) this._floatFront(win.dataset.path);
    }, 0);
  }

  // Commit a resize (via the CSS resize handle) back into the tile; skip clicks
  // that didn't change the size, so buttons don't churn the layout.
  _floatCommit(e, path) {
    const win = e.currentTarget;
    const o = this._tiles.find((t) => t.path === path);
    if (!o?.float) return;
    if (win.offsetWidth === o.float.w && win.offsetHeight === o.float.h) return;
    this._setFloat(path, { w: win.offsetWidth, h: win.offsetHeight });
  }

  _floatDragStart(ev, path) {
    if (ev.button !== 0 || ev.target.closest('button, select')) return;
    ev.preventDefault();
    this._floatFront(path);
    const win = this._floatWin(path);
    if (!win) return;
    const dx = ev.clientX - win.offsetLeft, dy = ev.clientY - win.offsetTop;
    const shield = dragShield();
    const move = (e) => {
      const x = Math.max(-win.offsetWidth + 60, Math.min(e.clientX - dx, window.innerWidth - 40));
      const y = Math.max(0, Math.min(e.clientY - dy, window.innerHeight - 24));
      win.style.left = x + 'px'; win.style.top = y + 'px';
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      shield();
      this._setFloat(path, { x: win.offsetLeft, y: win.offsetTop });
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  }

  _setFloat(path, patch) {
    this._mutateTiles((tiles) => tiles.map((o) =>
      o.path === path && o.float ? { ...o, float: { ...o.float, ...patch } } : o));
  }

  render() {
    return html`
      ${this._alerts.length ? html`<div class="alerts">
        ${this._alerts.map((a) => html`<div class="alert ${a.level}">
          <span class="ico">${a.level === 'crit' ? '\u26A0' : '\u26A1'}</span>${a.message}</div>`)}
      </div>` : nothing}
      <div class="top">
        <span class="logo">
          <svg class="mark" viewBox="0 0 64 64" width="20" height="20" aria-hidden="true">
            <path d="M18 4H56a4 4 0 0 1 4 4v38L46 60H8a4 4 0 0 1-4-4V18z" fill="var(--bx-accent,#f5a623)"></path>
            <path d="M21 21 43 43M43 21 21 43" stroke="#23272e" stroke-width="9" stroke-linecap="butt"></path>
            <circle cx="53" cy="11" r="2.6" fill="#23272e" opacity=".4"></circle>
            <circle cx="11" cy="53" r="2.6" fill="#23272e" opacity=".4"></circle>
          </svg>
          X/BIN</span>
        <span class="ws-chip">${this.name}</span>
        <span class="spacer"></span>
        <button class="chip" style="cursor:pointer; font:inherit" title="workspace settings (per user)"
                @click=${() => { this._settingsOpen = !this._settingsOpen; }}>🔧</button>
        ${this._settingsOpen ? html`
          <div class="ctx-backdrop" @pointerdown=${() => { this._settingsOpen = false; }}></div>
          <div class="wsmenu">
            <div class="hd">workspace settings</div>
            <div class="row"><span>Font size</span>
              <span class="fs">
                <button class="step" @click=${() => this._saveSettings({ fontSize: (this._settings.fontSize || 13) - 1 })}>−</button>
                <b>${this._settings.fontSize || 13}</b>
                <button class="step" @click=${() => this._saveSettings({ fontSize: (this._settings.fontSize || 13) + 1 })}>+</button>
                ${(this._settings.fontSize || 13) !== 13 ? html`
                  <button class="step" title="reset" style="width:auto; padding:0 6px"
                          @click=${() => this._saveSettings({ fontSize: 13 })}>reset</button>` : nothing}
              </span></div>
          </div>` : nothing}
        <a class="chip" href="/docs/" target="_blank"><span class="c" style="background:var(--bx-green,#43a047)"></span>docs</a>
        <a class="chip" href="/logout" @click=${(e) => { e.preventDefault(); fetch('/logout', { method: 'POST' }).then(() => location.reload()); }}><span class="c" style="background:var(--bx-red,#e5484d)"></span>sign out</a>
      </div>

      <div class="tabs">
        ${this._screens.map((s) => html`
          <div class="tab ${s.id === this._active ? 'on' : ''}"
               @click=${() => this._switchScreen(s.id)}
               @dblclick=${() => this._renameScreen(s.id)}
               title="double-click to rename">
            <span>${s.name}</span>
            ${this._screens.length > 1
              ? html`<button class="x" @click=${(e) => this._closeScreen(s.id, e)}>✕</button>` : nothing}
          </div>`)}
        <div class="tab add" @click=${() => this._addScreen()} title="new screen">+</div>
      </div>

      <div class="body">
        ${this._side.collapsed ? html`
          <aside class="collapsed">
            <button class="expand" title="expand sidebar"
                    @click=${() => this._saveSide({ collapsed: false })}>»</button>
          </aside>` : html`
          <aside style="width:${this._side.width || 224}px"
                 @dragover=${(e) => e.preventDefault()}
                 @drop=${(e) => { // dropped outside any folder → unfile
                   const path = e.dataTransfer.getData('text/plain');
                   if (path) this._fileInto('', path);
                 }}>
            <div class="side-top">
              <button class="mini" title="new folder (view-only grouping — nothing moves on disk)"
                      @click=${() => this._addFolder()}>＋ folder</button>
              <span style="flex:1"></span>
              <button class="mini" title="collapse sidebar"
                      @click=${() => this._saveSide({ collapsed: true })}>«</button>
            </div>
            <div class="side-scroll">
              ${(this._side.folders ?? []).map((f) => this._folderTemplate(f))}
              ${this._groups.map(([top, comps]) => html`
                <div class="group">${top} <span class="n">${comps.length}</span></div>
                ${comps.map((c) => this._itemTemplate(c, null, this._sideLabel(top, c.path)))}
              `)}
              ${this._groups.length === 0 && (this._side.folders ?? []).length === 0
                ? html`<div class="empty">no components yet<br>· mkdir one ·</div>` : nothing}
            </div>
            ${this._adminOrgs?.size ? html`
              <button class="orgbtn" title="manage the orgs you administer (members, teams, access)"
                @click=${() => { this._orgsOpen = !this._orgsOpen; }}>
                ⚑ orgs &amp; teams <span class="n">${this._adminOrgs.size}</span>
              </button>` : nothing}
            ${this._statusFooter()}
            ${this._buildFoot()}
          </aside>
          <div class="side-handle" title="drag to resize"
               @pointerdown=${(e) => this._sideResizeStart(e)}></div>`}
        <main @contextmenu=${(e) => this._openCtx(e)}>
          <div class="grants"><bx-grants></bx-grants><bx-bindings></bx-bindings></div>
          <div class="canvas" style="min-height:${this._gridExtent().h}px; min-width:${this._gridExtent().w}px">
            ${repeat(this._tiles.filter((o) => !o.float), (o) => o.path, (o) => this._gridCard(o))}
          </div>
          ${this._tiles.filter((o) => !o.float).length === 0 && !this._tiles.some((o) => o.float)
            ? html`<div class="empty">empty screen — open a tile from the sidebar</div>` : nothing}
          <slot style="display:none"></slot>
        </main>
      </div>

      ${repeat(this._tiles.filter((o) => o.float), (o) => o.path, (o) => this._floatTemplate(o))}

      ${this._adminFor ? html`
        <div class="admin-pop-backdrop" @click=${() => { this._adminFor = null; }}></div>
        <div class="admin-pop" style="left:${this._adminPos.x}px; top:${this._adminPos.y}px">
          <bx-tile-admin .path=${this._adminFor}></bx-tile-admin>
        </div>` : nothing}

      ${this._orgsOpen ? html`
        <div class="admin-pop-backdrop" @click=${() => { this._orgsOpen = false; }}></div>
        <div class="admin-pop orgs-pop">
          <bx-org-admin ?wsadmin=${this._isAdmin}></bx-org-admin>
        </div>` : nothing}

      ${repeat(this._spawnWins, (w) => w.id, (w) => this._spawnTemplate(w))}
      ${repeat(this._dialogs, (d) => d.id, (d) => html`
        <bx-dialog open .spec=${d.spec} from=${d.from}
          @bx-dialog-resolve=${(e) => this._resolveDialog(d.id, e.detail)}></bx-dialog>`)}

      ${this._ctx ? html`
        <div class="ctx-backdrop" @pointerdown=${() => { this._ctx = null; }}
             @contextmenu=${(e) => { e.preventDefault(); this._ctx = null; }}></div>
        <div class="ctxmenu" style="left:${this._ctx.x}px; top:${this._ctx.y}px">
          <button @click=${() => this._ctxDo(() => this._newTileDialog())}>✦ Create a new tile…</button>
          <button @click=${() => this._ctxDo(() => this._addScreen())}>▦ New screen</button>
          <button @click=${() => this._ctxDo(() => this._addFolder())}>▸ New sidebar folder…</button>
        </div>` : nothing}

      ${this._create ? html`
        <bx-dialog open .spec=${this._create}
          @bx-dialog-resolve=${(e) => this._onNewTile(e.detail)}></bx-dialog>` : nothing}

      ${this._folderEdit ? html`
        <bx-dialog open .spec=${this._folderEdit.spec}
          @bx-dialog-resolve=${(e) => this._onFolderEdit(e.detail)}></bx-dialog>` : nothing}
    `;
  }
}

customElements.define('bx-shell', BxShell);
