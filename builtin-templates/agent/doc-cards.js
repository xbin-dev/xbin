// doc-cards.js — a document the agent wrote (D201: sheet_write, doc_write,
// slides_write) as its line in the chat: the render step the tool journals
// names the file it made (detail.doc) beside its preview (detail.path), so
// the line opens the preview and downloads the .xlsx/.docx/.pptx itself.
// chat-cards.js stepTpl hands a render step that names a document here.
import { html } from '/vendor/lit-all.min.js';

const icon = (name) => html`<bx-icon name=${name}></bx-icon>`;

const KIND = { xlsx: ['grid', 'workbook'], docx: ['doc', 'document'], pptx: ['window', 'slides'] };

// docKind: what the file is, by its extension ({glyph, word}).
export function docKind(path) {
  const ext = String(path || '').split('.').pop().toLowerCase();
  const [glyph, word] = KIND[ext] || ['file', 'file'];
  return { glyph, word };
}

// docStepTpl(b, act): the line — the document's name and version, the
// preview, the download.
export function docStepTpl(b, act) {
  const d = b.detail || {};
  const k = docKind(d.doc);
  const base = String(d.doc).split('/').pop();
  return html`<div class="step render doc" data-k=${b.id}><span class="g">${icon(k.glyph)}</span>
    <button class="lnk steplnk" title="show its preview" @click=${() => act.openPreview?.(d.path, d.version, b.run)}>${k.word} ${d.doc} v${d.docVersion || ''}</button>
    · <button class="lnk steplnk" title=${`download ${base}`} @click=${() => act.downloadFile?.(b.run, d.doc)}>download</button></div>`;
}
