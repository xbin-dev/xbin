/**
 * <bx-org-admin> — org management for ORG ADMINS (and workspace admins), the
 * delegated half of docs/auth.md "Organizations & teams": members, co-admins,
 * base permission, and team CRUD for the orgs the signed-in human
 * administers. Everything is click-through: people are picked from chip
 * dropdowns (backed by /users-directory — identity only), tile grants from
 * row editors with a datalist of real paths/patterns. Workspace-security
 * knobs (policy rows, team term-api/term-net, org create/delete) stay in the
 * admin tile / bx — the footnote says so, and for non-workspace-admins the
 * toggles aren't rendered (the server 403s them regardless, D21).
 *
 * Lives in workspace CHROME (the shell pops it), not in a tile, and uses RAW
 * fetch on purpose: the cookie principal is the signed-in human, which is
 * exactly the authority org admins act with. Granting them the admin TILE
 * instead would hand them its frame token and the tile's own xbin
 * capabilities (docs/auth.md).
 *
 * Set the `wsadmin` attribute when the viewer is a workspace admin to expose
 * the term-flag toggles.
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

export class BxOrgAdmin extends LitElement {
  static properties = {
    wsadmin: { type: Boolean },
    _orgs: { state: true },
    _dir: { state: true },    // /users-directory — [{id,name}]
    _comps: { state: true },  // component paths for the target datalist
    _drafts: { state: true }, // row-editor drafts keyed by context
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
    .hd .t { font-size: 12px; font-weight: 700; }
    .err { color: var(--bx-red, #e5484d); font-size: 11px; padding: 4px 10px; }
    details { border-bottom: 1px solid var(--bx-border, #e4e8ed); }
    details:last-of-type { border-bottom: 0; }
    summary { cursor: pointer; user-select: none; list-style-position: inside;
      padding: 6px 10px; font-size: 11px; font-weight: 600;
      color: var(--bx-text, #33414e); }
    summary:hover { background: var(--bx-panel-2, #f7f8fa); }
    summary .mono { font-family: var(--bx-mono, monospace); }
    summary .muted { font-weight: 400; }
    .sec { padding: 2px 10px 10px; }
    .pill { display: inline-block; font-size: 10.5px; padding: 0 6px; border-radius: 999px;
      background: var(--bx-panel-2, #f7f8fa); border: 1px solid var(--bx-border, #e4e8ed);
      margin: 1px 3px 1px 0; }
    .pill.lv-read { color: #3577c8; border-color: color-mix(in srgb, #3577c8 40%, transparent); }
    .pill.lv-write { color: var(--bx-green, #43a047);
      border-color: color-mix(in srgb, var(--bx-green, #43a047) 40%, transparent); }
    .pill.lv-terminal { color: var(--bx-accent, #f5a623);
      border-color: color-mix(in srgb, var(--bx-accent, #f5a623) 45%, transparent); }
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
    .editor { padding: 6px 8px; background: var(--bx-panel-2, #f7f8fa); border-radius: 6px; }
    .erow { display: flex; gap: 5px; align-items: center; margin-bottom: 4px; }
    .warn { color: var(--bx-red, #e5484d); font-size: 10.5px; }
    .note { font-size: 10.5px; color: var(--bx-muted, #8794a1); padding: 6px 10px 8px; }
  `;

  constructor() {
    super();
    this._err = '';
    this._orgs = null;
  }

  connectedCallback() {
    super.connectedCallback();
    this._load();
  }

  async _load() {
    try {
      this._orgs = (await api('/orgs')).orgs ?? [];
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); this._orgs = []; }
    api('/users-directory').then((d) => { this._dir = d.users ?? []; }).catch(() => { this._dir = []; });
    api('/components').then((d) => {
      const list = Array.isArray(d) ? d : (d.components ?? []);
      this._comps = list.map((c) => c.path).filter((p) => p !== 'root' && p !== 'shell');
    }).catch(() => { this._comps = []; });
  }

  async _do(fn) {
    this._busy = true;
    try { await fn(); this._err = ''; } catch (e) { this._err = String(e.message ?? e); }
    this._busy = false;
    await this._load();
  }

  // ---- click-through pieces (same idioms as the admin tile) ----

  _peoplePicker(selected, onChange, only = null) {
    const ids = only ?? (this._dir ?? []).map((u) => u.id);
    const opts = ids.map((id) => {
      const u = (this._dir ?? []).find((x) => x.id === id);
      return { value: id, label: u?.name && u.name !== id ? `${id} — ${u.name}` : id };
    });
    return html`<bx-multiselect style="min-width:110px" .options=${opts} .selected=${selected ?? []}
      placeholder="— nobody —" @change=${(e) => onChange(e.detail.selected)}></bx-multiselect>`;
  }

  _draft(k) { return this._drafts?.[k]; }
  _setDraft(k, v) { this._drafts = { ...(this._drafts ?? {}), [k]: v }; }
  _dropDraft(k) { const d = { ...(this._drafts ?? {}) }; delete d[k]; this._drafts = d; }
  _toggleDraft(k, seed) { this._draft(k) ? this._dropDraft(k) : this._setDraft(k, seed()); }

  _targetDatalist(orgID) {
    const opts = new Set([`apps/o/${orgID}/*`, `o/${orgID}/*`, '*']);
    for (const p of (this._comps ?? [])) opts.add(p);
    return html`<datalist id="org-targets">
      ${[...opts].sort().map((t) => html`<option value=${t}></option>`)}
    </datalist>`;
  }

  // A pattern that can never match the org's paths is inert (the evaluation
  // clamp) — same rule as bx doctor / the admin tile.
  _patInert(org, pat) {
    if (!pat || pat === '*') return false;
    const p = pat.endsWith('/*') ? pat.slice(0, -2) : pat;
    const s = p.split('/');
    const pOrg = (s[0] === 'o' && s[1]) ? s[1] : (s.length >= 3 && s[1] === 'o' && s[2]) ? s[2] : null;
    if (pOrg) return pOrg !== org;
    if (p === pat) return true;
    return !(s.length === 1 || (s.length === 2 && s[1] === 'o'));
  }

  _tilesEditor(ctx, onSave, orgID) {
    const d = this._draft(ctx) ?? [];
    const upd = (i, patch) => this._setDraft(ctx, d.map((r, j) => (j === i ? { ...r, ...patch } : r)));
    return html`<div class="editor">
      ${d.map((r, i) => html`<div class="erow">
        <input list="org-targets" size="24" placeholder="path, prefix/* or *" .value=${r.target}
          @input=${(e) => upd(i, { target: e.target.value })}>
        <select @change=${(e) => upd(i, { level: e.target.value })}>
          ${['read', 'write', 'terminal'].map((l) => html`<option ?selected=${r.level === l}>${l}</option>`)}
        </select>
        ${this._patInert(orgID, r.target) ? html`<span class="warn"
          title="team grants only apply inside org ${orgID} — this entry can never match one of its paths">⚠ inert</span>` : nothing}
        <button class="act rm" @click=${() => this._setDraft(ctx, d.filter((_, j) => j !== i))}>✕</button>
      </div>`)}
      <div class="erow" style="margin-bottom:0">
        <button class="act" @click=${() => this._setDraft(ctx, [...d, { target: `apps/o/${orgID}/`, level: 'write' }])}>+ entry</button>
        <button class="act go" @click=${async () => {
          const tiles = {};
          for (const r of d) if (r.target.trim()) tiles[r.target.trim()] = r.level;
          await onSave(tiles); if (!this._err) this._dropDraft(ctx);
        }}>save</button>
        <button class="act" @click=${() => this._dropDraft(ctx)}>cancel</button>
        <span class="muted" style="font-size:10.5px">read = see · write = use/edit · terminal = root shell</span>
      </div>
    </div>`;
  }

  _patternsEditor(ctx, onSave, orgID) {
    const d = this._draft(ctx) ?? [];
    const upd = (i, v) => this._setDraft(ctx, d.map((r, j) => (j === i ? v : r)));
    return html`<div class="editor">
      ${d.map((r, i) => html`<div class="erow">
        <input list="org-targets" size="24" placeholder="prefix/* (create namespace)" .value=${r}
          @input=${(e) => upd(i, e.target.value)}>
        ${this._patInert(orgID, r) ? html`<span class="warn" title="create grants only apply inside org ${orgID}">⚠ inert</span>` : nothing}
        <button class="act rm" @click=${() => this._setDraft(ctx, d.filter((_, j) => j !== i))}>✕</button>
      </div>`)}
      <div class="erow" style="margin-bottom:0">
        <button class="act" @click=${() => this._setDraft(ctx, [...d, `apps/o/${orgID}/*`])}>+ pattern</button>
        <button class="act go" @click=${async () => {
          await onSave(d.map((s) => s.trim()).filter(Boolean));
          if (!this._err) this._dropDraft(ctx);
        }}>save</button>
        <button class="act" @click=${() => this._dropDraft(ctx)}>cancel</button>
        <span class="muted" style="font-size:10.5px">members creating here auto-get terminal on the new tile</span>
      </div>
    </div>`;
  }

  _teamRow(o, t) {
    const tpath = `/orgs/${encodeURIComponent(o.id)}/teams/${encodeURIComponent(t.id)}`;
    const patch = (body) => this._do(() => api(tpath, { method: 'PATCH', ...jbody(body) }));
    const orgPeople = [...new Set([...(o.admins ?? []), ...(o.members ?? [])])];
    const tilesKey = `team:${o.id}/${t.id}:tiles`;
    const createKey = `team:${o.id}/${t.id}:create`;
    return html`<tr>
      <td class="mono">${t.id}</td>
      <td>${this._peoplePicker(t.members, (m) => patch({ members: m }), orgPeople)}</td>
      <td>${Object.entries(t.tiles || {}).map(([p, l]) => html`<span class="pill lv-${l}">${p} · ${l}</span>`)}
          ${(t.canCreate || []).map((c) => html`<span class="pill">create·${c}</span>`)}
          ${t.termApi ? html`<span class="pill">term-api</span>` : nothing}
          ${t.termNet ? html`<span class="pill">term-net</span>` : nothing}</td>
      <td><select title="level the team gets on tiles created in it" ?disabled=${this._busy}
            @change=${(e) => patch({ newTiles: e.target.value })}>
          ${['read', 'write', 'terminal'].map((l) => html`<option value=${l} ?selected=${t.newTiles === l}>new: ${l}</option>`)}
        </select></td>
      <td style="text-align:right; white-space:nowrap">
        <button class="act" ?disabled=${this._busy} @click=${() => this._toggleDraft(tilesKey,
          () => Object.entries(t.tiles ?? {}).map(([target, level]) => ({ target, level })))}>tiles…</button>
        <button class="act" ?disabled=${this._busy} @click=${() => this._toggleDraft(createKey,
          () => [...(t.canCreate ?? [])])}>create…</button>
        ${this.wsadmin ? html`
          <button class="act" ?disabled=${this._busy} @click=${() => patch({ termApi: !t.termApi })}>${t.termApi ? '− api' : '+ api'}</button>
          <button class="act" ?disabled=${this._busy} @click=${() => patch({ termNet: !t.termNet })}>${t.termNet ? '− net' : '+ net'}</button>` : nothing}
        <button class="act rm" ?disabled=${this._busy} @click=${() => confirm(`Delete team ${o.id}/${t.id}? Its access grants vanish.`)
          && this._do(() => api(tpath, { method: 'DELETE' }))}>del</button>
      </td>
    </tr>
    ${this._draft(tilesKey) ? html`<tr><td colspan="5">
      ${this._tilesEditor(tilesKey, (tiles) => this._do(() => api(tpath, { method: 'PATCH', ...jbody({ tiles }) })), o.id)}
    </td></tr>` : nothing}
    ${this._draft(createKey) ? html`<tr><td colspan="5">
      ${this._patternsEditor(createKey, (canCreate) => this._do(() => api(tpath, { method: 'PATCH', ...jbody({ canCreate }) })), o.id)}
    </td></tr>` : nothing}`;
  }

  _orgSec(o) {
    const opath = `/orgs/${encodeURIComponent(o.id)}`;
    const patch = (body) => this._do(() => api(opath, { method: 'PATCH', ...jbody(body) }));
    return html`
      <details ?open=${this._orgs.length === 1}>
        <summary><span class="mono">${o.id}</span>
          <span class="muted">${o.name !== o.id ? ` — ${o.name}` : ''} ·
            ${(o.members || []).length + (o.admins || []).length} people ·
            ${(o.teams || []).length} team(s)</span></summary>
        <div class="sec">
          ${this._targetDatalist(o.id)}
          <div class="row" style="margin-top:0">
            <label class="muted" style="font-size:11px">base
              <select title="floor every member gets on org tiles (terminal is never implicit)"
                ?disabled=${this._busy}
                @change=${(e) => patch({ basePermission: e.target.value })}>
                ${[['', 'none'], ['read', 'read'], ['write', 'write']].map(([v, l]) =>
                  html`<option value=${v} ?selected=${(o.basePermission ?? '') === v}>${l}</option>`)}
              </select></label>
            <label class="muted" style="font-size:11px">admins
              ${this._peoplePicker(o.admins, (a) => patch({ admins: a }))}</label>
            <label class="muted" style="font-size:11px">members
              ${this._peoplePicker(o.members, (m) => patch({ members: m }))}</label>
          </div>
          <table style="margin-top:6px">
            ${(o.teams || []).length ? html`<tr><th style="text-align:left">team</th><th style="text-align:left">members</th><th style="text-align:left">grants</th><th></th><th></th></tr>` : nothing}
            ${(o.teams || []).map((t) => this._teamRow(o, t))}
            ${!(o.teams || []).length ? html`<tr><td class="muted">no teams yet</td></tr>` : nothing}
          </table>
          <form class="row" @submit=${(e) => {
            e.preventDefault(); const f = e.target;
            const id = f.tid.value.trim(); if (!id) return;
            this._do(() => api(`/orgs/${encodeURIComponent(o.id)}/teams`, { method: 'POST', ...jbody({ id }) }));
            f.reset();
          }}>
            <input name="tid" placeholder="new team id" size="11">
            <button class="act go" ?disabled=${this._busy}>add team</button>
            <span class="muted" style="font-size:10.5px">then pick members / grant tiles on its row</span>
          </form>
        </div>
      </details>`;
  }

  render() {
    return html`
      <div class="hd"><span class="t">orgs & teams</span>
        <span class="muted" style="font-size:10.5px">you administer these</span>
        <span style="flex:1"></span>
        <button class="act" title="reload" @click=${() => this._load()}>⟳</button>
      </div>
      ${this._err ? html`<div class="err">${this._err}</div>` : nothing}
      ${this._orgs == null ? html`<div class="note">…</div>`
        : this._orgs.length === 0 ? html`<div class="note">You don't administer any orgs.</div>`
        : this._orgs.map((o) => this._orgSec(o))}
      <div class="note">
        Per-tile access lives on each tile's ⚙ → access. Creating/deleting orgs,
        policy ceilings${this.wsadmin ? '' : ', and team term-api/term-net'} are
        workspace-admin operations (admin tile / bx).
      </div>`;
  }
}

customElements.define('bx-org-admin', BxOrgAdmin);
