// shell/shell-kit.js — what the shell and its child elements share: the
// grid module the layout is measured in, a window's live square, the status
// levels' glyphs and words, the partitioned marker and partition chip (a
// card's head and a pop-out's), the touch long-press gesture, the
// shell-text selection check, the ⇄ change-proposal badge and the tile
// deployments store behind the ⇈ badges. Imported relatively by
// bx-shell.js and its child elements; nothing here touches element state.
import { html, nothing } from 'lit';
import '/vendor/bx-icons.js';
import { useDeployLookup, deploySummary, deployHint, deployFailed, deployNames, shownDeployment } from './menus.js';
import { partitionView, markTitle, partitionChip, framedTile } from './partition-mode.js';

// The grid module lives in grid-layout.js (lit-free, so its layout math is
// node-testable); re-exported here for the shell's existing imports.
export { GRID, GAP, DEF_W, DEF_H, MIN_W, MIN_H, snap } from './grid-layout.js';

// A window's live square (product-ui 3; D184): the 8 px square before a
// tile's name says the state of the code it shows — filled in the accent
// when the saved change is live, hollow while it builds, a danger outline
// and the word "failed" when the build failed, hollow for a tile that is
// switched off. The runtime, which the square's colour used to say, is in
// its tooltip. state: 'live' | 'building' | 'failed' (bx-frame buildState)
// or 'off' (liveState below); runtime: the /components row's.
const RUNTIME_NAME = { go: 'a Go backend', node: 'a Node backend', python: 'a Python backend' };
const LIVE_WORD = { live: 'live', building: 'building', failed: 'build failed', off: 'switched off' };
export const runtimeName = (runtime) => RUNTIME_NAME[runtime] ?? 'static (no backend)';
// liveState(c, build) → the square's state for the tile's /components row c
// and its frame's build state ('' while the frame isn't there yet).
export function liveState(c, build) {
  if (c?.state && c.state !== 'enabled') return 'off';
  return build === 'building' || build === 'failed' ? build : 'live';
}
export function liveSquare(state, runtime) {
  const title = `${LIVE_WORD[state] ?? 'live'} · ${runtimeName(runtime)}`;
  return html`<span class="lsq ${state}" role="img" aria-label=${title} title=${title}></span>${state === 'failed'
    ? html`<span class="lfail">failed</span>` : nothing}`;
}

// The status levels (tiles → workspace, /alerts): a glyph of its own shape
// and a word with the colour (R2) — never a dot alone.
export const STATUS = Object.freeze({
  ok: { icon: 'ok', word: 'OK' }, info: { icon: 'info', word: 'Info' },
  warn: { icon: 'warning', word: 'Warning' }, error: { icon: 'error', word: 'Error' },
});
// statusIcon(level) → the level's glyph (class sti: its colour from --st,
// statusCss), named by its word.
export const statusIcon = (level) => {
  const s = STATUS[level] ?? STATUS.info;
  return html`<bx-icon class="sti" name=${s.icon} label=${s.word}></bx-icon>`;
};

// The partitioned marker (PD-53, design A): an 8px ring with its left half
// filled — "divided" by its shape, not by its hue alone — in a calm teal
// (--bx-part; shell-css.js partCss). Status, not a button: no border, no
// hover, cursor default, role img with the tooltip as its name. On a tile
// whose recorded mode has user partitions (a pending switch doesn't change
// it) it replaces the window head's runtime dot and sits before a sidebar
// row's ⋯; null for every other tile, and for every row of an xbind that
// doesn't partition, so the caller draws what it always drew.
export function partitionMark(c) {
  const t = markTitle(partitionView(c));
  if (!t) return null;
  return html`<span class="pm" role="img" aria-label=${t} title=${t}>${markShape}</span>`;
}
// markShape: the marker's drawing alone (the settings menu's "your
// partitions" entry carries it too, shell-account.js).
export const markShape = html`<svg viewBox="0 0 8 8" aria-hidden="true">
    <circle cx="4" cy="4" r="3.35" fill="none" stroke="currentColor" stroke-width="1.1"/>
    <path d="M4 .65a3.35 3.35 0 0 0 0 6.7z" fill="currentColor"/></svg>`;

// The partition chip (owner ruling I3; partition-mode.js partitionChip):
// whose partition a partitioned tile's window shows — quiet, in the
// marker's hue (shell-css.js partCss .pchip); a deployment's `shared` chip
// keeps F14's .dshare class. nothing for no chip.
export const chipTag = (chip) => (chip
  ? html`<span class=${chip.kind === 'shared' ? 'pchip dshare' : 'pchip'} data-chip=${chip.kind} title=${chip.title}>${chip.text}</span>`
  : nothing);

// spawnTitle(title, src, components, who) → the start of a tile's pop-out
// window's head (bx-shell _spawnTemplate; xbin.window): the framed tile's
// marker, the window's title and its partition chip — a pop-out framing a
// partitioned tile (a sub-path of its own, or spec.src) is a window of it
// too, so it says whose partition it shows, as the tile's card does.
export function spawnTitle(title, src, components, who) {
  const { row, shown: want } = framedTile(src, components);
  const shown = row ? shownDeployment(want, deployState(row.path)) : '';
  return html`${shown ? nothing : partitionMark(row)}<span class="stitle">${title}</span>${chipTag(partitionChip(partitionView(row), { shown, who }))}`;
}

// Long-press (touch/pen, phones only — the mouse keeps right-click): hold
// ~450 ms without moving 8px to open the menu the right-click would. A
// scroll cancels (pointercancel), and the menu's backdrop ignores the
// press's own pointerup/click.
export class LongPress {
  start(e, fire, mobile) {
    if (!mobile || e.pointerType === 'mouse' || e.button !== 0) return;
    this.cancel();
    const x = e.clientX, y = e.clientY;
    this._p = { x, y, t: setTimeout(() => { this._p = null; fire(); }, 450) };
  }
  move(e) { if (this._p && Math.hypot(e.clientX - this._p.x, e.clientY - this._p.y) > 8) this.cancel(); }
  cancel() { if (this._p) { clearTimeout(this._p.t); this._p = null; } }
}

// Selected text in the shell's own document — toString() of the document
// selection (Chromium and Firefox both read it through nested shadow
// roots for real, selectable text) or of Chromium's non-standard
// ShadowRoot.getSelection(). Not getComposedRanges: it also reports ranges
// over user-select:none chrome (card heads, sidebar rows — which no person
// can select) and retargets nested-shadow ranges to the host, so a caret
// left by a click read as "text selected" and swallowed every right-click.
export function selectedText(root) {
  for (const r of [root, document]) {
    const t = r?.getSelection?.()?.toString?.() ?? '';
    if (t.trim()) return t;
  }
  return '';
}

// The ⇄ badge: n open change proposals; with onOpen it is the button that
// opens the tile's proposals panel (card heads), without it a plain marker
// (sidebar rows).
export function prBadge(n, onOpen = null) {
  if (!n) return nothing;
  const title = `${n} open change proposal${n === 1 ? '' : 's'} — review in the tile's terminal window (its proposals panel)`;
  return onOpen
    ? html`<button class="prb" title=${title} aria-label=${title}
                   @pointerdown=${(e) => e.stopPropagation()}
                   @click=${(e) => { e.stopPropagation(); onOpen(); }}><bx-icon name="diff"></bx-icon>${n}</button>`
    : html`<span class="prb" title=${title}><bx-icon name="diff"></bx-icon>${n}</span>`;
}

// Tree items: '#screen:<id>' parks a personal tab, '#orgscreen:<id>' references
// an org screen (D55) — the live screen either way, never a snapshot.
export const isScreenItem = (s) => typeof s === 'string' && s.startsWith('#screen:');
export const isOrgScreenItem = (s) => typeof s === 'string' && s.startsWith('#orgscreen:');
export const screenIdOf = (s) => s.slice(s.indexOf(':') + 1);

// Owner sections (D24) ↔ shared-folder scopes (D55): 'workspace' ↔ 'ws',
// 'org:<id>' ↔ itself, 'mine' has no shared set.
export const scopeOf = (sectionKey) => (sectionKey === 'workspace' ? 'ws' : sectionKey?.startsWith('org:') ? sectionKey : null);
export const sectionOf = (scope) => (scope === 'ws' ? 'workspace' : scope);
// Which section a component lists under: mine (the caller owns it), its org, or workspace.
export function ownerKeyOf(c, myId) {
  const owner = c?.owner ?? '';
  if (myId && owner === 'user:' + myId) return 'mine';
  return owner.startsWith('org:') ? owner : 'workspace';
}

// Highest-severity status among the given component paths (null if none).
export function worstStatus(status, paths) {
  const rank = { ok: 0, info: 1, warn: 2, error: 3 };
  let best = null, bestR = -1;
  for (const p of paths) {
    const s = status?.[p];
    if (s && (rank[s.level] ?? -1) > bestR) { best = s.level; bestR = rank[s.level]; }
  }
  return best;
}

// ---- tile deployments (optional; docs/tile-deployments.md) ----
// One per-page store of tiles' deployments states (GET
// /api/xbin/deployments?tile=, in the signed-in person's view: raw fetch,
// like the tile admin's calls) for the ⇈ badges, the tile menu's
// Deployments line and the tile admin. A state loads only for a tile whose
// /components row carries the primary summary, or whose record a
// `deployments` event says changed: an xbind without tile deployments sends
// neither, so it is never asked. Loaded states follow their tile's record
// and deploy events and the page coming back into view. Words come from the
// binary's /vendor/deploy-state.js by dynamic import: an older binary
// doesn't serve it, and a failed static import would take the whole shell
// down (docs/compat.md rule 3), so a failed import leaves plain text.
const dstore = new Map(); // path → {state: undefined | null | State, gen}
const dsubs = new Set();
let dwords = null, dwordsAsked = false, dfollowing = false, dseen = 0;

const dnotify = () => { for (const fn of dsubs) { try { fn(); } catch (err) { console.error(err); } } };
// onDeployChange(fn) → unsubscribe: fn runs after a state loads or the words arrive.
export function onDeployChange(fn) { dsubs.add(fn); return () => dsubs.delete(fn); }
// deployState(path) → undefined (not loaded), null (this xbind couldn't answer) or the state.
export const deployState = (path) => dstore.get(path)?.state;
// loadDeployState(path): fetch the tile's state now; the newest answer wins.
export function loadDeployState(path) {
  const r = dstore.get(path) ?? { state: undefined, gen: 0 };
  dstore.set(path, r);
  const gen = ++r.gen;
  return fetch(`/api/xbin/deployments?tile=${encodeURIComponent(path)}`)
    .then((res) => (res.ok ? res.json() : null)).catch(() => null)
    .then((s) => {
      if (gen !== r.gen) return;
      r.state = s && typeof s === 'object' && s.tile === path ? s : null;
      if (r.state?.record && !dwordsAsked) {
        dwordsAsked = true;
        import('/vendor/deploy-state.js').then((m) => { dwords = m; dnotify(); }, () => { });
      }
      dnotify();
    });
}
// wantDeployState(path, c): load once, for a tile whose row carries the summary.
export function wantDeployState(path, c) {
  if (c?.deployments && !dstore.has(path)) loadDeployState(path);
}
useDeployLookup((path, c) => { wantDeployState(path, c); return deployState(path); });

// followDeployments(): keep the store current (once per page). A record
// event also loads a tile nobody asked about yet: its first record.
export function followDeployments() {
  if (dfollowing || typeof window.xbin?.events?.on !== 'function') return;
  dfollowing = true;
  const timers = new Map();
  window.xbin.events.on((e) => {
    const op = e?.type === 'deployments' ? e.data?.op : '', p = e?.component;
    if (!p || !(op === 'record' || (op === 'deploy' && dstore.has(p)))) return;
    clearTimeout(timers.get(p));
    timers.set(p, setTimeout(() => { timers.delete(p); loadDeployState(p); }, 300));
  });
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState !== 'visible' || !dstore.size || Date.now() - dseen < 30000) return;
    dseen = Date.now();
    for (const p of dstore.keys()) loadDeployState(p);
  });
}

// deployChip(c, state) → null, or the card head's ⇈ badge {icon, title,
// failed}: while the primary is pinned or its last deploy failed, titled
// with the terminal window's chip sentence once the words are here. A
// failed deploy draws the error glyph and the word (R2).
export function deployChip(c, st) {
  const sum = deploySummary(c, st), failed = deployFailed(st);
  if (!sum || (!sum.pinned && !failed)) return null;
  let title = '';
  try { title = (st?.record && dwords?.chip?.(st)?.title) || ''; } catch { /* the words changed: plain text */ }
  return { icon: failed ? 'error' : 'deploy', title: title || deployHint(sum, st), failed };
}
// deployIcon(c, state, shown) → null, or a tile window head's ⇈ {icon,
// title, failed}: while the tile has a deployment the viewer may show besides
// the primary, the primary is pinned, or its last deploy failed — and while
// the window shows another deployment (shown). Its click picks what the
// window shows (menus.js deployMenu).
export function deployIcon(c, st, shown) {
  const chip = deployChip(c, st), others = deployNames(st).length > 1;
  if (!chip && !others && !shown) return null;
  const title = [chip?.title, others ? 'pick the deployment this window shows' : ''].filter(Boolean).join(' · ');
  return { icon: chip?.icon || 'deploy', title, failed: !!chip?.failed };
}
// deployBadge(chip, onOpen): the ⇄N badge's manners; with onOpen a button
// (onOpen(event): a menu anchors at event.currentTarget).
export function deployBadge(d, onOpen = null) {
  if (!d) return nothing;
  const body = html`<bx-icon name=${d.icon || 'deploy'}></bx-icon>${d.failed ? 'failed' : nothing}`;
  return onOpen
    ? html`<button class="prb dpb ${d.failed ? 'bad' : ''}" title=${d.title} aria-label=${d.title}
                   @pointerdown=${(e) => e.stopPropagation()}
                   @click=${(e) => { e.stopPropagation(); onOpen(e); }}>${body}</button>`
    : html`<span class="prb dpb ${d.failed ? 'bad' : ''}" title=${d.title}>${body}</span>`;
}
// openDeployments(root, path): the tile's terminal window on its Deployments
// layout, from an element inside the shell (root: its getRootNode()); a tile
// that has no card yet is opened first, as the tile menu's squares do.
export function openDeployments(root, path) {
  const canvas = root?.querySelector?.('bx-canvas');
  if (canvas && !canvas.frameOpen(path, 'deployments')) {
    canvas.dispatchEvent(new CustomEvent('bx-toggle-tile', { detail: path, bubbles: true, composed: true }));
  }
}
