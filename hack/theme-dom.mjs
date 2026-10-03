// hack/theme-dom.mjs — the smallest DOM the theme tests need, no
// dependencies (hack/theme-*.test.mjs, hack/xbin-client-appearance.test.mjs):
// a document with <html> and <head>, elements with attributes, prepend and
// remove, querySelector(All) for the selectors web/bx-theme.js,
// web/theme-boot.js and web/xbin-client.js use (`[head >] tag[attr="v"]…`
// with =, ~= and *=), a cookie jar that records every write, and a window
// that is an EventTarget with matchMedia.

class El {
  constructor(tag, doc) {
    this.localName = tag;
    this.tagName = tag.toUpperCase();
    this.ownerDocument = doc;
    this.attrs = new Map();
    this.children = [];
    this.parentNode = null;
  }

  getAttribute(n) { return this.attrs.has(n) ? this.attrs.get(n) : null; }
  setAttribute(n, v) { this.attrs.set(n, String(v)); }
  removeAttribute(n) { this.attrs.delete(n); }
  hasAttribute(n) { return this.attrs.has(n); }
  get content() { return this.getAttribute('content') ?? ''; }
  set content(v) { this.setAttribute('content', v); }
  get href() { return this.getAttribute('href') ?? ''; }

  prepend(...nodes) {
    for (const n of nodes.reverse()) { n.remove(); n.parentNode = this; this.children.unshift(n); }
  }

  append(...nodes) {
    for (const n of nodes) { n.remove(); n.parentNode = this; this.children.push(n); }
  }

  remove() {
    if (!this.parentNode) return;
    this.parentNode.children = this.parentNode.children.filter((c) => c !== this);
    this.parentNode = null;
  }

  getBoundingClientRect() { return { x: 0, y: 0, width: 800, height: 600 }; }

  * walk() {
    for (const c of this.children) { yield c; yield* c.walk(); }
  }
}

// [head >] tag[attr="v"][attr~="v"][attr*="v"][attr]
function compile(sel) {
  const m = /^\s*(?:(head)\s*>\s*)?([a-z]+)((?:\[[^\]]+\])*)\s*$/i.exec(sel);
  if (!m) throw new Error(`theme-dom: unsupported selector ${sel}`);
  const [, parent, tag, attrPart] = m;
  const attrs = [...attrPart.matchAll(/\[([\w-]+)(?:([~*]?=)"([^"]*)")?\]/g)].map((a) => ({ name: a[1], op: a[2], value: a[3] }));
  return (el) => {
    if (el.localName !== tag.toLowerCase()) return false;
    if (parent && el.parentNode?.localName !== parent) return false;
    return attrs.every(({ name, op, value }) => {
      const v = el.getAttribute(name);
      if (v === null) return false;
      if (!op) return true;
      if (op === '=') return v === value;
      if (op === '~=') return v.split(/\s+/).includes(value);
      return v.includes(value);
    });
  };
}

// makeDocument({theme, density, auto, metas, head, href, cookieThrows}):
// metas is {name: content} for more <meta>s in <head>; head lists other
// elements as [tag, {attr: v}].
export function makeDocument({ theme, density, auto = true, metas = {}, head = [], href = 'https://ws.example/c/shell/', cookieThrows = false } = {}) {
  const doc = new EventTarget();
  const html = new El('html', doc);
  const headEl = new El('head', doc);
  const body = new El('body', doc);
  html.append(headEl, body);
  if (auto) html.setAttribute('data-bx-theme', 'auto');
  doc.documentElement = html;
  doc.head = headEl;
  doc.body = body;
  doc.location = new URL(href);
  doc.cookies = []; // every write, in order
  Object.defineProperty(doc, 'cookie', {
    get() {
      if (cookieThrows) throw new DOMException('no cookies in an opaque origin', 'SecurityError');
      return doc.jar ?? '';
    },
    set(v) {
      if (cookieThrows) throw new DOMException('no cookies in an opaque origin', 'SecurityError');
      doc.cookies.push(v);
    },
  });
  doc.createElement = (tag) => new El(tag, doc);
  doc.querySelectorAll = (sel) => { const ok = compile(sel); return [...html.walk()].filter(ok); };
  doc.querySelector = (sel) => doc.querySelectorAll(sel)[0] ?? null;
  doc.getSelection = () => ({ toString: () => '' });
  const all = { ...metas };
  if (theme) all['xbin-theme'] = theme;
  if (density) all['xbin-density'] = density;
  for (const [name, content] of Object.entries(all)) {
    const m = new El('meta', doc);
    m.setAttribute('name', name);
    m.setAttribute('content', content);
    headEl.append(m);
  }
  for (const [tag, attrs] of head) {
    const e = new El(tag, doc);
    for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
    headEl.append(e);
  }
  return doc;
}

// The metas a document's head holds now: {name: content}.
export const metasOf = (doc) => Object.fromEntries(doc.head.children.filter((c) => c.localName === 'meta').map((m) => [m.getAttribute('name'), m.getAttribute('content')]));

// makeWindow(doc, {light, contrast}): an EventTarget window whose matchMedia
// answers the two queries bx-theme.js listens to; flip(query, matches) fires
// a change on it.
export function makeWindow(doc, { light = false, contrast = false } = {}) {
  const win = new EventTarget();
  const queries = new Map();
  const state = { '(prefers-color-scheme: light)': light, '(prefers-contrast: more)': contrast };
  win.matchMedia = (q) => {
    if (!queries.has(q)) {
      const mq = new EventTarget();
      Object.defineProperty(mq, 'matches', { get: () => !!state[q] });
      mq.media = q;
      queries.set(q, mq);
    }
    return queries.get(q);
  };
  win.flip = (q, matches) => { state[q] = matches; queries.get(q)?.dispatchEvent(new Event('change')); };
  win.document = doc;
  doc.defaultView = win;
  return win;
}
