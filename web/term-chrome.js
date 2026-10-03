/**
 * term-chrome.js — what <bx-terminal> (and <bx-logs>) keep around xterm:
 * loading it once per document (loadXterm), waiting for the face before
 * xterm measures its cells (fontsLoaded), and the terminal's own shadow
 * markup and styles (termShadowHTML, D184): the screen's host (the face
 * and size xterm reads, from the tokens), the settings gear and its menu
 * (theme, font size, predictive echo), the slow-link chip and the
 * prediction overlay's layer. xterm's stylesheet is linked inside the
 * shadow root, so <bx-terminal> works anywhere, including inside other
 * elements' shadow DOM. Split from bx-terminal.js (its size budget); the
 * element owns the behaviour and the classes it queries.
 */
import { SRTT_SHOW } from '/vendor/term-predict.js';
import { scrollCssText } from '/vendor/bx-scroll.js';
import { iconSvg } from '/vendor/bx-icons.js';

// Load a classic script once per document. Several elements (the terminal,
// the read-only logs view) share the tag by id, so a second caller must wait
// for the SAME tag to load — not resolve just because the tag exists.
const scriptOnce = (src) => {
  const id = 'bxs-' + src.replace(/\W/g, '');
  let s = document.getElementById(id);
  if (s?.dataset.loaded) return Promise.resolve();
  if (!s) {
    s = document.createElement('script');
    s.id = id; s.src = src;
    document.head.appendChild(s);
  }
  return new Promise((res, rej) => {
    s.addEventListener('load', () => { s.dataset.loaded = '1'; res(); }, { once: true });
    s.addEventListener('error', rej, { once: true });
  });
};

// fontsLoaded(family, px): xterm measures its cells once (and again only when
// its font options change), so the face must be there first. Waits for the
// regular and bold faces, at most `wait` ms; a face that fails to load (or a
// document without it) resolves at once: the fallback face is measured then.
export function fontsLoaded(family, px, wait = 1500) {
  const fs = document.fonts;
  if (!fs?.load || !family) return Promise.resolve();
  const both = Promise.all([fs.load(`${px}px ${family}`), fs.load(`700 ${px}px ${family}`)]).catch(() => { });
  return Promise.race([both, new Promise((r) => setTimeout(r, wait))]);
}

let xtermReady = null;
export function loadXterm() {
  xtermReady ??= (async () => {
    await scriptOnce('/vendor/xterm.js');
    await scriptOnce('/vendor/addon-fit.js');
    await scriptOnce('/vendor/addon-web-links.js');
  })();
  return xtermReady;
}

// termShadowHTML(): the shadow root's markup, styles first.
export const termShadowHTML = () =>
  `<link rel="stylesheet" href="/vendor/xterm.css">` +
  `<style>
    ${scrollCssText}
    :host{display:block; position:relative}
    /* the face and size xterm reads (#start): the tokens, Night's where
       a document has no theme.css */
    .host{height:100%; background:var(--bx-term-bg, #0B0C12);
      font-family:var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); font-size:var(--bx-term-size, 12px)}
    /* product-ui §7: 8px 12px around the screen (fit measures inside
       it); ligatures off: a program's -> and != are drawn as typed */
    .host .xterm{padding:8px 12px; font-variant-ligatures:none; font-feature-settings:"liga" 0, "calt" 0}
    button, select{font:inherit}
    :focus-visible{outline:var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset:var(--bx-focus-offset, 2px);
      box-shadow:var(--bx-focus-halo, 0 0 0 2px #0B0C12)}
    .gear, .lag{position:absolute; top:6px; z-index:8; box-sizing:border-box; height:24px; padding:0;
      border:1px solid var(--bx-border, #33353F); border-radius:var(--bx-radius, 2px); cursor:pointer;
      background:var(--bx-panel, #1F2028); color:var(--bx-muted, #A3A6B6);}
    .gear{right:12px; width:24px; display:grid; place-items:center; opacity:0;
      transition:opacity var(--bx-dur-ui, 120ms) var(--bx-ease-out, cubic-bezier(0.16, 1, 0.3, 1));}
    :host(:hover) .gear, .gear:focus-visible, .gear.open{opacity:1}
    .gear:hover, .gear.open{color:var(--bx-text, #E9EAF0); background:var(--bx-hover, #2A2B34)}
    .gear svg, .lag svg{display:block; flex:none}
    .tmenu{position:absolute; top:34px; right:12px; z-index:9; min-width:232px; box-sizing:border-box;
      background:var(--bx-panel, #1F2028); color:var(--bx-text, #E9EAF0);
      border:1px solid var(--bx-border, #33353F); border-radius:var(--bx-radius, 2px); padding:8px var(--bx-pad, 12px);
      box-shadow:var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6)); font:var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif);}
    .tmenu[hidden]{display:none}
    .tmenu .hd{font:var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif);
      letter-spacing:var(--bx-tracking-micro, 0.06em); text-transform:uppercase; color:var(--bx-muted, #A3A6B6); margin:0 0 6px;}
    .tmenu .row{display:flex; align-items:center; justify-content:space-between; gap:8px; margin:4px 0;}
    .tmenu select{flex:1; min-width:0; box-sizing:border-box; height:var(--bx-control-h, 28px); padding:0 6px;
      border:1px solid var(--bx-border-strong, #666A7E); border-radius:var(--bx-radius, 2px);
      background:var(--bx-panel, #1F2028); color:var(--bx-text, #E9EAF0);}
    .tmenu .fs{display:flex; align-items:center; gap:6px;}
    .tmenu .fs b{min-width:30px; text-align:center; font-weight:600; font-variant-numeric:tabular-nums;}
    .tmenu .step{width:var(--bx-control-h, 28px); height:var(--bx-control-h, 28px); padding:0;
      display:grid; place-items:center; border:1px solid var(--bx-border-strong, #666A7E);
      border-radius:var(--bx-radius, 2px); background:var(--bx-panel, #1F2028); color:var(--bx-text, #E9EAF0); cursor:pointer;}
    .tmenu .step:hover{background:var(--bx-hover, #2A2B34);}
    .tmenu .pstat{font:var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif);
      font-variant-numeric:tabular-nums; color:var(--bx-muted, #A3A6B6); margin:2px 0 0;}
    /* the slow-link chip: predictive echo is drawing what you type */
    .lag{right:42px; display:flex; align-items:center; gap:4px; padding:0 7px; user-select:none;
      font:var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); font-variant-numeric:tabular-nums;}
    .lag:hover{color:var(--bx-text, #E9EAF0); background:var(--bx-hover, #2A2B34)}
    .lag[hidden]{display:none}
    /* the prediction overlay: a layer over xterm's screen, one span per run
       of predicted cells, placed by the renderer's cell metrics (works in
       the alternate buffer too, where xterm hides its own decorations) */
    .pov{position:absolute; left:0; top:0; right:0; bottom:0; z-index:7; pointer-events:none; overflow:hidden;}
    .pov[hidden]{display:none}
    .pov span{position:absolute; white-space:pre; overflow:hidden; font-kerning:none; box-sizing:border-box;}
    /* while a predicted cursor is drawn, the real one steps aside (xterm's
       own rules are (0,5,0) with !important; these outrank them) */
    :host(.pcur) .host .xterm .xterm-screen .xterm-rows .xterm-cursor.xterm-cursor-block{background-color:transparent !important; color:var(--bxp-fg) !important;}
    :host(.pcur) .host .xterm .xterm-screen .xterm-rows .xterm-cursor.xterm-cursor-outline{outline:none !important;}
    :host(.pcur) .host .xterm .xterm-screen .xterm-rows .xterm-cursor.xterm-cursor-bar{box-shadow:none !important;}
    :host(.pcur) .host .xterm .xterm-screen .xterm-rows .xterm-cursor.xterm-cursor-underline{border-bottom:0 !important; height:100% !important;}
  </style>` +
  `<div class="host"></div>` +
  `<button class="gear" title="terminal settings" aria-label="terminal settings" aria-haspopup="true">${iconSvg('settings')}</button>` +
  `<button class="lag" hidden title="slow link: typed text is shown before the server confirms it (predictive echo, terminal settings)">${iconSvg('bolt')}<span class="lagt"></span></button>` +
  `<div class="tmenu" hidden>` +
    `<div class="hd">terminal</div>` +
    `<div class="row"><span>Theme</span><select class="theme" aria-label="terminal theme"></select></div>` +
    `<div class="row"><span>Font size</span>` +
      `<span class="fs"><button class="step" data-d="-1" aria-label="smaller" title="smaller">${iconSvg('minus')}</button>` +
      `<b class="fsv"></b>` +
      `<button class="step" data-d="1" aria-label="larger" title="larger">${iconSvg('plus')}</button></span></div>` +
    `<div class="row"><span>Predictive echo</span><select class="predict" aria-label="predictive echo">` +
      `<option value="auto">auto · on when RTT &gt; ${SRTT_SHOW} ms</option>` +
      `<option value="on">on</option><option value="off">off</option></select></div>` +
    `<div class="pstat"></div>` +
  `</div>`;
