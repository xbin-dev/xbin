// ws/css.js — the workspace agent's page's own stylesheet, as text (ws/page.js
// puts it in the document beside the chat kit's): theme.css's tokens only
// (D184), both themes, usable from the dock's narrowest (360 px) to a whole
// page. Classes start with `wsp-` (the page) and `wsc-` (its cards).
export const WS_CSS = `
html, body { height: 100%; }
body.bx { margin: 0; overflow: hidden; background: var(--bx-panel); color: var(--bx-text); }
.wsp { display: flex; flex-direction: column; height: 100vh; min-width: 0; position: relative; }
.wsp .grow { flex: 1; }
.wsp-vh { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
.wsp bx-icon { flex: none; }

/* the strip: the session, the scope chip, the page's menu */
.wsp-strip { position: relative; display: flex; align-items: center; gap: 8px; padding: 4px 8px 4px 12px; min-height: var(--bx-topbar-h);
  box-sizing: border-box; border-bottom: 1px solid var(--bx-border); background: var(--bx-panel); }
.bx .wsp-sess { display: inline-flex; align-items: center; gap: 4px; border-color: transparent; background: transparent; font-weight: 600; max-width: 40%; }
.wsp-sess .nm { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.bx .wsp-chip { display: flex; align-items: stretch; padding: 0; min-width: 0; flex: 0 1 auto; margin-left: auto; overflow: hidden; }
.wsp-chip .seg { display: inline-flex; align-items: center; gap: 4px; padding: 0 8px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.wsp-chip .seg + .seg { border-left: 1px solid var(--bx-border); }
.wsp-chip .seg.model { color: var(--bx-muted); }
.wsp-chip.on { background: var(--bx-selection); color: var(--bx-selection-text); box-shadow: inset 0 -2px 0 var(--bx-accent); }
.bx .wsp-menu-b { border-color: transparent; background: transparent; padding: 4px 6px; }

/* pop-overs inside the page (the scope panel, the sessions, the menu) */
.wsp-pop { position: absolute; z-index: 20; left: 8px; right: 8px; top: calc(100% + 4px); background: var(--bx-panel); border: 1px solid var(--bx-border-strong);
  border-radius: var(--bx-radius); box-shadow: var(--bx-shadow-pop); max-height: calc(100vh - 96px); overflow: auto; }
.wsp-menu { left: auto; right: 8px; min-width: 200px; padding: 4px 0; }
.bx .wsp-menu button { display: flex; width: 100%; gap: 8px; align-items: center; border: 0; background: transparent; text-align: left; padding: 4px 12px; }
.bx .wsp-menu button:hover { background: var(--bx-hover); }
.wsp-pop-h { display: flex; align-items: center; gap: 8px; padding: 8px 12px; border-bottom: 1px solid var(--bx-border); }
.wsp-pop-h b { flex: 1; }
.wsp-pop-f { position: sticky; bottom: 0; display: flex; gap: 8px; align-items: center; padding: 8px 12px; border-top: 1px solid var(--bx-border); background: var(--bx-panel); }
.wsp-pop-f p { flex: 1; margin: 0; color: var(--bx-muted); font: var(--bx-font-meta); }
.wsp-pop-f.col { flex-direction: column; align-items: stretch; }
.wsp-pad { padding: 8px 12px; }
.wsp-foot { margin: 8px 12px; color: var(--bx-muted); font: var(--bx-font-meta); }

/* the scope panel */
.wsp-totals { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 8px; padding: 8px 12px; }
.wsp-total { border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 6px 8px; display: flex; flex-direction: column; min-width: 0; }
.wsp-total b { font: var(--bx-font-heading); font-variant-numeric: tabular-nums; }
.wsp-total span { display: inline-flex; gap: 4px; align-items: center; color: var(--bx-muted); font: var(--bx-font-meta); }
.wsp-modelrow, .wsp-filter { display: flex; align-items: center; gap: 6px; padding: 4px 12px; flex-wrap: wrap; }
.bx .wsp-filter button { padding: 2px 8px; }
.bx .wsp-filter button.on { background: var(--bx-selection); color: var(--bx-selection-text); border-color: var(--bx-accent); }
.wsp-tree { padding: 4px 0; border-top: 1px solid var(--bx-border); }
.wsp-ti { display: flex; align-items: center; gap: 6px; min-height: var(--bx-row); padding: 2px 12px; cursor: pointer; flex-wrap: wrap; }
.wsp-ti:hover { background: var(--bx-hover); }
.wsp-ti.tile { padding-left: 40px; }
.wsp-ti.dim .wsp-tname { color: var(--bx-muted); }
.wsp-ti:focus-visible { outline: var(--bx-focus-width) solid var(--bx-focus); outline-offset: calc(-1 * var(--bx-focus-width)); }
.wsp-box { width: 14px; height: 14px; box-sizing: border-box; border: 1px solid var(--bx-border-strong); border-radius: var(--bx-radius); display: inline-flex;
  align-items: center; justify-content: center; flex: none; --bx-icon-size: 12px; }
.wsp-box.on, .wsp-box.mixed { background: var(--bx-accent); border-color: var(--bx-accent); color: var(--bx-accent-ink); }
.wsp-box.disabled { opacity: .4; }
.wsp-tick { display: inline-flex; }
.wsp-state { display: inline-flex; align-items: center; gap: 4px; font: var(--bx-font-meta); color: var(--bx-muted); }
.wsp-state.ok { color: var(--bx-ok); } .wsp-state.warn { color: var(--bx-warn); }
.wsp-via { flex-basis: 100%; padding-left: 20px; }
.wsp-reason { display: flex; gap: 8px; padding: 8px 12px 8px 40px; background: var(--bx-warn-bg); color: var(--bx-text); }
.wsp-reason bx-icon { color: var(--bx-warn); margin-top: 2px; }
.wsp-reason button { margin-top: 4px; }
.wsp-mlist { display: flex; flex-direction: column; gap: 6px; padding: 8px 12px; }
.wsp-model { display: flex; gap: 8px; align-items: flex-start; border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 6px 8px; cursor: pointer; }
.wsp-model.on { border-color: var(--bx-accent); background: var(--bx-selection); color: var(--bx-selection-text); }
.wsp-where { display: block; }
.wsp-tag { display: inline-flex; align-items: center; gap: 4px; border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 0 6px; font: var(--bx-font-meta);
  color: var(--bx-muted); white-space: nowrap; }
.wsp-tag.ok { color: var(--bx-ok); } .wsp-tag.warn { color: var(--bx-warn); }
.wsp-whatif { margin: 0 12px 8px; border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 8px; display: flex; flex-direction: column; gap: 4px; }
.wsp-wh, .wsp-conn, .wsp-disc { display: flex; gap: 6px; align-items: center; }
.wsp-conn { color: var(--bx-ok); }
.wsp-names { display: flex; flex-wrap: wrap; gap: 4px; }
.wsp-name { display: inline-flex; gap: 4px; align-items: center; border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 0 6px; font: var(--bx-font-meta); }
.wsp-dlist { margin: 0; padding-left: 20px; }

/* sessions */
.wsp-sessions { right: auto; width: min(360px, calc(100% - 16px)); }
.wsp-slist { list-style: none; margin: 0; padding: 4px 0; }
.wsp-srow.on { background: var(--bx-selection); color: var(--bx-selection-text); box-shadow: inset 2px 0 0 var(--bx-accent); }
.bx .wsp-sbtn { display: flex; flex-direction: column; align-items: stretch; width: 100%; border: 0; background: transparent; text-align: left; padding: 6px 12px; gap: 2px; color: inherit; }
.bx .wsp-sbtn:hover { background: var(--bx-hover); }
.wsp-sname { display: flex; gap: 8px; align-items: baseline; }
.wsp-sname b { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.wsp-sst { display: inline-flex; align-items: center; gap: 4px; font: var(--bx-font-meta); color: var(--bx-muted); white-space: nowrap; }
.wsp-sst.warn { color: var(--bx-warn); }
.wsp-rename { margin: 4px 12px; width: calc(100% - 24px); }
.wsp-older summary { list-style: none; cursor: pointer; padding: 4px 12px; color: var(--bx-muted); display: flex; gap: 4px; align-items: center; }
.wsp-sacts { display: flex; gap: 8px; padding: 8px 12px; border-top: 1px solid var(--bx-border); flex-wrap: wrap; }

/* banners: a state of the page, what still works, who fixes it */
.wsp-banner { display: flex; gap: 8px; align-items: flex-start; padding: 8px 12px; border-bottom: 1px solid var(--bx-border); background: var(--bx-panel-2); }
.wsp-banner.warn { background: var(--bx-warn-bg); } .wsp-banner.info { background: var(--bx-info-bg); } .wsp-banner.bad { background: var(--bx-danger-bg); }
.wsp-banner > bx-icon { margin-top: 2px; }
.wsp-banner .wsp-bt { flex: 1; min-width: 0; }

/* the conversation */
.wsp-main { flex: 1; min-height: 0; overflow: auto; padding: 12px; overflow-anchor: none; box-sizing: border-box; --bxc-gap: 10px; }
.wsp-earlier { text-align: center; margin: 0 0 8px; color: var(--bx-muted); font: var(--bx-font-meta); }
.wsp-activity { color: var(--bx-muted); font: var(--bx-font-meta); margin: 4px 0 8px; display: flex; gap: 6px; align-items: center; }
.wsp-activity .sq { width: 8px; height: 8px; background: var(--bx-accent); flex: none; }
.wsp-jump { position: sticky; bottom: 0; display: flex; justify-content: center; }
.wsp-about { display: inline-flex; align-items: center; gap: 4px; border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 0 6px;
  font: var(--bx-font-meta); background: var(--bx-panel); color: var(--bx-text); margin-bottom: 4px; max-width: 100%; }
.wsp-about b { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.wsp-about.not { border-color: var(--bx-warn); }

/* the composer */
.wsp-compose { border-top: 1px solid var(--bx-border); padding: 8px 12px; display: flex; flex-direction: column; gap: 4px; background: var(--bx-panel); }
.wsp-ctx { display: flex; gap: 6px; align-items: center; }
.bx .wsp-ctx .wsp-about { margin: 0; }
.bx .wsp-ctx button.wsp-about { cursor: pointer; }
.bx .wsp-ctx .x { border: 0; background: transparent; padding: 0 2px; min-height: 0; }
.wsp-hint { color: var(--bx-muted); font: var(--bx-font-meta); margin: 0; }

/* the empty state */
.wsp-empty { max-width: 720px; }
.wsp-empty h2 { font: var(--bx-font-heading); margin: 8px 0 4px; }
.wsp-empty h3 { font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted); margin: 12px 0 4px; }
.wsp-empty p { margin: 0; }
.wsp-cols { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 8px 16px; }
.wsp-cols ul, .wsp-starters { list-style: none; margin: 0; padding: 0; }
.wsp-cols li { display: flex; gap: 6px; align-items: baseline; margin: 2px 0; }
.wsp-cols .can bx-icon { color: var(--bx-ok); } .wsp-cols .cant bx-icon { color: var(--bx-muted); }
.wsp-starters { display: flex; flex-direction: column; gap: 6px; }
.bx .wsp-starter { display: flex; gap: 8px; align-items: baseline; width: 100%; text-align: left; padding: 6px 8px; }
.wsp-kind { font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted); min-width: 52px; flex: none; }

/* the notebook */
.wsp-nb-h { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 8px; }
.wsp-nb-h h2 { font: var(--bx-font-heading); margin: 0; }
.wsp-pages { list-style: none; margin: 0; padding: 0; border-top: 1px solid var(--bx-border); }
.wsp-pg { border-bottom: 1px solid var(--bx-border); }
.bx .wsp-pg > button { display: flex; gap: 8px; align-items: flex-start; width: 100%; border: 0; background: transparent; text-align: left; padding: 8px 4px; color: inherit; }
.bx .wsp-pg > button:hover:not(:disabled) { background: var(--bx-hover); }
.bx .wsp-pg.closed > button:disabled { opacity: 1; }
.wsp-pg.closed b { color: var(--bx-muted); }
.wsp-pgt { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.wsp-pmeta { margin-bottom: 8px; }
.wsp-marks { margin: 0 0 8px; font: var(--bx-font-meta); }
.wsp-edit { display: flex; flex-direction: column; gap: 8px; }
.wsp-edit label { display: flex; flex-direction: column; gap: 4px; font: var(--bx-font-meta); color: var(--bx-muted); }
.wsp-edit input, .wsp-edit textarea { width: 100%; box-sizing: border-box; color: var(--bx-text); font: var(--bx-font-body); }
.wsp-confirm { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; padding: 8px; margin-bottom: 8px; background: var(--bx-danger-bg); }
.wsp-confirm span { flex: 1; }

/* the agent's own cards (inside the kit's .bxc-host frame) */
.wsc { display: flex; flex-direction: column; gap: 4px; }
.wsc-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.wsc-name { display: inline-flex; align-items: center; gap: 6px; min-width: 0; flex: 1 1 150px; flex-wrap: wrap; }
.wsc-name b { overflow-wrap: break-word; }
.wsc-muted { color: var(--bx-muted); font: var(--bx-font-meta); }
.wsc-mono { font: var(--bx-font-code); overflow-wrap: anywhere; }
.wsc-st { display: inline-flex; align-items: center; gap: 4px; font: var(--bx-font-meta); color: var(--bx-muted); white-space: nowrap; }
.wsc-st.wsc-ok { color: var(--bx-ok); } .wsc-st.wsc-warn { color: var(--bx-warn); } .wsc-st.wsc-bad { color: var(--bx-danger); }
.wsc-line { font: var(--bx-font-meta); color: var(--bx-muted); overflow: hidden; text-overflow: ellipsis; }
.wsc-line b { color: var(--bx-text); font-weight: 600; }
.wsc-last { white-space: nowrap; }
.wsc-btns { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-top: 4px; }
.bx .wsc-open { display: inline-flex; gap: 4px; align-items: center; }
.wsc-err { color: var(--bx-danger); font: var(--bx-font-meta); }
.wsc-p { margin: 0; }
.wsc-quote { font-style: italic; }
.wsc-wrap { white-space: normal; }
.wsc-pick { display: inline-flex; gap: 6px; align-items: center; }
.wsc-call { color: var(--bx-muted); }
.bxc-host:has(.wsc-waits) { box-shadow: inset 2px 0 0 var(--bx-warn); }
.bxc-host:has(.wsc-policy) { box-shadow: inset 2px 0 0 var(--bx-warn); }
.wsc-fields { margin: 0; display: flex; flex-direction: column; gap: 6px; }
.wsc-field { border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 4px 8px; }
.wsc-field dt { font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted); }
.wsc-field dd { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; }
.wsc-field textarea { width: 100%; box-sizing: border-box; font: var(--bx-font-body); color: var(--bx-text); }
.bx .wsc-edit, .bx .wsc-start { display: inline-flex; gap: 4px; align-items: center; }
.wsc-quiet { flex-direction: row; align-items: center; gap: 6px; color: var(--bx-muted); font: var(--bx-font-meta); }
.bxc-host:has(.wsc-quiet), .bxc-host:has(.wsc-replies) { border: 0; padding: 0 0 0 0; background: transparent; }
.wsc-replies { flex-direction: row; flex-wrap: wrap; gap: 8px; }
.wsc-ways { display: flex; flex-direction: column; gap: 6px; margin-top: 4px; }
.bx .wsc-way { display: flex; gap: 8px; align-items: flex-start; width: 100%; text-align: left; padding: 6px 8px; }
.wsc-way > span { display: flex; flex-direction: column; min-width: 0; }

/* narrow: the dock at its narrowest, a phone */
@media (max-width: 480px) {
  .wsp-main { padding: 8px; }
  .wsp-totals { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .wsp-strip { gap: 4px; padding: 4px; }
  .wsp-chip .seg { padding: 0 6px; }
}
`;
