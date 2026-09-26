/**
 * xb/rt-markdown.js — markdown for the native runtime: the vendored marked
 * lexer turns `markdown source` (and `message markdown text`) into a small,
 * sanitized token tree the app renders natively, so web and iOS agree on
 * what a document is. Same policy as the web kit's renderers (bx-md.js,
 * the agent template's chat-md.js): gfm with single-newline breaks, raw HTML
 * dropped, images shown as their text (never loaded), links only for
 * http/https/mailto. The token shapes are native/spec/tree.md "Markdown
 * tokens".
 *
 * Streaming: while a node is `streaming`, only the tail (the last block of
 * the previous lex) is re-lexed when the source grows by appending; the final
 * render (streaming false) is always a full lex.
 */
import { Lexer } from '/vendor/marked.esm.js';

// A fresh options object per lexer: marked mutates it (tokenizer), and
// marked.use() elsewhere in a document changes the shared defaults.
const options = () => ({ async: false, breaks: true, extensions: null, gfm: true, hooks: null,
  pedantic: false, renderer: null, silent: false, tokenizer: null, walkTokens: null });

const ENT = { amp: '&', lt: '<', gt: '>', quot: '"', apos: "'", nbsp: ' ', copy: '©', reg: '®',
  trade: '™', hellip: '…', mdash: '—', ndash: '–', lsquo: '‘', rsquo: '’', ldquo: '“', rdquo: '”',
  bull: '•', middot: '·', deg: '°', times: '×', divide: '÷', euro: '€', pound: '£', yen: '¥', cent: '¢',
  sect: '§', para: '¶', plusmn: '±', laquo: '«', raquo: '»', larr: '←', rarr: '→', uarr: '↑', darr: '↓',
  harr: '↔', check: '✓', hearts: '♥', micro: 'µ', frac12: '½', frac14: '¼', frac34: '¾', shy: '­' };
function entities(s) {
  if (s.indexOf('&') < 0) return s;
  return s.replace(/&(#x[0-9a-fA-F]+|#[0-9]+|[a-zA-Z][a-zA-Z0-9]*);/g, (all, e) => {
    if (e[0] === '#') {
      const n = e[1] === 'x' || e[1] === 'X' ? parseInt(e.slice(2), 16) : parseInt(e.slice(1), 10);
      return Number.isFinite(n) && n > 0 && n <= 0x10ffff ? String.fromCodePoint(n) : all;
    }
    return Object.prototype.hasOwnProperty.call(ENT, e) ? ENT[e] : all;
  });
}

const SAFE_LINK = /^(https?:|mailto:)/i;

// inline(marked inline tokens) → wire inline tokens; adjacent text merges.
function inline(tokens, out = []) {
  const text = (s) => {
    if (!s) return;
    const last = out[out.length - 1];
    if (last && last.t === 'text') last.text += s;
    else out.push({ t: 'text', text: s });
  };
  for (const tk of tokens || []) {
    switch (tk.type) {
      case 'text':
        if (tk.tokens && tk.tokens.length) inline(tk.tokens, out);
        else text(entities(tk.text || ''));
        break;
      case 'escape': text(tk.text || ''); break;
      case 'strong': case 'em': case 'del':
        out.push({ t: tk.type, c: inline(tk.tokens) });
        break;
      case 'codespan': out.push({ t: 'codespan', text: tk.text || '' }); break;
      case 'br': out.push({ t: 'br' }); break;
      case 'link': {
        const href = String(tk.href || '').trim();
        if (SAFE_LINK.test(href)) out.push({ t: 'link', href, c: inline(tk.tokens) });
        else inline(tk.tokens, out);
        break;
      }
      case 'image': text(`[image: ${entities(tk.text || '') || tk.href || ''}]`); break;
      case 'html': case 'checkbox': break; // raw HTML is dropped
      default:
        if (tk.tokens) inline(tk.tokens, out);
        else if (typeof tk.text === 'string') text(entities(tk.text));
    }
  }
  return out;
}

// blocks(marked block tokens) → wire block tokens.
function blocks(tokens, o, out = []) {
  for (const tk of tokens || []) {
    const b = block(tk, o);
    if (b) out.push(b);
  }
  return out;
}

function block(tk, o) {
  switch (tk.type) {
    case 'space': case 'def': case 'html': return null;
    case 'heading': return { t: 'heading', depth: tk.depth, c: inline(tk.tokens) };
    case 'paragraph': return { t: 'paragraph', c: inline(tk.tokens) };
    case 'text': return { t: 'paragraph', c: tk.tokens ? inline(tk.tokens) : inline([{ type: 'text', text: tk.text }]) };
    case 'code': {
      const b = { t: 'code', text: tk.text || '' };
      const lang = String(tk.lang || '').trim().split(/\s+/)[0];
      if (lang) b.lang = lang;
      return b;
    }
    case 'blockquote': return { t: 'blockquote', c: blocks(tk.tokens, o) };
    case 'hr': return { t: 'hr' };
    case 'list': {
      const b = { t: 'list', ordered: !!tk.ordered, loose: !!tk.loose, items: [] };
      if (tk.ordered) b.start = Number(tk.start) || 1;
      for (const it of tk.items || []) {
        const item = { c: blocks(it.tokens, o) };
        if (it.task) { item.task = true; item.checked = !!it.checked; }
        b.items.push(item);
      }
      return b;
    }
    case 'table':
      if (!o.tables) return { t: 'code', text: String(tk.raw || '').replace(/\s+$/, '') };
      return { t: 'table', align: (tk.align || []).map((a) => a || null),
        header: (tk.header || []).map((cell) => inline(cell.tokens)),
        rows: (tk.rows || []).map((row) => row.map((cell) => inline(cell.tokens))) };
    default:
      if (tk.tokens) return { t: 'paragraph', c: inline(tk.tokens) };
      if (typeof tk.text === 'string' && tk.text) return { t: 'paragraph', c: [{ t: 'text', text: entities(tk.text) }] };
      return null;
  }
}

// lex(src) → {src, parts: [{raw, out: [wire blocks]}], ok}: one part per
// top-level marked token, `ok` when the raws re-assemble the (normalized)
// source so a later tail re-lex can trust the offsets.
function lex(src, o) {
  const norm = src.replace(/\r\n?/g, '\n');
  let toks;
  try { toks = new Lexer(options()).lex(norm); } catch { return { parts: [{ raw: norm, out: fallback(norm) }], ok: false }; }
  const parts = toks.map((tk) => ({ raw: tk.raw || '', space: tk.type === 'space', out: blocks([tk], o) }));
  return { parts, ok: parts.map((p) => p.raw).join('') === norm };
}
const fallback = (s) => (s ? [{ t: 'paragraph', c: [{ t: 'text', text: s }] }] : []);

const flat = (parts) => parts.flatMap((p) => p.out);

// markdownTokens(src) — the sanitized tokens of a whole document.
export function markdownTokens(src, { tables = true } = {}) {
  return flat(lex(String(src ?? ''), { tables }).parts);
}

// MarkdownCache — tokens per node key across renders. begin() before a
// render, tokens(key, src, streaming) per markdown node, end() after it
// (entries of keys that were not rendered are dropped).
export class MarkdownCache {
  constructor() { this.prev = new Map(); this.next = new Map(); this.stats = { full: 0, tail: 0, hit: 0 }; }
  begin() { this.next = new Map(); }
  end() { this.prev = this.next; this.next = new Map(); }
  tokens(key, src, streaming, { tables = true } = {}) {
    src = String(src ?? '');
    const o = { tables };
    const e = this.prev.get(key);
    let r;
    if (e && e.src === src && e.tables === tables && (e.full || streaming)) { r = e; this.stats.hit++; }
    else if (streaming && e && e.ok && e.tables === tables && src.startsWith(e.src) && src.indexOf('\r') < 0) {
      r = tail(e, src, o); this.stats.tail++;
    } else {
      const l = lex(src, o);
      r = { src, tables, parts: l.parts, ok: l.ok, full: true, tokens: flat(l.parts) };
      this.stats.full++;
    }
    this.next.set(key, r);
    return r.tokens;
  }
}

// tail(e, src): keep every part before the last non-space one, re-lex the rest.
function tail(e, src, o) {
  let keep = e.parts.length;
  while (keep > 0 && e.parts[keep - 1].space) keep--;
  if (keep > 0) keep--; // the last block may still change (an open paragraph, list, fence)
  const kept = e.parts.slice(0, keep);
  const start = kept.reduce((n, p) => n + p.raw.length, 0);
  const l = lex(src.slice(start), o);
  const parts = kept.concat(l.parts);
  return { src, tables: o.tables, parts, ok: l.ok, full: false, tokens: flat(parts) };
}
