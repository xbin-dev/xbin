/**
 * <bx-llm-admin .fetcher> — a workspace admin's view of xbind's LLM
 * connector (D226, docs/llm.md), the same element on the /xbin/llm page
 * and in the admin console's workspace → LLM tab:
 *
 *   status      configured or not, on or off, the vault, what can't work
 *               as written; the connector's address and binding ref
 *   providers   <bx-llm-upstreams>: add / edit / remove, masked keys, Test
 *   models      the default model, each role's (agent, harness, your own),
 *               and aliases — one Save, sending only what differs
 *   people      who may call directly: admins, everyone, nobody
 *   quotas      <bx-llm-quotas>: workspace, default, per person, per org
 *   usage       <bx-llm-usage>: per person and per component
 *
 * Every setting the host's deployment file sets (GET /llm/config's `locks`)
 * is drawn read-only with "Set by your host" and its controls disabled; a
 * connector the host locks entirely says so at the top. xbind refuses a
 * write to a locked setting whatever this page shows.
 *
 * `.fetcher`: plain fetch by default (the person's session), the admin
 * console's xbin.fetch there. Emits `llm-denied` when the configuration
 * isn't the viewer's to read (the page then shows a person's own view),
 * `llm-absent` on an xbind without the connector.
 */
import { LitElement, html, css, nothing } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import { limitsCss } from '/vendor/bx-limits.js';
import { llmCss, lockTpl, errTpl } from '/vendor/llm-css.js';
import { llmClient, directory } from '/vendor/llm-client.js';
import '/vendor/llm-upstreams.js';
import '/vendor/llm-quotas.js';
import '/vendor/llm-usage.js';
import { ROLES, PEOPLE, lockOf, fullyLocked, statusOf, vaultWords, modelChoices, modelsDraft, modelsChanges } from '/vendor/llm-kit.js';

export class BxLlmAdmin extends LitElement {
  static properties = {
    fetcher: { attribute: false },
    _cfg: { state: true },     // GET /llm/config
    _models: { state: true },  // the model names a default may take
    _dir: { state: true },     // {people, orgs}: ids the quota overrides offer
    _md: { state: true },      // the models form's draft (modelsDraft); null: as saved
    _remind: { state: true },  // admins are being reminded to set the connector up (the llm-setup alert)
    _err: { state: true }, _merr: { state: true }, _busy: { state: true }, _saved: { state: true },
  };
  static styles = [limitsCss, llmCss, css`
    .state { display: flex; gap: 8px; align-items: center; font-weight: 600; margin: 4px 0 8px; }
    .kv { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 4px 12px; margin: 8px 0; }
    .kv > .v { overflow-wrap: anywhere; }
    .rows { display: grid; grid-template-columns: minmax(9em, max-content) minmax(0, 26em) max-content; gap: 6px 12px; align-items: center; }
    .rows input { width: 100%; }
    .rows .lab { display: flex; flex-direction: column; }
    ul.plain { margin: 4px 0 0; padding-left: 18px; }
    @media (max-width: 720px) { .rows { grid-template-columns: minmax(0, 1fr); } }
  `];

  constructor() { super(); this._cfg = null; this._models = []; this._dir = { people: [], orgs: [] }; this._md = null; this._err = ''; this._merr = ''; this._saved = ''; }
  get _llm() { return this._c ??= llmClient(this.fetcher); }

  connectedCallback() { super.connectedCallback(); this.reload(); }
  async reload() {
    const r = await this._llm.config();
    if (!r.ok) {
      this._err = r.error;
      if (r.status === 403 || r.status === 401) this.dispatchEvent(new CustomEvent('llm-denied', { bubbles: true, composed: true }));
      if (r.status === 404) this.dispatchEvent(new CustomEvent('llm-absent', { bubbles: true, composed: true }));
      return;
    }
    this._take(r.data);
    directory(this.fetcher).then((d) => { this._dir = d; });
  }
  // a write's answer is the configuration in force
  _take(cfg) {
    this._cfg = cfg ?? {}; this._err = '';
    this._llm.models().then((m) => { this._models = modelChoices(m.ok ? m.data : null, this._cfg); });
    if (this._cfg.configured) this._remind = false; else this._llm.setupPending().then((on) => { this._remind = on; });
    this.dispatchEvent(new CustomEvent('llm-loaded', { detail: this._cfg }));
  }
  async _put(changes, what) {
    this._busy = true; this._saved = '';
    const r = await this._llm.putConfig(changes);
    this._busy = false;
    if (!r.ok) return r.error;
    this._take(r.data); this._saved = what;
    return '';
  }
  async _switch(el, changes, what, undo) {
    const err = await this._put(changes, what);
    if (err) { this._err = err; undo(el); }
  }
  async _saveModels() {
    const { changes, error } = modelsChanges(this._md, this._cfg);
    if (error) { this._merr = error; return; }
    if (!changes) { this._md = null; this._merr = ''; return; }
    this._merr = await this._put(changes, 'models');
    if (!this._merr) this._md = null;
  }

  _notes(cfg) {
    const all = fullyLocked(cfg), vault = vaultWords(cfg.vault);
    return html`
      ${all ? html`<div class="note" data-locked-all><bx-icon name="lock"></bx-icon><span><b>Set by your host.</b> Your host’s deployment file configures
        this workspace’s LLM connector: every setting below is read-only. Usage is yours to read.</span></div>`
        : cfg.host?.active && Object.keys(cfg.locks ?? {}).length ? html`<div class="note" data-locked-some><bx-icon name="lock"></bx-icon><span>Your host’s deployment file
          sets part of this: those settings are marked <b>Set by your host</b> and can’t be changed here. The rest is yours.</span></div>` : nothing}
      ${cfg.host?.error ? html`<div class="note warn" data-host-error><bx-icon name="warning"></bx-icon><span>${cfg.host.error}</span></div>` : nothing}
      ${cfg.storeError ? html`<div class="note crit"><bx-icon name="error"></bx-icon><span>The settings saved here can’t be read and are not in force (nothing is written over them): ${cfg.storeError}</span></div>` : nothing}
      ${vault && (cfg.upstreams ?? []).length ? html`<div class="note warn" data-vault=${cfg.vault}><bx-icon name="lock"></bx-icon><span>${vault}</span></div>` : nothing}
      ${(cfg.problems ?? []).length ? html`<div class="note warn" data-problems><bx-icon name="warning"></bx-icon><span>These can’t work as written:
        <ul class="plain">${cfg.problems.map((p) => html`<li>${p}</li>`)}</ul></span></div>` : nothing}`;
  }

  _status(cfg) {
    const st = statusOf(cfg), lock = lockOf(cfg.locks, 'enabled');
    return html`<h3>Status</h3>
      <div class="state" data-status=${st.state}><bx-icon class=${st.cls === 'on' ? 'ok' : st.cls === 'warn' ? 'warnc' : 'muted'} name=${st.icon}></bx-icon><span>${st.words}</span></div>
      <label class="sw"><input type="checkbox" data-enabled .checked=${!!cfg.enabled} ?disabled=${lock.locked || this._busy}
        @change=${(e) => this._switch(e.target, { enabled: e.target.checked }, 'enabled', (el) => { el.checked = !!this._cfg.enabled; })}>
        <span><b>Connector on</b> <span class="muted">— off refuses every call, whoever makes it</span> ${lockTpl(lock)}</span></label>
      ${this._remind ? html`<div class="line" data-remind><span class="muted">Workspace admins see a reminder to set this up until a provider exists.</span>
        <button class="small quiet" data-remind-off ?disabled=${this._busy} title="leave the connector off: no more reminder, for any admin"
          @click=${async () => { const r = await this._llm.dismissSetup(); if (r.ok) this._remind = false; else this._err = r.error; }}>Stop reminding</button></div>` : nothing}
      <div class="kv">
        <span class="k">endpoint</span><span class="v mono" data-endpoint>${location.origin}/api/xbin/llm/v1</span>
        <span class="k">tiles bind</span><span class="v"><span class="mono">xbin:llm</span> <span class="muted">— an <code>openai</code> slot bound to it lets a tile call
          (binding → binding in the admin console, or <code>bx bind &lt;tile&gt; llm=xbin:llm</code>)</span></span>
      </div>`;
  }

  _modelsTpl(cfg) {
    const d = this._md ?? modelsDraft(cfg), dirty = !!this._md, locks = cfg.locks;
    const set = (patch) => { this._md = { ...d, ...patch }; this._saved = ''; };
    const known = Object.fromEntries(ROLES.map((r) => [r.id, r]));
    const field = (value, lock, onInput, attrs) => html`<input list="llm-models" .value=${value} ?disabled=${lock.locked || this._busy} placeholder="provider/model"
      autocomplete="off" spellcheck="false" data-model=${attrs} @input=${(e) => onInput(e.target.value)}>`;
    // a row is [name, model, added?]: one added here keeps its name field until it is saved
    const roleRow = ([role, model, added], i) => {
      const lock = lockOf(locks, `defaults.roles.${role}`), saved = !added;
      const upd = (patch) => set({ roles: d.roles.map((r, j) => (j === i ? [patch.role ?? r[0], patch.model ?? r[1], r[2]] : r)) });
      return html`
        <span class="lab">${saved ? html`<span>${known[role]?.label ?? role}</span><span class="meta mono">xbin/${role}</span>`
          : html`<input aria-label="role name" data-role-name .value=${role} placeholder="role" spellcheck="false" @input=${(e) => upd({ role: e.target.value })}>`}</span>
        ${field(model, lock, (v) => upd({ model: v }), `role:${role}`)}
        <span>${lockTpl(lock)}</span>`;
    };
    const aliasRow = ([alias, target, added], i) => {
      const lock = lockOf(locks, `aliases.${alias}`), saved = !added;
      const upd = (patch) => set({ aliases: d.aliases.map((r, j) => (j === i ? [patch.alias ?? r[0], patch.target ?? r[1], r[2]] : r)) });
      return html`
        <span class="lab">${saved ? html`<span class="mono">${alias}</span>${alias === '*' ? html`<span class="meta">every unknown name</span>` : nothing}`
          : html`<input aria-label="alias" data-alias-name .value=${alias} placeholder="the name a client sends, or *" spellcheck="false" @input=${(e) => upd({ alias: e.target.value })}>`}</span>
        ${field(target, lock, (v) => upd({ target: v }), `alias:${alias}`)}
        <span>${lock.locked ? lockTpl(lock) : html`<button type="button" class="quiet small" title="remove this alias" aria-label="remove this alias"
          @click=${() => set({ aliases: d.aliases.filter((_, j) => j !== i) })}><bx-icon name="xmark"></bx-icon></button>`}</span>`;
    };
    const dl = lockOf(locks, 'defaults.model'), all = fullyLocked(cfg);
    return html`<h3>Models</h3>
      <div class="hint">A model is named <code>provider/model</code>. A call that names none gets the default; <code>xbin/agent</code> and
        <code>xbin/harness</code> get their role’s model, or the default. An unknown name is refused, never mapped to the default — unless an alias says so.</div>
      <datalist id="llm-models">${this._models.map((m) => html`<option value=${m}></option>`)}</datalist>
      <form data-models @submit=${(e) => { e.preventDefault(); this._saveModels(); }}>
        <div class="rows">
          <span class="lab"><span>default</span><span class="meta mono">xbin/default</span></span>
          ${field(d.model, dl, (v) => set({ model: v }), 'default')}<span>${lockTpl(dl)}</span>
          ${d.roles.map(roleRow)}
        </div>
        <h4>Aliases</h4>
        <div class="hint">Another name for a model — what a coding tool sends on its own (<code>gpt-6-astra</code> → <code>openai/gpt-5</code>);
          <code>*</code> catches every unknown name.</div>
        ${d.aliases.length ? html`<div class="rows" data-aliases>${d.aliases.map(aliasRow)}</div>` : nothing}
        ${errTpl(this._merr)}
        <div class="line">
          <button class="primary" type="submit" data-save-models ?disabled=${!dirty || this._busy}>Save models</button>
          ${dirty ? html`<button type="button" class="quiet" @click=${() => { this._md = null; this._merr = ''; }}>Cancel</button>` : nothing}
          <button type="button" class="small" data-add-role ?disabled=${all} @click=${() => set({ roles: [...d.roles, ['', '', true]] })}>+ role</button>
          <button type="button" class="small" data-add-alias ?disabled=${all} @click=${() => set({ aliases: [...d.aliases, ['', '', true]] })}>+ alias</button>
          ${this._saved === 'models' ? html`<span class="ok" data-saved="models"><bx-icon name="ok"></bx-icon> saved</span>` : nothing}
        </div>
      </form>`;
  }

  _people(cfg) {
    const lock = lockOf(cfg.locks, 'people');
    return html`<h3>Who may call directly ${lockTpl(lock)}</h3>
      <div class="hint">A person calling the endpoint with their own sign-in — a browser, the app, <code>bx</code>. Tiles are not affected: a tile
        calls by its binding, and its calls are metered to whoever it acts for.</div>
      <div role="radiogroup" aria-label="who may call directly" data-people=${cfg.people}>
        ${PEOPLE.map((p) => html`<label class="sw"><input type="radio" name="people" value=${p.id} .checked=${cfg.people === p.id} ?disabled=${lock.locked || this._busy}
          @change=${(e) => this._switch(e.target, { people: p.id }, 'people', () => {
            for (const r of this.renderRoot.querySelectorAll('input[name=people]')) r.checked = r.value === this._cfg.people; })}>
          <span><b>${p.label}</b> <span class="muted">— ${p.hint}</span></span></label>`)}
      </div>`;
  }

  render() {
    const cfg = this._cfg;
    if (!cfg) return this._err ? errTpl(this._err) : html`<div class="empty">…</div>`;
    return html`<div data-llm-admin @llm-config=${(e) => this._take(e.detail)}>
      ${this._notes(cfg)}
      ${errTpl(this._err)}
      ${this._status(cfg)}
      <h3>Providers</h3>
      <div class="hint">The model providers this workspace calls: any OpenAI-compatible API. Their addresses come only from here — never from a request.</div>
      <bx-llm-upstreams .cfg=${cfg} .client=${this._llm}></bx-llm-upstreams>
      ${this._modelsTpl(cfg)}
      ${this._people(cfg)}
      <h3>Quotas</h3>
      <div class="hint">Tokens (input plus output) and cost per UTC day and month. Reaching a limit stops that person’s calls until it resets, or only
        warns; either way the workspace’s admins get an alert. <code>unlimited</code> lifts a limit a broader quota sets.</div>
      <bx-llm-quotas .client=${this._llm} .people=${this._dir.people} .orgs=${this._dir.orgs}></bx-llm-quotas>
      <h3>Usage</h3>
      <bx-llm-usage .client=${this._llm}></bx-llm-usage>
    </div>`;
  }

  testApi() {
    const s = this;
    return {
      get config() { return s._cfg; },
      get error() { return s._err || s._merr || ''; },
      get models() { return s._models; },
      reload: () => s.reload(),
    };
  }
}
customElements.define('bx-llm-admin', BxLlmAdmin);
