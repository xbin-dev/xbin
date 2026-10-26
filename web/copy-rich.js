// copy-rich.js — rich copy for tiles (D199): a tile's text with its HTML
// written to the clipboard by the page that embeds it. A tile is a
// sandboxed, opaque-origin frame with no clipboard of its own; it hands the
// copy up (xbin.copy → xbin:copy; or a right-click's selection, which rides
// xbin:contextmenu) and the embedder writes it here.
//
// The tile is untrusted, so its HTML is rebuilt from an allowlist before it
// reaches the clipboard (and from there any editor it is pasted into):
// paragraphs and line breaks, strong/em (b/i read as them), code and pre,
// lists (an ol's start kept), tables (thead, tbody, tr, th, td, with
// colspan/rowspan), headings, blockquotes, and links to http(s) and mailto
// only. Everything else is unwrapped — its text kept — except what has no
// text worth keeping (script, style, a control, an image, an embed), which
// goes whole. No attribute but those survives: no class, no style, no event
// handler. Parsing is DOMParser's: an inert document, nothing loads or runs.
//
//   sanitizeCopyHTML(html) → string   the allowlisted HTML ('' when none)
//   writeRich({text, html}) → Promise<boolean>
//       the async clipboard (text/plain + text/html), else execCommand('copy')
//       on a scratch selection whose copy event sets both — the shape an
//       http LAN deployment (no navigator.clipboard) or an unfocused document
//       needs. Call it inside the user's activation (a click, a menu item).

export const COPY_MAX = 262144; // 256 KiB of HTML: any prose selection or message

const KEEP = new Set(['p', 'br', 'strong', 'em', 'code', 'pre', 'ul', 'ol', 'li', 'table', 'thead', 'tbody', 'tr', 'th', 'td',
  'a', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'blockquote', 'hr']);
const ALIAS = { b: 'strong', i: 'em' };
const DROP = new Set(['script', 'style', 'template', 'iframe', 'object', 'embed', 'svg', 'math', 'img', 'picture', 'video', 'audio',
  'canvas', 'button', 'input', 'textarea', 'select', 'option', 'noscript', 'head', 'title', 'meta', 'link', 'bx-icon']);
const safeHref = (h) => /^(https?:|mailto:)/i.test(String(h || '').trim());
const count = (v) => (/^\d{1,4}$/.test(String(v || '')) ? String(Number(v)) : '');

export function sanitizeCopyHTML(html) {
  const src = String(html || '').slice(0, COPY_MAX);
  if (!src.trim() || typeof DOMParser === 'undefined') return '';
  const doc = new DOMParser().parseFromString(src, 'text/html');
  const out = document.implementation.createHTMLDocument('');
  const walk = (from, to) => {
    for (const n of [...from.childNodes]) {
      if (n.nodeType === 3) { to.appendChild(out.createTextNode(n.nodeValue)); continue; }
      if (n.nodeType !== 1) continue;
      const tag = n.localName;
      if (DROP.has(tag)) continue;
      const as = ALIAS[tag] || tag;
      if (!KEEP.has(as)) { walk(n, to); continue; } // unwrapped: its text stays
      const el = out.createElement(as);
      if (as === 'a') {
        const h = n.getAttribute('href');
        if (!safeHref(h)) { walk(n, to); continue; } // a link to anywhere else: its words only
        el.setAttribute('href', h.trim());
      }
      if (as === 'ol' && count(n.getAttribute('start'))) el.setAttribute('start', count(n.getAttribute('start')));
      if (as === 'th' || as === 'td') {
        for (const a of ['colspan', 'rowspan']) if (count(n.getAttribute(a))) el.setAttribute(a, count(n.getAttribute(a)));
      }
      walk(n, el);
      to.appendChild(el);
    }
  };
  const box = out.createElement('div');
  walk(doc.body, box);
  return box.innerHTML.trim() ? box.innerHTML.slice(0, COPY_MAX) : '';
}

export async function writeRich({ text = '', html = '' } = {}) {
  const t = String(text || '');
  const h = sanitizeCopyHTML(html);
  if (!t && !h) return false;
  try {
    if (h && navigator.clipboard?.write && typeof ClipboardItem !== 'undefined') {
      await navigator.clipboard.write([new ClipboardItem({
        'text/plain': new Blob([t], { type: 'text/plain' }), 'text/html': new Blob([h], { type: 'text/html' }) })]);
      return true;
    }
    if (!h && navigator.clipboard?.writeText) { await navigator.clipboard.writeText(t); return true; }
  } catch { /* refused (unfocused, no permission): the legacy way below */ }
  return legacyCopy(t, h);
}

// frameCopy(frame, d): a <bx-frame>'s end of xbin.copy({text, html}) —
// d is the tile's xbin:copy message, already checked to come from the
// frame's own iframe. Writes the clipboard within the click's activation,
// answers the tile (xbin:reply, true when it took) and fires `bx-copied`
// {from, ok} so the page can say so.
export function frameCopy(frame, d) {
  const text = typeof d.text === 'string' ? d.text.slice(0, COPY_MAX) : '';
  const html = typeof d.html === 'string' ? d.html.slice(0, COPY_MAX) : '';
  const reply = (result) => frame._iframe?.contentWindow?.postMessage({ type: 'xbin:reply', id: d.id, result }, frame._frame?.origin || '*');
  writeRich({ text, html }).then((ok) => {
    reply(ok);
    frame.dispatchEvent(new CustomEvent('bx-copied', { bubbles: true, composed: true, detail: { from: frame.src, ok } }));
  }, () => reply(false));
}

// legacyCopy: execCommand('copy') with a copy listener that sets both
// flavours itself — a scratch element is selected so the command has
// something to copy, the person's own selection and focus come back after.
function legacyCopy(text, html) {
  const prev = document.activeElement;
  const sel = document.getSelection();
  const ranges = sel ? [...Array(sel.rangeCount).keys()].map((i) => sel.getRangeAt(i)) : [];
  const ta = Object.assign(document.createElement('textarea'), { value: text || ' ', readOnly: true });
  ta.setAttribute('aria-hidden', 'true');
  ta.style.cssText = 'position:fixed;top:0;left:0;width:1px;height:1px;opacity:0;pointer-events:none';
  const onCopy = (e) => {
    e.clipboardData.setData('text/plain', text);
    if (html) e.clipboardData.setData('text/html', html);
    e.preventDefault();
  };
  document.body.appendChild(ta);
  document.addEventListener('copy', onCopy, true);
  let ok = false;
  try {
    ta.focus({ preventScroll: true }); ta.select();
    ok = document.execCommand('copy');
  } catch { /* no command */ }
  document.removeEventListener('copy', onCopy, true);
  ta.remove();
  try {
    if (sel && ranges.length) { sel.removeAllRanges(); for (const r of ranges) sel.addRange(r); }
    if (prev?.isConnected) prev.focus({ preventScroll: true });
  } catch { /* fine */ }
  return ok;
}
