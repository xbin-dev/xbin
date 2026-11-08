/**
 * shell-route.js — where the person is, in the page's URL (#7): a link to a
 * screen, a tile on it, and a place inside that tile. The shell page's
 * query, readable, the way the limits page takes `?tile=`:
 *
 *   /c/root/?screen=k3x9a2b                      a screen (a tab: personal or org)
 *   /c/root/?screen=k3x9a2b&tile=apps/notes      a tile on it, the active window
 *   /c/root/?tile=apps/notes&at=projects/q3      a tile wherever it is, and its
 *                                                page's fragment (#projects/q3)
 *
 * The URL follows the person: another screen or another tile brought to the
 * front is a new history entry (pushState); a tile's own fragment changing
 * replaces the entry — the tile's frame already added its own history entry
 * when it navigated, so Back steps through both once. Back and forward
 * (popstate) and the page's first load land on the URL's place: the screen
 * activated (an org screen hidden from the tabs comes back; a parked screen
 * is un-parked), the tile found on it — or on another tab that holds it, or
 * opened on the person's own screen when no tab does — brought to the front
 * and scrolled to, and its page sent to `at` (xbin:navigate, /vendor/bx-frame.js
 * navigate()). What a link names but the viewer can't reach — a screen that
 * is gone or not shared with them, a tile that doesn't exist, isn't theirs to
 * read or is offloaded — says so in a strip above the canvas; the rest of the
 * link still applies. `/c/root/` with no query is the workspace as before.
 *
 * The URL's grammar is route.js (pure: hack/shell-route.test.mjs); this is
 * the shell's ends of it: followRoute, routeUpdated and routeBar.
 */
import { html, nothing } from 'lit';
import { frontWindow } from './zorder.js';
import { offloaded } from './menus.js';
import { parseRoute, buildRoute, sameRoute, validTile, cleanAt } from './route.js';

// followRoute(shell) → stop: take the page's URL as the place to land on once
// the layout and the components are in, and every back/forward after.
export function followRoute(s) {
  s._routeWant = parseRoute(location.search);
  s._routeReplace = true; // the first write describes the page that loaded: no new entry
  const onPop = () => { s._routeWant = parseRoute(location.search); s._routeReplace = true; s.requestUpdate(); };
  // a tile's page moved: the URL's `at` follows the tile it names
  const onLoc = (e) => { if (e.detail?.path && e.detail.path === s._routeTile) s.requestUpdate(); };
  addEventListener('popstate', onPop);
  s.addEventListener('bx-location', onLoc);
  return () => { removeEventListener('popstate', onPop); s.removeEventListener('bx-location', onLoc); };
}

const ready = (s) => s._layoutLoaded && s._compsLoaded;

// a tab's tiles as shown (an org screen's draft, when one is open)
const tilesOf = (s, t) => (t.kind === 'org' ? (s._orgDrafts?.[t.id]?.tiles ?? t.s.tiles) : t.s.tiles) ?? [];

function activate(s, t) {
  if (t.kind === 'org') s._openOrgScreen(t.id); else s._openScreen(t.id);
}

// applyRoute(shell, route): land on route; what can't be reached is
// s._routeMsg.
function applyRoute(s, r) {
  const msgs = [];
  const tabs = s._tabList();
  const target = r.screen ? tabs.find((t) => t.id === r.screen) : null;
  if (r.screen && !target) msgs.push('The screen in this link doesn’t exist any more, or isn’t shared with you.');
  if (target && target.id !== s._active) activate(s, target);
  s._routeTile = '';
  if (r.tile) {
    const c = validTile(r.tile) ? s._components.find((x) => x.path === r.tile) : null;
    if (!c) msgs.push(`The tile ${r.tile} doesn’t exist, or you don’t have access to it — ask its owner or a workspace admin.`);
    else if (offloaded(c)) msgs.push(`The tile ${r.tile} is offloaded (archived): it opens again once an admin restores it.`);
    else {
      const holds = (t) => tilesOf(s, t).some((o) => o.path === r.tile);
      const here = s._tabList().find((t) => t.id === s._active);
      if (!(here && holds(here))) {
        // the linked screen doesn't hold it: another visible tab that does, else open it
        const other = !target && s._visibleTabs().find(holds);
        if (other) activate(s, other);
        else if (!s._activeOrgScreen && s._screen) s._toggle(r.tile);
        else s._openOnPersonal(r.tile); // a shared screen never opens a draft from a link
      }
      s._routeTile = r.tile;
      s._routeFocus = { path: r.tile, at: r.at, tries: 0 };
    }
  }
  s._routeMsg = msgs.length ? msgs : null;
  s._routeFront = s._front; // the active window before landing: only a later change moves the URL's tile
}

// the linked tile, once its card exists: to the front, into view, its page to `at`
function focusLinked(s) {
  const f = s._routeFocus;
  const fr = s._canvas?.frameFor(f.path);
  if (!fr) {
    if (++f.tries > 40) { s._routeFocus = null; return; } // never drawn (a screen mode without it): give up after ~4 s
    setTimeout(() => s.requestUpdate(), 100);
    return;
  }
  s._routeFocus = null;
  frontWindow(`tile:${f.path}`);
  s._routeFront = `tile:${f.path}`;
  fr.closest('.card, .float')?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' });
  fr.navigate?.(f.at);
}

// the place the page shows now
function current(s) {
  if (s._front !== s._routeFront) { // another window came to the front
    s._routeFront = s._front;
    if (s._front?.startsWith('tile:')) s._routeTile = s._front.slice(5);
  }
  const tile = s._routeTile && s._tiles.some((o) => o.path === s._routeTile) ? s._routeTile : '';
  return { screen: s._active || '', tile, at: tile ? cleanAt(s._canvas?.frameFor(tile)?.hash) : '' };
}

// routeUpdated(shell): after each render — land on a wanted route, else write
// the place shown into the URL when it moved.
export function routeUpdated(s) {
  if (!ready(s)) return;
  if (s._routeWant) { const r = s._routeWant; s._routeWant = null; applyRoute(s, r); s.requestUpdate(); return; }
  if (s._routeFocus) { focusLinked(s); return; }
  const cur = current(s), shown = parseRoute(location.search);
  if (sameRoute(cur, shown)) { s._routeReplace = false; return; }
  const url = location.pathname + buildRoute(cur, location.search) + location.hash;
  const moved = cur.screen !== shown.screen || cur.tile !== shown.tile;
  try {
    if (moved && !s._routeReplace) history.pushState(null, '', url);
    else history.replaceState(null, '', url);
  } catch { /* a document that refuses: the URL stays */ }
  s._routeReplace = false;
}

// routeBar(shell): what a link named but can't be shown, above the canvas
export function routeBar(s) {
  if (!s._routeMsg) return nothing;
  return html`<div class="orgbar routemsg" role="alert">
    <bx-icon class="ico" name="warning" label="Warning"></bx-icon>
    <span class="txt">${s._routeMsg.map((m) => html`<div>${m}</div>`)}</span>
    <span class="spacer"></span>
    <button class="act" @click=${() => { s._routeMsg = null; }}>dismiss</button>
  </div>`;
}
