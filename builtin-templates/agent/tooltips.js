// tooltips.js — what a part of a page is, on demand (D214: the project
// overview explains every cell, chip, button and the bar). Any element with
// `data-tip` has one: its text, the first sentence its title (or
// `data-tip-title`, then data-tip is all body). Drawn as plain text.
//
//   hover   after HOVER ms of the pointer resting on the element — passing
//           over things shows nothing; once one shows, moving onto another
//           tipped element switches at once (and for SKIP ms after one
//           closed)
//   focus   at once when it gets keyboard focus (:focus-visible); the
//           element is aria-describedby the tooltip while it shows
//   touch   a tap shows it until the next tap
//   Escape, a click, a scroll or the element going away hides it
// It is kept inside the window, and fades only without reduced motion.
// Imported once (projects.js through project-status.js); nothing to call:
// the listeners are on the document. syncTips() hides a tooltip whose
// element left the page (a paint hook calls it).

export const HOVER = 600;
const SKIP = 300;
const ID = 'bx-tip';

let box = null;      // the tooltip element (made on first use)
let cur = null;      // the element whose tooltip shows
let how = '';        // how it was shown: hover | focus | touch
let wait = null;     // the element the hover timer waits on
let timer = 0;
let closedAt = 0;
let pressed = false; // a mouse button is down (a drag): no tooltips meanwhile
let described = null; // [element, its own aria-describedby] while it points at the tooltip
const at = { x: 0, y: 0 };

function tipBox() {
  if (box && box.isConnected) return box;
  box = document.createElement('div');
  box.id = ID;
  box.className = 'bxtip';
  box.setAttribute('role', 'tooltip');
  box.hidden = true;
  document.body.append(box);
  return box;
}

/** tipParts(el): {title, body} of an element's data-tip. */
export function tipParts(el) {
  const text = String(el.dataset.tip || '').trim();
  if (el.dataset.tipTitle != null) return { title: el.dataset.tipTitle, body: text };
  const m = /^(.+?[.?!])\s+([\s\S]+)$/.exec(text);
  return m ? { title: m[1].replace(/\.$/, ''), body: m[2] } : { title: '', body: text };
}

function undescribe() {
  if (!described) return;
  const [el, own] = described;
  if (own) el.setAttribute('aria-describedby', own); else el.removeAttribute('aria-describedby');
  described = null;
}

function place(b, r, x, y) {
  const w = b.offsetWidth, h = b.offsetHeight;
  const vw = document.documentElement.clientWidth, vh = document.documentElement.clientHeight;
  let left, top;
  if (r) { left = r.left; top = r.bottom + 6; if (top + h > vh - 8) top = r.top - h - 6; } // under the element, else above it
  else { left = x + 14; top = y + 18; if (top + h > vh - 8) top = y - h - 10; } // by the pointer
  if (left + w > vw - 8) left = vw - 8 - w;
  b.style.left = Math.round(Math.max(8, left)) + 'px';
  b.style.top = Math.round(Math.max(8, top)) + 'px';
}

function show(el, mode) {
  clearTimeout(timer); wait = null;
  const b = tipBox();
  const { title, body } = tipParts(el);
  if (!title && !body) return;
  if (cur !== el) {
    b.replaceChildren();
    if (title) { const t = document.createElement('b'); t.textContent = title; b.append(t); }
    if (body) { const d = document.createElement('span'); d.textContent = body; b.append(d); }
  }
  undescribe();
  cur = el; how = mode;
  b.hidden = false;
  if (mode === 'focus') {
    described = [el, el.getAttribute('aria-describedby')];
    el.setAttribute('aria-describedby', [described[1], ID].filter(Boolean).join(' '));
    place(b, el.getBoundingClientRect());
  } else place(b, null, at.x, at.y);
  b.classList.add('on');
}

/** hideTip(): the tooltip goes. */
export function hideTip() {
  clearTimeout(timer); wait = null;
  if (!cur) return;
  undescribe();
  cur = null; how = '';
  closedAt = Date.now();
  if (box) { box.classList.remove('on'); box.hidden = true; }
}

/** syncTips(): hide a tooltip whose element left the page (call after a paint). */
export function syncTips() { if (cur && !cur.isConnected) hideTip(); if (wait && !wait.isConnected) { clearTimeout(timer); wait = null; } }

const tipped = (t) => (t && t.closest ? t.closest('[data-tip]') : null);

function over(e) {
  if (e.buttons === 0) pressed = false; // a button let go outside the window
  if (e.pointerType === 'touch' || pressed) return;
  at.x = e.clientX; at.y = e.clientY;
  const el = tipped(e.target);
  if (el && (el === cur || el === wait)) return;
  if (!el) { if (how !== 'focus') hideTip(); else { clearTimeout(timer); wait = null; } return; }
  if ((cur && how === 'hover') || Date.now() - closedAt < SKIP) { show(el, 'hover'); return; } // one is open: switch at once
  clearTimeout(timer);
  wait = el;
  timer = setTimeout(() => { if (wait === el && el.isConnected) show(el, 'hover'); }, HOVER);
}

function down(e) {
  if (e.pointerType === 'touch') {
    const el = tipped(e.target);
    at.x = e.clientX; at.y = e.clientY;
    if (el && el !== cur) show(el, 'touch'); else hideTip();
    return;
  }
  pressed = true;
  hideTip();
}

if (typeof document !== 'undefined') {
  document.addEventListener('pointerover', over, true);
  document.addEventListener('pointermove', (e) => { at.x = e.clientX; at.y = e.clientY; }, { capture: true, passive: true });
  document.addEventListener('pointerout', (e) => { if (!e.relatedTarget && how === 'hover') hideTip(); if (!e.relatedTarget) { clearTimeout(timer); wait = null; } }, true);
  document.addEventListener('pointerdown', down, true);
  document.addEventListener('pointerup', () => { pressed = false; }, true);
  document.addEventListener('pointercancel', () => { pressed = false; }, true);
  document.addEventListener('focusin', (e) => {
    const el = tipped(e.target);
    let kb = false;
    try { kb = e.target.matches(':focus-visible'); } catch { /* an old engine: no keyboard tooltips */ }
    if (el && kb && !pressed) show(el, 'focus'); // a click into a text box is not the keyboard's
  });
  document.addEventListener('focusout', () => { if (how === 'focus') hideTip(); });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && cur) hideTip(); }, true);
  document.addEventListener('scroll', () => { if (cur && how !== 'focus') hideTip(); }, { capture: true, passive: true });

  const style = document.createElement('style');
  style.textContent = `
    .bxtip { position: fixed; z-index: 1000; max-width: 340px; box-sizing: border-box; padding: 6px 10px; pointer-events: none;
      background: var(--bx-text); color: var(--bx-bg); border-radius: var(--bx-radius); box-shadow: var(--bx-shadow-pop);
      font: var(--bx-font-meta); overflow-wrap: anywhere; opacity: 0; transition: opacity var(--bx-dur-ui) var(--bx-ease-out); }
    .bxtip.on { opacity: 1; }
    .bxtip[hidden] { display: none; }
    .bxtip > b { display: block; font-weight: 600; margin-bottom: 2px; }
    @media (prefers-reduced-motion: reduce) { .bxtip { transition: none; } }
  `;
  document.head.append(style);
}
