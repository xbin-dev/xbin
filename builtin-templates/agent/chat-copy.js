// chat-copy.js — copying from the chat with its formatting (#34, D199).
//
// Ctrl/⌘+C on a selection in the transcript puts two flavours on the
// clipboard: text/html — the selection as rendered, without the chat's own
// chrome (buttons, glyphs, classes, the md-b wrappers) — so an editor keeps
// the numbered list, the table, the emphasis; and text/plain written the way
// markdown reads — "1." before an ordered item, "- " before a bullet, a table
// as pipes, a fence around code — so a plain-text paste keeps them too. A
// copy event sets its own data, so this works in the sandboxed frame, which
// has no clipboard API of its own.
//
// An answer's Copy (copyAnswer) copies the whole message: its markdown as the
// text and its rendering as the HTML, through xbin.copy — the embedding page
// writes the clipboard (D199) — and, on an xbind without it, a copy command
// in the frame itself.
import { md } from './chat-md.js';

const DROP = 'button, bx-icon, svg, img, input, textarea, select, script, style, .cur, .acts';
const KEEP = new Set(['p', 'br', 'strong', 'b', 'em', 'i', 'code', 'pre', 'ul', 'ol', 'li', 'table', 'thead', 'tbody', 'tr', 'th', 'td',
  'a', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'blockquote', 'hr']);

// clean(fragment) → a <div> holding the fragment without the chat's chrome:
// only the tags markdown makes, only href/start/colspan/rowspan on them.
function clean(frag) {
  const box = document.createElement('div');
  box.appendChild(frag);
  box.querySelectorAll(DROP).forEach((n) => n.remove());
  const walk = (el) => {
    for (const n of [...el.children]) {
      walk(n);
      const tag = n.localName;
      if (!KEEP.has(tag)) { n.replaceWith(...n.childNodes); continue; } // a wrapper: its content stays
      for (const a of [...n.attributes]) {
        if (!['href', 'start', 'colspan', 'rowspan'].includes(a.name)) n.removeAttribute(a.name);
      }
    }
  };
  walk(box);
  return box;
}

// plainOf(node) → the text, written as markdown reads.
export function plainOf(node) {
  const out = [];
  const inline = (n) => [...n.childNodes].map((c) => {
    if (c.nodeType === 3) return c.nodeValue;
    if (c.nodeType !== 1) return '';
    const t = inline(c);
    switch (c.localName) {
      case 'strong': case 'b': return t.trim() ? `**${t}**` : t;
      case 'em': case 'i': return t.trim() ? `_${t}_` : t;
      case 'code': return '`' + t + '`';
      case 'a': return c.getAttribute('href') && c.getAttribute('href') !== t ? `[${t}](${c.getAttribute('href')})` : t;
      case 'br': return '\n';
      default: return t;
    }
  }).join('');
  const block = (n, depth = 0) => {
    for (const c of n.childNodes) {
      if (c.nodeType === 3) { if (c.nodeValue.trim()) out.push(c.nodeValue.trim(), ''); continue; }
      if (c.nodeType !== 1) continue;
      const tag = c.localName;
      if (/^h[1-6]$/.test(tag)) { out.push('#'.repeat(Number(tag[1])) + ' ' + inline(c).trim(), ''); continue; }
      if (tag === 'p') { out.push(inline(c).trim(), ''); continue; }
      if (tag === 'pre') { out.push('```', c.textContent.replace(/\n$/, ''), '```', ''); continue; }
      if (tag === 'blockquote') {
        const sub = plainOf(c);
        out.push(sub.split('\n').map((l) => '> ' + l).join('\n'), '');
        continue;
      }
      if (tag === 'hr') { out.push('---', ''); continue; }
      if (tag === 'ul' || tag === 'ol') {
        let n0 = Number(c.getAttribute('start')) || 1;
        for (const li of c.children) {
          if (li.localName !== 'li') continue;
          const lead = '  '.repeat(depth) + (tag === 'ol' ? `${n0++}. ` : '- ');
          const own = [...li.childNodes].filter((x) => !(x.nodeType === 1 && (x.localName === 'ul' || x.localName === 'ol')));
          const holder = document.createElement('div');
          own.forEach((x) => holder.appendChild(x.cloneNode(true)));
          out.push(lead + inline(holder).replace(/\s+/g, ' ').trim());
          for (const sub of li.children) if (sub.localName === 'ul' || sub.localName === 'ol') block({ childNodes: [sub] }, depth + 1);
        }
        if (!depth) out.push('');
        continue;
      }
      if (tag === 'table') {
        const rows = [...c.querySelectorAll('tr')].map((tr) => [...tr.children].map((td) => inline(td).replace(/\|/g, '\\|').replace(/\s+/g, ' ').trim()));
        if (rows.length) {
          out.push('| ' + rows[0].join(' | ') + ' |', '|' + rows[0].map(() => ' --- ').join('|') + '|');
          for (const r of rows.slice(1)) out.push('| ' + r.join(' | ') + ' |');
          out.push('');
        }
        continue;
      }
      block(c, depth); // a wrapper
    }
  };
  block(node);
  return out.join('\n').replace(/\n{3,}/g, '\n\n').trim();
}

// The transcript's copy: both flavours, set in the copy event.
function onCopy(e) {
  const sel = document.getSelection();
  if (!sel || sel.isCollapsed || !sel.rangeCount) return;
  const tl = document.getElementById('timeline');
  const r = sel.getRangeAt(0);
  if (!tl || !tl.contains(r.commonAncestorContainer)) return; // not the chat's: the browser's own copy
  const frag = document.createDocumentFragment();
  for (let i = 0; i < sel.rangeCount; i++) frag.appendChild(sel.getRangeAt(i).cloneContents());
  const box = clean(frag);
  const text = plainOf(box);
  if (!text) return;
  e.clipboardData.setData('text/plain', text);
  e.clipboardData.setData('text/html', box.innerHTML);
  e.preventDefault();
}
document.addEventListener('copy', onCopy);

// frameCopy: the copy command in the frame, its data set in the event.
function frameCopy(text, html) {
  const ta = Object.assign(document.createElement('textarea'), { value: text, readOnly: true });
  ta.style.cssText = 'position:fixed;top:0;left:0;width:1px;height:1px;opacity:0;pointer-events:none';
  const set = (e) => { e.stopImmediatePropagation(); e.clipboardData.setData('text/plain', text); e.clipboardData.setData('text/html', html); e.preventDefault(); };
  document.body.appendChild(ta);
  document.addEventListener('copy', set, true);
  let ok = false;
  try { ta.select(); ok = document.execCommand('copy'); } catch { /* no command */ }
  document.removeEventListener('copy', set, true);
  ta.remove();
  return ok;
}

// copyAnswer(e, text): an answer's Copy button — its markdown and its
// rendering; the button says how it went.
export async function copyAnswer(e, text) {
  const btn = e.currentTarget;
  const html = clean(document.createRange().createContextualFragment(md(text))).innerHTML;
  let ok = false;
  if (typeof globalThis.xbin?.copy === 'function') ok = await globalThis.xbin.copy({ text, html }).catch(() => false);
  if (!ok) ok = frameCopy(text, html);
  if (!btn) return ok;
  btn.title = ok ? 'copied' : 'copy refused — select the text and press Ctrl+C';
  btn.classList.toggle('done', ok);
  setTimeout(() => { btn.title = 'copy this answer (with its formatting)'; btn.classList.remove('done'); }, 1500);
  return ok;
}
