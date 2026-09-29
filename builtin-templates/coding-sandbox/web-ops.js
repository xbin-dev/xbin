// web-ops.js — the operators' Sandboxes and Images tabs on the web: every
// consumer's sandboxes (metadata only: model/ops.js sandboxRows) with their
// lifecycle and snapshots, and who may use each (shown: only its home
// consumer or its owner changes that), usage against the quotas, the
// substrate's state and orphans; the images with their builds and the image
// editor. ui is web.js's page state; ui.run does an action and repaints.
// sharesTpl is "Yours"' sharing form (web-mine.js).
import { html, nothing } from '/vendor/lit-all.min.js';
import * as O from './model/ops.js';
import * as F from './model/format.js';
import { portsTpl } from './web-ports.js';

const ask = (text) => !text || confirm(text);

// --- Sandboxes -----------------------------------------------------------------------

export function opsTab(app, ui) {
  if (app.opsErr) return html`<div class="err" id="ops-err">${app.opsErr}</div>`;
  if (!app.ops) return html`<div class="muted">loading…</div>`;
  const b = app.backend();
  const rows = app.opRows();
  return html`
    ${backendTpl(b)}
    <h4>Sandboxes (${rows.length})</h4>
    ${rows.length ? html`<table class="grid" id="ops-table">
      <thead><tr><th>sandbox</th><th>consumer</th><th>owner</th><th>state</th><th>image</th><th>size</th><th>network</th>
        <th>isolation</th><th>disk</th><th>active</th><th></th></tr></thead>
      <tbody>${rows.map((r) => rowTpl(app, ui, r))}</tbody>
    </table>` : html`<div class="muted" id="ops-empty">None yet — bind a consumer:
      <code>bx bind apps/agent sandboxes+=${app.self}</code></div>`}
    ${usageTpl(app)}
    ${orphansTpl(app, ui)}`;
}

function backendTpl(b) {
  return html`<div class="substrate" id="substrate">
      <span class="muted">substrate</span> <b>${b.name}</b>
      ${b.modes.length ? html` · <span class="muted">modes</span> ${b.modes.map((m) => m.mode + (m.accel ? ` (${m.accel})` : '')).join(', ')}` : nothing}
      ${b.caps.length ? html` · <span class="muted">offers</span> <code>${b.caps.join(' ')}</code>` : nothing}
      ${b.egress.length ? html` · <span class="muted">networks</span> ${b.egress.map((e) => F.EGRESS[e] || e).join(', ')}` : nothing}
    </div>
    ${b.errors.map((e) => html`<div class="err substrate-err">${e}</div>`)}
    ${b.hint ? html`<div class="note small" id="grant-hint">${b.hint}</div>` : nothing}
    ${b.notes.map((n) => html`<div class="note small offer-note">${n}</div>`)}`;
}

function rowTpl(app, ui, r) {
  const open = ui.open[r.id] || '';
  const toggle = (what) => {
    ui.open = { [r.id]: open === what ? '' : what };
    if (what === 'snapshots' && open !== what) app.opSnapshots(r.id);
    ui.paint();
  };
  const act = (a) => {
    if (a.id === 'snapshots') return toggle(a.id);
    if (!ask(a.confirm)) return;
    ui.run(`${r.id}:${a.id}`, () => app.opAct(r.id, a.id), a.id === 'delete' ? `${r.name} deleted` : '');
  };
  return html`<tr class="sb" data-id=${r.id}>
      <td><b>${r.name}</b><div class="mono muted small">${r.id}</div>
        <div class="muted small who" title="who may use it — only its home consumer, or its owner there, changes this">${r.who}</div></td>
      <td class="mono small">${r.consumer}</td>
      <td>${r.owner}</td>
      <td><span class="pill ${r.tone}" title=${r.stateDetail}>${r.stateLabel}</span>
        ${r.stateDetail ? html`<div class="muted small detail">${r.stateDetail}</div>` : nothing}
        ${r.outdated ? html`<div class="muted small" title="the substrate's base image moved on">base outdated</div>` : nothing}</td>
      <td title=${r.image}>${r.imageId}</td>
      <td title=${r.sizeText}>${r.size}</td>
      <td>${r.egressText}</td>
      <td>${r.isolation}</td>
      <td class="num">${r.disk}</td>
      <td class="small">${r.lastText}</td>
      <td class="acts">${r.actions.map((a) => html`<button class="small ${a.danger ? 'rm' : ''} ${open === a.id ? 'on' : ''}" data-act=${a.id}
        ?disabled=${!!ui.busy} @click=${() => act(a)}>${a.label}</button>`)}
        <button class="small ${open === 'ports' ? 'on' : ''}" data-act="ports" title="whether it serves ports (live previews), and a probe of one"
          @click=${() => toggle('ports')}>Ports</button></td>
    </tr>
    ${open === 'snapshots' ? html`<tr class="panel"><td colspan="11">${snapshotsTpl(app, ui, r)}</td></tr>` : nothing}
    ${open === 'ports' ? html`<tr class="panel"><td colspan="11">${portsTpl(app, ui, r)}</td></tr>` : nothing}`;
}

function snapshotsTpl(app, ui, r) {
  const s = app.snaps && app.snaps.id === r.id ? app.snaps : null;
  const form = ui.forms['snap:' + r.id] || '';
  const take = () => {
    const name = ui.forms['snap:' + r.id] || ''; // as typed (typing doesn't repaint)
    ui.run('snap', () => app.opSnapshot(r.id, name).then(() => { ui.forms['snap:' + r.id] = ''; }), 'snapshot taken');
  };
  return html`<div class="snaps" id="snaps">
    <b>Snapshots of ${r.name}</b>
    ${!s || (!s.list && !s.err) ? html`<div class="muted">loading…</div>` : nothing}
    ${s && s.err ? html`<div class="err">${s.err}</div>` : nothing}
    ${s && s.list ? (s.list.length ? html`<table class="grid small">${s.list.map((x) => html`<tr data-snap=${x.id}>
        <td>${x.name || x.id}</td><td class="mono muted">${x.id}</td><td>${F.ago(x.created)}</td><td>${x.bytes ? F.bytes(x.bytes) : ''}</td>
        <td class="acts"><button class="small" data-act="restore" ?disabled=${!!ui.busy}
            @click=${() => ask(`Restore ${r.name} to “${x.name || x.id}”? What changed since is lost, and its running commands are killed.`) &&
              ui.run('restore', () => app.opRestore(r.id, x.id), `${r.name} restored to ${x.name || x.id}`)}>Restore</button>
          <button class="small rm" data-act="drop" ?disabled=${!!ui.busy}
            @click=${() => ask(`Delete the snapshot “${x.name || x.id}”?`) && ui.run('snapdel', () => app.opSnapDelete(r.id, x.id))}>Delete</button></td></tr>`)}</table>`
      : html`<div class="muted">none</div>`) : nothing}
    <div class="row"><input id="snap-name" placeholder="a name (optional)" .value=${form} @input=${(e) => { ui.forms['snap:' + r.id] = e.target.value; }}>
      <button id="snap-take" ?disabled=${!!ui.busy} @click=${take}>
        Take a snapshot</button><span class="muted small">(it may stop the sandbox briefly)</span></div>
  </div>`;
}

// sharesTpl: a sandbox's shares, and the form to add one (Yours: a person's
// own sandboxes). save sets them whole.
export function sharesTpl(ui, shares, save, key) {
  const now = () => ui.forms['share:' + key] || { consumer: '', users: '' }; // as typed (typing doesn't repaint)
  const f = now();
  const set = (k) => (e) => { ui.forms['share:' + key] = { ...now(), [k]: e.target.value }; };
  const add = () => {
    const g = now();
    const r = O.shareWith(shares, g.consumer, g.users);
    if (r.error) { ui.err = r.error; ui.paint(); return; }
    ui.run('share', () => save(r.shares).then(() => { ui.forms['share:' + key] = null; }), `shared with ${g.consumer.trim()}`);
  };
  return html`<div class="shares" id="shares">
    <b>Shared with</b>
    ${shares.length ? html`<ul class="plain">${shares.map((s) => html`<li data-consumer=${s.consumer}><span class="mono">${s.consumer}</span>
      — ${F.usersText(s.users)} <button class="small rm" data-act="unshare" ?disabled=${!!ui.busy}
        @click=${() => ui.run('unshare', () => save(O.unshare(shares, s.consumer)), `no longer shared with ${s.consumer}`)}>Stop sharing</button></li>`)}</ul>`
      : html`<div class="muted">no other consumer</div>`}
    <div class="row"><input id="share-consumer" class="mono" placeholder="apps/sandbox-terminal" .value=${f.consumer} @input=${set('consumer')}>
      <input id="share-users" placeholder="* (everyone it serves) or alice, bob" .value=${f.users} @input=${set('users')}>
      <button id="share-add" ?disabled=${!!ui.busy} @click=${add}>Share</button></div>
  </div>`;
}

function usageTpl(app) {
  const rows = app.usage();
  if (!rows.length) return nothing;
  return html`<h4>Usage against the quotas</h4>
    <table class="grid small" id="usage"><thead><tr><th></th>${['sandboxes', 'running', 'memory MiB', 'vCPUs', 'disk GiB'].map((h) => html`<th class="num">${h}</th>`)}</tr></thead>
    <tbody>${rows.map((u) => html`<tr class=${u.full ? 'full' : ''} data-who=${u.who}>
      <td><span class="muted">${u.kind}</span> <span class="mono">${u.who}</span>${u.override ? html` <span class="muted">(its own quota)</span>` : nothing}</td>
      ${u.cells.map((c) => html`<td class="num ${c.over ? 'over' : c.full ? 'fullc' : ''}">${c.text}</td>`)}</tr>`)}</tbody></table>`;
}

function orphansTpl(app, ui) {
  const list = (app.ops && app.ops.orphans) || [];
  if (!list.length) return nothing;
  return html`<h4>Orphans</h4>
    <div class="muted small">Sandboxes the substrate keeps for this tile that the manager doesn't know (a creation cut short).</div>
    <ul class="plain" id="orphans">${list.map((o) => html`<li><span class="mono">${o.name}</span> ${F.STATES[o.state] || o.state}
      <button class="small rm" ?disabled=${!!ui.busy} @click=${() => ask(`Delete the orphan ${o.name} at the substrate?`) &&
        ui.run('orphan', () => app.dropOrphan(o.name), `${o.name} deleted`)}>Delete</button></li>`)}</ul>`;
}

// --- Images --------------------------------------------------------------------------

export function imagesTab(app, ui) {
  if (app.opsErr) return html`<div class="err">${app.opsErr}</div>`;
  if (!app.ops) return html`<div class="muted">loading…</div>`;
  const rows = app.images();
  const images = app.ops.config.images || [];
  const form = ui.forms.image || null;
  const save = () => {
    const f = ui.forms.image; // as typed (typing doesn't repaint)
    const r = O.applyImage(images, f);
    if (r.error) { ui.err = r.error; ui.paint(); return; }
    ui.run('image', () => app.saveConfig({ images: r.images }).then(() => { ui.forms.image = null; }), `image ${f.id} saved`);
  };
  const remove = (id) => {
    const r = O.removeImage(images, id);
    if (r.error) { ui.err = r.error; ui.paint(); return; }
    if (ask(`Remove the image ${id}? Its sandboxes keep running; no new one is made from it.`)) ui.run('image', () => app.saveConfig({ images: r.images }), `image ${id} removed`);
  };
  return html`<div class="muted small">An image is the substrate's base plus an optional setup script, run once as root and
      snapshotted; every later sandbox of it is a clone (API.md §Images).</div>
    <div id="images">${rows.map((im) => html`<div class="card image" data-image=${im.id}>
      <div class="hd"><b>${im.title}</b> <span class="mono muted">${im.id}</span>
        ${im.default ? html`<span class="pill">default</span>` : nothing}
        <span class="pill ${im.tone}" title=${im.kept}>${im.buildText}</span>
        ${!im.offered ? html`<span class="pill warn" title="hello leaves it out (Sandboxes shows why)">not offered</span>` : nothing}
        <span class="grow"></span>
        ${im.setup ? html`<button class="small" data-act="build" ?disabled=${!im.canBuild || !!ui.busy}
          @click=${() => ui.run('build', () => app.build(im.id), `building ${im.id}…`)}>${im.built ? 'Rebuild' : 'Build now'}</button>` : nothing}
        <button class="small" data-act="edit" @click=${() => { ui.forms.image = O.imageForm(images.find((x) => x.id === im.id)); ui.paint(); }}>Edit</button>
        <button class="small rm" data-act="remove" ?disabled=${!!ui.busy} @click=${() => remove(im.id)}>Remove</button></div>
      ${im.tools.length ? html`<div class="small"><span class="muted">tools</span> ${im.tools.join(', ')}</div>` : nothing}
      ${im.built && im.built.detail ? html`<div class="err small">${im.built.detail}</div>` : nothing}
      ${im.kept ? html`<div class="note small kept">${im.kept}</div>` : nothing}
      ${im.setup ? html`<details><summary class="small">setup script${im.buildEgress ? ` (network while it builds: ${im.buildEgress})` : ''}</summary><pre>${im.setup}</pre></details>` : nothing}
      ${im.built && im.built.log ? html`<details class="log"><summary class="small">the last build's output</summary><pre>${im.built.log}</pre></details>` : nothing}
    </div>`)}</div>
    ${form ? imageFormTpl(ui, form, save) : html`<button class="go" id="image-new" @click=${() => { ui.forms.image = O.imageForm(); ui.paint(); }}>＋ New image</button>`}`;
}

function imageFormTpl(ui, form, save) {
  const set = (k, v) => { ui.forms.image = { ...ui.forms.image, [k]: v }; };
  return html`<div class="card form" id="image-form">
    <b>${form.was ? `Edit ${form.was}` : 'New image'}</b>
    <div class="row"><label>id <input id="img-id" class="mono" .value=${form.id} @input=${(e) => set('id', e.target.value)}></label>
      <label>title <input id="img-title" class="wide" .value=${form.title} @input=${(e) => set('title', e.target.value)}></label></div>
    <div class="row"><label>tools <input id="img-tools" class="wide" placeholder="git, node, pnpm" .value=${form.tools} @input=${(e) => set('tools', e.target.value)}></label>
      <label>network while it builds <select id="img-egress" @change=${(e) => set('buildEgress', e.target.value)}>
        ${[['', 'internet where bound, else none'], ['none', 'none'], ['internet', 'internet'], ['open', 'open']].map(([v, l]) =>
          html`<option value=${v} ?selected=${form.buildEgress === v}>${l}</option>`)}</select></label>
      <label class="chk"><input type="checkbox" id="img-default" .checked=${form.default} @change=${(e) => set('default', e.target.checked)}> the default</label></div>
    <label class="block">setup script — run as root in the workdir, once; empty: the substrate's base
      <textarea id="img-setup" rows="6" class="mono" placeholder="apt-get update && apt-get install -y nodejs npm" .value=${form.setup}
        @input=${(e) => set('setup', e.target.value)}></textarea></label>
    <div class="row"><button class="go" id="img-save" ?disabled=${!!ui.busy} @click=${save}>Save</button>
      <button id="img-cancel" @click=${() => { ui.forms.image = null; ui.paint(); }}>Cancel</button>
      <span class="muted small">A changed script rebuilds the image at its next use.</span></div>
  </div>`;
}
