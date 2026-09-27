// web-mine.js — "Your sandboxes" on the web: the page as a consumer of its
// own (API.md "Who is asking"): the sandboxes you may use, the create form,
// their lifecycle and, for the one you open, its files (browse, view,
// download, upload, make a directory, remove), a terminal (<bx-terminal
// src> on this tile's own tty route, dialled with the frame token so the
// manager sees you verified — docs/elements.md) and who may use it.
import { html, nothing, keyed } from '/vendor/lit-all.min.js';
import * as F from './model/format.js';
import * as M from './model/mine.js';
import { sharesTpl } from './web-ops.js';

const ask = (text) => !text || confirm(text);

export function mineTab(app, ui) {
  const rows = app.myRows();
  const sel = ui.sel && rows.find((r) => r.id === ui.sel);
  if (ui.sel && !sel && app.mine) { ui.sel = ''; ui.term = null; }
  return html`
    ${app.helloErr ? html`<div class="err" id="hello-err">${app.helloErr}</div>` : nothing}
    ${app.mineErr ? html`<div class="err" id="mine-err">${app.mineErr}</div>` : nothing}
    ${!app.operator ? html`<div class="muted small" id="reader-note">Sandboxes you make here are yours (the page is a consumer of its own);
      the tile's operators see every consumer's.</div>` : nothing}
    ${createTpl(app, ui)}
    <div id="mine">${rows.map((r) => rowTpl(app, ui, r))}</div>
    ${app.mine && !rows.length ? html`<div class="muted" id="mine-empty">You have no sandbox here yet.</div>` : nothing}
    ${sel ? detailTpl(app, ui, sel) : nothing}`;
}

function createTpl(app, ui) {
  if (!ui.forms.create) {
    const c = app.createForm({});
    return html`<button class="go" id="new" ?disabled=${!!c.cant} title=${c.cant} @click=${() => { ui.forms.create = {}; ui.paint(); }}>＋ New sandbox</button>
      ${c.cant ? html`<span class="muted small">${c.cant}</span>` : nothing}`;
  }
  const vm = app.createForm(ui.forms.create);
  const set = (k) => (e) => { ui.forms.create = { ...vm.f, ...ui.forms.create, [k]: e.target.value }; if (k !== 'name') ui.paint(); };
  const opts = (list, v) => list.map((o) => html`<option value=${o.value} ?selected=${o.value === v} title=${o.detail || ''}>${o.label}</option>`);
  const go = () => {
    const now = app.createForm(ui.forms.create);
    if (now.error) { ui.err = now.error; ui.paint(); return; }
    ui.run('create', () => app.create(now.f).then((s) => { ui.forms.create = null; ui.sel = s.id; }), `${now.f.name} is made`);
  };
  return html`<div class="card form" id="create">
    <b>New sandbox</b>
    ${vm.notes.map((n) => html`<div class="note small">${n}</div>`)}
    <div class="row"><label>name <input id="cf-name" .value=${vm.f.name} placeholder="api-dev" @input=${set('name')} @keydown=${(e) => { if (e.key === 'Enter') go(); }}></label>
      <label>image <select id="cf-image" @change=${set('image')}>${opts(vm.images, vm.f.image)}</select></label>
      <label>size <select id="cf-size" @change=${set('size')}>${opts(vm.sizes, vm.f.size)}</select></label></div>
    <div class="row"><label>network <select id="cf-egress" @change=${set('egress')}>${opts(vm.egress, vm.f.egress)}</select></label>
      <label>who may use it <select id="cf-vis" @change=${set('visibility')}>
        <option value="private" ?selected=${vm.f.visibility !== 'team'}>you</option>
        <option value="team" ?selected=${vm.f.visibility === 'team'}>anyone who may open this page</option></select></label></div>
    <div class="row"><button class="go" id="cf-create" ?disabled=${!!ui.busy} @click=${go}>${ui.busy === 'create' ? 'Creating…' : 'Create'}</button>
      <button id="cf-cancel" @click=${() => { ui.forms.create = null; ui.paint(); }}>Cancel</button>
      <span class="muted small">An image with a setup script builds on its first use: that one takes a while.</span></div>
  </div>`;
}

function rowTpl(app, ui, r) {
  const act = (a) => {
    if (!ask(a.confirm)) return;
    if (a.id === 'delete' && ui.sel === r.id) { endTerm(app, ui); ui.sel = ''; }
    ui.run(`${r.id}:${a.id}`, () => app.act(r.id, a.id), a.id === 'delete' ? `${r.name} deleted` : '');
  };
  const open = () => { if (ui.sel !== r.id) { endTerm(app, ui); ui.sel = r.id; ui.sub = ui.sub || 'files'; app.closeFiles(); } ui.paint(); };
  return html`<div class="card sbx ${ui.sel === r.id ? 'on' : ''}" data-id=${r.id}>
    <div class="hd"><b class="link" @click=${open}>${r.name}</b>
      <span class="pill ${r.tone}" title=${r.stateDetail}>${r.stateLabel}</span>
      <span class="pill">${r.visibility === 'team' ? 'team' : 'private'}</span>
      <span class="grow"></span>
      ${r.actions.map((a) => html`<button class="small ${a.danger ? 'rm' : ''}" data-act=${a.id} ?disabled=${!!ui.busy} @click=${() => act(a)}>${a.label}</button>`)}
      <button class="small" data-act="open" @click=${open}>Open</button></div>
    ${r.stateDetail ? html`<div class="muted small detail">${r.stateDetail}</div>` : nothing}
    <div class="muted small">${[r.image, r.sizeText, r.egressText, r.isolation, `owner ${r.owner}`, r.lastText && `active ${r.lastText}`].filter(Boolean).join(' · ')}</div>
  </div>`;
}

function detailTpl(app, ui, r) {
  const sub = ui.sub || 'files';
  const tab = (id, label, ok = true) => html`<button class="tab ${sub === id ? 'on' : ''}" id=${'sub-' + id} ?disabled=${!ok}
    @click=${() => { ui.sub = id; ui.paint(); }}>${label}</button>`;
  return html`<div class="detail-pane" id="detail">
    <div class="hd"><b>${r.name}</b> <span class="mono muted small">${r.id}</span> <span class="grow"></span>
      <nav class="tabs">${tab('files', 'Files', r.canFiles)}${tab('term', 'Terminal', r.canTerminal)}${tab('share', 'Sharing', r.canShare)}</nav>
      <button class="ghost" id="detail-close" title="Close" @click=${() => { endTerm(app, ui); ui.sel = ''; app.closeFiles(); ui.paint(); }}>✕</button></div>
    ${sub === 'files' ? filesTpl(app, ui, r) : sub === 'term' ? termTpl(app, ui, r) : shareTpl(app, ui, r)}
  </div>`;
}

// --- files -----------------------------------------------------------------------

function filesTpl(app, ui, r) {
  if (!r.canFiles) return html`<div class="muted">No files: ${r.state !== 'running' && r.state !== 'stopped' ? `the sandbox is ${r.stateLabel}` : 'the substrate serves none yet'}.</div>`;
  const fs = app.files && app.files.id === r.id ? app.files : null;
  if (!fs) {
    if (ui.browsing !== r.id) { ui.browsing = r.id; queueMicrotask(() => app.browse(r.id, r.workdir)); } // not while painting
    return html`<div class="muted">reading ${r.workdir}…</div>`;
  }
  ui.browsing = '';
  const go = (p) => { ui.forms.path = undefined; app.browse(r.id, p); };
  const typed = ui.forms.path ?? fs.path;
  const upload = (e) => {
    const files = [...e.target.files];
    e.target.value = '';
    ui.run('upload', async () => { for (const f of files) await app.upload(r.id, fs.path, f.name, f); }, `${files.length} file${files.length === 1 ? '' : 's'} uploaded`);
  };
  const download = (path) => ui.run('download', async () => { globalThis.xbin.download(F.baseName(path), await app.fetchFile(r.id, path)); });
  return html`<div class="files" id="files">
    <div class="row crumbs" id="crumbs"><span class="mono">${F.crumbs(fs.path).map((c, i) => html`${i > 1 ? '/' : ''}<span class="link" @click=${() => go(c.path)}>${c.name}</span>`)}</span>
      <span class="grow"></span>
      <input id="path" class="mono" .value=${typed} @input=${(e) => { ui.forms.path = e.target.value; }}
        @keydown=${(e) => { if (e.key === 'Enter') { ui.forms.path = undefined; go(e.target.value); } }}>
      <button class="small" id="up" @click=${() => go(F.parentPath(fs.path))}>Up</button>
      <label class="button small" id="upload-label">Upload<input type="file" id="upload" multiple hidden @change=${upload}></label>
      <button class="small" id="mkdir" @click=${() => {
        const name = prompt('A new directory in ' + fs.path);
        if (name) ui.run('mkdir', () => app.mkdir(r.id, F.joinPath(fs.path, name)));
      }}>New folder</button></div>
    ${fs.err ? html`<div class="err" id="files-err">${fs.err}</div>` : nothing}
    ${fs.busy && !fs.listing ? html`<div class="muted">reading…</div>` : nothing}
    ${fs.listing ? html`<table class="grid small" id="entries"><tbody>
      ${M.fileRows(fs.listing).map((e) => html`<tr data-name=${e.name}>
        <td><span class="link ${e.dir ? 'dir' : ''}" @click=${() => (e.dir ? go(e.path) : app.readFile(r.id, e.path))}>${e.dir ? '📁' : e.type === 'symlink' ? '🔗' : '📄'} ${e.name}</span></td>
        <td class="num">${e.detail}</td><td class="muted">${e.when}</td>
        <td class="acts">${e.dir ? nothing : html`<button class="small" data-act="download" @click=${() => download(e.path)}>Download</button>`}
          <button class="small rm" data-act="remove" @click=${() => ask(`Remove ${e.path}${e.dir ? ' and everything in it' : ''}?`) &&
            ui.run('remove', () => app.remove(r.id, e.path, e.dir))}>Remove</button></td></tr>`)}
      </tbody></table>${fs.listing.truncated ? html`<div class="muted small">(the first entries only)</div>` : nothing}
      ${!fs.listing.entries || !fs.listing.entries.length ? html`<div class="muted">empty</div>` : nothing}` : nothing}
    ${fs.file ? fileTpl(app, ui, r, fs.file, download) : nothing}
  </div>`;
}

function fileTpl(app, ui, r, f, download) {
  return html`<div class="viewer" id="viewer"><div class="hd"><b class="mono">${f.path}</b>
      ${f.size != null ? html`<span class="muted small">${F.bytes(f.size)}</span>` : nothing}<span class="grow"></span>
      <button class="small" @click=${() => download(f.path)}>Download</button>
      <button class="ghost" id="viewer-close" @click=${() => app.closeFile()}>✕</button></div>
    ${f.busy ? html`<div class="muted">reading…</div>` : f.err ? html`<div class="err">${f.err}</div>`
      : f.binary ? html`<div class="muted" id="binary">A binary file — download it.</div>`
      : html`<pre id="content">${f.text}</pre>${f.truncated ? html`<div class="muted small">(the first ${F.bytes(M.VIEW_MAX)}; download it for the rest)</div>` : nothing}`}
  </div>`;
}

// --- the terminal ----------------------------------------------------------------

function termTpl(app, ui, r) {
  if (!r.canTerminal) return html`<div class="muted">No terminal: ${r.state === 'error' ? 'the sandbox is in error' : 'the substrate offers none (tty)'}.</div>`;
  if (!ui.term || ui.term.id !== r.id) {
    ui.term = { id: r.id, key: (ui.termKey = (ui.termKey || 0) + 1), src: app.terminalSrc(r.id, r.workdir), session: '', ended: false };
    import('/vendor/bx-terminal.js').then(() => ui.paint(), (e) => { ui.err = 'no terminal: ' + e.message; ui.paint(); });
  }
  const t = ui.term;
  return html`<div class="term" id="term" @keydown=${(e) => { if (e.key === 'Escape') e.stopPropagation(); }}>
    <div class="row small"><span class="muted">a shell as ${r.user || 'the sandbox user'} in <span class="mono">${r.workdir}</span></span>
      ${t.ended ? html`<span class="pill">ended</span>` : nothing}<span class="grow"></span>
      ${t.ended ? html`<button class="small" id="term-again" @click=${() => { ui.term = null; ui.paint(); }}>New shell</button>` : nothing}
      <button class="small rm" id="term-end" ?disabled=${t.ended} title="End the shell" @click=${() => { endTerm(app, ui); ui.sub = 'files'; ui.paint(); }}>End</button></div>
    ${keyed(t.key, html`<bx-terminal src=${t.src} style="height:360px"
      @bx-session=${(e) => { t.session = e.detail.id; }} @bx-exit=${() => { t.ended = true; ui.paint(); }}></bx-terminal>`)}
  </div>`;
}

// endTerm ends the open terminal's shell (a terminal a page leaves runs on
// until the manager ends it).
function endTerm(app, ui) {
  const t = ui.term;
  ui.term = null;
  if (t && t.session && !t.ended) app.endShell(t.id, t.session).catch(() => {});
}

// --- who may use it ----------------------------------------------------------------

function shareTpl(app, ui, r) {
  return html`<div class="row" id="visibility"><span class="muted">who may use it here</span>
      <select id="vis-pick" ?disabled=${!!ui.busy} @change=${(e) => ui.run('vis', () => app.setVisibility(r.id, e.target.value))}>
        <option value="private" ?selected=${r.visibility !== 'team'}>you (and members)</option>
        <option value="team" ?selected=${r.visibility === 'team'}>anyone who may open this page</option></select></div>
    ${sharesTpl(ui, r.shares, (s) => app.setShares(r.id, s), `mine-${r.id}`)}
    <div class="muted small">A consumer tile you share it with (sandbox-terminal, an agent) lists it among its own; with named people, only they use it there.</div>`;
}
