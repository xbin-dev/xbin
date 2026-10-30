// native/doc-card.js — a document the agent wrote (D201: sheet_write,
// doc_write, slides_write) as its card in the chat: the render step names
// the file (detail.doc) beside its preview (detail.path); the card opens
// the preview, and Share / export hands the .xlsx/.docx/.pptx to the app.
import { html, native } from '/vendor/xb-native.js';
import { raw, base } from './ui.js';
import { openRender } from './tools.js';

const KIND = { xlsx: ['grid', 'Workbook'], docx: ['doc', 'Document'], pptx: ['window', 'Slides'] };

export function docCardTpl(run, d) {
  const ext = String(d.doc || '').split('.').pop().toLowerCase();
  const [glyph, word] = KIND[ext] || ['file', 'File'];
  return html`<toolcard title=${`${word} ${base(d.doc)} v${d.docVersion || ''}`} icon=${glyph} family="render" state="ok"
      @open=${() => openRender(run, d.path, d.version, false, d.links)}/>
    <button icon="share" @tap=${() => native.share({ file: raw(run, d.doc) })}>${`Share / export ${base(d.doc)}`}</button>`;
}
