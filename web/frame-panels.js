/**
 * frame-panels.js — before D202, the terminal window's body: one panel
 * (code, logs, proposals, Deployments) full width or beside the terminal
 * (D129). The window is an IDE now (frame-ide-view.js: panes of tabs, a
 * tool strip, the logs dock); this module keeps the names a page may still
 * import from this URL (docs/compat.md), each doing the nearest thing:
 * setLayout(f, l) is open(l), the body and its styles are the IDE's, and
 * the terminal always shows.
 */
import { PANELS } from '/vendor/term-sessions.js';
import { ideBody, ideCss, ideRestore, idePref, openLayout, sideTool } from '/vendor/frame-ide-view.js';

export { PANELS };
export const PANE_W = 55; // the old split's share, percent: what older frames read from the pref

// split(f): a panel shows beside the sessions (the side list or the logs dock).
export const split = (f) => !!(f._ide?.side || f._ide?.dock?.open);
// termShown(f): the sessions always show (D202).
export const termShown = () => true;
export function setLayout(f, l) { openLayout(f, l); }
// toggleBeside(f): the Files list beside the sessions, or not.
export function toggleBeside(f) { sideTool(f, f._ide?.side || 'files'); }
export const besideTitle = (f) => `Files beside the ${f._isAgent ? 'agent' : 'terminal'}`;
export function revealTerm() { }
export function restoreLayout(f, w) { ideRestore(f, w); }
export const layoutPref = (f) => idePref(f);
export const panels = (f) => ideBody(f);
export const panelsCss = ideCss;
