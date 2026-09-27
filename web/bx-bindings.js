/**
 * <bx-bindings> — the owner's interface-binding panel (docs/overview/11-interfaces.md).
 * The interface counterpart to <bx-grants>: when a component requests a typed
 * interface (a `net` egress, an `http` service, …) that nothing is bound to yet
 * — e.g. right after a tile is installed — it shows one row per unbound slot
 * with a <select> of the providers that can satisfy it, and a one-click bind.
 * The binding is the owner's authorization (agents can't self-bind), so this is
 * where that decision is made. Renders nothing when there is nothing to wire,
 * so it can sit permanently in the root page next to <bx-grants>.
 */
import { LitElement, html, css, nothing, repeat } from 'lit';
import '/vendor/bx-multiselect.js';
import { onEvent } from '/vendor/events-socket.js';
import { bindPreselect, blockedTitle } from '/vendor/bx-netrules.js';

export class BxBindings extends LitElement {
  static properties = {
    _pending: { state: true },
    _bindings: { state: true },
    _pick: { state: true },
    _route: { state: true },
    _errs: { state: true },
    _showAll: { state: true },
  };

  static styles = css`
    :host {
      display: block;
      font: var(--bx-font, 13px/1.45 system-ui, sans-serif);
      color: var(--bx-text, #d4d9e0);
    }
    .panel {
      background: var(--bx-panel, #23272e);
      border: 1px solid var(--bx-border, #363c45);
      border-left: 3px solid var(--bx-accent, #f5a623);
      border-radius: var(--bx-radius, 6px);
      box-shadow: var(--bx-shadow, 0 1px 2px rgba(0, 0, 0, 0.35));
      padding: 8px 12px;
    }
    h4 {
      margin: 0 0 4px; font-size: 10.5px; font-weight: 600;
      letter-spacing: .08em; text-transform: uppercase;
      color: var(--bx-muted, #868f9a);
    }
    .row { display: flex; align-items: center; gap: 8px; padding: 3px 0; }
    .who { font-family: var(--bx-mono, ui-monospace, monospace); font-size: 12px; }
    .slot { color: var(--bx-accent, #f5a623); font-size: 12px; font-weight: 600; }
    .kind { color: var(--bx-muted, #868f9a); font-size: 11px; }
    select {
      flex: 1; min-width: 0; font: inherit; font-size: 12px;
      padding: 2px 6px; border: 1px solid var(--bx-border, #363c45);
      border-radius: 5px; background: var(--bx-panel, #23272e); color: inherit;
    }
    .none { color: var(--bx-muted, #868f9a); font-size: 12px; flex: 1; font-style: italic; }
    button {
      background: var(--bx-green, #4caf50); color: #fff; border: 0;
      border-radius: 5px; padding: 2px 10px; cursor: pointer;
      font: inherit; font-size: 12px; font-weight: 600;
    }
    button:disabled { opacity: .45; cursor: default; }
    button.rm {
      background: var(--bx-panel, #23272e); color: var(--bx-red, #ef5350);
      border: 1px solid color-mix(in srgb, var(--bx-red, #ef5350) 40%, transparent);
      font-weight: 500;
    }
    a { color: var(--bx-muted, #868f9a); font-size: 12px; cursor: pointer; }
    a:hover { color: var(--bx-accent, #f5a623); }
    input {
      flex: 2; min-width: 0; font: inherit; font-size: 12px;
      padding: 2px 6px; border: 1px solid var(--bx-border, #363c45);
      border-radius: 5px; background: var(--bx-panel, #23272e); color: inherit;
    }
    select.mode { flex: none; width: auto; }
    .rerr { color: var(--bx-red, #ef5350); font-size: 11.5px; padding: 0 0 3px 2px; }
  `;

  constructor() {
    super();
    this._pending = [];
    this._bindings = {};
    this._pick = {};      // "comp slot" -> chosen provider id
    this._route = {};     // "comp slot" -> {mode:'host'|'zone', host, zone, listen} (expose rows)
    this._errs = {};      // "comp slot" -> last server refusal (rendered inline)
    this._showAll = false;
  }

  connectedCallback() {
    super.connectedCallback();
    this._off = onEvent((e) => { if (e.type === 'grants' || e.type === 'reload') this._load(); });
    this._load();
  }

  disconnectedCallback() { super.disconnectedCallback(); this._off?.(); }

  async _load() {
    try {
      const r = await fetch('/api/xbin/bindings');
      if (!r.ok) return; // not admin, or xbind restarting
      const d = await r.json();
      // Only slots THIS person may wire (ws admin: all; org admin: their
      // orgs' tiles). A tile you merely own is listed by the server for
      // information — its bind would be refused, so no row here.
      // options: null from an older daemon for a slot nobody provides
      this._pending = (d.pending ?? []).filter((p) => p.approvable !== false).map((p) => ({ ...p, options: p.options ?? [] }));
      this._bindings = d.bindings ?? {};
    } catch { /* next event reloads */ }
  }

  _key(p) { return `${p.component}\u0000${p.slot}`; }

  // Route config for an expose row (docs/ingress.md): http publishes need a
  // hostname authority (exact host OR wildcard zone); stream exposes take an
  // optional listen address (defaults to the manifest port).
  _routeFor(p) {
    return this._route[this._key(p)] ?? { mode: 'host', host: '', zone: '', listen: '' };
  }
  _setRoute(p, patch) {
    const k = this._key(p);
    this._route = { ...this._route, [k]: { ...this._routeFor(p), ...patch } };
  }
  _routeReady(p) {
    if (!p.expose) return true;
    if (p.kind !== 'http') return true; // stream: listen optional (manifest port default)
    const rt = this._routeFor(p);
    return !!(rt.mode === 'zone' ? rt.zone.trim() : rt.host.trim());
  }

  async _bind(p) {
    // Multi slots submit the multiselect's whole selection; single slots one id.
    const key = this._key(p);
    const picked = this._pick[key];
    const body = { component: p.component, slot: p.slot };
    if (p.multi) {
      const sel = Array.isArray(picked) ? picked : (picked ? [picked] : [p.options[0]?.id].filter(Boolean));
      if (sel.length === 0) return;
      body.providers = sel;
    } else {
      // what the picker shows: a sandbox-net class starts on none (bx-netrules)
      body.provider = picked ?? bindPreselect(p);
      if (!body.provider) return;
    }
    if (p.expose) {
      const rt = this._routeFor(p);
      if (p.kind === 'http') {
        if (rt.mode === 'zone') body.zone = rt.zone.trim(); else body.host = rt.host.trim();
      } else if (rt.listen.trim()) {
        body.listen = rt.listen.trim();
      }
    }
    const r = await fetch('/api/xbin/bindings', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!r.ok) {
      const d = await r.json().catch(() => ({}));
      this._errs = { ...this._errs, [key]: d.error ?? `bind failed (${r.status})` };
      return;
    }
    const { [key]: _, ...rest } = this._errs;
    this._errs = rest;
    this._load();
  }

  // unbind one binding of a slot: its provider (+ its route, for an exposed
  // endpoint — which takes many, D79); the slot's other bindings stay
  async _unbind(b) {
    const body = { component: b.component, slot: b.slot, provider: b.ref.ref };
    for (const k of ['host', 'zone', 'listen']) if (b.ref[k]) body[k] = b.ref[k];
    await fetch('/api/xbin/bindings', {
      method: 'DELETE', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    this._load();
  }

  // Flatten the {comp: {slot: ref | {ref, host|zone|listen} | […]}} table for
  // the "active bindings" list: a row per binding, its route spelled out.
  _active() {
    const out = [];
    for (const [component, slots] of Object.entries(this._bindings ?? {})) {
      for (const [slot, refs] of Object.entries(slots ?? {})) {
        for (const r of [].concat(refs ?? [])) { // string | object | array → rows
          const ref = typeof r === 'string' ? { ref: r } : (r ?? {});
          if (!ref.ref) continue;
          const route = ref.host || ref.zone || ref.listen;
          out.push({ component, slot, ref, provider: route ? `${ref.ref} (${route})` : ref.ref });
        }
      }
    }
    out.sort((a, b) => (a.component + a.slot).localeCompare(b.component + b.slot));
    return out;
  }

  render() {
    const active = this._active();
    if (this._pending.length === 0 && !this._showAll) {
      return active.length === 0 ? nothing
        : html`<a @click=${() => { this._showAll = true; }}>${active.length} interface binding(s)</a>`;
    }
    return html`<div class="panel">
      ${this._pending.length > 0 ? html`
        <h4>interfaces to bind</h4>
        ${repeat(this._pending, (p) => this._key(p), (p) => {
          // keyed: a row's <select> is never reused for another slot's row,
          // so what it shows is what its bind submits
          const key = this._key(p);
          const cur = this._pick[key] ?? bindPreselect(p);
          const rt = this._routeFor(p);
          // Publishing an EXPOSED endpoint carries route config in the same
          // bind (docs/ingress.md) — without it the server 400s, so the row
          // grows the route editor when expose is set.
          const routeEd = !p.expose ? nothing : p.kind === 'http' ? html`
            <select class="mode" title="exact hostname, or a delegated wildcard zone"
              @change=${(e) => this._setRoute(p, { mode: e.target.value })}>
              <option value="host" ?selected=${rt.mode !== 'zone'}>host</option>
              <option value="zone" ?selected=${rt.mode === 'zone'}>zone</option>
            </select>
            ${rt.mode === 'zone' ? html`
              <input placeholder="*.apps.example.com" .value=${rt.zone}
                @input=${(e) => this._setRoute(p, { zone: e.target.value })}>` : html`
              <input placeholder="app.example.com" .value=${rt.host}
                @input=${(e) => this._setRoute(p, { host: e.target.value })}>`}` : html`
            <input placeholder=":8443 (empty = manifest port)" .value=${rt.listen}
              @input=${(e) => this._setRoute(p, { listen: e.target.value })}>`;
          return html`
          <div class="row">
            <span class="who">${p.component}</span>
            <span class="slot">${p.slot}</span>
            <span class="kind">${p.expose ? `publish ${p.kind}` : p.service ? `${p.kind}:${p.service}` : p.kind}</span>
            ${p.options.length === 0 ? html`
              <span class="none">no provider available — install one first</span>` : p.multi ? html`
              <bx-multiselect style="flex:1" .options=${p.options} .selected=${this._pick[key] ?? []}
                placeholder="pick providers…"
                @change=${(e) => { this._pick = { ...this._pick, [key]: e.detail.selected }; }}></bx-multiselect>
              <button @click=${() => this._bind(p)}>bind</button>` : html`
              <select @change=${(e) => { this._pick = { ...this._pick, [key]: e.target.value }; }}>
                ${cur ? nothing : html`<option value="" disabled selected>pick one…</option>`}
                ${p.options.map((o) => html`<option value=${o.id} ?disabled=${!!o.blocked} ?selected=${o.id === cur}
                  title=${o.blocked ? blockedTitle(o) : ''}>${o.label}</option>`)}
              </select>
              ${routeEd}
              <button ?disabled=${!cur || !this._routeReady(p)}
                @click=${() => this._bind(p)}>${p.expose ? 'publish' : 'bind'}</button>`}
          </div>
          ${this._errs[key] ? html`<div class="rerr">${this._errs[key]}</div>` : nothing}`;
        })}` : nothing}
      ${this._showAll ? html`
        <h4 style="margin-top:.6rem">active bindings</h4>
        ${active.map((b) => html`
          <div class="row">
            <span class="who">${b.component}</span>
            <span class="slot">${b.slot}</span>
            <span class="none">→ ${b.provider}</span>
            <button class="rm" @click=${() => this._unbind(b)}>unbind</button>
          </div>`)}
        <a @click=${() => { this._showAll = false; }}>hide</a>` : (active.length > 0 ? html`
        <a @click=${() => { this._showAll = true; }}>show all bindings</a>` : nothing)}
    </div>`;
  }
}

customElements.define('bx-bindings', BxBindings);
