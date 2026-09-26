/**
 * xb/render-markdown.js — markdown tokens (native/spec/tree.md "Markdown
 * tokens") drawn as lit templates. The runtime lexes; this only draws the
 * sanitized subset: every string is a text binding (never parsed as HTML),
 * links keep only http/https/mailto targets and never navigate — a tap goes
 * to `link(href)` (the node's `link` event).
 *
 * A tree without tokens (a hand-written fixture, an old runtime) is lexed
 * here with the runtime's own lexer (xb/rt-markdown.js), so both agree.
 */
import { html, nothing } from '/vendor/lit-all.min.js';
import { markdownTokens } from '/vendor/xb/rt-markdown.js';
import { icon } from '/vendor/xb/render-base.js';

const SAFE = /^(https?:|mailto:)/i;
const ALIGN = new Set(['left', 'center', 'right']);

// tokensOf(p, textProp): the wire tokens, or a local lex of the source.
export function tokensOf(p, textProp) {
  if (Array.isArray(p.tokens)) return p.tokens;
  const src = p[textProp];
  return typeof src === 'string' && src ? markdownTokens(src) : [];
}

export function mdBlocks(tokens, link) {
  return (Array.isArray(tokens) ? tokens : []).map((b) => block(b, link));
}

function block(b, L) {
  if (!b || typeof b !== 'object') return nothing;
  switch (b.t) {
    case 'heading': {
      const d = Math.min(6, Math.max(1, Number(b.depth) || 1));
      return html`<div class=${`md-h md-h${d}`} role="heading" aria-level=${d}>${inl(b.c, L)}</div>`;
    }
    case 'paragraph': return html`<p>${inl(b.c, L)}</p>`;
    case 'list': {
      const items = (b.items || []).map((it) => html`<li class=${it.task ? 'task' : ''}>${it.task
        ? html`<span class=${`md-check${it.checked ? ' on' : ''}`}>${icon(it.checked ? 'check' : 'ui-square')}</span>` : nothing}<div class="md-li">${mdBlocks(it.c, L)}</div></li>`);
      const c = `md-list${b.loose ? ' loose' : ''}`;
      return b.ordered ? html`<ol class=${c} start=${Number(b.start) || 1}>${items}</ol>` : html`<ul class=${c}>${items}</ul>`;
    }
    case 'code': return html`<div class="md-code">${b.lang ? html`<div class="md-lang">${String(b.lang)}</div>` : nothing}<pre>${String(b.text ?? '')}</pre></div>`;
    case 'blockquote': return html`<blockquote>${mdBlocks(b.c, L)}</blockquote>`;
    case 'table': {
      const al = (i) => (ALIGN.has(b.align?.[i]) ? `text-align:${b.align[i]}` : '');
      return html`<div class="md-table"><table>
        <thead><tr>${(b.header || []).map((c, i) => html`<th style=${al(i)}>${inl(c, L)}</th>`)}</tr></thead>
        <tbody>${(b.rows || []).map((r) => html`<tr>${(r || []).map((c, i) => html`<td style=${al(i)}>${inl(c, L)}</td>`)}</tr>`)}</tbody>
      </table></div>`;
    }
    case 'hr': return html`<hr>`;
    default: return b.text ? html`<p>${String(b.text)}</p>` : nothing;
  }
}

function inl(c, L) {
  return (Array.isArray(c) ? c : []).map((t) => {
    if (!t || typeof t !== 'object') return nothing;
    switch (t.t) {
      case 'text': return String(t.text ?? '');
      case 'strong': return html`<strong>${inl(t.c, L)}</strong>`;
      case 'em': return html`<em>${inl(t.c, L)}</em>`;
      case 'del': return html`<del>${inl(t.c, L)}</del>`;
      case 'codespan': return html`<code>${String(t.text ?? '')}</code>`;
      case 'br': return html`<br>`;
      case 'link': {
        const href = String(t.href ?? '');
        if (!SAFE.test(href)) return inl(t.c, L);
        return html`<a href=${href} title=${href} @click=${(e) => { e.preventDefault(); L?.(href); }}>${inl(t.c, L)}</a>`;
      }
      default: return t.text != null ? String(t.text) : inl(t.c, L);
    }
  });
}
