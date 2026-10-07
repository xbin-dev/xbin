/**
 * shell-tabs.js — the shell's screen tabs (D187): the strip, its gestures
 * and the screen actions behind them. On a wide screen the strip sits in
 * the top bar, between the mark and the settings chip, scrolling sideways
 * when the tabs don't fit and keeping the active one in view; below 820px it
 * stays a row of its own under the bar (which holds the menu button and the
 * mark). Drag a tab to reorder it, or into a sidebar folder to park it;
 * double-click to rename; right-click for its menu — Rename…, the screen's
 * Layout (Canvas · Document, D187), Close. An org screen's name and layout
 * are its org admins' (meta-only PUT /screens/org, never a revision).
 * Extracted from bx-shell, which is at its size budget: bx-shell renders
 * tabStrip(this) and calls revealActiveTab(this) after each update; the
 * state (_screens, _orgScreens, _tabOrder, _hiddenOrg, _active) stays there.
 */
import { html, nothing } from 'lit';
import { worstStatus, statusIcon } from './shell-kit.js';

const uid = () => Math.random().toString(36).slice(2, 9);

// screenMode(screen): how a screen is arranged — 'doc' (Document mode,
// D187) or 'canvas' (absent: every screen before it).
export const screenMode = (s) => (s?.mode === 'doc' ? 'doc' : 'canvas');

// May this person rename an org screen, or change its layout? Its org's
// admins and workspace admins — the server's rule for meta changes.
const orgMeta = (s, os) => !!(s._isAdmin || s._adminOrgs?.has(os.org));

const orgPut = async (s, os, patch, what) => {
  try {
    const r = await fetch('/api/xbin/screens/org', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: os.id, org: os.org, ...patch }),
    });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) s._pushToast(os.org, { level: 'error', message: d.error ?? `${what} failed (${r.status})` });
  } catch { /* offline: the reload below shows what holds */ }
  s._loadShared();
};

export function addScreen(s) {
  const n = { id: uid(), name: `Screen ${s._screens.length + 1}`, tiles: [] };
  s._screens = [...s._screens, n];
  s._active = n.id;
  s._save();
}

export async function renameScreen(s, id) {
  const os = (s._orgScreens ?? []).find((x) => x.id === id);
  if (os) { // org screens: an org-admin act, meta-only (never bumps the revision)
    if (!orgMeta(s, os)) return;
    const name = prompt('Org screen name:', os.name);
    if (name == null || !name.trim() || name.trim() === os.name) return;
    await orgPut(s, os, { name: name.trim() }, 'rename');
    return;
  }
  const cur = s._screens.find((x) => x.id === id);
  const name = prompt('Screen name:', cur?.name ?? '');
  if (name == null || !name.trim()) return;
  s._screens = s._screens.map((x) => x.id === id ? { ...x, name: name.trim() } : x);
  s._save();
}

// setScreenMode(s, id, mode): Canvas or Document for one screen. A personal
// screen keeps it in the layout pref (`mode`, absent for the canvas); an
// org screen on the server, for everyone, by those who may rename it. The
// tiles are untouched either way: Document mode reads their rows from
// `doc`, or from reading order where there is none (doc-layout.js).
export async function setScreenMode(s, id, mode) {
  const m = mode === 'doc' ? 'doc' : undefined;
  const os = (s._orgScreens ?? []).find((x) => x.id === id);
  if (os) {
    if (!orgMeta(s, os) || screenMode(os) === screenMode({ mode: m })) return;
    s._orgScreens = s._orgScreens.map((x) => x.id === id ? { ...x, mode: m } : x); // at once; the reload confirms
    await orgPut(s, os, { mode: m ?? 'canvas' }, 'layout change');
    return;
  }
  s._screens = s._screens.map((x) => {
    if (x.id !== id) return x;
    const n = { ...x };
    if (m) n.mode = m; else delete n.mode;
    return n;
  });
  s._save();
}

export function closeScreen(s, id) {
  if (s._visibleTabs().length <= 1) return; // keep at least one open tab
  // A screen parked in the folder tree: closing the TAB just parks it (the
  // layout stays, restorable from the tree) instead of deleting it.
  if (s._isTracked(id)) {
    s._screens = s._screens.map((x) => x.id === id ? { ...x, parked: true } : x);
    if (s._active === id) s._active = s._visibleTabs()[0].id;
    s._save();
    return;
  }
  const cur = s._screens.find((x) => x.id === id);
  if (cur.tiles.length && !confirm(`Close screen "${cur.name}" and its ${cur.tiles.length} tile(s)?`)) return;
  s._screens = s._screens.filter((x) => x.id !== id);
  if (s._active === id) s._active = (s._visibleTabs()[0] ?? s._tabList()[0])?.id ?? '';
  s._save();
}

// Hide an org tab for me only; it stays listed under its org in the sidebar.
export function hideOrgTab(s, id) {
  if (s._visibleTabs().length <= 1) return;
  s._hiddenOrg = { ...(s._hiddenOrg ?? {}), [id]: true };
  if (s._active === id) s._active = s._visibleTabs()[0].id;
  s._save();
}

// Reorder tabs (personal and org alike) — the order is personal state.
function moveScreen(s, dragId, beforeId) {
  if (dragId === beforeId) return;
  const ids = s._tabList().map((t) => t.id);
  if (!ids.includes(dragId)) return;
  const rest = ids.filter((x) => x !== dragId);
  const i = beforeId ? rest.indexOf(beforeId) : -1;
  const at = i < 0 ? rest.length : i;
  s._tabOrder = [...rest.slice(0, at), dragId, ...rest.slice(at)];
  s._save();
}

// layoutItems(s, screen, kind): the menu's Layout lines for one screen —
// Canvas · Document, the current one checked; disabled on an org screen
// for those who may not rename it.
export function layoutItems(s, sc, kind) {
  if (!sc) return [];
  const may = kind !== 'org' || orgMeta(s, sc), cur = screenMode(sc);
  const hint = may ? '' : 'org admins';
  return [{ kind: 'header', label: 'Layout' },
    { icon: 'grid', label: 'Canvas', checked: cur === 'canvas', disabled: !may, hint, title: 'tiles anywhere on a grid, each its own size',
      action: () => setScreenMode(s, sc.id, 'canvas') },
    { icon: 'doc', label: 'Document', checked: cur === 'doc', disabled: !may, hint, title: 'a scrolling page of rows (1, 2 or 4 tiles wide); each tile grows to its content',
      action: () => setScreenMode(s, sc.id, 'doc') }];
}

// The tab's menu (right-click): Rename…, Layout, Close / Hide.
function tabMenu(s, e, kind, sc) {
  e.preventDefault(); e.stopPropagation();
  const many = s._visibleTabs().length > 1;
  const items = [
    { icon: 'pencil', label: 'Rename…', disabled: kind === 'org' && !orgMeta(s, sc), hint: kind === 'org' && !orgMeta(s, sc) ? 'org admins' : '',
      action: () => renameScreen(s, sc.id) },
    { kind: 'sep' }, ...layoutItems(s, sc, kind), { kind: 'sep' },
    kind === 'org'
      ? { icon: 'eye-slash', label: 'Hide from my tabs', disabled: !many, action: () => hideOrgTab(s, sc.id) }
      : { icon: 'xmark', label: 'Close', disabled: !many, action: () => closeScreen(s, sc.id) },
  ];
  s._menu = { items, x: e.clientX, y: e.clientY, anchor: null, sheet: s._mobile, title: sc.name };
}

// revealActiveTab(s): after a render, scroll the strip so the active tab is
// in view — only when the active tab changed, so a strip the person
// scrolled by hand stays where they put it.
const shown = new WeakMap();
export function revealActiveTab(s) {
  const strip = s.renderRoot?.querySelector('.tabs');
  const on = strip?.querySelector('.tab.on');
  if (!on || shown.get(s) === s._active) return;
  shown.set(s, s._active);
  const a = on.offsetLeft - strip.offsetLeft, b = a + on.offsetWidth;
  if (a < strip.scrollLeft) strip.scrollLeft = a;
  else if (b > strip.scrollLeft + strip.clientWidth) strip.scrollLeft = b - strip.clientWidth;
}

// tabStrip(s): the strip — every visible tab, then +.
export function tabStrip(s) {
  const tabs = s._visibleTabs(), many = tabs.length > 1;
  return html`<div class="tabs" role="tablist" aria-label="screens" @wheel=${(e) => {
      const el = e.currentTarget; // a mouse wheel scrolls the strip sideways
      if (!e.deltaX && e.deltaY && el.scrollWidth > el.clientWidth) { el.scrollLeft += e.deltaY; e.preventDefault(); }
    }}>
    ${tabs.map(({ kind, s: sc }) => {
      const tst = worstStatus(s._status, (sc.tiles ?? []).map((t) => t.path));
      const draft = kind === 'org' ? s._orgDrafts?.[sc.id] : null;
      const doc = screenMode(sc) === 'doc';
      const title = tst ? `${sc.name} — a tile here needs attention (${tst})`
        : kind === 'org'
          ? `org screen — shared with ${sc.org}${sc.canEdit ? ' (edit layout to change it for everyone)' : ' (read-only for you)'}${orgMeta(s, sc) ? ' · double-click to rename' : ' · managed by org admins'} · drag to reorder · close hides it for you · right-click for its layout`
          : 'drag to reorder · drag into a sidebar folder to park · double-click to rename · right-click for its layout';
      return html`
      <div class="tab ${sc.id === s._active ? 'on' : ''} ${tst ? 'st-' + tst : ''} ${kind === 'org' ? 'org' : ''}" draggable="true"
           role="tab" aria-selected=${sc.id === s._active ? 'true' : 'false'} data-screen=${sc.id}
           @click=${() => { s._active = sc.id; s._save(); }}
           @dblclick=${() => renameScreen(s, sc.id)}
           @contextmenu=${(e) => tabMenu(s, e, kind, sc)}
           @dragstart=${(e) => { e.dataTransfer.setData('application/bx-screen', sc.id);
             if (kind === 'org') e.dataTransfer.setData('application/bx-orgscreen', sc.id);
             e.dataTransfer.effectAllowed = 'move'; }}
           @dragover=${(e) => { if (e.dataTransfer.types.includes('application/bx-screen')) e.preventDefault(); }}
           @drop=${(e) => { e.preventDefault(); const d = e.dataTransfer.getData('application/bx-screen'); if (d) moveScreen(s, d, sc.id); }}
           title=${title}>
        ${doc ? html`<bx-icon class="mode" name="doc" label="Document layout" title="Document layout"></bx-icon>` : nothing}
        <span>${sc.name}</span>
        ${kind === 'org' ? html`<span class="ob">${sc.org}</span>` : nothing}
        ${kind === 'org' && !sc.canEdit ? html`<bx-icon class="ro" name="lock" label="read-only for you" title="read-only for you"></bx-icon>` : nothing}
        ${draft?.dirty ? html`<bx-icon class="dirty" name="pencil" label="unsaved draft" title="unsaved draft — Save and update for everyone"></bx-icon>` : nothing}
        ${tst === 'warn' || tst === 'error' ? statusIcon(tst) : nothing}
        ${many ? html`<button class="x" title=${kind === 'org' ? 'hide this org screen from my tabs (reopen it from the sidebar)' : 'close'}
          aria-label=${kind === 'org' ? `hide ${sc.name}` : `close ${sc.name}`}
          @click=${(e) => { e.stopPropagation(); kind === 'org' ? hideOrgTab(s, sc.id) : closeScreen(s, sc.id); }}><bx-icon name="xmark"></bx-icon></button>` : nothing}
      </div>`;
    })}
    <div class="tab add" role="button" tabindex="0" aria-label="new screen" @click=${() => addScreen(s)}
         @keydown=${(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); addScreen(s); } }} title="new screen"><bx-icon name="plus"></bx-icon></div>
  </div>`;
}
