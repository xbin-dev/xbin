/**
 * xb/preview-host.js — draws a tile's native UI inside its own runtime
 * document, with the reference renderer: the browser plays the app. xbind
 * imports it into the runtime document when the URL has `&preview=1` (the
 * document then carries <meta name="xbin-native-preview" content="1">),
 * after /vendor/xb-native.js and before the tile's entry; `bx preview
 * --native` screenshots the result. Without that meta it does nothing.
 *
 * What it does as the app:
 *   - attach()es to the runtime: mount/patch go to an <xb-view> filling the
 *     page; the user's taps, typing and toggles go back via xbn.event with
 *     the n of the last tree it applied (native/spec/tree.md §4–§6);
 *   - drives the frame clock (xbn.frame() on each animation frame);
 *   - answers calls: copy → the clipboard, share → false (no share sheet),
 *     open → a new tab for https; meta → the document title;
 *   - keeps errors and diagnostics, shown in a strip at the bottom;
 *   - images and composer uploads go through xbin.fetch (the tile's own
 *     credentials), like the app's loader, under the app's rule (D103,
 *     xb/tile-resource.js): the tile's own /c/<self>/ and /api/<self>/
 *     paths only — never another tile's, a nested tile's, xbind's API or
 *     another site — and uploads by PUT, POST or PATCH only.
 * Query: &theme=light|dark, &text=large, &widget=small|wide — play an app
 * that shows widgets (caps list the "widget" feature, tree.md §13): the
 * tile's widget tree is drawn as a card of that size class instead of its
 * screen (whose tree still runs, hidden).
 *
 * window.xbnPreview = {view, widgetView, messages, errors, diagnostics,
 * ready, tree(), widgetTree()}:
 * `ready` settles after the first tree is drawn, or with the first error
 * that stops the tile (a module failure, an exception before any tree).
 */
import { attach } from '/vendor/xb-native.js';
import '/vendor/xb/render.js';
import { SCHEMES } from '/vendor/xb/render-theme.js';
import { appearance } from '/vendor/bx-theme.js';
import { VOCAB, fullCaps } from '/vendor/xb/vocab.js';
import { apiPath, assetPath, uploadMethod } from '/vendor/xb/tile-resource.js';

const G = globalThis;
const on = typeof document === 'object' && document.querySelector('meta[name="xbin-native-preview"]');

// Card sizes of the widget size classes, in points (the app's screen grid,
// XbinWidgetMetrics: two 177 pt columns, 12 pt margins and gap, 132 pt tall
// on a 390 pt phone — D128).
const WIDGET_BOX = { small: [177, 132], wide: [366, 132] };

function start() {
  const q = new URLSearchParams(location.search);
  const wsize = VOCAB.widget.sizes.includes(q.get('widget')) ? q.get('widget') : '';
  // before the runtime starts (its first render or frame): the caps it reads
  const inj = G.xbin && typeof G.xbin.native === 'object' ? G.xbin.native : null;
  if (wsize && inj && Object.isExtensible(inj) && !inj.caps) {
    const c = fullCaps();
    inj.caps = { ...c, features: [...c.features, VOCAB.widget.feature], widgetSize: wsize };
  }
  // theme.css for the preview's own strip (its error lines, on the theme's
  // tokens: D184); the page around the view in the renderer's colours — the
  // app's (render-theme.js) — so it matches the view in either scheme
  const sheet = document.createElement('link');
  sheet.rel = 'stylesheet';
  sheet.href = '/vendor/theme.css';
  const L = SCHEMES.light, D = SCHEMES.dark;
  const style = document.createElement('style');
  style.textContent = `html, body { margin: 0; height: 100%; background: ${L.bg}; }
    @media (prefers-color-scheme: dark) { html:not(.light), html:not(.light) body { background: ${D.bg}; } html:not(.light) .xbn-widget { box-shadow: ${D.shadow}; } }
    html.dark, html.dark body { background: ${D.bg}; }
    xb-view { position: fixed; inset: 0; }
    #xbn-strip { position: fixed; left: 0; right: 0; bottom: 0; z-index: 9; max-height: 30%; overflow: auto;
      font: var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace); font-variant-ligatures: none; color: var(--bx-danger, #FF7A7A);
      background: var(--bx-danger-bg, #3A2B32); border-top: 1px solid var(--bx-danger, #FF7A7A); padding: 6px 10px; white-space: pre-wrap; }
    #xbn-strip:empty { display: none; }
    /* a widget is the app's card: Base Two's square corner, as the app draws it (D185) */
    .xbn-widget { position: fixed; left: 12px; top: 24px; border-radius: var(--bx-radius, 2px); overflow: hidden; box-shadow: ${L.shadow}; }
    html.dark .xbn-widget { box-shadow: ${D.shadow}; }
    .xbn-widget xb-view { position: absolute; inset: 0; }
    xb-view.xbn-hidden { visibility: hidden; }`;
  document.head.append(sheet, style);
  // the scheme: &theme=, else the person's choice where they made one (the
  // document's injected meta, D184), else the system's
  const asked = q.get('theme'), chose = appearance(document).theme;
  const theme = asked === 'light' || asked === 'dark' ? asked : chose === 'light' || chose === 'dark' ? chose : '';
  if (theme) document.documentElement.classList.add(theme);

  const view = document.createElement('xb-view');
  view.theme = theme;
  if (q.get('text') === 'large') view.text = 'large';
  const strip = document.createElement('div');
  strip.id = 'xbn-strip';
  document.body.append(view, strip);
  let widgetView = null;
  if (wsize) {
    const box = document.createElement('div');
    box.className = 'xbn-widget';
    [box.style.width, box.style.height] = WIDGET_BOX[wsize].map((x) => `${x}px`);
    widgetView = document.createElement('xb-view');
    widgetView.theme = view.theme;
    widgetView.text = view.text;
    widgetView.compact = true;
    box.append(widgetView);
    document.body.insertBefore(box, strip);
    view.classList.add('xbn-hidden');
    widgetView.onevent = (k, type, payload, n) => G.xbn?.event(k, type, payload, n, 'widget');
    widgetView.addEventListener('xb-remount', () => G.xbn?.remount('widget'));
  }

  const messages = [];
  const errors = [];
  const diagnostics = [];
  let settle;
  let drawn = false;
  const ready = new Promise((r) => { settle = r; });
  const note = (line) => { strip.textContent = `${strip.textContent ? `${strip.textContent}\n` : ''}${line}`; };

  view.onevent = (k, type, payload, n) => G.xbn?.event(k, type, payload, n);
  view.addEventListener('xb-remount', () => G.xbn?.remount());
  view.oncopy = async (text) => { try { await navigator.clipboard.writeText(text); return true; } catch { return false; } };
  if (typeof G.xbin?.fetch === 'function') {
    // xbin.fetch attaches the tile's frame token to any URL: the app confines
    // every reference a tile hands over to that tile's own paths (D103), and
    // so does its stand-in here — an image or upload the app would refuse is
    // refused, so a builder never sees one work in the preview only. A path
    // a nested tile owns is refused too: the workspace's tile list, read
    // once (none when it can't be read).
    let tiles;
    const known = () => (tiles ??= G.xbin.fetch('/api/xbin/components')
      .then((r) => (r.ok ? r.json() : []))
      .then((l) => (Array.isArray(l) ? l.map((c) => c?.path).filter((p) => typeof p === 'string') : []), () => []));
    view.loadImage = async (src) => {
      const path = assetPath(src, G.xbin.self, await known());
      if (!path) throw new Error('not a tile resource');
      const r = await G.xbin.fetch(path);
      if (!r.ok) throw new Error(`${r.status}`);
      return URL.createObjectURL(await r.blob());
    };
    view.onupload = async (n, file) => {
      const up = n.p?.upload;
      if (!up?.path) return;
      // tile-relative: under the tile's own API unless already an /api/ path,
      // which must be the tile's own, as the app requires (docs/native.md);
      // {name} is the file's name
      const method = uploadMethod(up.method);
      if (!method) { note(`upload refused: method ${up.method} (PUT, POST or PATCH)`); return; }
      const path = apiPath(String(up.path), G.xbin.self, await known(), file.name);
      if (!path) { note(`upload refused: ${up.path} is not this tile's own API (/api/${G.xbin.self}/…)`); return; }
      try {
        const r = await G.xbin.fetch(path, { method, body: file, headers: { 'Content-Type': file.type || 'application/octet-stream' } });
        const text = await r.text();
        let response = text;
        try { response = JSON.parse(text); } catch { /* not JSON: the text */ }
        G.xbn?.event(n.k, 'uploaded', { name: file.name, response }, view.n);
      } catch (e) { note(`upload failed: ${e?.message ?? e}`); }
    };
  }

  const answer = async (m) => {
    let v = null; let err = null;
    try {
      if (m.what === 'copy') v = await view.oncopy(String(m.args?.text ?? ''));
      else if (m.what === 'share') v = false;
      else if (m.what === 'open') {
        const url = String(m.args?.url ?? '');
        if (!/^https:\/\//i.test(url)) err = 'https only';
        else { G.open(url, '_blank', 'noopener'); v = true; } // noopener: open() returns null
      } else err = `unknown call ${m.what}`;
    } catch (e) { err = String(e?.message ?? e); }
    G.xbn?.resolve(m.id, v, err);
  };

  attach((m) => {
    messages.push(m);
    if (messages.length > 2000) messages.shift();
    switch (m.op) {
      case 'mount': case 'patch':
        if (m.target === 'widget') { widgetView?.apply(m); break; }
        if (m.target) break; // a tree this host does not draw
        view.apply(m);
        if (!drawn) { drawn = true; view.updateComplete.then(() => settle({ ok: true })); }
        break;
      case 'meta': if (m.title) document.title = m.title; break;
      case 'call': answer(m); break;
      case 'error':
        errors.push(m);
        note(`${m.kind}: ${m.message}${m.where ? ` (${m.where})` : ''}`);
        if (!drawn && (m.kind === 'module' || m.kind === 'exception' || m.kind === 'unsupported')) settle({ ok: false, error: m });
        break;
      case 'diag':
        diagnostics.push(m);
        if (m.level !== 'info') note(`${m.level} ${m.code}: ${m.message}${m.where ? ` (${m.where})` : ''}`);
        break;
      default: break;
    }
  });

  const tick = () => { if (!document.hidden) G.xbn?.frame(); requestAnimationFrame(tick); };
  requestAnimationFrame(tick);
  G.xbnPreview = { view, widgetView, messages, errors, diagnostics, ready, tree: () => view.tree, widgetTree: () => widgetView?.tree ?? null };
}

if (on) start();
