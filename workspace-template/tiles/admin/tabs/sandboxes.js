/**
 * <bx-admin-sandboxes> — the admin console's runtime → sandboxes tab (D112):
 * every sandbox xbind runs — each backend generation, terminal and agent
 * session; later the sandboxes a tile manages itself, nested under it — with
 * how it is isolated (⧉ VM, 🔒 namespace sandbox, or none on a host without
 * isolation), the host's health (isolation tier, guards, whether VMs can
 * start and what is missing), the VM budget in use per tile and the VM
 * policy editor, the VM disks on the host, and what the sandbox layer
 * refused or failed at. Polls GET /sandboxes every 2 s; the editor saves
 * through PUT /vm/policy what the admin set (zero = the default), never the
 * effective values. Reports through bx-admin-err / bx-admin-notice /
 * bx-admin-tab.
 */
import { LitElement, html, nothing } from 'lit';
import { xbinApi as api, jbody } from '/vendor/bx-kit.js';
import { base, runtimeCss, sandboxesCss } from '../admin-css.js';
import { fmtBytes, fmtDur, WithFilter, WithRouter } from '../shared.js';

const MODE = { vm: '⧉ VM', namespace: '🔒 ns', host: 'host' };
const MODE_TITLE = {
  vm: 'a VM: its own kernel (D89)',
  namespace: 'the rootless namespace sandbox',
  host: 'no sandbox: xbind runs without --isolate',
};
const KINDS = ['backend', 'terminal', 'agent', 'tile'];
const STAGE_TITLE = {
  refused: 'refused by policy: switched off, the VM budget or count, VMs unavailable here',
  start: "the sandbox or VM couldn't be set up or spawned",
  health: 'a VM backend never listened',
  exit: 'the sandbox layer itself died: the VM (exit 125) or the sandbox init (127)',
};
// The policy's fields, as the editor shows them.
const POLICY_FIELDS = [
  ['memMiB', 'memory per VM (MiB)'], ['vcpus', 'vCPUs per VM'], ['maxVMs', 'VMs at once'],
  ['budgetMiB', 'memory budget (MiB)'], ['diskGiB', 'VM terminal disk (GiB)'],
];
// The pieces a VM needs (GET /sandboxes health.vm.assets), by the VMM that needs them.
const PIECES = [
  ['kernel', 'vmlinux', 'both'], ['agent', 'xbin-vmagent', 'both'], ['mkfsErofs', 'mkfs.erofs', 'both'], ['bx', 'bx', 'both'],
  ['firecracker', 'firecracker', 'kvm'],
  ['qemu', 'qemu', 'emulation'], ['qemuBios', 'qemu bios', 'emulation'], ['qemuPvh', 'qemu pvh', 'emulation'], ['vhostVsock', 'vhost-vsock', 'emulation'],
];

export const mib = (n) => (n >= 1024 ? `${+(n / 1024).toFixed(1)} GiB` : `${n || 0} MiB`);
const ago = (t) => fmtDur((Date.now() - Date.parse(t)) / 1000) + ' ago';

export class BxAdminSandboxes extends WithRouter(WithFilter(LitElement)) {
  static properties = {
    _d: { state: true },        // GET /sandboxes
    _pol: { state: true },      // the policy draft (null = not editing)
    _busy: { state: true },
    _mode: { state: true },     // mode filter: '' | vm | namespace | host
    _openFail: { state: true }, // failure rows opened to their whole error
    _q: { state: true },
    _cats: { state: true },     // kind chips (WithFilter)
    _err: { state: true },
  };
  static styles = [base, runtimeCss, sandboxesCss];

  constructor() {
    super();
    this._q = ''; this._cats = new Set(); this._mode = '';
    this._openFail = new Set(); this._pol = null;
  }
  connectedCallback() {
    super.connectedCallback();
    this.load();
    this._timer = setInterval(() => this.load(), 2000);
  }
  disconnectedCallback() { super.disconnectedCallback(); clearInterval(this._timer); }

  async load() {
    try { this._d = await api('/sandboxes'); } catch (e) { this._fail(e); }
  }
  refresh() { return this.load(); }

  render() {
    const d = this._d;
    if (!d) return html`<span class="muted">loading…</span>`;
    const h = d.health || {};
    return html`
      ${this._healthCard(h)}
      ${h.isolation?.isolate ? this._budget(h.vm) : nothing}
      ${this._policy(h)}
      ${this._list(d)}
      ${this._disks(d.disks)}
      ${this._failures(d)}`;
  }

  // ---- health: isolation, guards, whether VMs can run here ----
  _healthCard(h) {
    const iso = h.isolation || {}, v = h.vm || {};
    const kv = (label, val) => html`<div class="kv"><span>${label}</span> <b>${val}</b></div>`;
    const p = iso.protections || {};
    const avail = v.available ? (v.emulated ? 'emulated' : 'kvm') : 'no';
    return html`<div class="hostcard" data-sbx-health>
        ${kv('isolation', iso.tier === 3 ? 'on (tier 3)' : iso.tier === 2 ? 'uids (tier 2)' : 'off (tier 1)')}
        ${iso.isolate ? kv('rootfs', iso.rootfs) : nothing}
        ${iso.isolate ? kv('terminal guard', html`<span title="seccomp mount guard · Landlock read guard">mount ${p.seccomp ? '✓' : '✗'} · read ${p.landlock ? `✓ (ABI ${p.landlockAbi})` : '✗'}</span>`) : nothing}
        ${iso.isolate ? kv('uid range', iso.uidRange ? 'delegated' : html`<span title=${iso.uidRangeNote || ''}>single uid ⚠</span>`) : nothing}
        ${kv('accounting', iso.cgroup ? 'cgroup v2' : '/proc sampling')}
        ${kv('VM sandboxes', html`<span data-vm-avail=${avail}>${v.available ? (v.emulated ? 'yes — emulated (no KVM)' : 'yes — KVM') : 'no'}${v.forced ? ` (XBIN_VM_ACCEL=${v.forced})` : ''}</span>`)}
      </div>
      ${!v.available && v.reason ? html`<div class="warn-line" data-vm-reason>⚠ ${v.reason}</div>` : nothing}
      ${v.available && v.emulated ? html`<div class="warn-line">${v.note}</div>` : nothing}
      ${this._assets(v)}`;
  }

  // What each VMM has here: a checklist of the pieces, and why a VMM can't run.
  _assets(v) {
    const have = v.assets || {};
    if (!Object.keys(have).length && !(v.missing || []).length) return nothing;
    const piece = ([k, label, who]) => html`<span class="sbx-piece ${have[k] ? 'ok' : 'no'}" data-piece=${k}
      title=${have[k] || `missing (${who === 'both' ? 'every VM' : who === 'kvm' ? 'VMs on KVM' : 'emulated VMs'} need it)`}>${have[k] ? '✓' : '✗'} ${label}</span>`;
    return html`<div class="sbx-assets">
      ${PIECES.map(piece)}
      ${v.kvm ? html`<div class="muted sbx-why">KVM: ${v.kvm}</div>` : nothing}
      ${v.emulation ? html`<div class="muted sbx-why">emulation: ${v.emulation}</div>` : nothing}
    </div>`;
  }

  // ---- the VM budget: what running VMs hold, per tile ----
  _budget(v) {
    if (!v || !v.policy) return nothing;
    const p = v.policy, used = v.used || { vms: 0, memMiB: 0 };
    const over = used.memMiB > p.budgetMiB || used.vms > p.maxVMs;
    const by = Object.entries(v.usedBy || {}).sort((a, b) => b[1].memMiB - a[1].memMiB);
    return html`<div class="sbx-budget" ?data-over=${over}>
      <div class="bar">${by.map(([t, u], i) => html`<span class="seg c${i % 6}"
        style="width:${Math.min(100, (u.memMiB / Math.max(1, p.budgetMiB)) * 100)}%"
        title="${t}: ${u.vms} VM${u.vms === 1 ? '' : 's'}, ${mib(u.memMiB)}"></span>`)}</div>
      <div class="lbl">VM memory <b>${mib(used.memMiB)}</b> of ${mib(p.budgetMiB)} · <b>${used.vms}</b> of ${p.maxVMs} VMs
        ${over ? html`<span class="warn-line"> — over the budget: it was lowered while these ran; new VMs are refused until they end</span>` : nothing}</div>
      ${by.length ? html`<div class="sbx-by">${by.map(([t, u], i) => html`<span class="pill"><span class="dot c${i % 6}"></span>${t} · ${mib(u.memMiB)}</span>`)}</div>` : nothing}
    </div>`;
  }

  // ---- the VM policy (PUT /vm/policy) ----
  _policy(h) {
    const v = h.vm || {}, iso = h.isolation || {};
    if (!v.policy) return nothing;
    const p = v.policy;
    const onOff = (b) => (b ? '✓' : '✗');
    if (!iso.isolate) {
      return html`<div class="sbx-policy muted" data-vm-policy="off">VM sandboxes need isolation (<span class="mono">xbind --isolate</span>),
        so the policy can't be set here.</div>`;
    }
    if (!this._pol) {
      return html`<div class="sbx-policy" data-vm-policy="view">
        policy: terminals ${onOff(p.terminals)} · backends ${onOff(p.backends)} · ${mib(p.memMiB)} · ${p.vcpus} vCPU per VM ·
        ${p.maxVMs} VMs · budget ${mib(p.budgetMiB)} · disk ${p.diskGiB} GiB
        <button class="act" data-edit-policy @click=${() => { this._pol = { ...(v.stored || {}) }; }}>edit</button>
      </div>`;
    }
    const d = this._pol, stored = v.stored || {};
    const set = (patch) => { this._pol = { ...d, ...patch }; };
    const num = (k) => html`<input type="number" min="0" name=${k} .value=${d[k] ? String(d[k]) : ''} placeholder=${String(p[k])}
      @input=${(e) => set({ [k]: e.target.value === '' ? 0 : Number(e.target.value) })}>`;
    const effBudget = d.budgetMiB || (d.maxVMs || p.maxVMs) * (d.memMiB || p.memMiB);
    const warn = [];
    if (d.backends && v.emulated) warn.push('VMs run emulated here: a backend in a VM is several times slower.');
    if (stored.backends && !d.backends) warn.push('Running VM backends keep going; their next start fails with the reason.');
    if (effBudget < (v.used?.memMiB || 0)) warn.push(`The budget is below what running VMs hold (${mib(v.used.memMiB)}); new VMs are refused until they end.`);
    return html`<form class="sbx-policy editor" data-vm-policy="edit" @submit=${(e) => { e.preventDefault(); this._savePolicy(); }}>
      <label><input type="checkbox" name="terminals" .checked=${!!d.terminals} @change=${(e) => set({ terminals: e.target.checked })}> VM terminals and agent sessions</label>
      <label><input type="checkbox" name="backends" .checked=${!!d.backends} @change=${(e) => set({ backends: e.target.checked })}> VM backends (tiles with <span class="mono">"vm"</span> in xbin.json)</label>
      <div class="sbx-fields">${POLICY_FIELDS.map(([k, label]) => html`<label>${label} ${num(k)}</label>`)}</div>
      <div class="muted" style="font-size:10.5px">empty = the default (shown); the budget defaults to VMs × memory</div>
      ${warn.map((w) => html`<div class="warn-line">⚠ ${w}</div>`)}
      <div>
        <button class="act go" type="submit" data-save-policy ?disabled=${this._busy}>save</button>
        <button class="act" type="button" @click=${() => { this._pol = null; }}>cancel</button>
      </div>
    </form>`;
  }

  async _savePolicy() {
    const d = this._pol;
    const body = { terminals: !!d.terminals, backends: !!d.backends };
    for (const [k] of POLICY_FIELDS) body[k] = Number(d[k]) || 0;
    this._busy = true;
    try {
      await api('/vm/policy', jbody(body, 'PUT'));
      this._ok();
      this._pol = null;
      this._emit('bx-admin-notice', 'VM policy saved');
      await this.load();
    } catch (e) { this._fail(e); }
    this._busy = false;
  }

  // ---- the live list, by tile ----
  _list(d) {
    const all = d.sandboxes || [];
    const kinds = KINDS.filter((k) => all.some((e) => e.kind === k));
    const rows = all.filter((e) => this._catActive(e.kind) && (!this._mode || e.mode === this._mode) &&
      this._match(e.tile, e.user, e.id, e.name, e.label));
    const groups = new Map();
    for (const e of rows) (groups.get(e.tile) || groups.set(e.tile, []).get(e.tile)).push(e);
    return html`<h4>sandboxes <span class="muted" style="font-weight:400">(${all.length})</span></h4>
      ${this._filterBar('filter by tile, user, name or id…', kinds, rows.length, all.length)}
      <div class="chips sbx-modes">${['', 'vm', 'namespace', 'host'].map((m) => html`<span class="chip ${this._mode === m ? 'on' : ''}"
        data-mode-chip=${m || 'all'} @click=${() => { this._mode = m; }}>${m ? MODE[m] : 'all modes'}</span>`)}</div>
      <table class="sbx">
        <tr><th>kind</th><th>mode</th><th>user</th><th>reserved</th><th>cpu</th><th>mem</th><th>pids</th><th>up</th><th>pid · leaf</th></tr>
        ${[...groups].map(([tile, es]) => this._group(tile, es))}
        ${rows.length === 0 ? html`<tr><td class="muted" colspan="9">${all.length ? 'no matching sandboxes' : 'nothing is running in a sandbox right now'}</td></tr>` : nothing}
      </table>`;
  }

  _group(tile, es) {
    // a tile's backend generations share its cgroup leaf: count it once
    const scopes = new Map();
    let reserved = 0;
    for (const e of es) {
      reserved += e.memMiB || 0;
      if (e.stats) scopes.set(e.stats.scope === 'tile' ? 'tile' : e.id, e.stats);
    }
    const mem = [...scopes.values()].reduce((n, s) => n + (s.mem || 0), 0);
    const curGen = Math.max(0, ...es.filter((e) => e.kind === 'backend').map((e) => e.gen || 0));
    // nest a sandbox under the entry it belongs to (parent); the rest at the top
    const ids = new Set(es.map((e) => e.id));
    const kids = new Map();
    for (const e of es) if (e.parent && ids.has(e.parent)) (kids.get(e.parent) || kids.set(e.parent, []).get(e.parent)).push(e);
    const out = [];
    const walk = (e, depth) => { out.push(this._row(e, depth, curGen)); for (const k of kids.get(e.id) || []) walk(k, depth + 1); };
    for (const e of es) if (!e.parent || !ids.has(e.parent)) walk(e, 0);
    const owner = es.find((e) => e.owner)?.owner;
    return html`<tr class="sbx-tile" data-sbx-tile=${tile}><td colspan="9">
        <span class="mono">${tile}</span>${owner ? html` <span class="muted">· ${owner}</span>` : nothing}
        <span class="muted"> · ${es.length} sandbox${es.length === 1 ? '' : 'es'}${reserved ? ` · ${mib(reserved)} reserved` : ''}${mem ? ` · ${fmtBytes(mem)} in use` : ''}</span>
      </td></tr>${out}`;
  }

  _row(e, depth, curGen) {
    const s = e.stats;
    // a backend's stats are its tile's (every generation): shown on the current one
    const showStats = s && (s.scope !== 'tile' || e.gen === curGen);
    const kind = e.kind === 'backend' ? html`backend <span class="muted">g${e.gen}${e.gen === curGen ? '' : ' · draining'}</span>`
      : e.kind === 'agent' ? html`agent <span class="muted">${e.label || ''}${e.name ? ' · ' + e.name : ''}${e.status ? ' · ' + e.status : ''}</span>`
      : html`${e.kind} <span class="muted">${e.name || ''}</span>`;
    return html`<tr data-sbx-id=${e.id} data-sbx-kind=${e.kind} data-sbx-mode=${e.mode} data-depth=${depth}>
      <td style="padding-left:${depth * 18}px">${depth ? html`<span class="muted">↳ </span>` : nothing}${kind}</td>
      <td><span class="sbx-mode ${e.mode}" title=${MODE_TITLE[e.mode] || ''}>${MODE[e.mode] || e.mode}</span>${e.accel === 'emulate' ? html` <span class="pill" title="QEMU's software emulation: no KVM here">emulated</span>` : nothing}${e.restricted ? html` <span class="pill" title="a restricted user's session: the D17d limits">limited</span>` : nothing}</td>
      <td class="mono">${e.user || '—'}</td>
      <td class="mono">${e.memMiB ? `${mib(e.memMiB)} · ${e.vcpus} vCPU` : '—'}</td>
      <td class="num">${showStats ? s.cpu.toFixed(1) + '%' : ''}</td>
      <td class="num" title=${showStats && s.scope === 'tile' ? "the tile's cgroup: every generation" : ''}>${showStats ? fmtBytes(s.mem) : ''}</td>
      <td class="num">${showStats ? s.pids : ''}</td>
      <td class="mono">${fmtDur(e.uptimeSec)}</td>
      <td class="mono muted" title=${e.disk ? 'disk ' + e.disk : ''}>${e.pid || '—'}${e.leaf ? ' · ' + e.leaf : ''}${e.disk ? ' · 💾' : ''}</td>
    </tr>`;
  }

  // ---- VM disks on the host ----
  _disks(disks) {
    if (!disks || !disks.length) return nothing;
    return html`<h4>VM disks</h4>
      <table class="sbx">
        <tr><th>tile</th><th>size</th><th>on disk</th><th>in use</th><th>path</th></tr>
        ${disks.map((d) => html`<tr data-sbx-disk=${d.key}>
          <td class="mono">${d.tile || html`<span class="muted" title="no tile has this key now (deleted or renamed)">${d.key}</span>`}${d.sandbox
            ? html` <span class="muted" title="a tile sandbox's disk" data-sbx-disk-sandbox=${d.sandbox}>· sandbox ${d.sandbox}</span>` : nothing}</td>
          <td class="num">${fmtBytes(d.apparentBytes)}</td>
          <td class="num" title="sparse: what it takes on the host">${fmtBytes(d.allocatedBytes)}</td>
          <td>${d.inUse ? '✓' : html`<span class="muted">—</span>`}</td>
          <td class="mono muted">${d.path}</td>
        </tr>`)}
      </table>`;
  }

  // ---- what the sandbox layer refused or failed at ----
  _failures(d) {
    const fs = d.failures || [], c = d.failureCounts || {};
    const total = Object.values(c).reduce((a, b) => a + b, 0);
    const toggle = (i) => { const s = new Set(this._openFail); s.has(i) ? s.delete(i) : s.add(i); this._openFail = s; };
    return html`<h4>recent failures <span class="muted" style="font-weight:400">
      ${total ? `since start: ${Object.entries(c).map(([k, n]) => `${k} ${n}`).join(' · ')}` : '— none since start'}</span></h4>
      ${fs.length ? html`<table class="sbx">
        <tr><th>when</th><th>stage</th><th>tile</th><th>kind</th><th>mode</th><th>error</th></tr>
        ${fs.map((f, i) => html`<tr data-sbx-failure data-stage=${f.stage} style="cursor:pointer" @click=${() => toggle(i)}>
          <td class="mono">${ago(f.time)}${f.count > 1 ? html` <b title="the same failure, again">×${f.count}</b>` : nothing}</td>
          <td><span class="sbx-stage ${f.stage}" title=${STAGE_TITLE[f.stage] || ''}>${f.stage}</span></td>
          <td class="mono">${f.tile}${f.user ? html` <span class="muted">· ${f.user}</span>` : nothing}</td>
          <td>${f.kind}</td>
          <td>${MODE[f.mode] || f.mode}</td>
          <td class="sbx-err">${this._openFail.has(i) || f.error.length <= 140 ? f.error : f.error.slice(0, 140) + '…'}</td>
        </tr>`)}
      </table>` : nothing}`;
  }
}
customElements.define('bx-admin-sandboxes', BxAdminSandboxes);
