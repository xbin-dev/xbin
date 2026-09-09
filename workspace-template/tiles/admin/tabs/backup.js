/**
 * <bx-admin-backup> — the admin console's backup tab: the default archiver,
 * per-component archiver override, guided lifecycle (disable → back up →
 * offload), schedules, versions, restore. A tab element of tiles/admin (see
 * admin.js): the roster arrives as a property; bindings, schedules and
 * versions it loads itself; writes report through bx-admin-err /
 * bx-admin-refresh.
 */
import { LitElement, html, nothing } from 'lit';
import { xbinApi as api, jbody } from '/vendor/bx-kit.js';
import { base } from '../admin-css.js';
import { fmtBytes, setLifecycle } from '../shared.js';

export class BxAdminBackup extends LitElement {
  static properties = {
    components: { attribute: false }, // the roster (path, state, stateAt …)
    _ifaces: { state: true },    // /bindings (archivers + @archive bindings)
    _schedules: { state: true }, // [{component, schedule, retention}]
    _versions: { state: true },  // comp → [{version, time, size}] (lazy)
    _verOpen: { state: true },   // comps whose version list is expanded
    _busy: { state: true },      // comp mid heavy op
    _err: { state: true }, // the last refusal (reported to the router's slot)
  };
  static styles = [base];

  _emit(type, detail) { this.dispatchEvent(new CustomEvent(type, { detail, bubbles: true, composed: true })); }
  // fail(e) reports a refusal to the router's global slot; ok() clears it.
  _fail(e) { this._err = String(e?.message ?? e); this._emit('bx-admin-err', this._err); }
  _ok() { this._err = ''; this._emit('bx-admin-err', ''); }

  constructor() {
    super();
    this._schedules = []; this._versions = {}; this._verOpen = new Set(); this._busy = null;
  }
  connectedCallback() {
    super.connectedCallback();
    this.load();
  }
  refresh() { return this.load(); }

  // Lifecycle from the backup table: the shared confirm + write, then the
  // roster (router) and this tab's schedules/versions reload.
  async _setLifecycle(path, state) {
    this._busy = path;
    try {
      if (await setLifecycle(path, state)) { this._emit('bx-admin-refresh'); await this.load(); }
      else this.requestUpdate(); // declined: re-render reverts the control
    } catch (e) { this._fail(e); }
    finally { this._busy = null; }
  }

  async load() {
    try {
      const [ifaces, sched] = await Promise.all([api('/bindings'), api('/backup-schedule')]);
      this._ifaces = ifaces;
      this._schedules = sched.schedules || [];
      this._ok();
      // Load versions for disabled components so the offload gate is computable
      // (does a post-disable backup exist?) without expanding each one.
      const disabled = (this.components || []).filter((c) => c.state === 'disabled');
      await Promise.all(disabled.map((c) => this._loadVersions(c.path)));
    } catch (e) { this._fail(e); }
  }

  // Components that provide an `archive` interface (candidate archivers).
  _archivers() {
    const out = [];
    for (const c of (this._ifaces?.components || []))
      for (const def of Object.values(c.provides || {}))
        if (def.kind === 'archive') out.push(c.component);
    return out;
  }

  // '*' sets the workspace default; provider '' clears an override.
  async _setArchiver(comp, provider) {
    try {
      const body = JSON.stringify(provider ? { component: comp, slot: '@archive', provider } : { component: comp, slot: '@archive' });
      await api('/bindings', { method: provider ? 'POST' : 'DELETE', headers: { 'Content-Type': 'application/json' }, body });
      await this.load();
    } catch (e) { this._fail(e); }
  }

  async _toggleVersions(comp) {
    const s = new Set(this._verOpen);
    if (s.has(comp)) { s.delete(comp); this._verOpen = s; return; }
    s.add(comp); this._verOpen = s;
    await this._loadVersions(comp);
  }
  async _loadVersions(comp) {
    try {
      const d = await api('/backups?component=' + encodeURIComponent(comp));
      this._versions = { ...this._versions, [comp]: d.versions || [] };
    } catch (e) { this._versions = { ...this._versions, [comp]: [] }; this._fail(e); }
  }

  async _backupNow(comp) {
    this._busy = comp;
    try {
      await api('/backup', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ component: comp }) });
      this._verOpen = new Set(this._verOpen).add(comp);
      await this._loadVersions(comp);
    } catch (e) { this._fail(e); }
    finally { this._busy = null; }
  }

  async _restoreVersion(comp, version) {
    if (!confirm(`Restore ${comp} from ${version}? This replaces its current data/source.`)) return;
    this._busy = comp;
    try {
      await api('/restore', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ component: comp, version }) });
      this._emit('bx-admin-refresh');
      await this.load();
    } catch (e) { this._fail(e); }
    finally { this._busy = null; }
  }

  // Restore one file from a version — streamed back and offered as a download.
  async _restoreFile(comp, version) {
    const path = prompt('File path within the archive (e.g. source/index.html or data/kv.json):');
    if (!path) return;
    try {
      const r = await xbin.fetch('/api/xbin/restore', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ component: comp, version, file: path }),
      });
      if (!r.ok) throw new Error((await r.json()).error || r.status);
      xbin.download(path.split('/').pop() || 'file', await r.blob());
    } catch (e) { this._fail(e); }
  }

  async _setSchedule(comp, every, keep) {
    if (!every.trim()) return;
    try {
      await api('/backup-schedule', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ component: comp, schedule: '@every ' + every.trim(), retention: parseInt(keep, 10) || 0 }) });
      await this.load();
    } catch (e) { this._fail(e); }
  }
  async _clearSchedule(comp) {
    try { await api('/backup-schedule?component=' + encodeURIComponent(comp), { method: 'DELETE' }); await this.load(); }
    catch (e) { this._fail(e); }
  }

  render() {
    const ov = this.components ? { components: this.components } : null, ifaces = this._ifaces;
    if (!ov || !ifaces) return html`<div class="muted">loading…</div>`;
    const archivers = this._archivers();
    if (archivers.length === 0)
      return html`<p class="muted">No archiver installed. Import the <b>S3 Archiver</b> tile (or another
        <code>archive</code> provider) from the Tile Manager, then pick it as the default below.</p>`;
    const defArch = ifaces.bindings?.['*']?.['@archive'] || '';
    const comps = (ov.components || []).filter((c) => !archivers.includes(c.path)); // an archiver isn't its own target
    const schedFor = (p) => this._schedules.find((s) => s.component === p);
    return html`
      <p class="muted">Back up a component (its source + data + terminal layer) to an archiver, offload to
        free disk, or restore a version/file. Vault is not backed up. See
        <a href="/docs/overview/14-lifecycle.md" target="_blank">the lifecycle overview</a>.</p>
      <h3>Default archiver</h3>
      <select @change=${(e) => this._setArchiver('*', e.target.value)}>
        <option value="" ?selected=${!defArch}>— none —</option>
        ${archivers.map((a) => html`<option value=${a} ?selected=${defArch === a}>${a}</option>`)}
      </select>
      <span class="muted" style="margin-left:8px">used unless a component overrides it</span>

      <h3>Components</h3>
      <table class="tbl">
        <tr><th>component</th><th>lifecycle</th><th>archiver</th><th>schedule</th><th></th></tr>
        ${comps.map((c) => this._backupRow(c, archivers, defArch, schedFor(c.path)))}
      </table>`;
  }

  _backupRow(c, archivers, defArch, sched) {
    const override = this._ifaces.bindings?.[c.path]?.['@archive'] || '';
    const busy = this._busy === c.path;
    const open = this._verOpen.has(c.path);
    return html`
      <tr>
        <td class="mono">${c.path}</td>
        <td>${this._lifecycleControls(c)}</td>
        <td><select @change=${(e) => this._setArchiver(c.path, e.target.value)}>
          <option value="" ?selected=${!override}>default${defArch ? ' (' + defArch + ')' : ''}</option>
          ${archivers.map((a) => html`<option value=${a} ?selected=${override === a}>${a}</option>`)}
        </select></td>
        <td class="mono">${sched
          ? html`${sched.schedule}${sched.retention ? ' ·keep ' + sched.retention : ''}
              <a class="link" title="remove schedule" @click=${() => this._clearSchedule(c.path)}>✕</a>`
          : this._scheduleForm(c.path)}</td>
        <td style="white-space:nowrap">
          <a class="link" @click=${() => !busy && this._backupNow(c.path)}>${busy ? 'working…' : 'back up'}</a>
          · <a class="link" @click=${() => this._toggleVersions(c.path)}>versions${open ? ' ▾' : ''}</a>
        </td>
      </tr>
      ${open ? html`<tr><td colspan="5">${this._versionsList(c.path)}</td></tr>` : nothing}`;
  }

  // Guided lifecycle controls (docs/overview/14-lifecycle.md). Offload is deliberately a
  // two-step, safe flow: you must DISABLE first (stops the backend → a consistent
  // db), then take a backup, and only then does offload un-gray — so you never
  // free local data without a verified, stopped-state snapshot.
  _lifecycleControls(c) {
    const st = c.state || 'enabled';
    const busy = this._busy === c.path;
    const act = (label, state, opts = {}) => html`<a
      class="link ${opts.gated ? 'gated' : ''}" title=${opts.title || ''}
      @click=${() => !busy && !opts.gated && this._setLifecycle(c.path, state)}>${busy ? '…' : label}</a>`;

    if (st === 'enabled') return html`enabled · ${act('disable', 'disabled')}`;
    if (st === 'disabled') {
      const ready = this._hasPostDisableBackup(c);
      const why = ready ? 'Archive + remove local data (source kept).'
        : 'Back up first — offload needs a backup taken while disabled (a consistent snapshot).';
      const whyFull = ready ? 'Archive + remove data AND source.' : why;
      return html`disabled · ${act('enable', 'enabled')}
        · ${act('offload', 'offloaded', { gated: !ready, title: why })}
        · ${act('offload+src', 'offloaded-full', { gated: !ready, title: whyFull })}
        ${ready ? nothing : html`<span class="muted" style="font-size:11px"> (back up to enable offload)</span>`}`;
    }
    // offloaded / offloaded-full
    return html`${st} · ${act('restore', 'enabled')}`;
  }

  // Offload is allowed only once a backup exists that was taken AFTER the tile was
  // disabled (its snapshot is consistent because the backend is stopped).
  _hasPostDisableBackup(c) {
    if ((c.state || 'enabled') !== 'disabled' || !c.stateAt) return false;
    const since = Date.parse(c.stateAt);
    const vers = this._versions[c.path];
    return Array.isArray(vers) && vers.some((v) => Date.parse(v.time) >= since);
  }

  _scheduleForm(comp) {
    return html`<span>
      <input class="ev" placeholder="24h" style="width:48px">
      <input class="kp" placeholder="keep" style="width:44px">
      <a class="link" @click=${(e) => { const s = e.target.parentElement; this._setSchedule(comp, s.querySelector('.ev').value, s.querySelector('.kp').value); }}>set</a>
    </span>`;
  }

  _versionsList(comp) {
    const vers = this._versions[comp];
    if (!vers) return html`<span class="muted">loading…</span>`;
    if (vers.length === 0) return html`<span class="muted">no backups yet</span>`;
    return html`<table class="tbl" style="margin:2px 0 4px 16px">
      ${vers.map((v) => html`<tr>
        <td class="mono">${v.version}</td>
        <td class="muted">${v.time}</td>
        <td class="mono">${fmtBytes(v.size)}</td>
        <td style="white-space:nowrap">
          <a class="link" @click=${() => this._restoreVersion(comp, v.version)}>restore</a>
          · <a class="link" @click=${() => this._restoreFile(comp, v.version)}>file…</a>
        </td>
      </tr>`)}
    </table>`;
  }
}

customElements.define('bx-admin-backup', BxAdminBackup);
