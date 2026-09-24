/**
 * <bx-admin-ingress view="expose|endpoints"> — the admin console's ingress
 * group: publish a tile's declared exposes to an ingress source (host / zone
 * / host port), and the live public routing table (routes, stream
 * listeners, terminator doors). Loads /ingress itself; reports through
 * bx-admin-err / bx-admin-tab.
 */
import { LitElement, html, nothing } from 'lit';
import { xbinApi as api } from '/vendor/bx-kit.js';
import { base } from '../admin-css.js';
import { WithFilter, WithRouter } from '../shared.js';

export class BxAdminIngress extends WithRouter(WithFilter(LitElement)) {
  static properties = {
    view: { type: String },     // expose | endpoints
    _ingress: { state: true },  // /ingress {exposes, routes, streams, forwards, httpListener, terminators}
    _ingEdit: { state: true },  // per-endpoint add-a-route edits (comp\x00slot → {source, host, zone, listen})
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
    try { this._ingress = await api('/ingress'); this._ok(); } catch (e) { this._fail(e); }
  }

  render() { return this.view === 'endpoints' ? this._ingressEndpointsView() : this._ingressExposeView(); }

  _ingKey(comp, slot) { return comp + '\x00' + slot; }
  // every route of an exposed endpoint — it takes many (D79: more hostnames,
  // more host ports); an older xbind sends only the first, in the scalars
  _ingRoutes(e) {
    if (Array.isArray(e.routes)) return e.routes;
    return e.source ? [{ source: e.source, host: e.host, zone: e.zone, listen: e.listen }] : [];
  }
  // the add-a-route editor's working state (per endpoint)
  _ingEditFor(e) { return this._ingEdit?.[this._ingKey(e.component, e.slot)] ?? { source: '', host: '', zone: '', listen: '' }; }
  _ingSetEdit(e, patch) {
    const k = this._ingKey(e.component, e.slot);
    this._ingEdit = { ...(this._ingEdit || {}), [k]: { ...this._ingEditFor(e), ...patch } };
  }
  async _ingPublish(e) {
    const ed = this._ingEditFor(e);
    if (!ed.source) return;
    // a bound endpoint gets one MORE route; an unbound one is published
    const body = { component: e.component, slot: e.slot, provider: ed.source, add: this._ingRoutes(e).length > 0 };
    if (e.kind === 'http') {
      // Exactly one of host/zone — send whichever is filled (server validates).
      if (ed.zone) body.zone = ed.zone; else body.host = ed.host;
    } else if (ed.listen) body.listen = ed.listen;
    try {
      await api('/bindings', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      const k = this._ingKey(e.component, e.slot);
      const { [k]: _, ...rest } = this._ingEdit || {}; this._ingEdit = rest;
      await this.load();
    } catch (err) { this._fail(err); }
  }
  // remove one route (the endpoint stays published while any is left)
  async _ingRemove(e, r) {
    const body = { component: e.component, slot: e.slot, provider: r.source };
    if (r.host) body.host = r.host;
    if (r.zone) body.zone = r.zone;
    if (r.listen) body.listen = r.listen;
    try {
      await api('/bindings', { method: 'DELETE', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      await this.load();
    } catch (err) { this._fail(err); }
  }
  // ---- ingress → services / expose: publish tiles to the outside ----
  _ingressExposeView() {
    const d = this._ingress;
    if (!d) return html`<span class="muted">loading…</span>`;
    const streams = d.streams || [];
    const sources = (kind) => kind === 'http' ? ['runtime', ...(d.terminators || [])] : ['runtime'];
    const all = d.exposes || [];
    const exposes = all.filter((e) => this._match(e.component, e.slot, e.kind,
      ...this._ingRoutes(e).flatMap((r) => [r.source, r.host, r.zone, r.listen])));
    const routeText = (e, r) => e.kind === 'http' ? (r.zone ? `${r.zone} (zone)` : r.host) : `host ${r.listen || ':' + e.port} → :${e.port}`;
    const routeState = (e, r) => {
      if (e.kind === 'http') return html`<span class="st-healthy">public: ${r.zone || r.host}</span>`;
      const listen = r.listen || ':' + e.port;
      const st = streams.find((s) => s.component === e.component && s.slot === e.slot && s.listen === listen);
      return st?.error ? html`<span class="st-failed">⚠ ${st.error}</span>`
        : html`<span class="st-healthy">listening${st ? ` (${st.active} active)` : ''}</span>`;
    };
    return html`
      <p class="muted">Tiles declare <code>exposes</code> in their manifest; <b>binding a slot to an
        ingress source publishes it</b> to the outside — anonymous traffic, confined to the tile's
        declared public paths. Unbound = unreachable, exactly like interfaces. An endpoint takes
        <b>many routes</b>: more hostnames (through one terminator or several), more host ports —
        each hostname and port still belongs to exactly one endpoint.
        See <a href="/docs/ingress.md" target="_blank">docs/ingress.md</a>.</p>
      ${all.length === 0 ? html`<div class="muted">No tile declares <code>exposes</code> yet. Add an
        <span class="mono">exposes</span> block to a tile's <span class="mono">xbin.json</span>
        (http paths, or a tcp/udp port), or import the <b>Public HTTPS (Traefik)</b> tile.</div>` : html`
      ${this._filterBar('filter exposed endpoints…', null, exposes.length, all.length)}
      <table class="tbl">
        <tr><th>tile</th><th>endpoint</th><th>source</th><th>route</th><th></th><th>state</th></tr>
        ${exposes.map((e) => {
          const routes = this._ingRoutes(e);
          const ed = this._ingEditFor(e);
          const endpoint = e.kind === 'http'
            ? html`<span class="pill">http</span> <span class="muted">${(e.paths || []).join(' ')}</span>`
            : html`<span class="pill">${e.proto}:${e.port}</span>`;
          const head = (i) => i === 0 ? html`<td class="mono">${e.component}</td><td>${e.slot} ${endpoint}</td>` : html`<td></td><td></td>`;
          const routeEd = e.kind === 'http'
            ? html`<input class="mono ing-host" style="width:12em" placeholder="host (blog.example.com)"
                     .value=${ed.zone ? '' : ed.host} ?disabled=${!!ed.zone}
                     @input=${(ev) => this._ingSetEdit(e, { host: ev.target.value.trim(), zone: '' })}>
                   <input class="mono ing-zone" style="width:11em" placeholder="or zone (*.sites.…)"
                     .value=${ed.zone}
                     @input=${(ev) => this._ingSetEdit(e, { zone: ev.target.value.trim() })}>`
            : html`<input class="mono ing-listen" style="width:8em" placeholder=":${e.port} (host port)"
                     .value=${ed.listen}
                     @input=${(ev) => this._ingSetEdit(e, { listen: ev.target.value.trim() })}>`;
          const addState = e.blocked ? html`<span class="st-failed">⛔ ${e.blocked}</span>`
            : routes.length ? html`<span class="muted">add another ${e.kind === 'http' ? 'hostname' : 'host port'}</span>`
              : html`<span class="muted">unbound — not reachable</span>`;
          return html`
            ${routes.map((r, i) => html`<tr class="ing-route" data-ep=${e.component + '.' + e.slot}>
              ${head(i)}
              <td class="mono">${r.source}</td>
              <td class="mono">${routeText(e, r)}</td>
              <td><button class="act rm" title="remove this route" @click=${() => this._ingRemove(e, r)}>remove</button></td>
              <td>${e.blocked ? html`<span class="st-failed">⛔ ${e.blocked}</span>` : routeState(e, r)}</td></tr>`)}
            <tr class="ing-add" data-ep=${e.component + '.' + e.slot}>
              ${head(routes.length)}
              <td><select ?disabled=${!!e.blocked} @change=${(ev) => this._ingSetEdit(e, { source: ev.target.value })}>
                <option value="" ?selected=${!ed.source}>${routes.length ? '+ route via…' : '— unbound —'}</option>
                ${sources(e.kind).map((s) => html`<option value=${s} ?selected=${ed.source === s}>${s}</option>`)}
              </select></td>
              <td>${routeEd}</td>
              <td>${ed.source ? html`<button class="act go" @click=${() => this._ingPublish(e)}>${routes.length ? 'add' : 'publish'}</button>` : nothing}</td>
              <td>${addState}</td></tr>`;
        })}
        ${exposes.length === 0 ? html`<tr><td class="muted" colspan="6">no matching endpoints</td></tr>` : nothing}
      </table>`}`;
  }

  // ---- ingress → endpoints: live routes, listeners, terminators ----
  _ingressEndpointsView() {
    const d = this._ingress;
    if (!d) return html`<span class="muted">loading…</span>`;
    const routes = (d.routes || []).filter((r) => this._match(r.host, r.component, r.slot, r.source));
    const streams = d.streams || [];
    const forwards = d.forwards || [];
    const lst = d.httpListener || {};
    return html`
      <p class="muted">The live public routing table — what the outside can reach right now. Publish
        or unpublish under <a class="link" @click=${() => this._emit('bx-admin-tab', 'expose')}>services / expose</a>.</p>
      <div class="muted" style="margin:2px 0 10px">
        builtin HTTP listener: ${lst.listen ? html`<b>${lst.listen}</b> (${lst.tls ? 'TLS' : 'no TLS — front it, or use the Traefik tile'})` : 'off — start xbind with --ingress-listen'}
      </div>
      <h4>HTTP routes</h4>
      ${this._filterBar('filter routes by host or tile…', null, routes.length, (d.routes || []).length)}
      <table class="tbl">
        <tr><th>public host</th><th>→ tile</th><th>via</th></tr>
        ${routes.map((r) => html`<tr>
          <td class="mono">${r.host}</td>
          <td class="mono">${r.component}.${r.slot}</td>
          <td>${r.source}${r.zone ? html` <span class="muted">(zone ${r.zone})</span>` : nothing}</td></tr>`)}
        ${routes.length === 0 ? html`<tr><td class="muted" colspan="3">no HTTP routes${this._q ? ' match' : ''}</td></tr>` : nothing}
      </table>
      ${streams.length ? html`<h4>stream listeners (tcp / udp)</h4>
        <table class="tbl">
          <tr><th>host listen</th><th>→ tile</th><th>proto</th><th>state</th></tr>
          ${streams.map((s) => html`<tr>
            <td class="mono">${s.listen}</td>
            <td class="mono">${s.component}.${s.slot} → :${s.port}</td>
            <td>${s.proto}</td>
            <td>${s.error ? html`<span class="st-failed">⚠ ${s.error}</span>` : html`<span class="st-healthy">${s.active} active</span>`}</td></tr>`)}
        </table>` : nothing}
      ${forwards.length ? html`<h4>terminator forward doors</h4>
        <table class="tbl">
          <tr><th>terminator tile</th><th>state</th></tr>
          ${forwards.map((f) => html`<tr>
            <td class="mono">${f.source}</td>
            <td>${f.error ? html`<span class="st-failed">⚠ ${f.error}</span>` : html`<span class="st-healthy">up</span>`}</td></tr>`)}
        </table>` : nothing}`;
  }
}

customElements.define('bx-admin-ingress', BxAdminIngress);
