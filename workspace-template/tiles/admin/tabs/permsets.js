/**
 * <bx-admin-permsets> — the admin console's permission sets tab (D28): the
 * typed creator (D57) that builds a set from rows saying in words what
 * attached orgs' admins may approve, and attaches it to orgs in the same
 * save. A tab element of tiles/admin (see admin.js): data arrives as
 * properties, writes go through the API and are reported to the router
 * with composed events (bx-admin-err / bx-admin-refresh / bx-admin-tab).
 */
import { LitElement, html, nothing } from 'lit';
import { xbinApi as api, jbody } from '/vendor/bx-kit.js';
import { parseAllow, fmtAllow, allowProblem, describeAllow } from '/vendor/bx-allow.js';
import '/vendor/bx-multiselect.js';
import { base } from '../admin-css.js';
import { targetDatalist, serviceDatalist, allowRows, WithDrafts } from '../shared.js';

export class BxAdminPermsets extends WithDrafts(LitElement) {
  static properties = {
    permsets: { attribute: false }, // {sets, attachedTo} from /permission-sets
    orgs: { attribute: false },     // the orgs a set can attach to
    targets: { attribute: false },  // tile-target datalist options
    services: { attribute: false }, // http services (shared.serviceOptions)
    _drafts: { state: true }, // editor drafts keyed by context ("permset:<name>", …)
    _err: { state: true },    // the last API refusal (also reported to the router's slot)
  };
  static styles = [base];

  _emit(type, detail) { this.dispatchEvent(new CustomEvent(type, { detail, bubbles: true, composed: true })); }
  // The harness surface (routed here by the admin router's testApi).
  testApi() { return this.draftApi(); }
  // One write, then the router reloads the shared lists; the error (or its
  // clearing) lands in the router's global slot and in _err for `if (!this._err)`.
  async _orgAPI(method, path, body) {
    try {
      await api(path, body === undefined ? { method } : jbody(body, method));
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
    this._emit('bx-admin-err', this._err);
    this._emit('bx-admin-refresh');
  }

  // A set is built from rows that say in words what attached orgs' admins
  // may approve on their own tiles; bx-allow formats each row into the
  // grammar, flags problems inline before the round trip and describes
  // stored entries back in plain words. One save writes the set AND attaches
  // it to the chosen orgs.
  render() {
    const sets = this.permsets?.sets ?? {};
    const attached = this.permsets?.attachedTo ?? {};
    const editKey = (n) => `permset:${n}`;
    const creating = this._draft('permset:new');
    return html`
      ${targetDatalist(this.targets)}
      ${serviceDatalist(this.services)}
      <p class="muted" style="max-width:72ch">A permission set says what the <b>admins of an organisation may
        approve on their own tiles</b> without asking a workspace admin — "their tiles may use the LLM gateway
        as writer", "may bind a feed interface to our provider". Attach one set to many orgs; edit it once and
        every attached org follows. A set grants nothing by itself: a tile still <i>requests</i>, an org admin
        <i>approves</i> (organisations tile → ⚑). What an org's tiles may <i>reach</i> on the network is the
        <a class="link" @click=${() => this._emit('bx-admin-tab', 'netsets')}>network sets</a> tab.</p>
      ${creating ? this._setEditor('permset:new', creating, null) : html`
        <button class="act go" data-new-set @click=${() => this._setDraft('permset:new', this._newSetDraft())}>＋ new permission set</button>`}
      ${Object.entries(sets).sort(([a], [b]) => a.localeCompare(b)).map(([name, ps]) => {
        const key = editKey(name);
        const d = this._draft(key);
        const orgs = attached[name] ?? [];
        return html`
        <div class="setcard" data-set=${name} style="border:1px solid var(--bx-border, #363c45); border-radius:6px; padding:8px 10px; margin:8px 0">
          <div style="display:flex; align-items:baseline; gap:8px; flex-wrap:wrap">
            <b class="mono">⛭ ${name}</b>
            ${orgs.map((o) => html`<span class="pill">org ${o}</span>`)}
            ${!orgs.length ? html`<span class="muted" style="font-size:11px">not attached to any org yet</span>` : nothing}
            ${ps.termApi ? html`<span class="pill" title="members get a tile-scoped API token in their terminals">term-api</span>` : nothing}
            ${ps.termNet ? html`<span class="pill" title="members get internet in terminals on personal/workspace tiles">term-net</span>` : nothing}
            ${(ps.policy ?? []).length ? html`<span class="pill pol" title="ceiling rows (restrictive; edited via the API/bx for now)">⛔ ${ps.policy.length} ceiling row(s)</span>` : nothing}
            <span style="flex:1"></span>
            <button class="act" ?disabled=${!!d} @click=${() => this._setDraft(key, this._setDraftFrom(name, ps, orgs))}>edit</button>
            <button class="act rm" ?disabled=${orgs.length > 0} title=${orgs.length ? `detach from ${orgs.join(', ')} first (edit → attach)` : 'delete this set'}
              @click=${() => confirm(`Delete permission set ${name}?`) && this._orgAPI('DELETE', `/permission-sets/${encodeURIComponent(name)}`)}>del</button>
          </div>
          ${(ps.allow ?? []).length ? html`<ul class="allowlist">
            ${ps.allow.map((a) => html`<li><span class="pill mono" title=${a}>${a}</span> <span class="muted">${describeAllow(a)}</span></li>`)}
          </ul>` : html`<div class="muted" style="font-size:11px; margin-top:3px">no entries — attached orgs' admins approve nothing beyond intra-org wiring</div>`}
          ${d ? this._setEditor(key, d, name) : nothing}
        </div>`;
      })}
      ${!Object.keys(sets).length && !creating ? html`<p class="muted">No permission sets yet — create one to delegate approvals to org admins.</p>` : nothing}`;
  }

  _newSetDraft() {
    return { name: '', rows: [{ kind: 'tile', value: '', role: 'writer' }], termApi: false, termNet: false, orgs: [], err: '' };
  }
  _setDraftFrom(name, ps, orgs) {
    return { name, rows: (ps.allow ?? []).map(parseAllow), termApi: !!ps.termApi, termNet: !!ps.termNet, orgs: [...orgs], err: '' };
  }

  // The set form: name (new sets), the typed allow rows, the members'
  // terminal flags and the orgs to attach. Create/save is disabled until
  // every field passes; a server refusal shows inside the form.
  _setEditor(key, d, name) {
    const isNew = name === null;
    const set = (patch) => this._setDraft(key, { ...this._draft(key), ...patch });
    const nameOk = /^[a-z0-9][a-z0-9._-]{0,31}$/.test(d.name);
    const nameTaken = isNew && !!this.permsets?.sets?.[d.name];
    const wire = d.rows.map(fmtAllow);
    const problems = d.rows.map(allowProblem);
    const dup = new Set(wire).size !== wire.length;
    const ok = (isNew ? nameOk && !nameTaken : true) && !problems.some(Boolean) && !dup;
    const orgIds = (this.orgs ?? []).map((o) => o.id);
    return html`<div class="editor seteditor" data-editing=${isNew ? 'new' : name} style="margin-top:8px">
      ${isNew ? html`<div class="orow">
        <label class="muted">name <input name="setname" size="18" placeholder="infra" .value=${d.name}
          @input=${(e) => set({ name: e.target.value.trim().toLowerCase() })}></label>
        ${d.name && !nameOk ? html`<span class="err-pill">1–32 of a–z 0–9 . _ - starting with a letter or digit</span>` : nothing}
        ${nameTaken ? html`<span class="err-pill">a set with this name already exists</span>` : nothing}
      </div>` : nothing}
      <div class="muted" style="margin:4px 0 2px"><b>Admins of attached orgs may approve, on their own tiles:</b></div>
      ${allowRows(d.rows, (rows) => set({ rows }), { gotoTab: (t) => this._emit('bx-admin-tab', t) })}
      <div class="muted" style="margin:8px 0 2px"><b>Members of attached orgs, in their terminals:</b></div>
      <div class="orow">
        <label class="muted" title="a tile-scoped API token in every terminal (bx works there)"><input type="checkbox" .checked=${d.termApi}
          @change=${(e) => set({ termApi: e.target.checked })}> may use the tile API</label>
        <label class="muted" title="internet in terminals on personal/workspace tiles — org tiles follow the org's network sets"><input type="checkbox" .checked=${d.termNet}
          @change=${(e) => set({ termNet: e.target.checked })}> get internet on personal/workspace tiles</label>
      </div>
      <div class="muted" style="margin:8px 0 2px"><b>Attached to:</b></div>
      <div class="orow">
        <bx-multiselect style="min-width:180px" .options=${orgIds.map((o) => ({ value: o, label: o }))}
          .selected=${d.orgs} placeholder="— no organisations yet —" @change=${(e) => set({ orgs: e.detail.selected })}></bx-multiselect>
        <span class="muted" style="font-size:10.5px">a set can be attached later from an org's card too</span>
      </div>
      ${d.err ? html`<div class="err" role="alert">${d.err}</div>` : nothing}
      <div class="orow" style="margin-top:6px">
        <button class="act go" data-save-set ?disabled=${!ok}
          title=${ok ? (isNew ? 'create the set and attach it' : 'save — every attached org follows at once') : 'fix the highlighted fields first'}
          @click=${() => this._saveSet(key, d, isNew ? d.name : name)}>${isNew ? 'create set' : 'save'}</button>
        <button class="act" @click=${() => this._dropDraft(key)}>cancel</button>
        ${dup ? html`<span class="err-pill">duplicate entries</span>` : nothing}
      </div>
    </div>`;
  }

  async _saveSet(key, d, name) {
    const wire = d.rows.map(fmtAllow).filter(Boolean);
    const cur = this.permsets?.sets?.[name] ?? {};
    const before = new Set(this.permsets?.attachedTo?.[name] ?? []);
    const after = new Set(d.orgs);
    const json = (method, body) => ({ method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    try {
      await api(`/permission-sets/${encodeURIComponent(name)}`, json('PUT', { allow: wire, policy: cur.policy ?? [], termApi: d.termApi, termNet: d.termNet }));
      for (const o of this.orgs ?? []) {
        const had = before.has(o.id), want = after.has(o.id);
        if (had === want) continue;
        const sets = want ? [...(o.sets ?? []), name] : (o.sets ?? []).filter((s) => s !== name);
        await api(`/orgs/${encodeURIComponent(o.id)}`, json('PATCH', { sets }));
      }
      this._emit('bx-admin-err', '');
      this._dropDraft(key);
    } catch (e) {
      this._setDraft(key, { ...this._draft(key), err: String(e.message ?? e) });
    }
    this._emit('bx-admin-refresh');
  }
}

customElements.define('bx-admin-permsets', BxAdminPermsets);
