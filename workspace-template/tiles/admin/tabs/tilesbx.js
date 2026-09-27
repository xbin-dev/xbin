/**
 * <bx-admin-tile-sandboxes> — the runtime → sandboxes tab's part for the
 * sandboxes manager tiles run (D120, docs/protocol.md §Tile sandboxes):
 * every definition — stopped ones, and those of removed tiles too — grouped
 * under its manager tile, with an admin's two actions (stop, delete); the
 * runtime's health (the policy file's error, the total book of memory and
 * processes, the relays' flow budget, starts held for a low disk, what
 * waits for the confined remover); and the sandboxes policy editor. The
 * editor saves through PUT /sandboxes/policy what the admin set (zero = the
 * default) — the server merges it onto the stored policy, so per-tile
 * overrides stay. The parent tab passes its polled GET /sandboxes answer
 * (.data) and a reload.
 */
import { LitElement, css, html, nothing } from 'lit';
import { xbinApi as api, jbody } from '/vendor/bx-kit.js';
import { base, runtimeCss, sandboxesCss } from '../admin-css.js';
import { fmtBytes, fmtDur, WithRouter } from '../shared.js';

const mib = (n) => (n >= 1024 ? `${+(n / 1024).toFixed(1)} GiB` : `${n || 0} MiB`);
const MODE = { vm: '⧉ VM', namespace: '🔒 ns' };
const LIVE = new Set(['starting', 'running', 'stopping']);
// The policy's numbers, as the editor shows them: [path, label].
const POLICY_NUMS = [
  ['perTile.max', 'sandboxes per tile'], ['perTile.running', 'running per tile'],
  ['perTile.memMiB', "a tile's running memory (MiB)"], ['perTile.vcpus', "a tile's running vCPUs"],
  ['perTile.diskGiB', "a tile's disk (GiB)"],
  ['perSandbox.memMiB', 'default memory (MiB)'], ['perSandbox.vcpus', 'default vCPUs'], ['perSandbox.diskGiB', 'default disk (GiB)'],
  ['perSandbox.maxMemMiB', 'max memory (MiB)'], ['perSandbox.maxVCPUs', 'max vCPUs'], ['perSandbox.maxDiskGiB', 'max disk (GiB)'],
  ['perSandbox.pids', 'processes per sandbox'],
  ['total.memMiB', 'all together: memory (MiB)'], ['total.pids', 'all together: processes'],
  ['idleStopMin', 'idle stop (minutes)'], ['outputRingMiB', "an exec's output ring (MiB)"], ['outputBudgetMiB', "a tile's output rings (MiB)"],
];
const at = (o, path) => path.split('.').reduce((v, k) => v?.[k], o);
const setAt = (o, path, v) => {
  const ks = path.split('.');
  const out = { ...o };
  let cur = out;
  for (const k of ks.slice(0, -1)) { cur[k] = { ...(cur[k] || {}) }; cur = cur[k]; }
  cur[ks.at(-1)] = v;
  return out;
};
const ago = (ms) => fmtDur((Date.now() - ms) / 1000) + ' ago';

export class BxAdminTileSandboxes extends WithRouter(LitElement) {
  static properties = {
    data: { attribute: false },   // GET /sandboxes (the parent's poll)
    reload: { attribute: false }, // the parent's load()
    _pol: { state: true },        // GET /sandboxes/policy
    _draft: { state: true },      // the policy being edited (null = not)
    _busy: { state: true },
    _err: { state: true },
  };
  static styles = [base, runtimeCss, sandboxesCss, css`:host { display: block; margin-top: 14px; }`];

  constructor() { super(); this._draft = null; }
  connectedCallback() { super.connectedCallback(); this._loadPolicy(); }
  async _loadPolicy() {
    try { this._pol = await api('/sandboxes/policy'); } catch (e) { this._fail(e); }
  }

  render() {
    const d = this.data || {};
    const rows = d.tileSandboxes || [];
    return html`<h4 data-tsbx>tile sandboxes <span class="muted" style="font-weight:400">(${rows.length} defined — the sandboxes manager tiles run)</span></h4>
      ${this._health(d.health?.tileSandboxes)}
      ${this._policy()}
      ${this._table(rows)}`;
  }

  // ---- health: the policy file, the books, a low disk, the removal backlog ----
  _health(h) {
    if (!h) return nothing;
    const t = h.total || {}, mem = t.memMiB || {}, pids = t.pids || {}, fl = h.flows || {}, tr = h.trash || {};
    return html`
      ${h.policyError ? html`<div class="warn-line" data-tsbx-policy-error>⚠ the sandboxes policy file can't be read, so tile sandboxes are off until the policy is saved again: ${h.policyError}</div>` : nothing}
      ${h.lowDisk ? html`<div class="warn-line" data-tsbx-low-disk>⚠ the workspace disk is low: tile sandboxes don't start, and running ones above the fair share were stopped, until space is freed</div>` : nothing}
      ${h.cgroup ? html`<div class="warn-line">⚠ ${h.cgroup}</div>` : nothing}
      <div class="sbx-policy" data-tsbx-health>
        memory <b>${mib(mem.used)}</b> of ${mem.cap ? mib(mem.cap) : 'no cap'} ·
        processes <b>${pids.used >= 0 ? pids.used : '?'}</b> of ${pids.cap || '?'} ·
        relay flows <b>${fl.used || 0}</b> of ${fl.cap || '?'} ·
        <span title="state deleted or reset, waiting for the confined remover" data-tsbx-trash>removal backlog <b>${tr.entries || 0}</b>${tr.bytes ? ` (${fmtBytes(tr.bytes)})` : ''}</span>
      </div>`;
  }

  // ---- the sandboxes policy (GET/PUT /sandboxes/policy) ----
  _policy() {
    const v = this._pol;
    if (!v) return nothing;
    const p = v.policy || {}, ov = Object.keys(p.overrides || {});
    if (!this._draft) {
      return html`<div class="sbx-policy" data-tsbx-policy="view">
        policy: tile sandboxes ${p.enabled ? '✓' : '✗ off'} ·
        per tile ${p.perTile?.max} defined, ${p.perTile?.running} running, ${mib(p.perTile?.memMiB)}, ${p.perTile?.vcpus} vCPUs, ${p.perTile?.diskGiB} GiB ·
        all together ${p.total?.memMiB ? mib(p.total.memMiB) : "¾ of the host's memory"}, ${p.total?.pids} processes · idle stop ${p.idleStopMin} min
        ${ov.length ? html` · <span title=${ov.join(', ')}>${ov.length} tile override${ov.length === 1 ? '' : 's'}</span>` : nothing}
        <button class="act" data-tsbx-edit-policy @click=${() => { this._draft = { ...(v.stored || {}), enabled: v.stored?.enabled !== false }; }}>edit</button>
      </div>`;
    }
    const d = this._draft;
    const set = (path, val) => { this._draft = setAt(d, path, val); };
    const num = (path) => html`<input type="number" min="0" name=${path} .value=${at(d, path) ? String(at(d, path)) : ''}
      placeholder=${String(at(p, path) ?? '')} @input=${(e) => set(path, e.target.value === '' ? 0 : Number(e.target.value))}>`;
    const warn = [];
    if (p.enabled && !d.enabled) warn.push('Switching tile sandboxes off stops every running one now (their state is kept); starts are refused until it is on again.');
    if (v.error) warn.push("The policy file can't be read: saving writes a new one (on the defaults, plus what is set here).");
    warn.push('Lowered sizes and caps apply at the next start; a running sandbox shows restartNeeded.');
    return html`<form class="sbx-policy editor" data-tsbx-policy="edit" @submit=${(e) => { e.preventDefault(); this._savePolicy(); }}>
      <label><input type="checkbox" name="enabled" .checked=${!!d.enabled} @change=${(e) => set('enabled', e.target.checked)}> tile sandboxes (manager tiles with <span class="mono">cap:sandboxes</span> run them)</label>
      <div class="sbx-fields">${POLICY_NUMS.map(([path, label]) => html`<label>${label} ${num(path)}</label>`)}</div>
      <div class="muted" style="font-size:10.5px">empty = the default (shown)${ov.length ? ` · per-tile overrides (${ov.join(', ')}) are kept` : ''}</div>
      ${warn.map((w) => html`<div class="warn-line">⚠ ${w}</div>`)}
      <div>
        <button class="act go" type="submit" data-tsbx-save-policy ?disabled=${this._busy}>save</button>
        <button class="act" type="button" @click=${() => { this._draft = null; }}>cancel</button>
      </div>
    </form>`;
  }

  async _savePolicy() {
    const d = this._draft;
    let body = { enabled: !!d.enabled };
    for (const [path] of POLICY_NUMS) body = setAt(body, path, Number(at(d, path)) || 0);
    this._busy = true;
    try {
      this._pol = await api('/sandboxes/policy', jbody(body, 'PUT'));
      this._ok();
      this._draft = null;
      this._emit('bx-admin-notice', 'sandboxes policy saved');
      await this.reload?.();
    } catch (e) { this._fail(e); }
    this._busy = false;
  }

  // ---- every definition, under its manager tile ----
  _table(rows) {
    if (!rows.length) return html`<div class="muted" data-tsbx-none>no manager tile has defined a sandbox</div>`;
    const by = new Map();
    for (const r of rows) (by.get(r.tile) || by.set(r.tile, []).get(r.tile)).push(r);
    return html`<table class="sbx" data-tsbx-table>
      <tr><th>sandbox</th><th>state</th><th>mode</th><th>size</th><th>on disk</th><th>for</th><th>last active</th><th></th></tr>
      ${[...by].map(([tile, rs]) => html`
        <tr class="sbx-tile" data-tsbx-tile=${tile}><td colspan="8">
          <span class="mono">${tile}</span>
          ${rs[0].tileExists ? nothing : html`<span class="warn-line" data-tsbx-orphan
            title="their definitions and state are kept for you to delete; a tile created at this path can't inherit them"> · its tile was removed: leftovers</span>`}
          <span class="muted"> · ${rs.length} sandbox${rs.length === 1 ? '' : 'es'} · ${fmtBytes(rs.reduce((n, r) => n + (r.diskBytes || 0), 0))} on disk</span>
        </td></tr>
        ${rs.map((r) => this._row(r))}`)}
    </table>`;
  }

  _row(r) {
    return html`<tr data-tsbx-row=${r.tile + ':' + r.name} data-state=${r.state}>
      <td class="mono" title=${r.uid ? `uid ${r.uid}` : ''}>${r.name}</td>
      <td title=${r.stateDetail || ''}>${r.state}${r.stateDetail ? html` <span class="muted sbx-err" data-tsbx-detail>· ${r.stateDetail}</span>` : nothing}</td>
      <td><span class="sbx-mode ${r.mode}">${MODE[r.mode] || r.mode}</span>${r.accel === 'emulate' ? html` <span class="pill">emulated</span>` : nothing}</td>
      <td class="mono">${mib(r.memMiB)} · ${r.vcpus} vCPU · ${r.diskGiB} GiB</td>
      <td class="num">${r.diskBytes ? fmtBytes(r.diskBytes) : '—'}</td>
      <td class="mono" title="the manager's claims: shown, never trusted">${r.for || ''}${r.forUser ? ` · ${r.forUser}` : ''}</td>
      <td class="mono">${r.lastActive ? ago(r.lastActive) : '—'}</td>
      <td style="white-space:nowrap">
        ${LIVE.has(r.state) ? html`<button class="act" data-tsbx-stop ?disabled=${this._busy} @click=${() => this._stop(r)}>stop</button>` : nothing}
        <button class="act" data-tsbx-delete ?disabled=${this._busy} @click=${() => this._delete(r)}>delete</button>
      </td>
    </tr>`;
  }

  _path(r, verb = '') {
    return `/sandboxes/${encodeURIComponent(r.name)}${verb}?tile=${encodeURIComponent(r.tile)}`;
  }

  async _stop(r) {
    this._busy = true;
    try {
      await api(this._path(r, '/stop'), { method: 'POST' });
      this._ok();
      this._emit('bx-admin-notice', `stopped ${r.name} of ${r.tile} (its state is kept)`);
    } catch (e) { this._fail(e); }
    this._busy = false;
    await this.reload?.();
  }

  async _delete(r) {
    if (!confirm(`Delete the tile sandbox ${r.name} of ${r.tile}? Everything installed or written in it is removed; this can't be undone.`)) return;
    this._busy = true;
    try {
      await api(this._path(r), { method: 'DELETE' });
      this._ok();
      this._emit('bx-admin-notice', `deleted ${r.name} of ${r.tile}`);
    } catch (e) { this._fail(e); }
    this._busy = false;
    await this.reload?.();
  }
}
customElements.define('bx-admin-tile-sandboxes', BxAdminTileSandboxes);
