// model/docs.js — the documents the agent's document tools read (D201:
// doc_read — PDF, Word, Excel, PowerPoint, CSV), for the words a file's
// screen says about it (web settings-files.js, native/tools.js).
const DOC = /\.(pdf|docx|docm|xlsx|xlsm|pptx|pptm|csv|tsv)$/i;
const DOC_MIME = /^(application\/pdf|text\/csv|application\/vnd\.openxmlformats-officedocument\.)/;

export const isDoc = (path, mime) => DOC.test(String(path || '')) || DOC_MIME.test(String(mime || ''));

// whoReads: what the agent does with an attachment, in the file screen's words.
export const whoReads = (path, mime, img) =>
  (img ? 'sees it with file_view' : isDoc(path, mime) ? 'reads it with doc_read' : 'can list it but not read it as text');
