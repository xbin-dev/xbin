/**
 * frame-ide-view.js — the tile window's body as an IDE (D202). `f` is the
 * BxFrame; this reads its state (f._ide, the layout — web/frame-ide.js —
 * and f._sessions) and sets it.
 *
 * Down the left, the tool strip: Files, Changes and PRs open their list in
 * the side panel beside the panes (one at a time; again closes it); Deploy
 * opens the tile's Deployments as a tab (on an xbind with tile
 * deployments); Logs, at the bottom, opens the logs dock along the bottom.
 * Each tool is its glyph and one word under it (the glyph alone in a narrow
 * window, where the side panel opens over the panes).
 *
 * The panes fill the rest: each its own tab strip — sessions (shells and
 * coding agents), files, changes, change proposals, Deployments — with +
 * for a new session there, and split right / split down / close. A tab
 * from a list (a file, a commit, a proposal) opens in the focused pane: the
 * one last clicked or typed in; a tab already open shows where it is.
 * Tabs drag: onto another pane's middle to move there, onto a pane's right
 * or bottom edge to split it. Dividers drag (and take the arrow keys);
 * a double-click evens them. An empty pane offers the launcher.
 *
 * The sessions never move in the DOM: a <bx-terminal> or <bx-agent> lives
 * in one layer over the panes (the stage), keyed by session, in an order
 * that never changes (by key), and is placed over the body of the pane
 * whose active tab it is — hidden otherwise. A tab moved to another pane, a
 * split, a divider dragged: the element only changes its box, so a shell
 * keeps its scrollback and an agent its scroll position. The logs dock's
 * <bx-logs> is mounted once and hidden while the dock is shut.
 *
 * The status bar under it all is frame-status.js. The lists and views the
 * tools open are their own modules, loaded when first wanted:
 * code-tree.js (<bx-code-tree>, <bx-code-changes>), code-view.js
 * (<bx-code-view>, <bx-code-diff>), pr-panels.js (<bx-pr-list>,
 * <bx-pr-view>); a list asks for a tab with `bx-open-tab` {ref}.
 */
import { html, css, nothing, repeat } from 'lit';
import '/vendor/bx-icons.js';
import { dragPointer, clampBox } from '/vendor/bx-kit.js';
import { launcher, resumeHistory } from '/vendor/frame-launcher.js';
import { hasLayout } from '/vendor/frame-deploy.js';
import { sessionTarget } from '/vendor/deploy-state.js';
import { layoutToPref, uid } from '/vendor/term-sessions.js';
import * as M from '/vendor/frame-ide.js';
import { statusBar, sessionLabel } from '/vendor/frame-status.js';

const SHEET = typeof matchMedia === 'function' ? matchMedia('(max-width: 820px)') : { matches: false };
const TAB_H = 29; // a pane's tab strip: 28 px and its edge

// ---- the panels' modules, loaded when first wanted ----
const MODS = { tree: '/vendor/code-tree.js', view: '/vendor/code-view.js', prs: '/vendor/pr-panels.js' };
const loaded = new Set();
function need(f, m) {
  if (loaded.has(m)) return;
  loaded.add(m);
  import(MODS[m]).then(() => f.requestUpdate(), () => setTimeout(() => loaded.delete(m), 30000)); // failed: asked again later, not on every render
}
const modOf = (ref) => (ref.startsWith('pr:') ? 'prs' : /^[fwc]:/.test(ref) ? 'view' : null);

// ---- the layout's state on the frame ----
const set = (f, ide) => { if (ide !== f._ide) f._ide = ide; };
const refOf = (t) => `s:${t.ref}`;
const sessionAt = (f, ref) => f._sessions.findIndex((t) => refOf(t) === ref);
// other tabs than sessions: they keep the window open when its last session goes
export const ideHasTabs = (f) => M.allTabs(f._ide).some((r) => !M.isSession(r));

// ideSync(f): before each render where the sessions or the session in view
// changed — every session has a ref, the layout holds every session (a new
// one in the focused pane), and the session in view shows in its pane.
export function ideSync(f) {
  if (f._sessions.some((t) => !t.ref)) f._sessions = f._sessions.map((t) => (t.ref ? t : { ...t, ref: uid() }));
  let ide = M.sync(f._ide, f._sessions.map(refOf));
  const cur = f._sessions[f._active];
  if (cur && cur.key !== f._ideShown) {
    f._ideShown = cur.key;
    const r = refOf(cur), at = M.paneOf(ide, r);
    if (at && ide.panes[at].active !== r) ide = { ...M.activate(ide, at, r), focus: ide.focus };
  }
  set(f, ide);
}

// ideRestore(f, w): the window pref's layout (its `ide`, else an older
// frame's fields), fitted to the sessions the server lists now.
export function ideRestore(f, w) {
  const ide = M.fromPref(w?.ide, (id) => f._sessions.find((t) => t.id === id)?.ref ?? null) ?? M.fromLegacy(w);
  f._ide = M.sync(ide, f._sessions.filter((t) => t.ref).map(refOf));
  f._ideShown = f._sessions[f._active]?.key;
}

// idePref(f): the layout part of the window pref — the fields older frames
// read, and `ide` (sessions by server id; one without an id isn't saved).
export function idePref(f) {
  const idOf = (r) => f._sessions.find((t) => refOf(t) === r)?.id || null;
  return { ...layoutToPref(M.legacyOf(f._ide)), ide: M.toPref(f._ide, idOf) };
}

// ---- what the frame and its menus do ----

// a session chosen in a pane is the session in view (the status bar's)
function choose(f, ref) {
  const i = sessionAt(f, ref);
  if (i >= 0 && i !== f._active) f._setActive(i);
}
// a narrow window's side panel lies over the panes: a click in a pane shuts it
const unveil = (f) => { if (f._narrow && f._ide.side) set(f, M.setSide(f._ide, null)); };
export function focusPane(f, id) {
  unveil(f);
  set(f, M.focusPane(f._ide, id));
  const a = f._ide.panes[id]?.active;
  if (a && M.isSession(a)) choose(f, a);
}
export function activateTab(f, id, ref) {
  unveil(f);
  set(f, M.activate(f._ide, id, ref));
  if (M.isSession(ref)) choose(f, ref);
}
// openRef(f, ref, pane?): a tab opens (or shows where it is).
export function openRef(f, ref, pane) {
  const m = modOf(ref);
  if (m) need(f, m);
  set(f, M.openTab(f._ide, ref, pane));
  if (M.isSession(ref)) choose(f, ref);
}
function closeRef(f, ref) {
  if (M.isSession(ref)) { const i = sessionAt(f, ref); if (i >= 0) f._closeTerm(i); return; }
  set(f, M.closeTab(f._ide, ref));
  if (!f._sessions.length && !ideHasTabs(f)) f._termOpen = false;
}
export function splitPane(f, id, dir) { set(f, M.split(f._ide, id, dir)); }
export function closePane(f, id) { set(f, M.closePane(f._ide, id)); }
export function dropTab(f, ref, to, zone) { set(f, M.drop(f._ide, ref, to, zone)); }

// a narrow window widens when a panel opens beside the panes (as before D202)
function widen(f) {
  if (SHEET.matches || !f._pop || f._pop.w >= 760) return;
  const box = { ...f._popBox(), w: 960 };
  f._setPopBox(f._bounds() ? box : clampBox(box));
  f._saveTerm(); f._popChanged();
}
export function sideTool(f, tool, force = false) {
  const side = force || f._ide.side !== tool ? tool : null;
  if (side) { need(f, side === 'prs' ? 'prs' : 'tree'); widen(f); }
  set(f, M.setSide(f._ide, side));
}
export function dock(f, open) {
  if (open) f._dockMounted = true;
  set(f, M.setDock(f._ide, open));
}
function tool(f, id) {
  if (id === 'deploy') openRef(f, 'deploy');
  else if (id === 'logs') dock(f, !f._ide.dock.open);
  else sideTool(f, id);
}

// openLayout(f, layout): what open(layout) shows — the values the shell's
// tile menu has always passed: 'term' the session in view, 'code' (and
// 'split') the Files list, 'logs' the dock, 'prs' the proposals and the
// newest open one, 'deployments' the Deployments tab.
export function openLayout(f, layout) {
  const l = layout === 'split' ? 'code' : layout;
  if (l === 'code') sideTool(f, 'files', true);
  else if (l === 'logs') dock(f, true);
  else if (l === 'deployments') openRef(f, 'deploy');
  else if (l === 'prs') {
    sideTool(f, 'prs', true);
    fetch(`/api/xbin/code/prs?target=${encodeURIComponent(f.src)}&state=open`).then((r) => (r.ok ? r.json() : {}))
      .then((d) => { const n = Math.max(0, ...(d.prs || []).filter((p) => !p.state || p.state === 'open').map((p) => p.number | 0)); if (n) openRef(f, `pr:${n}`); }).catch(() => { });
  } else if (l === 'term') {
    const cur = f._sessions[f._active], r = cur && refOf(cur), at = r && M.paneOf(f._ide, r);
    if (at) activateTab(f, at, r);
  }
}

// ideLayout(f): the layout as the names open() takes (the test surface's
// `layout`): the Deployments tab in view, else the side panel's, the dock,
// or the terminal.
export function ideLayout(f) {
  const ide = f._ide, a = ide.panes[ide.focus]?.active;
  if (a === 'deploy') return 'deployments';
  if (ide.side === 'files' || ide.side === 'changes') return 'code';
  if (ide.side === 'prs') return 'prs';
  return ide.dock.open ? 'logs' : 'term';
}

// ---- drawing ----

const pct = (v) => `${(v * 100).toFixed(4)}%`;
// a pane's box in the panes' area: a pixel's gap to the pane before it
function box(r, top = 0) {
  const gx = r.x > 1e-6 ? 1 : 0, gy = r.y > 1e-6 ? 1 : 0;
  return `left:calc(${pct(r.x)} + ${gx}px); top:calc(${pct(r.y)} + ${gy + top}px); width:calc(${pct(r.w)} - ${gx}px); height:calc(${pct(r.h)} - ${gy + top}px)`;
}
const base = (p) => p.slice(p.lastIndexOf('/') + 1) || p;

// what a tab says: its glyph, its words, its tooltip
function tabInfo(f, ref) {
  if (M.isSession(ref)) {
    const t = f._sessions[sessionAt(f, ref)];
    const name = t ? sessionLabel(t) : '…';
    return { label: name, icon: t?.kind === 'agent' ? 'agent' : 'terminal', ended: !!t?.ended,
      title: `${name}${t?.ended ? ' (ended)' : ''} — double-click to rename · drag to another pane, or to a pane's edge to split` };
  }
  if (ref.startsWith('f:')) return { label: base(ref.slice(2)), icon: 'file', title: ref.slice(2) };
  if (ref.startsWith('w:')) return { label: base(ref.slice(2)), icon: 'diff', note: 'changes', title: `${ref.slice(2)} — changes not yet committed` };
  if (ref.startsWith('c:')) return { label: ref.slice(2, 9), icon: 'diff', title: `commit ${ref.slice(2)}` };
  if (ref.startsWith('pr:')) return { label: `#${ref.slice(3)}`, icon: 'branch', title: `change proposal #${ref.slice(3)}` };
  return { label: 'Deployments', icon: 'deploy', title: "this tile's deployments" };
}

// a non-session tab's view
function view(f, ref) {
  const src = f.src;
  if (ref.startsWith('f:')) return html`<bx-code-view component=${src} path=${ref.slice(2)}></bx-code-view>`;
  if (ref.startsWith('w:')) return html`<bx-code-diff component=${src} rev="" path=${ref.slice(2)}></bx-code-diff>`;
  if (ref.startsWith('c:')) return html`<bx-code-diff component=${src} rev=${ref.slice(2)}></bx-code-diff>`;
  if (ref.startsWith('pr:')) return html`<bx-pr-view target=${src} n=${ref.slice(3)} .ask=${(spec) => f._ask(spec)}></bx-pr-view>`;
  // the active session's target as a property, so the panel's Dev API tag follows it
  if (ref === 'deploy') return html`<bx-deployments component=${src} .frame=${f} .target=${sessionTarget(f._sessions[f._active])}></bx-deployments>`;
  return nothing;
}

function tabTpl(f, id, p, ref, on = p.active === ref) {
  const info = tabInfo(f, ref);
  const i = M.isSession(ref) ? sessionAt(f, ref) : -1;
  return html`<div class=${`ptab${on ? ' on' : ''}${info.ended ? ' ended' : ''}`} role="tab" aria-selected=${on ? 'true' : 'false'}
      tabindex=${on ? '0' : '-1'} draggable="true" title=${info.title} data-ref=${ref}
      @click=${() => activateTab(f, id, ref)}
      @keydown=${(e) => tabKey(f, e, id, p, ref)}
      @dblclick=${() => { if (i >= 0) f._renameTerm(i); }}
      @auxclick=${(e) => { if (e.button === 1) { e.preventDefault(); closeRef(f, ref); } }}
      @dragstart=${(e) => dragStart(f, e, ref, id)} @dragend=${() => dragEnd(f)}>
    <bx-icon name=${info.icon}></bx-icon><span class="lbl">${info.label}</span>${info.note ? html`<span class="note">${info.note}</span>` : nothing}
    <button class="tabx" aria-label=${`close ${info.label}`} title=${i >= 0 ? (info.ended ? 'dismiss (the session has ended)' : 'end this session') : 'close'}
      @click=${(e) => { e.stopPropagation(); closeRef(f, ref); }}><bx-icon name="xmark"></bx-icon></button>
  </div>`;
}

// a tab strip's keys (the ARIA tabs pattern): ←/→, Home and End move along
// it, choosing as they go; Enter or Space chooses
function tabKey(f, e, id, p, ref) {
  const i = p.tabs.indexOf(ref);
  const to = { ArrowLeft: i - 1, ArrowRight: i + 1, Home: 0, End: p.tabs.length - 1 }[e.key];
  if (e.key === 'Enter' || e.key === ' ') activateTab(f, id, ref);
  else if (to !== undefined && p.tabs[to]) {
    activateTab(f, id, p.tabs[to]);
    f.updateComplete.then(() => [...f.renderRoot.querySelectorAll(`.pane[data-pane="${id}"] .ptab`)].find((el) => el.dataset.ref === p.tabs[to])?.focus());
  } else return;
  e.preventDefault();
}

function paneTpl(f, r, n) {
  const id = r.id, p = f._ide.panes[id], focus = f._ide.focus === id, sheet = SHEET.matches;
  const a = p.active;
  // the phone sheet shows the focused pane alone (ideBody): its strip holds
  // every pane's tabs, each choosing its own pane, so none is out of reach
  const tabs = sheet && n === 1 && M.leaves(f._ide.tree).length > 1
    ? M.leaves(f._ide.tree).flatMap((pid) => f._ide.panes[pid].tabs.map((ref) => tabTpl(f, pid, f._ide.panes[pid], ref, pid === id && a === ref)))
    : p.tabs.map((ref) => tabTpl(f, id, p, ref));
  return html`<section class=${`pane${focus ? ' focus' : ''}`} style=${box(r)} data-pane=${id} @pointerdown=${() => focusPane(f, id)}>
    <div class="ptabs" @dragover=${(e) => over(f, e, id, 'c')} @drop=${(e) => dropOn(f, e, id, 'c')}>
      <div class="strip" role="tablist" aria-label="tabs" @wheel=${scrollStrip}>${tabs}</div>
      <button class="ib" title="new session here: Bash or a coding agent" aria-label="new session"
        @click=${(e) => { focusPane(f, id); f._openLauncher(e); }}><bx-icon name="plus"></bx-icon></button>
      <span class="grow"></span>
      ${sheet ? nothing : html`
        <button class="ib" title="split right" aria-label="split right" @click=${() => splitPane(f, id, 'row')}><bx-icon name="split"></bx-icon></button>
        <button class="ib down" title="split down" aria-label="split down" @click=${() => splitPane(f, id, 'col')}><bx-icon name="split"></bx-icon></button>`}
      ${n > 1 ? html`<button class="ib" title="close this pane (its tabs move to the pane beside it)" aria-label="close this pane"
        @click=${() => closePane(f, id)}><bx-icon name="minus"></bx-icon></button>` : nothing}
    </div>
    <div class="pbody">${!a ? launcher(f) : M.isSession(a) ? nothing : view(f, a)}</div>
  </section>`;
}

// the stage: every session, in one stable order, over its pane's body
function stage(f, placed) {
  const order = [...f._sessions].sort((a, b) => (a.key < b.key ? -1 : a.key > b.key ? 1 : 0));
  return html`<div class="stage">${repeat(order, (t) => t.key, (t) => {
    const at = placed.get(refOf(t));
    const style = at ? `${box(at, TAB_H)}; display:${t.kind === 'agent' ? 'flex' : 'block'}` : 'display:none';
    const focus = () => { const id = M.paneOf(f._ide, refOf(t)); if (id) activateTab(f, id, refOf(t)); };
    return t.kind === 'agent'
      ? html`<bx-agent style=${style} data-key=${t.key} @pointerdown=${focus} @focusin=${focus}
          component=${f.src} session=${t.id ?? nothing} provider=${t.history || t.restarting ? nothing : (t.provider || nothing)} ?ended=${!!t.ended} ?restarting=${!!t.restarting} ?vm=${!!t.vm}
          history=${t.history || nothing} resume=${t.resume || nothing}
          @bx-session=${(ev) => f._gotSession(t.key, ev)}
          @bx-resume=${(ev) => resumeHistory(f, ev.detail, t.key)} @bx-new-agent=${(ev) => f._startKind('agent', ev.detail.provider)}
          @bx-open-terminal=${(ev) => f._signIn(ev)}
          @bx-exit=${() => f._endTab(t.key)}></bx-agent>`
      : html`<bx-terminal style=${style} data-key=${t.key} @pointerdown=${focus} @focusin=${focus}
          cwd=${f.src} session=${t.id ?? nothing} net=${t.net || nothing} gpu=${t.gpu || 'none'} deployment=${t.deployment || nothing} api=${t.api === false ? '0' : '1'} vm=${t.vm ? '1' : '0'} run=${t.run || nothing}
          @bx-session=${(ev) => f._gotSession(t.key, ev)}
          @bx-exit=${() => f._closeTerm(t.key, true)}></bx-terminal>`;
  })}</div>`;
}

// ---- tab drag: the middle of a pane moves, its right or bottom edge splits ----
function dragStart(f, e, ref, from) {
  try { e.dataTransfer.setData('application/x-bx-tab', ref); e.dataTransfer.effectAllowed = 'move'; } catch { /* a synthetic event */ }
  // the drop zones appear after the drag began: a DOM change in dragstart itself ends it in Chrome
  setTimeout(() => { f._ideDrag = { ref, from }; f._ideOver = null; f.requestUpdate(); }, 0);
}
function dragEnd(f) { f._ideDrag = null; f._ideOver = null; f.requestUpdate(); }
function over(f, e, id, zone) {
  if (!f._ideDrag) return;
  e.preventDefault();
  const o = f._ideOver;
  if (!o || o.id !== id || o.zone !== zone) { f._ideOver = { id, zone }; f.requestUpdate(); }
}
function dropOn(f, e, id, zone) {
  const d = f._ideDrag;
  if (!d) return;
  e.preventDefault();
  dragEnd(f);
  dropTab(f, d.ref, id, zone);
}
function dropLayer(f, rs) {
  const o = f._ideOver, sheet = SHEET.matches;
  return html`<div class="dzl">${rs.map((r) => html`<div class="dzp" style=${box(r, TAB_H)}>
    ${o?.id === r.id ? html`<div class=${`dzhot ${o.zone}`}></div>` : nothing}
    <div class=${sheet ? 'dz all' : 'dz c'} @dragover=${(e) => over(f, e, r.id, 'c')} @drop=${(e) => dropOn(f, e, r.id, 'c')}></div>
    ${sheet ? nothing : html`<div class="dz r" @dragover=${(e) => over(f, e, r.id, 'r')} @drop=${(e) => dropOn(f, e, r.id, 'r')}></div>
      <div class="dz b" @dragover=${(e) => over(f, e, r.id, 'b')} @drop=${(e) => dropOn(f, e, r.id, 'b')}></div>`}
  </div>`)}</div>`;
}

// ---- dividers ----
function nodeAt(tree, path) { let n = tree; for (const c of path) n = n?.[c]; return n; }
function divTpl(f, d) {
  const r = nodeAt(f._ide.tree, d.path)?.r ?? 0.5, row = d.dir === 'row';
  const style = row ? `left:calc(${pct(d.x)} - 3px); top:${pct(d.y)}; width:7px; height:${pct(d.h)}`
    : `left:${pct(d.x)}; top:calc(${pct(d.y)} - 3px); width:${pct(d.w)}; height:7px`;
  return html`<div class=${`vd ${d.dir}`} style=${style} role="separator" tabindex="0" aria-orientation=${row ? 'vertical' : 'horizontal'}
    aria-label="resize the panes" aria-valuemin="10" aria-valuemax="90" aria-valuenow=${Math.round(r * 100)}
    title="drag to resize · double-click: half and half"
    @pointerdown=${(e) => divStart(f, e, d)} @keydown=${(e) => divKey(f, e, d, r)}
    @dblclick=${() => set(f, M.setRatio(f._ide, d.path, 0.5))}></div>`;
}
// The drag starts once the pointer moves (a shield up between a click's
// press and release would take the release, and the double-click would
// never reach the divider); until then the divider holds the pointer.
function divStart(f, e, d) {
  if (e.button !== 0) return;
  e.preventDefault();
  const bar = e.currentTarget, area = bar.parentElement.getBoundingClientRect(), x0 = e.clientX, y0 = e.clientY;
  const r0 = nodeAt(f._ide.tree, d.path)?.r ?? 0.5;
  const to = (ev) => {
    const v = d.dir === 'row' ? ((ev.clientX - area.left) / area.width - d.box.x) / d.box.w : ((ev.clientY - area.top) / area.height - d.box.y) / d.box.h;
    set(f, M.setRatio(f._ide, d.path, v));
  };
  try { bar.setPointerCapture(e.pointerId); } catch { /* not an active pointer */ }
  const done = () => { bar.removeEventListener('pointermove', move); bar.removeEventListener('pointerup', done); bar.removeEventListener('pointercancel', done); };
  const move = (ev) => {
    if (Math.abs(ev.clientX - x0) < 3 && Math.abs(ev.clientY - y0) < 3) return;
    done(); to(ev);
    dragPointer({ ev, cursor: d.dir === 'row' ? 'col-resize' : 'row-resize', onMove: to, onCancel: () => set(f, M.setRatio(f._ide, d.path, r0)) });
  };
  bar.addEventListener('pointermove', move);
  bar.addEventListener('pointerup', done);
  bar.addEventListener('pointercancel', done);
}
// ←/→ (↑/↓ for a horizontal one) move it 2 % (10 % with Shift), Home and End to either end
function divKey(f, e, d, r) {
  const step = e.shiftKey ? 0.1 : 0.02;
  const k = d.dir === 'row' ? { ArrowLeft: -step, ArrowRight: step } : { ArrowUp: -step, ArrowDown: step };
  let v;
  if (k[e.key] !== undefined) v = r + k[e.key];
  else if (e.key === 'Home') v = 0;
  else if (e.key === 'End') v = 1;
  else return;
  e.preventDefault();
  set(f, M.setRatio(f._ide, d.path, v));
}

// ---- the dock ----
function dockStart(f, e) {
  if (e.button !== 0) return;
  e.preventDefault();
  const y0 = e.clientY, h0 = f._ide.dock.h, max = Math.max(120, (e.currentTarget.parentElement.getBoundingClientRect().height || 600) - 120);
  dragPointer({ ev: e, cursor: 'row-resize', onMove: (ev) => set(f, M.setDock(f._ide, true, Math.min(max, h0 - (ev.clientY - y0)))),
    onCancel: () => set(f, M.setDock(f._ide, true, h0)) });
}
function dockTpl(f) {
  if (!f._dockMounted && !f._ide.dock.open) return nothing;
  f._dockMounted = true;
  const open = f._ide.dock.open;
  return html`<div class="hdiv" style=${open ? '' : 'display:none'} role="separator" aria-orientation="horizontal" aria-label="resize the logs"
      title="drag to resize" @pointerdown=${(e) => dockStart(f, e)}></div>
    <div class="dock" style=${open ? `height:${f._ide.dock.h}px` : 'display:none'}>
      <div class="dhead"><bx-icon name="list"></bx-icon><span>Logs</span><span class="sub">the backend's output, read-only</span><span class="grow"></span>
        <button class="ib" title="hide the logs" aria-label="hide the logs" @click=${() => dock(f, false)}><bx-icon name="chevron-down"></bx-icon></button></div>
      <bx-logs component=${f.src}></bx-logs>
    </div>`;
}

// ---- the tool strip and the side panel ----
const SIDE_TITLE = { files: 'Files', changes: 'Changes', prs: 'Change proposals' };
function toolsTpl(f) {
  const ide = f._ide, focusA = ide.panes[ide.focus]?.active;
  const t = (id, label, icon, title, on, badge) => html`<button class=${`tool${on ? ' on' : ''}`} title=${title}
      aria-label=${badge ? `${label} (${badge})` : label} aria-pressed=${on ? 'true' : 'false'} @click=${() => tool(f, id)}>
    <bx-icon name=${icon}></bx-icon><span class="tl">${label}</span>${badge ? html`<span class="badge">${badge}</span>` : nothing}</button>`;
  return html`<nav class="tools" aria-label="tools">
    ${t('files', 'Files', 'folder', "Files — the tile's code; a file opens in the focused pane", ide.side === 'files')}
    ${t('changes', 'Changes', 'diff', 'Changes — not yet committed, and the history', ide.side === 'changes')}
    ${t('prs', 'PRs', 'branch', "Change proposals — patches other tiles' agents suggested for this one", ide.side === 'prs', f._prCount || 0)}
    ${hasLayout(f) ? t('deploy', 'Deploy', 'deploy', "Deployments — this tile's", focusA === 'deploy') : nothing}
    <span class="grow"></span>
    ${t('logs', 'Logs', 'list', "Logs — the backend's output, along the bottom", ide.dock.open)}
  </nav>`;
}
function sideTpl(f) {
  const s = f._ide.side;
  if (!s) return nothing;
  const onOpen = (e) => {
    e.stopPropagation();
    const ref = e.detail?.ref;
    if (typeof ref !== 'string' || !/^(?:[fwc]:.|pr:\d)/.test(ref)) return;
    openRef(f, ref);
    if (f._narrow) sideTool(f, null, true);
  };
  return html`<aside class=${`side${f._narrow ? ' over' : ''}`} aria-label=${SIDE_TITLE[s]} @bx-open-tab=${onOpen}
      @bx-pr-count=${(e) => { f._prCount = e.detail?.n | 0; }}>
    <div class="shead"><span>${SIDE_TITLE[s]}</span><span class="grow"></span>
      <button class="ib" title="hide the panel" aria-label="hide the panel" @click=${() => sideTool(f, null, true)}><bx-icon name="chevron-left"></bx-icon></button></div>
    <div class="sbody">${s === 'files' ? html`<bx-code-tree component=${f.src}></bx-code-tree>`
      : s === 'changes' ? html`<bx-code-changes component=${f.src}></bx-code-changes>`
      : html`<bx-pr-list target=${f.src}></bx-pr-list>`}</div>
  </aside>`;
}

// revealTabs(f): after a render, each pane's strip scrolls its active tab
// into view — only when that pane's active tab changed (or the pane is new),
// so a strip the person scrolled by hand stays where they put it: a press on
// a tab scrolled into view must land on it (the window re-renders on every
// press, as it comes to the front).
export function revealTabs(f) {
  const shown = (f._ideRevealed ??= new Map());
  for (const pane of f.renderRoot?.querySelectorAll('.pane[data-pane]') ?? []) {
    const id = pane.dataset.pane, strip = pane.querySelector('.ptabs .strip'), on = strip?.querySelector('.ptab.on');
    if (!on || shown.get(id) === on.dataset.ref) continue;
    shown.set(id, on.dataset.ref);
    const s = strip.getBoundingClientRect(), t = on.getBoundingClientRect();
    if (t.left < s.left) strip.scrollLeft -= s.left - t.left;
    else if (t.right > s.right) strip.scrollLeft += t.right - s.right;
  }
}

const scrollStrip = (e) => {
  const s = e.currentTarget;
  if (s.scrollWidth <= s.clientWidth + 1 || Math.abs(e.deltaX) >= Math.abs(e.deltaY)) return;
  s.scrollLeft += e.deltaY;
  e.preventDefault();
};

// ideBody(f): everything under the title bar.
export function ideBody(f) {
  const ide = f._ide;
  for (const ref of M.allTabs(ide)) { const m = modOf(ref); if (m) need(f, m); }
  if (ide.side) need(f, ide.side === 'prs' ? 'prs' : 'tree');
  let { panes, dividers } = M.rects(ide.tree);
  // the phone sheet has no splits: a layout split on a wider screen shows its
  // focused pane alone, the whole width (the layout itself is kept)
  if (SHEET.matches && panes.length > 1) { panes = [{ id: ide.focus, x: 0, y: 0, w: 1, h: 1 }]; dividers = []; }
  const placed = new Map();
  for (const r of panes) { const a = ide.panes[r.id]?.active; if (a && M.isSession(a)) placed.set(a, r); }
  const ordered = [...panes].sort((a, b) => Number(a.id.slice(1)) - Number(b.id.slice(1))); // a stable DOM order: a pane's view never moves
  return html`<div class=${`ide${f._narrow ? ' narrow' : ''}`}>
      ${toolsTpl(f)}${sideTpl(f)}
      <div class="center">
        <div class="panes">
          ${repeat(ordered, (r) => r.id, (r) => paneTpl(f, r, panes.length))}
          ${stage(f, placed)}
          ${dividers.map((d) => divTpl(f, d))}
          ${f._ideDrag ? dropLayer(f, panes) : nothing}
        </div>
        ${dockTpl(f)}
      </div>
    </div>
    ${statusBar(f)}`;
}

// the test surface's `ide` (frame-testapi.js)
export function ideTestApi(f) {
  return {
    get panes() {
      const { panes } = M.rects(f._ide.tree);
      return panes.map((r) => ({ id: r.id, tabs: [...f._ide.panes[r.id].tabs], active: f._ide.panes[r.id].active, focus: f._ide.focus === r.id, rect: { x: r.x, y: r.y, w: r.w, h: r.h } }));
    },
    get focus() { return f._ide.focus; },
    focusPane: (id) => focusPane(f, id),
    get side() { return f._ide.side; },
    get dock() { return { ...f._ide.dock }; },
    get tree() { return JSON.parse(JSON.stringify(f._ide.tree)); },
    openTool: (id) => tool(f, id),
    openFile: (path) => openRef(f, `f:${path}`),
    open: (ref, pane) => openRef(f, ref, pane),
    close: (ref) => closeRef(f, ref),
    split: (dir = 'row', pane = f._ide.focus) => splitPane(f, pane, dir),
    closePane: (id) => closePane(f, id),
    move: (ref, pane, zone = 'c') => dropTab(f, ref, pane, zone),
    setRatio: (path, r) => set(f, M.setRatio(f._ide, path, r)),
    setDock: (open, h) => { if (open) f._dockMounted = true; set(f, M.setDock(f._ide, open, h)); },
    // the tab ref of session tab i (the frame's tab list)
    sessionRef: (i = f._active) => (f._sessions[i] ? refOf(f._sessions[i]) : null),
    get status() {
      const el = f.renderRoot?.querySelector('.status');
      return { folded: !!el?.querySelector('button.fold'), pickers: [...(el?.querySelectorAll('select.scope, button.vm') ?? [])].map((x) => x.className) };
    },
  };
}

// The body's styles (adopted by bx-frame).
export const ideCss = css`
  .ide { display: flex; flex: 1; min-height: 0; position: relative; background: var(--bx-panel, #1F2028); }
  .ide .grow { flex: 1; min-width: 0; }
  /* the tool strip: a glyph and one word, down the left */
  .tools {
    display: flex; flex-direction: column; gap: 2px; flex: none; box-sizing: border-box; width: 56px; padding: 6px 4px;
    background: var(--bx-panel-2, #262730); border-right: 1px solid var(--bx-border, #33353F);
  }
  .tools .grow { flex: 1; }
  .tool {
    position: relative; display: flex; flex-direction: column; align-items: center; gap: 2px; padding: 6px 0 5px;
    border: 0; border-radius: var(--bx-radius, 2px); background: transparent; color: var(--bx-muted, #A3A6B6); cursor: pointer;
  }
  .tool .tl { font: var(--bx-font-micro, 600 11px/14px system-ui, sans-serif); }
  .tool:hover { color: var(--bx-text, #E9EAF0); background: var(--bx-control-hover, #33353F); }
  .tool.on { color: var(--bx-selection-text, #E9EAF0); background: var(--bx-selection, #262C5C); box-shadow: inset 2px 0 0 var(--bx-accent, #8C9BFF); }
  .tool .badge {
    position: absolute; top: 1px; right: 4px; min-width: 14px; box-sizing: border-box; padding: 0 3px;
    font: var(--bx-font-micro, 600 11px/14px system-ui, sans-serif); text-align: center;
    color: var(--bx-accent-ink, #0B0C12); background: var(--bx-accent, #8C9BFF); border-radius: var(--bx-radius, 2px);
  }
  .ide.narrow .tools { width: 38px; }
  .ide.narrow .tool .tl { display: none; }
  .ide.narrow .tool .badge { right: 1px; }
  /* the side panel: a tool's list beside the panes (over them in a narrow window) */
  .side { display: flex; flex-direction: column; flex: none; box-sizing: border-box; width: 248px; min-height: 0;
    background: var(--bx-panel, #1F2028); border-right: 1px solid var(--bx-border, #33353F); }
  .side.over { position: absolute; left: 38px; top: 0; bottom: 0; z-index: 30; box-shadow: var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6)); }
  .shead, .dhead {
    display: flex; align-items: center; gap: 6px; flex: none; box-sizing: border-box; height: var(--bx-titlebar-h, 28px); padding: 0 0 0 10px;
    border-bottom: 1px solid var(--bx-border, #33353F); color: var(--bx-muted, #A3A6B6);
    font: var(--bx-font-micro, 600 11px/14px system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase;
  }
  .dhead .sub { text-transform: none; letter-spacing: 0; font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); }
  .sbody { flex: 1; min-height: 0; overflow: auto; }
  .sbody > * { display: block; min-height: 100%; }
  .center { display: flex; flex-direction: column; flex: 1; min-width: 0; min-height: 0; }
  /* the panes: their gaps are the area's colour */
  .panes { position: relative; flex: 1; min-height: 0; background: var(--bx-border, #33353F); }
  .pane { position: absolute; display: flex; flex-direction: column; box-sizing: border-box; min-width: 0; background: var(--bx-panel, #1F2028); }
  .ptabs { display: flex; align-items: stretch; flex: none; box-sizing: border-box; height: ${TAB_H}px; min-width: 0;
    background: var(--bx-panel-2, #262730); border-bottom: 1px solid var(--bx-border, #33353F); }
  .ptabs .strip { display: flex; align-items: stretch; min-width: 0; flex: 0 1 auto; overflow-x: auto; overflow-y: hidden; scrollbar-width: none; }
  .ptabs .strip::-webkit-scrollbar { display: none; }
  .ptab {
    display: inline-flex; align-items: center; gap: 6px; flex: none; box-sizing: border-box; max-width: 200px; padding: 0 2px 0 10px;
    border-right: 1px solid var(--bx-border, #33353F); color: var(--bx-muted, #A3A6B6); cursor: pointer; user-select: none;
  }
  .ptab .lbl { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .ptab .note { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); }
  .ptab:hover { color: var(--bx-text, #E9EAF0); }
  .ptab.on { color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028); box-shadow: inset 0 2px 0 var(--bx-border-strong, #666A7E); }
  .pane.focus .ptab.on { box-shadow: inset 0 2px 0 var(--bx-accent, #8C9BFF); }
  .ptab.ended { opacity: 0.6; }
  .ptab.ended .lbl { text-decoration: line-through; }
  .ptab .tabx { display: inline-flex; align-items: center; justify-content: center; width: 20px; height: 20px; padding: 0; border: 0;
    border-radius: var(--bx-radius, 2px); background: transparent; color: inherit; cursor: pointer; opacity: 0; }
  .ptab:hover .tabx, .ptab.on .tabx, .ptab .tabx:focus-visible { opacity: 0.7; }
  .ptab .tabx:hover { opacity: 1; color: var(--bx-close-hover-ink, #0B0C12); background: var(--bx-close-hover, #FF7A7A); }
  .ide button.ib { display: inline-flex; align-items: center; justify-content: center; flex: none; box-sizing: border-box;
    width: 28px; height: 28px; padding: 0; border: 0; border-radius: 0; background: transparent; color: var(--bx-muted, #A3A6B6); cursor: pointer; }
  .ide button.ib:hover { color: var(--bx-text, #E9EAF0); background: var(--bx-control-hover, #33353F); }
  .ide button.ib.down bx-icon { transform: rotate(90deg); }
  .ide button:focus-visible, .ptab:focus-visible { outline-offset: calc(-1 * var(--bx-focus-width, 3px)); box-shadow: none; }
  .pbody { position: relative; display: flex; flex-direction: column; flex: 1; min-height: 0; overflow: hidden; }
  .pbody > * { flex: 1; min-height: 0; }
  /* the stage: the sessions, over their panes' bodies */
  .stage { position: absolute; inset: 0; pointer-events: none; }
  .stage > * { position: absolute; box-sizing: border-box; pointer-events: auto; background: var(--bx-term-bg, #0B0C12); }
  /* the dividers: a wider grab zone than the pixel they sit on */
  .vd { position: absolute; z-index: 5; touch-action: none; }
  .vd.row { cursor: col-resize; }
  .vd.col { cursor: row-resize; }
  .vd:hover { background: var(--bx-border-strong, #666A7E); }
  .vd:focus-visible { background: var(--bx-focus, #3DD6F5); outline: none; box-shadow: none; }
  /* a tab being dragged: where it lands */
  .dzl { position: absolute; inset: 0; z-index: 20; }
  .dzp { position: absolute; }
  .dz { position: absolute; }
  .dz.c { left: 0; top: 0; right: 28%; bottom: 28%; }
  .dz.all { inset: 0; }
  .dz.r { right: 0; top: 0; bottom: 0; width: 28%; }
  .dz.b { left: 0; right: 28%; bottom: 0; height: 28%; }
  .dzhot { position: absolute; pointer-events: none; background: var(--bx-selection, #262C5C); opacity: 0.6;
    outline: 2px solid var(--bx-accent, #8C9BFF); outline-offset: -2px; }
  .dzhot.c { inset: 0; }
  .dzhot.r { left: 50%; top: 0; right: 0; bottom: 0; }
  .dzhot.b { left: 0; right: 0; top: 50%; bottom: 0; }
  /* the logs dock */
  .hdiv { flex: none; height: 5px; cursor: row-resize; touch-action: none; background: var(--bx-border, #33353F); }
  .hdiv:hover { background: var(--bx-border-strong, #666A7E); }
  .dock { display: flex; flex-direction: column; flex: none; min-height: 0; background: var(--bx-panel, #1F2028); }
  .dock bx-logs { flex: 1; min-height: 0; }
`;
