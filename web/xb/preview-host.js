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
 *     credentials), like the app's loader — this workspace's paths only.
 * Query: &theme=light|dark, &text=large.
 *
 * window.xbnPreview = {view, messages, errors, diagnostics, ready, tree()}:
 * `ready` settles after the first tree is drawn, or with the first error
 * that stops the tile (a module failure, an exception before any tree).
 */
import { attach } from '/vendor/xb-native.js';
import '/vendor/xb/render.js';

const G = globalThis;
const on = typeof document === 'object' && document.querySelector('meta[name="xbin-native-preview"]');

function start() {
  const q = new URLSearchParams(location.search);
  const style = document.createElement('style');
  style.textContent = `html, body { margin: 0; height: 100%; background: #f6f7f9; }
    @media (prefers-color-scheme: dark) { html:not(.light), html:not(.light) body { background: #1b1e24; } }
    html.dark, html.dark body { background: #1b1e24; }
    xb-view { position: fixed; inset: 0; }
    #xbn-strip { position: fixed; left: 0; right: 0; bottom: 0; z-index: 9; max-height: 30%; overflow: auto;
      font: 12px/1.4 ui-monospace, monospace; color: #fff; background: rgba(198, 40, 40, 0.94); padding: 6px 10px; white-space: pre-wrap; }
    #xbn-strip:empty { display: none; }`;
  document.head.append(style);
  const theme = q.get('theme');
  if (theme === 'light' || theme === 'dark') document.documentElement.classList.add(theme);

  const view = document.createElement('xb-view');
  view.theme = theme === 'light' || theme === 'dark' ? theme : '';
  if (q.get('text') === 'large') view.text = 'large';
  const strip = document.createElement('div');
  strip.id = 'xbn-strip';
  document.body.append(view, strip);

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
    // xbin.fetch attaches the tile's frame token to any URL: only this
    // workspace's own paths get it. An image elsewhere is not a tile resource
    // (docs/native.md §Images) and is not drawn — the app must not send the
    // token off-site either.
    const own = (u) => { try { return new URL(u, document.baseURI).origin === location.origin; } catch { return false; } };
    view.loadImage = async (src) => {
      if (!own(src)) throw new Error('not a tile resource');
      const r = await G.xbin.fetch(src);
      if (!r.ok) throw new Error(`${r.status}`);
      return URL.createObjectURL(await r.blob());
    };
    view.onupload = async (n, file) => {
      const up = n.p?.upload;
      if (!up?.path) return;
      // tile-relative: under the tile's own API unless already an /api/ path;
      // {name} is the file's name
      let path = String(up.path).split('{name}').join(encodeURIComponent(file.name));
      if (!path.startsWith('/api/')) path = `/api/${G.xbin.self}${path.startsWith('/') ? '' : '/'}${path}`;
      if (!own(path)) { note(`upload refused: ${path} is not on this workspace`); return; }
      try {
        const r = await G.xbin.fetch(path, { method: up.method || 'PUT', body: file, headers: { 'Content-Type': file.type || 'application/octet-stream' } });
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
  G.xbnPreview = { view, messages, errors, diagnostics, ready, tree: () => view.tree };
}

if (on) start();
