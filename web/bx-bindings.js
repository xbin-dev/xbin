/**
 * <bx-bindings> — the owner's interface-binding panel (docs/overview/11-interfaces.md).
 * The interface counterpart to <bx-grants>: when a component requests a typed
 * interface (a `net` egress, an `http` service, …) that nothing is bound to yet
 * — e.g. right after a tile is installed — it shows one row per unbound slot
 * with a <select> of the providers that can satisfy it, and a one-click bind.
 * The binding is the owner's authorization (agents can't self-bind), so this is
 * where that decision is made. Renders nothing when there is nothing to wire,
 * so it can sit permanently in the root page next to <bx-grants>. A row's
 * quiet Dismiss hides it for this person on all their devices (D188,
 * /vendor/bx-dismiss.js); "dismissed (N) · show" brings them back.
 */
import { LitElement, html, css, nothing, repeat } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import '/vendor/bx-multiselect.js';
import '/vendor/bx-icons.js';
import { onEvent } from '/vendor/events-socket.js';
import { bindPreselect, blockedTitle } from '/vendor/bx-netrules.js';
import { bindingKey, split, prune, dismiss, restore, loadDismissed, updateDismissed, dismissedEvent } from '/vendor/bx-dismiss.js';

// a link-like control that opens or closes a list: focusable, Enter and Space work
const toggle = (label, fn, title = '') => html`<a role="button" tabindex="0" title=${title} @click=${fn}
  @keydown=${(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); fn(); } }}>${label}</a>`;
const xfetch = (...a) => (window.xbin?.fetch ?? fetch)(...a);

export class BxBindings extends LitElement {
  static properties = {
    _pending: { state: true },
    _bindings: { state: true },
    _pick: { state: true },
    _route: { state: true },
    _errs: { state: true },
    _approvable: { state: true },
    _showAll: { state: true },
    _dismissed: { state: true }, // the person's dismissals (bx-dismiss.js); null until read
  };

  static styles = [scrollCss, css`
    :host {
      display: block;
      font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif);
      color: var(--bx-text, #E9EAF0);
    }
    :focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: var(--bx-focus-offset, 2px);
      box-shadow: var(--bx-focus-halo, 0 0 0 2px #0B0C12); }
    /* a card: a border, no shadow (D184) */
    .panel {
      background: var(--bx-panel, #1F2028);
      border: 1px solid var(--bx-border, #33353F);
      border-radius: var(--bx-radius, 2px);
      padding: 8px var(--bx-pad, 12px) 12px;
    }
    h4 {
      margin: 4px 0 8px; color: var(--bx-muted, #A3A6B6);
      font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif);
      letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase;
    }
    /* a narrow panel (a phone): the provider's picker takes a line of its
       own rather than shrink to "apps/egress-appr…" */
    .row { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; min-height: var(--bx-row, 28px); padding: 4px 0;
      border-top: 1px solid var(--bx-border, #33353F); }
    .who { font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); }
    .slot { font-weight: 600; }
    .kind { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
    select, input {
      box-sizing: border-box; min-width: 0; min-height: var(--bx-control-h, 28px); font: inherit; padding: 0 8px;
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
      background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
    }
    select { flex: 1 1 14em; }
    input { flex: 2; }
    input::placeholder { color: var(--bx-subtle, #8E91A2); opacity: 1; }
    .none { color: var(--bx-muted, #A3A6B6); flex: 1; font-style: italic; }
    /* buttons (product-ui §6): bind and publish are the primary action */
    button {
      box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 11px; cursor: pointer;
      background: var(--bx-accent, #8C9BFF); color: var(--bx-accent-ink, #0B0C12);
      border: 1px solid var(--bx-accent, #8C9BFF); border-radius: var(--bx-radius, 2px);
      font: inherit; font-weight: 600;
    }
    button:hover:not(:disabled) { background: var(--bx-accent-hover, #A9B4FF); border-color: var(--bx-accent-hover, #A9B4FF); }
    button:disabled { opacity: .5; cursor: default; }
    /* a row's unbind: a quiet button in the danger colour (R1: row actions are quiet) */
    button.rm { background: transparent; color: var(--bx-danger, #FF7A7A); border-color: transparent; }
    button.rm:hover { background: transparent; border-color: var(--bx-danger, #FF7A7A); }
    a { display: inline-block; color: var(--bx-muted, #A3A6B6); cursor: pointer; text-decoration: none; }
    a:hover { color: var(--bx-text, #E9EAF0); text-decoration: underline; }
    .panel > a { margin-top: 8px; }
    select.mode { flex: none; width: auto; }
    .rerr { color: var(--bx-danger, #FF7A7A); padding: 0 0 4px 2px; }
    .rerr bx-icon { margin-right: 6px; }
    /* Dismiss: a quiet row action (R1), last on the row */
    button.quiet { background: transparent; color: var(--bx-muted, #A3A6B6); border-color: transparent; font-weight: 400; }
    button.quiet:hover:not(:disabled) { background: transparent; border-color: var(--bx-border-strong, #666A7E); color: var(--bx-text, #E9EAF0); }
    .restore { display: block; color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
    .panel > .restore { margin-top: 8px; }
  `];

  constructor() {
    super();
    this._pending = [];
    this._bindings = {};
    this._pick = {};      // "comp slot" -> chosen provider id
    this._route = {};     // "comp slot" -> {mode:'host'|'zone', host, zone, listen} (expose rows)
    this._errs = {};      // "comp slot" -> last server refusal (rendered inline)
    this._showAll = false;
    this._approvable = null; // comp → true: the wiring this person may change (GET /bindings)
    this._dismissed = null;
  }

  connectedCallback() {
    super.connectedCallback();
    this._off = onEvent((e) => {
      if (e.type === 'grants' || e.type === 'reload') this._load();
      if (dismissedEvent(e)) this._loadDismissed(); // another tab or device dismissed or restored
    });
    this._load();
    this._loadDismissed();
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
      // the tiles whose wiring this person may change (an admin: all)
      this._approvable = d.approvable ?? null;
      this._loaded = true;
      this._prune();
    } catch { /* next event reloads */ }
  }

  async _loadDismissed() {
    const d = await loadDismissed(xfetch);
    if (d) { this._dismissed = d; this._prune(); }
  }

  // _prune drops the dismissals of slots the server no longer lists (bound,
  // or gone from the manifest) — once both are read.
  _prune() {
    if (!this._dismissed || !this._loaded) return;
    const live = this._pending.map(bindingKey);
    if (prune(this._dismissed, 'bindings', live) === this._dismissed) return;
    this._save((d) => prune(d, 'bindings', live));
  }

  // _save(fn): change the dismissals — at once here, then stored (a read,
  // change and write, so <bx-grants>' own kind is never undone).
  async _save(fn) {
    if (this._dismissed) this._dismissed = fn(this._dismissed);
    const d = await updateDismissed(xfetch, fn);
    if (d) this._dismissed = d;
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
    const { shown, hidden } = split(this._pending, this._dismissed, 'bindings', bindingKey);
    // the dismissed rows: one quiet line that brings them back
    const back = hidden.length === 0 ? nothing : html`<span class="restore" data-restore>${toggle(`dismissed (${hidden.length}) · show`,
      () => this._save((d) => restore(d, 'bindings', hidden.map(bindingKey))),
      'show the interfaces to bind you dismissed again')}</span>`;
    if (shown.length === 0 && !this._showAll) {
      // the count is a way into wiring someone may change: bindings on tiles
      // they can't rewire are no line on every screen (as bx-grants)
      const n = this._approvable ? active.filter((b) => this._approvable[b.component]).length : active.length;
      return n === 0 ? back
        : html`${toggle(`${n} interface ${n === 1 ? 'binding' : 'bindings'}`, () => { this._showAll = true; })}${back}`;
    }
    return html`<div class="panel">
      ${shown.length > 0 ? html`
        <h4>interfaces to bind</h4>
        ${repeat(shown, (p) => this._key(p), (p) => {
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
            <button class="quiet" data-dismiss title="hide this row for you, on all your devices — the slot stays unbound, and other admins still see it"
              @click=${() => this._save((d) => dismiss(d, 'bindings', bindingKey(p)))}>Dismiss</button>
          </div>
          ${this._errs[key] ? html`<div class="rerr" role="alert"><bx-icon name="error"></bx-icon>${this._errs[key]}</div>` : nothing}`;
        })}` : nothing}
      ${this._showAll ? html`
        <h4 style="margin-top:12px">active bindings</h4>
        ${active.map((b) => html`
          <div class="row">
            <span class="who">${b.component}</span>
            <span class="slot">${b.slot}</span>
            <span class="none">→ ${b.provider}</span>
            <button class="rm" @click=${() => this._unbind(b)}>unbind</button>
          </div>`)}
        ${toggle('hide', () => { this._showAll = false; })}` : (active.length > 0 ? toggle('show all bindings', () => { this._showAll = true; }) : nothing)}
      ${back}
    </div>`;
  }
}

customElements.define('bx-bindings', BxBindings);
