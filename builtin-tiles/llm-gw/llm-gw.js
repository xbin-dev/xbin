/**
 * <bx-llm-gw> — settings + model browser for the llm-gw tile. Manages MULTIPLE
 * named upstream backends (each a base URL + a vault token "api-token-<name>")
 * with live per-backend usage (requests, tokens in/out, active) from /stats.
 * Talks to its own backend (/config, /stats, /v1/models) via xbin.fetch; tokens
 * go through the backend into its vault — a tile's frontend can't reach the
 * vault API (D30). Model ids are namespaced "<backend>/<model>" when more than
 * one backend is configured. Usage by partition (calls from partitioned tiles)
 * is callers.js's.
 */
import { LitElement, html, css, nothing } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import '/vendor/bx-icons.js'; // <bx-icon name> (D184)

import { selfApi as api } from '/vendor/bx-kit.js';
import { callersView } from './callers.js';

const AUTO_REFRESH_MS = 60_000;

// Group digits with commas: 123123123 → "123,123,123". A plain space
// reads the same as the inter-column gap, blurring which group is which.
const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ',');

export class BxLlmGw extends LitElement {
  static properties = {
    _backends: { state: true },  // [{name, baseURL, hasToken}]
    _stats: { state: true },     // name -> {reqs, tokIn, tokOut, active, cost}
    _callers: { state: true },   // GET /stats' {callers, callerTotals, canManage}: partitioned tiles' calls
    _limit: { state: true },     // the fairness limit (0 = off)
    _aliases: { state: true },
    _preferred: { state: true }, // use-type -> model id
    _useTypes: { state: true },  // ordered list of use-types
    _models: { state: true },
    _modelsLoading: { state: true },
    _modelsErr: { state: true },
    _search: { state: true },
    _err: { state: true },
    _busy: { state: true },
  };

  // Base Two (D184): theme.css's tokens only (the page links it), so the page
  // is right in light and dark; controls as the product's (28px, square,
  // the focus ring), tables with 32px rows, tabular figures and a 2px rule
  // under the header.
  static styles = [scrollCss, css`
    :host { display: block; font: var(--bx-font); color: var(--bx-text); background: var(--bx-panel); }
    button, input, select { font: inherit; }
    ::placeholder { color: var(--bx-subtle); opacity: 1; }
    :focus-visible { outline: var(--bx-focus-outline); outline-offset: var(--bx-focus-offset); box-shadow: var(--bx-focus-halo); }
    .body { padding: var(--bx-pad); }
    .err { display: flex; gap: 6px; align-items: baseline; color: var(--bx-danger); margin: 4px 0; }
    h4 { margin: 16px 0 8px; font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro);
         text-transform: uppercase; color: var(--bx-muted); }
    h4:first-child { margin-top: 0; }
    .muted { color: var(--bx-muted); }
    .hint { font: var(--bx-font-meta); color: var(--bx-muted); }
    .row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-bottom: 8px; }
    input, select { box-sizing: border-box; min-height: var(--bx-control-h); padding: 4px 8px;
      border: 1px solid var(--bx-border-strong); border-radius: var(--bx-radius);
      background: var(--bx-panel); color: var(--bx-text); }
    button.act { box-sizing: border-box; min-height: var(--bx-control-h); display: inline-flex; align-items: center; gap: 6px;
      border: 1px solid var(--bx-border-strong); background: var(--bx-panel); color: var(--bx-text);
      border-radius: var(--bx-radius); font-weight: 600; padding: 4px 11px; cursor: pointer; white-space: nowrap; }
    button.act:hover { background: var(--bx-hover); }
    button.act:disabled { opacity: .5; cursor: default; }
    /* go: the primary (accent fill); quiet: text only, a table row's actions; rm: the danger outline */
    button.go { background: var(--bx-accent); border-color: var(--bx-accent); color: var(--bx-accent-ink); }
    button.go:hover { background: var(--bx-accent-hover); border-color: var(--bx-accent-hover); }
    button.quiet { background: transparent; border-color: transparent; padding: 4px 6px; }
    button.quiet:hover { background: transparent; color: var(--bx-accent); }
    button.rm { color: var(--bx-danger); border-color: var(--bx-danger); }
    button.quiet.rm { border-color: transparent; }
    .ok, .warn { display: inline-flex; align-items: center; gap: 4px; }
    .ok { color: var(--bx-ok); }
    .warn { color: var(--bx-warn); }
    .mono { font-family: var(--bx-mono); }
    td.mono { font: var(--bx-font-code); }
    table { border-collapse: collapse; width: 100%; font-variant-numeric: tabular-nums; }
    th { text-align: left; font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase;
         color: var(--bx-muted); padding: 4px 8px 4px 0; border-bottom: 2px solid var(--bx-text); }
    td { height: 32px; box-sizing: border-box; padding: 2px 8px 2px 0;
         border-top: 1px solid var(--bx-border); vertical-align: middle; }
    .search { width: 100%; margin-bottom: 8px; }
    .models { max-height: 260px; overflow: auto; border: 1px solid var(--bx-border); border-radius: var(--bx-radius); }
    .models table { width: 100%; }
    .models th { position: sticky; top: 0; background: var(--bx-panel); padding-left: 8px; }
    .models td { padding-left: 8px; }
    .count { font: var(--bx-font-meta); color: var(--bx-muted); text-transform: none; letter-spacing: 0; }
    .models-head { display: flex; align-items: baseline; justify-content: space-between; }
  `];

  constructor() {
    super();
    this._backends = [];
    this._stats = {};
    this._callers = {};
    this._limit = 0;
    this._aliases = {};
    this._preferred = {};
    this._useTypes = [];
    this._models = [];
    this._modelsLoading = false;
    this._modelsErr = '';
    this._search = '';
    this._err = '';
    this._busy = false;
  }

  connectedCallback() {
    super.connectedCallback();
    this._off = window.xbin?.events.on((e) => {
      if (e.type === 'reload' || e.type === 'build-ok') this._refresh();
    });
    this._timer = setInterval(() => this._loadModels(), AUTO_REFRESH_MS);
    this._statsTimer = setInterval(() => this._loadStats(), 3000);
    this._refresh();
  }
  disconnectedCallback() {
    super.disconnectedCallback();
    this._off?.();
    clearInterval(this._timer);
    clearInterval(this._statsTimer);
  }

  async _refresh() {
    try {
      const cfg = await api('/config');
      this._backends = cfg.backends ?? [];
      this._aliases = cfg.aliases ?? {};
      this._preferred = cfg.preferred ?? {};
      this._useTypes = cfg.useTypes ?? [];
      this._limit = cfg.partitionLimit ?? 0;
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
    this._loadStats();
    this._loadModels();
  }

  async _loadStats() {
    try {
      const s = await api('/stats');
      this._stats = s.backends ?? {};
      this._callers = { callers: s.callers, callerTotals: s.callerTotals, canManage: s.canManage };
    } catch { /* backend restarting; next tick */ }
  }

  async _loadModels() {
    this._modelsLoading = true;
    try {
      const d = await api('/v1/models');
      this._models = (d.data ?? []).filter((m) => !m.alias_of);
      this._modelsErr = d.error ?? '';
    } catch (e) { this._modelsErr = String(e.message ?? e); }
    this._modelsLoading = false;
  }

  // Add or update a backend. The backend stores the token in this tile's
  // vault (never kv); the page can't reach the vault API itself (D30).
  async _saveBackend(name, baseURL, token) {
    this._busy = true;
    try {
      await api('/config/backend', { method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, baseURL, token: token || '' }) });
      await this._refresh();
    } catch (e) { this._err = String(e.message ?? e); }
    this._busy = false;
  }

  async _removeBackend(name) {
    if (!confirm(`Remove backend "${name}"? Its vault token is deleted too.`)) return;
    this._busy = true;
    try {
      await api(`/config/backend/${encodeURIComponent(name)}`, { method: 'DELETE' });
      await this._refresh();
    } catch (e) { this._err = String(e.message ?? e); }
    this._busy = false;
  }

  async _setToken(name) {
    const v = prompt(`API token for backend "${name}":`);
    if (!v?.trim()) return;
    this._busy = true;
    try {
      await api(`/config/backend/${encodeURIComponent(name)}/token`, { method: 'PUT',
        headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token: v.trim() }) });
      await this._refresh();
    } catch (e) { this._err = String(e.message ?? e); }
    this._busy = false;
  }

  // Set (or clear) the workspace's preferred model for a use-type. Callers
  // (the agent, chat, pipelines) resolve their default model from here.
  async _savePreferred(use, model) {
    try {
      const d = await api('/config/preferred', {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ use, model }),
      });
      this._preferred = d.preferred ?? {};
    } catch (e) { this._err = String(e.message ?? e); }
  }

  async _saveLimit(partitionLimit) {
    try {
      const d = await api('/config', { method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ partitionLimit }) });
      this._limit = d.partitionLimit ?? 0;
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
  }

  async _saveAliases(aliases) {
    try {
      const d = await api('/config', { method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ aliases }) });
      this._aliases = d.aliases ?? {};
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
  }

  _addAlias(target) {
    const alias = prompt(`Alias name for "${target}" (e.g. best-model):`);
    if (!alias || !alias.trim()) return;
    this._saveAliases({ ...this._aliases, [alias.trim()]: target });
  }
  _editAlias(alias, target) {
    const nv = prompt(`Target model for alias "${alias}":`, target);
    if (nv == null || !nv.trim()) return;
    this._saveAliases({ ...this._aliases, [alias]: nv.trim() });
  }
  _removeAlias(alias) {
    const a = { ...this._aliases }; delete a[alias];
    this._saveAliases(a);
  }
  _addAliasForm(e) {
    e.preventDefault();
    const f = e.target;
    const alias = f.alias.value.trim(), target = f.target.value.trim();
    if (!alias || !target) return;
    this._saveAliases({ ...this._aliases, [alias]: target });
    f.reset();
  }

  render() {
    const q = this._search.trim().toLowerCase();
    const models = q ? this._models.filter((m) => String(m.id).toLowerCase().includes(q)) : this._models;
    const aliasEntries = Object.entries(this._aliases).sort(([a], [b]) => a.localeCompare(b));

    return html`
      <div class="body">
        ${this._err ? html`<div class="err"><bx-icon name="error"></bx-icon><span>${this._err}</span></div>` : nothing}

        <h4>backends</h4>
        <table>
          <tr><th>name</th><th>base URL</th><th>token</th><th style="text-align:right">reqs</th>
              <th style="text-align:right">tok in</th><th style="text-align:right">tok out</th>
              <th style="text-align:right">cost</th><th style="text-align:right">active</th><th></th></tr>
          ${this._backends.map((b) => {
            const st = this._stats[b.name] ?? {};
            return html`<tr>
              <td class="mono">${b.name}</td>
              <td class="mono muted" style="max-width:220px; overflow:hidden; text-overflow:ellipsis">${b.baseURL}</td>
              <td>${b.hasToken ? html`<span class="ok"><bx-icon name="ok"></bx-icon>set</span>` : html`<span class="warn"><bx-icon name="warning"></bx-icon>none</span>`}</td>
              <td class="mono" style="text-align:right">${fmtN(st.reqs)}</td>
              <td class="mono" style="text-align:right">${fmtN(st.tokIn)}</td>
              <td class="mono" style="text-align:right">${fmtN(st.tokOut)}</td>
              <td class="mono muted" style="text-align:right">${st.cost ? '$' + Number(st.cost).toFixed(2) : '—'}</td>
              <td class="mono" style="text-align:right">${st.active
                ? html`<span class="ok">${fmtN(st.active)}</span>` : '0'}</td>
              <td style="text-align:right; white-space:nowrap">
                <button class="act quiet" ?disabled=${this._busy} @click=${() => this._setToken(b.name)}>token</button>
                <button class="act quiet" ?disabled=${this._busy} @click=${() => {
                  const nv = prompt(`Base URL for "${b.name}":`, b.baseURL);
                  if (nv?.trim()) this._saveBackend(b.name, nv.trim());
                }}>url</button>
                ${this._backends.length > 1 ? html`
                  <button class="act quiet rm" ?disabled=${this._busy} @click=${() => this._removeBackend(b.name)}>del</button>` : nothing}
              </td></tr>`;
          })}
        </table>
        <form class="row" style="margin-top:6px" @submit=${(e) => { e.preventDefault(); const f = e.target;
            const name = f.name.value.trim(), url = f.url.value.trim();
            if (!name || !url) return;
            this._saveBackend(name, url, f.token.value.trim());
            f.reset(); }}>
          <input name="name" placeholder="name (ollama)" size="10" pattern="[a-z0-9][a-z0-9._-]*" ?disabled=${this._busy}>
          <input name="url" placeholder="https://host/v1" size="22" ?disabled=${this._busy}>
          <input name="token" type="password" placeholder="api token" size="14" autocomplete="off" ?disabled=${this._busy}>
          <button class="act go" ?disabled=${this._busy}>add backend</button>
        </form>
        ${this._backends.length > 1 ? html`
          <div class="hint" style="margin-top:4px">
            With several backends, model ids are namespaced
            <span class="mono">&lt;backend&gt;/&lt;model&gt;</span> — requests route by that prefix.
          </div>` : nothing}

        ${callersView(this._callers, this._limit, (n) => this._saveLimit(n))}

        <h4>preferred models</h4>
        <div class="hint" style="margin-bottom:8px">
          The workspace's default model per job. Tiles (the agent, chat,
          pipelines) resolve their model from here — set once, swap anywhere.
        </div>
        <div style="display:grid; grid-template-columns:repeat(auto-fill,minmax(190px,1fr)); gap:6px 10px; margin-bottom:4px">
          ${(this._useTypes ?? []).map((u) => html`
            <label style="display:flex; flex-direction:column; gap:4px">
              <span class="muted" style="text-transform:capitalize">${u}</span>
              <select ?disabled=${!this._models.length}
                @change=${(e) => this._savePreferred(u, e.target.value)}>
                <option value="" ?selected=${!this._preferred[u]}>— none —</option>
                ${this._models.map((m) => html`
                  <option value=${m.id} ?selected=${this._preferred[u] === m.id}>${m.id}</option>`)}
              </select>
            </label>`)}
        </div>

        <div class="models-head">
          <h4>models ${models.length ? html`<span class="count">(${models.length}${q ? ` of ${this._models.length}` : ''})</span>` : nothing}</h4>
          <button class="act" ?disabled=${this._modelsLoading} @click=${() => this._loadModels()}>
            <bx-icon name="refresh"></bx-icon> refresh
          </button>
        </div>
        ${this._modelsErr ? html`<div class="err"><bx-icon name="error"></bx-icon><span>${this._modelsErr}</span></div>` : nothing}
        <input class="search" type="search" placeholder="search models…"
          .value=${this._search} @input=${(e) => { this._search = e.target.value; }}>
        <div class="models">
          <table>
            <tr><th>id</th><th>owner</th><th></th></tr>
            ${models.length ? models.map((m) => html`<tr>
              <td class="mono">${m.id}</td>
              <td class="muted">${m.owned_by ?? ''}</td>
              <td style="text-align:right"><button class="act quiet" @click=${() => this._addAlias(m.id)}>+ alias</button></td>
            </tr>`) : html`<tr><td class="muted" colspan="3">${this._modelsLoading ? 'loading…' : this._backends.some((b) => b.hasToken) ? 'no models found' : 'set an api token to list models'}</td></tr>`}
          </table>
        </div>

        <h4>aliases</h4>
        <table>
          ${aliasEntries.length ? aliasEntries.map(([alias, target]) => html`<tr>
            <td class="mono">${alias}</td>
            <td class="muted">→</td>
            <td class="mono">${target}</td>
            <td style="text-align:right">
              <button class="act quiet" @click=${() => this._editAlias(alias, target)}>edit</button>
              <button class="act quiet rm" @click=${() => this._removeAlias(alias)}>del</button>
            </td>
          </tr>`) : html`<tr><td class="muted">none yet — add one from the model list above, or below.</td></tr>`}
        </table>
        <form class="row" @submit=${(e) => this._addAliasForm(e)}>
          <input name="alias" placeholder="alias (best-model)" size="16">
          <span class="muted">→</span>
          <input name="target" placeholder="real model id" size="22" list="llm-gw-models">
          <button class="act go">add alias</button>
        </form>
        <datalist id="llm-gw-models">
          ${this._models.map((m) => html`<option value=${m.id}></option>`)}
        </datalist>
      </div>`;
  }
}

customElements.define('bx-llm-gw', BxLlmGw);
