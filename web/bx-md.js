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
