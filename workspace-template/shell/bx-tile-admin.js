/**
 * <bx-tile-admin> — the tile-scoped mini admin panel the shell pops from a
 * card's title bar (admins, the tile's USER-OWNER, and the owning org's
 * admins). One tile's slice of the admin console: access (users/orgs, D24/
 * D31), lifecycle, runtime info, vault keys, roles/grants, interface
 * bindings, backups, and cron registrations — each a fold-out section,
 * loaded lazily and degrading independently (an owner/org admin gets the
 * access + lifecycle sections; workspace-admin-only sections say so).
 *
 * Runs in the ROOT page (workspace chrome), so it uses RAW fetch on purpose:
 * the cookie principal is the signed-in human, whereas xbin.fetch would
 * attach the chrome frame token and downgrade to a non-admin element
 * principal (docs/auth.md). This is exactly why org-admin delegation lives
 * HERE and not in the admin tile: granting a non-workspace-admin the admin
 * TILE would hand them its frame token and thereby the tile's own xbin
 * capabilities — chrome surfaces act as the signed-in human instead. The
 * shell gates visibility via /whoami; every endpoint 403s the unauthorized
 * anyway.
 */
import { LitElement, html, css, nothing } from 'lit';
import '/vendor/bx-multiselect.js';

const api = async (path, opts) => {
  const r = await fetch(`/api/xbin${path}`, opts);
  const text = await r.text();
  let data; try { data = text ? JSON.parse(text) : null; } catch { data = text; }
  if (!r.ok) throw new Error(data?.error ?? r.status);
  return data;
};
const jbody = (v) => ({ headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(v) });

export class BxTileAdmin extends LitElement {
  static properties = {
    path: { type: String },
    _ov: { state: true },       // this tile's /auth-overview slice (state, roles, uses)
    _grants: { state: true },   // {grants, pending} filtered to this tile
    _binds: { state: true },    // /bindings (full — options need all providers)
    _rt: { state: true },       // this tile's /runtime backend entry (lazy)
    _vault: { state: true },    // vault key names
    _backups: { state: true },  // versions
    _cron: { state: true },     // this tile's cron jobs
    _access: { state: true },   // the tile's ACL view (/access — owner + user/org entries)
    _dir: { state: true },      // /users-directory for the add-entry picker
    _accKind: { state: true },  // add-entry kind: user | org
    _secEdit: { state: true },  // vault key being re-set inline (name | null)
    _err: { state: true },
    _busy: { state: true },
  };

  static styles = css`
    :host {
      display: block; font: var(--bx-font, 12.5px/1.45 system-ui, sans-serif);
      color: var(--bx-text, #33414e);
    }
    .hd { display: flex; align-items: baseline; gap: 8px; padding: 8px 10px 6px;
      border-bottom: 1px solid var(--bx-border, #e4e8ed); }
    .hd .t { font-family: var(--bx-mono, monospace); font-size: 12px; font-weight: 700;
      overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .hd .st { margin-left: auto; }
    .err { color: var(--bx-red, #e5484d); font-size: 11px; padding: 4px 10px; }
    details { border-bottom: 1px solid var(--bx-border, #e4e8ed); }
    details:last-child { border-bottom: 0; }
    summary { cursor: pointer; user-select: none; list-style-position: inside;
      padding: 6px 10px; font-size: 10.5px; font-weight: 600; letter-spacing: .07em;
      text-transform: uppercase; color: var(--bx-muted, #8794a1); }
    summary:hover { background: var(--bx-panel-2, #f7f8fa); }
    .sec { padding: 2px 10px 10px; }
    .pill { display: inline-block; font-size: 10.5px; padding: 0 6px; border-radius: 999px;
      background: var(--bx-panel-2, #f7f8fa); border: 1px solid var(--bx-border, #e4e8ed);
      margin: 1px 3px 1px 0; }
    .pill.on { color: var(--bx-green, #43a047); border-color: color-mix(in srgb, var(--bx-green, #43a047) 45%, transparent); }
    .pill.off { color: var(--bx-red, #e5484d); border-color: color-mix(in srgb, var(--bx-red, #e5484d) 45%, transparent); }
    .mono { font-family: var(--bx-mono, monospace); }
    .muted { color: var(--bx-muted, #8794a1); }
    table { border-collapse: collapse; width: 100%; font-size: 11.5px; }
    td { padding: 2px 6px 2px 0; border-top: 1px solid var(--bx-border, #e4e8ed); vertical-align: middle; }
    tr:first-child td { border-top: 0; }
    button.act { border: 1px solid var(--bx-border, #e4e8ed); background: var(--bx-panel, #fff);
      color: var(--bx-text, #33414e); border-radius: 5px; font: inherit; font-size: 10.5px;
      padding: 1px 7px; cursor: pointer; }
    button.act:hover { background: var(--bx-panel-2, #f7f8fa); }
    button.act:disabled { opacity: .5; cursor: default; }
    button.go { color: var(--bx-green, #43a047); }
    button.rm { color: var(--bx-red, #e5484d); }
    input, select { font: inherit; font-size: 11px; padding: 2px 6px; max-width: 100%;
      border: 1px solid var(--bx-border, #e4e8ed); border-radius: 5px;
      background: var(--bx-panel, #fff); color: var(--bx-text, #33414e); }
    .row { display: flex; gap: 5px; align-items: center; flex-wrap: wrap; margin-top: 6px; }
    .kv { display: grid; grid-template-columns: auto 1fr; gap: 1px 10px; font-size: 11.5px; }
    .kv .k { color: var(--bx-muted, #8794a1); }
  `;

  constructor() {
    super();
    this._err = '';
  }

  connectedCallback() {
    super.connectedCallback();
    this._loadCore();
  }

  // The light aggregate everything except /runtime needs (that one walks
  // every backend's /proc + cgroup, so it loads only when its section opens).
  async _loadCore() {
    // Sections degrade independently: an ORG admin (docs/auth.md) may open
    // this panel on their org's tiles — /access works for them while the
    // workspace-admin-only aggregates 403 and render as "workspace-admin
    // only" rows instead of blanking the whole panel.
    try {
      const forbidden = () => ({ forbidden: true });
      const [ov, grants, binds, cron, backups, vault, access] = await Promise.all([
        api('/auth-overview').catch(forbidden),
        api('/grants').catch(forbidden),
        api('/bindings').catch(forbidden),
        api('/cron/jobs').catch(() => ({ jobs: [] })),
        api(`/backups?component=${encodeURIComponent(this.path)}`).catch(() => null),
        api(`/vault/${this.path}`).catch((e) => ({ err: String(e.message ?? e) })),
        api(`/access?tile=${encodeURIComponent(this.path)}`).catch((e) => ({ err: String(e.message ?? e) })),
      ]);
      // Picker for the access section (best-effort; owners/org admins may fetch).
      api('/users-directory').then((d) => { this._dir = d.users ?? []; }).catch(() => {});
      this._ov = ov.forbidden ? { forbidden: true }
        : ((ov.components ?? []).find((c) => c.path === this.path) ?? {});
      const mine = (g) => g.from === this.path || g.target === this.path ||
        g.target.startsWith(this.path + ':') || g.target.startsWith('res:' + this.path + '/');
      this._grants = grants.forbidden ? grants : {
        grants: (grants.grants ?? []).filter(mine),
        pending: (grants.pending ?? []).filter(mine),
      };
      this._binds = binds;
      this._cron = (cron.jobs ?? []).filter((j) => j.component === this.path);
      this._backups = backups?.versions ?? [];
      this._vault = vault;
      this._access = access;
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
  }

  async _loadRuntime() {
    if (this._rt) return;
    try {
      const rt = await api('/runtime');
      this._rt = (rt.backends ?? []).find((b) => b.path === this.path) ?? { none: true };
    } catch (e) { this._rt = { err: String(e.message ?? e) }; }
  }

  async _do(fn) {
    this._busy = true;
    try { await fn(); this._err = ''; } catch (e) { this._err = String(e.message ?? e); }
    this._busy = false;
    await this._loadCore();
  }

  // ---- sections ----

  _lifecycle() {
    if (this._ov?.forbidden) return html`<div class="sec muted">workspace-admin only</div>`;
    const st = this._ov?.state ?? 'enabled';
    const set = (state) => this._do(() => api('/lifecycle', { method: 'POST', ...jbody({ component: this.path, state }) }));
    return html`<div class="sec">
      <span class="pill ${st === 'enabled' ? 'on' : 'off'}">${st}</span>
      <span class="row">
        ${st !== 'enabled' ? html`<button class="act go" ?disabled=${this._busy}
          @click=${() => set('enabled')}>${st === 'hidden' ? 'unhide' : 'enable'}</button>` : nothing}
        ${st === 'enabled' ? html`<button class="act rm" ?disabled=${this._busy}
          @click=${() => confirm(`Disable ${this.path}? Its backend stops now.`) && set('disabled')}>disable</button>` : nothing}
        ${st !== 'hidden' && st !== 'offloaded' && st !== 'offloaded-full' ? html`<button class="act rm" ?disabled=${this._busy}
          title="disabled + removed from sidebars until unhidden (D42)"
          @click=${() => confirm(`Hide ${this.path}? It is disabled and drops out of sidebars until unhidden.`) && set('hidden')}>hide</button>` : nothing}
        <span class="muted" style="font-size:10.5px">offload lives in the admin tile</span>
      </span></div>`;
  }

  _runtime() {
    const rt = this._rt;
    if (!rt) return html`<div class="sec muted">…</div>`;
    if (rt.err) return html`<div class="sec err">${rt.err}</div>`;
    if (rt.none) return html`<div class="sec muted">no backend (static tile, or not spawned)</div>`;
    const act = rt.activity ?? {};
    return html`<div class="sec kv">
      <span class="k">state</span><span class="mono">${rt.state} (gen ${rt.gen ?? '—'}${rt.restarts ? `, ${rt.restarts} restarts` : ''})</span>
      <span class="k">pid</span><span class="mono">${rt.pid ?? '—'}${rt.isolated ? ' · sandboxed' : ''}</span>
      <span class="k">uptime</span><span class="mono">${rt.uptimeSec != null ? Math.floor(rt.uptimeSec / 60) + ' min' : '—'}</span>
      <span class="k">memory</span><span class="mono">${rt.rssKb != null ? (rt.rssKb / 1024).toFixed(1) + ' MB' : '—'}</span>
      <span class="k">cpu</span><span class="mono">${rt.cpuSec != null ? rt.cpuSec.toFixed(1) + ' s' : '—'}</span>
      <span class="k">conns</span><span class="mono">${rt.activeConns ?? 0} active</span>
      <span class="k">egress</span><span class="mono">${act.allowed ?? 0} allowed · ${act.denied ?? 0} denied</span>
    </div>`;
  }

  _vaultSec() {
    const v = this._vault;
    if (v?.err) return html`<div class="sec err">${v.err}</div>`;
    const keys = v?.keys ?? [];
    return html`<div class="sec">
      <table>${keys.length ? keys.map((k) => html`<tr>
          <td class="mono">${k}</td>
          <td class="mono muted">${this._secEdit === k ? html`
            <form style="display:inline-flex; gap:4px" @submit=${(e) => { e.preventDefault();
                const nv = e.target.nv.value;
                this._secEdit = null;
                if (nv) this._do(() => api(`/vault/${this.path}/${encodeURIComponent(k)}`, { method: 'PUT', ...jbody({ value: nv }) })); }}>
              <input name="nv" type="password" size="14" placeholder="new value (write-only)" autofocus>
              <button class="act" type="submit">save</button>
              <button class="act" type="button" @click=${() => { this._secEdit = null; }}>cancel</button>
            </form>` : '••••••'}</td>
          <td style="text-align:right; white-space:nowrap">
            ${this._secEdit === k ? nothing : html`<button class="act" @click=${() => { this._secEdit = k; }}>set</button>`}
            <button class="act rm" @click=${() => confirm(`Delete secret ${k}?`) &&
              this._do(() => api(`/vault/${this.path}/${encodeURIComponent(k)}`, { method: 'DELETE' }))}>✕</button>
          </td></tr>`) : html`<tr><td class="muted">no secrets</td></tr>`}</table>
      <form class="row" @submit=${(e) => { e.preventDefault(); const f = e.target;
          if (!f.k.value.trim()) return;
          this._do(() => api(`/vault/${this.path}/${encodeURIComponent(f.k.value.trim())}`,
            { method: 'PUT', ...jbody({ value: f.v.value }) }));
          f.reset(); }}>
        <input name="k" placeholder="key" size="9">
        <input name="v" placeholder="value" type="password" size="12">
        <button class="act go">set</button>
      </form></div>`;
  }

  // ---- access: the tile's ACL (docs/auth.md, D24/D31) ----
  // Exact entries are editable and AUTHORITATIVE for their user on this tile
  // (they override org membership, patterns and defaults; `none` excludes);
  // pattern rows are provenance-only (edited on the user/org object). The
  // tile's owner and org admins can use this section even when the
  // workspace-admin sections 403.
  _accessSec() {
    const a = this._access;
    if (!a) return html`<div class="sec muted">…</div>`;
    if (a.err) return html`<div class="sec err">${a.err}</div>`;
    const entries = a.entries ?? [];
    const setEntry = (kind, id, level) => this._do(() =>
      api('/access', { method: 'PUT', ...jbody({ tile: this.path, kind, id, level }) }));
    const levelsFor = (kind) => kind === 'user'
      ? ['read', 'write', 'terminal', 'none'] : ['read', 'write', 'terminal'];
    return html`<div class="sec">
      <div style="margin-bottom:4px" class="muted">
        owner: <span class="mono">${a.owner || 'workspace'}</span>
      </div>
      <table>
        ${entries.map((e) => html`<tr>
          <td><span class="pill">${e.kind === 'org' ? '🏢' : '👤'} ${e.kind}</span> <span class="mono">${e.id}</span></td>
          <td>${e.source === 'exact'
            ? html`<select ?disabled=${this._busy} @change=${(ev) => setEntry(e.kind, e.id, ev.target.value)}>
                ${levelsFor(e.kind).map((l) => html`<option value=${l} ?selected=${e.level === l}>${l === 'none' ? 'none (exclude)' : l}</option>`)}
              </select>`
            : html`<span class="pill">${e.level}</span>`}</td>
          <td class="muted" style="font-size:10px">${e.source}</td>
          <td style="text-align:right">${e.source === 'exact'
            ? html`<button class="act rm" title="remove this entry" ?disabled=${this._busy}
                @click=${() => setEntry(e.kind, e.id, '')}>✕</button>`
            : nothing}</td>
        </tr>`)}
        ${!entries.length ? html`<tr><td class="muted" colspan="4">no entries — owner/admins only</td></tr>` : nothing}
      </table>
      <form class="row" @submit=${(e) => {
        e.preventDefault(); const f = e.target;
        const id = f.who.value.trim(); if (!id) return;
        setEntry(this._accKind === 'org' ? 'org' : 'user', id, f.level.value); f.who.value = '';
      }}>
        <select @change=${(e) => { this._accKind = e.target.value; }}>
          <option value="user" ?selected=${this._accKind !== 'org'}>user</option>
          <option value="org" ?selected=${this._accKind === 'org'}>org</option>
        </select>
        <input name="who" list="acc-people" placeholder=${this._accKind === 'org' ? 'org id' : 'user id'} size="14">
        <datalist id="acc-people">
          ${this._accKind === 'org' ? nothing
            : (this._dir ?? []).map((u) => html`<option value=${u.id}>${u.name && u.name !== u.id ? u.name : ''}</option>`)}
        </datalist>
        <select name="level">
          <option>read</option><option selected>write</option><option>terminal</option>
          ${this._accKind !== 'org' ? html`<option value="none">none (exclude)</option>` : nothing}
        </select>
        <button class="act go" ?disabled=${this._busy}>add</button>
      </form>
      <div class="muted" style="font-size:10px; margin-top:4px">
        read = see the tile · write = use/edit it · terminal = a root shell on it.
        An exact user entry is authoritative — it overrides org membership,
        patterns and defaults; <i>none</i> excludes outright (D31). Pattern
        rows are edited on the user/org (admin tile or bx).
      </div>
    </div>`;
  }

  _grantsSec() {
    const g = this._grants ?? { grants: [], pending: [] };
    if (g.forbidden) return html`<div class="sec muted">workspace-admin only</div>`;
    const roles = this._ov?.roles ? Object.keys(this._ov.roles) : [];
    return html`<div class="sec">
      ${roles.length ? html`<div style="margin-bottom:4px">exposes:
        ${roles.map((r) => html`<span class="pill">${r}</span>`)}</div>` : nothing}
      <table>
        ${g.pending.map((p) => html`<tr style=${p.blocked ? 'opacity:.55' : ''}>
          <td class="mono" style="font-size:10.5px" title=${p.blocked ?? ''}>${p.from} → ${p.target}</td>
          <td><span class="pill">${p.role}</span></td>
          <td style="text-align:right">${p.blocked
            ? html`<button class="act" disabled title=${p.blocked}>blocked</button>`
            : html`<button class="act go" ?disabled=${this._busy}
                @click=${() => this._do(() => api('/grants', { method: 'POST', ...jbody({ from: p.from, target: p.target, role: p.role }) }))}>approve</button>`}</td>
        </tr>`)}
        ${g.grants.map((p) => html`<tr>
          <td class="mono" style="font-size:10.5px">${p.from} → ${p.target}</td>
          <td><span class="pill">${p.role}</span></td>
          <td style="text-align:right"><button class="act rm" ?disabled=${this._busy}
            @click=${() => this._do(() => api('/grants', { method: 'DELETE', ...jbody({ from: p.from, target: p.target, role: p.role }) }))}>revoke</button></td>
        </tr>`)}
        ${!g.grants.length && !g.pending.length ? html`<tr><td class="muted">no grants involve this tile</td></tr>` : nothing}
      </table></div>`;
  }

  _bindsSec() {
    const d = this._binds;
    if (!d) return html`<div class="sec muted">…</div>`;
    if (d.forbidden) return html`<div class="sec muted">workspace-admin only</div>`;
    const me = (d.components ?? []).find((c) => c.component === this.path);
    const slots = Object.entries(me?.interfaces ?? {});
    const provides = Object.entries(me?.provides ?? {});
    const instances = d.instances ?? {};
    // Options: same kind/service/own filter as the admin Interfaces tab.
    const optsFor = (def) => {
      const out = [];
      if (def.kind === 'net') out.push('internet', 'host');
      for (const c of d.components ?? []) {
        if (c.component === this.path) continue;
        for (const p of Object.values(c.provides ?? {})) {
          if (p.kind !== def.kind) continue;
          if (def.kind === 'http' && def.service && p.service !== def.service) continue;
          if (p.instances) {
            for (const id of Object.keys(instances[c.component] ?? {}).sort()) out.push(`${c.component}#${id}`);
          } else {
            out.push(c.component);
          }
        }
      }
      return out;
    };
    const set = (slot, providers) => this._do(() => api('/bindings', {
      method: providers.length ? 'POST' : 'DELETE',
      ...jbody(providers.length ? { component: this.path, slot, providers } : { component: this.path, slot }),
    }));
    return html`<div class="sec">
      <table>
        ${slots.map(([slot, def]) => {
          const bound = [].concat(d.bindings?.[this.path]?.[slot] ?? []);
          const opts = optsFor(def);
          return html`<tr>
            <td>${slot} <span class="pill">${def.kind}${def.service ? ':' + def.service : ''}${def.multi ? ' ×N' : ''}</span></td>
            <td style="text-align:right">${def.multi
              ? html`<bx-multiselect .options=${opts} .selected=${bound} placeholder="— unbound —"
                  @change=${(e) => set(slot, e.detail.selected)}></bx-multiselect>`
              : html`<select @change=${(e) => set(slot, e.target.value ? [e.target.value] : [])}>
                  <option value="" ?selected=${!bound.length}>— unbound —</option>
                  ${opts.map((p) => html`<option value=${p} ?selected=${bound[0] === p}>${p}</option>`)}
                </select>`}</td></tr>`;
        })}
        ${!slots.length ? html`<tr><td class="muted">no interface slots requested</td></tr>` : nothing}
      </table>
      ${provides.length ? html`<div style="margin-top:5px" class="muted">provides:
        ${provides.map(([slot, def]) => html`<span class="pill">${slot}: ${def.kind}${def.service ? ':' + def.service : ''}</span>`)}
        ${provides.some(([, def]) => def.instances) ? html`<div>
          ${Object.keys(instances[this.path] ?? {}).sort().map((id) => html`<span class="pill mono">#${id}</span>`)}
        </div>` : nothing}</div>` : nothing}
    </div>`;
  }

  _backupSec() {
    const vs = this._backups ?? [];
    return html`<div class="sec">
      <div class="row" style="margin-top:0">
        <button class="act go" ?disabled=${this._busy}
          @click=${() => this._do(() => api('/backup', { method: 'POST', ...jbody({ component: this.path }) }))}>backup now</button>
        <span class="muted" style="font-size:10.5px">needs an @archive binding</span>
      </div>
      <table style="margin-top:5px">
        ${vs.slice(0, 6).map((v) => html`<tr>
          <td class="mono" style="font-size:10.5px">${v.version}</td>
          <td class="muted">${v.size ? (v.size / 1048576).toFixed(1) + ' MB' : ''}</td>
          <td style="text-align:right"><button class="act" ?disabled=${this._busy}
            @click=${() => confirm(`Restore ${this.path} @ ${v.version}? Current state is replaced.`) &&
              this._do(() => api('/restore', { method: 'POST', ...jbody({ component: this.path, version: v.version }) }))}>restore</button></td>
        </tr>`)}
        ${!vs.length ? html`<tr><td class="muted">no archived versions</td></tr>` : nothing}
      </table></div>`;
  }

  _cronSec() {
    const jobs = this._cron ?? [];
    return html`<div class="sec">
      <table>
        ${jobs.map((j) => html`<tr>
          <td class="mono">${j.name}</td>
          <td class="mono muted">${j.schedule}</td>
          <td><span class="pill">${j.role || 'reader'}</span></td>
          <td style="text-align:right"><button class="act rm" ?disabled=${this._busy}
            @click=${() => confirm(`Unregister cron job ${j.name}?`) &&
              this._do(() => api(`/cron/jobs/${encodeURIComponent(j.name)}?component=${encodeURIComponent(this.path)}`, { method: 'DELETE' }))}>✕</button></td>
        </tr>`)}
        ${!jobs.length ? html`<tr><td class="muted">no cron registrations</td></tr>` : nothing}
      </table></div>`;
  }

  render() {
    const st = this._ov?.state ?? 'enabled';
    return html`
      <div class="hd">
        <span class="t">${this.path}</span>
        ${this._ov?.forbidden ? nothing
          : html`<span class="st pill ${st === 'enabled' ? 'on' : 'off'}">${st}</span>`}
        <button class="act" title="reload" @click=${() => { this._rt = null; this._loadCore(); }}>⟳</button>
      </div>
      ${this._err ? html`<div class="err">${this._err}</div>` : nothing}
      <details open><summary>lifecycle</summary>${this._lifecycle()}</details>
      <details><summary>access</summary>${this._accessSec()}</details>
      <details @toggle=${(e) => e.target.open && this._loadRuntime()}><summary>runtime</summary>${this._runtime()}</details>
      <details><summary>vault</summary>${this._vaultSec()}</details>
      <details><summary>roles & grants</summary>${this._grantsSec()}</details>
      <details><summary>interfaces</summary>${this._bindsSec()}</details>
      <details><summary>backup</summary>${this._backupSec()}</details>
      <details><summary>cron</summary>${this._cronSec()}</details>
    `;
  }
}

customElements.define('bx-tile-admin', BxTileAdmin);
