// chat-md.js — model text as markdown, sanitized.
//
// Raw HTML tokens are shown escaped (model output is untrusted — an injected
// <script>/<img> must never execute with this tile's frame token), links get
// safe schemes and a new tab, and images render as their source text RATHER
// THAN LOADING. That last one is load-bearing: the platform CSP on /c/
// documents has no img-src, so a model-authored <img src="https://…/?leak=…">
// would be a live exfiltration beacon. Streaming-tolerant: a parse error falls
// back to escaped text.
import { marked } from '/vendor/marked.esm.js';
import { esc } from '/vendor/bx-kit.js';

marked.use({
  breaks: true,
  renderer: {
    html({ text }) { return esc(text); },
    image({ text, href }) { return `<span class="muted">[image: ${esc(text || href || '')}]</span>`; },
    link({ href, title, tokens }) {
      const h = String(href || '').trim();
      const inner = this.parser.parseInline(tokens);
      if (/^(javascript|data|vbscript):/i.test(h)) return inner;
      return `<a href="${esc(h)}" target="_blank" rel="noopener noreferrer"${title ? ` title="${esc(title)}"` : ''}>${inner}</a>`;
    },
  },
});

export const md = (s) => { try { return marked.parse(String(s ?? '')); } catch { return esc(s); } };

// mdInto(el, s): markdown rendered into el a top-level block at a time, for
// text that streams (D130). The text is lexed whole, but only the blocks
// whose source changed are parsed and swapped — the paragraph being written
// — so the ones before it keep their DOM and a selection in them survives
// the next token. Each block sits in a `<div class="md-b">` (display:
// contents); its HTML is what md() makes of it within the whole text.
export function mdInto(el, s) {
  const text = String(s ?? '');
  if (el.$mdText === text) return;
  el.$mdText = text;
  let toks;
  try { toks = marked.lexer(text); } catch { el.replaceChildren(document.createTextNode(text)); el.$mdRaw = null; return; }
  const links = toks.links;
  toks = toks.filter((t) => t.type !== 'space');
  const raws = el.$mdRaw || (el.replaceChildren(), []);
  const kids = el.children;
  toks.forEach((t, i) => {
    if (raws[i] === t.raw && kids[i]) return;
    let h;
    try { const one = [t]; one.links = links; h = marked.parser(one); } catch { h = esc(t.raw); }
    const b = kids[i] || el.appendChild(document.createElement('div'));
    b.className = 'md-b';
    b.innerHTML = h;
  });
  while (kids.length > toks.length) el.lastElementChild.remove();
  el.$mdRaw = toks.map((t) => t.raw);
}
