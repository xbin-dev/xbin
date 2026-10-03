/**
 * bx-icons.js — xbin's icon set (D184): plain drawn glyphs in place of emoji
 * and of text glyphs set in whatever font the button falls back to.
 *
 * Drawn on a 16px grid with a 1.5px stroke (about 2px at 20px), square caps
 * and mitred joins, from squares, rectangles and 45° diagonals; outline by
 * default, a filled square for "live". Straight strokes sit on .25/.75 so
 * they land on whole device pixels at 2x. Names follow the native
 * vocabulary's (/vendor/xb/vocab.js) where the meaning is the same, so a
 * tile's web page and its native view say the same thing. There are no
 * faces, robots, brains or sparkles, by design.
 *
 *   <bx-icon name="xmark"></bx-icon>         decorative (aria-hidden)
 *   <bx-icon name="warning" label="Warning"> an image with a name
 *   iconSvg('lock', {size: 20})             the same SVG as a string
 *   ICON_NAMES                              every name, aliases included
 *
 * The element is 16px square (size="20" for 20px), takes currentColor and
 * sits on the text's baseline like a glyph; style it from outside as any
 * element. An icon-only button needs its own aria-label or title. A name
 * that isn't in the set draws nothing (and warns once). <option> text
 * can't hold an icon: there the word stands alone.
 */

const F = 'fill="currentColor" stroke="none"';
const sq = (x, y, w, h = w) => `<rect x="${x}" y="${y}" width="${w}" height="${h}" ${F}/>`;
const p = (d) => `<path d="${d}"/>`;
const FRAME = 'M2.75 2.75h10.5v10.5h-10.5z';

const GLYPHS = {
  // ---- window controls and chrome ----
  xmark: p('M3.75 3.75l8.5 8.5M12.25 3.75l-8.5 8.5'),
  minimize: p('M3.25 11.75h9.5'),
  maximize: p(FRAME),
  restore: p('M5.75 5.25v-2.5h7.5v7.5h-2.5M2.75 5.75h7.5v7.5h-7.5z'),
  popout: p('M7.25 2.75h-4.5v10.5h10.5v-4.5M9.75 2.75h3.5v3.5M13.25 2.75l-5.5 5.5'),
  ellipsis: sq(2, 7, 2) + sq(7, 7, 2) + sq(12, 7, 2),
  menu: p('M2.75 3.75h10.5M2.75 7.75h10.5M2.75 11.75h10.5'),
  settings: p('M2.25 4.25h11.5M2.25 7.75h11.5M2.25 11.25h11.5') + sq(9.5, 2.5, 3.5) + sq(3, 6, 3.5) + sq(7, 9.5, 3.5),
  grip: sq(5, 3, 2) + sq(9, 3, 2) + sq(5, 7, 2) + sq(9, 7, 2) + sq(5, 11, 2) + sq(9, 11, 2),
  grid: p('M2.75 2.75h4v4h-4zM9.25 2.75h4v4h-4zM2.75 9.25h4v4h-4zM9.25 9.25h4v4h-4z'),
  split: p(`${FRAME}M7.75 2.75v10.5`),
  plus: p('M7.75 2.75v10.5M2.75 7.75h10.5'),
  minus: p('M2.75 7.75h10.5'),
  check: p('M2.75 8.25l3.5 3.5 7-7'),
  'chevron-left': p('M10.25 3.25l-4.5 4.5 4.5 4.5'),
  'chevron-right': p('M5.75 3.25l4.5 4.5-4.5 4.5'),
  'chevron-up': p('M3.25 10.25l4.5-4.5 4.5 4.5'),
  'chevron-down': p('M3.25 5.75l4.5 4.5 4.5-4.5'),
  'caret-right': `<path d="M5.75 3.25v9l4.5-4.5z" ${F}/>`,
  'caret-down': `<path d="M3.25 5.75h9l-4.5 4.5z" ${F}/>`,
  'arrow-left': p('M13.25 7.75H2.75M6.25 4.25l-3.5 3.5 3.5 3.5'),
  'arrow-right': p('M2.75 7.75h10.5M9.75 4.25l3.5 3.5-3.5 3.5'),
  search: p('M2.75 2.75h7.5v7.5h-7.5zM10.75 10.75l2.5 2.5'),
  filter: p('M2.75 3.25h10.5l-4 4.5v5l-2.5 1.5v-6.5z'),
  refresh: p('M2.75 9.25v-6h9M9.5 1l2.25 2.25L9.5 5.5M13.25 6.25v6h-9M6.5 10l-2.25 2.25L6.5 14.5'),
  pencil: p('M3.25 12.75V10l7-7 2.75 2.75-7 7zM8.75 4.75l2.5 2.5'),
  copy: p('M5.75 5.75h7.5v7.5h-7.5zM10.25 4.25v-1.5h-7.5v7.5h1.5'),
  clipboard: p('M3.75 3.75h8.5v9.5h-8.5zM5.75 2.25h4v3h-4zM5.75 8.25h4.5M5.75 10.75h3'),
  download: p('M7.75 2.75v7.5M4.5 7l3.25 3.25L11 7M2.75 13.25h10.5'),
  upload: p('M7.75 10.75v-7.5M4.5 6.5l3.25-3.25L11 6.5M2.75 13.25h10.5'),
  send: p('M2.25 7.75h9.5M8.25 4.25l3.5 3.5-3.5 3.5'),
  play: `<path d="M4.75 2.75v10.5l5.25-5.25z" ${F}/>`,
  pause: p('M5.25 3.25v9.5M10.25 3.25v9.5'),
  power: p('M5.25 4.25h-2.5v9h10v-9h-2.5M7.75 1.75v6'),
  live: sq(4, 4, 8),

  // ---- the parts (brand 9) ----
  window: p(`${FRAME}M2.75 5.75h10.5`),
  terminal: p('M3.25 4.25l3.5 3.5-3.5 3.5M8.75 11.75h4.5'),
  agent: p('M2.75 2.75h5.5v5.5h-5.5zM9.75 9.75h3.5v3.5h-3.5zM5.5 8.25v3.25h4.25'),
  key: p('M2.75 5.25h5v5h-5zM7.75 7.75h5.5M10.75 7.75v2.5M13.25 7.75v3'),
  server: p(`${FRAME}M2.75 6.25h10.5M2.75 9.75h10.5`) + sq(10.25, 3.75, 1.5) + sq(10.25, 7.25, 1.5) + sq(10.25, 10.75, 1.5),

  // ---- status: shape, then colour (brand 4.6) ----
  ok: p(`${FRAME}M5 7.75l2 2 4-4`),
  warning: p('M7.75 2L14.25 13.25H1.25zM7.75 6.25v3.25') + sq(7, 10.5, 1.5),
  error: p('M5.25 1.75h5.5l3.5 3.5v5.5l-3.5 3.5h-5.5l-3.5-3.5v-5.5zM4.75 7.75h6'),
  info: p(`${FRAME}M7.75 7v4.25`) + sq(7, 4.25, 1.5),
  question: p(`${FRAME}M5.75 5.75v-1.5h4v3l-2 1v1.25`) + sq(7, 10.5, 1.5),

  // ---- things ----
  folder: p('M2.75 3.25h4l1.5 1.5h5v8h-10.5z'),
  file: p('M3.75 2.75h5.5l3.5 3.5v7h-9zM9.25 2.75v3.5h3.5'),
  doc: p('M3.75 2.75h5.5l3.5 3.5v7h-9zM9.25 2.75v3.5h3.5M5.75 8.25h4.5M5.75 10.75h4.5'),
  code: p('M5.75 4l-3.75 3.75 3.75 3.75M9.75 4l3.75 3.75-3.75 3.75'),
  list: p('M2.75 3.75h10.5M2.75 6.75h10.5M2.75 9.75h7M2.75 12.75h8.5'),
  diff: p('M7.75 2.75v6M4.75 5.75h6M4.75 12.25h6'),
  deploy: p('M2.75 2.75h10.5M7.75 13.25v-7.5M4.5 9l3.25-3.25L11 9'),
  branch: p('M3.25 2.25h3v3h-3zM3.25 10.75h3v3h-3zM10.25 4.25h3v3h-3zM4.75 5.25v5.5M4.75 9.25l3.5-3.5h2'),
  lock: p('M3.25 7.25h9v6h-9zM5.25 7.25v-3.5h5v3.5'),
  unlock: p('M3.25 7.25h9v6h-9zM5.25 7.25v-3.5h5v1.5'),
  shield: p('M2.75 2.75h10.5v5.75L8 13.75 2.75 8.5z'),
  pin: p('M5.25 2.75h5M6.25 2.75v4L4 9h7.5l-2.25-2.25v-4M7.75 9v4.75'),
  plug: p('M3.75 6.25h8v3.5l-2 2h-4l-2-2zM5.75 2.75v3.5M9.75 2.75v3.5M7.75 11.75v2.5'),
  link: p('M6.25 5.25h-3.5v5h3.5M9.25 5.25h3.5v5h-3.5M5.25 7.75h5'),
  globe: p('M7.75 1.75l6 6-6 6-6-6zM1.75 7.75h12M7.75 1.75v12'),
  network: p('M5.75 2.75h4v3h-4zM2.75 10.25h4v3h-4zM8.75 10.25h4v3h-4zM7.75 5.75V8M4.75 10.25V8h6v2.25'),
  database: p(`${FRAME}M2.75 6.25h10.5M2.75 9.75h10.5M6.75 4.5h2M6.75 8h2M6.75 11.5h2`),
  cpu: p('M4.75 4.75h6v6h-6zM6.25 2.25v2.5M9.25 2.25v2.5M6.25 10.75v2.5M9.25 10.75v2.5M2.25 6.25h2.5M2.25 9.25h2.5M10.75 6.25h2.5M10.75 9.25h2.5'),
  host: p('M3.75 3.75h8.5v6.5h-8.5zM1.75 12.25h12.5'),
  vm: p('M2.75 2.75h10.5v7.5h-10.5zM7.75 10.25v2.5M5.25 13.25h5'),
  device: p('M4.25 1.75h7v12.5h-7zM7 11.75h1.5'),
  org: p('M3.75 2.75h8.5v10.5h-8.5z') + sq(5.5, 4.75, 1.5) + sq(8.5, 4.75, 1.5) + sq(5.5, 7.75, 1.5) + sq(8.5, 7.75, 1.5) + sq(7, 10.75, 1.5, 2.5),
  person: p('M5.75 2.75h4v4h-4zM2.75 13.25v-2l2-2h6l2 2v2'),
  people: p('M4 4.75h3.5v3.5H4zM1.75 13.25V11.5L3.5 9.75H8l1.75 1.75v1.75M9.25 2.75h3.5v3.5h-3.5zM11.25 7.75h1.5l1.5 1.5v2.25'),
  eye: p('M1.75 7.75l3-3h6l3 3-3 3h-6z') + sq(6.5, 6.5, 2.5),
  'eye-slash': p('M1.75 7.75l3-3h6l3 3-3 3h-6zM2.75 2.75l10.5 10.5') + sq(6.5, 6.5, 2.5),
  bell: p('M4.25 11.25v-5l2-2h3l2 2v5M2.75 11.25h10M6.75 13.25h2M7.75 2.25v2'),
  'bell-slash': p('M4.25 11.25v-5l2-2h3l2 2v5M2.75 11.25h10M6.75 13.25h2M7.75 2.25v2M2.25 2.25l11.5 11.5'),
  mail: p('M2.75 3.75h10.5v8.5h-10.5zM2.75 3.75l5 5 5-5'),
  call: p('M2.75 2.75h3.5v3l3.5 3.5h3.5v4h-4l-6.5-6.5z'),
  chat: p('M2.75 2.75h10.5v7.5h-5.5l-3 3v-3h-2z'),
  thought: p('M2.75 2.75h10.5v7.5h-5.5l-3 3v-3h-2z') + sq(4.75, 5.75, 1.5) + sq(7, 5.75, 1.5) + sq(9.25, 5.75, 1.5),
  calendar: p('M2.75 3.75h10.5v9.5h-10.5zM2.75 6.75h10.5M5.25 2.25v2.5M10.25 2.25v2.5') + sq(5, 8.5, 2),
  clock: p(`${FRAME}M7.75 4.75v3h2.5`),
  wait: p('M3.25 2.75h9M3.25 13.25h9M4.25 2.75l3.5 3.5 3.5-3.5M4.25 13.25l3.5-3.5 3.5 3.5M7.75 6.25v3.5'),
  compact: p('M7.75 1.75v3.5M5.5 3l2.25 2.25L10 3M7.75 13.75v-3.5M5.5 12.5l2.25-2.25L10 12.5M2.75 7.75h10'),
  box: p('M2.75 5.25h10.5v8h-10.5zM2.75 5.25l2-2h6.5l2 2M7.75 5.25v3'),
  archive: p('M2.25 2.75h11v3h-11zM3.25 5.75v7.5h9v-7.5M6.25 8.25h3'),
  save: p('M2.75 2.75h8.5l2 2v8.5h-10.5zM5.25 2.75v3.5h5v-3.5M4.75 13.25v-4h6v4'),
  trash: p('M2.75 4.25h10.5M6.25 4.25v-1.5h3v1.5M4.25 4.25l.75 9h5.5l.75-9M6.75 6.75v4M8.75 6.75v4'),
  photo: p(`${FRAME}M2.75 11.25l4-4 3.5 3.5 1.5-1.5 1.5 1.5`) + sq(9.5, 4.5, 2),
  paperclip: p('M6.25 5.75v5h3v-8h-6v10.5h9v-8'),
  tag: p('M2.75 2.75h5.5l5 5-5.5 5.5-5-5z') + sq(4.5, 4.5, 1.5),
  bolt: p('M9.75 1.75L4.25 7.25h7l-5.5 5.5'),
  signal: p('M7.75 8.25v6M5 4.5L2.75 6.75 5 9M10.5 4.5l2.25 2.25L10.5 9') + sq(6.75, 5.75, 2),
  chart: p('M2.75 13.25h10.5M4.75 11.25v-3M7.75 11.25v-6M10.75 11.25v-4.5'),
  home: p('M2.75 7.25l5-5 5 5M4.25 5.75v7.5h7v-7.5M6.75 13.25v-3h2v3'),
};

// Another name for the same glyph: the brand's words, the emoji a glyph
// replaces in prose, the native vocabulary's synonyms.
const ALIASES = {
  close: 'xmark', more: 'ellipsis', gear: 'settings', wrench: 'settings',
  external: 'popout', back: 'chevron-left', forward: 'chevron-right',
  expand: 'maximize', collapse: 'minimize', logs: 'list', rack: 'server',
  danger: 'error', warn: 'warning', stop: 'live', reload: 'refresh', edit: 'pencil',
  attach: 'paperclip', image: 'photo', package: 'box', storage: 'database',
  gpu: 'cpu', laptop: 'host', phone: 'device', building: 'org', user: 'person',
  team: 'people', 'view-as': 'eye', channel: 'signal', hourglass: 'wait',
};

export const ICON_NAMES = Object.freeze([...Object.keys(GLYPHS), ...Object.keys(ALIASES)].sort());

const markup = (name) => GLYPHS[ALIASES[name] || name] ?? null;
export const hasIcon = (name) => markup(name) !== null;

const warned = new Set();
export function iconSvg(name, { size = 16, label = '' } = {}) {
  const inner = markup(name);
  if (inner === null && !warned.has(name)) { warned.add(name); console.warn(`bx-icon: no glyph named ${JSON.stringify(name)}`); }
  const a11y = label
    ? `role="img" aria-label="${String(label).replace(/[&<>"]/g, (c) => `&#${c.charCodeAt(0)};`)}"`
    : 'aria-hidden="true"';
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" width="${size}" height="${size}" ${a11y} focusable="false" `
    + 'fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="square" stroke-linejoin="miter" stroke-miterlimit="10">'
    + `${inner ?? ''}</svg>`;
}

// <bx-icon name="…" [label="…"] [size="20"]>: the SVG in a shadow root.
const HOST_CSS = ':host{display:inline-block;flex:none;width:var(--bx-icon-size,16px);height:var(--bx-icon-size,16px);'
  + 'vertical-align:-0.2em;line-height:0}svg{display:block;width:100%;height:100%;overflow:visible}';

// One sheet for every icon in this document where the browser can share it.
let shared = null;
try { shared = new CSSStyleSheet(); shared.replaceSync(HOST_CSS); } catch { shared = null; }

if (typeof customElements !== 'undefined' && !customElements.get('bx-icon')) {
  customElements.define('bx-icon', class extends HTMLElement {
    static observedAttributes = ['name', 'label', 'size'];
    connectedCallback() { this.#draw(); }
    attributeChangedCallback() { if (this.isConnected) this.#draw(); }
    #draw() {
      const root = this.shadowRoot || this.attachShadow({ mode: 'open' });
      const label = this.getAttribute('label') || '';
      const size = Number(this.getAttribute('size')) || 0;
      if (size) this.style.setProperty('--bx-icon-size', `${size}px`);
      else this.style.removeProperty('--bx-icon-size');
      if (label) { this.setAttribute('role', 'img'); this.setAttribute('aria-label', label); this.removeAttribute('aria-hidden'); }
      else { this.setAttribute('aria-hidden', 'true'); this.removeAttribute('role'); this.removeAttribute('aria-label'); }
      if (shared && root.adoptedStyleSheets.length === 0) root.adoptedStyleSheets = [shared];
      root.innerHTML = (shared ? '' : `<style>${HOST_CSS}</style>`) + iconSvg(this.getAttribute('name') || '');
    }
  });
}
