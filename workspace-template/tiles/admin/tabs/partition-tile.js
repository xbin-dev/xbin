/**
 * <bx-admin-partition-tile tile="apps/x"> — one partitioned tile in the
 * admin console's runtime → partitions view (partitions.js;
 * docs/partitions.md §Operating people's partitions): what GET /api/xbin/partitions?tile= answers an admin, and the
 * acts on it, each a person's act through the admin tile's frame
 * (AdminFrameDriver: xbind judges the person driving it; a driver who isn't
 * an admin reads the tile's state only — its mode and request, which a
 * manager decides here too):
 *
 *   - the mode, and an open or declined request: Keep the current mode, or
 *     Switch… after a dry run and the typed tile path (POST
 *     /partitions/mode — a manager's decision);
 *   - "reviewed code only" (POST /partitions/reviewed);
 *   - the limits (POST /partitions/limits);
 *   - people's metadata rows — never what a partition holds: stop (POST
 *     /partitions/stop), reset after the typed "<tile> user:<id>" (POST
 *     /partitions/reset), restore from a backup of that partition (GET
 *     /partitions/backups, POST /partitions/restore — audited, the person
 *     is told);
 *   - the personal binds on the tile (DELETE /partitions/binds: admins list
 *     and delete the records, never what they reach);
 *   - its orphaned partitions, the global inbox's counts and the mode
 *     history (the backup keys erased, the partitions restored).
 *
 * Confirmations are inline (the view works outside the shell too). Emits
 * bx-admin-partitions-changed after an act so the list reloads its totals.
 */
import { LitElement, html, css, nothing } from 'lit';
import { xbinApi as api, jbody } from '/vendor/bx-kit.js';
import { base } from '../admin-css.js';
import { WithRouter } from '../shared.js';
import {
  modeName, stateWords, requestText, switchLines, modeBody, deletesNothing, resetConfirm, partitionOp,
  bytesText, regsText, mailText, runningText, shortId, when, historyText, limitsBody, orphanWhy,
} from './partitions-view.js';

// The view's styles (partitions.js adopts them too).
export const partitionsCss = css`
  .pt-intro { max-width: 760px; }
  .pt-warn { display: flex; gap: 6px; align-items: baseline; color: var(--bx-warn); margin: 4px 0 8px; }
  table.pt td, table.pt th { padding-right: 12px; }
  table.pt td.num, table.pt th { white-space: nowrap; }
  tr.pt-row { cursor: pointer; }
  tr.pt-row:hover td { background: var(--bx-hover); }
  tr.pt-row.open td { background: var(--bx-selection); }
  td.pt-detail { padding: 6px 0 12px 18px; border-top: 0; }
  /* a partition mode: a square badge in the partition marker's colour */
  .pt-mode { box-sizing: border-box; display: inline-flex; align-items: center; height: 20px; padding: 0 6px;
    font: var(--bx-font-meta); border-radius: var(--bx-radius); border: 1px solid var(--bx-part); color: var(--bx-part); }
  .pt-state.pending, .pt-state.invalid { color: var(--bx-warn); }
  .pt-note { display: block; font: var(--bx-font-meta); }
  .pt-note.warn { color: var(--bx-warn); }
  .pt-note.ok { color: var(--bx-ok); }
  .pt-note.info { color: var(--bx-muted); }
  /* a confirmation that deletes or replaces: the warning tint, its border */
  .pt-ask { margin: 8px 0; padding: 8px 12px; border-radius: var(--bx-radius); max-width: 760px;
    background: var(--bx-warn-bg); border: 1px solid var(--bx-warn); }
  .pt-ask .row { display: flex; gap: 8px; margin-top: 8px; align-items: center; flex-wrap: wrap; }
  .pt-ask ul { margin: 4px 0 0; padding-left: 18px; }
  .pt-bar { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin: 10px 0 4px; }
`;

export class BxAdminPartitionTile extends WithRouter(LitElement) {
  static properties = {
    tile: {},
    _d: { state: true },       // GET /partitions?tile= (undefined: loading)
    _ask: { state: true },     // the open confirmation: {kind: switch|reset|restore, …}
    _limits: { state: true },  // the limits draft (null: not editing)
    _busy: { state: true },
    _err: { state: true },
  };
  static styles = [base, partitionsCss, css`
    :host { display: block; }
    .grid { display: flex; gap: 18px; flex-wrap: wrap; align-items: flex-start; }
    .grid > div { min-width: 240px; }
    .line { margin: 2px 0; }
    table.pt td.acts { white-space: nowrap; }
    input.typed { min-width: 18em; }
    .hist { width: auto; }
    .hist td { height: auto; padding: 2px 12px 2px 0; }
  `];

  constructor() { super(); this._ask = null; this._limits = null; this._busy = false; }

  connectedCallback() {
    super.connectedCallback();
    this.load();
    this._off = window.xbin?.events.on((e) => { if (e.type === 'partitions' || e.type === 'reload') this._soon(); });
  }
  disconnectedCallback() { super.disconnectedCallback(); clearTimeout(this._t); this._off?.(); }
  _soon() { clearTimeout(this._t); this._t = setTimeout(() => this.load(), 300); }

  async load() {
    if (!this.tile) return;
    try { this._d = await api(`/partitions?tile=${encodeURIComponent(this.tile)}`); } catch (e) { this._fail(e); }
  }

  // _act runs one write, says so, and reloads this tile and the list.
  async _act(path, body, notice, { method = 'POST', keepAsk = false } = {}) {
    this._busy = true;
    let out = null;
    try {
      out = await api(path, jbody(body, method));
      this._ok();
      if (notice) this._emit('bx-admin-notice', typeof notice === 'function' ? notice(out) : notice);
      if (!keepAsk) this._ask = null;
    } catch (e) { this._fail(e); }
    this._busy = false;
    await this.load();
    this._emit('bx-admin-partitions-changed');
    return out;
  }

  // ---- the mode ----
  async _dryRun() {
    const d = this._d;
    this._busy = true;
    try {
      const dry = await api('/partitions/mode', jbody(modeBody(this.tile, d, 'switch', { dryRun: true }), 'POST'));
      this._ok();
      this._ask = { kind: 'switch', dry, typed: '', yes: false };
    } catch (e) { this._fail(e); }
    this._busy = false;
  }

  async _switch() {
    const a = this._ask, d = this._d;
    if (a.typed.trim() !== this.tile) { this._ask = { ...a, error: `Type the tile's path exactly (${this.tile}) to switch.` }; return; }
    if (a.dry?.managers?.length && !a.yes) { this._ask = { ...a, error: 'Tick "switch anyway" to switch with those sandbox managers bound.' }; return; }
    await this._act('/partitions/mode', modeBody(this.tile, d, 'switch', a.yes ? { confirm: this.tile, yes: true } : { confirm: this.tile }),
      (r) => `switched ${this.tile} to ${modeName(r?.to ?? d.request?.spec)}${r?.deletes && !String(r.deletes).startsWith('nothing') ? `: ${r.deletes} deleted` : ''}`);
  }

  _mode(d) {
    const q = d.request, from = d.spec, to = q?.spec;
    const a = this._ask?.kind === 'switch' ? this._ask : null;
    return html`<div data-pt-mode=${d.state}>
      <div class="line">mode <span class="pt-mode">${modeName(from)}</span>
        <span class="pt-state ${d.state}">· ${stateWords(d.state)}</span></div>
      ${d.error ? html`<div class="pt-note warn">${d.error}</div>` : nothing}
      ${q ? html`<div class="line ${q.declined ? 'muted' : 'pt-note warn'}" data-pt-request>${requestText(this.tile, d)}</div>
        <div class="pt-bar">
          ${q.declined ? nothing : html`<button class="act" data-pt-keep ?disabled=${this._busy || !!a}
            @click=${() => this._act('/partitions/mode', modeBody(this.tile, d, 'keep'), `kept ${modeName(from)}: ${this.tile} runs again, nothing was deleted`)}>Keep the current mode</button>`}
          <button class="act ${deletesNothing(from, to) ? '' : 'rm'}" data-pt-switch ?disabled=${this._busy || !!a} @click=${() => this._dryRun()}>Switch…</button>
        </div>` : nothing}
      ${a ? html`<div class="pt-ask" data-pt-switch-confirm>
        ${switchLines(this.tile, d, a.dry).map((l) => html`<div>${l}</div>`)}
        ${a.error ? html`<div class="err">${a.error}</div>` : nothing}
        <div class="row">
          <input class="typed mono" data-pt-typed placeholder=${this.tile} .value=${a.typed} aria-label=${`type ${this.tile} to confirm`}
            @input=${(e) => { this._ask = { ...a, typed: e.target.value, error: '' }; }}>
          ${a.dry?.managers?.length ? html`<label><input type="checkbox" .checked=${a.yes} @change=${(e) => { this._ask = { ...a, yes: e.target.checked }; }}> switch anyway</label>` : nothing}
          <button class="act ${deletesNothing(from, to) ? 'go' : 'rm'}" data-pt-switch-go
            ?disabled=${this._busy || a.typed.trim() !== this.tile || (a.dry?.managers?.length && !a.yes)} @click=${() => this._switch()}>
            ${deletesNothing(from, to) ? 'Switch' : 'Switch and delete all data'}</button>
          <button class="act" @click=${() => { this._ask = null; }}>cancel</button>
        </div>
      </div>` : nothing}
    </div>`;
  }

  // ---- reviewed code only, and the limits ----
  _reviewed(d) {
    const ro = d.reviewedOnly;
    if (!ro) return nothing; // an older xbind
    return html`<div data-pt-reviewed=${ro.on ? 'on' : 'off'}>
      <label class="line"><input type="checkbox" data-pt-reviewed-switch .checked=${!!ro.on} ?disabled=${this._busy || d.state !== 'partitioned'}
        @change=${(e) => { const on = e.target.checked; e.target.checked = !!ro.on; this._act('/partitions/reviewed', { tile: this.tile, on }, on ? `${this.tile} runs reviewed code only` : `${this.tile}: reviewed code only is off`); }}>
        <b>reviewed code only</b></label>
      <div class="muted line">Its primary, and every provider bound to it that isn't partitioned, must be protected: code then moves only
        by a manager naming the reviewed checkpoint (<a href="/docs/tile-deployments.md" target="_blank">tile-deployments</a>).</div>
      ${ro.on && ro.by ? html`<div class="muted line">on since ${when(ro.at)}, by ${ro.by}</div>` : nothing}
      ${ro.error ? html`<div class="pt-note warn">${ro.error}</div>` : nothing}
      ${ro.unprotected?.length ? html`<div class="pt-note warn">not protected: ${ro.unprotected.join(', ')}</div>` : nothing}
    </div>`;
  }

  _limitsView(d) {
    const l = d.limits;
    if (!l) return nothing;
    const dr = this._limits;
    return html`<div data-pt-limits>
      <div class="line"><b>limits</b> — people's instances running at once: <span class="mono">${l.maxRunning || 'no cap'}</span> ·
        data per partition: <span class="mono">${l.partitionBytes ? bytesText(l.partitionBytes) : 'no ceiling'}</span>
        ${dr ? nothing : html`<button class="act" data-pt-limits-edit @click=${() => { this._limits = { maxRunning: '', partitionMiB: '' }; }}>edit</button>`}</div>
      ${dr ? html`<form class="inline" @submit=${(e) => { e.preventDefault(); this._saveLimits(); }}>
        <label>at once <input type="number" min="0" name="maxRunning" placeholder=${String(l.maxRunning || '')} .value=${dr.maxRunning}
          @input=${(e) => { this._limits = { ...dr, maxRunning: e.target.value }; }}></label>
        <label>MiB per partition <input type="number" min="0" name="partitionMiB" placeholder=${l.partitionBytes ? String(Math.round(l.partitionBytes / 1048576)) : ''}
          .value=${dr.partitionMiB} @input=${(e) => { this._limits = { ...dr, partitionMiB: e.target.value }; }}></label>
        <button class="act go" type="submit" ?disabled=${this._busy}>save</button>
        <button class="act" type="button" @click=${() => { this._limits = null; }}>cancel</button>
        <span class="muted hint">empty: unchanged · 0: back to the default</span>
      </form>` : nothing}
    </div>`;
  }

  async _saveLimits() {
    const body = limitsBody(this.tile, this._limits);
    if (body.maxRunning === undefined && body.partitionBytes === undefined) { this._limits = null; return; }
    await this._act('/partitions/limits', body, `${this.tile}: limits saved`);
    if (!this._err) this._limits = null;
  }

  // ---- people ----
  async _openRestore(row) {
    this._busy = true;
    try {
      const b = await api(`/partitions/backups?tile=${encodeURIComponent(this.tile)}&user=${encodeURIComponent(row.user)}`);
      this._ok();
      this._ask = { kind: 'restore', row, versions: b.versions || [], version: b.versions?.[0]?.version || '', typed: '' };
    } catch (e) { this._fail(e); }
    this._busy = false;
  }

  _personAsk(row) {
    const a = this._ask;
    if (!a || a.row?.partitionId !== row.partitionId) return nothing;
    const want = resetConfirm(this.tile, row.user);
    const typed = html`<input class="typed mono" data-pt-typed placeholder=${want} .value=${a.typed}
      @input=${(e) => { this._ask = { ...a, typed: e.target.value }; }}>`;
    if (a.kind === 'reset') {
      return html`<tr><td colspan="9"><div class="pt-ask" data-pt-reset-confirm=${row.user}>
        Reset ${row.user}'s partition of ${this.tile}: its data, vault, registrations, ledger, log, mail, terminal layers and agent-session
        history are deleted in every deployment, and its backup keys erased. ${row.user} is told. Type <span class="mono">${want}</span> to confirm.
        <div class="row">${typed}
          <button class="act rm" data-pt-reset-go ?disabled=${this._busy || a.typed.trim() !== want}
            @click=${() => this._act('/partitions/reset', partitionOp(this.tile, row, { confirm: want }), `reset ${row.user}'s partition of ${this.tile}`)}>Reset</button>
          <button class="act" @click=${() => { this._ask = null; }}>cancel</button></div>
      </div></td></tr>`;
    }
    return html`<tr><td colspan="9"><div class="pt-ask" data-pt-restore-confirm=${row.user}>
      ${a.versions.length ? html`Replace ${row.user}'s partition of ${this.tile} — its data, vault and registrations — with a backup of it.
        The restore is audited and ${row.user} is told. Type <span class="mono">${want}</span> to confirm.
        <div class="row"><select data-pt-version @change=${(e) => { this._ask = { ...a, version: e.target.value }; }}>
            ${a.versions.map((v) => html`<option value=${v.version} ?selected=${v.version === a.version}>${when(v.time)} · ${bytesText(v.size)}</option>`)}
          </select>${typed}
          <button class="act rm" data-pt-restore-go ?disabled=${this._busy || a.typed.trim() !== want}
            @click=${() => this._act('/partitions/restore', { tile: this.tile, user: row.user, version: a.version, confirm: want }, `restored ${row.user}'s partition of ${this.tile}`)}>Restore</button>
          <button class="act" @click=${() => { this._ask = null; }}>cancel</button></div>`
        : html`No backup of ${row.user}'s partition of ${this.tile} yet. <button class="act" @click=${() => { this._ask = null; }}>close</button>`}
    </div></td></tr>`;
  }

  _people(d) {
    if (d.partitions === undefined) return html`<p class="muted" data-pt-people="hidden">Who holds a partition of ${this.tile} is an admin's to see.</p>`;
    const rows = d.partitions || [];
    if (!rows.length) return html`<p class="muted" data-pt-people="0">Nobody holds a partition of ${this.tile} yet: a person's starts on their first use.</p>`;
    return html`<table class="pt" data-pt-people=${rows.length}>
      <tr><th>person</th><th>partition</th><th>state</th><th>instance</th><th>data</th><th>registrations</th><th>inbox</th><th>log</th><th></th></tr>
      ${rows.map((r) => html`<tr data-pt-person=${r.user} data-state=${r.state}>
          <td class="mono">${r.user}</td>
          <td class="mono" title=${r.partitionId}>${shortId(r.partitionId)}</td>
          <td>${r.state}${r.why ? html`<span class="pt-note info">${r.state === 'orphaned' ? orphanWhy(r.why) : r.why}</span>` : nothing}</td>
          <td>${runningText(r)}${r.lastStarted ? html`<span class="pt-note info">last started ${when(r.lastStarted)}</span>` : nothing}
            ${r.lastExit ? html`<span class="pt-note info">last exit ${when(r.lastExit)}${r.restarts ? ` · ${r.restarts} restarts` : ''}</span>` : nothing}</td>
          <td class="num">${bytesText(r.bytes)}</td>
          <td>${regsText(r.registrations) || html`<span class="muted">—</span>`}</td>
          <td data-pt-mail>${mailText(r.mail) || html`<span class="muted">—</span>`}</td>
          <td>${r.logShare?.until ? html`<span title="read it in the tile's logs panel in the shell">shared until ${when(r.logShare.until)}</span>` : html`<span class="muted">private</span>`}</td>
          <td class="acts">${r.state === 'orphaned' ? nothing : html`
            ${r.running ? html`<button class="act quiet" data-pt-stop ?disabled=${this._busy}
              @click=${() => this._act('/partitions/stop', partitionOp(this.tile, r), `stopped ${r.user}'s instance of ${this.tile}`)}>stop</button>` : nothing}
            <button class="act quiet rm" data-pt-reset ?disabled=${this._busy} @click=${() => { this._ask = { kind: 'reset', row: r, typed: '' }; }}>reset…</button>
            <button class="act quiet" data-pt-restore ?disabled=${this._busy} @click=${() => this._openRestore(r)}>restore…</button>`}</td>
        </tr>${this._personAsk(r)}`)}
    </table>`;
  }

  // ---- personal binds, orphans, history ----
  _binds(d) {
    const rows = d.binds || [];
    if (!rows.length) return nothing;
    return html`<h4>personal binds</h4>
      <p class="muted line">A person wires a tile they own into their own partition only. You can remove the record; what it reaches stays theirs.</p>
      <table class="pt" data-pt-binds=${rows.length}>
        <tr><th>person</th><th>slot</th><th>provider</th><th>state</th><th>since</th><th></th></tr>
        ${rows.map((b) => html`<tr data-pt-bind=${b.id}>
          <td class="mono">${b.user}</td><td class="mono">${b.slot}</td><td class="mono">${b.provider}</td>
          <td>${b.live ? 'live' : html`<span class="pt-note warn">${b.why || 'not live'}</span>`}</td>
          <td class="mono">${when(b.at)}</td>
          <td><button class="act quiet rm" data-pt-unbind ?disabled=${this._busy}
            @click=${() => this._act('/partitions/binds', { id: b.id, user: b.user }, `removed ${b.user}'s personal bind ${b.slot} → ${b.provider}`, { method: 'DELETE' })}>remove</button></td>
        </tr>`)}
      </table>`;
  }

  _history(d) {
    const h = d.history || [];
    if (!h.length && !d.lastWipe) return nothing;
    return html`<h4>mode history</h4>
      ${d.lastWipe ? html`<div class="line muted" data-pt-lastwipe>last switch that deleted data: ${modeName(d.lastWipe.from)} → ${modeName(d.lastWipe.to)}, ${when(d.lastWipe.at)}
        (a workspace backup of ${this.tile} from before it restores only with that date typed; a person's partition restores as above)</div>` : nothing}
      <table class="hist" data-pt-history=${h.length}>${h.slice(0, 20).map((e) => html`<tr><td class="mono">${when(e.at)}</td><td>${historyText(e)}</td></tr>`)}</table>`;
  }

  render() {
    const d = this._d;
    if (d === undefined) return html`<div class="muted">loading ${this.tile}…</div>`;
    const tot = d.totals || {};
    return html`<div data-pt-view=${this.tile}>
      <div class="grid">
        <div>${this._mode(d)}</div>
        <div>${this._reviewed(d)}</div>
      </div>
      ${this._limitsView(d)}
      <div class="cards" style="margin-top:8px">
        ${[['people', tot.people], ['running', tot.running], ['data', tot.bytes === undefined ? undefined : bytesText(tot.bytes)], ['cron jobs', tot.cron], ['bus subs', tot.bus]]
          .filter(([, v]) => v !== undefined).map(([l, v]) => html`<div class="stat"><div class="n">${v}</div><div class="l">${l}</div></div>`)}
        ${d.globalMail ? html`<div class="stat" data-pt-global-mail title="the global instance's inbox: counts only"><div class="n">${d.globalMail.pending}</div><div class="l">global inbox</div></div>` : nothing}
      </div>
      <h4>people</h4>
      ${this._people(d)}
      ${this._binds(d)}
      ${(d.orphans || []).length ? html`<h4>orphaned here</h4><div class="muted line" data-pt-tile-orphans=${d.orphans.length}>
        ${d.orphans.map((o) => `${o.user} · ${shortId(o.partition)} (${orphanWhy(o.reason)})`).join(' · ')} — purge them in the list below.</div>` : nothing}
      ${this._history(d)}
    </div>`;
  }
}
customElements.define('bx-admin-partition-tile', BxAdminPartitionTile);
