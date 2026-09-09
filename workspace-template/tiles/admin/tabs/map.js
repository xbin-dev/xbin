/**
 * <bx-admin-map> — the admin console's access map: the ownership structure
 * (workspace → orgs → members) and the resolved users×tiles matrix straight
 * from /access-matrix, with the D39 owner-transfer editor (preview → confirm).
 * One of the admin console's tab elements (admin.js routes to it); it loads
 * its own data when mounted and reports through composed events the router
 * shows in its global slots: `bx-admin-err` (message), `bx-admin-notice`
 * (message), `bx-admin-refresh` (the router should reload users/orgs),
 * `bx-admin-show-hidden` (the D42 "show hidden tiles" toggle the router
 * shares with its component lists).
 */
import { LitElement, html, nothing } from 'lit';
import { xbinApi as api, jbody } from '/vendor/bx-kit.js';
import { base, mapCss } from '../admin-css.js';
import { WithRouter } from '../shared.js';

export class BxAdminMap extends WithRouter(LitElement) {
  static properties = {
    users: { attribute: false },     // [{id, name, role, …}] from the router
    orgs: { attribute: false },      // [{id, name, members, sets, policy, ownedTiles, …}]
    wsPolicy: { attribute: false },  // workspace policy-ceiling rows
    showHidden: { attribute: false }, // D42: include hidden tiles
    _matrix: { state: true },   // /access-matrix payload
    _compState: { state: true }, // path → lifecycle state (hidden-tile filtering)
    _mapSel: { state: true },   // selected matrix cell {user, tile} → derivation panel
    _mapTileQ: { state: true }, // filter: tile path / owner substring
    _mapUserQ: { state: true }, // filter: user id/name substring
    _mapLayout: { state: true }, // 'auto' (default) | 'matrix' | 'list'
    _mapOpen: { state: true },  // set of tiles expanded in the by-tile list view
    _ownerEdit: { state: true }, // owner reassignment {tile, to, rep?, perr?} (D39)
  };
  static styles = [base, mapCss];

  connectedCallback() {
    super.connectedCallback();
    this.load();
  }
  // refresh(): the router calls it when the workspace changed under us.
  refresh() { return this.load(true); }

  async load(force = false) {
    if (this._matrix && !force) return;
    try {
      this._matrix = await api('/access-matrix');
      // Lifecycle states for the map's hidden-tile filtering (D42).
      const comps = await api('/components').catch(() => []);
      this._compState = Object.fromEntries((comps ?? []).map((c) => [c.path, c.state || 'enabled']));
    } catch (e) { this._emit('bx-admin-err', String(e.message ?? e)); }
  }

  _lvChip(level) {
    const s = { read: 'r', write: 'w', terminal: 't' }[level] ?? '·';
    return html`<span class="lv lv-${level || 'none'}" title=${level || 'no access'}>${s}</span>`;
  }

  _srcLabel(src) {
    const [kind, ...rest] = String(src).split(':');
    switch (kind) {
      case 'admin': return 'workspace admin';
      case 'owner': return 'owns the tile (D24)';
      case 'exact': return 'exact entry — authoritative (D31)';
      case 'org-admin': return `admin of owning org ${rest[0]}`;
      case 'org-member': return html`member level in owning org <span class="mono">${rest[0]}</span>`;
      case 'org-share': return html`shared to org <span class="mono">${rest[0]}</span> · <span class="mono">${rest.slice(1).join(':')}</span>`;
      case 'direct': return html`own entry <span class="mono">${rest.join(':')}</span>`;
      case 'default': return html`workspace default <span class="mono">${rest.join(':')}</span>`;
      default: return src;
    }
  }

  _structureView() {
    const users = this.users ?? [];
    const orgs = this.orgs ?? [];
    const inOrg = new Set(orgs.flatMap((o) => (o.members ?? []).map((m) => m.id)));
    const wsAdmins = users.filter((u) => u.role === 'admin').map((u) => u.id);
    const outside = users.filter((u) => u.role !== 'admin' && !inOrg.has(u.id)).map((u) => u.id);
    const person = (m) => html`<span class="pill ${m.admin ? 'crown' : ''}"
      title="level ${m.level}${m.create ? ' · may create org tiles' : ''}${m.admin ? ' · org admin' : ''}">
      ${m.admin ? '★ ' : ''}${m.id}·${(m.level || 'read')[0]}${m.create && !m.admin ? '+' : ''}</span>`;
    return html`
      <div class="snode ws">
        <div class="shead">workspace</div>
        <div>admins: ${wsAdmins.length ? wsAdmins.map((a) => html`<span class="pill crown">★ ${a}</span>`) : html`<span class="muted">root token only</span>`}
          ${this.wsPolicy?.length ? html`<span class="pill pol" title=${this.wsPolicy.map((r) => `tiles=${r.tiles}${r.deny?.length ? ` deny=${r.deny.join(',')}` : ''}${r.mayCall?.length ? ` mayCall=${r.mayCall.join(',')}` : ''}`).join('\n')}>⛔ ${this.wsPolicy.length} policy row(s)</span>` : nothing}
        </div>
        ${outside.length ? html`<div style="margin-top:3px"><span class="muted" style="font-size:11px">in no org:</span> ${outside.map((u) => html`<span class="pill">${u}</span>`)}</div>` : nothing}
      </div>
      ${orgs.map((o) => html`
        <div class="snode org">
          <div class="shead"><span class="mono">${o.id}</span>${o.name !== o.id ? html` <span class="muted">${o.name}</span>` : nothing}
            ${(o.sets ?? []).map((n) => html`<span class="pill" title="permission set">⛭ ${n}</span>`)}
            ${(o.resolvedAllow ?? []).length ? html`<span class="pill" title=${'org admins may self-approve:\n' + o.resolvedAllow.join('\n')}>✓ ${o.resolvedAllow.length} allowance(s)</span>` : nothing}
            ${o.policy?.length ? html`<span class="pill pol" title=${o.policy.map((r) => `tiles=${r.tiles}${r.deny?.length ? ` deny=${r.deny.join(',')}` : ''}${r.mayCall?.length ? ` mayCall=${r.mayCall.join(',')}` : ''}`).join('\n')}>⛔ ${o.policy.length} policy row(s)</span>` : nothing}
          </div>
          <div>${(o.members ?? []).length ? (o.members ?? []).map(person) : html`<span class="muted">no members</span>`}</div>
          ${(o.ownedTiles ?? []).length ? html`<div style="margin-top:3px">
            ${(o.ownedTiles ?? []).map((p) => html`<span class="pill mono" title="owned by ${o.id}">${p}</span>`)}</div>` : nothing}
        </div>`)}`;
  }

  _mapDetail() {
    const s = this._mapSel;
    const c = s && this._matrix?.matrix?.[s.user]?.[s.tile];
    if (!c) return nothing;
    return html`
      <div style="margin-top:8px; padding:8px 10px; border:1px solid var(--bx-border, #363c45); border-radius:6px">
        <span class="mono">${s.user}</span> on <span class="mono">${s.tile}</span> →
        ${this._lvChip(c.level)} <b>${c.level}</b>
        <table style="margin-top:5px">
          ${(c.explain ?? []).map((v, i) => html`<tr style=${i === 0 ? '' : 'opacity:.65'}>
            <td style="white-space:nowrap">${this._lvChip(v.level)} ${v.level}</td>
            <td>${this._srcLabel(v.source)}</td>
            <td class="muted" style="font-size:10.5px">${i === 0 ? '← effective (highest wins)' : 'unioned'}</td>
          </tr>`)}
        </table>
      </div>`;
  }

  // _ownerCell: the owner pill + the D39 transfer entry point — a labeled
  // button, not an icon (review feedback: ⇄ alone wasn't discoverable).
  _ownerCell(m, tile) {
    const owner = m?.owners?.[tile] ?? '';
    const icon = owner.startsWith('user:') ? '👤 ' : owner.startsWith('org:') ? '🏢 ' : '';
    return html`<span class="pill mono" title="owner (D24)">${icon}${owner || 'workspace'}</span>
      <button class="act" title="reassign this tile's owner — previews impact first (D39)"
        @click=${() => {
          this._ownerEdit = this._ownerEdit?.tile === tile ? null
            : { tile, to: owner };
        }}>transfer</button>`;
  }

  // _mapGroups: tiles grouped by OWNER (D24), filtered by the tile/owner query.
  _mapHiddenCount(m) {
    return (m.components ?? []).filter((p) => this._compState?.[p] === 'hidden').length;
  }

  _mapGroups(m, tq) {
    const groups = new Map();
    for (const tile of (m?.components ?? [])) {
      if (!this.showHidden && this._compState?.[tile] === 'hidden') continue; // D42
      const owner = m?.owners?.[tile] ?? '';
      if (tq && !tile.toLowerCase().includes(tq) &&
          !(owner || 'workspace').toLowerCase().includes(tq)) continue;
      const key = owner.startsWith('org:') ? `org ${owner.slice(4)}`
        : owner.startsWith('user:') ? `user ${owner.slice(5)}`
        : (tile.includes('/') ? tile.split('/')[0] : 'workspace');
      if (!groups.has(key)) groups.set(key, []);
      groups.get(key).push(tile);
    }
    return [...groups.entries()].sort(([a], [b]) => a.localeCompare(b));
  }

  // The wide users×tiles matrix — the classic view for small workspaces.
  _mapMatrix(m, grouped, cols) {
    return html`
      <div style="overflow-x:auto">
        <table class="matrix">
          <tr><th style="text-align:left"></th><th style="text-align:left">owner</th>
            ${cols.map((u) => html`<th class="mono" title=${u.name}>${u.id}</th>`)}</tr>
          ${grouped.map(([grp, tiles]) => html`
            <tr><td class="mgrp" colspan=${cols.length + 2}>${grp}</td></tr>
            ${tiles.map((tile) => html`<tr>
              <td class="mono mtile">${tile}</td>
              <td class="mown">${this._ownerCell(m, tile)}</td>
              ${cols.map((u) => {
                const c = m.matrix?.[u.id]?.[tile];
                const sel = this._mapSel?.user === u.id && this._mapSel?.tile === tile;
                return html`<td class="mcell ${sel ? 'msel' : ''} ${c ? 'has' : ''}"
                  @click=${() => { this._mapSel = c ? { user: u.id, tile } : null; }}>${this._lvChip(c?.level)}</td>`;
              })}
            </tr>
            ${this._ownerEdit?.tile === tile ? html`<tr>
              <td colspan=${cols.length + 2}>${this._ownerEditor(m)}</td>
            </tr>` : nothing}`)}`)}
        </table>
      </div>`;
  }

  // The by-tile list — the many-users layout: one row per tile (owner +
  // transfer + who-has-access summary), expandable to that tile's users with
  // levels + provenance. Cells still click through to the derivation panel.
  _mapList(m, grouped, cols) {
    const open = this._mapOpen ?? new Set();
    const toggle = (tile) => {
      const next = new Set(open);
      next.has(tile) ? next.delete(tile) : next.add(tile);
      this._mapOpen = next;
    };
    return html`${grouped.map(([grp, tiles]) => html`
      <div class="mgrp" style="padding:8px 4px 2px">${grp}</div>
      ${tiles.map((tile) => {
        const withAccess = cols.filter((u) => m.matrix?.[u.id]?.[tile]);
        const isOpen = open.has(tile);
        return html`
          <div class="maprow">
            <button class="act" style="width:20px" title=${isOpen ? 'collapse' : 'who has access'}
              @click=${() => toggle(tile)}>${isOpen ? '▾' : '▸'}</button>
            <span class="mono" style="flex:1">${tile}</span>
            ${this._ownerCell(m, tile)}
            <span class="muted" style="white-space:nowrap; cursor:pointer" @click=${() => toggle(tile)}>
              ${withAccess.length} user${withAccess.length === 1 ? '' : 's'}</span>
          </div>
          ${this._ownerEdit?.tile === tile ? this._ownerEditor(m) : nothing}
          ${isOpen ? html`<table class="mapsub">
            ${withAccess.length ? withAccess.map((u) => {
              const c = m.matrix[u.id][tile];
              const sel = this._mapSel?.user === u.id && this._mapSel?.tile === tile;
              return html`<tr class=${sel ? 'msel' : ''} style="cursor:pointer"
                  @click=${() => { this._mapSel = { user: u.id, tile }; }}>
                <td class="mono">${u.id}</td>
                <td>${this._lvChip(c.level)} ${c.level}</td>
                <td class="muted">${this._srcLabel(c.explain?.[0]?.source ?? '')}</td>
              </tr>`;
            }) : html`<tr><td class="muted">no regular users reach this tile</td></tr>`}
          </table>` : nothing}`;
      })}`)}`;
  }

  render() {
    const m = this._matrix;
    const allCols = (m?.users ?? []).filter((u) => u.role !== 'admin');
    const admins = (m?.users ?? []).filter((u) => u.role === 'admin').map((u) => u.id);
    const tq = (this._mapTileQ ?? '').trim().toLowerCase();
    const uq = (this._mapUserQ ?? '').trim().toLowerCase();
    const cols = uq ? allCols.filter((u) =>
      u.id.toLowerCase().includes(uq) || (u.name ?? '').toLowerCase().includes(uq)) : allCols;
    const grouped = this._mapGroups(m, tq);
    const nTiles = grouped.reduce((n, [, ts]) => n + ts.length, 0);
    // The wide matrix stops scaling past ~10 user columns; big (or force-
    // toggled) workspaces get the by-tile list instead.
    const layout = this._mapLayout === 'matrix' || this._mapLayout === 'list'
      ? this._mapLayout : (cols.length > 10 ? 'list' : 'matrix');
    return html`
      <h4>structure</h4>
      <p class="muted" style="font-size:11px; max-width:64ch; margin-top:2px">
        Who is where: ★ = admin of that box. Level pills on teams are their
        grants (union — the highest matching source wins per tile); ⛔ marks a
        policy ceiling on what those tiles may be granted (hover for rows).</p>
      ${this._structureView()}

      <h4 style="margin-top:14px">effective access</h4>
      <p class="muted" style="font-size:11px; max-width:64ch; margin-top:2px">
        The resolved model, straight from the server: what each user can do on
        each tile, and who OWNS it (transfer = reassign, with an impact
        preview). <span class="lv lv-read">r</span> read ·
        <span class="lv lv-write">w</span> write ·
        <span class="lv lv-terminal">t</span> terminal (root shell) ·
        <span class="lv lv-none">·</span> none.
        ${layout === 'matrix' ? 'Click a cell to see WHY.'
          : 'Expand a tile to see who reaches it; click a row for the full derivation.'}
        Workspace admins (${admins.length ? admins.join(', ') : 'root token only'})
        hold terminal everywhere and are omitted; chrome (root/shell) is always
        viewable and outside the model.</p>
      ${!m ? html`<p class="muted">loading…</p>` : !allCols.length
        ? html`<p class="muted">No regular users yet — add some in the users tab.</p>`
        : html`
        <div class="row" style="margin:6px 0; flex-wrap:wrap">
          <input placeholder="filter tiles / owner…" .value=${this._mapTileQ ?? ''}
            @input=${(e) => { this._mapTileQ = e.target.value; }} style="width:170px">
          <input placeholder="filter users…" .value=${this._mapUserQ ?? ''}
            @input=${(e) => { this._mapUserQ = e.target.value; }} style="width:130px">
          <select title="layout" @change=${(e) => { this._mapLayout = e.target.value; }}>
            ${[['auto', `auto (${cols.length > 10 ? 'by-tile list' : 'matrix'})`],
               ['matrix', 'matrix'], ['list', 'by-tile list']].map(([v, l]) =>
              html`<option value=${v} ?selected=${(this._mapLayout ?? 'auto') === v}>${l}</option>`)}
          </select>
          <span class="muted" style="font-size:11px">${nTiles} tile${nTiles === 1 ? '' : 's'} ·
            ${cols.length}/${allCols.length} user${allCols.length === 1 ? '' : 's'}</span>
          ${this._mapHiddenCount(m) ? html`<label class="muted" style="font-size:11px;display:inline-flex;gap:5px;align-items:center">
            <input type="checkbox" .checked=${!!this.showHidden}
              @change=${(e) => { this._emit('bx-admin-show-hidden', e.target.checked); }}> show hidden (${this._mapHiddenCount(m)})</label>` : nothing}
        </div>
        ${!cols.length ? html`<p class="muted">no users match the filter</p>`
          : layout === 'list' ? this._mapList(m, grouped, cols)
          : this._mapMatrix(m, grouped, cols)}
        ${this._mapDetail()}`}
    `;
  }

  // ---- owner reassignment (D39, docs/auth.md §Ownership): picker → preview → confirm ----
  _xferReport(rep) {
    if (!rep) return nothing;
    const lv = rep.callerLevel;
    return html`<div style="margin-top:5px; font-size:11.5px">
      ${lv && lv.before !== lv.after ? html`<div>your access: <b>${lv.before || 'none'}</b> → <b>${lv.after || 'none'}</b></div>` : nothing}
      ${(rep.deadBindings ?? []).map((b) => html`<div style="color:var(--bx-red, #ef5350)">
        binding <span class="mono">${b.slot}</span> will be <b>UNBOUND</b>: ${b.reason}</div>`)}
      ${(rep.deadGrants ?? []).map((g) => html`<div style="color:var(--bx-red, #ef5350)">
        grant <span class="mono">${g.target}:${g.role}</span> becomes inert: ${g.reason}</div>`)}
      ${(rep.planeChanges ?? []).map((s) => html`<div class="muted">${s}</div>`)}
      ${(rep.unbound ?? []).length ? html`<div>unbound: ${rep.unbound.map((s) => html`<span class="pill mono">${s}</span>`)}</div>` : nothing}
    </div>`;
  }

  _ownerEditor(m) {
    const oe = this._ownerEdit;
    if (!oe) return nothing;
    const cur = m?.owners?.[oe.tile] ?? '';
    const opts = [{ value: '', label: '— workspace —' },
      ...(this.users ?? []).map((u) => ({ value: 'user:' + u.id, label: 'user: ' + u.id })),
      ...(this.orgs ?? []).map((o) => ({ value: 'org:' + o.id, label: 'org: ' + o.id }))];
    const pick = (to) => { this._ownerEdit = { tile: oe.tile, to, rep: null, perr: null }; };
    return html`<div style="padding:6px 8px; border:1px solid var(--bx-accent,#f5a623); border-radius:6px; margin:2px 0">
      <div class="row">owner of <span class="mono">${oe.tile}</span>:
        <span class="mono">${cur || 'workspace'}</span> →
        <select @change=${(e) => pick(e.target.value)}>
          ${opts.map((o) => html`<option value=${o.value} ?selected=${oe.to === o.value}>${o.label}</option>`)}
        </select>
        <button class="act" ?disabled=${oe.to === cur} @click=${async () => {
          try {
            const rep = await api(`/owner/preview?tile=${encodeURIComponent(oe.tile)}&to=${encodeURIComponent(oe.to)}`);
            this._ownerEdit = { ...oe, rep, perr: null };
          } catch (e) { this._ownerEdit = { ...oe, rep: null, perr: String(e.message ?? e) }; }
        }}>preview</button>
        ${oe.rep ? html`<button class="go" @click=${async () => {
          try {
            const done = await api('/owner', jbody({ tile: oe.tile, to: oe.to }, 'POST'));
            this._ownerEdit = null;
            this._emit('bx-admin-notice', `${oe.tile} → ${oe.to || 'workspace'}`
              + ((done?.unbound ?? []).length ? ` — unbound: ${done.unbound.join(', ')}` : ''));
            this._emit('bx-admin-refresh'); // owners changed: the router's lists follow
            this.load(true);
          } catch (e) { this._ownerEdit = { ...oe, perr: String(e.message ?? e) }; }
        }}>transfer</button>` : nothing}
        <button @click=${() => { this._ownerEdit = null; }}>cancel</button>
      </div>
      ${oe.perr ? html`<div class="err" style="margin-top:4px">${oe.perr}</div>` : nothing}
      ${this._xferReport(oe.rep)}
    </div>`;
  }
}

customElements.define('bx-admin-map', BxAdminMap);
