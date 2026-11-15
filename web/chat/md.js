/**
 * chat/md.js — the one markdown-to-HTML renderer for UNTRUSTED text (D224):
 * a coding agent's output in the Agent tab, the agent template's chat, a
 * screen's text blocks. Raw HTML tokens are escaped (model output must never
 * inject live markup with chrome's privileges or a tile's frame token),
 * images render as their source text rather than loading (an `<img src>`
 * would be a live exfiltration beacon — no subresource is ever fetched from
 * model text), and links are limited to safe schemes and open in a new tab.
 * Everything else is HTML this renderer produced from markdown structure. A
 * parse error falls back to escaped text, so streaming half-parsed input is
 * safe.
 *
 * Its marked is its own instance: nothing here changes the shared `marked`
 * of /vendor/marked.esm.js, and a marked.use() elsewhere in the document
 * changes nothing here.
 *
 * Fenced code in a language highlight.js knows is coloured (#26): its
 * output is the code escaped, in class-named spans (mdCssText colours them
 * on the --bx-syn-* tokens). Only a CLOSED fence, at most MAX_HL — a block
 * still streaming in stays plain until its fence closes, so a token burst
 * never re-highlights it, and a pasted dump isn't worth the stall.
 *
 * The highlighter is optional. This module imports /vendor/highlight.min.js
 * on the first fence that could use it and draws code plain until it is
 * there (and for good when it never arrives). When it arrives, mdEpoch moves
 * on and onHighlighter's callbacks run: HTML made before it (a caller's
 * memo, mdInto's blocks) is of the old epoch and is made again at the next
 * paint. A document that wants colour from its first paint imports
 * /vendor/bx-md.js instead — the same renderer with the highlighter loaded
 * before anything is drawn.
 */
import { Marked } from '/vendor/marked.esm.js';
import { esc } from '/vendor/bx-kit.js';

const MAX_HL = 32 << 10;
const LANG = /^[\w+#.-]+$/; // a language that is a name; anything else is no class at all

let hljs = null; // the highlighter once it is here; false: asked for and not there
let hlAsked = false;
const hlWaiters = new Set();
/** mdEpoch: moves on when the highlighter arrives; HTML of an older epoch is stale. */
export let mdEpoch = 0;
/** onHighlighter(fn): fn runs when the highlighter arrives after HTML was made without it. */
export const onHighlighter = (fn) => { hlWaiters.add(fn); };
/**
 * useHighlighter(h): take a highlight.js that is already loaded (what
 * /vendor/bx-md.js does) instead of importing one on the first fence.
 */
export function useHighlighter(h) {
  if (!h || typeof h.highlight !== 'function' || typeof h.getLanguage !== 'function' || hljs === h) return;
  hljs = h;
  hlAsked = true;
  mdEpoch++;
  for (const f of hlWaiters) { try { f(); } catch { /* a waiter's own trouble */ } }
}
function highlighter() {
  if (!hlAsked) {
    hlAsked = true;
    import('/vendor/highlight.min.js').then((m) => { if (!hljs) useHighlighter(m.default); }, () => {})
      .finally(() => { if (!hljs) hljs = false; });
  }
  return hljs || null;
}

/**
 * closedFence(raw): a fenced code block's source ends with its closing
 * fence — the same character as the opening one, at least as many of them.
 */
export function closedFence(raw) {
  const lines = String(raw || '').replace(/\s+$/, '').split('\n');
  const open = lines[0].trim().match(/^(`{3,}|~{3,})/);
  const last = lines[lines.length - 1].trim();
  return lines.length >= 2 && !!open && last.length >= open[1].length && last === last[0].repeat(last.length) && last[0] === open[1][0];
}

const marked = new Marked({
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
    code({ text, lang, raw }) {
      const code = String(text ?? '');
      const lg = String(lang || '').trim().split(/\s+/)[0].toLowerCase();
      const named = !!lg && LANG.test(lg);
      if (named && code.length <= MAX_HL && closedFence(raw)) {
        const h = highlighter();
        if (h && h.getLanguage(lg)) {
          try { return `<pre><code class="hljs language-${lg}">${h.highlight(code, { language: lg, ignoreIllegals: true }).value}</code></pre>\n`; } catch { /* plain, below */ }
        }
      }
      return `<pre><code${named ? ` class="language-${lg}"` : ''}>${esc(code)}</code></pre>\n`;
    },
  },
});

/** md(s): render markdown to sanitized HTML (escaped text on any error). */
export const md = (s) => { try { return marked.parse(String(s ?? '')); } catch { return esc(s); } };

/**
 * mdCssText(scope): how rendered markdown looks under `scope` (a selector),
 * as CSS text for any shadow root (lit: unsafeCSS(mdCssText('.md'))): code
 * blocks in the code face on the code well, inline code, tables with
 * tabular figures, quotes and links — on the theme's tokens (D184), with
 * Night's values where a document has no theme.css.
 */
export function mdCssText(scope) {
  const s = (sel) => sel.split(',').map((x) => `${scope} ${x.trim()}`).join(', ');
  return `
  ${s('p, ul, ol, blockquote')} { margin: 4px 0; }
  ${s('ul, ol')} { padding-left: 24px; }
  ${s('h1, h2, h3, h4')} { margin: 8px 0 4px; }
  ${s('h1, h2')} { font: var(--bx-font-title, 600 16px/22px "Instrument Sans", system-ui, sans-serif); }
  ${s('h3, h4')} { font: var(--bx-font-body, 400 14px/20px "Instrument Sans", system-ui, sans-serif); font-weight: 600; }
  ${s('pre')} { margin: 4px 0; padding: 8px 12px; overflow-x: auto; background: var(--bx-code-bg, #16171D);
    border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); }
  ${s(':not(pre) > code')} { padding: 0 4px; background: var(--bx-code-bg, #16171D); border: 1px solid var(--bx-border, #33353F);
    border-radius: var(--bx-radius, 2px); font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); font-size: 0.92em; }
  ${s('pre code')} { padding: 0; background: none; border: 0; font: inherit; white-space: pre; }
  ${s('pre, code')} { font-variant-ligatures: none; font-feature-settings: "liga" 0, "calt" 0; }
  ${s('table')} { border-collapse: collapse; margin: 4px 0; display: block; overflow-x: auto; font-variant-numeric: tabular-nums; }
  ${s('th, td')} { padding: 2px 8px; border: 1px solid var(--bx-border, #33353F); text-align: left; vertical-align: top; }
  ${s('th')} { font-weight: 600; background: var(--bx-panel-2, #262730); }
  ${s('blockquote')} { padding: 0 0 0 12px; border-left: 2px solid var(--bx-accent, #8C9BFF); color: var(--bx-muted, #A3A6B6); }
  ${s('a')} { color: var(--bx-link, #8C9BFF); text-decoration: underline; }
  ${s('hr')} { border: 0; border-top: 1px solid var(--bx-border, #33353F); margin: 8px 0; }
  ${s('.md-img')} { color: var(--bx-muted, #A3A6B6); font-style: italic; }
  /* colour for reading at a glance: headings, list markers, inline code, the quote's rule; fenced code in the theme's syntax tokens */
  ${s('h1, h2, h3, h4')} { color: var(--bx-syn-function, #96A6FF); }
  ${s('li::marker')} { color: var(--bx-accent, #8C9BFF); }
  ${s(':not(pre) > code')} { color: var(--bx-syn-string, #7BE0B0); }
  ${s('.hljs-comment, .hljs-quote')} { color: var(--bx-syn-comment, #9598A9); font-style: italic; }
  ${s('.hljs-keyword, .hljs-selector-tag, .hljs-section, .hljs-name, .hljs-doctag, .hljs-variable.language_')} { color: var(--bx-syn-keyword, #FF8CC8); }
  ${s('.hljs-string, .hljs-regexp, .hljs-char.escape_')} { color: var(--bx-syn-string, #7BE0B0); }
  ${s('.hljs-number, .hljs-literal, .hljs-symbol, .hljs-bullet')} { color: var(--bx-syn-number, #FFD54A); }
  ${s('.hljs-title, .hljs-title.function_')} { color: var(--bx-syn-function, #96A6FF); }
  ${s('.hljs-type, .hljs-title.class_')} { color: var(--bx-syn-type, #5CCBE3); }
  ${s('.hljs-attr, .hljs-attribute, .hljs-property, .hljs-selector-attr, .hljs-selector-class, .hljs-selector-id')} { color: var(--bx-syn-attr, #F5B07A); }
  ${s('.hljs-built_in, .hljs-meta, .hljs-variable, .hljs-template-variable')} { color: var(--bx-syn-builtin, #C6A8FF); }
  ${s('.hljs-deletion')} { color: var(--bx-syn-deletion, #FF8F8F); background: var(--bx-diff-del-bg, #342429); }
  ${s('.hljs-addition')} { color: var(--bx-syn-addition, #7BE0B0); background: var(--bx-diff-add-bg, #1D302D); }`;
}

/**
 * mdInto(el, s, memo): render markdown into el a top-level block at a time
 * (D130): the text is lexed whole, but only blocks whose source changed are
 * rendered and swapped — a streaming message re-parses its last paragraph,
 * and the blocks before it keep their DOM, so a selection in them survives
 * the next token. Each block sits in a `<div class="md-b">` (display:
 * contents). `memo` (optional: an object kept with the text's owner)
 * remembers each block's HTML, so a row that leaves the window and comes
 * back renders without parsing. Per-block HTML equals md()'s for the whole
 * text. Blocks and memo are of one mdEpoch: once the highlighter arrives,
 * the next call renders every block again.
 */
export function mdInto(el, s, memo) {
  const text = String(s ?? '');
  if (el.$mdText === text && el.$mdEpoch === mdEpoch) return;
  if (el.$mdEpoch !== mdEpoch) { el.$mdEpoch = mdEpoch; el.$mdRaw = null; }
  el.$mdText = text;
  let toks;
  try { toks = marked.lexer(text); } catch { el.replaceChildren(document.createTextNode(text)); el.$mdRaw = null; return; }
  const links = toks.links;
  toks = toks.filter((t) => t.type !== 'space');
  const prev = (memo && memo.epoch === mdEpoch && memo.parts) || [];
  const parts = toks.map((t, i) => {
    if (prev[i] && prev[i].raw === t.raw) return prev[i];
    let h;
    try { const one = [t]; one.links = links; h = marked.parser(one); } catch { h = esc(t.raw); }
    return { raw: t.raw, html: h };
  });
  if (memo) { memo.parts = parts; memo.epoch = mdEpoch; }
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
