// native/render-doc.js — the document a render preview shows (render_html
// output) in the native view: a `canvas html=` island, which the app loads
// with scripts off. Scripts off does not stop subresource LOADS: a bare
// <img src="https://…/?leak=…"> in the model's file would still fire a real
// request from the person's phone, which in a private-lane run is
// exfiltration. So, exactly like the web's frameDoc() (agent.js — the
// policy test/frame-policy.mjs holds in a real browser, and the same test
// holds this one to), the model's HTML is parsed with DOMParser (a document
// with no browsing context: nothing loads or runs), meta refreshes and
// off-page links are removed, what the policy will refuse is counted, and a
// CSP that loads nothing external is put first in <head>.
//
// The runtime is a real WebKit document, so DOMParser is there. Without one
// (node tests) the fallback puts the same CSP first in the byte stream and
// neutralises meta refreshes textually — never looser than the parse.
//
// Links to the run's own files (#46, D208): the render step names them
// (detail.links: [{href, path}], href as the report wrote it). Those keep
// their href — the app never follows one: an app with canvas rev 2 hands
// the tap to the view (`@link`), which shares the file — and every other
// off-page href is removed, as the web's frameDoc does. zoom (#49): for an
// app without canvas rev 2's `zoom`, the page is scaled by CSS instead
// (it reloads; rev 2 zooms in place).

export const FRAME_CSP = "default-src 'none'; style-src 'unsafe-inline'; img-src data:; " +
                         "font-src data:; form-action 'none'; base-uri 'none'";
// theme-ok: the model's HTML assumes a white page with the browser's own type (D184)
const FRAME_CSS = 'html{background:#fff;color:#111;color-scheme:light}' +
                  // theme-ok: the model's HTML assumes a white page with the browser's own type
                  'body{margin:12px;font:14px/1.5 system-ui,-apple-system,sans-serif}' +
                  'img,svg,video,canvas,table,pre{max-width:100%}' +
                  'pre{overflow-x:auto}table{border-collapse:collapse}';

const EXTERNAL = (u) => u && !/^(data:|#)/i.test(u);

// renderDoc(src, {links, zoom}) → {html, blocked}: what the island shows,
// and how many external resources the policy blocks (said to the person).
// links: the hrefs that stay ([{href}]); zoom: a CSS zoom (1: none).
export function renderDoc(src, { links = [], zoom = 1 } = {}) {
  const P = globalThis.DOMParser;
  const keep = new Set((links || []).map((l) => l && l.href).filter((h) => typeof h === 'string' && h));
  const z = Number(zoom) > 0 && Number(zoom) !== 1 ? Math.min(3, Math.max(0.5, Number(zoom))) : 1;
  return typeof P === 'function' ? parsed(new P(), src, keep, z) : textual(src, z, keep);
}

const zoomCss = (z) => (z !== 1 ? `html{zoom:${z}}` : '');

function parsed(parser, src, keep = new Set(), z = 1) {
  const doc = parser.parseFromString(String(src ?? ''), 'text/html');
  let blocked = 0;
  doc.querySelectorAll('meta[http-equiv]').forEach((m) => {
    if (/^\s*refresh\s*$/i.test(m.getAttribute('http-equiv') || '')) { m.remove(); blocked++; }
  });
  doc.querySelectorAll('[target]').forEach((e) => e.removeAttribute('target'));
  doc.querySelectorAll('a[href]').forEach((a) => {
    const h = a.getAttribute('href') || '';
    if (!h.startsWith('#') && !keep.has(h)) a.removeAttribute('href');
  });
  doc.querySelectorAll('img[src], source[src], link[href], use[href], iframe[src], object[data]').forEach((el) => {
    if (EXTERNAL(el.getAttribute('src') || el.getAttribute('href') || el.getAttribute('data') || '')) blocked++;
  });
  const mk = (tag, attrs, text) => {
    const e = doc.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
    if (text) e.textContent = text;
    return e;
  };
  // The CSP meta must be the first thing in <head> in the serialized bytes.
  doc.head.prepend(
    mk('meta', { 'http-equiv': 'Content-Security-Policy', content: FRAME_CSP }),
    mk('meta', { charset: 'utf-8' }),
    mk('meta', { name: 'viewport', content: 'width=device-width,initial-scale=1' }),
    mk('base', { target: '_blank' }),
    mk('style', {}, FRAME_CSS + zoomCss(z)),
  );
  return { html: '<!doctype html>' + doc.documentElement.outerHTML, blocked };
}

function textual(src, z = 1, keep = new Set()) {
  let s = String(src ?? '');
  let blocked = 0;
  // a meta refresh navigates the island: make its http-equiv say nothing
  s = s.replace(/(<meta\b[^>]*?http-equiv\s*=\s*["']?)\s*refresh\b/gi, (all, head) => { blocked++; return head + 'x-refresh-removed'; });
  for (const m of s.matchAll(/<(?:img|source|link|use|iframe|object)\b[^>]*?\s(?:src|href|data)\s*=\s*["']?([^"'\s>]+)/gi)) if (EXTERNAL(m[1])) blocked++;
  // a link keeps its href only when it is in-page or one of the run's files
  s = s.replace(/(<a\b[^>]*?\s)href(\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+)))/gi, (all, head, rest, dq, sq, bare) => {
    const h = dq ?? sq ?? bare ?? '';
    return h.startsWith('#') || keep.has(h) ? all : head;
  });
  const head = `<meta http-equiv="Content-Security-Policy" content="${FRAME_CSP}"><meta charset="utf-8">` +
    `<meta name="viewport" content="width=device-width,initial-scale=1"><base target="_blank"><style>${FRAME_CSS}${zoomCss(z)}</style>`;
  return { html: '<!doctype html><html><head>' + head + '</head><body>' + s.replace(/<!doctype[^>]*>/i, '') + '</body></html>', blocked };
}
