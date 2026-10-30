// settings-files.js — the settings panel's Files tab (split out of agent.js,
// which is at its size budget): this run's session files, the store the
// agent's file_* tools write. agent.js hands in what the tab needs of the page
// (ctx): the app, $, rawBlob, closeSettings, openPreview, closePreview,
// refreshView and the open pane (ctx.preview, read when the tab acts).
// Every file downloads from its row; several picked (the checkboxes, or all
// of them) download as one zip or delete with one confirmation (D207).
import { esc } from '/vendor/bx-kit.js';
import * as actions from './model/actions.js';
import { whoReads } from './model/docs.js';
import { zipName } from './model/report-links.js';

const { fmtBytes } = actions;
const num = (v) => Number(v) || 0;
const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');
const isHtmlPath = (p) => /\.html?$/i.test(p || '');

let filesCache = []; // session files for the files tab (lookup by index)
let filesSel = null; // path of the file being edited (null = new)
const picked = new Set(); // the files ticked for a bulk download or delete (paths)
let pickedRun = null;     // …of this run
const baseName = (p) => String(p).split('/').pop();
// selectFile(path): the file the tab opens on next.
export const selectFile = (path) => { filesSel = path; };

// Files tab: this run's session files — the same store the agent's file_*
// tools write. Human-editable on purpose: fixing a typo and
// hitting Render is the fastest debug loop there is. Optimistic concurrency —
// we send back the version we loaded, so a write the agent made in between
// comes back as a visible 409 instead of silently losing one side.
export async function tabFiles(bd, ctx) {
  const { app, $, rawBlob, closeSettings, openPreview, refreshView } = ctx;
  if (app.sel == null) { bd.innerHTML = '<div class="empty">select a run to see its session files</div>'; return; }
  filesCache = await actions.files(app.sel);
  if (pickedRun !== app.sel) { picked.clear(); pickedRun = app.sel; }
  for (const p of [...picked]) if (!filesCache.some((f) => f.path === p)) picked.delete(p); // gone since
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
      <div class="hint">${esc(cur.mime || 'binary')} · ${fmtBytes(num(cur.bytes))} — the agent ${whoReads(cur.path, cur.mime, isImg(cur))}.</div>
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
      ${filesCache.length ? `<div class="fbulk"><span class="muted" id="fl-n"></span>
        <button class="btn ghost btnsm" id="fl-zip" title="Download the selected files as one zip"><bx-icon name="download"></bx-icon>Download (zip)</button>
        <button class="btn rm btnsm" id="fl-bdel" title="Delete the selected files">Delete</button>
        <span class="err" id="fl-berr"></span></div>` : ''}
      <div class="tblwrap"><table class="tbl"><tr><th class="fpick"><input type="checkbox" id="fl-all" title="Select all" aria-label="Select all files" ${filesCache.length ? '' : 'disabled'}></th><th>path</th><th>type</th><th>bytes</th><th>v</th><th></th></tr>
      ${filesCache.length ? filesCache.map((f, i) => `<tr>
        <td class="fpick"><input type="checkbox" data-fp="${i}" aria-label="Select ${esc(f.path)}" ${picked.has(f.path) ? 'checked' : ''}></td>
        <td class="mono">${esc(f.path)}</td>
        <td class="muted">${esc(f.binary ? f.mime : (f.mime || 'text'))}</td>
        <td class="muted">${fmtN(f.bytes)}</td>
        <td class="muted">${num(f.version)}</td>
        <td style="text-align:right; white-space:nowrap">
          ${isHtmlPath(f.path) ? `<button class="btn ghost btnsm" data-fr="${i}" title="show in the render pane">Render</button> ` : ''}
          <button class="btn ghost btnsm icon" data-fdl="${i}" title="Download ${esc(baseName(f.path))}" aria-label="Download ${esc(f.path)}"><bx-icon name="download"></bx-icon></button>
          <button class="btn ghost btnsm" data-fe="${i}">${f.binary ? 'View' : 'Edit'}</button>
          <button class="btn rm btnsm" data-fd="${i}">Del</button></td></tr>`).join('')
        : '<tr><td colspan="6" class="muted">no files yet — the agent writes these with its file tools; attach your own with the paperclip below the chat</td></tr>'}
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

  wireBulk(bd, ctx);
  bd.querySelectorAll('[data-fdl]').forEach((b) => b.onclick = async () => {
    const f = filesCache[+b.dataset.fdl];
    try { xbin.download(baseName(f.path), await rawBlob(app.sel, f.path)); } catch (e) { alert(e.message); }
  });
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
    gone(ctx, [f.path]);
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

// gone: files just deleted — the editor lets go of one, and the preview pane
// closes when it showed one of them (that run's).
function gone(ctx, paths) {
  if (paths.includes(filesSel)) filesSel = null;
  for (const p of paths) picked.delete(p);
  const pv = ctx.preview;
  if (pv && pv.kind !== 'live' && pv.run === ctx.app.sel && paths.includes(pv.path)) ctx.closePreview();
}

// wireBulk: the checkboxes, select-all, and what the picked files share —
// one zip (every file: the backend's all, a short URL), one Delete.
function wireBulk(bd, ctx) {
  const { app, $, refreshView } = ctx;
  const sync = () => {
    const n = picked.size, all = $('fl-all');
    if (all) { all.checked = n > 0 && n === filesCache.length; all.indeterminate = n > 0 && n < filesCache.length; }
    if (!$('fl-n')) return;
    $('fl-n').textContent = n ? `${n} of ${filesCache.length} selected` : 'select files to download or delete several at once';
    $('fl-zip').disabled = $('fl-bdel').disabled = !n;
  };
  bd.querySelectorAll('[data-fp]').forEach((c) => c.onchange = () => {
    const p = filesCache[+c.dataset.fp].path;
    if (c.checked) picked.add(p); else picked.delete(p);
    sync();
  });
  if ($('fl-all')) $('fl-all').onchange = () => {
    const on = $('fl-all').checked;
    picked.clear();
    if (on) for (const f of filesCache) picked.add(f.path);
    bd.querySelectorAll('[data-fp]').forEach((c) => { c.checked = on; });
    sync();
  };
  sync();
  if (!$('fl-zip')) return;
  const err = (m) => { $('fl-berr').textContent = m || ''; };
  $('fl-zip').onclick = async () => {
    err('');
    const run = app.sel, all = picked.size === filesCache.length;
    $('fl-zip').disabled = true;
    try { xbin.download(zipName(run), await actions.zipFiles(`/api/${xbin.self}`, run, all ? [] : [...picked])); }
    catch (e) { err(e.message); }
    finally { if ($('fl-zip')) $('fl-zip').disabled = !picked.size; }
  };
  $('fl-bdel').onclick = async () => {
    const paths = filesCache.map((f) => f.path).filter((p) => picked.has(p));
    if (!paths.length) return;
    const list = paths.slice(0, 12).join('\n') + (paths.length > 12 ? `\n… and ${paths.length - 12} more` : '');
    if (!confirm(`Delete ${paths.length} file${paths.length > 1 ? 's' : ''}?\n\n${list}`)) return;
    const r = await actions.deleteFiles(app.sel, paths);
    gone(ctx, r.deleted);
    await tabFiles(bd, ctx); refreshView();
    if (r.failed.length) err(`${r.failed.length} not deleted: ${r.failed.map((f) => `${f.path} (${f.error})`).join(', ')}`);
  };
}
