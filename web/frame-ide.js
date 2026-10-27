// web/frame-ide.js — the tile window's layout as an IDE (D202): panes, each
// with its own tabs, split side by side or one over the other; a tool in
// the side panel (Files, Changes, PRs); the logs dock along the bottom.
// Pure functions over plain data, so hack/frame-ide.test.mjs runs them under
// node (`make js-test`); the view is frame-ide-view.js.
//
// The layout (`ide`):
//   {tree, panes: {<id>: {tabs: [ref], active: ref|null}}, focus: <id>,
//    side: 'files'|'changes'|'prs'|null, dock: {open, h}, n}
// tree: a pane {pane: <id>}, or a split {dir: 'row'|'col', r, a, b} — 'row'
//   puts a beside b, 'col' a over b; r is a's share (0.1–0.9).
// A tab is a ref:
//   s:<ref>   a session (a shell or an agent; <ref> is the session's stable
//             `ref`, which outlives a restart's new key)
//   f:<path>  a file, read-only
//   w:<path>  a file's changes not yet committed
//   c:<rev>   a commit's changes
//   pr:<n>    a change proposal
//   deploy    the tile's Deployments
// Every edit returns a new layout (lit sees the change); none touches the
// sessions themselves — sync() fits the layout to them.
//
// The window pref (term-sessions.js) keeps the layout as `ide` beside the
// fields older frames read: sessions by their server id (`s#<id>`), never a
// Deployments tab, a read-only past session's or a session still starting.

export const SIDE_TOOLS = ['files', 'changes', 'prs'];
export const DOCK_H = 200;
const MAX_PANES = 12, MAX_TABS = 48, MAX_DEPTH = 12;
const R_MIN = 0.1, R_MAX = 0.9;

const clampR = (r) => (Number.isFinite(Number(r)) && r !== null && r !== '' ? Math.min(R_MAX, Math.max(R_MIN, Number(r))) : 0.5);
const isPane = (n) => !!n && typeof n.pane === 'string';

export const isSession = (ref) => typeof ref === 'string' && ref.startsWith('s:');
export const sessionRef = (t) => (t?.ref ? `s:${t.ref}` : null);
const REF_RE = /^(s:[\w-]{1,64}|f:.{1,1024}|w:.{1,1024}|c:[0-9a-fA-F]{4,64}|pr:\d{1,9}|deploy)$/;

export function emptyIde() {
  return { tree: { pane: 'p1' }, panes: { p1: { tabs: [], active: null } }, focus: 'p1', side: null, dock: { open: false, h: DOCK_H }, n: 2 };
}

export function leaves(n, out = []) {
  if (!n) return out;
  if (isPane(n)) out.push(n.pane);
  else { leaves(n.a, out); leaves(n.b, out); }
  return out;
}
// paneOf(ide, ref) → the pane holding the tab, or null.
export const paneOf = (ide, ref) => leaves(ide.tree).find((id) => ide.panes[id]?.tabs.includes(ref)) ?? null;
// allTabs(ide) → every tab, pane by pane.
export const allTabs = (ide) => leaves(ide.tree).flatMap((id) => ide.panes[id]?.tabs ?? []);

const clone = (ide) => ({ ...ide, panes: Object.fromEntries(Object.entries(ide.panes).map(([k, p]) => [k, { tabs: [...p.tabs], active: p.active }])), dock: { ...ide.dock } });
function freshId(ide) {
  let n = Math.max(2, ide.n | 0);
  while (ide.panes[`p${n}`]) n++;
  return { id: `p${n}`, n: n + 1 };
}
function swapLeaf(n, id, fn) {
  if (isPane(n)) return n.pane === id ? fn(n) : n;
  return { ...n, a: swapLeaf(n.a, id, fn), b: swapLeaf(n.b, id, fn) };
}
function dropLeaf(n, id) {
  if (!n) return null;
  if (isPane(n)) return n.pane === id ? null : n;
  const a = dropLeaf(n.a, id), b = dropLeaf(n.b, id);
  return !a ? b : !b ? a : { ...n, a, b };
}
// a pane gone empty goes (unless it is the last one); focus moves to a pane that's left
function tidy(ide) {
  for (const id of leaves(ide.tree)) {
    const p = ide.panes[id];
    if (!p.tabs.includes(p.active)) p.active = p.tabs[p.tabs.length - 1] ?? null;
    if (!p.tabs.length && leaves(ide.tree).length > 1) { ide.tree = dropLeaf(ide.tree, id); delete ide.panes[id]; }
  }
  const ls = leaves(ide.tree);
  if (!ls.includes(ide.focus)) ide.focus = ls[0];
  return ide;
}

// focusPane(ide, id): the pane new tabs open in.
export function focusPane(ide, id) {
  if (!ide.panes[id] || ide.focus === id) return ide;
  return { ...ide, focus: id };
}

// activate(ide, id, ref): the pane shows that tab and has the focus.
export function activate(ide, id, ref) {
  const p = ide.panes[id];
  if (!p || !p.tabs.includes(ref) || (p.active === ref && ide.focus === id)) return ide;
  const out = clone(ide);
  out.panes[id].active = ref;
  out.focus = id;
  return out;
}

// openTab(ide, ref, id?): a tab already open is shown where it is;
// otherwise it opens in pane `id` (default: the focused one), after that
// pane's active tab.
export function openTab(ide, ref, id = ide.focus) {
  const at = paneOf(ide, ref);
  if (at) return activate(ide, at, ref);
  const out = clone(ide);
  const pid = out.panes[id] ? id : out.focus;
  const p = out.panes[pid];
  if (p.tabs.length >= MAX_TABS) return ide;
  const i = p.tabs.indexOf(p.active);
  p.tabs.splice(i < 0 ? p.tabs.length : i + 1, 0, ref);
  p.active = ref;
  out.focus = pid;
  return out;
}

// closeTab(ide, ref): the tab goes; a pane left empty goes too (not the last).
export function closeTab(ide, ref) {
  const at = paneOf(ide, ref);
  if (!at) return ide;
  const out = clone(ide);
  const p = out.panes[at], i = p.tabs.indexOf(ref);
  p.tabs.splice(i, 1);
  if (p.active === ref) p.active = p.tabs[Math.min(i, p.tabs.length - 1)] ?? null;
  return tidy(out);
}

// split(ide, id, dir): a new pane beside ('row') or below ('col') pane id;
// the pane's active tab moves into it when the pane has others to show.
export function split(ide, id, dir) {
  if (!ide.panes[id] || leaves(ide.tree).length >= MAX_PANES || depthOf(ide.tree, id) >= MAX_DEPTH) return ide;
  const out = clone(ide), { id: nid, n } = freshId(out);
  const p = out.panes[id];
  const moved = p.tabs.length > 1 && p.active ? [p.active] : [];
  const at = p.tabs.indexOf(p.active);
  p.tabs = p.tabs.filter((t) => !moved.includes(t));
  if (!p.tabs.includes(p.active)) p.active = p.tabs[Math.min(at, p.tabs.length - 1)] ?? null; // its neighbour, as a close does
  out.panes[nid] = { tabs: moved, active: moved[0] ?? null };
  out.tree = swapLeaf(out.tree, id, (leaf) => ({ dir: dir === 'col' ? 'col' : 'row', r: 0.5, a: leaf, b: { pane: nid } }));
  out.focus = nid; out.n = n;
  return out;
}

// drop(ide, ref, to, zone): a dragged tab let go over pane `to` — its middle
// ('c': the tab moves there) or its right or bottom edge ('r', 'b': a new
// pane there takes it). Dropping a pane's only tab on its own edge does nothing.
export function drop(ide, ref, to, zone) {
  const from = paneOf(ide, ref);
  if (!from || !ide.panes[to]) return ide;
  if (zone === 'c') {
    if (from === to) return activate(ide, to, ref);
    const out = clone(ide);
    out.panes[from].tabs = out.panes[from].tabs.filter((t) => t !== ref);
    out.panes[to].tabs.push(ref);
    out.panes[to].active = ref;
    out.focus = to;
    return tidy(out);
  }
  if (from === to && ide.panes[from].tabs.length < 2) return ide;
  if (leaves(ide.tree).length >= MAX_PANES || depthOf(ide.tree, to) >= MAX_DEPTH) return ide;
  const out = clone(ide), { id: nid, n } = freshId(out);
  out.panes[from].tabs = out.panes[from].tabs.filter((t) => t !== ref);
  out.panes[nid] = { tabs: [ref], active: ref };
  out.tree = swapLeaf(out.tree, to, (leaf) => ({ dir: zone === 'b' ? 'col' : 'row', r: 0.5, a: leaf, b: { pane: nid } }));
  out.focus = nid; out.n = n;
  return tidy(out);
}

// closePane(ide, id): its tabs move to the pane that takes its place.
export function closePane(ide, id) {
  const ls = leaves(ide.tree);
  if (ls.length < 2 || !ide.panes[id]) return ide;
  const out = clone(ide);
  out.tree = dropLeaf(out.tree, id);
  const to = sibling(ide.tree, id) ?? leaves(out.tree)[0];
  out.panes[to].tabs.push(...out.panes[id].tabs);
  delete out.panes[id];
  out.focus = to;
  return tidy(out);
}
// the first pane of the subtree that takes id's place when it goes
function sibling(n, id) {
  if (isPane(n)) return null;
  if (isPane(n.a) && n.a.pane === id) return leaves(n.b)[0];
  if (isPane(n.b) && n.b.pane === id) return leaves(n.a)[0];
  return sibling(n.a, id) ?? sibling(n.b, id);
}
function depthOf(n, id, d = 0) {
  if (isPane(n)) return n.pane === id ? d : -1;
  return Math.max(depthOf(n.a, id, d + 1), depthOf(n.b, id, d + 1));
}

// setRatio(ide, path, r): the split at `path` ('' the root, then 'a'/'b'
// steps) gives r to its first side, clamped.
export function setRatio(ide, path, r) {
  const set = (n, p) => (isPane(n) ? n : !p ? { ...n, r: clampR(r) } : p[0] === 'a' ? { ...n, a: set(n.a, p.slice(1)) } : { ...n, b: set(n.b, p.slice(1)) });
  return { ...ide, tree: set(ide.tree, path || '') };
}

export function setSide(ide, tool) {
  const side = SIDE_TOOLS.includes(tool) ? tool : null;
  return side === ide.side ? ide : { ...ide, side };
}
export function setDock(ide, open, h = ide.dock.h) {
  const d = { open: !!open, h: Math.round(Math.max(80, Math.min(1200, Number(h) || DOCK_H))) };
  return d.open === ide.dock.open && d.h === ide.dock.h ? ide : { ...ide, dock: d };
}

// sync(ide, refs, keep): fit the layout to the sessions (`refs`, in tab
// order): a session that's gone loses its tab, one no pane holds opens in
// the focused pane; a tab `keep` refuses (a file of a tile that changed) goes.
export function sync(ide, refs, keep = () => true) {
  const live = new Set(refs);
  let changed = false;
  const out = clone(ide);
  for (const id of leaves(out.tree)) {
    const p = out.panes[id];
    const tabs = p.tabs.filter((t) => (isSession(t) ? live.has(t) : keep(t)));
    if (tabs.length !== p.tabs.length) { p.tabs = tabs; changed = true; }
  }
  const held = new Set(allTabs(out));
  for (const r of refs) {
    if (held.has(r)) continue;
    const p = out.panes[out.focus];
    p.tabs.push(r);
    if (!p.active || !p.tabs.includes(p.active)) p.active = r;
    changed = true;
  }
  if (!changed) return ide;
  return tidy(out);
}

// rects(tree) → {panes: [{id, x, y, w, h}], dividers: [{path, dir, x, y, w, h, box}]}
// in fractions of the whole area; box is the split's own rect (a divider's
// drag measures its ratio inside it).
export function rects(tree) {
  const panes = [], dividers = [];
  const walk = (n, x, y, w, h, path) => {
    if (isPane(n)) { panes.push({ id: n.pane, x, y, w, h }); return; }
    const box = { x, y, w, h };
    if (n.dir === 'col') {
      const ha = h * n.r;
      walk(n.a, x, y, w, ha, path + 'a'); walk(n.b, x, y + ha, w, h - ha, path + 'b');
      dividers.push({ path, dir: 'col', x, y: y + ha, w, h: 0, box });
    } else {
      const wa = w * n.r;
      walk(n.a, x, y, wa, h, path + 'a'); walk(n.b, x + wa, y, w - wa, h, path + 'b');
      dividers.push({ path, dir: 'row', x: x + wa, y, w: 0, h, box });
    }
  };
  walk(tree, 0, 0, 1, 1, '');
  return { panes, dividers };
}

// ---- the window pref ----

const saveable = (ref) => !isSession(ref) && ref !== 'deploy';
// toPref(ide, idOf) → the pref's `ide`: sessions by server id (idOf(ref) →
// id, or null: a past session's, one still starting — not saved).
export function toPref(ide, idOf) {
  const mapRef = (r) => (isSession(r) ? (idOf(r) ? `s#${idOf(r)}` : null) : saveable(r) ? r : null);
  const panes = {};
  for (const id of leaves(ide.tree)) {
    const p = ide.panes[id];
    const tabs = p.tabs.map(mapRef).filter(Boolean);
    panes[id] = { tabs, active: mapRef(p.active) ?? tabs[tabs.length - 1] ?? null };
  }
  return { v: 1, tree: ide.tree, panes, focus: ide.focus, side: ide.side, dock: { ...ide.dock } };
}

// fromPref(p, refOf) → a layout from the pref's `ide` (refOf(serverId) → a
// session's ref, or null: gone), or null when it isn't one this frame reads.
// What came over the wire is checked: the shapes, the bounds, the refs.
export function fromPref(p, refOf) {
  if (!p || typeof p !== 'object' || p.v !== 1) return null;
  const seen = new Set();
  const walk = (n, d) => {
    if (!n || typeof n !== 'object' || d > MAX_DEPTH) return null;
    if (typeof n.pane === 'string') {
      if (!/^p\d{1,4}$/.test(n.pane) || seen.has(n.pane) || seen.size >= MAX_PANES) return null;
      seen.add(n.pane);
      return { pane: n.pane };
    }
    const a = walk(n.a, d + 1), b = walk(n.b, d + 1);
    if (!a || !b) return a || b;
    return { dir: n.dir === 'col' ? 'col' : 'row', r: clampR(n.r), a, b };
  };
  const tree = walk(p.tree, 0);
  if (!tree) return null;
  const mapRef = (r) => {
    if (typeof r !== 'string') return null;
    if (r.startsWith('s#')) { const ref = refOf(r.slice(2)); return ref ? `s:${ref}` : null; }
    return REF_RE.test(r) && saveable(r) ? r : null;
  };
  const panes = {}, held = new Set();
  let n = 2;
  for (const id of leaves(tree)) {
    const src = p.panes?.[id];
    const tabs = (Array.isArray(src?.tabs) ? src.tabs : []).map(mapRef).filter((r) => r && !held.has(r) && (held.add(r), true)).slice(0, MAX_TABS);
    const active = mapRef(src?.active);
    panes[id] = { tabs, active: tabs.includes(active) ? active : tabs[tabs.length - 1] ?? null };
    n = Math.max(n, Number(id.slice(1)) + 1);
  }
  const ls = leaves(tree);
  const out = { tree, panes, focus: ls.includes(p.focus) ? p.focus : ls[0], side: SIDE_TOOLS.includes(p.side) ? p.side : null, dock: { open: false, h: DOCK_H }, n };
  return tidy(setDock(out, !!p.dock?.open, p.dock?.h));
}

// fromLegacy(w) → a layout from an older frame's pref fields: code beside
// the terminal ('split', or code with beside) opens Files, proposals open
// PRs, logs open the dock; the Deployments layout was never saved.
export function fromLegacy(w) {
  const ide = emptyIde();
  const l = w?.layout === 'split' ? 'code' : w?.layout;
  if (l === 'code') ide.side = 'files';
  else if (l === 'prs') ide.side = 'prs';
  else if (l === 'logs') ide.dock = { open: true, h: DOCK_H };
  return ide;
}

// legacyOf(ide) → the fields older frames read ({layout, beside, paneW}):
// Files open as code beside the terminal, PRs as proposals beside it, the
// dock as logs beside it; otherwise the terminal alone.
export function legacyOf(ide) {
  if (ide.side === 'files') return { layout: 'code', beside: true, paneW: 55 };
  if (ide.side === 'prs') return { layout: 'prs', beside: true, paneW: 55 };
  if (ide.dock.open) return { layout: 'logs', beside: true, paneW: 55 };
  return { layout: 'term', beside: false, paneW: 55 };
}
