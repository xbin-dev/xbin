// hack/demo/cam/overlay.js — the camera's in-page layer: a synthetic cursor
// and click ripples, drawn by the page itself (a headless or Xvfb capture
// has no visible pointer). cam.js installs source() as an init script in
// every page; only the top document draws, in the top layer (a manual
// popover, re-raised whenever a modal dialog opens above it), inside a closed
// shadow root so neither the page's styles nor its locators can see it.
//
// Node drives it through window.__cam: play(segs) animates the cursor along
// a planned path with the Web Animations API — a compositor animation timed
// by the frame clock, so it stays smooth while the page's main thread is
// busy, and stays exact when the capture controls the frames (beginframe).
// The path math is motion.js's pointAt, sent in as source.
'use strict';
const { pointAt } = require('./motion');

// The cursors, drawn for a dark UI at 1× (CSS px); hot = the hotspot.
const CURSORS = {
  // the classic arrow: dark fill, white rim
  default: {
    hot: [3, 2],
    svg: '<svg width="26" height="30" viewBox="0 0 26 30"><path d="M3 2v20.6l5.1-4.9 3.3 7.8 3.6-1.5-3.3-7.7h7.1z" fill="#14161a" stroke="#fff" stroke-width="1.6" stroke-linejoin="round"/></svg>',
  },
  // the pointing hand, over anything with cursor: pointer
  pointer: {
    hot: [10, 3],
    svg: '<svg width="28" height="30" viewBox="0 0 28 30"><path d="M10 2.6c-1.2 0-2.1.9-2.1 2.1v10.6L6.3 13.6c-.8-.9-2.2-1-3.1-.2-.8.7-1 1.9-.4 2.9l4.6 7.1c1.5 2.3 4 3.7 6.8 3.7h2.3c4 0 7.2-3.2 7.2-7.2v-5.4c0-1.1-.9-2-2-2-.5 0-1 .2-1.3.5v-.3c0-1.1-.9-2-2-2-.6 0-1.2.3-1.6.8-.2-1-1-1.6-2-1.6-.5 0-1 .2-1.4.5V4.7c0-1.2-.9-2.1-2.1-2.1z" fill="#fff" stroke="#14161a" stroke-width="1.4" stroke-linejoin="round"/><path d="M12.1 13.4v5.2M15.6 14.2v4.6M19.1 15v3.8" stroke="#14161a" stroke-width="1.2" stroke-linecap="round"/></svg>',
  },
  // the text I-beam, over inputs, editors and terminals
  text: {
    hot: [8, 12],
    svg: '<svg width="16" height="24" viewBox="0 0 16 24"><path d="M4 2.5h2.6c.6 0 1.1.3 1.4.6.3-.3.8-.6 1.4-.6H12M8 3.2v17.6M4 21.5h2.6c.6 0 1.1-.3 1.4-.6.3.3.8.6 1.4.6H12M5.6 12h4.8" fill="none" stroke="#fff" stroke-width="3.4" stroke-linecap="round"/><path d="M4 2.5h2.6c.6 0 1.1.3 1.4.6.3-.3.8-.6 1.4-.6H12M8 3.2v17.6M4 21.5h2.6c.6 0 1.1-.3 1.4-.6.3.3.8.6 1.4.6H12M5.6 12h4.8" fill="none" stroke="#14161a" stroke-width="1.4" stroke-linecap="round"/></svg>',
  },
};

// The page side. Runs in every document; draws only in the top one.
function overlayMain(cfg, CURSORS, pointAt) {
  if (window.top !== window || window.__cam) return;
  const st = { x: -60, y: -60, kind: 'default', shown: true, anim: null, segs: null, hint: null, scale: cfg.scale || 1 };
  try { // continuity across a navigation: where the cursor was
    const s = JSON.parse(sessionStorage.getItem('__camPos') || 'null');
    if (Array.isArray(s)) [st.x, st.y] = s;
  } catch { /* storage off: start off-screen */ }
  let host = null, root = null, cur = null, shape = null;
  const css = `
    :host { all: initial; }
    .layer { position: fixed; inset: 0; pointer-events: none; overflow: hidden; contain: strict; }
    .cur { position: absolute; left: 0; top: 0; will-change: transform; }
    .shape { position: absolute; transition: transform 120ms cubic-bezier(.2,.8,.3,1);
      filter: drop-shadow(0 1px 1.5px rgba(0,0,0,.45)); }
    .shape.down { transform: scale(.86); transition-duration: 70ms; }
    .shape svg { display: block; }
    .rip { position: absolute; width: 46px; height: 46px; margin: -23px 0 0 -23px; border-radius: 50%;
      box-sizing: border-box; border: 2px solid var(--ring); background: var(--fill);
      animation: rip 560ms cubic-bezier(.2,.7,.3,1) forwards; }
    @keyframes rip { from { transform: scale(.2); opacity: 1; } 60% { opacity: .55; } to { transform: scale(1.3); opacity: 0; } }
    .hidden { display: none; }`;
  function mount() {
    if (host && host.isConnected) return true;
    const de = document.documentElement;
    if (!de) return false;
    host = document.createElement('xbin-cam');
    host.setAttribute('popover', 'manual');
    host.style.cssText = 'position:fixed!important;inset:0!important;width:100vw!important;height:100vh!important;max-width:none!important;max-height:none!important;margin:0!important;padding:0!important;border:0!important;background:transparent!important;overflow:visible!important;pointer-events:none!important;z-index:2147483647!important;display:block!important;color-scheme:normal!important;';
    root = host.attachShadow({ mode: 'closed' });
    root.innerHTML = `<style>${css}</style><div class="layer" style="--ring:${cfg.ring};--fill:${cfg.fill}"><div class="cur"><div class="shape"></div></div></div>`;
    cur = root.querySelector('.cur');
    shape = root.querySelector('.shape');
    de.appendChild(host);
    raise();
    setKind(st.kind, true);
    place(st.x, st.y);
    show(st.shown);
    return true;
  }
  // back on top of the top layer: a modal opened after us covers us
  function raise() {
    if (!host || !host.showPopover) return;
    try { if (host.matches(':popover-open')) host.hidePopover(); host.showPopover(); } catch { /* not connected yet */ }
  }
  // .cur sits exactly on the pointer's point; the shape hangs off it by its
  // hotspot, so a change of cursor mid-move never shifts the point
  const at = (x, y) => `translate3d(${x}px, ${y}px, 0)`;
  function place(x, y) {
    st.x = x; st.y = y;
    if (cur) cur.style.transform = at(x, y);
  }
  function setKind(kind, force) {
    if (!CURSORS[kind]) kind = 'default';
    if (kind === st.kind && !force) return;
    st.kind = kind;
    if (!shape) return;
    const [hx, hy] = CURSORS[kind].hot;
    shape.innerHTML = CURSORS[kind].svg;
    shape.style.left = `${-hx}px`;
    shape.style.top = `${-hy}px`;
    shape.style.transformOrigin = `${hx}px ${hy}px`;
    shape.style.scale = String(st.scale);
  }
  function show(v) {
    st.shown = v;
    if (cur) cur.classList.toggle('hidden', !v);
  }
  // the CSS cursor the page shows at (x, y): through open shadow roots and
  // into same-origin frames — what the OS pointer would turn into. null: a
  // frame this document can't look into (a sandboxed tile) — the move's hint
  // (the target's own cursor, read by Node) answers for its box there.
  function kindAt(x, y) {
    let doc = document, el = null, ox = 0, oy = 0;
    for (let depth = 0; depth < 8; depth++) {
      el = doc.elementFromPoint(x - ox, y - oy);
      while (el && el.shadowRoot) {
        const inner = el.shadowRoot.elementFromPoint(x - ox, y - oy);
        if (!inner || inner === el) break;
        el = inner;
      }
      if (el && el.tagName === 'IFRAME') {
        let d = null;
        try { d = el.contentDocument; } catch { d = null; }
        if (!d) return null;
        const r = el.getBoundingClientRect();
        ox += r.left + el.clientLeft; oy += r.top + el.clientTop;
        doc = d;
        continue;
      }
      break;
    }
    if (!el) return 'default';
    const c = getComputedStyle(el).cursor;
    if (c === 'pointer') return 'pointer';
    if (c === 'text' || c === 'vertical-text') return 'text';
    if (c === 'auto' && (el.isContentEditable || /^(INPUT|TEXTAREA)$/.test(el.tagName))) return 'text';
    return 'default';
  }
  const inBox = (p, b) => b && p[0] >= b.x && p[0] <= b.x + b.width && p[1] >= b.y && p[1] <= b.y + b.height;
  const kindFor = (p) => kindAt(p[0], p[1]) ?? (st.hint && inBox(p, st.hint.box) ? st.hint.kind : 'default');
  let kindTimer = 0;
  function trackKind(on) {
    clearInterval(kindTimer);
    if (on) kindTimer = setInterval(() => setKind(kindFor(here())), 40);
  }
  // where the cursor is drawn now: mid-animation, from the animation's own clock
  function here() {
    if (st.anim && st.segs && st.anim.currentTime != null) return pointAt(st.segs, Number(st.anim.currentTime));
    return [st.x, st.y];
  }
  function play(segs, hint) {
    if (!mount()) return 0;
    stop();
    st.hint = hint || null;
    const total = segs.reduce((n, s) => n + s.dur, 0);
    if (!total) return 0;
    const n = Math.max(2, Math.ceil(total / 8));
    const frames = [];
    for (let i = 0; i <= n; i++) {
      const p = pointAt(segs, (total * i) / n);
      frames.push({ transform: at(p[0], p[1]), offset: i / n });
    }
    const end = segs[segs.length - 1].p1;
    st.segs = segs;
    st.anim = cur.animate(frames, { duration: total, easing: 'linear', fill: 'forwards' });
    st.anim.onfinish = () => { if (st.segs === segs) { stop(); place(end[0], end[1]); setKind(kindFor(end)); save(); } };
    trackKind(true);
    return total;
  }
  function stop() {
    if (st.anim) {
      const p = here();
      st.anim.onfinish = null;
      st.anim.cancel();
      st.anim = null;
      st.segs = null;
      place(p[0], p[1]);
    }
    trackKind(false);
  }
  function save() { try { sessionStorage.setItem('__camPos', JSON.stringify([st.x, st.y])); } catch { /* fine */ } }
  function ripple(x, y) {
    if (!mount()) return;
    const el = document.createElement('div');
    el.className = 'rip';
    el.style.left = `${x}px`;
    el.style.top = `${y}px`;
    el.addEventListener('animationend', () => el.remove());
    root.querySelector('.layer').insertBefore(el, cur);
  }
  window.__cam = {
    play,
    set(x, y) { if (!mount()) return; stop(); place(x, y); setKind(kindFor([x, y])); save(); },
    down(x, y) { if (!mount()) return; shape.classList.add('down'); if (cfg.ripple) ripple(x, y); },
    up() { if (shape) shape.classList.remove('down'); },
    show(v) { mount(); show(!!v); },
    kind: (k) => setKind(k),
    state: () => ({ x: here()[0], y: here()[1], kind: st.kind, shown: st.shown, moving: !!st.anim }),
    raise,
  };
  // a modal dialog or popover opened after us would cover the cursor
  const watch = () => {
    new MutationObserver((ms) => {
      if (ms.some((m) => m.target !== host && m.target.hasAttribute && m.target.hasAttribute('open'))) raise();
    }).observe(document, { subtree: true, attributes: true, attributeFilter: ['open'] });
    document.addEventListener('toggle', (e) => { if (e.target !== host && e.newState === 'open') raise(); }, true);
  };
  if (document.documentElement) { mount(); watch(); } else {
    document.addEventListener('readystatechange', function once() { if (mount()) { watch(); document.removeEventListener('readystatechange', once); } });
  }
}

// source(cfg): the init script. cfg: {scale, ring, fill, ripple}.
function source(cfg = {}) {
  const c = { scale: 1, ring: 'rgba(245,166,35,.95)', fill: 'rgba(245,166,35,.16)', ripple: true, ...cfg };
  return `(${overlayMain.toString()})(${JSON.stringify(c)}, ${JSON.stringify(CURSORS)}, ${pointAt.toString()});`;
}

module.exports = { source, CURSORS };
