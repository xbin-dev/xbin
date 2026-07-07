/**
 * <bx-llm-gw> — settings + model browser for the llm-gw tile. Manages MULTIPLE
 * named upstream backends (each a base URL + a vault token "api-token-<name>")
 * with live per-backend usage (requests, tokens in/out, active) from /stats.
 * Talks to its own backend (/config, /stats, /v1/models) via xbin.fetch and to
 * its own vault keys directly — an element always reaches its own vault
 * (docs/auth.md). Model ids are namespaced "<backend>/<model>" when more than
 * one backend is configured.
 */
import { LitElement, html, css, nothing } from 'lit';

const api = async (path, opts) => {
  const r = await xbin.fetch(`/api/${xbin.self}${path}`, opts);
  const text = await r.text();
  let data; try { data = text ? JSON.parse(text) : null; } catch { data = text; }
  if (!r.ok) throw new Error(data?.error ?? r.status);
  return data;
};

const vault = async (key, opts) => {
  const r = await xbin.fetch(`/api/xbin/vault/${xbin.self}/${encodeURIComponent(key)}`, opts);
  const text = await r.text();
  let data; try { data = text ? JSON.parse(text) : null; } catch { data = text; }
  if (!r.ok) throw new Error(data?.error ?? r.status);
  return data;
};

const AUTO_REFRESH_MS = 60_000;

// Group digits for readability: 123123123 → "123 123 123" (narrow no-break
// space so counts don't wrap mid-number in the table).
const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');

export class BxLlmGw extends LitElement {
  static properties = {
    _backends: { state: true },  // [{name, baseURL, hasToken}]
    _stats: { state: true },     // name -> {reqs, tokIn, tokOut, active}
    _aliases: { state: true },
    _models: { state: true },
    _modelsLoading: { state: true },
    _modelsErr: { state: true },
    _search: { state: true },
    _err: { state: true },
    _busy: { state: true },
  };

  static styles = css`
    :host { display: block; font: var(--bx-font, 13px/1.45 system-ui, sans-serif);
            color: var(--bx-text, #33414e); background: var(--bx-panel, #fff); }
    .body { padding: 12px 14px; }
    .err { color: var(--bx-red, #e5484d); font-size: 12px; margin: 4px 0; }
    h4 { margin: 16px 0 6px; font-size: 10.5px; font-weight: 600; letter-spacing: .08em;
         text-transform: uppercase; color: var(--bx-muted, #8794a1); }
    h4:first-child { margin-top: 0; }
    .muted { color: var(--bx-muted, #8794a1); }
    .row { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; margin-bottom: 6px; }
    label.f { display: flex; flex-direction: column; gap: 2px; font-size: 10.5px;
      font-weight: 600; letter-spacing: .05em; text-transform: uppercase;
      color: var(--bx-muted, #8794a1); flex: 1 1 220px; }
    input, select { font: inherit; font-size: 12px; padding: 3px 7px;
      border: 1px solid var(--bx-border, #e4e8ed); border-radius: 5px;
      background: var(--bx-panel, #fff); color: var(--bx-text, #33414e); }
    input:focus, select:focus { outline: 2px solid color-mix(in srgb, var(--bx-accent) 30%, transparent); }
    button.act { border: 1px solid var(--bx-border, #e4e8ed); background: var(--bx-panel, #fff);
      color: var(--bx-text, #33414e); border-radius: 5px; font: inherit; font-size: 11px;
      padding: 3px 10px; cursor: pointer; white-space: nowrap; }
    button.act:hover { background: var(--bx-panel-2, #f7f8fa); }
    button.act:disabled { opacity: .5; cursor: default; }
    button.go { background: var(--bx-accent, #1e88e5); color: #fff; border-color: transparent; }
    button.rm { color: var(--bx-red, #e5484d); border-color: color-mix(in srgb, var(--bx-red) 40%, transparent); }
    .pill { display: inline-block; font-size: 11px; padding: 0 6px; border-radius: 999px;
      background: var(--bx-panel-2, #f7f8fa); border: 1px solid var(--bx-border, #e4e8ed);
      margin: 1px 4px 1px 0; }
    .ok { color: var(--bx-green, #43a047); }
    .warn { color: var(--bx-amber, #f2a71b); }
    .mono { font-family: var(--bx-mono, monospace); }
    table { border-collapse: collapse; width: 100%; font-size: 12px; }
    th { text-align: left; font-size: 10px; text-transform: uppercase; letter-spacing: .06em;
         color: var(--bx-muted, #8794a1); font-weight: 600; padding: 3px 8px 3px 0; }
    td { padding: 3px 8px 3px 0; border-top: 1px solid var(--bx-border, #e4e8ed);
         vertical-align: middle; }
    .search { width: 100%; box-sizing: border-box; margin-bottom: 6px; }
    .models { max-height: 260px; overflow: auto; border: 1px solid var(--bx-border, #e4e8ed);
      border-radius: 6px; }
    .models table { width: 100%; }
    .models th { position: sticky; top: 0; background: var(--bx-panel-2, #f7f8fa);
      padding-left: 8px; }
    .models td { padding-left: 8px; }
    .count { font-size: 11px; color: var(--bx-muted, #8794a1); margin-bottom: 4px; }
    .models-head { display: flex; align-items: baseline; justify-content: space-between; }
    .spin { display: inline-block; animation: spin 0.8s linear infinite; }
    @keyframes spin { to { transform: rotate(360deg); } }
  `;

  constructor() {
    super();
    this._backends = [];
    this._stats = {};
    this._aliases = {};
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
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
    this._loadStats();
    this._loadModels();
  }

  async _loadStats() {
    try { this._stats = (await api('/stats')).backends ?? {}; }
    catch { /* backend restarting; next tick */ }
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

  // Add or update a backend: name+URL via /config/backend, token straight
  // into this tile's own vault (never through kv).
  async _saveBackend(name, baseURL, token) {
    this._busy = true;
    try {
      await api('/config/backend', { method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, baseURL }) });
      if (token) {
        await vault(`api-token-${name}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ value: token }) });
      }
      await this._refresh();
    } catch (e) { this._err = String(e.message ?? e); }
    this._busy = false;
  }

  async _removeBackend(name) {
    if (!confirm(`Remove backend "${name}"? Its vault token is deleted too.`)) return;
    this._busy = true;
    try {
      await api(`/config/backend/${encodeURIComponent(name)}`, { method: 'DELETE' });
      await vault(`api-token-${name}`, { method: 'DELETE' }).catch(() => {});
      await this._refresh();
    } catch (e) { this._err = String(e.message ?? e); }
    this._busy = false;
  }

  async _setToken(name) {
    const v = prompt(`API token for backend "${name}":`);
    if (!v?.trim()) return;
    this._busy = true;
    try {
      await vault(`api-token-${name}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ value: v.trim() }) });
      await this._refresh();
    } catch (e) { this._err = String(e.message ?? e); }
    this._busy = false;
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
        ${this._err ? html`<div class="err">${this._err}</div>` : nothing}

        <h4>backends</h4>
        <table>
          <tr><th>name</th><th>base URL</th><th>token</th><th style="text-align:right">reqs</th>
              <th style="text-align:right">tok in</th><th style="text-align:right">tok out</th>
              <th style="text-align:right">active</th><th></th></tr>
          ${this._backends.map((b) => {
            const st = this._stats[b.name] ?? {};
            return html`<tr>
              <td class="mono">${b.name}</td>
              <td class="mono muted" style="max-width:220px; overflow:hidden; text-overflow:ellipsis">${b.baseURL}</td>
              <td>${b.hasToken ? html`<span class="ok">✓</span>` : html`<span class="warn">none</span>`}</td>
              <td class="mono" style="text-align:right">${fmtN(st.reqs)}</td>
              <td class="mono" style="text-align:right">${fmtN(st.tokIn)}</td>
              <td class="mono" style="text-align:right">${fmtN(st.tokOut)}</td>
              <td class="mono" style="text-align:right">${st.active
                ? html`<span class="ok">${fmtN(st.active)}</span>` : '0'}</td>
              <td style="text-align:right; white-space:nowrap">
                <button class="act" ?disabled=${this._busy} @click=${() => this._setToken(b.name)}>token</button>
                <button class="act" ?disabled=${this._busy} @click=${() => {
                  const nv = prompt(`Base URL for "${b.name}":`, b.baseURL);
                  if (nv?.trim()) this._saveBackend(b.name, nv.trim());
                }}>url</button>
                ${this._backends.length > 1 ? html`
                  <button class="act rm" ?disabled=${this._busy} @click=${() => this._removeBackend(b.name)}>del</button>` : nothing}
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
          <div class="muted" style="font-size:11px; margin-top:4px">
            With several backends, model ids are namespaced
            <span class="mono">&lt;backend&gt;/&lt;model&gt;</span> — requests route by that prefix.
          </div>` : nothing}

        <div class="models-head">
          <h4>models ${models.length ? html`<span class="count">(${models.length}${q ? ` of ${this._models.length}` : ''})</span>` : nothing}</h4>
          <button class="act" ?disabled=${this._modelsLoading} @click=${() => this._loadModels()}>
            <span class=${this._modelsLoading ? 'spin' : ''}>⟳</span> refresh
          </button>
        </div>
        ${this._modelsErr ? html`<div class="err">${this._modelsErr}</div>` : nothing}
        <input class="search" type="search" placeholder="search models…"
          .value=${this._search} @input=${(e) => { this._search = e.target.value; }}>
        <div class="models">
          <table>
            <tr><th>id</th><th>owner</th><th></th></tr>
            ${models.length ? models.map((m) => html`<tr>
              <td class="mono">${m.id}</td>
              <td class="muted">${m.owned_by ?? ''}</td>
              <td style="text-align:right"><button class="act" @click=${() => this._addAlias(m.id)}>+ alias</button></td>
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
              <button class="act" @click=${() => this._editAlias(alias, target)}>edit</button>
              <button class="act rm" @click=${() => this._removeAlias(alias)}>del</button>
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
