/**
 * <bx-admin-runtime view="components|resources"> — the admin console's
 * runtime group. components: the tile roster (manifest/principal view from
 * /auth-overview merged with live backend state), filterable and
 * category-chipped, each row expanding into who-can-reach-it and the
 * backend's process/namespaces/egress detail, with lifecycle links and a
 * code & history drill-in (files, commits, diffs). resources: host health,
 * workspace totals, the live per-tile stats table (CPU / memory / I/O
 * sampled by xbind) and the brokered resources by type. One element for
 * both sub-tabs: they share the /runtime snapshot this element polls and
 * the bus-rate ring sampled from it, so switching between them keeps the
 * history. Reports through bx-admin-err / bx-admin-tab / bx-admin-refresh
 * / bx-admin-show-hidden.
 */
import { LitElement, html, nothing, svg } from 'lit';
import { unsafeHTML } from 'lit';
import { xbinApi as api } from '/vendor/bx-kit.js';
import { diffHTML, hl, langFor } from '/vendor/bx-code.js';
import { base, runtimeCss } from '../admin-css.js';
import { fmtBytes, fmtDur, setLifecycle, WithFilter, WithRouter } from '../shared.js';

export class BxAdminRuntime extends WithRouter(WithFilter(LitElement)) {
  static properties = {
    view: { type: String },            // components | resources
    ov: { attribute: false },          // /auth-overview (components, counts)
    vaultStatus: { attribute: false }, // {initialized, sealed, mode, insecure} — the banner
    showHidden: { attribute: false },  // reveal hidden (state=hidden) tiles (D42)
    _rt: { state: true },       // /runtime snapshot {host, backends, stats, resources}
    _rtOpen: { state: true },   // set of expanded component paths
    _busy: { state: true },     // comp path mid lifecycle change
    _codeComp: { state: true }, // component being browsed in the drill-in
    _codeTree: { state: true }, // its files
    _codeFile: { state: true }, // {path, content|binary|truncated}
    _codeLog: { state: true },  // its commits
    _codeDiff: { state: true }, // {rev, diff}
    _codeMode: { state: true }, // 'file' | 'diff'
    _stSort: { state: true },   // live-stats table sort {col, dir}
    _stTotOrg: { state: true }, // totals charts: split lines per org
    _resType: { state: true },  // resources table: active type tab
    _stFilter: { state: true }, // live-stats name-prefix filter
    _stGroup: { state: true },  // live-stats: group rows by org
    _stOpen: { state: true },   // live-stats: tile expanded into big charts
    _q: { state: true },
    _cats: { state: true },
    _err: { state: true },
  };
  static styles = [base, runtimeCss];

  constructor() {
    super();
    this._rtOpen = new Set();
    this._q = ''; this._cats = new Set();
    this._access = {}; // per-component access relations, lazily loaded
    this._stSort = { col: 'cpu', dir: -1 };
    this._stFilter = '';
    this._stGroup = false;
    this._stOpen = null;
  }
  connectedCallback() {
    super.connectedCallback();
    this.load();
    this._timer = setInterval(() => this.load(), 2000);
  }
  disconnectedCallback() {
    super.disconnectedCallback(); clearInterval(this._timer);
  }
  // Switching sub-tab drops the drill-in and the filter, as a fresh view would.
  willUpdate(changed) {
    if (changed.has('view') && changed.get('view') !== undefined) { this._codeComp = null; this._q = ''; this._cats = new Set(); }
  }
  // The router calls this when the components tab is clicked again: back to the list.
  closeCode() { this._codeComp = null; }

  // Polled every 2 s while the element is on screen (backend state, host
  // health, per-tile stats). Bus resources keep a ring of cumulative event
  // counts → events/min; a failed poll reports once per failure, a good one
  // does not clear the slot (the router's errors are not ours to clear).
  async load() {
    try {
      const rt = await api('/runtime');
      const now = Date.now();
      this._busRing = this._busRing || new Map();
      for (const r of rt.resources || []) {
        if (r.type !== 'bus') continue;
        const ring = this._busRing.get(r.id) || [];
        ring.push({ t: now, n: r.events || 0 });
        while (ring.length > 40) ring.shift();
        this._busRing.set(r.id, ring);
      }
      this._rt = rt;
    } catch (e) { this._fail(e); }
  }
  refresh() { return this.load(); }

  render() {
    if (this.view === 'resources') return this._resourcesView();
    return this._codeComp ? this._codeView() : this._componentsView();
  }

  // ---- code & history (a drill-in from the component list) ----
  async _openCode(comp) {
    this._codeComp = comp; this._codeFile = null; this._codeDiff = null; this._codeMode = 'file';
    await this._loadCode();
    const files = this._codeTree?.files ?? [];
    const def = files.find((f) => f.path === 'index.html') || files.find((f) => f.path === 'xbin.json') || files[0];
    if (def) this._loadFile(def.path);
  }
  async _loadCode() {
    const c = encodeURIComponent(this._codeComp);
    try {
      const [tree, log] = await Promise.all([api(`/code/tree?component=${c}`), api(`/git/log?component=${c}`)]);
      this._codeTree = tree; this._codeLog = log;
    } catch (e) { this._fail(e); }
  }
  async _loadFile(path) {
    try {
      this._codeFile = await api(`/code/file?component=${encodeURIComponent(this._codeComp)}&file=${encodeURIComponent(path)}`);
      this._codeMode = 'file';
    } catch (e) { this._fail(e); }
  }
  async _loadDiff(rev) {
    try {
      const d = await api(`/git/diff?component=${encodeURIComponent(this._codeComp)}&rev=${encodeURIComponent(rev)}`);
      this._codeDiff = { rev, diff: d.diff ?? '' }; this._codeMode = 'diff';
    } catch (e) { this._fail(e); }
  }
  _fmtDate(iso) { try { return new Date(iso).toLocaleDateString(); } catch { return iso; } }

  _codeView() {
    if (!this._codeComp) return this._componentsView(); // reached only defensively; the list is the picker
    const tree = this._codeTree?.files ?? [];
    const log = this._codeLog?.commits ?? [];
    const noRepo = this._codeLog?.repo === false;
    return html`
      <div class="hd">
        <a class="link" @click=${() => { this._codeComp = null; }}>← components</a>
        <span class="path">${this._codeComp}</span>
        ${this._codeLog?.remote ? html`<span class="muted" style="font-size:11px" title="git remote (origin)">${this._codeLog.remote.replace(/^https:\/\/|\.git$/g, '')}</span>` : nothing}
      </div>
      <div class="code">
        <div class="side">
          <div class="files">
            <div class="grouphd">files</div>
            ${tree.length ? tree.map((f) => html`
              <div class="row ${this._codeMode === 'file' && this._codeFile?.path === f.path ? 'on' : ''}"
                   @click=${() => this._loadFile(f.path)}>${f.path}</div>`)
              : html`<div class="row muted">—</div>`}
          </div>
          <div class="hist">
            <div class="grouphd">history</div>
            <div class="row ${this._codeMode === 'diff' && this._codeDiff?.rev === '' ? 'on' : ''}"
                 @click=${() => this._loadDiff('')}>● uncommitted changes</div>
            ${noRepo ? html`<div class="row muted">not a git repo</div>`
              : log.length ? log.map((c) => html`
                <div class="row ${this._codeMode === 'diff' && this._codeDiff?.rev === c.hash ? 'on' : ''}"
                     @click=${() => this._loadDiff(c.hash)}>
                  <div>${c.subject}</div><div class="s">${c.short} · ${c.author} · ${this._fmtDate(c.date)}</div>
                </div>`)
              : html`<div class="row muted">no commits touch this component</div>`}
          </div>
        </div>
        <div class="main">
          ${this._codeMode === 'diff'
            ? html`<pre class="diff hljs">${unsafeHTML(diffHTML(this._codeDiff?.diff))}</pre>`
            : this._codeFile
              ? (this._codeFile.binary ? html`<span class="muted">binary file (${this._codeFile.size} bytes)</span>`
                : this._codeFile.truncated ? html`<span class="muted">file too large to display (${this._codeFile.size} bytes)</span>`
                : html`<pre class="hljs"><code>${unsafeHTML(hl(this._codeFile.content ?? '', langFor(this._codeFile.path ?? '')))}</code></pre>`)
              : html`<span class="muted">select a file or a commit</span>`}
        </div>
      </div>`;
  }

  // ---- runtime detail ----
  _toggleBk(path) {
    const s = new Set(this._rtOpen); s.has(path) ? s.delete(path) : s.add(path); this._rtOpen = s;
  }
  _mem(b) {
    if (b.cgroup && b.cgroup.memCurrent) return fmtBytes(b.cgroup.memCurrent);
    if (b.rssKb) return fmtBytes(b.rssKb * 1024);
    return '—';
  }
  _flowTime(f) {
    const ageS = Math.max(0, (Date.now() - f.start) / 1000);
    const age = ageS < 60 ? (ageS | 0) + 's ago' : fmtDur(ageS) + ' ago';
    if (!f.end) return age + ' · open';
    const dur = (f.end - f.start) / 1000;
    return age + (dur >= 0.05 ? ' · ' + dur.toFixed(1) + 's' : '');
  }

  // Terminal secret-mask guards (docs/isolation.md): mount guard = seccomp
  // (masks can't be umounted), read guard = Landlock (secret files can't be
  // read even if a mask is peeled). Green when the kernel supports each.
  _guardStatus(p) {
    p = p || {};
    const mark = (on) => (on ? '✓' : '✗');
    const land = p.landlock ? `✓ (ABI ${p.landlockAbi})` : '✗';
    return html`<span title="seccomp mount guard · Landlock read guard"
      >mount ${mark(p.seccomp)} · read ${land}</span>`;
  }

  // One tab per resource type; per-type columns (bus: live events/min from
  // the cumulative counter sampled every poll — see load()).
  static resTypeOrder = ['filesystem', 'sqlite', 'kv', 'blob', 'bus', 'cron'];

  _busRate(id) {
    const ring = this._busRing?.get(id);
    if (!ring || ring.length < 2) return null;
    // events in the trailing ≤60s window, scaled to a minute
    const last = ring[ring.length - 1];
    let first = ring[0];
    for (const s of ring) { if (last.t - s.t <= 65000) { first = s; break; } }
    const dtMin = (last.t - first.t) / 60000;
    if (dtMin <= 0) return null;
    return Math.max(0, (last.n - first.n) / dtMin);
  }

  _resourcesSection(resources) {
    if (!resources || !resources.length) return nothing;
    const types = BxAdminRuntime.resTypeOrder.filter((t) => resources.some((r) => r.type === t))
      .concat([...new Set(resources.map((r) => r.type))].filter((t) => !BxAdminRuntime.resTypeOrder.includes(t)));
    const active = types.includes(this._resType) ? this._resType : types[0];
    const rows = resources.filter((r) => r.type === active);
    const cols = active === 'bus' ? ['id', 'events/min', 'events total']
      : active === 'cron' ? ['id', 'jobs']
      : active === 'kv' ? ['id', 'size', 'keys']
      : ['id', 'size', 'detail'];
    const cell = (r, c) => {
      switch (c) {
        case 'id': return html`<span class="p" title=${r.id}>${r.id}</span>`;
        case 'size': return html`<span class="num">${r.size ? fmtBytes(r.size) : '—'}</span>`;
        case 'events/min': { const v = this._busRate(r.id);
          return html`<span class="num">${v == null ? '…' : v < 10 ? v.toFixed(1) : Math.round(v)}</span>`; }
        case 'events total': return html`<span class="num">${r.events || 0}</span>`;
        default: return html`<span class="muted">${r.detail || ''}</span>`;
      }
    };
    return html`
      <h4>resources</h4>
      <div class="strip" style="gap:2px">
        ${types.map((t) => html`<button class="act ${t === active ? 'on' : ''}"
          style=${t === active ? 'font-weight:600' : ''}
          @click=${() => { this._resType = t; }}>${t}
          <span class="muted">${resources.filter((r) => r.type === t).length}</span></button>`)}
      </div>
      <div class="bk"><div class="row hdr rrow" style="grid-template-columns: 1fr ${cols.slice(1).map(() => '110px').join(' ')}">
        ${cols.map((c) => html`<span class=${c === 'id' ? '' : 'num'}>${c}</span>`)}</div></div>
      ${rows.map((r) => html`<div class="bk"><div class="row rrow" style="grid-template-columns: 1fr ${cols.slice(1).map(() => '110px').join(' ')}">
        ${cols.map((c) => cell(r, c))}
      </div></div>`)}`;
  }

  _bkDetail(b) {
    const act = b.activity;
    return html`<div class="detail">
      <div>
        <h5>process</h5>
        <div class="mono">runtime ${b.runtime || 'static'} · gen ${b.gen} · up ${fmtDur(b.uptimeSec)}</div>
        <div class="mono">threads ${b.threads || '—'} · restarts ${b.restarts} · last req ${b.lastReqSec < 0 ? 'never' : fmtDur(b.lastReqSec) + ' ago'}</div>
        ${b.cgroup ? html`<div class="mono">cgroup: ${fmtBytes(b.cgroup.memCurrent)}${b.cgroup.memMax > 0 ? ' / ' + fmtBytes(b.cgroup.memMax) : ''} · cpu ${(b.cgroup.cpuUsec / 1e6).toFixed(1)}s · ${b.cgroup.pidsCurrent} pid(s)</div>` : nothing}
        ${b.error ? html`<div class="err-pill">${b.error}</div>` : nothing}
      </div>
      <div>
        <h5>namespaces</h5>
        ${b.namespaces
          ? Object.entries(b.namespaces).map(([k, v]) => html`<div class="nsrow">${k}: <span class=${v.isolated ? 'iso' : 'shared'}>${v.isolated ? 'isolated' : 'shared'}</span> <span class="muted mono">${v.id}</span></div>`)
          : html`<span class="muted">shared with host (not sandboxed)</span>`}
      </div>
      <div>
        <h5>egress ${act ? html`· ${fmtBytes(act.txBytes)}↑ ${fmtBytes(act.rxBytes)}↓ · ${act.active} active` : nothing}</h5>
        ${b.netRef ? html`<div class="mono" style="font-size:11px">net ${b.netRef === 'org'
            ? html`<span class="pill" title=${(b.netRules ?? []).join('\n') || 'org network (no relay rules)'}>🏢 ${b.netSource || 'org network'}</span>`
            : b.netRef}${b.net ? html` <span class="muted">· ${b.net}</span>` : nothing}</div>` : nothing}
        ${b.netNote ? html`<div class="warn-line">⚠ ${b.netNote}</div>` : nothing}
        ${(b.egress && b.egress.length) ? html`<div class="mono">${b.egress.join(', ')}</div>` : html`<span class="muted">${b.isolated ? 'no egress granted (deny-all)' : 'unrestricted (host network)'}</span>`}
        ${act && act.recent && act.recent.length ? html`
          <table class="flowtab"><tbody>
            ${act.recent.slice(0, 12).map((f) => html`<tr>
              <td class=${f.allowed ? 'flow-allow' : 'flow-deny'}>${f.allowed ? '✓' : '⛔'}</td>
              <td class="mono">${f.proto} ${f.dst}:${f.port}</td>
              <td class="mono">${fmtBytes(f.txBytes)}↑ ${fmtBytes(f.rxBytes)}↓</td>
              <td class="muted">${this._flowTime(f)}</td>
            </tr>`)}
          </tbody></table>` : nothing}
      </div>
    </div>`;
  }

  // Vault banner at the top of the overview: unmissable when the barrier is
  // sealed/unconfigured (stateful components are held), quiet when healthy.
  _vaultBanner() {
    const st = this.vaultStatus;
    if (!st) return nothing;
    const goVault = () => this._emit('bx-admin-tab', 'vault');
    if (st.mode === 'sealed') {
      return html`<div class="vault-banner sealed" @click=${goVault}
        title="open the vault tab to unseal">
        🔒 VAULT SEALED — encrypted resources are unmounted and stateful components are HELD.
        Click to unseal.</div>`;
    }
    if (st.mode === 'unconfigured') {
      return html`<div class="vault-banner sealed" @click=${goVault}
        title="open the vault tab to set a passphrase">
        🔒 VAULT UNCONFIGURED — secret & resource storage is refused until a passphrase is set.
        Click to set one.</div>`;
    }
    if (st.mode === 'plaintext') {
      return html`<div class="vault-banner warn" @click=${goVault}>
        ⚠ vault: plaintext at rest (dev mode) — click to encrypt.</div>`;
    }
    return html`<div class="vault-banner ok">vault unsealed — encryption at rest active</div>`;
  }

  // ---- components (runtime → components): the tile roster ----
  // Merges the manifest/principal view (_ov) with live backend state (_rt) and
  // adds per-component access relations on expand. Filterable + category-chipped
  // so it scales to thousands of tiles.
  _bkByPath() {
    const m = {};
    for (const b of (this._rt?.backends ?? [])) m[b.path] = b;
    return m;
  }
  _toggleComp(path) {
    const s = new Set(this._rtOpen); s.has(path) ? s.delete(path) : s.add(path); this._rtOpen = s;
    if (s.has(path) && this._access[path] === undefined) this._loadAccess(path);
  }
  async _loadAccess(path) {
    this._access = { ...this._access, [path]: null }; // mark loading
    try {
      const d = await api('/access?tile=' + encodeURIComponent(path));
      this._access = { ...this._access, [path]: d };
    } catch (e) { this._access = { ...this._access, [path]: { error: String(e.message ?? e) } }; }
  }

  _componentsView() {
    const ov = this.ov; if (!ov) return html`<span class="muted">loading…</span>`;
    const c = ov.counts;
    const bk = this._bkByPath();
    const all = ov.components ?? [];
    const cats = [...new Set(all.map((k) => this._catOf(k.path)))].sort();
    const rows = all.filter((k) => this._catActive(this._catOf(k.path)) &&
      this._match(k.path, k.runtime, (k.uses ?? []).map((u) => u.target).join(' ')));
    const hiddenN = rows.filter((k) => k.state === 'hidden').length;
    const live = rows.filter((k) => !this._isOffloaded(k) && (this.showHidden || k.state !== 'hidden'));
    const off = rows.filter((k) => this._isOffloaded(k));
    return html`
      ${this._vaultBanner()}
      <div class="cards">
        <div class="stat"><div class="n">${c.components}</div><div class="l">components</div></div>
        <div class="stat"><div class="n">${c.exposed}</div><div class="l">expose APIs</div></div>
        <div class="stat"><div class="n">${c.grants}</div><div class="l">grants</div></div>
        <div class="stat ${c.pending ? 'warn' : ''}"><div class="n">${c.pending}</div><div class="l">pending</div></div>
      </div>
      ${this._filterBar('filter tiles by path, runtime or use…', cats, rows.length, all.length)}
      ${hiddenN ? html`<label class="muted" style="font-size:11px;display:inline-flex;gap:5px;align-items:center;margin:2px 0 6px">
        <input type="checkbox" .checked=${!!this.showHidden}
          @change=${(e) => { this._emit('bx-admin-show-hidden', e.target.checked); }}> show hidden (${hiddenN})</label>` : nothing}
      <table>
        <tr><th></th><th>component</th><th>runtime</th><th>state</th><th>exposes</th><th>uses</th><th>vault</th><th>lifecycle</th></tr>
        ${live.map((k) => this._compRow(k, bk[k.path]))}
        ${live.length === 0 ? html`<tr><td></td><td class="muted" colspan="7">no matching components</td></tr>` : nothing}
      </table>
      ${off.length ? html`<h4>offloaded <span class="muted" style="font-weight:400;text-transform:none;letter-spacing:0">— archived, not running</span></h4>
        <table>
          <tr><th>component</th><th>state</th><th></th></tr>
          ${off.map((k) => html`<tr>
            <td class="mono">${k.path}</td>
            <td><span class="pill st-failed">${k.state}</span></td>
            <td style="text-align:right"><a class="link" @click=${() => this._emit('bx-admin-tab', 'backup')}>restore in Backup →</a></td>
          </tr>`)}
        </table>` : nothing}`;
  }

  _compRow(k, b) {
    const open = this._rtOpen.has(k.path);
    const state = b ? html`<span class="state ${b.state}">${b.state}</span>${b.isolated ? html` <span class="lock" title="sandboxed">🔒</span>` : nothing}`
      : html`<span class="muted">${(k.runtime && k.runtime !== 'static') ? 'idle' : 'static'}</span>`;
    return html`
      <tr>
        <td><span class="caret ${open ? 'o' : ''}" style="cursor:pointer" @click=${() => this._toggleComp(k.path)}>▶</span></td>
        <td class="mono"><a class="link" @click=${() => this._openCode(k.path)} title="view code & history">${k.path}</a>${k.manifestError ? html` <span class="st-failed" title=${k.manifestError}>⚠</span>` : nothing}</td>
        <td class="muted">${k.runtime || 'static'}</td>
        <td>${state}</td>
        <td>${k.roles ? Object.keys(k.roles).map((r) => html`<span class="pill">${r}</span>`) : html`<span class="muted">—</span>`}</td>
        <td>${(k.uses ?? []).map((u) => html`<span class="pill">${u.target}:${u.role}</span>`)}</td>
        <td>${k.hasVault ? '🔑' : ''}</td>
        <td>${this._lifecycleCell(k)}</td>
      </tr>
      ${open ? html`<tr><td></td><td colspan="7">${this._compDetail(k, b)}</td></tr>` : nothing}`;
  }

  // Component detail (expanded): who can reach it (access relations) + live
  // backend runtime. The access relations are the "user relations" view — the
  // reverse of the access map, per tile.
  _compDetail(k, b) {
    const acc = this._access[k.path];
    return html`<div class="detail" style="grid-template-columns:1fr">
      <div>
        <h5>access — who can reach this tile</h5>
        ${acc === undefined || acc === null ? html`<span class="muted">loading…</span>`
          : acc.error ? html`<span class="err-pill">${acc.error}</span>`
          : html`
            ${acc.org ? html`<div class="mono" style="font-size:11px;margin-bottom:3px">org: ${acc.org}</div>` : nothing}
            ${(acc.entries ?? []).length === 0 ? html`<span class="muted">no users or teams have access (admins always do)</span>` : html`
            <table class="tbl"><tr><th>who</th><th>level</th><th>via</th></tr>
              ${acc.entries.map((e) => html`<tr>
                <td>${e.kind === 'team' ? '👥' : '👤'} <span class="mono">${e.id}</span></td>
                <td><span class="pill">${e.level}</span></td>
                <td class="muted">${e.source}</td></tr>`)}
            </table>`}
            <a class="link" @click=${() => this._emit('bx-admin-tab', 'map')}>full access map →</a>`}
      </div>
      ${b ? html`<div style="margin-top:8px"><h5>runtime</h5>${this._bkDetail(b)}</div>` : nothing}
    </div>`;
  }

  _isOffloaded(k) { return k.state === 'offloaded' || k.state === 'offloaded-full'; }

  // ---- live per-tile stats (runtime → resources) --------------------------
  // CPU / memory / I/O rates sampled by xbind (cgroup leaves, /proc fallback;
  // internal/runner/stats.go), polled with the rest of /runtime every 2s.

  // Multi-line SVG sparkline over a stats series. keys/colors pick up to two
  // fields of each point; scaled to the window max (shared across lines).
  _stSpark(series, keys, colors, w = 84, ht = 18) {
    if (!series || series.length < 2) return html`<span class="muted" style="font-size:10px">—</span>`;
    let max = 0;
    for (const p of series) for (const k of keys) max = Math.max(max, p[k] || 0);
    const step = w / (series.length - 1);
    const pts = (k) => series.map((p, i) =>
      `${(i * step).toFixed(1)},${(ht - (max ? ((p[k] || 0) / max) : 0) * (ht - 2) - 1).toFixed(1)}`).join(' ');
    const p1 = pts(keys[0]);
    const p2 = keys[1] ? pts(keys[1]) : '';
    return html`<svg class="spark" width=${w} height=${ht} viewBox="0 0 ${w} ${ht}">
      <polyline points=${p1} fill="none" stroke=${colors[0]} stroke-width="1.2"></polyline>
      <polyline points=${p2} fill="none" stroke=${colors[1] || 'none'} stroke-width="1.2"></polyline>
    </svg>`;
  }

  // One metric cell: current value over its sparkline.
  _stCell(t, keys, colors, fmt) {
    return html`<div class="stcell">
      <span class="stval">${fmt(t.cur)}</span>
      ${this._stSpark(t.series, keys, colors)}
    </div>`;
  }

  // Sort on a ~1-minute moving average (last 30 points at 2s), not the
  // instantaneous sample — otherwise rows reshuffle on every poll.
  _stAvg(t, keys) {
    const s = t.series || [];
    const tail = s.slice(-30);
    if (!tail.length) return 0;
    let sum = 0;
    for (const p of tail) for (const k of keys) sum += p[k] || 0;
    return sum / tail.length;
  }

  _stSortKey(t) {
    switch (this._stSort.col) {
      case 'tile': return t.path;
      case 'org': return t.owner || '';
      case 'mem': return this._stAvg(t, ['mem']);
      case 'io': return this._stAvg(t, ['rbps', 'wbps']);
      case 'iops': return this._stAvg(t, ['riops', 'wiops']);
      case 'pids': return this._stAvg(t, ['pids']);
      default: return this._stAvg(t, ['cpu']);
    }
  }

  _stTh(col, label, title) {
    const s = this._stSort;
    return html`<th class="sortable" title=${title || ''}
      @click=${() => { this._stSort = { col, dir: s.col === col ? -s.dir : (col === 'tile' || col === 'org' ? 1 : -1) }; }}>
      ${label}${s.col === col ? (s.dir > 0 ? ' ▲' : ' ▼') : ''}</th>`;
  }

  // The four chart specs shared by cells and the expanded view. I/O series
  // are syscall-level (all file activity incl. FUSE-backed resources).
  static stMetrics = [
    { label: 'cpu', keys: ['cpu'], colors: ['var(--bx-accent,#f5a623)'], fmt: (c) => `${(c.cpu || 0).toFixed(1)}%` },
    { label: 'mem', keys: ['mem'], colors: ['var(--bx-green, #4caf50)'], fmt: (c, el) => fmtBytes(c.mem || 0) },
    { label: 'i/o r+w', keys: ['rbps', 'wbps'], colors: ['#5b8def', 'var(--bx-red, #ef5350)'], fmt: (c, el) => `${fmtBytes(c.rbps || 0)}/s · ${fmtBytes(c.wbps || 0)}/s` },
    { label: 'iops r+w', keys: ['riops', 'wiops'], colors: ['#5b8def', 'var(--bx-red, #ef5350)'], fmt: (c) => `${Math.round(c.riops || 0)} · ${Math.round(c.wiops || 0)}` },
  ];

  // N-line sparkline over precomputed numeric arrays (shared max). Used by
  // the totals charts, where one line per org can exceed _stSpark's two.
  _stSparkN(lines, w = 220, ht = 44) {
    const len = Math.max(0, ...lines.map((l) => l.vals.length));
    if (len < 2) return html`<span class="muted" style="font-size:10px">gathering…</span>`;
    let max = 0;
    for (const l of lines) for (const v of l.vals) max = Math.max(max, v);
    const step = w / (len - 1);
    return html`<svg class="spark" width=${w} height=${ht} viewBox="0 0 ${w} ${ht}">
      ${lines.map((l) => svg`<polyline fill="none" stroke=${l.color} stroke-width="1.3"
        points=${l.vals.map((v, i) => `${((i + (len - l.vals.length)) * step).toFixed(1)},${(ht - (max ? v / max : 0) * (ht - 2) - 1).toFixed(1)}`).join(' ')}></polyline>`)}
    </svg>`;
  }

  static orgPalette = ['#5b8def', '#43a047', '#f5a623', '#e5484d', '#9c27b0',
    '#00acc1', '#8d6e63', '#7cb342'];

  // Workspace totals: each metric summed across tiles point-by-point
  // (series share the sampler's cadence; aligned on the tail). "by org"
  // splits the sum into one line per owner.
  _stTotals(stats) {
    const tiles = stats?.tiles ?? [];
    if (!tiles.length) return nothing;
    const M = BxAdminRuntime.stMetrics;
    const val = (p, keys) => keys.reduce((s, k) => s + (p[k] || 0), 0);
    const sum = (list, keys) => {
      const len = Math.max(0, ...list.map((t) => (t.series || []).length));
      const out = new Array(len).fill(0);
      for (const t of list) {
        const ser = t.series || [];
        for (let i = 0; i < ser.length; i++) out[len - ser.length + i] += val(ser[i], keys);
      }
      return out;
    };
    let orgs = null;
    if (this._stTotOrg) {
      const buckets = new Map();
      for (const t of tiles) {
        const k = t.owner || 'workspace';
        if (!buckets.has(k)) buckets.set(k, []);
        buckets.get(k).push(t);
      }
      orgs = [...buckets.entries()];
    }
    const chart = (m) => {
      const totalNow = tiles.reduce((s, t) => s + val(t.cur || {}, m.keys), 0);
      const lines = orgs
        ? orgs.map(([org, list], i) => ({ org, color: BxAdminRuntime.orgPalette[i % BxAdminRuntime.orgPalette.length], vals: sum(list, m.keys) }))
        : [{ color: m.colors[0], vals: sum(tiles, m.keys) }];
      return html`<div class="stchart">
        <div class="muted" style="font-size:10px;text-transform:uppercase;letter-spacing:.06em">${m.label}
          <b style="text-transform:none;letter-spacing:0"> ${m.label === 'cpu' ? `${totalNow.toFixed(1)}%`
            : m.label.startsWith('iops') ? `${Math.round(totalNow)}/s`
            : `${fmtBytes(totalNow)}${m.label === 'mem' ? '' : '/s'}`}</b></div>
        ${this._stSparkN(lines)}
      </div>`;
    };
    return html`
      <h4 style="display:flex;align-items:center;gap:12px">workspace totals
        <label class="muted" style="font-size:11px;font-weight:400;display:inline-flex;gap:5px;align-items:center">
          <input type="checkbox" .checked=${this._stTotOrg}
            @change=${(e) => { this._stTotOrg = e.target.checked; }}>
          by org</label></h4>
      <div class="stbig" style="padding:4px 0 2px">${M.map(chart)}</div>
      ${orgs && orgs.length > 1 ? html`<div class="strip" style="gap:10px;flex-wrap:wrap">
        ${orgs.map(([org], i) => html`<span class="muted mono" style="font-size:10.5px">
          <span style="display:inline-block;width:9px;height:9px;border-radius:2px;background:${BxAdminRuntime.orgPalette[i % BxAdminRuntime.orgPalette.length]}"></span>
          ${org}</span>`)}
      </div>` : nothing}`;
  }

  _liveStatsSection(stats) {
    const tiles = stats?.tiles ?? [];
    const M = BxAdminRuntime.stMetrics;
    const f = (this._stFilter || '').trim();
    let rows = f ? tiles.filter((t) => t.path.startsWith(f) || (t.owner || '').startsWith(f)) : tiles.slice();
    const dir = this._stSort.dir;
    rows.sort((a, b) => {
      const ka = this._stSortKey(a); const kb = this._stSortKey(b);
      const c = typeof ka === 'string' ? ka.localeCompare(kb) : ka - kb;
      return c ? c * dir : a.path.localeCompare(b.path);
    });

    const head = html`<tr>
      ${this._stTh('tile', 'tile')}
      ${this._stGroup ? nothing : this._stTh('org', 'owner')}
      ${this._stTh('cpu', 'cpu %')}
      ${this._stTh('mem', 'memory')}
      ${this._stTh('io', 'i/o', 'read + write, syscall-level (includes resource/FUSE I/O)')}
      ${this._stTh('iops', 'iops', 'read + write ops/s')}
      ${this._stTh('pids', 'pids')}
    </tr>`;

    const row = (t) => {
      const open = this._stOpen === t.path;
      return html`<tr class="strow ${open ? 'on' : ''}" @click=${() => { this._stOpen = open ? null : t.path; }}>
        <td class="mono">${t.path}</td>
        ${this._stGroup ? nothing : html`<td class="muted mono" style="font-size:11px">${t.owner || '—'}</td>`}
        <td>${this._stCell(t, M[0].keys, M[0].colors, (c) => M[0].fmt(c, this))}</td>
        <td>${this._stCell(t, M[1].keys, M[1].colors, (c) => M[1].fmt(c, this))}</td>
        <td>${this._stCell(t, M[2].keys, M[2].colors, (c) => M[2].fmt(c, this))}</td>
        <td>${this._stCell(t, M[3].keys, M[3].colors, (c) => M[3].fmt(c, this))}</td>
        <td>${t.cur?.pids || 0}</td>
      </tr>
      ${open ? html`<tr><td colspan=${this._stGroup ? 6 : 7} class="stbig">
        ${M.map((m) => html`<div class="stchart">
          <div class="muted" style="font-size:10px;text-transform:uppercase;letter-spacing:.06em">${m.label}
            <b style="text-transform:none;letter-spacing:0"> ${m.fmt(t.cur || {}, this)}</b></div>
          ${this._stSpark(t.series, m.keys, m.colors, 300, 56)}
        </div>`)}
      </td></tr>` : nothing}`;
    };

    // Group by org: bucket per owner ref ("org:…", "user:…", or workspace).
    let body;
    if (this._stGroup) {
      const buckets = new Map();
      for (const t of rows) {
        const k = t.owner || 'workspace';
        if (!buckets.has(k)) buckets.set(k, []);
        buckets.get(k).push(t);
      }
      const agg = (list, key) => list.reduce((s, t) => s + (this._aggVal(t, key)), 0);
      body = [...buckets.entries()].map(([org, list]) => html`
        <tr><td colspan="6" class="grouphd mono">${org} <span style="float:right;font-weight:400">
          ${list.length} tile${list.length === 1 ? '' : 's'} · ${agg(list, 'cpu').toFixed(1)}% ·
          ${fmtBytes(agg(list, 'mem'))} · ${fmtBytes(agg(list, 'io'))}/s</span></td></tr>
        ${list.map(row)}`);
    } else {
      body = rows.map(row);
    }

    return html`
      <h4>live tiles</h4>
      <div class="strip">
        <input placeholder="filter by name prefix…" .value=${this._stFilter}
          @input=${(e) => { this._stFilter = e.target.value; }} style="width:200px">
        <label class="muted" style="font-size:11px;display:inline-flex;gap:5px;align-items:center">
          <input type="checkbox" .checked=${this._stGroup}
            @change=${(e) => { this._stGroup = e.target.checked; this._stSort = this._stGroup && this._stSort.col === 'org' ? { col: 'cpu', dir: -1 } : this._stSort; }}>
          group by org</label>
        ${stats && !stats.cgroup ? html`<span class="muted" style="font-size:10.5px" title="run under the installed service (systemd Delegate=yes) for exact whole-tree accounting">process-tree sampling</span>` : nothing}
      </div>
      ${rows.length === 0 ? html`<p class="muted">${tiles.length === 0
        ? 'no running backends — live stats appear when a tile’s backend runs.'
        : 'no tiles match the filter.'}</p>`
      : html`<table class="stats">${head}${body}</table>`}`;
  }

  _aggVal(t, key) {
    const c = t.cur || {};
    if (key === 'io') return (c.rbps || 0) + (c.wbps || 0);
    return c[key] || 0;
  }

  // ---- resources (runtime → resources): host health + brokered state ----
  _resourcesView() {
    const rt = this._rt; if (!rt) return html`<span class="muted">loading…</span>`;
    const h = rt.host || {};
    const kv = (label, val) => html`<div class="kv"><span>${label}</span> <b>${val}</b></div>`;
    return html`
      <div class="hostcard">
        ${kv('xbind', h.version)}
        ${kv('kernel', h.kernel || '—')}
        ${kv('pid', h.pid)}
        ${kv('euid', h.uid)}
        ${kv('cpus', h.numCPU)}
        ${kv('goroutines', h.goroutines)}
        ${kv('heap', fmtBytes((h.heapMB || 0) * 1e6))}
        ${kv('uptime', fmtDur(h.uptimeSec))}
        ${kv('isolation', h.isolate ? 'on (tier 3)' : (h.scopeUids ? 'uids (tier 2)' : 'off (tier 1)'))}
        ${h.isolate ? kv('rootfs', h.rootfs) : nothing}
        ${h.isolate ? kv('terminal guard', this._guardStatus(h.protections)) : nothing}
      </div>
      ${this._stTotals(rt.stats)}
      ${this._liveStatsSection(rt.stats)}
      ${this._resourcesSection(rt.resources)}
      ${(!rt.resources || !rt.resources.length) ? html`<p class="muted">no brokered resources provisioned yet — declare them in a <span class="mono">scope.json</span> (kv, blob, bus, cron, sqlite, filesystem). See <a href="/docs/resources.md" target="_blank">docs/resources.md</a>.</p>` : nothing}`;
  }

  // Lifecycle toggle (docs/overview/14-lifecycle.md). Static/CGI components with no backend
  // still list, but only a running-backend runtime benefits — offer the toggle
  // for any runtime the owner may want paused.
  _lifecycleCell(k) {
    const st = k.state || 'enabled';
    const disabled = st !== 'enabled';
    return html`${disabled ? html`<span class="pill st-failed" title="not running">${st}</span> ` : nothing}
      <a class="link" @click=${() => this._setLifecycle(k.path, disabled ? 'enabled' : 'disabled')}>${st === 'hidden' ? 'unhide' : disabled ? 'enable' : 'disable'}</a>
      ${st !== 'hidden' ? html` · <a class="link" title="disabled + removed from sidebars until unhidden (D42)"
        @click=${() => this._setLifecycle(k.path, 'hidden')}>hide</a>` : nothing}`;
  }

  async _setLifecycle(path, state) {
    this._busy = path;
    try {
      if (!await setLifecycle(path, state)) return; // declined
      this._emit('bx-admin-refresh');
    } catch (e) { this._fail(e); }
    finally { this._busy = null; }
  }
}

customElements.define('bx-admin-runtime', BxAdminRuntime);
