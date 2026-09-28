/**
 * bx-md.js — a hardened markdown-to-HTML renderer for UNTRUSTED text (a
 * coding agent's output in the terminal window's Agent tab). Same policy as
 * the agent template's renderer: raw HTML tokens are escaped (model output
 * must never inject live markup with chrome's privileges), images render as
 * their source text rather than loading (an `<img src>` would be a live
 * exfiltration beacon — no subresource is ever fetched from model text),
 * and links are limited to safe schemes and open in a new tab. Everything
 * else is HTML this renderer produced from markdown structure. A parse
 * error falls back to escaped text, so streaming half-parsed input is safe.
 */
import { marked } from '/vendor/marked.esm.js';
import { esc } from '/vendor/bx-kit.js';

marked.use({
  breaks: true,
  renderer: {
    html({ text }) { return esc(text); },
    image({ text, href }) { return `<span class="md-img">[image: ${esc(text || href || '')}]</span>`; },
    link({ href, title, tokens }) {
      const h = String(href || '').trim();
      const inner = this.parser.parseInline(tokens);
      if (/^(javascript|data|vbscript):/i.test(h)) return inner;
      return `<a href="${esc(h)}" target="_blank" rel="noopener noreferrer"${title ? ` title="${esc(title)}"` : ''}>${inner}</a>`;
    },
  },
});

/** md(s): render markdown to sanitized HTML (escaped text on any error). */
export const md = (s) => { try { return marked.parse(String(s ?? '')); } catch { return esc(s); } };

/**
 * mdInto(el, s, memo): render markdown into el a top-level block at a time
 * (D130): the text is lexed whole, but only blocks whose source changed are
 * rendered and swapped — a streaming message re-parses its last paragraph,
 * and the blocks before it keep their DOM, so a selection in them survives
 * the next token. Each block sits in a `<div class="md-b">` (display:
 * contents). `memo` (an object kept with the text's owner) remembers each
 * block's HTML, so a row that leaves the window and comes back renders
 * without parsing. Per-block HTML equals md()'s for the whole text.
 */
export function mdInto(el, s, memo) {
  const text = String(s ?? '');
  if (el.$mdText === text) return;
  el.$mdText = text;
  let toks;
  try { toks = marked.lexer(text); } catch { el.replaceChildren(document.createTextNode(text)); el.$mdRaw = null; return; }
  const links = toks.links;
  toks = toks.filter((t) => t.type !== 'space');
  const prev = (memo && memo.parts) || [];
  const parts = toks.map((t, i) => {
    if (prev[i] && prev[i].raw === t.raw) return prev[i];
    let h;
    try { const one = [t]; one.links = links; h = marked.parser(one); } catch { h = esc(t.raw); }
    return { raw: t.raw, html: h };
  });
  if (memo) memo.parts = parts;
  const raws = el.$mdRaw || (el.replaceChildren(), []);
  const kids = el.children;
  parts.forEach((p, i) => {
    if (raws[i] === p.raw && kids[i]) return;
    const b = kids[i] || el.appendChild(document.createElement('div'));
    b.className = 'md-b';
    b.innerHTML = p.html;
  });
  while (kids.length > parts.length) el.lastElementChild.remove();
  el.$mdRaw = parts.map((p) => p.raw);
}
