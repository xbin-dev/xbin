// settings-files.js — the settings panel's Files tab (split out of agent.js,
// which is at its size budget): this run's session files, the store the
// agent's file_* tools write. agent.js hands in what the tab needs of the page
// (ctx): the app, $, rawBlob, closeSettings, openPreview, closePreview,
// refreshView and the open pane (ctx.preview, read when the tab acts).
import { esc } from '/vendor/bx-kit.js';
import * as actions from './model/actions.js';

const { fmtBytes } = actions;
const num = (v) => Number(v) || 0;
const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');
const isHtmlPath = (p) => /\.html?$/i.test(p || '');

let filesCache = []; // session files for the files tab (lookup by index)
let filesSel = null; // path of the file being edited (null = new)
// selectFile(path): the file the tab opens on next.
export const selectFile = (path) => { filesSel = path; };

// Files tab: this run's session files — the same store the agent's file_*
// tools write. Human-editable on purpose: fixing a typo and
// hitting Render is the fastest debug loop there is. Optimistic concurrency —
// we send back the version we loaded, so a write the agent made in between
// comes back as a visible 409 instead of silently losing one side.
export async function tabFiles(bd, ctx) {
  const { app, $, rawBlob, closeSettings, openPreview, closePreview, refreshView } = ctx;
  if (app.sel == null) { bd.innerHTML = '<div class="empty">select a run to see its session files</div>'; return; }
  filesCache = await actions.files(app.sel);
  const cur = filesSel != null ? filesCache.find((f) => f.path === filesSel) : null;
  let body = '';
  if (cur && !cur.binary) {
    const full = await actions.file(app.sel, cur.path);
    body = full.content || '';
    cur.version = full.version;
  }
  // An attachment (binary) is never loaded into the textarea: it gets a
  // preview when it is an image, and a download either way.
  const isImg = (f) => f && f.binary && /^image\/(png|jpeg|gif|webp)$/.test(f.mime || '');
  const editor = cur && cur.binary ? `
    <div class="sec"><h4>Attachment · ${esc(cur.path)}
      <button class="btn ghost btnsm" id="fl-new">+ new</button></h4>
      ${isImg(cur) ? '<img id="fl-img" class="fprev" alt="">' : ''}
      <div class="hint">${esc(cur.mime || 'binary')} · ${fmtBytes(num(cur.bytes))} — the agent ${isImg(cur) ? 'sees it with file_view' : 'can list it but not read it as text'}.</div>
      <div style="margin-top:6px"><button class="btn ghost" id="fl-dl">Download</button> <span class="err" id="fl-err"></span></div>
    </div>` : `
    <div class="sec"><h4>${cur ? 'Edit · ' + esc(cur.path) : 'New file'}
      ${cur ? '<button class="btn ghost btnsm" id="fl-new">+ new</button>' : ''}</h4>
      <div class="field"><label>Path</label>
        <input id="fl-path" class="mono" value="${esc(cur ? cur.path : '')}" ${cur ? 'readonly' : ''} placeholder="report.html"></div>
      <div class="field"><label>Content</label>
        <textarea id="fl-body" class="mono" rows="14" spellcheck="false">${esc(body)}</textarea></div>
      <div><button class="btn" id="fl-save">Save</button>
        ${cur && isHtmlPath(cur.path) ? ' <button class="btn ghost" id="fl-render">Render</button>' : ''}
        <span class="err" id="fl-err"></span></div>
    </div>`;
  bd.innerHTML = `
    <div class="sec"><h4>Session files · run ${app.sel}</h4>
      <div class="tblwrap"><table class="tbl"><tr><th>path</th><th>type</th><th>bytes</th><th>v</th><th></th></tr>
      ${filesCache.length ? filesCache.map((f, i) => `<tr>
        <td class="mono">${esc(f.path)}</td>
        <td class="muted">${esc(f.binary ? f.mime : (f.mime || 'text'))}</td>
        <td class="muted">${fmtN(f.bytes)}</td>
        <td class="muted">${num(f.version)}</td>
        <td style="text-align:right; white-space:nowrap">
          ${isHtmlPath(f.path) ? `<button class="btn ghost btnsm" data-fr="${i}" title="show in the render pane">Render</button> ` : ''}
          <button class="btn ghost btnsm" data-fe="${i}">${f.binary ? 'View' : 'Edit'}</button>
          <button class="btn rm btnsm" data-fd="${i}">Del</button></td></tr>`).join('')
        : '<tr><td colspan="5" class="muted">no files yet — the agent writes these with its file tools; attach your own with the paperclip below the chat</td></tr>'}
      </table></div>
      <div class="hint">Text lives in this run's database; attachments in the tile's blob store. Deleting the run deletes both.</div>
    </div>${editor}`;

  if (cur && cur.binary) {
    const run = app.sel;
    if ($('fl-img')) rawBlob(run, cur.path).then((b) => {
      const img = $('fl-img');
      if (!img) return;
      img.src = URL.createObjectURL(b);
      img.onload = () => URL.revokeObjectURL(img.src);
    }).catch((e) => { if ($('fl-err')) $('fl-err').textContent = e.message; });
    $('fl-dl').onclick = async () => {
      try { xbin.download(cur.path.split('/').pop(), await rawBlob(run, cur.path)); }
      catch (e) { $('fl-err').textContent = e.message; }
    };
  }

  bd.querySelectorAll('[data-fe]').forEach((b) => b.onclick = () => {
    filesSel = filesCache[+b.dataset.fe].path; tabFiles(bd, ctx);
  });
  bd.querySelectorAll('[data-fr]').forEach((b) => b.onclick = () => {
    const f = filesCache[+b.dataset.fr];
    closeSettings(); openPreview(f.path, f.version, false);
  });
  bd.querySelectorAll('[data-fd]').forEach((b) => b.onclick = async () => {
    const f = filesCache[+b.dataset.fd];
    if (!confirm(`Delete "${f.path}"?`)) return;
    try { await actions.deleteFile(app.sel, f.path); }
    catch (e) { return alert(e.message); }
    if (filesSel === f.path) filesSel = null;
    if (ctx.preview && ctx.preview.path === f.path) closePreview();
    tabFiles(bd, ctx); refreshView();
  });
  if ($('fl-new')) $('fl-new').onclick = () => { filesSel = null; tabFiles(bd, ctx); };
  if ($('fl-render')) $('fl-render').onclick = () => { closeSettings(); openPreview(cur.path, cur.version, false); };
  if (!$('fl-save')) return;
  $('fl-save').onclick = async () => {
    const path = $('fl-path').value.trim();
    $('fl-err').textContent = '';
    if (!path) { $('fl-err').textContent = 'need a path'; return; }
    try {
      const r = await actions.saveFile(app.sel, { path, content: $('fl-body').value, version: cur ? cur.version : 0 });
      filesSel = path;
      // An open pane showing this file must repaint: bump it to the new version.
      if (ctx.preview && ctx.preview.path === path) openPreview(path, r.version, ctx.preview.live);
      tabFiles(bd, ctx); refreshView();
    } catch (e) { $('fl-err').textContent = e.message; }
  };
}
