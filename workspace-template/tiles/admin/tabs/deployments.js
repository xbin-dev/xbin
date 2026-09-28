/**
 * <bx-admin-deployments> — the runtime → deployments tab (D127m, extended by
 * the owner 2026-09-28; docs/tile-deployments.md "Managing protection"):
 * every tile with a deployment record — its primary, whether the primary is
 * protected, where live reload is, its deployments and the last deploy —
 * with a tile manager's acts on it: protect or unprotect the primary,
 * reassign it (behind the loud confirmation the terminal window shows), and
 * deliveries and alwaysOn per non-primary deployment. Everything else is the
 * tile's Deployments panel, which the row's link opens in the shell
 * (xbin:open-deployments, docs/protocol.md).
 *
 * The acts go through this tile's frame, which stands in for the signed-in
 * person at the manager gate (docs/auth.md): the server judges the person,
 * so a tile they don't manage reads the reader view, every act disabled with
 * the server's reason. The words and the confirmations are the terminal
 * window's own (/vendor/deploy-state.js, /vendor/deploy-panel.js): a dry run
 * of the exact request renders the confirmation, and the confirmed request
 * carries the dry run's seq and the checkpoint the dialog showed.
 */
import { LitElement, html, nothing } from 'lit';
import { sandboxed } from '/vendor/bx-kit.js';
import * as ds from '/vendor/deploy-state.js';
import * as dp from '/vendor/deploy-panel.js';
import { base, deploymentsCss } from '../admin-css.js';
import { WithFilter, WithRouter } from '../shared.js';

// The acts this tab does, and their routes under /api/xbin/deployments.
const ROUTE = { primary: 'primary', protect: 'protect', unprotect: 'protect', deliveries: 'deliveries', alwaysOn: 'always-on' };
const TILE_ACTS = new Set(['reassign', 'protect', 'unprotect']);
const DEP_ACTS = new Set(['deliveries', 'alwaysOn']);

// call(path, body?) → {status, body} or {status, error}: GET without a body,
// POST with one, through this document's credential (bx-kit's split: the
// frame token in a sandboxed tile), keeping the status a conflict needs.
async function call(path, body) {
  const f = sandboxed() && window.xbin?.fetch ? window.xbin.fetch : fetch;
  const init = body === undefined ? { cache: 'no-store' }
    : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) };
  try {
    const r = await f('/api/xbin' + path, init);
    const j = await r.json().catch(() => null);
    return r.ok ? { status: r.status, body: j } : { status: r.status, error: j?.error || `error ${r.status}` };
  } catch (e) {
    return { status: 0, error: String(e?.message ?? e) };
  }
}

// lastDeploy(state) → the newest deploy-log entry the state's rows carry
// ({how, result, by, at}), naming its row's deployment.
function lastDeploy(s) {
  const at = (e) => Date.parse(e.at || e.finishedAt || e.requestedAt || '') || 0;
  return (s?.deployments || []).map((d) => d.lastDeploy && { ...d.lastDeploy, deployment: d.lastDeploy.deployment || d.name,
    at: d.lastDeploy.at || d.lastDeploy.finishedAt || d.lastDeploy.requestedAt || '' })
    .filter(Boolean).sort((a, b) => at(b) - at(a))[0] || null;
}

export class BxAdminDeployments extends WithFilter(WithRouter(LitElement)) {
  static properties = {
    _tiles: { state: true },  // the paths of tiles with a deployment record (/components' summary)
    _states: { state: true }, // path → the deployments state in this frame's view, or {error}
    _busy: { state: true },   // the tile an act is running on
    _said: { state: true },   // path → the last act's result line, or {error}
    _q: { state: true },
    _cats: { state: true },
    _err: { state: true },
  };
  static styles = [base, deploymentsCss];

  constructor() {
    super();
    this._tiles = null; this._states = {}; this._said = {}; this._q = ''; this._cats = new Set();
  }
  connectedCallback() {
    super.connectedCallback();
    this.refresh();
    // A record changes: that tile's state again (a tile gaining one: the list).
    this._off = window.xbin?.events.on((e) => {
      if (e.type !== 'deployments' && e.type !== 'reload') return;
      clearTimeout(this._t);
      this._t = setTimeout(() => (e.type === 'deployments' && this._tiles?.includes(e.component) ? this._load(e.component) : this.refresh()), 250);
    });
  }
  disconnectedCallback() { super.disconnectedCallback(); this._off?.(); clearTimeout(this._t); }

  async refresh() {
    const r = await call('/components');
    if (r.error) { this._fail(r.error); return; }
    const tiles = (Array.isArray(r.body) ? r.body : []).filter((c) => c.deployments).map((c) => c.path).sort();
    const states = {};
    await Promise.all(tiles.map(async (t) => { states[t] = await this._fetch(t); }));
    this._tiles = tiles; this._states = states;
  }
  async _fetch(tile) {
    const r = await call('/deployments?tile=' + encodeURIComponent(tile));
    return r.error ? { error: r.error } : r.body;
  }
  async _load(tile) { this._states = { ...this._states, [tile]: await this._fetch(tile) }; }
  _say(tile, v) { this._said = { ...this._said, [tile]: v }; }

  render() {
    const tiles = this._tiles;
    if (!tiles) return html`<p class="muted">loading…</p>`;
    const shown = tiles.filter((t) => this._match(t, this._states[t]?.primary));
    return html`
      <h4 data-dep-head>tile deployments <span class="muted" style="font-weight:400">(${tiles.length} ${tiles.length === 1 ? 'tile has' : 'tiles have'} a deployment record)</span></h4>
      <p class="muted dep-note">Protection, the primary, deliveries and alwaysOn are tile managers' acts: the tile's owner, its org's admins, or a workspace admin.
        Here they run as you, in your own session — never from a tile terminal. Code moves and everything else are in each tile's Deployments panel.</p>
      ${tiles.length ? this._filterBar('filter tiles…', null, shown.length, tiles.length) : html`<p class="muted" data-dep-empty>No tile has deployments yet: every tile follows its work tree (live reload). A tile gets a record when its live reload is paused or a deployment is added — in its terminal window's Deployments panel, or with <span class="mono">bx live-reload pause</span> / <span class="mono">bx deployment add</span>.</p>`}
      ${shown.map((t) => this._tile(t, this._states[t]))}`;
  }

  _tile(tile, s) {
    const said = this._said[tile], busy = this._busy === tile;
    if (!s || s.error) {
      return html`<div class="dcard" data-dep-tile=${tile}><div class="dhead"><span class="mono dpath">${tile}</span></div>
        <div class="err">${s?.error || 'no state'}</div></div>`;
    }
    const chip = ds.chip(s), last = lastDeploy(s), rows = dp.panelRows(s);
    const acts = dp.panelActions(s, null).filter((a) => TILE_ACTS.has(a.id));
    return html`<div class="dcard" data-dep-tile=${tile} data-view=${s.view || ''}>
      <div class="dhead">
        <span class="mono dpath">${tile}</span>
        <span class="pill" title="everything from outside reaches the primary">primary: <b class="mono">${s.primary || 'main'}</b></span>
        ${s.protectedPrimary ? html`<span class="pill prot" data-dep-protected title="only tile managers change its code">🛡 protected</span>`
          : html`<span class="pill muted">not protected</span>`}
        ${chip ? html`<span class="pill lr" title=${chip.title}>${chip.text}</span>` : nothing}
        ${s.view === 'reader' ? html`<span class="pill muted" title="you don't manage this tile: the primary's facts only">reader view</span>` : nothing}
      </div>
      ${last ? html`<div class="muted dlast" data-dep-last>last deploy: ${last.how || 'deploy'} ${last.checkpoint || ''} → ${last.deployment}
        · ${last.result}${last.by ? ` · ${ds.who(last.by)}` : ''}${last.at ? ` · ${ds.ago(last.at)}` : ''}</div>` : nothing}
      <table class="dtab"><tr><th>deployment</th><th>code</th><th>status</th><th>data</th><th>deliveries · alwaysOn</th></tr>
        ${rows.map((r) => html`<tr data-dep-row=${r.name}>
          <td class="mono">${r.name}${r.primary ? html` <span class="muted">(primary${r.protected ? ' 🛡' : ''})</span>` : nothing}</td>
          <td class="mono">${r.code}</td>
          <td class=${r.lastDeployFailed ? 'st-failed' : ''}>${r.status}</td>
          <td class="muted">${r.data}</td>
          <td>${r.primary ? html`<span class="muted">${r.deliveries}</span>` : this._switches(tile, s, r.name, busy)}</td>
        </tr>`)}
      </table>
      <div class="dacts">
        ${acts.map((a) => html`<button class="act ${a.id === 'unprotect' ? '' : a.id === 'protect' ? 'go' : 'rm'}" data-dep-act=${a.id}
          ?disabled=${busy || !a.enabled} title=${a.enabled ? a.title : a.why || a.title}
          @click=${() => (a.id === 'reassign' ? this._reassign(tile) : this._run(tile, a.id))}>${a.id === 'protect' ? '🛡 ' : ''}${a.label}</button>`)}
        <button class="act" data-dep-open title="open ${tile}'s terminal window on its Deployments panel: deploy, promote, roll back, live reload, edges, data"
          @click=${() => this._openPanel(tile)}>⇈ Deployments panel</button>
        ${acts.some((a) => !a.enabled && a.why) ? html`<span class="muted dwhy">${acts.find((a) => !a.enabled && a.why).why}</span>` : nothing}
      </div>
      ${said ? html`<div class=${said.error ? 'err' : 'notice'} data-dep-said>${said.error || said}</div>` : nothing}
    </div>`;
  }

  // _switches: deliveries and (when its code declares it) alwaysOn of one
  // non-primary deployment, each a toggle whose on direction confirms.
  _switches(tile, s, name, busy) {
    const acts = dp.panelActions(s, name).filter((a) => DEP_ACTS.has(a.id));
    return html`${acts.map((a) => html`<label class="dsw" title=${a.enabled ? a.title : a.why || a.title}>
      <input type="checkbox" data-dep-switch=${`${name}/${a.id}`} .checked=${!!a.on} ?disabled=${busy || !a.enabled}
        @change=${(e) => { e.target.checked = !!a.on; this._run(tile, a.id, { deployment: name, on: !a.on, quiet: a.on }); }}>
      ${a.id === 'alwaysOn' ? 'alwaysOn' : 'deliveries'}</label>`)}`;
  }

  _openPanel(tile) {
    // The shell opens the tile's terminal window on its Deployments layout
    // (bx-frame relays it; a tile the viewer doesn't list is ignored).
    try { window.parent.postMessage({ type: 'xbin:open-deployments', tile }, '*'); } catch { /* not framed */ }
  }

  _body(tile, op, x) {
    const b = { tile };
    if (x.deployment) b.deployment = x.deployment;
    if (op === 'protect' || op === 'unprotect') b.on = op === 'protect';
    if (op === 'deliveries' || op === 'alwaysOn') b.on = !!x.on;
    return b;
  }

  // _run(tile, op, x): the terminal window's flow (web/bx-deploy.js): a dry
  // run of the exact request renders the confirmation (x.quiet: the safe
  // direction, none); the confirmed request carries the dry run's seq and
  // what the dialog showed. A moved record: read it again and ask again.
  async _run(tile, op, x = {}) {
    if (this._busy) return;
    this._busy = tile; this._say(tile, '');
    try {
      for (let attempt = 0; attempt < 3; attempt++) {
        const s = this._states[tile], body = this._body(tile, op, x);
        const seq = (st) => { if (Number.isInteger(st?.seq)) body.seq = st.seq; };
        seq(s);
        if (!x.quiet) {
          const dry = await call('/deployments/' + ROUTE[op], { ...body, ...(op === 'primary' ? { confirm: 'data-stays' } : {}), dryRun: true });
          if (dry.error) {
            if (ds.conflict(dry.status, dry.error) === 'seq' && attempt < 2) { await this._load(tile); continue; }
            this._say(tile, { error: dry.error }); return;
          }
          const cur = dry.body?.state?.tile === tile ? dry.body.state : s;
          const c = ds.confirmation(op, { state: cur, impact: dry.body?.impact, deployment: x.deployment, ...x }, { now: Date.now() });
          let values, err;
          for (;;) {
            const a = await window.xbin.dialog(err ? { ...c.spec, error: err } : c.spec);
            if (a?.button !== 'ok') return;
            values = a.values || {};
            if (c.required.every((n) => values[n])) break;
            err = ds.REASON.tick;
          }
          const extra = c.send(values);
          if (!extra) return;
          seq(cur);
          Object.assign(body, extra);
        }
        const res = await call('/deployments/' + ROUTE[op], body);
        if (res.error) {
          if (ds.conflict(res.status, res.error) === 'seq' && attempt < 2) { await this._load(tile); continue; }
          this._say(tile, { error: res.error }); await this._load(tile); return;
        }
        if (res.body?.state?.tile === tile) this._states = { ...this._states, [tile]: res.body.state };
        this._say(tile, ds.result(op, res.body, { deployment: x.deployment, prev: s }) || '');
        await this._load(tile);
        return;
      }
    } finally {
      this._busy = '';
    }
  }

  // _reassign: the healthy deployments that may become the primary; with
  // more than one, which (the panel's own picker), then the loud confirm.
  async _reassign(tile) {
    const ys = dp.reassignable(this._states[tile]);
    let Y = ys[0];
    if (ys.length > 1) {
      const a = await window.xbin.dialog({ title: 'Reassign the primary…', fields: [{ name: 'to', label: 'Deployment', type: 'select', value: Y, options: ys }],
        buttons: [{ label: 'Cancel', value: null }, { label: 'Reassign the primary…', value: 'ok', primary: true }] });
      if (a?.button !== 'ok') return;
      Y = a.values?.to || Y;
    }
    if (Y) await this._run(tile, 'primary', { deployment: Y });
  }
}

customElements.define('bx-admin-deployments', BxAdminDeployments);
