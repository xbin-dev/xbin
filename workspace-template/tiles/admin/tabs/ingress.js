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
    _ingEdit: { state: true },  // per-row route edits before publish (comp\x00slot → {…})
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
  _ingEditFor(e) {
    // The working row state: pending edits over the current binding.
    return this._ingEdit?.[this._ingKey(e.component, e.slot)]
      ?? { source: e.source || '', host: e.host || '', zone: e.zone || '', listen: e.listen || '' };
  }
  _ingSetEdit(e, patch) {
    const k = this._ingKey(e.component, e.slot);
    this._ingEdit = { ...(this._ingEdit || {}), [k]: { ...this._ingEditFor(e), ...patch } };
  }
  async _ingPublish(e) {
    const ed = this._ingEditFor(e);
    if (!ed.source) return;
    const body = { component: e.component, slot: e.slot, provider: ed.source };
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
  async _ingUnpublish(e) {
    try {
      await api('/bindings', { method: 'DELETE', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ component: e.component, slot: e.slot }) });
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
    const exposes = all.filter((e) => this._match(e.component, e.slot, e.kind, e.host, e.zone));
    return html`
      <p class="muted">Tiles declare <code>exposes</code> in their manifest; <b>binding a slot to an
        ingress source publishes it</b> to the outside — anonymous traffic, confined to the tile's
        declared public paths. Unbound = unreachable, exactly like interfaces.
        See <a href="/docs/ingress.md" target="_blank">docs/ingress.md</a>.</p>
      ${all.length === 0 ? html`<div class="muted">No tile declares <code>exposes</code> yet. Add an
        <span class="mono">exposes</span> block to a tile's <span class="mono">xbin.json</span>
        (http paths, or a tcp/udp port), or import the <b>Public HTTPS (Traefik)</b> tile.</div>` : html`
      ${this._filterBar('filter exposed endpoints…', null, exposes.length, all.length)}
      <table class="tbl">
        <tr><th>tile</th><th>endpoint</th><th>source</th><th>route</th><th></th><th>state</th></tr>
        ${exposes.map((e) => {
          const ed = this._ingEditFor(e);
          const bound = !!e.source;
          const dirty = ed.source !== (e.source || '') || ed.host !== (e.host || '')
            || ed.zone !== (e.zone || '') || ed.listen !== (e.listen || '');
          const endpoint = e.kind === 'http'
            ? html`<span class="pill">http</span> <span class="muted">${(e.paths || []).join(' ')}</span>`
            : html`<span class="pill">${e.proto}:${e.port}</span>`;
          const routeEd = e.kind === 'http'
            ? html`<input class="mono" style="width:12em" placeholder="host (blog.example.com)"
                     .value=${ed.zone ? '' : ed.host} ?disabled=${!!ed.zone}
                     @input=${(ev) => this._ingSetEdit(e, { host: ev.target.value.trim(), zone: '' })}>
                   <input class="mono" style="width:11em" placeholder="or zone (*.sites.…)"
                     .value=${ed.zone}
                     @input=${(ev) => this._ingSetEdit(e, { zone: ev.target.value.trim() })}>`
            : html`<input class="mono" style="width:8em" placeholder=":${e.port} (host port)"
                     .value=${ed.listen}
                     @input=${(ev) => this._ingSetEdit(e, { listen: ev.target.value.trim() })}>`;
          let state = html`<span class="muted">unbound — not reachable</span>`;
          if (e.blocked) state = html`<span class="st-failed">⛔ ${e.blocked}</span>`;
          else if (bound && e.kind === 'http') state = html`<span class="st-healthy">public: ${e.zone || e.host}</span>`;
          else if (bound) {
            const st = streams.find((s) => s.component === e.component && s.slot === e.slot);
            state = st?.error ? html`<span class="st-failed">⚠ ${st.error}</span>`
              : html`<span class="st-healthy">host ${e.listen || ':' + e.port} → :${e.port}${st ? ` (${st.active} active)` : ''}</span>`;
          }
          return html`<tr>
            <td class="mono">${e.component}</td><td>${e.slot} ${endpoint}</td>
            <td><select ?disabled=${!!e.blocked} @change=${(ev) => this._ingSetEdit(e, { source: ev.target.value })}>
              <option value="" ?selected=${!ed.source}>— unbound —</option>
              ${sources(e.kind).map((s) => html`<option value=${s} ?selected=${ed.source === s}>${s}</option>`)}
            </select></td>
            <td>${routeEd}</td>
            <td>
              ${ed.source && (dirty || !bound) ? html`<button class="act go" @click=${() => this._ingPublish(e)}>publish</button>` : nothing}
              ${bound ? html`<button class="act rm" @click=${() => this._ingUnpublish(e)}>unpublish</button>` : nothing}
            </td>
            <td>${state}</td></tr>`;
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
