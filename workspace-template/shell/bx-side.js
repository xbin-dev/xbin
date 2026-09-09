/**
 * <bx-side> — the shell's sidebar: the owner-sectioned tree (D24/D55) of
 * personal folders, each section's shared curated folders, the readable
 * tiles and the org screens, the filter box and owner filter, the
 * show-hidden toggle (D42), the organisations button, the admin-only
 * system-status footer and the xbind build line. The host element IS the
 * aside: the shell sizes it, marks it `drawer`/`open` on phones, and keeps
 * the collapsed stub and the resize handle beside it.
 *
 * It renders from one `state` view the shell assembles (_sideState) and
 * acts through one `actions` object (_sideActions): every mutation —
 * filing, folders, drafts, screens, opening a tile or a menu — is the
 * shell's, so the layout persistence and the draft flows have one owner.
 * Local state is what only the sidebar cares about: the filter text and
 * the drag-hover highlights.
 */
import { LitElement, html, nothing } from 'lit';
import { pathHas } from '/vendor/bx-kit.js';
import { RUNTIME_COLOR, LongPress, prBadge, isScreenItem, isOrgScreenItem, screenIdOf, scopeOf, ownerKeyOf, worstStatus } from './shell-kit.js';
import { offloaded, hidden } from './menus.js';
import { ago } from './rev-draft.js';
import { sideCss, statusCss, prbCss } from './shell-css.js';

export class BxSide extends LitElement {
  static properties = {
    state: { attribute: false },   // the shell's view: see _sideState in bx-shell.js
    actions: { attribute: false }, // the shell's handlers: see _sideActions
    _q: { state: true },           // the filter box
    _dropBefore: { state: true },  // row hovered as a drop target
    _dropFolder: { state: true },  // folder hovered as a drop target
  };
  static styles = [sideCss, statusCss, prbCss];

  constructor() {
    super();
    this.state = {}; this.actions = {};
    this._q = ''; this._dropBefore = null; this._dropFolder = null;
    this._press = new LongPress();
  }
  get _s() { return this.state ?? {}; }
  get _a() { return this.actions ?? {}; }

  // ---- the tree's data ----
  // Owner-based sections (D24): "mine" (tiles you own), one per org, and
  // "workspace" for the rest — the directory tree lives WITHIN each section.
  // Collapses to a flat tree when only one section exists.
  _ownerSections() {
    const s = this._s;
    const filed = new Set((s.side?.folders ?? []).flatMap((f) => f.items));
    const secs = new Map(); // key → {key, label, comps}
    const sec = (key, label) => {
      if (!secs.has(key)) secs.set(key, { key, label, comps: [] });
      return secs.get(key);
    };
    if (s.myId) sec('mine', 'mine');
    for (const o of (s.who?.orgs ?? [])) sec('org:' + o.id, o.id);
    for (const c of (s.components ?? [])) {
      if (c.path === 'root') continue; // framing root inside root recurses
      if (c.template) continue; // blueprints aren't openable tiles (instantiate via Tile Manager)
      if (offloaded(c)) continue; // archived — restore from the admin console
      if (hidden(c) && !s.showHidden) continue; // D42: behind the show-hidden toggle
      if (filed.has(c.path)) continue; // shown under its personal folder instead
      const key = ownerKeyOf(c, s.myId);
      sec(key, key === 'mine' ? 'mine' : key === 'workspace' ? 'workspace' : key.slice(4)).comps.push(c);
    }
    // An org section also lists the org's screens, and a section whose shared
    // folders are being curated stays put even while empty.
    const hasScreens = (key) => (s.orgScreens ?? []).some((o) => 'org:' + o.org === key);
    return [...secs.values()].filter((x) => x.comps.length || hasScreens(x.key) || !!s.folderDrafts?.[scopeOf(x.key)]);
  }
  // Flat-tree labels (D55): the basename, or the full path when two tiles in
  // the section share one. The tooltip always carries the full path.
  _labelsFor(comps) {
    const base = (p) => p.slice(p.lastIndexOf('/') + 1);
    const n = {};
    for (const c of comps) n[base(c.path)] = (n[base(c.path)] ?? 0) + 1;
    return new Map(comps.map((c) => [c.path, n[base(c.path)] > 1 ? c.path : base(c.path)]));
  }
  _ctx(key) { return this._a.folderCtx?.(key ?? 'top') ?? { key: 'top', folders: [], canEdit: false, shared: false, mutate() {} }; }
  _childFolders(parentId, ctx) { return ctx.folders.filter((f) => (f.parent ?? null) === (parentId ?? null)); }
  _isFolderOpen(f, ctx) {
    if (this._q.trim()) return true; // force-open while filtering
    return ctx.shared ? (this._s.side?.sharedOpen?.[f.id] ?? true) : !!f.open;
  }
  _comp(path) { return (this._s.components ?? []).find((c) => c.path === path); }
  _isOpen(path) { return !!this._s.openPaths?.has(path); }
  _statusOf(path) { return this._s.status?.[path]; }
  _whoLabel(id) { return !id ? 'someone' : id === this._s.myId ? 'you' : id; }

  // ---- filter ----
  _sideMatch(text) { const q = this._q.trim().toLowerCase(); return !q || (text ?? '').toLowerCase().includes(q); }
  _folderHasMatch(f, ctx) {
    if (!this._q.trim()) return true;
    if (this._sideMatch(f.name)) return true;
    for (const it of f.items) {
      if (isScreenItem(it)) {
        const s = (this._s.screens ?? []).find((x) => x.id === screenIdOf(it));
        if (s && this._sideMatch(s.name)) return true;
      } else if (isOrgScreenItem(it)) {
        const s = (this._s.orgScreens ?? []).find((x) => x.id === screenIdOf(it));
        if (s && this._sideMatch(s.name)) return true;
      } else if (this._sideMatch(it)) return true;
    }
    return this._childFolders(f.id, ctx).some((c) => this._folderHasMatch(c, ctx));
  }
  _sideEmptyMsg() {
    const q = this._q.trim();
    const top = this._ctx('top');
    const sections = this._ownerSections();
    if (q) {
      const anyFolder = this._childFolders(null, top).some((f) => this._folderHasMatch(f, top));
      const anyTile = sections.some((x) => x.comps.some((c) => this._sideMatch(c.path)))
        || (this._s.orgScreens ?? []).some((o) => this._sideMatch(o.name));
      return (!anyFolder && !anyTile) ? html`<div class="empty">no matches for “${q}”</div>` : nothing;
    }
    return (sections.length === 0 && (this._s.side?.folders ?? []).length === 0)
      ? html`<div class="empty">no components yet<br>· mkdir one ·</div>` : nothing;
  }

  // ---- gestures ----
  // A right-click on a row opens the tile menu; inputs, links and buttons
  // keep the native menu. Stopped either way: the shell's <main> handler
  // must not turn a sidebar right-click into a canvas menu.
  _onContextMenu(e) {
    e.stopPropagation();
    if (this._s.mobile && this._s.menuOpen) { e.preventDefault(); return; }
    if (pathHas(e, 'input, textarea, select, a, button, .prb, bx-menu, bx-dialog')) return;
    const row = e.target.closest('.item[data-path]');
    if (row) { e.preventDefault(); this._a.tileMenu?.({ clientX: e.clientX, clientY: e.clientY }, row.dataset.path); }
  }
  // Drop onto a row: inside a folder = reorder there; on a root row = unfile
  // from that row's context (a shared scope only while curating it).
  _dropOnItem(e, targetPath, folderId, ctxKey) {
    e.preventDefault(); e.stopPropagation();
    this._dropBefore = null;
    const path = e.dataTransfer.getData('application/bx-comp');
    if (!path || path === targetPath) return;
    const ctx = this._ctx(ctxKey ?? 'top');
    if (!ctx.canEdit) return;
    if (folderId) this._a.moveInto?.(folderId, path, targetPath, ctx);
    else this._a.fileInto?.('', path, ctx);
  }
  // Dropped on empty sidebar space: a folder → top level of its context, a
  // component → out of personal folders.
  _dropOnSpace(e) {
    const fid = e.dataTransfer.getData('application/bx-folder');
    const fctx = e.dataTransfer.getData('application/bx-folder-ctx') || 'top';
    if (fid) { this._a.unnestFolder?.(fid, this._ctx(fctx)); return; }
    const path = e.dataTransfer.getData('application/bx-comp') || e.dataTransfer.getData('text/plain');
    if (path) this._a.fileInto?.('', path, this._ctx('top'));
  }

  // ---- templates ----
  _bar(label, frac, detail) {
    const pct = frac == null ? null : Math.max(0, Math.min(1, frac));
    return html`
      <div class="sysrow" title=${detail ?? ''}>
        <span class="l">${label}</span>
        <span class="v">${detail ?? (pct == null ? '—' : Math.round(pct * 100) + '%')}</span>
      </div>
      <div class="sysbar"><div class="fill" style="width:${(pct ?? 0) * 100}%"></div></div>`;
  }
  // System status footer (admin-only; the shell polls /status every 5s).
  _statusFooter() {
    const s = this._s.sys;
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
  // xbind build commit (bottom of the sidebar; admin-only).
  _buildFoot() {
    const v = this._s.sys?.version;
    if (!v) return nothing;
    const dirty = v.endsWith('-dirty');
    return html`
      <div class="buildfoot" title="the running xbind daemon's build commit">
        <span class="glyph">⬡</span>
        <span class="label">xbind</span>
        <span class="ver ${dirty ? 'dirty' : ''}">${v}</span>
      </div>`;
  }

  // The owner-sectioned tree (D55): every section — mine / each org /
  // workspace — is ONE tree: its shared curated folders (org admins or
  // ws-admins curate; everyone else reads), then every remaining readable
  // tile flat at the root, then (orgs) the org's screens. No directory
  // headers. One section (solo workspace) renders without the header; more
  // get collapsible owner headers plus the owner filter. A live search forces
  // everything open.
  _sectionsTemplate() {
    const sections = this._ownerSections();
    const filter = this._s.side?.ownerFilter ?? '';
    const single = sections.length <= 1;
    return sections
      .filter((x) => single || !filter || x.key === filter)
      .map((x) => this._sectionTemplate(x, single));
  }

  _sectionTemplate(x, single) {
    const scope = scopeOf(x.key);
    const ctx = scope ? this._ctx(scope) : null;
    const editing = !!ctx?.draft;
    const labels = this._labelsFor(x.comps);
    const section = { key: x.key, labels, comps: x.comps };
    const filed = new Set(ctx ? ctx.folders.flatMap((f) => f.items ?? []) : []);
    const root = x.comps
      .filter((c) => !filed.has(c.path) && (this._sideMatch(c.path) || this._sideMatch(labels.get(c.path))))
      .sort((a, b) => labels.get(a.path).localeCompare(labels.get(b.path)));
    const screens = x.key.startsWith('org:')
      ? (this._s.orgScreens ?? []).filter((o) => 'org:' + o.org === x.key && this._sideMatch(o.name)) : [];
    const depth = single ? 0 : 1;
    const folders = ctx ? this._childFolders(null, ctx).map((f) => this._folderTemplate(f, depth, ctx, section)) : [];
    const body = html`
      ${editing ? this._sectionEditBar(ctx) : nothing}
      ${folders}
      ${root.map((c) => this._itemTemplate(c, null, labels.get(c.path), depth, scope))}
      ${screens.map((o) => this._orgScreenItemTemplate(o.id, depth))}`;
    if (single) return body;
    const collapsed = !!this._s.side?.ownerCollapsed?.[x.key] && !this._q.trim() && !editing;
    const label = x.key === 'mine' ? 'mine' : x.key === 'workspace' ? 'workspace' : x.label;
    const n = x.comps.length + screens.length;
    return html`
      <div class="group owner ${editing ? 'editing' : ''}" title="tiles owned by ${x.key === 'mine' ? 'you' : x.key === 'workspace' ? 'the workspace' : 'org ' + x.label} — click to fold"
           @click=${() => this._a.toggleOwnerSec?.(x.key)}
           @dragover=${(e) => { if (ctx?.canEdit && e.dataTransfer.types.includes('application/bx-comp')) e.preventDefault(); }}
           @drop=${(e) => { // dropped on the header while curating → unfile from this scope
             const path = e.dataTransfer.getData('application/bx-comp');
             if (ctx?.canEdit && path) { e.preventDefault(); e.stopPropagation(); this._a.fileInto?.('', path, ctx); } }}>
        <span class="tri">${collapsed ? '▸' : '▾'}</span>
        ${x.key === 'mine' ? '👤 ' : x.key !== 'workspace' ? '⚑ ' : ''}${label}
        <span class="n">${n}</span>
        ${ctx?.curator && !editing ? html`<button class="pen" title="curate this section's shared folders (everyone here sees them)"
          @click=${(e) => { e.stopPropagation(); this._a.enterFolderEdit?.(scope); }}>✎</button>` : nothing}
      </div>
      ${collapsed ? nothing : body}`;
  }

  // While curating a shared folder set: add folders, then publish or discard.
  _sectionEditBar(ctx) {
    const d = ctx.draft, set = ctx.set;
    const newer = set && (set.rev ?? 0) > d.baseRev;
    return html`
      <div class="secbar">
        <div class="l">✎ editing shared folders${d.dirty ? ' · unsaved' : ''}</div>
        ${newer ? html`<div class="newer">⚠ rev ${set.rev} saved by ${this._whoLabel(set.updatedBy)} ${ago(set.updatedAt)} —
          <a @click=${() => { if (!d.dirty || confirm('Drop your draft and take the newer folders?')) this._a.dropFolderDraft?.(ctx.key); }}>reload theirs</a></div>` : nothing}
        <div class="r">
          <button class="mini" title="new shared folder" @click=${() => this._a.addFolder?.(ctx)}>＋ folder</button>
          <span style="flex:1"></span>
          <button class="mini" @click=${() => this._a.discardFolderDraft?.(ctx.key)}>discard</button>
          <button class="mini go" ?disabled=${!d.dirty} title="publish these folders to everyone in this section"
            @click=${() => this._a.saveFolderDraft?.(ctx.key)}>Save for everyone</button>
        </div>
      </div>`;
  }

  // One sidebar row for a component — used by folders and section roots alike.
  // ctxKey names the folder context a drop on this row acts in.
  _itemTemplate(c, folderId = null, label = null, depth = 0, ctxKey = null) {
    if (!c) return nothing;
    const st = this._statusOf(c.path);
    const title = st ? `${c.path} — ${st.level}${st.message ? ': ' + st.message : ''}`
      : (c.manifestError ? `${c.path} — manifest error: ${c.manifestError}` : c.path);
    return html`
      <div class="item ${this._isOpen(c.path) ? 'open' : ''} ${this._dropBefore === c.path ? 'dropinto' : ''} ${st ? 'st-' + st.level : ''} ${hidden(c) ? 'hid' : ''}"
           data-path=${c.path}
           draggable=${this._s.mobile ? 'false' : 'true'}
           style=${depth ? `padding-left:${12 + depth * 12}px` : nothing}
           title=${title}
           @pointerdown=${(e) => this._press.start(e, () => this._a.tileMenu?.(null, c.path), this._s.mobile)}
           @pointermove=${(e) => this._press.move(e)}
           @pointerup=${() => this._press.cancel()} @pointercancel=${() => this._press.cancel()} @pointerleave=${() => this._press.cancel()}
           @dragstart=${(e) => { e.dataTransfer.setData('application/bx-comp', c.path);
             e.dataTransfer.setData('text/plain', c.path); e.dataTransfer.effectAllowed = 'move'; }}
           @dragover=${(e) => { if (e.dataTransfer.types.includes('application/bx-comp')) {
             e.preventDefault(); e.stopPropagation(); this._dropBefore = c.path; } }}
           @dragleave=${() => { if (this._dropBefore === c.path) this._dropBefore = null; }}
           @drop=${(e) => this._dropOnItem(e, c.path, folderId, ctxKey)}
           @click=${() => this._a.toggle?.(c.path)}>
        <span class="c" style="background:${RUNTIME_COLOR[c.runtime ?? ''] ?? RUNTIME_COLOR['']}"></span>
        <span>${label ?? c.path.slice(c.path.lastIndexOf('/') + 1)}</span>
        ${prBadge(this._s.prs?.[c.path])}
        ${st ? html`<span class="stdot"></span>` : nothing}
        ${c.manifestError ? html`<span class="err">⚠</span>` : nothing}
        ${hidden(c) ? html`<span class="hidb">hidden</span>` : nothing}
        <span class="rt">${c.runtime || ''}</span>
        <button class="more" title="tile menu" @pointerdown=${(e) => e.stopPropagation()}
                @click=${(e) => { e.stopPropagation(); this._a.tileMenu?.(e, c.path, e.currentTarget.getBoundingClientRect()); }}>⋯</button>
      </div>`;
  }

  // A sidebar folder — may nest child folders and hold components; a personal
  // (top) folder also holds parked tabs and org-screen refs. Recursive;
  // `depth` drives indentation; `ctx` says whose folder list this is and
  // whether it may change; `section` (shared trees) bounds the visible items
  // to that owner section's readable tiles.
  _folderTemplate(f, depth = 0, ctx = this._ctx('top'), section = null) {
    if (!this._folderHasMatch(f, ctx) && !ctx.canEdit) return nothing;
    const open = this._isFolderOpen(f, ctx);
    const inSection = section ? new Set(section.comps.map((c) => c.path)) : null;
    const items = (f.items ?? []).filter((it) => {
      if (isScreenItem(it)) {
        if (ctx.shared) return false;
        const s = (this._s.screens ?? []).find((x) => x.id === screenIdOf(it));
        return s && this._sideMatch(s.name);
      }
      if (isOrgScreenItem(it)) {
        if (ctx.shared) return false;
        const s = (this._s.orgScreens ?? []).find((x) => x.id === screenIdOf(it));
        return s && this._sideMatch(s.name);
      }
      if (inSection && !inSection.has(it)) return false; // unreadable, other owner, or filed personally
      const c = this._comp(it);
      return c && !offloaded(c) && (this._sideMatch(it) || this._sideMatch(section?.labels.get(it)));
    });
    const children = this._childFolders(f.id, ctx);
    const comps = items.filter((it) => !isScreenItem(it) && !isOrgScreenItem(it)).map((p) => this._comp(p)).filter(Boolean);
    // A shared folder with nothing this user can see stays out of their way —
    // unless its curator is editing (they must see what they just created).
    if (ctx.shared && !ctx.canEdit && !items.length && !children.length) return nothing;
    const fst = !open ? worstStatus(this._s.status, comps.map((c) => c.path)) : null;
    const ro = ctx.shared && !ctx.canEdit;
    return html`
      <div class="group folder ${this._dropFolder === f.id ? 'dropping' : ''} ${fst ? 'st-' + fst : ''} ${ro ? 'ro' : ''}" draggable=${ctx.canEdit ? 'true' : 'false'}
           style="padding-left:${8 + depth * 12}px"
           title=${ro ? 'shared folder (curated by admins) — click to fold' : 'click to fold · double-click to rename/icon · drop a tile' + (ctx.shared ? '' : ', a tab,') + ' or another folder in'}
           @click=${() => this._a.toggleFolder?.(f, ctx, open)}
           @dblclick=${() => { if (ctx.canEdit) this._a.folderDialog?.(f, ctx); }}
           @dragstart=${(e) => { if (!ctx.canEdit) { e.preventDefault(); return; }
             e.dataTransfer.setData('application/bx-folder', f.id); e.dataTransfer.setData('application/bx-folder-ctx', ctx.key);
             e.dataTransfer.effectAllowed = 'move'; e.stopPropagation(); }}
           @dragover=${(e) => { e.preventDefault(); this._dropFolder = ctx.canEdit ? f.id : null; }}
           @dragleave=${() => { if (this._dropFolder === f.id) this._dropFolder = null; }}
           @drop=${(e) => { this._dropFolder = null; this._a.dropOnFolder?.(e, f, ctx); }}>
        <span class="tri">${open ? '▾' : '▸'}</span>
        <span class="ficon">${f.icon || '📁'}</span>
        <span class="fname">${f.name}</span> <span class="n">${items.length + children.length}</span>
        ${fst ? html`<span class="stdot"></span>` : nothing}
        ${ctx.canEdit ? html`<button class="fx" title="delete folder (contents return to the section root / tabs)"
                @click=${(e) => { e.stopPropagation(); this._a.deleteFolder?.(f, ctx); }}>✕</button>` : nothing}
      </div>
      ${open ? html`
        ${children.map((c) => this._folderTemplate(c, depth + 1, ctx, section))}
        ${items.map((it) => isScreenItem(it)
          ? this._screenItemTemplate(screenIdOf(it), depth + 1)
          : isOrgScreenItem(it)
            ? this._orgScreenItemTemplate(screenIdOf(it), depth + 1, f.id)
            : this._itemTemplate(this._comp(it), f.id, section?.labels.get(it) ?? null, depth + 1, ctx.key))}`
        : nothing}`;
  }

  // An org screen in the tree: under its org section (always, so a hidden tab
  // is never stranded) or as a personal folder reference. Click re-opens it.
  _orgScreenItemTemplate(id, depth = 0, folderId = null) {
    const s = (this._s.orgScreens ?? []).find((x) => x.id === id);
    if (!s) return nothing; // stale ref (deleted / membership lost)
    const hiddenTab = !!this._s.hiddenOrg?.[id];
    const dirty = !!this._s.orgDrafts?.[id]?.dirty;
    return html`
      <div class="item screen org ${this._s.active === id ? 'on' : ''}" style="padding-left:${12 + depth * 12}px"
           draggable="true"
           title=${`org screen "${s.name}" (${s.org}) — click to open${hiddenTab ? ' (hidden from the tab bar)' : ''}`}
           @dragstart=${(e) => { e.dataTransfer.setData('application/bx-screen', id);
             e.dataTransfer.setData('application/bx-orgscreen', id); e.dataTransfer.effectAllowed = 'move'; e.stopPropagation(); }}
           @click=${() => this._a.openOrgScreen?.(id)}>
        <span class="sic">▦</span>
        <span class="sname">${s.name}${dirty ? ' ●' : ''}</span>
        ${folderId ? html`<span class="ob">${s.org}</span>` : nothing}
        ${hiddenTab ? html`<span class="pk">hidden</span>` : nothing}
        ${folderId ? html`<button class="xt" title="remove from this folder" @click=${(e) => { e.stopPropagation(); this._a.fileInto?.('', '#orgscreen:' + id, this._ctx('top')); }}>✕</button>` : nothing}
      </div>`;
  }

  // A parked/opened screen tab, shown in the tree. It's the live screen (by id),
  // not a snapshot: clicking re-opens it and restores its exact layout.
  _screenItemTemplate(id, depth = 0) {
    const s = (this._s.screens ?? []).find((x) => x.id === id);
    if (!s) return nothing; // stale ref (the screen was deleted)
    return html`
      <div class="item screen ${this._s.active === id ? 'on' : ''}" style="padding-left:${12 + depth * 12}px"
           title=${`screen "${s.name}" — click to open${s.parked ? ' (parked)' : ''}`}
           @click=${() => this._a.openScreen?.(id)}>
        <span class="sic">▦</span>
        <span class="sname">${s.name}</span>
        ${s.parked ? html`<span class="pk">parked</span>` : nothing}
        <button class="xt" title="remove from tree" @click=${(e) => { e.stopPropagation(); this._a.removeScreenFromTree?.(id); }}>✕</button>
      </div>`;
  }

  render() {
    const s = this._s, a = this._a;
    const top = this._ctx('top');
    const secs = this._ownerSections();
    // a solo section has no header to hold its ✎ — offer it in the top row
    const soloScope = secs.length > 1 ? null : scopeOf(secs[0]?.key ?? 'workspace');
    const soloCtx = soloScope ? this._ctx(soloScope) : null;
    return html`
      <div class="root" @contextmenu=${(e) => this._onContextMenu(e)}
           @dragover=${(e) => e.preventDefault()}
           @drop=${(e) => this._dropOnSpace(e)}>
        <div class="side-top">
          <button class="mini" title="new personal folder (view-only grouping — nothing moves on disk)"
                  @click=${() => a.addFolder?.()}>＋ folder</button>
          ${soloCtx?.curator && !soloCtx.draft ? html`<button class="mini" title="curate the shared folders everyone sees"
            @click=${() => a.enterFolderEdit?.(soloScope)}>✎ shared</button>` : nothing}
          <span style="flex:1"></span>
          <button class="mini" title="collapse sidebar" @click=${() => a.saveSide?.({ collapsed: true })}>«</button>
        </div>
        <div class="side-search">
          <input class="side-q" placeholder="filter tiles &amp; tabs…" .value=${this._q}
                 @input=${(e) => { this._q = e.target.value; }}>
          ${this._q ? html`<button class="qx" title="clear" @click=${() => { this._q = ''; }}>✕</button>` : nothing}
        </div>
        ${secs.length > 1 ? html`<div class="side-owner">
          <select title="show tiles by owner" .value=${s.side?.ownerFilter ?? ''}
                  @change=${(e) => a.saveSide?.({ ownerFilter: e.target.value })}>
            <option value="">all owners</option>
            ${secs.map((x) => html`
              <option value=${x.key} ?selected=${(s.side?.ownerFilter ?? '') === x.key}>
                ${x.key === 'mine' ? 'mine' : x.key === 'workspace' ? 'workspace' : 'org: ' + x.label}</option>`)}
          </select>
        </div>` : nothing}
        <div class="side-scroll">
          ${this._childFolders(null, top).map((f) => this._folderTemplate(f, 0, top))}
          ${this._sectionsTemplate()}
          ${s.hiddenCount ? html`<button class="hidtoggle"
              title="hidden tiles are disabled; manage via the tile ⚙ or admin console"
              @click=${() => a.toggleShowHidden?.()}>
            ${s.showHidden ? 'hide' : 'show'} hidden (${s.hiddenCount})</button>` : nothing}
          ${this._sideEmptyMsg()}
        </div>
        ${s.orgButton ? html`
          <button class="orgbtn" title="your organisations: memberships, owned tiles, sharing, approvals${s.pendingN ? ` — ${s.pendingN} pending` : ''}"
            @click=${() => a.openOrganisations?.()}>
            ⚑ organisations${s.pendingN ? html` <span class="n">${s.pendingN}</span>` : nothing}
          </button>` : nothing}
        ${this._statusFooter()}
        ${this._buildFoot()}
      </div>`;
  }
}

customElements.define('bx-side', BxSide);
