// shell-css.js — the workspace shell's stylesheet, one `css` template the
// shell (bx-shell.js) sets as its static styles. Sections follow the
// element's parts (top bar, screen tabs, org bar, body/sidebar, admin
// popover, spawned windows, context menu, alerts + toasts, the settings
// menu, the grid canvas, floating windows, the phone layout). When a part
// becomes its own element (bx-side, bx-screens, bx-canvas, bx-toasts) its
// section moves with it; until then everything lives here, synchronous —
// never a <link>.
//
// Base Two (D184): every colour, font, radius and shadow is a --bx-* token
// from /vendor/theme.css (the fallbacks are Concrete Night, for a bare
// page); corners are --bx-radius, never pills; status is an icon, a word
// and a colour, never a dot alone; windows wear the window-chrome tokens,
// and the active one — the last one brought to the front — the -active set.
import { css } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';

// What every shell shadow root carries: controls in the UI font, the focus
// ring (a ring inside where a row's container clips it), and placeholders
// and selected text from the tokens — document rules don't reach in here.
export const baseCss = css`
    button, input, select, textarea { font: inherit; color: inherit; }
    :focus-visible:not(iframe) {
      outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: var(--bx-focus-offset, 2px);
      box-shadow: var(--bx-focus-halo, 0 0 0 2px #0B0C12);
    }
    ::placeholder { color: var(--bx-subtle, #8E91A2); opacity: 1; }
    ::selection { background: var(--bx-selection, #262C5C); color: var(--bx-selection-text, #E9EAF0); }
    bx-icon { flex: none; }
`;

// Window chrome every title bar shares (product-ui 3): the controls — 28 × 28
// squares with 16 px glyphs, close turning danger under the pointer — and
// the live square: the accent when the saved change is live, hollow while
// it builds, a danger outline (and the word) when the build failed, hollow
// too for a tile that is switched off. The shell's spawned windows and the
// canvas's cards and floats both carry it.
export const chromeCss = css`
    .wc {
      flex: none; display: inline-flex; align-items: center; justify-content: center; box-sizing: border-box;
      width: 28px; height: 28px; padding: 0; margin: 0; cursor: pointer;
      border: 0; border-radius: 0; background: transparent; color: var(--bx-muted, #A3A6B6);
    }
    .wc:hover { background: var(--bx-control-hover, #33353F); color: var(--bx-text, #E9EAF0); }
    .wc.close:hover { background: var(--bx-close-hover, #FF7A7A); color: var(--bx-close-hover-ink, #0B0C12); }
    .wc:focus-visible { outline-offset: calc(-1 * var(--bx-focus-width, 3px)); box-shadow: none; }
    .lsq { flex: none; box-sizing: border-box; width: 8px; height: 8px; background: var(--bx-accent, #8C9BFF); }
    .lsq.building, .lsq.off { background: transparent; border: 1px solid var(--bx-border-strong, #666A7E); }
    .lsq.failed { background: transparent; border: 1px solid var(--bx-danger, #FF7A7A); }
    .lfail { flex: none; font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); font-weight: 600; color: var(--bx-danger, #FF7A7A); }
`;

export const shellCss = [scrollCss, baseCss, chromeCss, css`
    :host {
      display: flex; flex-direction: column; height: 100vh;
      background: var(--bx-bg, #0B0C12);
      color: var(--bx-text, #E9EAF0);
      font: var(--bx-font, 13px/18px system-ui, sans-serif);
    }

    /* ---- top bar (product-ui 2): the wordmark or the workspace's own
       title, then the chips; nothing coloured but focus ---- */
    .top {
      display: flex; align-items: center; gap: 8px; flex: none; box-sizing: border-box;
      height: var(--bx-topbar-h, 40px); padding: 0 12px;
      background: var(--bx-panel, #1F2028);
      border-bottom: 1px solid var(--bx-border, #33353F);
    }
    .logo {
      display: flex; align-items: center; gap: 8px; min-width: 0; white-space: nowrap;
      font: 800 16px/1 var(--bx-display, system-ui, sans-serif); letter-spacing: 0.02em;
    }
    .logo .mark { flex: none; }
    .logo img.mark { width: 20px; height: 20px; object-fit: contain; border-radius: var(--bx-radius, 2px); }
    .ws-title { font: var(--bx-font-title, 600 16px/22px system-ui, sans-serif); letter-spacing: 0; overflow: hidden; text-overflow: ellipsis; }
    .ws-chip {
      display: inline-flex; align-items: center; box-sizing: border-box; height: 20px; padding: 0 6px; white-space: nowrap;
      font: var(--bx-font-micro, 600 11px/14px system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase;
      color: var(--bx-muted, #A3A6B6); border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px);
    }
    .top .spacer { flex: 1; }
    .top a.chip, .top button.chip {
      display: inline-flex; align-items: center; gap: 6px; flex: none; box-sizing: border-box;
      height: var(--bx-control-h, 28px); padding: 0 10px; white-space: nowrap; cursor: pointer; text-decoration: none;
      font: var(--bx-font-ui, 400 13px/18px system-ui, sans-serif); font-weight: 600;
      color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028);
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    }
    .top a.chip:hover, .top button.chip:hover, .top button.chip.on { background: var(--bx-hover, #2A2B34); }

    /* ---- view-as banner: an admin reading the workspace as a user (D64) —
       the elevated field, full width, ink on yellow (product-ui 4, 9) ---- */
    .viewas {
      display: flex; align-items: center; gap: 8px; flex: none; box-sizing: border-box;
      min-height: var(--bx-elevated-h, 28px); padding: 2px 12px;
      background: var(--bx-elevated-bg, #FFD000); color: var(--bx-elevated-ink, #0B0C12);
      font: var(--bx-font-ui, 400 13px/18px system-ui, sans-serif);
    }
    .viewas .msg { flex: 1; min-width: 0; }
    .viewas b { font-weight: 600; }
    .viewas button.chip {
      flex: none; box-sizing: border-box; height: 24px; padding: 0 10px; cursor: pointer;
      font-weight: 600; color: inherit; background: transparent;
      border: 1px solid currentColor; border-radius: var(--bx-radius, 2px);
    }
    .viewas button.chip:hover { background: color-mix(in srgb, currentColor 14%, transparent); }

    /* ---- screen tabs: text tabs, the active one underlined in the accent ---- */
    .tabs {
      display: flex; align-items: stretch; flex: none; box-sizing: border-box;
      height: 32px; padding: 0 4px; overflow-x: auto; overflow-y: hidden;
      background: var(--bx-panel, #1F2028);
      border-bottom: 1px solid var(--bx-border, #33353F);
    }
    .tab {
      position: relative; display: flex; align-items: center; gap: 6px; flex: none; box-sizing: border-box;
      padding: 0 12px; cursor: pointer; white-space: nowrap; user-select: none;
      font: var(--bx-font-ui, 400 13px/18px system-ui, sans-serif); color: var(--bx-muted, #A3A6B6);
      background: transparent; border: 0;
    }
    .tab:hover { color: var(--bx-text, #E9EAF0); background: var(--bx-hover, #2A2B34); }
    .tab.on { color: var(--bx-text, #E9EAF0); box-shadow: inset 0 -2px 0 var(--bx-accent, #8C9BFF); }
    .tab:focus-visible { outline-offset: calc(-1 * var(--bx-focus-width, 3px)); box-shadow: none; }
    .tab .x {
      display: inline-flex; align-items: center; justify-content: center; flex: none;
      width: 20px; height: 20px; padding: 0; margin-right: -6px; cursor: pointer;
      border: 0; border-radius: var(--bx-radius, 2px); background: transparent; color: var(--bx-muted, #A3A6B6);
    }
    .tab .x:hover { background: var(--bx-close-hover, #FF7A7A); color: var(--bx-close-hover-ink, #0B0C12); }
    .tab.add { padding: 0 8px; color: var(--bx-muted, #A3A6B6); }
    .tab .dirty { display: inline-flex; color: var(--bx-warn, #F2994A); }
    .tab[draggable="true"] { cursor: grab; }
    .tab .ob {
      display: inline-flex; align-items: center; box-sizing: border-box; height: 18px; padding: 0 5px;
      font: var(--bx-font-micro, 600 11px/14px system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase;
      color: var(--bx-muted, #A3A6B6); border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    }
    .tab .ro { display: inline-flex; color: var(--bx-muted, #A3A6B6); }

    /* ---- shared org screen strip (D55): a pinned row under the screen tabs —
       what the screen is and who saved it (view), or the draft's save/discard
       (edit). Part of the column, so it never scrolls or floats over content. ---- */
    .orgbar {
      flex: none; display: flex; align-items: center; gap: 8px; box-sizing: border-box; min-height: 36px; padding: 4px 12px;
      font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif);
      color: var(--bx-muted, #A3A6B6); background: var(--bx-panel, #1F2028);
      border-bottom: 1px solid var(--bx-border, #33353F);
    }
    .orgbar.editing { color: var(--bx-text, #E9EAF0); background: var(--bx-info-bg, #30323B); }
    .orgbar b { color: var(--bx-text, #E9EAF0); font-weight: 600; }
    .orgbar .ico { flex: none; display: inline-flex; }
    .orgbar .txt { min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .orgbar .spacer { flex: 1; }
    .orgbar .muted { white-space: nowrap; }
    .orgbar .newer { display: inline-flex; align-items: center; gap: 4px; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
      color: var(--bx-warn, #F2994A); }
    .orgbar .newer a { color: var(--bx-link, #8C9BFF); cursor: pointer; text-decoration: underline; }
    .orgbar button.act {
      flex: none; box-sizing: border-box; height: var(--bx-control-h, 28px); padding: 0 10px; white-space: nowrap; cursor: pointer;
      font: var(--bx-font-ui, 400 13px/18px system-ui, sans-serif); font-weight: 600;
      color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028);
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    }
    .orgbar button.act:hover { background: var(--bx-hover, #2A2B34); }
    .orgbar button.act.go { background: var(--bx-accent, #8C9BFF); border-color: var(--bx-accent, #8C9BFF); color: var(--bx-accent-ink, #0B0C12); }
    .orgbar button.act.go:hover { background: var(--bx-accent-hover, #A9B4FF); border-color: var(--bx-accent-hover, #A9B4FF); }
    .orgbar button.act.go:disabled { opacity: 0.5; cursor: default; }

    /* ---- body ---- */
    .body { display: flex; flex: 1; min-height: 0; }
    bx-side { width: 224px; flex: none; }

    /* ---- per-tile admin popover (D56): wide, resizable, click-outside
       closes; an admin console surface, so it carries the admin part tab ---- */
    .admin-pop-backdrop { position: fixed; inset: 0; z-index: 2400; }
    .admin-pop {
      /* sized by its content up to max-height (inline); the user may still
         drag the corner to a fixed size */
      position: fixed; z-index: 2500; display: flex; flex-direction: column; box-sizing: border-box;
      min-width: 300px; min-height: 120px; overflow: hidden; resize: both;
      background: var(--bx-panel, #1F2028); border: 1px solid var(--bx-window-border-active, #9396A4);
      border-radius: var(--bx-radius, 2px); box-shadow: var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6));
    }
    .admin-pop::before { content: ''; flex: none; height: var(--bx-part-tab-h, 3px); background: var(--bx-part-tab-admin, #FFD000); }
    .admin-pop .ahead {
      flex: none; display: flex; align-items: center; gap: 8px; height: var(--bx-titlebar-h, 28px); padding: 0 0 0 10px;
      color: var(--bx-title-text, #E9EAF0); background: var(--bx-titlebar-active, #262730);
      border-bottom: 1px solid var(--bx-border, #33353F); user-select: none;
    }
    .admin-pop .ahead .t { flex: 1; min-width: 0; display: flex; align-items: center; gap: 8px; overflow: hidden; white-space: nowrap; }
    .admin-pop .ahead .t b { font-weight: 600; }
    .admin-pop .ahead .t .path { min-width: 0; overflow: hidden; text-overflow: ellipsis; font: var(--bx-font-code, 12px/18px ui-monospace, monospace); color: var(--bx-muted, #A3A6B6); }
    .admin-pop > bx-tile-admin { flex: 1; min-height: 0; overflow: auto; }

    /* ---- tile-spawned pop-out windows: window chrome ---- */
    .spawn {
      position: fixed; display: flex; flex-direction: column; box-sizing: border-box;
      border: 1px solid var(--bx-window-border, #666A7E); border-radius: var(--bx-radius, 2px);
      background: var(--bx-panel, #1F2028); box-shadow: var(--bx-shadow-rest, 0 10px 28px rgba(0, 0, 0, 0.4));
      overflow: hidden; resize: both; min-width: 200px; min-height: 120px;
    }
    .spawn.active { border-color: var(--bx-window-border-active, #9396A4); box-shadow: var(--bx-shadow-active, 0 16px 40px rgba(0, 0, 0, 0.55)); }
    .spawn .shead {
      display: flex; align-items: center; gap: 8px; flex: none; height: var(--bx-titlebar-h, 28px); padding: 0 0 0 10px;
      cursor: grab; user-select: none; touch-action: none;
      color: var(--bx-title-text-inactive, #8E91A2); background: var(--bx-titlebar, #1F2028);
      border-bottom: 1px solid var(--bx-border, #33353F);
    }
    .spawn.active .shead { color: var(--bx-title-text, #E9EAF0); background: var(--bx-titlebar-active, #262730); }
    .spawn .shead:active { cursor: grabbing; }
    .spawn .stitle { min-width: 0; font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .spawn .sfrom { margin-left: auto; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
      font: var(--bx-font-code, 12px/18px ui-monospace, monospace); color: var(--bx-muted, #A3A6B6); }
    .spawn .sbody { flex: 1; min-height: 0; position: relative; }

    /* ---- background context menu ---- */
    .ctx-backdrop { position: fixed; inset: 0; z-index: 3000; }

    /* ---- workspace health banners (/alerts) and the first-run card: the
       status tint, its icon and word, then the message ---- */
    .alerts { position: sticky; top: 0; z-index: 3500; display: flex; flex-direction: column; flex: none; }
    .alert {
      display: flex; align-items: center; gap: 8px; box-sizing: border-box; min-height: 32px; padding: 4px 12px;
      font: var(--bx-font-ui, 400 13px/18px system-ui, sans-serif); color: var(--bx-text, #E9EAF0);
      background: var(--st-bg); border-bottom: 1px solid var(--st);
    }
    .alert.warn { --st: var(--bx-warn, #F2994A); --st-bg: var(--bx-warn-bg, #382F2C); }
    .alert.crit { --st: var(--bx-danger, #FF7A7A); --st-bg: var(--bx-danger-bg, #3A2B32); }
    .alert .ico { flex: none; display: inline-flex; color: var(--st); }
    .alert .lvl { flex: none; font-weight: 600; color: var(--st); }
    .alert .msg { flex: 1; min-width: 0; }
    .alert .dismiss {
      flex: none; box-sizing: border-box; height: 24px; padding: 0 10px; cursor: pointer; font-weight: 600;
      color: var(--bx-text, #E9EAF0); background: transparent;
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    }
    .alert .dismiss:hover { background: var(--bx-hover, #2A2B34); }

    /* ---- component status on the screen tabs (tiles → workspace): the
       level's glyph in its colour, the active tab underlined in it ---- */
    .tab.st-warn, .tab.st-error { color: var(--st); }
    .tab.on.st-warn, .tab.on.st-error { box-shadow: inset 0 -2px 0 var(--st); }
    /* transient notifications (xbin.notify) — bottom right, at most three,
       auto-dismissed, click to close (or to act): panel, border, the status
       glyph and word (product-ui 6) */
    .toasts { position: fixed; right: 16px; bottom: 16px; z-index: 4000;
      display: flex; flex-direction: column; gap: 8px; width: min(380px, calc(100vw - 32px)); }
    .toast {
      display: flex; align-items: flex-start; gap: 8px; box-sizing: border-box; padding: 8px 12px; cursor: pointer;
      font: var(--bx-font-ui, 400 13px/18px system-ui, sans-serif); color: var(--bx-text, #E9EAF0);
      background: var(--bx-panel, #1F2028); border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
      box-shadow: var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6));
      animation: bx-toast-in var(--bx-dur-panel, 200ms) var(--bx-ease-out, cubic-bezier(0.16, 1, 0.3, 1));
    }
    .toast .sti { margin-top: 1px; }
    .toast .lvl { flex: none; font-weight: 600; color: var(--st, var(--bx-muted, #A3A6B6)); }
    .toast .tmsg { min-width: 0; overflow: hidden; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 4; overflow-wrap: anywhere; }
    .toast b { font-family: var(--bx-mono, ui-monospace, monospace); font-weight: 600; }
    @keyframes bx-toast-in { from { opacity: 0; transform: translateY(8px); } to { opacity: 1; transform: none; } }

    /* ---- the settings menu: a popover off the settings chip ---- */
    .wsmenu {
      position: fixed; top: calc(var(--bx-topbar-h, 40px) + 4px); right: 12px; z-index: 3001; box-sizing: border-box;
      min-width: 300px; max-width: calc(100vw - 24px); max-height: calc(100vh - 56px); overflow-y: auto; padding: 4px 12px 12px;
      font: var(--bx-font-ui, 400 13px/18px system-ui, sans-serif);
      background: var(--bx-panel, #1F2028); border: 1px solid var(--bx-border-strong, #666A7E);
      border-radius: var(--bx-radius, 2px); box-shadow: var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6));
    }
    .wsmenu .hd {
      margin: 10px 0 4px;
      font: var(--bx-font-micro, 600 11px/14px system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase;
      color: var(--bx-muted, #A3A6B6);
    }
    .wsmenu .row { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: var(--bx-row, 28px); margin: 2px 0; }
    .wsmenu .fs { display: flex; align-items: center; gap: 4px; }
    .wsmenu .fs b { min-width: 28px; text-align: center; font-weight: 600; font-variant-numeric: tabular-nums; }
    .wsmenu .fs input[type=range] { width: 104px; margin: 0 4px; accent-color: var(--bx-accent, #8C9BFF); background: transparent; }
    .wsmenu .fs b.gs { min-width: 92px; text-align: left; }
    .wsmenu .gshint { margin: 0 0 4px; font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); color: var(--bx-muted, #A3A6B6); }
    .wsmenu .step {
      display: inline-flex; align-items: center; justify-content: center; box-sizing: border-box; min-width: 28px; height: 28px; padding: 0 6px; cursor: pointer;
      color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028);
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    }
    .wsmenu .step:hover { background: var(--bx-hover, #2A2B34); }
    /* Theme and Density: square segmented controls, the pressed one the
       selection (product-ui 6) */
    .wsmenu .seg { display: inline-flex; }
    .wsmenu .seg button {
      position: relative; box-sizing: border-box; height: var(--bx-control-h, 28px); padding: 0 10px; margin-left: -1px; cursor: pointer;
      color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028);
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: 0;
    }
    .wsmenu .seg button:first-child { margin-left: 0; border-radius: var(--bx-radius, 2px) 0 0 var(--bx-radius, 2px); }
    .wsmenu .seg button:last-child { border-radius: 0 var(--bx-radius, 2px) var(--bx-radius, 2px) 0; }
    .wsmenu .seg button:hover { background: var(--bx-hover, #2A2B34); }
    .wsmenu .seg button[aria-pressed="true"] {
      z-index: 1; font-weight: 600; color: var(--bx-selection-text, #E9EAF0);
      background: var(--bx-selection, #262C5C); border-color: var(--bx-accent, #8C9BFF);
    }
    .wsmenu .seg button:focus-visible { z-index: 2; }
    .wsmenu .seg button:disabled { cursor: default; opacity: 0.5; }
    .wsmenu input:not([type=checkbox]):not([type=range]), .wsmenu select {
      box-sizing: border-box; height: var(--bx-control-h, 28px); padding: 0 8px;
      color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028);
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    }
    .wsmenu select { padding: 0 4px; max-width: 100%; }
    .wsmenu input[type=checkbox] { margin: 0; accent-color: var(--bx-accent, #8C9BFF); }
    .wsmenu form.pw { display: flex; flex-direction: column; gap: 4px; }
    .wsmenu label.rmdev { display: flex; align-items: center; gap: 6px; min-height: 24px; font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); color: var(--bx-muted, #A3A6B6); }
    .wsmenu .share { display: flex; flex-wrap: wrap; gap: 4px; align-items: center; }
    .wsmenu .menu-msg { display: flex; align-items: flex-start; gap: 6px; margin-top: 8px; font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); }
    .wsmenu .menu-msg.ok { color: var(--bx-ok, #A3CF5E); }
    .wsmenu .menu-msg.bad { color: var(--bx-danger, #FF7A7A); }
    .wsmenu button.act, .wsmenu a.act {
      display: inline-flex; align-items: center; justify-content: center; gap: 6px; box-sizing: border-box;
      min-height: var(--bx-control-h, 28px); padding: 0 10px; cursor: pointer; text-decoration: none; font-weight: 600;
      color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028);
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    }
    .wsmenu button.act:hover, .wsmenu a.act:hover { background: var(--bx-hover, #2A2B34); }
    .wsmenu .wide { width: 100%; margin-top: 6px; }
    /* "add a device": the settings menu's first item — where the phone's QR code is */
    .wsmenu button.add-device {
      display: grid; grid-template-columns: auto 1fr; column-gap: 10px; align-items: center; justify-content: stretch;
      width: 100%; margin: 6px 0 4px; padding: 6px 10px; text-align: left;
    }
    .wsmenu button.add-device bx-icon { grid-row: span 2; color: var(--bx-muted, #A3A6B6); }
    .wsmenu button.add-device b { font-weight: 600; }
    .wsmenu button.add-device span { font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); font-weight: 400; color: var(--bx-muted, #A3A6B6); }
    /* "your partitions" (owner ruling I13, shell-account.js): the account
       block's link to the partitions page — a button like devices…, with the
       partitioned marker's shape (partCss .pm) */
    .wsmenu a.parts { width: 100%; margin-top: 6px; }
    .wsmenu a.parts .ext { display: inline-flex; color: var(--bx-muted, #A3A6B6); }
    .wsmenu a.parts .pm { cursor: inherit; }

    aside.collapsed {
      flex: none; box-sizing: border-box; width: 28px; padding: 4px 0;
      background: var(--bx-sidebar, #111218); border-right: 1px solid var(--bx-border, #33353F);
    }
    aside.collapsed .expand {
      display: flex; align-items: center; justify-content: center; width: 100%; height: 28px; padding: 0; cursor: pointer;
      border: 0; background: none; color: var(--bx-muted, #A3A6B6);
    }
    aside.collapsed .expand:hover { background: var(--bx-control-hover, #33353F); color: var(--bx-text, #E9EAF0); }
    /* the sidebar's edge: a 6 px handle over a 1 px divider */
    .side-handle { position: relative; z-index: 1; flex: none; width: 6px; margin: 0 -3px; cursor: col-resize; }
    .side-handle::after { content: ''; position: absolute; top: 0; bottom: 0; left: 2px; width: 1px; background: var(--bx-border, #33353F); }
    .side-handle:hover::after { left: 2px; width: 2px; background: var(--bx-border-strong, #666A7E); }

    main { flex: 1; min-width: 0; overflow: auto; padding: 14px; }
    .grants { margin-bottom: 12px; display: flex; flex-direction: column; gap: 8px; }

    /* ======================= mobile layout (≤ 820px) ======================= */
    /* The desktop shell is mouse-driven (fixed sidebar, absolute snap-grid,
       floating windows). On narrow screens we switch interaction models: the
       sidebar becomes an off-canvas drawer, tiles stack full-width (no drag or
       resize — content scrolls inside), and floating windows / terminals become
       full-screen sheets. */
    .ham {
      flex: none; display: inline-flex; align-items: center; justify-content: center; box-sizing: border-box;
      width: var(--bx-control-h, 28px); height: var(--bx-control-h, 28px); padding: 0; cursor: pointer;
      color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028);
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    }
    @media (max-width: 820px) {
      .top { gap: 6px; padding: 0 8px; }
      .top .ws-chip { max-width: 34vw; overflow: hidden; text-overflow: ellipsis; }
      .top a.chip, .top button.chip { padding: 0 8px; }
      .tabs { height: 40px; }                   /* larger tap targets */
      .tab { padding: 0 14px; }
      main { padding: 8px; }
      .grants { margin-bottom: 8px; }

      /* sidebar → off-canvas drawer, slid in over a scrim */
      .body.mobile bx-side.drawer {
        position: fixed; left: 0; top: 0; bottom: 0; z-index: 3600;
        width: min(290px, 84vw) !important;
        transform: translateX(-100%); transition: transform var(--bx-dur-panel, 200ms) var(--bx-ease-out, cubic-bezier(0.16, 1, 0.3, 1));
        border-right: 1px solid var(--bx-border, #33353F);
        box-shadow: var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6));
      }
      .body.mobile bx-side.drawer.open { transform: none; }
      .drawer-backdrop { position: fixed; inset: 0; z-index: 3550; background: var(--bx-scrim, rgba(0, 0, 0, 0.55)); }

      main { -webkit-touch-callout: none; }

      /* the tile-admin popover → a sheet under the bars (tap above to close) */
      .admin-pop-backdrop { background: var(--bx-scrim, rgba(0, 0, 0, 0.55)); }
      .admin-pop {
        left: 0 !important; right: 0; top: 48px !important; bottom: 0;
        width: auto !important; height: auto !important;
        resize: none; border-radius: 0; border: 0;
      }
    }
    @media (max-width: 480px) {
      /* a phone: the chips fit, and the workspace's title keeps some room */
      .top { gap: 4px; padding: 0 6px; }
      .top .ws-chip { display: none; }
      .top a.chip, .top button.chip { padding: 0 6px; }
    }
`];

// The ⇄ change-proposal badge and the ⇈ deployments badge: sidebar rows (the
// shell) and card heads (bx-canvas) — square, 20 px, its glyph and count.
export const prbCss = css`
    .prb {
      flex: none; display: inline-flex; align-items: center; gap: 2px; box-sizing: border-box; height: 20px; padding: 0 4px;
      font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); font-weight: 600; font-variant-numeric: tabular-nums;
      color: var(--bx-text, #E9EAF0); background: transparent;
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    }
    button.prb { cursor: pointer; }
    button.prb:hover { background: var(--bx-hover, #2A2B34); }
    .prb.bad { color: var(--bx-danger, #FF7A7A); border-color: var(--bx-danger, #FF7A7A); }
    /* +dev: the window shows a deployment that isn't the primary (card heads) */
    .dtag {
      flex: none; box-sizing: border-box; height: 20px; max-width: 14ch; padding: 0 6px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
      font: var(--bx-font-code, 12px/18px ui-monospace, monospace); font-weight: 600; line-height: 20px;
      color: var(--bx-accent-ink, #0B0C12); background: var(--bx-accent, #8C9BFF); border-radius: var(--bx-radius, 2px);
    }
`;

// bx-canvas: the snappable grid, its cards, the floating windows — and their
// mobile shape (stacked cards, full-screen sheets).
export const canvasCss = [scrollCss, baseCss, chromeCss, css`
    :host { display: block; }
    /* view mode of a shared screen: no grab cursor, no resize handles */
    .canvas.ro .card .head { cursor: default; }
    .canvas.ro .rz { display: none; }
    /* ---- fixed snappable grid canvas ---- */
    /* Absolutely-positioned tiles on a GRID-px module; positions never reflow on
       window resize. Height grows to fit the lowest tile — floored to the
       viewport so the mark field always fills the pane (min size set inline). */
    /* A crosshair mark at every 48px intersection, sitting on the real snap
       crossing. A tile sits on a 48k node but renders GAP (8px) narrower, so
       four tile corners meet in each gutter cross (~48k+44); mask-position
       20px lands the SVG's centred crosshair there. Drawn on a ::before
       overlay via mask (not a background image) so the colour stays a token
       (--bx-canvas-dot) — a data-URI SVG can't read CSS vars, so the SVG is
       only the mask shape. pointer-events:none keeps tile drag / canvas
       context-menu intact, and it paints behind the (later-in-DOM)
       absolutely-positioned tiles. */
    .canvas { position: relative; background-color: var(--bx-bg, #0B0C12); }
    .canvas::before {
      content: ''; position: absolute; inset: 0; pointer-events: none;
      background: var(--bx-canvas-dot, #1F2028);
      -webkit-mask: url("data:image/svg+xml,%3Csvg%20xmlns='http://www.w3.org/2000/svg'%20width='48'%20height='48'%3E%3Cpath%20d='M24%2020.5v7M20.5%2024h7'%20fill='none'%20stroke='white'%20stroke-width='1.3'%20stroke-linecap='butt'/%3E%3C/svg%3E") calc(var(--grid-px, 48px) * 20 / 48) calc(var(--grid-px, 48px) * 20 / 48) / var(--grid-px, 48px) var(--grid-px, 48px) repeat;
      mask: url("data:image/svg+xml,%3Csvg%20xmlns='http://www.w3.org/2000/svg'%20width='48'%20height='48'%3E%3Cpath%20d='M24%2020.5v7M20.5%2024h7'%20fill='none'%20stroke='white'%20stroke-width='1.3'%20stroke-linecap='butt'/%3E%3C/svg%3E") calc(var(--grid-px, 48px) * 20 / 48) calc(var(--grid-px, 48px) * 20 / 48) / var(--grid-px, 48px) var(--grid-px, 48px) repeat;
    }
    .gtile { position: absolute; display: flex; }
    .gtile.dragging { opacity: 0.85; z-index: 50; }
    .gtile.dragging .card { box-shadow: var(--bx-shadow-active, 0 16px 40px rgba(0, 0, 0, 0.55)); }
    /* the push ghost (D66): where a neighbour lands if the drag is released here */
    .ghost {
      position: absolute; pointer-events: none; z-index: 40; box-sizing: border-box;
      border: 2px dashed var(--bx-accent, #8C9BFF);
      border-radius: var(--bx-radius, 2px);
      background: color-mix(in srgb, var(--bx-accent, #8C9BFF) 8%, transparent);
    }
    .ghost::after {
      content: attr(data-path); position: absolute; left: 8px; top: 6px;
      font: var(--bx-font-code, 12px/18px ui-monospace, monospace); color: var(--bx-accent, #8C9BFF);
    }
    /* the resize corner — above what covers a card body: bx-frame's build-error
       overlay (9) and a pending tile's partition overlay (11, partCss); a 6 px
       mark in a 12 px grip */
    .gtile .rz {
      position: absolute; right: 0; bottom: 0; width: 12px; height: 12px;
      cursor: nwse-resize; z-index: 12; touch-action: none;
    }
    .gtile .rz::after {
      content: ''; position: absolute; right: 2px; bottom: 2px; width: 6px; height: 6px; box-sizing: border-box;
      border-right: 2px solid var(--bx-border-strong, #666A7E); border-bottom: 2px solid var(--bx-border-strong, #666A7E);
    }
    .gtile .rz:hover::after { border-color: var(--bx-text, #E9EAF0); }
    /* a window (product-ui 3): a 1 px edge, square corners; tiled ones sit
       flat on the grid, the active one's edge goes a step stronger */
    .card {
      flex: 1; min-width: 0; min-height: 0; display: flex; flex-direction: column; box-sizing: border-box;
      background: var(--bx-panel, #1F2028); border: 1px solid var(--bx-window-border, #666A7E);
      border-radius: var(--bx-radius, 2px);
      box-shadow: var(--bx-shadow, 0 1px 2px rgba(0, 0, 0, 0.4));
      overflow: hidden;
    }
    .card.active { border-color: var(--bx-window-border-active, #9396A4); }
    /* the part tab (product-ui 3): a 3 px strip on the admin console's windows */
    .card[data-part="admin"]::before { content: ''; flex: none; height: var(--bx-part-tab-h, 3px); background: var(--bx-part-tab-admin, #FFD000); }
    .card .head {
      flex: none;
      display: flex; align-items: center; gap: 8px; height: var(--bx-titlebar-h, 28px); padding: 0 0 0 10px;
      color: var(--bx-title-text-inactive, #8E91A2); background: var(--bx-titlebar, #1F2028);
      border-bottom: 1px solid var(--bx-border, #33353F);
      cursor: grab; user-select: none; touch-action: none;
    }
    .card.active .head { color: var(--bx-title-text, #E9EAF0); background: var(--bx-titlebar-active, #262730); }
    .card .head:active { cursor: grabbing; }
    /* the name (the folder's, UI 600) and the path (mono, muted) */
    .card .head .t { display: flex; align-items: baseline; gap: 8px; min-width: 0; overflow: hidden; white-space: nowrap; }
    .card .head .t .nm { flex: none; max-width: 100%; overflow: hidden; text-overflow: ellipsis; font-weight: 600; }
    .card .head .t .path { min-width: 0; overflow: hidden; text-overflow: ellipsis;
      font: var(--bx-font-code, 12px/18px ui-monospace, monospace); color: var(--bx-muted, #A3A6B6); }
    .card .head .spacer { flex: 1; }
    /* the tile body: fixed height, content scrolls inside — never stretches the card */
    .card .cbody { flex: 1; min-height: 0; overflow: hidden; position: relative; }
    .card .cbody > bx-frame { position: absolute; inset: 0; }
    .empty { color: var(--bx-muted, #A3A6B6); padding: 24px; text-align: center; }

    /* ---- floating (unpinned) tile windows: they cast the window shadows ---- */
    .float {
      position: fixed; z-index: 100; box-sizing: border-box;
      display: flex; flex-direction: column;
      border: 1px solid var(--bx-window-border, #666A7E);
      border-radius: var(--bx-radius, 2px);
      background: var(--bx-panel, #1F2028);
      box-shadow: var(--bx-shadow-rest, 0 10px 28px rgba(0, 0, 0, 0.4));
      overflow: hidden; resize: both; min-width: 220px; min-height: 120px;
    }
    .float.active { border-color: var(--bx-window-border-active, #9396A4); box-shadow: var(--bx-shadow-active, 0 16px 40px rgba(0, 0, 0, 0.55)); }
    .float > .card { border: 0; border-radius: 0; box-shadow: none; }

    @media (max-width: 820px) {
      /* tiles → stacked full-width cards (keep each tile's own height inline) */
      .canvas {
        position: static !important; min-width: 0 !important; min-height: 0 !important;
        display: flex; flex-direction: column; gap: 10px; background: none;
      }
      /* no snap grid to show: and a static .canvas would size the crosshair
         overlay to whatever positioned box is above it, drawing it over the
         tab strip, the bind panel and every card head */
      .canvas::before { display: none; }
      .gtile {
        position: static !important; left: auto !important; top: auto !important;
        width: 100% !important; min-height: 260px; max-height: 82vh;
      }
      .gtile .rz { display: none; }             /* no resize on touch */
      .ghost { display: none; }                 /* no drag, no push preview */
      .gtile .card .head { cursor: default; }   /* no drag on touch */
      .card .head { height: 40px; }             /* tap targets */
      .card .head .wc { width: 40px; height: 40px; }
      /* floating windows → full-screen sheets */
      .float {
        position: fixed !important; inset: 0 !important;
        width: auto !important; height: auto !important;
        resize: none !important; border-radius: 0; border: 0;
      }
    }
`];

// Status levels: the colour token and its tint, carried as --st / --st-bg so
// the glyph (.sti) and a tinted row inherit them — sidebar rows (bx-side),
// screen tabs and toasts (the shell). A level is never its colour alone: a
// glyph of its own shape draws it (shell-kit.js statusIcon), with its word.
export const statusCss = css`
    .st-ok    { --st: var(--bx-ok, #A3CF5E); --st-bg: var(--bx-ok-bg, #2F352E); }
    .st-info  { --st: var(--bx-info, #A9B4C6); --st-bg: var(--bx-info-bg, #30323B); }
    .st-warn  { --st: var(--bx-warn, #F2994A); --st-bg: var(--bx-warn-bg, #382F2C); }
    .st-error { --st: var(--bx-danger, #FF7A7A); --st-bg: var(--bx-danger-bg, #3A2B32); }
    .sti { flex: none; color: var(--st, var(--bx-muted, #A3A6B6)); }
`;

// Partitioned tiles (docs/partitions.md; PD-53 design A): the marker a
// sidebar row and a window head carry (shell-kit.js partitionMark) and a
// pending tile's card overlay (bx-canvas.js). The hue is --bx-part, the
// partition marker's token (teal; deeper in Day, so the ring keeps its
// contrast). Plain green is status "ok" (above); the half-filled shape, not
// the hue, says "divided". The marker is status, not a button: no border,
// no hover state.
export const partCss = css`
    :host { --bx-part-c: var(--bx-part, #3FB5A3); }
    .pm { flex: none; display: inline-flex; width: 8px; height: 8px; color: var(--bx-part-c);
      cursor: default; border: 0; background: none; padding: 0; }
    .pm svg { width: 8px; height: 8px; display: block; }
    .item .pm { margin-left: 4px; }
    /* a pending tile (docs/partitions.md §The mode): its card greys out
       under a note, and a tile manager decides there — the head stays live
       (menu, terminal, close) */
    .pover { position: absolute; inset: 0; z-index: 11; display: flex; align-items: center; justify-content: center;
      padding: 12px; box-sizing: border-box; overflow: auto;
      background: color-mix(in srgb, var(--bx-bg, #0B0C12) 70%, transparent);
      -webkit-backdrop-filter: grayscale(1); backdrop-filter: grayscale(1); }
    .pbox { max-width: 440px; margin: auto; box-sizing: border-box; padding: 12px;
      font: var(--bx-font-ui, 400 13px/18px system-ui, sans-serif);
      background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
      box-shadow: var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6)); }
    .pbox .phead { display: flex; align-items: center; gap: 8px; margin: 0 0 6px; font-weight: 600; }
    .pbox .phead .pico { flex: none; color: var(--bx-warn, #F2994A); }
    .pbox .phead .pmore { margin-left: auto; font-weight: 400; color: var(--bx-link, #8C9BFF); text-decoration: none; }
    .pbox .phead .pmore:hover { text-decoration: underline; }
    .pbox .pmsg { margin: 0; }
    .pbox .pnote { margin: 8px 0 0; padding-left: 8px; border-left: 2px solid var(--bx-border, #33353F);
      white-space: pre-wrap; overflow-wrap: anywhere; }
    .pbox .pwho, .pbox .pdone { margin: 8px 0 0; color: var(--bx-muted, #A3A6B6); white-space: pre-wrap; }
    .pbox .perr { margin: 8px 0 0; padding: 6px 8px; white-space: pre-wrap;
      color: var(--bx-danger, #FF7A7A); background: var(--bx-danger-bg, #3A2B32);
      border: 1px solid var(--bx-danger, #FF7A7A); border-radius: var(--bx-radius, 2px); }
    .pbox .pbtns { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; margin: 12px 0 0; }
    .pbox button { box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 12px; cursor: pointer; font-weight: 600;
      color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028);
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); }
    .pbox button:hover:not(:disabled) { background: var(--bx-hover, #2A2B34); }
    .pbox button:disabled { opacity: 0.5; cursor: default; }
    .pbox button.pdel { color: var(--bx-danger, #FF7A7A); border-color: var(--bx-danger, #FF7A7A); }
    /* the partition chip (owner ruling I3): whose partition a partitioned
       tile's window shows — yours, shared (a deployment's one instance, no
       marker there), global — in the marker's hue; status, not a button:
       no border, no hover state. "no partition" (view-as, the workspace
       token on a tile without a global instance) is muted */
    .pchip { flex: none; box-sizing: border-box; height: 20px; padding: 0 6px; border-radius: var(--bx-radius, 2px); cursor: default; user-select: none;
      font: var(--bx-font-code, 12px/18px ui-monospace, monospace); font-weight: 600; line-height: 20px; color: var(--bx-part-c); border: 0;
      background: color-mix(in srgb, var(--bx-part-c) 14%, transparent); }
    .pchip[data-chip="none"] { color: var(--bx-muted, #A3A6B6); background: color-mix(in srgb, var(--bx-muted, #A3A6B6) 14%, transparent); }
`;

// bx-side: the sidebar's tree, filter, folders, footers — and its mobile rows.
export const sideCss = [scrollCss, baseCss, css`
    :host { display: flex; flex-direction: column; box-sizing: border-box; padding: 4px 0 0; overflow: hidden;
      background: var(--bx-sidebar, #111218); font: var(--bx-font-ui, 400 13px/18px system-ui, sans-serif); }
    .root { display: flex; flex-direction: column; flex: 1; min-height: 0; }
    .side-scroll { flex: 1; min-height: 0; overflow-y: auto; overflow-x: hidden; padding-bottom: 12px; }
    /* micro caps: section labels and the footers' keys */
    .group, .sysrow .l, .buildfoot .label, .hidb, .item.screen .pk, .item .ob {
      font: var(--bx-font-micro, 600 11px/14px system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase;
    }
    .sysfoot { flex: none; border-top: 1px solid var(--bx-border, #33353F); padding: 8px 12px 10px; }
    .sysrow { display: flex; justify-content: space-between; align-items: baseline; margin-top: 6px; }
    .sysrow .l { color: var(--bx-muted, #A3A6B6); }
    .sysrow .v { font: var(--bx-font-code, 12px/18px ui-monospace, monospace); color: var(--bx-text, #E9EAF0); }
    .sysrow .v.ok { color: var(--bx-ok, #A3CF5E); }
    .sysrow .v.bad { color: var(--bx-danger, #FF7A7A); font-weight: 700; }
    .sysbar { height: 4px; border-radius: var(--bx-radius, 2px); background: var(--bx-border, #33353F); overflow: hidden; margin-top: 2px; }
    .sysbar .fill { height: 100%; background: var(--bx-muted, #A3A6B6); transition: width var(--bx-dur-window, 240ms) var(--bx-ease-out, cubic-bezier(0.16, 1, 0.3, 1)); }
    /* ---- xbind build commit (sidebar bottom) ---- */
    .buildfoot {
      flex: none; display: flex; align-items: center; gap: 6px; padding: 6px 12px 8px;
      border-top: 1px solid var(--bx-border, #33353F);
    }
    .buildfoot .glyph { display: inline-flex; color: var(--bx-muted, #A3A6B6); }
    .buildfoot .label { color: var(--bx-muted, #A3A6B6); }
    .buildfoot .ver { margin-left: auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
      font: var(--bx-font-code, 12px/18px ui-monospace, monospace); color: var(--bx-text, #E9EAF0); }
    .buildfoot .ver.dirty { color: var(--bx-warn, #F2994A); }
    .orgbtn {
      display: flex; align-items: center; gap: 8px; box-sizing: border-box; width: calc(100% - 16px); min-height: var(--bx-control-h, 28px);
      margin: 8px 8px 0; padding: 0 8px; cursor: pointer; text-align: left;
      color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028);
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    }
    .orgbtn:hover { background: var(--bx-hover, #2A2B34); }
    .orgbtn bx-icon { color: var(--bx-muted, #A3A6B6); }
    .orgbtn .n { margin-left: auto; font: var(--bx-font-code, 12px/18px ui-monospace, monospace); color: var(--bx-muted, #A3A6B6); }
    /* ---- rows: 28 px; the hovered row, the selected one (a tile open on
       this screen, the screen shown) in the selection with a 2 px accent
       rule (product-ui 6) ---- */
    .item {
      position: relative; display: flex; align-items: center; gap: 8px; box-sizing: border-box;
      min-height: var(--bx-row, 28px); padding: 0 8px 0 16px; cursor: pointer; white-space: nowrap;
      overflow: hidden; text-overflow: ellipsis; color: var(--bx-text, #E9EAF0);
    }
    .item:hover { background: var(--bx-control-hover, #33353F); }
    .item.open, .item.screen.on { background: var(--bx-selection, #262C5C); color: var(--bx-selection-text, #E9EAF0); }
    .item.open::before, .item.screen.on::before {
      content: ''; position: absolute; left: 0; top: 0; bottom: 0; width: 2px; background: var(--bx-accent, #8C9BFF); }
    .item:focus-visible { outline-offset: calc(-1 * var(--bx-focus-width, 3px)); box-shadow: none; }
    .item .sti, .group.folder .sti { margin-left: 2px; }
    .item.dropinto { box-shadow: inset 0 2px 0 var(--bx-accent, #8C9BFF); }
    .item .more {
      flex: none; display: inline-flex; align-items: center; justify-content: center; width: 24px; height: 24px; padding: 0; margin-right: -4px;
      border: 0; border-radius: var(--bx-radius, 2px); background: none; color: var(--bx-muted, #A3A6B6); opacity: 0; cursor: pointer;
    }
    .item:hover .more, .item:focus-within .more, :host(.drawer) .item .more { opacity: 1; }
    .item .more:hover { background: var(--bx-control-hover, #33353F); color: var(--bx-text, #E9EAF0); }
    .item .tic { flex: none; display: inline-flex; color: var(--bx-muted, #A3A6B6); }
    .item.open .tic { color: inherit; }
    .item .nm { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .item .err { display: inline-flex; color: var(--bx-danger, #FF7A7A); }
    .empty { color: var(--bx-muted, #A3A6B6); padding: 24px; text-align: center; }
    .group.folder { cursor: grab; }
    .group.folder .ficon { flex: none; display: inline-flex; align-items: center; line-height: 1; color: var(--bx-muted, #A3A6B6); }
    .side-top { display: flex; align-items: center; gap: 2px; padding: 2px 8px 4px; }
    .mini {
      display: inline-flex; align-items: center; gap: 4px; box-sizing: border-box; height: 28px; padding: 0 6px; cursor: pointer;
      border: 0; border-radius: var(--bx-radius, 2px); background: none; color: var(--bx-muted, #A3A6B6);
    }
    .mini:hover { background: var(--bx-control-hover, #33353F); color: var(--bx-text, #E9EAF0); }
    /* sidebar filter */
    .side-search { display: flex; align-items: center; gap: 4px; padding: 0 8px 6px; }
    .side-q { flex: 1; min-width: 0; box-sizing: border-box; height: var(--bx-control-h, 28px); padding: 0 8px;
      color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028);
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); }
    .qx { flex: none; display: inline-flex; align-items: center; justify-content: center; width: 24px; height: 24px; padding: 0; cursor: pointer;
      border: 0; border-radius: var(--bx-radius, 2px); background: none; color: var(--bx-muted, #A3A6B6); }
    .qx:hover { background: var(--bx-control-hover, #33353F); color: var(--bx-text, #E9EAF0); }
    /* a screen (tab) parked in the folder tree */
    .item.hid { opacity: 0.6; }
    .hidb, .item.screen .pk, .item .ob { flex: none; display: inline-flex; align-items: center; box-sizing: border-box; height: 18px; padding: 0 4px;
      color: var(--bx-muted, #A3A6B6); border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); }
    .hidtoggle { display: block; box-sizing: border-box; width: calc(100% - 16px); min-height: var(--bx-control-h, 28px); margin: 6px 8px; padding: 0 8px; cursor: pointer;
      font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); color: var(--bx-muted, #A3A6B6); background: none;
      border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); }
    .hidtoggle:hover { color: var(--bx-text, #E9EAF0); background: var(--bx-control-hover, #33353F); }
    .item.screen { color: var(--bx-muted, #A3A6B6); }
    .item.screen .sic { flex: none; display: inline-flex; color: var(--bx-muted, #A3A6B6); }
    .item.screen.on .sic { color: inherit; }
    .item.screen .sname { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .item.screen .xt, .group.folder .fx {
      flex: none; display: inline-flex; align-items: center; justify-content: center; width: 24px; height: 24px; padding: 0; cursor: pointer;
      border: 0; border-radius: var(--bx-radius, 2px); background: none; color: var(--bx-muted, #A3A6B6); opacity: 0;
    }
    .group.folder .fx { margin-left: auto; }
    .item.screen:hover .xt, .group.folder:hover .fx, .item.screen .xt:focus-visible, .group.folder .fx:focus-visible { opacity: 1; }
    .item.screen .xt:hover, .group.folder .fx:hover { background: var(--bx-close-hover, #FF7A7A); color: var(--bx-close-hover-ink, #0B0C12); }
    .group.folder { cursor: pointer; user-select: none; align-items: center; min-height: var(--bx-row, 28px); padding-top: 0; padding-bottom: 0; }
    .group .tri { flex: none; display: inline-flex; width: 16px; }
    .group.folder .fname { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .group.folder:hover { color: var(--bx-text, #E9EAF0); background: var(--bx-control-hover, #33353F); }
    .group.folder.dropping { color: var(--bx-selection-text, #E9EAF0); background: var(--bx-selection, #262C5C); }
    .group.folder.ro { cursor: pointer; }
    /* ✎ on an owner header: curate that section's shared folders (D55) */
    .group.owner .pen {
      margin-left: auto; display: inline-flex; align-items: center; justify-content: center; width: 24px; height: 24px; padding: 0; cursor: pointer;
      border: 0; border-radius: var(--bx-radius, 2px); background: none; color: var(--bx-muted, #A3A6B6); opacity: 0;
    }
    .group.owner:hover .pen, .group.owner .pen:focus-visible { opacity: 1; }
    .group.owner .pen:hover { background: var(--bx-control-hover, #33353F); color: var(--bx-text, #E9EAF0); }
    .group.owner.editing { color: var(--bx-text, #E9EAF0); }
    /* the section's edit bar while curating shared folders */
    .secbar {
      margin: 2px 8px 4px; padding: 6px 8px;
      font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); color: var(--bx-text, #E9EAF0);
      background: var(--bx-info-bg, #30323B); border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    }
    .secbar .l { display: flex; align-items: center; gap: 6px; margin-bottom: 4px; }
    .secbar .newer { display: flex; align-items: flex-start; gap: 4px; color: var(--bx-warn, #F2994A); margin-bottom: 4px; }
    .secbar .newer a { color: var(--bx-link, #8C9BFF); cursor: pointer; text-decoration: underline; }
    .secbar .r { display: flex; align-items: center; gap: 4px; }
    .secbar .mini { height: 24px; font: var(--bx-font-ui, 400 13px/18px system-ui, sans-serif); }
    .secbar .mini.go { color: var(--bx-accent-ink, #0B0C12); background: var(--bx-accent, #8C9BFF); padding: 0 8px; font-weight: 600; }
    .secbar .mini.go:hover { background: var(--bx-accent-hover, #A9B4FF); }
    .secbar .mini.go:disabled { opacity: 0.5; cursor: default; }
    .group.owner { cursor: pointer; user-select: none; color: var(--bx-text, #E9EAF0);
      border-top: 1px solid var(--bx-border, #33353F); margin-top: 4px; }
    .group.owner .oico { display: inline-flex; color: var(--bx-muted, #A3A6B6); }
    .side-owner { display: flex; align-items: center; gap: 4px; padding: 0 8px 6px; }
    .side-owner select { flex: 1; min-width: 0; box-sizing: border-box; height: var(--bx-control-h, 28px); padding: 0 4px;
      color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028);
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); }
    .group {
      display: flex; align-items: center; gap: 6px; box-sizing: border-box; min-height: 28px; padding: 8px 12px 2px;
      color: var(--bx-muted, #A3A6B6);
    }
    .group .n { font-weight: 400; color: var(--bx-subtle, #8E91A2); font-variant-numeric: tabular-nums; }
    @media (max-width: 820px) {
      /* rows: tap-sized, long-press opens the tile menu (no callout/selection) */
      .item { min-height: 40px; -webkit-touch-callout: none; user-select: none; }
    }
`];
