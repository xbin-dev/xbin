/**
 * <bx-admin-binding view="grants|roles|providers|wiring"> — the admin
 * console's binding group: the grant table + approvals, the exposed-role
 * catalog, the interface providers, and the wiring of every requested slot
 * to a provider (net slots through bx-netrules, D54). One element for the
 * four sub-tabs because they share the interface model; it loads /bindings
 * itself and reports through bx-admin-err / bx-admin-refresh / bx-admin-tab.
 */
import { LitElement, html, nothing } from 'lit';
import { xbinApi as api } from '/vendor/bx-kit.js';
import { netOptions } from '/vendor/bx-netrules.js';
import { capInfo } from '/vendor/bx-allow.js';
import '/vendor/bx-multiselect.js';
import { base } from '../admin-css.js';
import { WithDrafts, WithFilter, WithRouter } from '../shared.js';

export class BxAdminBinding extends WithRouter(WithFilter(WithDrafts(LitElement))) {
  static properties = {
    view: { type: String },        // grants | roles | providers | wiring
    ov: { attribute: false },      // /auth-overview (components, grants, pending) for grants + roles
    orgs: { attribute: false },    // the org list (which org owns a tile — net rows)
    _ifaces: { state: true },      // /bindings
    _drafts: { state: true },      // bindcustom:<comp>:<slot> free-text refs
    _q: { state: true },
    _cats: { state: true },
    _err: { state: true },
  };
  static styles = [base];

  constructor() {
    super();
    this._q = ''; this._cats = new Set();
  }
  connectedCallback() {
    super.connectedCallback();
    this.load();
  }
  refresh() { return this.load(); }
  async load() {
    try { this._ifaces = await api('/bindings'); this._ok(); } catch (e) { this._fail(e); }
  }
  testApi() { return this.draftApi(); }

  render() {
    switch (this.view) {
      case 'grants': return this._grantsView();
      case 'roles': return this._rolesCatalogView();
      case 'providers': return this._providersView();
      default: return this._bindingView();
    }
  }

  async _grant(from, target, role) {
    await api('/grants', { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ from, target, role }) });
    this._emit('bx-admin-refresh');
  }
  async _revoke(g) {
    await api('/grants', { method: 'DELETE', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(g) });
    this._emit('bx-admin-refresh');
  }

  // ---- binding → grants: the grant table + approvals ----
  _grantsView() {
    const ov = this.ov; if (!ov) return html`<span class="muted">loading…</span>`;
    const comps = ov.components ?? [];
    const pending = (ov.pending ?? []).filter((g) => this._match(g.from, g.target, g.role));
    const grants = (ov.grants ?? []).filter((g) => this._match(g.from, g.target, g.role));
    const total = (ov.grants ?? []).length + (ov.pending ?? []).length;
    return html`
      ${this._filterBar('filter grants by caller, target or role…', null, pending.length + grants.length, total)}
      ${pending.length ? html`<h4>pending requests</h4>
        <table>${pending.map((g) => html`<tr>
          <td class="mono" title=${capInfo(g.target)?.desc ?? ''}>${g.from} → ${g.target}</td>
          <td><span class="pill">${g.role}</span></td>
          <td style="text-align:right">${g.blocked
            ? html`<span class="err-pill" title=${g.blocked}>⛔ blocked by policy</span>`
            : html`<button class="act go" @click=${() => this._grant(g.from, g.target, g.role)}>approve</button>`}</td>
        </tr>`)}</table>` : nothing}

      <h4>active grants</h4>
      <table>${grants.length ? grants.map((g) => html`<tr>
        <td class="mono">${g.from} → ${g.target}</td>
        <td><span class="pill">${g.role}</span></td>
        <td style="text-align:right"><button class="act rm" @click=${() => this._revoke(g)}>revoke</button></td>
      </tr>`) : html`<tr><td class="muted">${this._q ? 'no matching grants' : 'none'}</td></tr>`}</table>

      <h4>add grant</h4>
      <form class="inline" @submit=${(e) => { e.preventDefault(); const f = e.target;
          if (f.from.value && f.target.value && f.role.value) this._grant(f.from.value, f.target.value.trim(), f.role.value.trim());
          f.reset(); }}>
        <select name="from">${comps.map((k) => html`<option>${k.path}</option>`)}</select>
        <span class="muted">→</span>
        <input name="target" placeholder="apps/other or res:…/… or xbin" size="20">
        <span class="muted">:</span>
        <input name="role" placeholder="reader" size="8" value="reader">
        <button class="act go">grant</button>
      </form>`;
  }

  // ---- binding → roles: the exposed-role catalog ----
  _rolesCatalogView() {
    const ov = this.ov; if (!ov) return html`<span class="muted">loading…</span>`;
    const rows = (ov.components ?? []).filter((k) => k.roles)
      .flatMap((k) => Object.entries(k.roles).map(([role, desc]) => ({ path: k.path, role, desc })))
      .filter((r) => this._match(r.path, r.role, r.desc));
    return html`
      <p class="muted">Every role a component <b>exposes</b> for others to be granted (manifest
        <code>expose.roles</code>). Callers request them in <code>uses</code>; you approve in
        <a class="link" @click=${() => this._emit('bx-admin-tab', 'grants')}>grants</a>.</p>
      ${this._filterBar('filter roles by component, role or description…', null, rows.length, rows.length)}
      <table>
        <tr><th>component</th><th>role</th><th>description</th></tr>
        ${rows.map((r) => html`<tr>
          <td class="mono">${r.path}</td><td><span class="pill">${r.role}</span></td>
          <td class="muted">${r.desc}</td></tr>`)}
        ${rows.length === 0 ? html`<tr><td class="muted" colspan="3">no exposed roles${this._q ? ' match' : ''}</td></tr>` : nothing}
      </table>`;
  }

  // ---- interfaces (typed capability wiring; docs/overview/11-interfaces.md) ----
  // Resolves true when the bind went through. A refusal (400 "not covered",
  // 403) is shown, and the caller snaps its <select> back — lit re-renders
  // the same `selected` attributes, so the browser would keep showing the
  // refused pick as if it had been applied.
  async _bindSet(component, slot, provider) {
    try {
      await api('/bindings', {
        method: provider ? 'POST' : 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ component, slot, provider }),
      });
      await this.load();
      return true;
    } catch (e) { this._fail(e); return false; }
  }
  // Replace a multi slot's whole set (bx-multiselect emits the full selection).
  async _bindSetMulti(component, slot, providers) {
    try {
      await api('/bindings', {
        method: providers.length ? 'POST' : 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(providers.length ? { component, slot, providers } : { component, slot }),
      });
      await this.load();
    } catch (e) { this._fail(e); }
  }
  // Shared iface model: the provider roster (by kind, service-aware, instances
  // expanded) and the request list — consumed by both providers + binding views.
  _ifaceModel() {
    const d = this._ifaces || {};
    const comps = d.components || [];
    const instances = d.instances || {};
    const providersByKind = {};
    for (const c of comps)
      for (const def of Object.values(c.provides || {})) {
        const list = (providersByKind[def.kind] ||= []);
        if (def.instances) {
          for (const id of Object.keys(instances[c.component] || {}).sort())
            list.push({ ref: `${c.component}#${id}`, service: def.service });
        } else {
          list.push({ ref: c.component, service: def.service });
        }
      }
    // Net builtins are NOT a fixed list any more: the owning org's network
    // sets decide (org / none / "not covered") — see _netBindRow (D54).
    const builtins = {};
    const requests = comps.flatMap((c) =>
      Object.entries(c.interfaces || {}).map(([slot, def]) => ({ comp: c.component, slot, def })));
    return { d, comps, instances, providersByKind, builtins, requests };
  }

  // The org that owns a tile (null for personal/workspace tiles) — the org
  // list already carries every org's ownedTiles.
  _orgOfTile(comp) {
    return (this.orgs ?? []).find((o) => (o.ownedTiles ?? []).includes(comp)) ?? null;
  }

  // One net slot's row (D54): options come from bx-netrules (the org's sets
  // decide what is offered and what is "not covered"), an unlisted bound ref
  // (lan:… / internet:… from `custom…`) shows as its own option, an inert
  // binding carries the server's reason, and `custom…` reveals a free-text
  // input for the D35 filtered forms.
  _netBindRow(r, d, bound, providers) {
    const pend = (d.pending ?? []).find((p) => p.component === r.comp && p.slot === r.slot);
    const org = this._orgOfTile(r.comp);
    const opts = netOptions({ org, providers, pending: pend });
    const cur = bound[0] ?? '';
    const known = opts.some((o) => o.id === cur);
    const inert = d.inert?.[r.comp]?.[r.slot];
    const ck = `bindcustom:${r.comp}:${r.slot}`;
    const custom = this._draft(ck);
    return html`<tr>
      <td class="mono">${r.comp}</td><td>${r.slot}</td><td><span class="pill">net</span></td>
      <td>
        <select @change=${(e) => {
          const v = e.target.value, el = e.target;
          if (v === '__custom') { this._setDraft(ck, cur && !known ? cur : ''); el.value = cur; return; }
          this._dropDraft(ck);
          this._bindSet(r.comp, r.slot, v).then((ok) => { if (!ok && el.isConnected) el.value = cur; });
        }}>
          ${opts.map((o) => html`<option value=${o.id} title=${o.title} ?selected=${o.id === cur} ?disabled=${!!o.disabled}>${o.label}</option>`)}
          ${cur && !known ? html`<option value=${cur} selected>${cur}</option>` : nothing}
        </select>
        ${custom !== undefined ? html`<form class="inline" style="display:inline-flex; gap:4px; margin-left:4px"
            @submit=${(e) => { e.preventDefault(); const v = e.target.ref.value.trim(); if (!v) return; this._dropDraft(ck); this._bindSet(r.comp, r.slot, v); }}>
            <input name="ref" size="28" placeholder="lan:10.0.0.0/8 · internet:api.example.com:443" .value=${custom}
              title="filtered egress (D35): lan:<ip|cidr>[:port] or internet:<host|ip|cidr>[:port][,…] — hostnames are DNS-pinned; no globs in bindings">
            <button class="act go">bind</button>
            <button class="act" type="button" @click=${() => this._dropDraft(ck)}>✕</button></form>` : nothing}
        ${inert ? html`<span class="pill pol" title=${inert}>inert</span> <span class="warn-line" style="display:inline">${inert}</span>` : nothing}
        ${org ? html`<span class="muted" style="font-size:10.5px" title="owned by org:${org.id} — its network sets bound this list">🏢 ${org.id}</span>` : nothing}
      </td></tr>`;
  }

  // ---- binding → interface providers ----
  _providersView() {
    if (!this._ifaces) return html`<div class="muted">loading…</div>`;
    const { comps, instances, providersByKind } = this._ifaceModel();
    const rows = comps.flatMap((c) => Object.entries(c.provides || {})
      .map(([slot, def]) => ({ comp: c.component, slot, def })))
      .filter((r) => this._match(r.comp, r.slot, r.def.kind, r.def.service));
    return html`
      <p class="muted">Tiles that <b>provide</b> a typed interface others can bind to — net providers,
        service (<code>http</code>) endpoints, ingress terminators. See
        <a href="/docs/elements.md" target="_blank">docs/elements.md</a>.</p>
      ${this._filterBar('filter providers by tile, slot, kind or service…', null, rows.length, rows.length)}
      <table class="tbl">
        <tr><th>tile</th><th>slot</th><th>kind</th><th>instances</th></tr>
        ${rows.map((r) => html`<tr>
          <td class="mono">${r.comp}</td><td>${r.slot}</td>
          <td><span class="pill">${r.def.kind}${r.def.service ? ':' + r.def.service : ''}</span></td>
          <td class="mono">${r.def.instances
            ? (Object.keys(instances[r.comp] || {}).sort().map((id) => html`<span class="pill">#${id}</span>`) || nothing)
            : html`<span class="muted">—</span>`}</td></tr>`)}
        ${rows.length === 0 ? html`<tr><td class="muted" colspan="4">no provider tiles${this._q ? ' match' : ''}${!this._q && Object.keys(providersByKind).length === 0 ? '' : ''}</td></tr>` : nothing}
      </table>`;
  }

  // ---- binding → binding: wire each requested slot to a provider ----
  _bindingView() {
    if (!this._ifaces) return html`<div class="muted">loading…</div>`;
    const { d, providersByKind, builtins, requests } = this._ifaceModel();
    const rows = requests.filter((r) => this._match(r.comp, r.slot, r.def.kind, r.def.service));
    return html`
      <p class="muted">Each component <b>requests</b> typed interface slots; you <b>bind</b> each to a
        provider. The binding is the authorization — unbound means no capability. Public exposure is
        under <a class="link" @click=${() => this._emit('bx-admin-tab', 'expose')}>ingress → services / expose</a>.</p>
      ${this._filterBar('filter by component, slot, kind or service…', null, rows.length, requests.length)}
      <table class="tbl">
        <tr><th>component</th><th>slot</th><th>kind</th><th>bound to</th></tr>
        ${rows.map((r) => {
          const raw = d.bindings?.[r.comp]?.[r.slot];
          const bound = [].concat(raw ?? []).map((x) => (x && x.ref) ? x.ref : x); // string|{ref}|array → refs
          const own = (p) => p === r.comp || p.startsWith(r.comp + '#');
          const opts = [...(builtins[r.def.kind] || []),
            ...(providersByKind[r.def.kind] || [])
              .filter((e) => !own(e.ref) &&
                (r.def.kind !== 'http' || !r.def.service || e.service === r.def.service))
              .map((e) => e.ref)];
          const kind = html`<span class="pill">${r.def.kind}${r.def.service ? ':' + r.def.service : ''}${r.def.multi ? ' ×N' : ''}</span>`;
          if (r.def.kind === 'net' && !r.def.multi) return this._netBindRow(r, d, bound, opts);
          if (r.def.multi) {
            return html`<tr>
              <td class="mono">${r.comp}</td><td>${r.slot}</td><td>${kind}</td>
              <td><bx-multiselect .options=${opts} .selected=${bound} placeholder="— unbound —"
                  @change=${(e) => this._bindSetMulti(r.comp, r.slot, e.detail.selected)}></bx-multiselect></td></tr>`;
          }
          return html`<tr>
            <td class="mono">${r.comp}</td><td>${r.slot}</td><td>${kind}</td>
            <td><select @change=${(e) => this._bindSet(r.comp, r.slot, e.target.value)}>
              <option value="" ?selected=${bound.length === 0}>— unbound —</option>
              ${opts.map((p) => html`<option value=${p} ?selected=${bound[0] === p}>${p}</option>`)}
            </select></td></tr>`;
        })}
        ${rows.length === 0 ? html`<tr><td class="muted" colspan="4">no components request interfaces${this._q ? ' match' : ''}</td></tr>` : nothing}
      </table>`;
  }
}

customElements.define('bx-admin-binding', BxAdminBinding);
