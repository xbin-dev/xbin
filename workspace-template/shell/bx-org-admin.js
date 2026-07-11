/**
 * <bx-org-admin> — org management for ORG ADMINS (and workspace admins), the
 * delegated half of docs/auth.md "Organizations & teams": members, co-admins,
 * base permission, and team CRUD for the orgs the signed-in human
 * administers. Workspace-security knobs (policy rows, team term-api/term-net,
 * org create/delete) stay in the admin tile / bx — the footnote says so, and
 * for non-workspace-admins the toggles aren't rendered (the server 403s them
 * regardless, D21).
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
  }

  async _do(fn) {
    this._busy = true;
    try { await fn(); this._err = ''; } catch (e) { this._err = String(e.message ?? e); }
    this._busy = false;
    await this._load();
  }

  _promptList(title, cur) {
    const v = prompt(title, (cur || []).join(', '));
    return v == null ? null : v.split(',').map((s) => s.trim()).filter(Boolean);
  }

  // tiles spec ⇄ map, same grammar as the admin tile ("path=level, …").
  _tilesSpec(tiles) {
    return Object.entries(tiles || {}).map(([p, l]) => `${p}=${l}`).join(', ');
  }
  _parseTilesSpec(s) {
    const tiles = {};
    for (const part of (s || '').split(',')) {
      const t = part.trim(); if (!t) continue;
      const [path, level] = t.split('=').map((x) => x.trim());
      if (path) tiles[path] = level || 'write';
    }
    return tiles;
  }

  _teamRow(o, t) {
    const tpath = `/orgs/${encodeURIComponent(o.id)}/teams/${encodeURIComponent(t.id)}`;
    const patch = (body) => this._do(() => api(tpath, { method: 'PATCH', ...jbody(body) }));
    return html`<tr>
      <td class="mono">${t.id}</td>
      <td>${(t.members || []).map((m) => html`<span class="pill">${m}</span>`)}
          ${Object.entries(t.tiles || {}).map(([p, l]) => html`<span class="pill">${p}·${l}</span>`)}
          ${(t.canCreate || []).map((c) => html`<span class="pill">create·${c}</span>`)}
          ${t.termApi ? html`<span class="pill">term-api</span>` : nothing}
          ${t.termNet ? html`<span class="pill">term-net</span>` : nothing}</td>
      <td><select title="level the team gets on tiles created in it" ?disabled=${this._busy}
            @change=${(e) => patch({ newTiles: e.target.value })}>
          ${['read', 'write', 'terminal'].map((l) => html`<option value=${l} ?selected=${t.newTiles === l}>new: ${l}</option>`)}
        </select></td>
      <td style="text-align:right; white-space:nowrap">
        <button class="act" ?disabled=${this._busy} @click=${() => {
          const m = this._promptList(`Members of ${o.id}/${t.id} (must be org members):`, t.members);
          if (m) patch({ members: m });
        }}>members</button>
        <button class="act" ?disabled=${this._busy} @click=${() => {
          const v = prompt(
            `Tile access for ${o.id}/${t.id} — "pattern=level" entries (read|write|terminal); `
            + `only paths in org ${o.id} apply (e.g. apps/o/${o.id}/*):`,
            this._tilesSpec(t.tiles));
          if (v != null) patch({ tiles: this._parseTilesSpec(v) });
        }}>tiles</button>
        <button class="act" ?disabled=${this._busy} @click=${() => {
          const c = this._promptList(`Create patterns for ${o.id}/${t.id} (e.g. apps/o/${o.id}/*):`, t.canCreate);
          if (c) patch({ canCreate: c });
        }}>create</button>
        ${this.wsadmin ? html`
          <button class="act" ?disabled=${this._busy} @click=${() => patch({ termApi: !t.termApi })}>${t.termApi ? '− api' : '+ api'}</button>
          <button class="act" ?disabled=${this._busy} @click=${() => patch({ termNet: !t.termNet })}>${t.termNet ? '− net' : '+ net'}</button>` : nothing}
        <button class="act rm" ?disabled=${this._busy} @click=${() => confirm(`Delete team ${o.id}/${t.id}? Its access grants vanish.`)
          && this._do(() => api(tpath, { method: 'DELETE' }))}>del</button>
      </td>
    </tr>`;
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
          <div class="row" style="margin-top:0">
            <label class="muted" style="font-size:11px">base
              <select title="floor every member gets on org tiles (terminal is never implicit)"
                ?disabled=${this._busy}
                @change=${(e) => patch({ basePermission: e.target.value })}>
                ${[['', 'none'], ['read', 'read'], ['write', 'write']].map(([v, l]) =>
                  html`<option value=${v} ?selected=${(o.basePermission ?? '') === v}>${l}</option>`)}
              </select></label>
            <button class="act" ?disabled=${this._busy} @click=${() => {
              const m = this._promptList(`Members of ${o.id}:`, o.members);
              if (m) patch({ members: m });
            }}>members</button>
            <button class="act" ?disabled=${this._busy} @click=${() => {
              const a = this._promptList(`Org admins of ${o.id} (they manage members/teams/access; security knobs stay with workspace admins):`, o.admins);
              if (a) patch({ admins: a });
            }}>admins</button>
          </div>
          <div style="margin-top:4px; font-size:11.5px">
            admins: ${(o.admins || []).map((a) => html`<span class="pill">${a}</span>`)}
            members: ${(o.members || []).length ? (o.members || []).map((m) => html`<span class="pill">${m}</span>`) : html`<span class="muted">none</span>`}
          </div>
          <table style="margin-top:6px">
            ${(o.teams || []).map((t) => this._teamRow(o, t))}
            ${!(o.teams || []).length ? html`<tr><td class="muted">no teams yet</td></tr>` : nothing}
          </table>
          <form class="row" @submit=${(e) => {
            e.preventDefault(); const f = e.target;
            const id = f.tid.value.trim(); if (!id) return;
            this._do(() => api(`/orgs/${encodeURIComponent(o.id)}/teams`, {
              method: 'POST',
              ...jbody({ id, members: f.tmembers.value.split(',').map((s) => s.trim()).filter(Boolean) }),
            }));
            f.reset();
          }}>
            <input name="tid" placeholder="new team id" size="11">
            <input name="tmembers" placeholder="members: a, b" size="13">
            <button class="act go" ?disabled=${this._busy}>add team</button>
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
