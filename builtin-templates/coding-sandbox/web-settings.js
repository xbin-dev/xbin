// web-settings.js — the operators' Settings tab on the web: the isolation
// mode, the sandbox networks (the sandbox-net classes and their bindings),
// the sizes, the quotas and the rest (layout, idle stop, mounts). Each
// section edits a draft (ui.forms) and saves its own top-level config
// fields (PUT /ops/config replaces those whole: API.md §Config).
import { html, nothing } from '/vendor/lit-all.min.js';
import * as O from './model/ops.js';
import * as F from './model/format.js';

export function settingsTab(app, ui) {
  if (app.opsErr) return html`<div class="err">${app.opsErr}</div>`;
  if (!app.ops) return html`<div class="muted">loading…</div>`;
  return html`${modeTpl(app, ui)}${egressTpl(app)}${sizesTpl(app, ui)}${quotasTpl(app, ui)}${advancedTpl(app, ui)}`;
}

function modeTpl(app, ui) {
  const m = app.mode();
  const pick = ui.forms.mode ?? m.value;
  return html`<h4>Isolation</h4>
    <div class="row" id="mode">
      <select id="mode-pick" @change=${(e) => { ui.forms.mode = e.target.value; ui.paint(); }}>
        ${m.options.map((o) => html`<option value=${o.value} ?selected=${pick === o.value}>${o.label}${o.why ? ` — unavailable: ${o.why}` : ''}</option>`)}</select>
      <button class="go" id="mode-save" ?disabled=${pick === m.value || !!ui.busy}
        @click=${() => ui.run('mode', () => app.saveConfig({ mode: pick }).then(() => { ui.forms.mode = undefined; }), 'the mode is saved: it applies to new sandboxes')}>Save</button>
    </div>
    ${m.now ? html`<div class="muted small" id="mode-now">Now ${m.nowText}; existing ones keep theirs (each says its isolation).</div>`
      : html`<div class="err small" id="mode-blocked">No new sandbox can be made now: ${m.blocked}</div>`}`;
}

function egressTpl(app) {
  const b = app.backend();
  return html`<h4>Networks</h4>
    <div class="muted small">What sandboxes may reach is a <code>sandbox-net</code> class of this tile's, bound by an approver
      (the binding panel, or <code>bx bind</code>); unbound, sandboxes get no network. Never the network of the manager itself.</div>
    <table class="grid small" id="egress"><thead><tr><th>class</th><th>bound to</th><th>reaches</th><th>offered</th><th></th></tr></thead>
    <tbody>${b.classes.map((c) => html`<tr data-class=${c.slot}><td class="mono">${c.slot}</td><td class="mono">${c.ref || html`<span class="muted">unbound</span>`}</td>
      <td>${F.EGRESS[c.reach] || c.reach}</td><td>${c.offered ? 'yes' : 'no'}</td>
      <td>${c.note ? html`<span class="muted">${c.note}</span>` : nothing}${!c.ref ? html`<code class="small">${c.bind}</code>` : nothing}</td></tr>`)}</tbody></table>`;
}

function sizesTpl(app, ui) {
  const forms = ui.forms.sizes || O.sizeForms(app.ops.config.sizes);
  const set = (i, k, v) => { ui.forms.sizes = forms.map((f, j) => (j === i ? { ...f, [k]: v } : k === 'default' && v ? { ...f, default: false } : f)); ui.paint(); };
  const save = () => {
    const r = O.applySizes(forms);
    if (r.error) { ui.err = r.error; ui.paint(); return; }
    ui.run('sizes', () => app.saveConfig({ sizes: r.sizes }).then(() => { ui.forms.sizes = null; }), 'the sizes are saved');
  };
  const num = (i, k) => html`<input type="number" min="1" class="n" data-k=${k} .value=${String(forms[i][k] ?? '')} @change=${(e) => set(i, k, e.target.value)}>`;
  return html`<h4>Sizes</h4>
    <table class="grid small" id="sizes"><thead><tr><th>id</th><th>title</th><th>memory MiB</th><th>vCPUs</th><th>disk GiB</th><th>default</th><th></th></tr></thead>
    <tbody>${forms.map((f, i) => html`<tr data-size=${f.id}>
      <td><input class="mono n2" .value=${f.id} @change=${(e) => set(i, 'id', e.target.value)}></td>
      <td><input .value=${f.title} @change=${(e) => set(i, 'title', e.target.value)}></td>
      <td>${num(i, 'memMiB')}</td><td>${num(i, 'vcpus')}</td><td>${num(i, 'diskGiB')}</td>
      <td><input type="radio" name="size-default" .checked=${!!f.default} @change=${() => set(i, 'default', true)}></td>
      <td><button class="small rm" @click=${() => { ui.forms.sizes = forms.filter((_, j) => j !== i); ui.paint(); }}>Remove</button></td></tr>`)}</tbody></table>
    <div class="row"><button id="size-add" @click=${() => { ui.forms.sizes = [...forms, { id: '', title: '', memMiB: 2048, vcpus: 2, diskGiB: 20 }]; ui.paint(); }}><bx-icon name="plus"></bx-icon>Size</button>
      <button class="go" id="sizes-save" ?disabled=${!ui.forms.sizes || !!ui.busy} @click=${save}>Save sizes</button>
      ${ui.forms.sizes ? html`<button @click=${() => { ui.forms.sizes = null; ui.paint(); }}>Discard</button>` : nothing}
      <span class="muted small">A size over the substrate's per-sandbox caps isn't offered.</span></div>`;
}

function quotasTpl(app, ui) {
  const quotas = ui.forms.quotas || app.ops.config.quotas || {};
  const rows = O.quotaRows({ quotas });
  const nf = ui.forms.quotaNew || { kind: 'consumer', key: '' };
  const edit = (r, k, v) => { ui.forms.quotas = O.setQuota(quotas, r.kind, r.key, { ...r.values, [k]: v }); ui.paint(); };
  const keys = Object.keys(O.QUOTA_LABELS);
  return html`<h4>Quotas</h4>
    <div class="muted small">0 is no limit. A consumer's quota counts the sandboxes it made; a person's, those they own across consumers.
      The substrate's own limits for this tile bind too.</div>
    <table class="grid small" id="quotas"><thead><tr><th></th>${keys.map((k) => html`<th>${O.QUOTA_LABELS[k]}</th>`)}<th></th></tr></thead>
    <tbody>${rows.map((r) => html`<tr data-quota=${r.kind + ':' + r.key}><td>${r.label}</td>
      ${keys.map((k) => html`<td><input type="number" min="0" class="n" data-k=${k} .value=${String(r.values[k])} @change=${(e) => edit(r, k, e.target.value)}></td>`)}
      <td>${r.removable ? html`<button class="small rm" @click=${() => { ui.forms.quotas = O.setQuota(quotas, r.kind, r.key, null); ui.paint(); }}>Remove</button>` : nothing}</td></tr>`)}</tbody></table>
    <div class="row">
      <select id="quota-kind" @change=${(e) => { ui.forms.quotaNew = { ...(ui.forms.quotaNew || nf), kind: e.target.value }; }}>
        <option value="consumer" ?selected=${nf.kind === 'consumer'}>a consumer</option><option value="person" ?selected=${nf.kind === 'person'}>a person</option></select>
      <input id="quota-key" class="mono" placeholder="apps/agent, or a user id" .value=${nf.key} @input=${(e) => { ui.forms.quotaNew = { ...(ui.forms.quotaNew || nf), key: e.target.value }; }}>
      <button id="quota-add" @click=${() => {
        const n = ui.forms.quotaNew || nf; // as typed (typing doesn't repaint)
        const key = String(n.key || '').trim();
        if (!key) return;
        const base = n.kind === 'consumer' ? quotas.consumer : quotas.person;
        ui.forms.quotas = O.setQuota(quotas, n.kind, key, { sandboxes: 1, ...(base || {}) });
        ui.forms.quotaNew = null;
        ui.paint();
      }}><bx-icon name="plus"></bx-icon>Its own quota</button>
      <button class="go" id="quotas-save" ?disabled=${!ui.forms.quotas || !!ui.busy}
        @click=${() => ui.run('quotas', () => app.saveConfig({ quotas }).then(() => { ui.forms.quotas = null; }), 'the quotas are saved')}>Save quotas</button>
      ${ui.forms.quotas ? html`<button @click=${() => { ui.forms.quotas = null; ui.paint(); }}>Discard</button>` : nothing}
    </div>`;
}

function advancedTpl(app, ui) {
  const cfg = app.ops.config;
  const fresh = () => ui.forms.adv || { layout: { ...cfg.layout }, autoStopMin: cfg.autoStopMin || 0, mounts: [...(cfg.mounts || [])], mount: '' };
  const f = fresh();
  const set = (patch) => { ui.forms.adv = { ...fresh(), ...patch }; ui.paint(); };
  const lay = (k, num = false) => html`<label>${k} <input class="mono ${num ? 'n' : ''}" type=${num ? 'number' : 'text'} data-k=${k} .value=${String(f.layout[k] ?? '')}
    @change=${(e) => set({ layout: { ...f.layout, [k]: num ? Number(e.target.value) : e.target.value } })}></label>`;
  const addMount = () => {
    const g = fresh(); // as typed (typing doesn't repaint)
    const r = O.parseMount(g.mount);
    if (r.error) { ui.err = r.error; ui.paint(); return; }
    set({ mounts: [...g.mounts, r.mount], mount: '' });
  };
  return html`<h4>Layout, idle stop, mounts</h4>
    <div class="muted small">Where people work in every new sandbox (on a substrate that runs everything as root, root at /root).</div>
    <div class="row" id="layout">${lay('workdir')}${lay('home')}${lay('user')}${lay('uid', true)}${lay('gid', true)}${lay('shell')}</div>
    <div class="row"><label>idle stop, minutes (0: the substrate's) <input id="autostop" type="number" min="0" max="1440" class="n" .value=${String(f.autoStopMin)}
      @change=${(e) => set({ autoStopMin: Number(e.target.value) })}></label></div>
    <div class="small"><span class="muted">mounts in every new sandbox — filesystem resources this tile holds:</span>
      ${f.mounts.length ? html`<ul class="plain" id="mounts">${f.mounts.map((m, i) => html`<li class="mono">${O.mountText(m)}
        <button class="small rm" @click=${() => set({ mounts: f.mounts.filter((_, j) => j !== i) })}>Remove</button></li>`)}</ul>` : html` none`}</div>
    <div class="row"><input id="mount-new" class="mono wide" placeholder="res:${app.self}/cache:go /cache ro" .value=${f.mount}
        @input=${(e) => { ui.forms.adv = { ...fresh(), mount: e.target.value }; }}>
      <button id="mount-add" @click=${addMount}><bx-icon name="plus"></bx-icon>Mount</button></div>
    <div class="row"><button class="go" id="adv-save" ?disabled=${!ui.forms.adv || !!ui.busy}
        @click=${() => { const g = fresh(); ui.run('adv', () => app.saveConfig({ layout: g.layout, autoStopMin: g.autoStopMin, mounts: g.mounts }).then(() => { ui.forms.adv = null; }),
          'saved: new sandboxes get it'); }}>Save</button>
      ${ui.forms.adv ? html`<button @click=${() => { ui.forms.adv = null; ui.paint(); }}>Discard</button>` : nothing}</div>
    <div class="muted small" id="backend-name">Backend: <b>${app.backend().name}</b> — registered in this build:
      ${((app.ops.backend && app.ops.backend.registered) || []).join(', ')} (changing it is PUT /ops/config {backend}, with no sandboxes left: API.md).</div>`;
}
