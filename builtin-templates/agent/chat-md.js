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
