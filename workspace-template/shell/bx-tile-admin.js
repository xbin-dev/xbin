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
import { netOptions } from '/vendor/bx-netrules.js';
import { capInfo } from '/vendor/bx-allow.js';

import { xbinApi as api, jbody } from '/vendor/bx-kit.js';

export class BxTileAdmin extends LitElement {
  static properties = {
    path: { type: String },
    section: { type: String },  // a section to open + scroll to (lifecycle|access|runtime|vault|grants|interfaces|backup|cron)
    noTitle: { type: Boolean, attribute: 'no-title' }, // the host's window chrome already names the tile
    _ov: { state: true },       // this tile's /auth-overview slice (state, roles, uses)
    _grants: { state: true },   // {grants, pending} filtered to this tile
    _binds: { state: true },    // /bindings (full — options need all providers)
    _orgs: { state: true },     // /orgs (the owning org's network sets drive the net picker, D54)
    _netCustom: { state: true }, // slot whose `custom…` net input is open
    _rt: { state: true },       // this tile's /runtime backend entry (lazy)
    _vault: { state: true },    // vault key names
    _backups: { state: true },  // versions
    _cron: { state: true },     // this tile's cron jobs
    _access: { state: true },   // the tile's ACL view (/access — owner + user/org entries)
    _dir: { state: true },      // /users-directory for the add-entry picker
    _accKind: { state: true },  // add-entry kind: user | org
    _secEdit: { state: true },  // vault key being re-set inline (name | null)
    _err: { state: true },
    _errSec: { state: true },   // section whose action produced _err ('' = header)
    _busy: { state: true },
  };

  static styles = css`
    :host {
      display: block; min-width: 0; font: var(--bx-font, 12.5px/1.45 system-ui, sans-serif);
      color: var(--bx-text, #d4d9e0);
    }
    .hd { display: flex; align-items: baseline; gap: 8px; padding: 8px 10px 6px;
      border-bottom: 1px solid var(--bx-border, #363c45); }
    .hd .t { font-family: var(--bx-mono, monospace); font-size: 12px; font-weight: 700;
      overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .hd .st { margin-left: auto; }
    .err { color: var(--bx-red, #ef5350); font-size: 11px; padding: 4px 10px; }
    details { border-bottom: 1px solid var(--bx-border, #363c45); }
    details:last-child { border-bottom: 0; }
    summary { cursor: pointer; user-select: none; list-style-position: inside;
      padding: 6px 10px; font-size: 10.5px; font-weight: 600; letter-spacing: .07em;
      text-transform: uppercase; color: var(--bx-muted, #868f9a); }
    summary:hover { background: var(--bx-panel-2, #2b3038); }
    .sec { padding: 2px 10px 10px; }
    .pill { display: inline-block; font-size: 10.5px; padding: 0 6px; border-radius: 999px;
      background: var(--bx-panel-2, #2b3038); border: 1px solid var(--bx-border, #363c45);
      margin: 1px 3px 1px 0; }
    .pill.on { color: var(--bx-green, #4caf50); border-color: color-mix(in srgb, var(--bx-green, #4caf50) 45%, transparent); }
    .pill.off { color: var(--bx-red, #ef5350); border-color: color-mix(in srgb, var(--bx-red, #ef5350) 45%, transparent); }
    .mono { font-family: var(--bx-mono, monospace); }
    .muted { color: var(--bx-muted, #868f9a); }
    table { border-collapse: collapse; width: 100%; font-size: 11.5px; }
    /* Control-heavy tables share the width; long refs ellipsize (the full
       text rides on title=) instead of pushing the window into a scroll. */
    table.fx { table-layout: fixed; }
    td { padding: 2px 6px 2px 0; border-top: 1px solid var(--bx-border, #363c45); vertical-align: middle; }
    td.ref { max-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    td.ctl { text-align: right; white-space: nowrap; }
    tr:first-child td { border-top: 0; }
    bx-multiselect { max-width: 100%; }
    button.act { border: 1px solid var(--bx-border, #363c45); background: var(--bx-panel, #23272e);
      color: var(--bx-text, #d4d9e0); border-radius: 5px; font: inherit; font-size: 10.5px;
      padding: 1px 7px; cursor: pointer; }
    button.act:hover { background: var(--bx-panel-2, #2b3038); }
    button.act:disabled { opacity: .5; cursor: default; }
    button.go { color: var(--bx-green, #4caf50); }
    button.rm { color: var(--bx-red, #ef5350); }
    input, select { font: inherit; font-size: 11px; padding: 2px 6px; max-width: 100%; box-sizing: border-box;
      border: 1px solid var(--bx-border, #363c45); border-radius: 5px;
      background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0); }
    select { text-overflow: ellipsis; }
    .row { display: flex; gap: 5px; align-items: center; flex-wrap: wrap; margin-top: 6px; }
    .kv { display: grid; grid-template-columns: auto 1fr; gap: 1px 10px; font-size: 11.5px; }
    .kv .k { color: var(--bx-muted, #868f9a); }
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
      const [ov, grants, binds, cron, backups, vault, access, orgs] = await Promise.all([
        api('/auth-overview').catch(forbidden),
        api('/grants').catch(forbidden),
        api('/bindings').catch(forbidden),
        api('/cron/jobs').catch(() => ({ jobs: [] })),
        api(`/backups?component=${encodeURIComponent(this.path)}`).catch(() => null),
        api(`/vault/${this.path}`).catch((e) => ({ err: String(e.message ?? e) })),
        api(`/access?tile=${encodeURIComponent(this.path)}`).catch((e) => ({ err: String(e.message ?? e) })),
        api('/orgs').catch(() => ({ orgs: [] })), // empty for non-admins — the net picker then shows the classic list
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
      this._orgs = orgs.orgs ?? [];
      this._cron = (cron.jobs ?? []).filter((j) => j.component === this.path);
      this._backups = backups?.versions ?? [];
      this._vault = vault;
      this._access = access;
      // A load error is reported like an action's; a successful reload does
      // NOT clear an action's refusal — that message belongs to the person.
    } catch (e) { this._err = String(e.message ?? e); this._errSec = ''; }
  }

  async _loadRuntime() {
    if (this._rt) return;
    try {
      const rt = await api('/runtime');
      this._rt = (rt.backends ?? []).find((b) => b.path === this.path) ?? { none: true };
    } catch (e) { this._rt = { err: String(e.message ?? e) }; }
  }

  // _do runs one action, then refreshes. A refusal stays on screen through
  // the refresh (the reload must not wipe it — it did, which is how a
  // rejected `host` bind once looked like a success) and is rendered inside
  // the section that asked (`sec`), where the person is looking, not only in
  // the header. Resolves true when the action went through.
  async _do(fn, sec = '') {
    this._busy = true;
    let ok = true;
    try { await fn(); this._err = ''; this._errSec = ''; } catch (e) { this._err = String(e.message ?? e); this._errSec = sec; ok = false; }
    this._busy = false;
    await this._loadCore();
    return ok;
  }

  _secErr(sec) {
    return this._err && this._errSec === sec ? html`<div class="err" role="alert">${this._err}</div>` : nothing;
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
      ${rt.netRef ? html`<span class="k">net</span><span class="mono" title=${(rt.netRules ?? []).join('\n')}>${rt.netRef === 'org'
        ? `org → ${rt.netSource || 'org network'}` : rt.netRef}${rt.net ? ` · ${rt.net}` : ''}</span>` : nothing}
      ${rt.netNote ? html`<span class="k"></span><span class="err">${rt.netNote}</span>` : nothing}
      <span class="k">egress</span><span class="mono">${act.allowed ?? 0} allowed · ${act.denied ?? 0} denied</span>
    </div>`;
  }

  _vaultSec() {
    const v = this._vault;
    if (v?.err) return html`<div class="sec err">${v.err}</div>`;
    const keys = v?.keys ?? [];
    return html`<div class="sec">
      <table class="fx">${keys.length ? keys.map((k) => html`<tr>
          <td class="mono ref" title=${k}>${k}</td>
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
      <table class="fx">
        ${g.pending.map((p) => html`<tr style=${p.blocked ? 'opacity:.55' : ''}>
          <td class="mono ref" style="font-size:10.5px" title=${p.blocked ?? capInfo(p.target)?.desc ?? `${p.from} → ${p.target}`}>${p.from} → ${p.target}</td>
          <td style="width:5.5em"><span class="pill">${p.role}</span></td>
          <td class="ctl" style="width:5em">${p.blocked
            ? html`<button class="act" disabled title=${p.blocked}>blocked</button>`
            : html`<button class="act go" ?disabled=${this._busy}
                @click=${() => this._do(() => api('/grants', { method: 'POST', ...jbody({ from: p.from, target: p.target, role: p.role }) }))}>approve</button>`}</td>
        </tr>`)}
        ${g.grants.map((p) => html`<tr>
          <td class="mono ref" style="font-size:10.5px" title=${capInfo(p.target)?.desc ?? `${p.from} → ${p.target}`}>${p.from} → ${p.target}</td>
          <td style="width:5.5em"><span class="pill">${p.role}</span></td>
          <td class="ctl" style="width:5em"><button class="act rm" ?disabled=${this._busy}
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
    // Options: same kind/service/own filter as the admin Interfaces tab. Net
    // builtins are not a fixed list — the owning org's network sets decide
    // (org / none / "not covered"), via bx-netrules (D54).
    const org = (this._orgs ?? []).find((o) => (o.ownedTiles ?? []).includes(this.path)) ?? null;
    const optsFor = (def) => {
      const out = [];
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
    // Who may wire this tile: the server says (ws admin, or an admin of the
    // owning org — D26). An owner opening ⚙ on their personal tile sees the
    // wiring read-only; a bind would be refused.
    const mayBind = !!d.approvable?.[this.path];
    const boundOf = (slot) => [].concat(d.bindings?.[this.path]?.[slot] ?? []).map((x) => (x && x.ref) ? x.ref : x);
    // set() resolves after the reload; a <select> keeps a refused choice on
    // screen (lit re-renders the same `selected` attributes, the browser keeps
    // the picked index), so the caller snaps it back to what is really bound.
    const set = (slot, providers) => this._do(() => api('/bindings', {
      method: providers.length ? 'POST' : 'DELETE',
      ...jbody(providers.length ? { component: this.path, slot, providers } : { component: this.path, slot }),
    }), 'interfaces');
    const setFrom = async (el, slot, providers) => {
      await set(slot, providers);
      // What is REALLY bound now — read from the reloaded this._binds, not
      // the snapshot this render closed over (that one still holds the old
      // value and would jump a successful re-bind back to it).
      const live = [].concat(this._binds?.bindings?.[this.path]?.[slot] ?? []).map((x) => (x && x.ref) ? x.ref : x);
      if (el?.isConnected) el.value = live[0] ?? '';
    };
    const who = org ? `a workspace admin or an admin of org:${org.id}` : 'a workspace admin';
    return html`<div class="sec">
      ${this._secErr('interfaces')}
      ${!mayBind && slots.length ? html`<div class="muted" style="margin-bottom:4px" data-readonly>
        wiring is set by ${who} — shown read-only</div>` : nothing}
      <table>
        ${slots.map(([slot, def]) => {
          const bound = boundOf(slot);
          const opts = optsFor(def);
          if (def.kind === 'net' && !def.multi) {
            const pend = (d.pending ?? []).find((p) => p.component === this.path && p.slot === slot);
            const nopts = netOptions({ org, providers: opts, pending: pend, options: d.netOptions?.[this.path] });
            const cur = bound[0] ?? '';
            const known = nopts.some((o) => o.id === cur);
            const inert = d.inert?.[this.path]?.[slot];
            if (!mayBind) {
              const shown = nopts.find((o) => o.id === cur);
              return html`<tr>
                <td class="ref" title=${slot}>${slot} <span class="pill">net</span>
                  ${inert ? html`<span class="pill off" title=${inert}>inert</span>` : nothing}</td>
                <td class="ctl" style="width:62%"><span class="mono" title=${shown?.title ?? cur}>${shown?.label ?? cur ?? '— unbound —'}</span>
                  ${inert ? html`<div class="err" style="font-size:10.5px">${inert}</div>` : nothing}</td></tr>`;
            }
            return html`<tr>
              <td class="ref" title=${slot}>${slot} <span class="pill">net</span>
                ${inert ? html`<span class="pill off" title=${inert}>inert</span>` : nothing}</td>
              <td class="ctl" style="width:62%">
                <select title=${cur || 'unbound'} @change=${(e) => {
                  const v = e.target.value;
                  if (v === '__custom') { this._netCustom = slot; e.target.value = cur; return; }
                  this._netCustom = null; setFrom(e.target, slot, v ? [v] : []);
                }}>
                  ${nopts.map((o) => html`<option value=${o.id} title=${o.title} ?selected=${o.id === cur} ?disabled=${!!o.disabled}>${o.label}</option>`)}
                  ${cur && !known ? html`<option value=${cur} selected>${cur}</option>` : nothing}
                </select>
                ${this._netCustom === slot ? html`<form class="row" style="justify-content:flex-end; margin-top:3px"
                    @submit=${(e) => { e.preventDefault(); const v = e.target.ref.value.trim(); if (!v) return; this._netCustom = null; set(slot, [v]); }}>
                    <input name="ref" size="24" placeholder="lan:10.0.0.0/8 · internet:host:443" .value=${cur && !known ? cur : ''}
                      title="filtered egress (D35): lan:<ip|cidr>[:port] or internet:<host|ip|cidr>[:port][,…] — no globs in bindings">
                    <button class="act go" type="submit">bind</button>
                    <button class="act" type="button" @click=${() => { this._netCustom = null; }}>✕</button></form>` : nothing}
                ${inert ? html`<div class="err" style="font-size:10.5px">${inert}</div>` : nothing}
              </td></tr>`;
          }
          if (!mayBind) {
            return html`<tr>
              <td class="ref" title=${slot}>${slot} <span class="pill">${def.kind}${def.service ? ':' + def.service : ''}${def.multi ? ' ×N' : ''}</span></td>
              <td class="ctl" style="width:62%"><span class="mono" title=${bound.join(', ')}>${bound.length ? bound.join(', ') : '— unbound —'}</span></td></tr>`;
          }
          return html`<tr>
            <td class="ref" title=${slot}>${slot} <span class="pill">${def.kind}${def.service ? ':' + def.service : ''}${def.multi ? ' ×N' : ''}</span></td>
            <td class="ctl" style="width:62%">${def.multi
              ? html`<bx-multiselect .options=${opts} .selected=${bound} placeholder="— unbound —"
                  @change=${(e) => set(slot, e.detail.selected)}></bx-multiselect>`
              : html`<select title=${bound[0] ?? 'unbound'} @change=${(e) => setFrom(e.target, slot, e.target.value ? [e.target.value] : [])}>
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
      <table class="fx" style="margin-top:5px">
        ${vs.slice(0, 6).map((v) => html`<tr>
          <td class="mono ref" style="font-size:10.5px" title=${v.version}>${v.version}</td>
          <td class="muted" style="width:5em">${v.size ? (v.size / 1048576).toFixed(1) + ' MB' : ''}</td>
          <td class="ctl" style="width:5em"><button class="act" ?disabled=${this._busy}
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

  // show(section) opens one section and scrolls to it — the tile menu's
  // "Access…", "Interfaces…" lines land here (D56).
  show(section) {
    this.section = section;
    this._reveal(section);
  }
  updated(changed) {
    if (changed.has('section') && this.section) this._reveal(this.section);
  }
  _reveal(section) {
    const d = this.renderRoot.querySelector(`details[data-sec="${CSS.escape(section)}"]`);
    if (!d) return;
    d.open = true;
    if (section === 'runtime') this._loadRuntime();
    this.updateComplete.then(() => d.scrollIntoView({ block: 'start', behavior: 'smooth' }));
  }

  render() {
    const st = this._ov?.state ?? 'enabled';
    return html`
      <div class="hd">
        ${this.noTitle ? nothing : html`<span class="t">${this.path}</span>`}
        ${this._ov?.forbidden ? nothing
          : html`<span class="st pill ${st === 'enabled' ? 'on' : 'off'}">${st}</span>`}
        <button class="act" title="reload" @click=${() => { this._rt = null; this._loadCore(); }}>⟳</button>
      </div>
      ${this._err && !this._errSec ? html`<div class="err" role="alert">${this._err}</div>` : nothing}
      <details open data-sec="lifecycle"><summary>lifecycle</summary>${this._lifecycle()}</details>
      <details data-sec="access"><summary>access</summary>${this._accessSec()}</details>
      <details data-sec="runtime" @toggle=${(e) => e.target.open && this._loadRuntime()}><summary>runtime</summary>${this._runtime()}</details>
      <details data-sec="vault"><summary>vault</summary>${this._vaultSec()}</details>
      <details data-sec="grants"><summary>roles & grants</summary>${this._grantsSec()}</details>
      <details data-sec="interfaces"><summary>interfaces</summary>${this._bindsSec()}</details>
      <details data-sec="backup"><summary>backup</summary>${this._backupSec()}</details>
      <details data-sec="cron"><summary>cron</summary>${this._cronSec()}</details>
    `;
  }
}

customElements.define('bx-tile-admin', BxTileAdmin);
