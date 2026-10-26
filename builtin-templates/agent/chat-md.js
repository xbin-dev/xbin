// chat-md.js — model text as markdown, sanitized.
//
// Raw HTML tokens are shown escaped (model output is untrusted — an injected
// <script>/<img> must never execute with this tile's frame token), links get
// safe schemes and a new tab, and images render as their source text RATHER
// THAN LOADING. That last one is load-bearing: the platform CSP on /c/
// documents has no img-src, so a model-authored <img src="https://…/?leak=…">
// would be a live exfiltration beacon. Streaming-tolerant: a parse error falls
// back to escaped text.
//
// Fenced code with a language is highlighted (#26) by xbind's
// /vendor/highlight.min.js — the hljs build bx-code.js themes — imported on
// the first such fence and drawn with its classes on the theme's --bx-syn-*
// tokens (index.html). Without it (an older xbind) code stays plain, as it
// was. Only a CLOSED fence is highlighted: the one being streamed stays plain
// until its closing fence arrives (mdInto parses that block again then), so a
// growing block isn't re-coloured token by token; past HL_MAX it stays plain.
// What hljs returns is its own escaped spans, so the escaping above holds.
// When the highlighter arrives, mdEpoch moves on: HTML parsed before it
// (chat-cards.js memos, mdInto's blocks) is parsed again at the next paint,
// which onHighlighter asks for.
import { marked } from '/vendor/marked.esm.js';
import { esc } from '/vendor/bx-kit.js';

const HL_MAX = 20000;
let hljs = null; // the highlighter once imported; false: not there
let hlAsked = false;
const hlWaiters = new Set();
export let mdEpoch = 0;
export const onHighlighter = (fn) => { hlWaiters.add(fn); };
function highlighter() {
  if (!hlAsked) {
    hlAsked = true;
    import('/vendor/highlight.min.js').then((m) => { hljs = m.default && typeof m.default.highlight === 'function' ? m.default : false; })
      .catch(() => { hljs = false; })
      .finally(() => {
        if (!hljs) return;
        mdEpoch++;
        for (const f of hlWaiters) { try { f(); } catch { /* a waiter's own trouble */ } }
      });
  }
  return hljs || null;
}

// closedFence: a fenced code block's source ends with its closing fence.
export function closedFence(raw) {
  const lines = String(raw || '').replace(/\s+$/, '').split('\n');
  const open = lines[0].trim().match(/^(`{3,}|~{3,})/);
  const last = lines[lines.length - 1].trim();
  return lines.length >= 2 && !!open && last.length >= open[1].length && last === last[0].repeat(last.length) && last[0] === open[1][0];
}

marked.use({
  breaks: true,
  renderer: {
    html({ text }) { return esc(text); },
    code({ text, lang, raw }) {
      const l = String(lang || '').trim().split(/\s+/)[0].toLowerCase();
      const cls = l ? ` class="language-${esc(l)}"` : '';
      if (l && String(text).length <= HL_MAX && closedFence(raw)) {
        const h = highlighter();
        if (h && h.getLanguage(l)) {
          try { return `<pre><code class="hljs language-${esc(l)}">${h.highlight(String(text), { language: l, ignoreIllegals: true }).value}</code></pre>\n`; } catch { /* plain, below */ }
        }
      }
      return `<pre><code${cls}>${esc(text)}</code></pre>\n`;
    },
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
  if (el.$mdText === text && el.$mdEpoch === mdEpoch) return;
  if (el.$mdEpoch !== mdEpoch) { el.$mdEpoch = mdEpoch; el.$mdRaw = null; } // the highlighter arrived: every block again
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
